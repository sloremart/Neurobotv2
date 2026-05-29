package services

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

type SlotService struct {
	doctorRepo   repository.DoctorRepository
	scheduleRepo repository.ScheduleRepository
}

func NewSlotService(doctorRepo repository.DoctorRepository, scheduleRepo repository.ScheduleRepository) *SlotService {
	return &SlotService{
		doctorRepo:   doctorRepo,
		scheduleRepo: scheduleRepo,
	}
}

type SlotQuery struct {
	CupsCode        string
	PatientAge      int
	IsContrasted    bool
	IsSedated       bool
	Espacios        int    // Consecutive slots needed
	PreferredDoctor string                          // Doctor document (from prior consultation)
	AfterDate       string                          // For pagination (YYYY-MM-DD)
	MaxSlots        int                             // Default 5
	ClinicAddress   string                          // Procedure clinic address
	ProcedureType   string                          // cups_procedimientos.tipo — filters agendas by NombreAgenda
	MonthFilter     func(year, month int) (bool, error) // Optional: true = month allowed, nil = no filter
}

type AvailableSlot struct {
	Date            string `json:"date"`
	TimeSlot        string `json:"time_slot"`
	TimeDisplay     string `json:"time_display"`
	DoctorName      string `json:"doctor_name"`
	DoctorDoc       string `json:"doctor_doc"`        // Cédula Antares — para matching preferred_doctor
	DoctorSiesaCode string `json:"doctor_siesa_code"` // sis_medi.codigo — para cod_medi en citas SIESA
	AgendaID        int    `json:"agenda_id"`
	ClinicAddress   string `json:"clinic_address"`
	Duration        int    `json:"duration"` // Minutes per slot
}

// GetAvailableSlots searches for available appointment slots with all filters applied.
func (s *SlotService) GetAvailableSlots(ctx context.Context, query SlotQuery) ([]AvailableSlot, error) {
	if query.MaxSlots == 0 {
		query.MaxSlots = 5
	}
	if query.Espacios == 0 {
		query.Espacios = 1
	}

	// 1. Find doctors for this CUPS
	doctors, err := s.doctorRepo.FindByCupsCode(ctx, query.CupsCode)
	if err != nil {
		return nil, fmt.Errorf("find doctors: %w", err)
	}
	for i, d := range doctors {
		slog.Debug("doctor_found", "index", i, "document", d.Document, "full_name", d.FullName, "cup_id", d.CupID)
	}
	slog.Debug("slot_search_doctors_found", "cups_code", query.CupsCode, "doctor_count", len(doctors), "espacios", query.Espacios)
	if len(doctors) == 0 {
		slog.Warn("no_doctors_for_cups", "cups_code", query.CupsCode)
		return nil, nil
	}

	// 2. Filter by age restrictions
	var filteredDoctors []domain.Doctor
	for _, doc := range doctors {
		minAge, _, exists := GetDoctorAgeRestriction(doc.Document)
		if exists && query.PatientAge < minAge {
			continue
		}
		filteredDoctors = append(filteredDoctors, doc)
	}

	// 3. Filter by preferred doctor (from prior consultation)
	if query.PreferredDoctor != "" {
		var preferred []domain.Doctor
		for _, doc := range filteredDoctors {
			if doc.Document == query.PreferredDoctor {
				preferred = append(preferred, doc)
			}
		}
		if len(preferred) > 0 {
			filteredDoctors = preferred
		}
		// If preferred doctor not in filtered list, use all doctors
	}

	if len(filteredDoctors) == 0 {
		slog.Warn("no_doctors_after_age_filter", "cups_code", query.CupsCode, "patient_age", query.PatientAge, "original_count", len(doctors))
		return nil, nil
	}

	// 4. Build doctor docs list and lookup map
	docDocs := make([]string, len(filteredDoctors))
	docMap := make(map[string]domain.Doctor)
	for i, doc := range filteredDoctors {
		docDocs[i] = doc.Document
		docMap[doc.Document] = doc
	}

	// 5. Find future working days for all doctors
	workingDays, err := s.scheduleRepo.FindFutureWorkingDays(ctx, docDocs)
	if err != nil {
		return nil, fmt.Errorf("find working days: %w", err)
	}
	slog.Debug("slot_search_working_days_found", "cups_code", query.CupsCode, "doctor_count", len(docDocs), "working_days_count", len(workingDays))
	if len(workingDays) == 0 {
		slog.Warn("no_working_days_found", "cups_code", query.CupsCode, "doctor_documents", docDocs)
		return nil, nil
	}

	// 6. Calculate available slots per working day
	var allSlots []AvailableSlot
	configCache := make(map[string]*domain.ScheduleConfig)
	agendaCache := make(map[int]bool)   // agendaID → matches procedure type
	monthCache := make(map[string]bool) // "YYYY-MM" → allowed

	for _, day := range workingDays {
		slog.Debug("slot_day_processing", "agenda_id", day.AgendaID, "date", day.Date, "doctor", day.DoctorDocument, "morning", day.MorningEnabled, "afternoon", day.AfternoonEnabled)

		// Skip if before pagination cursor
		if query.AfterDate != "" && day.Date <= query.AfterDate {
			continue
		}

		// MRC monthly limit filter
		if query.MonthFilter != nil {
			dt, _ := time.Parse("2006-01-02", day.Date)
			key := fmt.Sprintf("%d-%02d", dt.Year(), int(dt.Month()))
			if allowed, ok := monthCache[key]; ok {
				if !allowed {
					slog.Debug("slot_skip_mrc_month_cached", "agenda_id", day.AgendaID, "date", day.Date, "month_key", key)
					continue
				}
			} else {
				ok2, err2 := query.MonthFilter(dt.Year(), int(dt.Month()))
				if err2 != nil {
					ok2 = true // fail-open
				}
				monthCache[key] = ok2
				slog.Debug("slot_mrc_month_check", "date", day.Date, "month_key", key, "allowed", ok2, "err", err2)
				if !ok2 {
					continue
				}
			}
		}

		// Contrasted: no Saturdays
		if query.IsContrasted {
			dt, _ := time.Parse("2006-01-02", day.Date)
			if dt.Weekday() == time.Saturday {
				continue
			}
		}

		// Agenda name type filter: skip days whose agenda doesn't match the procedure type
		if query.ProcedureType != "" {
			if allowed, ok := agendaCache[day.AgendaID]; ok {
				if !allowed {
					slog.Debug("slot_skip_agenda_type_cached", "agenda_id", day.AgendaID, "procedure_type", query.ProcedureType)
					continue
				}
			} else {
				schedule, err2 := s.scheduleRepo.FindByScheduleID(ctx, day.AgendaID, query.ProcedureType)
				match := schedule != nil
				agendaCache[day.AgendaID] = match
				slog.Debug("slot_agenda_type_check", "agenda_id", day.AgendaID, "procedure_type", query.ProcedureType, "match", match, "err", err2)
				if !match {
					continue
				}
			}
		}

		// Get schedule config (cached per agenda+doctor)
		cacheKey := fmt.Sprintf("%d-%s", day.AgendaID, day.DoctorDocument)
		cfg, ok := configCache[cacheKey]
		if !ok {
			cfg, err = s.scheduleRepo.FindScheduleConfig(ctx, day.AgendaID, day.DoctorDocument)
			if err != nil || cfg == nil {
				slog.Debug("slot_skip_no_config", "agenda_id", day.AgendaID, "doctor", day.DoctorDocument, "date", day.Date)
				continue
			}
			configCache[cacheKey] = cfg
		}

		// Get booked slots for this day+agenda
		bookedSlots, err := s.scheduleRepo.FindBookedSlots(ctx, day.AgendaID, day.Date)
		if err != nil {
			slog.Warn("find_booked_slots_error", "agenda_id", day.AgendaID, "date", day.Date, "error", err)
			continue
		}
		slog.Debug("booked_slots_fetched",
			"agenda_id", day.AgendaID,
			"date", day.Date,
			"booked_count", len(bookedSlots),
			"booked_slots", bookedSlots,
		)
		bookedSet := make(map[string]bool)
		for _, ts := range bookedSlots {
			bookedSet[ts] = true
		}

		doctor := docMap[day.DoctorDocument]
		slog.Debug("slot_pre_calc", "date", day.Date, "agenda_id", day.AgendaID, "cfg_duration", cfg.AppointmentDuration, "morning_start_mon", cfg.MorningStart[1], "morning_end_mon", cfg.MorningEnd[1], "workday_mon", cfg.WorkDays[1], "workday_0", cfg.WorkDays[0], "workday_1", cfg.WorkDays[1], "workday_2", cfg.WorkDays[2], "workday_3", cfg.WorkDays[3], "workday_4", cfg.WorkDays[4], "workday_5", cfg.WorkDays[5], "workday_6", cfg.WorkDays[6])
		daySlots := calculateDaySlots(cfg, day, query, doctor, bookedSet)
		allSlots = append(allSlots, daySlots...)

		if len(allSlots) >= query.MaxSlots {
			break
		}
	}

	if len(allSlots) > query.MaxSlots {
		allSlots = allSlots[:query.MaxSlots]
	}

	slog.Debug("slot_search_complete", "cups_code", query.CupsCode, "slots_found", len(allSlots), "espacios_required", query.Espacios)
	return allSlots, nil
}

// calculateDaySlots generates available slots for a specific working day.
func calculateDaySlots(cfg *domain.ScheduleConfig, day domain.WorkingDay, query SlotQuery, doctor domain.Doctor, booked map[string]bool) []AvailableSlot {
	dt, _ := time.Parse("2006-01-02", day.Date)
	weekday := int(dt.Weekday()) // 0=Sunday

	// For today: only show slots at least 3 hours from now
	now := time.Now()
	minMinutes := -1
	if day.Date == now.Format("2006-01-02") {
		minMinutes = now.Hour()*60 + now.Minute() + 3*60
	}

	slog.Debug("calc_day_slots_entry", "date", day.Date, "weekday", weekday, "workday_enabled", cfg.WorkDays[weekday], "morning_enabled", day.MorningEnabled, "afternoon_enabled", day.AfternoonEnabled, "duration", cfg.AppointmentDuration)
	if !cfg.WorkDays[weekday] {
		slog.Debug("calc_day_slots_skip_workday", "date", day.Date, "weekday", weekday)
		return nil
	}

	duration := cfg.AppointmentDuration
	if duration <= 0 {
		return nil
	}

	dateStr := strings.ReplaceAll(day.Date, "-", "") // YYYYMMDD

	// Collect time ranges (morning + afternoon)
	type timeRange struct {
		start, end int // minutes from midnight
	}
	var ranges []timeRange

	if day.MorningEnabled && cfg.MorningStart[weekday] != "" && cfg.MorningEnd[weekday] != "" {
		start := parseHHMMToMinutes(cfg.MorningStart[weekday])
		end := parseHHMMToMinutes(cfg.MorningEnd[weekday])
		if start < end {
			ranges = append(ranges, timeRange{start, end})
		}
	}

	if day.AfternoonEnabled && cfg.AfternoonStart[weekday] != "" && cfg.AfternoonEnd[weekday] != "" {
		start := parseHHMMToMinutes(cfg.AfternoonStart[weekday])
		end := parseHHMMToMinutes(cfg.AfternoonEnd[weekday])
		if start < end {
			ranges = append(ranges, timeRange{start, end})
		}
	}

	// Contrasted time restrictions: 7AM - 5PM only
	if query.IsContrasted {
		var filtered []timeRange
		for _, r := range ranges {
			start := r.start
			end := r.end
			if start < 7*60 {
				start = 7 * 60
			}
			if end > 17*60 {
				end = 17 * 60
			}
			if start < end {
				filtered = append(filtered, timeRange{start, end})
			}
		}
		ranges = filtered
	}

	var slots []AvailableSlot
	for _, r := range ranges {
		for minutes := r.start; minutes+duration <= r.end; minutes += duration {
			// Skip past slots for today (minimum 3 hours from now)
			if minMinutes >= 0 && minutes < minMinutes {
				continue
			}

			timeSlot := fmt.Sprintf("%s%02d%02d", dateStr, minutes/60, minutes%60)

			if booked[timeSlot] {
				continue
			}

			// Check consecutive slot availability
			if query.Espacios > 1 {
				allFree := true
				for i := 1; i < query.Espacios; i++ {
					nextMin := minutes + (i * duration)
					nextSlot := fmt.Sprintf("%s%02d%02d", dateStr, nextMin/60, nextMin%60)
					if nextMin+duration > r.end || booked[nextSlot] {
						allFree = false
						break
					}
				}
				if !allFree {
					continue
				}
			}

			slots = append(slots, AvailableSlot{
				Date:            day.Date,
				TimeSlot:        timeSlot,
				TimeDisplay:     FormatTimeSlot(timeSlot),
				DoctorName:      doctor.FullName,
				DoctorDoc:       doctor.Document,
				DoctorSiesaCode: day.DoctorSiesaCode,
				AgendaID:        day.AgendaID,
				ClinicAddress:   query.ClinicAddress,
				Duration:        duration,
			})
		}
	}

	return slots
}

// parseHHMMToMinutes converts "HH:mm" to minutes since midnight.
func parseHHMMToMinutes(t string) int {
	if len(t) < 5 {
		return 0
	}
	hour, _ := strconv.Atoi(t[:2])
	minute, _ := strconv.Atoi(t[3:5])
	return hour*60 + minute
}
