package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
)

// Con el puente de Néctar puesto, los recordatorios ABREN la conversación con la plantilla y el
// pending queda bajo la clave de la sesión que contestará ("nectar:<id>"), con el teléfono del
// paciente en ContactPhone. Bird no ve nada: ni la plantilla ni el lookup de su conversación.

type loggedEvent struct {
	phone, eventType string
	data             map[string]interface{}
}

type eventRecorder struct {
	mu     sync.Mutex
	events []loggedEvent
}

func (r *eventRecorder) LogEvent(_ context.Context, _, phone, eventType string, data map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, loggedEvent{phone: phone, eventType: eventType, data: data})
}

func (r *eventRecorder) find(eventType string) (loggedEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.eventType == eventType {
			return e, true
		}
	}
	return loggedEvent{}, false
}

// nectarReminderRig arma un recordatorio de día-antes (o del mismo día) para UN paciente con el
// puente puesto. birdHits cuenta toda petición que llegue a Bird.
type nectarReminderRig struct {
	tasks    *Tasks
	nm       *notifications.NotificationManager
	bridge   *fakenectar.Bridge
	tracker  *eventRecorder
	birdHits *atomic.Int32
}

const reminderPhone = "+573001234567"

func newNectarReminderRig(t *testing.T, bridge *fakenectar.Bridge, sameDay bool) *nectarReminderRig {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-bird"}`))
	}))
	t.Cleanup(srv.Close)

	birdClient := bird.NewClientForTest(srv.URL)
	birdClient.SetNectar(bridge)
	cfg := testConfig()
	cfg.SameDayRemindersEnabled = true
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	t.Cleanup(func() { stopPendingTimers(nm, "nectar:77", "nectar:55", "nectar:5", reminderPhone) })

	day := time.Now().AddDate(0, 0, 1)
	slot := day
	if sameDay {
		day = time.Now()
		slot = day.Add(90 * time.Minute)
	}
	appt := domain.Appointment{
		ID: "apt-n1", PatientID: "P001", PatientName: "Ana",
		PatientPhone: "3001234567", Date: day, TimeSlot: slot.Format("200601021504"),
		Procedures: []domain.AppointmentProcedure{{CupCode: "890271", CupName: "EMG", Quantity: 1}},
	}
	tracker := &eventRecorder{}
	return &nectarReminderRig{
		tasks: &Tasks{
			AppointmentRepo: &testutil.MockAppointmentRepo{
				FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
					return []domain.Appointment{appt}, nil
				},
			},
			BirdClient:    birdClient,
			NotifyManager: nm,
			ProcedureRepo: &testutil.MockProcedureRepo{
				FindByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
					return &domain.Procedure{Code: code, Preparation: "Ayuno de 8 horas"}, nil
				},
			},
			Cfg:          cfg,
			Tracker:      tracker,
			NotifHistory: &mockNotifHistory{},
		},
		nm: nm, bridge: bridge, tracker: tracker, birdHits: &hits,
	}
}

func stopPendingTimers(nm *notifications.NotificationManager, keys ...string) {
	for _, k := range keys {
		if p, ok := nm.LoadPendingForTest(k); ok && p.Timer != nil {
			p.Timer.Stop()
		}
	}
}

func (r *nectarReminderRig) run(t *testing.T, sameDay bool) {
	t.Helper()
	var err error
	if sameDay {
		err = r.tasks.sendSameDayReminders(context.Background())
	} else {
		err = r.tasks.sendWhatsAppReminders(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestReminder_NectarRegistersPendingUnderSessionKey(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		name := map[bool]string{false: "día antes", true: "mismo día"}[sameDay]
		t.Run(name, func(t *testing.T) {
			rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenID: 77, OpenMsg: "901"}, sameDay)
			rig.run(t, sameDay)

			if got := rig.bridge.Opens(); len(got) != 1 || got[0] != reminderPhone {
				t.Fatalf("se abre al teléfono del paciente: %v", got)
			}
			p, ok := rig.nm.LoadPendingForTest("nectar:77")
			if !ok {
				t.Fatal("el pending queda bajo la clave de la sesión de Néctar")
			}
			if p.ContactPhone != reminderPhone || p.ConversationID != "77" || p.BirdMessageID != "901" {
				t.Fatalf("pending = %+v", p)
			}
			if rig.nm.HasPending(reminderPhone) {
				t.Error("no queda un pending bajo el teléfono")
			}
			ev, ok := rig.tracker.find("notification_sent")
			if !ok || ev.phone != "nectar:77" || ev.data["conversation_id"] != "77" {
				t.Fatalf("el evento va con la clave de la sesión: %+v", ev)
			}
			if rig.birdHits.Load() != 0 {
				t.Fatalf("Bird no recibe nada (ni plantilla ni lookup): %d", rig.birdHits.Load())
			}
		})
	}
}

// La preparación (texto libre) solo sale si Néctar dice que la ventana está abierta; la
// conversación recién abierta con una plantilla normalmente la tiene cerrada.
func TestReminder_NectarPrepFollowsNectarWindow(t *testing.T) {
	for _, open := range []bool{false, true} {
		rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenID: 77, Window: open}, false)
		rig.run(t, false)
		if q := rig.bridge.WindowQuestions(); len(q) != 1 || q[0] != 77 {
			t.Fatalf("ventana=%v: la pregunta va a Néctar por la 77: %v", open, q)
		}
		texts := rig.bridge.Texts()
		if open && (len(texts) != 1 || texts[0] != 77) {
			t.Errorf("ventana abierta: la preparación va a la 77: %v", texts)
		}
		if !open && len(texts) != 0 {
			t.Errorf("ventana cerrada: sin preparación: %v", texts)
		}
	}
}

// La idempotencia mira el teléfono del paciente aunque el pending viva bajo "nectar:<id>".
func TestReminder_IdempotentByContactPhone(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenID: 77}, sameDay)
		rig.nm.RegisterPending(notifications.PendingNotification{
			Type: "confirmation", Phone: "nectar:5", ContactPhone: reminderPhone, AppointmentID: "apt-n1",
		})
		rig.run(t, sameDay)
		if got := rig.bridge.Opens(); len(got) != 0 {
			t.Errorf("sameDay=%v: ya hay pending para ese teléfono, no se abre otra: %v", sameDay, got)
		}
	}
}

// Si el contacto ya conversa con un agente en Néctar, no se le escribe ni se registra pending.
func TestReminder_AgentOwnedSkipped(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		rig := newNectarReminderRig(t,
			&fakenectar.Bridge{OpenErr: &bird.AlreadyOpenError{ConversationID: 55, Mine: false}}, sameDay)
		rig.run(t, sameDay)

		ev, ok := rig.tracker.find("notification_skipped_agent_owned")
		if !ok || ev.data["appointment_id"] != "apt-n1" {
			t.Fatalf("sameDay=%v: falta el evento del salto: %+v", sameDay, rig.tracker.events)
		}
		if rig.nm.PendingCount() != 0 {
			t.Errorf("sameDay=%v: sin pending", sameDay)
		}
		if _, sent := rig.tracker.find("notification_sent"); sent {
			t.Errorf("sameDay=%v: no hubo envío", sameDay)
		}
	}
}

// Una conversación viva del bot (409 mine:true) recibe la plantilla y el pending va bajo su clave.
func TestReminder_AlreadyOpenMineUsesThatConversation(t *testing.T) {
	rig := newNectarReminderRig(t,
		&fakenectar.Bridge{OpenErr: &bird.AlreadyOpenError{ConversationID: 55, Mine: true}}, false)
	rig.run(t, false)
	p, ok := rig.nm.LoadPendingForTest("nectar:55")
	if !ok || p.ConversationID != "55" || p.ContactPhone != reminderPhone {
		t.Fatalf("pending bajo nectar:55: %+v ok=%v", p, ok)
	}
}

// Re-enganche matutino: una sesión de Néctar (clave "nectar:<id>") recibe el saludo solo con la
// ventana de Néctar abierta; los teléfonos de Bird siguen como siempre.
type reengageStub struct{ phones []string }

func (s reengageStub) PhonesForMorningReengage(context.Context) ([]string, error) {
	return s.phones, nil
}

func TestMorningReengage_NectarFollowsWindow(t *testing.T) {
	for _, open := range []bool{false, true} {
		var birdPosts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				birdPosts.Add(1)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"m"}`))
		}))
		birdClient := bird.NewClientForTest(srv.URL)
		bridge := &fakenectar.Bridge{Window: open}
		birdClient.SetNectar(bridge)
		cfg := testConfig()
		cfg.MorningReengageEnabled = true
		tasks := &Tasks{
			BirdClient:     birdClient,
			NotifyManager:  notifications.NewNotificationManager(birdClient, nil, cfg),
			Cfg:            cfg,
			ReengageSource: reengageStub{phones: []string{"nectar:9"}},
		}
		if err := tasks.sendMorningReengagement(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		texts := bridge.Texts()
		if open && (len(texts) != 1 || texts[0] != 9) {
			t.Errorf("ventana abierta: el saludo va a la 9: %v", texts)
		}
		if !open && len(texts) != 0 {
			t.Errorf("ventana cerrada: sin saludo: %v", texts)
		}
		if birdPosts.Load() != 0 {
			t.Errorf("Bird no recibe nada: %d", birdPosts.Load())
		}
	}
}
