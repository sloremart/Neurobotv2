package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// D-04: respuesta al recordatorio pendiente de SIESA.

const siesaTestPhone = "+573001234567"

var errSiesaDown = errors.New("siesa: connection refused")

// sentBird es un Bird de prueba que anota los cuerpos de cada POST y satisface la escalación.
type sentBird struct {
	mu     sync.Mutex
	bodies []string
}

func (b *sentBird) start(t *testing.T) *bird.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost {
			b.mu.Lock()
			b.bodies = append(b.bodies, string(raw))
			b.mu.Unlock()
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/agents"):
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","teams":[{"id":"team-fallback"}],"availability":{"status":"active"},"rootItemAssignedCount":0}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-1","feedId":"channel:ch-test","closed":false}]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return bird.NewClientForTest(srv.URL)
}

// count dice cuántos POST llevaron el texto (comparado ya codificado en JSON, como viaja).
func (b *sentBird) count(text string) int {
	enc, _ := json.Marshal(text)
	needle := strings.Trim(string(enc), `"`)
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, body := range b.bodies {
		if strings.Contains(body, needle) {
			n++
		}
	}
	return n
}

// memSiesaStore es el SiesaPendingStore en memoria.
type memSiesaStore struct {
	mu        sync.Mutex
	rows      map[int64]*SiesaPendingRow
	outcomes  map[int64]string
	statuses  map[int64]string
	nextID    int64
	finishErr error // simula la BD caída al cerrar la fila
	markErr   error
	marked    map[string]int64 // teléfono+cita → fila: como el repo real, volver a marcar reusa la fila y su status
}

func newMemSiesaStore() *memSiesaStore {
	return &memSiesaStore{
		rows: map[int64]*SiesaPendingRow{}, outcomes: map[int64]string{}, statuses: map[int64]string{},
		marked: map[string]int64{},
	}
}

func (s *memSiesaStore) MarkSiesaPending(_ context.Context, row SiesaPendingRow, status string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return false, s.markErr
	}
	key := row.Phone + "|" + row.AppointmentID
	if id, ok := s.marked[key]; ok {
		row.ID = id
		s.rows[id] = &row
		delete(s.outcomes, id)
		return s.statuses[id] == status, nil
	}
	s.nextID++
	row.ID = s.nextID
	s.marked[key] = row.ID
	s.rows[row.ID] = &row
	s.statuses[row.ID] = status
	return false, nil
}

func (s *memSiesaStore) FindSiesaPending(_ context.Context, limit int) ([]SiesaPendingRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SiesaPendingRow
	for id := int64(1); id <= s.nextID && len(out) < limit; id++ {
		if r, ok := s.rows[id]; ok {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *memSiesaStore) RecordSiesaAttempt(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		r.Attempts++
	}
	return nil
}

func (s *memSiesaStore) FinishSiesaPending(_ context.Context, id int64, outcome string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finishErr != nil {
		return false, s.finishErr
	}
	if _, ok := s.rows[id]; !ok {
		return false, nil
	}
	delete(s.rows, id)
	s.outcomes[id] = outcome
	return true, nil
}

func (s *memSiesaStore) pending() []SiesaPendingRow {
	rows, _ := s.FindSiesaPending(context.Background(), 1000)
	return rows
}

// siesaFake es la SIESA de prueba: la lectura y las escrituras fallan mientras down sea true.
type siesaFake struct {
	mu        sync.Mutex
	readDown  bool
	writeDown bool
	appt      domain.Appointment
	confirms  int
	cancels   int
}

func (f *siesaFake) repo() *mockApptRepoNotif {
	return &mockApptRepoNotif{
		findByIDFn: func(_ context.Context, id string) (*domain.Appointment, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.readDown {
				return nil, errSiesaDown
			}
			a := f.appt
			a.ID = id
			return &a, nil
		},
		confirmFn: func(context.Context, string, string, string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writeDown {
				return errSiesaDown
			}
			f.confirms++
			f.appt.Confirmed = true
			return nil
		},
		cancelFn: func(context.Context, string, string, string, string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writeDown {
				return errSiesaDown
			}
			f.cancels++
			f.appt.Canceled = true
			return nil
		},
	}
}

func (f *siesaFake) set(fn func(f *siesaFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// futureAppt es una cita de mañana a las 10:00 (lejos del tope de 2 h).
func futureAppt() domain.Appointment {
	a := sampleAppt()
	tomorrow := time.Now().AddDate(0, 0, 1)
	a.Date = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, time.Local)
	a.TimeSlot = tomorrow.Format("20060102") + "1000"
	return a
}

type siesaHarness struct {
	mgr     *NotificationManager
	bird    *sentBird
	store   *memSiesaStore
	siesa   *siesaFake
	tracker *mockEventLoggerNotif
	sess    *mockSessionCreator
	wp      *mockVirtualEnqueuer
	bridge  *escalationBridge // nil = Bird
	phone   string            // clave del pending: teléfono (Bird) o "nectar:<id>"
	conv    string
}

func newSiesaHarness(t *testing.T) *siesaHarness {
	t.Helper()
	return buildSiesaHarness(t, false)
}

// newNectarSiesaHarness es el arnés con el puente de Néctar: la clave es "nectar:777".
func newNectarSiesaHarness(t *testing.T) *siesaHarness {
	t.Helper()
	return buildSiesaHarness(t, true)
}

func buildSiesaHarness(t *testing.T, nectarBridge bool) *siesaHarness {
	t.Helper()
	h := &siesaHarness{
		bird: &sentBird{}, store: newMemSiesaStore(), siesa: &siesaFake{appt: futureAppt()}, tracker: &mockEventLoggerNotif{},
		sess: &mockSessionCreator{}, wp: &mockVirtualEnqueuer{}, phone: siesaTestPhone, conv: "conv-1",
	}
	client := h.bird.start(t)
	if nectarBridge {
		h.bridge = &escalationBridge{}
		client.SetNectar(h.bridge)
		h.phone, h.conv = nectarPhone, "777"
	}
	h.mgr = NewNotificationManager(client, services.NewAppointmentService(h.siesa.repo(), nil), &config.Config{BirdTeamFallback: "team-fallback"})
	h.mgr.SetSiesaPendingStore(h.store)
	h.mgr.SetTracker(h.tracker)
	h.mgr.SetWaitingListDeps(nil, h.sess, h.wp)
	return h
}

// respond registra el recordatorio y entrega la respuesta del paciente.
func (h *siesaHarness) respond(payload string) {
	h.mgr.RegisterPending(PendingNotification{
		Type: "confirmation", Phone: h.phone, AppointmentID: "APT001", AppointmentSlot: futureAppt().TimeSlot,
	})
	h.mgr.HandleResponse(h.phone, payload, h.conv)
}

func (h *siesaHarness) pass(now time.Time) {
	h.mgr.retrySiesaPending(context.Background(), now)
}

func (h *siesaHarness) escalations() int {
	h.tracker.mu.Lock()
	defer h.tracker.mu.Unlock()
	n := 0
	for _, e := range h.tracker.events {
		if e == "notification_escalated_agent" {
			n++
		}
	}
	return n
}

func (h *siesaHarness) onlyPending(t *testing.T, action string) SiesaPendingRow {
	t.Helper()
	rows := h.store.pending()
	if len(rows) != 1 {
		t.Fatalf("esperaba 1 respuesta pendiente de SIESA, hay %d", len(rows))
	}
	r := rows[0]
	if r.Action != action || r.Phone != h.phone || r.AppointmentID != "APT001" || r.ConversationID != h.conv ||
		r.Type != "confirmation" || r.AppointmentSlot != futureAppt().TimeSlot {
		t.Fatalf("fila pendiente = %+v", r)
	}
	return r
}

const confirmedDetailsPrefix = "Tu cita ha sido confirmada!"

// (a) La lectura de la cita falla: la respuesta queda pendiente y el paciente recibe el aviso exacto.
func TestSiesaPending_ConfirmReadFails_MarksPendingAndTellsPatient(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })

	h.respond("confirm")

	h.onlyPending(t, siesaActionConfirm)
	if got := h.bird.count(siesaConfirmDeferredText); got != 1 {
		t.Fatalf("aviso de confirmación diferida enviado %d veces, want 1", got)
	}
	if siesaConfirmDeferredText != "Recibimos tu confirmación. La registraremos en unos minutos." {
		t.Fatalf("texto = %q", siesaConfirmDeferredText)
	}
	if n := h.escalations(); n != 0 {
		t.Fatalf("no debía escalar, escaló %d veces", n)
	}
	if h.bird.count(confirmedDetailsPrefix) != 0 {
		t.Fatal("no se puede decir «confirmada» sin SIESA")
	}
}

// (b) La escritura (ConfirmBlock) falla: lo mismo, sin el escalado de antes.
func TestSiesaPending_ConfirmWriteFails_MarksPendingAndTellsPatient(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.writeDown = true })

	h.respond("confirm")

	h.onlyPending(t, siesaActionConfirm)
	if got := h.bird.count(siesaConfirmDeferredText); got != 1 {
		t.Fatalf("aviso enviado %d veces, want 1", got)
	}
	if h.bird.count("Tuvimos un inconveniente al confirmar tu cita") != 0 {
		t.Fatal("ya no se manda el texto del escalado")
	}
	if n := h.escalations(); n != 0 {
		t.Fatalf("no debía escalar, escaló %d veces", n)
	}
}

// Sin dónde guardar la respuesta (la BD local también falla) no se promete nada: se escala como antes.
func TestSiesaPending_MarkFails_FallsBackToEscalation(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.writeDown = true })
	h.store.markErr = errors.New("mysql down")

	h.respond("confirm")

	if h.bird.count(siesaConfirmDeferredText) != 0 {
		t.Fatal("sin la marca guardada no se puede prometer registrarla")
	}
	if n := h.escalations(); n != 1 {
		t.Fatalf("escalaciones = %d, want 1", n)
	}
}

// (c) SIESA vuelve: se aplica, sale el mensaje de confirmación de siempre UNA vez y la fila queda aplicada.
func TestSiesaPending_SiesaBack_AppliesAndSendsConfirmationOnce(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)

	// Primera pasada con SIESA aún caída: suma un intento y no manda nada.
	h.pass(time.Now())
	if rows := h.store.pending(); len(rows) != 1 || rows[0].Attempts != 1 {
		t.Fatalf("con SIESA caída la fila sigue pendiente con 1 intento: %+v", rows)
	}
	if h.bird.count(confirmedDetailsPrefix) != 0 || h.siesa.confirms != 0 {
		t.Fatal("con SIESA caída no se confirma ni se avisa")
	}

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	h.pass(time.Now())

	if h.siesa.confirms != 1 {
		t.Fatalf("ConfirmBlock = %d, want 1", h.siesa.confirms)
	}
	if got := h.bird.count(confirmedDetailsPrefix); got != 1 {
		t.Fatalf("mensaje de confirmación enviado %d veces, want 1", got)
	}
	if h.store.outcomes[row.ID] != siesaOutcomeApplied {
		t.Fatalf("desenlace = %q, want applied", h.store.outcomes[row.ID])
	}
	if n := h.escalations(); n != 0 {
		t.Fatalf("no debía escalar, escaló %d veces", n)
	}
}

// El mensaje diferido es EL MISMO que el inmediato (misma construcción).
func TestSiesaPending_DeferredMessageMatchesImmediate(t *testing.T) {
	immediate := newSiesaHarness(t)
	immediate.respond("confirm")

	deferred := newSiesaHarness(t)
	deferred.siesa.set(func(f *siesaFake) { f.readDown = true })
	deferred.respond("confirm")
	deferred.siesa.set(func(f *siesaFake) { f.readDown = false })
	deferred.pass(time.Now())

	a := futureAppt()
	a.ID = "APT001"
	want := immediate.mgr.buildConfirmationMessage(context.Background(), &a, []domain.Appointment{a})
	if !strings.HasPrefix(want, confirmedDetailsPrefix) {
		t.Fatalf("mensaje inesperado: %q", want)
	}
	if immediate.bird.count(want) != 1 || deferred.bird.count(want) != 1 {
		t.Fatalf("inmediato=%d diferido=%d, want 1 y 1", immediate.bird.count(want), deferred.bird.count(want))
	}
}

// (d) La cita cambió (cancelada) antes de aplicar: no se toca, la fila se cierra y se escala una vez.
func TestSiesaPending_AppointmentChanged_NotAppliedAndEscalated(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)

	h.siesa.set(func(f *siesaFake) { f.readDown = false; f.appt.Canceled = true })
	h.pass(time.Now())
	h.pass(time.Now())

	if h.siesa.confirms != 0 {
		t.Fatal("una cita cancelada no se confirma")
	}
	if h.store.outcomes[row.ID] != siesaOutcomeNotApplicable {
		t.Fatalf("desenlace = %q, want not_applicable", h.store.outcomes[row.ID])
	}
	if n := h.escalations(); n != 1 {
		t.Fatalf("escalaciones = %d, want 1", n)
	}
	if h.bird.count(confirmedDetailsPrefix) != 0 {
		t.Fatal("no se dice «confirmada»")
	}
}

// La cita ya está como pidió el paciente (la confirmó otro, o una pasada anterior no pudo cerrar la
// fila tras enviar): se cierra sin volver a escribir ni a mandar nada.
func TestSiesaPending_AlreadyConfirmed_ClosesWithoutSending(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)

	h.siesa.set(func(f *siesaFake) { f.readDown = false; f.appt.Confirmed = true })
	h.pass(time.Now())

	if h.siesa.confirms != 0 || h.bird.count(confirmedDetailsPrefix) != 0 || h.escalations() != 0 {
		t.Fatalf("confirms=%d envíos=%d escalaciones=%d, want 0/0/0", h.siesa.confirms, h.bird.count(confirmedDetailsPrefix), h.escalations())
	}
	if h.store.outcomes[row.ID] != siesaOutcomeAlreadyApplied {
		t.Fatalf("desenlace = %q, want already_applied", h.store.outcomes[row.ID])
	}
}

// (e) Más de 6 h pendiente: se rinde y escala UNA sola vez.
func TestSiesaPending_MaxAge_GivesUpAndEscalatesOnce(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)
	h.store.rows[row.ID].PendingFor = siesaPendingMaxAge + time.Minute

	h.pass(time.Now())
	h.pass(time.Now())

	if n := h.escalations(); n != 1 {
		t.Fatalf("escalaciones = %d, want 1", n)
	}
	if h.store.outcomes[row.ID] != siesaOutcomeGaveUp {
		t.Fatalf("desenlace = %q, want gave_up", h.store.outcomes[row.ID])
	}
	if siesaPendingMaxAge != 6*time.Hour || siesaPendingMinLead != 2*time.Hour {
		t.Fatalf("topes = %v / %v, want 6h / 2h", siesaPendingMaxAge, siesaPendingMinLead)
	}
}

// (e') La cita es dentro de menos de 2 h: se rinde y escala, sin tocar SIESA.
func TestSiesaPending_AppointmentSoon_GivesUpAndEscalates(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	slot, err := time.ParseInLocation("200601021504", h.siesa.appt.TimeSlot, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	h.pass(slot.Add(-90 * time.Minute))

	if h.siesa.confirms != 0 {
		t.Fatal("a menos de 2 h no se aplica")
	}
	if h.store.outcomes[row.ID] != siesaOutcomeGaveUp || h.escalations() != 1 {
		t.Fatalf("desenlace=%q escalaciones=%d", h.store.outcomes[row.ID], h.escalations())
	}
}

// (f) Cancelar con SIESA caída: queda pendiente con su aviso literal; al volver SIESA NO se cancela:
// se abre la misma confirmación de la cancelación que al pulsar «Cancelar», una sola vez.
func TestSiesaPending_CancelWithSiesaDown_PromptsConfirmationWithoutCancelling(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })

	h.respond("cancelar")

	row := h.onlyPending(t, siesaActionCancel)
	const want = "Recibimos tu solicitud de cancelación. Te escribiremos en unos minutos para confirmarla."
	if siesaCancelDeferredText != want {
		t.Fatalf("texto = %q, want %q", siesaCancelDeferredText, want)
	}
	if got := h.bird.count(want); got != 1 {
		t.Fatalf("aviso de cancelación diferida enviado %d veces, want 1", got)
	}
	if h.bird.count("No pudimos encontrar tu cita") != 0 {
		t.Fatal("ya no se manda «No pudimos encontrar tu cita» con SIESA caída")
	}
	if h.sess.createdSession != nil {
		t.Fatal("sin la cita no se abre la sesión de cancelación")
	}

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	h.pass(time.Now())
	h.pass(time.Now())

	if h.siesa.cancels != 0 {
		t.Fatalf("el proceso de fondo no cancela: CancelBatch = %d", h.siesa.cancels)
	}
	if s := h.sess.createdSession; s == nil || s.CurrentState != sm.StateConfirmCancelNotif || s.PhoneNumber != siesaTestPhone {
		t.Fatalf("sesión abierta = %+v, want CONFIRM_CANCEL_NOTIF", s)
	}
	if h.sess.batchKVs["notif_appt_id"] != "APT001" {
		t.Fatalf("contexto de la sesión = %v", h.sess.batchKVs)
	}
	if n := len(h.wp.calls); n != 1 {
		t.Fatalf("mensajes virtuales encolados = %d, want 1 (la pregunta de confirmación)", n)
	}
	if h.store.outcomes[row.ID] != siesaOutcomeCancelPrompted || h.escalations() != 0 {
		t.Fatalf("desenlace=%q escalaciones=%d", h.store.outcomes[row.ID], h.escalations())
	}
}

// Con el paciente en otra conversación con el bot no se la interrumpe: se escala una vez.
// busySession es la conversación en curso del paciente (otro paso del bot), sin vencer en ninguno
// de los relojes de los tests (algunos corren la pasada al día siguiente).
func busySession(status string) func(context.Context, string) (*session.Session, error) {
	return func(context.Context, string) (*session.Session, error) {
		return &session.Session{ID: "otra", Status: status, CurrentState: sm.StateMainMenu, ExpiresAt: time.Now().Add(72 * time.Hour)}, nil
	}
}

// untouched comprueba que la conversación en curso del paciente quedó intacta: sin escalar en
// Néctar, sin cambio de estado ni de contexto, sin cerrarla ni abrir otra encima.
func (h *siesaHarness) untouched(t *testing.T) {
	t.Helper()
	h.sess.mu.Lock()
	defer h.sess.mu.Unlock()
	if len(h.sess.statusCalls) != 0 || h.sess.batchSessionID == "otra" || h.sess.completedPhone != "" || h.sess.createdSession != nil {
		t.Fatalf("se tocó la sesión del paciente: estados=%v contexto=%q cerrada=%q creada=%+v",
			h.sess.statusCalls, h.sess.batchSessionID, h.sess.completedPhone, h.sess.createdSession)
	}
	if h.bridge != nil {
		h.bridge.mu.Lock()
		defer h.bridge.mu.Unlock()
		if len(h.bridge.escalated) != 0 {
			t.Fatalf("se escaló la conversación de Néctar: %d", len(h.bridge.escalated))
		}
	}
	if len(h.wp.calls) != 0 {
		t.Fatalf("se encoló un mensaje: %v", h.wp.calls)
	}
}

func (h *siesaHarness) logged(event string) int {
	h.tracker.mu.Lock()
	defer h.tracker.mu.Unlock()
	n := 0
	for _, e := range h.tracker.events {
		if e == event {
			n++
		}
	}
	return n
}

// Néctar, paciente conversando con el bot: la fila espera (sin intento, sin escalar, sin tocar nada).
func TestSiesaPending_Nectar_CancelWithPatientBusy_Waits(t *testing.T) {
	h := newNectarSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("cancelar")
	row := h.onlyPending(t, siesaActionCancel)

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	h.sess.findCurrentFn = busySession(session.StatusActive)
	h.pass(time.Now())
	h.pass(time.Now())

	h.untouched(t)
	if rows := h.store.pending(); len(rows) != 1 || rows[0].Attempts != 0 {
		t.Fatalf("la fila sigue pendiente sin intentos: %+v", rows)
	}
	if _, closed := h.store.outcomes[row.ID]; closed || h.escalations() != 0 || h.siesa.cancels != 0 {
		t.Fatalf("cerrada=%v escalaciones=%d cancelaciones=%d", closed, h.escalations(), h.siesa.cancels)
	}
}

// Néctar: cuando la conversación del paciente termina, la pasada siguiente le pide confirmar la
// cancelación, como siempre.
func TestSiesaPending_Nectar_BusyEnds_PromptsCancel(t *testing.T) {
	h := newNectarSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("cancelar")
	row := h.onlyPending(t, siesaActionCancel)

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	h.sess.findCurrentFn = busySession(session.StatusActive)
	h.pass(time.Now())
	h.untouched(t)

	h.sess.mu.Lock()
	h.sess.findCurrentFn = nil // la conversación terminó
	h.sess.mu.Unlock()
	h.pass(time.Now())

	if s := h.sess.createdSession; s == nil || s.CurrentState != sm.StateConfirmCancelNotif || s.PhoneNumber != nectarPhone {
		t.Fatalf("sesión abierta = %+v, want CONFIRM_CANCEL_NOTIF bajo la clave de Néctar", s)
	}
	if len(h.wp.calls) != 1 || h.store.outcomes[row.ID] != siesaOutcomeCancelPrompted || h.escalations() != 0 {
		t.Fatalf("encoladas=%d desenlace=%q escalaciones=%d", len(h.wp.calls), h.store.outcomes[row.ID], h.escalations())
	}
}

// Néctar: vence el tope de 6 h con el paciente todavía conversando (aquí, con un agente): no se
// escala ni se toca su sesión; la fila sigue esperando y escala UNA vez cuando el paciente queda libre.
func TestSiesaPending_Nectar_CapWithPatientBusy_WaitsThenEscalatesOnce(t *testing.T) {
	h := newNectarSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("cancelar")
	row := h.onlyPending(t, siesaActionCancel)

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	h.sess.findCurrentFn = busySession(session.StatusEscalated)
	h.store.rows[row.ID].PendingFor = siesaPendingMaxAge + time.Minute
	h.pass(time.Now())
	h.pass(time.Now())

	h.untouched(t)
	if _, closed := h.store.outcomes[row.ID]; closed || h.escalations() != 0 || h.logged("notification_siesa_unattended") != 0 {
		t.Fatalf("con la cita por delante se espera: cerrada=%v escalaciones=%d", closed, h.escalations())
	}

	h.sess.mu.Lock()
	h.sess.findCurrentFn = nil // la conversación terminó
	h.sess.mu.Unlock()
	h.pass(time.Now())
	h.pass(time.Now())

	if h.store.outcomes[row.ID] != siesaOutcomeGaveUp || h.escalations() != 1 {
		t.Fatalf("desenlace=%q escalaciones=%d, want gave_up y 1", h.store.outcomes[row.ID], h.escalations())
	}
	h.bridge.only(t)
	if len(h.wp.calls) != 0 {
		t.Fatal("pasado el tope no se pide la confirmación: se escala")
	}
}

// Néctar, tope duro: la cita ya pasó y el paciente nunca quedó libre (confirmación con SIESA caída):
// unattended UNA sola vez, con su evento, sin escalar ni tocar la sesión.
func TestSiesaPending_Nectar_AppointmentPassedWhileBusy_UnattendedOnce(t *testing.T) {
	h := newNectarSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)
	h.store.rows[row.ID].PendingFor = siesaPendingMaxAge + time.Minute
	h.sess.findCurrentFn = busySession(session.StatusActive)

	// Mientras la cita está por delante, espera.
	h.pass(time.Now())
	if _, closed := h.store.outcomes[row.ID]; closed {
		t.Fatal("con la cita por delante la fila sigue esperando")
	}

	slot, err := time.ParseInLocation("200601021504", row.AppointmentSlot, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	h.pass(slot.Add(time.Minute))
	h.pass(slot.Add(2 * time.Minute))

	h.untouched(t)
	if h.store.outcomes[row.ID] != siesaOutcomeUnattended || h.escalations() != 0 || h.logged("notification_siesa_unattended") != 1 {
		t.Fatalf("desenlace=%q escalaciones=%d eventos=%d, want unattended, 0 y 1",
			h.store.outcomes[row.ID], h.escalations(), h.logged("notification_siesa_unattended"))
	}
}

func TestAppointmentPassed(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		name string
		row  SiesaPendingRow
		appt *domain.Appointment
		want bool
	}{
		{"recordada por delante", SiesaPendingRow{AppointmentSlot: "202611051030"}, nil, false},
		{"recordada pasada", SiesaPendingRow{AppointmentSlot: "202611050930"}, nil, true},
		{"sin recordada, releída por delante", SiesaPendingRow{}, &domain.Appointment{TimeSlot: "202611061000"}, false},
		{"la recordada manda sobre la releída", SiesaPendingRow{AppointmentSlot: "202611050930"}, &domain.Appointment{TimeSlot: "202611061000"}, true},
		{"sin hora conocida", SiesaPendingRow{}, nil, true},
	} {
		if got := appointmentPassed(c.row, c.appt, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Néctar, cita cambiada con el paciente conversando: espera; libre ya, escala por el puente.
func TestSiesaPending_Nectar_ChangedWaitsThenEscalates(t *testing.T) {
	h := newNectarSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)

	h.siesa.set(func(f *siesaFake) { f.readDown = false; f.appt.Canceled = true })
	h.sess.findCurrentFn = busySession(session.StatusActive)
	h.pass(time.Now())
	h.untouched(t)
	if _, closed := h.store.outcomes[row.ID]; closed || h.escalations() != 0 {
		t.Fatalf("con el paciente conversando se espera: cerrada=%v escalaciones=%d", closed, h.escalations())
	}

	h.sess.mu.Lock()
	h.sess.findCurrentFn = nil
	h.sess.mu.Unlock()
	h.pass(time.Now())

	if h.store.outcomes[row.ID] != siesaOutcomeNotApplicable || h.escalations() != 1 {
		t.Fatalf("desenlace=%q escalaciones=%d, want not_applicable y 1", h.store.outcomes[row.ID], h.escalations())
	}
	h.bridge.only(t)
}

// Bird: escalar no toca ninguna sesión, así que al vencer el tope con el paciente conversando se
// escala como siempre (antes del tope, espera igual que en Néctar).
func TestSiesaPending_Bird_CapWithPatientBusy_Escalates(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("cancelar")
	row := h.onlyPending(t, siesaActionCancel)

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	h.sess.findCurrentFn = busySession(session.StatusActive)
	h.pass(time.Now())
	if _, closed := h.store.outcomes[row.ID]; closed || h.escalations() != 0 {
		t.Fatalf("antes del tope se espera: cerrada=%v escalaciones=%d", closed, h.escalations())
	}

	h.store.rows[row.ID].PendingFor = siesaPendingMaxAge + time.Minute
	h.pass(time.Now())
	h.pass(time.Now())

	h.untouched(t)
	if h.store.outcomes[row.ID] != siesaOutcomeGaveUp || h.escalations() != 1 {
		t.Fatalf("desenlace=%q escalaciones=%d, want gave_up y 1", h.store.outcomes[row.ID], h.escalations())
	}
}

// Una sesión activa ya vencida no es una conversación en curso: se abre la confirmación igual.
func TestSessionInUse(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		s    *session.Session
		want bool
	}{
		{"sin sesión", nil, false},
		{"activa vigente", &session.Session{Status: session.StatusActive, ExpiresAt: now.Add(time.Minute)}, true},
		{"activa vencida", &session.Session{Status: session.StatusActive, ExpiresAt: now.Add(-time.Minute)}, false},
		{"escalada vencida", &session.Session{Status: session.StatusEscalated, ExpiresAt: now.Add(-time.Hour)}, true},
	} {
		if got := sessionInUse(c.s, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// (g) Dos pasadas seguidas no duplican el envío, ni siquiera si la fila no se pudo cerrar tras enviar.
func TestSiesaPending_TwoPassesDoNotDuplicate(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.writeDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)

	h.siesa.set(func(f *siesaFake) { f.writeDown = false })
	h.store.finishErr = errors.New("mysql down") // se aplica y se envía, pero la fila no se cierra
	h.pass(time.Now())
	if h.siesa.confirms != 1 || h.bird.count(confirmedDetailsPrefix) != 1 {
		t.Fatalf("pasada 1: confirms=%d envíos=%d", h.siesa.confirms, h.bird.count(confirmedDetailsPrefix))
	}

	h.store.finishErr = nil
	h.pass(time.Now())
	h.pass(time.Now())

	if h.siesa.confirms != 1 {
		t.Fatalf("ConfirmBlock = %d, want 1", h.siesa.confirms)
	}
	if got := h.bird.count(confirmedDetailsPrefix); got != 1 {
		t.Fatalf("mensaje de confirmación enviado %d veces, want 1", got)
	}
	if h.store.outcomes[row.ID] != siesaOutcomeAlreadyApplied || h.escalations() != 0 {
		t.Fatalf("desenlace=%q escalaciones=%d", h.store.outcomes[row.ID], h.escalations())
	}
}

// hungSiesaStore cuelga la lista hasta que se cancele su ctx (MySQL que no responde).
type hungSiesaStore struct{ memSiesaStore }

func (h *hungSiesaStore) FindSiesaPending(ctx context.Context, _ int) ([]SiesaPendingRow, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// La pasada termina al vencer su plazo (D-07) con la BD local colgada.
func TestSiesaPending_RunPassHonoursDeadline(t *testing.T) {
	m := &NotificationManager{siesaPending: &hungSiesaStore{}, passTimeout: 20 * time.Millisecond}
	done := make(chan struct{})
	go func() {
		m.runSiesaPendingPass(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("la pasada no terminó al vencer su plazo")
	}
}

// Cada caso de D-04 viaja con su motivo y su caso en la vista de la escalación, que es lo que elige
// el menú del agente (handlers.siesaPendingReturnOptions; aquí un menú que los devuelve tal cual).
func TestSiesaPending_EscalationCarriesCase(t *testing.T) {
	for reason, wantCase := range map[escalationReason]string{
		escalationSiesaConfirm: sm.NotifSiesaCaseConfirm,
		escalationSiesaCancel:  sm.NotifSiesaCaseCancel,
		escalationSiesaChanged: sm.NotifSiesaCaseChanged,
	} {
		mgr, bridge := nectarManager(t, &config.Config{
			BirdTeamFallback: "team-fallback", ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
		})
		sm.RegisterReturnMenu(sm.StateNotifPending, func(sess *session.Session) []sm.ReturnOption {
			label := sess.GetContext(sm.CtxNotifEscalationDetail) + "/" + sess.GetContext(sm.CtxNotifSiesaCase)
			return []sm.ReturnOption{{Label: label, Value: "x", Close: true}}
		})
		mgr.escalateToAgent(&PendingNotification{Phone: nectarPhone, AppointmentID: "APT001", Type: "confirmation", ConversationID: "777"}, reason)
		want := sm.EscalationReasonNotifSiesaPending + "/" + wantCase
		if esc := bridge.only(t); !strings.Contains(string(esc.Input), want) {
			t.Errorf("reason %v: la vista no lleva %q: %s", reason, want, esc.Input)
		}
	}
}

// La cita movida con el mismo id (otra fecha u hora que la recordada) es una cita cambiada: no se
// confirma ni se anuncia la fecha nueva; se escala una vez.
func TestSiesaPending_MovedAppointment_NotConfirmedAndEscalated(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)

	h.siesa.set(func(f *siesaFake) {
		f.readDown = false
		moved := f.appt.Date.AddDate(0, 0, 2)
		f.appt.Date = moved
		f.appt.TimeSlot = moved.Format("20060102") + "1000"
	})
	h.pass(time.Now())
	h.pass(time.Now())

	if h.siesa.confirms != 0 || h.bird.count(confirmedDetailsPrefix) != 0 {
		t.Fatalf("una cita movida no se confirma: confirms=%d envíos=%d", h.siesa.confirms, h.bird.count(confirmedDetailsPrefix))
	}
	if h.store.outcomes[row.ID] != siesaOutcomeNotApplicable || h.escalations() != 1 {
		t.Fatalf("desenlace=%q escalaciones=%d, want not_applicable y 1", h.store.outcomes[row.ID], h.escalations())
	}
}

// Una fila sin fecha y hora guardadas (anterior al dato) no compara el horario: se confirma.
func TestSiesaPending_RowWithoutSlot_KeepsBehaviour(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.mgr.RegisterPending(PendingNotification{Type: "confirmation", Phone: siesaTestPhone, AppointmentID: "APT001"})
	h.mgr.HandleResponse(siesaTestPhone, "confirm", "conv-1")

	h.siesa.set(func(f *siesaFake) { f.readDown = false; f.appt.TimeSlot = futureAppt().TimeSlot[:8] + "1130" })
	h.pass(time.Now())

	if h.siesa.confirms != 1 || h.bird.count(confirmedDetailsPrefix) != 1 {
		t.Fatalf("confirms=%d envíos=%d, want 1 y 1", h.siesa.confirms, h.bird.count(confirmedDetailsPrefix))
	}
}

// La escritura falla con el pending sin fecha y hora: la marca guarda las de la cita leída.
func TestSiesaPending_WriteFailure_StoresReadSlot(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.writeDown = true })
	h.mgr.RegisterPending(PendingNotification{Type: "confirmation", Phone: siesaTestPhone, AppointmentID: "APT001"})
	h.mgr.HandleResponse(siesaTestPhone, "confirm", "conv-1")

	if rows := h.store.pending(); len(rows) != 1 || rows[0].AppointmentSlot != futureAppt().TimeSlot {
		t.Fatalf("fila = %+v, want la fecha y hora de la cita leída", rows)
	}
}

// Si la fila no se pudo cerrar tras abrir la pregunta de cancelación, la pasada siguiente reconoce
// esa sesión como propia: ni escalación falsa ni una segunda sesión.
func TestSiesaPending_CancelPromptFinishFails_NoFalseEscalation(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("cancelar")
	row := h.onlyPending(t, siesaActionCancel)

	h.siesa.set(func(f *siesaFake) { f.readDown = false })
	h.store.finishErr = errors.New("mysql down")
	h.pass(time.Now())
	opened := h.sess.createdSession
	if opened == nil || len(h.wp.calls) != 1 {
		t.Fatalf("pasada 1: sesión=%+v encoladas=%d", opened, len(h.wp.calls))
	}

	// La sesión abierta ocupa el teléfono (la lee FindCurrentByPhone, bajo el candado del mock).
	h.sess.findCurrentFn = func(context.Context, string) (*session.Session, error) { return opened, nil }
	h.store.finishErr = nil
	h.pass(time.Now())
	h.pass(time.Now())

	if len(h.wp.calls) != 1 || h.sess.createdSession != opened {
		t.Fatalf("se abrió otra pregunta: encoladas=%d", len(h.wp.calls))
	}
	if h.store.outcomes[row.ID] != siesaOutcomeCancelPrompted || h.escalations() != 0 {
		t.Fatalf("desenlace=%q escalaciones=%d, want cancel_prompted y 0", h.store.outcomes[row.ID], h.escalations())
	}
}

// Cancelar una cita que ya estaba confirmada pide la confirmación, como con SIESA arriba.
func TestSiesaPending_CancelConfirmedAppointment_Prompts(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("cancelar")
	row := h.onlyPending(t, siesaActionCancel)

	h.siesa.set(func(f *siesaFake) { f.readDown = false; f.appt.Confirmed = true })
	h.pass(time.Now())

	if h.store.outcomes[row.ID] != siesaOutcomeCancelPrompted || h.escalations() != 0 || len(h.wp.calls) != 1 {
		t.Fatalf("desenlace=%q escalaciones=%d encoladas=%d", h.store.outcomes[row.ID], h.escalations(), len(h.wp.calls))
	}
}

// Pasado el tope de 6 h, una cita que ya quedó como pidió el paciente se cierra en silencio.
func TestSiesaPending_MaxAgeAlreadyApplied_ClosesSilently(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	row := h.onlyPending(t, siesaActionConfirm)
	h.store.rows[row.ID].PendingFor = siesaPendingMaxAge + time.Minute

	h.siesa.set(func(f *siesaFake) { f.readDown = false; f.appt.Confirmed = true })
	h.pass(time.Now())

	if h.store.outcomes[row.ID] != siesaOutcomeAlreadyApplied || h.escalations() != 0 {
		t.Fatalf("desenlace=%q escalaciones=%d, want already_applied y 0", h.store.outcomes[row.ID], h.escalations())
	}
}

// f5: el agente reintenta desde NOTIF_PENDING con SIESA todavía caída. La respuesta vuelve a quedar
// pendiente, pero el paciente, que ya recibió el acuse, no lo recibe otra vez: se le avisa al agente
// con una nota interna.
func TestSiesaPending_AgentRetryStillDown_NoSecondAckToPatient(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("confirm")
	if got := h.bird.count(siesaConfirmDeferredText); got != 1 {
		t.Fatalf("primer acuse enviado %d veces, want 1", got)
	}

	h.mgr.HandleNotifPendingCommand(h.phone, "confirm", h.conv, "APT001", "confirmation", "")

	if got := h.bird.count(siesaConfirmDeferredText); got != 1 {
		t.Fatalf("el reintento del agente repitió el acuse al paciente: %d envíos", got)
	}
	if got := h.bird.count(siesaStillDownAgentNote); got != 1 {
		t.Fatalf("la nota al agente salió %d veces, want 1", got)
	}
	if rows := h.store.pending(); len(rows) != 1 || rows[0].Action != siesaActionConfirm {
		t.Fatalf("el reintento sigue pendiente en la misma fila: %+v", rows)
	}
}

// M-2: si el agente reintenta OTRA acción, el acuse que el paciente recibió no era de esta: sí se le
// avisa. Pendiente «Cancelar» y el agente reintenta «Confirmar» con SIESA caída.
func TestSiesaPending_AgentRetryOtherAction_AcksPatient(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })
	h.respond("cancel")
	if got := h.bird.count(siesaCancelDeferredText); got != 1 {
		t.Fatalf("acuse de la cancelación enviado %d veces, want 1", got)
	}

	h.mgr.HandleNotifPendingCommand(h.phone, "confirm", h.conv, "APT001", "confirmation", "")

	if got := h.bird.count(siesaConfirmDeferredText); got != 1 {
		t.Fatalf("el paciente debe oír la confirmación: %d envíos", got)
	}
	if got := h.bird.count(siesaStillDownAgentNote); got != 0 {
		t.Fatalf("no es un reintento de lo mismo: %d notas", got)
	}
}
