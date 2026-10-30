package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakeurgent"
)

// Los avisos administrativos (confirmaciones de agenda, cancelaciones, reprogramaciones y la
// confirmación desde el dashboard) con el puente de Néctar puesto ABREN la conversación con la
// plantilla: el pending queda bajo "nectar:<id>", con el teléfono del paciente en ContactPhone y el
// id numérico como conversación — sin lookup de Bird.

const adminPhone = "+573001112233"

// safeEvents es un InternalEventLogger seguro para las goroutines de los envíos en segundo plano.
type safeEvents struct {
	mu     sync.Mutex
	events []mockLoggedEvent
}

func (s *safeEvents) LogEvent(_ context.Context, _, phone, eventType string, data map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, mockLoggedEvent{eventType: eventType, phone: phone, data: data})
}

func (s *safeEvents) find(eventType string) (mockLoggedEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.eventType == eventType {
			return e, true
		}
	}
	return mockLoggedEvent{}, false
}

type adminRig struct {
	h        *InternalHandler
	nm       *notifications.NotificationManager
	events   *safeEvents
	birdHits *atomic.Int32
}

// newAdminRig arma el handler; bridge nil = sin puente (todo por Bird).
func newAdminRig(t *testing.T, bridge *fakenectar.Bridge, repo *mockApptRepoAPI) *adminRig {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-bird","results":[]}`))
	}))
	t.Cleanup(srv.Close)
	client := bird.NewClientForTest(srv.URL)
	if bridge != nil {
		client.SetNectar(bridge)
	}
	cfg := &config.Config{
		CenterName:                        "Centro",
		BirdTemplateConfirmProjectID:      "proj-confirm",
		BirdTemplateCancellationProjectID: "proj-cancel",
		BirdTemplateRescheduleProjectID:   "proj-resched",
	}
	nm := notifications.NewNotificationManager(client, nil, cfg)
	t.Cleanup(func() {
		for _, k := range []string{"nectar:77", adminPhone} {
			if p, ok := nm.LoadPendingForTest(k); ok && p.Timer != nil {
				p.Timer.Stop()
			}
		}
	})
	events := &safeEvents{}
	h := &InternalHandler{
		appointmentRepo: repo, birdClient: client, notifyManager: nm, tracker: events,
		cfg: cfg, startTime: time.Now(),
	}
	return &adminRig{h: h, nm: nm, events: events, birdHits: &hits}
}

func adminAppt(id string) domain.Appointment {
	return domain.Appointment{
		ID: id, PatientID: "P1", PatientName: "Ana", PatientPhone: "3001112233",
		DoctorID: "8", AgendaID: 704, Date: time.Now().AddDate(0, 0, 2), TimeSlot: "202607090800",
	}
}

// waitFor espera a que la goroutine del envío en segundo plano deje su rastro.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("no ocurrió a tiempo: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertNectarPending comprueba el pending de Néctar y que Bird no vio nada.
func (r *adminRig) assertNectarPending(t *testing.T, notifType string) {
	t.Helper()
	p, ok := r.nm.LoadPendingForTest("nectar:77")
	if !ok {
		t.Fatal("el pending queda bajo la clave de la sesión de Néctar")
	}
	if p.Type != notifType || p.ContactPhone != adminPhone || p.ConversationID != "77" || p.BirdMessageID != "901" {
		t.Fatalf("pending = %+v", p)
	}
	if r.nm.HasPending(adminPhone) {
		t.Error("no queda un pending bajo el teléfono")
	}
	if n := r.birdHits.Load(); n != 0 {
		t.Fatalf("Bird no recibe nada (ni plantilla ni lookup): %d", n)
	}
}

func notifyConfirmationRequest(rig *adminRig) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/internal/appointment/A1/notify-confirmation", nil)
	req.SetPathValue("id", "A1")
	rec := httptest.NewRecorder()
	rig.h.HandleNotifyConfirmation(rec, req)
	return rec
}

func findA1(_ context.Context, id string) (*domain.Appointment, error) {
	a := adminAppt(id)
	return &a, nil
}

func TestNotifyConfirmation_NectarRegistersUnderKey(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77, OpenMsg: "901"}
	rig := newAdminRig(t, bridge, &mockApptRepoAPI{findByIDFn: findA1})

	if rec := notifyConfirmationRequest(rig); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := bridge.Opens(); len(got) != 1 || got[0] != adminPhone {
		t.Fatalf("se abre al teléfono del paciente: %v", got)
	}
	rig.assertNectarPending(t, "confirmation")
}

// Sin puente, el pending sigue bajo el teléfono y la conversación sale del lookup de Bird.
func TestNotifyConfirmation_BirdUnchanged(t *testing.T) {
	rig := newAdminRig(t, nil, &mockApptRepoAPI{findByIDFn: findA1})
	if rec := notifyConfirmationRequest(rig); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p, ok := rig.nm.LoadPendingForTest(adminPhone)
	if !ok || p.ContactPhone != "" || p.BirdMessageID != "msg-bird" {
		t.Fatalf("pending bajo el teléfono: %+v ok=%v", p, ok)
	}
	if rig.birdHits.Load() < 2 {
		t.Fatalf("plantilla y lookup van a Bird como siempre: %d peticiones", rig.birdHits.Load())
	}
}

// El paciente ya conversa con un agente en Néctar: 409, evento y ningún pending.
func TestNotifyConfirmation_AgentOwnedIs409(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenErr: &bird.AlreadyOpenError{ConversationID: 55, Mine: false}}
	rig := newAdminRig(t, bridge, &mockApptRepoAPI{findByIDFn: findA1})

	if rec := notifyConfirmationRequest(rig); rec.Code != http.StatusConflict {
		t.Fatalf("status %d, esperaba 409: %s", rec.Code, rec.Body.String())
	}
	ev, ok := rig.events.find("notification_skipped_agent_owned")
	if !ok || ev.data["appointment_id"] != "A1" || ev.data["type"] != "confirmation" {
		t.Fatalf("evento del salto: %+v", rig.events.events)
	}
	if rig.nm.PendingCount() != 0 {
		t.Fatal("sin pending")
	}
}

func TestCancelAppointment_NectarNotifiesUnderKey(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77, OpenMsg: "901"}
	repo := &mockApptRepoAPI{
		findByIDFn:         findA1,
		cancelBatchBlockFn: func(context.Context, []string, string, string, string) error { return nil },
	}
	rig := newAdminRig(t, bridge, repo)
	req := httptest.NewRequest("POST", "/api/internal/appointment/A1/cancel",
		bytes.NewBufferString(`{"notify_patient":true,"release_slots":false}`))
	req.SetPathValue("id", "A1")
	rec := httptest.NewRecorder()
	rig.h.HandleCancelAppointment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	rig.assertNectarPending(t, "cancellation")
}

func TestSendAgendaConfirmations_NectarRegistersUnderKey(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77, OpenMsg: "901"}
	repo := &mockApptRepoAPI{
		findByAgendaAndDateFn: func(context.Context, int, string) ([]domain.Appointment, error) {
			return []domain.Appointment{adminAppt("A1")}, nil
		},
	}
	rig := newAdminRig(t, bridge, repo)
	req := httptest.NewRequest("POST", "/api/internal/agenda-confirmations",
		bytes.NewBufferString(`{"agenda_id":704,"date":"2026-11-02"}`))
	rig.h.HandleSendAgendaConfirmations(httptest.NewRecorder(), req)

	waitFor(t, "notification_sent", func() bool { _, ok := rig.events.find("notification_sent"); return ok })
	rig.assertNectarPending(t, "confirmation")
	if ev, _ := rig.events.find("notification_sent"); ev.phone != "nectar:77" || ev.data["conversation_id"] != "77" {
		t.Fatalf("el evento va con la clave de la sesión: %+v", ev)
	}
}

func TestSendAgendaConfirmations_AgentOwnedSkipped(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenErr: &bird.AlreadyOpenError{ConversationID: 55, Mine: false}}
	repo := &mockApptRepoAPI{
		findByAgendaAndDateFn: func(context.Context, int, string) ([]domain.Appointment, error) {
			return []domain.Appointment{adminAppt("A1")}, nil
		},
	}
	rig := newAdminRig(t, bridge, repo)
	req := httptest.NewRequest("POST", "/api/internal/agenda-confirmations",
		bytes.NewBufferString(`{"agenda_id":704,"date":"2026-11-02"}`))
	rig.h.HandleSendAgendaConfirmations(httptest.NewRecorder(), req)

	waitFor(t, "notification_skipped_agent_owned", func() bool {
		_, ok := rig.events.find("notification_skipped_agent_owned")
		return ok
	})
	if rig.nm.PendingCount() != 0 {
		t.Fatal("sin pending")
	}
	if _, sent := rig.events.find("notification_sent"); sent {
		t.Fatal("no hubo envío")
	}
}

func TestCancelAgenda_NectarNotifiesUnderKey(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77, OpenMsg: "901"}
	repo := &mockApptRepoAPI{
		findByAgendaAndDateFn: func(context.Context, int, string) ([]domain.Appointment, error) {
			return []domain.Appointment{adminAppt("A1")}, nil
		},
		cancelBatchFn: func(context.Context, []string, string, string, string) error { return nil },
	}
	rig := newAdminRig(t, bridge, repo)
	req := httptest.NewRequest("POST", "/api/internal/cancel-agenda",
		bytes.NewBufferString(`{"agenda_id":704,"date":"2026-11-02","reason":"médico incapacitado","notify_patients":true}`))
	rig.h.HandleCancelAgenda(httptest.NewRecorder(), req)

	waitFor(t, "notification_sent", func() bool { _, ok := rig.events.find("notification_sent"); return ok })
	rig.assertNectarPending(t, "cancellation")
}

func TestRescheduleNotifications_NectarRegistersUnderKey(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77, OpenMsg: "901"}
	rig := newAdminRig(t, bridge, &mockApptRepoAPI{})
	n := rig.h.sendRescheduleNotifications([]domain.Appointment{adminAppt("A1")}, RescheduleAgendaRequest{
		AgendaID: 704, OldDate: "2026-11-02", NewDate: "2026-11-09", NotifyPatients: true,
	})
	if n != 1 {
		t.Fatalf("a notificar = %d", n)
	}
	waitFor(t, "notification_sent", func() bool { _, ok := rig.events.find("notification_sent"); return ok })
	rig.assertNectarPending(t, "reschedule")
}

// Apertura en duda: la confirmación pudo haber salido. 409 que lo dice (no el 502 genérico, que
// invita a reintentar), evento notification_in_doubt y ningún pending.
func TestNotifyConfirmation_InDoubtIs409NoRetry(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenErr: nectar.ErrOpenInDoubt}
	rig := newAdminRig(t, bridge, &mockApptRepoAPI{findByIDFn: findA1})

	rec := notifyConfirmationRequest(rig)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no reintentar") {
		t.Fatalf("status %d cuerpo %q, esperaba 409 «no reintentar»", rec.Code, rec.Body.String())
	}
	ev, ok := rig.events.find("notification_in_doubt")
	if !ok || ev.data["appointment_id"] != "A1" || ev.data["type"] != "confirmation" {
		t.Fatalf("evento en duda: %+v", rig.events.events)
	}
	if rig.nm.PendingCount() != 0 {
		t.Fatal("sin pending")
	}
	if got := bridge.Opens(); len(got) != 1 {
		t.Fatalf("una sola apertura: %v", got)
	}
}

// adminErrorLog desvía slog a un buffer seguro para goroutines (los lotes envían en segundo plano).
type adminErrorLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *adminErrorLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *adminErrorLog) errors() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.buf.String(), "\n") {
		if strings.Contains(line, "level=ERROR") {
			out = append(out, line)
		}
	}
	return out
}

func captureAdminErrors(t *testing.T) *adminErrorLog {
	t.Helper()
	l := &adminErrorLog{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return l
}

func threePatients() []domain.Appointment {
	out := make([]domain.Appointment, 0, 3)
	for i := 1; i <= 3; i++ {
		a := adminAppt(fmt.Sprintf("A%d", i))
		a.PatientID = fmt.Sprintf("P%d", i)
		a.PatientPhone = fmt.Sprintf("300111220%d", i)
		out = append(out, a)
	}
	return out
}

// Un error de CONFIGURACIÓN de Néctar (la clave rechazada) en un lote administrativo falla igual
// con todos: el lote se detiene en la primera apertura y deja UN solo error con cuántos quedaron.
func TestAdminBatches_ConfigErrorStopsBatch(t *testing.T) {
	agendaRepo := func() *mockApptRepoAPI {
		return &mockApptRepoAPI{
			findByAgendaAndDateFn: func(context.Context, int, string) ([]domain.Appointment, error) {
				return threePatients(), nil
			},
			cancelBatchFn: func(context.Context, []string, string, string, string) error { return nil },
		}
	}
	batches := map[string]func(rig *adminRig){
		"confirmaciones de agenda": func(rig *adminRig) {
			rig.h.HandleSendAgendaConfirmations(httptest.NewRecorder(), httptest.NewRequest("POST",
				"/api/internal/agenda-confirmations", bytes.NewBufferString(`{"agenda_id":704,"date":"2026-11-02"}`)))
		},
		"cancelación de agenda": func(rig *adminRig) {
			rig.h.HandleCancelAgenda(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/internal/cancel-agenda",
				bytes.NewBufferString(`{"agenda_id":704,"date":"2026-11-02","reason":"x","notify_patients":true}`)))
		},
		"reprogramación": func(rig *adminRig) {
			rig.h.sendRescheduleNotifications(threePatients(), RescheduleAgendaRequest{
				AgendaID: 704, OldDate: "2026-11-02", NewDate: "2026-11-09", NotifyPatients: true,
			})
		},
	}
	for name, start := range batches {
		t.Run(name, func(t *testing.T) {
			logs := captureAdminErrors(t)
			bridge := &fakenectar.Bridge{OpenErr: nectar.ErrUnauthorized}
			rig := newAdminRig(t, bridge, agendaRepo())
			urgent := &fakeurgent.Recorder{}
			rig.h.SetUrgentReporter(urgent)
			start(rig)

			waitFor(t, "el error de la corrida", func() bool { return len(logs.errors()) > 0 })
			time.Sleep(50 * time.Millisecond) // por si el lote siguiera: le da tiempo a equivocarse
			if got := bridge.Opens(); len(got) != 1 {
				t.Fatalf("el lote se detiene en la primera apertura: %v", got)
			}
			logged := logs.errors()
			if len(logged) != 1 || !strings.Contains(logged[0], "unsent=3") || !strings.Contains(logged[0], "401") {
				t.Fatalf("un solo error con la clase y cuántos quedaron: %v", logged)
			}
			if rig.nm.PendingCount() != 0 {
				t.Fatal("sin pending")
			}
			// El lote detenido también dispara el aviso urgente de volver a Bird, una vez.
			if fires := urgent.Fires(); len(fires) != 1 || fires[0].Trigger != monitor.TriggerProactiveStopped ||
				!strings.Contains(fires[0].Detail, "unsent=3") || strings.Contains(fires[0].Detail, "300111") {
				t.Fatalf("un aviso urgente sin teléfonos: %+v", fires)
			}
		})
	}
}
