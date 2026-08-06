package statemachine

import (
	"strings"
	"testing"
)

// El auto-chain de machine.go acumula mensajes de varios estados en un turno (texto de transición
// + prompt siguiente = 2-3 envíos cobrados). CoalesceMessages los fusiona en el chokepoint de
// envío: mismo destinatario, mismo turno, misma información — menos burbujas y menos costo.

func TestCoalesce_TextPlusText(t *testing.T) {
	out := CoalesceMessages([]OutboundMessage{
		&TextMessage{Text: "✅ ¡Registro exitoso!"},
		&TextMessage{Text: "Envía una foto clara de tu orden médica."},
	})
	if len(out) != 1 {
		t.Fatalf("esperaba 1 mensaje fusionado, got %d", len(out))
	}
	txt := out[0].(*TextMessage).Text
	if !strings.Contains(txt, "Registro exitoso") || !strings.Contains(txt, "foto clara") {
		t.Errorf("el texto fusionado debe conservar ambos contenidos: %q", txt)
	}
}

func TestCoalesce_ThreeTexts(t *testing.T) {
	out := CoalesceMessages([]OutboundMessage{
		&TextMessage{Text: "Cita confirmada."},
		&TextMessage{Text: "Ahora el siguiente procedimiento."},
		&TextMessage{Text: "Horarios: 1) lunes 2) martes"},
	})
	if len(out) != 1 {
		t.Fatalf("esperaba 1 mensaje, got %d", len(out))
	}
}

func TestCoalesce_TextIntoButtons(t *testing.T) {
	out := CoalesceMessages([]OutboundMessage{
		&TextMessage{Text: "Tu orden indica medio de contraste."},
		&ButtonMessage{Text: "¿El paciente pesa más de 100 kg?", Buttons: []Button{{Text: "Sí", Payload: "y"}, {Text: "No", Payload: "n"}}},
	})
	if len(out) != 1 {
		t.Fatalf("esperaba 1 mensaje, got %d", len(out))
	}
	btn, ok := out[0].(*ButtonMessage)
	if !ok {
		t.Fatalf("esperaba ButtonMessage, got %T", out[0])
	}
	if !strings.Contains(btn.Text, "contraste") || !strings.Contains(btn.Text, "100 kg") {
		t.Errorf("el body debe llevar ambos textos: %q", btn.Text)
	}
	if len(btn.Buttons) != 2 {
		t.Errorf("los botones deben conservarse, got %d", len(btn.Buttons))
	}
}

func TestCoalesce_TextIntoList(t *testing.T) {
	out := CoalesceMessages([]OutboundMessage{
		&TextMessage{Text: "Veo que enviaste tu orden 📷"},
		&ListMessage{Body: "Selecciona tu tipo de entidad", Title: "Entidades", Sections: []ListSection{{Title: "S", Rows: []ListRow{{ID: "1", Title: "EPS"}}}}},
	})
	if len(out) != 1 {
		t.Fatalf("esperaba 1 mensaje, got %d", len(out))
	}
	lst, ok := out[0].(*ListMessage)
	if !ok {
		t.Fatalf("esperaba ListMessage, got %T", out[0])
	}
	if !strings.Contains(lst.Body, "enviaste tu orden") || !strings.Contains(lst.Body, "tipo de entidad") {
		t.Errorf("el body debe llevar ambos textos: %q", lst.Body)
	}
}

// No se fusiona si el body interactivo excedería el límite de WhatsApp: mejor 2 mensajes que un 131009.
func TestCoalesce_RespectsInteractiveLimit(t *testing.T) {
	long := strings.Repeat("preparación muy larga ", 50) // ~1100 chars
	out := CoalesceMessages([]OutboundMessage{
		&TextMessage{Text: long},
		&ListMessage{Body: "Detalle de tu cita", Sections: []ListSection{{Rows: []ListRow{{ID: "1", Title: "Volver"}}}}},
	})
	if len(out) != 2 {
		t.Fatalf("no debe fusionar por encima del límite interactivo, got %d mensajes", len(out))
	}
}

func TestCoalesce_RespectsTextLimit(t *testing.T) {
	a := strings.Repeat("a", 2500)
	b := strings.Repeat("b", 2500)
	out := CoalesceMessages([]OutboundMessage{&TextMessage{Text: a}, &TextMessage{Text: b}})
	if len(out) != 2 {
		t.Fatalf("dos textos que exceden el límite juntos deben quedar separados, got %d", len(out))
	}
}

// No se fusiona hacia atrás: lo que viene DESPUÉS de un interactivo se queda aparte.
func TestCoalesce_NoBackwardMerge(t *testing.T) {
	out := CoalesceMessages([]OutboundMessage{
		&ListMessage{Body: "Menú", Sections: []ListSection{{Rows: []ListRow{{ID: "1", Title: "Op"}}}}},
		&TextMessage{Text: "Texto posterior"},
	})
	if len(out) != 2 {
		t.Fatalf("no debe fusionar tras un interactivo, got %d", len(out))
	}
}

func TestCoalesce_InteractivePlusInteractive(t *testing.T) {
	out := CoalesceMessages([]OutboundMessage{
		&ButtonMessage{Text: "A", Buttons: []Button{{Text: "x", Payload: "x"}}},
		&ListMessage{Body: "B", Sections: []ListSection{{Rows: []ListRow{{ID: "1", Title: "y"}}}}},
	})
	if len(out) != 2 {
		t.Fatalf("dos interactivos no se fusionan, got %d", len(out))
	}
}

func TestCoalesce_SingleAndEmptyPassthrough(t *testing.T) {
	if out := CoalesceMessages(nil); len(out) != 0 {
		t.Errorf("nil → vacío, got %d", len(out))
	}
	single := []OutboundMessage{&TextMessage{Text: "solo"}}
	if out := CoalesceMessages(single); len(out) != 1 || out[0].(*TextMessage).Text != "solo" {
		t.Errorf("un solo mensaje pasa intacto")
	}
}

// No debe mutar los mensajes originales (los handlers pueden reusar templates).
func TestCoalesce_DoesNotMutateOriginals(t *testing.T) {
	orig := &ListMessage{Body: "Original", Sections: []ListSection{{Rows: []ListRow{{ID: "1", Title: "r"}}}}}
	CoalesceMessages([]OutboundMessage{&TextMessage{Text: "prefijo"}, orig})
	if orig.Body != "Original" {
		t.Errorf("el mensaje original fue mutado: %q", orig.Body)
	}
}
