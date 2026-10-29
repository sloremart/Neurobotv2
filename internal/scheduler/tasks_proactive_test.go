package scheduler

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
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
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
	return newNectarReminderRigN(t, bridge, sameDay, 1)
}

// newNectarReminderRigN es el mismo armado con n pacientes (P001…, apt-n1…, 300123456<i>).
func newNectarReminderRigN(t *testing.T, bridge *fakenectar.Bridge, sameDay bool, n int) *nectarReminderRig {
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
	appts := make([]domain.Appointment, 0, n)
	for i := 1; i <= n; i++ {
		phone := "3001234567"
		if i > 1 {
			phone = fmt.Sprintf("300123450%d", i)
		}
		appts = append(appts, domain.Appointment{
			ID: fmt.Sprintf("apt-n%d", i), PatientID: fmt.Sprintf("P%03d", i), PatientName: "Ana",
			PatientPhone: phone, Date: day, TimeSlot: slot.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{{CupCode: "890271", CupName: "EMG", Quantity: 1}},
		})
	}
	tracker := &eventRecorder{}
	return &nectarReminderRig{
		tasks: &Tasks{
			AppointmentRepo: &testutil.MockAppointmentRepo{
				FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
					return appts, nil
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

// Sin puente una clave "nectar:<id>" no es un destino: se salta como antes y Bird no ve nada
// (ni la consulta de la ventana ni un envío por BSUID).
func TestMorningReengage_NectarKeyWithoutBridgeSkipped(t *testing.T) {
	var birdHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		birdHits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"m"}`))
	}))
	defer srv.Close()
	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	cfg.MorningReengageEnabled = true
	recent := time.Now().Add(-1 * time.Hour)
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	nm.SetWaitingListDeps(nil, &windowSessionStub{last: &recent}, nil) // la BD diría «abierta»
	tasks := &Tasks{
		BirdClient: birdClient, NotifyManager: nm, Cfg: cfg,
		ReengageSource: reengageStub{phones: []string{"nectar:9"}},
	}
	if err := tasks.sendMorningReengagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := birdHits.Load(); n != 0 {
		t.Fatalf("Bird no recibe nada: %d", n)
	}
}

// Apertura en duda: la plantilla pudo salir. Evento notification_in_doubt, sin pending y sin
// reintento (una sola apertura).
func TestReminder_InDoubtNoPendingNoRetry(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenErr: nectar.ErrOpenInDoubt}, sameDay)
		rig.run(t, sameDay)

		ev, ok := rig.tracker.find("notification_in_doubt")
		if !ok || ev.data["appointment_id"] != "apt-n1" || ev.phone != reminderPhone {
			t.Fatalf("sameDay=%v: falta el evento en duda: %+v", sameDay, rig.tracker.events)
		}
		if rig.nm.PendingCount() != 0 {
			t.Errorf("sameDay=%v: sin pending", sameDay)
		}
		if got := rig.bridge.Opens(); len(got) != 1 {
			t.Errorf("sameDay=%v: una sola apertura, sin reintento: %v", sameDay, got)
		}
	}
}

// captureErrors desvía slog a un buffer durante la prueba y devuelve cuántas líneas ERROR dejó.
func captureErrors(t *testing.T) func() []string {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, line := range strings.Split(buf.String(), "\n") {
			if strings.Contains(line, "level=ERROR") {
				out = append(out, line)
			}
		}
		return out
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Un error de CONFIGURACIÓN de Néctar (aquí la plantilla rechazada) falla igual con cualquier
// paciente: la corrida se detiene en el primero —una sola apertura—, deja UN solo error con cuántos
// quedaron sin enviar y ningún pending.
func TestReminder_ConfigErrorStopsRun(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		name := map[bool]string{false: "día antes", true: "mismo día"}[sameDay]
		t.Run(name, func(t *testing.T) {
			errorsLogged := captureErrors(t)
			rig := newNectarReminderRigN(t, &fakenectar.Bridge{
				OpenErr: fmt.Errorf("%w (409 template_other_account)", nectar.ErrTemplateRejected),
			}, sameDay, 3)
			rig.run(t, sameDay)

			if got := rig.bridge.Opens(); len(got) != 1 {
				t.Fatalf("la corrida se detiene en la primera apertura: %v", got)
			}
			logged := errorsLogged()
			if len(logged) != 1 {
				t.Fatalf("un solo error por corrida, hubo %d: %v", len(logged), logged)
			}
			if !strings.Contains(logged[0], "unsent=3") || !strings.Contains(logged[0], "template_other_account") {
				t.Fatalf("el error nombra la clase y cuántos quedaron sin enviar: %s", logged[0])
			}
			if strings.Contains(logged[0], "300123") {
				t.Fatalf("sin teléfonos en el log: %s", logged[0])
			}
			if rig.nm.PendingCount() != 0 {
				t.Error("sin pending")
			}
		})
	}
}

// Las aperturas EN DUDA de una corrida (la plantilla pudo o no salir; sin pending tampoco habrá
// IVR) se resumen en UN error al final, con cuántas y qué citas, sin teléfonos.
func TestReminder_InDoubtSummary(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		name := map[bool]string{false: "día antes", true: "mismo día"}[sameDay]
		t.Run(name, func(t *testing.T) {
			errorsLogged := captureErrors(t)
			rig := newNectarReminderRigN(t, &fakenectar.Bridge{OpenErr: nectar.ErrOpenInDoubt}, sameDay, 2)
			rig.run(t, sameDay)

			if got := rig.bridge.Opens(); len(got) != 2 {
				t.Fatalf("en duda no detiene la corrida: %v", got)
			}
			logged := errorsLogged()
			if len(logged) != 1 {
				t.Fatalf("un solo resumen por corrida, hubo %d: %v", len(logged), logged)
			}
			line := logged[0]
			if !strings.Contains(line, "count=2") || !strings.Contains(line, "apt-n1") || !strings.Contains(line, "apt-n2") {
				t.Fatalf("el resumen trae cuántas y qué citas: %s", line)
			}
			if strings.Contains(line, "300123") || strings.Contains(line, "+57") {
				t.Fatalf("sin teléfonos en el resumen: %s", line)
			}
		})
	}
}

// Sin dudas no hay resumen: una corrida limpia no deja ningún error.
func TestReminder_NoInDoubtNoSummary(t *testing.T) {
	errorsLogged := captureErrors(t)
	rig := newNectarReminderRigN(t, &fakenectar.Bridge{OpenID: 77}, false, 1)
	rig.run(t, false)
	if logged := errorsLogged(); len(logged) != 0 {
		t.Fatalf("sin errores: %v", logged)
	}
}
