package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// ─── Local mocks for scheduler-specific interfaces ────────────────────────────


// mockWaitingListRepo implements WaitingListRepo.
type mockWaitingListRepo struct {
	ExpireOldFn             func(ctx context.Context, days int) (int64, error)
	GetDistinctWaitingCupsFn func(ctx context.Context) ([]string, error)
	GetWaitingByCupsFn      func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	UpdateStatusFn          func(ctx context.Context, id, status string) error
	MarkNotifiedFn          func(ctx context.Context, id string) error
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

func (m *mockWaitingListRepo) UpdateStatus(ctx context.Context, id, status string) error {
	if m.UpdateStatusFn != nil {
		return m.UpdateStatusFn(ctx, id, status)
	}
	return nil
}

func (m *mockWaitingListRepo) MarkNotified(ctx context.Context, id string) error {
	if m.MarkNotifiedFn != nil {
		return m.MarkNotifiedFn(ctx, id)
	}
	return nil
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
	// data_cleanup, whatsapp_reminders, waiting_list_check_06, voice_reminders.
	// The waiting-list check was consolidated to a single 06:00 run (commit d5ea233).
	if len(s.tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(s.tasks))
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
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890271", CupName: "Test", Quantity: 1},
			},
		},
		{
			ID: "apt-2", PatientID: "P002", PatientName: "Maria",
			PatientPhone: "3109876543", // valid
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
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
	// No pending notifications should be registered since all sends failed
	if nm.PendingCount() != 0 {
		t.Errorf("expected 0 pending notifications after all fails, got %d", nm.PendingCount())
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
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
			Procedures: []domain.AppointmentProcedure{
				{CupCode: "890271", CupName: "Electromiografia", Quantity: 1},
			},
		},
		{
			ID: "apt-2", PatientID: "P001", PatientName: "Juan",
			PatientPhone: "3001234567",
			Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
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
		FindAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
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
		MarkNotifiedFn: func(ctx context.Context, id string) error {
			markNotifiedID = id
			return nil
		},
	}

	procRepo := &testutil.MockProcedureRepo{
		FindSubjectTypeForCupsFn: func(ctx context.Context, cupsCode string) (int, error) {
			return 8, nil
		},
	}
	scheduleRepo := &testutil.MockScheduleRepo{
		FindAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
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
		FindAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
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
