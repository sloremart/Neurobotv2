// Package recovery implementa la capa de recuperación asistida por IA: cuando el bot agota sus
// reintentos en un estado de texto libre, intenta desbloquear el input con el LLM antes de escalar
// a un agente humano. Ver docs/RECUPERACION-IA.md.
package recovery

// Decision es la salida estructurada del LLM por cada intento de recuperación.
// Tags JSON cortos a propósito: cada token de salida cuesta ~4× un token de entrada (§7.1).
type Decision struct {
	OK     bool              `json:"ok"`          // formateó con éxito
	Value  string            `json:"v"`           // valor a inyectar por machine.Process ("" si no)
	Carry  map[string]string `json:"c,omitempty"` // dato adelantado (opcional)
	Msg    string            `json:"m"`           // mensaje al paciente (claro y breve)
	Reason string            `json:"r"`           // código corto, no prosa: "num_doc"|"ambiguous"|...
}
