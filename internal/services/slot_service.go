package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/repository"
)

type SlotService struct {
	procedureRepo repository.ProcedureRepository
	scheduleRepo  repository.ScheduleRepository
}

func NewSlotService(procedureRepo repository.ProcedureRepository, scheduleRepo repository.ScheduleRepository) *SlotService {
	return &SlotService{
		procedureRepo: procedureRepo,
		scheduleRepo:  scheduleRepo,
	}
}

type SlotQuery struct {
	CupsCode        string
	PatientAge      int
	IsContrasted    bool
	IsSedated       bool
	Espacios        int                                 // Consecutive slots needed
	PreferredDoctor string                              // Doctor document (cédula) from prior consultation
	AfterDate       string                              // For pagination (YYYY-MM-DD)
	MaxSlots        int                                 // Default 5
	ClinicAddress   string                              // Procedure clinic address
	MonthFilter     func(year, month int) (bool, error) // Optional: true = month allowed, nil = no filter
}

type AvailableSlot struct {
	Date            string `json:"date"`
	TimeSlot        string `json:"time_slot"`
	TimeDisplay     string `json:"time_display"`
	DoctorName      string `json:"doctor_name"`
	DoctorDoc       string `json:"doctor_doc"`        // sis_medi.cedula — para matching preferred_doctor
	DoctorSiesaCode string `json:"doctor_siesa_code"` // sis_medi.codigo — para cod_medi en citas SIESA
	AgendaID        int    `json:"agenda_id"`
	AgendaSede      int    `json:"agenda_sede"` // programacion_medico.id_sede — ground truth para citas.id_sede
	ClinicAddress   string `json:"clinic_address"`
	Duration        int    `json:"duration"` // Minutes per slot
}

// GetAvailableSlots searches for available appointment slots with all filters applied.
//
// SIESA pre-generates slots, so this no longer reconstructs schedules in memory. It:
//  1. Resolves the subject (asunto_id) for the CUPS from the local catalog (sedation forces 17).
//  2. Fetches every free slot for that subject via the unified query (3h..90-day window,
//     pagination, agenda eligibility, and the booked/blocked filter are all done in SQL).
//  3. Applies the remaining business filters in Go: age restriction, preferred doctor,
//     contrast (no Saturdays, 7AM–5PM), CUPS-specific time windows, and consecutive-slot
//     availability for multi-space procedures.
func (s *SlotService) GetAvailableSlots(ctx context.Context, query SlotQuery) ([]AvailableSlot, error) {
	if query.MaxSlots == 0 {
		query.MaxSlots = 5
	}
	if query.Espacios == 0 {
		query.Espacios = 1
	}

	// 1. Resolve the SIESA subject for this CUPS. Sedation (patient-declared) overrides.
	subjectType, err := s.procedureRepo.FindSubjectTypeForCups(ctx, query.CupsCode)
	if err != nil {
		return nil, fmt.Errorf("find subject for cups: %w", err)
	}
	if query.IsSedated {
		subjectType = 17 // SOPORTE SEDACION
	}
	slog.Debug("slot_subject_resolved", "cups_code", query.CupsCode, "subject", subjectType, "is_sedated", query.IsSedated)
	if subjectType == 0 {
		slog.Warn("no_subject_for_cups", "cups_code", query.CupsCode)
		return nil, nil
	}

	// 2. Fetch all free slots for this subject (SQL already applies the time window,
	//    agenda eligibility, the booked/blocked filter, and pagination).
	rows, err := s.scheduleRepo.FindAvailableSlots(ctx, subjectType, query.AfterDate)
	if err != nil {
		return nil, fmt.Errorf("find available slots: %w", err)
	}
	slog.Debug("slot_search_rows_found", "cups_code", query.CupsCode, "subject", subjectType, "row_count", len(rows))
	if len(rows) == 0 {
		return nil, nil
	}

	// Build per-(agenda, date) sets of free slot start minutes, for consecutive-slot checks.
	type agendaDay struct {
		agenda int
		date   string
	}
	freeByAgendaDay := make(map[agendaDay]map[int]bool)
	for _, row := range rows {
		date := row.SlotTime.Format("2006-01-02")
		minutes := row.SlotTime.Hour()*60 + row.SlotTime.Minute()
		key := agendaDay{row.AgendaID, date}
		if freeByAgendaDay[key] == nil {
			freeByAgendaDay[key] = make(map[int]bool)
		}
		freeByAgendaDay[key][minutes] = true
	}

	// If the preferred doctor has any slot, restrict to them; otherwise keep everyone.
	preferredHasSlots := false
	if query.PreferredDoctor != "" {
		for _, row := range rows {
			if row.DoctorDocument == query.PreferredDoctor {
				preferredHasSlots = true
				break
			}
		}
	}

	cupMinHour, cupMaxHour, cupHasWindow := cupTimeRestriction(query.CupsCode)
	monthCache := make(map[string]bool) // "YYYY-MM" → allowed

	var out []AvailableSlot
	for _, row := range rows {
		date := row.SlotTime.Format("2006-01-02")
		minutes := row.SlotTime.Hour()*60 + row.SlotTime.Minute()

		// Preferred doctor filter.
		if query.PreferredDoctor != "" && preferredHasSlots && row.DoctorDocument != query.PreferredDoctor {
			continue
		}

		// Age restriction (keyed by doctor cédula).
		if minAge, _, exists := GetDoctorAgeRestriction(row.DoctorDocument); exists && query.PatientAge < minAge {
			continue
		}

		dt, _ := time.Parse("2006-01-02", date)

		// Contrasted: no Saturdays, and only 7AM–5PM.
		if query.IsContrasted {
			if dt.Weekday() == time.Saturday {
				continue
			}
			if minutes < 7*60 || minutes >= 17*60 {
				continue
			}
		}

		// CUPS-specific preparation time window (e.g. 879420 TAC → 10AM–3PM).
		if cupHasWindow && (minutes < cupMinHour*60 || minutes >= cupMaxHour*60) {
			continue
		}

		// MRC monthly limit filter (cached per month).
		if query.MonthFilter != nil {
			key := fmt.Sprintf("%d-%02d", dt.Year(), int(dt.Month()))
			allowed, ok := monthCache[key]
			if !ok {
				a, err2 := query.MonthFilter(dt.Year(), int(dt.Month()))
				if err2 != nil {
					a = true // fail-open
				}
				allowed = a
				monthCache[key] = a
			}
			if !allowed {
				continue
			}
		}

		// Consecutive-slot availability for multi-space procedures.
		if query.Espacios > 1 {
			free := freeByAgendaDay[agendaDay{row.AgendaID, date}]
			allFree := true
			for i := 1; i < query.Espacios; i++ {
				if !free[minutes+i*row.DurationMin] {
					allFree = false
					break
				}
			}
			if !allFree {
				continue
			}
		}

		timeSlot := fmt.Sprintf("%s%02d%02d", strings.ReplaceAll(date, "-", ""), minutes/60, minutes%60)
		out = append(out, AvailableSlot{
			Date:            date,
			TimeSlot:        timeSlot,
			TimeDisplay:     FormatTimeSlot(timeSlot),
			DoctorName:      row.DoctorName,
			DoctorDoc:       row.DoctorDocument,
			DoctorSiesaCode: row.DoctorSiesaCode,
			AgendaID:        row.AgendaID,
			AgendaSede:      row.AgendaSede,
			ClinicAddress:   query.ClinicAddress,
			Duration:        row.DurationMin,
		})

		if len(out) >= query.MaxSlots {
			break
		}
	}

	slog.Debug("slot_search_complete", "cups_code", query.CupsCode, "slots_found", len(out), "espacios_required", query.Espacios)
	return out, nil
}

// cupTimeRestriction returns the allowed hour window (minHour, maxHour) for CUPS codes
// that require preparation time, limiting when appointments can be scheduled.
// Returns ok=false if no restriction applies.
func cupTimeRestriction(cupsCode string) (minHour, maxHour int, ok bool) {
	base := strings.SplitN(cupsCode, "-", 2)[0]
	switch base {
	case "879420": // TAC con prep 3h → solo 10:00 AM – 3:00 PM
		return 10, 15, true
	}
	return 0, 0, false
}
