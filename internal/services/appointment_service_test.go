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
	slotCountFn             func(ctx context.Context, apptID string) (int, error)
	writeCreationAuditFn    func(ctx context.Context, appointmentID, observations string)
	createProcBatchFn       func(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error
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

func (m *mockAppointmentRepo) FindPendingEmgAppointment(_ context.Context, _ string, _ []string) (*domain.Appointment, error) {
	return nil, nil
}

func (m *mockAppointmentRepo) FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
	if m.findByAgendaAndDateFn != nil {
		return m.findByAgendaAndDateFn(ctx, agendaID, date)
	}
	return nil, nil
}

func (m *mockAppointmentRepo) FindAgendasByDoctor(_ context.Context, _, _ string) ([]domain.AgendaSummary, error) {
	return nil, nil
}

func (m *mockAppointmentRepo) FindAgendaAppointmentsPaged(_ context.Context, _ domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error) {
	return &domain.AgendaAppointmentsPage{}, nil
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

func (m *mockAppointmentRepo) RescheduleDayOfAgenda(_ context.Context, _ domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
	return domain.RescheduleDayResult{}, nil
}

func (m *mockAppointmentRepo) SlotCountForAppointment(ctx context.Context, apptID string) (int, error) {
	if m.slotCountFn != nil {
		return m.slotCountFn(ctx, apptID)
	}
	return 0, nil
}

func (m *mockAppointmentRepo) WriteCreationAudit(ctx context.Context, appointmentID, observations string) {
	if m.writeCreationAuditFn != nil {
		m.writeCreationAuditFn(ctx, appointmentID, observations)
	}
}

func (m *mockAppointmentRepo) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	return nil
}

func (m *mockAppointmentRepo) CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error {
	if m.createProcBatchFn != nil {
		return m.createProcBatchFn(ctx, inputs)
	}
	return nil
}

// --- Tests ---

// apptWithProcs arma una cita con procedimientos (para consolidación).
func apptWithProcs(id string, procs ...domain.AppointmentProcedure) *domain.Appointment {
	return &domain.Appointment{ID: id, Procedures: procs}
}

func proc(code string, qty int) domain.AppointmentProcedure {
	return domain.AppointmentProcedure{CupCode: code, Quantity: qty}
}

// Consolidar: agregar un dependiente (Onda F) a una cita EMG existente → in-place, agrega solo ese CUP.
func TestConsolidateIntoAppointment_InPlaceAddsDependent(t *testing.T) {
	var captured []domain.CreateAppointmentProcedureInput
	repo := &mockAppointmentRepo{
		slotCountFn: func(_ context.Context, _ string) (int, error) { return 1, nil },
		createProcBatchFn: func(_ context.Context, in []domain.CreateAppointmentProcedureInput) error {
			captured = in
			return nil
		},
	}
	svc := NewAppointmentService(repo, &config.Config{})
	appt := apptWithProcs("18234", proc("930860", 2), proc("891509", 8)) // EMG×2 + NC×8

	res, err := svc.ConsolidateIntoAppointment(context.Background(), appt, []CUPSEntry{cup("891514", "Onda F", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if res.NeedsReschedule {
		t.Fatal("no debía requerir reprogramación (espacios sin cambio)")
	}
	if len(res.AddedCups) != 1 || res.AddedCups[0] != "891514" {
		t.Errorf("esperaba agregar [891514], got %v", res.AddedCups)
	}
	if len(captured) != 1 || captured[0].CupCode != "891514" || captured[0].AppointmentID != 18234 || captured[0].Quantity != 1 {
		t.Errorf("batch inesperado: %+v", captured)
	}
}

// Consolidar: subir una orden de NC cuando la cita ya tiene la NC sintetizada → nada que agregar.
func TestConsolidateIntoAppointment_NCAlreadyPresent(t *testing.T) {
	batchCalled := false
	repo := &mockAppointmentRepo{
		slotCountFn: func(_ context.Context, _ string) (int, error) { return 1, nil },
		createProcBatchFn: func(_ context.Context, _ []domain.CreateAppointmentProcedureInput) error {
			batchCalled = true
			return nil
		},
	}
	svc := NewAppointmentService(repo, &config.Config{})
	appt := apptWithProcs("18234", proc("930860", 2), proc("891509", 8)) // EMG×2 + NC×8

	res, err := svc.ConsolidateIntoAppointment(context.Background(), appt, []CUPSEntry{cup("891509", "NC", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if res.NeedsReschedule || len(res.AddedCups) != 0 || batchCalled {
		t.Errorf("esperaba nada que agregar (NC ya presente), got added=%v reschedule=%v batchCalled=%v", res.AddedCups, res.NeedsReschedule, batchCalled)
	}
}

// El contrato SANITAS MRC (5/6) solo aplica si algún CUP es de un grupo MRC; si no, degrada a Evento (7/4).
func TestEffectiveContractForCups(t *testing.T) {
	cases := []struct {
		name     string
		contract string
		cups     []string
		want     string
	}{
		{"MRC subsidiado + CUP MRC → 5", "5", []string{"890274"}, "5"},
		{"MRC subsidiado + CUP no-MRC → 7", "5", []string{"999999"}, "7"},
		{"MRC contributivo + CUP no-MRC → 4", "6", []string{"999999"}, "4"},
		{"MRC contributivo + mezcla (1 MRC) → 6", "6", []string{"999999", "930860"}, "6"},
		{"MRC + CUP MRC con sufijo (base) → 5", "5", []string{"890274-1"}, "5"},
		{"MRC + sin CUPS → degrada", "5", []string{}, "7"},
		{"Evento subsidiado no cambia", "7", []string{"999999"}, "7"},
		{"Evento contributivo no cambia", "4", []string{"930860"}, "4"},
		{"No SANITAS no cambia", "13", []string{"999999"}, "13"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveContractForCups(tc.contract, tc.cups); got != tc.want {
				t.Errorf("EffectiveContractForCups(%q, %v) = %q, quiero %q", tc.contract, tc.cups, got, tc.want)
			}
		})
	}
}

// Consolidar: la cita almacena la NC con sufijo (891509-8 = 8 unidades); agregar Onda F NO debe
// duplicar la NC (normalización a código base + cantidad efectiva por sufijo).
func TestConsolidateIntoAppointment_SuffixNCNotDuplicated(t *testing.T) {
	var captured []domain.CreateAppointmentProcedureInput
	repo := &mockAppointmentRepo{
		slotCountFn: func(_ context.Context, _ string) (int, error) { return 1, nil },
		createProcBatchFn: func(_ context.Context, in []domain.CreateAppointmentProcedureInput) error {
			captured = in
			return nil
		},
	}
	svc := NewAppointmentService(repo, &config.Config{})
	appt := apptWithProcs("18235", proc("930860", 2), proc("891509-8", 1)) // EMG×2 + NC como 891509-8

	res, err := svc.ConsolidateIntoAppointment(context.Background(), appt, []CUPSEntry{cup("891514", "Onda F", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if res.NeedsReschedule {
		t.Fatal("no debía reprogramar")
	}
	if len(res.AddedCups) != 1 || res.AddedCups[0] != "891514" {
		t.Errorf("esperaba agregar solo [891514] (NC ya presente como 891509-8), got %v", res.AddedCups)
	}
	for _, in := range captured {
		if in.CupCode == "891509" {
			t.Errorf("NO debía re-insertar la NC (891509): %+v", captured)
		}
	}
}

// Consolidar: la orden nueva trae EMG y sube el conteo → el bloque crece → reprogramar (no in-place).
func TestConsolidateIntoAppointment_GrowsToReschedule(t *testing.T) {
	batchCalled := false
	repo := &mockAppointmentRepo{
		slotCountFn: func(_ context.Context, _ string) (int, error) { return 1, nil },
		createProcBatchFn: func(_ context.Context, _ []domain.CreateAppointmentProcedureInput) error {
			batchCalled = true
			return nil
		},
	}
	svc := NewAppointmentService(repo, &config.Config{})
	appt := apptWithProcs("18234", proc("930860", 2)) // EMG×2 (1 slot)

	res, err := svc.ConsolidateIntoAppointment(context.Background(), appt, []CUPSEntry{cup("29120", "EMG", 2)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsReschedule {
		t.Errorf("esperaba NeedsReschedule=true (EMG total 4 → 2 espacios > 1 slot)")
	}
	if batchCalled {
		t.Error("no debía insertar procedimientos en el camino de reprogramación")
	}
	if res.Espacios != 2 {
		t.Errorf("esperaba espacios=2, got %d", res.Espacios)
	}
}

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

func TestSlotCountForAppointment(t *testing.T) {
	var gotID string
	repo := &mockAppointmentRepo{
		slotCountFn: func(_ context.Context, apptID string) (int, error) {
			gotID = apptID
			return 3, nil
		},
	}
	svc := NewAppointmentService(repo, nil)

	n, err := svc.SlotCountForAppointment(context.Background(), "7160")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("esperaba 3 slots, got %d", n)
	}
	if gotID != "7160" {
		t.Errorf("esperaba apptID=7160, got %q", gotID)
	}
}

// TestCreateWithConsecutive_WritesCreationAudit verifica que el alta dispara la auditoría
// (log_citas + log_citas_procedimientos) con el id de la cita creada, después de los CUPS.
func TestCreateWithConsecutive_WritesCreationAudit(t *testing.T) {
	var auditedID, auditedObs string
	called := false
	repo := &mockAppointmentRepo{
		createFn: func(_ context.Context, _ domain.CreateAppointmentInput) (*domain.Appointment, error) {
			return &domain.Appointment{ID: "7160"}, nil
		},
		writeCreationAuditFn: func(_ context.Context, appointmentID, observations string) {
			called = true
			auditedID = appointmentID
			auditedObs = observations
		},
	}
	svc := NewAppointmentService(repo, nil)

	id, err := svc.CreateWithConsecutive(context.Background(),
		domain.CreateAppointmentInput{Observations: "obs-x"}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "7160" {
		t.Errorf("esperaba id 7160, got %q", id)
	}
	if !called {
		t.Fatal("esperaba que WriteCreationAudit fuera invocado")
	}
	if auditedID != "7160" {
		t.Errorf("esperaba auditar id 7160, got %q", auditedID)
	}
	if auditedObs != "obs-x" {
		t.Errorf("esperaba observations 'obs-x', got %q", auditedObs)
	}
}

func TestCheckSOATLimit_NonSOAT(t *testing.T) {
	svc := NewAppointmentService(&mockAppointmentRepo{}, nil)

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "890271", "4", 1)
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

	blocked, _, err := svc.CheckMRCLimit(context.Background(), "861411", "6", 1)
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

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "861411", "6", 1)
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

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "861411", "4", 1)
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

	blocked, msg, err := svc.CheckMRCLimit(context.Background(), "861411", "6", 1)
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

	blocked, err := svc.CheckMRCLimitForMonth(context.Background(), "861411", "6", 1, 2026, 4)
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

	blocked, err := svc.CheckMRCLimitForMonth(context.Background(), "861411", "6", 1, 2026, 3)
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

	blocked, err := svc.CheckMRCLimitForMonth(context.Background(), "861411", "4", 1, 2026, 3)
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

	// Un CUP con sufijo debe reconocerse por su base (891509-8 → grupo de 891509).
	groupName3, _, found3 := IsMRCGroupCups("891509-8")
	if !found3 {
		t.Error("expected 891509-8 to be matched by base 891509")
	}
	if groupName3 != "otros_procedimientos" {
		t.Errorf("expected otros_procedimientos, got %s", groupName3)
	}
}

// El umbral suma la cantidad real de la orden. La orden llega con el CUP BASE (891509) y la cantidad
// se define en el OCR y se pasa en `quantity`. Escenario: consumido=930, tope otros_procedimientos=932.
func TestCheckMRCLimit_OrderQuantityCrossesCap(t *testing.T) {
	repo := &mockAppointmentRepo{
		countMonthlyByGroupFn: func(_ context.Context, _ []string, _, _ int) (int, error) {
			return 930, nil // otros_procedimientos max=932
		},
	}
	svc := NewAppointmentService(repo, nil)

	// Base + quantity 8 → 930 + 8 = 938 > 932 → bloquea.
	blocked, _, err := svc.CheckMRCLimit(context.Background(), "891509", "5", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("expected blocked: 930 + 8 = 938 > 932")
	}

	// Base + quantity 1 → 930 + 1 = 931 <= 932 → cabe.
	blocked2, _, err := svc.CheckMRCLimit(context.Background(), "891509", "5", 1)
	if err != nil {
		t.Fatal(err)
	}
	if blocked2 {
		t.Error("expected not blocked: 930 + 1 = 931 <= 932")
	}

	// Fallback: quantity=0 con un CUP que ya trae el sufijo → usa el sufijo (8) → bloquea.
	blocked3, _, err := svc.CheckMRCLimit(context.Background(), "891509-8", "5", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked3 {
		t.Error("expected blocked via fallback del sufijo: 930 + 8 = 938 > 932")
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
	id, err := svc.CreateWithConsecutive(context.Background(), input, 1)
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
		findUpcomingByPatientFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
			// El "bloque" ahora son TODAS las citas del paciente ese día (incluso en agendas
			// distintas), no el bloque consecutivo Antares. AgendaID 1/2/3 lo demuestra.
			return []domain.Appointment{
				{ID: "a1", Date: date, TimeSlot: "202603150800", DoctorID: "doc1", AgendaID: 1, PatientID: "p1"},
				{ID: "a2", Date: date, TimeSlot: "202603151400", DoctorID: "doc2", AgendaID: 2, PatientID: "p1"},
				{ID: "a3", Date: date, TimeSlot: "202603151600", DoctorID: "doc3", AgendaID: 3, PatientID: "p1"},
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
	// El bloque = todas las citas del paciente ese día (3, en agendas distintas)
	if len(block) != 3 {
		t.Errorf("expected block of 3, got %d", len(block))
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
	// Nuevo modelo: 1 sola cita. Si repo.Create falla (p.ej. no caben los N slots consecutivos),
	// CreateWithConsecutive propaga el error y NO reintenta (Create se llama una vez).
	callCount := 0
	repo := &mockAppointmentRepo{
		createFn: func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
			callCount++
			return nil, fmt.Errorf("slots_consecutivos_insuficientes")
		},
	}
	svc := NewAppointmentService(repo, nil)

	input := domain.CreateAppointmentInput{
		TimeSlot: "202603150800",
		Procedures: []domain.CreateProcedureInput{
			{CupCode: "890271", Quantity: 1},
		},
	}
	_, err := svc.CreateWithConsecutive(context.Background(), input, 3)
	if err == nil {
		t.Error("expected error when repo.Create fails")
	}
	if callCount != 1 {
		t.Errorf("expected Create called once (single cita model), got %d", callCount)
	}
}

func TestCreateWithConsecutive_Multiple(t *testing.T) {
	// Nuevo modelo: una sola cita que ocupa N slots. repo.Create se llama UNA vez con
	// input.Espacios = N; el reclamo de los N slots contiguos ocurre dentro de repo.Create
	// (atómico, validado contra BD), no creando N citas.
	callIdx := 0
	var gotEspacios int
	var gotTimeSlot string
	repo := &mockAppointmentRepo{
		createFn: func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
			callIdx++
			gotEspacios = input.Espacios
			gotTimeSlot = input.TimeSlot
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
	id, err := svc.CreateWithConsecutive(context.Background(), input, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "201" {
		t.Errorf("expected appointment ID '201', got %q", id)
	}
	if callIdx != 1 {
		t.Errorf("expected repo.Create called once (single cita), got %d", callIdx)
	}
	if gotEspacios != 3 {
		t.Errorf("expected input.Espacios=3 passed to repo.Create, got %d", gotEspacios)
	}
	if gotTimeSlot != "202603150800" {
		t.Errorf("expected start timeslot preserved, got %q", gotTimeSlot)
	}
}

func (m *mockAppointmentRepo) CancelBatchAndBlockSlots(_ context.Context, _ []string, _, _, _ string) error {
	return nil
}
