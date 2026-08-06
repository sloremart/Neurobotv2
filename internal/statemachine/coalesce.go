// Package statemachine implementa la máquina de estados conversacional del bot: estados,
// handlers, interceptores, el auto-chain de estados automáticos y la fusión de mensajes salientes.
package statemachine

// Límites CONSERVADORES de WhatsApp para fusionar (los reales: 4096 texto, 1024 body interactivo).
// Si la fusión excedería el tope, se dejan los mensajes separados: mejor 2 envíos que un 131009.
const (
	coalesceTextMax        = 3800
	coalesceInteractiveMax = 950
)

// CoalesceMessages fusiona los mensajes acumulados de UN turno (el auto-chain de machine.go
// concatena "texto de transición" + "prompt del siguiente estado" = 2-3 envíos cobrados por Bird
// con la misma información). Reglas:
//   - Textos consecutivos → un solo texto separado por línea en blanco.
//   - Texto(s) inmediatamente ANTES de botones/lista → se anteponen a su body (el patrón que el
//     saludo ya usaba a mano: greeting.go concatena la bienvenida al body del menú).
//   - Nunca fusiona hacia atrás (nada se pega DESPUÉS de un interactivo) ni interactivo+interactivo.
//   - No muta los mensajes originales.
func CoalesceMessages(msgs []OutboundMessage) []OutboundMessage {
	if len(msgs) < 2 {
		return msgs
	}

	out := make([]OutboundMessage, 0, len(msgs))
	pending := "" // textos acumulados aún no colocados

	flush := func() {
		if pending != "" {
			out = append(out, &TextMessage{Text: pending})
			pending = ""
		}
	}

	for _, m := range msgs {
		switch v := m.(type) {
		case *TextMessage:
			switch {
			case pending == "":
				pending = v.Text
			case len([]rune(pending))+len([]rune(v.Text))+2 <= coalesceTextMax:
				pending += "\n\n" + v.Text
			default:
				flush()
				pending = v.Text
			}
		case *ButtonMessage:
			if pending != "" && len([]rune(pending))+len([]rune(v.Text))+2 <= coalesceInteractiveMax {
				merged := *v
				merged.Text = pending + "\n\n" + v.Text
				pending = ""
				out = append(out, &merged)
			} else {
				flush()
				out = append(out, m)
			}
		case *ListMessage:
			if pending != "" && len([]rune(pending))+len([]rune(v.Body))+2 <= coalesceInteractiveMax {
				merged := *v
				merged.Body = pending + "\n\n" + v.Body
				pending = ""
				out = append(out, &merged)
			} else {
				flush()
				out = append(out, m)
			}
		default:
			flush()
			out = append(out, m)
		}
	}
	flush()
	return out
}
