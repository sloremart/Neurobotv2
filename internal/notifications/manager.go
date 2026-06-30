package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// PendingNotification tracks a proactive notification awaiting response.
type PendingNotification struct {
	Type           string // "confirmation", "reschedule", "cancellation", "waiting_list"
	Phone          string
	AppointmentID  string
	WaitingListID  string // only for waiting_list type
	BirdMessageID  string
	ConversationID string
	CallID         string // Bird IVR call ID (set after PlaceCall, persisted for restart recovery)
	Timer          *time.Timer
	RetryCount     int
	InvalidInputs  int // free-text messages received while waiting for button press
	CreatedAt      time.Time
	// ExpiresAt es el instante de vencimiento de la entrada vigente. N-19: handleTimeout lo
	// re-valida antes de actuar, para no procesar como "vencida" una entrada que fue re-armada
	// (p.ej. el timer post-IVR de MarkIVRSent) cuando la dispara un trigger viejo.
	ExpiresAt time.Time
}

// WaitingListFinder reads/updates waiting list entries (avoids importing repo/local directly).
type WaitingListFinder interface {
	FindByID(ctx context.Context, id string) (*domain.WaitingListEntry, error)
	UpdateStatus(ctx context.Context, id, status string) error
}

// SessionCreator creates sessions and sets context (avoids importing repo/local directly).
type SessionCreator interface {
	Create(ctx context.Context, s *session.Session) error
	SetContextBatch(ctx context.Context, sessionID string, kvs map[string]string) error
	UpdateStatus(ctx context.Context, sessionID, status string) error
	CompleteActiveByPhone(ctx context.Context, phone string) error
}

// VirtualEnqueuer enqueues virtual messages for the worker pool.
type VirtualEnqueuer interface {
	EnqueueVirtual(phone string)
}

// PreparationFinder looks up procedure preparation data.
type PreparationFinder interface {
	FindByCode(ctx context.Context, code string) (*domain.Procedure, error)
}

// NotificationPersister persists pending notifications to the database.
type NotificationPersister interface {
	Upsert(ctx context.Context, phone, nType, apptID, wlID, birdMsgID, convID string, retryCount int, expiresAt time.Time) error
	UpdateCallID(ctx context.Context, phone, callID string) error
	Resolve(ctx context.Context, phone, status string) error
	DeleteHistoryOlderThan(ctx context.Context, days int) (int64, error)
	FindExpired(ctx context.Context) ([]PendingRow, error)
	FindAll(ctx context.Context) ([]PendingRow, error)
}

// CallTracker persists IVR call records to the database for KPI tracking.
type CallTracker interface {
	InsertCall(ctx context.Context, callID, phone, appointmentID string) error
	UpdateCallResult(ctx context.Context, callID, status, result string) error
}

// PendingRow represents a pending notification row from the database.
type PendingRow struct {
	Phone          string
	Type           string
	AppointmentID  string
	WaitingListID  string
	BirdMessageID  string
	ConversationID string
	CallID         string
	RetryCount     int
	ExpiresAt      time.Time
	CreatedAt      time.Time
}

// EventLogger logs notification events for auditing.
type EventLogger interface {
	LogEvent(ctx context.Context, sessionID, phone, eventType string, data map[string]interface{})
}

// SlotSearcher checks slot availability (used by Cambio 13 real-time WL check).
type SlotSearcher interface {
	GetAvailableSlots(ctx context.Context, query services.SlotQuery) ([]services.AvailableSlot, error)
}

// FutureAppointmentChecker checks if a patient already has a future appointment for a CUPS.
type FutureAppointmentChecker interface {
	HasFutureForCup(ctx context.Context, patientID, cupCode string) (bool, error)
}

// WaitingListChecker reads and updates waiting list entries for real-time notification.
type WaitingListChecker interface {
	GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	MarkNotified(ctx context.Context, id string) (bool, error)
	UpdateStatus(ctx context.Context, id, status string) error
}

// NotificationManager handles responses to proactive WhatsApp templates.
type NotificationManager struct {
	pending         sync.Map // phone → *PendingNotification
	callIDMap       sync.Map // callId → phone (for IVR DTMF result correlation)
	birdClient      *bird.Client
	apptSvc         *services.AppointmentService
	cfg             *config.Config
	waitingListRepo WaitingListFinder
	sessionRepo     SessionCreator
	workerPool      VirtualEnqueuer
	procRepo        PreparationFinder
	addrMapper      *services.AddressMapper
	persister       NotificationPersister
	callTracker     CallTracker
	tracker         EventLogger
	escalations     EscalationRecorder

	// Cambio 13: real-time WL notification on cancellation
	slotSearcher SlotSearcher
	apptChecker  FutureAppointmentChecker
	wlChecker    WaitingListChecker

	// phoneLock serializa por-teléfono TODO acceso al estado de notificación (pending/callIDMap),
	// que es propiedad exclusiva de este manager. Cierra el data race N-17 (campos de
	// *PendingNotification mutados desde webhooks, timers y el scheduler) y el TOCTOU N-19. Es
	// una instancia PROPIA (no la del worker pool): el worker no toca `pending`, así que un
	// candado dedicado los protege al 100% y evita deadlocks al cruzar EnqueueVirtual.
	phoneLock *session.PhoneMutex
}

// notifLockTimeout acota la ESPERA por el candado de un teléfono (no cuánto se sostiene).
const notifLockTimeout = 30 * time.Second

// NewNotificationManager creates a new notification manager.
func NewNotificationManager(birdClient *bird.Client, apptSvc *services.AppointmentService, cfg *config.Config) *NotificationManager {
	return &NotificationManager{
		birdClient: birdClient,
		apptSvc:    apptSvc,
		cfg:        cfg,
		phoneLock:  session.NewPhoneMutex(),
	}
}

// lockPhone adquiere el candado por-teléfono del estado de notificación. Devuelve false si no
// pudo (timeout); el caller debe abortar la operación. Si no hay candado configurado (tests que
// construyen el manager por literal), procede sin serializar.
func (m *NotificationManager) lockPhone(phone string) bool {
	if m.phoneLock == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifLockTimeout)
	defer cancel()
	if err := m.phoneLock.Lock(ctx, phone); err != nil {
		slog.Warn("notif_phone_lock_timeout", "phone", utils.MaskPhone(phone), "error", err)
		return false
	}
	return true
}

func (m *NotificationManager) unlockPhone(phone string) {
	if m.phoneLock != nil {
		m.phoneLock.Unlock(phone)
	}
}

// SetWaitingListDeps injects Phase 13 dependencies after construction.
func (m *NotificationManager) SetWaitingListDeps(wlRepo WaitingListFinder, sessRepo SessionCreator, wp VirtualEnqueuer) {
	m.waitingListRepo = wlRepo
	m.sessionRepo = sessRepo
	m.workerPool = wp
}

// SetProcedureRepo injects the procedure repository for preparation lookups.
func (m *NotificationManager) SetProcedureRepo(repo PreparationFinder) {
	m.procRepo = repo
}

// SetAddressMapper injects the address-to-maps-URL mapper.
func (m *NotificationManager) SetAddressMapper(am *services.AddressMapper) {
	m.addrMapper = am
}

// SetPersister injects the database persister for pending notifications.
func (m *NotificationManager) SetPersister(p NotificationPersister) {
	m.persister = p
}

// CleanupHistory borra el historial de notificaciones (notification_history) con más de `days`
// días, para que la tabla de auditoría no crezca indefinidamente. Devuelve cuántas borró.
func (m *NotificationManager) CleanupHistory(ctx context.Context, days int) (int64, error) {
	if m.persister == nil {
		return 0, nil
	}
	return m.persister.DeleteHistoryOlderThan(ctx, days)
}

// SetCallTracker injects the KPI tracker for IVR call records.
func (m *NotificationManager) SetCallTracker(ct CallTracker) {
	m.callTracker = ct
}

// SetTracker injects the event logger for auditing.
func (m *NotificationManager) SetTracker(t EventLogger) {
	m.tracker = t
}

// EscalationRecorder registra una escalación en la tabla `escalations` (una fila por escalación).
type EscalationRecorder interface {
	Create(ctx context.Context, sessionID, phone, fromState, teamID, agentID, agentName string) error
}

// SetEscalationRecorder injects the per-escalation recorder (tabla escalations) for SLA tracking.
func (m *NotificationManager) SetEscalationRecorder(e EscalationRecorder) {
	m.escalations = e
}

// SetWaitingListCheckDeps injects Cambio 13 dependencies for real-time WL notification.
func (m *NotificationManager) SetWaitingListCheckDeps(ss SlotSearcher, ac FutureAppointmentChecker, wlc WaitingListChecker) {
	m.slotSearcher = ss
	m.apptChecker = ac
	m.wlChecker = wlc
}

// RegisterPending registers a pending notification with a type-appropriate timeout.
// Confirmation/reschedule use configurable ConfirmFollowup1Hours; others default to 6h.
// RegisterPending almacena un pending. Devuelve false si NO se pudo registrar (timeout del lock por
// teléfono): el caller que necesite garantizar el seguimiento de la respuesta puede recuperarse
// (p.ej. la lista de espera revierte el claim a 'waiting'). L12.
func (m *NotificationManager) RegisterPending(notif PendingNotification) bool {
	if !m.lockPhone(notif.Phone) {
		slog.Warn("register pending: phone lock timeout, not stored", "phone", utils.MaskPhone(notif.Phone), "type", notif.Type)
		return false
	}
	defer m.unlockPhone(notif.Phone)

	notif.CreatedAt = time.Now()

	// Confirmation/reschedule: when followup disabled, keep pending alive until after 1 PM IVR
	// (timer fires at 8h to not interfere); when enabled, use configurable follow-up hours.
	var duration time.Duration
	switch notif.Type {
	case "confirmation", "reschedule":
		if m.cfg.ConfirmFollowupEnabled {
			duration = time.Duration(safeHours(m.cfg.ConfirmFollowup1Hours, 3)) * time.Hour
		} else {
			duration = 8 * time.Hour // fires at ~3 PM, well after 1 PM IVR
		}
	default:
		duration = 6 * time.Hour
	}

	expiresAt := notif.CreatedAt.Add(duration)
	notif.ExpiresAt = expiresAt // N-19

	// #6 (auditoría): detener el timer del pending PREVIO (si existe) antes de sobreescribirlo, o su
	// time.AfterFunc seguiría vivo y dispararía handleTimeout sobre datos viejos (fuga de timer).
	if old, ok := m.pending.Load(notif.Phone); ok {
		if op, _ := old.(*PendingNotification); op != nil && op.Timer != nil {
			op.Timer.Stop()
		}
	}

	// In-memory timer (handles timeout while running)
	notif.Timer = time.AfterFunc(duration, func() {
		m.handleTimeout(notif.Phone)
	})

	m.pending.Store(notif.Phone, &notif)

	// Persist to DB (survives restarts)
	if m.persister != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.persister.Upsert(ctx, notif.Phone, notif.Type,
			notif.AppointmentID, notif.WaitingListID, notif.BirdMessageID, notif.ConversationID,
			notif.RetryCount, expiresAt); err != nil {
			slog.Error("persist pending notification", "phone", utils.MaskPhone(notif.Phone), "error", err)
		}
	}

	slog.Info("pending notification registered", "phone", utils.MaskPhone(notif.Phone), "type", notif.Type)
	return true
}

// HandleResponse processes a patient's response to a proactive template.
// Uses LoadAndDelete to atomically claim ownership and prevent race with handleTimeout.
func (m *NotificationManager) HandleResponse(phone, payload, conversationID string) {
	if !m.lockPhone(phone) {
		return
	}
	defer m.unlockPhone(phone)

	val, ok := m.pending.LoadAndDelete(phone)
	if !ok {
		slog.Warn("no pending notification for phone", "phone", utils.MaskPhone(phone), "payload", payload)
		return
	}

	pending := val.(*PendingNotification)
	pending.Timer.Stop()
	// Cleanup callIDMap
	if pending.CallID != "" {
		m.callIDMap.Delete(pending.CallID)
	}
	// Only overwrite if the webhook provides a non-empty conversationID.
	// Template responses often arrive without conversationId; don't lose the
	// stored value from the original template send or outbound webhook.
	if conversationID != "" {
		pending.ConversationID = conversationID
	}
	// If still empty, try cache (outbound webhook may have populated it) or API lookup
	if pending.ConversationID == "" {
		if cached := m.birdClient.GetCachedConversationID(phone); cached != "" {
			pending.ConversationID = cached
		} else if looked, err := m.birdClient.LookupConversationByPhone(phone); err == nil && looked != "" {
			pending.ConversationID = looked
		}
	}

	normalized := normalizePostback(payload)

	// Mover a notification_history con el estado según la respuesta del paciente (evidencia +
	// conversation_id), y quitar de la tabla activa.
	if m.persister != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.persister.Resolve(ctx, phone, responseStatus(normalized))
	}

	switch pending.Type {
	case "confirmation":
		m.handleConfirmation(phone, normalized, pending)
	case "reschedule":
		m.handleReschedule(phone, normalized, pending)
	case "cancellation":
		m.handleCancellation(phone, normalized, pending)
	case "waiting_list":
		if m.waitingListRepo != nil {
			m.handleWaitingList(phone, normalized, pending)
		}
	}
}

// HandleNotifPendingCommand processes a /bot resume NOTIF_PENDING command from an agent.
// Called when the pending notification was already removed from memory (escalated path),
// so the appointment data is reconstructed from the session context.
func (m *NotificationManager) HandleNotifPendingCommand(phone, action, convID, appointmentID, notifType string) {
	if !m.lockPhone(phone) {
		return
	}
	defer m.unlockPhone(phone)

	slog.Info(
		"agent handling notif_pending command",
		"phone", utils.MaskPhone(phone),
		"action", action,
		"appointment_id", appointmentID,
		"notif_type", notifType,
	)

	pending := &PendingNotification{
		Phone:          phone,
		AppointmentID:  appointmentID,
		Type:           notifType,
		ConversationID: convID,
	}

	normalized := normalizePostback(action)

	switch notifType {
	case "confirmation":
		m.handleConfirmation(phone, normalized, pending)
	case "reschedule":
		m.handleReschedule(phone, normalized, pending)
	case "cancellation":
		m.handleCancellation(phone, normalized, pending)
	default:
		// Unknown type — treat as confirmation (safest fallback)
		slog.Warn("HandleNotifPendingCommand: unknown notif type, treating as confirmation",
			"phone", utils.MaskPhone(phone), "notif_type", notifType)
		m.handleConfirmation(phone, normalized, pending)
	}
}

// HasPending checks if there's a pending notification for a phone number.
func (m *NotificationManager) HasPending(phone string) bool {
	_, ok := m.pending.Load(phone)
	return ok
}

// HandleInvalidInput is called when a patient sends free text while a notification is pending.
// Resends the confirmation prompt (up to 3 times) instead of starting a new bot session.
// Returns true if the message was consumed (caller should not route to state machine).
func (m *NotificationManager) HandleInvalidInput(phone, conversationID string) bool {
	if !m.lockPhone(phone) {
		return false
	}
	defer m.unlockPhone(phone)

	val, ok := m.pending.LoadAndDelete(phone) // atomic claim
	if !ok {
		return false
	}
	p := val.(*PendingNotification)

	// Intercept all notification types that show buttons
	switch p.Type {
	case "confirmation", "reschedule", "cancellation", "waiting_list":
		// These types have interactive buttons — intercept free text
	default:
		m.pending.Store(phone, p) // re-store: not our type
		return false
	}

	p.InvalidInputs++

	if p.InvalidInputs > 3 {
		// Demasiados intentos inválidos — escalar al agente para que gestione
		if p.Timer != nil {
			p.Timer.Stop()
		}
		if p.CallID != "" {
			m.callIDMap.Delete(p.CallID)
		}
		if m.persister != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = m.persister.Resolve(ctx, phone, "escalated_agent")
		}
		// Actualizar caché con el convID del mensaje entrante (puede diferir del template)
		if conversationID != "" {
			m.birdClient.CacheConversationID(phone, conversationID)
		}
		m.escalateNotifToAgent(p, conversationID)
		return true
	}

	m.pending.Store(phone, p)

	convID := conversationID
	if convID == "" {
		convID = p.ConversationID
	}
	if convID == "" {
		if cached := m.birdClient.GetCachedConversationID(phone); cached != "" {
			convID = cached
		} else if looked, lookErr := m.birdClient.LookupConversationByPhone(phone); lookErr == nil && looked != "" {
			convID = looked
		}
	}

	// Actualizar caché: el paciente puede estar en una conversación diferente al template
	if conversationID != "" {
		m.birdClient.CacheConversationID(phone, conversationID)
	}

	// Re-send type-appropriate buttons
	switch p.Type {
	case "confirmation", "reschedule":
		m.birdClient.SendButtons(phone, convID,
			"Por favor selecciona una opción para gestionar tu cita de mañana:",
			[]bird.Button{
				{Text: "Confirmar", Payload: "confirm"},
				{Text: "Reprogramar", Payload: "reprogramar"},
				{Text: "Cancelar", Payload: "cancelar"},
			})
	case "cancellation":
		m.birdClient.SendButtons(phone, convID,
			"Por favor selecciona una opción para continuar con tu cita:",
			[]bird.Button{
				{Text: "Entendido", Payload: "understood"},
				{Text: "Reprogramar", Payload: "reschedule"},
			})
	case "waiting_list":
		m.birdClient.SendButtons(phone, convID,
			"Se liberó un espacio para tu procedimiento. ¿Deseas agendar la cita?",
			[]bird.Button{
				{Text: "Agendar", Payload: "wl_schedule"},
				{Text: "No, gracias", Payload: "wl_decline"},
			})
	}

	slog.Info(
		"notification invalid input — resent prompt",
		"phone", utils.MaskPhone(phone),
		"type", p.Type,
		"invalid_inputs", p.InvalidInputs,
	)
	return true
}

// GetPendingForIVR returns pending confirmations/reschedules ready for IVR call.
// When followup chain is disabled: returns anyone who received the initial reminder (RetryCount==0).
// When followup chain is enabled: returns those who completed the WA chain (RetryCount==2).
func (m *NotificationManager) GetPendingForIVR() []*PendingNotification {
	targetRetry := 2
	if !m.cfg.ConfirmFollowupEnabled {
		targetRetry = 0
	}
	var result []*PendingNotification
	m.pending.Range(func(key, val interface{}) bool {
		phone, _ := key.(string)
		if !m.lockPhone(phone) {
			return true
		}
		p := val.(*PendingNotification)
		if (p.Type == "confirmation" || p.Type == "reschedule") && p.RetryCount == targetRetry {
			// N-17: devolver una COPIA consistente (leída bajo el lock), no el puntero vivo del
			// mapa, para que el scheduler no aliase un *PendingNotification que MarkIVRSent muta.
			cp := *p
			cp.Timer = nil
			result = append(result, &cp)
		}
		m.unlockPhone(phone)
		return true
	})
	return result
}

// MarkIVRSent updates a pending notification after IVR call was placed.
// Stops old safety-net timer, sets RetryCount=3, starts post-IVR timer (minutes).
func (m *NotificationManager) MarkIVRSent(phone string) {
	if !m.lockPhone(phone) {
		return
	}
	defer m.unlockPhone(phone)

	val, ok := m.pending.Load(phone)
	if !ok {
		return
	}
	p := val.(*PendingNotification)
	if p.Timer != nil {
		p.Timer.Stop()
	}
	p.RetryCount = 3

	if !m.cfg.ConfirmFollowupEnabled {
		// H1 (auditoría): followup deshabilitado, pero el paciente AÚN NO contestó el DTMF. Antes se
		// borraba el pending de inmediato y, al llegar el webhook de voz, HandleVoiceGatherResult ya no
		// encontraba la cita (AppointmentID/ConversationID solo viven en el pending) → confirmar/cancelar
		// por teléfono se perdían (cupo ocupado, no-show garantizado). Ahora se mantiene el pending durante
		// una ventana de gracia (ConfirmPostIVRMinutes) para que el DTMF se procese; si el paciente no
		// contesta, se limpia (pending + callIDMap) y se resuelve como escalado a IVR — SIN cadena de
		// followup (que es justo lo que este flag deshabilita).
		duration := time.Duration(safeMinutes(m.cfg.ConfirmPostIVRMinutes, 30)) * time.Minute
		p.ExpiresAt = time.Now().Add(duration)
		p.Timer = time.AfterFunc(duration, func() {
			if !m.lockPhone(phone) {
				return
			}
			defer m.unlockPhone(phone)
			val, ok := m.pending.LoadAndDelete(phone)
			if !ok {
				return // el DTMF ya lo consumió
			}
			pp := val.(*PendingNotification)
			if pp.CallID != "" {
				m.callIDMap.Delete(pp.CallID) // evita el leak de callIDMap en llamadas sin respuesta
			}
			if m.persister != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = m.persister.Resolve(ctx, phone, "escalated_to_ivr")
			}
			slog.Info("IVR grace window expired (followup disabled), pending cleared", "phone", utils.MaskPhone(phone))
		})
		m.pending.Store(phone, p)
		if m.persister != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := m.persister.Upsert(ctx, p.Phone, p.Type,
				p.AppointmentID, p.WaitingListID, p.BirdMessageID, p.ConversationID,
				p.RetryCount, p.ExpiresAt); err != nil {
				slog.Error("persist IVR sent (followup disabled)", "phone", utils.MaskPhone(phone), "error", err)
			}
		}
		slog.Info("IVR sent (followup disabled), grace window started", "phone", utils.MaskPhone(phone), "minutes", safeMinutes(m.cfg.ConfirmPostIVRMinutes, 30))
		return
	}

	duration := time.Duration(safeMinutes(m.cfg.ConfirmPostIVRMinutes, 30)) * time.Minute
	expiresAt := time.Now().Add(duration)
	p.ExpiresAt = expiresAt // N-19
	p.Timer = time.AfterFunc(duration, func() {
		m.handleTimeout(phone)
	})
	m.pending.Store(phone, p)

	if m.persister != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.persister.Upsert(ctx, p.Phone, p.Type,
			p.AppointmentID, p.WaitingListID, p.BirdMessageID, p.ConversationID,
			p.RetryCount, expiresAt); err != nil {
			slog.Error("persist IVR sent notification", "phone", utils.MaskPhone(phone), "error", err)
		}
	}

	slog.Info("IVR sent, post-IVR timer started", "phone", utils.MaskPhone(phone), "retry", p.RetryCount, "minutes", safeMinutes(m.cfg.ConfirmPostIVRMinutes, 30))
}

// RegisterCallID stores the mapping callId → phone so that when Bird sends the
// voice webhook (call_command_gather_finished), we can resolve the patient.
// Also persists callId to DB (restart recovery) and inserts a KPI call record.
func (m *NotificationManager) RegisterCallID(callID, phone string) {
	if !m.lockPhone(phone) {
		return
	}
	defer m.unlockPhone(phone)

	m.callIDMap.Store(callID, phone)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Store callID in the in-memory pending so MarkIVRSent can carry it forward
	apptID := ""
	if val, ok := m.pending.Load(phone); ok {
		p := val.(*PendingNotification)
		p.CallID = callID
		m.pending.Store(phone, p)
		apptID = p.AppointmentID
	}

	// Persist callId to notification_pending for restart recovery
	if m.persister != nil {
		if err := m.persister.UpdateCallID(ctx, phone, callID); err != nil {
			slog.Error("persist call_id to notification_pending", "phone", utils.MaskPhone(phone), "callId", callID, "error", err)
		}
	}

	// KPI: insert call record into communication_calls
	if m.callTracker != nil {
		if err := m.callTracker.InsertCall(ctx, callID, phone, apptID); err != nil {
			slog.Error("insert ivr call record", "callId", callID, "phone", utils.MaskPhone(phone), "error", err)
		}
	}
}

// HandleVoiceGatherResult processes the DTMF result from a Bird voice IVR call.
//
//   - keys == "1"  → confirm in DB, internal note to Bird Inbox, clear pending (no WA to patient)
//   - keys != "" && != "1" → cancel in DB, internal note, clear pending (no WA to patient)
//   - keys == ""   → patient didn't press any key (gather timed out after 50s);
//     leave appointment and pending untouched, send internal note only
//
// If the call was never answered: no gather webhook fires, the post-IVR timer
// continues and eventually escalates to a human agent.
func (m *NotificationManager) HandleVoiceGatherResult(callID, keys string) {
	val, ok := m.callIDMap.LoadAndDelete(callID)
	if !ok {
		slog.Error("voice gather result: unknown or already processed callId", "callId", callID)
		return
	}
	phone := val.(string)

	if !m.lockPhone(phone) {
		return
	}
	defer m.unlockPhone(phone)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch {
	case keys == "1":
		// ── CONFIRM ──────────────────────────────────────────────────────────
		slog.Info("IVR: patient confirmed", "phone", utils.MaskPhone(phone), "callId", callID)

		pendVal, ok := m.pending.LoadAndDelete(phone)
		if !ok {
			return
		}
		p := pendVal.(*PendingNotification)
		if p.Timer != nil {
			p.Timer.Stop()
		}
		if m.persister != nil {
			_ = m.persister.Resolve(ctx, phone, "confirmed")
		}

		appt, _, err := m.apptSvc.FindBlockByAppointmentID(ctx, p.AppointmentID)
		// N-16: capturar el resultado real de la BD; no afirmar "confirmada" si falló.
		// bug_001: cita no encontrada (appt==nil sin error) también es fallo, no éxito.
		confirmErr := err
		if err == nil && appt == nil {
			confirmErr = errors.New("appointment not found")
		}
		if err == nil && appt != nil {
			// Confirm ALL patient's appointments for this date
			allAppts, aErr := m.apptSvc.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
			if aErr != nil || len(allAppts) == 0 {
				allAppts = []domain.Appointment{*appt}
			}
			confirmErr = m.apptSvc.ConfirmBlock(ctx, allAppts, "ivr", callID)
		}

		if confirmErr != nil {
			slog.Error("IVR confirm failed", "phone", utils.MaskPhone(phone), "callId", callID, "error", confirmErr)
			m.ivrInternalNote(p.ConversationID, phone, callID,
				"⚠️ *IVR — El paciente oprimio *1* (confirmar) pero la confirmacion FALLO en el sistema.*\n"+
					"Revisar y confirmar la cita manualmente.")
			if m.callTracker != nil {
				_ = m.callTracker.UpdateCallResult(ctx, callID, "completed", "error")
			}
			observability.Emit(observability.TraceNotif(p.AppointmentID), "notif_recordatorio", "error",
				observability.EmitOpts{Phone: phone, Reason: "ivr_confirm_persist"})
		} else {
			m.ivrInternalNote(p.ConversationID, phone, callID,
				"✅ *IVR — Cita CONFIRMADA por el paciente via llamada telefonica.*\n"+
					"El paciente oprimio *1*. La cita queda confirmada en el sistema.")
			if m.callTracker != nil {
				_ = m.callTracker.UpdateCallResult(ctx, callID, "completed", "confirmed")
			}
			if m.tracker != nil {
				m.tracker.LogEvent(ctx, "", phone, "notification_confirmed_ivr",
					map[string]interface{}{"appointment_id": p.AppointmentID, "call_id": callID})
			}
			observability.Emit(observability.TraceNotif(p.AppointmentID), "notif_recordatorio", "confirmed",
				observability.EmitOpts{Phone: phone, Reason: "ivr"})
		}

	case keys != "":
		// ── CANCEL (pressed any key other than 1) ────────────────────────────
		slog.Info("IVR: patient cancelled", "phone", utils.MaskPhone(phone), "keys", keys, "callId", callID)

		pendVal, ok := m.pending.LoadAndDelete(phone)
		if !ok {
			return
		}
		p := pendVal.(*PendingNotification)
		if p.Timer != nil {
			p.Timer.Stop()
		}
		if m.persister != nil {
			_ = m.persister.Resolve(ctx, phone, "cancelled")
		}

		appt, _, err := m.apptSvc.FindBlockByAppointmentID(ctx, p.AppointmentID)
		// N-16: capturar el resultado real; un slot que el paciente cree liberado pero sigue
		// ocupado en SIESA debe quedar marcado como error (no "cancelada") para revisión manual.
		// bug_001: cita no encontrada (appt==nil sin error) también es fallo.
		cancelErr := err
		if err == nil && appt == nil {
			cancelErr = errors.New("appointment not found")
		}
		if err == nil && appt != nil {
			// Cancel ALL patient's appointments for this date
			allAppts, aErr := m.apptSvc.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
			if aErr != nil || len(allAppts) == 0 {
				allAppts = []domain.Appointment{*appt}
			}
			cancelErr = m.apptSvc.CancelBlock(ctx, allAppts, "Cancelada por paciente via llamada IVR", "ivr", "")
		}

		if cancelErr != nil {
			slog.Error("IVR cancel failed", "phone", utils.MaskPhone(phone), "callId", callID, "error", cancelErr)
			m.ivrInternalNote(p.ConversationID, phone, callID,
				fmt.Sprintf("⚠️ *IVR — El paciente oprimio *%s* (cancelar) pero la cancelacion FALLO en el sistema.*\n"+
					"La cita puede seguir ocupando el cupo. Revisar y cancelar manualmente.", keys))
			if m.callTracker != nil {
				_ = m.callTracker.UpdateCallResult(ctx, callID, "completed", "error")
			}
			observability.Emit(observability.TraceNotif(p.AppointmentID), "notif_recordatorio", "error",
				observability.EmitOpts{Phone: phone, Reason: "ivr_cancel_persist"})
		} else {
			m.ivrInternalNote(p.ConversationID, phone, callID,
				"❌ *IVR — Cita CANCELADA por el paciente via llamada telefonica.*\n"+
					fmt.Sprintf("El paciente oprimio *%s* (≠1). La cita fue cancelada en el sistema.", keys))
			if m.callTracker != nil {
				_ = m.callTracker.UpdateCallResult(ctx, callID, "completed", "cancelled")
			}
			if m.tracker != nil {
				m.tracker.LogEvent(ctx, "", phone, "notification_cancelled_ivr",
					map[string]interface{}{"appointment_id": p.AppointmentID, "call_id": callID, "keys": keys})
			}
			observability.Emit(observability.TraceNotif(p.AppointmentID), "notif_recordatorio", "cancelled",
				observability.EmitOpts{Phone: phone, Reason: "ivr"})
		}

	default:
		// ── NO KEY PRESSED (gather timed out after 50 s) ─────────────────────
		// Appointment stays unconfirmed; post-IVR timer continues.
		slog.Info("IVR: no DTMF received (timeout)", "phone", utils.MaskPhone(phone), "callId", callID)

		convID := ""
		if pendVal, ok := m.pending.Load(phone); ok {
			convID = pendVal.(*PendingNotification).ConversationID
		}
		m.ivrInternalNote(convID, phone, callID,
			"⚠️ *IVR — El paciente NO oprimio ninguna tecla durante la llamada.*\n"+
				"La cita queda pendiente de confirmacion. El sistema continuara el flujo de seguimiento.")

		if m.callTracker != nil {
			_ = m.callTracker.UpdateCallResult(ctx, callID, "completed", "no_dtmf") // L11: KPI best-effort
		}
	}
}

// HandleVoiceCallCompleted is called when a voice call completes (via notification webhook).
// If the callId is still in callIDMap at this point (no gather webhook was received),
// it means the call was not answered or went to voicemail. Sends an internal note and cleans up.
func (m *NotificationManager) HandleVoiceCallCompleted(callID string) {
	val, ok := m.callIDMap.LoadAndDelete(callID)
	if !ok {
		return // Already handled by gather result
	}
	phone := val.(string)

	if !m.lockPhone(phone) {
		return
	}
	defer m.unlockPhone(phone)

	slog.Info("IVR: call completed without gather (no answer / voicemail)", "phone", utils.MaskPhone(phone), "callId", callID)

	convID := ""
	if pendVal, ok := m.pending.Load(phone); ok {
		convID = pendVal.(*PendingNotification).ConversationID
	}
	m.ivrInternalNote(convID, phone, callID,
		"📵 *IVR — Llamada no contestada o cayo en buzon de voz.*\n"+
			"La grabacion queda disponible en Bird. La cita sigue pendiente de confirmacion.")

	if m.callTracker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.callTracker.UpdateCallResult(ctx, callID, "completed", "no_answer") // L11: KPI best-effort
	}
}

// ivrInternalNote sends an internal (agent-only) note to Bird Inbox for the voice call.
// If convID is empty, looks up the conversation by phone.
func (m *NotificationManager) ivrInternalNote(convID, phone, callID, note string) {
	if convID == "" {
		convID = m.birdClient.GetCachedConversationID(phone)
	}
	msg := note + fmt.Sprintf("\n\n🎙️ CallID: `%s`\n_(La grabacion esta disponible en la seccion de Grabaciones de Bird)_", callID)
	m.birdClient.SendInternalText(convID, msg)
}

// LoadPendingForTest exposes the pending sync.Map entry for test manipulation.
// Do NOT use in production code.
func (m *NotificationManager) LoadPendingForTest(phone string) (*PendingNotification, bool) {
	val, ok := m.pending.Load(phone)
	if !ok {
		return nil, false
	}
	return val.(*PendingNotification), true
}

// PendingCount returns the number of pending notifications.
func (m *NotificationManager) PendingCount() int {
	count := 0
	m.pending.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// RestorePending loads pending notifications from the database on startup.
// Already-expired entries are processed immediately via handleTimeout.
func (m *NotificationManager) RestorePending(ctx context.Context) {
	if m.persister == nil {
		return
	}

	rows, err := m.persister.FindAll(ctx)
	if err != nil {
		slog.Error("restore pending notifications", "error", err)
		return
	}

	now := time.Now()
	restored := 0
	expired := 0
	var expiredPhones []string // L10: se procesan después del loop, fuera del camino de arranque

	for _, row := range rows {
		notif := &PendingNotification{
			Type:           row.Type,
			Phone:          row.Phone,
			AppointmentID:  row.AppointmentID,
			WaitingListID:  row.WaitingListID,
			BirdMessageID:  row.BirdMessageID,
			ConversationID: row.ConversationID,
			CallID:         row.CallID,
			RetryCount:     row.RetryCount,
			CreatedAt:      row.CreatedAt,
			ExpiresAt:      row.ExpiresAt, // N-19
		}

		// Rebuild callIDMap so in-flight IVR webhooks are correlated after restart
		if row.CallID != "" {
			m.callIDMap.Store(row.CallID, row.Phone)
		}

		if now.After(row.ExpiresAt) {
			// Ya vencida. L10: NO procesar inline (handleTimeout adquiere lock 30s + red si el followup
			// está activo → serializaría el arranque). Guardar y recolectar; se procesa tras el loop.
			m.pending.Store(row.Phone, notif)
			expiredPhones = append(expiredPhones, row.Phone)
			expired++
			continue
		}

		// Still valid — set timer for remaining duration
		remaining := time.Until(row.ExpiresAt)
		phone := row.Phone
		notif.Timer = time.AfterFunc(remaining, func() {
			m.handleTimeout(phone)
		})
		m.pending.Store(row.Phone, notif)
		restored++
	}

	if restored > 0 || expired > 0 {
		slog.Info("pending notifications restored", "restored", restored, "expired", expired)
	}

	// L10: procesar las vencidas en UNA goroutine secuencial (no serializa el arranque ni dispara una
	// estampida de locks/red). Respeta el ctx para parar en el apagado.
	if len(expiredPhones) > 0 {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("restore pending: panic processing expired timeouts", "panic", r)
				}
			}()
			for _, phone := range expiredPhones {
				if ctx.Err() != nil {
					return
				}
				//nolint:contextcheck // handleTimeout no toma ctx por diseño (callback de timer; gestiona su propio lock/timeout acotado).
				m.handleTimeout(phone)
			}
		}()
	}
}

// StartExpirationChecker runs a ticker that checks for expired notifications in the DB.
// This catches any expirations missed by in-memory timers (e.g., after restart + race).
func (m *NotificationManager) StartExpirationChecker(ctx context.Context) {
	if m.persister == nil {
		return
	}

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkExpired(ctx)
		}
	}
}

func (m *NotificationManager) checkExpired(ctx context.Context) {
	rows, err := m.persister.FindExpired(ctx)
	if err != nil {
		slog.Error("check expired notifications", "error", err)
		return
	}

	for _, row := range rows {
		// Only process if still in sync.Map (LoadAndDelete atomicity prevents double-processing)
		if _, ok := m.pending.Load(row.Phone); ok {
			m.handleTimeout(row.Phone)
		} else {
			// Stale DB row — moverla a historial como expirada
			_ = m.persister.Resolve(ctx, row.Phone, "expired")
		}
	}
}

// handleTimeout is called when a patient doesn't respond within 6 hours.
// Uses LoadAndDelete to atomically claim ownership and prevent race with HandleResponse.
func (m *NotificationManager) handleTimeout(phone string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error(
				"PANIC in handleTimeout",
				"phone", utils.MaskPhone(phone),
				"error", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()

	if !m.lockPhone(phone) {
		return
	}
	defer m.unlockPhone(phone)

	val, ok := m.pending.LoadAndDelete(phone)
	if !ok {
		return
	}

	pending := val.(*PendingNotification)

	// N-19: si la entrada fue re-armada con un ExpiresAt futuro (p.ej. MarkIVRSent fijó el timer
	// post-IVR), y este disparo viene de un trigger viejo (checkExpired sobre un snapshot de BD
	// rancio, o un timer anterior), NO procesarla como vencida: re-insertarla y salir.
	if !pending.ExpiresAt.IsZero() && time.Now().Before(pending.ExpiresAt) {
		m.pending.Store(phone, pending)
		slog.Debug("handleTimeout: entry re-armed, not expired yet — skipping", "phone", utils.MaskPhone(phone))
		return
	}

	// Cleanup callIDMap
	if pending.CallID != "" {
		m.callIDMap.Delete(pending.CallID)
	}

	// Mover a historial como expirada (sin respuesta). Si hay reintento/escalación, se re-inserta
	// una nueva pendiente más abajo.
	if m.persister != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.persister.Resolve(ctx, phone, "expired")
	}

	// Resolve conversationID if still empty (template sends don't always return it).
	// By timeout time, outbound webhooks or conversation.created may have populated the cache.
	if pending.ConversationID == "" {
		if cached := m.birdClient.GetCachedConversationID(phone); cached != "" {
			pending.ConversationID = cached
		} else if looked, err := m.birdClient.LookupConversationByPhone(phone); err == nil && looked != "" {
			pending.ConversationID = looked
		}
	}

	// Log timeout event
	if m.tracker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m.tracker.LogEvent(ctx, "", phone, "notification_timeout",
			map[string]interface{}{
				"type":            pending.Type,
				"appointment_id":  pending.AppointmentID,
				"retry":           pending.RetryCount,
				"conversation_id": pending.ConversationID,
			})
	}

	switch pending.Type {
	case "confirmation":
		m.handleConfirmationTimeout(pending)
	case "reschedule":
		m.handleConfirmationTimeout(pending) // Same behavior
	case "cancellation":
		// No timeout action — patient didn't respond to confirmation step
	case "waiting_list":
		if m.waitingListRepo != nil {
			m.handleWaitingListTimeout(pending)
		}
	}
}

// normalizePostback maps Bird template postbacks to internal actions.
func normalizePostback(payload string) string {
	switch payload {
	case "confirm":
		return "confirm"
	case "cancelar": // Confirmation flow uses "cancelar"
		return "cancel"
	case "cancel": // Reschedule flow uses "cancel"
		return "cancel"
	case "understood":
		return "acknowledge"
	case "reprogramar": // Self-service reschedule button
		return "reschedule"
	case "reschedule": // Legacy cancellation flow button
		return "reschedule"
	case "wl_schedule":
		return "schedule"
	case "wl_decline":
		return "decline"
	default:
		return payload
	}
}

// responseStatus mapea la respuesta del paciente (payload ya normalizado por normalizePostback) al
// estado con el que se archiva la notificación en notification_history.
// responseStatus traduce la respuesta del paciente al estado que se archiva en notification_history.
// L8: las acciones que solo INICIAN un flujo asíncrono (reschedule/cancel/schedule arrancan una
// sesión en el state machine que puede no completarse) se archivan como "*_requested" — el historial
// es evidencia de la RESPUESTA a la notificación, no del desenlace de la cita (que vive en el ciclo
// de la cita). Antes se archivaban como 'rescheduled'/'cancelled'/'wl_scheduled', sobre-declarando el
// resultado por el solo hecho de pulsar el botón. confirm/acknowledge/decline sí son terminales.
func responseStatus(normalized string) string {
	switch strings.ToLower(normalized) {
	case "confirm":
		return "confirmed"
	case "cancel":
		return "cancel_requested"
	case "reschedule":
		return "reschedule_requested"
	case "schedule":
		return "schedule_requested"
	case "decline":
		return "declined"
	case "acknowledge":
		return "acknowledged"
	default:
		return "responded"
	}
}

// escalateNotifToAgent escala al agente cuando el paciente envió texto libre repetidamente
// durante el flujo de confirmación proactiva. Crea una sesión escalada en estado NOTIF_PENDING
// para que el agente tenga comandos disponibles (/bot resume NOTIF_PENDING confirm/reschedule/cancel).
func (m *NotificationManager) escalateNotifToAgent(p *PendingNotification, incomingConvID string) {
	convID := incomingConvID
	if convID == "" {
		convID = p.ConversationID
	}
	if convID == "" {
		convID = m.birdClient.GetCachedConversationID(p.Phone)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Obtener datos de la cita para el mensaje al agente
	apptDate := ""
	apptTime := ""
	cupsName := ""
	patientName := ""
	if appt, _, err := m.apptSvc.FindBlockByAppointmentID(ctx, p.AppointmentID); err == nil && appt != nil {
		apptDate = utils.FormatFriendlyDate(appt.Date)
		apptTime = services.FormatTimeSlot(appt.TimeSlot)
		patientName = appt.PatientName
		if len(appt.Procedures) > 0 {
			cupsName = appt.Procedures[0].CupName
		}
	}

	// Crear sesión escalada con contexto de la notificación
	var sessID string
	if m.sessionRepo != nil {
		sess := &session.Session{
			ID:             uuid.New().String(),
			PhoneNumber:    p.Phone,
			CurrentState:   sm.StateNotifPending,
			ConversationID: convID,
			Status:         session.StatusEscalated,
			ExpiresAt:      time.Now().Add(24 * time.Hour),
		}
		sessCtx := map[string]string{
			"notif_appointment_id": p.AppointmentID,
			"notif_type":           p.Type,
			"notif_appt_date":      apptDate,
			"notif_appt_time":      apptTime,
			"notif_cups_name":      cupsName,
			"notif_conv_id":        convID,
			"notif_bird_msg_id":    p.BirdMessageID,
			"patient_name":         patientName,
			"pre_escalation_state": sm.StateNotifPending,
		}
		if err := m.sessionRepo.Create(ctx, sess); err != nil {
			slog.Error("escalateNotifToAgent: create session", "error", err, "phone", utils.MaskPhone(p.Phone))
		} else {
			sessID = sess.ID
			if err := m.sessionRepo.SetContextBatch(ctx, sess.ID, sessCtx); err != nil {
				slog.Error("escalateNotifToAgent: set context", "error", err, "phone", utils.MaskPhone(p.Phone))
			}
			// Sesión proactiva: contarla en total_sessions/sesiones-por-hora como las demás.
			if m.tracker != nil {
				m.tracker.LogEvent(ctx, sess.ID, p.Phone, "session_started", map[string]interface{}{"proactive": true})
			}
		}
	}

	// Nota interna + mensaje al paciente + asignación de agente
	note := fmt.Sprintf("Paciente envio texto libre repetidamente durante confirmacion de cita.\n"+
		"Cita ID: %s | Fecha: %s %s | Procedimiento: %s\n"+
		"Requiere gestion manual del agente.",
		p.AppointmentID, apptDate, apptTime, cupsName)

	slog.Info(
		"escalateNotifToAgent",
		"phone", utils.MaskPhone(p.Phone),
		"conv_id", convID,
		"appointment_id", p.AppointmentID,
		"team_fallback", m.cfg.BirdTeamFallback,
	)

	commands := fmt.Sprintf(
		"Comandos disponibles:\n" +
			"  /bot resume NOTIF_PENDING confirm — Confirmar la cita\n" +
			"  /bot resume NOTIF_PENDING reschedule — Reprogramar la cita\n" +
			"  /bot resume NOTIF_PENDING cancel — Cancelar la cita\n" +
			"  /bot cerrar — Cerrar la conversacion",
	)

	// Send patient message first — this populates the convID cache via Channels API if empty
	m.birdClient.SendText(p.Phone, convID,
		"Te voy a conectar con un agente para gestionar tu cita. Un momento por favor...")

	// Pick up convID from cache (SendText via Channels API caches it)
	if convID == "" {
		convID = m.birdClient.GetCachedConversationID(p.Phone)
	}
	if convID == "" {
		slog.Error("escalateNotifToAgent: no conversation ID — cannot assign agent", "phone", utils.MaskPhone(p.Phone))
		return
	}

	// Internal notes visible only in Bird Inbox (agent context)
	m.birdClient.SendInternalText(convID, note)
	m.birdClient.SendInternalText(convID, commands)

	if _, _, err := m.birdClient.EscalateToAgent(ctx, convID, p.Phone,
		m.cfg.BirdTeamFallback, "Call Center",
		patientName, m.cfg.BirdTeamFallback); err != nil {
		slog.Error(
			"escalateNotifToAgent: EscalateToAgent failed",
			"phone", utils.MaskPhone(p.Phone),
			"conv_id", convID,
			"team", m.cfg.BirdTeamFallback,
			"error", err,
		)
		return
	}

	// Escalación efectiva: registrar el evento (cuenta en 'Escaladas' y en el KPI de paso from_state) y
	// la fila en la tabla escalations (SLA por escalación, #11). from_state = NOTIF_PENDING.
	if sessID != "" {
		if m.tracker != nil {
			m.tracker.LogEvent(ctx, sessID, p.Phone, "escalated_to_agent", map[string]interface{}{
				"from_state": sm.StateNotifPending,
				"agent":      "Call Center",
			})
		}
		if m.escalations != nil {
			if err := m.escalations.Create(ctx, sessID, p.Phone, sm.StateNotifPending, m.cfg.BirdTeamFallback, "", "Call Center"); err != nil {
				slog.Warn("escalateNotifToAgent: record escalation", "error", err, "phone", utils.MaskPhone(p.Phone))
			}
		}
	}

	slog.Info("notif escalated to agent (invalid inputs)", "phone", utils.MaskPhone(p.Phone), "appointment_id", p.AppointmentID)
}

// safeHours returns v if > 0, otherwise fallback. Guards against zero-value configs in tests.
func safeHours(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// safeMinutes returns v if > 0, otherwise fallback. Guards against zero-value configs in tests.
func safeMinutes(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
