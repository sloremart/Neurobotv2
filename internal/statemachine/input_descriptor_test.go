package statemachine

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// El descriptor `input` del protocolo de escalado v1. Lo que se fija aquí es la TRADUCCIÓN: el
// menú y la entrada de un paso salen del mismo registro y aterrizan en una sola clave, con los
// nombres del esquema de Néctar y sin que sobre ninguno (es additionalProperties:false: una
// clave de más, o escrita en inglés, es un cuerpo que no valida).

const stepDescriptor = "RETURN_TEST_DESCRIPTOR"

// buildDescriptor arma la máquina y el paso (config declarativa opcional, entrada opcional y las
// opciones dadas) y devuelve el descriptor ya construido.
func buildDescriptor(t *testing.T, cfg *HandlerConfig, in *ReturnInput, options ...ReturnOption) InputDescriptor {
	t.Helper()
	m := NewMachine()
	if cfg != nil {
		m.RegisterWithConfig(stepDescriptor, *cfg)
	}
	registerTestMenu(t, stepDescriptor, options...)
	if in != nil {
		registerTestInput(t, stepDescriptor, *in)
	}
	return m.InputDescriptor(&session.Session{}, stepDescriptor)
}

func keysOf(t *testing.T, raw json.RawMessage) map[string]bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// Las claves de cable, una por una, en los tres niveles que emite el bot.
func TestInputDescriptor_WireKeysAreTheSchemas(t *testing.T) {
	d := buildDescriptor(t,
		&HandlerConfig{InputType: InputText, ErrorMsg: "el formato", Handler: noopHandler},
		&ReturnInput{
			Kind: "texto_largo", Label: "Escriba el dato", Target: "OTRO_PASO",
			Required: true, MaxLen: 300, Pattern: `^\d+$`,
		},
		ReturnOption{Label: "No hay agenda", Value: "bot_sin_agenda", NoInput: true},
	)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"paso": true, "opciones": true, "campos": true, "minimo_campos": true, "retoma_en": true}
	for k := range got {
		if !want[k] {
			t.Errorf("clave %q fuera del esquema del descriptor", k)
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("faltan claves: %v", want)
	}

	var options []json.RawMessage
	if err := json.Unmarshal(got["opciones"], &options); err != nil {
		t.Fatal(err)
	}
	for _, o := range options {
		for k := range keysOf(t, o) {
			if k != "rotulo" && k != "valor" && k != "salida" {
				t.Errorf("opción: clave %q fuera del esquema", k)
			}
		}
	}

	var fields []json.RawMessage
	if err := json.Unmarshal(got["campos"], &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 {
		t.Fatalf("la entrada registrada es UN campo: %s", got["campos"])
	}
	fieldKeys := keysOf(t, fields[0])
	for _, k := range []string{"clave", "rotulo", "tipo", "requerido", "largo", "patron", "ayuda"} {
		if !fieldKeys[k] {
			t.Errorf("campo: falta %q: %s", k, fields[0])
		}
		delete(fieldKeys, k)
	}
	if len(fieldKeys) != 0 {
		t.Errorf("campo: claves fuera del esquema: %v", fieldKeys)
	}

	var field struct {
		Key      string `json:"clave"`
		Label    string `json:"rotulo"`
		Kind     string `json:"tipo"`
		Required bool   `json:"requerido"`
		MaxLen   int    `json:"largo"`
		Pattern  string `json:"patron"`
		Help     string `json:"ayuda"`
	}
	if err := json.Unmarshal(fields[0], &field); err != nil {
		t.Fatal(err)
	}
	if field.Key != InputFieldKey || field.Label != "Escriba el dato" || field.Kind != "texto_largo" ||
		!field.Required || field.MaxLen != 300 || field.Pattern != `^\d+$` || field.Help != "el formato" {
		t.Errorf("campo: %+v", field)
	}
}

// Un paso de botones sale SIN `campos`, sin `minimo_campos` y sin `retoma_en`: solo paso y
// opciones. Y `opciones` nunca es vacía —las dos universales van en todos los menús—, así que el
// `minItems: 1` del esquema se cumple siempre.
func TestInputDescriptor_ZeroFieldsOnlyOptions(t *testing.T) {
	d := buildDescriptor(t, &HandlerConfig{InputType: InputButton, Options: []string{"si"}, Handler: noopHandler}, nil,
		ReturnOption{Label: "Sí", Value: "si"})
	if d.Fields != nil || d.MinFields != 0 || d.ResumesAt != "" {
		t.Fatalf("un paso de botones solo lleva paso y opciones: %+v", d)
	}
	if len(d.Options) != 3 || d.Options[1].Value != ReturnResume || d.Options[2].Value != ReturnClose {
		t.Fatalf("la propia y las dos universales: %+v", d.Options)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	keys := keysOf(t, raw)
	if len(keys) != 2 || !keys["paso"] || !keys["opciones"] {
		t.Fatalf("el JSON de un paso de botones es {paso, opciones}: %s", raw)
	}

	// Aunque el paso sea de texto, sin entrada registrada no se pinta campo: la entrada es un
	// registro explícito.
	d = buildDescriptor(t, &HandlerConfig{InputType: InputText, TextValidate: func(string) bool { return true }, Handler: noopHandler}, nil)
	if d.Fields != nil || len(d.Options) != 2 {
		t.Fatalf("sin entrada registrada no hay campos y quedan las dos universales: %+v", d)
	}
	// Ni un paso que no existe en la máquina.
	m := NewMachine()
	if d := m.InputDescriptor(&session.Session{}, "RETURN_TEST_UNKNOWN"); d.Fields != nil || len(d.Options) != 2 {
		t.Fatalf("paso desconocido: %+v", d)
	}
}

// `salida` sale de Close/NoInput —la MISMA derivación que el `tipo` del dialecto— y nunca va
// ausente. CERRAR manda sobre NoInput: decir "reanudar_sin_dato" de algo que el bot cierra le
// pintaría al agente un botón que promete volver al flujo.
func TestInputDescriptor_ExitDerivedFromCloseAndNoInput(t *testing.T) {
	d := buildDescriptor(t, nil, nil,
		ReturnOption{Label: "sin dato", Value: "a", NoInput: true},
		ReturnOption{Label: "cierra", Value: "b", Close: true, NoInput: true},
		ReturnOption{Label: "normal", Value: "c"},
	)
	want := []string{"reanudar_sin_dato", "cerrar", "reanudar", "reanudar", "cerrar"} // + las dos universales
	if len(d.Options) != len(want) {
		t.Fatalf("opciones: %+v", d.Options)
	}
	for i, exit := range want {
		if d.Options[i].Exit != exit {
			t.Errorf("opciones[%d].salida = %q, quiero %q", i, d.Options[i].Exit, exit)
		}
	}
	// Es la MISMA derivación que el dialecto, no una copia que pueda desviarse.
	menu := BuildReturnMenu(&session.Session{}, stepDescriptor)
	for i := range menu.Menu {
		if menu.Menu[i].Tipo != d.Options[i].Exit {
			t.Errorf("opciones[%d]: el dialecto dice %q y v1 dice %q", i, menu.Menu[i].Tipo, d.Options[i].Exit)
		}
	}
}

// Los literales de cable son los que Néctar y el registro de capacidades declaran.
func TestInputDescriptor_KindsAreTheLiteralsTheRegistryPublishes(t *testing.T) {
	if InputKindText != "texto" {
		t.Fatalf("el tipo de campo es texto: %q", InputKindText)
	}
}

// Una opción incompleta descarta LA OPCIÓN, no el menú entero: dejar al agente sin ninguna
// salida es peor que perder una.
func TestInputDescriptor_IncompleteOptionDropsOnlyThat(t *testing.T) {
	d := buildDescriptor(t, nil, nil,
		ReturnOption{Label: "", Value: "sin_rotulo"},
		ReturnOption{Label: "sin valor", Value: ""},
		ReturnOption{Label: "buena", Value: "ok"},
	)
	if len(d.Options) != 3 || d.Options[0].Value != "ok" { // la buena + las dos universales
		t.Fatalf("sobrevive la completa: %+v", d.Options)
	}
}

// El campo ÚNICO es la entrada traducida: su rótulo, su ayuda y su tipo salen de donde ya salían
// (el registro, o el ErrorMsg del paso), no de una segunda copia que se quede vieja.
func TestInputDescriptor_TheFieldInheritsLabelHelpAndKind(t *testing.T) {
	cfg := &HandlerConfig{InputType: InputText, ErrorMsg: "ese es el formato", Handler: noopHandler}

	d := buildDescriptor(t, cfg, &ReturnInput{Label: "Escribir el documento", Help: "solo el número"})
	if len(d.Fields) != 1 {
		t.Fatalf("campos: %+v", d.Fields)
	}
	f := d.Fields[0]
	if f.Key != "dato" || f.Label != "Escribir el documento" || f.Help != "solo el número" || f.Kind != "texto" {
		t.Errorf("rótulo y ayuda del registro, tipo texto por omisión: %+v", f)
	}

	// Sin rótulo ni ayuda registrados: el de por omisión y el ErrorMsg del paso, igual que la
	// `entrada` del dialecto.
	d = buildDescriptor(t, cfg, &ReturnInput{})
	if d.Fields[0].Label != defaultReturnInputLabel || d.Fields[0].Help != "ese es el formato" {
		t.Errorf("rótulo y ayuda por omisión: %+v", d.Fields[0])
	}

	// En un paso que no espera texto (una subida de foto) el ErrorMsg describe la foto, no el
	// dato escrito: no se usa de ayuda.
	d = buildDescriptor(t, &HandlerConfig{InputType: InputImage, ErrorMsg: "Por favor envía una imagen.", Handler: noopHandler},
		&ReturnInput{Label: "CUPS"})
	if d.Fields[0].Help != "" {
		t.Errorf("la ayuda no sale del mensaje de la foto: %q", d.Fields[0].Help)
	}

	// Y en un paso sin config declarativa el campo se declara igual (los de la orden médica).
	d = buildDescriptor(t, nil, &ReturnInput{Label: "CUPS", Kind: "texto_largo"})
	if len(d.Fields) != 1 || d.Fields[0].Kind != "texto_largo" || d.Fields[0].Help != "" {
		t.Errorf("sin config declarativa: %+v", d.Fields)
	}
}

// `retoma_en` sale de ReturnInput.Target. Si se perdiera, los pasos que no saben recibir lo que
// trae el agente volverían a escalar en bucle: el retorno no retoma donde escaló.
func TestInputDescriptor_ResumesAtComesFromTheInputTarget(t *testing.T) {
	d := buildDescriptor(t, nil, &ReturnInput{Label: "escriba", Target: "OTRO_PASO"})
	if d.ResumesAt != "OTRO_PASO" {
		t.Fatalf("retoma_en: %q", d.ResumesAt)
	}
	d = buildDescriptor(t, nil, &ReturnInput{Label: "escriba"})
	if d.ResumesAt != "" {
		t.Fatalf("sin Target, el mismo paso: %q", d.ResumesAt)
	}
}

// `requerido` y `minimo_campos` solo viajan cuando el paso declara el campo requerido: es lo que
// el bot aplica (el vacío se rechaza), y donde no aplica la clave sigue yendo ausente. Un campo
// único requerido ES un mínimo de uno: se declara en las dos formas.
func TestInputDescriptor_RequiredEmitsRequeridoAndMinimoCampos(t *testing.T) {
	d := buildDescriptor(t, nil, &ReturnInput{Label: "escriba", Required: true})
	if !d.Fields[0].Required || d.MinFields != 1 {
		t.Fatalf("requerido: %+v, mínimo %d", d.Fields[0], d.MinFields)
	}
	d = buildDescriptor(t, nil, &ReturnInput{Label: "escriba"})
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if d.Fields[0].Required || d.MinFields != 0 {
		t.Fatalf("sin declarar, ausentes: %+v, mínimo %d", d.Fields[0], d.MinFields)
	}
	var got struct {
		MinFields *int                         `json:"minimo_campos"`
		Fields    []map[string]json.RawMessage `json:"campos"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.MinFields != nil {
		t.Errorf("minimo_campos va AUSENTE, no 0: %s", raw)
	}
	for _, k := range []string{"requerido", "largo", "patron"} {
		if _, has := got.Fields[0][k]; has {
			t.Errorf("%q va ausente donde nadie lo declaró: %s", k, raw)
		}
	}
}

// StaticInputDescriptor es la forma de armar el descriptor sin máquina: para un paso estático sin
// entrada tiene que ser BYTE A BYTE el de Machine.InputDescriptor, o dos emisores dirían cosas
// distintas del mismo paso.
func TestStaticInputDescriptor_MatchesTheMachine(t *testing.T) {
	registerTestMenu(t, stepDescriptor,
		ReturnOption{Label: "Confirmar la cita", Value: "confirm", Target: stepDescriptor, Data: "confirm"},
		ReturnOption{Label: "Sin dato", Value: "sin_dato", NoInput: true},
	)
	want, err := json.Marshal(NewMachine().InputDescriptor(nil, stepDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	got := StaticInputDescriptor(nil, stepDescriptor)
	if string(got) != string(want) {
		t.Fatalf("StaticInputDescriptor = %s\nMachine.InputDescriptor = %s", got, want)
	}
	if !strings.HasSuffix(string(got), `{"rotulo":"Caso cerrado, no reanudar","valor":"`+ReturnClose+`","salida":"cerrar"}]}`) {
		t.Errorf("las opciones universales deben ir al final: %s", got)
	}
}

// Un paso con campo no cabe en el descriptor estático: es un error de programación y no puede
// salir un descriptor sin `campos`. Pero el que llama es el NotificationManager en producción, y
// un panic ahí tumbaría el envío: se registra como ERROR (nombrando el paso y la alternativa) y
// el descriptor no viaja (nil). El test de return_menus_test.go que compara con la máquina lo
// atrapa en CI para el paso real.
func TestStaticInputDescriptor_LogsAndReturnsNilOnAStepWithInput(t *testing.T) {
	registerTestMenu(t, stepDescriptor)
	registerTestInput(t, stepDescriptor, ReturnInput{Label: "Escriba el dato"})
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	if got := StaticInputDescriptor(nil, stepDescriptor); got != nil {
		t.Fatalf("un paso con ReturnInput no emite descriptor estático: %s", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, stepDescriptor) ||
		!strings.Contains(logged, "Machine.InputDescriptor") {
		t.Errorf("el error debe nombrar el paso y la alternativa: %s", logged)
	}
}

// Un menú que depende de la sesión (el de NOTIF_PENDING cambia según el tipo de notificación)
// también se arma sin máquina: StaticInputDescriptor recibe la VISTA de la sesión y emite lo mismo
// que Machine.InputDescriptor con esa vista, byte a byte.
func TestStaticInputDescriptor_UsesTheSessionView(t *testing.T) {
	RegisterReturnMenu(stepDescriptor, func(sess *session.Session) []ReturnOption {
		if sess != nil && sess.GetContext("notif_type") == "waiting_list" {
			return []ReturnOption{{Label: "Agendar ahora", Value: "wl_schedule", Target: stepDescriptor, Data: "wl_schedule"}}
		}
		return []ReturnOption{{Label: "Confirmar la cita", Value: "confirm", Target: stepDescriptor, Data: "confirm"}}
	})
	t.Cleanup(func() {
		returnMenusMu.Lock()
		defer returnMenusMu.Unlock()
		delete(returnMenus, stepDescriptor)
	})

	view := &session.Session{Context: map[string]string{"notif_type": "waiting_list"}}
	want, err := json.Marshal(NewMachine().InputDescriptor(view, stepDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	got := StaticInputDescriptor(view, stepDescriptor)
	if string(got) != string(want) {
		t.Fatalf("StaticInputDescriptor = %s\nMachine.InputDescriptor = %s", got, want)
	}
	if !strings.Contains(string(got), `"wl_schedule"`) || strings.Contains(string(got), `"confirm"`) {
		t.Errorf("el descriptor debe salir de la vista de la sesión: %s", got)
	}
	if def := StaticInputDescriptor(nil, stepDescriptor); !strings.Contains(string(def), `"confirm"`) {
		t.Errorf("sin sesión sale el menú por defecto: %s", def)
	}
}

// Un campo `lista` lleva sus `opciones` armadas con la sesión: el `<select>` del agente muestra
// exactamente lo que el paciente vería en su lista.
func TestInputDescriptor_ListFieldCarriesOptionsFromTheSession(t *testing.T) {
	m := NewMachine()
	registerTestInput(t, stepDescriptor, ReturnInput{
		Kind: InputKindList, Label: "Entidad",
		Options: func(sess *session.Session) []InputChoice {
			if sess == nil {
				return nil
			}
			return []InputChoice{
				{Label: sess.GetContext("primera"), Value: "1"},
				{Label: "Otra", Value: "2"},
			}
		},
	})
	sess := &session.Session{Context: map[string]string{"primera": "Sanitas"}}

	raw, err := json.Marshal(m.InputDescriptor(sess, stepDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Campos []struct {
			Tipo     string            `json:"tipo"`
			Opciones []json.RawMessage `json:"opciones"`
		} `json:"campos"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Campos) != 1 || got.Campos[0].Tipo != "lista" {
		t.Fatalf("un campo `lista`: %s", raw)
	}
	if len(got.Campos[0].Opciones) != 2 {
		t.Fatalf("las opciones salen de la sesión: %s", raw)
	}
	if k := keysOf(t, got.Campos[0].Opciones[0]); len(k) != 2 || !k["rotulo"] || !k["valor"] {
		t.Fatalf("cada opción lleva exactamente rotulo y valor: %v", k)
	}
	if !strings.Contains(string(raw), `{"rotulo":"Sanitas","valor":"1"}`) {
		t.Fatalf("la opción viaja con su rótulo y valor: %s", raw)
	}

	// Un campo que no es lista no lleva `opciones`.
	registerTestInput(t, stepDescriptor, ReturnInput{Label: "Texto"})
	d := m.InputDescriptor(sess, stepDescriptor)
	field, err := json.Marshal(d.Fields[0])
	if err != nil {
		t.Fatal(err)
	}
	if keysOf(t, field)["opciones"] {
		t.Fatalf("un campo de texto no lleva opciones: %s", field)
	}
}

// Un campo `fecha` con formato iso lo declara: es lo que hace que la consola pinte el selector.
func TestInputDescriptor_DateFieldDeclaresIsoFormat(t *testing.T) {
	d := buildDescriptor(t, nil, &ReturnInput{Kind: InputKindDate, Format: InputFormatISO, Label: "Fecha"})
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tipo":"fecha"`) || !strings.Contains(string(raw), `"formato":"iso"`) {
		t.Fatalf("el campo fecha declara formato iso: %s", raw)
	}
	if InputKindNumber != "numero" || InputKindList != "lista" || InputKindDate != "fecha" || InputFormatISO != "iso" {
		t.Fatal("literales de cable")
	}
}
