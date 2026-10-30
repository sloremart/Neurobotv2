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
			if p.ContactPhone != reminderPhone || p.ConversationID != "77" || p.BirdMessageID != "901" || p.Unsent {
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

// Si el contacto ya conversa con un agente en Néctar, no se le escribe. El del mismo día no deja
// pending; el de día-antes lo deja bajo el teléfono para el IVR (TestReminder_UnsentRegistersPendingForIVR).
func TestReminder_AgentOwnedSkipped(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		rig := newNectarReminderRig(t,
			&fakenectar.Bridge{OpenErr: &bird.AlreadyOpenError{ConversationID: 55, Mine: false}}, sameDay)
		stopRigTimers(t, rig.nm, 1)
		rig.run(t, sameDay)

		ev, ok := rig.tracker.find("notification_skipped_agent_owned")
		if !ok || ev.data["appointment_id"] != "apt-n1" {
			t.Fatalf("sameDay=%v: falta el evento del salto: %+v", sameDay, rig.tracker.events)
		}
		if want := map[bool]int{false: 1, true: 0}[sameDay]; rig.nm.PendingCount() != want {
			t.Errorf("sameDay=%v: %d pending, se esperaba %d", sameDay, rig.nm.PendingCount(), want)
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

// Apertura en duda: la plantilla pudo salir. Evento notification_in_doubt y sin reintento (una sola
// apertura). Sin pending en el del mismo día; el de día-antes lo deja bajo el teléfono para el IVR.
func TestReminder_InDoubtNoPendingNoRetry(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenErr: nectar.ErrOpenInDoubt}, sameDay)
		stopRigTimers(t, rig.nm, 1)
		rig.run(t, sameDay)

		ev, ok := rig.tracker.find("notification_in_doubt")
		if !ok || ev.data["appointment_id"] != "apt-n1" || ev.phone != reminderPhone {
			t.Fatalf("sameDay=%v: falta el evento en duda: %+v", sameDay, rig.tracker.events)
		}
		if want := map[bool]int{false: 1, true: 0}[sameDay]; rig.nm.PendingCount() != want {
			t.Errorf("sameDay=%v: %d pending, se esperaba %d", sameDay, rig.nm.PendingCount(), want)
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
// paciente: los envíos se detienen en el primero —una sola apertura— y queda UN solo error con cuántos
// quedaron sin enviar. Sin pending en el del mismo día; en el de día-antes los 3 quedan con pending
// para el IVR (TestReminder_ConfigStopRegistersPendingForAll).
func TestReminder_ConfigErrorStopsRun(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		name := map[bool]string{false: "día antes", true: "mismo día"}[sameDay]
		t.Run(name, func(t *testing.T) {
			errorsLogged := captureErrors(t)
			rig := newNectarReminderRigN(t, &fakenectar.Bridge{
				OpenErr: fmt.Errorf("%w (409 template_other_account)", nectar.ErrTemplateRejected),
			}, sameDay, 3)
			stopRigTimers(t, rig.nm, 3)
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
			if want := map[bool]int{false: 3, true: 0}[sameDay]; rig.nm.PendingCount() != want {
				t.Errorf("%d pending, se esperaba %d", rig.nm.PendingCount(), want)
			}
		})
	}
}

// Las aperturas EN DUDA de una corrida (la plantilla pudo o no salir; en el día antes queda el
// pending del teléfono para el IVR, en el mismo día no hay IVR) se resumen en UN error al final,
// con cuántas y qué citas, sin teléfonos.
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

// === Recordatorio que no salió: pending para la llamada IVR de las 13:00 ===

// rigPhone es el teléfono (+57…) del paciente i del armado (newNectarReminderRigN).
func rigPhone(i int) string {
	if i == 1 {
		return reminderPhone
	}
	return fmt.Sprintf("+57300123450%d", i)
}

// stopRigTimers detiene los timers de los pending bajo el teléfono de los n pacientes del armado.
func stopRigTimers(t *testing.T, nm *notifications.NotificationManager, n int) {
	t.Helper()
	t.Cleanup(func() {
		for i := 1; i <= n; i++ {
			stopPendingTimers(nm, rigPhone(i))
		}
	})
}

// assertUnsentPending exige el pending de confirmación bajo el teléfono, sin rastro de envío, listo
// para el IVR, y el evento notification_send_failed con la razón.
func assertUnsentPending(t *testing.T, rig *nectarReminderRig, phone, apptID, reason string) {
	t.Helper()
	p, ok := rig.nm.LoadPendingForTest(phone)
	if !ok {
		t.Fatalf("falta el pending bajo %s (razón %s)", phone, reason)
	}
	if p.Type != "confirmation" || p.AppointmentID != apptID || p.BirdMessageID != "" ||
		p.ConversationID != "" || p.ContactPhone != "" || p.RetryCount != 0 || !p.Unsent {
		t.Fatalf("pending = %+v", p)
	}
	inIVR := false
	for _, target := range rig.nm.GetPendingForIVR() {
		if target.Phone == phone && target.AppointmentID == apptID {
			inIVR = true
		}
	}
	if !inIVR {
		t.Fatalf("el pending de %s debe ser objetivo del IVR", phone)
	}
	found := false
	rig.tracker.mu.Lock()
	for _, e := range rig.tracker.events {
		if e.eventType == "notification_send_failed" && e.phone == phone &&
			e.data["appointment_id"] == apptID && e.data["reason"] == reason && e.data["type"] == "confirmation" {
			found = true
		}
	}
	rig.tracker.mu.Unlock()
	if !found {
		t.Fatalf("falta notification_send_failed(%s, %s): %+v", apptID, reason, rig.tracker.events)
	}
}

// Cada falla de envío del recordatorio de día-antes deja el pending para que el IVR llame.
func TestReminder_UnsentRegistersPendingForIVR(t *testing.T) {
	cases := []struct {
		name, reason string
		openErr      error
	}{
		{"agente", "agent_owned", &bird.AlreadyOpenError{ConversationID: 55, Mine: false}},
		{"en duda", "in_doubt", nectar.ErrOpenInDoubt},
		{"no contactable", "non_contactable", bird.ErrNonContactable},
		{"error de envío", "send_error", fmt.Errorf("nectar: 500 boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenErr: tc.openErr}, false)
			stopRigTimers(t, rig.nm, 1)
			rig.run(t, false)

			assertUnsentPending(t, rig, reminderPhone, "apt-n1", tc.reason)
			if got := rig.bridge.Opens(); len(got) != 1 {
				t.Fatalf("una sola apertura, sin reintento: %v", got)
			}
			if rig.nm.PendingCount() != 1 {
				t.Fatalf("un solo pending: %d", rig.nm.PendingCount())
			}
		})
	}
}

// Número con entregas de WhatsApp fallando: no se le envía, pero el IVR sí lo llama.
func TestReminder_NoWhatsAppRegistersPendingForIVR(t *testing.T) {
	rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenID: 77}, false)
	stopRigTimers(t, rig.nm, 1)
	rig.nm.SetDeliverability(&failingDeliverability{failures: 2})
	rig.run(t, false)

	if got := rig.bridge.Opens(); len(got) != 0 {
		t.Fatalf("sin apertura: %v", got)
	}
	if _, ok := rig.tracker.find("reminder_suppressed_no_whatsapp"); !ok {
		t.Fatal("sigue el evento de supresión")
	}
	assertUnsentPending(t, rig, reminderPhone, "apt-n1", "no_whatsapp")
}

// Error de configuración en el primero de 3: se deja de enviar (una sola apertura, un solo error)
// y los 3 quedan con pending para el IVR.
func TestReminder_ConfigStopRegistersPendingForAll(t *testing.T) {
	errorsLogged := captureErrors(t)
	rig := newNectarReminderRigN(t, &fakenectar.Bridge{
		OpenErr: fmt.Errorf("%w (409 template_other_account)", nectar.ErrTemplateRejected),
	}, false, 3)
	stopRigTimers(t, rig.nm, 3)
	rig.run(t, false)

	if got := rig.bridge.Opens(); len(got) != 1 {
		t.Fatalf("una sola apertura: %v", got)
	}
	if logged := errorsLogged(); len(logged) != 1 || !strings.Contains(logged[0], "unsent=3") {
		t.Fatalf("un solo error con unsent=3: %v", logged)
	}
	for i := 1; i <= 3; i++ {
		assertUnsentPending(t, rig, rigPhone(i), fmt.Sprintf("apt-n%d", i), "config_stop")
	}
	if rig.nm.PendingCount() != 3 {
		t.Fatalf("3 pending: %d", rig.nm.PendingCount())
	}
}

// Contexto cancelado (apagado): no se registra nada; el catch-up re-corre.
func TestReminder_CanceledContextNoPending(t *testing.T) {
	rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenErr: fmt.Errorf("boom")}, false)
	stopRigTimers(t, rig.nm, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rig.tasks.sendWhatsAppReminders(ctx); err != nil {
		t.Fatal(err)
	}
	if rig.nm.PendingCount() != 0 {
		t.Fatalf("sin pending: %d", rig.nm.PendingCount())
	}
}

// Teléfono inválido y fuera de la lista blanca: no hay a quién llamar (o es modo prueba), sin pending.
func TestReminder_GuardsStillNoPending(t *testing.T) {
	t.Run("teléfono inválido", func(t *testing.T) {
		rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenErr: fmt.Errorf("boom")}, false)
		stopRigTimers(t, rig.nm, 1)
		repo := rig.tasks.AppointmentRepo.(*testutil.MockAppointmentRepo)
		inner := repo.FindPendingByDateFn
		repo.FindPendingByDateFn = func(ctx context.Context, d string) ([]domain.Appointment, error) {
			appts, err := inner(ctx, d)
			for i := range appts {
				appts[i].PatientPhone = "123"
			}
			return appts, err
		}
		rig.run(t, false)
		if rig.nm.PendingCount() != 0 {
			t.Fatalf("sin pending: %d", rig.nm.PendingCount())
		}
	})
	t.Run("fuera de lista blanca", func(t *testing.T) {
		rig := newNectarReminderRig(t, &fakenectar.Bridge{OpenErr: fmt.Errorf("boom")}, false)
		stopRigTimers(t, rig.nm, 1)
		rig.tasks.Cfg.TestingWhitelistPhones = []string{"+573009999999"}
		rig.run(t, false)
		if rig.nm.PendingCount() != 0 || len(rig.bridge.Opens()) != 0 {
			t.Fatalf("sin pending ni apertura: %d", rig.nm.PendingCount())
		}
	})
}
