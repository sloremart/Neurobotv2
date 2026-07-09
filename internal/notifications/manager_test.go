package notifications

import (
	"context"
	"fmt"
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
)

// --- minimal mock repos for notification tests ---

type mockApptRepoNotif struct {
	findByIDFn func(ctx context.Context, id string) (*domain.Appointment, error)
	confirmFn  func(ctx context.Context, id string, channel, channelID string) error
	cancelFn   func(ctx context.Context, id string, reason, channel, channelID string) error
}

func (m *mockApptRepoNotif) FindByID(ctx context.Context, id string) (*domain.Appointment, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *mockApptRepoNotif) FindUpcomingByPatient(ctx context.Context, patientID string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoNotif) FindPendingEmgAppointment(_ context.Context, _ string, _ []string) (*domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoNotif) FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoNotif) FindAgendasByDoctor(_ context.Context, _, _ string) ([]domain.AgendaSummary, error) {
	return nil, nil
}

func (m *mockApptRepoNotif) FindAgendaAppointmentsPaged(_ context.Context, _ domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error) {
	return &domain.AgendaAppointmentsPage{}, nil
}

func (m *mockApptRepoNotif) Create(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoNotif) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	return nil
}

func (m *mockApptRepoNotif) CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error {
	return nil
}

func (m *mockApptRepoNotif) Confirm(ctx context.Context, id string, channel, channelID string) error {
	if m.confirmFn != nil {
		return m.confirmFn(ctx, id, channel, channelID)
	}
	return nil
}

func (m *mockApptRepoNotif) Cancel(ctx context.Context, id string, reason, channel, channelID string) error {
	if m.cancelFn != nil {
		return m.cancelFn(ctx, id, reason, channel, channelID)
	}
	return nil
}

func (m *mockApptRepoNotif) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	if m.confirmFn != nil {
		for _, id := range ids {
			if err := m.confirmFn(ctx, id, channel, channelID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mockApptRepoNotif) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	if m.cancelFn != nil {
		for _, id := range ids {
			if err := m.cancelFn(ctx, id, reason, channel, channelID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mockApptRepoNotif) DeleteBatch(ctx context.Context, ids []string) error {
	return nil
}

func (m *mockApptRepoNotif) HasFutureForCup(ctx context.Context, patientID, cupCode string) (bool, error) {
	return false, nil
}

func (m *mockApptRepoNotif) FindLastDoctorForCups(ctx context.Context, patientID string, cups []string) (string, error) {
	return "", nil
}

func (m *mockApptRepoNotif) CountMonthlyByGroup(ctx context.Context, cupsCodes []string, year, month int) (int, error) {
	return 0, nil
}

func (m *mockApptRepoNotif) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoNotif) RescheduleDayOfAgenda(_ context.Context, _ domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
	return domain.RescheduleDayResult{}, nil
}

func (m *mockApptRepoNotif) FindDoctorAgendasOnDate(_ context.Context, _, _ string) ([]domain.DoctorAgendaOnDate, error) {
	return nil, nil
}

func (m *mockApptRepoNotif) SlotCountForAppointment(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *mockApptRepoNotif) WriteCreationAudit(_ context.Context, _, _ string) {}

func newTestBirdClient() (*bird.Client, *httptest.Server) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-ok"}`))
	}))
	return bird.NewClientForTest(srv.URL), srv
}

func sampleAppt() domain.Appointment {
	return domain.Appointment{
		ID:         "APT001",
		PatientID:  "PAT001",
		DoctorID:   "DOC001",
		DoctorName: "Dr. Garcia",
		Date:       time.Now().AddDate(0, 0, 1),
		TimeSlot:   "202603201000",
		AgendaID:   1,
		Procedures: []domain.AppointmentProcedure{
			{CupCode: "890271", CupName: "EMG", Quantity: 1},
		},
	}
}

// === Original tests ===

func TestNotificationManager_RegisterAndHandle(t *testing.T) {
	m := &NotificationManager{}
	m.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-1",
	})

	if !m.HasPending("+573001234567") {
		t.Fatal("expected pending notification to exist")
	}

	m.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	if m.HasPending("+573001234567") {
		t.Error("expected pending notification to be removed after handling")
	}
}

func TestNotificationManager_HandleResponse_NoPending(t *testing.T) {
	m := &NotificationManager{}
	m.HandleResponse("+573009999999", "confirm", "conv-1")
}

func TestNotificationManager_HasPending(t *testing.T) {
	m := &NotificationManager{}

	if m.HasPending("+573001111111") {
		t.Error("expected no pending before register")
	}

	m.RegisterPending(PendingNotification{
		Type:  "cancellation",
		Phone: "+573001111111",
	})

	if !m.HasPending("+573001111111") {
		t.Error("expected pending after register")
	}

	if m.HasPending("+573002222222") {
		t.Error("expected no pending for different phone")
	}
}

func TestNotificationManager_PendingCount(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{ConfirmFollowup1Hours: 3}
	m := NewNotificationManager(birdClient, nil, cfg)

	if m.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", m.PendingCount())
	}

	phones := []string{"+573001111111", "+573002222222", "+573003333333"}
	for _, phone := range phones {
		m.RegisterPending(PendingNotification{
			Type:  "confirmation",
			Phone: phone,
		})
	}

	if m.PendingCount() != 3 {
		t.Errorf("expected 3 pending, got %d", m.PendingCount())
	}
}

func TestNormalizePostback(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"confirm", "confirm"},
		{"cancelar", "cancel"},
		{"cancel", "cancel"},
		{"understood", "acknowledge"},
		{"reschedule", "reschedule"},
		{"reprogramar", "reschedule"},
		{"wl_schedule", "schedule"},
		{"wl_decline", "decline"},
		{"random_payload", "random_payload"},
		{"", ""},
	}

	for _, tc := range cases {
		got := normalizePostback(tc.input)
		if got != tc.expected {
			t.Errorf("normalizePostback(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// === New Batch 6 tests ===

func TestHandleConfirmation_Confirm(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.ID = id
			return &appt, nil
		},
		confirmFn: func(ctx context.Context, id, source, chID string) error {
			return nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}

	mgr := NewNotificationManager(birdClient, apptSvc, cfg)
	mgr.RegisterPending(PendingNotification{
		Type:          "confirmation",
		Phone:         "+573001234567",
		AppointmentID: "APT001",
	})

	mgr.HandleResponse("+573001234567", "confirm", "conv-1")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted after confirm")
	}
}

func TestHandleConfirmation_Cancel(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.ID = id
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}

	mgr := NewNotificationManager(birdClient, apptSvc, cfg)
	mgr.RegisterPending(PendingNotification{
		Type:          "confirmation",
		Phone:         "+573001234567",
		AppointmentID: "APT001",
	})

	mgr.HandleResponse("+573001234567", "cancelar", "conv-1")

	// Cancel now goes through state machine session, not direct cancel.
	// Pending should still be consumed by HandleResponse.
	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted after cancel")
	}
}

func TestHandleReschedule_Confirm(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	confirmCalled := false
	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			return &appt, nil
		},
		confirmFn: func(ctx context.Context, id, source, chID string) error {
			confirmCalled = true
			return nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}

	mgr := NewNotificationManager(birdClient, apptSvc, cfg)
	mgr.RegisterPending(PendingNotification{
		Type:          "reschedule",
		Phone:         "+573001234567",
		AppointmentID: "APT001",
	})

	mgr.HandleResponse("+573001234567", "confirm", "conv-1")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted")
	}
	if !confirmCalled {
		t.Error("expected confirm to be called")
	}
}

func TestHandleReschedule_Cancel(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			return &appt, nil
		},
		cancelFn: func(ctx context.Context, id, reason, source, chID string) error {
			return nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}

	mgr := NewNotificationManager(birdClient, apptSvc, cfg)
	mgr.RegisterPending(PendingNotification{
		Type:          "reschedule",
		Phone:         "+573001234567",
		AppointmentID: "APT001",
	})

	mgr.HandleResponse("+573001234567", "cancel", "conv-1")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted")
	}
}

func TestHandleCancellation_Acknowledge(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.RegisterPending(PendingNotification{
		Type:          "cancellation",
		Phone:         "+573001234567",
		AppointmentID: "APT001",
	})

	mgr.HandleResponse("+573001234567", "understood", "conv-1")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted after acknowledge")
	}
}

func TestHandleCancellation_Reschedule(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.ID = id
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(nil, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "cancellation",
		Phone:         "+573001234567",
		AppointmentID: "APT001",
	})

	mgr.HandleResponse("+573001234567", "reprogramar", "conv-1")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted after reschedule request")
	}

	// Verify self-reschedule was triggered (session created, enqueue called)
	if sessRepo.createdSession == nil {
		t.Fatal("expected session to be created for self-reschedule")
	}
	if sessRepo.createdSession.CurrentState != "SEARCH_SLOTS" {
		t.Errorf("expected state SEARCH_SLOTS, got %s", sessRepo.createdSession.CurrentState)
	}

	// Verify skipCancel=true for cancellation flow
	kvs := sessRepo.batchKVs
	if kvs["reschedule_skip_cancel"] != "1" {
		t.Errorf("expected reschedule_skip_cancel=1, got %s", kvs["reschedule_skip_cancel"])
	}

	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 1 {
		t.Fatalf("expected 1 EnqueueVirtual call, got %d", len(enqueuer.calls))
	}
}

// Cambio 14: Escalation chain tests

func TestHandleTimeout_Confirmation_Step0_FollowUp1(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		ConfirmFollowupEnabled: true, // escalation chain only runs when enabled
		ConfirmFollowup1Hours:  3,
		ConfirmFollowup2Hours:  3,
		ConfirmPostIVRMinutes:  30,
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:       "confirmation",
		Phone:      "+573001234567",
		RetryCount: 0,
	})

	mgr.handleTimeout("+573001234567")

	if !mgr.HasPending("+573001234567") {
		t.Error("expected pending to exist after follow-up 1")
	}
	val, _ := mgr.pending.Load("+573001234567")
	p := val.(*PendingNotification)
	if p.RetryCount != 1 {
		t.Errorf("expected retry count 1, got %d", p.RetryCount)
	}
}

func TestHandleTimeout_Confirmation_Step1_FollowUp2(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		ConfirmFollowupEnabled: true, // escalation chain only runs when enabled
		ConfirmFollowup1Hours:  3,
		ConfirmFollowup2Hours:  3,
		ConfirmPostIVRMinutes:  30,
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:       "confirmation",
		Phone:      "+573001234567",
		RetryCount: 1,
	})

	mgr.handleTimeout("+573001234567")

	if !mgr.HasPending("+573001234567") {
		t.Error("expected pending to exist after follow-up 2")
	}
	val, _ := mgr.pending.Load("+573001234567")
	p := val.(*PendingNotification)
	if p.RetryCount != 2 {
		t.Errorf("expected retry count 2, got %d", p.RetryCount)
	}
}

func TestHandleTimeout_Confirmation_Step2_SafetyEscalation(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{
		BirdTeamFallback:       "team-fallback",
		ConfirmFollowupEnabled: true, // escalation chain only runs when enabled
		ConfirmFollowup1Hours:  3,
		ConfirmFollowup2Hours:  3,
		ConfirmPostIVRMinutes:  30,
	}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:           "confirmation",
		Phone:          "+573001234567",
		AppointmentID:  "APT001",
		ConversationID: "conv-1",
		RetryCount:     2, // IVR didn't run → safety escalation
	})

	mgr.handleTimeout("+573001234567")

	// After escalation, pending should be deleted (LoadAndDelete in handleTimeout)
	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted after agent escalation")
	}
}

func TestHandleTimeout_Confirmation_Step3_PostIVR(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{
		BirdTeamFallback:       "team-fallback",
		ConfirmFollowupEnabled: true, // escalation chain only runs when enabled
		ConfirmFollowup1Hours:  3,
		ConfirmFollowup2Hours:  3,
		ConfirmPostIVRMinutes:  30,
	}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:           "confirmation",
		Phone:          "+573001234567",
		AppointmentID:  "APT001",
		ConversationID: "conv-1",
		RetryCount:     3, // Post-IVR timeout
	})

	mgr.handleTimeout("+573001234567")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted after post-IVR escalation")
	}
}

func TestHandleTimeout_Confirmation_EscalateNoAppt(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			return nil, nil // Not found
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{BirdTeamFallback: "team-fallback"}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:           "confirmation",
		Phone:          "+573001234567",
		AppointmentID:  "APT-MISSING",
		ConversationID: "conv-1",
		RetryCount:     3,
	})

	// Should not panic even when appointment not found
	mgr.handleTimeout("+573001234567")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted")
	}
}

func TestGetPendingForIVR(t *testing.T) {
	// Followup chain enabled → IVR targets are those who completed the WA chain (RetryCount==2).
	cfg := &config.Config{ConfirmFollowupEnabled: true}
	mgr := NewNotificationManager(nil, nil, cfg)

	// confirmation retry=2 → should be returned
	mgr.pending.Store("+573001111111", &PendingNotification{
		Type: "confirmation", Phone: "+573001111111", RetryCount: 2,
	})
	// reschedule retry=2 → should be returned
	mgr.pending.Store("+573002222222", &PendingNotification{
		Type: "reschedule", Phone: "+573002222222", RetryCount: 2,
	})
	// confirmation retry=1 → NOT returned (not ready)
	mgr.pending.Store("+573003333333", &PendingNotification{
		Type: "confirmation", Phone: "+573003333333", RetryCount: 1,
	})
	// waiting_list retry=2 → NOT returned (wrong type)
	mgr.pending.Store("+573004444444", &PendingNotification{
		Type: "waiting_list", Phone: "+573004444444", RetryCount: 2,
	})
	// confirmation retry=3 → NOT returned (already past IVR)
	mgr.pending.Store("+573005555555", &PendingNotification{
		Type: "confirmation", Phone: "+573005555555", RetryCount: 3,
	})

	targets := mgr.GetPendingForIVR()
	if len(targets) != 2 {
		t.Errorf("expected 2 IVR targets, got %d", len(targets))
	}

	// Verify the correct phones are in the result
	phones := make(map[string]bool)
	for _, p := range targets {
		phones[p.Phone] = true
	}
	if !phones["+573001111111"] {
		t.Error("expected +573001111111 in IVR targets")
	}
	if !phones["+573002222222"] {
		t.Error("expected +573002222222 in IVR targets")
	}
}

// TestNotificationManager_ConcurrentPendingNoRace cubre N-17: varias goroutines golpean en
// paralelo, sobre el MISMO teléfono, los métodos que leen/mutan los campos del *PendingNotification
// (RegisterPending, RegisterCallID, MarkIVRSent, GetPendingForIVR). Con el candado por-teléfono
// del manager no debe haber data race. Correr con -race; sin el lock, el detector dispara.
func TestNotificationManager_ConcurrentPendingNoRace(t *testing.T) {
	cfg := &config.Config{} // ConfirmFollowupEnabled=false → sin I/O de seguimiento
	mgr := NewNotificationManager(nil, nil, cfg)
	phone := "+573001234567"

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 60; j++ {
				switch (i + j) % 4 {
				case 0:
					mgr.RegisterPending(PendingNotification{
						Type: "confirmation", Phone: phone,
						AppointmentID: "APT-1", ConversationID: "conv-1",
					})
				case 1:
					mgr.RegisterCallID("call-x", phone)
				case 2:
					mgr.MarkIVRSent(phone)
				case 3:
					_ = mgr.GetPendingForIVR()
				}
			}
		}(i)
	}
	wg.Wait()

	if p, ok := mgr.LoadPendingForTest(phone); ok && p.Timer != nil {
		p.Timer.Stop()
	}

	// Invariante: un solo teléfono ⇒ a lo sumo una entrada pendiente (nunca duplicados).
	if n := mgr.PendingCount(); n > 1 {
		t.Errorf("expected at most 1 pending entry for a single phone, got %d", n)
	}
}

func TestMarkIVRSent(t *testing.T) {
	// Followup chain enabled → IVR is not the last step; pending survives with a post-IVR timer.
	cfg := &config.Config{ConfirmFollowupEnabled: true, ConfirmPostIVRMinutes: 30}
	mgr := NewNotificationManager(nil, nil, cfg)

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:       "confirmation",
		Phone:      "+573001234567",
		RetryCount: 2,
	})

	mgr.MarkIVRSent("+573001234567")

	val, ok := mgr.pending.Load("+573001234567")
	if !ok {
		t.Fatal("expected pending to still exist after MarkIVRSent")
	}
	p := val.(*PendingNotification)
	if p.RetryCount != 3 {
		t.Errorf("expected retry count 3, got %d", p.RetryCount)
	}
	if p.Timer == nil {
		t.Error("expected new timer to be set")
	}
	p.Timer.Stop()
}

func TestMarkIVRSent_NotFound(t *testing.T) {
	cfg := &config.Config{ConfirmPostIVRMinutes: 30}
	mgr := NewNotificationManager(nil, nil, cfg)

	// Should not panic when phone not found
	mgr.MarkIVRSent("+573009999999")

	if mgr.HasPending("+573009999999") {
		t.Error("expected no pending for non-existent phone")
	}
}

// TestRegisterPending_ReturnsTrueOnSuccess (L12): RegisterPending ahora reporta si almacenó.
func TestRegisterPending_ReturnsTrueOnSuccess(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()
	mgr := NewNotificationManager(birdClient, nil, &config.Config{})

	ok := mgr.RegisterPending(PendingNotification{Type: "waiting_list", Phone: "+573001234567", WaitingListID: "wl-1"})
	if !ok {
		t.Fatal("esperaba true al registrar con éxito")
	}
	if !mgr.HasPending("+573001234567") {
		t.Error("esperaba pending almacenado")
	}
	if p, okp := mgr.LoadPendingForTest("+573001234567"); okp && p.Timer != nil {
		p.Timer.Stop()
	}
}

func TestRegisterPending_ConfirmationTimer(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{ConfirmFollowup1Hours: 3}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	mgr.RegisterPending(PendingNotification{
		Type:  "confirmation",
		Phone: "+573001234567",
	})

	val, ok := mgr.pending.Load("+573001234567")
	if !ok {
		t.Fatal("expected pending to be stored")
	}
	p := val.(*PendingNotification)
	if p.Timer == nil {
		t.Error("expected timer to be set")
	}
	p.Timer.Stop()
}

func TestRegisterPending_WaitingListStill6h(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{ConfirmFollowup1Hours: 3}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	mgr.RegisterPending(PendingNotification{
		Type:  "waiting_list",
		Phone: "+573009876543",
	})

	val, ok := mgr.pending.Load("+573009876543")
	if !ok {
		t.Fatal("expected pending to be stored")
	}
	p := val.(*PendingNotification)
	if p.Timer == nil {
		t.Error("expected timer to be set")
	}
	p.Timer.Stop()
}

func TestHandleTimeout_Cancellation(t *testing.T) {
	mgr := &NotificationManager{}

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:  "cancellation",
		Phone: "+573001234567",
	})

	mgr.handleTimeout("+573001234567")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be deleted for cancellation timeout")
	}
}

func TestBoolToStr(t *testing.T) {
	if boolToStr(true) != "1" {
		t.Error("expected '1' for true")
	}
	if boolToStr(false) != "0" {
		t.Error("expected '0' for false")
	}
}

// === Waiting list mock structs ===

type mockWaitingListFinder struct {
	mu                  sync.Mutex
	findByIDFn          func(ctx context.Context, id string) (*domain.WaitingListEntry, error)
	updateStatusFn      func(ctx context.Context, id, status string) error
	resolveIfNotifiedFn func(ctx context.Context, id, status string) (bool, error)
	updatedID           string
	updatedStatus       string
}

func (m *mockWaitingListFinder) FindByID(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *mockWaitingListFinder) UpdateStatus(ctx context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedID = id
	m.updatedStatus = status
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, id, status)
	}
	return nil
}

func (m *mockWaitingListFinder) ResolveIfNotified(ctx context.Context, id, status string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolveIfNotifiedFn != nil {
		return m.resolveIfNotifiedFn(ctx, id, status)
	}
	// Por defecto simula que la entrada seguía 'notified' y se resolvió: registra igual que UpdateStatus.
	m.updatedID = id
	m.updatedStatus = status
	return true, nil
}

type mockSessionCreator struct {
	mu             sync.Mutex
	createFn       func(ctx context.Context, s *session.Session) error
	setCtxBatchFn  func(ctx context.Context, sessionID string, kvs map[string]string) error
	createdSession *session.Session
	batchSessionID string
	batchKVs       map[string]string
}

func (m *mockSessionCreator) Create(ctx context.Context, s *session.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdSession = s
	if m.createFn != nil {
		return m.createFn(ctx, s)
	}
	return nil
}

func (m *mockSessionCreator) SetContextBatch(ctx context.Context, sessionID string, kvs map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchSessionID = sessionID
	m.batchKVs = kvs
	if m.setCtxBatchFn != nil {
		return m.setCtxBatchFn(ctx, sessionID, kvs)
	}
	return nil
}

func (m *mockSessionCreator) UpdateStatus(ctx context.Context, sessionID, status string) error {
	return nil
}

func (m *mockSessionCreator) CompleteActiveByPhone(ctx context.Context, phone string) error {
	return nil
}

type mockVirtualEnqueuer struct {
	mu    sync.Mutex
	calls []string
}

func (m *mockVirtualEnqueuer) EnqueueVirtual(phone string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, phone)
}

func sampleWaitingListEntry() *domain.WaitingListEntry {
	return &domain.WaitingListEntry{
		ID:             "wl-1",
		PhoneNumber:    "+573001234567",
		PatientID:      "PAT-100",
		PatientDoc:     "1234567890",
		PatientName:    "Juan Perez",
		PatientAge:     45,
		PatientGender:  "M",
		PatientEntity:  "EPS-TEST",
		CupsCode:       "890271",
		CupsName:       "Electromiografia",
		IsContrasted:   false,
		IsSedated:      false,
		Espacios:       1,
		ProceduresJSON: `[{"code":"890271","name":"Electromiografia","qty":1}]`,
		Status:         "notified",
	}
}

// === Waiting list tests ===

func TestSetWaitingListDeps(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	// Before setting deps: HandleResponse with waiting_list should not crash
	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001111111",
		WaitingListID: "wl-pre",
	})
	// This should silently do nothing (deps nil → skipped)
	mgr.HandleResponse("+573001111111", "wl_schedule", "conv-1")
	// Pending was consumed by HandleResponse (LoadAndDelete removes it)
	if mgr.HasPending("+573001111111") {
		t.Error("expected pending to be removed after HandleResponse")
	}

	// Now set deps and verify processing works
	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
			return sampleWaitingListEntry(), nil
		},
	}
	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573002222222",
		WaitingListID: "wl-post",
	})
	mgr.HandleResponse("+573002222222", "wl_schedule", "conv-2")

	if sessRepo.createdSession == nil {
		t.Fatal("expected session to be created after SetWaitingListDeps")
	}
	if len(enqueuer.calls) != 1 {
		t.Errorf("expected 1 EnqueueVirtual call, got %d", len(enqueuer.calls))
	}
}

func TestHandleWaitingList_Schedule(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	entry := sampleWaitingListEntry()
	entry.GfrCreatinine = 1.2
	entry.GfrHeightCm = 170
	entry.GfrWeightKg = 75.5
	entry.GfrDiseaseType = "tipo1"
	entry.GfrCalculated = 90.5
	entry.IsPregnant = true
	entry.BabyWeightCat = "normal"
	entry.PreferredDoctorDoc = "DOC-999"

	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
			return entry, nil
		},
	}
	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-1",
	})

	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	// Verify session created
	if sessRepo.createdSession == nil {
		t.Fatal("expected session to be created")
	}
	if sessRepo.createdSession.CurrentState != "SEARCH_SLOTS" {
		t.Errorf("expected state SEARCH_SLOTS, got %s", sessRepo.createdSession.CurrentState)
	}
	if sessRepo.createdSession.Status != session.StatusActive {
		t.Errorf("expected status active, got %s", sessRepo.createdSession.Status)
	}
	if sessRepo.createdSession.PhoneNumber != "+573001234567" {
		t.Errorf("expected phone +573001234567, got %s", sessRepo.createdSession.PhoneNumber)
	}

	// Verify context batch was set
	if sessRepo.batchSessionID == "" {
		t.Fatal("expected SetContextBatch to be called")
	}
	kvs := sessRepo.batchKVs
	if kvs["patient_id"] != "PAT-100" {
		t.Errorf("expected patient_id=PAT-100, got %s", kvs["patient_id"])
	}
	if kvs["patient_doc"] != "1234567890" {
		t.Errorf("expected patient_doc=1234567890, got %s", kvs["patient_doc"])
	}
	if kvs["patient_name"] != "Juan Perez" {
		t.Errorf("expected patient_name=Juan Perez, got %s", kvs["patient_name"])
	}
	if kvs["patient_age"] != "45" {
		t.Errorf("expected patient_age=45, got %s", kvs["patient_age"])
	}
	if kvs["cups_code"] != "890271" {
		t.Errorf("expected cups_code=890271, got %s", kvs["cups_code"])
	}
	if kvs["cups_name"] != "Electromiografia" {
		t.Errorf("expected cups_name=Electromiografia, got %s", kvs["cups_name"])
	}
	if kvs["menu_option"] != "agendar" {
		t.Errorf("expected menu_option=agendar, got %s", kvs["menu_option"])
	}
	if kvs["waiting_list_entry_id"] != "wl-1" {
		t.Errorf("expected waiting_list_entry_id=wl-1, got %s", kvs["waiting_list_entry_id"])
	}

	// Verify GFR data
	if kvs["gfr_creatinine"] != "1.20" {
		t.Errorf("expected gfr_creatinine=1.20, got %s", kvs["gfr_creatinine"])
	}
	if kvs["gfr_height_cm"] != "170" {
		t.Errorf("expected gfr_height_cm=170, got %s", kvs["gfr_height_cm"])
	}
	if kvs["gfr_weight_kg"] != "75.5" {
		t.Errorf("expected gfr_weight_kg=75.5, got %s", kvs["gfr_weight_kg"])
	}
	if kvs["gfr_disease_type"] != "tipo1" {
		t.Errorf("expected gfr_disease_type=tipo1, got %s", kvs["gfr_disease_type"])
	}
	if kvs["gfr_calculated"] != "90.5" {
		t.Errorf("expected gfr_calculated=90.5, got %s", kvs["gfr_calculated"])
	}

	// Verify extras
	if kvs["is_pregnant"] != "1" {
		t.Errorf("expected is_pregnant=1, got %s", kvs["is_pregnant"])
	}
	if kvs["baby_weight_cat"] != "normal" {
		t.Errorf("expected baby_weight_cat=normal, got %s", kvs["baby_weight_cat"])
	}
	if kvs["preferred_doctor_doc"] != "DOC-999" {
		t.Errorf("expected preferred_doctor_doc=DOC-999, got %s", kvs["preferred_doctor_doc"])
	}

	// Verify EnqueueVirtual called
	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 1 {
		t.Fatalf("expected 1 EnqueueVirtual call, got %d", len(enqueuer.calls))
	}
	if enqueuer.calls[0] != "+573001234567" {
		t.Errorf("expected EnqueueVirtual(+573001234567), got %s", enqueuer.calls[0])
	}

	// Verify pending removed
	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be removed after schedule")
	}
}

func TestHandleWaitingList_Schedule_NotFound(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
			return nil, nil // not found
		},
	}
	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-missing",
	})

	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	// Session should NOT be created since entry was nil
	if sessRepo.createdSession != nil {
		t.Error("expected no session creation when entry not found")
	}

	// EnqueueVirtual should NOT be called
	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 0 {
		t.Errorf("expected 0 EnqueueVirtual calls, got %d", len(enqueuer.calls))
	}
}

func TestHandleWaitingList_Schedule_FindByIDError(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
			return nil, fmt.Errorf("db connection error")
		},
	}
	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-err",
	})

	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	if sessRepo.createdSession != nil {
		t.Error("expected no session when FindByID errors")
	}
}

func TestHandleWaitingList_Schedule_CreateSessionError(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
			return sampleWaitingListEntry(), nil
		},
	}
	sessRepo := &mockSessionCreator{
		createFn: func(ctx context.Context, s *session.Session) error {
			return fmt.Errorf("session create error")
		},
	}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-1",
	})

	// Should not panic
	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	// EnqueueVirtual should NOT be called since session creation failed
	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 0 {
		t.Errorf("expected 0 EnqueueVirtual calls on create error, got %d", len(enqueuer.calls))
	}
}

func TestHandleWaitingList_Schedule_SetContextBatchError(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
			return sampleWaitingListEntry(), nil
		},
	}
	sessRepo := &mockSessionCreator{
		setCtxBatchFn: func(ctx context.Context, sessionID string, kvs map[string]string) error {
			return fmt.Errorf("batch context error")
		},
	}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-1",
	})

	// Should not panic
	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	// Session was created but context batch failed; EnqueueVirtual should NOT be called
	if sessRepo.createdSession == nil {
		t.Error("expected session to be created")
	}
	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 0 {
		t.Errorf("expected 0 EnqueueVirtual calls on batch error, got %d", len(enqueuer.calls))
	}
}

func TestHandleWaitingList_Decline(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{}
	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:           "waiting_list",
		Phone:          "+573001234567",
		WaitingListID:  "wl-decline-1",
		BirdMessageID:  "bird-msg-1",
		ConversationID: "conv-decline",
	})

	mgr.HandleResponse("+573001234567", "wl_decline", "conv-decline")

	// Verify UpdateStatus called with "declined"
	wlRepo.mu.Lock()
	defer wlRepo.mu.Unlock()
	if wlRepo.updatedID != "wl-decline-1" {
		t.Errorf("expected UpdateStatus id=wl-decline-1, got %s", wlRepo.updatedID)
	}
	if wlRepo.updatedStatus != "declined" {
		t.Errorf("expected UpdateStatus status=declined, got %s", wlRepo.updatedStatus)
	}

	// Session should NOT be created for decline
	if sessRepo.createdSession != nil {
		t.Error("expected no session creation for decline")
	}

	// Pending should be removed
	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be removed after decline")
	}
}

func TestHandleWaitingListTimeout(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{}
	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	// Directly store pending to avoid the real 6h timer
	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-timeout-1",
		Timer:         time.NewTimer(999 * time.Hour), // dummy, won't fire
	})

	// Simulate timeout
	mgr.handleTimeout("+573001234567")

	// Verify UpdateStatus called with "expired"
	wlRepo.mu.Lock()
	defer wlRepo.mu.Unlock()
	if wlRepo.updatedID != "wl-timeout-1" {
		t.Errorf("expected UpdateStatus id=wl-timeout-1, got %s", wlRepo.updatedID)
	}
	if wlRepo.updatedStatus != "expired" {
		t.Errorf("expected UpdateStatus status=expired, got %s", wlRepo.updatedStatus)
	}

	// Pending should be removed
	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be removed after timeout")
	}
}

func TestHandleWaitingListTimeout_NilDeps(t *testing.T) {
	// When waitingListRepo is nil, timeout should just delete the pending
	mgr := &NotificationManager{}

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-nil-deps",
		Timer:         time.NewTimer(999 * time.Hour),
	})

	// Should not panic
	mgr.handleTimeout("+573001234567")

	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be removed even with nil deps")
	}
}

// El paciente agendó la oferta desde el bot (entrada → 'scheduled') mientras el timer de 6h seguía
// vivo. Al dispararse el timeout, ResolveIfNotified NO la encuentra en 'notified' → NO debe degradarla.
func TestHandleWaitingListTimeout_SkipsWhenAlreadyScheduled(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{
		resolveIfNotifiedFn: func(_ context.Context, _, _ string) (bool, error) {
			return false, nil // ya no está 'notified' (fue agendada → 'scheduled')
		},
	}
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, &mockSessionCreator{}, &mockVirtualEnqueuer{})

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-scheduled-1",
		Timer:         time.NewTimer(999 * time.Hour),
	})

	mgr.handleTimeout("+573001234567")

	wlRepo.mu.Lock()
	defer wlRepo.mu.Unlock()
	if wlRepo.updatedStatus != "" {
		t.Errorf("no debía degradar una entrada ya resuelta, pero updatedStatus=%q", wlRepo.updatedStatus)
	}
	if mgr.HasPending("+573001234567") {
		t.Error("el pending debe eliminarse igual tras el timeout")
	}
}

// Igual para decline: si la entrada ya fue agendada desde el bot, un decline tardío no debe degradarla.
func TestHandleWaitingList_Decline_SkipsWhenAlreadyScheduled(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	wlRepo := &mockWaitingListFinder{
		resolveIfNotifiedFn: func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		},
	}
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, &mockSessionCreator{}, &mockVirtualEnqueuer{})

	mgr.RegisterPending(PendingNotification{
		Type:           "waiting_list",
		Phone:          "+573001234567",
		WaitingListID:  "wl-scheduled-2",
		ConversationID: "conv-decline",
	})

	mgr.HandleResponse("+573001234567", "wl_decline", "conv-decline")

	wlRepo.mu.Lock()
	defer wlRepo.mu.Unlock()
	if wlRepo.updatedStatus != "" {
		t.Errorf("no debía degradar a 'declined' una entrada ya resuelta, updatedStatus=%q", wlRepo.updatedStatus)
	}
}

func TestHandleResponse_WaitingList_NilDeps(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	// Do NOT call SetWaitingListDeps

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-nil",
	})

	// Should not crash; pending is consumed by LoadAndDelete but handler is skipped
	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	// Pending removed by LoadAndDelete in HandleResponse
	if mgr.HasPending("+573001234567") {
		t.Error("expected pending to be consumed by HandleResponse even with nil deps")
	}
}

// === Self-reschedule tests ===

func TestSelfReschedule_FromConfirmation(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.ID = id
			appt.PatientID = "PAT-200"
			appt.Entity = "EPS-GOLD"
			appt.DoctorID = "55"            // cod_medi interno — NO debe usarse para preferred_doctor_doc
			appt.DoctorDocument = "DOC-555" // cédula — el valor correcto (N14)
			appt.Observations = "Contrastada y Sedacion"
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(nil, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:           "confirmation",
		Phone:          "+573001234567",
		AppointmentID:  "APT-CONF-1",
		ConversationID: "conv-conf",
		BirdMessageID:  "bird-conf",
	})

	mgr.HandleResponse("+573001234567", "reprogramar", "conv-conf")

	// Session should be created at CONFIRM_RESCHEDULE_NOTIF (confirmation step before search)
	if sessRepo.createdSession == nil {
		t.Fatal("expected session to be created")
	}
	if sessRepo.createdSession.CurrentState != "CONFIRM_RESCHEDULE_NOTIF" {
		t.Errorf("expected CONFIRM_RESCHEDULE_NOTIF, got %s", sessRepo.createdSession.CurrentState)
	}

	kvs := sessRepo.batchKVs
	if kvs["patient_id"] != "PAT-200" {
		t.Errorf("expected patient_id=PAT-200, got %s", kvs["patient_id"])
	}
	if kvs["patient_entity"] != "EPS-GOLD" {
		t.Errorf("expected patient_entity=EPS-GOLD, got %s", kvs["patient_entity"])
	}
	if kvs["patient_age"] != "0" {
		t.Errorf("expected patient_age=0, got %s", kvs["patient_age"])
	}
	if kvs["cups_code"] != "890271" {
		t.Errorf("expected cups_code=890271, got %s", kvs["cups_code"])
	}
	if kvs["is_contrasted"] != "1" {
		t.Errorf("expected is_contrasted=1, got %s", kvs["is_contrasted"])
	}
	if kvs["is_sedated"] != "1" {
		t.Errorf("expected is_sedated=1, got %s", kvs["is_sedated"])
	}
	if kvs["preferred_doctor_doc"] != "DOC-555" {
		t.Errorf("expected preferred_doctor_doc=DOC-555, got %s", kvs["preferred_doctor_doc"])
	}
	if kvs["reschedule_appt_id"] != "APT-CONF-1" {
		t.Errorf("expected reschedule_appt_id=APT-CONF-1, got %s", kvs["reschedule_appt_id"])
	}
	// Confirmation flow: skipCancel=false
	if kvs["reschedule_skip_cancel"] != "0" {
		t.Errorf("expected reschedule_skip_cancel=0, got %s", kvs["reschedule_skip_cancel"])
	}
	if kvs["menu_option"] != "agendar" {
		t.Errorf("expected menu_option=agendar, got %s", kvs["menu_option"])
	}

	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 1 || enqueuer.calls[0] != "+573001234567" {
		t.Errorf("expected EnqueueVirtual(+573001234567), got %v", enqueuer.calls)
	}
}

func TestSelfReschedule_FromRescheduleFlow(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.ID = id
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(nil, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "reschedule",
		Phone:         "+573005555555",
		AppointmentID: "APT-RSCH-1",
	})

	mgr.HandleResponse("+573005555555", "reprogramar", "conv-rsch")

	if sessRepo.createdSession == nil {
		t.Fatal("expected session to be created")
	}

	kvs := sessRepo.batchKVs
	// Reschedule flow: skipCancel=false (old rescheduled appt gets cancelled)
	if kvs["reschedule_skip_cancel"] != "0" {
		t.Errorf("expected reschedule_skip_cancel=0, got %s", kvs["reschedule_skip_cancel"])
	}
	if kvs["reschedule_appt_id"] != "APT-RSCH-1" {
		t.Errorf("expected reschedule_appt_id=APT-RSCH-1, got %s", kvs["reschedule_appt_id"])
	}
}

func TestSelfReschedule_NoProcedures(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.Procedures = nil // No procedures
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{BirdTeamFallback: "team-fallback"}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(nil, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "confirmation",
		Phone:         "+573006666666",
		AppointmentID: "APT-NOPROC",
	})

	mgr.HandleResponse("+573006666666", "reprogramar", "conv-noproc")

	// Session is created even with no procedures (at CONFIRM_RESCHEDULE_NOTIF)
	if sessRepo.createdSession == nil {
		t.Fatal("expected session to be created")
	}
	if sessRepo.createdSession.CurrentState != "CONFIRM_RESCHEDULE_NOTIF" {
		t.Errorf("expected CONFIRM_RESCHEDULE_NOTIF, got %s", sessRepo.createdSession.CurrentState)
	}

	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 1 {
		t.Errorf("expected 1 EnqueueVirtual call, got %d", len(enqueuer.calls))
	}
}

func TestSelfReschedule_MissingDeps(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)
	// Do NOT call SetWaitingListDeps → sessionRepo and workerPool are nil

	mgr.RegisterPending(PendingNotification{
		Type:          "confirmation",
		Phone:         "+573007777777",
		AppointmentID: "APT-NODEPS",
	})

	// Should not panic, should send error message
	mgr.HandleResponse("+573007777777", "reprogramar", "conv-nodeps")
}

func TestSelfReschedule_ApptNotFound(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			return nil, nil // Not found
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(nil, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "cancellation",
		Phone:         "+573008888888",
		AppointmentID: "APT-MISSING",
	})

	mgr.HandleResponse("+573008888888", "reprogramar", "conv-missing")

	// Should NOT create session
	if sessRepo.createdSession != nil {
		t.Error("expected no session when appointment not found")
	}

	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 0 {
		t.Errorf("expected 0 EnqueueVirtual calls, got %d", len(enqueuer.calls))
	}
}

func TestSelfReschedule_BlockSize(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	// FindByAgendaAndDate returns 2 consecutive appointments → block size = 2
	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.ID = id
			appt.AgendaID = 5
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(nil, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "reschedule",
		Phone:         "+573009999999",
		AppointmentID: "APT001",
	})

	mgr.HandleResponse("+573009999999", "reprogramar", "conv-block")

	if sessRepo.createdSession == nil {
		t.Fatal("expected session to be created")
	}

	// FindByAgendaAndDate returns nil → fallback to single appointment → espacios=1
	kvs := sessRepo.batchKVs
	if kvs["espacios"] != "1" {
		t.Errorf("expected espacios=1 (single fallback), got %s", kvs["espacios"])
	}
}

func TestSelfReschedule_CreateSessionError(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			return &appt, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, apptSvc, cfg)

	sessRepo := &mockSessionCreator{
		createFn: func(ctx context.Context, s *session.Session) error {
			return fmt.Errorf("session create error")
		},
	}
	enqueuer := &mockVirtualEnqueuer{}
	mgr.SetWaitingListDeps(nil, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "confirmation",
		Phone:         "+573001010101",
		AppointmentID: "APT-ERR",
	})

	// Should not panic
	mgr.HandleResponse("+573001010101", "reprogramar", "conv-err")

	// EnqueueVirtual should NOT be called
	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if len(enqueuer.calls) != 0 {
		t.Errorf("expected 0 EnqueueVirtual calls on session create error, got %d", len(enqueuer.calls))
	}
}

// === Cambio 13: CheckWaitingListForCups tests ===

type mockSlotSearcher struct {
	getSlotsFn func(ctx context.Context, query services.SlotQuery) ([]services.AvailableSlot, error)
}

func (m *mockSlotSearcher) GetAvailableSlots(ctx context.Context, query services.SlotQuery) ([]services.AvailableSlot, error) {
	if m.getSlotsFn != nil {
		return m.getSlotsFn(ctx, query)
	}
	return nil, nil
}

type mockFutureApptChecker struct {
	hasFutureFn func(ctx context.Context, patientID, cupCode string) (bool, error)
}

func (m *mockFutureApptChecker) HasFutureForCup(ctx context.Context, patientID, cupCode string) (bool, error) {
	if m.hasFutureFn != nil {
		return m.hasFutureFn(ctx, patientID, cupCode)
	}
	return false, nil
}

type mockWLChecker struct {
	mu             sync.Mutex
	getWaitingFn   func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	getWaitingInFn func(ctx context.Context, cupsCodes []string, limit int) ([]domain.WaitingListEntry, error)
	markNotifiedFn func(ctx context.Context, id string) (bool, error)
	updateStatusFn func(ctx context.Context, id, status string) error
	notifiedID     string
	updatedID      string
	updatedStatus  string
}

func (m *mockWLChecker) GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
	if m.getWaitingFn != nil {
		return m.getWaitingFn(ctx, cupsCode, limit)
	}
	return nil, nil
}

func (m *mockWLChecker) GetWaitingByCupsIn(ctx context.Context, cupsCodes []string, limit int) ([]domain.WaitingListEntry, error) {
	if m.getWaitingInFn != nil {
		return m.getWaitingInFn(ctx, cupsCodes, limit)
	}
	return nil, nil
}

func (m *mockWLChecker) MarkNotified(ctx context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifiedID = id
	if m.markNotifiedFn != nil {
		return m.markNotifiedFn(ctx, id)
	}
	return true, nil
}

func (m *mockWLChecker) UpdateStatus(ctx context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedID = id
	m.updatedStatus = status
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, id, status)
	}
	return nil
}

func TestCheckWaitingListForCups_HappyPath(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
		BirdTemplateWaitingListVersionID: "ver-wl-456",
		BirdTemplateWaitingListLocale:    "es-CO",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	wlChecker := &mockWLChecker{
		getWaitingFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{
				{
					ID:          "wl-check-1",
					PhoneNumber: "+573005551234",
					PatientID:   "PAT-WL-1",
					PatientName: "Maria Lopez",
					CupsCode:    "890271",
					CupsName:    "Electromiografia",
					PatientAge:  35,
					Espacios:    1,
				},
			}, nil
		},
	}

	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(ctx context.Context, query services.SlotQuery) ([]services.AvailableSlot, error) {
			return []services.AvailableSlot{{TimeSlot: "202603201000"}}, nil
		},
	}

	apptChecker := &mockFutureApptChecker{
		hasFutureFn: func(ctx context.Context, patientID, cupCode string) (bool, error) {
			return false, nil
		},
	}

	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, nil, nil)

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")

	if count != 1 {
		t.Errorf("expected 1 notification, got %d", count)
	}

	wlChecker.mu.Lock()
	notifiedID := wlChecker.notifiedID
	wlChecker.mu.Unlock()

	if notifiedID != "wl-check-1" {
		t.Errorf("expected notifiedID wl-check-1, got %s", notifiedID)
	}

	if !mgr.HasPending("+573005551234") {
		t.Error("expected pending notification for notified patient")
	}
}

func TestCheckWaitingListForCups_NoEntries(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	wlChecker := &mockWLChecker{} // Returns nil entries
	slotSearcher := &mockSlotSearcher{}
	apptChecker := &mockFutureApptChecker{}

	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, nil, nil)

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")
	if count != 0 {
		t.Errorf("expected 0 notifications, got %d", count)
	}
}

func TestCheckWaitingListForCups_NoSlots(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	wlChecker := &mockWLChecker{
		getWaitingFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{{ID: "wl-no-slots", PatientID: "PAT-1", CupsCode: "890271"}}, nil
		},
	}
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(ctx context.Context, query services.SlotQuery) ([]services.AvailableSlot, error) {
			return nil, nil // No slots
		},
	}
	apptChecker := &mockFutureApptChecker{}

	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, nil, nil)

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")
	if count != 0 {
		t.Errorf("expected 0 notifications when no slots, got %d", count)
	}
}

func TestCheckWaitingListForCups_DuplicateFound(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	wlChecker := &mockWLChecker{
		getWaitingFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{{ID: "wl-dup", PatientID: "PAT-DUP", CupsCode: "890271"}}, nil
		},
	}
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(_ context.Context, _ services.SlotQuery) ([]services.AvailableSlot, error) {
			return []services.AvailableSlot{{}}, nil // hay capacidad → el loop corre y detecta el duplicado
		},
	}
	apptChecker := &mockFutureApptChecker{
		hasFutureFn: func(ctx context.Context, patientID, cupCode string) (bool, error) {
			return true, nil // Already has appointment
		},
	}

	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, nil, nil)

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")
	if count != 0 {
		t.Errorf("expected 0 notifications for duplicate, got %d", count)
	}

	wlChecker.mu.Lock()
	status := wlChecker.updatedStatus
	wlChecker.mu.Unlock()

	if status != "duplicate_found" {
		t.Errorf("expected status duplicate_found, got %s", status)
	}
}

func TestCheckWaitingListForCups_TemplateNotConfigured(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{} // No ProjectID
	mgr := NewNotificationManager(birdClient, nil, cfg)

	wlChecker := &mockWLChecker{}
	slotSearcher := &mockSlotSearcher{}
	apptChecker := &mockFutureApptChecker{}

	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, nil, nil)

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")
	if count != 0 {
		t.Errorf("expected 0 when template not configured, got %d", count)
	}
}

func TestCheckWaitingListForCups_NilDeps(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	// No SetWaitingListCheckDeps call → deps are nil

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")
	if count != 0 {
		t.Errorf("expected 0 when deps nil, got %d", count)
	}
}

// === Matching por SLOT (CheckWaitingListForSlot) ===

type mockAgendaResolver struct {
	fn func(ctx context.Context, agendaID int) ([]int, error)
}

func (m *mockAgendaResolver) GetAsuntosByAgenda(ctx context.Context, agendaID int) ([]int, error) {
	if m.fn != nil {
		return m.fn(ctx, agendaID)
	}
	return nil, nil
}

type mockCupsResolver struct {
	fn func(ctx context.Context, medicoID int, asuntos []int) ([]string, error)
}

func (m *mockCupsResolver) FindCupsForDoctorAndAsuntos(ctx context.Context, medicoID int, asuntos []int) ([]string, error) {
	if m.fn != nil {
		return m.fn(ctx, medicoID, asuntos)
	}
	return nil, nil
}

// Un slot de resonancia liberado (médico 3, agenda 100) debe alcanzar a una entrada de un CUP DISTINTO
// del que se canceló, siempre que el médico lo realice y el asunto sea el de la agenda. Es el fix del
// keyeo por-CUP: el pool de candidatos son TODOS los CUPS elegibles del slot.
func TestCheckWaitingListForSlot_NotifiesAcrossCups(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
		BirdTemplateWaitingListVersionID: "ver-wl-456",
		BirdTemplateWaitingListLocale:    "es-CO",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	var gotCups []string
	wlChecker := &mockWLChecker{
		getWaitingInFn: func(_ context.Context, cupsCodes []string, _ int) ([]domain.WaitingListEntry, error) {
			gotCups = cupsCodes
			return []domain.WaitingListEntry{{
				ID:          "wl-slot-1",
				PhoneNumber: "+573005551234",
				PatientID:   "PAT-1",
				CupsCode:    "883221", // distinto del representativo; mismo técnico+asunto
				CupsName:    "RM columna torácica",
				Espacios:    1,
			}}, nil
		},
	}
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(_ context.Context, _ services.SlotQuery) ([]services.AvailableSlot, error) {
			return []services.AvailableSlot{{DoctorSiesaCode: "3", AgendaID: 100, Date: "2026-03-20", TimeSlot: "202603201000"}}, nil
		},
	}
	apptChecker := &mockFutureApptChecker{
		hasFutureFn: func(_ context.Context, _, _ string) (bool, error) { return false, nil },
	}
	agendaResolver := &mockAgendaResolver{
		fn: func(_ context.Context, _ int) ([]int, error) { return []int{4}, nil },
	}
	cupsResolver := &mockCupsResolver{
		fn: func(_ context.Context, _ int, _ []int) ([]string, error) { return []string{"883101", "883221"}, nil },
	}

	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, agendaResolver, cupsResolver)

	count := mgr.CheckWaitingListForSlot(context.Background(), 3, 100)
	if count != 1 {
		t.Fatalf("expected 1 notification, got %d", count)
	}
	if len(gotCups) != 2 {
		t.Errorf("esperaba pool de 2 cups elegibles, got %v", gotCups)
	}
	wlChecker.mu.Lock()
	notifiedID := wlChecker.notifiedID
	wlChecker.mu.Unlock()
	if notifiedID != "wl-slot-1" {
		t.Errorf("esperaba notificar wl-slot-1, got %s", notifiedID)
	}
}

// Sin CUPS elegibles (médico no realiza ninguno de los asuntos de la agenda) → no notifica (fail-closed).
func TestCheckWaitingListForSlot_NoEligibleCups(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{BirdTemplateWaitingListProjectID: "proj-wl-123"}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	agendaResolver := &mockAgendaResolver{fn: func(_ context.Context, _ int) ([]int, error) { return []int{12}, nil }}
	cupsResolver := &mockCupsResolver{fn: func(_ context.Context, _ int, _ []int) ([]string, error) { return nil, nil }}
	mgr.SetWaitingListCheckDeps(&mockSlotSearcher{}, &mockFutureApptChecker{}, &mockWLChecker{}, agendaResolver, cupsResolver)

	if count := mgr.CheckWaitingListForSlot(context.Background(), 99, 200); count != 0 {
		t.Errorf("esperaba 0 sin cups elegibles, got %d", count)
	}
}

// Sin los resolvers de matching por slot (nil) → no notifica (el matching por CUP se usa por otras rutas).
func TestCheckWaitingListForSlot_NilResolvers(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{BirdTemplateWaitingListProjectID: "proj-wl-123"}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListCheckDeps(&mockSlotSearcher{}, &mockFutureApptChecker{}, &mockWLChecker{}, nil, nil)

	if count := mgr.CheckWaitingListForSlot(context.Background(), 3, 100); count != 0 {
		t.Errorf("esperaba 0 con resolvers nil, got %d", count)
	}
}

func TestHandleWaitingList_Schedule_NoGFR(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	entry := sampleWaitingListEntry()
	// GfrCreatinine is 0 → no GFR data should be set

	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
			return entry, nil
		},
	}
	sessRepo := &mockSessionCreator{}
	enqueuer := &mockVirtualEnqueuer{}

	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	mgr.SetWaitingListDeps(wlRepo, sessRepo, enqueuer)

	mgr.RegisterPending(PendingNotification{
		Type:          "waiting_list",
		Phone:         "+573001234567",
		WaitingListID: "wl-no-gfr",
	})

	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	kvs := sessRepo.batchKVs
	if _, exists := kvs["gfr_creatinine"]; exists {
		t.Error("expected no gfr_creatinine when GfrCreatinine is 0")
	}
	if _, exists := kvs["is_pregnant"]; exists {
		t.Error("expected no is_pregnant when IsPregnant is false")
	}
	if _, exists := kvs["baby_weight_cat"]; exists {
		t.Error("expected no baby_weight_cat when empty")
	}
	if _, exists := kvs["preferred_doctor_doc"]; exists {
		t.Error("expected no preferred_doctor_doc when empty")
	}
}

// TestCheckWaitingListForCups_FIFOSkipByCapacity valida FIFO con skip: si el primero requiere mas
// slots de los disponibles, se salta y se notifica al siguiente que si cabe; sin sobre-cupo.
func TestCheckWaitingListForCups_FIFOSkipByCapacity(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()
	cfg := &config.Config{BirdTemplateWaitingListProjectID: "proj-wl"}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	var notified []string
	wlChecker := &mockWLChecker{
		getWaitingFn: func(_ context.Context, _ string, _ int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{
				{ID: "A", PatientID: "PA", CupsCode: "890271", PhoneNumber: "+573001111111", Espacios: 2},
				{ID: "B", PatientID: "PB", CupsCode: "890271", PhoneNumber: "+573002222222", Espacios: 1},
			}, nil
		},
		markNotifiedFn: func(_ context.Context, id string) (bool, error) {
			notified = append(notified, id)
			return true, nil
		},
	}
	// Capacidad = 1 (Espacios=1 -> 1 slot). No hay bloque de 2 (Espacios>=2 -> 0).
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(_ context.Context, q services.SlotQuery) ([]services.AvailableSlot, error) {
			if q.Espacios >= 2 {
				return nil, nil
			}
			return []services.AvailableSlot{{}}, nil
		},
	}
	mgr.SetWaitingListCheckDeps(slotSearcher, &mockFutureApptChecker{}, wlChecker, nil, nil)

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")
	if count != 1 {
		t.Errorf("esperaba 1 notificado (B), got %d", count)
	}
	if len(notified) != 1 || notified[0] != "B" {
		t.Errorf("esperaba notificar solo a B (skip de A por capacidad), got %v", notified)
	}
}

// TestCheckWaitingListForCups_ClaimThenSend valida que si la entrada ya fue reclamada por otra
// corrida (MarkNotified=false), NO se notifica (claim-then-send, N-33).
func TestCheckWaitingListForCups_ClaimThenSend(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()
	cfg := &config.Config{BirdTemplateWaitingListProjectID: "proj-wl"}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	wlChecker := &mockWLChecker{
		getWaitingFn: func(_ context.Context, _ string, _ int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{
				{ID: "A", PatientID: "PA", CupsCode: "890271", PhoneNumber: "+573001111111", Espacios: 1},
			}, nil
		},
		markNotifiedFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil // otra corrida ya la reclamo
		},
	}
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(_ context.Context, _ services.SlotQuery) ([]services.AvailableSlot, error) {
			return []services.AvailableSlot{{}}, nil
		},
	}
	mgr.SetWaitingListCheckDeps(slotSearcher, &mockFutureApptChecker{}, wlChecker, nil, nil)

	if count := mgr.CheckWaitingListForCups(context.Background(), "890271"); count != 0 {
		t.Errorf("esperaba 0 notificados (ya reclamada), got %d", count)
	}
}

// mockPersister implementa NotificationPersister capturando la resolucion.
type mockPersister struct {
	resolvedPhone  string
	resolvedStatus string
	findAllRows    []PendingRow
}

func (m *mockPersister) Upsert(_ context.Context, _, _, _, _, _, _ string, _ int, _ time.Time) error {
	return nil
}
func (m *mockPersister) UpdateCallID(_ context.Context, _, _ string) error { return nil }
func (m *mockPersister) Resolve(_ context.Context, phone, status string) error {
	m.resolvedPhone = phone
	m.resolvedStatus = status
	return nil
}

func (m *mockPersister) DeleteHistoryOlderThan(_ context.Context, _ int) (int64, error) {
	return 0, nil
}

func (m *mockPersister) FindExpired(_ context.Context) ([]PendingRow, error) { return nil, nil }

func (m *mockPersister) FindAll(_ context.Context) ([]PendingRow, error) { return m.findAllRows, nil }

// TestRestorePending_ExpiredProcessedAsync (L10): las notificaciones VENCIDAS no se procesan inline
// (no bloquean el arranque) sino en una goroutine; las vigentes se restauran con su timer.
func TestRestorePending_ExpiredProcessedAsync(t *testing.T) {
	mgr := NewNotificationManager(nil, nil, &config.Config{})
	expiredPhone := "+573009998881"
	validPhone := "+573009998882"
	// 1ª fila vencida (ExpiresAt en el pasado), 2ª vigente.
	mgr.SetPersister(&mockPersister{findAllRows: []PendingRow{
		{Phone: expiredPhone, Type: "cancellation", ExpiresAt: time.Now().Add(-time.Hour)},
		{Phone: validPhone, Type: "cancellation", ExpiresAt: time.Now().Add(2 * time.Hour)},
	}})

	// Sostener el lock del teléfono vencido: si RestorePending lo procesara INLINE, handleTimeout
	// bloquearía hasta 30s esperando el lock → demostramos que NO bloquea el arranque.
	if !mgr.lockPhone(expiredPhone) {
		t.Fatal("no se pudo tomar el lock del teléfono vencido")
	}

	start := time.Now()
	mgr.RestorePending(context.Background())
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("RestorePending bloqueó %v — las vencidas deben procesarse async", d)
	}

	// La vigente quedó restaurada con timer.
	if !mgr.HasPending(validPhone) {
		t.Error("la notificación vigente debió restaurarse")
	}
	if p, ok := mgr.LoadPendingForTest(validPhone); ok && p.Timer != nil {
		p.Timer.Stop()
	}

	// Liberar el lock → la goroutine procesa la vencida (handleTimeout la quita de pending).
	mgr.unlockPhone(expiredPhone)
	deadline := time.Now().Add(3 * time.Second)
	for mgr.HasPending(expiredPhone) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if mgr.HasPending(expiredPhone) {
		t.Error("L10: la notificación vencida debió procesarse async (quitarse de pending)")
	}
}

func TestResponseStatus(t *testing.T) {
	cases := map[string]string{
		// L8: las acciones async se archivan como intención ("*_requested"), no como completadas.
		"confirm": "confirmed", "cancel": "cancel_requested", "reschedule": "reschedule_requested",
		"schedule": "schedule_requested", "decline": "declined", "acknowledge": "acknowledged",
		"weird": "responded",
	}
	for in, want := range cases {
		if got := responseStatus(in); got != want {
			t.Errorf("responseStatus(%q)=%q want %q", in, got, want)
		}
	}
}

// TestHandleResponse_ResolvesWithStatus verifica que al responder, la notificacion se ARCHIVA
// (Resolve) con el estado segun la respuesta, en vez de borrarse sin traza.
func TestHandleResponse_ResolvesWithStatus(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()
	cfg := &config.Config{}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	p := &mockPersister{}
	mgr.SetPersister(p)

	// Tipo waiting_list sin waitingListRepo: HandleResponse resuelve y archiva sin correr sub-handler.
	mgr.RegisterPending(PendingNotification{Type: "waiting_list", Phone: "+573001112233", ConversationID: "conv-x"})
	mgr.HandleResponse("+573001112233", "confirm", "conv-x")

	if p.resolvedStatus != "confirmed" {
		t.Errorf("esperaba Resolve con status 'confirmed', got %q", p.resolvedStatus)
	}
	if p.resolvedPhone != "+573001112233" {
		t.Errorf("esperaba phone +573001112233, got %q", p.resolvedPhone)
	}
}

// --- M5 (re-análisis): escalateNotifToAgent debe emitir session_started + escalated_to_agent y
// registrar la escalación en la tabla escalations (antes creaba la sesión escalada sin ningún evento). ---

type mockEventLoggerNotif struct {
	mu     sync.Mutex
	events []string
}

func (m *mockEventLoggerNotif) LogEvent(_ context.Context, _, _, eventType string, _ map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, eventType)
}

type mockEscRecorderNotif struct {
	mu        sync.Mutex
	created   int
	fromState string
}

func (m *mockEscRecorderNotif) Create(_ context.Context, _, _, fromState, _, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created++
	m.fromState = fromState
	return nil
}

func TestEscalateNotifToAgent_EmitsEventsAndRecordsEscalation(t *testing.T) {
	// Server que satisface EscalateToAgent (lista de agentes + búsqueda de feed item para asignar).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/agents"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","name":"Test Agent","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-1","feedId":"channel:ch-test","closed":false}]}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		}
	}))
	defer srv.Close()
	birdClient := bird.NewClientForTest(srv.URL)

	apptSvc := services.NewAppointmentService(&mockApptRepoNotif{}, nil)
	mgr := NewNotificationManager(birdClient, apptSvc, &config.Config{BirdTeamFallback: "team-fallback"})
	sessRepo := &mockSessionCreator{}
	mgr.SetWaitingListDeps(nil, sessRepo, &mockVirtualEnqueuer{})
	tracker := &mockEventLoggerNotif{}
	mgr.SetTracker(tracker)
	rec := &mockEscRecorderNotif{}
	mgr.SetEscalationRecorder(rec)

	mgr.escalateNotifToAgent(&PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT001", Type: "confirmation",
	}, "conv-1")

	if sessRepo.createdSession == nil {
		t.Fatal("expected escalated session to be created")
	}
	tracker.mu.Lock()
	evs := append([]string{}, tracker.events...)
	tracker.mu.Unlock()
	hasStarted, hasEsc := false, false
	for _, e := range evs {
		switch e {
		case "session_started":
			hasStarted = true
		case "escalated_to_agent":
			hasEsc = true
		}
	}
	if !hasStarted {
		t.Errorf("expected session_started emitted; got %v", evs)
	}
	if !hasEsc {
		t.Errorf("expected escalated_to_agent emitted; got %v", evs)
	}
	rec.mu.Lock()
	created, fs := rec.created, rec.fromState
	rec.mu.Unlock()
	if created != 1 {
		t.Errorf("expected 1 escalations.Create call, got %d", created)
	}
	if fs != "NOTIF_PENDING" {
		t.Errorf("expected from_state NOTIF_PENDING, got %q", fs)
	}
}

// H1 (auditoría): con ConfirmFollowupEnabled=false, MarkIVRSent NO debe borrar el pending de inmediato
// (antes lo hacía y el DTMF de confirmar/cancelar por voz se perdía). Debe mantenerlo vivo en la
// ventana de gracia para que HandleVoiceGatherResult pueda procesarlo.
func TestMarkIVRSent_FollowupDisabled_KeepsPendingForDTMF(t *testing.T) {
	mgr := &NotificationManager{cfg: &config.Config{ConfirmFollowupEnabled: false, ConfirmPostIVRMinutes: 30}}
	mgr.RegisterPending(PendingNotification{Type: "confirmation", Phone: "+573001234567", AppointmentID: "APT1"})
	mgr.RegisterCallID("call-1", "+573001234567")

	mgr.MarkIVRSent("+573001234567")

	if !mgr.HasPending("+573001234567") {
		t.Error("con followup deshabilitado, MarkIVRSent debe MANTENER el pending para que el DTMF se procese")
	}
}

func (m *mockApptRepoNotif) CancelBatchAndBlockSlots(_ context.Context, _ []string, _, _, _ string) error {
	return nil
}
