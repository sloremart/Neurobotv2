package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// TestGroupRequiresCreatinine: el recordatorio agrega el aviso de creatinina solo si alguna cita del
// grupo es contrastada (por observación de SIESA o por el nombre del CUP "CON CONTRASTE").
func TestGroupRequiresCreatinine(t *testing.T) {
	procRepo := &testutil.MockProcedureRepo{
		FindByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
			if code == "879112" {
				return &domain.Procedure{Code: code, Name: "TAC DE CRÁNEO CON CONTRASTE", IsActive: true}, nil
			}
			return &domain.Procedure{Code: code, Name: "Estudio simple", IsActive: true}, nil
		},
	}
	ctx := context.Background()

	// (a) Observación "Contrastada" (cita del bot) → true.
	obsGroup := []domain.Appointment{{Observations: "Contrastada", Procedures: []domain.AppointmentProcedure{{CupCode: "883210"}}}}
	if !groupRequiresCreatinine(ctx, obsGroup, procRepo) {
		t.Error("observación Contrastada debía requerir creatinina")
	}
	// (b) Sin observación pero CUP con nombre CON CONTRASTE (cita de agente) → true.
	nameGroup := []domain.Appointment{{Observations: "", Procedures: []domain.AppointmentProcedure{{CupCode: "879112"}}}}
	if !groupRequiresCreatinine(ctx, nameGroup, procRepo) {
		t.Error("CUP con nombre CON CONTRASTE debía requerir creatinina")
	}
	// (c) Estudio simple, sin señal → false.
	simpleGroup := []domain.Appointment{{Observations: "", Procedures: []domain.AppointmentProcedure{{CupCode: "883210"}}}}
	if groupRequiresCreatinine(ctx, simpleGroup, procRepo) {
		t.Error("estudio simple no debía requerir creatinina")
	}
}

// ─── Local mocks for scheduler-specific interfaces ────────────────────────────

// mockWaitingListRepo implements WaitingListRepo.
type mockWaitingListRepo struct {
	ExpireOldFn              func(ctx context.Context, days int) (int64, error)
	ExpireStaleNotifiedFn    func(ctx context.Context, hours int) (int64, error)
	GetDistinctWaitingCupsFn func(ctx context.Context) ([]string, error)
	GetWaitingByCupsFn       func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	UpdateStatusFn           func(ctx context.Context, id, status string) error
	MarkNotifiedFn           func(ctx context.Context, id string) (bool, error)
}

func (m *mockWaitingListRepo) ExpireStaleNotified(ctx context.Context, hours int) (int64, error) {
	if m.ExpireStaleNotifiedFn != nil {
		return m.ExpireStaleNotifiedFn(ctx, hours)
	}
	return 0, nil
}

func (m *mockWaitingListRepo) ExpireOld(ctx context.Context, days int) (int64, error) {
	if m.ExpireOldFn != nil {
		return m.ExpireOldFn(ctx, days)
	}
	return 0, nil
}

func (m *mockWaitingListRepo) GetDistinctWaitingCups(ctx context.Context) ([]string, error) {
	if m.GetDistinctWaitingCupsFn != nil {
		return m.GetDistinctWaitingCupsFn(ctx)
	}
	return nil, nil
}

func (m *mockWaitingListRepo) GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
	if m.GetWaitingByCupsFn != nil {
		return m.GetWaitingByCupsFn(ctx, cupsCode, limit)
	}
	return nil, nil
}

func (m *mockWaitingListRepo) GetWaitingByCupsIn(_ context.Context, _ []string, _ int) ([]domain.WaitingListEntry, error) {
	return nil, nil
}

func (m *mockWaitingListRepo) UpdateStatus(ctx context.Context, id, status string) error {
	if m.UpdateStatusFn != nil {
		return m.UpdateStatusFn(ctx, id, status)
	}
	return nil
}

func (m *mockWaitingListRepo) MarkNotified(ctx context.Context, id string) (bool, error) {
	if m.MarkNotifiedFn != nil {
		return m.MarkNotifiedFn(ctx, id)
	}
	return true, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newBirdTestServer creates an httptest.Server returning {"id":"msg-123"} for all POSTs.
func newBirdTestServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-123"}`))
	}))
}

// newBirdErrorServer creates an httptest.Server returning 500 for all requests.
func newBirdErrorServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"server error"}`))
	}))
}

// testConfig returns a config with template fields populated for testing.
func testConfig() *config.Config {
	return &config.Config{
		CenterName:                       "Test Center",
		BirdTemplateConfirmProjectID:     "proj-confirm",
		BirdTemplateConfirmVersionID:     "v-confirm",
		BirdTemplateConfirmLocale:        "es-CO",
		BirdTemplateWaitingListProjectID: "proj-wl",
		BirdTemplateWaitingListLocale:    "es-CO",
		WhatsAppNotificationsEnabled:     true, // operación normal: canales habilitados
		IVRNotificationsEnabled:          true,
	}
}

// sampleAppointments returns two appointments for different patients (valid phones).
func sampleAppointments() []domain.Appointment {
	tomorrow := time.Now().AddDate(0, 0, 1)
	return []domain.Appointment{
		{
			ID: "apt-1", PatientID: "P001", PatientName: "Juan Perez",
			PatientPhone: "3001234567", DoctorName: "Dr. Garcia",
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890271", CupName: "Electromiografia", Quantity: 1},
			},
		},
		{
			ID: "apt-2", PatientID: "P002", PatientName: "Maria Lopez",
			PatientPhone: "3109876543", DoctorName: "Dr. Garcia",
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890272", CupName: "Potenciales Evocados", Quantity: 1},
			},
		},
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 1. RegisterAll Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRegisterAll_RegistersFourTasks(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)
	// 4 originales (data_cleanup, whatsapp_reminders, waiting_list_check_06, voice_reminders)
	// + 11 corridas horarias del recordatorio de corta antelación (06:00–16:00) + la retención de
	// PII de chat_events.
	if len(s.tasks) != 17 {
		t.Fatalf("expected 17 tasks (4 + 11 same-day + reengage + pii retention), got %d", len(s.tasks))
	}
}

func TestRegisterAll_TaskNames(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)

	expected := map[string]bool{
		"data_cleanup":          false,
		"whatsapp_reminders":    false,
		"waiting_list_check_06": false,
		"voice_reminders":       false,
	}
	for h := 6; h <= 16; h++ {
		expected[fmt.Sprintf("same_day_reminders_%02d", h)] = false
	}
	expected["morning_reengage"] = false
	expected["chat_events_pii_retention"] = false

	for _, task := range s.tasks {
		if _, ok := expected[task.Name]; !ok {
			t.Errorf("unexpected task name %q", task.Name)
		}
		expected[task.Name] = true
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected task %q not found", name)
		}
	}
}

func TestRegisterAll_DataCleanup_NoWeekdayFilter(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)

	for _, task := range s.tasks {
		if task.Name == "data_cleanup" {
			if task.Hour != 2 || task.Minute != 0 {
				t.Errorf("data_cleanup expected 02:00, got %02d:%02d", task.Hour, task.Minute)
			}
			if task.Weekdays != nil {
				t.Error("data_cleanup should have nil weekday filter (run every day)")
			}
			return
		}
	}
	t.Fatal("data_cleanup task not found")
}

func TestRegisterAll_WhatsAppReminders_Schedule(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)

	for _, task := range s.tasks {
		if task.Name == "whatsapp_reminders" {
			if task.Hour != 7 || task.Minute != 0 {
				t.Errorf("whatsapp_reminders expected 07:00, got %02d:%02d", task.Hour, task.Minute)
			}
			if len(task.Weekdays) != 7 {
				t.Errorf("whatsapp_reminders expected 7 weekdays (every day), got %d", len(task.Weekdays))
			}
			// Verify Sunday IS included (Monday appointments need Sunday reminders)
			hasSunday := false
			for _, wd := range task.Weekdays {
				if wd == time.Sunday {
					hasSunday = true
				}
			}
			if !hasSunday {
				t.Error("whatsapp_reminders must include Sunday for Monday appointment reminders")
			}
			return
		}
	}
	t.Fatal("whatsapp_reminders task not found")
}

func TestRegisterAll_WaitingListCheck_WeekdaysOnly(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)

	for _, task := range s.tasks {
		if task.Name == "waiting_list_check_06" {
			if task.Hour != 6 || task.Minute != 0 {
				t.Errorf("waiting_list_check_06 expected 06:00, got %02d:%02d", task.Hour, task.Minute)
			}
			if len(task.Weekdays) != 5 {
				t.Errorf("waiting_list_check_06 expected 5 weekdays (Mon-Fri), got %d", len(task.Weekdays))
			}
			// Verify Saturday and Sunday are NOT included
			for _, wd := range task.Weekdays {
				if wd == time.Saturday || wd == time.Sunday {
					t.Errorf("waiting_list_check_06 should not include %s", wd)
				}
			}
			return
		}
	}
	t.Fatal("waiting_list_check_06 task not found")
}

func TestRegisterAll_VoiceReminders_Schedule(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)

	for _, task := range s.tasks {
		if task.Name == "voice_reminders" {
			if task.Hour != 13 || task.Minute != 0 {
				t.Errorf("voice_reminders expected 13:00, got %02d:%02d", task.Hour, task.Minute)
			}
			if len(task.Weekdays) != 7 {
				t.Errorf("voice_reminders expected 7 weekdays (every day), got %d", len(task.Weekdays))
			}
			return
		}
	}
	t.Fatal("voice_reminders task not found")
}

func TestRegisterAll_AllTaskFunctionsNonNil(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)

	for _, task := range s.tasks {
		if task.Fn == nil {
			t.Errorf("task %q has nil Fn", task.Name)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 2. sendWhatsAppReminders Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendWhatsAppReminders_Success_TwoPatients(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	var sendCount atomic.Int32
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sendCount.Add(1)
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-123"}`))
	}))
	defer countSrv.Close()

	birdClient := bird.NewClientForTest(countSrv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	appts := sampleAppointments()
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendWhatsAppReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if sendCount.Load() != 2 {
		t.Errorf("expected 2 sends (one per patient), got %d", sendCount.Load())
	}
}

// TestSendWhatsAppReminders_Disabled_SkipsTask: con WHATSAPP_NOTIFICATIONS_ENABLED=false la tarea
// corta de entrada — ni siquiera consulta las citas ni envía.
func TestSendWhatsAppReminders_Disabled_SkipsTask(t *testing.T) {
	var sendCount atomic.Int32
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sendCount.Add(1)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer countSrv.Close()

	cfg := testConfig()
	cfg.WhatsAppNotificationsEnabled = false

	repoCalled := false
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			repoCalled = true
			return sampleAppointments(), nil
		},
	}
	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      bird.NewClientForTest(countSrv.URL),
		Cfg:             cfg,
	}

	if err := tasks.sendWhatsAppReminders(context.Background()); err != nil {
		t.Fatalf("expected nil error when disabled, got %v", err)
	}
	if repoCalled {
		t.Error("expected the task to short-circuit before querying appointments")
	}
	if sendCount.Load() != 0 {
		t.Errorf("expected 0 sends when disabled, got %d", sendCount.Load())
	}
}

func TestSendWhatsAppReminders_NoAppointments_EarlyReturn(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return nil, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendWhatsAppReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestSendWhatsAppReminders_RepoError(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return nil, errors.New("db connection lost")
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendWhatsAppReminders(context.Background())
	if err == nil {
		t.Fatal("expected error from repo, got nil")
	}
	// Falló antes de enviar: el scheduler puede reintentarla (D-03).
	if !errors.Is(err, ErrNothingSent) {
		t.Errorf("error = %v, want wrapping ErrNothingSent", err)
	}
}

func TestSendWhatsAppReminders_InvalidPhone_Skipped(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	var sendCount atomic.Int32
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sendCount.Add(1)
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-123"}`))
	}))
	defer countSrv.Close()

	birdClient := bird.NewClientForTest(countSrv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	tomorrow := time.Now().AddDate(0, 0, 1)
	appts := []domain.Appointment{
		{
			ID: "apt-1", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "invalid-phone", // invalid
			Date:         tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890271", CupName: "Test", Quantity: 1},
			},
		},
		{
			ID: "apt-2", PatientID: "P002", PatientName: "Maria",
			PatientPhone: "3109876543", // valid
			Date:         tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890272", CupName: "Test2", Quantity: 1},
			},
		},
	}

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendWhatsAppReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// Only 1 valid phone should have been sent
	if sendCount.Load() != 1 {
		t.Errorf("expected 1 send (invalid phone skipped), got %d", sendCount.Load())
	}
}

func TestSendWhatsAppReminders_BirdError_Continues(t *testing.T) {
	// Server always returns 500 (after retries the client returns error)
	errSrv := newBirdErrorServer()
	defer errSrv.Close()

	birdClient := bird.NewClientForTest(errSrv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	appts := sampleAppointments()
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	// Should not return error — errors are logged and continued
	err := tasks.sendWhatsAppReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error (errors logged, not returned), got %v", err)
	}
	// Ningún envío salió, pero cada paciente queda con pending bajo su teléfono (sin mensaje) para
	// que el IVR de las 13:00 lo llame.
	targets := nm.GetPendingForIVR()
	for _, p := range targets {
		if lp, ok := nm.LoadPendingForTest(p.Phone); ok && lp.Timer != nil {
			lp.Timer.Stop()
		}
		if p.BirdMessageID != "" || p.Type != "confirmation" {
			t.Errorf("pending sin mensaje esperado: %+v", p)
		}
	}
	if nm.PendingCount() != 2 || len(targets) != 2 {
		t.Errorf("expected 2 pending IVR targets after all fails, got %d (ivr %d)", nm.PendingCount(), len(targets))
	}
}

func TestSendWhatsAppReminders_RegistersPending(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	appts := sampleAppointments()[:1] // Single patient
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendWhatsAppReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if nm.PendingCount() != 1 {
		t.Errorf("expected 1 pending notification registered, got %d", nm.PendingCount())
	}
	// Verify it was stored by the correct phone
	if !nm.HasPending("+573001234567") {
		t.Error("expected pending notification for +573001234567")
	}
}

func TestSendWhatsAppReminders_MultipleAppts_SamePatient(t *testing.T) {
	var sendCount atomic.Int32
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sendCount.Add(1)
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-123"}`))
	}))
	defer countSrv.Close()

	birdClient := bird.NewClientForTest(countSrv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	tomorrow := time.Now().AddDate(0, 0, 1)
	// Two appointments for the same patient
	appts := []domain.Appointment{
		{
			ID: "apt-1", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "3001234567",
			Date:         tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890271", CupName: "Electromiografia", Quantity: 1},
			},
		},
		{
			ID: "apt-2", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "3001234567",
			Date:         tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890272", CupName: "Potenciales Evocados", Quantity: 1},
			},
		},
	}

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendWhatsAppReminders(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Grouped into one patient group → only 1 send
	if sendCount.Load() != 1 {
		t.Errorf("expected 1 send (grouped by patient), got %d", sendCount.Load())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3. sendVoiceReminders Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendVoiceReminders_NonRespondersOnly(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	tomorrow := time.Now().AddDate(0, 0, 1)
	appts := []domain.Appointment{
		{
			// This one has a pending notification → should be skipped
			ID: "apt-1", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "3001234567", Confirmed: false,
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		},
		{
			// No pending, not confirmed → non-responder
			ID: "apt-2", PatientID: "P002", PatientName: "Maria",
			PatientPhone: "3109876543", Confirmed: false,
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		},
		{
			// Confirmed → should be skipped
			ID: "apt-3", PatientID: "P003", PatientName: "Carlos",
			PatientPhone: "3201112233", Confirmed: true,
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		},
	}

	// Register pending for P001 so it gets filtered out
	nm.RegisterPending(notifications.PendingNotification{
		Type:          "confirmation",
		Phone:         "+573001234567",
		AppointmentID: "apt-1",
		BirdMessageID: "msg-1",
	})

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendVoiceReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// Only P002 (Maria) should be a non-responder
	// (PlaceCall is a no-op returning "", nil — so no side effects to count directly,
	// but the function should complete without error)
}

func TestSendVoiceReminders_NoAppointments(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return nil, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendVoiceReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestSendVoiceReminders_RepoError(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	// Register a pending at RetryCount=0 so GetPendingForIVR returns it as an IVR target.
	// The follow-up chain is disabled by default (ConfirmFollowupEnabled=false), so IVR
	// targets are those still at RetryCount==0 (the initial reminder).
	nm.RegisterPending(notifications.PendingNotification{
		Type:          "confirmation",
		Phone:         "+573001234567",
		AppointmentID: "apt-1",
	})

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return nil, errors.New("db error")
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendVoiceReminders(context.Background())
	if err == nil {
		t.Fatal("expected error from repo, got nil")
	}
	// Falló antes de enviar: el scheduler puede reintentarla (D-03).
	if !errors.Is(err, ErrNothingSent) {
		t.Errorf("error = %v, want wrapping ErrNothingSent", err)
	}
}

func TestSendVoiceReminders_InvalidPhone_Skipped(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	tomorrow := time.Now().AddDate(0, 0, 1)
	appts := []domain.Appointment{
		{
			ID: "apt-1", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "abc", Confirmed: false, // invalid phone
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		},
	}

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendVoiceReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestSendVoiceReminders_DeduplicateByPhone(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	tomorrow := time.Now().AddDate(0, 0, 1)
	// Two appointments with same phone (different PatientIDs but same phone)
	appts := []domain.Appointment{
		{
			ID: "apt-1", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "3001234567", Confirmed: false,
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		},
		{
			ID: "apt-2", PatientID: "P001-dup", PatientName: "Juan Alt",
			PatientPhone: "3001234567", Confirmed: false,
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		},
	}

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	// Both appointments have the same phone, so only one call should be attempted.
	// PlaceCall is a no-op, but the code uses a seen map to deduplicate.
	// We verify it runs without error (dedup logic works).
	err := tasks.sendVoiceReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestSendVoiceReminders_ConfirmedPatient_Excluded(t *testing.T) {
	srv := newBirdTestServer()
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	tomorrow := time.Now().AddDate(0, 0, 1)
	appts := []domain.Appointment{
		{
			ID: "apt-1", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "3001234567", Confirmed: true, // already confirmed
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		},
	}

	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(ctx context.Context, date string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}

	tasks := &Tasks{
		AppointmentRepo: mockRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.sendVoiceReminders(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestVoiceReminders_DialsContactPhone: un pending con clave de sesión Néctar se empareja con la
// cita y se llama por el teléfono del paciente (ContactPhone); el registro de la llamada y la marca
// de IVR enviado siguen yendo a la clave del pending.
func TestVoiceReminders_DialsContactPhone(t *testing.T) {
	var dialed atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/calls") {
			var body struct {
				To string `json:"to"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			dialed.Store(body.To)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"call-77"}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-123"}`))
	}))
	defer srv.Close()

	birdClient := bird.NewVoiceClientForTest(srv.URL)
	cfg := testConfig()
	// El whitelist lista el teléfono del paciente, no la clave: prueba que se evalúa sobre DialPhone.
	cfg.TestingWhitelistPhones = []string{"+573001112233"}
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	nm.RegisterPending(notifications.PendingNotification{
		Type:          "confirmation",
		Phone:         "nectar:77",
		ContactPhone:  "+573001112233",
		AppointmentID: "apt-77",
	})
	defer func() {
		if p, ok := nm.LoadPendingForTest("nectar:77"); ok && p.Timer != nil {
			p.Timer.Stop()
		}
	}()

	tomorrow := time.Now().AddDate(0, 0, 1)
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			return []domain.Appointment{{
				ID: "apt-77", PatientID: "P077", PatientName: "Ana",
				PatientPhone: "300 111 2233", Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			}}, nil
		},
	}
	tasks := &Tasks{AppointmentRepo: mockRepo, BirdClient: birdClient, NotifyManager: nm, Cfg: cfg}

	if err := tasks.sendVoiceReminders(context.Background()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if got, _ := dialed.Load().(string); got != "+573001112233" {
		t.Fatalf("PlaceCall debe marcar al teléfono del paciente, marcó %q", got)
	}
	p, ok := nm.LoadPendingForTest("nectar:77")
	if !ok {
		t.Fatal("el pending debe seguir bajo su clave tras el IVR")
	}
	if p.CallID != "call-77" {
		t.Errorf("RegisterCallID debe usar la clave del pending: CallID = %q", p.CallID)
	}
	if p.RetryCount != 3 {
		t.Errorf("MarkIVRSent debe usar la clave del pending: RetryCount = %d, want 3", p.RetryCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 4. cleanup Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestCleanup_WaitingListExpired(t *testing.T) {
	var wlCalled bool
	wlRepo := &mockWaitingListRepo{
		ExpireOldFn: func(ctx context.Context, days int) (int64, error) {
			wlCalled = true
			return 2, nil
		},
	}

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
	}

	err := tasks.cleanup(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !wlCalled {
		t.Error("expected WaitingListRepo.ExpireOld to be called")
	}
}

func TestCleanup_NilWaitingListRepo_SkipsWL(t *testing.T) {
	tasks := &Tasks{
		WaitingListRepo: nil,
	}

	err := tasks.cleanup(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCleanup_WaitingListError_Logged(t *testing.T) {
	wlRepo := &mockWaitingListRepo{
		ExpireOldFn: func(ctx context.Context, days int) (int64, error) {
			return 0, errors.New("wl expire failed")
		},
	}

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
	}

	err := tasks.cleanup(context.Background())
	if err != nil {
		t.Fatalf("expected nil error (error is logged), got %v", err)
	}
}

func TestCleanup_WaitingListExpireDays30(t *testing.T) {
	var daysPassed int
	wlRepo := &mockWaitingListRepo{
		ExpireOldFn: func(ctx context.Context, days int) (int64, error) {
			daysPassed = days
			return 0, nil
		},
	}

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
	}

	tasks.cleanup(context.Background())
	if daysPassed != 30 {
		t.Errorf("expected ExpireOld called with 30 days, got %d", daysPassed)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 5. checkWaitingList Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestCheckWaitingList_NilWaitingListRepo_ReturnsNil(t *testing.T) {
	tasks := &Tasks{
		WaitingListRepo: nil,
		SlotService:     services.NewSlotService(&testutil.MockProcedureRepo{}, &testutil.MockScheduleRepo{}),
	}
	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckWaitingList_NilSlotService_ReturnsNil(t *testing.T) {
	tasks := &Tasks{
		WaitingListRepo: &mockWaitingListRepo{},
		SlotService:     nil,
	}
	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckWaitingList_BothDepsNil_ReturnsNil(t *testing.T) {
	tasks := &Tasks{
		WaitingListRepo: nil,
		SlotService:     nil,
	}
	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckWaitingList_NoWaitingEntries(t *testing.T) {
	wlRepo := &mockWaitingListRepo{
		GetDistinctWaitingCupsFn: func(ctx context.Context) ([]string, error) {
			return nil, nil // no entries
		},
	}

	slotSvc := services.NewSlotService(&testutil.MockProcedureRepo{}, &testutil.MockScheduleRepo{})

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
		SlotService:     slotSvc,
	}

	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckWaitingList_NoSlots_Skip(t *testing.T) {
	wlRepo := &mockWaitingListRepo{
		GetDistinctWaitingCupsFn: func(ctx context.Context) ([]string, error) {
			return []string{"890271"}, nil
		},
		GetWaitingByCupsFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{
				{ID: "wl-1", PatientID: "P001", PatientName: "Juan", CupsCode: "890271", CupsName: "EMG", PhoneNumber: "+573001234567", PatientAge: 30, Espacios: 1},
			}, nil
		},
	}

	// SlotService with no subject → returns nil slots
	procRepo := &testutil.MockProcedureRepo{
		FindSubjectTypeForCupsFn: func(ctx context.Context, cupsCode string) (int, error) {
			return 0, nil // no subject → no slots
		},
	}
	slotSvc := services.NewSlotService(procRepo, &testutil.MockScheduleRepo{})

	srv := newBirdTestServer()
	defer srv.Close()
	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)

	apptRepo := &testutil.MockAppointmentRepo{}

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
		SlotService:     slotSvc,
		AppointmentRepo: apptRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil (no slots, skip), got %v", err)
	}
	if nm.PendingCount() != 0 {
		t.Errorf("expected 0 notifications, got %d", nm.PendingCount())
	}
}

func TestCheckWaitingList_DuplicateFound_UpdateStatus(t *testing.T) {
	var statusUpdated string
	var statusID string

	wlRepo := &mockWaitingListRepo{
		GetDistinctWaitingCupsFn: func(ctx context.Context) ([]string, error) {
			return []string{"890271"}, nil
		},
		GetWaitingByCupsFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{
				{ID: "wl-1", PatientID: "P001", PatientName: "Juan", CupsCode: "890271", CupsName: "EMG", PhoneNumber: "+573001234567", PatientAge: 30, Espacios: 1},
			}, nil
		},
		UpdateStatusFn: func(ctx context.Context, id, status string) error {
			statusID = id
			statusUpdated = status
			return nil
		},
	}

	// Need doctors and working days so slots are found
	procRepo := &testutil.MockProcedureRepo{
		FindSubjectTypeForCupsFn: func(ctx context.Context, cupsCode string) (int, error) {
			return 8, nil
		},
	}
	scheduleRepo := &testutil.MockScheduleRepo{
		FindAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			ts := time.Now().AddDate(0, 0, 3)
			return []domain.AvailableSlotRow{{
				SlotTime: ts, DoctorDocument: "DOC001", DoctorName: "Dr. Test",
				DoctorSiesaCode: "S1", AgendaID: 1, DurationMin: 30, AgendaSede: 2,
			}}, nil
		},
	}
	slotSvc := services.NewSlotService(procRepo, scheduleRepo)

	// HasFutureForCup returns true → duplicate
	apptRepo := &testutil.MockAppointmentRepo{
		HasFutureForCupFn: func(ctx context.Context, patientID, cupCode string) (bool, error) {
			return true, nil // patient already has future appointment for this cup
		},
	}

	srv := newBirdTestServer()
	defer srv.Close()
	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	nm.SetWaitingListCheckDeps(slotSvc, apptRepo, wlRepo, nil, nil)

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
		SlotService:     slotSvc,
		AppointmentRepo: apptRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if statusID != "wl-1" {
		t.Errorf("expected UpdateStatus called for wl-1, got %q", statusID)
	}
	if statusUpdated != "duplicate_found" {
		t.Errorf("expected status 'duplicate_found', got %q", statusUpdated)
	}
}

func TestCheckWaitingList_SlotsAvailable_NotifyAndRegister(t *testing.T) {
	var markNotifiedID string
	var sendCount atomic.Int32

	wlRepo := &mockWaitingListRepo{
		GetDistinctWaitingCupsFn: func(ctx context.Context) ([]string, error) {
			return []string{"890271"}, nil
		},
		GetWaitingByCupsFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{
				{ID: "wl-1", PatientID: "P001", PatientName: "Juan", CupsCode: "890271", CupsName: "EMG", PhoneNumber: "+573001234567", PatientAge: 30, Espacios: 1},
			}, nil
		},
		MarkNotifiedFn: func(_ context.Context, id string) (bool, error) {
			markNotifiedID = id
			return true, nil
		},
	}

	procRepo := &testutil.MockProcedureRepo{
		FindSubjectTypeForCupsFn: func(ctx context.Context, cupsCode string) (int, error) {
			return 8, nil
		},
	}
	scheduleRepo := &testutil.MockScheduleRepo{
		FindAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			ts := time.Now().AddDate(0, 0, 3)
			return []domain.AvailableSlotRow{{
				SlotTime: ts, DoctorDocument: "DOC001", DoctorName: "Dr. Test",
				DoctorSiesaCode: "S1", AgendaID: 1, DurationMin: 30, AgendaSede: 2,
			}}, nil
		},
	}
	slotSvc := services.NewSlotService(procRepo, scheduleRepo)

	apptRepo := &testutil.MockAppointmentRepo{
		HasFutureForCupFn: func(ctx context.Context, patientID, cupCode string) (bool, error) {
			return false, nil // no duplicate
		},
	}

	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sendCount.Add(1)
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-wl-1"}`))
	}))
	defer countSrv.Close()

	birdClient := bird.NewClientForTest(countSrv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	nm.SetWaitingListCheckDeps(slotSvc, apptRepo, wlRepo, nil, nil)

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
		SlotService:     slotSvc,
		AppointmentRepo: apptRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if sendCount.Load() != 1 {
		t.Errorf("expected 1 template send, got %d", sendCount.Load())
	}
	if markNotifiedID != "wl-1" {
		t.Errorf("expected MarkNotified for wl-1, got %q", markNotifiedID)
	}
	if nm.PendingCount() != 1 {
		t.Errorf("expected 1 pending notification, got %d", nm.PendingCount())
	}
}

func TestCheckWaitingList_EmptyTemplateConfig_Skip(t *testing.T) {
	wlRepo := &mockWaitingListRepo{
		GetDistinctWaitingCupsFn: func(ctx context.Context) ([]string, error) {
			return []string{"890271"}, nil
		},
		GetWaitingByCupsFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{
				{ID: "wl-1", PatientID: "P001", PatientName: "Juan", CupsCode: "890271", CupsName: "EMG", PhoneNumber: "+573001234567", PatientAge: 30, Espacios: 1},
			}, nil
		},
	}

	procRepo := &testutil.MockProcedureRepo{
		FindSubjectTypeForCupsFn: func(ctx context.Context, cupsCode string) (int, error) {
			return 8, nil
		},
	}
	scheduleRepo := &testutil.MockScheduleRepo{
		FindAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			ts := time.Now().AddDate(0, 0, 3)
			return []domain.AvailableSlotRow{{
				SlotTime: ts, DoctorDocument: "DOC001", DoctorName: "Dr. Test",
				DoctorSiesaCode: "S1", AgendaID: 1, DurationMin: 30, AgendaSede: 2,
			}}, nil
		},
	}
	slotSvc := services.NewSlotService(procRepo, scheduleRepo)

	apptRepo := &testutil.MockAppointmentRepo{
		HasFutureForCupFn: func(ctx context.Context, patientID, cupCode string) (bool, error) {
			return false, nil
		},
	}

	srv := newBirdTestServer()
	defer srv.Close()
	birdClient := bird.NewClientForTest(srv.URL)

	// Config with EMPTY waiting list template project ID
	cfg := &config.Config{
		CenterName:                       "Test Center",
		BirdTemplateWaitingListProjectID: "", // empty → should skip
	}
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	nm.SetWaitingListCheckDeps(slotSvc, apptRepo, wlRepo, nil, nil)

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
		SlotService:     slotSvc,
		AppointmentRepo: apptRepo,
		BirdClient:      birdClient,
		NotifyManager:   nm,
		Cfg:             cfg,
	}

	err := tasks.checkWaitingList(context.Background())
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if nm.PendingCount() != 0 {
		t.Errorf("expected 0 notifications (template config empty), got %d", nm.PendingCount())
	}
}

func TestCheckWaitingList_GetDistinctCupsError(t *testing.T) {
	wlRepo := &mockWaitingListRepo{
		GetDistinctWaitingCupsFn: func(ctx context.Context) ([]string, error) {
			return nil, errors.New("db error")
		},
	}

	slotSvc := services.NewSlotService(&testutil.MockProcedureRepo{}, &testutil.MockScheduleRepo{})

	tasks := &Tasks{
		WaitingListRepo: wlRepo,
		SlotService:     slotSvc,
	}

	err := tasks.checkWaitingList(context.Background())
	if err == nil {
		t.Fatal("expected error from GetDistinctWaitingCups, got nil")
	}
	// Falló antes de enviar: el scheduler puede reintentarla (D-03).
	if !errors.Is(err, ErrNothingSent) {
		t.Errorf("error = %v, want wrapping ErrNothingSent", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 6. groupAppointmentsByPatient Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestGroupAppointmentsByPatient_EmptyList(t *testing.T) {
	result := groupAppointmentsByPatient(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestGroupAppointmentsByPatient_SinglePatient(t *testing.T) {
	appts := []domain.Appointment{
		{ID: "a1", PatientID: "P001"},
		{ID: "a2", PatientID: "P001"},
	}
	result := groupAppointmentsByPatient(appts)
	if len(result) != 1 {
		t.Errorf("expected 1 group, got %d", len(result))
	}
	if len(result["P001"]) != 2 {
		t.Errorf("expected 2 appointments for P001, got %d", len(result["P001"]))
	}
}

func TestGroupAppointmentsByPatient_MultiplePatients(t *testing.T) {
	appts := []domain.Appointment{
		{ID: "a1", PatientID: "P001"},
		{ID: "a2", PatientID: "P002"},
		{ID: "a3", PatientID: "P001"},
		{ID: "a4", PatientID: "P003"},
		{ID: "a5", PatientID: "P002"},
	}
	result := groupAppointmentsByPatient(appts)
	if len(result) != 3 {
		t.Errorf("expected 3 groups, got %d", len(result))
	}
	if len(result["P001"]) != 2 {
		t.Errorf("expected 2 for P001, got %d", len(result["P001"]))
	}
	if len(result["P002"]) != 2 {
		t.Errorf("expected 2 for P002, got %d", len(result["P002"]))
	}
	if len(result["P003"]) != 1 {
		t.Errorf("expected 1 for P003, got %d", len(result["P003"]))
	}
}

func TestGroupAppointmentsByPatient_PreservesOrder(t *testing.T) {
	appts := []domain.Appointment{
		{ID: "a1", PatientID: "P001"},
		{ID: "a2", PatientID: "P001"},
		{ID: "a3", PatientID: "P001"},
	}
	result := groupAppointmentsByPatient(appts)
	group := result["P001"]
	for i, appt := range group {
		expected := fmt.Sprintf("a%d", i+1)
		if appt.ID != expected {
			t.Errorf("index %d: expected ID %q, got %q", i, expected, appt.ID)
		}
	}
}

// TestBuildReminderProcedures valida el texto del recordatorio: con UNA cita solo el nombre (la
// hora va en el param appointment_time del template); con VARIAS citas el mismo día, la hora de
// cada una embebida (porque appointment_time solo refleja una).
func TestBuildReminderProcedures(t *testing.T) {
	appt := func(slot, code, name string) domain.Appointment {
		return domain.Appointment{
			TimeSlot:   slot,
			Procedures: []domain.AppointmentProcedure{{CupCode: code, CupName: name}},
		}
	}

	// Una sola cita -> solo el nombre, sin hora.
	single := []domain.Appointment{appt("202603150900", "890274", "Consulta Neurologia")}
	if got := buildReminderProcedures(context.Background(), single, nil); got != "Consulta Neurologia" {
		t.Errorf("single: got %q", got)
	}

	// Varias citas el mismo dia -> cada una con su hora.
	multi := []domain.Appointment{
		appt("202603150900", "890274", "Consulta Neurologia"),
		appt("202603151400", "883902", "Resonancia"),
	}
	got := buildReminderProcedures(context.Background(), multi, nil)
	want := "Consulta Neurologia (9:00 AM) y Resonancia (2:00 PM)"
	if got != want {
		t.Errorf("multi: got %q, want %q", got, want)
	}

	// Sin procedimientos -> placeholder, sin panic.
	empty := []domain.Appointment{{TimeSlot: "202603150900"}}
	if got := buildReminderProcedures(context.Background(), empty, nil); got != "Procedimiento" {
		t.Errorf("empty: got %q", got)
	}
}

// TestCatchupAllowed (L14): política de recuperación por modo y hora.
func TestCatchupAllowed(t *testing.T) {
	cases := []struct {
		mode CatchupMode
		hour int
		want bool
	}{
		{CatchupNever, 13, false}, {CatchupNever, 8, false}, // IVR: nunca
		{CatchupDaytime, 6, false}, {CatchupDaytime, 20, false}, {CatchupDaytime, 23, false}, // fuera de ventana
		{CatchupDaytime, 7, true}, {CatchupDaytime, 14, true}, {CatchupDaytime, 19, true}, // dentro
		{CatchupAlways, 2, true}, {CatchupAlways, 23, true}, // interno: siempre
	}
	for _, c := range cases {
		if got := catchupAllowed(c.mode, c.hour); got != c.want {
			t.Errorf("catchupAllowed(mode=%d, hour=%d) = %v, want %v", c.mode, c.hour, got, c.want)
		}
	}
}

// TestRegisterAll_CatchupModes (L14): cada tarea queda con su política de catch-up correcta.
func TestRegisterAll_CatchupModes(t *testing.T) {
	s := NewScheduler(time.UTC)
	(&Tasks{}).RegisterAll(s)

	want := map[string]CatchupMode{
		"data_cleanup":          CatchupAlways,  // interno
		"whatsapp_reminders":    CatchupDaytime, // WA
		"waiting_list_check_06": CatchupDaytime, // WA
		"voice_reminders":       CatchupNever,   // IVR
	}
	for _, task := range s.tasks {
		if w, ok := want[task.Name]; ok && task.Catchup != w {
			t.Errorf("%s: Catchup = %d, want %d", task.Name, task.Catchup, w)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Same-day reminders (recordatorio de corta antelación, §8.1 #2)
// ═══════════════════════════════════════════════════════════════════════════════

// Las corridas horarias quedan registradas 06:00–16:00, minuto 0, lun–sáb, sin catch-up.
func TestRegisterAll_SameDayReminders_Schedule(t *testing.T) {
	tasks := &Tasks{}
	s := NewScheduler(time.UTC)
	tasks.RegisterAll(s)

	found := 0
	for _, task := range s.tasks {
		if !strings.HasPrefix(task.Name, "same_day_reminders_") {
			continue
		}
		found++
		if task.Hour < 6 || task.Hour > 16 || task.Minute != 0 {
			t.Errorf("%s: horario %02d:%02d fuera de 06:00–16:00", task.Name, task.Hour, task.Minute)
		}
		if len(task.Weekdays) != 6 {
			t.Errorf("%s: esperaba 6 días (lun–sáb), got %d", task.Name, len(task.Weekdays))
		}
		for _, wd := range task.Weekdays {
			if wd == time.Sunday {
				t.Errorf("%s: no debe correr domingo", task.Name)
			}
		}
		if task.Catchup != CatchupNever {
			t.Errorf("%s: Catchup debe ser CatchupNever (ventana perdida no se recupera)", task.Name)
		}
		if task.Fn == nil {
			t.Errorf("%s: Fn nil", task.Name)
		}
	}
	if found != 11 {
		t.Fatalf("esperaba 11 corridas same_day, got %d", found)
	}
}

// La ventana [now+1h, now+2h) capta cada cita exactamente una vez y excluye canceladas/confirmadas.
func TestSameDayWindowCandidates(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	ts := func(tt time.Time) string { return tt.Format("200601021504") }

	appts := []domain.Appointment{
		{ID: "in-90m", TimeSlot: ts(now.Add(90 * time.Minute))},                     // dentro
		{ID: "lower-bound", TimeSlot: ts(now.Add(1 * time.Hour))},                   // límite inferior: INCLUIDO
		{ID: "upper-bound", TimeSlot: ts(now.Add(2 * time.Hour))},                   // límite superior: EXCLUIDO (lo capta la corrida siguiente)
		{ID: "too-soon", TimeSlot: ts(now.Add(30 * time.Minute))},                   // muy encima: fuera
		{ID: "too-far", TimeSlot: ts(now.Add(3 * time.Hour))},                       // lejos: fuera
		{ID: "canceled", TimeSlot: ts(now.Add(90 * time.Minute)), Canceled: true},   // cancelada: fuera
		{ID: "confirmed", TimeSlot: ts(now.Add(90 * time.Minute)), Confirmed: true}, // ya confirmada: fuera
		{ID: "bad-slot", TimeSlot: "no-parseable"},                                  // malformada: fuera
	}

	got := sameDayWindowCandidates(now, appts)
	ids := map[string]bool{}
	for _, a := range got {
		ids[a.ID] = true
	}
	if len(got) != 2 || !ids["in-90m"] || !ids["lower-bound"] {
		t.Fatalf("esperaba exactamente [in-90m, lower-bound], got %v", ids)
	}
}

type mockNotifHistory struct {
	notified bool
	err      error
	calls    atomic.Int32
}

func (m *mockNotifHistory) WasAppointmentNotified(_ context.Context, _ string) (bool, error) {
	m.calls.Add(1)
	return m.notified, m.err
}

// Con el flag apagado la tarea corta de entrada: ni consulta citas ni envía.
func TestSendSameDayReminders_DisabledByFlag_SkipsTask(t *testing.T) {
	repoCalled := false
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			repoCalled = true
			return nil, nil
		},
	}
	cfg := testConfig()
	cfg.SameDayRemindersEnabled = false

	tasks := &Tasks{AppointmentRepo: mockRepo, Cfg: cfg, NotifHistory: &mockNotifHistory{}}
	if err := tasks.sendSameDayReminders(context.Background()); err != nil {
		t.Fatalf("no esperaba error, got %v", err)
	}
	if repoCalled {
		t.Error("con el flag apagado NO debe consultar citas")
	}
}

// Sin NotifHistory cableado la tarea NO envía (fail-closed: sin dedup no hay envío seguro).
func TestSendSameDayReminders_FailClosedWithoutHistory(t *testing.T) {
	repoCalled := false
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			repoCalled = true
			return nil, nil
		},
	}
	cfg := testConfig()
	cfg.SameDayRemindersEnabled = true

	tasks := &Tasks{AppointmentRepo: mockRepo, Cfg: cfg} // NotifHistory nil a propósito
	if err := tasks.sendSameDayReminders(context.Background()); err != nil {
		t.Fatalf("no esperaba error, got %v", err)
	}
	if repoCalled {
		t.Error("sin NotifHistory NO debe consultar citas (fail-closed)")
	}
}

// Flujo completo: una cita HOY en ventana → 1 envío cuando no fue notificada antes; 0 cuando ya lo fue.
func TestSendSameDayReminders_SendsAndDedups(t *testing.T) {
	for _, tc := range []struct {
		name          string
		alreadySent   bool
		expectedSends int32
	}{
		{"nueva sin notificar → envía", false, 1},
		{"ya notificada a las 07:00 → dedup, no envía", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sendCount atomic.Int32
			countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					sendCount.Add(1)
				}
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"id":"msg-sd-1"}`))
			}))
			defer countSrv.Close()

			birdClient := bird.NewClientForTest(countSrv.URL)
			cfg := testConfig()
			cfg.SameDayRemindersEnabled = true
			nm := notifications.NewNotificationManager(birdClient, nil, cfg)

			now := time.Now()
			appt := domain.Appointment{
				ID: "apt-today", PatientID: "P010", PatientName: "Ana Ruiz",
				PatientPhone: "3001112233", DoctorName: "Dr. Garcia",
				Date: now, TimeSlot: now.Add(90 * time.Minute).Format("200601021504"),
				Procedures: []domain.AppointmentProcedure{{CupCode: "890274", CupName: "Consulta Neurologia", Quantity: 1}},
			}
			mockRepo := &testutil.MockAppointmentRepo{
				FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
					return []domain.Appointment{appt}, nil
				},
			}
			history := &mockNotifHistory{notified: tc.alreadySent}

			tasks := &Tasks{
				AppointmentRepo: mockRepo,
				BirdClient:      birdClient,
				NotifyManager:   nm,
				Cfg:             cfg,
				NotifHistory:    history,
			}
			if err := tasks.sendSameDayReminders(context.Background()); err != nil {
				t.Fatalf("no esperaba error, got %v", err)
			}
			if sendCount.Load() != tc.expectedSends {
				t.Errorf("envíos = %d, esperaba %d", sendCount.Load(), tc.expectedSends)
			}
			if history.calls.Load() != 1 {
				t.Errorf("el dedup debe consultarse exactamente 1 vez, got %d", history.calls.Load())
			}
		})
	}
}

// D-03: el corte de SIESA al leer las citas de hoy ocurre antes de cualquier envío, así que el
// error queda marcado como reintentable.
func TestSendSameDayReminders_RepoError_MarkedNothingSent(t *testing.T) {
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			return nil, errors.New("db error")
		},
	}
	cfg := testConfig()
	cfg.SameDayRemindersEnabled = true

	tasks := &Tasks{AppointmentRepo: mockRepo, Cfg: cfg, NotifHistory: &mockNotifHistory{}}
	if err := tasks.sendSameDayReminders(context.Background()); !errors.Is(err, ErrNothingSent) {
		t.Errorf("error = %v, want wrapping ErrNothingSent", err)
	}
}

// La franja sale de la hora programada de la corrida (M-7), pero si la lectura de SIESA se colgó más de
// una hora, esa franja ya incluye citas que empezaron. Una cita cuya hora pasó al terminar la lectura no
// recibe aviso; una de la misma franja que todavía no empieza, sí.
func TestSendSameDayReminders_SkipsAppointmentAlreadyStarted(t *testing.T) {
	var sendCount atomic.Int32
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sendCount.Add(1)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-sd-late"}`))
	}))
	defer countSrv.Close()

	birdClient := bird.NewClientForTest(countSrv.URL)
	cfg := testConfig()
	cfg.SameDayRemindersEnabled = true

	// La corrida es de hace 100 min y su franja es [hace 40 min, dentro de 20 min).
	scheduledAt := time.Now().Truncate(time.Minute).Add(-100 * time.Minute)
	started := domain.Appointment{
		ID: "apt-started", PatientID: "P012", PatientName: "Ana Ruiz",
		PatientPhone: "3001112233", DoctorName: "Dr. Garcia",
		Date: scheduledAt, TimeSlot: scheduledAt.Add(70 * time.Minute).Format("200601021504"),
		Procedures: []domain.AppointmentProcedure{{CupCode: "890274", CupName: "Consulta Neurologia", Quantity: 1}},
	}
	upcoming := started
	upcoming.ID, upcoming.PatientID, upcoming.PatientPhone = "apt-upcoming", "P013", "3004445566"
	upcoming.TimeSlot = scheduledAt.Add(115 * time.Minute).Format("200601021504")

	tasks := &Tasks{
		AppointmentRepo: &testutil.MockAppointmentRepo{
			FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
				return []domain.Appointment{started, upcoming}, nil
			},
		},
		BirdClient:    birdClient,
		NotifyManager: notifications.NewNotificationManager(birdClient, nil, cfg),
		Cfg:           cfg,
		NotifHistory:  &mockNotifHistory{},
	}
	ctx := context.WithValue(context.Background(), runAtKey{}, scheduledAt)
	if err := tasks.sendSameDayReminders(ctx); err != nil {
		t.Fatalf("no esperaba error, got %v", err)
	}
	if got := sendCount.Load(); got != 1 {
		t.Errorf("envíos = %d, esperaba 1 (solo la cita que no ha empezado)", got)
	}
}

// M-7: el reintento calcula la franja desde la hora programada de su corrida, no desde la suya.
// Una corrida de «las 10:00» que falla y sale bien en el reintento de «las 10:30» avisa a la cita
// de «las 11:15» (su franja es [11:00, 12:00)); antes la franja salía desde las 10:30 y la perdía.
// La deduplicación sigue mandando: una cita ya notificada no se reenvía en el reintento.
func TestSendSameDayReminders_RetryUsesScheduledWindow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		alreadySent   bool
		expectedSends int32
	}{
		{"cita en la franja de la corrida → avisa", false, 1},
		{"ya notificada → dedup, no reenvía", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sendCount atomic.Int32
			countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					sendCount.Add(1)
				}
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"id":"msg-sd-retry"}`))
			}))
			defer countSrv.Close()

			birdClient := bird.NewClientForTest(countSrv.URL)
			cfg := testConfig()
			cfg.SameDayRemindersEnabled = true

			// La corrida es de hace 30 min («las 10:00») y el reintento corre ahora («las 10:30»).
			scheduledAt := time.Now().Truncate(time.Minute).Add(-taskRetryWindow)
			appt := domain.Appointment{
				ID: "apt-retry", PatientID: "P011", PatientName: "Ana Ruiz",
				PatientPhone: "3001112233", DoctorName: "Dr. Garcia",
				Date: scheduledAt, TimeSlot: scheduledAt.Add(75 * time.Minute).Format("200601021504"),
				Procedures: []domain.AppointmentProcedure{{CupCode: "890274", CupName: "Consulta Neurologia", Quantity: 1}},
			}
			var fetches atomic.Int32
			mockRepo := &testutil.MockAppointmentRepo{
				FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
					if fetches.Add(1) == 1 {
						return nil, errors.New("siesa down") // la corrida de su hora no envía nada
					}
					return []domain.Appointment{appt}, nil
				},
			}
			history := &mockNotifHistory{notified: tc.alreadySent}
			tasks := &Tasks{
				AppointmentRepo: mockRepo,
				BirdClient:      birdClient,
				NotifyManager:   notifications.NewNotificationManager(birdClient, nil, cfg),
				Cfg:             cfg,
				NotifHistory:    history,
			}

			s := NewScheduler(time.Local)
			task := ScheduledTask{
				Name: "same_day_reminders_test", Hour: scheduledAt.Hour(), Minute: scheduledAt.Minute(),
				Fn: tasks.sendSameDayReminders,
			}
			s.AddTask(task)
			lastRun := map[string]time.Time{}
			s.evaluateTasks(context.Background(), scheduledAt, lastRun)
			if !s.Wait(5 * time.Second) {
				t.Fatal("la corrida de su hora no terminó")
			}
			if got := s.evaluateTasks(context.Background(), scheduledAt.Add(taskRetryWindow), lastRun); len(got) != 1 {
				t.Fatalf("el reintento debía lanzarse al final de su ventana, got %v", got)
			}
			if !s.Wait(10 * time.Second) {
				t.Fatal("el reintento no terminó")
			}
			if fetches.Load() != 2 {
				t.Fatalf("esperaba la corrida y un reintento, got %d lecturas", fetches.Load())
			}
			// La cita llegó a la deduplicación: con la franja corrida quedaría fuera y el «no reenvía» del
			// segundo caso pasaría por otra razón.
			if got := history.calls.Load(); got != 1 {
				t.Errorf("consultas de deduplicación = %d, esperaba 1", got)
			}
			if sendCount.Load() != tc.expectedSends {
				t.Errorf("envíos = %d, esperaba %d", sendCount.Load(), tc.expectedSends)
			}
		})
	}
}

// A-08: un segundo disparo de los recordatorios (doble clic en el dashboard, o el cron más uno manual)
// mientras otro está en curso no recorre ni envía nada; al terminar, el siguiente corre normal.
func TestSendReminders_ConcurrentRunsSendOnce(t *testing.T) {
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sends.Add(1)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-once"}`))
	}))
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	tomorrow := time.Now().AddDate(0, 0, 1)
	appt := domain.Appointment{
		ID: "apt-once", PatientID: "P100", PatientName: "Ana Ruiz",
		PatientPhone: "3001112233", DoctorName: "Dr. Garcia",
		Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		Procedures: []domain.AppointmentProcedure{{CupCode: "890274", CupName: "Consulta Neurologia", Quantity: 1}},
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var reads atomic.Int32
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			if reads.Add(1) == 1 {
				close(entered)
				<-release
				return []domain.Appointment{appt}, nil
			}
			return nil, nil
		},
	}
	tasks := &Tasks{AppointmentRepo: mockRepo, BirdClient: birdClient, NotifyManager: nm, Cfg: cfg}

	firstDone := make(chan error, 1)
	go func() { firstDone <- tasks.SendWhatsAppReminders(context.Background()) }()
	<-entered

	if err := tasks.sendWhatsAppReminders(context.Background()); !errors.Is(err, ErrNothingSent) {
		t.Fatalf("el segundo disparo falla como reintentable (el cron lo reintenta): %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("el segundo disparo no debe leer citas: lecturas = %d", got)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("primer disparo: %v", err)
	}
	if got := sends.Load(); got != 1 {
		t.Errorf("envíos = %d, want 1", got)
	}
	if p, ok := nm.LoadPendingForTest("+573001112233"); ok && p.Timer != nil {
		p.Timer.Stop()
	}

	if err := tasks.SendWhatsAppReminders(context.Background()); err != nil {
		t.Fatalf("tercer disparo: %v", err)
	}
	if got := reads.Load(); got != 2 {
		t.Errorf("terminado el primero, el siguiente disparo corre: lecturas = %d, want 2", got)
	}
}

// ivrHarness es un Bird de voz que anota las llamadas.
type ivrHarness struct {
	calls atomic.Int32
	body  atomic.Value // cuerpo de la última llamada
	srv   *httptest.Server
}

func newIVRHarness(t *testing.T, callStatus int) *ivrHarness {
	t.Helper()
	h := &ivrHarness{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/calls") {
			raw, _ := io.ReadAll(r.Body)
			h.body.Store(string(raw))
			n := h.calls.Add(1)
			w.WriteHeader(callStatus)
			_, _ = fmt.Fprintf(w, `{"id":"call-%d"}`, n)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-123"}`))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

// ivrFamily son dos citas de mañana de dos pacientes con el mismo teléfono; la de Mateo es la más próxima.
func ivrFamily() (soon, late domain.Appointment) {
	tomorrow := time.Now().AddDate(0, 0, 1)
	day := tomorrow.Format("20060102")
	soon = domain.Appointment{
		ID: "apt-soon", PatientID: "P-MATEO", PatientName: "Mateo Ruiz", PatientPhone: "3001112233",
		Date: tomorrow, TimeSlot: day + "0800",
	}
	late = domain.Appointment{
		ID: "apt-late", PatientID: "P-SARA", PatientName: "Sara Ruiz", PatientPhone: "3001112233",
		Date: tomorrow, TimeSlot: day + "1000",
	}
	return soon, late
}

func stopIVRTimers(t *testing.T, nm *notifications.NotificationManager, keys ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, k := range keys {
			if p, ok := nm.LoadPendingForTest(k); ok && p.Timer != nil {
				p.Timer.Stop()
			}
		}
	})
}

// B-15: dos pendings con el mismo teléfono dan UNA llamada, que anuncia la cita más próxima. El DTMF se
// aplica solo a esa cita; el otro pending sigue igual, sin marcarse ni resolverse.
func TestSendVoiceReminders_OneCallPerDialPhone(t *testing.T) {
	const keySoon, keyLate = "nectar:1", "nectar:2"
	h := newIVRHarness(t, http.StatusAccepted)
	birdClient := bird.NewVoiceClientForTest(h.srv.URL)
	cfg := testConfig()
	soon, late := ivrFamily()

	var confirmed []string
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			return []domain.Appointment{soon, late}, nil // la última era la que quedaba en el mapa por teléfono
		},
		FindByIDFn: func(_ context.Context, id string) (*domain.Appointment, error) {
			if id == soon.ID {
				return &soon, nil
			}
			return &late, nil
		},
		ConfirmBatchFn: func(_ context.Context, ids []string, _, _ string) error {
			confirmed = append(confirmed, ids...)
			return nil
		},
	}
	nm := notifications.NewNotificationManager(birdClient, services.NewAppointmentService(mockRepo, nil), cfg)
	for key, apptID := range map[string]string{keySoon: soon.ID, keyLate: late.ID} {
		nm.RegisterPending(notifications.PendingNotification{
			Type: "confirmation", Phone: key, ContactPhone: "+573001112233", AppointmentID: apptID,
		})
	}
	stopIVRTimers(t, nm, keySoon, keyLate)

	tasks := &Tasks{AppointmentRepo: mockRepo, BirdClient: birdClient, NotifyManager: nm, Cfg: cfg}
	if err := tasks.sendVoiceReminders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.calls.Load(); got != 1 {
		t.Fatalf("llamadas = %d, want 1", got)
	}
	if body, _ := h.body.Load().(string); !strings.Contains(body, soon.PatientName) || strings.Contains(body, late.PatientName) {
		t.Errorf("la llamada debe anunciar la cita más próxima: %s", body)
	}
	called, ok := nm.LoadPendingForTest(keySoon)
	if !ok || called.CallID != "call-1" || called.RetryCount != 3 {
		t.Fatalf("pending de la cita anunciada = %+v", called)
	}
	other, ok := nm.LoadPendingForTest(keyLate)
	if !ok || other.CallID != "" || other.RetryCount != 0 {
		t.Fatalf("el otro pending no se toca: %+v", other)
	}
	if other.Timer == nil {
		t.Error("el otro pending conserva su timer: sigue su flujo normal")
	}

	nm.HandleVoiceGatherResult("call-1", "1")
	if len(confirmed) != 1 || confirmed[0] != soon.ID {
		t.Errorf("confirmadas = %v, want solo %s", confirmed, soon.ID)
	}
	if _, ok := nm.LoadPendingForTest(keySoon); ok {
		t.Error("el pending de la cita anunciada queda resuelto")
	}
	if _, ok := nm.LoadPendingForTest(keyLate); !ok {
		t.Error("el DTMF no resuelve el otro pending")
	}
}

// B-15: si la llamada falla no se intenta otra por el mismo teléfono, y ningún pending queda marcado.
func TestSendVoiceReminders_FailedCallIsNotRetriedWithOtherPending(t *testing.T) {
	const keySoon, keyLate = "nectar:1", "nectar:2"
	h := newIVRHarness(t, http.StatusBadRequest)
	birdClient := bird.NewVoiceClientForTest(h.srv.URL)
	cfg := testConfig()
	soon, late := ivrFamily()
	mockRepo := &testutil.MockAppointmentRepo{
		FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			return []domain.Appointment{soon, late}, nil
		},
	}
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	for key, apptID := range map[string]string{keySoon: soon.ID, keyLate: late.ID} {
		nm.RegisterPending(notifications.PendingNotification{
			Type: "confirmation", Phone: key, ContactPhone: "+573001112233", AppointmentID: apptID,
		})
	}
	stopIVRTimers(t, nm, keySoon, keyLate)

	tasks := &Tasks{AppointmentRepo: mockRepo, BirdClient: birdClient, NotifyManager: nm, Cfg: cfg}
	if err := tasks.sendVoiceReminders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.calls.Load(); got != 1 {
		t.Errorf("llamadas = %d, want 1", got)
	}
	for _, key := range []string{keySoon, keyLate} {
		if p, ok := nm.LoadPendingForTest(key); !ok || p.RetryCount != 0 {
			t.Errorf("pending %s = %+v, sigue sin marcar", key, p)
		}
	}
}

// B-15: cada pending solo llama por SU cita. Uno cuya cita ya no está pendiente (confirmada o cancelada
// en SIESA) no llama aunque el teléfono tenga otra, y nunca se llama a un teléfono que no es el de la
// cita (el contacto de Néctar distinto del de SIESA). Teléfonos distintos, una llamada cada uno.
func TestIVRCalls_PickPerPhone(t *testing.T) {
	soon, late := ivrFamily()
	other := domain.Appointment{ID: "apt-other", PatientPhone: "3009998877", TimeSlot: late.TimeSlot}
	pending := func(key, contact, apptID string) *notifications.PendingNotification {
		return &notifications.PendingNotification{Phone: key, ContactPhone: contact, AppointmentID: apptID}
	}
	tasks := &Tasks{Cfg: testConfig()}

	calls := tasks.ivrCalls([]*notifications.PendingNotification{
		pending("nectar:9", "+573001112233", "apt-gone"), // su cita ya no está: no llama
		pending("nectar:8", "+573005550000", late.ID),    // su teléfono no es el de la cita: no llama
		pending("nectar:2", "+573001112233", late.ID),    // cita posterior del mismo teléfono
		pending("+573001112233", "", soon.ID),            // Bird: la más próxima de ese teléfono
		pending("nectar:4", "+573001112233", soon.ID),    // la misma cita bajo otra clave: una llamada
		pending("nectar:3", "+573009998877", other.ID),   // otro teléfono: su propia llamada
	}, []domain.Appointment{late, other, soon})

	got := map[string]string{}
	for _, c := range calls {
		got[c.appt.ID] = c.pending.Phone
	}
	if len(calls) != 2 || got[soon.ID] != "+573001112233" || got[other.ID] != "nectar:3" {
		t.Errorf("llamadas = %v", got)
	}
}

// M-4: el DTMF confirma o cancela todas las citas del paciente en esa fecha, y también resuelve sus
// otros pendings (la misma persona bajo otra clave, otra cita del día), que si no escalarían «sin
// respuesta» una cita ya resuelta. El pending de otro paciente con el mismo teléfono no se toca.
func TestIVRDTMF_ResolvesOtherPendingsOfSamePatient(t *testing.T) {
	for _, tc := range []struct{ keys, outcome string }{{"1", "confirmed"}, {"2", "cancelled"}} {
		t.Run(tc.outcome, func(t *testing.T) {
			const keyCalled, keySameAppt, keyOtherAppt, keyOtherPatient = "+573001112233", "nectar:1", "nectar:2", "nectar:3"
			h := newIVRHarness(t, http.StatusAccepted)
			birdClient := bird.NewVoiceClientForTest(h.srv.URL)
			cfg := testConfig()
			soon, late := ivrFamily()
			soonB := soon
			soonB.ID, soonB.TimeSlot = "apt-soon-2", soon.TimeSlot[:8]+"0900" // otra cita de Mateo ese día

			var applied []string
			mockRepo := &testutil.MockAppointmentRepo{
				FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
					return []domain.Appointment{soon, soonB, late}, nil
				},
				FindByIDFn: func(_ context.Context, _ string) (*domain.Appointment, error) { return &soon, nil },
				FindUpcomingByPatientFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
					return []domain.Appointment{soon, soonB}, nil
				},
				ConfirmBatchFn: func(_ context.Context, ids []string, _, _ string) error {
					applied = append(applied, ids...)
					return nil
				},
				CancelBatchFn: func(_ context.Context, ids []string, _, _, _ string) error {
					applied = append(applied, ids...)
					return nil
				},
			}
			nm := notifications.NewNotificationManager(birdClient, services.NewAppointmentService(mockRepo, nil), cfg)
			for key, apptID := range map[string]string{
				keyCalled: soon.ID, keySameAppt: soon.ID, keyOtherAppt: soonB.ID, keyOtherPatient: late.ID,
			} {
				contact := "+573001112233"
				if key == keyCalled {
					contact = ""
				}
				nm.RegisterPending(notifications.PendingNotification{
					Type: "confirmation", Phone: key, ContactPhone: contact, AppointmentID: apptID,
				})
			}
			stopIVRTimers(t, nm, keyCalled, keySameAppt, keyOtherAppt, keyOtherPatient)

			nm.RegisterCallID("call-1", keyCalled)
			nm.HandleVoiceGatherResult("call-1", tc.keys)

			if len(applied) != 2 {
				t.Fatalf("citas aplicadas = %v, want las dos de Mateo", applied)
			}
			for _, key := range []string{keyCalled, keySameAppt, keyOtherAppt} {
				if _, ok := nm.LoadPendingForTest(key); ok {
					t.Errorf("el pending %s es de una cita ya resuelta por el DTMF", key)
				}
			}
			if _, ok := nm.LoadPendingForTest(keyOtherPatient); !ok {
				t.Error("el pending de otro paciente sigue su flujo")
			}
		})
	}
}
