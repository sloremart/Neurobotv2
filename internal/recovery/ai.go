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
	b.WriteString("Un paciente está respondiendo por WhatsApp a un bot de una clínica de neurología. ")
	b.WriteString("El sistema necesita este dato:\n")
	b.WriteString(req.Hint)
	b.WriteString("\n")
	if len(req.Options) > 0 {
		b.WriteString("Opciones válidas: ")
		b.WriteString(strings.Join(req.Options, ", "))
		b.WriteString("\n")
	}
	b.WriteString("\nTu tarea: según el mensaje del paciente, identifica cuál de esas opciones está ")
	b.WriteString("eligiendo, aunque lo escriba con palabras, sinónimos, incompleto, sin tildes o mal ")
	b.WriteString("escrito. Elige la opción cuyo significado coincida con lo que quiso decir.\n")
	b.WriteString("- Si identificas la opción → ok=true y v = el valor exacto que espera el sistema (el ")
	b.WriteString("número de la opción), nunca las palabras del paciente.\n")
	b.WriteString("- Si el mensaje de verdad no corresponde a ninguna opción, o se niega o pregunta otra ")
	b.WriteString("cosa → ok=false y en m escribe, como un agente humano cálido y natural (no robótico), ")
	b.WriteString("una respuesta breve a lo que el paciente acaba de decir que lo invite a elegir de la lista.\n")
	if req.Attempt >= 1 {
		b.WriteString("Ya se lo pediste antes y no sirvió: en m reformula distinto, con más claridad, sin repetir.\n")
	}
	b.WriteString("\nNo inventes ni infieras datos clínicos que el paciente no dio. Responde SOLO un JSON: ")
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
