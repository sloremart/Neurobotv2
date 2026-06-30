package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/tracking"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

const (
	defaultWorkers        = 10
	defaultQueueSize      = 100
	maxOverflowGoroutines = 20
	dedupTTL              = 5 * time.Minute
	dedupCleanup          = 2 * time.Minute
	phoneLockTimeout      = 30 * time.Second
	processMsgTimeout     = 2 * time.Minute // BUG-004: cota por mensaje del ctx desacoplado del apagado
	agentCmdQueueSize     = 50
	maxDedupEntries       = 100000 // safety cap; fail-open if exceeded (allow potential dups)

	// Re-replay del WAL (M7): recupera mensajes que quedaron 'pending' (p.ej. descartados por
	// backpressure) sin esperar al reinicio. El umbral (> dedupTTL) evita reprocesar mensajes en
	// vuelo y garantiza que el claim de dedup del intento previo ya expiró.
	staleReplayMinutes  = 10
	staleReplayInterval = 5 * time.Minute
)

// StalePendingSource entrega mensajes inbound que quedaron 'pending' hace más de `minutes` minutos.
// La implementación real (adapter sobre el inbox repo) parsea el raw_body a InboundMessage.
type StalePendingSource interface {
	PendingOlderThan(ctx context.Context, minutes int) ([]bird.InboundMessage, error)
}

// SessionManagement abstracts session manager operations for testability.
type SessionManagement interface {
	PhoneMutex() *session.PhoneMutex
	FindOrCreate(ctx context.Context, phone string) (*session.Session, bool, error)
	RenewTimeout(ctx context.Context, sess *session.Session) error
	TouchPatientActivity(ctx context.Context, sess *session.Session) error
	TouchAgentActivity(ctx context.Context, phone string) error
	SaveState(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error
	ClearAllContext(ctx context.Context, sess *session.Session) error
	Escalate(ctx context.Context, sess *session.Session, teamID string) error
	ResumeFromEscalation(ctx context.Context, sess *session.Session, targetState string) error
	Complete(ctx context.Context, sess *session.Session) error
	UpdateConversationID(ctx context.Context, phone, conversationID string) error
	SetContext(ctx context.Context, sess *session.Session, key, value string) error
}

// MessageSender abstracts outbound message sending and conversation cache for testability.
type MessageSender interface {
	SendText(phone, conversationID, text string) (string, error)
	SendButtons(phone, conversationID, text string, buttons []bird.Button) (string, error)
	SendList(phone, conversationID, body, title string, sections []bird.ListSection) (string, error)
	SendInternalText(conversationID, text string) (string, error)
	UnassignFeedItem(conversationID string, closed bool) error
	CloseFeedItems(conversationID string) error
	GetCachedConversationID(phone string) string
	LookupConversationByPhone(phone string) (string, error)
}

// MessageProcessor abstracts state machine processing for testability.
type MessageProcessor interface {
	Process(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error)
}

// OCRAnalyzer abstracts OCR text analysis for testability.
type OCRAnalyzer interface {
	AnalyzeText(ctx context.Context, description string) (*services.OCRResult, error)
}

// NotificationResponder processes responses to proactive notification templates.
type NotificationResponder interface {
	HandleResponse(phone, payload, conversationID string)
	// HandleNotifPendingCommand processes a /bot resume NOTIF_PENDING command from an agent.
	// Used when the pending notification was already removed from memory (escalated path).
	HandleNotifPendingCommand(phone, action, convID, appointmentID, notifType string)
}

// InboxMarker marks messages as processed in the inbox (WAL pattern).
type InboxMarker interface {
	MarkDone(ctx context.Context, id string) error
}

type MessageWorkerPool struct {
	queue          chan bird.InboundMessage
	agentCmds      chan AgentCommand
	recentMessages sync.Map     // messageID -> time.Time (dedup)
	dedupCount     atomic.Int64 // approximate count for safety cap
	workers        int
	activeOverflow atomic.Int32
	wg             sync.WaitGroup  // tracks all goroutines for graceful shutdown
	ctx            context.Context // stored from Start() for overflow goroutines

	// #5 (auditoría): cierra el race wg.Add vs wg.Wait. Stop() fija closing=true bajo closeMu.Lock()
	// ANTES de wg.Wait(); los enqueuers registran su goroutine de overflow vía tryAddOverflow(), que
	// toma closeMu.RLock() y se niega a hacer wg.Add si ya se está cerrando. Así ningún Add puede
	// colarse después de que Wait empiece (evita el panic "WaitGroup is reused").
	closeMu sync.RWMutex
	closing bool

	botEnabled bool // BOT_ENABLED=false → escala directo sin tocar SIESA/Antares

	// Dependencias (inyectadas después de creación)
	sessionManager  SessionManagement
	birdClient      MessageSender
	machine         MessageProcessor
	tracker         *tracking.EventTracker
	ocrService      OCRAnalyzer
	inboxRepo       InboxMarker // WAL crash recovery (optional)
	notifyResponder NotificationResponder
}

func NewMessageWorkerPool(workers, queueSize int) *MessageWorkerPool {
	if workers <= 0 {
		workers = defaultWorkers
	}
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	return &MessageWorkerPool{
		queue:     make(chan bird.InboundMessage, queueSize),
		agentCmds: make(chan AgentCommand, agentCmdQueueSize),
		workers:   workers,
		ctx:       context.Background(),
	}
}

// SetDependencies inyecta las dependencias necesarias para procesar mensajes
func (p *MessageWorkerPool) SetDependencies(sm SessionManagement, bc MessageSender, m MessageProcessor) {
	p.sessionManager = sm
	p.birdClient = bc
	p.machine = m
}

// SetTracker sets the event tracker for persisting events to the database.
func (p *MessageWorkerPool) SetTracker(t *tracking.EventTracker) {
	p.tracker = t
}

// SetOCRService sets the OCR service for /bot orden command processing.
func (p *MessageWorkerPool) SetOCRService(svc OCRAnalyzer) {
	p.ocrService = svc
}

// SetInboxRepo sets the inbox repo for WAL crash recovery.
func (p *MessageWorkerPool) SetInboxRepo(repo InboxMarker) {
	p.inboxRepo = repo
}

// SetNotifyResponder injects the notification responder for NOTIF_PENDING agent commands.
func (p *MessageWorkerPool) SetNotifyResponder(nr NotificationResponder) {
	p.notifyResponder = nr
}

// SetBotEnabled activa o desactiva la autogestión del bot.
// Con false, el primer mensaje de cualquier sesión activa escala directo a un agente
// sin consultar SIESA, Antares ni ejecutar ningún flujo del bot.
func (p *MessageWorkerPool) SetBotEnabled(enabled bool) {
	p.botEnabled = enabled
}

// QueueStats returns the current queue size and capacity.
func (p *MessageWorkerPool) QueueStats() (size, capacity int) {
	return len(p.queue), cap(p.queue)
}

// Start inicia los worker goroutines
func (p *MessageWorkerPool) Start(ctx context.Context) {
	p.ctx = ctx
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func(id int) {
			defer p.wg.Done()
			p.worker(ctx, id)
		}(i)
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.startDedupCleanup(ctx)
	}()
	slog.Info("worker pool started", "workers", p.workers, "queue_size", cap(p.queue))
}

// Stop waits for all workers and overflow goroutines to finish,
// then drains remaining queued messages (up to 15s) before returning.
func (p *MessageWorkerPool) Stop() {
	// #5: marcar el cierre ANTES de Wait, bajo Lock. tryAddOverflow toma RLock, así que cualquier
	// enqueuer en curso termina su chequeo+Add antes de que este Lock retorne; tras esto, los nuevos
	// enqueuers ven closing=true y NO hacen wg.Add → no hay Add concurrente con el Wait de abajo.
	p.closeMu.Lock()
	p.closing = true
	p.closeMu.Unlock()

	p.wg.Wait()

	// Drain remaining queued messages with a deadline
	remaining := len(p.queue)
	if remaining == 0 {
		return
	}

	slog.Info("draining queued messages before shutdown", "count", remaining)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer drainCancel()

	drained := 0
	for {
		select {
		case msg := <-p.queue:
			// safeProcess (no processMessage directo): un panic durante el drain de apagado no
			// debe crashear el proceso; los mensajes se replayan vía WAL al reiniciar (N-35).
			p.safeProcess(drainCtx, msg)
			drained++
		case <-drainCtx.Done():
			dropped := len(p.queue)
			if dropped > 0 {
				slog.Warn("shutdown drain timeout, messages remaining", "dropped", dropped)
			}
			return
		default:
			// Queue is empty
			slog.Info("shutdown drain complete", "drained", drained)
			return
		}
	}
}

// Enqueue agrega un mensaje al queue. Retorna false si es duplicado o si se excede backpressure.
// tryAddOverflow registra una goroutine de overflow en el WaitGroup salvo que el pool esté cerrando
// (#5). Devuelve false si Stop() ya marcó el cierre; en ese caso el caller NO debe lanzar la goroutine
// (el mensaje se descarta y se replaya por WAL al reiniciar). Hace activeOverflow.Add(1)+wg.Add(1)
// atómicamente respecto al cierre, así que nunca ocurre un wg.Add después de que Stop empiece a esperar.
func (p *MessageWorkerPool) tryAddOverflow() bool {
	p.closeMu.RLock()
	defer p.closeMu.RUnlock()
	if p.closing {
		return false
	}
	p.activeOverflow.Add(1)
	p.wg.Add(1)
	return true
}

func (p *MessageWorkerPool) Enqueue(msg bird.InboundMessage) bool {
	// 1. Dedup check (fail-open if map is too large — allow potential dups rather than leak memory).
	// Se reclama ANTES de encolar (claim-then-enqueue) para que dos entregas duplicadas concurrentes
	// no se procesen ambas. `claimed` permite REVERTIR el claim si el mensaje se descarta (M7).
	claimed := false
	if p.dedupCount.Load() < maxDedupEntries {
		if _, exists := p.recentMessages.LoadOrStore(msg.ID, time.Now()); exists {
			slog.Debug("duplicate message ignored", "id", msg.ID, "phone", utils.MaskPhone(msg.Phone))
			return false
		}
		p.dedupCount.Add(1)
		claimed = true
	} else {
		slog.Warn("dedup map at capacity, skipping dedup check", "id", msg.ID, "cap", maxDedupEntries)
	}

	// 2. Intentar encolar en el channel (no bloqueante)
	select {
	case p.queue <- msg:
		return true
	default:
		// 3. Channel lleno — overflow con límite
		if p.activeOverflow.Load() >= int32(maxOverflowGoroutines) {
			// M7: el mensaje se descarta SIN procesarse. Liberar el claim de dedup para que el
			// re-replay del WAL (o un reintento) pueda re-encolarlo; si no, quedaría "visto" 5 min
			// y el mensaje se perdería hasta el próximo reinicio.
			if claimed {
				p.recentMessages.Delete(msg.ID)
				p.dedupCount.Add(-1)
			}
			slog.Error("backpressure: overflow limit reached, dropping message",
				"id", msg.ID,
				"queue_size", len(p.queue),
				"overflow_active", p.activeOverflow.Load())
			return false
		}
		// #5: registrar la goroutine respetando el cierre. Si el pool ya está cerrando, se descarta el
		// mensaje (se replaya por WAL al reiniciar) y se libera el claim de dedup para no perderlo.
		if !p.tryAddOverflow() {
			if claimed {
				p.recentMessages.Delete(msg.ID)
				p.dedupCount.Add(-1)
			}
			slog.Warn("pool closing, dropping overflow message", "id", msg.ID)
			return false
		}
		go func() {
			defer p.wg.Done()
			defer p.activeOverflow.Add(-1)
			slog.Warn("processing in overflow goroutine", "id", msg.ID)
			ctx, cancel := context.WithTimeout(p.ctx, 2*time.Minute) // L15: ligado al app-ctx → el shutdown lo cancela
			defer cancel()
			p.safeProcess(ctx, msg)
		}()
		return true
	}
}

// EnqueueAgentCommand enqueues a /bot command from a human agent.
func (p *MessageWorkerPool) EnqueueAgentCommand(cmd AgentCommand) {
	select {
	case p.agentCmds <- cmd:
		slog.Info("agent command enqueued", "phone", utils.MaskPhone(cmd.Phone), "action", cmd.Action)
	default:
		slog.Warn("agent command queue full, dropping", "phone", utils.MaskPhone(cmd.Phone), "action", cmd.Action)
	}
}

func (p *MessageWorkerPool) worker(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-p.queue:
			p.safeProcess(ctx, msg)
		case cmd := <-p.agentCmds:
			p.safeProcessCmd(ctx, cmd)
		}
	}
}

// safeProcess wraps processMessage with panic recovery so a single message
// panic doesn't kill the entire worker pool / bot process.
func (p *MessageWorkerPool) safeProcess(ctx context.Context, msg bird.InboundMessage) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error(
				"PANIC processing message",
				"phone", utils.MaskPhone(msg.Phone),
				"message_id", msg.ID,
				"error", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	p.processMessage(ctx, msg)
}

// safeProcessCmd wraps processAgentCommand with panic recovery.
func (p *MessageWorkerPool) safeProcessCmd(ctx context.Context, cmd AgentCommand) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error(
				"PANIC processing agent command",
				"phone", utils.MaskPhone(cmd.Phone),
				"action", cmd.Action,
				"error", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	p.processAgentCommand(ctx, cmd)
}

// recoverLog logs panics from short-lived background goroutines.
func recoverLog(name string) {
	if r := recover(); r != nil {
		slog.Error(
			"PANIC in background goroutine",
			"goroutine", name,
			"error", fmt.Sprintf("%v", r),
			"stack", string(debug.Stack()),
		)
	}
}

func (p *MessageWorkerPool) processMessage(parentCtx context.Context, msg bird.InboundMessage) {
	// BUG-004: el procesamiento NO debe abortarse si el app-ctx se cancela en pleno apagado/redeploy.
	// Antes, un mensaje en vuelo veía sus escrituras (SaveState/MarkDone/log) abortadas con
	// "context canceled" → el estado del paciente quedaba sin persistir (víctima de reinicio). Se
	// desacopla con WithoutCancel + timeout propio: el worker deja de tomar mensajes NUEVOS al apagar
	// (worker() <-ctx.Done()), pero el que YA empezó completa su persistencia en la ventana de gracia
	// (Stop espera al wg; main da 20s). Aplica a todas las rutas (worker, overflow y drain).
	parentCtx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), processMsgTimeout)
	defer cancel()

	// WAL: mark message as done after processing (crash recovery)
	if p.inboxRepo != nil && msg.ID != "" && !strings.HasPrefix(msg.ID, "virtual-") && !strings.HasPrefix(msg.ID, "agent-") {
		defer func() {
			if err := p.inboxRepo.MarkDone(parentCtx, msg.ID); err != nil {
				slog.Error("inbox mark done failed", "id", msg.ID, "error", err)
			}
		}()
	}

	// 1. Crear contexto con timeout de 30s para adquisición del lock
	lockCtx, lockCancel := context.WithTimeout(parentCtx, phoneLockTimeout)
	defer lockCancel()

	// 2. Adquirir lock por teléfono (serializa mensajes del mismo usuario)
	if err := p.sessionManager.PhoneMutex().Lock(lockCtx, msg.Phone); err != nil {
		slog.Warn("phone lock timeout", "phone", utils.MaskPhone(msg.Phone), "error", err)
		observability.Emit("infra:phone_lock_timeout", "infra", "phone_lock_timeout",
			observability.EmitOpts{Phone: msg.Phone, Reason: "message"})
		// Notify patient so they know their message was received but delayed
		p.birdClient.SendText(msg.Phone, "",
			"Tu mensaje anterior está siendo procesado. Por favor espera un momento antes de enviar otro mensaje.")
		return
	}
	defer p.sessionManager.PhoneMutex().Unlock(msg.Phone)

	// 3. Cargar/crear sesión
	sess, isNew, err := p.sessionManager.FindOrCreate(parentCtx, msg.Phone)
	if err != nil {
		slog.Error("session error", "phone", utils.MaskPhone(msg.Phone), "error", err)
		// Send error message via Channels API so patient is not left in silence
		p.birdClient.SendText(msg.Phone, "",
			"Lo sentimos, estamos experimentando dificultades técnicas. Por favor intenta nuevamente en unos minutos.")
		return
	}

	if isNew {
		slog.Info("new session created", "session_id", sess.ID, "phone", utils.MaskPhone(msg.Phone))
		if p.tracker != nil {
			p.tracker.LogEvent(parentCtx, sess.ID, msg.Phone, "session_started", nil)
		}
	}

	// 3b. Store conversationId from Bird webhook (needed for escalation)
	if msg.ConversationID != "" && sess.ConversationID != msg.ConversationID {
		slog.Debug(
			"conversation_id_updated",
			"session_id", sess.ID,
			"phone", utils.MaskPhone(msg.Phone),
			"old", sess.ConversationID,
			"new", msg.ConversationID,
		)
		sess.ConversationID = msg.ConversationID
	}

	// 3c. Validate conversationID: cache check → API lookup if needed.
	// After bot restart the in-memory cache is empty, and the session may hold
	// a stale ID from a closed Bird conversation. We must verify/refresh it.
	if cached := p.birdClient.GetCachedConversationID(msg.Phone); cached != "" {
		// Cache is authoritative (populated by conversation.created webhook or prior API lookup)
		if cached != sess.ConversationID {
			slog.Debug(
				"conversation_id_updated_from_cache",
				"session_id", sess.ID,
				"phone", utils.MaskPhone(msg.Phone),
				"old", sess.ConversationID,
				"new", cached,
			)
			sess.ConversationID = cached
		}
	} else {
		// Cache empty (e.g., after restart) — verify via API lookup.
		// LookupConversationByPhone populates the cache on success,
		// so subsequent messages from this phone won't trigger another lookup.
		if looked, err := p.birdClient.LookupConversationByPhone(msg.Phone); err != nil {
			slog.Warn(
				"conversation_lookup_failed",
				"session_id", sess.ID,
				"phone", utils.MaskPhone(msg.Phone),
				"error", err,
			)
		} else if looked != "" {
			if looked != sess.ConversationID {
				slog.Info(
					"conversation_id_refreshed",
					"session_id", sess.ID,
					"phone", utils.MaskPhone(msg.Phone),
					"old", sess.ConversationID,
					"new", looked,
				)
			}
			sess.ConversationID = looked
		} else if sess.ConversationID != "" && msg.ConversationID == "" {
			// Lookup returned empty but session has a conversationID. Don't clear it —
			// the conversation may still be usable even if not indexed yet.
			// trySendToConversation handles 422 (not active) with self-heal.
			slog.Warn(
				"conversation_id_lookup_miss_kept",
				"session_id", sess.ID,
				"phone", utils.MaskPhone(msg.Phone),
				"kept", sess.ConversationID,
			)
		}
	}

	// Log warning if conversation_id is still empty after all resolution attempts
	if sess.ConversationID == "" && !isNew {
		slog.Warn(
			"conversation_id_still_empty",
			"session_id", sess.ID,
			"phone", utils.MaskPhone(msg.Phone),
			"state", sess.CurrentState,
			"msg_conv_id", msg.ConversationID,
		)
	}

	slog.Debug(
		"session_state",
		"session_id", sess.ID,
		"phone", utils.MaskPhone(msg.Phone),
		"state", sess.CurrentState,
		"status", sess.Status,
		"retry_count", sess.RetryCount,
		"conversation_id", sess.ConversationID,
		"is_new", isNew,
	)

	// 4. Kill switch: BOT_ENABLED=false → escalar inmediatamente sin pasar por la state machine.
	// Solo actúa sobre sesiones activas; sesiones ya escaladas siguen su flujo normal.
	if !p.botEnabled && sess.Status == session.StatusActive {
		sess.CurrentState = statemachine.StateEscalateToAgent
		sess.Context["_pre_auto_state"] = "BOT_DISABLED"
		sess.Context["escalation_note"] = "Bot desactivado — atender al paciente directamente desde el inicio."
		slog.Info("bot_disabled_escalating", "session_id", sess.ID, "phone", utils.MaskPhone(msg.Phone))
	}

	// 4b. If session is escalated, log but DO NOT process through state machine
	// Check BEFORE RenewTimeout to avoid race with agent ResumeFromEscalation
	if sess.Status == session.StatusEscalated {
		slog.Info(
			"msg during escalation (ignored by bot)",
			"session_id", sess.ID,
			"phone", utils.MaskPhone(msg.Phone),
			"text", msg.Text,
		)
		// Sellar actividad del paciente: mantiene viva la sesión escalada mientras el paciente
		// escribe y reinicia la ventana de recordatorios al agente (el paciente sí está respondiendo).
		if err := p.sessionManager.TouchPatientActivity(parentCtx, sess); err != nil {
			slog.Error("touch patient activity (escalated) error", "phone", utils.MaskPhone(msg.Phone), "session_id", sess.ID, "error", err)
		}
		if p.tracker != nil {
			p.tracker.LogEvent(parentCtx, sess.ID, msg.Phone, "msg_during_escalation", map[string]interface{}{
				"text": msg.Text,
				"type": msg.MessageType,
			})
		}
		return
	}

	// 5b. Renew timeout + seal patient activity for active sessions
	if err := p.sessionManager.TouchPatientActivity(parentCtx, sess); err != nil {
		slog.Error("touch patient activity error", "phone", utils.MaskPhone(msg.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID, "error", err)
	}

	// 5c. Reset inactivity reminders when patient sends a message
	if r := sess.GetContext("inactivity_reminders"); r != "" && r != "0" {
		_ = p.sessionManager.SetContext(parentCtx, sess, "inactivity_reminders", "0")
	}

	// 6. Log mensaje recibido
	slog.Info(
		"processing message",
		"session_id", sess.ID,
		"phone", utils.MaskPhone(msg.Phone),
		"state", sess.CurrentState,
		"type", msg.MessageType,
		"text", msg.Text,
	)

	// 7. Ejecutar state machine
	prevState := sess.CurrentState
	result, err := p.machine.Process(parentCtx, sess, msg)
	if err != nil {
		errMsg := "Lo sentimos, ocurrió un error. Por favor intenta de nuevo."
		eventType := "state_machine_error"
		if errors.Is(err, context.DeadlineExceeded) {
			errMsg = "Tardó demasiado procesar tu solicitud. Por favor intenta de nuevo."
			eventType = "state_machine_timeout"
		}
		slog.Error(
			eventType,
			"phone", utils.MaskPhone(msg.Phone),
			"session_id", sess.ID,
			"conversation_id", sess.ConversationID,
			"state", sess.CurrentState,
			"error", err,
		)
		p.birdClient.SendText(msg.Phone, sess.ConversationID, errMsg)
		if p.tracker != nil {
			p.tracker.LogEvent(parentCtx, sess.ID, msg.Phone, eventType, map[string]interface{}{
				"error": err.Error(),
				"state": sess.CurrentState,
			})
		}
		return
	}

	slog.Debug(
		"state_transition",
		"session_id", sess.ID,
		"phone", utils.MaskPhone(msg.Phone),
		"from", prevState,
		"to", result.NextState,
		"messages_count", len(result.Messages),
		"events_count", len(result.Events),
		"retry_count", sess.RetryCount,
	)

	// 8. Enviar mensajes de respuesta + persistir
	p.sendAndSave(parentCtx, sess, msg.Phone, result)
}

// processAgentCommand handles /bot commands from human agents via outbound webhook.
func (p *MessageWorkerPool) processAgentCommand(parentCtx context.Context, cmd AgentCommand) {
	// BUG-004: desacoplar del ctx de apagado (igual que processMessage) para no abortar a medias el
	// comando del agente (resume/close/etc.) si el redeploy cancela el app-ctx en vuelo.
	parentCtx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), processMsgTimeout)
	defer cancel()

	// 1. Acquire phone lock
	lockCtx, lockCancel := context.WithTimeout(parentCtx, phoneLockTimeout)
	defer lockCancel()

	if err := p.sessionManager.PhoneMutex().Lock(lockCtx, cmd.Phone); err != nil {
		slog.Warn("phone lock timeout (agent cmd)", "phone", utils.MaskPhone(cmd.Phone), "error", err)
		observability.Emit("infra:phone_lock_timeout", "infra", "phone_lock_timeout",
			observability.EmitOpts{Phone: cmd.Phone, Reason: "agent_cmd"})
		return
	}
	defer p.sessionManager.PhoneMutex().Unlock(cmd.Phone)

	// 2. Find escalated session
	sess, _, err := p.sessionManager.FindOrCreate(parentCtx, cmd.Phone)
	if err != nil {
		slog.Error("session error (agent cmd)", "phone", utils.MaskPhone(cmd.Phone), "error", err)
		return
	}
	if sess == nil || sess.Status != session.StatusEscalated {
		slog.Warn("agent command but no escalated session", "phone", utils.MaskPhone(cmd.Phone), "action", cmd.Action)
		return
	}

	// 3. Renovar timeout (mantiene sesión escalada viva mientras agente interactúa)
	if err := p.sessionManager.RenewTimeout(parentCtx, sess); err != nil {
		slog.Error("renew timeout error (agent cmd)", "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID, "error", err)
	}

	slog.Info(
		"processing agent command",
		"session_id", sess.ID,
		"phone", utils.MaskPhone(cmd.Phone),
		"action", cmd.Action,
		"state", cmd.State,
		"data", cmd.Data,
	)

	switch cmd.Action {
	case "resume":
		p.handleAgentResume(parentCtx, sess, cmd)

	case "reset":
		p.handleAgentReset(parentCtx, sess, cmd)

	case "close":
		p.handleAgentClose(parentCtx, sess, cmd)

	case "info":
		p.handleAgentInfo(parentCtx, sess)

	case "orden":
		p.handleAgentOrder(parentCtx, sess, cmd)

	case "cups":
		p.handleAgentCups(parentCtx, sess, cmd)
	}
}

// handleAgentResume resumes a session from escalation at a specific state, optionally with corrected data.
func (p *MessageWorkerPool) handleAgentResume(ctx context.Context, sess *session.Session, cmd AgentCommand) {
	// Special case: NOTIF_PENDING — agent resolves a confirmation/reschedule/cancel directly.
	// The pending notification was already removed from memory when escalation happened,
	// so we must use the session context instead of HandleResponse (which needs in-memory pending).
	if cmd.State == statemachine.StateNotifPending && cmd.Data != "" && p.notifyResponder != nil {
		action := strings.TrimSpace(cmd.Data)
		if action == "confirm" || action == "reschedule" || action == "cancel" {
			appointmentID := sess.GetContext("notif_appointment_id")
			notifType := sess.GetContext("notif_type")
			convID := sess.ConversationID
			if convID == "" {
				convID = sess.GetContext("notif_conv_id")
			}
			// N-26: loguear los errores descartados de Complete/UnassignFeedItem en lugar de tragarlos.
			if err := p.sessionManager.Complete(ctx, sess); err != nil {
				slog.Error("notif_pending: complete session failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", convID)
			}
			if err := p.birdClient.UnassignFeedItem(convID, false); err != nil {
				slog.Error("notif_pending: unassign feed item failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "conversation_id", convID)
			}
			p.notifyResponder.HandleNotifPendingCommand(sess.PhoneNumber, action, convID, appointmentID, notifType)
			slog.Info(
				"agent resolved notif_pending",
				"phone", utils.MaskPhone(cmd.Phone),
				"action", action,
				"session_id", sess.ID,
				"appointment_id", appointmentID,
			)
			return
		}
	}

	// Determine target state
	targetState := cmd.State
	if targetState == "" {
		targetState = sess.GetContext("pre_escalation_state")
	}
	if targetState == "" {
		targetState = statemachine.StateGreeting
	}

	// Resume session in DB
	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, targetState); err != nil {
		slog.Error("resume from escalation failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		return
	}

	// Unassign agent from Bird Inbox (bot takes over, keep item open)
	p.birdClient.UnassignFeedItem(sess.ConversationID, false)

	if cmd.Data != "" {
		// Agent provided corrected data — inject as virtual message
		p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, "Hemos retomado tu atención. Procesando tu información...")

		virtualMsg := bird.InboundMessage{
			ID:          fmt.Sprintf("agent-cmd-%s-%d", cmd.Phone, time.Now().UnixNano()),
			Phone:       sess.PhoneNumber,
			Text:        cmd.Data,
			MessageType: "text",
			ReceivedAt:  time.Now(),
		}

		result, err := p.machine.Process(ctx, sess, virtualMsg)
		if err != nil {
			slog.Error("process virtual message failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
			return
		}
		p.sendAndSave(ctx, sess, sess.PhoneNumber, result)
	} else {
		// No data — notify patient and trigger the state handler.
		// For automatic states: executes immediately (e.g., CHECK_ENTITY).
		// For interactive states: re-displays the prompt/buttons (e.g., MAIN_MENU).
		p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, "Hemos retomado tu atención. Continuamos con tu proceso.")

		virtualMsg := bird.InboundMessage{
			ID:          fmt.Sprintf("agent-resume-%s-%d", cmd.Phone, time.Now().UnixNano()),
			Phone:       sess.PhoneNumber,
			Text:        "__resume__",
			MessageType: "text",
			ReceivedAt:  time.Now(),
		}
		result, err := p.machine.Process(ctx, sess, virtualMsg)
		if err != nil {
			slog.Error("process virtual resume message failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		} else if result != nil {
			p.sendAndSave(ctx, sess, sess.PhoneNumber, result)
		}
	}

	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "escalation_ended", map[string]interface{}{
			"resumed_at_state": targetState,
			"data_corrected":   cmd.Data != "",
			"by":               "agent_command",
		})
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "agent_resumed",
		observability.EmitOpts{Phone: cmd.Phone})
}

// handleAgentReset restarts the session from GREETING (like /bot without arguments).
func (p *MessageWorkerPool) handleAgentReset(ctx context.Context, sess *session.Session, cmd AgentCommand) {
	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, statemachine.StateGreeting); err != nil {
		slog.Error("agent reset failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		return
	}
	if err := p.sessionManager.ClearAllContext(ctx, sess); err != nil {
		slog.Error("clear context on reset failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
	}

	// Unassign agent from Bird Inbox (bot restarts, keep item open)
	p.birdClient.UnassignFeedItem(sess.ConversationID, false)

	p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, "Hemos retomado tu atención. Vamos a comenzar de nuevo.")

	// Trigger GREETING handler
	virtualMsg := bird.InboundMessage{
		ID:          fmt.Sprintf("agent-reset-%s-%d", cmd.Phone, time.Now().UnixNano()),
		Phone:       sess.PhoneNumber,
		MessageType: "text",
		ReceivedAt:  time.Now(),
	}
	result, err := p.machine.Process(ctx, sess, virtualMsg)
	if err != nil {
		// N-26: loguear el error descartado en lugar de tragarlo silenciosamente.
		slog.Error("process virtual reset message failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
	} else if result != nil {
		p.sendAndSave(ctx, sess, sess.PhoneNumber, result)
	}

	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "escalation_ended", map[string]interface{}{
			"resumed_at_state": statemachine.StateGreeting,
			"by":               "agent_reset",
		})
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "agent_resumed",
		observability.EmitOpts{Phone: cmd.Phone, Reason: "reset"})
}

// handleAgentClose closes the session (status=completed).
func (p *MessageWorkerPool) handleAgentClose(ctx context.Context, sess *session.Session, cmd AgentCommand) {
	if err := p.sessionManager.Complete(ctx, sess); err != nil {
		slog.Error("agent close failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		return
	}

	p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, "Tu consulta ha sido resuelta. Gracias por comunicarte con nosotros!")

	// Delay close so Bird finishes processing the outbound message delivery
	closingConvID := sess.ConversationID
	p.wg.Add(1) // L16: wg-tracked + sleep cancelable
	go func() {
		defer p.wg.Done()
		defer recoverLog("close-feed-agent")
		select {
		case <-time.After(3 * time.Second):
		case <-p.ctx.Done():
			return
		}
		if err := p.birdClient.CloseFeedItems(closingConvID); err != nil {
			slog.Warn("close feed items on agent close failed", "conversation_id", closingConvID, "error", err)
		}
	}()

	if p.tracker != nil {
		// session_completed además de escalation_closed: la sesión SÍ se completó (el agente la resolvió
		// y cerró). Sin él, el donut por status la cuenta como 'completed' pero el StatCard 'Completadas'
		// (que cuenta session_completed) y avg_session_duration la excluían → cifras incoherentes.
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "session_completed", nil)
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "escalation_closed", nil)
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "agent_closed",
		observability.EmitOpts{Phone: cmd.Phone})
}

// handleAgentInfo sends a session context summary (visible to the agent in Bird Inbox).
func (p *MessageWorkerPool) handleAgentInfo(ctx context.Context, sess *session.Session) {
	info := fmt.Sprintf(
		"Info de sesion\n\n"+
			"ID: %s\n"+
			"Estado: %s\n"+
			"Status: %s\n"+
			"Paciente: %s\n"+
			"Documento: %s\n"+
			"Menu: %s\n"+
			"CUPS: %s\n"+
			"Servicio: %s\n"+
			"Equipo: %s\n"+
			"Estado pre-escalacion: %s",
		sess.ID,
		sess.CurrentState,
		sess.Status,
		sess.GetContext("patient_name"),
		sess.GetContext("patient_doc"),
		sess.GetContext("menu_option"),
		sess.GetContext("cups_code"),
		sess.GetContext("service_name"),
		sess.GetContext("escalation_team"),
		sess.GetContext("pre_escalation_state"),
	)
	p.birdClient.SendInternalText(sess.ConversationID, info)
}

// handleAgentOrder processes /bot orden commands — extracts CUPS from a text description using OCR AI.
func (p *MessageWorkerPool) handleAgentOrder(ctx context.Context, sess *session.Session, cmd AgentCommand) {
	if cmd.Data == "" {
		p.birdClient.SendInternalText(sess.ConversationID,
			"Uso: /bot orden <descripcion de la orden>\n"+
				"Ej: /bot orden Resonancia cerebral simple codigo 883141 cantidad 1\n"+
				"Ej: /bot orden Electromiografia 4 ext codigo 930810 cantidad 1, Resonancia columna lumbar codigo 883210 cantidad 1")
		return
	}

	if p.ocrService == nil {
		p.birdClient.SendInternalText(sess.ConversationID, "Error: servicio OCR no disponible.")
		return
	}

	// 1. Call OCR service with text description
	result, err := p.ocrService.AnalyzeText(ctx, cmd.Data)
	if err != nil || !result.Success {
		errMsg := "No se pudieron extraer procedimientos de la descripcion."
		if err != nil {
			errMsg += " Error: " + err.Error()
		} else if result != nil && result.Error != "" {
			errMsg += " Detalle: " + result.Error
		}
		p.birdClient.SendInternalText(sess.ConversationID, errMsg)
		return
	}

	// 2. Serialize CUPS to JSON and store in session
	cupsJSON, _ := json.Marshal(result.Cups)
	sess.SetContext("ocr_cups_json", string(cupsJSON))

	// 3. Resume at VALIDATE_OCR (same flow as image OCR success)
	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, statemachine.StateValidateOCR); err != nil {
		slog.Error("orden resume failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		p.birdClient.SendInternalText(sess.ConversationID, "Error al retomar sesion: "+err.Error())
		return
	}
	p.birdClient.UnassignFeedItem(sess.ConversationID, false)

	// 4. Send confirmation to agent (internal only)
	var summary strings.Builder
	summary.WriteString("Procedimientos extraidos:\n")
	for _, c := range result.Cups {
		fmt.Fprintf(&summary, "- %s: %s (x%d)\n", c.Code, c.Name, c.Quantity)
	}
	summary.WriteString("\nProcesando con el paciente...")
	p.birdClient.SendInternalText(sess.ConversationID, summary.String())

	// 5. Notify patient and trigger VALIDATE_OCR
	p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID,
		"Hemos retomado tu atención. Verificando tu orden médica...")

	virtualMsg := bird.InboundMessage{
		ID:          fmt.Sprintf("agent-orden-%s-%d", cmd.Phone, time.Now().UnixNano()),
		Phone:       sess.PhoneNumber,
		MessageType: "text",
		ReceivedAt:  time.Now(),
	}
	r, err := p.machine.Process(ctx, sess, virtualMsg)
	if err != nil {
		// N-26: loguear el error descartado en lugar de tragarlo silenciosamente.
		slog.Error("process virtual orden message failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
	} else if r != nil {
		p.sendAndSave(ctx, sess, sess.PhoneNumber, r)
	}

	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "agent_orden_processed", map[string]interface{}{
			"cups_count":  len(result.Cups),
			"description": cmd.Data,
		})
	}
}

// handleAgentCups injects CUPS codes directly without AI extraction.
// Syntax: /bot cups <code1>[:qty] <code2>[:qty] ...
// Example: /bot cups 883141 930810:2
// VALIDATE_OCR will automatically enrich names from the procedure DB.
func (p *MessageWorkerPool) handleAgentCups(ctx context.Context, sess *session.Session, cmd AgentCommand) {
	if cmd.Data == "" {
		p.birdClient.SendInternalText(sess.ConversationID,
			"Uso: /bot cups <codigo1>[:cantidad] <codigo2>[:cantidad] ...\n"+
				"Ej: /bot cups 883141\n"+
				"Ej: /bot cups 883141:1 930810:2")
		return
	}

	// Parse each token: "883141" or "883141:2"
	tokens := strings.Fields(cmd.Data)
	var cups []services.CUPSEntry
	for _, tok := range tokens {
		parts := strings.SplitN(tok, ":", 2)
		code := strings.TrimSpace(parts[0])
		if code == "" {
			continue
		}
		qty := 1
		if len(parts) == 2 {
			if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && n > 0 {
				qty = n
			}
		}
		cups = append(cups, services.CUPSEntry{
			Code:     code,
			Quantity: qty,
			// Name is intentionally empty — VALIDATE_OCR enriches from procedure DB
		})
	}

	if len(cups) == 0 {
		p.birdClient.SendInternalText(sess.ConversationID, "No se encontraron codigos CUPS validos en el comando.")
		return
	}

	cupsJSON, _ := json.Marshal(cups)
	sess.SetContext("ocr_cups_json", string(cupsJSON))

	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, statemachine.StateValidateOCR); err != nil {
		slog.Error("cups inject resume failed", "error", err, "phone", utils.MaskPhone(cmd.Phone))
		p.birdClient.SendInternalText(sess.ConversationID, "Error al retomar sesion: "+err.Error())
		return
	}
	p.birdClient.UnassignFeedItem(sess.ConversationID, false)

	// Internal summary for agent
	var summary strings.Builder
	summary.WriteString("Codigos CUPS inyectados:\n")
	for _, c := range cups {
		fmt.Fprintf(&summary, "- %s (x%d)\n", c.Code, c.Quantity)
	}
	summary.WriteString("\nProcesando con el paciente...")
	p.birdClient.SendInternalText(sess.ConversationID, summary.String())

	// Notify patient and trigger VALIDATE_OCR
	p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID,
		"Hemos retomado tu atención. Verificando tu orden médica...")

	virtualMsg := bird.InboundMessage{
		ID:          fmt.Sprintf("agent-cups-%s-%d", cmd.Phone, time.Now().UnixNano()),
		Phone:       sess.PhoneNumber,
		MessageType: "text",
		ReceivedAt:  time.Now(),
	}
	r, err := p.machine.Process(ctx, sess, virtualMsg)
	if err != nil {
		// N-26: loguear el error descartado en lugar de tragarlo silenciosamente.
		slog.Error("process virtual cups message failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
	} else if r != nil {
		p.sendAndSave(ctx, sess, sess.PhoneNumber, r)
	}

	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "agent_cups_injected", map[string]interface{}{
			"cups_count": len(cups),
			"raw":        cmd.Data,
		})
	}
}

// sendAndSave sends result messages and persists state (shared between processMessage and agent commands).
func (p *MessageWorkerPool) sendAndSave(ctx context.Context, sess *session.Session, phone string, result *statemachine.StateResult) {
	// Send messages — route via Conversations API when conversationID is available.
	// Add a short delay between consecutive messages so WhatsApp preserves order
	// (separate API calls have no delivery-order guarantee).
	convID := sess.ConversationID
	for i, outMsg := range result.Messages {
		if i > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		// Refresh convID from cache — Channels API responses populate it
		if convID == "" {
			if cached := p.birdClient.GetCachedConversationID(phone); cached != "" {
				convID = cached
				sess.ConversationID = cached
			}
		}
		slog.Debug("sending_message", "phone", utils.MaskPhone(phone), "type", outMsg.Type(), "conversation_id", convID)
		birdMsgID, err := p.sendMessage(phone, convID, outMsg)
		if err != nil {
			slog.Error("send message error", "phone", utils.MaskPhone(phone), "session_id", sess.ID, "conversation_id", convID, "type", outMsg.Type(), "error", err)
			// Clear convID for remaining messages so they route via Channels API
			// directly, avoiding repeated failures against a stuck conversation
			if convID != "" {
				slog.Warn(
					"clearing_conversation_id_for_batch",
					"phone", utils.MaskPhone(phone),
					"conversation_id", convID,
					"failed_type", outMsg.Type(),
				)
				convID = ""
			}
			// E6: async retry after 10s via Channels API (L16: wg-tracked → el shutdown lo espera/cancela)
			p.wg.Add(1)
			go func(m statemachine.OutboundMessage) {
				defer p.wg.Done()
				p.retrySend(phone, m)
			}(outMsg)
			continue
		}
		slog.Debug("message_sent_ok", "phone", utils.MaskPhone(phone), "type", outMsg.Type(), "bird_msg_id", birdMsgID)
		if p.tracker != nil {
			p.tracker.LogMessageSent(ctx, sess.ID, phone, outMsg.Type(), birdMsgID)
		}
	}

	// Post-loop: capture conversation_id from Channels API response.
	// For single-message results, the in-loop cache check runs before the send
	// (cache empty), so the conversationId from the API response is missed.
	// This final check ensures it's captured and persisted by SaveState below.
	if sess.ConversationID == "" {
		if cached := p.birdClient.GetCachedConversationID(phone); cached != "" {
			sess.ConversationID = cached
			slog.Info(
				"conversation_id_captured_post_send",
				"session_id", sess.ID,
				"phone", utils.MaskPhone(phone),
				"conversation_id", cached,
			)
		}
	}

	// Persist state + context
	clearAll := false
	for _, k := range result.ClearCtx {
		if k == "__all__" {
			clearAll = true
			break
		}
	}

	if clearAll {
		if err := p.sessionManager.ClearAllContext(ctx, sess); err != nil {
			slog.Error("clear all context error", "phone", utils.MaskPhone(phone), "session_id", sess.ID, "conversation_id", convID, "error", err)
		}
		result.ClearCtx = nil
	}

	if err := p.sessionManager.SaveState(ctx, sess, result.NextState, result.UpdateCtx, result.ClearCtx); err != nil {
		ctxKeys := make([]string, 0, len(result.UpdateCtx))
		for k := range result.UpdateCtx {
			ctxKeys = append(ctxKeys, k)
		}
		slog.Error(
			"save_state_error",
			"phone", utils.MaskPhone(phone),
			"session_id", sess.ID,
			"conversation_id", convID,
			"next_state", result.NextState,
			"context_keys", ctxKeys,
			"error", err,
		)
		// Force-close the session so the next message creates a fresh one
		// instead of loading stale state that causes confusing regressions.
		if completeErr := p.sessionManager.Complete(ctx, sess); completeErr != nil {
			slog.Error(
				"save_state_fallback_complete_failed",
				"phone", utils.MaskPhone(phone),
				"session_id", sess.ID,
				"error", completeErr,
			)
		} else {
			sess.Status = session.StatusCompleted
			slog.Warn(
				"session force-completed after save_state failure",
				"phone", utils.MaskPhone(phone),
				"session_id", sess.ID,
			)
		}
	}

	// Close feed items in Bird Inbox when session completes (bot-driven termination).
	// Delay 3s so Bird finishes processing the outbound message delivery before we close,
	// otherwise the delivery confirmation can reopen the conversation.
	if sess.Status == session.StatusCompleted && convID != "" {
		closingConvID := convID
		p.wg.Add(1) // L16: wg-tracked + sleep cancelable
		go func() {
			defer p.wg.Done()
			defer recoverLog("close-feed-complete")
			select {
			case <-time.After(3 * time.Second):
			case <-p.ctx.Done():
				return
			}
			if err := p.birdClient.CloseFeedItems(closingConvID); err != nil {
				slog.Warn("close feed items on completion failed", "phone", utils.MaskPhone(phone), "conversation_id", closingConvID, "error", err)
			}
		}()
	}

	// Log events
	if p.tracker != nil && len(result.Events) > 0 {
		p.tracker.LogBatch(ctx, sess.ID, phone, result.Events)
	}
	for _, event := range result.Events {
		slog.Info(
			"event",
			"session_id", sess.ID,
			"phone", utils.MaskPhone(phone),
			"type", event.Type,
			"data", event.Data,
		)
	}
}

// sendMessage envía un mensaje saliente vía Bird API.
// Routes via Conversations API when conversationID is available.
func (p *MessageWorkerPool) sendMessage(phone, conversationID string, msg statemachine.OutboundMessage) (string, error) {
	switch m := msg.(type) {
	case *statemachine.TextMessage:
		return p.birdClient.SendText(phone, conversationID, m.Text)
	case *statemachine.ButtonMessage:
		buttons := make([]bird.Button, len(m.Buttons))
		for i, b := range m.Buttons {
			buttons[i] = bird.Button{Text: b.Text, Payload: b.Payload}
		}
		return p.birdClient.SendButtons(phone, conversationID, m.Text, buttons)
	case *statemachine.ListMessage:
		sections := make([]bird.ListSection, len(m.Sections))
		for i, s := range m.Sections {
			rows := make([]bird.ListRow, len(s.Rows))
			for j, r := range s.Rows {
				rows[j] = bird.ListRow{ID: r.ID, Title: r.Title, Description: r.Description}
			}
			sections[i] = bird.ListSection{Title: s.Title, Rows: rows}
		}
		return p.birdClient.SendList(phone, conversationID, m.Body, m.Title, sections)
	default:
		return "", fmt.Errorf("unknown message type: %T", msg)
	}
}

// retrySend retries a failed outbound message after a delay.
// Uses Channels API directly (empty conversationID) to avoid repeating
// a Conversations API failure. Launched as a goroutine from sendAndSave.
func (p *MessageWorkerPool) retrySend(phone string, msg statemachine.OutboundMessage) {
	// L16: sleep cancelable por el app-ctx para no re-entregar un mensaje tras el shutdown.
	select {
	case <-time.After(10 * time.Second):
	case <-p.ctx.Done():
		return
	}

	_, err := p.sendMessage(phone, "", msg)
	if err != nil {
		slog.Error(
			"send message retry failed",
			"phone", utils.MaskPhone(phone),
			"type", msg.Type(),
			"error", err,
		)
		return
	}
	slog.Info(
		"send_message_retry_succeeded",
		"phone", utils.MaskPhone(phone),
		"type", msg.Type(),
	)
}

// UpdateConversationID persists a conversationID to the active session for a phone.
// Called from webhook handlers when conversation.created or outbound events arrive.
func (p *MessageWorkerPool) UpdateConversationID(phone, conversationID string) {
	if err := p.sessionManager.UpdateConversationID(p.ctx, phone, conversationID); err != nil {
		slog.Warn(
			"update_conversation_id_failed",
			"phone", utils.MaskPhone(phone),
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

// TouchAgentActivity records a human-agent reply for the escalated session of a phone (if any),
// which stops the agent-reminder clock. Called from the outbound webhook. No-op on error (best-effort).
func (p *MessageWorkerPool) TouchAgentActivity(phone string) {
	if err := p.sessionManager.TouchAgentActivity(p.ctx, phone); err != nil {
		slog.Warn("touch agent activity failed", "phone", utils.MaskPhone(phone), "error", err)
	}
}

// botInterstitialPrefixes son los textos que el PROPIO bot envía al paciente alrededor de la
// escalación (transferir / retomar / cerrar). Se excluyen de la detección de "respuesta del agente"
// para que no frenen el recordatorio falsamente.
// NOTE: si cambias estos textos en escalation.go / pool.go, actualízalos aquí también.
var botInterstitialPrefixes = []string{
	"Te voy a conectar con un agente",
	"Hemos retomado tu atención",
	"Tu consulta ha sido resuelta",
	"Tu mensaje anterior está siendo procesado",
	"Lo sentimos,",
	"Tardó demasiado procesar",
}

// IsBotInterstitialMessage reporta si un texto saliente fue emitido por el bot (no por el agente).
func IsBotInterstitialMessage(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range botInterstitialPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// EnqueueVirtual enqueues a virtual trigger message for the waiting list flow.
// The session already exists with SEARCH_SLOTS state, so we just need to trigger processing.
func (p *MessageWorkerPool) EnqueueVirtual(phone string) {
	msg := bird.InboundMessage{
		ID:          fmt.Sprintf("virtual-%s-%d", phone, time.Now().UnixNano()),
		Phone:       phone,
		MessageType: "text",
		Text:        "__waiting_list_trigger__",
		ReceivedAt:  time.Now(),
	}

	select {
	case p.queue <- msg:
		slog.Info("virtual message enqueued", "phone", utils.MaskPhone(phone))
	default:
		// M4 (auditoría): mismo tope de overflow que Enqueue — sin él, bajo saturación EnqueueVirtual
		// lanzaba goroutines sin límite (sin backpressure).
		if p.activeOverflow.Load() >= int32(maxOverflowGoroutines) {
			slog.Error("backpressure: overflow limit reached, dropping virtual message",
				"phone", utils.MaskPhone(phone),
				"queue_size", len(p.queue),
				"overflow_active", p.activeOverflow.Load())
			return
		}
		// Overflow processing — tracked with WaitGroup. #5: respetar el cierre (no wg.Add tras Stop).
		if !p.tryAddOverflow() {
			slog.Warn("pool closing, dropping virtual message", "phone", utils.MaskPhone(phone))
			return
		}
		go func() {
			defer p.wg.Done()
			defer p.activeOverflow.Add(-1)
			ctx, cancel := context.WithTimeout(p.ctx, 2*time.Minute) // L15: ligado al app-ctx → el shutdown lo cancela
			defer cancel()
			p.safeProcess(ctx, msg)
		}()
	}
}

// StartStaleReplay corre un ticker que cada staleReplayInterval re-encola los mensajes que quedaron
// 'pending' hace más de staleReplayMinutes (M7): recupera los descartados por backpressure sin
// esperar al reinicio. Idempotente: re-usa Enqueue (dedup) y processMessage marca 'done' la fila, así
// que un mensaje ya procesado no se vuelve a recoger.
func (p *MessageWorkerPool) StartStaleReplay(ctx context.Context, src StalePendingSource) {
	if src == nil {
		return
	}
	ticker := time.NewTicker(staleReplayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.replayStale(ctx, src)
		}
	}
}

// replayStale recoge los mensajes 'pending' viejos y los re-encola. Separado para poder testearlo.
func (p *MessageWorkerPool) replayStale(ctx context.Context, src StalePendingSource) {
	msgs, err := src.PendingOlderThan(ctx, staleReplayMinutes)
	if err != nil {
		slog.Error("stale replay: query failed", "error", err)
		return
	}
	if len(msgs) == 0 {
		return
	}
	slog.Warn("stale replay: re-encolando mensajes 'pending' atascados", "count", len(msgs))
	for _, m := range msgs {
		// Enqueue es un encolado no-bloqueante SIN context por diseño (el overflow ya deriva de p.ctx);
		// se llama igual desde el webhook y el replay de arranque.
		p.Enqueue(m) //nolint:contextcheck // enqueue no-bloqueante; dedup+backpressure normales, processMessage marca 'done'
	}
}

func (p *MessageWorkerPool) startDedupCleanup(ctx context.Context) {
	ticker := time.NewTicker(dedupCleanup)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			var cleaned int64
			p.recentMessages.Range(func(key, value interface{}) bool {
				if now.Sub(value.(time.Time)) > dedupTTL {
					p.recentMessages.Delete(key)
					cleaned++
				}
				return true
			})
			if cleaned > 0 {
				p.dedupCount.Add(-cleaned)
			}
		}
	}
}
