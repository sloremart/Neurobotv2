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
}

func newMemSiesaStore() *memSiesaStore {
	return &memSiesaStore{rows: map[int64]*SiesaPendingRow{}, outcomes: map[int64]string{}, statuses: map[int64]string{}}
}

func (s *memSiesaStore) MarkSiesaPending(_ context.Context, row SiesaPendingRow, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return s.markErr
	}
	s.nextID++
	row.ID = s.nextID
	s.rows[row.ID] = &row
	s.statuses[row.ID] = status
	return nil
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
	mu         sync.Mutex
	readDown   bool
	writeDown  bool
	appt       domain.Appointment
	confirms   int
	cancels    int
	cancelArgs []string
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
		cancelFn: func(_ context.Context, id, reason, _, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writeDown {
				return errSiesaDown
			}
			f.cancels++
			f.cancelArgs = append(f.cancelArgs, id+"|"+reason)
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
}

func newSiesaHarness(t *testing.T) *siesaHarness {
	t.Helper()
	h := &siesaHarness{bird: &sentBird{}, store: newMemSiesaStore(), siesa: &siesaFake{appt: futureAppt()}, tracker: &mockEventLoggerNotif{}, sess: &mockSessionCreator{}}
	h.mgr = NewNotificationManager(h.bird.start(t), services.NewAppointmentService(h.siesa.repo(), nil), &config.Config{BirdTeamFallback: "team-fallback"})
	h.mgr.SetSiesaPendingStore(h.store)
	h.mgr.SetTracker(h.tracker)
	h.mgr.SetWaitingListDeps(nil, h.sess, &mockVirtualEnqueuer{})
	return h
}

// respond registra el recordatorio y entrega la respuesta del paciente.
func (h *siesaHarness) respond(payload string) {
	h.mgr.RegisterPending(PendingNotification{Type: "confirmation", Phone: siesaTestPhone, AppointmentID: "APT001"})
	h.mgr.HandleResponse(siesaTestPhone, payload, "conv-1")
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
	if r.Action != action || r.Phone != siesaTestPhone || r.AppointmentID != "APT001" || r.ConversationID != "conv-1" || r.Type != "confirmation" {
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

// (f) Cancelar con SIESA caída: queda pendiente con su aviso; al volver SIESA se cancela y sale el
// mensaje de cancelación de siempre.
func TestSiesaPending_CancelWithSiesaDown(t *testing.T) {
	h := newSiesaHarness(t)
	h.siesa.set(func(f *siesaFake) { f.readDown = true })

	h.respond("cancelar")

	row := h.onlyPending(t, siesaActionCancel)
	if siesaCancelDeferredText != "Recibimos tu solicitud de cancelación. La registraremos en unos minutos." {
		t.Fatalf("texto = %q", siesaCancelDeferredText)
	}
	if got := h.bird.count(siesaCancelDeferredText); got != 1 {
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

	if h.siesa.cancels != 1 || h.siesa.cancelArgs[0] != "APT001|Cancelada por paciente via WhatsApp" {
		t.Fatalf("cancelaciones = %d %v", h.siesa.cancels, h.siesa.cancelArgs)
	}
	if got := h.bird.count(siesaCancelDoneText); got != 1 {
		t.Fatalf("mensaje de cancelación enviado %d veces, want 1", got)
	}
	if h.store.outcomes[row.ID] != siesaOutcomeApplied || h.escalations() != 0 {
		t.Fatalf("desenlace=%q escalaciones=%d", h.store.outcomes[row.ID], h.escalations())
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
