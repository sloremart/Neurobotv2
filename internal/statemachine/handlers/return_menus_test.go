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
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Los menús y entradas de retorno de los pasos escalables de Neuro (Task 9c). Reflejan lo que
// buildAgentCommands le ofrece hoy a un agente de Bird como `/bot resume ESTADO dato`.

// returnMenuMachine registra los handlers que declaran menús de retorno, con dependencias nil:
// el registro no las toca. Los registros de menú son globales, así que basta con registrarlos.
func returnMenuMachine() *sm.Machine {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)
	RegisterIdentificationHandlers(m, nil)
	RegisterMedicalOrderHandlers(m, nil, nil, nil, nil, nil, nil)
	RegisterAppointmentHandlers(m, nil, nil, nil, nil, nil)
	RegisterSlotHandlers(m, nil, nil, nil, nil, nil, nil, nil, nil)
	// Los registros de estos handlers no tocan sus dependencias: se pasan nil.
	RegisterRegistrationHandlers(m, nil, nil)
	RegisterEntityManagementHandlers(m, nil, nil)
	RegisterMedicalValidationHandlers(m, nil, nil, nil, nil)
	RegisterPostActionHandlers(m, nil)
	RegisterMedicationHandlers(m, nil, nil)
	RegisterResultsAndLocationHandlers(m, sampleConfig(), nil)
	RegisterResultsFlowHandlers(m, sampleConfig(), nil, nil, nil)
	// BOT_DISABLED registra su menú aquí: sin él, que figurara o no en RegisteredReturnSteps
	// dependería del orden de las pruebas.
	RegisterEscalationHandlers(m, nil, nil, nil)
	return m
}

// cupsSteps entregan lo escrito al comando de CUPS del asesor.
var cupsSteps = []string{
	sm.StateAskMedicalOrder, sm.StateUploadMedicalOrder, sm.StateValidateOCR,
	sm.StateOCRFailed, sm.StateConfirmOCRResult, sm.StateAskManualCups,
	sm.StateSearchSlots, sm.StateCheckSpecialCups,
}

// hasReturnEntry dice si un paso ofrece al agente algún botón propio (no universal) o un campo,
// con la sesión con la que se escala desde él (las opciones de algunos pasos salen de ella).
func hasReturnEntry(m *sm.Machine, step string) bool {
	d := m.InputDescriptor(representativeSess(step), step)
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
		for _, o := range sm.ReturnOptionsFor(representativeSess(step), step) {
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
		// Todo estado que buildAgentCommands nombra tiene que darle al agente de Néctar algo más
		// que las salidas universales: no hay pasos aplazados.
		if !hasReturnEntry(m, step) {
			t.Errorf("%s: buildAgentCommands lo nombra pero no tiene menú ni entrada", step)
		}
	}
	// Los inalcanzables deben seguir siendo estados que buildAgentCommands nombra.
	named := agentCommandSteps(t)
	for _, step := range unreachableAgentCommandSteps {
		if !slices.Contains(named, step) {
			t.Errorf("%s: figura como inalcanzable pero buildAgentCommands ya no lo nombra", step)
		}
	}
}

// deterministicAutomaticSteps son pasos AUTOMÁTICOS que escalan de forma determinista (o el paso
// interactivo que los precede): «devolver en el mismo paso» sin dato los re-ejecuta con el mismo
// contexto, vuelve a escalar con `__resume__` y el corte anti-bucle manda al paciente al menú.
var deterministicAutomaticSteps = []string{
	sm.StateSearchSlots, sm.StateCreateAppointment, sm.StateCheckSpecialCups, sm.StateValidateOCR,
	sm.StateOCRFailed, sm.StateAskMedicalOrder, sm.StateShowEntityList, sm.StateMedicationPrepareSchedule,
}

// P1-5: en esos pasos, o no se ofrece bot_reanudar, o es la salida que lleva un dato requerido
// (C6: en los pasos con campo se re-rotula, no se oculta). Y ocultarlo en un paso con campo
// dejaría a Néctar sin opción que lleve lo escrito.
func TestReturnMenus_NoPlainResumeIntoADeterministicAutomaticStep(t *testing.T) {
	m := returnMenuMachine()
	offersResume := func(step string) bool {
		for _, o := range sm.ReturnOptionsFor(representativeSess(step), step) {
			if o.Value == sm.ReturnResume {
				return true
			}
		}
		return false
	}
	for _, step := range deterministicAutomaticSteps {
		if !m.IsRegistered(step) {
			t.Errorf("%s: no es un estado registrado", step)
		}
		if offersResume(step) && !sm.ReturnInputRequired(step) {
			t.Errorf("%s: ofrece «devolver en el mismo paso» sin un dato requerido: re-escala en bucle", step)
		}
	}
	for _, step := range sm.RegisteredReturnSteps() {
		if !offersResume(step) && len(m.InputDescriptor(nil, step).Fields) > 0 {
			t.Errorf("%s: oculta bot_reanudar pero tiene campo: lo escrito no tendría cómo volver", step)
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

	published := nectar.Capabilities(config.ProtocolEscaladoV1, true).Capabilities.Input

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
		{"SIESA pendiente: confirmación", view(sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifSiesaPending, sm.CtxNotifSiesaCase, sm.NotifSiesaCaseConfirm, "notif_type", "confirmation"), []want{
			{"Reintentar la confirmación en SIESA", "confirm", np, "confirm", false},
			{"Ya la confirmé a mano en SIESA", "confirmada_manual", "", "", true},
		}},
		{"SIESA pendiente: cancelación", view(sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifSiesaPending, sm.CtxNotifSiesaCase, sm.NotifSiesaCaseCancel, "notif_type", "confirmation"), []want{
			{"Pedirle confirmar la cancelación", "cancel", np, "cancel", false},
			{"Ya la cancelé a mano en SIESA", "cancelada_manual", "", "", true},
		}},
		{"SIESA pendiente: cita cambiada", view(sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifSiesaPending, sm.CtxNotifSiesaCase, sm.NotifSiesaCaseChanged, "notif_type", "confirmation"), []want{
			{"Ya la revisé con el paciente", "revisada_manual", "", "", true},
			{"Confirmar la cita", "confirm", np, "confirm", false},
			{"Reprogramar la cita", "reschedule", np, "reschedule", false},
			{"Cancelar la cita", "cancel", np, "cancel", false},
		}},
		{"lista de espera", view("notif_type", "waiting_list"), []want{
			{"Agendar ahora", "wl_schedule", np, "wl_schedule", false},
			{"No le interesa", "wl_decline", np, "wl_decline", false},
		}},
		{"cancelación", view("notif_type", "cancellation"), []want{
			{"Entendido, sin reprogramar", "understood", np, "understood", false},
			{"Reprogramar", "reschedule", np, "reschedule", false},
		}},
		{"reprogramar sin procedimiento", view(sm.CtxNotifEscalationDetail, sm.EscalationReasonRescheduleNoProcedure, "notif_type", "cancellation"), []want{
			{"Ya la reprogramé a mano en SIESA", "reprogramada_manual", "", "", true},
			{"Entendido, sin reprogramar", "understood", np, "understood", false},
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
		for _, o := range sm.ReturnOptionsFor(representativeSess(step), step) {
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
		sm.StateConfirmOCRResult:        {"ocr_correct", "ocr_incorrect"},
		// Solo del agente: el paciente escribe el número; weeks_yes/weeks_no son los comandos que el
		// handler reconoce en el mensaje virtual del agente (ID «agent-…»).
		sm.StateAskGestationalWeeks: {"weeks_yes", "weeks_no"},
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
	// Los pasos con config de botones: sus opciones propias con Data son cfg.Options, en dos sentidos,
	// salvo los botones que solo son del paciente (patientOnlyPayloads).
	for _, step := range sm.RegisteredReturnSteps() {
		cfg, ok := m.Config(step)
		if !ok || cfg.InputType != sm.InputButton {
			continue
		}
		options := slices.DeleteFunc(slices.Clone(cfg.Options), func(o string) bool {
			return slices.Contains(patientOnlyPayloads[step], o)
		})
		if got := ownData(step); !slices.Equal(got, sortedCopy(options)) {
			t.Errorf("%s: Data de las opciones = %v, cfg.Options = %v", step, got, sortedCopy(options))
		}
	}
}

// patientOnlyPayloads son botones que el paso acepta del paciente y NO se le ofrecen al agente:
// «Reprogramar» de NOTIF_RESCHEDULE_FALLBACK escala a un agente, y ofrecérselo al propio agente lo
// devolvería a la cola en vez de resolver.
var patientOnlyPayloads = map[string][]string{
	sm.StateNotifRescheduleFallback: {"reschedule"},
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
	want := []string{"medicamentos", "agendar", "consultar", "resultados", "siau", "ubicacion", "ayuda"}
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
	for _, step := range []string{sm.StateOfferWaitingList, sm.StateConfirmBooking, sm.StateShowSlots} {
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
		for _, o := range sm.ReturnOptionsFor(representativeSess(step), step) {
			if sm.IsUniversalReturnValue(o.Value) || o.Data == "" {
				continue
			}
			sess := representativeSess(step)
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

// sampleAppointments son dos citas con el formato de appointments_json.
func sampleAppointments() []domain.Appointment {
	return []domain.Appointment{
		{
			ID: "90011", Date: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), TimeSlot: "202611030930",
			DoctorName: "Ortega", Procedures: []domain.AppointmentProcedure{{CupCode: "890274", CupName: "CONSULTA NEUROLOGIA"}},
		},
		{
			ID: "90012", Date: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), TimeSlot: "202611041400",
			DoctorName: "Plazas", Procedures: []domain.AppointmentProcedure{{CupCode: "883141", CupName: "RM CEREBRO"}},
		},
	}
}

// sampleSearchedProcedures son dos resultados de la búsqueda manual (search_procedures_json).
const sampleSearchedProcedures = `[{"ID":7,"Code":"883141","Name":"RM CEREBRO","ServiceName":"RESONANCIA","RequiredSpaces":1},` +
	`{"ID":9,"Code":"883101","Name":"RM COLUMNA","ServiceName":"RESONANCIA","RequiredSpaces":2}]`

// representativeSess es la sesión con la que se escala desde cada paso: los pasos cuyas opciones
// salen de la sesión llevan los datos que el paciente vio.
func representativeSess(step string) *session.Session {
	sess := testSess(step)
	switch step {
	case sm.StateShowSlots:
		sess.Context["available_slots_json"] = slotsJSON(nSlots(5))
	case sm.StateListAppointments:
		b, _ := json.Marshal(sampleAppointments())
		sess.Context["appointments_json"] = string(b)
	case sm.StateSelectProcedure:
		sess.Context["search_procedures_json"] = sampleSearchedProcedures
	case sm.StateAskEntityNumber:
		sess.Context["entity_list_names"] = "SANITAS|NUEVA EPS|FAMISANAR"
		sess.Context["entity_list_codes"] = "EPS005,EPS037,EPS017"
		sess.Context["entity_list_count"] = "3"
	case sm.StateCreateAppointment:
		// Su única salida propia es seguir con el siguiente grupo: solo existe si queda uno.
		sess = twoGroupsSess(step, 0)
	}
	return sess
}

func TestAppointmentReturnOptions_DataIsTheAppointmentID(t *testing.T) {
	appts := sampleAppointments()
	// La 2ª cita solo trae el código y no trae médico: el menú del agente se arma en el turno de
	// escalación sin consultar el catálogo (rotula con el código) y sin «Dr.» vacío.
	appts[1].Procedures[0].CupName = ""
	appts[1].DoctorName = ""
	b, _ := json.Marshal(appts)
	sess := testSess(sm.StateListAppointments)
	sess.Context["appointments_json"] = string(b)
	got := appointmentReturnOptions(sess)
	if len(got) != 3 {
		t.Fatalf("opciones = %+v, quiero una por cita y recargar", got)
	}
	wantRest := []string{" — Dr. Ortega — CONSULTA NEUROLOGIA", " — 883141"}
	for i, a := range appts {
		wantLabel := utils.FormatFriendlyDateShort(a.Date) + " " + services.FormatTimeSlot(a.TimeSlot) + wantRest[i]
		o := got[i]
		if o.Label != wantLabel || o.Value != "cita_"+a.ID || o.Data != a.ID || o.Target != "" {
			t.Errorf("opción %d = %+v, quiero rótulo %q, token cita_%s y Data %q", i, o, wantLabel, a.ID, a.ID)
		}
	}
	// Una cita que ya no está en la sesión deja su token sin resolver.
	returnMenuMachine()
	sess.Context["appointments_json"] = "[]"
	if _, ok := sm.ResolveReturnValue(sess, sm.StateListAppointments, got[0].Value); ok {
		t.Error("el token de una cita que ya no está no debe resolver")
	}
	sess.Context["appointments_json"] = string(b)
	if r := got[2]; r.Value != "recargar_citas" || r.Target != sm.StateFetchAppointments || r.Data != "" ||
		r.Label != "Recargar citas desde el sistema" {
		t.Errorf("recargar = %+v", r)
	}

	m := sm.NewMachine()
	m.Register(sm.StateListAppointments, listAppointmentsHandler(nil, nil))
	res, err := m.Process(context.Background(), sess, virtualAgentMessage(got[1].Data))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateAppointmentAction || res.UpdateCtx["selected_appointment_id"] != "90012" {
		t.Fatalf("el postback del ID elige esa cita, fue %s %+v", res.NextState, res.UpdateCtx)
	}
}

func TestEntityNumberInput_ListFromTheShownNames(t *testing.T) {
	m := returnMenuMachine()
	sess := representativeSess(sm.StateAskEntityNumber)
	d := m.InputDescriptor(sess, sm.StateAskEntityNumber)
	if len(d.Fields) != 1 {
		t.Fatalf("campos = %+v", d.Fields)
	}
	f := d.Fields[0]
	want := []sm.InputChoice{{Label: "SANITAS", Value: "1"}, {Label: "NUEVA EPS", Value: "2"}, {Label: "FAMISANAR", Value: "3"}}
	if f.Kind != sm.InputKindList || !f.Required || f.Label != "Entidad del paciente" || !slices.Equal(f.Options, want) {
		t.Fatalf("campo = %+v, quiero lista requerida con %+v", f, want)
	}
	if r := m.ResolveReturnV1(sess, sm.StateAskEntityNumber, json.RawMessage(`{"dato":"2"}`)); !r.OK() || r.Value != "2" {
		t.Errorf("«2» está en la lista: %+v", r)
	}
	if r := m.ResolveReturnV1(sess, sm.StateAskEntityNumber, json.RawMessage(`{"dato":"9"}`)); !r.Rejected() {
		t.Errorf("«9» no está en la lista: %+v", r)
	}
	// Rótulo de la salida que lleva el dato y la salida sin dato a otro tipo de entidad.
	var resume, change bool
	for _, o := range sm.ReturnOptionsFor(sess, sm.StateAskEntityNumber) {
		if o.Value == sm.ReturnResume {
			resume = o.Label == "Usar la entidad elegida"
		}
		if o.Value == "cambiar_tipo_entidad" {
			change = o.Target == sm.StateAskClientType && o.NoInput && o.Data == "" &&
				o.Label == "Elegir otro tipo de entidad"
		}
	}
	if !resume || !change {
		t.Errorf("menú de ASK_ENTITY_NUMBER: reanudar=%v cambiar=%v", resume, change)
	}

	// El handler real acepta el número elegido como lo escribiría el paciente.
	hm := sm.NewMachine()
	hm.Register(sm.StateAskEntityNumber, askEntityNumberHandler(nil))
	res, err := hm.Process(context.Background(), sess, textM("2"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateAskDocumentType || res.UpdateCtx["selected_entity_code"] != "EPS037" {
		t.Fatalf("«2» elige NUEVA EPS, fue %s %+v", res.NextState, res.UpdateCtx)
	}
}

func TestEntityNumberInput_EmptyListDegradesAndValidates(t *testing.T) {
	m := returnMenuMachine()
	sess := testSess(sm.StateAskEntityNumber)
	d := m.InputDescriptor(sess, sm.StateAskEntityNumber)
	if len(d.Fields) != 1 || len(d.Fields[0].Options) != 0 {
		t.Fatalf("sin nombres en la sesión la lista sale vacía: %+v", d.Fields)
	}
	for _, v := range []string{"abc", "1"} {
		if r := m.ResolveReturnV1(sess, sm.StateAskEntityNumber, json.RawMessage(`{"dato":"`+v+`"}`)); !r.Rejected() {
			t.Errorf("%q sin lista debe rechazarse: %+v", v, r)
		}
	}
	// El Validate propio también exige el rango aunque el valor estuviera en la lista.
	sess.Context["entity_list_names"] = "SANITAS|NUEVA EPS"
	if r := m.ResolveReturnV1(sess, sm.StateAskEntityNumber, json.RawMessage(`{"dato":"2"}`)); !r.Rejected() {
		t.Errorf("sin entity_list_count no hay rango válido: %+v", r)
	}
}

func TestShowEntityListReturn_HidesResumeAndOffersOtherType(t *testing.T) {
	returnMenuMachine()
	var change bool
	for _, o := range sm.ReturnOptionsFor(nil, sm.StateShowEntityList) {
		if o.Value == sm.ReturnResume {
			t.Error("SHOW_ENTITY_LIST no ofrece reanudar en el mismo paso: volvería a escalar")
		}
		if o.Value == "cambiar_tipo_entidad" {
			change = o.Target == sm.StateAskClientType && o.Data == ""
		}
	}
	if !change {
		t.Error("falta cambiar_tipo_entidad hacia ASK_CLIENT_TYPE")
	}
}

func TestSelectProcedureReturnOptions_PostbackIDs(t *testing.T) {
	sess := representativeSess(sm.StateSelectProcedure)
	got := procedureReturnOptions(sess)
	want := []sm.ReturnOption{
		{Label: "883141 — RM CEREBRO", Value: "proc_7", Data: "7"},
		{Label: "883101 — RM COLUMNA", Value: "proc_9", Data: "9"},
		{Label: "Buscar de nuevo", Value: "buscar_procedimiento", Target: sm.StateAskManualCups, NoInput: true},
	}
	if len(got) != len(want) {
		t.Fatalf("opciones = %+v", got)
	}
	for i, w := range want {
		if o := got[i]; o.Label != w.Label || o.Value != w.Value || o.Data != w.Data || o.Target != w.Target || o.NoInput != w.NoInput {
			t.Errorf("opción %d = %+v, quiero %+v", i, o, w)
		}
	}
	// Sin la lista queda la salida de buscar de nuevo.
	if got := procedureReturnOptions(nil); len(got) != 1 || got[0].Value != "buscar_procedimiento" {
		t.Errorf("sin sesión solo queda buscar de nuevo: %+v", got)
	}

	m := sm.NewMachine()
	m.Register(sm.StateSelectProcedure, selectProcedureHandler())
	res, err := m.Process(context.Background(), sess, virtualAgentMessage("9"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateCheckSpecialCups || res.UpdateCtx["cups_code"] != "883101" {
		t.Fatalf("el postback del ID elige ese procedimiento, fue %s %+v", res.NextState, res.UpdateCtx)
	}
}

// S14/C11: corregir la entidad es el flujo legado CHECK_ENTITY, olvidando el contrato elegido.
func TestCoverageReturn_CorrectEntityClearsContract(t *testing.T) {
	returnMenuMachine()
	o, ok := sm.ResolveReturnValue(nil, sm.StateCoverageNoConvenio, "cov_entidad")
	if !ok {
		t.Fatal("cov_entidad no está en el menú de COVERAGE_NO_CONVENIO")
	}
	if o.Target != sm.StateCheckEntity || o.Data != "" || o.Label != "Corregir la entidad con el paciente" ||
		!slices.Equal(o.Clear, []string{"patient_contract", "coverage_from_booking"}) {
		t.Fatalf("cov_entidad = %+v", o)
	}
}

// UPLOAD_EMG_ORDER: el agente escribe los CUPS de la orden de EMG y puede volver a pedir la foto.
func TestUploadEmgOrderReturn_CupsFieldAndAskAgain(t *testing.T) {
	m := returnMenuMachine()
	d := m.InputDescriptor(nil, sm.StateUploadEmgOrder)
	if len(d.Fields) != 1 || d.Fields[0].Label != "CUPS de la orden de electromiografía (si ya se leyeron dependientes de la 1ª orden, se suman)" {
		t.Fatalf("campo = %+v", d.Fields)
	}
	r := m.ResolveReturnInput(testSess(sm.StateUploadEmgOrder), sm.StateUploadEmgOrder, sm.ReturnInputPrefix+"930860:2")
	if !r.OK() || r.Command != sm.ReturnCommandCups {
		t.Fatalf("los CUPS van al comando cups: %+v", r)
	}
	var resume, again bool
	for _, o := range sm.ReturnOptionsFor(nil, sm.StateUploadEmgOrder) {
		if o.Value == sm.ReturnResume {
			resume = o.Label == "Enviar los CUPS escritos al bot"
		}
		if o.Value == "pedir_orden_emg" {
			again = o.Target == sm.StateUploadEmgOrder && o.NoInput && o.Data == ""
		}
	}
	if !resume || !again {
		t.Errorf("menú de UPLOAD_EMG_ORDER: reanudar=%v pedir=%v", resume, again)
	}
}

// En SEARCH_SLOTS y CHECK_SPECIAL_CUPS lo escrito REEMPLAZA la orden ya agrupada: se borran las
// mismas claves por grupo que borra seguir con el siguiente procedimiento (entre ellas la entrada
// de lista de espera, que la cita nueva marcaría como agendada) y el rótulo lo dice. En los pasos
// de la orden, donde aún no hay grupos, no se borra nada.
func TestReturnMenus_CupsFieldAfterGroupingResetsTheGroupContext(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range []string{sm.StateSearchSlots, sm.StateCheckSpecialCups} {
		r := m.ResolveReturnInput(testSess(step), step, sm.ReturnInputPrefix+"883141")
		if !r.OK() || !slices.Equal(r.Clear, procedureGroupContextKeys) {
			t.Errorf("%s: Clear = %v, quiero las claves por grupo", step, r.Clear)
		}
		if !slices.Contains(r.Clear, "waiting_list_entry_id") {
			t.Errorf("%s: falta waiting_list_entry_id en %v", step, r.Clear)
		}
		in := m.InputDescriptor(nil, step)
		if len(in.Fields) != 1 || !strings.Contains(in.Fields[0].Label+" "+in.Fields[0].Help, "reemplaza la orden completa") {
			t.Errorf("%s: el campo debe decir que reemplaza la orden completa: %+v", step, in.Fields)
		}
	}
	for _, step := range []string{sm.StateAskMedicalOrder, sm.StateValidateOCR} {
		if r := m.ResolveReturnInput(testSess(step), step, sm.ReturnInputPrefix+"883141"); len(r.Clear) != 0 {
			t.Errorf("%s: no hay grupos que borrar, Clear = %v", step, r.Clear)
		}
	}
}

// El candado de «Resolver» se declara exactamente en los pasos donde el bot deja trabajo
// pendiente; cualquier paso nuevo con candado exige tocar esta tabla a propósito.
func TestResolveGuards_DeclaredWherePendingWork(t *testing.T) {
	returnMenuMachine()
	want := map[string]string{}
	for _, s := range []string{
		sm.StateAskDocument, sm.StateAskMedicalOrder, sm.StateUploadMedicalOrder, sm.StateValidateOCR,
		sm.StateOCRFailed, sm.StateConfirmOCRResult, sm.StateAskManualCups, sm.StateSelectProcedure,
		sm.StateUploadEmgOrder,
	} {
		want[s] = "retorno_pendiente"
	}
	for _, s := range []string{
		sm.StateNotifPending, sm.StateSearchSlots, sm.StateCreateAppointment, sm.StateCheckSpecialCups,
	} {
		want[s] = "cita_pendiente"
	}
	got := sm.DeclaredResolveGuards()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: candado %q, quiero %q", k, got[k], v)
		}
	}
	for k, v := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s declara el candado %q y no está en la tabla", k, v)
		}
	}
	// Es una copia: tocarla no cambia lo declarado.
	got[sm.StateAskDocument] = "otro"
	if sm.DeclaredResolveGuards()[sm.StateAskDocument] != "retorno_pendiente" {
		t.Error("DeclaredResolveGuards debe devolver una copia")
	}
}
