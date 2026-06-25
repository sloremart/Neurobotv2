package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// SessionRepo define la interfaz que necesita el manager (implementada por local.SessionRepo)
type SessionRepo interface {
	FindActiveByPhone(ctx context.Context, phone string) (*Session, error)
	Create(ctx context.Context, session *Session) error
	Save(ctx context.Context, session *Session) error
	UpdateStatus(ctx context.Context, sessionID, status string) error
	RenewExpiry(ctx context.Context, sessionID string, expiresAt time.Time) error
	MarkEscalated(ctx context.Context, sessionID, teamID string) error
	ResumeSession(ctx context.Context, sessionID, newState string, timeoutMinutes int) error
	SetContext(ctx context.Context, sessionID, key, value string) error
	SetContextBatch(ctx context.Context, sessionID string, kvs map[string]string) error
	GetContext(ctx context.Context, sessionID, key string) (string, error)
	GetAllContext(ctx context.Context, sessionID string) (map[string]string, error)
	ClearContext(ctx context.Context, sessionID string, keys ...string) error
	ClearAllContext(ctx context.Context, sessionID string) error
	FindInactiveSessions(ctx context.Context, idleMinutes int) ([]InactiveSession, error)
	FindExpiredEscalatedSessions(ctx context.Context) ([]ExpiredEscalatedSession, error)
	MarkAbandoned(ctx context.Context, sessionID string) error
	CompleteActiveByPhone(ctx context.Context, phone string) error
}

// InactivityBirdClient defines the Bird client methods needed by the inactivity checker.
type InactivityBirdClient interface {
	SendText(to, conversationID, text string) (string, error)
	UnassignFeedItem(conversationID string, closed bool) error
	CloseFeedItems(conversationID string) error
}

// EventLogger defines the event tracking method needed by the inactivity checker.
type EventLogger interface {
	LogEvent(ctx context.Context, sessionID, phone, event string, data map[string]interface{})
}

// InactivityDeps holds dependencies for the inactivity checker goroutine.
type InactivityDeps struct {
	BirdClient  InactivityBirdClient
	Tracker     EventLogger
	ReminderMin int // Minutes before sending the single reminder
	CloseMin    int // Minutes before silent close (must be > ReminderMin)
}

type SessionManager struct {
	repo    SessionRepo
	mutex   *PhoneMutex
	timeout time.Duration
}

func NewSessionManager(repo SessionRepo, timeoutMinutes int) *SessionManager {
	return &SessionManager{
		repo:    repo,
		mutex:   NewPhoneMutex(),
		timeout: time.Duration(timeoutMinutes) * time.Minute,
	}
}

// FindOrCreate busca sesión activa o crea una nueva.
// Retorna (session, isNew, error).
func (m *SessionManager) FindOrCreate(ctx context.Context, phone string) (*Session, bool, error) {
	s, err := m.repo.FindActiveByPhone(ctx, phone)
	if err != nil {
		return nil, false, err
	}

	if s != nil {
		// Cargar contexto desde BD
		ctxMap, err := m.repo.GetAllContext(ctx, s.ID)
		if err != nil {
			return nil, false, err
		}
		s.Context = ctxMap
		return s, false, nil
	}

	// Crear nueva sesión
	newSession := &Session{
		ID:           uuid.New().String(),
		PhoneNumber:  phone,
		CurrentState: "CHECK_BUSINESS_HOURS",
		Status:       StatusActive,
		ExpiresAt:    time.Now().Add(m.timeout),
		Context:      make(map[string]string),
	}

	if err := m.repo.Create(ctx, newSession); err != nil {
		return nil, false, err
	}

	return newSession, true, nil
}

// RenewTimeout renueva el expires_at con cada mensaje
func (m *SessionManager) RenewTimeout(ctx context.Context, s *Session) error {
	s.ExpiresAt = time.Now().Add(m.timeout)
	return m.repo.RenewExpiry(ctx, s.ID, s.ExpiresAt)
}

// SaveState persiste el estado y contexto de la sesión después de procesar un handler
func (m *SessionManager) SaveState(ctx context.Context, s *Session, nextState string, updateCtx map[string]string, clearCtx []string) error {
	s.CurrentState = nextState

	// Guardar sesión
	if err := m.repo.Save(ctx, s); err != nil {
		return err
	}

	// Borrar contexto primero (para que un set posterior en la misma cadena tenga precedencia)
	if len(clearCtx) > 0 {
		if err := m.repo.ClearContext(ctx, s.ID, clearCtx...); err != nil {
			return err
		}
		// Borrar en memoria también
		for _, k := range clearCtx {
			delete(s.Context, k)
		}
	}

	// Actualizar contexto (después del clear, para que el set gane sobre el clear)
	if len(updateCtx) > 0 {
		if err := m.repo.SetContextBatch(ctx, s.ID, updateCtx); err != nil {
			return err
		}
		// Actualizar en memoria también
		for k, v := range updateCtx {
			s.SetContext(k, v)
		}
	}

	return nil
}

// SetContext guarda un valor de contexto (BD + memoria)
func (m *SessionManager) SetContext(ctx context.Context, s *Session, key, value string) error {
	if err := m.repo.SetContext(ctx, s.ID, key, value); err != nil {
		return err
	}
	s.SetContext(key, value)
	return nil
}

// SetContextBatch guarda múltiples valores de contexto (BD + memoria)
func (m *SessionManager) SetContextBatch(ctx context.Context, s *Session, kvs map[string]string) error {
	if err := m.repo.SetContextBatch(ctx, s.ID, kvs); err != nil {
		return err
	}
	for k, v := range kvs {
		s.SetContext(k, v)
	}
	return nil
}

// ClearAllContext borra todo el contexto de la sesión (BD + memoria)
func (m *SessionManager) ClearAllContext(ctx context.Context, s *Session) error {
	if err := m.repo.ClearAllContext(ctx, s.ID); err != nil {
		return err
	}
	s.Context = make(map[string]string)
	return nil
}

// Complete marca la sesión como completada
func (m *SessionManager) Complete(ctx context.Context, s *Session) error {
	s.Status = StatusCompleted
	return m.repo.UpdateStatus(ctx, s.ID, StatusCompleted)
}

// Escalate marca la sesión como escalada a agente con tracking de equipo.
func (m *SessionManager) Escalate(ctx context.Context, s *Session, teamID string) error {
	s.Status = StatusEscalated
	s.EscalatedTeam = teamID
	now := time.Now()
	s.EscalatedAt = &now
	return m.repo.MarkEscalated(ctx, s.ID, teamID)
}

// ResumeFromEscalation transitions a session from escalated back to active at a specific state.
func (m *SessionManager) ResumeFromEscalation(ctx context.Context, s *Session, targetState string) error {
	s.Status = StatusActive
	s.CurrentState = targetState
	now := time.Now()
	s.ResumedAt = &now
	s.ExpiresAt = now.Add(m.timeout)
	return m.repo.ResumeSession(ctx, s.ID, targetState, int(m.timeout.Minutes()))
}

// UpdateConversationID finds the active session for a phone and persists the conversationID.
// No-op if no active session exists or if the ID is already set to the same value.
func (m *SessionManager) UpdateConversationID(ctx context.Context, phone, conversationID string) error {
	if phone == "" || conversationID == "" {
		return nil
	}
	s, err := m.repo.FindActiveByPhone(ctx, phone)
	if err != nil || s == nil {
		return err
	}
	if s.ConversationID == conversationID {
		return nil
	}
	s.ConversationID = conversationID
	if err := m.repo.Save(ctx, s); err != nil {
		slog.Error(
			"update_conversation_id_failed",
			"phone", utils.MaskPhone(phone),
			"conversation_id", conversationID,
			"session_id", s.ID,
			"error", err,
		)
		return err
	}
	return nil
}

// PhoneMutex retorna el mutex para uso del worker pool
func (m *SessionManager) PhoneMutex() *PhoneMutex {
	return m.mutex
}

// StartInactivityChecker runs a goroutine that checks for inactive sessions every minute.
// For active sessions: sends reminders at configured intervals and auto-closes after final timeout.
// For escalated sessions: marks as abandoned when SESSION_TIMEOUT_MINUTES expires and closes Bird feed item.
func (m *SessionManager) StartInactivityChecker(ctx context.Context, deps InactivityDeps) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	var checking atomic.Bool // prevents concurrent checks if a tick takes > 1 min

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !checking.CompareAndSwap(false, true) {
				slog.Debug("inactivity check skipped, previous still running")
				continue
			}
			m.checkInactiveSessions(ctx, deps)
			m.checkExpiredEscalations(ctx, deps)
			checking.Store(false)
		}
	}
}

// checkInactiveSessions handles reminder sending and auto-close for active sessions.
// Two tiers: (1) single reminder at ReminderMin, (2) silent close at CloseMin.
func (m *SessionManager) checkInactiveSessions(ctx context.Context, deps InactivityDeps) {
	// Query sessions idle for at least the reminder threshold
	sessions, err := m.repo.FindInactiveSessions(ctx, deps.ReminderMin)
	if err != nil {
		slog.Error("inactivity check error", "error", err)
		return
	}

	for _, s := range sessions {
		elapsed := time.Since(s.LastActivity)
		elapsedMin := int(elapsed.Minutes())

		if elapsedMin >= deps.CloseMin && s.Reminders >= 1 {
			if err := m.repo.UpdateStatus(ctx, s.ID, StatusCompleted); err != nil {
				slog.Error("inactivity close failed", "session_id", s.ID, "error", err)
				continue
			}
			if s.ConversationID != "" {
				deps.BirdClient.CloseFeedItems(s.ConversationID)
			}
			if deps.Tracker != nil {
				deps.Tracker.LogEvent(ctx, s.ID, s.PhoneNumber, "session_closed_inactivity", map[string]interface{}{
					"idle_minutes": elapsedMin,
				})
			}
			slog.Info("session closed by inactivity",
				"session_id", s.ID, "phone", utils.MaskPhone(s.PhoneNumber), "idle_min", elapsedMin)

		} else if elapsedMin >= deps.ReminderMin && s.Reminders == 0 {
			// Single reminder with close warning
			closeIn := deps.CloseMin - deps.ReminderMin
			deps.BirdClient.SendText(s.PhoneNumber, s.ConversationID,
				fmt.Sprintf("¿Sigues ahí? Si no respondes en %s se cerrará la sesión.\n\nPuedes volver al menú principal enviando *0* o *menú*.", formatMinutes(closeIn)))
			if err := m.repo.SetContext(ctx, s.ID, "inactivity_reminders", "1"); err != nil {
				slog.Error("set reminder failed", "session_id", s.ID, "error", err)
			}
			slog.Debug("inactivity reminder sent", "session_id", s.ID, "phone", utils.MaskPhone(s.PhoneNumber))
		}
	}
}

// checkExpiredEscalations marks expired escalated sessions as abandoned and closes their Bird feed items.
func (m *SessionManager) checkExpiredEscalations(ctx context.Context, deps InactivityDeps) {
	sessions, err := m.repo.FindExpiredEscalatedSessions(ctx)
	if err != nil {
		slog.Error("expired escalation check error", "error", err)
		return
	}

	for _, s := range sessions {
		if err := m.repo.MarkAbandoned(ctx, s.ID); err != nil {
			slog.Error("mark escalated abandoned failed", "session_id", s.ID, "error", err)
			continue
		}
		if s.ConversationID != "" {
			deps.BirdClient.CloseFeedItems(s.ConversationID)
		}
		if deps.Tracker != nil {
			deps.Tracker.LogEvent(ctx, s.ID, s.PhoneNumber, "escalation_expired", nil)
		}
		observability.Emit(observability.TraceSession(s.ID), "escalacion", "escalation_expired",
			observability.EmitOpts{Phone: s.PhoneNumber})
		slog.Info("escalated session expired",
			"session_id", s.ID, "phone", utils.MaskPhone(s.PhoneNumber),
			"conversation_id", fmt.Sprintf("%.8s", s.ConversationID))
	}
}

// formatMinutes convierte minutos a texto legible: "1 hora", "2 horas", "1 hora 30 minutos", etc.
func formatMinutes(m int) string {
	if m < 60 {
		return fmt.Sprintf("%d minutos", m)
	}
	h := m / 60
	rem := m % 60
	var hText string
	if h == 1 {
		hText = "1 hora"
	} else {
		hText = fmt.Sprintf("%d horas", h)
	}
	if rem == 0 {
		return hText
	}
	return fmt.Sprintf("%s %d minutos", hText, rem)
}
