package handlers

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// agentFieldCase son valores que el handler del paso acepta y que rechaza; el campo del agente
// tiene que darles el MISMO veredicto.
type agentFieldCase struct {
	step     string
	kind     string
	accepted []string
	rejected []string
	// prepare deja la sesión como la deja el paso anterior (lo que el handler lee).
	prepare func(*session.Session)
}

func agentFieldCases() []agentFieldCase {
	weeksPrompted := func(s *session.Session) { s.SetContext("_prompted_weeks", "1") }
	return []agentFieldCase{
		{step: sm.StateAskUpdatePhone, kind: sm.InputKindNumber, accepted: []string{"3001234567"}, rejected: []string{"12345", "__resume__"}},
		{step: sm.StateAskUpdateEmail, kind: sm.InputKindText, accepted: []string{"NA", "ana@correo.com", "No tengo"}, rejected: []string{"ana@", "__resume__"}},
		{step: sm.StateChangeEntity, kind: sm.InputKindText, accepted: []string{"Sanitas"}, rejected: []string{""}},
		{step: sm.StateRegFirstSurname, kind: sm.InputKindText, accepted: []string{"Pérez"}, rejected: []string{"P3rez", "__resume__"}},
		{step: sm.StateRegFirstName, kind: sm.InputKindText, accepted: []string{"Ana"}, rejected: []string{"Ana2", "__resume__"}},
		{step: sm.StateRegSecondSurname, kind: sm.InputKindText, accepted: []string{"NA", "Gómez"}, rejected: []string{"G0mez", "__resume__"}},
		{step: sm.StateRegSecondName, kind: sm.InputKindText, accepted: []string{"na", "María"}, rejected: []string{"M@ria", "__resume__"}},
		{step: sm.StateRegBirthDate, kind: sm.InputKindDate, accepted: []string{"1992-04-17", "17/04/1992"}, rejected: []string{"2999-01-01", "1850-01-01", "ayer", "__resume__"}},
		{step: sm.StateRegPhone, kind: sm.InputKindNumber, accepted: []string{"3001234567", "+57 300 123 4567"}, rejected: []string{"12345", "__resume__"}},
		{step: sm.StateRegPhone2, kind: sm.InputKindText, accepted: []string{"NA", "3001234567"}},
		{step: sm.StateRegEmail, kind: sm.InputKindText, accepted: []string{"NA", "ana@correo.com"}, rejected: []string{"ana@", "__resume__"}},
		{step: sm.StateRegAddress, kind: sm.InputKindText, accepted: []string{"Calle 1 # 2-3"}, rejected: []string{"__resume__"}},
		{step: sm.StateRegOccupation, kind: sm.InputKindText, accepted: []string{"Docente", "NA"}, rejected: []string{"__resume__"}},
		{step: sm.StateRegBarrio, kind: sm.InputKindText, accepted: []string{"La Esperanza", "NA"}, rejected: []string{"__resume__"}},
		{step: sm.StateRegMunicipality, kind: sm.InputKindText, accepted: []string{"Villavicencio - Meta"}, rejected: []string{""}},
		{step: sm.StateGfrCreatinine, kind: sm.InputKindNumber, accepted: []string{"0,96", "0.96", "30"}, rejected: []string{"31", "0", "abc", "__resume__"}},
		{step: sm.StateGfrExamDate, kind: sm.InputKindDate, accepted: []string{"2026-07-05", "05/07/2026"}, rejected: []string{"2999-01-01", "ayer", "__resume__"}},
		{step: sm.StateGfrHeight, kind: sm.InputKindNumber, accepted: []string{"170", "30", "250"}, rejected: []string{"29", "251", "__resume__"}},
		{step: sm.StateGfrWeight, kind: sm.InputKindNumber, accepted: []string{"70", "70,5"}, rejected: []string{"5", "__resume__"}},
		{step: sm.StateAskGestationalWeeks, kind: sm.InputKindNumber, prepare: weeksPrompted, accepted: []string{"12", "12,5"}, rejected: []string{"0", "doce", "__resume__"}},
		{step: sm.StateMedicationAskLastDate, kind: sm.InputKindText, accepted: []string{"hace 6 meses"}, rejected: []string{"__resume__"}},
		{step: sm.StateMedicationAskDose, kind: sm.InputKindText, accepted: []string{"1 jeringa de 40mg"}, rejected: []string{"__resume__"}},
		{step: sm.StateMedicationAskFrequency, kind: sm.InputKindText, accepted: []string{"cada mes"}, rejected: []string{"__resume__"}},
	}
}

// handlerAccepts pasa el valor por el handler REAL del paso: lo acepta si sale del paso. Con
// dependencias nil, uno que llega a su lógica de negocio puede paniquear o devolver error
// DESPUÉS de validar: eso cuenta como aceptado (un re-pregunta nunca llega ahí).
func handlerAccepts(t *testing.T, m *sm.Machine, c agentFieldCase, value string) (accepted bool) {
	t.Helper()
	sess := testSess(c.step)
	if c.prepare != nil {
		c.prepare(sess)
	}
	defer func() {
		if r := recover(); r != nil {
			accepted = true
		}
	}()
	res, err := m.Process(context.Background(), sess, textM(value))
	if err != nil {
		return true
	}
	return res.NextState != c.step
}

func TestAgentTextInputs_AcceptWhatTheHandlerAccepts(t *testing.T) {
	m := returnMenuMachine()
	for _, c := range agentFieldCases() {
		d := m.InputDescriptor(nil, c.step)
		if len(d.Fields) != 1 || d.Fields[0].Kind != c.kind || !d.Fields[0].Required {
			t.Errorf("%s: campo = %+v, quiero uno requerido de tipo %q", c.step, d.Fields, c.kind)
			continue
		}
		for _, v := range c.accepted {
			sess := testSess(c.step)
			if r := m.ResolveReturnInput(sess, c.step, sm.ReturnInputPrefix+v); !r.OK() {
				t.Errorf("%s: el campo rechaza %q, fue %+v", c.step, v, r)
			}
			if !handlerAccepts(t, m, c, v) {
				t.Errorf("%s: la tabla dice que el handler acepta %q y no lo acepta", c.step, v)
			}
		}
		for _, v := range c.rejected {
			sess := testSess(c.step)
			r := m.ResolveReturnInput(sess, c.step, sm.ReturnInputPrefix+v)
			if !r.Rejected() || r.Problem == "" {
				t.Errorf("%s: el campo acepta %q, fue %+v", c.step, v, r)
			}
			if handlerAccepts(t, m, c, v) {
				t.Errorf("%s: la tabla dice que el handler rechaza %q y lo acepta", c.step, v)
			}
		}
	}
}

func TestAgentTextInputs_DateFieldsAreIso(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range []string{sm.StateRegBirthDate, sm.StateGfrExamDate} {
		d := m.InputDescriptor(nil, step)
		if len(d.Fields) != 1 || d.Fields[0].Kind != sm.InputKindDate || d.Fields[0].Format != sm.InputFormatISO {
			t.Errorf("%s: campo = %+v, quiero fecha con formato iso", step, d.Fields)
			continue
		}
		if r := m.ResolveReturnInput(testSess(step), step, sm.ReturnInputPrefix+"2026-07-05"); !r.OK() {
			t.Errorf("%s: la fecha iso 2026-07-05 debe aceptarse, fue %+v", step, r)
		}
	}
}

// freeTextSteps son los pasos de texto libre de C12: el handler no puede guardar una palabra
// reservada, y por eso tampoco ofrecen «volver a preguntar».
var freeTextSteps = map[string]string{
	sm.StateRegAddress:             "reg_address",
	sm.StateRegOccupation:          "reg_occupation",
	sm.StateRegBarrio:              "reg_barrio",
	sm.StateMedicationAskLastDate:  "med_last_date",
	sm.StateMedicationAskDose:      "med_dose",
	sm.StateMedicationAskFrequency: "med_frequency",
}

// «Volver a preguntarle al paciente» reanuda con __resume__: solo sirve donde el handler REAL
// lo rechaza y re-pregunta. Se comprueban el validador del campo y el handler.
func TestAskAgainOnlyWhereTheHandlerRejectsReservedKeywords(t *testing.T) {
	m := returnMenuMachine()
	var offered []string
	for _, step := range sm.RegisteredReturnSteps() {
		var opt *sm.ReturnOption
		for _, o := range sm.ReturnOptionsFor(nil, step) {
			if o.Value == "repreguntar" {
				opt = &o
			}
		}
		if opt == nil {
			continue
		}
		offered = append(offered, step)
		if !opt.NoInput || opt.Data != "" || opt.Target != "" || opt.Close {
			t.Errorf("%s: repreguntar = %+v, quiero NoInput sin dato ni destino", step, *opt)
		}
		if r := m.ResolveReturnInput(testSess(step), step, sm.ReturnInputPrefix+"__resume__"); !r.Rejected() {
			t.Errorf("%s: ofrece repreguntar pero su campo acepta __resume__", step)
		}
		sess := testSess(step)
		sess.SetContext("_prompted_weeks", "1")
		res, err := m.Process(context.Background(), sess, textM("__resume__"))
		if err != nil || res.NextState != step || len(res.Messages) == 0 {
			t.Errorf("%s: con __resume__ el handler no re-pregunta en el mismo paso (err=%v, res=%+v)", step, err, res)
		}
	}
	for _, step := range []string{
		sm.StateRegAddress, sm.StateRegOccupation, sm.StateRegMunicipality, sm.StateRegBarrio,
		sm.StateMedicationAskLastDate, sm.StateMedicationAskDose, sm.StateMedicationAskFrequency,
		sm.StateChangeEntity,
	} {
		if slices.Contains(offered, step) {
			t.Errorf("%s: no debe ofrecer repreguntar", step)
		}
	}
	for _, step := range []string{sm.StateAskDocument, sm.StateRegFirstName, sm.StateGfrCreatinine, sm.StateAskGestationalWeeks} {
		if !slices.Contains(offered, step) {
			t.Errorf("%s: debe ofrecer repreguntar", step)
		}
	}
}

func textsOf(res *sm.StateResult) []string {
	var out []string
	for _, msg := range res.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok {
			out = append(out, tm.Text)
		}
	}
	return out
}

// C12 (Bird y Néctar): una palabra reservada nunca se guarda como dato; el paso re-pregunta con
// el texto de la entrada vacía (en REG_OCCUPATION, que acepta el vacío, con su pregunta).
func TestFreeTextHandlers_ReservedKeywordNotStored(t *testing.T) {
	m := returnMenuMachine()
	for _, phone := range []string{"+573001112233", "nectar:7"} {
		for step, key := range freeTextSteps {
			run := func(text string) (*session.Session, *sm.StateResult) {
				sess := testSess(step)
				sess.PhoneNumber = phone
				msg := bird.InboundMessage{ID: "m", Phone: phone, MessageType: "text", Text: text, ReceivedAt: time.Now()}
				res, err := m.Process(context.Background(), sess, msg)
				if err != nil {
					t.Fatalf("%s/%s: %v", phone, step, err)
				}
				return sess, res
			}
			sess, res := run("__resume__")
			if _, stored := sess.Context[key]; stored {
				t.Errorf("%s/%s: guardó %s = %q en la sesión", phone, step, key, sess.Context[key])
			}
			if _, stored := res.UpdateCtx[key]; stored {
				t.Errorf("%s/%s: el resultado escribe %s = %q", phone, step, key, res.UpdateCtx[key])
			}
			if res.NextState != step {
				t.Errorf("%s/%s: pasó a %s, debe seguir en el paso", phone, step, res.NextState)
			}
			want := []string{occupationQuestion}
			if step != sm.StateRegOccupation {
				_, empty := run("")
				want = textsOf(empty)
			}
			if got := textsOf(res); len(got) == 0 || !slices.Equal(got, want) {
				t.Errorf("%s/%s: textos = %q, quiero %q", phone, step, got, want)
			}
		}
	}
}

// En un paso con campo, Néctar adjunta lo escrito a la PRIMERA salida «reanudar»: tiene que haber
// exactamente una y ser bot_reanudar.
func TestReturnMenus_FieldStepsHaveExactlyOneCarryingOption(t *testing.T) {
	m := returnMenuMachine()
	for _, step := range sm.RegisteredReturnSteps() {
		d := m.InputDescriptor(nil, step)
		if len(d.Fields) == 0 {
			continue
		}
		var carrying []string
		for _, o := range d.Options {
			if o.Exit == "reanudar" {
				carrying = append(carrying, o.Value)
			}
		}
		if !slices.Equal(carrying, []string{sm.ReturnResume}) {
			t.Errorf("%s: opciones «reanudar» = %v, quiero solo %s", step, carrying, sm.ReturnResume)
		}
	}
}

// Las opciones sin dato de los pasos de campo (P1-4) y su rótulo de reanudar.
func TestAgentTextInputs_NoInputOptionsAndResumeLabels(t *testing.T) {
	returnMenuMachine()
	find := func(step, value string) (sm.ReturnOption, bool) {
		for _, o := range sm.ReturnOptionsFor(nil, step) {
			if o.Value == value {
				return o, true
			}
		}
		return sm.ReturnOption{}, false
	}
	for _, step := range []string{sm.StateUploadMedicalOrder, sm.StateValidateOCR, sm.StateOCRFailed, sm.StateConfirmOCRResult, sm.StateAskMedicalOrder} {
		o, ok := find(step, "pedir_orden")
		if !ok || o.Target != sm.StateUploadMedicalOrder || !o.NoInput || o.Data != "" {
			t.Errorf("%s: pedir_orden = %+v (ok=%v)", step, o, ok)
		}
	}
	for _, v := range []string{"ocr_correct", "ocr_incorrect"} {
		o, ok := find(sm.StateConfirmOCRResult, v)
		if !ok || o.Data != v || !o.NoInput {
			t.Errorf("CONFIRM_OCR_RESULT: %s = %+v (ok=%v)", v, o, ok)
		}
	}
	for _, v := range []string{"weeks_yes", "weeks_no"} {
		o, ok := find(sm.StateAskGestationalWeeks, v)
		if !ok || o.Data != v || !o.NoInput {
			t.Errorf("ASK_GESTATIONAL_WEEKS: %s = %+v (ok=%v)", v, o, ok)
		}
	}
	resumeLabel := func(step string) string {
		o, _ := find(step, sm.ReturnResume)
		return o.Label
	}
	for _, c := range agentFieldCases() {
		if got := resumeLabel(c.step); got != "Enviar el dato escrito al bot" {
			t.Errorf("%s: rótulo de reanudar = %q", c.step, got)
		}
	}
	if got := resumeLabel(sm.StateAskDocument); got != "Enviar el dato escrito al bot" {
		t.Errorf("ASK_DOCUMENT: rótulo de reanudar = %q", got)
	}
	for _, step := range cupsSteps {
		if got := resumeLabel(step); got != "Enviar los CUPS escritos al bot" {
			t.Errorf("%s: rótulo de reanudar = %q", step, got)
		}
	}
}
