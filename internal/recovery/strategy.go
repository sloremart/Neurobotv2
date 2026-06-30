package recovery

import "context"

// Request es el contexto que el coordinador pasa a una estrategia para que decida qué hacer con un
// input bloqueado. No contiene PII más allá del input del paciente (que la estrategia procesa pero
// no debe loguear).
type Request struct {
	BlockedState string            // estado de la FSM donde el paciente quedó bloqueado
	Input        string            // texto del paciente a interpretar
	Hint         string            // HandlerConfig.AIInputHint: formato/opciones esperadas
	Options      []string          // opciones válidas (estados de selección), si aplica
	Validate     func(string) bool // validador puro del estado (mismo que usa el bot)
	History      []string          // últimos 1-2 mensajes (contexto mínimo)
	CarryKeys    map[string]string // mapeo dato_adelantado → clave de contexto de sesión
}

// Result es la decisión de una estrategia sobre un input bloqueado.
type Result struct {
	Handled  bool              // la estrategia tomó el control de este intento
	Value    string            // valor a inyectar por machine.Process (si formateó)
	Message  string            // mensaje aclaratorio al paciente (si no formateó)
	Carry    map[string]string // dato adelantado a persistir en sesión
	Escalate bool              // pide escalar a humano (fallback)
	Reason   string            // código corto para KPIs/auditoría (no PII)
}

// Strategy intenta recuperar un input bloqueado. Implementaciones: AIRecovery (LLM) y
// HumanEscalation (fallback). El coordinador las prueba en orden.
type Strategy interface {
	Name() string
	Try(ctx context.Context, req Request) Result
}

// HumanEscalation es la estrategia de fallback: no intenta recuperar, pide escalar a un agente
// humano (el comportamiento actual del bot). Siempre disponible como último recurso.
type HumanEscalation struct{}

// Name implementa Strategy.
func (HumanEscalation) Name() string { return "human_escalation" }

// Try implementa Strategy: siempre solicita escalación.
func (HumanEscalation) Try(_ context.Context, _ Request) Result {
	return Result{Handled: true, Escalate: true, Reason: "fallback"}
}
