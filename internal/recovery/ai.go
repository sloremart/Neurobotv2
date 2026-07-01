package recovery

import (
	"context"
	"fmt"
	"strings"
)

// AIRecovery interpreta el input bloqueado con el LLM (gpt-4.1-nano). Devuelve un valor a inyectar
// (si pudo formatear) o un mensaje aclaratorio para el paciente. La regla de dominio (no inferir
// datos sensibles) se aplica antes vía HandlerConfig.AIDomainBlock en el coordinador.
type AIRecovery struct {
	llm *LLMClient
}

// NewAIRecovery crea la estrategia de IA.
func NewAIRecovery(llm *LLMClient) *AIRecovery {
	return &AIRecovery{llm: llm}
}

// Name implementa Strategy.
func (*AIRecovery) Name() string { return "ai_recovery" }

// Try implementa Strategy: una sola llamada que interpreta el input y, a la vez, redacta el mensaje
// aclaratorio "en reserva". Si formateó, el coordinador usa Value y descarta Message; si no, envía
// Message. Ante error del LLM retorna Handled=false para que el coordinador caiga al fallback.
func (a *AIRecovery) Try(ctx context.Context, req Request) Result {
	dec, usage, err := a.llm.Complete(ctx, systemPrompt(req), userPrompt(req))
	if err != nil {
		return Result{Handled: false, Reason: "llm_error", Usage: usage}
	}
	res := Result{
		Handled: true,
		Message: dec.Msg,
		Carry:   dec.Carry,
		Reason:  dec.Reason,
		Usage:   usage,
	}
	if dec.OK {
		res.Value = strings.TrimSpace(dec.Value)
	}
	return res
}

// systemPrompt enmarca la tarea como una SELECCIÓN limpia ("¿qué opción está eligiendo el
// paciente?") con las opciones como lista, sin ejemplos que distraigan. Persona humana solo para el
// mensaje de fallback. Mantiene el contrato JSON y las reglas de seguridad.
func systemPrompt(req Request) string {
	var b strings.Builder
	b.WriteString("Eres un agente de una clínica de neurología en Colombia atendiendo a un paciente por ")
	b.WriteString("WhatsApp. El bot le pidió UN dato y el paciente respondió algo que el sistema no pudo ")
	b.WriteString("procesar. Tu tarea es entender qué quiso decir y entregar ese dato en el formato EXACTO ")
	b.WriteString("que el sistema necesita.\n\nDato requerido:\n")
	b.WriteString(req.Hint)
	b.WriteString("\n")
	if len(req.Options) > 0 {
		b.WriteString("Opciones válidas: ")
		b.WriteString(strings.Join(req.Options, ", "))
		b.WriteString("\n")
	}
	b.WriteString("\nCómo decidir:\n")
	b.WriteString("- Interpreta con flexibilidad: el paciente puede escribir en palabras, con sinónimos, ")
	b.WriteString("sin tildes, mal escrito, incompleto o desordenado.\n")
	b.WriteString("- Si logras obtener el dato → ok=true y v = EXACTAMENTE el valor en el formato que indica ")
	b.WriteString("'Dato requerido' (SOLO el valor final: sin etiquetas, sin explicaciones, sin el texto del ")
	b.WriteString("paciente tal cual). Respeta el formato de ese dato (no quites ni agregues nada que ese ")
	b.WriteString("formato requiera). Si el dato admite un valor especial (p. ej. NA cuando el paciente dice ")
	b.WriteString("que no tiene), úsalo.\n")
	b.WriteString("- Si el mensaje NO permite obtener el dato con seguridad (se niega, pregunta otra cosa, no ")
	b.WriteString("lo aporta o es ambiguo) → ok=false y en m escribe, como una persona real cálida y natural, ")
	b.WriteString("una respuesta breve a lo que el paciente dijo que lo oriente a darlo. No inventes.\n")
	b.WriteString("- Nunca infieras datos clínicos o de identidad que el paciente no haya dado explícitamente.\n")
	if req.Attempt >= 1 {
		b.WriteString("Ya se lo pediste antes y no sirvió: en m reformula distinto, con más claridad, sin repetir.\n")
	}
	b.WriteString("\nResponde SOLO un JSON: ")
	b.WriteString(`{"ok":bool,"v":string,"m":string,"r":string} `)
	b.WriteString("(r = código corto del motivo). Nada de texto fuera del JSON.")
	return b.String()
}

// userPrompt lleva el contexto mínimo: nº de intento + lo que respondió el paciente.
func userPrompt(req Request) string {
	var b strings.Builder
	if len(req.History) > 0 {
		b.WriteString("Contexto reciente:\n")
		for _, h := range req.History {
			b.WriteString("- ")
			b.WriteString(h)
			b.WriteString("\n")
		}
	}
	if req.Attempt >= 1 {
		fmt.Fprintf(&b, "(Ya le pediste esto %d vez/veces y no sirvió.)\n", req.Attempt)
	}
	fmt.Fprintf(&b, "El paciente respondió: %q", req.Input)
	return b.String()
}
