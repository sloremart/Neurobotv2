package recovery

import (
	"context"
	"strconv"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Claves de contexto de sesión del modo recuperación. CtxRecoveryActive guarda el estado bloqueado
// (para inyectar después en el handler correcto); CtxRecoveryAttempts el contador de intentos del
// paciente con la IA (separado de RetryCount del bot).
const (
	CtxRecoveryActive   = "ai_recovery_active"
	CtxRecoveryAttempts = "ai_recovery_attempts"
)

// MonthlyCounter controla el tope mensual de recuperaciones (AI_RECOVERY_MONTHLY_LIMIT). nil = sin
// límite. TryReserve incrementa y reporta si aún hay cupo este mes.
type MonthlyCounter interface {
	TryReserve(ctx context.Context) (allowed bool, err error)
}

// Config parametriza el coordinador (desde variables de entorno).
type Config struct {
	Enabled            bool
	MaxPatientAttempts int
	Monthly            MonthlyCounter // nil = sin tope mensual
}

// Coordinator orquesta la recuperación: prueba la estrategia de IA y, como fallback, la escalación
// humana. Se invoca vía la interfaz sm.RecoveryCoordinator (mantiene la FSM limpia y aísla el LLM).
type Coordinator struct {
	machine     *sm.Machine
	ai          Strategy // estrategia de IA; nil = sin IA (no recupera)
	human       Strategy // fallback de escalación humana
	enabled     bool
	maxAttempts int
	monthly     MonthlyCounter
}

// NewCoordinator crea el coordinador. ai puede ser nil (capa sin IA → nunca recupera).
func NewCoordinator(machine *sm.Machine, ai Strategy, cfg Config) *Coordinator {
	maxAttempts := cfg.MaxPatientAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	return &Coordinator{
		machine:     machine,
		ai:          ai,
		human:       HumanEscalation{},
		enabled:     cfg.Enabled,
		maxAttempts: maxAttempts,
		monthly:     cfg.Monthly,
	}
}

// Active indica si la sesión está en modo recuperación (lo consulta el interceptor).
func (c *Coordinator) Active(sess *session.Session) bool {
	return sess.GetContext(CtxRecoveryActive) != ""
}

// TryStart intenta iniciar la recuperación en lugar de escalar (guard del handler de escalación).
// Retorna (result, true) si toma el control; (nil, false) para escalar de verdad.
func (c *Coordinator) TryStart(ctx context.Context, sess *session.Session, msg bird.InboundMessage, blockedState string) (*sm.StateResult, bool) {
	if !c.enabled || c.ai == nil {
		return nil, false
	}
	cfg, ok := c.machine.Config(blockedState)
	if !ok || !cfg.AIRecovery {
		return nil, false
	}
	// Tope mensual: si se alcanzó, no se actúa y se escala directo.
	if c.monthly != nil {
		allowed, err := c.monthly.TryReserve(ctx)
		if err == nil && !allowed {
			c.emit(sess, "ai_month_cap_reached", "month_cap", Usage{})
			return nil, false
		}
	}
	c.emit(sess, "ai_recovery_started", "", Usage{})

	// Deshacer el cambio de estado que hizo el auto-chain hacia ESCALATE_TO_AGENT: quedamos
	// congelados en el estado bloqueado para inyectar/validar contra el handler correcto.
	sess.CurrentState = blockedState

	res := c.evaluate(ctx, sess, cfg, blockedState, msg.Text)
	if !res.Handled {
		return nil, false // error del LLM → escalar
	}
	if res.Value != "" {
		if out, ok := c.inject(ctx, sess, blockedState, res, msg); ok {
			c.emitResolved(sess, res)
			return out, true
		}
		res.Value = "" // inyección falló (no pasó validación) → tratar como aclaración
	}
	if res.Message == "" {
		return nil, false // sin mensaje que enviar → escalar
	}
	// Primer consumo sin formatear: NO cuenta intento (attempts=0).
	c.emit(sess, "ai_clarified", res.Reason, res.Usage)
	return c.clarify(blockedState, res, 0), true
}

// Handle procesa cada mensaje del paciente mientras el modo recuperación está activo (interceptor).
func (c *Coordinator) Handle(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, bool) {
	blockedState := sess.GetContext(CtxRecoveryActive)
	if blockedState == "" {
		return nil, false
	}
	cfg, ok := c.machine.Config(blockedState)
	if !ok {
		return nil, false
	}

	res := c.evaluate(ctx, sess, cfg, blockedState, msg.Text)
	if res.Handled && res.Value != "" {
		if out, ok := c.inject(ctx, sess, blockedState, res, msg); ok {
			c.emitResolved(sess, res)
			return out, true
		}
		res.Value = ""
	}

	// Falló este intento del paciente.
	attempts := parseAttempts(sess.GetContext(CtxRecoveryAttempts)) + 1
	if !res.Handled || res.Message == "" || attempts >= c.maxAttempts {
		// Presupuesto agotado (o error LLM) → fallback de escalación humana. Se deja el flag activo
		// a propósito: el guard del handler de escalación lo detectará, escalará de verdad y limpiará
		// los flags.
		esc := c.human.Try(ctx, Request{BlockedState: blockedState})
		c.emit(sess, "ai_failed", esc.Reason, res.Usage)
		return sm.NewResult(sm.StateEscalateToAgent), true
	}
	// Aún hay presupuesto: segundo mensaje aclaratorio.
	c.emit(sess, "ai_clarified", res.Reason, res.Usage)
	return c.clarify(blockedState, res, attempts), true
}

// evaluate corre el validador puro del bot, luego la regla de dominio, luego el LLM.
func (c *Coordinator) evaluate(ctx context.Context, sess *session.Session, cfg sm.HandlerConfig, blockedState, input string) Result {
	// 1. Validador puro del bot (gratis): si pasa, lo resuelve el bot sin LLM.
	if cfg.TextValidate != nil && cfg.TextValidate(input) {
		return Result{Handled: true, ByBot: true, Value: input, Reason: "bot_validate"}
	}
	// 2. Regla de dominio (p. ej. no inferir tipo de documento desde el número).
	if cfg.AIDomainBlock != nil {
		if blocked, message, _ := cfg.AIDomainBlock(input); blocked {
			c.emit(sess, "ai_domain_block", "domain", Usage{})
			return Result{Handled: true, Value: "", Message: message, Reason: "domain"}
		}
	}
	// 3. LLM.
	return c.ai.Try(ctx, Request{
		BlockedState: blockedState,
		Input:        input,
		Hint:         cfg.AIInputHint,
		Options:      cfg.Options,
	})
}

// inject corre el valor recuperado por machine.Process (misma validación que el bot). Limpia el modo
// recuperación antes para que el interceptor no re-entre. Retorna (result, true) si el estado avanzó.
func (c *Coordinator) inject(ctx context.Context, sess *session.Session, blockedState string, res Result, orig bird.InboundMessage) (*sm.StateResult, bool) {
	delete(sess.Context, CtxRecoveryActive)
	delete(sess.Context, CtxRecoveryAttempts)
	sess.CurrentState = blockedState

	vmsg := bird.InboundMessage{
		Phone:          sess.PhoneNumber,
		Text:           res.Value,
		MessageType:    "text",
		ReceivedAt:     orig.ReceivedAt,
		ConversationID: orig.ConversationID,
	}
	out, err := c.machine.Process(ctx, sess, vmsg)
	if err != nil || out == nil {
		return nil, false
	}
	if out.NextState == blockedState {
		return nil, false // no avanzó → el valor no pasó la validación
	}
	out.WithClearCtx(CtxRecoveryActive, CtxRecoveryAttempts)
	return out, true
}

// clarify construye el resultado que envía el mensaje aclaratorio y mantiene el modo activo.
func (c *Coordinator) clarify(blockedState string, res Result, attempts int) *sm.StateResult {
	return sm.NewResult(blockedState).
		WithContext(CtxRecoveryActive, blockedState).
		WithContext(CtxRecoveryAttempts, strconv.Itoa(attempts)).
		WithText(res.Message)
}

// emitResolved emite el evento de éxito según haya resuelto el validador puro o la IA.
func (c *Coordinator) emitResolved(sess *session.Session, res Result) {
	step := "ai_recovered"
	if res.ByBot {
		step = "ai_resolved_by_bot"
	}
	c.emit(sess, step, res.Reason, res.Usage)
}

func (c *Coordinator) emit(sess *session.Session, step, reason string, u Usage) {
	observability.Emit(observability.TraceSession(sess.ID), "recuperacion", step, observability.EmitOpts{
		Phone:  sess.PhoneNumber,
		Reason: reason,
		Attrs:  map[string]interface{}{"in": u.PromptTokens, "out": u.CompletionTokens, "calls": u.Calls},
	})
}

func parseAttempts(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Verificación en tiempo de compilación de que Coordinator implementa la interfaz de la FSM.
var _ sm.RecoveryCoordinator = (*Coordinator)(nil)
