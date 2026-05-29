package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// --- Mock DoctorRepository for SlotService tests ---

type mockDoctorRepo struct {
	findByCupsCodeFn func(ctx context.Context, cupsCode string) ([]domain.Doctor, error)
	findByCupIDFn    func(ctx context.Context, cupID int) ([]domain.Doctor, error)
	findByDocumentFn func(ctx context.Context, doc string) (*domain.Doctor, error)
}

func (m *mockDoctorRepo) FindByCupsCode(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
	if m.findByCupsCodeFn != nil {
		return m.findByCupsCodeFn(ctx, cupsCode)
	}
	return nil, nil
}
func (m *mockDoctorRepo) FindByCupID(ctx context.Context, cupID int) ([]domain.Doctor, error) {
	if m.findByCupIDFn != nil {
		return m.findByCupIDFn(ctx, cupID)
	}
	return nil, nil
}
func (m *mockDoctorRepo) FindByDocument(ctx context.Context, doc string) (*domain.Doctor, error) {
	if m.findByDocumentFn != nil {
		return m.findByDocumentFn(ctx, doc)
	}
	return nil, nil
}

// --- Mock ScheduleRepository for SlotService tests ---

type mockScheduleRepo struct {
	findFutureWorkingDaysFn func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error)
	findScheduleConfigFn    func(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error)
	findByScheduleIDFn      func(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error)
	findBookedSlotsFn       func(ctx context.Context, agendaID int, date string) ([]string, error)
}

func (m *mockScheduleRepo) FindFutureWorkingDays(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
	if m.findFutureWorkingDaysFn != nil {
		return m.findFutureWorkingDaysFn(ctx, doctorDocs)
	}
	return nil, nil
}
func (m *mockScheduleRepo) FindScheduleConfig(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
	if m.findScheduleConfigFn != nil {
		return m.findScheduleConfigFn(ctx, scheduleID, doctorDoc)
	}
	return nil, nil
}
func (m *mockScheduleRepo) FindByScheduleID(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error) {
	if m.findByScheduleIDFn != nil {
		return m.findByScheduleIDFn(ctx, scheduleID, scheduleType)
	}
	return nil, nil
}
func (m *mockScheduleRepo) FindBookedSlots(ctx context.Context, agendaID int, date string) ([]string, error) {
	if m.findBookedSlotsFn != nil {
		return m.findBookedSlotsFn(ctx, agendaID, date)
	}
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
func (m *mockScheduleRepo) FindAsuntoForCups(_ context.Context, _ string) int { return 0 }

func makeConfig(duration int, morningStart, morningEnd string) *domain.ScheduleConfig {
	cfg := &domain.ScheduleConfig{
		AppointmentDuration: duration,
	}
	// Enable all weekdays (Mon-Fri = 1-5)
	for i := 1; i <= 5; i++ {
		cfg.WorkDays[i] = true
		cfg.MorningStart[i] = morningStart
		cfg.MorningEnd[i] = morningEnd
	}
	// Saturday (6)
	cfg.WorkDays[6] = true
	cfg.MorningStart[6] = morningStart
	cfg.MorningEnd[6] = morningEnd
	return cfg
}

func TestCalculateDaySlots_Basic(t *testing.T) {
	cfg := makeConfig(30, "08:00", "12:00")
	day := domain.WorkingDay{
		DoctorDocument: "doc1",
		Date:           "2026-03-16", // Monday
		MorningEnabled: true,
		AgendaID:       1,
	}
	doctor := domain.Doctor{Document: "doc1", FullName: "Dr. Test"}

	booked := map[string]bool{
		"202603160900": true, // 9:00 AM booked
	}

	query := SlotQuery{CupsCode: "890271", Espacios: 1}
	slots := calculateDaySlots(cfg, day, query, doctor, booked)

	// 8:00 to 12:00 with 30min = 8 slots. Minus 1 booked = 7
	if len(slots) != 7 {
		t.Errorf("expected 7 slots, got %d", len(slots))
	}

	// Verify booked slot is excluded
	for _, s := range slots {
		if s.TimeSlot == "202603160900" {
			t.Error("booked slot 0900 should be excluded")
		}
	}

	// Verify first slot
	if len(slots) > 0 && slots[0].TimeSlot != "202603160800" {
		t.Errorf("expected first slot 0800, got %s", slots[0].TimeSlot)
	}
}

func TestCalculateDaySlots_Contrasted(t *testing.T) {
	// Config: 6:00 AM to 6:00 PM, 60min slots
	cfg := makeConfig(60, "06:00", "18:00")
	day := domain.WorkingDay{
		DoctorDocument: "doc1",
		Date:           "2026-03-16", // Monday
		MorningEnabled: true,
		AgendaID:       1,
	}
	doctor := domain.Doctor{Document: "doc1", FullName: "Dr. Test"}

	query := SlotQuery{CupsCode: "890271", IsContrasted: true, Espacios: 1}
	slots := calculateDaySlots(cfg, day, query, doctor, map[string]bool{})

	// Contrasted: clipped to 7AM-5PM = 10 hours with 60min slots = 10 slots
	if len(slots) != 10 {
		t.Errorf("expected 10 contrasted slots (7AM-5PM), got %d", len(slots))
	}

	// Verify no slots before 7AM or after 5PM
	for _, s := range slots {
		mins := ParseTimeSlotToMinutes(s.TimeSlot)
		if mins < 7*60 || mins >= 17*60 {
			t.Errorf("contrasted slot outside 7AM-5PM range: %s (%d min)", s.TimeSlot, mins)
		}
	}

	// Saturday: contrasted not allowed
	daySat := domain.WorkingDay{
		DoctorDocument: "doc1",
		Date:           "2026-03-21", // Saturday
		MorningEnabled: true,
		AgendaID:       1,
	}
	slotsSat := calculateDaySlots(cfg, daySat, query, doctor, map[string]bool{})
	// Saturday is weekday 6, which is enabled in cfg, but contrasted clips.
	// Saturday contrasted IS allowed in calculateDaySlots (Saturday filtering
	// happens in GetAvailableSlots), so this should still generate slots
	if len(slotsSat) == 0 {
		// calculateDaySlots doesn't filter Saturday — GetAvailableSlots does
		// This is expected behavior
	}
}

func TestCalculateDaySlots_ConsecutiveSpaces(t *testing.T) {
	cfg := makeConfig(30, "08:00", "10:00")
	day := domain.WorkingDay{
		DoctorDocument: "doc1",
		Date:           "2026-03-16", // Monday
		MorningEnabled: true,
		AgendaID:       1,
	}
	doctor := domain.Doctor{Document: "doc1", FullName: "Dr. Test"}

	// Book 0900 → blocks consecutive pairs that include 0900
	booked := map[string]bool{
		"202603160900": true,
	}

	query := SlotQuery{CupsCode: "890271", Espacios: 2}
	slots := calculateDaySlots(cfg, day, query, doctor, booked)

	// 8:00-10:00, 30min slots, need 2 consecutive:
	// Possible pairs: 0800+0830, 0830+0900(booked!), 0900(booked!)+0930
	// Valid: 0800 (needs 0800+0830 both free) ✓
	// Invalid: 0830 (needs 0830+0900, but 0900 booked) ✗
	// Invalid: 0900 (booked) ✗
	// Valid: 0930 needs 0930+1000... but 1000 is end, 1000+30=1030 > 1000 end... actually 0930+30=1000 which is end → not valid since 0930+duration must <= end... 0930+30=960 <= 600 (10:00=600)... wait 10:00 = 600 minutes... no, 10*60=600. 0930=570, 570+30=600 = 10:00 end. So slot 0930 fits. But for 2 consecutive: need 0930+1000. 1000+30=1030 > 600? No, 1000=600, 600+30=630 > 600. So 1000 doesn't fit.
	// Actually: 0930 is at minute 570. Next would be 570+30=600. 600+30=630 > 600 (end). So 0930 pair fails.
	// Only valid: 0800
	if len(slots) != 1 {
		t.Errorf("expected 1 slot for 2 consecutive spaces with booked 0900, got %d", len(slots))
	}
}

func TestCalculateDaySlots_NoAfternoon(t *testing.T) {
	cfg := makeConfig(60, "08:00", "12:00")
	// Also set afternoon config
	for i := 1; i <= 5; i++ {
		cfg.AfternoonStart[i] = "14:00"
		cfg.AfternoonEnd[i] = "18:00"
	}

	day := domain.WorkingDay{
		DoctorDocument:   "doc1",
		Date:             "2026-03-16", // Monday
		MorningEnabled:   true,
		AfternoonEnabled: false, // Afternoon disabled
		AgendaID:         1,
	}
	doctor := domain.Doctor{Document: "doc1", FullName: "Dr. Test"}

	query := SlotQuery{CupsCode: "890271", Espacios: 1}
	slots := calculateDaySlots(cfg, day, query, doctor, map[string]bool{})

	// Only morning: 8-12, 60min = 4 slots
	if len(slots) != 4 {
		t.Errorf("expected 4 morning-only slots, got %d", len(slots))
	}

	// Enable afternoon
	day.AfternoonEnabled = true
	slotsAll := calculateDaySlots(cfg, day, query, doctor, map[string]bool{})
	// Morning 4 + afternoon 4 = 8
	if len(slotsAll) != 8 {
		t.Errorf("expected 8 slots with afternoon, got %d", len(slotsAll))
	}
}

// =============================================================================
// GetAvailableSlots integration-level tests (with mocked repos)
// =============================================================================

func TestGetAvailableSlots_BasicFlow(t *testing.T) {
	// 1 doctor, 1 working day (Monday 2026-03-16), 30-min slots 08:00-10:00 = 4 slots
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{
				{Document: "12345", FullName: "Dr. Garcia", CupID: 1},
			}, nil
		},
	}
	scheduleRepo := &mockScheduleRepo{
		findFutureWorkingDaysFn: func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
			return []domain.WorkingDay{
				{DoctorDocument: "12345", Date: "2026-03-16", MorningEnabled: true, AgendaID: 1},
			}, nil
		},
		findScheduleConfigFn: func(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
			return makeConfig(30, "08:00", "10:00"), nil
		},
		findBookedSlotsFn: func(ctx context.Context, agendaID int, date string) ([]string, error) {
			return nil, nil
		},
	}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode: "890271",
		MaxSlots: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 08:00-10:00 with 30min = 4 slots
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

func TestGetAvailableSlots_NoDoctors(t *testing.T) {
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{}, nil // empty
		},
	}
	scheduleRepo := &mockScheduleRepo{}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slots != nil {
		t.Errorf("expected nil slots for no doctors, got %d", len(slots))
	}
}

func TestGetAvailableSlots_AgeRestrictionFilters(t *testing.T) {
	// 2 doctors: "7178922" has minAge=18 restriction, "99999" has none
	// Patient is 10 years old → "7178922" should be filtered out
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{
				{Document: "7178922", FullName: "Dr. Restricted", CupID: 1},
				{Document: "99999", FullName: "Dr. NoRestriction", CupID: 2},
			}, nil
		},
	}
	scheduleRepo := &mockScheduleRepo{
		findFutureWorkingDaysFn: func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
			var days []domain.WorkingDay
			for _, doc := range doctorDocs {
				days = append(days, domain.WorkingDay{
					DoctorDocument: doc, Date: "2026-03-16", MorningEnabled: true, AgendaID: 1,
				})
			}
			return days, nil
		},
		findScheduleConfigFn: func(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
			return makeConfig(60, "08:00", "10:00"), nil
		},
		findBookedSlotsFn: func(ctx context.Context, agendaID int, date string) ([]string, error) {
			return nil, nil
		},
	}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode:   "890271",
		PatientAge: 10,
		MaxSlots:   20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only Dr. NoRestriction's slots should appear
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
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{
				{Document: "docA", FullName: "Dr. Alpha", CupID: 1},
				{Document: "docB", FullName: "Dr. Beta", CupID: 2},
			}, nil
		},
	}
	scheduleRepo := &mockScheduleRepo{
		findFutureWorkingDaysFn: func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
			var days []domain.WorkingDay
			for _, doc := range doctorDocs {
				days = append(days, domain.WorkingDay{
					DoctorDocument: doc, Date: "2026-03-16", MorningEnabled: true, AgendaID: 1,
				})
			}
			return days, nil
		},
		findScheduleConfigFn: func(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
			return makeConfig(60, "08:00", "10:00"), nil
		},
		findBookedSlotsFn: func(ctx context.Context, agendaID int, date string) ([]string, error) {
			return nil, nil
		},
	}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode:        "890271",
		PreferredDoctor: "docB",
		MaxSlots:        20,
	})
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
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{
				{Document: "doc1", FullName: "Dr. Test", CupID: 1},
			}, nil
		},
	}
	scheduleRepo := &mockScheduleRepo{
		findFutureWorkingDaysFn: func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
			return []domain.WorkingDay{
				{DoctorDocument: "doc1", Date: "2026-03-16", MorningEnabled: true, AgendaID: 1},
				{DoctorDocument: "doc1", Date: "2026-03-17", MorningEnabled: true, AgendaID: 1},
				{DoctorDocument: "doc1", Date: "2026-03-18", MorningEnabled: true, AgendaID: 1},
			}, nil
		},
		findScheduleConfigFn: func(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
			return makeConfig(60, "08:00", "10:00"), nil
		},
		findBookedSlotsFn: func(ctx context.Context, agendaID int, date string) ([]string, error) {
			return nil, nil
		},
	}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	// AfterDate "2026-03-17" → skip 2026-03-16 and 2026-03-17 (<=), only 2026-03-18 returned
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode:  "890271",
		AfterDate: "2026-03-17",
		MaxSlots:  20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range slots {
		if s.Date != "2026-03-18" {
			t.Errorf("expected only date 2026-03-18 after pagination, got %q", s.Date)
		}
	}
	// 2026-03-18 is Wednesday (weekday 3), 08:00-10:00 with 60min = 2 slots
	if len(slots) != 2 {
		t.Errorf("expected 2 slots from 3rd day only, got %d", len(slots))
	}
}

func TestGetAvailableSlots_MaxSlotsLimit(t *testing.T) {
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{
				{Document: "doc1", FullName: "Dr. Test", CupID: 1},
			}, nil
		},
	}
	scheduleRepo := &mockScheduleRepo{
		findFutureWorkingDaysFn: func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
			return []domain.WorkingDay{
				{DoctorDocument: "doc1", Date: "2026-03-16", MorningEnabled: true, AgendaID: 1},
			}, nil
		},
		findScheduleConfigFn: func(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
			return makeConfig(30, "08:00", "12:00"), nil // 8 slots available
		},
		findBookedSlotsFn: func(ctx context.Context, agendaID int, date string) ([]string, error) {
			return nil, nil
		},
	}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode: "890271",
		MaxSlots: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 3 {
		t.Errorf("expected MaxSlots=3 to limit to 3, got %d", len(slots))
	}
}

func TestGetAvailableSlots_ContrastedSkipsSaturday(t *testing.T) {
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{
				{Document: "doc1", FullName: "Dr. Test", CupID: 1},
			}, nil
		},
	}
	scheduleRepo := &mockScheduleRepo{
		findFutureWorkingDaysFn: func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
			return []domain.WorkingDay{
				{DoctorDocument: "doc1", Date: "2026-03-20", MorningEnabled: true, AgendaID: 1}, // Friday
				{DoctorDocument: "doc1", Date: "2026-03-21", MorningEnabled: true, AgendaID: 1}, // Saturday
			}, nil
		},
		findScheduleConfigFn: func(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
			return makeConfig(60, "08:00", "10:00"), nil
		},
		findBookedSlotsFn: func(ctx context.Context, agendaID int, date string) ([]string, error) {
			return nil, nil
		},
	}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode:     "890271",
		IsContrasted: true,
		MaxSlots:     20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range slots {
		if s.Date == "2026-03-21" {
			t.Error("contrasted should skip Saturday slots")
		}
	}
	// Friday 08:00-10:00, 60min = 2 slots
	if len(slots) != 2 {
		t.Errorf("expected 2 slots (Friday only), got %d", len(slots))
	}
}

func TestGetAvailableSlots_FindWorkingDaysError(t *testing.T) {
	doctorRepo := &mockDoctorRepo{
		findByCupsCodeFn: func(ctx context.Context, cupsCode string) ([]domain.Doctor, error) {
			return []domain.Doctor{
				{Document: "doc1", FullName: "Dr. Test", CupID: 1},
			}, nil
		},
	}
	scheduleRepo := &mockScheduleRepo{
		findFutureWorkingDaysFn: func(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
			return nil, fmt.Errorf("database connection failed")
		},
	}

	svc := NewSlotService(doctorRepo, scheduleRepo)
	_, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271"})
	if err == nil {
		t.Error("expected error to be propagated from FindFutureWorkingDays")
	}
}

func TestParseHHMMToMinutes(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"08:00", 480},
		{"12:30", 750},
		{"00:00", 0},
		{"23:59", 1439},
		{"abc", 0},    // too short
		{"", 0},       // empty
		{"1:00", 0},   // too short (len < 5)
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := parseHHMMToMinutes(tc.input)
			if got != tc.expected {
				t.Errorf("parseHHMMToMinutes(%q) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}
