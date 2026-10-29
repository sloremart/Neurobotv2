package statemachine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// El menú de retornos y su vuelta (return_value / retorno v1). Lo que se fija aquí es que el
// token producido y el token resuelto son EL MISMO, que el JSON lleva los nombres del contrato
// (Néctar guarda el menú opaco y nadie más los va a corregir) y que lo que el agente escribe se
// juzga con las reglas del paso ANTES de llegarle al paciente.

// Los registros de menú, entrada y candado son globales del paquete: cada prueba usa sus propios
// nombres de paso y los borra al terminar, para no contaminar a las demás.

func registerTestMenu(t *testing.T, step string, options ...ReturnOption) {
	t.Helper()
	RegisterStaticReturnMenu(step, options...)
	t.Cleanup(func() {
		returnMenusMu.Lock()
		defer returnMenusMu.Unlock()
		delete(returnMenus, step)
	})
}

func registerTestInput(t *testing.T, step string, in ReturnInput) {
	t.Helper()
	RegisterReturnInput(step, in)
	t.Cleanup(func() {
		returnMenusMu.Lock()
		defer returnMenusMu.Unlock()
		delete(returnInputs, step)
	})
}

const (
	stepText     = "RETURN_TEST_TEXT"
	stepButton   = "RETURN_TEST_BUTTON"
	stepPlain    = "RETURN_TEST_PLAIN"
	stepNoMenu   = "RETURN_TEST_NO_MENU"
	stepUnsought = "RETURN_TEST_UNREGISTERED"
)

func noopHandler(context.Context, *session.Session, bird.InboundMessage) (*StateResult, error) {
	return NewResult("NEXT"), nil
}

// textStepMachine arma una máquina con un paso de texto que el agente puede contestar escribiendo
// (config declarativa con validador y ErrorMsg + entrada registrada), un paso de botones sin
// entrada y un paso SIN config declarativa (Register a secas) que sí registra entrada.
func textStepMachine(t *testing.T) *Machine {
	t.Helper()
	m := NewMachine()
	m.RegisterWithConfig(stepText, HandlerConfig{
		InputType:    InputText,
		TextValidate: func(s string) bool { return len(s) >= 4 },
		ErrorMsg:     "Escribe al menos cuatro caracteres.",
		Handler:      noopHandler,
	})
	registerTestInput(t, stepText, ReturnInput{Label: "Escribir el dato", Required: true})

	m.RegisterWithConfig(stepButton, HandlerConfig{
		InputType: InputButton,
		Options:   []string{"si"},
		ErrorMsg:  "Elige una opción.",
		Handler:   noopHandler,
	})

	m.Register(stepPlain, noopHandler)
	registerTestInput(t, stepPlain, ReturnInput{Label: "CUPS leídos", Command: "cups"})
	return m
}

// --- El menú ---

func TestBuildReturnMenu_UniversalOptionsLast(t *testing.T) {
	registerTestMenu(t, "RETURN_TEST_ORDER",
		ReturnOption{Label: "Primera", Value: "uno", Target: "OTRO", Data: "uno"},
		ReturnOption{Label: "Segunda", Value: "dos"})

	sess := &session.Session{}
	menu := BuildReturnMenu(sess, "RETURN_TEST_ORDER")
	if menu.PasoActual != "RETURN_TEST_ORDER" {
		t.Fatalf("paso_actual: %q", menu.PasoActual)
	}
	got := make([]string, 0, len(menu.Menu))
	for _, o := range menu.Menu {
		got = append(got, o.Valor)
	}
	want := []string{"uno", "dos", ReturnResume, ReturnReset, ReturnClose}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lo específico primero y las tres universales al final: got %v, want %v", got, want)
	}
	// Y tipo: reanudar para lo que vuelve al flujo (aunque sea a OTRO paso), cerrar para el cierre.
	wantTipo := []string{"reanudar", "reanudar", "reanudar", "reanudar_sin_dato", "cerrar"}
	for i, o := range menu.Menu {
		if o.Tipo != wantTipo[i] {
			t.Errorf("%s: tipo = %q, quiero %q", o.Valor, o.Tipo, wantTipo[i])
		}
	}

	// Un paso sin menú propio ofrece, igual, las tres universales: nunca se queda sin salida.
	opts := ReturnOptionsFor(sess, stepNoMenu)
	if len(opts) != 3 || opts[0].Value != ReturnResume || opts[1].Value != ReturnReset || !opts[1].Reset ||
		opts[2].Value != ReturnClose || !opts[2].Close {
		t.Fatalf("universales de un paso sin menú: %+v", opts)
	}
}

func TestBuildReturnMenu_DropsOptionsWithoutLabelOrValue(t *testing.T) {
	// Una registración mal hecha no puede dejar al agente sin NINGUNA salida: se cae esa
	// opción, no el menú (las universales siempre quedan).
	registerTestMenu(t, "RETURN_TEST_INCOMPLETE",
		ReturnOption{Label: "", Value: "sin_etiqueta"},
		ReturnOption{Label: "Sin valor", Value: ""},
		ReturnOption{Label: "Buena", Value: "buena"})

	menu := BuildReturnMenu(&session.Session{}, "RETURN_TEST_INCOMPLETE")
	if len(menu.Menu) != 4 { // la buena + las tres universales
		t.Fatalf("%+v", menu.Menu)
	}
	for _, o := range menu.Menu {
		if o.Etiqueta == "" || o.Valor == "" {
			t.Fatalf("una opción incompleta llegó al cable: %+v", o)
		}
	}
}

// La interfaz del agente EXIGE esta forma: arreglo bajo `menu`, y cada opción con `etiqueta`,
// `valor` y `tipo` no vacías. Néctar no valida nada, así que un nombre distinto no da error en
// ninguna parte: el agente simplemente no ve botones. Este test es el único guardián.
func TestBuildReturnMenu_WireShape(t *testing.T) {
	registerTestMenu(t, "RETURN_TEST_WIRE", ReturnOption{Label: "Cita agendada, continúa", Value: "cita_ok"})

	raw, err := json.Marshal(BuildReturnMenu(&session.Session{}, "RETURN_TEST_WIRE"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("el menú tiene que ser JSON válido: %v", err)
	}
	for k := range got {
		if k != "paso_actual" && k != "menu" {
			t.Errorf("clave %q fuera del dialecto (sin entrada, solo viajan paso_actual y menu)", k)
		}
	}
	arr, ok := got["menu"].([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("el arreglo TIENE que llamarse `menu` y no ir vacío, o el agente no ve ningún botón: %s", raw)
	}
	for i, item := range arr {
		opt, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("opción %d no es un objeto", i)
		}
		for _, key := range []string{"etiqueta", "valor", "tipo"} {
			if s, ok := opt[key].(string); !ok || s == "" {
				t.Fatalf("opción %d: %q tiene que ser una cadena NO vacía: %v", i, key, opt)
			}
		}
	}
}

// El vocabulario de `tipo` es CERRADO y se fija POR AGOTAMIENTO: las dos banderas dan cuatro
// combinaciones y el conjunto de literales producido tiene que ser EXACTAMENTE los tres
// acordados con Néctar. CERRAR manda sobre NoInput: anunciar "reanudar_sin_dato" de algo que el
// bot cierra le promete al agente una vuelta al flujo que no va a ocurrir.
func TestBuildReturnMenu_TipoVocabularyIsExactlyThreeLiterals(t *testing.T) {
	registerTestMenu(t, "RETURN_TEST_TIPO",
		ReturnOption{Label: "Ni cierra ni exime", Value: "v1"},
		ReturnOption{Label: "Cierra", Value: "v2", Close: true},
		ReturnOption{Label: "Reanuda sin dato", Value: "v3", NoInput: true},
		ReturnOption{Label: "Las dos", Value: "v4", Close: true, NoInput: true})

	byValue := map[string]string{}
	seen := map[string]bool{}
	for _, o := range BuildReturnMenu(&session.Session{}, "RETURN_TEST_TIPO").Menu {
		byValue[o.Valor] = o.Tipo
		seen[o.Tipo] = true
	}
	want := map[string]string{
		"v1": "reanudar", "v2": "cerrar", "v3": "reanudar_sin_dato", "v4": "cerrar",
	}
	for value, tipo := range want {
		if byValue[value] != tipo {
			t.Errorf("%s: tipo = %q, quiero %q", value, byValue[value], tipo)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("el vocabulario producido es %v; los tres acordados son reanudar, cerrar y reanudar_sin_dato", seen)
	}
}

// El menú puede depender de la sesión, y uno estático no la necesita: con sesión nil sale igual.
// Lo exige quien arma el descriptor de una notificación proactiva, que no tiene sesión.
func TestReturnOptionsFor_SessionOptionalForStaticMenus(t *testing.T) {
	registerTestMenu(t, "RETURN_TEST_STATIC", ReturnOption{Label: "Confirmar", Value: "confirm", Data: "confirm"})
	RegisterReturnMenu("RETURN_TEST_BY_SESSION", func(s *session.Session) []ReturnOption {
		return []ReturnOption{{Label: "Contrato " + s.GetContext("contract"), Value: s.GetContext("contract")}}
	})
	t.Cleanup(func() {
		returnMenusMu.Lock()
		defer returnMenusMu.Unlock()
		delete(returnMenus, "RETURN_TEST_BY_SESSION")
	})

	opts := ReturnOptionsFor(nil, "RETURN_TEST_STATIC")
	if len(opts) != 4 || opts[0].Value != "confirm" || opts[1].Value != ReturnResume || opts[3].Value != ReturnClose {
		t.Fatalf("un menú estático no necesita sesión: %+v", opts)
	}
	if menu := BuildReturnMenu(nil, "RETURN_TEST_STATIC"); len(menu.Menu) != 4 {
		t.Fatalf("BuildReturnMenu tampoco: %+v", menu.Menu)
	}

	sess := &session.Session{Context: map[string]string{"contract": "4"}}
	if opts := ReturnOptionsFor(sess, "RETURN_TEST_BY_SESSION"); opts[0].Value != "4" {
		t.Fatalf("el constructor recibe la sesión: %+v", opts[0])
	}
}

func TestResolveReturnValue_UnknownTokenIsFalse(t *testing.T) {
	registerTestMenu(t, "RETURN_TEST_RESOLVE",
		ReturnOption{Label: "Sin cupo", Value: "sin_cupo", Target: "OTRO_PASO", Data: "sin_cupo"})

	// No se adivina: quien llama reanuda sin decisión. Una rama equivocada es peor que ninguna,
	// porque el agente ya habló con el paciente y el bot no sabe qué le dijo.
	if _, ok := ResolveReturnValue(&session.Session{}, "RETURN_TEST_RESOLVE", "token_de_otro_paso"); ok {
		t.Fatal("un token que no es de este paso no se resuelve")
	}
	if _, ok := ResolveReturnValue(&session.Session{}, "RETURN_TEST_RESOLVE", ""); ok {
		t.Fatal("sin valor no hay decisión")
	}

	// Y los que sí son del paso dan lo que el bot debe hacer.
	opt, ok := ResolveReturnValue(&session.Session{}, "RETURN_TEST_RESOLVE", "sin_cupo")
	if !ok || opt.Target != "OTRO_PASO" || opt.Data != "sin_cupo" {
		t.Fatalf("ok=%v opt=%+v", ok, opt)
	}
	if opt, ok := ResolveReturnValue(&session.Session{}, "RETURN_TEST_RESOLVE", ReturnClose); !ok || !opt.Close {
		t.Fatalf("el token de cierre se resuelve desde cualquier paso: ok=%v %+v", ok, opt)
	}
	if opt, ok := ResolveReturnValue(&session.Session{}, stepNoMenu, ReturnResume); !ok || opt.Close {
		t.Fatalf("reanudar también, y no cierra: ok=%v %+v", ok, opt)
	}
}

// El candado del botón «Resolver»: lo declara el PASO, y sin registro no hay candado.
func TestResolveGuard_DeclaredByTheStep(t *testing.T) {
	if _, ok := ResolveGuardFor("RETURN_TEST_NO_GUARD"); ok {
		t.Fatal("un paso sin registro escala SIN candado")
	}
	RegisterResolveGuard("RETURN_TEST_GUARD", "cita_pendiente")
	t.Cleanup(func() { RegisterResolveGuard("RETURN_TEST_GUARD", "") })

	motivo, ok := ResolveGuardFor("RETURN_TEST_GUARD")
	if !ok || motivo != "cita_pendiente" {
		t.Fatalf("motivo=%q ok=%v", motivo, ok)
	}
	// Un motivo vacío es lo mismo que no tener candado: así se limpia sin dejar una cadena
	// vacía viajando al cable.
	RegisterResolveGuard("RETURN_TEST_GUARD", "")
	if _, ok := ResolveGuardFor("RETURN_TEST_GUARD"); ok {
		t.Fatal("un motivo vacío no es un candado")
	}
}

// LO QUE EL AGENTE ESCRIBIÓ NO ENTRA EN NINGÚN REGISTRO: la CLASE del token explica la vuelta, su
// contenido no. Un token sin prefijo lo eligió el agente de un menú que armó el propio bot, es
// vocabulario nuestro y se traza entero.
func TestReturnValueForLog_NeverEchoesData(t *testing.T) {
	cases := map[string]string{
		"bot_dato:1000000001":   ReturnInputPrefix,
		"bot_dato:":             ReturnInputPrefix,
		"bot_dato:CUPS 890274":  ReturnInputPrefix,
		ReturnResume:            ReturnResume,
		ReturnClose:             ReturnClose,
		ReturnValueQueueExpired: ReturnValueQueueExpired,
		"confirm":               "confirm",
		"":                      "",
	}
	for token, want := range cases {
		if got := ReturnValueForLog(token); got != want {
			t.Errorf("ReturnValueForLog(%q) = %q, quiero %q", token, got, want)
		}
	}
	if got := ReturnValueForLog("bot_dato:1000000001"); strings.Contains(got, "1000000001") {
		t.Fatalf("el dato escrito no sale en el registro: %q", got)
	}
}

// --- La entrada de texto libre (dialecto) ---

// La interfaz del agente necesita TRES cosas para pintar el campo: que hay campo, qué se espera
// y con qué prefijo devolverlo. Sin el prefijo el bot no podría distinguir un dato escrito de un
// token de menú.
func TestReturnMenu_RegisteredInputDeclaresEntrada(t *testing.T) {
	m := textStepMachine(t)

	raw, err := json.Marshal(m.ReturnMenu(&session.Session{}, stepText))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Entrada *struct {
			Etiqueta     string `json:"etiqueta"`
			Ayuda        string `json:"ayuda"`
			PrefijoValor string `json:"prefijo_valor"`
		} `json:"entrada"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Entrada == nil {
		t.Fatal("un paso con entrada registrada tiene que declararla, si no el agente no puede escribir")
	}
	if got.Entrada.Etiqueta != "Escribir el dato" {
		t.Errorf("etiqueta = %q, sale del registro", got.Entrada.Etiqueta)
	}
	if got.Entrada.PrefijoValor != ReturnInputPrefix {
		t.Errorf("prefijo_valor = %q, debe ser el que el bot resuelve", got.Entrada.PrefijoValor)
	}
	// La ayuda sale del mensaje de error del paso: es la explicación que el bot ya le da al
	// paciente, y escribirla aparte se desincronizaría al primer cambio.
	if got.Entrada.Ayuda != "Escribe al menos cuatro caracteres." {
		t.Errorf("ayuda = %q, debe salir del ErrorMsg del paso", got.Entrada.Ayuda)
	}

	// En un paso SIN config declarativa no hay ErrorMsg de dónde sacar la ayuda: sale vacía y
	// el rótulo es el del registro.
	menu := m.ReturnMenu(&session.Session{}, stepPlain)
	if menu.Entrada == nil || menu.Entrada.Etiqueta != "CUPS leídos" || menu.Entrada.Ayuda != "" {
		t.Fatalf("paso sin config: %+v", menu.Entrada)
	}
}

// El mensaje de error de un paso que NO espera texto no describe el formato del dato escrito
// (el de una subida de foto dice «envía una imagen»): no sirve de ayuda para el campo del agente.
func TestReturnMenu_HelpIgnoresTheErrorMsgOfANonTextStep(t *testing.T) {
	m := NewMachine()
	m.RegisterWithConfig("RETURN_TEST_IMAGE", HandlerConfig{
		InputType: InputImage,
		ErrorMsg:  "Por favor envía una imagen.",
		Handler:   noopHandler,
	})
	registerTestInput(t, "RETURN_TEST_IMAGE", ReturnInput{Label: "CUPS leídos de la orden"})

	menu := m.ReturnMenu(&session.Session{}, "RETURN_TEST_IMAGE")
	if menu.Entrada == nil || menu.Entrada.Ayuda != "" {
		t.Fatalf("la ayuda no sale del mensaje de la foto: %+v", menu.Entrada)
	}
}

func TestReturnMenu_NoInputRegisteredMeansNoEntrada(t *testing.T) {
	m := textStepMachine(t)
	// Un paso de botones y uno que ni existe no llevan campo: ofrecerle escribir al agente donde
	// el paso no sabe recibirlo lo invita a mandar algo que se pierde. Y NO basta que el paso sea
	// de texto: la entrada es un registro explícito.
	m.RegisterWithConfig("RETURN_TEST_TEXT_NO_INPUT", HandlerConfig{
		InputType:    InputText,
		TextValidate: func(string) bool { return true },
		Handler:      noopHandler,
	})
	for _, step := range []string{stepButton, stepUnsought, "RETURN_TEST_TEXT_NO_INPUT"} {
		if menu := m.ReturnMenu(&session.Session{}, step); menu.Entrada != nil {
			t.Errorf("%s no lleva entrada: %+v", step, menu.Entrada)
		}
	}
	if m.ResolveReturnInput(&session.Session{}, stepButton, ReturnInputPrefix+"1000000001").Written {
		t.Error("y un dato escrito para un paso sin entrada no se acepta")
	}
}

func TestResolveReturnInput_PrefixedValueGoesToValidate(t *testing.T) {
	m := textStepMachine(t)
	sess := &session.Session{}

	// Lo que va DESPUÉS del prefijo es el dato, y lo juzga el mismo validador que el paso le
	// aplica al paciente (TextValidate de su config declarativa).
	res := m.ResolveReturnInput(sess, stepText, ReturnInputPrefix+"1000000001")
	if !res.OK() || res.Value != "1000000001" {
		t.Fatalf("lo escrito debe poder inyectarse: %+v", res)
	}
	res = m.ResolveReturnInput(sess, stepText, ReturnInputPrefix+"ab")
	if !res.Rejected() || res.Problem != "Escribe al menos cuatro caracteres." {
		t.Fatalf("lo que el paso rechaza del paciente se rechaza del agente, con el motivo del paso: %+v", res)
	}

	// La validación FINA ve la sesión: el formato de un dato suele depender de algo que el
	// paciente ya contestó, y solo está ahí.
	registerTestInput(t, stepText, ReturnInput{
		Label: "Escribir",
		Validate: func(s *session.Session, value string) InputRejection {
			if value == s.GetContext("esperado") {
				return InputRejection{}
			}
			return Reject("Ese no es el valor esperado.")
		},
	})
	sess.SetContext("esperado", "1000000001")
	if m.ResolveReturnInput(sess, stepText, ReturnInputPrefix+"9999999999").OK() {
		t.Error("lo que la validación fina rechaza no se inyecta")
	}
	if !m.ResolveReturnInput(sess, stepText, ReturnInputPrefix+"1000000001").OK() {
		t.Error("y lo que acepta, sí")
	}
}

// Un paso sin config declarativa (Register a secas: los de la orden médica) no tiene validador de
// texto que aplicar: acepta, y el Validate de su entrada es la única regla.
func TestResolveReturnInput_StepWithoutConfigOnlyValidateApplies(t *testing.T) {
	m := textStepMachine(t)
	sess := &session.Session{}

	if res := m.ResolveReturnInput(sess, stepPlain, ReturnInputPrefix+"890274:1"); !res.OK() {
		t.Fatalf("sin config ni Validate se acepta: %+v", res)
	}

	registerTestInput(t, stepPlain, ReturnInput{
		Command: "cups",
		Validate: func(_ *session.Session, value string) InputRejection {
			if strings.Contains(value, ":") {
				return InputRejection{}
			}
			return Reject("Falta la cantidad.")
		},
	})
	if res := m.ResolveReturnInput(sess, stepPlain, ReturnInputPrefix+"890274"); !res.Rejected() || res.Problem != "Falta la cantidad." {
		t.Fatalf("la única regla es Validate: %+v", res)
	}
	if res := m.ResolveReturnInput(sess, stepPlain, ReturnInputPrefix+"890274:1"); !res.OK() {
		t.Fatalf("%+v", res)
	}
}

// El error del agente no puede acabar en la cara del paciente: se rechaza ANTES de inyectar. Y
// lo que NO era un dato escrito (token de menú) o no se puede atribuir a su culpa (paso sin
// campo) no es un rechazo: se resuelve por el token.
func TestResolveReturnInput_WhatIsNotWrittenIsNotJudged(t *testing.T) {
	m := textStepMachine(t)
	sess := &session.Session{}

	notWritten := map[string]struct{ step, value string }{
		"sin prefijo, es un token de menú": {stepText, ReturnResume},
		"el paso no ofrece campo":          {stepButton, ReturnInputPrefix + "1000000001"},
		"el paso ni siquiera existe":       {stepUnsought, ReturnInputPrefix + "1000000001"},
		// Vacío en un paso sin campo: no es culpa del agente que la interfaz lo pintara.
		"vacío en un paso sin campo": {stepButton, ReturnInputPrefix + "   "},
	}
	for name, c := range notWritten {
		if m.ResolveReturnInput(sess, c.step, c.value).Written {
			t.Errorf("%s: no se juzga como dato escrito", name)
		}
	}

	// Vacío en un paso que SÍ ofrece campo: sí vuelve a una persona.
	if res := m.ResolveReturnInput(sess, stepText, ReturnInputPrefix+"   "); !res.Rejected() {
		t.Errorf("un campo vacío en un paso con campo se rechaza: %+v", res)
	}
}

// El destino con que se retoma sale de la entrada: hay pasos que NO saben recibir lo que trae el
// agente, y inyectarles el texto los haría escalar otra vez.
func TestResolveReturnInput_TargetComesFromTheInput(t *testing.T) {
	m := textStepMachine(t)
	registerTestInput(t, stepText, ReturnInput{Target: "OTRO_PASO"})

	res := m.ResolveReturnInput(&session.Session{}, stepText, ReturnInputPrefix+"1000000001")
	if !res.OK() || res.Target != "OTRO_PASO" {
		t.Fatalf("res = %+v", res)
	}
}

// El Command declarado en la entrada sobrevive a la resolución, por las tres puertas: el
// worker lo lee para entregar el dato al comando del asesor (cups/orden) en vez de inyectarlo
// como mensaje virtual.
func TestReturnInput_CommandSurvivesResolve(t *testing.T) {
	m := textStepMachine(t)
	sess := &session.Session{}

	res := m.ResolveReturnInput(sess, stepPlain, ReturnInputPrefix+"890274:1")
	if !res.OK() || res.Command != "cups" {
		t.Fatalf("dialecto: %+v", res)
	}
	res = m.ResolveReturnV1(sess, stepPlain, json.RawMessage(`{"dato":"890274:1"}`))
	if !res.OK() || res.Command != "cups" || res.Value != "890274:1" {
		t.Fatalf("v1: %+v", res)
	}

	registerTestInput(t, stepPlain, ReturnInput{
		Command: "orden",
		Parse: func(*session.Session, json.RawMessage) (string, InputRejection) {
			return "texto de la orden", InputRejection{}
		},
	})
	res = m.ResolveReturnV1(sess, stepPlain, json.RawMessage(`{"a":"1","b":"2"}`))
	if !res.OK() || res.Command != "orden" || res.Value != "texto de la orden" {
		t.Fatalf("v1 con parser del paso: %+v", res)
	}

	// Sin Command declarado, el resultado no lleva ninguno: el worker inyecta como mensaje virtual.
	if res := m.ResolveReturnInput(sess, stepText, ReturnInputPrefix+"1000000001"); res.Command != "" {
		t.Fatalf("sin Command no hay comando: %q", res.Command)
	}
}

// El motivo SIGUE SIENDO EL TEXTO, y el campo y la pieza que el bot separa viajan AL LADO. Los
// motivos que no nombran ninguna clave salen sin campo ni pieza: no se inventa un campo donde el
// bot no separa ninguno.
func TestResolveReturnInput_RejectionKeepsFieldAndPiece(t *testing.T) {
	m := textStepMachine(t)
	registerTestInput(t, stepText, ReturnInput{
		Validate: func(*session.Session, string) InputRejection {
			return InputRejection{Reason: "un código no está en la orden: 881235", Field: "cups", Piece: "881235"}
		},
	})
	res := m.ResolveReturnInput(&session.Session{}, stepText, ReturnInputPrefix+"algo largo")
	if !res.Rejected() || res.Problem != "un código no está en la orden: 881235" ||
		res.Field != "cups" || res.Piece != "881235" {
		t.Fatalf("motivo/campo/pieza: %+v", res)
	}

	// Por el validador del paso (sin Validate de la entrada), el rechazo no trae campo ni pieza.
	registerTestInput(t, stepText, ReturnInput{})
	res = m.ResolveReturnInput(&session.Session{}, stepText, ReturnInputPrefix+"ab")
	if !res.Rejected() || res.Field != "" || res.Piece != "" {
		t.Fatalf("campo/pieza tienen que quedar vacíos: %+v", res)
	}
}

// Un rechazo vacío es «no hay rechazo»: es lo que hace que un Validate que acepta no tenga que
// devolver nada especial.
func TestInputRejection_ZeroValueIsNotARejection(t *testing.T) {
	var none InputRejection
	if none.Rejected() {
		t.Error("el valor cero de InputRejection no es un rechazo")
	}
	if !Reject("algo").Rejected() || Reject("algo").Reason != "algo" {
		t.Error("con motivo, sí lo es")
	}
	// Campo o pieza sin motivo no son un veredicto: no hay nada que mostrarle al agente.
	if (InputRejection{Field: "cups"}).Rejected() {
		t.Error("sin motivo no hay rechazo")
	}
}

// --- El retorno del protocolo v1 ---
//
// La bifurcación deja de ser por prefijo y pasa a ser por la PRESENCIA y la FORMA de `valores`;
// los tres desenlaces son los mismos que los del dialecto: se inyecta, vuelve a una persona, o no
// había dato y decide el token.

func TestResolveReturnV1_ValuesParsedBySteps(t *testing.T) {
	m := textStepMachine(t)
	sess := &session.Session{}

	// Sin `valores` (ausente, nulo o vacío) no hay dato escrito: se resuelve por el token.
	for _, values := range []string{"", "null", "{}", "  "} {
		if res := m.ResolveReturnV1(sess, stepText, json.RawMessage(values)); res.Written {
			t.Fatalf("valores=%q: no hay dato escrito: %+v", values, res)
		}
	}

	// Una sola clave de texto es el dato escrito, recortado en los bordes (Néctar recorta y el
	// bot no se fía) y juzgado por el mismo validador del paso.
	res := m.ResolveReturnV1(sess, stepText, json.RawMessage(`{"dato":"  1000000001\n"}`))
	if !res.OK() || res.Value != "1000000001" {
		t.Fatalf("una clave de texto: %+v", res)
	}
	res = m.ResolveReturnV1(sess, stepText, json.RawMessage(`{"dato":"abc"}`))
	if !res.Rejected() || res.Problem != "Escribe al menos cuatro caracteres." {
		t.Fatalf("el validador del paso es el mismo: %+v", res)
	}
	if res := m.ResolveReturnV1(sess, stepText, json.RawMessage(`{"dato":"   "}`)); !res.Rejected() {
		t.Fatalf("un campo vacío vuelve a una persona: %+v", res)
	}

	// Un paso que registra Parse recibe el `valores` ENTERO y crudo; lo que devuelve se inyecta
	// sin volver a validarse, y su rechazo llega con campo y pieza separados.
	var seen json.RawMessage
	registerTestInput(t, stepText, ReturnInput{
		Target: "OTRO_PASO",
		Parse: func(_ *session.Session, values json.RawMessage) (string, InputRejection) {
			seen = values
			return "ok", InputRejection{}
		},
	})
	values := `{"fecha":"2026-09-15","hora":"09:00"}`
	res = m.ResolveReturnV1(sess, stepText, json.RawMessage(values))
	if !res.OK() || res.Value != "ok" || res.Target != "OTRO_PASO" {
		t.Fatalf("parser del paso: %+v", res)
	}
	if string(seen) != values {
		t.Fatalf("el parser recibe `valores` entero y crudo: %s", seen)
	}
	registerTestInput(t, stepText, ReturnInput{
		Parse: func(*session.Session, json.RawMessage) (string, InputRejection) {
			return "", InputRejection{Reason: "un campo excede el largo máximo: nota", Field: "nota"}
		},
	})
	res = m.ResolveReturnV1(sess, stepText, json.RawMessage(`{"nota":"x"}`))
	if !res.Rejected() || res.Problem != "un campo excede el largo máximo: nota" || res.Field != "nota" || res.Piece != "" {
		t.Fatalf("el rechazo del parser llega entero: %+v", res)
	}
}

// Varias claves sin parser del paso, un tipo que no es cadena o un `valores` que no es objeto: el
// bot NO adivina. Va a una persona con el motivo, nunca al paciente.
func TestResolveReturnV1_UnsupportedShapesGoToAPerson(t *testing.T) {
	m := textStepMachine(t)
	cases := map[string]string{
		"dos claves de texto": `{"dato":"1","otro":"2"}`,
		"tipo no cadena":      `{"dato":123}`,
		"objeto anidado":      `{"dato":{"numero":"1"}}`,
		"una lista":           `{"dato":["1"]}`,
		"no es un objeto":     `["1000000001"]`,
		"JSON roto":           `{"dato":`,
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			res := m.ResolveReturnV1(&session.Session{}, stepText, json.RawMessage(values))
			if !res.Rejected() || res.Problem != ReturnValuesUnsupportedProblem {
				t.Fatalf("un solo motivo literal para la forma que no se entiende: %+v", res)
			}
		})
	}
}

// El agente escribió en un paso que no ofrecía campo (menú de otro paso, o la interfaz lo pintó
// donde no debía): no es culpa suya y se reanuda por el token, sin rechazo, sea cual sea la forma.
func TestResolveReturnV1_StepWithoutInputIsNeverBlamed(t *testing.T) {
	m := textStepMachine(t)
	for _, values := range []string{`{"dato":"1000000001"}`, `{"dato":"1","otro":"2"}`} {
		if res := m.ResolveReturnV1(&session.Session{}, stepButton, json.RawMessage(values)); res.Written {
			t.Fatalf("valores=%s: %+v", values, res)
		}
	}
}

// El retorno retoma donde el paso declaró, igual que el dialecto.
func TestResolveReturnV1_ResumesAtTheDeclaredTarget(t *testing.T) {
	m := textStepMachine(t)
	registerTestInput(t, stepText, ReturnInput{Target: "OTRO_PASO"})
	res := m.ResolveReturnV1(&session.Session{}, stepText, json.RawMessage(`{"dato":"881235"}`))
	if !res.OK() || res.Target != "OTRO_PASO" {
		t.Fatalf("res = %+v", res)
	}
}

// El error de `encoding/json` incluye EL CARÁCTER OFENSOR del cuerpo, y el cuerpo puede llevar el
// documento del paciente. La traza dice el PASO y nada más.
func TestResolveReturnV1_UnreadableValuesNeverEchoedInTheLog(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	m := textStepMachine(t)
	// Sin los dos puntos, `encoding/json` responde «invalid character '1' after object key», y
	// ese '1' es el primer dígito del documento del paciente.
	res := m.ResolveReturnV1(&session.Session{}, stepText, json.RawMessage(`{"dato" 1000000001}`))
	if !res.Rejected() {
		t.Fatalf("res = %+v", res)
	}
	line := logged.String()
	if strings.Contains(line, `"error"`) || strings.Contains(line, "1000000001") || strings.Contains(line, "dato") {
		t.Fatalf("el cuerpo no se ecoa en la traza:\n%s", line)
	}
	if !strings.Contains(line, stepText) {
		t.Fatalf("y el paso sí, que es lo que sirve para encontrarlo:\n%s", line)
	}
}

// --- El rechazo tipado que cruza de un turno al otro ---

func TestEncodeDecodeRejection_RoundTrip(t *testing.T) {
	in := PersistedRejection{
		Reason: "Falta la cantidad.", Attempt: 2, Echo: "890274",
		Field: "cups", Piece: "890274",
	}
	raw, err := EncodeRejection(in)
	if err != nil {
		t.Fatal(err)
	}
	// Las claves son las del cable, en español, y las vacías no viajan.
	var wire map[string]any
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"motivo", "intento", "eco", "campo", "pieza"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("falta la clave de cable %q: %s", k, raw)
		}
	}
	got, ok := DecodeRejection(raw)
	if !ok || got != in {
		t.Fatalf("ida y vuelta: ok=%v got=%+v want=%+v", ok, got, in)
	}

	onlyReason, _ := EncodeRejection(PersistedRejection{Reason: "x"})
	if onlyReason != `{"motivo":"x"}` {
		t.Errorf("solo el motivo es obligatorio, el resto va ausente: %s", onlyReason)
	}

	// Ausente, ilegible o sin motivo son lo mismo para quien arma el sobre: no hay rechazo que emitir.
	for _, raw := range []string{"", "{no es json", `{"intento":3}`, `{"motivo":""}`} {
		if _, ok := DecodeRejection(raw); ok {
			t.Errorf("DecodeRejection(%q) no debe dar rechazo", raw)
		}
	}
}

// Una copia sin leer del literal que Néctar destaca en el resumen: si cambia, hay que avisarles.
func TestRejectedInputNote_UsesTheWireMarker(t *testing.T) {
	note := RejectedInputNote(2, "abc", "Falta la cantidad.")
	if !strings.HasPrefix(note, RejectedInputMarker) || RejectedInputMarker != "EL DATO QUE SE DEVOLVIO NO SE PUDO USAR" {
		t.Fatalf("la nota empieza por el literal de cable: %q", note)
	}
	for _, want := range []string{"(intento 2)", `Se recibio: "abc"`, "Motivo: Falta la cantidad."} {
		if !strings.Contains(note, want) {
			t.Errorf("la nota no trae %q: %s", want, note)
		}
	}
}

// Solo un campo requerido rechaza el vacío: uno opcional lo acepta como «sin dato». Vale por las
// dos puertas, y «sin campo» sigue yendo antes que «vacío».
func TestResolveReturnInput_RequiredGovernsEmptyValue(t *testing.T) {
	m := textStepMachine(t)
	sess := &session.Session{}

	// stepPlain se registró sin Required.
	res := m.ResolveReturnInput(sess, stepPlain, ReturnInputPrefix+"   ")
	if res.Written || res.OK() || res.Rejected() || res.Command != "" {
		t.Fatalf("un campo opcional vacío es «sin dato»: NO escrito, sin comando: %+v", res)
	}
	if res := m.ResolveReturnV1(sess, stepPlain, json.RawMessage(`{"dato":"  "}`)); res.Written || res.OK() || res.Rejected() {
		t.Fatalf("v1 igual: %+v", res)
	}

	registerTestInput(t, stepPlain, ReturnInput{Required: true})
	if res := m.ResolveReturnInput(sess, stepPlain, ReturnInputPrefix+"   "); !res.Rejected() {
		t.Fatalf("un campo requerido vacío se rechaza: %+v", res)
	}
	if res := m.ResolveReturnV1(sess, stepPlain, json.RawMessage(`{"dato":"  "}`)); !res.Rejected() {
		t.Fatalf("v1 igual: %+v", res)
	}
}

func TestRegisterReturnInput_UnknownCommandPanics(t *testing.T) {
	for _, cmd := range []string{"", ReturnCommandCups, ReturnCommandOrder} {
		registerTestInput(t, "RETURN_TEST_CMD", ReturnInput{Command: cmd}) // no hace panic
	}
	if ReturnCommandCups != "cups" || ReturnCommandOrder != "orden" {
		t.Fatalf("literales: %q %q", ReturnCommandCups, ReturnCommandOrder)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("un Command desconocido es un error de programación y debe hacer panic")
		}
	}()
	RegisterReturnInput("RETURN_TEST_CMD_BAD", ReturnInput{Command: "otro"})
}

// HidePlainResume quita «Devolver al bot en el mismo paso» de un paso donde reanudar tal cual
// no lleva a ninguna parte; el resto de las universales sigue.
func TestReturnOptionsFor_HiddenPlainResume(t *testing.T) {
	const step = "T1_HIDE"
	registerTestMenu(t, step, ReturnOption{Label: "Otra", Value: "otra", Target: StateGreeting})
	t.Cleanup(func() {
		returnMenusMu.Lock()
		defer returnMenusMu.Unlock()
		delete(hiddenPlainResume, step)
	})
	HidePlainResume(step)

	var hasResume, hasClose bool
	for _, o := range ReturnOptionsFor(nil, step) {
		hasResume = hasResume || o.Value == ReturnResume
		hasClose = hasClose || o.Value == ReturnClose
	}
	if hasResume {
		t.Error("el paso oculta «devolver en el mismo paso»")
	}
	if !hasClose {
		t.Error("las demás universales siguen")
	}
}

// HidePlainResumeWhen oculta «Devolver al bot en el mismo paso» solo en las sesiones que el
// predicado señala; en las demás (y sin sesión, si el predicado lo dice) se ofrece.
func TestReturnOptionsFor_HiddenPlainResumeWhen(t *testing.T) {
	const step = "T1_HIDE_WHEN"
	registerTestMenu(t, step, ReturnOption{Label: "Otra", Value: "otra", Target: StateGreeting})
	t.Cleanup(func() {
		returnMenusMu.Lock()
		defer returnMenusMu.Unlock()
		delete(hiddenPlainResume, step)
	})
	HidePlainResumeWhen(step, func(sess *session.Session) bool {
		return sess != nil && sess.GetContext("tipo") == "otro"
	})
	offers := func(sess *session.Session) bool {
		for _, o := range ReturnOptionsFor(sess, step) {
			if o.Value == ReturnResume {
				return true
			}
		}
		return false
	}
	hidden := &session.Session{Context: map[string]string{"tipo": "otro"}}
	shown := &session.Session{Context: map[string]string{"tipo": "normal"}}
	if offers(hidden) {
		t.Error("el predicado oculta la reanudación en esa sesión")
	}
	if !offers(shown) || !offers(nil) {
		t.Error("donde el predicado no la oculta, se ofrece")
	}
}

// Un valor de `lista` que no está entre las opciones del paso se rechaza ANTES de la validación
// propia: la consola solo valida en el cliente, y el bot no se fía.
func TestResolveReturnV1_ListValueOutsideOptionsIsRejected(t *testing.T) {
	m := NewMachine()
	const step = "T5_LIST"
	validated := false
	registerTestInput(t, step, ReturnInput{
		Kind: InputKindList, Label: "Entidad",
		Options: func(sess *session.Session) []InputChoice {
			if sess == nil {
				return nil
			}
			return []InputChoice{{Label: "Uno", Value: "1"}, {Label: "Dos", Value: "2"}}
		},
		Validate: func(*session.Session, string) InputRejection {
			validated = true
			return InputRejection{}
		},
	})
	sess := &session.Session{Context: map[string]string{}}

	res := m.ResolveReturnV1(sess, step, json.RawMessage(`{"dato":"7"}`))
	if !res.Rejected() || res.Problem != "El valor elegido no está en la lista de este paso." {
		t.Fatalf("fuera de la lista se rechaza: %+v", res)
	}
	if validated {
		t.Error("el rechazo por lista va antes de Validate")
	}
	if res := m.ResolveReturnV1(sess, step, json.RawMessage(`{"dato":"2"}`)); !res.OK() || res.Value != "2" {
		t.Fatalf("un valor de la lista se acepta: %+v", res)
	}
}

// Si la entrada declara Validate, ésa es LA regla: el TextValidate del paso no se aplica. Sin
// Validate, se aplica el del paso (ASK_DOCUMENT sigue igual).
func TestResolveReturnInput_ValidateReplacesTheStepTextValidate(t *testing.T) {
	m := NewMachine()
	const step = "T5_VALIDATE"
	m.RegisterWithConfig(step, HandlerConfig{
		InputType:    InputText,
		TextValidate: func(s string) bool { return s != "NA" },
		ErrorMsg:     "No se acepta NA.",
		Handler:      noopHandler,
	})
	registerTestInput(t, step, ReturnInput{Label: "Correo"})
	sess := &session.Session{}
	if res := m.ResolveReturnInput(sess, step, ReturnInputPrefix+"NA"); !res.Rejected() {
		t.Fatalf("sin Validate se aplica el TextValidate del paso: %+v", res)
	}

	registerTestInput(t, step, ReturnInput{
		Label:    "Correo",
		Validate: func(*session.Session, string) InputRejection { return InputRejection{} },
	})
	if res := m.ResolveReturnInput(sess, step, ReturnInputPrefix+"NA"); !res.OK() {
		t.Fatalf("con Validate, el TextValidate del paso no se aplica: %+v", res)
	}
}

// RegisterResumeLabel re-rotula «devolver en el mismo paso» sin cambiar su token.
func TestReturnOptionsFor_ResumeLabelOverride(t *testing.T) {
	const step = "T5_RESUME_LABEL"
	t.Cleanup(func() {
		returnMenusMu.Lock()
		defer returnMenusMu.Unlock()
		delete(resumeLabels, step)
	})
	RegisterResumeLabel(step, "Devolver con el dato escrito")

	var found bool
	for _, o := range ReturnOptionsFor(nil, step) {
		if o.Value == ReturnResume {
			found = true
			if o.Label != "Devolver con el dato escrito" {
				t.Errorf("rótulo = %q", o.Label)
			}
		}
	}
	if !found {
		t.Fatal("la opción bot_reanudar sigue en el menú")
	}
	for _, o := range ReturnOptionsFor(nil, "T5_OTRO_PASO") {
		if o.Value == ReturnResume && o.Label != "Devolver al bot en el mismo paso" {
			t.Errorf("los demás pasos conservan el rótulo: %q", o.Label)
		}
	}
	if !IsUniversalReturnValue(ReturnResume) || !IsUniversalReturnValue(ReturnClose) || IsUniversalReturnValue("otra") {
		t.Error("IsUniversalReturnValue reconoce solo las universales")
	}
}

// RegisteredReturnSteps lista, ordenados y sin repetir, los pasos con menú o con entrada.
func TestRegisteredReturnSteps_ListsMenusAndInputs(t *testing.T) {
	registerTestMenu(t, "T5_REG_B", ReturnOption{Label: "x", Value: "x"})
	registerTestInput(t, "T5_REG_A", ReturnInput{Label: "y", Required: true})
	registerTestMenu(t, "T5_REG_C", ReturnOption{Label: "z", Value: "z"})
	registerTestInput(t, "T5_REG_C", ReturnInput{Label: "z"})

	steps := RegisteredReturnSteps()
	var mine []string
	for _, s := range steps {
		if strings.HasPrefix(s, "T5_REG_") {
			mine = append(mine, s)
		}
	}
	if strings.Join(mine, ",") != "T5_REG_A,T5_REG_B,T5_REG_C" {
		t.Fatalf("pasos registrados = %v", mine)
	}
	for i := 1; i < len(steps); i++ {
		if steps[i-1] >= steps[i] {
			t.Fatalf("ordenados y sin repetir: %v", steps)
		}
	}
	if !ReturnInputRequired("T5_REG_A") || ReturnInputRequired("T5_REG_C") || ReturnInputRequired("T5_REG_B") {
		t.Error("ReturnInputRequired lee el Required de la entrada")
	}
}

// Una lista que se quedó sin opciones no admite ningún valor.
func TestResolveReturnV1_ListWithoutOptionsRejectsEverything(t *testing.T) {
	m := NewMachine()
	const step = "T5_LIST_EMPTY"
	registerTestInput(t, step, ReturnInput{
		Kind: InputKindList, Label: "Entidad",
		Options: func(*session.Session) []InputChoice { return nil },
	})
	res := m.ResolveReturnV1(&session.Session{}, step, json.RawMessage(`{"dato":"1"}`))
	if !res.Rejected() || res.Problem != ReturnInputNotInListProblem {
		t.Fatalf("sin opciones se rechaza: %+v", res)
	}
}

// Las combinaciones de tipo y formato que la consola no sabría pintar se detectan al arrancar.
func TestRegisterReturnInput_KindAndFormatMustMatch(t *testing.T) {
	for name, in := range map[string]ReturnInput{
		"fecha sin iso":      {Kind: InputKindDate},
		"hora sin iso":       {Kind: InputKindTime},
		"fecha otro formato": {Kind: InputKindDate, Format: "dd/mm"},
		"formato en texto":   {Kind: InputKindText, Format: InputFormatISO},
		"formato sin tipo":   {Format: InputFormatISO},
		"lista sin opciones": {Kind: InputKindList},
		"opciones en texto":  {Options: func(*session.Session) []InputChoice { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("debía hacer panic")
				}
				UnregisterReturnStepForTest("T5_PANIC")
			}()
			RegisterReturnInput("T5_PANIC", in)
		})
	}
	// Las válidas no hacen panic.
	registerTestInput(t, "T5_OK_DATE", ReturnInput{Kind: InputKindDate, Format: InputFormatISO})
	registerTestInput(t, "T5_OK_TIME", ReturnInput{Kind: InputKindTime, Format: InputFormatISO})
}

// La forma del motivo del candado se exige al registrarlo: viaja en el cable de Néctar, que lo
// guarda como cadena opaca de hasta 64 runas.
func TestResolveGuard_ShapeIsEnforced(t *testing.T) {
	mustPanic := func(name, motivo string) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: un motivo %q fuera de ^[a-z_]{1,64}$ debe hacer panic", name, motivo)
			}
		}()
		RegisterResolveGuard("RETURN_TEST_SHAPE", motivo)
	}
	mustPanic("mayúscula", "Retorno")
	mustPanic("65 runas", strings.Repeat("a", 65))
	mustPanic("espacio", "cita pendiente")

	t.Cleanup(func() { RegisterResolveGuard("RETURN_TEST_SHAPE", "") })
	RegisterResolveGuard("RETURN_TEST_SHAPE", "")                      // limpiar no es un motivo
	RegisterResolveGuard("RETURN_TEST_SHAPE", strings.Repeat("a", 64)) // el límite exacto vale
}

// "" significa «sin candado»: borra la entrada, no la deja vacía contando en lo declarado.
func TestResolveGuard_EmptyMotivoRemovesTheEntry(t *testing.T) {
	RegisterResolveGuard("RETURN_TEST_EMPTY", "cita_pendiente")
	t.Cleanup(func() { RegisterResolveGuard("RETURN_TEST_EMPTY", "") })
	if _, ok := DeclaredResolveGuards()["RETURN_TEST_EMPTY"]; !ok {
		t.Fatal("el candado registrado debe figurar")
	}
	RegisterResolveGuard("RETURN_TEST_EMPTY", "")
	if _, ok := DeclaredResolveGuards()["RETURN_TEST_EMPTY"]; ok {
		t.Fatal("un motivo vacío debe borrar la entrada de lo declarado")
	}
}
