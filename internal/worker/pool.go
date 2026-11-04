package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
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
	// persistStateTimeout acota el guardado de estado, que corre con contexto PROPIO (desacoplado del
	// presupuesto del mensaje) para que un handler que se pasó de tiempo no se lleve por delante el
	// estado del paciente. Ver sendAndSave (auditoría 2026-07-27).
	persistStateTimeout = 15 * time.Second
	processMsgTimeout   = 2 * time.Minute // BUG-004: cota por mensaje del ctx desacoplado del apagado
	agentCmdQueueSize   = 50
	maxDedupEntries     = 100000 // safety cap; fail-open if exceeded (allow potential dups)

	// ctxNonContactable: clave de contexto de sesión — el identificador del contacto no es un
	// teléfono E.164 y ningún envío por Channels API puede entregársele.
	ctxNonContactable = "non_contactable"

	// Re-replay del WAL (M7): recupera mensajes que quedaron 'pending' (p.ej. descartados por
	// backpressure) sin esperar al reinicio. El umbral (> dedupTTL) evita reprocesar mensajes en
	// vuelo y garantiza que el claim de dedup del intento previo ya expiró.
	staleReplayMinutes  = 10
	staleReplayInterval = 5 * time.Minute

	// maxPhoneLockWait: cuánto se sigue reencolando un mensaje que no consigue el candado de su
	// teléfono, contado desde su primer intento fallido, antes de cerrar su fila del WAL sin procesarlo
	// (B-01). Mismo margen que staleReplayMinutes: cubre una ráfaga de ~8 hojas con OCR de 75 s.
	maxPhoneLockWait = staleReplayMinutes * time.Minute
	// noticeClockTolerance: holgura de reloj entre Néctar (occurred_at) y la BD del bot
	// (sessions.escalated_at) al decidir si un aviso es anterior a la escalación vigente. Son
	// servidores distintos; mismo margen que nectarreports.ClockTolerance (D-02).
	noticeClockTolerance = 2 * time.Minute
	// phoneLockRetryDelay: pausa antes de reencolar un mensaje cuyo teléfono ya tiene un worker
	// esperando el candado; así una ráfaga de un teléfono ocupa a lo sumo un worker en espera (B-01).
	phoneLockRetryDelay = 2 * time.Second
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
	// AttachContact guarda el contacto de la conversación de Néctar (columna + contexto).
	AttachContact(ctx context.Context, sess *session.Session, contact string) error
	// FindForAgentCommand devuelve la sesión activa/escalada del teléfono AUNQUE esté vencida
	// (los comandos de agente nunca crean sesión). nil sin error = no hay sesión aplicable.
	FindForAgentCommand(ctx context.Context, phone string) (*session.Session, error)
	RenewTimeout(ctx context.Context, sess *session.Session) error
	TouchPatientActivity(ctx context.Context, sess *session.Session) error
	TouchAgentActivity(ctx context.Context, phone string) error
	SaveState(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error
	// ExtendExpiry estira el vencimiento de la sesión hasta ese instante (nunca lo acorta). Lo
	// pide el paso cuya escalación queda esperando a una persona más de lo que dura una sesión:
	// StateResult.ExtendExpiryUntil.
	ExtendExpiry(ctx context.Context, sess *session.Session, until time.Time) error
	ClearAllContext(ctx context.Context, sess *session.Session) error
	Escalate(ctx context.Context, sess *session.Session, teamID string) error
	ResumeFromEscalation(ctx context.Context, sess *session.Session, targetState string) error
	ResumeFromEscalationNoShow(ctx context.Context, sess *session.Session, targetState string) error
	// SetResolution guarda quién resolvió y quién escaló en la escalación abierta; va antes de Complete.
	SetResolution(ctx context.Context, sess *session.Session, resolvedBy, escalatedBy string) error
	Complete(ctx context.Context, sess *session.Session) error
	CompleteAsNoShow(ctx context.Context, sess *session.Session) error
	UpdateConversationID(ctx context.Context, phone, conversationID string) error
	SetContext(ctx context.Context, sess *session.Session, key, value string) error
}

// AgentActivityChecker consulta si el agente ya respondió la escalación abierta (solo lectura).
// Lo usa el acuse de recibo durante escalación (§8.1 #6).
type AgentActivityChecker interface {
	HasAgentResponded(ctx context.Context, phone string) (bool, error)
}

// shouldSendEscalationAck decide si enviar el acuse ÚNICO al paciente que espera agente (§8.1 #6):
// solo su primer mensaje (sin flag previo), con checker cableado y con certeza de que el agente NO ha
// respondido. Ante error o checker ausente: fail-quiet (silencio = comportamiento previo; jamás
// interrumpir una conversación humana en curso).
func shouldSendEscalationAck(ctx context.Context, checker AgentActivityChecker, ackAlreadySent bool, phone string) bool {
	if ackAlreadySent || checker == nil {
		return false
	}
	responded, err := checker.HasAgentResponded(ctx, phone)
	if err != nil || responded {
		return false
	}
	return true
}

// MessageSender abstracts outbound message sending and conversation cache for testability.
type MessageSender interface {
	SendText(phone, conversationID, text string) (string, error)
	SendButtons(phone, conversationID, text string, buttons []bird.Button) (string, error)
	SendList(phone, conversationID, body, title string, sections []bird.ListSection) (string, error)
	SendInternalText(phone, conversationID, text string) (string, error)
	UnassignFeedItem(phone, conversationID string, closed bool) error
	CloseFeedItems(phone, conversationID string) error
	GetCachedConversationID(phone string) string
	LookupConversationByPhone(phone string) (string, error)
}

// MessageProcessor abstracts state machine processing for testability.
type MessageProcessor interface {
	Process(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error)
	// ResolveReturnInput juzga el dato que el agente ESCRIBIÓ al devolver la conversación
	// (dialecto). Va aquí y no en una interfaz aparte para que el compilador obligue a cualquier
	// doble de prueba a decidir qué hace con él.
	ResolveReturnInput(sess *session.Session, step, value string) statemachine.ReturnInputResult
	// ResolveReturnV1 es la misma traducción para el retorno del protocolo v1, donde lo escrito
	// viaja en `valores`, aparte del token.
	ResolveReturnV1(sess *session.Session, step string, values json.RawMessage) statemachine.ReturnInputResult
	// IsRegistered dice si el estado tiene handler: un retorno nunca debe reanudar en uno que no.
	IsRegistered(state string) bool
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
	// waitingListID es el id de la entrada de lista de espera notificada ("" en los demás tipos).
	HandleNotifPendingCommand(phone, action, convID, appointmentID, notifType, waitingListID string)
}

// notifPendingActions son las acciones que el atajo de NOTIF_PENDING entrega a
// HandleNotifPendingCommand: las del menú de retornos del paso, para todos los tipos.
var notifPendingActions = map[string]bool{
	"confirm": true, "reschedule": true, "cancel": true,
	"understood": true, "wl_schedule": true, "wl_decline": true,
}

// notifPendingActionsFor son las acciones que HandleNotifPendingCommand sabe ejecutar para un tipo
// de notificación. Un tipo desconocido se trata como confirmación, igual que allá.
func notifPendingActionsFor(notifType string) []string {
	switch notifType {
	case "cancellation":
		return []string{"understood", "reschedule"}
	case "waiting_list":
		return []string{"wl_schedule", "wl_decline"}
	default:
		return []string{"confirm", "reschedule", "cancel"}
	}
}

// refuseNotifPendingAction devuelve al agente una acción de NOTIF_PENDING que no corresponde al tipo
// de la notificación. En Bird, una nota interna con los comandos válidos; en Néctar, una re-escalada
// como dato rechazado (su nota interna no tiene dónde aterrizar).
func (p *MessageWorkerPool) refuseNotifPendingAction(ctx context.Context, sess *session.Session,
	cmd AgentCommand, convID, notifType, action string, valid []string,
) {
	slog.Warn("notif_pending: acción que no corresponde al tipo de notificación",
		"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "action", action, "notif_type", notifType)
	if session.IsNectarSession(sess.PhoneNumber) {
		p.reescalateRejectedAgentInput(ctx, sess, cmd, statemachine.StateNotifPending, statemachine.ReturnInputResult{
			Written: true,
			Value:   action,
			Problem: "La acción no corresponde a este tipo de notificación; elija una de las opciones del menú.",
		})
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "La acción %q no aplica a esta notificación. Comandos disponibles:\n", action)
	for _, a := range valid {
		fmt.Fprintf(&b, "  /bot resume NOTIF_PENDING %s\n", a)
	}
	b.WriteString("  /bot cerrar")
	if _, err := p.birdClient.SendInternalText(sess.PhoneNumber, convID, b.String()); err != nil {
		slog.Error("notif_pending: nota de acción inválida no enviada", "error", err,
			"phone", utils.MaskPhone(cmd.Phone), "conversation_id", convID)
	}
}

// InboxMarker marks messages as processed in the inbox (WAL pattern).
type InboxMarker interface {
	MarkDone(ctx context.Context, id string) error
}

type MessageWorkerPool struct {
	queue          chan bird.InboundMessage
	agentCmds      chan AgentCommand
	recentMessages sync.Map     // messageID -> time.Time (dedup)
	lockMisses     sync.Map     // messageID -> lockMiss: primer intento fallido por el candado del teléfono (B-01)
	lockWaiters    sync.Map     // phone -> struct{}: ya hay un worker esperando el candado de ese teléfono (B-01)
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

	// retryDelay: espera antes del reintento de un envío fallido (solo errores transitorios).
	retryDelay time.Duration
	// passTimeout acota cada pasada del replay de atascados (0 = utils.BackgroundPassTimeout).
	passTimeout time.Duration
	// lockTimeout: espera máxima por el candado del teléfono (phoneLockTimeout; los tests lo acortan).
	lockTimeout time.Duration
	// lockRetryDelay: pausa antes de reencolar un mensaje cuyo teléfono ya tiene un worker esperando
	// el candado (phoneLockRetryDelay; los tests lo acortan).
	lockRetryDelay time.Duration

	// Dependencias (inyectadas después de creación)
	sessionManager  SessionManagement
	birdClient      MessageSender
	agentChecker    AgentActivityChecker // acuse durante escalación (§8.1 #6); nil = fail-quiet
	machine         MessageProcessor
	tracker         *tracking.EventTracker
	ocrService      OCRAnalyzer
	inboxRepo       InboxMarker // WAL crash recovery (optional)
	notifyResponder NotificationResponder
	// businessHoursText devuelve el horario de atención EN PALABRAS. Se pregunta en cada uso
	// (puede cambiar sin redespliegue); nil = el aviso sale sin nombrarlo. Ver queue_expired.go.
	businessHoursText func() string
	// escalationQueuedNextDay dice si la escalación de ESTA sesión de Néctar se va a encolar para el
	// próximo día hábil. La consulta PreEscalationFlush; nil = nunca (el aviso del caller sale).
	escalationQueuedNextDay func(*session.Session) bool
}

func NewMessageWorkerPool(workers, queueSize int) *MessageWorkerPool {
	if workers <= 0 {
		workers = defaultWorkers
	}
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	return &MessageWorkerPool{
		queue:          make(chan bird.InboundMessage, queueSize),
		agentCmds:      make(chan AgentCommand, agentCmdQueueSize),
		workers:        workers,
		ctx:            context.Background(),
		retryDelay:     10 * time.Second,
		lockTimeout:    phoneLockTimeout,
		lockRetryDelay: phoneLockRetryDelay,
	}
}

// SetDependencies inyecta las dependencias necesarias para procesar mensajes
func (p *MessageWorkerPool) SetDependencies(sm SessionManagement, bc MessageSender, m MessageProcessor) {
	p.sessionManager = sm
	p.birdClient = bc
	p.machine = m
}

// SetTracker sets the event tracker for persisting events to the database.
// SetAgentActivityChecker cablea el chequeo "¿el agente ya respondió?" para el acuse de escalación.
func (p *MessageWorkerPool) SetAgentActivityChecker(c AgentActivityChecker) {
	p.agentChecker = c
}

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

// EnqueueAgentCommand enqueues a /bot command from a human agent. Devuelve false si no lo encoló
// (buffer lleno o pool apagándose): quien lo trajo de una fila del WAL la deja 'pending' para el
// replay (D-02). Encola bajo closeMu.RLock, como requeueLockMiss: tras el Stop nada entra al buffer.
func (p *MessageWorkerPool) EnqueueAgentCommand(cmd AgentCommand) bool {
	p.closeMu.RLock()
	defer p.closeMu.RUnlock()
	if p.closing || p.ctx.Err() != nil {
		slog.Warn("agent command dropped: pool apagándose", "phone", utils.MaskPhone(cmd.Phone),
			"action", cmd.Action, "event_id", cmd.EventID)
		return false
	}
	select {
	case p.agentCmds <- cmd:
		slog.Info("agent command enqueued", "phone", utils.MaskPhone(cmd.Phone), "action", cmd.Action)
		return true
	default:
		slog.Warn("agent command queue full, dropping", "phone", utils.MaskPhone(cmd.Phone),
			"action", cmd.Action, "event_id", cmd.EventID)
		return false
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

// lockMiss sigue a un mensaje del WAL que no consiguió el candado de su teléfono (B-01): cuándo empezó
// su primer intento fallido y si el paciente ya recibió el aviso de espera.
type lockMiss struct {
	first    time.Time
	notified bool
}

// handleLockMiss atiende un mensaje que no consiguió el candado de su teléfono (B-01) y dice si su
// fila del WAL debe quedar 'pending'. Un mensaje del WAL se reencola al final de la cola (sin pasar
// por el dedup de Enqueue): de inmediato si venció su espera, o tras `delay` si no llegó a esperar
// porque su teléfono ya tenía un worker esperando. Así se reprocesa apenas se libere el candado y en
// el mismo estado de la sesión; no conserva su lugar en la ráfaga (el candado no es FIFO). Con la
// cola llena o el pool apagándose no se reencola: la fila queda 'pending' para el replay de
// atascados o el de arranque. Pasado maxPhoneLockWait desde el primer intento fallido se rinde,
// cierra la fila y alerta, porque el mensaje se pierde. El aviso al paciente sale una sola vez, en el
// primer intento en que el mensaje ya lleva lockTimeout sin procesarse (como antes, cuando solo lo
// daba una espera vencida); un diferido que se procesa antes no lo recibe. Lo que no rastrea el WAL
// (virtual-, agent-, sin id) se descarta tras el aviso, como siempre.
//
// attemptStart es cuándo empezó este intento: antes de esperar el candado, o al diferirse. La
// entrada de lockMisses se borra al procesarse el mensaje o al rendirse; si se va al replay (cola
// llena, apagado) se conserva para no reiniciar el plazo ni repetir el aviso, y la limpieza
// periódica purga la de un mensaje que no volvió (purgeStaleLockMisses).
func (p *MessageWorkerPool) handleLockMiss(appCtx context.Context, msg bird.InboundMessage, walTracked bool,
	attemptStart time.Time, delay time.Duration,
) bool {
	if !walTracked {
		p.sendLockBusyNotice(msg.Phone)
		return false
	}
	// Sin carrera por el mismo id: el claim de dedup (renovado al reencolar) no deja dos copias en vuelo.
	miss := lockMiss{first: attemptStart}
	if v, retried := p.lockMisses.Load(msg.ID); retried {
		miss = v.(lockMiss)
	}
	waited := time.Since(miss.first)
	if waited >= maxPhoneLockWait {
		p.lockMisses.Delete(msg.ID)
		slog.Error("phone lock timeout: mensaje descartado tras agotar la espera",
			"id", msg.ID, "phone", utils.MaskPhone(msg.Phone), "waited", waited.Round(time.Second))
		return false
	}
	if !miss.notified && waited >= p.lockTimeout {
		p.sendLockBusyNotice(msg.Phone)
		miss.notified = true
	}
	p.lockMisses.Store(msg.ID, miss) // antes del reencolado: quien lo tome ya lo ve como reintento
	if appCtx.Err() != nil {
		slog.Warn("phone lock timeout: pool apagándose, mensaje pendiente para el replay",
			"id", msg.ID, "phone", utils.MaskPhone(msg.Phone))
		return true
	}
	if delay <= 0 {
		p.requeueLockMiss(msg)
	} else {
		// Timer y no Sleep: el worker queda libre. Stop no espera por él (no entra al wg); si dispara
		// ya en el cierre, requeueLockMiss no envía y la fila queda para el replay de arranque.
		time.AfterFunc(delay, func() { p.requeueLockMiss(msg) })
	}
	return true
}

// sendLockBusyNotice avisa al paciente que su mensaje llegó pero espera al anterior.
func (p *MessageWorkerPool) sendLockBusyNotice(phone string) {
	if _, err := p.birdClient.SendText(phone, "",
		"Tu mensaje anterior está siendo procesado. Por favor espera un momento antes de enviar otro mensaje."); err != nil {
		slog.Warn("phone lock notice send failed", "phone", utils.MaskPhone(phone), "error", err)
	}
}

// requeueLockMiss reencola, sin bloquear, un mensaje que no consiguió el candado de su teléfono.
// Envía bajo closeMu.RLock: Stop marca el cierre con Lock, así que nada entra a la cola después de
// que Stop empezó (lo que entró antes lo toma el drain). p.queue nunca se cierra.
func (p *MessageWorkerPool) requeueLockMiss(msg bird.InboundMessage) {
	p.closeMu.RLock()
	defer p.closeMu.RUnlock()
	if p.closing || p.ctx.Err() != nil {
		slog.Warn("phone lock timeout: pool apagándose, mensaje pendiente para el replay",
			"id", msg.ID, "phone", utils.MaskPhone(msg.Phone))
		return
	}
	select {
	case p.queue <- msg:
		p.renewDedupClaim(msg.ID)
		slog.Info("phone lock timeout: mensaje reencolado", "id", msg.ID, "phone", utils.MaskPhone(msg.Phone))
	default:
		slog.Warn("phone lock timeout: cola llena, mensaje pendiente para el replay",
			"id", msg.ID, "phone", utils.MaskPhone(msg.Phone))
	}
}

// renewDedupClaim refresca el claim de dedup de un mensaje reencolado: su reencolado puede durar más
// que dedupTTL y, sin renovarlo, el replay de atascados metería una segunda copia en vuelo (B-01).
// Un reenvío del webhook no llega aquí: el WAL ya lo rechaza en InsertIfNotExists.
func (p *MessageWorkerPool) renewDedupClaim(id string) {
	if _, loaded := p.recentMessages.Swap(id, time.Now()); !loaded {
		p.dedupCount.Add(1) // la limpieza ya lo había borrado y descontado
	}
}

// purgeStaleLockMisses borra las entradas de lockMisses de mensajes que se fueron al replay y nunca
// volvieron (fila cerrada por otro camino). El margen doble de maxPhoneLockWait no toca a un mensaje
// aún en vuelo: ese se rinde en su siguiente intento pasado maxPhoneLockWait y borra su entrada.
func (p *MessageWorkerPool) purgeStaleLockMisses(now time.Time) {
	p.lockMisses.Range(func(key, value interface{}) bool {
		if now.Sub(value.(lockMiss).first) > 2*maxPhoneLockWait {
			p.lockMisses.Delete(key)
		}
		return true
	})
}

// lockPhone toma el candado del teléfono. Un mensaje del WAL no espera si otro worker ya espera ese
// teléfono: devuelve deferred sin tomarlo (B-01). La marca de espera se borra al volver Lock, salga
// como salga, para no dejar diferidos para siempre los mensajes de ese teléfono.
func (p *MessageWorkerPool) lockPhone(ctx context.Context, phone string, walTracked bool) (deferred bool, err error) {
	if walTracked {
		if _, waiting := p.lockWaiters.LoadOrStore(phone, struct{}{}); waiting {
			return true, nil
		}
		defer p.lockWaiters.Delete(phone)
	}
	return false, p.sessionManager.PhoneMutex().Lock(ctx, phone)
}

func (p *MessageWorkerPool) processMessage(parentCtx context.Context, msg bird.InboundMessage) {
	// BUG-004: el procesamiento NO debe abortarse si el app-ctx se cancela en pleno apagado/redeploy.
	// Antes, un mensaje en vuelo veía sus escrituras (SaveState/MarkDone/log) abortadas con
	// "context canceled" → el estado del paciente quedaba sin persistir (víctima de reinicio). Se
	// desacopla con WithoutCancel + timeout propio: el worker deja de tomar mensajes NUEVOS al apagar
	// (worker() <-ctx.Done()), pero el que YA empezó completa su persistencia en la ventana de gracia
	// (Stop espera al wg; main da 20s). Aplica a todas las rutas (worker, overflow y drain).
	appCtx := parentCtx // B-01: el reencolado sí mira el apagado
	parentCtx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), processMsgTimeout)
	defer cancel()

	// WAL: mark message as done after processing (crash recovery)
	walTracked := p.inboxRepo != nil && msg.ID != "" && !strings.HasPrefix(msg.ID, "virtual-") && !strings.HasPrefix(msg.ID, "agent-")
	// keepPending: el mensaje no se procesó (candado vencido) y su fila se deja 'pending': la cierra
	// el intento que lo procese (B-01, ver handleLockMiss).
	keepPending := false
	if walTracked {
		defer func() {
			if keepPending {
				return
			}
			// N-01: con ctx propio, como el comando de agente: un turno que agotó processMsgTimeout
			// cierra igual su fila y el replay de atascados no lo reprocesa.
			doneCtx, doneCancel := utils.MarkContext(parentCtx)
			defer doneCancel()
			if err := p.inboxRepo.MarkDone(doneCtx, msg.ID); err != nil {
				slog.Error("inbox mark done failed", "id", msg.ID, "error", err)
			}
		}()
	}

	// 1-2. Adquirir lock por teléfono (serializa mensajes del mismo usuario), con timeout de 30s. B-01:
	// si otro worker ya espera ese teléfono, este mensaje del WAL no ocupa otro: se reencola tras
	// lockRetryDelay. attemptStart va antes del timeout: una espera vencida cuenta lockTimeout entero.
	attemptStart := time.Now()
	lockCtx, lockCancel := context.WithTimeout(parentCtx, p.lockTimeout)
	defer lockCancel()
	deferred, lockErr := p.lockPhone(lockCtx, msg.Phone, walTracked)
	if deferred {
		slog.Debug("phone lock busy: mensaje diferido", "id", msg.ID, "phone", utils.MaskPhone(msg.Phone))
		keepPending = p.handleLockMiss(appCtx, msg, walTracked, attemptStart, p.lockRetryDelay)
		return
	}
	if lockErr != nil {
		slog.Warn("phone lock timeout", "phone", utils.MaskPhone(msg.Phone), "error", lockErr)
		observability.Emit("infra:phone_lock_timeout", "infra", "phone_lock_timeout",
			observability.EmitOpts{Phone: msg.Phone, Reason: "message"})
		keepPending = p.handleLockMiss(appCtx, msg, walTracked, attemptStart, 0)
		return
	}
	defer p.sessionManager.PhoneMutex().Unlock(msg.Phone)
	p.lockMisses.Delete(msg.ID)

	// 3. Cargar/crear sesión
	sess, isNew, err := p.sessionManager.FindOrCreate(parentCtx, msg.Phone)
	if err != nil {
		slog.Error("session error", "phone", utils.MaskPhone(msg.Phone), "error", err)
		// Send error message via Channels API so patient is not left in silence
		p.birdClient.SendText(msg.Phone, "",
			"Lo sentimos, estamos experimentando dificultades técnicas. Por favor intenta nuevamente en unos minutos.")
		return
	}

	// 3a. Contacto de la conversación de Néctar (D2): se guarda ANTES del primer evento de la sesión,
	// porque chat_events y escalations lo copian de la sesión al insertar. Solo Néctar lo trae. Un
	// fallo no detiene el mensaje: el contacto es un dato de apoyo, no parte del flujo.
	if msg.ContactIdentifier != "" {
		if err := p.sessionManager.AttachContact(parentCtx, sess, msg.ContactIdentifier); err != nil {
			slog.Warn("attach nectar contact failed", "session_id", sess.ID, "error", err)
		}
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
		sess.Context[statemachine.CtxPreAutoState] = statemachine.BotDisabledStep
		sess.Context[statemachine.CtxEscalationReason] = statemachine.EscalationReasonBotDisabled
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
		// §8.1 #6: acuse ÚNICO al paciente que espera agente (2.061 ses/mes hablándole al vacío).
		// Solo su primer mensaje y solo si el agente NO ha respondido; fail-quiet en cualquier duda.
		if shouldSendEscalationAck(parentCtx, p.agentChecker, sess.GetContext("escalation_ack_sent") != "", msg.Phone) {
			if _, err := p.birdClient.SendText(msg.Phone, sess.ConversationID,
				"🙋 Seguimos aquí. Un asesor te responderá tan pronto esté disponible — tus mensajes quedan registrados para él."); err != nil {
				slog.Warn("escalation ack send failed", "phone", utils.MaskPhone(msg.Phone), "error", err)
			} else {
				if err := p.sessionManager.SetContext(parentCtx, sess, "escalation_ack_sent", "1"); err != nil {
					slog.Warn("escalation ack flag persist failed", "phone", utils.MaskPhone(msg.Phone), "error", err)
				}
				if p.tracker != nil {
					p.tracker.LogEvent(parentCtx, sess.ID, msg.Phone, "escalation_ack_sent", nil)
				}
				observability.Emit(observability.TraceSession(sess.ID), "escalacion", "ack_sent",
					observability.EmitOpts{Phone: msg.Phone})
			}
		}
		if p.tracker != nil {
			p.tracker.LogEvent(parentCtx, sess.ID, msg.Phone, "msg_during_escalation", map[string]interface{}{
				"text": msg.Text,
				"type": msg.MessageType,
			})
		}
		return
	}

	// 5a. Sesión cortada por el tope de escalaciones: la entrada se ignora en silencio y NO renueva
	// la expiración ni la actividad (tampoco se guarda nada). La sesión vence como siempre, contando
	// desde lo último que el bot sí atendió, y el mensaje siguiente abre una sesión nueva con el
	// saludo; si se renovara, un paciente cortado que vuelve al otro día recibiría silencio siempre.
	if statemachine.IgnoredByEscalationCap(sess, msg) {
		statemachine.TraceEscalationCapIgnored(sess, msg)
		slog.Info("entrada ignorada: sesión cortada por el tope de escalaciones",
			"session_id", sess.ID, "phone", utils.MaskPhone(msg.Phone), "type", msg.MessageType)
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

	// WAL (D-02): el comando de una resuelta o devuelta de Néctar cierra su fila al terminar, como
	// processMessage la de un mensaje. keepPending: no se aplicó (sin candado o sin sesión legible)
	// y la fila queda 'pending' para el replay de atascados. Re-aplicarlo es inocuo: con la sesión
	// ya fuera de escalación el comando no hace nada (paso 2), y el aviso anterior a la escalación
	// vigente se ignora (isStaleNectarNotice). «Procesado» no es «aplicado»: si el handler falla
	// (p. ej. ResumeFromEscalation), la fila se cierra igual, como antes.
	keepPending := false
	if p.inboxRepo != nil && cmd.EventID != "" {
		defer func() {
			if keepPending {
				return
			}
			// Con ctx propio: el presupuesto del turno, ya gastado, no decide si la fila se cierra.
			doneCtx, doneCancel := utils.MarkContext(parentCtx)
			defer doneCancel()
			if err := p.inboxRepo.MarkDone(doneCtx, cmd.EventID); err != nil {
				slog.Error("inbox mark done failed (agent cmd)", "id", cmd.EventID, "error", err)
			}
		}()
	}

	// 1. Acquire phone lock
	lockCtx, lockCancel := context.WithTimeout(parentCtx, p.lockTimeout)
	defer lockCancel()

	if err := p.sessionManager.PhoneMutex().Lock(lockCtx, cmd.Phone); err != nil {
		slog.Warn("phone lock timeout (agent cmd)", "phone", utils.MaskPhone(cmd.Phone), "error", err)
		observability.Emit("infra:phone_lock_timeout", "infra", "phone_lock_timeout",
			observability.EmitOpts{Phone: cmd.Phone, Reason: "agent_cmd"})
		keepPending = true
		return
	}
	defer p.sessionManager.PhoneMutex().Unlock(cmd.Phone)

	// 2. Find escalated session — NUNCA FindOrCreate: un comando de agente no crea sesión, y la
	// escalada puede estar VENCIDA (invisible para FindOrCreate pero bloqueante para el Create →
	// ErrActiveSessionExists en bucle: el checker re-encolaba el no_show cada 60s; auditoría 07-25).
	sess, err := p.sessionManager.FindForAgentCommand(parentCtx, cmd.Phone)
	if err != nil {
		slog.Error("session error (agent cmd)", "phone", utils.MaskPhone(cmd.Phone), "error", err)
		keepPending = true
		return
	}
	if sess == nil || sess.Status != session.StatusEscalated {
		slog.Warn("agent command but no escalated session", "phone", utils.MaskPhone(cmd.Phone), "action", cmd.Action)
		return
	}
	if isStaleNectarNotice(cmd, sess) {
		slog.Info("nectar: aviso anterior a la escalación vigente, ignorado",
			"event_id", cmd.EventID, "action", cmd.Action, "session_id", sess.ID,
			"occurred_at", cmd.OccurredAt, "escalated_at", *sess.EscalatedAt)
		observability.Emit(observability.TraceSession(sess.ID), "escalacion", "aviso_tardio_ignorado",
			observability.EmitOpts{Phone: cmd.Phone, Attrs: map[string]interface{}{"trigger": cmd.Action}})
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

	case "no_show":
		p.handleAgentNoShow(parentCtx, sess)
	}
}

// isStaleNectarNotice dice si el comando viene de un aviso de Néctar anterior a la escalación
// vigente (más la holgura de reloj): una copia tardía (replay) de un aviso ya aplicado, cuya
// escalación terminó y fue reemplazada por otra. Aplicarlo cerraría o devolvería la escalación
// nueva. Sin occurred_at o sin escalated_at no se puede saber: se aplica, como siempre (D-02).
func isStaleNectarNotice(cmd AgentCommand, sess *session.Session) bool {
	if cmd.EventID == "" || cmd.OccurredAt.IsZero() || sess.EscalatedAt == nil {
		return false
	}
	return cmd.OccurredAt.Before(sess.EscalatedAt.Add(-noticeClockTolerance))
}

// ResumeEscalationNoShow implementa session.EscalationResumer: lo invoca el checker de escalaciones
// cuando una escalación agotó los recordatorios sin que el agente respondiera. Encola un comando
// 'no_show' para que el worker (que tiene la state machine) reanude al bot y re-promptee el paso.
func (p *MessageWorkerPool) ResumeEscalationNoShow(phone string) {
	p.EnqueueAgentCommand(AgentCommand{Action: "no_show", Phone: phone})
}

// handleAgentNoShow devuelve al bot una escalación NO atendida por el agente: reanuda en el
// pre_escalation_state, re-promptea el paso al paciente, libera el feed item y marca 'agent_no_show'
// (métrica del agente). Análogo a handleAgentResume sin-dato, pero con outcome no-show y mensaje propio.
func (p *MessageWorkerPool) handleAgentNoShow(ctx context.Context, sess *session.Session) {
	// Guard de idempotencia: si entre el encolado y aquí la sesión dejó de estar escalada (el agente
	// respondió, o ya se procesó otro no_show), no re-promptear de nuevo.
	if sess.Status != session.StatusEscalated {
		return
	}
	// El no-show no existe para Néctar: la cola la administra Néctar, y el caso que nadie toma
	// vuelve por su propio camino (cola_vencida, handleQueueExpired).
	if session.IsNectarSession(sess.PhoneNumber) {
		slog.Info("no_show ignorado: la sesión es de Néctar",
			"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
		return
	}

	targetState := sess.GetContext("pre_escalation_state")
	if targetState == "" {
		targetState = statemachine.StateGreeting
	}

	// Reanuda la sesión marcando la escalación como 'agent_no_show' (no 'returned').
	if err := p.sessionManager.ResumeFromEscalationNoShow(ctx, sess, targetState); err != nil {
		slog.Error("no_show resume failed", "error", err, "phone", utils.MaskPhone(sess.PhoneNumber), "session_id", sess.ID)
		return
	}
	// El bot retoma: la espera de esta escalación se acabó, aunque sea por la mala.
	p.clearNextDayQueueMark(ctx, sess)

	// El bot retoma: liberar el feed item en Bird (mantener abierto, sin agente).
	if err := p.birdClient.UnassignFeedItem(sess.PhoneNumber, sess.ConversationID, false); err != nil {
		slog.Warn("no_show unassign feed item failed", "error", err, "conversation_id", sess.ConversationID)
	}

	// Avisar al paciente y re-promptear el paso (virtual __resume__ → la state machine re-muestra el
	// prompt). El aviso NO se envía aparte: se antepone a los mensajes del turno para que el
	// coalescer de sendAndSave lo funda con el primero (el menú, en la inmensa mayoría de no-shows).
	// Enviarlo suelto costaba un mensaje cobrado extra en CADA no-show —531 en 7 días— sin que el
	// paciente leyera nada distinto. Si el turno no produce mensajes, el aviso sale solo: es lo
	// único que le dice que el bot volvió a atenderle.
	const noShowNotice = "No pudimos conectarte con un agente en este momento. Sigamos por aquí para continuar tu solicitud."
	virtualMsg := bird.InboundMessage{
		ID:          fmt.Sprintf("no-show-%s-%d", sess.PhoneNumber, time.Now().UnixNano()),
		Phone:       sess.PhoneNumber,
		Text:        "__resume__",
		MessageType: "text",
		ReceivedAt:  time.Now(),
	}
	result, err := p.machine.Process(ctx, sess, virtualMsg)
	if err != nil {
		slog.Error("no_show re-prompt failed", "error", err, "phone", utils.MaskPhone(sess.PhoneNumber), "session_id", sess.ID)
		result = nil
	}
	if result == nil {
		result = statemachine.NewResult(sess.CurrentState)
	}
	result.Messages = append([]statemachine.OutboundMessage{&statemachine.TextMessage{Text: noShowNotice}}, result.Messages...)
	p.sendAndSave(ctx, sess, sess.PhoneNumber, result)

	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, sess.PhoneNumber, "escalation_agent_no_show", map[string]interface{}{
			"resumed_at_state": targetState,
		})
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "agent_no_show",
		observability.EmitOpts{Phone: sess.PhoneNumber})
	slog.Info("escalation returned to bot (agent no-show)",
		"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber), "state", targetState)
}

// handleAgentResume resumes a session from escalation at a specific state, optionally with corrected data.
func (p *MessageWorkerPool) handleAgentResume(ctx context.Context, sess *session.Session, cmd AgentCommand) {
	// COLA VENCIDA: Néctar devuelve la conversación porque nadie la tomó a tiempo. Se reconoce
	// SIEMPRE y ANTES de traducir el retorno contra el menú del paso: no lo eligió un agente en
	// ningún menú —lo declaró el barredor—, así que no pertenece a ningún paso. Da igual que
	// llegue como `return_value` a secas o como la `opcion` de un retorno v1 (ChosenOption).
	if cmd.ChosenOption() == statemachine.ReturnValueQueueExpired {
		p.handleQueueExpired(ctx, sess)
		return
	}

	// Decisión del agente en Néctar: opción del menú de retornos o dato escrito. Se traduce ANTES
	// del atajo de NOTIF_PENDING: es la traducción quien llena cmd.State y cmd.Data desde la
	// opción elegida, y el atajo se evalúa sobre ese comando ya ajustado (así `confirm` en Néctar
	// hace lo mismo que `/bot resume NOTIF_PENDING confirm`). Un comando de Bird no trae
	// ReturnValue ni ReturnV1, así que para él esto no hace nada.
	if cmd.ReturnV1 != nil || cmd.ReturnValue != "" {
		var done bool
		if cmd, done = p.applyAgentReturn(ctx, sess, cmd); done {
			return
		}
	}

	// Special case: NOTIF_PENDING — agent resolves the notification directly.
	// The pending notification was already removed from memory when escalation happened,
	// so we must use the session context instead of HandleResponse (which needs in-memory pending).
	// Las acciones son las de cada tipo de notificación: confirmación/reprogramación
	// (confirm/reschedule/cancel), cancelación (understood/reschedule) y lista de espera
	// (wl_schedule/wl_decline).
	if cmd.State == statemachine.StateNotifPending && cmd.Data != "" && p.notifyResponder != nil {
		action := strings.TrimSpace(cmd.Data)
		if notifPendingActions[action] {
			appointmentID := sess.GetContext("notif_appointment_id")
			notifType := sess.GetContext("notif_type")
			convID := sess.ConversationID
			if convID == "" {
				convID = sess.GetContext("notif_conv_id")
			}
			// Una acción de OTRO tipo (confirm sobre una lista de espera) no se ejecuta: la sesión
			// sigue escalada, sin completar ni desasignar.
			if valid := notifPendingActionsFor(notifType); !slices.Contains(valid, action) {
				p.refuseNotifPendingAction(ctx, sess, cmd, convID, notifType, action, valid)
				return
			}
			// N-26: loguear los errores descartados de Complete/UnassignFeedItem en lugar de tragarlos.
			if err := p.sessionManager.Complete(ctx, sess); err != nil {
				slog.Error("notif_pending: complete session failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", convID)
			}
			// La marca de la cola no sobrevive a una sesión completada (igual que en handleAgentClose).
			p.clearNextDayQueueMark(ctx, sess)
			if err := p.birdClient.UnassignFeedItem(cmd.Phone, convID, false); errors.Is(err, bird.ErrNectarBridgeOff) {
				// Vuelta a Bird: la conversación de Néctar no tiene canal. Es esperado, no una falla
				// (un Error iría a la alerta de Telegram).
				slog.Warn("notif_pending: unassign sin puente de Néctar", "error", err, "phone", utils.MaskPhone(cmd.Phone), "conversation_id", convID)
			} else if err != nil {
				slog.Error("notif_pending: unassign feed item failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "conversation_id", convID)
			}
			p.notifyResponder.HandleNotifPendingCommand(sess.PhoneNumber, action, convID, appointmentID, notifType,
				sess.GetContext(statemachine.CtxNotifWaitingListID))
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
	// Un destino sin handler rompería la sesión («ocurrió un error») en el siguiente turno, venga
	// de un comando de Bird o de un paso que no es un estado (el del kill switch): al saludo.
	if !p.machine.IsRegistered(targetState) {
		slog.Warn("retorno a un estado sin handler, se retoma en el saludo",
			"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "target_state", targetState)
		observability.Emit(observability.TraceSession(sess.ID), "escalacion", "retorno_estado_inexistente",
			observability.EmitOpts{Phone: sess.PhoneNumber, Reason: targetState})
		targetState = statemachine.StateGreeting
		// El dato (escrito o de la opción) era para el paso que no existe: inyectado como texto en
		// el saludo sería una respuesta a una pregunta que no se hizo.
		cmd.Data = ""
		cmd.DataIsPostback = false
		cmd.FreshTurn = false
	}

	// Resume session in DB
	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, targetState); err != nil {
		slog.Error("resume from escalation failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		return
	}
	// Las claves que la opción elegida manda olvidar se borran ANTES del turno: el paso al que se
	// vuelve no puede verlas. Si no se pudieran persistir, se borran igual en memoria para este
	// turno.
	if len(cmd.ClearCtx) > 0 {
		if err := p.sessionManager.SaveState(ctx, sess, targetState, nil, cmd.ClearCtx); err != nil {
			slog.Error("no se pudo borrar el contexto de la opción elegida", "error", err,
				"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID)
		}
		for _, k := range cmd.ClearCtx {
			delete(sess.Context, k)
		}
	}
	// La espera de esta escalación se acabó: una persona tomó el caso. Va ANTES del turno de
	// abajo, para que si ese turno vuelve a escalar no herede la marca.
	p.clearNextDayQueueMark(ctx, sess)
	p.clearEscalationCap(ctx, sess)

	// Unassign agent from Bird Inbox (bot takes over, keep item open)
	_ = p.birdClient.UnassignFeedItem(sess.PhoneNumber, sess.ConversationID, false)

	if cmd.Data != "" {
		// Agent provided corrected data — inject as virtual message.
		// En Néctar NO se acusa el retorno: el agente acaba de hablar con el paciente y el
		// mensaje de verdad sale en este mismo turno; un acuse pegado a él es un mensaje cobrado
		// que no le dice nada a nadie.
		if !session.IsNectarSession(sess.PhoneNumber) {
			_, _ = p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, "Hemos retomado tu atención. Procesando tu información...")
		}

		virtualMsg := bird.InboundMessage{
			ID:          fmt.Sprintf("agent-cmd-%s-%d", cmd.Phone, time.Now().UnixNano()),
			Phone:       sess.PhoneNumber,
			Text:        cmd.Data,
			MessageType: "text",
			ReceivedAt:  time.Now(),
		}
		// El Data de una opción del menú es la respuesta a un botón: viaja como el postback del
		// webhook (tipo, bandera y payload) y además como texto.
		if cmd.DataIsPostback {
			virtualMsg.MessageType = "postback"
			virtualMsg.IsPostback = true
			virtualMsg.PostbackPayload = cmd.Data
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
		if !session.IsNectarSession(sess.PhoneNumber) {
			_, _ = p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, "Hemos retomado tu atención. Continuamos con tu proceso.")
		}

		virtualMsg := bird.InboundMessage{
			ID:          fmt.Sprintf("agent-resume-%s-%d", cmd.Phone, time.Now().UnixNano()),
			Phone:       sess.PhoneNumber,
			Text:        "__resume__",
			MessageType: "text",
			ReceivedAt:  time.Now(),
		}
		// Una decisión nueva del agente no es «el bot vuelve al mismo punto»: sin `__resume__`, el
		// corte anti-bucle no se traga la escalación que el paso de destino pueda necesitar.
		if cmd.FreshTurn {
			virtualMsg.Text = ""
		}
		// En Néctar el paciente no escribió nada: el paso re-pregunta sin gastarle un reintento
		// (Process borra la marca al terminar). Bird sigue contando el `__resume__` como respuesta.
		if session.IsNectarSession(sess.PhoneNumber) {
			sess.SetContext(statemachine.CtxAgentResumeTurn, "1")
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
	_ = p.birdClient.UnassignFeedItem(sess.PhoneNumber, sess.ConversationID, false)

	// En Néctar el saludo sale en este mismo turno: un acuse pegado a él es un mensaje cobrado que
	// no le dice nada a nadie.
	if !session.IsNectarSession(sess.PhoneNumber) {
		_, _ = p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, "Hemos retomado tu atención. Vamos a comenzar de nuevo.")
	}

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
	// conversacion_resuelta (Reason no vacío) trae quién cerró y quién escaló: se guarda ANTES de
	// completar, porque Complete cierra la escalación abierta y después SetResolution ya no la
	// encuentra. Si falla, el cierre sigue: la autoría es un dato de informe, no del flujo.
	if cmd.Reason != "" && (cmd.ResolvedBy != "" || cmd.EscalatedBy != "") {
		if err := p.sessionManager.SetResolution(ctx, sess, cmd.ResolvedBy, cmd.EscalatedBy); err != nil {
			slog.Warn("no se pudo guardar quién cerró y quién escaló",
				"session_id", sess.ID, "phone", utils.MaskPhone(cmd.Phone), "error", err)
		}
	}
	// Una resuelta con motivo cola_vencida (declarado en el contrato, hoy no se emite) es «el asesor
	// no contestó»: misma marca que la cola vencida devuelta. Todo otro cierre es un cierre normal.
	complete := p.sessionManager.Complete
	if cmd.Reason == CloseReasonQueueExpired {
		complete = p.sessionManager.CompleteAsNoShow
	}
	if err := complete(ctx, sess); err != nil {
		slog.Error("agent close failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		return
	}
	// El caso se resolvió: la marca de la cola no sobrevive a una sesión completada.
	p.clearNextDayQueueMark(ctx, sess)

	if session.IsNectarSession(sess.PhoneNumber) {
		p.closeNectarConversation(sess, cmd)
	} else {
		_, _ = p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, agentCloseFarewell)

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
			if err := p.birdClient.CloseFeedItems(sess.PhoneNumber, closingConvID); err != nil {
				slog.Warn("close feed items on agent close failed", "conversation_id", closingConvID, "error", err)
			}
		}()
	}

	if p.tracker != nil {
		// session_completed además de escalation_closed: la sesión SÍ se completó (el agente la resolvió
		// y cerró). Sin él, el donut por status la cuenta como 'completed' pero el StatCard 'Completadas'
		// (que cuenta session_completed) y avg_session_duration la excluían → cifras incoherentes.
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "session_completed", nil)
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "escalation_closed", nil)
	}
	// El motivo de conversacion_resuelta (p. ej. cola_vencida) queda en la traza; vacío no viaja.
	var attrs map[string]interface{}
	if cmd.Reason != "" {
		attrs = map[string]interface{}{"motivo": cmd.Reason}
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "agent_closed",
		observability.EmitOpts{Phone: cmd.Phone, Attrs: attrs})
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
	_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, info)
}

// handleAgentOrder processes /bot orden commands — extracts CUPS from a text description using OCR AI.
//
// Devuelve "" si la orden se procesó, o el motivo del fallo en palabras del asesor. El aviso de
// siempre sale igual como nota interna; el motivo existe para el retorno de Néctar, donde la nota
// interna no tiene destino y el fallo tiene que volver al agente (applyAgentReturn).
func (p *MessageWorkerPool) handleAgentOrder(ctx context.Context, sess *session.Session, cmd AgentCommand) string {
	if cmd.Data == "" {
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID,
			"Uso: /bot orden <descripcion de la orden>\n"+
				"Ej: /bot orden Resonancia cerebral simple codigo 883141 cantidad 1\n"+
				"Ej: /bot orden Electromiografia 4 ext codigo 930810 cantidad 1, Resonancia columna lumbar codigo 883210 cantidad 1")
		return "No se escribió la descripción de la orden."
	}

	if p.ocrService == nil {
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, "Error: servicio OCR no disponible.")
		return "El servicio que lee la orden no está disponible; escriba los códigos CUPS o vuelva a intentarlo más tarde."
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
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, errMsg)
		return "No se pudieron extraer procedimientos de la descripción; revísela o escriba los códigos CUPS."
	}

	// Un procedimiento sin código CUPS no se retoma con el paciente: la asignación por nombre de
	// VALIDATE_OCR podría dejarlo en duda y la sesión volvería a escalar justo después de que el agente
	// la devolvió. El agente ve la orden: se le rechaza el dato pidiéndole el código.
	if missing := cupsWithoutCode(result.Cups); len(missing) > 0 {
		problem := "La descripción trae procedimientos sin código CUPS (" + strings.Join(missing, "; ") +
			"). Escriba el código CUPS de cada uno."
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, problem)
		return problem
	}

	// 2. Serialize CUPS to JSON and store in session
	cupsJSON, _ := json.Marshal(result.Cups)
	sess.SetContext("ocr_cups_json", string(cupsJSON))

	// 3. Resume at VALIDATE_OCR (same flow as image OCR success)
	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, statemachine.StateValidateOCR); err != nil {
		slog.Error("orden resume failed", "error", err, "phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "conversation_id", sess.ConversationID)
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, "Error al retomar sesion: "+err.Error())
		return agentCommandResumeProblem
	}
	p.clearNextDayQueueMark(ctx, sess)
	p.clearEscalationCap(ctx, sess)
	_ = p.birdClient.UnassignFeedItem(sess.PhoneNumber, sess.ConversationID, false)

	// 4. Send confirmation to agent (internal only)
	var summary strings.Builder
	summary.WriteString("Procedimientos extraidos:\n")
	for _, c := range result.Cups {
		fmt.Fprintf(&summary, "- %s: %s (x%d)\n", c.Code, c.Name, c.Quantity)
	}
	summary.WriteString("\nProcesando con el paciente...")
	_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, summary.String())

	// 5. Notify patient and trigger VALIDATE_OCR
	if !session.IsNectarSession(sess.PhoneNumber) {
		_, _ = p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID,
			"Hemos retomado tu atención. Verificando tu orden médica...")
	}

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
	return ""
}

// cupsWithoutCode son los nombres de los procedimientos leídos sin código CUPS.
func cupsWithoutCode(cups []services.CUPSEntry) []string {
	var out []string
	for _, c := range cups {
		if strings.TrimSpace(c.Code) == "" {
			out = append(out, strings.TrimSpace(c.Name))
		}
	}
	return out
}

// handleAgentCups injects CUPS codes directly without AI extraction.
// Syntax: /bot cups <code1>[:qty] <code2>[:qty] ...
// Example: /bot cups 883141 930810:2
// VALIDATE_OCR will automatically enrich names from the procedure DB.
//
// Devuelve "" si los CUPS se inyectaron, o el motivo del fallo en palabras del asesor (mismo
// contrato que handleAgentOrder).
func (p *MessageWorkerPool) handleAgentCups(ctx context.Context, sess *session.Session, cmd AgentCommand) string {
	if cmd.Data == "" {
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID,
			"Uso: /bot cups <codigo1>[:cantidad] <codigo2>[:cantidad] ...\n"+
				"Ej: /bot cups 883141\n"+
				"Ej: /bot cups 883141:1 930810:2")
		return "No se escribió ningún código CUPS."
	}

	// Cada token: "883141" o "883141:2". La gramática vive en services.ParseAgentCups, que
	// también valida el campo de CUPS del menú de retornos. El nombre queda vacío: VALIDATE_OCR
	// lo completa desde el catálogo de procedimientos.
	cups := services.ParseAgentCups(cmd.Data)

	if len(cups) == 0 {
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, "No se encontraron codigos CUPS validos en el comando.")
		return services.AgentCupsNoCodesProblem
	}
	// Mismo tope que el campo de CUPS de Néctar: se le rechaza al asesor y la sesión sigue con él.
	if problem := services.AgentCupsQuantityProblem(cups); problem != "" {
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, problem)
		return problem
	}

	// En Néctar, en UPLOAD_EMG_ORDER lo escrito es la orden de EMG: se SUMA a los dependientes de
	// la 1ª orden, como hace el handler con la foto (C5). En Bird, y fuera de ese paso, lo escrito
	// reemplaza, como siempre.
	depCupsJSON := sess.GetContext("emg_dep_cups_json")
	mergeEmg := session.IsNectarSession(sess.PhoneNumber) &&
		sess.GetContext("pre_escalation_state") == statemachine.StateUploadEmgOrder && depCupsJSON != ""
	if mergeEmg {
		mergedJSON, _, _ := services.MergeEmgAndDependentCups(cups, depCupsJSON)
		sess.SetContext("ocr_cups_json", mergedJSON)
		delete(sess.Context, "emg_dep_cups_json")
		// El resumen al agente lista lo que de verdad se va a validar: lo escrito más los
		// dependientes.
		var merged []services.CUPSEntry
		if err := json.Unmarshal([]byte(mergedJSON), &merged); err == nil {
			cups = merged
		}
	} else {
		cupsJSON, _ := json.Marshal(cups)
		sess.SetContext("ocr_cups_json", string(cupsJSON))
	}

	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, statemachine.StateValidateOCR); err != nil {
		slog.Error("cups inject resume failed", "error", err, "phone", utils.MaskPhone(cmd.Phone))
		_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, "Error al retomar sesion: "+err.Error())
		return agentCommandResumeProblem
	}
	// Los dependientes ya fusionados se borran también en la BD, como el handler de la foto. Y lo
	// que el campo del paso manda olvidar (ReturnInput.Clear: lo de los grupos de una orden que lo
	// escrito reemplaza) se borra antes del turno, también en memoria si no se pudo persistir.
	clearCtx := slices.Clone(cmd.ClearCtx)
	if mergeEmg {
		clearCtx = append(clearCtx, "emg_dep_cups_json")
	}
	if len(clearCtx) > 0 {
		if err := p.sessionManager.SaveState(ctx, sess, statemachine.StateValidateOCR, nil, clearCtx); err != nil {
			slog.Error("no se pudo borrar el contexto antes de validar los CUPS", "error", err,
				"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID)
		}
		for _, k := range cmd.ClearCtx {
			delete(sess.Context, k)
		}
	}
	p.clearNextDayQueueMark(ctx, sess)
	p.clearEscalationCap(ctx, sess)
	_ = p.birdClient.UnassignFeedItem(sess.PhoneNumber, sess.ConversationID, false)

	// Internal summary for agent
	var summary strings.Builder
	summary.WriteString("Codigos CUPS inyectados:\n")
	for _, c := range cups {
		fmt.Fprintf(&summary, "- %s (x%d)\n", c.Code, c.Quantity)
	}
	summary.WriteString("\nProcesando con el paciente...")
	_, _ = p.birdClient.SendInternalText(sess.PhoneNumber, sess.ConversationID, summary.String())

	// Notify patient and trigger VALIDATE_OCR
	if !session.IsNectarSession(sess.PhoneNumber) {
		_, _ = p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID,
			"Hemos retomado tu atención. Verificando tu orden médica...")
	}

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
	return ""
}

// PreEscalationFlush es el gancho de statemachine.Machine para Néctar: manda los mensajes
// acumulados (el aviso al paciente) ANTES del escalate, porque después la conversación ya
// es del agente. Para Bird devuelve false y el flujo sigue como siempre (envío tras el turno).
func (p *MessageWorkerPool) PreEscalationFlush(_ context.Context, sess *session.Session, messages []statemachine.OutboundMessage) bool {
	if !session.IsNectarSession(sess.PhoneNumber) {
		return false
	}
	// Con la cola del próximo día, el aviso GENÉRICO de conexión («te voy a conectar… un momento
	// por favor») contradice al del próximo día hábil que manda el handler: se quita, y SOLO él.
	// Lo demás del turno (la confirmación de una cita ya agendada, el motivo del caller) sale
	// como siempre. Se devuelve true aunque no quede nada: con false los mensajes saldrían tras
	// el escalate y morirían en 404.
	if p.escalationQueuedNextDay != nil && p.escalationQueuedNextDay(sess) {
		kept := statemachine.WithoutConnectionNotice(messages)
		if len(kept) != len(messages) {
			slog.Info("pre-escalation: aviso de conexión omitido, la escalación se encola para el próximo día",
				"phone", utils.MaskPhone(sess.PhoneNumber), "session_id", sess.ID)
		}
		messages = kept
	}
	for i, m := range statemachine.CoalesceMessages(messages) {
		if i > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		if _, err := p.sendMessage(sess.PhoneNumber, sess.ConversationID, m); err != nil {
			slog.Error("pre-escalation send error", "phone", utils.MaskPhone(sess.PhoneNumber),
				"session_id", sess.ID, "type", m.Type(), "error", err)
		}
	}
	return true
}

// sendAndSave sends result messages and persists state (shared between processMessage and agent commands).
func (p *MessageWorkerPool) sendAndSave(ctx context.Context, sess *session.Session, phone string, result *statemachine.StateResult) {
	// Coalescer: los mensajes acumulados del turno (auto-chain: texto de transición + prompt del
	// siguiente estado) se fusionan en el mínimo de envíos — Bird cobra cada uno por separado.
	result.Messages = statemachine.CoalesceMessages(result.Messages)

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
			// Errores PERMANENTES (4xx salvo 408/429, identificador no-E.164): reintentar es pagar
			// otro envío a Bird condenado al mismo fallo — se omite el retry.
			if bird.IsPermanentSendError(err) {
				slog.Warn("send retry skipped (permanent error)",
					"phone", utils.MaskPhone(phone), "type", outMsg.Type(), "error", err)
				// Identificador no contactable: marcar la sesión UNA vez (contexto persistido) y
				// emitir el flow_event para que dashboard/auditoría vean al paciente inalcanzable.
				if errors.Is(err, bird.ErrNonContactable) && sess.GetContext(ctxNonContactable) != "1" {
					sess.SetContext(ctxNonContactable, "1")
					if result.UpdateCtx == nil {
						result.UpdateCtx = map[string]string{}
					}
					result.UpdateCtx[ctxNonContactable] = "1"
					observability.Emit(observability.TraceSession(sess.ID), "infra", "non_contactable_identifier",
						observability.EmitOpts{Phone: phone})
				}
				continue
			}
			// E6: async retry (retryDelay) via Channels API para errores transitorios
			// (L16: wg-tracked → el shutdown lo espera/cancela)
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

	// Guardar el último texto que el bot envía al paciente, como contexto conversacional para la
	// capa de recuperación IA (le permite reaccionar a lo último que se le mostró al paciente).
	for i := len(result.Messages) - 1; i >= 0; i-- {
		if t := statemachine.OutboundText(result.Messages[i]); t != "" {
			if r := []rune(t); len(r) > 600 { // cap rune-safe (columna de contexto)
				t = string(r[:600])
			}
			if result.UpdateCtx == nil {
				result.UpdateCtx = map[string]string{}
			}
			result.UpdateCtx[statemachine.CtxLastBotMsg] = t
			break
		}
	}

	// Persist state + context.
	//
	// El guardado corre con SU PROPIO contexto, desacoplado del presupuesto del mensaje. Auditoría
	// 2026-07-27: cuando el handler agotaba los 2 min del mensaje (OCR colgado), TODA la persistencia
	// posterior heredaba un contexto ya vencido y fallaba con "context deadline exceeded" — el paciente
	// perdía el estado de su sesión y quedaba reintentando el mismo paso. El estado es lo último que se
	// debe perder: aunque el mensaje se pasara de tiempo, lo ya decidido se persiste.
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), persistStateTimeout)
	defer persistCancel()

	clearAll := false
	for _, k := range result.ClearCtx {
		if k == "__all__" {
			clearAll = true
			break
		}
	}

	if clearAll {
		if err := p.sessionManager.ClearAllContext(persistCtx, sess); err != nil {
			slog.Error("clear all context error", "phone", utils.MaskPhone(phone), "session_id", sess.ID, "conversation_id", convID, "error", err)
		}
		result.ClearCtx = nil
	}

	if err := p.sessionManager.SaveState(persistCtx, sess, result.NextState, result.UpdateCtx, result.ClearCtx); err != nil {
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
		// Contexto propio también aquí: el rescate se invoca JUSTO cuando el guardado falló, y si el
		// motivo fue un deadline compartido, reusarlo hacía que el rescate fallara siempre.
		if completeErr := p.sessionManager.Complete(persistCtx, sess); completeErr != nil {
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

	// VENCIMIENTO EXTENDIDO: el paso pidió que la sesión siga viva hasta un instante concreto —la
	// escalación encolada para el próximo día hábil, que con el TTL normal vence de madrugada—.
	// Va DESPUÉS del guardado y solo si la sesión NO quedó cerrada: si SaveState falló, el
	// rescate de arriba la completó y no hay nada que mantener vivo. Un fallo aquí no cambia nada
	// de lo que ve el paciente, así que se registra y se sigue.
	if !result.ExtendExpiryUntil.IsZero() && sess.Status != session.StatusCompleted {
		if err := p.sessionManager.ExtendExpiry(persistCtx, sess, result.ExtendExpiryUntil); err != nil {
			slog.Error("extend_expiry_error",
				"phone", utils.MaskPhone(phone), "session_id", sess.ID,
				"until", result.ExtendExpiryUntil, "error", err)
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
			if err := p.birdClient.CloseFeedItems(sess.PhoneNumber, closingConvID); err != nil {
				slog.Warn("close feed items on completion failed", "phone", utils.MaskPhone(phone), "conversation_id", closingConvID, "error", err)
			}
		}()
	}

	// Log events
	if p.tracker != nil && len(result.Events) > 0 {
		p.tracker.LogBatch(ctx, sess.ID, phone, result.Events)
	}
	logResultEvents(sess.ID, phone, result.Events)
}

// logResultEvents deja en el log INFO cada evento con sus datos redactados (H2); los eventos
// que van a chat_events no cambian.
func logResultEvents(sessionID, phone string, events []statemachine.Event) {
	for _, event := range events {
		slog.Info(
			"event",
			"session_id", sessionID,
			"phone", utils.MaskPhone(phone),
			"type", event.Type,
			"data", utils.RedactLogMap(event.Data),
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
	case <-time.After(p.retryDelay):
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

// HandleAgentTakeover pausa el bot cuando un agente humano responde manualmente una conversación
// cuya sesión está ACTIVA (sin escalación previa): la marca escalada para que el bot deje de
// procesar (los mensajes del paciente van al Inbox, como en cualquier escalación) y el agente
// pueda devolverla con /bot resume. Sin esto, bot y agente respondían a la vez.
func (p *MessageWorkerPool) HandleAgentTakeover(phone string) {
	if p.sessionManager == nil {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	defer cancel()
	sess, err := p.sessionManager.FindForAgentCommand(ctx, phone)
	if err != nil || sess == nil || sess.Status != session.StatusActive {
		return
	}
	if err := p.sessionManager.Escalate(ctx, sess, "manual_takeover"); err != nil {
		slog.Error("agent takeover escalate failed", "phone", utils.MaskPhone(phone), "session_id", sess.ID, "error", err)
		return
	}
	slog.Warn("agent takeover: bot pausado por intervención manual del agente",
		"phone", utils.MaskPhone(phone), "session_id", sess.ID, "state", sess.CurrentState)
	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, phone, "agent_takeover_detected", map[string]interface{}{
			"state": sess.CurrentState,
		})
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "agent_takeover",
		observability.EmitOpts{Phone: phone, Attrs: map[string]interface{}{"state": sess.CurrentState}})
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
			p.runStaleReplayPass(ctx, src)
		}
	}
}

// runStaleReplayPass es UNA pasada del replay de atascados, con plazo propio (D-07): con MySQL
// colgado la pasada termina y la siguiente vuelve a intentar.
func (p *MessageWorkerPool) runStaleReplayPass(ctx context.Context, src StalePendingSource) {
	utils.RunPass(ctx, "stale-replay", p.passTimeout, func(ctx context.Context) {
		p.replayStale(ctx, src)
	})
}

// replayStale recoge los mensajes 'pending' viejos y los re-encola. Separado para poder testearlo.
func (p *MessageWorkerPool) replayStale(ctx context.Context, src StalePendingSource) {
	msgs, err := src.PendingOlderThan(ctx, staleReplayMinutes)
	if err != nil {
		if ctx.Err() == nil { // pasada vencida: el error ya lo deja RunPass
			slog.Error("stale replay: query failed", "error", err)
		}
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
			p.purgeStaleLockMisses(now)
		}
	}
}
