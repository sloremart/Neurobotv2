package recovery

import "context"

// Request es el contexto que el coordinador pasa a una estrategia para interpretar un input
// bloqueado. No se loguea el Input (PII potencial).
type Request struct {
	BlockedState string   // estado de la FSM donde el paciente quedó bloqueado
	Input        string   // texto del paciente a interpretar
	Hint         string   // HandlerConfig.AIInputHint: formato/opciones esperadas
	Options      []string // opciones válidas (estados de selección), si aplica
	History      []string // últimos 1-2 mensajes (contexto mínimo)
	Attempt      int      // # de aclaración: 0 = primer intento; 1+ = reintentos (para escalar claridad)
}

// Result es la decisión de una estrategia sobre un input bloqueado.
type Result struct {
	Handled  bool              // la estrategia tomó el control de este intento
	ByBot    bool              // resuelto por el validador puro (sin LLM)
	Value    string            // valor a inyectar por machine.Process (si formateó)
	Message  string            // mensaje aclaratorio al paciente (si no formateó)
	Carry    map[string]string // dato adelantado (claves crudas del LLM; el coordinador las mapea)
	Escalate bool              // pide escalar a humano (fallback)
	Reason   string            // código corto para KPIs/auditoría (no PII)
	Usage    Usage             // tokens consumidos (para KPIs/costo)
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
