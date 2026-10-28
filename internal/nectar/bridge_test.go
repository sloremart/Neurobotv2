package nectar

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// El puente traduce los tipos de Bird al cable de Néctar (§8.5) y devuelve los ids como cadena.
// Va contra el mismo falso Néctar de client_test.go: lo que se mira es el CUERPO que llega.

func TestBridge_SendTemplateTraduceNombreIdiomaYParametros(t *testing.T) {
	f := newFakeNectar(t)
	b := NewBridge(newTestClient(f), map[string]string{"proj-recordatorio": "recordatorio_cita"})

	id, err := b.SendTemplate(context.Background(), 1234, bird.TemplateConfig{
		ProjectID: "proj-recordatorio",
		Locale:    "es_CO",
		Params: []bird.TemplateParam{
			{Type: "string", Key: "1", Value: "Paciente Prueba"},
			{Type: "string", Key: "2", Value: "18 de septiembre"},
		},
	})
	if err != nil || id != "55" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	var got struct {
		Type     string       `json:"type"`
		Template TemplateBody `json:"template"`
	}
	if err := json.Unmarshal(f.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "template" || got.Template.Name != "recordatorio_cita" || got.Template.Language != "es_CO" {
		t.Fatalf("el ProjectID de Bird se traduce al nombre de Meta y el Locale es el idioma: %s", f.bodies[0])
	}
	// Los parámetros viajan POR NOMBRE DE MARCADOR (objeto, no arreglo).
	want := map[string]string{"1": "Paciente Prueba", "2": "18 de septiembre"}
	if !reflect.DeepEqual(got.Template.Parameters, want) {
		t.Fatalf("parámetros = %v, quiero %v", got.Template.Parameters, want)
	}
}

func TestBridge_SendTemplateSinIdiomaUsaEs(t *testing.T) {
	f := newFakeNectar(t)
	b := NewBridge(newTestClient(f), map[string]string{"p": "plantilla_x"})
	if _, err := b.SendTemplate(context.Background(), 1234, bird.TemplateConfig{ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Template TemplateBody `json:"template"`
	}
	_ = json.Unmarshal(f.bodies[0], &got)
	if got.Template.Language != "es" {
		t.Fatalf("sin Locale el idioma es «es»: %s", f.bodies[0])
	}
	// Sin parámetros el objeto va vacío, nunca `null`.
	if !strings.Contains(string(f.bodies[0]), `"parameters":{}`) {
		t.Fatalf("parameters va como objeto vacío: %s", f.bodies[0])
	}
}

func TestBridge_SendTemplateSinNombreConfiguradoFallaSinLlamarANectar(t *testing.T) {
	f := newFakeNectar(t)
	for nombre, nombres := range map[string]map[string]string{
		"mapa vacío":       nil,
		"otro project_id":  {"otro": "x"},
		"nombre en blanco": {"p": ""},
	} {
		t.Run(nombre, func(t *testing.T) {
			b := NewBridge(newTestClient(f), nombres)
			_, err := b.SendTemplate(context.Background(), 1234, bird.TemplateConfig{ProjectID: "p"})
			if err == nil || !strings.Contains(err.Error(), "NECTAR_TEMPLATE_NAMES") {
				t.Fatalf("el error dice qué falta configurar: %v", err)
			}
		})
	}
	if len(f.calls) != 0 {
		t.Fatalf("una plantilla sin nombre de Meta no sale hacia Néctar: %v", f.calls)
	}
}

func TestBridge_SendButtonsYSendListMapeanAlCable(t *testing.T) {
	f := newFakeNectar(t)
	b := NewBridge(newTestClient(f), nil)
	ctx := context.Background()

	if id, err := b.SendButtons(ctx, 1234, "elige",
		[]bird.Button{{Text: "Agendar", Payload: "AGENDAR"}, {Text: "Cancelar", Payload: "CANCELAR"}}); err != nil || id != "55" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	var buttons struct {
		Type    string        `json:"type"`
		Text    string        `json:"text"`
		Buttons []ReplyButton `json:"buttons"`
	}
	_ = json.Unmarshal(f.bodies[0], &buttons)
	wantButtons := []ReplyButton{{ID: "AGENDAR", Title: "Agendar"}, {ID: "CANCELAR", Title: "Cancelar"}}
	if buttons.Type != "buttons" || buttons.Text != "elige" || !reflect.DeepEqual(buttons.Buttons, wantButtons) {
		t.Fatalf("Payload es el id y Text es el título: %s", f.bodies[0])
	}

	if id, err := b.SendList(ctx, 1234, "cuerpo", "Ver opciones", []bird.ListSection{{
		Title: "Sede",
		Rows:  []bird.ListRow{{ID: "S1", Title: "Sede 01", Description: "Calle 1"}, {ID: "S2", Title: "Sede 02"}},
	}}); err != nil || id != "55" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	var list struct {
		Type string   `json:"type"`
		Text string   `json:"text"`
		List ListBody `json:"list"`
	}
	_ = json.Unmarshal(f.bodies[1], &list)
	wantSections := []ListSection{{
		Title: "Sede",
		Rows:  []ListRow{{ID: "S1", Title: "Sede 01", Description: "Calle 1"}, {ID: "S2", Title: "Sede 02"}},
	}}
	if list.Type != "list" || list.Text != "cuerpo" || list.List.Button != "Ver opciones" ||
		!reflect.DeepEqual(list.List.Sections, wantSections) {
		t.Fatalf("la lista llega con su rótulo, secciones y filas: %s", f.bodies[1])
	}
}

func TestBridge_AvailableYIsNotMine(t *testing.T) {
	f := newFakeNectar(t)
	b := NewBridge(newTestClient(f), nil)
	ok, reason, err := b.Available(context.Background(), 1234)
	if err != nil || ok || reason != "fuera_de_horario" {
		t.Fatalf("ok=%v reason=%q err=%v", ok, reason, err)
	}

	f.notMine = true
	_, err = b.SendText(context.Background(), 1234, "x")
	if !b.IsNotMine(err) {
		t.Fatalf("el 404 es «ya no es mía»: %v", err)
	}
	if b.IsNotMine(ErrWindowClosed) {
		t.Fatal("una ventana cerrada NO es «ya no es mía»")
	}
}
