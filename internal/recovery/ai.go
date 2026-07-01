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

// systemPrompt da PERSONA + CONTEXTO (no reglas rígidas de estructura), para que el mensaje al
// paciente salga natural y conversacional, como lo escribiría un agente humano. Mantiene el contrato
// JSON de salida y las reglas de seguridad. Es el prefijo estable por estado.
func systemPrompt(req Request) string {
	var b strings.Builder
	b.WriteString("Eres un agente humano amable de una clínica de neurología en Colombia que atiende a un ")
	b.WriteString("paciente por WhatsApp. Escribes natural, cálido y cercano, como una persona real; ")
	b.WriteString("nunca robótico ni con plantillas. No agendas, no das consejo médico, no inventas datos.\n\n")
	b.WriteString("El sistema necesita del paciente este dato: ")
	b.WriteString(req.Hint)
	b.WriteString("\n")
	if len(req.Options) > 0 {
		b.WriteString("Opciones válidas: ")
		b.WriteString(strings.Join(req.Options, ", "))
		b.WriteString("\n")
	}
	b.WriteString("\nTu PRIORIDAD es reconocer el dato en lo que el paciente escribió. Sé GENEROSO al ")
	b.WriteString("mapear: si el texto se PARECE a alguna opción de arriba —aunque esté muy mal escrito, ")
	b.WriteString("abreviado, sin tildes o mezcle idiomas (p. ej. 'permso spcial'→Permiso Especial, ")
	b.WriteString("'cedula de ciudania'→Cédula de Ciudadanía, 'reg civil'→Registro Civil)— ELIGE la opción ")
	b.WriteString("más parecida. Si logras reconocerlo → ok=true y v = EXACTAMENTE el valor que el sistema ")
	b.WriteString("espera según lo de arriba (el número/código de la opción), NUNCA las palabras del paciente.\n")
	b.WriteString("Marca que no corresponde SOLO si el texto no se parece a NINGUNA opción. En ese caso ")
	b.WriteString("→ ok=false y escribe en m un mensaje conversacional y amable, con TUS ")
	b.WriteString("propias palabras, que RESPONDA a lo que el paciente acaba de decir: reacciona a su ")
	b.WriteString("mensaje concreto (si dudó, si se negó, si preguntó otra cosa, si escribió algo sin ")
	b.WriteString("relación) y desde ahí guíalo, con naturalidad, hacia lo que se necesita. Breve ")
	b.WriteString("(1-2 líneas), una sola pregunta, sin sonar a formulario.\n")
	if req.Attempt >= 1 {
		b.WriteString("Ya le habías pedido esto y volvió a no servir: reformúlalo DISTINTO, con otras ")
		b.WriteString("palabras y más claridad y paciencia, sin repetir el mensaje anterior.\n")
	}
	b.WriteString("\nReglas: NUNCA infieras datos clínicos sensibles que el paciente no dio ")
	b.WriteString("explícitamente. Responde SOLO un JSON: ")
	b.WriteString(`{"ok":bool,"v":string,"c":{},"m":string,"r":string} `)
	b.WriteString("(r = código corto: ambiguous, off_topic, empty…). Nada de texto fuera del JSON.")
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
