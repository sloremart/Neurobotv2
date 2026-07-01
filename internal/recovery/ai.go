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

// systemPrompt fija el rol acotado, el contrato JSON de salida (claves cortas) y las reglas de
// seguridad. Es el prefijo estable por estado.
func systemPrompt(req Request) string {
	var b strings.Builder
	b.WriteString("Eres un asistente que SOLO reformatea la respuesta de un paciente al formato exacto ")
	b.WriteString("que espera un bot de agendamiento médico. No agendas, no das consejo médico, no inventas datos.\n")
	b.WriteString("Formato esperado para este paso: ")
	b.WriteString(req.Hint)
	b.WriteString("\n")
	if len(req.Options) > 0 {
		b.WriteString("Opciones válidas: ")
		b.WriteString(strings.Join(req.Options, ", "))
		b.WriteString("\n")
	}
	b.WriteString("REGLAS:\n")
	b.WriteString("- Si puedes mapear la respuesta al formato exacto, responde ok=true y v=<valor exacto>.\n")
	b.WriteString("- Si NO puedes, responde ok=false y m=<un mensaje breve y claro, una sola pregunta, ")
	b.WriteString("con el formato/opciones exactas, máx 2 líneas> en español.\n")
	b.WriteString("- NUNCA infieras datos clínicos sensibles que el paciente no dio explícitamente.\n")
	b.WriteString("- Responde SOLO un JSON con las claves: ")
	b.WriteString(`{"ok":bool,"v":string,"c":{},"m":string,"r":string}. `)
	b.WriteString("r es un código corto (ej: ambiguous, off_topic, empty). No agregues texto fuera del JSON.")
	return b.String()
}

// userPrompt lleva el contexto mínimo: últimos mensajes + la respuesta a interpretar.
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
	fmt.Fprintf(&b, "Respuesta del paciente a reformatear: %q", req.Input)
	return b.String()
}
