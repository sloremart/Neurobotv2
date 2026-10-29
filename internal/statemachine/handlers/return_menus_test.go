package handlers

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
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
	// Los registros de estos handlers no tocan sus dependencias: se pasan nil.
	RegisterRegistrationHandlers(m, nil, nil)
	RegisterEntityManagementHandlers(m, nil, nil)
	RegisterMedicalValidationHandlers(m, nil, nil, nil)
	RegisterPostActionHandlers(m, nil)
	RegisterMedicationHandlers(m, nil, nil)
	RegisterResultsAndLocationHandlers(m, sampleConfig(), nil)
	// BOT_DISABLED registra su menú aquí: sin él, que figurara o no en RegisteredReturnSteps
	// dependería del orden de las pruebas.
	RegisterEscalationHandlers(m, nil, nil, nil)
	return m
}

// cupsSteps entregan lo escrito al comando de CUPS del asesor.
var cupsSteps = []string{
	sm.StateAskMedicalOrder, sm.StateUploadMedicalOrder, sm.StateValidateOCR,
	sm.StateOCRFailed, sm.StateConfirmOCRResult, sm.StateAskManualCups,
}

// hasReturnEntry dice si un paso ofrece al agente algún botón propio (no universal) o un campo.
func hasReturnEntry(m *sm.Machine, step string) bool {
	d := m.InputDescriptor(nil, step)
	if len(d.Fields) > 0 {
		return true
	}
	for _, o := range d.Options {
		if !sm.IsUniversalReturnValue(o.Value) {
			return true
		}
	}
	return false
}

func TestReturnMenuValuesExistInTheStateMachine(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range sm.RegisteredReturnSteps() {
		// BOT_DISABLED es el único paso con menú que no es un estado (el del kill switch).
		if step != sm.BotDisabledStep && !m.IsRegistered(step) {
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
	// El descriptor de NOTIF_PENDING se arma SIN máquina; sin sesión sale el menú de la
	// confirmación, y el worker reconoce `State == NOTIF_PENDING` + `Data` = acción.
	returnMenuMachine()
	var got []string
	for _, o := range sm.ReturnOptionsFor(nil, sm.StateNotifPending) {
		if sm.IsUniversalReturnValue(o.Value) {
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
	if got := sm.StaticInputDescriptor(nil, sm.StateNotifPending); string(got) != string(want) {
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
	"ASK_UPDATE_PHONE", "ASK_UPDATE_EMAIL", "ASK_ENTITY_NUMBER", "CHANGE_ENTITY",
	"SHOW_ENTITY_LIST", "SELECT_PROCEDURE", "ASK_GESTATIONAL_WEEKS", "GFR_CREATININE",
	"GFR_HEIGHT", "GFR_WEIGHT", "CHECK_SPECIAL_CUPS", "SHOW_SLOTS", "LIST_APPOINTMENTS",
}

// unreachableAgentCommandSteps son pasos que buildAgentCommands nombra pero que NO son un estado
// de la máquina (sin handler): nunca se escala desde ellos, así que no llevan menú.
var unreachableAgentCommandSteps = []string{"CANCEL_REASON"}

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
		if slices.Contains(unreachableAgentCommandSteps, step) {
			if m.IsRegistered(step) {
				t.Errorf("%s: figura como inalcanzable pero tiene handler registrado", step)
			}
			continue
		}
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
	for _, step := range append(slices.Clone(deferredAgentCommandSteps), unreachableAgentCommandSteps...) {
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
	for _, step := range sm.RegisteredReturnSteps() {
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

// NOTIF_PENDING ofrece las acciones del TIPO de notificación (y del motivo): la lista de espera y
// la cancelación tienen sus propios botones, y el fallo de persistencia en SIESA ofrece reintentar
// o dar la cita por confirmada a mano. Sin sesión sale el menú de confirmación de siempre.
func TestNotifPendingReturnOptions_ByTypeAndDetail(t *testing.T) {
	type want struct {
		label, value, target, data string
		close                      bool
	}
	view := func(kv ...string) *session.Session {
		s := testSess(sm.StateNotifPending)
		for i := 0; i+1 < len(kv); i += 2 {
			s.Context[kv[i]] = kv[i+1]
		}
		return s
	}
	np := sm.StateNotifPending
	cases := []struct {
		name string
		sess *session.Session
		want []want
	}{
		{"fallo de sistema", view(sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifSystemFailure, "notif_type", "confirmation"), []want{
			{"Reintentar la confirmación en SIESA", "confirm", np, "confirm", false},
			{"Ya la confirmé a mano en SIESA", "confirmada_manual", "", "", true},
		}},
		{"lista de espera", view("notif_type", "waiting_list"), []want{
			{"Agendar ahora", "wl_schedule", np, "wl_schedule", false},
			{"No le interesa", "wl_decline", np, "wl_decline", false},
		}},
		{"cancelación", view("notif_type", "cancellation"), []want{
			{"Entendido, sin reprogramar", "understood", np, "understood", false},
			{"Reprogramar", "reschedule", np, "reschedule", false},
		}},
		{"confirmación", view("notif_type", "confirmation", sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifNoResponse), []want{
			{"Confirmar la cita", "confirm", np, "confirm", false},
			{"Reprogramar la cita", "reschedule", np, "reschedule", false},
			{"Cancelar la cita", "cancel", np, "cancel", false},
		}},
		{"sin sesión", nil, []want{
			{"Confirmar la cita", "confirm", np, "confirm", false},
			{"Reprogramar la cita", "reschedule", np, "reschedule", false},
			{"Cancelar la cita", "cancel", np, "cancel", false},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := notifPendingReturnOptions(c.sess)
			if len(got) != len(c.want) {
				t.Fatalf("opciones = %+v, quiero %+v", got, c.want)
			}
			for i, w := range c.want {
				o := got[i]
				if o.Label != w.label || o.Value != w.value || o.Target != w.target || o.Data != w.data || o.Close != w.close {
					t.Errorf("opción %d = %+v, quiero %+v", i, o, w)
				}
			}
			// El registro es el que leen el descriptor y el worker: las opciones del tipo van
			// primero y las universales después.
			returnMenuMachine()
			if reg := sm.ReturnOptionsFor(c.sess, np); len(reg) < len(c.want) || reg[0].Value != c.want[0].value {
				t.Errorf("el registro de NOTIF_PENDING no es notifPendingReturnOptions: %+v", reg)
			}
		})
	}
}

// En un paso con campo, Néctar adjunta lo escrito a la PRIMERA salida «reanudar», que tiene que
// ser bot_reanudar: ninguna opción propia del paso puede ser «reanudar» (NoInput o Close).
func TestReturnInputSteps_OwnOptionsNeverCarryTheValues(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range sm.RegisteredReturnSteps() {
		if len(m.InputDescriptor(nil, step).Fields) == 0 {
			continue
		}
		for _, o := range sm.ReturnOptionsFor(nil, step) {
			if !sm.IsUniversalReturnValue(o.Value) && !o.Close && !o.NoInput {
				t.Errorf("%s: la opción %q es «reanudar» en un paso con campo y recibiría lo escrito; debe ser NoInput o Close",
					step, o.Value)
			}
		}
	}
}

// ownData son los `Data` de las opciones propias del paso (las universales no cuentan, y las
// opciones solo-Target, como buscar_horarios, tampoco: no inyectan ningún payload).
func ownData(step string) []string {
	var got []string
	for _, o := range sm.ReturnOptionsFor(nil, step) {
		if !sm.IsUniversalReturnValue(o.Value) && o.Data != "" {
			got = append(got, o.Data)
		}
	}
	slices.Sort(got)
	return got
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func rowIDs(rows []sm.ListRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// handlerButtonPayloads son, por paso, los payloads que el handler acepta HOY. Los pasos con
// HandlerConfig se cruzan además con cfg.Options; los que validan con ValidateButtonResponse
// directo (sin config) se fijan aquí, tomados del código de cada handler.
func handlerButtonPayloads() map[string][]string {
	docTypes := make([]string, len(documentTypeCatalog))
	for i, d := range documentTypeCatalog {
		docTypes[i] = d.Code
	}
	return map[string][]string{
		sm.StateConfirmIdentity:         {"identity_yes", "identity_no"},
		sm.StateConfirmContactInfo:      {"contact_ok", "contact_update"},
		sm.StateAskEpsRegimen:           {"regimen_1", "regimen_2"},
		sm.StateAskClientType:           {"ct_1", "ct_2", "ct_3", "ct_4", "ct_5", "ct_6"},
		sm.StateConfirmEntity:           {"entity_ok", "entity_change"},
		sm.StateRegistrationStart:       {"register_yes", "register_no"},
		sm.StateConfirmRegistration:     {"reg_confirm", "reg_correct"},
		sm.StateRegSelectCorrection:     correctionPayloads(),
		sm.StateRegDocumentType:         docTypes,
		sm.StateRegGender:               {"M", "F"},
		sm.StateRegZone:                 {"U", "R"},
		sm.StateRegBloodType:            bloodTypeValues(),
		sm.StateRegMaritalStatus:        rowIDs(maritalStatusListRows()),
		sm.StateRegEducationLevel:       educationLevelValues(),
		sm.StateRegUserType:             userTypePayloads(),
		sm.StateRegAffiliationType:      affiliationTypeValues(),
		sm.StateConfirmMunicipality:     {"muni_ok", "muni_change"},
		sm.StateAskContrasted:           {"contrast_yes", "contrast_no"},
		sm.StateAskPregnancy:            {"pregnant_no", "pregnant_yes"},
		sm.StateAskBabyWeight:           {"baby_normal", "baby_low"},
		sm.StateGfrDisease:              {"disease_none", "disease_renal", "disease_diabetica"},
		sm.StateAskSedation:             {"sedated_yes", "sedated_no"},
		sm.StateOfferWaitingList:        {"wl_yes", "wl_no"},
		sm.StateConfirmBooking:          {"booking_confirm", "booking_change"},
		sm.StateReconfirmBooking:        {"reconfirm_yes", "reconfirm_no"},
		sm.StateSlotSearchRetry:         {"retry", "menu"},
		sm.StateNoAppointments:          {"no_appt_menu", "no_appt_end"},
		sm.StateAppointmentAction:       {"appt_confirm", "appt_cancel", "appt_reschedule", "appt_preparation", "appt_back", "appt_menu"},
		sm.StateConfirmAppointment:      {"confirm_yes", "confirm_no"},
		sm.StateCancelAppointment:       {"cancel_yes", "cancel_no"},
		sm.StateConfirmRescheduleNotif:  {"reschedule_yes", "reschedule_no"},
		sm.StateConfirmCancelNotif:      {"cancel_yes", "cancel_no"},
		sm.StateNotifRescheduleFallback: {"confirm", "cancel"},
		sm.StatePostActionMenu:          {"ver_citas", "menu_principal", "terminar_chat"},
		sm.StateFallbackMenu:            {"action:restart", "action:end"},
		sm.StateOutOfHoursMenu:          {"ooh_resultados", "ooh_ubicacion", "ooh_ayuda"},
		sm.StateMedicationAskSymptoms:   {"sin_sintomas", "con_sintomas"},
		sm.StateMedicationConfirmOrden:  {"med_confirm_ok", "med_confirm_retry"},
		sm.StateConfirmConsolidate:      {"consolidate_yes", "consolidate_no"},
		sm.StateAskEmgOrder:             {"emg_order_yes", "emg_order_no"},
	}
}

// Cada paso de elección ofrece al agente exactamente los payloads que su handler acepta del
// paciente (llegan como postback y texto, Tarea 5).
func TestReturnMenus_ButtonStepsOfferTheHandlerPayloads(t *testing.T) {
	m := returnMenuMachine()
	for step, want := range handlerButtonPayloads() {
		if !m.IsRegistered(step) {
			t.Errorf("%s: el paso de la tabla no está registrado", step)
			continue
		}
		if got := ownData(step); !slices.Equal(got, sortedCopy(want)) {
			t.Errorf("%s: Data de las opciones = %v, el handler acepta %v", step, got, sortedCopy(want))
		}
	}
	// Los pasos con config de botones: sus opciones propias con Data son cfg.Options, en dos sentidos.
	for _, step := range sm.RegisteredReturnSteps() {
		cfg, ok := m.Config(step)
		if !ok || cfg.InputType != sm.InputButton {
			continue
		}
		if got := ownData(step); !slices.Equal(got, sortedCopy(cfg.Options)) {
			t.Errorf("%s: Data de las opciones = %v, cfg.Options = %v", step, got, sortedCopy(cfg.Options))
		}
	}
}

// CANCEL_REASON lo nombra buildAgentCommands pero no tiene handler: no se escala desde ahí.
func TestReturnMenus_CancelReasonIsUnreachable(t *testing.T) {
	m := returnMenuMachine()
	if m.IsRegistered(sm.StateCancelReason) {
		t.Fatal("CANCEL_REASON tiene handler: ya es alcanzable, debe declarar un menú")
	}
	if !slices.Contains(unreachableAgentCommandSteps, sm.StateCancelReason) {
		t.Error("CANCEL_REASON debe figurar en unreachableAgentCommandSteps")
	}
	if slices.Contains(sm.RegisteredReturnSteps(), sm.StateCancelReason) {
		t.Error("un paso inalcanzable no debe registrar menú")
	}
}

func TestReturnMenus_MainMenuOffersEveryMenuOption(t *testing.T) {
	returnMenuMachine()
	want := []string{"medicamentos", "agendar", "consultar", "resultados", "ubicacion", "ayuda"}
	if got := ownData(sm.StateMainMenu); !slices.Equal(got, sortedCopy(want)) {
		t.Errorf("MAIN_MENU ofrece %v, quiero %v", got, sortedCopy(want))
	}
}

// Los pasos con lista derivan sus opciones de las filas del paciente, con rótulo y payload iguales.
func TestOptionsFromRows_UsesTitleAndIDAsLabelAndPayload(t *testing.T) {
	got := optionsFromRows([]sm.ListRow{{ID: "a", Title: "Uno"}, {ID: "b", Title: "Dos", Description: "x"}})
	want := []sm.ReturnOption{{Label: "Uno", Value: "a", Data: "a"}, {Label: "Dos", Value: "b", Data: "b"}}
	if !slices.EqualFunc(got, want, func(x, y sm.ReturnOption) bool {
		return x.Label == y.Label && x.Value == y.Value && x.Data == y.Data && x.Target == y.Target
	}) {
		t.Fatalf("optionsFromRows = %+v, quiero %+v", got, want)
	}
}

// Las opciones solo-Target (buscar horarios) retoman en SEARCH_SLOTS sin inyectar ningún payload.
func TestReturnMenus_SearchSlotsOptionsRetakeWithoutData(t *testing.T) {
	returnMenuMachine()
	for _, step := range []string{sm.StateOfferWaitingList, sm.StateConfirmBooking} {
		var found bool
		for _, o := range sm.ReturnOptionsFor(nil, step) {
			if o.Value == "buscar_horarios" && o.Target == sm.StateSearchSlots && o.Data == "" && !o.Close {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: falta «buscar_horarios» hacia SEARCH_SLOTS sin dato", step)
		}
	}
}

// virtualAgentMessage es el mensaje virtual con el que el worker reanuda una opción con Data:
// postback y texto a la vez (igual que el webhook de Bird).
func virtualAgentMessage(data string) bird.InboundMessage {
	return bird.InboundMessage{
		ID: "msg-virtual", Phone: "+573001234567", MessageType: "postback",
		Text: data, IsPostback: true, PostbackPayload: data, ReceivedAt: time.Now(),
	}
}

// Pasa cada opción propia con Data por el handler REAL del paso, con el mensaje virtual del
// worker. La opción debe ser aceptada: sin reintento contado y sin el aviso de entrada inválida.
// Con dependencias nil, un handler que llega a su lógica de negocio puede paniquear o devolver
// error DESPUÉS de validar: eso cuenta como aceptada (un re-prompt nunca llega ahí) y se cuenta
// aparte en el log. Ningún paso queda excluido: todos se pueden manejar con nil.
func TestReturnMenus_EveryOptionIsAcceptedByTheRealHandler(t *testing.T) {
	m := returnMenuMachine()
	var driven, reachedBusinessLogic int
	for _, step := range sm.RegisteredReturnSteps() {
		if !m.IsRegistered(step) {
			continue
		}
		for _, o := range sm.ReturnOptionsFor(nil, step) {
			if sm.IsUniversalReturnValue(o.Value) || o.Data == "" {
				continue
			}
			sess := testSess(step)
			func() {
				defer func() {
					if r := recover(); r != nil {
						reachedBusinessLogic++
						t.Logf("negocio: %s/%s", step, o.Value)
						driven++
						if sess.RetryCount != 0 {
							t.Errorf("%s/%s: contó un reintento antes de entrar en la lógica de negocio", step, o.Value)
						}
					}
				}()
				result, err := m.Process(context.Background(), sess, virtualAgentMessage(o.Data))
				driven++
				if err != nil {
					reachedBusinessLogic++
					t.Logf("negocio(error): %s/%s", step, o.Value)
					return
				}
				if sess.RetryCount != 0 {
					t.Errorf("%s/%s: RetryCount = %d, la opción no se aceptó", step, o.Value, sess.RetryCount)
				}
				for _, e := range result.Events {
					if e.Type == "invalid_input" {
						t.Errorf("%s/%s: el handler la rechazó como entrada inválida", step, o.Value)
					}
				}
			}()
		}
	}
	t.Logf("opciones pasadas por el handler real: %d (llegaron a la lógica de negocio con dependencias nil: %d)",
		driven, reachedBusinessLogic)
	if driven < 100 {
		t.Errorf("solo se pasaron %d opciones; la prueba dejó de recorrer los pasos", driven)
	}
}

// stubMunicipalityRepo cuenta las búsquedas por nombre y no devuelve ninguna.
type stubMunicipalityRepo struct{ searched []string }

func (s *stubMunicipalityRepo) Search(_ context.Context, name string) ([]domain.Municipality, error) {
	s.searched = append(s.searched, name)
	return nil, nil
}

func (s *stubMunicipalityRepo) SearchBarrios(context.Context, string, string, string) ([]domain.Barrio, error) {
	return nil, nil
}

func (s *stubMunicipalityRepo) CreateNeighborhood(context.Context, string, string, string, string) (string, error) {
	return "", nil
}

// C2: CONFIRM_MUNICIPALITY solo acepta POSTBACK. El Data de la opción llega como postback y por
// eso funciona; el mismo valor como texto solo se toma por un nombre de municipio a buscar.
func TestReturnMenus_ConfirmMunicipalityOnlyAcceptsThePostback(t *testing.T) {
	repo := &stubMunicipalityRepo{}
	m := sm.NewMachine()
	RegisterRegistrationHandlers(m, nil, repo)

	sess := testSess(sm.StateConfirmMunicipality)
	res, err := m.Process(context.Background(), sess, virtualAgentMessage("muni_change"))
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.searched) != 0 {
		t.Fatalf("como postback no se busca ningún municipio, se buscó %v", repo.searched)
	}
	if res.NextState != sm.StateConfirmMunicipality || len(res.Messages) == 0 {
		t.Fatalf("muni_change como postback debe pedir el municipio de nuevo, fue %+v", res)
	}

	textOnly := bird.InboundMessage{ID: "m", Phone: "+573001234567", MessageType: "text", Text: "muni_change", ReceivedAt: time.Now()}
	if _, err := m.Process(context.Background(), testSess(sm.StateConfirmMunicipality), textOnly); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repo.searched, []string{"muni_change"}) {
		t.Fatalf("como texto solo se debe tomar por un nombre a buscar, búsquedas = %v", repo.searched)
	}
}
