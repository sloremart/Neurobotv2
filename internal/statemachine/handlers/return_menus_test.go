package handlers

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Los menús y entradas de retorno de los pasos escalables de Neuro (Task 9c). Reflejan lo que
// buildAgentCommands le ofrece hoy a un agente de Bird como `/bot resume ESTADO dato`.

// returnMenuMachine registra los handlers que declaran menús de retorno, con dependencias nil:
// el registro no las toca. Los registros de menú son globales, así que basta con registrarlos.
func returnMenuMachine() *sm.Machine {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)
	RegisterIdentificationHandlers(m, nil)
	RegisterMedicalOrderHandlers(m, nil, nil, nil, nil, nil)
	RegisterAppointmentHandlers(m, nil, nil, nil, nil)
	RegisterSlotHandlers(m, nil, nil, nil, nil, nil, nil, nil, nil)
	return m
}

// stepsWithReturnMenu son los pasos que 9c declara, con lo que ofrecen.
var (
	// cupsSteps entregan lo escrito al comando de CUPS del asesor.
	cupsSteps = []string{
		sm.StateAskMedicalOrder, sm.StateUploadMedicalOrder, sm.StateValidateOCR,
		sm.StateOCRFailed, sm.StateConfirmOCRResult, sm.StateAskManualCups,
	}
	declaredSteps = append([]string{
		sm.StateMainMenu, sm.StateAskDocumentType, sm.StateAskDocument,
		sm.StateNotifPending, sm.StateCoverageNoConvenio,
	}, cupsSteps...)
)

// universalReturnOptions es cuántas opciones lleva TODO paso aunque no declare ninguna (las
// universales de ReturnOptionsFor), medido sobre un paso sin registro.
var universalReturnOptions = len(sm.ReturnOptionsFor(nil, "PASO_SIN_REGISTRO_9C"))

// hasReturnEntry dice si un paso ofrece al agente algún botón propio o un campo.
func hasReturnEntry(m *sm.Machine, step string) bool {
	d := m.InputDescriptor(nil, step)
	return len(d.Fields) > 0 || len(d.Options) > universalReturnOptions
}

func TestReturnMenuValuesExistInTheStateMachine(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range declaredSteps {
		if !m.IsRegistered(step) {
			t.Errorf("%s: el paso declara retorno pero no está registrado", step)
		}
		if !hasReturnEntry(m, step) {
			t.Errorf("%s: no declara ni opciones propias ni campo", step)
		}
		seen := map[string]bool{}
		for _, o := range sm.ReturnOptionsFor(nil, step) {
			if o.Label == "" || o.Value == "" {
				t.Errorf("%s: opción incompleta %+v", step, o)
			}
			if strings.HasPrefix(o.Value, sm.ReturnInputPrefix) {
				t.Errorf("%s: el valor %q usa el prefijo reservado a lo escrito por el agente", step, o.Value)
			}
			if seen[o.Value] {
				t.Errorf("%s: valor %q repetido", step, o.Value)
			}
			seen[o.Value] = true
			if o.Target != "" && !m.IsRegistered(o.Target) {
				t.Errorf("%s: la opción %q retoma en %q, que no es un estado registrado", step, o.Value, o.Target)
			}
		}
		if in := m.InputDescriptor(nil, step); in.ResumesAt != "" && !m.IsRegistered(in.ResumesAt) {
			t.Errorf("%s: el campo retoma en %q, que no es un estado registrado", step, in.ResumesAt)
		}
	}
}

func TestReturnMenus_NotifPendingIsStaticAndMatchesTheWorkerShortcut(t *testing.T) {
	// Task 10 arma el descriptor de NOTIF_PENDING SIN sesión ni máquina, y el worker reconoce
	// `State == NOTIF_PENDING` + `Data` ∈ {confirm, reschedule, cancel}.
	returnMenuMachine()
	var got []string
	for _, o := range sm.ReturnOptionsFor(nil, sm.StateNotifPending) {
		if o.Value == sm.ReturnResume || o.Value == sm.ReturnClose {
			continue
		}
		if o.Target != sm.StateNotifPending || o.Data != o.Value {
			t.Errorf("opción %+v: debe retomar en NOTIF_PENDING con Data = acción", o)
		}
		got = append(got, o.Data)
	}
	if want := []string{"confirm", "reschedule", "cancel"}; !slices.Equal(got, want) {
		t.Fatalf("acciones = %v, quiero %v", got, want)
	}
}

// El descriptor de NOTIF_PENDING sin máquina (lo que usa el NotificationManager) es el mismo que
// el de la máquina: si alguien le añade un campo al paso, esto avisa de que el atajo ya no basta.
func TestReturnMenus_NotifPendingStaticDescriptorMatchesTheMachine(t *testing.T) {
	m := returnMenuMachine()
	want, err := json.Marshal(m.InputDescriptor(nil, sm.StateNotifPending))
	if err != nil {
		t.Fatal(err)
	}
	if got := sm.StaticInputDescriptor(sm.StateNotifPending); string(got) != string(want) {
		t.Fatalf("StaticInputDescriptor = %s\nMachine.InputDescriptor = %s", got, want)
	}
}

func TestReturnMenus_DocumentInputUsesTheStepValidator(t *testing.T) {
	m := returnMenuMachine()
	sess := testSess(sm.StateAskDocument)
	if r := m.ResolveReturnInput(sess, sm.StateAskDocument, sm.ReturnInputPrefix+"abc"); !r.Rejected() {
		t.Errorf("un documento con letras debe rechazarse, fue %+v", r)
	}
	if r := m.ResolveReturnInput(sess, sm.StateAskDocument, sm.ReturnInputPrefix+"1020304050"); !r.OK() {
		t.Errorf("un documento válido debe aceptarse, fue %+v", r)
	}
}

func TestReturnMenus_CupsInputUsesTheAgentCommandGrammar(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range cupsSteps {
		sess := testSess(step)
		in := m.InputDescriptor(nil, step)
		if len(in.Fields) != 1 || in.Fields[0].Kind != sm.InputKindText || !in.Fields[0].Required {
			t.Errorf("%s: campo = %+v, quiero un único campo de texto requerido", step, in.Fields)
		}
		bad := m.ResolveReturnInput(sess, step, sm.ReturnInputPrefix+":::")
		if !bad.Rejected() || bad.Problem != services.AgentCupsNoCodesProblem {
			t.Errorf("%s: sin ningún código se rechaza con el motivo del comando, fue %+v", step, bad)
		}
		ok := m.ResolveReturnInput(sess, step, sm.ReturnInputPrefix+"883141:1 930810:2")
		if !ok.OK() || ok.Command != sm.ReturnCommandCups {
			t.Errorf("%s: CUPS válidos van al comando cups, fue %+v", step, ok)
		}
	}
}

// deferredAgentCommandSteps son los estados que buildAgentCommands nombra y que 9c NO declara:
// la tarea cubre los pasos de su alcance (menú, identificación, orden médica, notificación y
// cobertura). Cada uno de estos conserva hoy SOLO el comando `/bot resume` de Bird. Declararlos
// es trabajo de la fase B: hasta entonces estos pasos devuelven SOLO con las opciones universales
// (reanudar / cerrar). La lista está FIJADA aquí para que nadie quede fuera en silencio: un paso
// nuevo en buildAgentCommands, o uno de estos que gane registro, hace fallar la prueba.
var deferredAgentCommandSteps = []string{
	"OUT_OF_HOURS_MENU", "CONFIRM_IDENTITY", "CONFIRM_CONTACT_INFO", "ASK_UPDATE_PHONE",
	"ASK_UPDATE_EMAIL", "ASK_EPS_REGIMEN", "ASK_CLIENT_TYPE", "ASK_ENTITY_NUMBER",
	"CONFIRM_ENTITY", "CHANGE_ENTITY", "SHOW_ENTITY_LIST", "REGISTRATION_START",
	"CONFIRM_REGISTRATION", "REG_SELECT_CORRECTION", "SELECT_PROCEDURE",
	"ASK_GESTATIONAL_WEEKS", "ASK_CONTRASTED", "ASK_PREGNANCY", "ASK_BABY_WEIGHT",
	"GFR_CREATININE", "GFR_HEIGHT", "GFR_WEIGHT", "GFR_DISEASE", "ASK_SEDATION",
	"CHECK_SPECIAL_CUPS", "SHOW_SLOTS", "OFFER_WAITING_LIST", "CONFIRM_BOOKING",
	"RECONFIRM_BOOKING", "NO_APPOINTMENTS", "LIST_APPOINTMENTS", "APPOINTMENT_ACTION",
	"CONFIRM_APPOINTMENT", "CANCEL_APPOINTMENT", "CANCEL_REASON",
	"CONFIRM_RESCHEDULE_NOTIF", "CONFIRM_CANCEL_NOTIF", "NOTIF_RESCHEDULE_FALLBACK",
	"POST_ACTION_MENU", "FALLBACK_MENU",
}

// agentCommandSteps lee los estados que buildAgentCommands nombra con un `case` propio. Se lee
// del código fuente porque la función cae a un caso genérico con cualquier estado, y por eso
// llamarla no distingue los que tienen instrucciones propias.
func agentCommandSteps(t *testing.T) []string {
	t.Helper()
	statesSrc, err := os.ReadFile("../states.go")
	if err != nil {
		t.Fatal(err)
	}
	value := map[string]string{}
	for _, m := range regexp.MustCompile(`(State\w+)\s*=\s*"([A-Z_]+)"`).FindAllStringSubmatch(string(statesSrc), -1) {
		value[m[1]] = m[2]
	}
	escSrc, err := os.ReadFile("escalation.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(escSrc)
	start := strings.Index(body, "func buildAgentCommands(")
	if start < 0 {
		t.Fatal("buildAgentCommands no está en escalation.go")
	}
	var steps []string
	for _, m := range regexp.MustCompile(`(?m)^\s*case sm\.(State\w+):`).FindAllStringSubmatch(body[start:], -1) {
		v, ok := value[m[1]]
		if !ok {
			t.Fatalf("la constante %s no está en states.go", m[1])
		}
		steps = append(steps, v)
	}
	if len(steps) < 40 {
		t.Fatalf("solo se leyeron %d estados de buildAgentCommands", len(steps))
	}
	return steps
}

func TestReturnMenusCoverAgentCommands(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range agentCommandSteps(t) {
		covered := hasReturnEntry(m, step)
		deferred := slices.Contains(deferredAgentCommandSteps, step)
		switch {
		case !covered && !deferred:
			t.Errorf("%s: buildAgentCommands lo nombra pero no tiene menú ni entrada, y no está en la lista de aplazados", step)
		case covered && deferred:
			t.Errorf("%s: ya declara retorno; quitarlo de deferredAgentCommandSteps", step)
		}
	}
	// Los aplazados deben seguir siendo estados que buildAgentCommands nombra.
	named := agentCommandSteps(t)
	for _, step := range deferredAgentCommandSteps {
		if !slices.Contains(named, step) {
			t.Errorf("%s: figura como aplazado pero buildAgentCommands ya no lo nombra", step)
		}
	}
}

// El descriptor no puede mentir: los `tipos` y las `salidas` que EMITEN los pasos tienen que ser
// los que publica nectar.Capabilities (Task 5), en los dos sentidos.
//
// Es IGUALDAD estricta: lo que no emite ningún paso no se publica, y viceversa.
func TestElRegistroDelDescriptorNoMiente(t *testing.T) {
	m := returnMenuMachine()
	kinds, exits := map[string]bool{}, map[string]bool{}
	for _, step := range declaredSteps {
		d := m.InputDescriptor(nil, step)
		for _, f := range d.Fields {
			kinds[f.Kind] = true
		}
		for _, o := range d.Options {
			exits[o.Exit] = true
		}
	}

	published := nectar.Capabilities(config.ProtocolEscaladoV1).Capabilities.Input

	for k := range kinds {
		if !slices.Contains(published.Kinds, k) {
			t.Errorf("un paso emite el tipo %q que Capabilities no publica", k)
		}
	}
	for _, k := range published.Kinds {
		if !kinds[k] {
			t.Errorf("Capabilities publica el tipo %q y ningún paso lo emite", k)
		}
	}
	for e := range exits {
		if !slices.Contains(published.Exits, e) {
			t.Errorf("un paso emite la salida %q que Capabilities no publica", e)
		}
	}
	for _, e := range published.Exits {
		if !exits[e] {
			t.Errorf("Capabilities publica la salida %q y ningún paso la emite", e)
		}
	}
}

// BOT_DISABLED no es un estado con handler: «devolver tal cual» lo rompería. El menú ofrece
// empezar desde el saludo y esconde la reanudación simple.
func TestBotDisabledReturnMenu_OffersGreetingAndHidesResume(t *testing.T) {
	m := sm.NewMachine()
	// El registro es global y de solo escritura idempotente (siempre el mismo menú): dejarlo
	// puesto no contamina a otras pruebas, igual que el resto de returnMenuMachine.
	RegisterEscalationHandlers(m, nil, nil, nil)

	var found bool
	for _, o := range sm.ReturnOptionsFor(nil, sm.BotDisabledStep) {
		if o.Value == sm.ReturnResume {
			t.Error("BOT_DISABLED no ofrece reanudar en el mismo paso")
		}
		if o.Value == "bot_iniciar" {
			found = o.Target == sm.StateGreeting && o.NoInput
		}
	}
	if !found {
		t.Error("falta la opción bot_iniciar hacia GREETING sin dato")
	}
}
