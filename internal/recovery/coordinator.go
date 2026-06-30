package recovery

import (
	"context"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// sessionCtxRecoveryActive marca que la sesión está en modo recuperación; su valor es el estado
// bloqueado (para inyectar después en el handler correcto). sessionCtxRecoveryAttempts lleva el
// contador de intentos del paciente con la IA (separado de RetryCount del bot).
const (
	CtxRecoveryActive   = "ai_recovery_active"
	CtxRecoveryAttempts = "ai_recovery_attempts"
)

// Coordinator orquesta la recuperación: prueba la estrategia de IA y, como fallback, la escalación
// humana. Mantiene la FSM limpia (se invoca vía la interfaz sm.RecoveryCoordinator) y aísla la
// dependencia del LLM. En Fase 1 es un esqueleto no-op (no desvía nada → comportamiento actual).
type Coordinator struct {
	machine *sm.Machine
	human   Strategy // fallback de escalación humana
	enabled bool     // AI_RECOVERY_ENABLED
}

// NewCoordinator crea el coordinador. En Fase 1 la estrategia de IA es nil.
func NewCoordinator(machine *sm.Machine, enabled bool) *Coordinator {
	return &Coordinator{
		machine: machine,
		human:   HumanEscalation{},
		enabled: enabled,
	}
}

// Active indica si la sesión está en modo recuperación (lo consulta el interceptor).
func (c *Coordinator) Active(sess *session.Session) bool {
	return sess.GetContext(CtxRecoveryActive) != ""
}

// Handle procesa un mensaje del paciente mientras el modo recuperación está activo.
// Fase 1: no-op (la lógica de IA llega en Fase 2).
func (c *Coordinator) Handle(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, bool) {
	return nil, false
}

// TryStart intenta iniciar la recuperación en lugar de escalar (guard del handler de escalación).
// Fase 1: no-op → retorna (nil, false) para que la escalación proceda como hoy.
func (c *Coordinator) TryStart(_ context.Context, _ *session.Session, _ bird.InboundMessage, _ string) (*sm.StateResult, bool) {
	return nil, false
}

// Verificación en tiempo de compilación de que Coordinator implementa la interfaz de la FSM.
var _ sm.RecoveryCoordinator = (*Coordinator)(nil)
