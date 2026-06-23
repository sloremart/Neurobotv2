package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// --- Mock AppointmentRepository ---

type mockAppointmentRepo struct {
	hasFutureForCupFn       func(ctx context.Context, pid, cup string) (bool, error)
	findLastDoctorForCupsFn func(ctx context.Context, pid string, cups []string) (string, error)
	countMonthlyByGroupFn   func(ctx context.Context, cups []string, year, month int) (int, error)
	findUpcomingByPatientFn func(ctx context.Context, patientID string) ([]domain.Appointment, error)
	findByIDFn              func(ctx context.Context, id string) (*domain.Appointment, error)
	findByAgendaAndDateFn   func(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error)
	createFn                func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error)
}

func (m *mockAppointmentRepo) FindByID(ctx context.Context, id string) (*domain.Appointment, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, nil
}
func (m *mockAppointmentRepo) FindUpcomingByPatient(ctx context.Context, patientID string) ([]domain.Appointment, error) {
	if m.findUpcomingByPatientFn != nil {
		return m.findUpcomingByPatientFn(ctx, patientID)
	}
	return nil, nil
}
func (m *mockAppointmentRepo) FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
	if m.findByAgendaAndDateFn != nil {
		return m.findByAgendaAndDateFn(ctx, agendaID, date)
	}
	return nil, nil
}
func (m *mockAppointmentRepo) Create(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
	if m.createFn != nil {
		return m.createFn(ctx, input)
	}
	return &domain.Appointment{ID: "100"}, nil
}
func (m *mockAppointmentRepo) Confirm(ctx context.Context, id, channel, channelID string) error {
	return nil
}
func (m *mockAppointmentRepo) Cancel(ctx context.Context, id, reason, channel, channelID string) error {
	return nil
}
func (m *mockAppointmentRepo) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	return nil
}
func (m *mockAppointmentRepo) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	return nil
}
func (m *mockAppointmentRepo) DeleteBatch(ctx context.Context, ids []string) error {
	return nil
}
func (m *mockAppointmentRepo) HasFutureForCup(ctx context.Context, pid, cup string) (bool, error) {
	if m.hasFutureForCupFn != nil {
		return m.hasFutureForCupFn(ctx, pid, cup)
	}
	return false, nil
}
func (m *mockAppointmentRepo) FindLastDoctorForCups(ctx context.Context, pid string, cups []string) (string, error) {
	if m.findLastDoctorForCupsFn != nil {
		return m.findLastDoctorForCupsFn(ctx, pid, cups)
	}
	return "", nil
}
func (m *mockAppointmentRepo) CountMonthlyByGroup(ctx context.Context, cups []string, year, month int) (int, error) {
	if m.countMonthlyByGroupFn != nil {
		return m.countMonthlyByGroupFn(ctx, cups, year, month)
	}
	return 0, nil
}
func (m *mockAppointmentRepo) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	return nil, nil
}
func (m *mockAppointmentRepo) RescheduleDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (int, error) {
	return 0, nil
}
func (m *mockAppointmentRepo) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	return nil
}
func (m *mockAppointmentRepo) CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error {
	return nil
}

// --- Tests ---

func TestFormatTimeSlot(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"202603150700", "7:00 AM"},
		{"202603151430", "2:30 PM"},
		{"202603150000", "12:00 AM"},
		{"202603151200", "12:00 PM"},
		{"202603151330", "1:30 PM"},
		{"202603150830", "8:30 AM"},
		{"short", "Hora no disponible"},
		{"", "Hora no disponible"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := FormatTimeSlot(tc.input)
			if got != tc.expected {
				t.Errorf("FormatTimeSlot(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestParseTimeSlotToMinutes(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"202603150730", 450},  // 7*60+30
		{"202603151400", 840},  // 14*60
		{"202603150000", 0},    // midnight
		{"202603152359", 1439}, // 23*60+59
		{"short", 0},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseTimeSlotToMinutes(tc.input)
			if got != tc.expected {
				t.Errorf("ParseTimeSlotToMinutes(%q) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func TestFindConsecutiveBlock(t *testing.T) {
	svc := NewAppointmentService(&mockAppointmentRepo{}, nil)

	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	// 3 consecutive appointments: 0700, 0730, 0800 (same doctor, same agenda, same patient)
	appointments := []domain.Appointment{
		{ID: "a1", Date: date, TimeSlot: "202603150700", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
		{ID: "a2", Date: date, TimeSlot: "202603150730", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
		{ID: "a3", Date: date, TimeSlot: "202603150800", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
		// Isolated appointment (different doctor + different patient)
		{ID: "a4", Date: date, TimeSlot: "202603150900", DoctorID: "doc2", AgendaID: 2, PatientID: "p2"},
	}

	// Block starting from a1 should include a1, a2, a3
	block := svc.FindConsecutiveBlock(appointments, "a1")
	if len(block) != 3 {
		t.Errorf("expected block of 3, got %d", len(block))
	}

	// a4 is isolated → block of 1
	block4 := svc.FindConsecutiveBlock(appointments, "a4")
	if len(block4) != 1 {
		t.Errorf("expected block of 1 for isolated appointment, got %d", len(block4))
	}

	// Non-existent appointment → nil
	blockNil := svc.FindConsecutiveBlock(appointments, "nonexistent")
	if blockNil != nil {
		t.Errorf("expected nil for non-existent appointment, got %v", blockNil)
	}
}

func TestCheckSOATLimit_NonSOAT(t *testing.T) {
	svc := NewAppointmentService(&mockAppointmentRepo{}, nil)

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "890271", "4")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected not blocked for non-SOAT entity")
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestCheckSOATLimit_WithinLimit(t *testing.T) {
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(ctx context.Context, cups []string, year, month int) (int, error) {
			return 10, nil // Within limit (aplicacion_sustancia max=20)
		},
	}
	svc := NewAppointmentService(repo, nil)

	blocked, _, err := svc.CheckMRCLimit(context.Background(), "861411", "6")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected not blocked when within MRC limit")
	}
}

func TestCheckSOATLimit_ExceedsLimit(t *testing.T) {
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(ctx context.Context, cups []string, year, month int) (int, error) {
			return 20, nil // At limit (aplicacion_sustancia max=20)
		},
	}
	svc := NewAppointmentService(repo, nil)

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "861411", "6")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("expected blocked when MRC limit reached")
	}
	if msg == "" {
		t.Error("expected non-empty message when blocked")
	}
}

func TestCheckMRCLimit_EventoContract_NotBlocked(t *testing.T) {
	// Contract 4 (SANITAS EVENTO CONTRIBUTIVO) is NOT subject to MRC limits
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(ctx context.Context, cups []string, year, month int) (int, error) {
			t.Fatal("CountMonthlyByGroup should not be called for an Evento contract")
			return 999, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "861411", "4")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected Evento contract NOT blocked — MRC only applies to contracts 5/6")
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestCheckSOATLimit_Disabled(t *testing.T) {
	// With feature flag disabled, even an MRC contract should not be checked
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(ctx context.Context, cups []string, year, month int) (int, error) {
			t.Fatal("CountMonthlyByGroup should not be called when disabled")
			return 999, nil
		},
	}
	cfg := &config.Config{CupsGroupLimitsEnabled: false}
	svc := NewAppointmentService(repo, cfg)

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "861411", "6")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected not blocked when CUPS_GROUP_LIMITS_ENABLED=false")
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestCheckMRCLimitForMonth_WithinLimit(t *testing.T) {
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(ctx context.Context, cups []string, year, month int) (int, error) {
			if year != 2026 || month != 4 {
				t.Errorf("expected year=2026 month=4, got year=%d month=%d", year, month)
			}
			return 10, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	blocked, err := svc.CheckMRCLimitForMonth(context.Background(), "861411", "6", 2026, 4)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected not blocked for April when under MRC limit")
	}
}

func TestCheckMRCLimitForMonth_AtLimit(t *testing.T) {
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(ctx context.Context, cups []string, year, month int) (int, error) {
			return 20, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	blocked, err := svc.CheckMRCLimitForMonth(context.Background(), "861411", "6", 2026, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("expected blocked when at MRC limit for month")
	}
}

func TestCheckMRCLimitForMonth_NonMRCContract(t *testing.T) {
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(ctx context.Context, cups []string, year, month int) (int, error) {
			t.Fatal("CountMonthlyByGroup should not be called for a non-MRC contract")
			return 999, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	blocked, err := svc.CheckMRCLimitForMonth(context.Background(), "861411", "4", 2026, 3)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected not blocked for a non-MRC contract")
	}
}

func TestIsMRCGroupCups(t *testing.T) {
	groupName, maxPerMonth, found := IsMRCGroupCups("861411")
	if !found {
		t.Error("expected 861411 to be in soat group")
	}
	if groupName != "aplicacion_sustancia" {
		t.Errorf("expected aplicacion_sustancia, got %s", groupName)
	}
	if maxPerMonth != 20 {
		t.Errorf("expected max 20, got %d", maxPerMonth)
	}

	_, _, found2 := IsMRCGroupCups("890271")
	if found2 {
		t.Error("890271 should not be in any soat group")
	}
}

func TestGetDoctorAgeRestriction(t *testing.T) {
	// Known restricted doctor
	minAge, reason, exists := GetDoctorAgeRestriction("74372158")
	if !exists {
		t.Error("expected restriction to exist for doctor 74372158")
	}
	if minAge != 5 {
		t.Errorf("expected minAge 5, got %d", minAge)
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}

	// Unknown doctor → no restriction
	_, _, exists2 := GetDoctorAgeRestriction("unknown")
	if exists2 {
		t.Error("expected no restriction for unknown doctor")
	}
}

func TestCheckPriorConsultation_NotRequired(t *testing.T) {
	svc := NewAppointmentService(&mockAppointmentRepo{}, nil)
	blocked, doctor, msg, err := svc.CheckPriorConsultation(context.Background(), "890271", "PAT001")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("890271 should not require prior consultation")
	}
	if doctor != "" {
		t.Errorf("expected no doctor, got %q", doctor)
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestCheckPriorConsultation_HasConsultation(t *testing.T) {
	repo := &mockAppointmentRepo{
		findLastDoctorForCupsFn: func(ctx context.Context, pid string, cups []string) (string, error) {
			return "12345678", nil // Found prior neurologist
		},
	}
	svc := NewAppointmentService(repo, nil)
	blocked, doctor, _, err := svc.CheckPriorConsultation(context.Background(), "053105", "PAT001")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("should not be blocked when prior consultation exists")
	}
	if doctor != "12345678" {
		t.Errorf("expected doctor 12345678, got %q", doctor)
	}
}

func TestCheckPriorConsultation_Blocked(t *testing.T) {
	repo := &mockAppointmentRepo{
		findLastDoctorForCupsFn: func(ctx context.Context, pid string, cups []string) (string, error) {
			return "", nil // No prior consultation found
		},
	}
	svc := NewAppointmentService(repo, nil)
	blocked, _, msg, err := svc.CheckPriorConsultation(context.Background(), "053105", "PAT001")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("should be blocked when no prior consultation found")
	}
	if msg == "" {
		t.Error("expected blocking message")
	}
}

func TestHasExistingAppointment(t *testing.T) {
	repo := &mockAppointmentRepo{
		hasFutureForCupFn: func(ctx context.Context, pid, cup string) (bool, error) {
			return pid == "PAT001" && cup == "890271", nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	has, err := svc.HasExistingAppointment(context.Background(), "PAT001", "890271")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("expected true")
	}

	has2, _ := svc.HasExistingAppointment(context.Background(), "PAT001", "890272")
	if has2 {
		t.Error("expected false for different CUPS")
	}
}

func TestConfirmBlock(t *testing.T) {
	confirmed := []string{}
	repo := &mockAppointmentRepo{}
	repo.hasFutureForCupFn = nil // not needed
	svc := &AppointmentService{repo: &confirmTracker{confirmed: &confirmed}}

	block := []domain.Appointment{
		{ID: "a1"}, {ID: "a2"}, {ID: "a3"},
	}
	err := svc.ConfirmBlock(context.Background(), block, "whatsapp", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(confirmed) != 3 {
		t.Errorf("expected 3 confirmations, got %d", len(confirmed))
	}
}

func TestCancelBlock(t *testing.T) {
	cancelled := []string{}
	svc := &AppointmentService{repo: &cancelTracker{cancelled: &cancelled}}

	block := []domain.Appointment{{ID: "a1"}, {ID: "a2"}}
	err := svc.CancelBlock(context.Background(), block, "patient_request", "whatsapp", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 2 {
		t.Errorf("expected 2 cancellations, got %d", len(cancelled))
	}
}

func TestGetFirstCupName(t *testing.T) {
	// With CupName
	appt := domain.Appointment{Procedures: []domain.AppointmentProcedure{{CupCode: "890271", CupName: "EMG"}}}
	if got := GetFirstCupName(appt); got != "EMG" {
		t.Errorf("expected EMG, got %s", got)
	}

	// Without CupName, fallback to code
	appt2 := domain.Appointment{Procedures: []domain.AppointmentProcedure{{CupCode: "890271"}}}
	if got := GetFirstCupName(appt2); got != "890271" {
		t.Errorf("expected 890271, got %s", got)
	}

	// No procedures
	appt3 := domain.Appointment{}
	if got := GetFirstCupName(appt3); got != "Procedimiento" {
		t.Errorf("expected Procedimiento, got %s", got)
	}
}

func TestCreateWithConsecutive_Single(t *testing.T) {
	repo := &mockAppointmentRepo{}
	svc := NewAppointmentService(repo, nil)

	input := domain.CreateAppointmentInput{TimeSlot: "202603150800"}
	id, err := svc.CreateWithConsecutive(context.Background(), input, 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Error("expected non-empty ID")
	}
}

// Helper trackers for Confirm/Cancel

type confirmTracker struct {
	mockAppointmentRepo
	confirmed *[]string
}

func (ct *confirmTracker) Confirm(ctx context.Context, id, channel, channelID string) error {
	*ct.confirmed = append(*ct.confirmed, id)
	return nil
}
func (ct *confirmTracker) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	*ct.confirmed = append(*ct.confirmed, ids...)
	return nil
}

type cancelTracker struct {
	mockAppointmentRepo
	cancelled *[]string
}

func (ct *cancelTracker) Cancel(ctx context.Context, id, reason, channel, channelID string) error {
	*ct.cancelled = append(*ct.cancelled, id)
	return nil
}
func (ct *cancelTracker) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	*ct.cancelled = append(*ct.cancelled, ids...)
	return nil
}

// =============================================================================
// GetUpcomingAppointments tests
// =============================================================================

func TestGetUpcomingAppointments(t *testing.T) {
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	repo := &mockAppointmentRepo{
		findUpcomingByPatientFn: func(ctx context.Context, patientID string) ([]domain.Appointment, error) {
			return []domain.Appointment{
				{ID: "a1", Date: date, TimeSlot: "202603150800", DoctorID: "doc1", AgendaID: 1, PatientID: patientID},
				{ID: "a2", Date: date, TimeSlot: "202603150830", DoctorID: "doc1", AgendaID: 1, PatientID: patientID},
				{ID: "a3", Date: date, TimeSlot: "202603151400", DoctorID: "doc2", AgendaID: 2, PatientID: patientID},
			}, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	appts, err := svc.GetUpcomingAppointments(context.Background(), "PAT001")
	if err != nil {
		t.Fatal(err)
	}
	if len(appts) != 3 {
		t.Errorf("expected 3 appointments, got %d", len(appts))
	}
	if appts[0].ID != "a1" {
		t.Errorf("expected first appointment ID 'a1', got %q", appts[0].ID)
	}
	if appts[2].DoctorID != "doc2" {
		t.Errorf("expected third appointment doctorID 'doc2', got %q", appts[2].DoctorID)
	}
}

func TestGetUpcomingAppointments_Error(t *testing.T) {
	repo := &mockAppointmentRepo{
		findUpcomingByPatientFn: func(ctx context.Context, patientID string) ([]domain.Appointment, error) {
			return nil, fmt.Errorf("database unavailable")
		},
	}
	svc := NewAppointmentService(repo, nil)

	_, err := svc.GetUpcomingAppointments(context.Background(), "PAT001")
	if err == nil {
		t.Error("expected error to be propagated")
	}
}

// =============================================================================
// FindBlockByAppointmentID tests
// =============================================================================

func TestFindBlockByAppointmentID_Found(t *testing.T) {
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	repo := &mockAppointmentRepo{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			if id == "a1" {
				return &domain.Appointment{
					ID: "a1", Date: date, TimeSlot: "202603150800",
					DoctorID: "doc1", AgendaID: 1, PatientID: "p1",
				}, nil
			}
			return nil, nil
		},
		findByAgendaAndDateFn: func(ctx context.Context, agendaID int, dateStr string) ([]domain.Appointment, error) {
			// Return 3 consecutive appointments on the same day/doctor/agenda/patient
			return []domain.Appointment{
				{ID: "a1", Date: date, TimeSlot: "202603150800", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
				{ID: "a2", Date: date, TimeSlot: "202603150830", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
				{ID: "a3", Date: date, TimeSlot: "202603150900", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
			}, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	appt, block, err := svc.FindBlockByAppointmentID(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if appt == nil {
		t.Fatal("expected appointment, got nil")
	}
	if appt.ID != "a1" {
		t.Errorf("expected appointment ID 'a1', got %q", appt.ID)
	}
	// Block should contain all 3 consecutive appointments
	if len(block) != 3 {
		t.Errorf("expected block of 3, got %d", len(block))
	}
}

func TestFindConsecutiveBlock_DoesNotMixPatients(t *testing.T) {
	svc := NewAppointmentService(&mockAppointmentRepo{}, nil)
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	// Same doctor, same agenda, consecutive slots, BUT different patients
	appointments := []domain.Appointment{
		{ID: "a1", Date: date, TimeSlot: "202603150700", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
		{ID: "a2", Date: date, TimeSlot: "202603150730", DoctorID: "doc1", AgendaID: 1, PatientID: "p2"},
		{ID: "a3", Date: date, TimeSlot: "202603150800", DoctorID: "doc1", AgendaID: 1, PatientID: "p3"},
	}

	// Each appointment should be a block of 1 (not grouped with other patients)
	block1 := svc.FindConsecutiveBlock(appointments, "a1")
	if len(block1) != 1 {
		t.Errorf("expected block of 1 for patient p1, got %d (was grouping other patients' appointments!)", len(block1))
	}

	block2 := svc.FindConsecutiveBlock(appointments, "a2")
	if len(block2) != 1 {
		t.Errorf("expected block of 1 for patient p2, got %d", len(block2))
	}
}

func TestFindBlockByAppointmentID_NotFound(t *testing.T) {
	repo := &mockAppointmentRepo{
		findByIDFn: func(ctx context.Context, id string) (*domain.Appointment, error) {
			return nil, nil // not found
		},
	}
	svc := NewAppointmentService(repo, nil)

	appt, block, err := svc.FindBlockByAppointmentID(context.Background(), "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if appt != nil {
		t.Errorf("expected nil appointment, got %+v", appt)
	}
	if block != nil {
		t.Errorf("expected nil block, got %+v", block)
	}
}

// =============================================================================
// CreateWithConsecutive error tests
// =============================================================================

func TestCreateWithConsecutive_Error(t *testing.T) {
	callCount := 0
	repo := &mockAppointmentRepo{
		createFn: func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
			callCount++
			if callCount == 2 {
				return nil, fmt.Errorf("insert failed on 2nd slot")
			}
			return &domain.Appointment{ID: fmt.Sprintf("%d", 100+callCount)}, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	input := domain.CreateAppointmentInput{
		TimeSlot: "202603150800",
		Procedures: []domain.CreateProcedureInput{
			{CupCode: "890271", Quantity: 1},
		},
	}
	_, err := svc.CreateWithConsecutive(context.Background(), input, 3, 30)
	if err == nil {
		t.Error("expected error on 2nd consecutive creation")
	}
	if callCount != 2 {
		t.Errorf("expected Create to be called 2 times before failure, got %d", callCount)
	}
}

func TestCreateWithConsecutive_Multiple(t *testing.T) {
	var createdSlots []string
	callIdx := 0
	repo := &mockAppointmentRepo{
		createFn: func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
			callIdx++
			createdSlots = append(createdSlots, input.TimeSlot)
			return &domain.Appointment{ID: fmt.Sprintf("%d", 200+callIdx)}, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	input := domain.CreateAppointmentInput{
		TimeSlot: "202603150800",
		Procedures: []domain.CreateProcedureInput{
			{CupCode: "890271", Quantity: 1},
		},
	}
	id, err := svc.CreateWithConsecutive(context.Background(), input, 3, 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "201" {
		t.Errorf("expected first appointment ID '201', got %q", id)
	}
	if len(createdSlots) != 3 {
		t.Fatalf("expected 3 slots created, got %d", len(createdSlots))
	}
	// Verify time slots: 0800, 0830, 0900
	expected := []string{"202603150800", "202603150830", "202603150900"}
	for i, exp := range expected {
		if createdSlots[i] != exp {
			t.Errorf("slot %d: expected %q, got %q", i, exp, createdSlots[i])
		}
	}
}
