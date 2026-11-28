package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/results"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// --- Fakes ---

// resultsCalls registra en orden las llamadas a SIESA y al tope (prueba del orden de reserva).
type resultsCalls struct{ log []string }

func (c *resultsCalls) add(s string) { c.log = append(c.log, s) }

// fakeAttempts imita el tope del repo: cuenta documento y teléfono por separado, el intento es
// el mayor de los dos y bloquea al llegar a 3. forcedAttempt fuerza el número que devuelve
// RecordFailure (p. ej. 4: otra sesión ya gastó el tope).
type fakeAttempts struct {
	calls         *resultsCalls
	lockedUp      bool
	docF, phoneF  int
	forcedAttempt int
	phones        []string
	isLockedErr   error
}

func (f *fakeAttempts) IsLocked(_ context.Context, _, phone string, _ time.Time) (bool, error) {
	f.calls.add("IsLocked")
	f.phones = append(f.phones, phone)
	if f.isLockedErr != nil {
		return false, f.isLockedErr
	}
	return f.lockedUp || max(f.docF, f.phoneF) >= 3, nil
}

func (f *fakeAttempts) RecordFailure(_ context.Context, _, phone string, _ time.Time) (int, bool, error) {
	f.calls.add("RecordFailure")
	f.phones = append(f.phones, phone)
	f.docF++
	f.phoneF++
	attempt := max(f.docF, f.phoneF)
	if f.forcedAttempt > 0 {
		attempt = f.forcedAttempt
	}
	return attempt, attempt >= 3, nil
}

func (f *fakeAttempts) Reset(context.Context, string) error {
	f.calls.add("Reset")
	f.docF = 0
	return nil
}

func (f *fakeAttempts) Release(_ context.Context, phone string, _ time.Time) error {
	f.calls.add("Release")
	f.phones = append(f.phones, phone)
	f.phoneF = max(f.phoneF-1, 0)
	return nil
}

type fakeSearcher struct {
	studies []results.Study
	err     error
	docs    []string
	// Descarga: pdf y dlErr son lo que devuelve; block la deja colgada hasta que venza el ctx.
	pdf   []byte
	dlErr error
	block bool
	files []string
	order *resultsCalls
}

func (f *fakeSearcher) Search(_ context.Context, doc string) ([]results.Study, error) {
	f.docs = append(f.docs, doc)
	return f.studies, f.err
}

func (f *fakeSearcher) DownloadPDF(ctx context.Context, file string) ([]byte, error) {
	f.files = append(f.files, file)
	if f.order != nil {
		f.order.add("DownloadPDF")
	}
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.pdf, f.dlErr
}

type sentDocument struct {
	phone, filename, caption string
	data                     []byte
}

// fakeResultsSender registra los envíos; order es el registro compartido con la descarga.
type fakeResultsSender struct {
	order   *resultsCalls
	texts   []string
	docs    []sentDocument
	docErr  error
	docWait bool
}

func (f *fakeResultsSender) SendTextCtx(_ context.Context, _, _, text string) (string, error) {
	f.order.add("SendText")
	f.texts = append(f.texts, text)
	return "m1", nil
}

func (f *fakeResultsSender) SendDocumentCtx(ctx context.Context, phone, filename string, data []byte, caption string) (string, error) {
	f.order.add("SendDocument")
	if f.docWait {
		<-ctx.Done()
		return "", ctx.Err()
	}
	f.docs = append(f.docs, sentDocument{phone: phone, filename: filename, caption: caption, data: data})
	return "m2", f.docErr
}

// --- Utilidades ---

const (
	resultsTestDoc  = "1029384756"
	resultsTestDate = "17/04/1992"
)

func resultsCfg() *config.Config {
	c := sampleConfig()
	c.ResultsAPIURL = "http://results.test"
	c.ResultsPDFEnabled = true
	c.ResultsURL = "https://portal.test/resultados"
	return c
}

func resultsPatient() *domain.Patient {
	return &domain.Patient{
		ID: "P1", FirstName: "Juan", FirstSurname: "Perez",
		BirthDate: time.Date(1992, 4, 17, 13, 45, 0, 0, time.Local),
	}
}

// resultsRig arma la máquina del flujo con fakes que comparten el registro de llamadas.
type resultsRig struct {
	m        *sm.Machine
	cfg      *config.Config
	calls    *resultsCalls
	attempts *fakeAttempts
	searcher *fakeSearcher
	sender   *fakeResultsSender
	sendLog  *resultsCalls
	patient  *domain.Patient
	siesaErr error
}

func newResultsRig() *resultsRig {
	r := &resultsRig{cfg: resultsCfg(), calls: &resultsCalls{}, sendLog: &resultsCalls{}, searcher: &fakeSearcher{
		studies: []results.Study{{PatientName: "JUAN PEREZ", StudyType: "EMG", File: "a.pdf"}},
		pdf:     []byte("%PDF-1.4 resultado"),
	}, patient: resultsPatient()}
	r.searcher.order = r.sendLog
	r.sender = &fakeResultsSender{order: r.sendLog}
	r.attempts = &fakeAttempts{calls: r.calls}
	repo := &mockPatientRepo{findByDocumentFn: func(context.Context, string, string) (*domain.Patient, error) {
		r.calls.add("FindByDocument")
		return r.patient, r.siesaErr
	}}
	r.m = sm.NewMachine()
	RegisterGreetingHandlers(r.m, r.cfg, nil)
	RegisterResultsAndLocationHandlers(r.m, r.cfg, nil)
	RegisterResultsFlowHandlers(r.m, r.cfg, repo, r.searcher, r.sender, r.attempts)
	return r
}

func nectarResultsSess(state string) *session.Session {
	s := testSess(state)
	s.PhoneNumber = "nectar:42"
	s.ContactIdentifier = "573001234567"
	return s
}

// birthDateSess es la sesión que ya dio tipo y número.
func birthDateSess() *session.Session {
	s := nectarResultsSess(sm.StateResultsAskBirthDate)
	s.Context[ctxResultsDocType] = "CC"
	s.Context[ctxResultsDoc] = resultsTestDoc
	s.Context[ctxResultsDocHash] = local.HashDocument("CC", resultsTestDoc)
	return s
}

// step procesa un mensaje y aplica el resultado a la sesión, como el worker.
func step(t *testing.T, m *sm.Machine, sess *session.Session, msg bird.InboundMessage) *sm.StateResult {
	t.Helper()
	r, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range r.ClearCtx {
		delete(sess.Context, k)
	}
	for k, v := range r.UpdateCtx {
		sess.Context[k] = v
	}
	sess.CurrentState = r.NextState
	return r
}

func texts(r *sm.StateResult) []string {
	var out []string
	for _, m := range r.Messages {
		if tm, ok := m.(*sm.TextMessage); ok {
			out = append(out, tm.Text)
		}
	}
	return out
}

func eventData(r *sm.StateResult, typ string) (map[string]interface{}, bool) {
	for _, e := range r.Events {
		if e.Type == typ {
			return e.Data, true
		}
	}
	return nil, false
}

// --- Pruebas ---

func TestResults_FeatureOffKeepsTodayBehavior(t *testing.T) {
	cases := map[string]func(*config.Config, *session.Session){
		"sin URL":       func(c *config.Config, _ *session.Session) { c.ResultsAPIURL = "" },
		"bandera false": func(c *config.Config, _ *session.Session) { c.ResultsPDFEnabled = false },
		"sesión Bird":   func(_ *config.Config, s *session.Session) { s.PhoneNumber = "+573001234567" },
	}
	for name, tweak := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := resultsCfg()
			sess := nectarResultsSess(sm.StateMainMenu)
			tweak(cfg, sess)
			m := sm.NewMachine()
			RegisterGreetingHandlers(m, cfg, nil)
			r, err := m.Process(context.Background(), sess, postbackM("resultados"))
			if err != nil {
				t.Fatal(err)
			}
			if r.NextState != sm.StateShowResults {
				t.Fatalf("NextState = %s, quiero SHOW_RESULTS", r.NextState)
			}
		})
	}
	// Encendida: entra a la verificación.
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, resultsCfg(), nil)
	r, err := m.Process(context.Background(), nectarResultsSess(sm.StateMainMenu), postbackM("resultados"))
	if err != nil || r.NextState != sm.StateResultsStart {
		t.Fatalf("encendida: NextState = %v (err %v), quiero RESULTS_START", r, err)
	}
}

func TestResults_DocTypeThenNumberThenBirthDate(t *testing.T) {
	rig := newResultsRig()
	sess := nectarResultsSess(sm.StateMainMenu)

	r := step(t, rig.m, sess, postbackM("resultados"))
	if r.NextState != sm.StateResultsAskDocType || !slices.Contains(texts(r), docTypeMenuText()) {
		t.Fatalf("menú: %s %v", r.NextState, texts(r))
	}
	if _, ok := eventData(r, "results_menu"); !ok {
		t.Fatal("falta results_menu")
	}

	r = step(t, rig.m, sess, textM("1"))
	if r.NextState != sm.StateResultsAskDocNumber || sess.Context[ctxResultsDocType] != "CC" ||
		!slices.Contains(texts(r), documentNumberQuestion) {
		t.Fatalf("tipo: %s %v ctx=%v", r.NextState, texts(r), sess.Context)
	}

	r = step(t, rig.m, sess, textM(resultsTestDoc))
	if r.NextState != sm.StateResultsAskBirthDate || !slices.Contains(texts(r), birthDateHelp) {
		t.Fatalf("número: %s %v", r.NextState, texts(r))
	}
	if sess.Context[ctxResultsDoc] != resultsTestDoc || sess.Context[ctxResultsDocHash] != local.HashDocument("CC", resultsTestDoc) {
		t.Fatalf("contexto del número: %v", sess.Context)
	}
	// El tope se mira con el teléfono del contacto de Néctar, no con la clave de la sesión.
	for _, p := range rig.attempts.phones {
		if p != "+573001234567" {
			t.Fatalf("teléfono del tope = %q", p)
		}
	}
	if !slices.Equal(rig.calls.log, []string{"IsLocked", "IsLocked"}) {
		t.Fatalf("llamadas = %v: el tope se mira al entrar y con el documento", rig.calls.log)
	}
}

func TestResults_VerifiedGoesToLookup(t *testing.T) {
	rig := newResultsRig()
	var seen map[string]string
	rig.m.Register(sm.StateResultsSend, func(_ context.Context, s *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		seen = make(map[string]string, len(s.Context))
		for k, v := range s.Context {
			seen[k] = v
		}
		return sm.NewResult(sm.StateTerminated), nil
	})
	sess := birthDateSess()
	r := step(t, rig.m, sess, textM(resultsTestDate))

	if _, ok := eventData(r, "results_verified"); !ok {
		t.Fatalf("falta results_verified: %+v", r.Events)
	}
	if !slices.Equal(rig.searcher.docs, []string{resultsTestDoc}) {
		t.Fatalf("la API se consultó con %v", rig.searcher.docs)
	}
	if seen == nil {
		t.Fatal("no llegó a RESULTS_SEND")
	}
	if seen[ctxResultsFirstName] != "Juan" || seen[ctxResultsFirstSurname] != "Perez" {
		t.Fatalf("nombre de SIESA no guardado: %v", seen)
	}
	var studies []results.Study
	if err := json.Unmarshal([]byte(seen[ctxResultsStudies]), &studies); err != nil || len(studies) != 1 || studies[0].File != "a.pdf" {
		t.Fatalf("results_studies = %q (%v)", seen[ctxResultsStudies], err)
	}
	if _, ok := seen[ctxResultsDoc]; ok {
		t.Fatal("el número sigue en el contexto tras la consulta")
	}
	if !slices.Equal(rig.calls.log, []string{"FindByDocument", "RecordFailure", "Reset", "Release"}) {
		t.Fatalf("llamadas = %v", rig.calls.log)
	}
}

func TestResults_LookupErrorCountsAsNoResults(t *testing.T) {
	rig := newResultsRig()
	rig.searcher.err = errors.New("results: la API respondió 500")
	r := step(t, rig.m, birthDateSess(), textM(resultsTestDate))
	if _, ok := eventData(r, "results_none"); !ok {
		t.Fatalf("falta results_none: %+v", r.Events)
	}
	if got := texts(r); len(got) != 1 || got[0] != resultsNoneWant(rig.cfg) {
		t.Fatalf("texto = %q", got)
	}
}

func TestResults_MismatchSameMessageAsNotFound(t *testing.T) {
	want := "Los datos no coinciden. Volvamos a intentarlo.\n\n" + docTypeMenuText()
	run := func(p *domain.Patient) (*sm.StateResult, *session.Session) {
		rig := newResultsRig()
		rig.patient = p
		sess := birthDateSess()
		return step(t, rig.m, sess, textM(resultsTestDate)), sess
	}
	other := resultsPatient()
	other.BirthDate = time.Date(1990, 1, 1, 0, 0, 0, 0, time.Local)
	rMismatch, sMismatch := run(other)
	rMissing, _ := run(nil)

	for name, r := range map[string]*sm.StateResult{"fecha distinta": rMismatch, "no existe": rMissing} {
		if r.NextState != sm.StateResultsAskDocType {
			t.Errorf("%s: NextState = %s", name, r.NextState)
		}
		if got := texts(r); len(got) != 1 || got[0] != want {
			t.Errorf("%s: texto = %q", name, got)
		}
	}
	if eventsOf(t, rMismatch) != eventsOf(t, rMissing) {
		t.Errorf("los eventos distinguen los dos casos: %s vs %s", eventsOf(t, rMismatch), eventsOf(t, rMissing))
	}
	if _, ok := sMismatch.Context[ctxResultsDoc]; ok {
		t.Error("tras el fallo el número debe borrarse (se vuelve a pedir desde el tipo)")
	}
}

// fail lleva la sesión por tipo y número (si no es la primera vuelta) y una fecha distinta.
func failOnce(t *testing.T, rig *resultsRig, sess *session.Session, first bool) *sm.StateResult {
	t.Helper()
	if !first {
		step(t, rig.m, sess, textM("1"))
		step(t, rig.m, sess, textM(resultsTestDoc))
	}
	return step(t, rig.m, sess, textM("01/01/1990"))
}

func TestResults_ThirdFailureLocksAndOffersPortalAndSIAU(t *testing.T) {
	rig := newResultsRig()
	sess := birthDateSess()
	wantText := "Por seguridad, la consulta está bloqueada por hoy. Puedes consultar tus resultados en " +
		rig.cfg.ResultsURL + " o escribir *menú* y elegir Atención al usuario (SIAU)."
	for i := 1; i <= 3; i++ {
		r := failOnce(t, rig, sess, i == 1)
		failed, ok := eventData(r, "results_verification_failed")
		if !ok || failed["attempt"] != i {
			t.Fatalf("intento %d: evento = %v", i, failed)
		}
		if i < 3 {
			continue
		}
		if got := texts(r); len(got) != 1 || got[0] != wantText {
			t.Fatalf("texto al tercero = %q", got)
		}
		if fb, _ := eventData(r, "results_fallback_link"); fb["reason"] != "locked" {
			t.Fatalf("results_fallback_link = %v", fb)
		}
		if r.NextState != sm.StateTerminated {
			t.Fatalf("NextState = %s: cierra el flujo sin escalar", r.NextState)
		}
	}
	// Un 4.º intento, aun con la fecha CORRECTA, se rechaza sin comparar.
	rig.calls.log = nil
	r := step(t, rig.m, birthDateSess(), textM(resultsTestDate))
	if _, ok := eventData(r, "results_verified"); ok {
		t.Fatal("el 4.º intento verificó")
	}
	if got := texts(r); len(got) != 1 || got[0] != wantText {
		t.Fatalf("texto al 4.º = %q", got)
	}
	if slices.Contains(rig.calls.log, "Reset") || slices.Contains(rig.calls.log, "Release") {
		t.Fatalf("llamadas = %v", rig.calls.log)
	}
	if len(rig.searcher.docs) != 0 {
		t.Fatal("se consultó la API sin verificar")
	}
}

func TestResults_TwoFailuresThenCorrectDateVerifies(t *testing.T) {
	rig := newResultsRig()
	sess := birthDateSess()
	failOnce(t, rig, sess, true)
	failOnce(t, rig, sess, false)
	step(t, rig.m, sess, textM("1"))
	step(t, rig.m, sess, textM(resultsTestDoc))
	r := step(t, rig.m, sess, textM(resultsTestDate))
	if _, ok := eventData(r, "results_verified"); !ok {
		t.Fatalf("el 3.er intento con la fecha correcta debe verificar: %+v %q", r.Events, texts(r))
	}
	if rig.attempts.docF != 0 || rig.attempts.phoneF != 2 {
		t.Fatalf("tras el acierto: doc=%d teléfono=%d, quiero 0 y 2 (Reset y Release)", rig.attempts.docF, rig.attempts.phoneF)
	}
}

func TestResults_LockedUpFront(t *testing.T) {
	rig := newResultsRig()
	rig.attempts.lockedUp = true
	r := step(t, rig.m, nectarResultsSess(sm.StateMainMenu), postbackM("resultados"))
	want := "Por seguridad, la consulta está bloqueada por hoy. Puedes consultar tus resultados en " +
		rig.cfg.ResultsURL + " o escribir *menú* y elegir Atención al usuario (SIAU)."
	if got := texts(r); len(got) != 1 || got[0] != want {
		t.Fatalf("texto = %q", got)
	}
	if r.NextState != sm.StateTerminated {
		t.Fatalf("NextState = %s", r.NextState)
	}
	if fb, _ := eventData(r, "results_fallback_link"); fb["reason"] != "locked" {
		t.Fatalf("results_fallback_link = %v", fb)
	}
}

func TestResults_LockedDocumentDoesNotAskBirthDate(t *testing.T) {
	rig := newResultsRig()
	sess := nectarResultsSess(sm.StateResultsAskDocNumber)
	sess.Context[ctxResultsDocType] = "CC"
	rig.attempts.lockedUp = true
	r := step(t, rig.m, sess, textM(resultsTestDoc))
	if r.NextState != sm.StateTerminated || len(texts(r)) != 1 || !strings.HasPrefix(texts(r)[0], "Por seguridad") {
		t.Fatalf("%s %q", r.NextState, texts(r))
	}
}

func TestResults_AttemptsDBDownFailsClosed(t *testing.T) {
	rig := newResultsRig()
	rig.attempts.isLockedErr = errors.New("db down")
	r := step(t, rig.m, nectarResultsSess(sm.StateMainMenu), postbackM("resultados"))
	if r.NextState != sm.StateTerminated || slices.Contains(texts(r), docTypeMenuText()) {
		t.Fatalf("sin tope legible no se entra al flujo: %s %q", r.NextState, texts(r))
	}
}

func TestResults_SIESADownSendsPortalNoAttempt(t *testing.T) {
	rig := newResultsRig()
	rig.siesaErr = errors.New("siesa: timeout")
	r := step(t, rig.m, birthDateSess(), textM(resultsTestDate))
	if slices.Contains(rig.calls.log, "RecordFailure") {
		t.Fatalf("SIESA caída no cuenta el intento: %v", rig.calls.log)
	}
	if got := texts(r); len(got) != 1 || !strings.Contains(got[0], rig.cfg.ResultsURL) {
		t.Fatalf("texto = %q", got)
	}
	if fb, _ := eventData(r, "results_fallback_link"); fb["reason"] != "siesa_down" {
		t.Fatalf("results_fallback_link = %v", fb)
	}
	if r.NextState != sm.StateTerminated {
		t.Fatalf("NextState = %s", r.NextState)
	}
}

// Ruling 2: el intento se reserva ANTES de usar la comparación. Si la reserva bloquea, ni una
// fecha correcta verifica; si coincide, Reset va después.
func TestResults_ReservesAttemptBeforeComparing(t *testing.T) {
	// Otra sesión (otro teléfono) ya gastó el tope: la reserva devuelve el 4.º y la fecha
	// CORRECTA no llega a compararse.
	rig := newResultsRig()
	rig.attempts.forcedAttempt = 4
	r := step(t, rig.m, birthDateSess(), textM(resultsTestDate))
	if _, ok := eventData(r, "results_verified"); ok {
		t.Fatal("verificó pese a que la reserva pasó el tope")
	}
	if !slices.Equal(rig.calls.log, []string{"FindByDocument", "RecordFailure"}) {
		t.Fatalf("llamadas = %v: nada después de la reserva rechazada", rig.calls.log)
	}
	if len(rig.searcher.docs) != 0 || r.NextState != sm.StateTerminated {
		t.Fatalf("no debe consultar la API: docs=%v state=%s", rig.searcher.docs, r.NextState)
	}
	if fb, _ := eventData(r, "results_fallback_link"); fb["reason"] != "locked" {
		t.Fatalf("results_fallback_link = %v", fb)
	}

	// Al coincidir: la reserva va antes, y Reset y Release después.
	ok := newResultsRig()
	step(t, ok.m, birthDateSess(), textM(resultsTestDate))
	if !slices.Equal(ok.calls.log, []string{"FindByDocument", "RecordFailure", "Reset", "Release"}) {
		t.Fatalf("llamadas al coincidir = %v", ok.calls.log)
	}
	if ok.attempts.phones[len(ok.attempts.phones)-1] != "+573001234567" {
		t.Fatalf("Release con el teléfono %q", ok.attempts.phones[len(ok.attempts.phones)-1])
	}
}

// El retorno sin dato del agente no es un intento: re-pregunta sin tocar el tope.
func TestResults_AgentResumeAtBirthDateIsNoAttempt(t *testing.T) {
	rig := newResultsRig()
	sess := birthDateSess()
	sess.Context[sm.CtxAgentResumeTurn] = "1"
	r := step(t, rig.m, sess, textM("__resume__"))
	if r.NextState != sm.StateResultsAskBirthDate || !slices.Contains(texts(r), birthDateHelp) {
		t.Fatalf("%s %q", r.NextState, texts(r))
	}
	if len(rig.calls.log) != 0 {
		t.Fatalf("llamadas = %v", rig.calls.log)
	}
}

func TestResults_EventsDoNotLeakPII(t *testing.T) {
	rig := newResultsRig()
	sess := nectarResultsSess(sm.StateMainMenu)
	var all []sm.Event
	for _, msg := range []bird.InboundMessage{
		postbackM("resultados"),
		textM("CC " + resultsTestDoc), // el número en el paso del tipo
		textM("1"),
		textM(resultsTestDoc + "x"), // número inválido
		textM(resultsTestDoc),
		textM("1992-04-17 mañana"), // fecha ilegible
		textM("18/04/1992"),        // no coincide
		textM("1"),
		textM(resultsTestDoc),
		textM(resultsTestDate),
	} {
		all = append(all, step(t, rig.m, sess, msg).Events...)
	}
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	ev := string(b)
	assertNoneIn(t, ev, resultsTestDoc, "17/04/1992", "18/04/1992", "1992-04-17", "Juan", "Perez", "JUAN", "a.pdf", "Resultado-")
	for _, typ := range []string{"results_menu", "invalid_input", "results_verification_failed", "results_verified", "results_sent"} {
		if !strings.Contains(ev, `"`+typ+`"`) {
			t.Errorf("falta el evento %s: %s", typ, ev)
		}
	}
}

func TestResults_ReturnMenus(t *testing.T) {
	newResultsRig()
	for _, st := range []string{sm.StateResultsAskDocType, sm.StateResultsAskDocNumber, sm.StateResultsAskBirthDate} {
		var menu, resume bool
		for _, o := range sm.ReturnOptionsFor(nil, st) {
			if o.Value == "results_menu" && o.Target == sm.StateMainMenu && slices.Contains(o.Clear, ctxResultsDoc) {
				menu = true
			}
			if o.Value == sm.ReturnResume && o.Label == "Reanudar" {
				resume = true
			}
		}
		if !menu || !resume {
			t.Errorf("%s: menú=%v reanudar=%v", st, menu, resume)
		}
	}
}

// --- Lista y envío del PDF (Tarea 7) ---

func resultsNoneWant(cfg *config.Config) string {
	return "No encontramos resultados para ese documento. 🔍\nPuede que aún no estén disponibles; intenta mañana.\n\n" +
		"También puedes consultarlos en " + cfg.ResultsURL + closingNote
}

func resultsFallbackWant(cfg *config.Config) string {
	return "No pude enviarte el archivo en este momento. 🙏 Puedes descargarlo en " + cfg.ResultsURL + closingNote
}

func lists(r *sm.StateResult) []*sm.ListMessage {
	var out []*sm.ListMessage
	for _, m := range r.Messages {
		if lm, ok := m.(*sm.ListMessage); ok {
			out = append(out, lm)
		}
	}
	return out
}

func studyOn(name, kind, file string, day int) results.Study {
	return results.Study{
		PatientName: name, StudyType: kind, File: file, Admission: "A" + file,
		Date: time.Date(2026, 3, day, 9, 30, 0, 0, time.Local),
	}
}

// verify lleva una sesión verificada (fecha correcta) por RESULTS_LOOKUP con los estudios dados.
func verifyWith(t *testing.T, rig *resultsRig, studies []results.Study) (*sm.StateResult, *session.Session) {
	t.Helper()
	rig.searcher.studies = studies
	sess := birthDateSess()
	return step(t, rig.m, sess, textM(resultsTestDate)), sess
}

func TestResults_FiltersByVerifiedName(t *testing.T) {
	// Solo el del paciente: aunque el ajeno sea más reciente, sale el suyo y directo (uno solo).
	rig := newResultsRig()
	r, _ := verifyWith(t, rig, []results.Study{
		studyOn("MARIA GOMEZ", "TAC", "ajeno.pdf", 20),
		studyOn("PEREZ LOPEZ JUAN CARLOS", "EMG", "suyo.pdf", 10),
	})
	if len(lists(r)) != 0 || !slices.Equal(rig.searcher.files, []string{"suyo.pdf"}) || len(rig.sender.docs) != 1 {
		t.Fatalf("listas=%d descargas=%v docs=%d", len(lists(r)), rig.searcher.files, len(rig.sender.docs))
	}

	// Ninguno coincide: el texto de «no encontramos» y results_name_mismatch; nada se baja ni envía.
	rig = newResultsRig()
	r, sess := verifyWith(t, rig, []results.Study{
		studyOn("MARIA GOMEZ", "TAC", "x.pdf", 20),
		studyOn("JUANA PEREZ", "RX", "y.pdf", 21),
	})
	if got := texts(r); len(got) != 1 || got[0] != resultsNoneWant(rig.cfg) {
		t.Fatalf("texto = %q", got)
	}
	if _, ok := eventData(r, "results_name_mismatch"); !ok {
		t.Fatalf("falta results_name_mismatch: %+v", r.Events)
	}
	if _, ok := eventData(r, "results_none"); ok {
		t.Fatal("el sin-coincidencia no es results_none")
	}
	if len(rig.searcher.files) != 0 || len(rig.sendLog.log) != 0 || r.NextState != sm.StateTerminated {
		t.Fatalf("descargas=%v envíos=%v estado=%s", rig.searcher.files, rig.sendLog.log, r.NextState)
	}
	if _, ok := sess.Context[ctxResultsStudies]; ok {
		t.Fatal("results_studies sigue en el contexto")
	}
}

func TestResults_OneStudySentDirectly(t *testing.T) {
	rig := newResultsRig()
	r, _ := verifyWith(t, rig, []results.Study{studyOn("JUAN PEREZ", "EMG", "a.pdf", 5)})
	if len(lists(r)) != 0 {
		t.Fatal("con un estudio no hay lista")
	}
	if len(rig.sender.docs) != 1 || r.NextState != sm.StateTerminated {
		t.Fatalf("docs=%d estado=%s", len(rig.sender.docs), r.NextState)
	}
	if got := texts(r); len(got) != 1 || got[0] != resultsSentText+closingNote {
		t.Fatalf("texto = %q", got)
	}
}

func TestResults_ListUpTo10(t *testing.T) {
	rig := newResultsRig()
	long := "RESONANCIA MAGNÉTICA DE CEREBRO SIMPLE"
	var studies []results.Study
	for day := 1; day <= 12; day++ {
		studies = append(studies, studyOn("JUAN PEREZ", "RX "+strconv.Itoa(day), "f"+strconv.Itoa(day)+".pdf", day))
	}
	studies[11].StudyType = long                                          // el más reciente (día 12)
	studies[10].Admission = strings.Repeat("9", 80)                       // día 11: descripción larga
	studies = append(studies, studyOn("JUAN PEREZ", "DUP", "f12.pdf", 1)) // archivo repetido
	r, sess := verifyWith(t, rig, studies)

	ls := lists(r)
	if len(ls) != 1 || r.NextState != sm.StateResultsList {
		t.Fatalf("listas=%d estado=%s", len(ls), r.NextState)
	}
	rows := ls[0].Sections[0].Rows
	if len(rows) != 10 {
		t.Fatalf("filas = %d, quiero 10", len(rows))
	}
	if rows[0].Title != truncate(long, 24) || utf8.RuneCountInString(rows[0].Title) > 24 {
		t.Fatalf("título = %q", rows[0].Title)
	}
	if rows[0].Description != "Fecha: 12/03/2026 | Adm: Af12.pdf" {
		t.Fatalf("descripción = %q", rows[0].Description)
	}
	if want := truncate("Fecha: 11/03/2026 | Adm: "+strings.Repeat("9", 80), 72); rows[1].Description != want ||
		utf8.RuneCountInString(rows[1].Description) > 72 {
		t.Fatalf("descripción larga = %q", rows[1].Description)
	}
	if rows[9].Title != "RX 3" {
		t.Fatalf("la última fila es %q: solo los 10 más recientes", rows[9].Title)
	}
	if got := texts(r); !slices.Contains(got, "Para resultados anteriores ingresa a "+rig.cfg.ResultsURL) {
		t.Fatalf("falta el texto de los anteriores: %q", got)
	}
	if len(rig.searcher.files) != 0 {
		t.Fatal("no se baja nada antes de elegir")
	}

	// Elegir la 2.ª fila (con número escrito) envía ese estudio.
	r = step(t, rig.m, sess, textM("2"))
	if !slices.Equal(rig.searcher.files, []string{"f11.pdf"}) || len(rig.sender.docs) != 1 {
		t.Fatalf("descargas=%v docs=%d", rig.searcher.files, len(rig.sender.docs))
	}
	if r.NextState != sm.StateTerminated {
		t.Fatalf("estado = %s", r.NextState)
	}

	// Con 10 o menos no hay texto de anteriores; la elección por fila también vale.
	rig = newResultsRig()
	r, sess = verifyWith(t, rig, []results.Study{
		studyOn("JUAN PEREZ", "RX", "a.pdf", 1), studyOn("JUAN PEREZ", "TAC", "b.pdf", 2),
	})
	if len(texts(r)) != 0 || len(lists(r)) != 1 {
		t.Fatalf("textos=%q listas=%d", texts(r), len(lists(r)))
	}
	step(t, rig.m, sess, postbackM(lists(r)[0].Sections[0].Rows[1].ID))
	if !slices.Equal(rig.searcher.files, []string{"a.pdf"}) {
		t.Fatalf("descargas = %v", rig.searcher.files)
	}
}

func TestResults_ListInvalidChoiceAndAgentResume(t *testing.T) {
	rig := newResultsRig()
	_, sess := verifyWith(t, rig, []results.Study{
		studyOn("JUAN PEREZ", "RX", "a.pdf", 1), studyOn("JUAN PEREZ", "TAC", "b.pdf", 2),
	})
	r := step(t, rig.m, sess, textM("7"))
	if r.NextState != sm.StateResultsList || len(rig.searcher.files) != 0 {
		t.Fatalf("elección inválida: %s descargas=%v", r.NextState, rig.searcher.files)
	}
	sess.Context[sm.CtxAgentResumeTurn] = "1"
	r = step(t, rig.m, sess, textM("__resume__"))
	if r.NextState != sm.StateResultsList || len(lists(r)) != 1 || len(lists(r)[0].Sections[0].Rows) != 2 {
		t.Fatalf("el retorno del agente repite la lista: %s %d", r.NextState, len(lists(r)))
	}
}

func TestResults_NoneFound(t *testing.T) {
	rig := newResultsRig()
	r, sess := verifyWith(t, rig, nil)
	if got := texts(r); len(got) != 1 || got[0] != resultsNoneWant(rig.cfg) {
		t.Fatalf("texto = %q", got)
	}
	if _, ok := eventData(r, "results_none"); !ok {
		t.Fatalf("falta results_none: %+v", r.Events)
	}
	if r.NextState != sm.StateTerminated || len(rig.sendLog.log) != 0 {
		t.Fatalf("estado=%s envíos=%v", r.NextState, rig.sendLog.log)
	}
	if _, ok := sess.Context[ctxResultsFirstName]; ok {
		t.Fatal("el nombre sigue en el contexto")
	}
}

func TestResults_SendWaitsThenDocument(t *testing.T) {
	rig := newResultsRig()
	kind := "RESONANCIA MAGNÉTICA DE CRÁNEO (SIMPLE)"
	r, _ := verifyWith(t, rig, []results.Study{studyOn("JUAN PEREZ", kind, "a.pdf", 5)})

	if !slices.Equal(rig.sendLog.log, []string{"SendText", "DownloadPDF", "SendDocument"}) {
		t.Fatalf("orden = %v: el aviso sale antes de la descarga", rig.sendLog.log)
	}
	if !slices.Equal(rig.sender.texts, []string{"Un momento, estoy preparando tu resultado… ⏳"}) {
		t.Fatalf("aviso = %q", rig.sender.texts)
	}
	doc := rig.sender.docs[0]
	if doc.filename != "Resultado-RESONANCIA-MAGNETICA-DE-CRANEO-SIMPLE-2026-03-05.pdf" {
		t.Fatalf("filename = %q", doc.filename)
	}
	if want := "📄 Hola Juan, tu resultado de *" + kind + "*."; doc.caption != want {
		t.Fatalf("caption = %q, quiero %q", doc.caption, want)
	}
	if doc.phone != "nectar:42" || string(doc.data) != "%PDF-1.4 resultado" {
		t.Fatalf("phone=%q data=%q", doc.phone, doc.data)
	}
	sent, ok := eventData(r, "results_sent")
	if !ok || sent["study_type"] != kind || len(sent) != 1 {
		t.Fatalf("results_sent = %v", sent)
	}
	// El aviso no va en los mensajes del turno (ya salió directo).
	if slices.Contains(texts(r), resultsWaitText) {
		t.Fatal("el aviso salió dos veces")
	}
}

func TestResults_FilenameLimits(t *testing.T) {
	long := results.Study{StudyType: strings.Repeat("ÁB ", 150), Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local)}
	if f := resultsFilename(long); len(f) > 200 || !strings.HasPrefix(f, "Resultado-AB-AB") || !strings.HasSuffix(f, "-2026-01-02.pdf") ||
		strings.Contains(f, "--") {
		t.Fatalf("filename largo = %q (%d)", f, len(f))
	}
	if f := resultsFilename(results.Study{StudyType: "¿?"}); f != "Resultado-Estudio-sin-fecha.pdf" {
		t.Fatalf("filename vacío = %q", f)
	}
	if c := resultsCaption("Juan", strings.Repeat("X", 2000)); utf8.RuneCountInString(c) > 1024 || !strings.HasSuffix(c, "*.") {
		t.Fatalf("caption largo: %d", utf8.RuneCountInString(c))
	}
}

func TestResults_SendFailuresFallBackToPortal(t *testing.T) {
	cases := []struct {
		name   string
		dlErr  error
		docErr error
		reason string
	}{
		{"archivo inválido", results.ErrInvalidFile, nil, "invalid_file"},
		{"PDF muy grande", results.ErrTooLarge, nil, "pdf_too_large"},
		{"API caída", errors.New("results: la API respondió 503"), nil, "api_down"},
		{"tipo no soportado", nil, nectar.ErrUnsupportedMedia, "unsupported_media"},
		{"adjunto muy grande", nil, nectar.ErrTooLarge, "attachment_too_large"},
		{"adjunto no disponible", nil, nectar.ErrAttachmentUnavailable, "attachment_unavailable"},
		{"conversación de Bird", nil, bird.ErrDocumentNotSupported, "send_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newResultsRig()
			rig.searcher.dlErr = tc.dlErr
			rig.sender.docErr = tc.docErr
			r, sess := verifyWith(t, rig, []results.Study{studyOn("JUAN PEREZ", "EMG", "a.pdf", 5)})
			if got := texts(r); len(got) != 1 || got[0] != resultsFallbackWant(rig.cfg) {
				t.Fatalf("texto = %q", got)
			}
			if fb, _ := eventData(r, "results_fallback_link"); fb["reason"] != tc.reason {
				t.Fatalf("results_fallback_link = %v, quiero %s", fb, tc.reason)
			}
			if _, ok := eventData(r, "results_sent"); ok {
				t.Fatal("results_sent en una falla")
			}
			if r.NextState != sm.StateTerminated {
				t.Fatalf("estado = %s", r.NextState)
			}
			for _, k := range resultsFlowKeys {
				if _, ok := sess.Context[k]; ok {
					t.Fatalf("%s sigue en el contexto", k)
				}
			}
		})
	}
}

func TestResults_SendRespectsBudget(t *testing.T) {
	for name, tweak := range map[string]func(*resultsRig){
		"descarga colgada": func(r *resultsRig) { r.searcher.block = true },
		"envío colgado":    func(r *resultsRig) { r.sender.docWait = true },
	} {
		t.Run(name, func(t *testing.T) {
			rig := newResultsRig()
			tweak(rig)
			rig.m.Register(sm.StateResultsSend, resultsSendHandler(rig.cfg, rig.searcher, rig.sender, 50*time.Millisecond))
			start := time.Now()
			r, _ := verifyWith(t, rig, []results.Study{studyOn("JUAN PEREZ", "EMG", "a.pdf", 5)})
			if el := time.Since(start); el > 5*time.Second {
				t.Fatalf("tardó %s: el presupuesto no cortó", el)
			}
			if fb, _ := eventData(r, "results_fallback_link"); fb["reason"] != "timeout" {
				t.Fatalf("results_fallback_link = %v", fb)
			}
			if got := texts(r); len(got) != 1 || got[0] != resultsFallbackWant(rig.cfg) {
				t.Fatalf("texto = %q", got)
			}
		})
	}
	if resultsSendBudget != 45*time.Second {
		t.Fatalf("resultsSendBudget = %s", resultsSendBudget)
	}
}

func TestResults_ContextCleared(t *testing.T) {
	rig := newResultsRig()
	_, sess := verifyWith(t, rig, []results.Study{studyOn("JUAN PEREZ", "EMG", "a.pdf", 5)})
	if len(rig.sender.docs) != 1 {
		t.Fatal("no se envió")
	}
	for _, k := range []string{
		ctxResultsDoc, ctxResultsDocHash, ctxResultsDocType, ctxResultsStudies,
		ctxResultsFirstName, ctxResultsFirstSurname, ctxResultsChoice,
	} {
		if _, ok := sess.Context[k]; ok {
			t.Errorf("%s sigue en el contexto", k)
		}
	}
}

// Defensa: sin el documento en el contexto (tipo, número o hash) ni la fecha ni la consulta
// avanzan; se vuelve al tipo sin gastar intento ni llamar a SIESA ni a la API.
func TestResults_MissingDocGoesBackToDocType(t *testing.T) {
	for _, k := range []string{ctxResultsDocType, ctxResultsDoc, ctxResultsDocHash} {
		rig := newResultsRig()
		sess := birthDateSess()
		delete(sess.Context, k)
		r := step(t, rig.m, sess, textM(resultsTestDate))
		if r.NextState != sm.StateResultsAskDocType || !slices.Contains(texts(r), docTypeMenuText()) {
			t.Fatalf("sin %s (fecha): %s %q", k, r.NextState, texts(r))
		}
		if len(rig.calls.log) != 0 || len(rig.searcher.docs) != 0 {
			t.Fatalf("sin %s (fecha): llamadas=%v consultas=%v", k, rig.calls.log, rig.searcher.docs)
		}

		lookup := birthDateSess()
		lookup.CurrentState = sm.StateResultsLookup
		delete(lookup.Context, k)
		r, err := resultsLookupHandler(rig.cfg, rig.searcher)(context.Background(), lookup, bird.InboundMessage{})
		if err != nil || r.NextState != sm.StateResultsAskDocType || len(rig.searcher.docs) != 0 {
			t.Fatalf("sin %s (consulta): %v %v consultas=%v", k, r, err, rig.searcher.docs)
		}
	}
}

func TestResults_ListReturnMenu(t *testing.T) {
	newResultsRig()
	var menu bool
	for _, o := range sm.ReturnOptionsFor(nil, sm.StateResultsList) {
		if o.Value == "results_menu" && o.Target == sm.StateMainMenu && slices.Contains(o.Clear, ctxResultsStudies) {
			menu = true
		}
	}
	if !menu {
		t.Fatal("RESULTS_LIST sin «Volver al menú»")
	}
}
