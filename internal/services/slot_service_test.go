package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// --- Mocks for SlotService tests ---

type mockProcedureRepo struct {
	findSubjectTypeForCupsFn func(ctx context.Context, cupsCode string) (int, error)
}

func (m *mockProcedureRepo) FindByCode(ctx context.Context, code string) (*domain.Procedure, error) {
	return nil, nil
}
func (m *mockProcedureRepo) FindByID(ctx context.Context, id int) (*domain.Procedure, error) {
	return nil, nil
}
func (m *mockProcedureRepo) SearchByName(ctx context.Context, name string) ([]domain.Procedure, error) {
	return nil, nil
}
func (m *mockProcedureRepo) FindAllActive(ctx context.Context) ([]domain.Procedure, error) {
	return nil, nil
}
func (m *mockProcedureRepo) FindSubjectTypeForCups(ctx context.Context, cupsCode string) (int, error) {
	if m.findSubjectTypeForCupsFn != nil {
		return m.findSubjectTypeForCupsFn(ctx, cupsCode)
	}
	// Default: a valid consultation subject so slot search proceeds.
	return 8, nil
}

type mockScheduleRepo struct {
	findAvailableSlotsFn func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error)
}

func (m *mockScheduleRepo) FindAvailableSlots(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
	if m.findAvailableSlotsFn != nil {
		return m.findAvailableSlotsFn(ctx, asuntoID, afterDate)
	}
	return nil, nil
}
func (m *mockScheduleRepo) FindByScheduleID(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error) {
	return nil, nil
}
func (m *mockScheduleRepo) FindWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (*domain.WorkingDay, error) {
	return nil, nil
}
func (m *mockScheduleRepo) UpdateWorkingDayExceptionDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (bool, error) {
	return false, nil
}
func (m *mockScheduleRepo) DeleteWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (bool, error) {
	return false, nil
}

// slotRow builds an AvailableSlotRow at the given date (YYYY-MM-DD) and HH:MM.
func slotRow(doc, name, siesaCode, date, hhmm string, agendaID, durationMin int) domain.AvailableSlotRow {
	t, _ := time.Parse("2006-01-02 15:04", date+" "+hhmm)
	return domain.AvailableSlotRow{
		SlotTime:        t,
		DoctorDocument:  doc,
		DoctorName:      name,
		DoctorSiesaCode: siesaCode,
		AgendaID:        agendaID,
		DurationMin:     durationMin,
		AgendaSede:      2,
	}
}

// dayRows generates a row per slot from start to end (HH:MM, exclusive) every durationMin.
func dayRows(doc, name, date string, agendaID, durationMin, startMin, endMin int) []domain.AvailableSlotRow {
	var rows []domain.AvailableSlotRow
	for m := startMin; m < endMin; m += durationMin {
		hhmm := fmt.Sprintf("%02d:%02d", m/60, m%60)
		rows = append(rows, slotRow(doc, name, "S-"+doc, date, hhmm, agendaID, durationMin))
	}
	return rows
}

func TestGetAvailableSlots_BasicFlow(t *testing.T) {
	// 08:00–10:00, 30-min slots = 4 slots.
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			return dayRows("12345", "Dr. Garcia", "2026-03-16", 1, 30, 8*60, 10*60), nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", MaxSlots: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 4 {
		t.Errorf("expected 4 slots, got %d", len(slots))
	}
	if len(slots) > 0 {
		if slots[0].DoctorName != "Dr. Garcia" {
			t.Errorf("expected doctor name 'Dr. Garcia', got %q", slots[0].DoctorName)
		}
		if slots[0].Date != "2026-03-16" {
			t.Errorf("expected date '2026-03-16', got %q", slots[0].Date)
		}
		if slots[0].TimeSlot != "202603160800" {
			t.Errorf("expected first time slot '202603160800', got %q", slots[0].TimeSlot)
		}
	}
}

func TestGetAvailableSlots_NoSubject(t *testing.T) {
	// Subject resolves to 0 → no slots.
	procRepo := &mockProcedureRepo{
		findSubjectTypeForCupsFn: func(ctx context.Context, cupsCode string) (int, error) { return 0, nil },
	}
	svc := NewSlotService(procRepo, &mockScheduleRepo{})
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slots != nil {
		t.Errorf("expected nil slots when subject is 0, got %d", len(slots))
	}
}

func TestGetAvailableSlots_NoRows(t *testing.T) {
	svc := NewSlotService(&mockProcedureRepo{}, &mockScheduleRepo{})
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slots != nil {
		t.Errorf("expected nil slots, got %d", len(slots))
	}
}

func TestGetAvailableSlots_AgeRestrictionFilters(t *testing.T) {
	// "7178922" has a minimum-age restriction; "99999" has none. A 10-year-old patient
	// must only get slots from the unrestricted doctor.
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			var rows []domain.AvailableSlotRow
			rows = append(rows, dayRows("7178922", "Dr. Restricted", "2026-03-16", 1, 60, 8*60, 10*60)...)
			rows = append(rows, dayRows("99999", "Dr. NoRestriction", "2026-03-16", 2, 60, 8*60, 10*60)...)
			return rows, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", PatientAge: 10, MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range slots {
		if s.DoctorDoc == "7178922" {
			t.Errorf("slot from age-restricted doctor should have been filtered: %+v", s)
		}
	}
	if len(slots) == 0 {
		t.Error("expected at least some slots from unrestricted doctor")
	}
}

func TestGetAvailableSlots_PreferredDoctor(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			var rows []domain.AvailableSlotRow
			rows = append(rows, dayRows("docA", "Dr. Alpha", "2026-03-16", 1, 60, 8*60, 10*60)...)
			rows = append(rows, dayRows("docB", "Dr. Beta", "2026-03-16", 2, 60, 8*60, 10*60)...)
			return rows, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", PreferredDoctor: "docB", MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range slots {
		if s.DoctorDoc != "docB" {
			t.Errorf("expected only preferred doctor docB, got %q", s.DoctorDoc)
		}
	}
	if len(slots) == 0 {
		t.Error("expected at least some slots for preferred doctor")
	}
}

func TestGetAvailableSlots_PaginationAfterDate(t *testing.T) {
	// The repo is responsible for the afterDate window; assert SlotService forwards it
	// and returns whatever the repo gives back.
	var gotAfter string
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			gotAfter = afterDate
			return dayRows("doc1", "Dr. Test", "2026-03-18", 1, 60, 8*60, 10*60), nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", AfterDate: "2026-03-17", MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAfter != "2026-03-17" {
		t.Errorf("expected afterDate forwarded as '2026-03-17', got %q", gotAfter)
	}
	for _, s := range slots {
		if s.Date != "2026-03-18" {
			t.Errorf("expected only date 2026-03-18, got %q", s.Date)
		}
	}
	if len(slots) != 2 {
		t.Errorf("expected 2 slots, got %d", len(slots))
	}
}

func TestGetAvailableSlots_MaxSlotsLimit(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			return dayRows("doc1", "Dr. Test", "2026-03-16", 1, 30, 8*60, 12*60), nil // 8 slots
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", MaxSlots: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 3 {
		t.Errorf("expected MaxSlots=3 to limit to 3, got %d", len(slots))
	}
}

func TestGetAvailableSlots_ContrastedSkipsSaturday(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			var rows []domain.AvailableSlotRow
			rows = append(rows, dayRows("doc1", "Dr. Test", "2026-03-20", 1, 60, 8*60, 10*60)...) // Friday
			rows = append(rows, dayRows("doc1", "Dr. Test", "2026-03-21", 1, 60, 8*60, 10*60)...) // Saturday
			return rows, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", IsContrasted: true, MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range slots {
		if s.Date == "2026-03-21" {
			t.Error("contrasted should skip Saturday slots")
		}
	}
	if len(slots) != 2 {
		t.Errorf("expected 2 slots (Friday only), got %d", len(slots))
	}
}

func TestGetAvailableSlots_ContrastedTimeWindow(t *testing.T) {
	// Contrasted: only 7AM–5PM. Provide slots at 06:00 and 18:00 (excluded) plus 09:00 (kept).
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			return []domain.AvailableSlotRow{
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-20", "06:00", 1, 60),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-20", "09:00", 1, 60),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-20", "18:00", 1, 60),
			}, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", IsContrasted: true, MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 1 || slots[0].TimeSlot != "202603200900" {
		t.Errorf("expected only 09:00 slot within 7AM-5PM, got %+v", slots)
	}
}

func TestGetAvailableSlots_ConsecutiveSpaces(t *testing.T) {
	// 08:00–10:00, 30-min slots, but 09:00 missing (booked). Need 2 consecutive:
	// valid starts: 08:00 (08:00+08:30). 08:30 fails (needs 09:00). 09:30 fails (needs 10:00).
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			return []domain.AvailableSlotRow{
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:00", 1, 30),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:30", 1, 30),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "09:30", 1, 30),
			}, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", Espacios: 2, MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 1 || slots[0].TimeSlot != "202603160800" {
		t.Errorf("expected only 08:00 valid for 2 consecutive spaces, got %+v", slots)
	}
}

func TestGetAvailableSlots_FindSlotsError(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
			return nil, fmt.Errorf("database connection failed")
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	_, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271"})
	if err == nil {
		t.Error("expected error to be propagated from FindAvailableSlots")
	}
}
