package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// ErrActiveSessionExists lo devuelve Create cuando el índice único uq_active_phone (migración 032)
// rechaza una segunda sesión activa/escalada para el mismo teléfono. FindOrCreate lo maneja re-leyendo
// la sesión ganadora (cierra el race M3 worker↔NotificationManager sin compartir candado).
var ErrActiveSessionExists = errors.New("active session already exists for phone")

// SessionRepo define la interfaz que necesita el manager (implementada por local.SessionRepo)
type SessionRepo interface {
	FindActiveByPhone(ctx context.Context, phone string) (*Session, error)
	Create(ctx context.Context, session *Session) error
	Save(ctx context.Context, session *Session) error
	UpdateStatus(ctx context.Context, sessionID, status string) error
	UpdateConversationIDByPhone(ctx context.Context, phone, conversationID string) error
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
	FindEscalatedSessions(ctx context.Context) ([]EscalatedSession, error)
	TouchPatientActivity(ctx context.Context, sessionID string, expiresAt time.Time) error
	TouchAgentActivity(ctx context.Context, phone string) error
	IncrementAgentReminders(ctx context.Context, sessionID string) error
	MarkAbandoned(ctx context.Context, sessionID string) error
	CompleteActiveByPhone(ctx context.Context, phone string) error
}

// InactivityBirdClient defines the Bird client methods needed by the inactivity checker.
type InactivityBirdClient interface {
	SendText(to, conversationID, text string) (string, error)
	SendInternalText(conversationID, text string) (string, error)
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
	ReminderMin int // Minutes before sending the single reminder (active sessions)
	CloseMin    int // Minutes before silent close of an active session (must be > ReminderMin)

	// Escalated chats (agente humano)
	EscalationCloseMin int // Cierre por silencio del PACIENTE (no del agente)
	AgentReminderMin   int // Minutos sin respuesta del agente antes de recordarle
	AgentReminderMax   int // Máximo de recordatorios por ventana de espera
	// Resumer devuelve al bot las escalaciones no atendidas por el agente (no-show). nil = no actúa
	// (en ese caso el no-show no se procesa y la escalación sigue hasta el cierre por silencio).
	Resumer EscalationResumer
}

// EscalationRecorder registra el ciclo de vida de cada escalación en la tabla `escalations`
// (una fila por escalación, para el SLA y el KPI por paso). Opcional (nil = no registra).
type EscalationRecorder interface {
	TouchAgent(ctx context.Context, phone string) error
	Resume(ctx context.Context, phone string) error
	Close(ctx context.Context, phone string) error
	Expire(ctx context.Context, sessionID string) error
	NoShow(ctx context.Context, sessionID string) error // agente nunca atendió → bot retoma
}

// EscalationResumer devuelve al bot una escalación que el agente NUNCA atendió (no-show): reanuda la
// sesión en su pre_escalation_state y re-muestra el paso al paciente. Lo implementa el worker (necesita
// la state machine para re-promptear); el checker de escalaciones lo invoca. Opcional (nil = no actúa).
type EscalationResumer interface {
	ResumeEscalationNoShow(phone string)
}

type SessionManager struct {
	repo        SessionRepo
	mutex       *PhoneMutex
	timeout     time.Duration
	escalations EscalationRecorder
}

func NewSessionManager(repo SessionRepo, timeoutMinutes int) *SessionManager {
	return &SessionManager{
		repo:    repo,
		mutex:   NewPhoneMutex(),
		timeout: time.Duration(timeoutMinutes) * time.Minute,
	}
}

// SetEscalationRecorder inyecta el registro por-escalación (opcional).
func (m *SessionManager) SetEscalationRecorder(e EscalationRecorder) { m.escalations = e }

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
		// M3: otra ruta (worker/NotificationManager) creó la sesión activa de este teléfono entre nuestro
		// FindActiveByPhone y el Create (índice único uq_active_phone). Re-leemos la ganadora en vez de
		// duplicar — así nunca quedan dos sesiones activas para el mismo teléfono.
		if errors.Is(err, ErrActiveSessionExists) {
			if existing, ferr := m.repo.FindActiveByPhone(ctx, phone); ferr == nil && existing != nil {
				ctxMap, cerr := m.repo.GetAllContext(ctx, existing.ID)
				if cerr != nil {
					return nil, false, cerr
				}
				existing.Context = ctxMap
				return existing, false, nil
			}
		}
		return nil, false, err
	}

	return newSession, true, nil
}

// RenewTimeout renueva el expires_at con cada mensaje (TTL de liveness de la sesión).
// Lo usa tanto el flujo del paciente como el del agente, así que NO toca los relojes de
// actividad del paciente — para eso está TouchPatientActivity.
func (m *SessionManager) RenewTimeout(ctx context.Context, s *Session) error {
	s.ExpiresAt = time.Now().Add(m.timeout)
	return m.repo.RenewExpiry(ctx, s.ID, s.ExpiresAt)
}

// TouchPatientActivity registra un mensaje inbound del paciente: renueva el TTL y sella el reloj de
// actividad del paciente (last_patient_msg_at), reiniciando la ventana de recordatorios al agente.
// Debe llamarse SOLO desde el flujo del paciente (no desde comandos del agente).
func (m *SessionManager) TouchPatientActivity(ctx context.Context, s *Session) error {
	s.ExpiresAt = time.Now().Add(m.timeout)
	return m.repo.TouchPatientActivity(ctx, s.ID, s.ExpiresAt)
}

// TouchAgentActivity sella last_agent_msg_at para la sesión escalada del teléfono (si existe).
// La invoca el webhook outbound cuando el agente responde; frena el recordatorio al agente.
func (m *SessionManager) TouchAgentActivity(ctx context.Context, phone string) error {
	if m.escalations != nil {
		if err := m.escalations.TouchAgent(ctx, phone); err != nil {
			slog.Warn("escalation touch agent failed", "error", err)
		}
	}
	return m.repo.TouchAgentActivity(ctx, phone)
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
	// Si se completa una sesión ESCALADA (p.ej. el agente cerró con /bot cerrar), marca la escalación.
	if s.Status == StatusEscalated && m.escalations != nil {
		if err := m.escalations.Close(ctx, s.PhoneNumber); err != nil {
			slog.Warn("escalation close failed", "error", err)
		}
	}
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
	if m.escalations != nil {
		if err := m.escalations.Resume(ctx, s.PhoneNumber); err != nil {
			slog.Warn("escalation resume failed", "error", err)
		}
	}
	return m.repo.ResumeSession(ctx, s.ID, targetState, int(m.timeout.Minutes()))
}

// ResumeFromEscalationNoShow reanuda igual que ResumeFromEscalation pero marca la escalación como
// 'agent_no_show' (el agente nunca atendió), no como 'returned'. Lo usa el worker al devolver al bot
// una escalación que agotó los recordatorios sin respuesta del agente.
func (m *SessionManager) ResumeFromEscalationNoShow(ctx context.Context, s *Session, targetState string) error {
	s.Status = StatusActive
	s.CurrentState = targetState
	now := time.Now()
	s.ResumedAt = &now
	s.ExpiresAt = now.Add(m.timeout)
	if m.escalations != nil {
		if err := m.escalations.NoShow(ctx, s.ID); err != nil {
			slog.Warn("escalation no-show mark failed", "error", err)
		}
	}
	return m.repo.ResumeSession(ctx, s.ID, targetState, int(m.timeout.Minutes()))
}

// UpdateConversationID persiste el conversationID en la sesión activa/escalada del teléfono.
// H2: hace un UPDATE DIRIGIDO de solo esa columna (no FindActiveByPhone + Save de la fila completa).
// El viejo enfoque corría desde el webhook outbound FUERA del phone-lock y, al reescribir toda la
// fila desde un snapshot viejo, podía revertir current_state y la PII del paciente a mitad de un
// agendamiento (lost update). Un UPDATE de una columna no puede pisar el resto de la fila.
func (m *SessionManager) UpdateConversationID(ctx context.Context, phone, conversationID string) error {
	if phone == "" || conversationID == "" {
		return nil
	}
	if err := m.repo.UpdateConversationIDByPhone(ctx, phone, conversationID); err != nil {
		slog.Error(
			"update_conversation_id_failed",
			"phone", utils.MaskPhone(phone),
			"conversation_id", conversationID,
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
			m.checkEscalatedSessions(ctx, deps)
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
			// Cierre por inactividad = ABANDONO (no "completada"): el paciente dejó de responder.
			// Antes se marcaba 'completed', mezclando abandono con finalización real en sessions.status.
			if err := m.repo.UpdateStatus(ctx, s.ID, StatusAbandoned); err != nil {
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
			observability.Emit(observability.TraceSession(s.ID), "infra", "session_abandoned",
				observability.EmitOpts{
					Phone: s.PhoneNumber,
					Attrs: map[string]interface{}{"duration_ms": elapsed.Milliseconds()},
				})

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

// checkEscalatedSessions, por cada chat escalado, decide UNA de dos cosas:
//  1. Cerrar (marcar abandonado + cerrar feed) si el PACIENTE lleva EscalationCloseMin en silencio.
//     El silencio del agente NUNCA cierra el chat (last_patient_msg_at solo lo mueve el paciente).
//  2. Si el paciente sigue esperando y el agente no ha respondido, recordarle al agente vía Inbox
//     cada AgentReminderMin (hasta AgentReminderMax) sin molestar al paciente.
func (m *SessionManager) checkEscalatedSessions(ctx context.Context, deps InactivityDeps) {
	sessions, err := m.repo.FindEscalatedSessions(ctx)
	if err != nil {
		slog.Error("escalated sessions check error", "error", err)
		return
	}

	now := time.Now()
	for _, s := range sessions {
		patientSilent := now.Sub(s.LastPatientMsg)
		agentEverReplied := s.LastAgentMsg != nil

		// (0) Agente NO-SHOW: el agente NUNCA respondió y ya pasó la ventana de recordatorios
		// (AgentReminderMin*(Max+1) = 15 min tras el último recordatorio). NO es abandono del paciente
		// (estaba esperando una respuesta que no llegó) → se DEVUELVE al bot en su paso (el worker
		// re-promptea) y se marca 'agent_no_show' para la métrica del agente. Requiere el resumer.
		if !agentEverReplied && deps.Resumer != nil && deps.AgentReminderMin > 0 &&
			patientSilent >= time.Duration(deps.AgentReminderMin*(deps.AgentReminderMax+1))*time.Minute {
			deps.Resumer.ResumeEscalationNoShow(s.PhoneNumber)
			slog.Info("escalation no-show: agente nunca atendió, devolviendo al bot",
				"session_id", s.ID, "phone", utils.MaskPhone(s.PhoneNumber),
				"patient_silent_min", int(patientSilent.Minutes()), "reminders_sent", s.RemindersSent)
			continue
		}

		// (1) Cierre por silencio del PACIENTE — solo si el agente YA atendió al menos una vez. Si el
		// agente nunca atendió, lo maneja (0) no-show; el fallback (sin resumer) cierra igual para no
		// fugar la sesión.
		if deps.EscalationCloseMin > 0 && patientSilent >= time.Duration(deps.EscalationCloseMin)*time.Minute &&
			(agentEverReplied || deps.Resumer == nil) {
			if err := m.repo.MarkAbandoned(ctx, s.ID); err != nil {
				slog.Error("mark escalated abandoned failed", "session_id", s.ID, "error", err)
				continue
			}
			if m.escalations != nil {
				if err := m.escalations.Expire(ctx, s.ID); err != nil {
					slog.Warn("escalation expire failed", "session_id", s.ID, "error", err)
				}
			}
			if s.ConversationID != "" {
				if err := deps.BirdClient.CloseFeedItems(s.ConversationID); err != nil {
					slog.Warn("close feed on escalated close failed", "session_id", s.ID, "error", err)
				}
			}
			if deps.Tracker != nil {
				deps.Tracker.LogEvent(ctx, s.ID, s.PhoneNumber, "escalation_expired", map[string]interface{}{
					"patient_silent_min": int(patientSilent.Minutes()),
				})
			}
			observability.Emit(observability.TraceSession(s.ID), "escalacion", "escalation_expired",
				observability.EmitOpts{
					Phone: s.PhoneNumber,
					Attrs: map[string]interface{}{"duration_ms": patientSilent.Milliseconds()},
				})
			slog.Info("escalated session closed by patient silence",
				"session_id", s.ID, "phone", utils.MaskPhone(s.PhoneNumber),
				"patient_silent_min", int(patientSilent.Minutes()))
			continue
		}

		// (2) Recordatorio al agente: el paciente espera y el agente no ha respondido a su último mensaje.
		if deps.AgentReminderMin <= 0 || s.ConversationID == "" {
			continue
		}
		agentReplied := s.LastAgentMsg != nil && !s.LastAgentMsg.Before(s.LastPatientMsg)
		if agentReplied || s.RemindersSent >= deps.AgentReminderMax {
			continue
		}
		// Próximo recordatorio en AgentReminderMin * (enviados + 1) tras el último mensaje del paciente.
		dueAfter := time.Duration(deps.AgentReminderMin*(s.RemindersSent+1)) * time.Minute
		if patientSilent < dueAfter {
			continue
		}
		reminder := fmt.Sprintf(
			"Recordatorio interno: el paciente lleva %s esperando respuesta en este chat escalado. "+
				"Por favor atiéndelo. (aviso %d de %d)",
			formatMinutes(int(patientSilent.Minutes())), s.RemindersSent+1, deps.AgentReminderMax,
		)
		if _, err := deps.BirdClient.SendInternalText(s.ConversationID, reminder); err != nil {
			// BUG-005: si Bird responde que la conversación ya NO está activa (el agente la cerró),
			// el recordatorio nunca tendrá éxito. Sin esto, el checker reintentaba cada minuto
			// indefinidamente (no incrementa RemindersSent en el error) hasta la expiración a las 6h.
			// La conversación cerrada = el agente terminó el chat → cerrar la escalación, igual que el
			// cierre por silencio del paciente.
			if errors.Is(err, bird.ErrConversationNotActive) {
				if cerr := m.repo.MarkAbandoned(ctx, s.ID); cerr != nil {
					slog.Error("close escalated on inactive conversation failed", "session_id", s.ID, "error", cerr)
					continue
				}
				if m.escalations != nil {
					if eerr := m.escalations.Expire(ctx, s.ID); eerr != nil {
						slog.Warn("escalation expire failed", "session_id", s.ID, "error", eerr)
					}
				}
				if deps.Tracker != nil {
					deps.Tracker.LogEvent(ctx, s.ID, s.PhoneNumber, "escalation_closed_conversation_inactive", nil)
				}
				observability.Emit(observability.TraceSession(s.ID), "escalacion", "agent_closed",
					observability.EmitOpts{Phone: s.PhoneNumber, Reason: "conversation_inactive"})
				slog.Info("escalated session closed: bird conversation inactive (agente la cerró)",
					"session_id", s.ID, "phone", utils.MaskPhone(s.PhoneNumber))
				continue
			}
			slog.Error("agent reminder send failed", "session_id", s.ID, "conversation_id", s.ConversationID, "error", err)
			continue
		}
		if err := m.repo.IncrementAgentReminders(ctx, s.ID); err != nil {
			slog.Error("increment agent reminders failed", "session_id", s.ID, "error", err)
		}
		if deps.Tracker != nil {
			deps.Tracker.LogEvent(ctx, s.ID, s.PhoneNumber, "agent_reminder_sent", map[string]interface{}{
				"reminder_n":         s.RemindersSent + 1,
				"patient_silent_min": int(patientSilent.Minutes()),
			})
		}
		observability.Emit(observability.TraceSession(s.ID), "escalacion", "agent_reminder_sent",
			observability.EmitOpts{
				Phone: s.PhoneNumber,
				Attrs: map[string]interface{}{"n": s.RemindersSent + 1, "duration_ms": patientSilent.Milliseconds()},
			})
		slog.Info("agent reminder sent",
			"session_id", s.ID, "phone", utils.MaskPhone(s.PhoneNumber),
			"reminder_n", s.RemindersSent+1, "patient_silent_min", int(patientSilent.Minutes()))
	}
}

// formatMinutes convierte minutos a texto legible: "1 hora", "2 horas", "1 hora 30 minutos", etc.
func formatMinutes(m int) string {
	if m < 0 {
		m = 0 // L17: un override con CLOSE_MIN <= REMINDER_MIN daría closeIn negativo → "-5 minutos"
	}
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
