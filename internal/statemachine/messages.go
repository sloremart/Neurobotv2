package statemachine

// OutboundMessage es la interfaz para todos los tipos de mensajes salientes
type OutboundMessage interface {
	Type() string
}

// CtxLastBotMsg es la clave de contexto de sesión donde se guarda el último texto que el bot le
// envió al paciente. Sirve de contexto conversacional para la capa de recuperación IA.
const CtxLastBotMsg = "last_bot_msg"

// OutboundText devuelve el texto visible de un mensaje saliente (para historial/contexto). "" si el
// tipo no tiene texto legible.
func OutboundText(m OutboundMessage) string {
	switch v := m.(type) {
	case *TextMessage:
		return v.Text
	case *ButtonMessage:
		return v.Text
	case *ListMessage:
		return v.Body
	default:
		return ""
	}
}

// TextMessage es un mensaje de texto simple
type TextMessage struct {
	Text string
}

func (m *TextMessage) Type() string { return "text" }

// Button representa un botón postback (máx 3 por mensaje)
type Button struct {
	Text    string
	Payload string
}

// ButtonMessage es un mensaje con botones interactivos
type ButtonMessage struct {
	Text    string
	Buttons []Button
}

func (m *ButtonMessage) Type() string { return "interactive_buttons" }

// ListSection es una sección de una lista interactiva
type ListSection struct {
	Title string
	Rows  []ListRow
}

// ListRow es una fila dentro de una sección de lista
type ListRow struct {
	ID          string
	Title       string
	Description string
}

// ListMessage es un mensaje con lista interactiva
type ListMessage struct {
	Body     string
	Title    string
	Sections []ListSection
}

func (m *ListMessage) Type() string { return "interactive_list" }
