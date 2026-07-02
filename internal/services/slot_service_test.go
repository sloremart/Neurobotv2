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
	findMedicosForCupsFn     func(ctx context.Context, cupsCode string) ([]int, error)
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

func (m *mockProcedureRepo) FindMedicosForCups(ctx context.Context, cupsCode string) ([]int, error) {
	if m.findMedicosForCupsFn != nil {
		return m.findMedicosForCupsFn(ctx, cupsCode)
	}
	// Default: un médico mapeado (pasa el gate estricto de cups_medico). Los tests que prueban el
	// filtrado por médico o el corte estricto fijan findMedicosForCupsFn explícitamente.
	return []int{1}, nil
}

func (m *mockProcedureRepo) FindCupsForDoctorAndAsuntos(_ context.Context, _ int, _ []int) ([]string, error) {
	return nil, nil
}

type mockScheduleRepo struct {
	findAvailableSlotsFn func(ctx context.Context, asuntoID int, afterDate string, allowedDoctors []int) ([]domain.AvailableSlotRow, error)
}

func (m *mockScheduleRepo) FindAvailableSlots(ctx context.Context, asuntoID int, afterDate string, allowedDoctors []int) ([]domain.AvailableSlotRow, error) {
	if m.findAvailableSlotsFn != nil {
		return m.findAvailableSlotsFn(ctx, asuntoID, afterDate, allowedDoctors)
	}
	return nil, nil
}

func (m *mockScheduleRepo) GetAsuntosByAgenda(_ context.Context, _ int) ([]int, error) {
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
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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

func TestGetAvailableSlots_CupsMedicoFilter(t *testing.T) {
	proc := &mockProcedureRepo{
		findSubjectTypeForCupsFn: func(_ context.Context, _ string) (int, error) { return 15, nil },
		findMedicosForCupsFn:     func(_ context.Context, _ string) ([]int, error) { return []int{19, 22}, nil },
	}

	t.Run("el filtro cups_medico se pasa a la query", func(t *testing.T) {
		var got []int
		sched := &mockScheduleRepo{
			findAvailableSlotsFn: func(_ context.Context, _ int, _ string, allowedDoctors []int) ([]domain.AvailableSlotRow, error) {
				got = allowedDoctors
				return nil, nil
			},
		}
		_, _ = NewSlotService(proc, sched).GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "930860"})
		if len(got) != 2 || got[0] != 19 || got[1] != 22 {
			t.Errorf("esperaba allowedDoctors [19 22], got %v", got)
		}
	})

	t.Run("con sedacion NO se restringe por cups_medico", func(t *testing.T) {
		got := []int{-1} // sentinela: debe quedar en nil
		sched := &mockScheduleRepo{
			findAvailableSlotsFn: func(_ context.Context, _ int, _ string, allowedDoctors []int) ([]domain.AvailableSlotRow, error) {
				got = allowedDoctors
				return nil, nil
			},
		}
		_, _ = NewSlotService(proc, sched).GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "930860", IsSedated: true})
		if got != nil {
			t.Errorf("con sedación esperaba allowedDoctors nil, got %v", got)
		}
	})

	// Modo estricto: un CUP sin médico en cups_medico → 0 slots y ni siquiera consulta la agenda (el
	// bot no lo agenda; se enruta a lista de espera/agente). P.ej. PET y 891503 se manejan por agente.
	t.Run("cups sin médico → estricto, 0 slots", func(t *testing.T) {
		proc := &mockProcedureRepo{
			findSubjectTypeForCupsFn: func(_ context.Context, _ string) (int, error) { return 15, nil },
			findMedicosForCupsFn:     func(_ context.Context, _ string) ([]int, error) { return nil, nil },
		}
		called := false
		sched := &mockScheduleRepo{
			findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
				called = true
				return []domain.AvailableSlotRow{{}}, nil
			},
		}
		slots, err := NewSlotService(proc, sched).GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "891503"})
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) != 0 {
			t.Errorf("CUP sin médico → esperaba 0 slots, got %d", len(slots))
		}
		if called {
			t.Error("no debía consultar la agenda si el CUP no tiene médico (corte temprano)")
		}
	})
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
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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
		findAvailableSlotsFn: func(_ context.Context, _ int, afterDate string, _ []int) ([]domain.AvailableSlotRow, error) {
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
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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

// TestGetAvailableSlots_ContrastWindowConsecutive verifica el fix N1: en un bloque
// consecutivo contrastado, NINGÚN slot del bloque puede caer fuera de la ventana 7–17h,
// no solo el inicial. Slots de 30 min, Espacios=2, contraste:
//   - inicio 16:00 → 2º slot 16:30 (<17:00) → VÁLIDO
//   - inicio 16:30 → 2º slot 17:00 (>=17:00) → INVÁLIDO (antes del fix se aceptaba)
//   - inicio 17:00 → el slot inicial ya cae fuera de ventana → INVÁLIDO
func TestGetAvailableSlots_ContrastWindowConsecutive(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			return []domain.AvailableSlotRow{
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "16:00", 1, 30),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "16:30", 1, 30),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "17:00", 1, 30),
			}, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	// CUPS 890271 no tiene ventana propia → aísla la ventana de contraste.
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode: "890271", Espacios: 2, IsContrasted: true, MaxSlots: 20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 1 || slots[0].TimeSlot != "202603161600" {
		t.Errorf("esperaba solo 16:00 (bloque dentro de 7-17h), got %+v", slots)
	}
}

// TestGetAvailableSlots_CupWindowConsecutive verifica el fix N9: la ventana específica de
// CUPS (879420 TAC → 10–15h) también se aplica a cada slot del bloque consecutivo.
// Slots de 30 min, Espacios=2:
//   - inicio 14:00 → 2º slot 14:30 (<15:00) → VÁLIDO
//   - inicio 14:30 → 2º slot 15:00 (>=15:00) → INVÁLIDO (antes del fix se aceptaba)
//   - inicio 15:00 → slot inicial fuera de ventana → INVÁLIDO
func TestGetAvailableSlots_CupWindowConsecutive(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			return []domain.AvailableSlotRow{
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "14:00", 1, 30),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "14:30", 1, 30),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "15:00", 1, 30),
			}, nil
		},
	}
	// El asunto debe resolver para que el flujo siga; TAC 879420 usa la ventana 10-15h.
	procRepo := &mockProcedureRepo{findSubjectTypeForCupsFn: func(_ context.Context, _ string) (int, error) { return 3, nil }}
	svc := NewSlotService(procRepo, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode: "879420", Espacios: 2, MaxSlots: 20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 1 || slots[0].TimeSlot != "202603161400" {
		t.Errorf("esperaba solo 14:00 (bloque dentro de 10-15h), got %+v", slots)
	}
}

// TestGetAvailableSlots_PreferredDoctorAgeRestricted verifica el fix N8: si el médico
// preferido está descalificado por edad, NO debe activar el filtro "solo preferido" (que
// dejaría 0 slots); deben devolverse los slots de los demás médicos elegibles.
func TestGetAvailableSlots_PreferredDoctorAgeRestricted(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			var rows []domain.AvailableSlotRow
			rows = append(rows, dayRows("7178922", "Dr. Restringido", "2026-03-16", 1, 60, 8*60, 10*60)...) // min 18 años
			rows = append(rows, dayRows("99999", "Dr. Libre", "2026-03-16", 2, 60, 8*60, 10*60)...)
			return rows, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	// Paciente de 10 años con el preferido = médico restringido por edad.
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{
		CupsCode: "890271", PatientAge: 10, PreferredDoctor: "7178922", MaxSlots: 20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) == 0 {
		t.Fatal("esperaba slots del médico libre; el preferido restringido por edad no debe vaciar el resultado")
	}
	for _, s := range slots {
		if s.DoctorDoc == "7178922" {
			t.Errorf("no debería haber slots del médico restringido por edad: %+v", s)
		}
	}
}

// TestGetAvailableSlots_ConsecutiveRealInterval prueba el fix del intervalo real (bug B):
// los slots están a 20 min, pero DurationMin viene en 30 (el default falso de SIESA, pm.intervalo
// NULL). La verificación de consecutivos debe usar el intervalo DERIVADO (20) de los gaps, no 30.
// Espacios=2: 08:00 (necesita 08:20) y 08:20 (necesita 08:40) son válidos; 08:40 no (no hay 09:00).
func TestGetAvailableSlots_ConsecutiveRealInterval(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			return []domain.AvailableSlotRow{
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:00", 1, 30), // DurationMin=30 (falso)
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:20", 1, 30),
				slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:40", 1, 30),
			}, nil
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	slots, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", Espacios: 2, MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 2 {
		t.Fatalf("esperaba 2 starts válidos (08:00, 08:20) con intervalo real 20min, got %d: %+v", len(slots), slots)
	}
	if slots[0].TimeSlot != "202603160800" || slots[1].TimeSlot != "202603160820" {
		t.Errorf("starts inesperados: %+v", slots)
	}
	// La duración reportada debe ser el intervalo real derivado (20), no 30.
	if slots[0].Duration != 20 {
		t.Errorf("esperaba Duration=20 (intervalo real), got %d", slots[0].Duration)
	}
}

// TestGetAvailableSlots_GridIntervalGCD prueba el fix C: el intervalo (grilla) se deriva con
// el MCD de los gaps entre libres, no el menor gap. Libres a 08:00, 08:20, 08:50 (gaps 20 y 30):
// un gap de 30 prueba que la grilla ≤ 10 (30 no es múltiplo de 20), así que MCD(20,30)=10. Con
// grilla=10 NINGÚN par de libres es físicamente adyacente, por lo que con Espacios=2 no debe
// ofrecerse ningún inicio (el claim de repo.Create reclama filas contiguas y los rechazaría).
// Con el viejo min-gap=20 se habría ofrecido 08:00 (08:20 libre) → oferta falsa.
func TestGetAvailableSlots_GridIntervalGCD(t *testing.T) {
	rows := func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
		return []domain.AvailableSlotRow{
			slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:00", 1, 30),
			slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:20", 1, 30),
			slotRow("doc1", "Dr. Test", "S-doc1", "2026-03-16", "08:50", 1, 30),
		}, nil
	}
	svc := NewSlotService(&mockProcedureRepo{}, &mockScheduleRepo{findAvailableSlotsFn: rows})

	// Espacios=1: los 3 slots son válidos como cita simple.
	single, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", Espacios: 1, MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(single) != 3 {
		t.Fatalf("Espacios=1: esperaba 3 slots, got %d", len(single))
	}
	if single[0].Duration != 10 {
		t.Errorf("esperaba grilla derivada=10 (MCD de 20 y 30), got %d", single[0].Duration)
	}

	// Espacios=2: ningún par contiguo a grilla 10 → 0 inicios válidos.
	multi, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271", Espacios: 2, MaxSlots: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(multi) != 0 {
		t.Fatalf("Espacios=2: esperaba 0 inicios (grilla 10, sin pares adyacentes), got %d: %+v", len(multi), multi)
	}
}

func TestGCD(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{0, 20, 20}, {20, 30, 10}, {20, 0, 20}, {12, 8, 4}, {10, 10, 10}, {0, 0, 0},
	}
	for _, c := range cases {
		if got := gcd(c.a, c.b); got != c.want {
			t.Errorf("gcd(%d,%d)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestGetAvailableSlots_FindSlotsError(t *testing.T) {
	scheduleRepo := &mockScheduleRepo{
		findAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
			return nil, fmt.Errorf("database connection failed")
		},
	}
	svc := NewSlotService(&mockProcedureRepo{}, scheduleRepo)
	_, err := svc.GetAvailableSlots(context.Background(), SlotQuery{CupsCode: "890271"})
	if err == nil {
		t.Error("expected error to be propagated from FindAvailableSlots")
	}
}

func TestIsAbdomenTAC(t *testing.T) {
	for _, c := range []struct {
		code string
		want bool
	}{
		{"879410", true},
		{"879420", true},
		{"879411", true},
		{"879420-1", true}, // con sufijo → código base
		{"879460", false},  // pelvis: NO abdomen (confirmado)
		{"879421", false},  // cadera: NO
		{"879111", false},  // TAC cráneo
		{"883401", false},  // RNM abdomen (no TAC)
	} {
		if got := isAbdomenTAC(c.code); got != c.want {
			t.Errorf("isAbdomenTAC(%q)=%v, quiero %v", c.code, got, c.want)
		}
	}
}

func TestContrastWindowAllows(t *testing.T) {
	hm := func(h, m int) int { return h*60 + m }
	for _, c := range []struct {
		name    string
		subject int
		abdomen bool
		minutes int
		want    bool
	}{
		// TAC no-abdomen
		{"TAC 07:20 antes", 3, false, hm(7, 20), false},
		{"TAC 07:40 ok", 3, false, hm(7, 40), true},
		{"TAC 12:59 ok", 3, false, hm(12, 59), true},
		{"TAC 13:00 almuerzo", 3, false, hm(13, 0), false},
		{"TAC 14:00 ok", 3, false, hm(14, 0), true},
		{"TAC 16:20 ok", 3, false, hm(16, 20), true},
		{"TAC 16:40 corte", 3, false, hm(16, 40), false},
		// TAC abdomen: no antes de 10:00
		{"TAC abd 07:40 no", 3, true, hm(7, 40), false},
		{"TAC abd 09:59 no", 3, true, hm(9, 59), false},
		{"TAC abd 10:00 ok", 3, true, hm(10, 0), true},
		{"TAC abd 14:00 ok", 3, true, hm(14, 0), true},
		{"TAC abd 16:40 corte", 3, true, hm(16, 40), false},
		// RNM
		{"RNM 07:40 ok", 4, false, hm(7, 40), true},
		{"RNM 11:59 ok", 4, false, hm(11, 59), true},
		{"RNM 12:00 mediodia", 4, false, hm(12, 0), false},
		{"RNM 14:00 ok", 4, false, hm(14, 0), true},
		{"RNM 16:19 ok", 4, false, hm(16, 19), true},
		{"RNM 16:20 corte", 4, false, hm(16, 20), false},
		// Otras (RX): regla amplia 07:00-17:00
		{"RX 06:59 no", 2, false, hm(6, 59), false},
		{"RX 07:00 ok", 2, false, hm(7, 0), true},
		{"RX 17:00 corte", 2, false, hm(17, 0), false},
	} {
		if got := contrastWindowAllows(c.subject, c.abdomen, c.minutes); got != c.want {
			t.Errorf("%s: contrastWindowAllows(%d,%v,%d)=%v, quiero %v", c.name, c.subject, c.abdomen, c.minutes, got, c.want)
		}
	}
}
