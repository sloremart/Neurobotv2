package services

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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
	GroupCups       []string // TODOS los CUPS reales que comparten esta cita (incluye CupsCode)
	PatientAge      int
	IsContrasted    bool
	IsSedated       bool
	Espacios        int                                 // Consecutive slots needed
	PreferredDoctor string                              // Doctor document (cédula) from prior consultation
	AfterDate       string                              // For pagination (YYYY-MM-DD)
	MaxSlots        int                                 // Default 5
	ClinicAddress   string                              // Procedure clinic address
	MonthFilter     func(year, month int) (bool, error) // Optional: true = month allowed, nil = no filter
	MinDate         time.Time                           // Optional: exclude slots before this date (gestational window start)
	MaxDate         time.Time                           // Optional: exclude slots after this date (gestational window cutoff)
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

	// CUPS del grupo que comparten esta cita. Las restricciones de médico y de ventana horaria se
	// aplican sobre TODOS (no solo el ancla): un grupo TAC tórax+abdomen debe ofrecerse en un slot
	// que sirva para AMBOS (médico que haga los dos, y franja válida para el más restrictivo).
	groupCups := query.GroupCups
	if len(groupCups) == 0 {
		groupCups = []string{query.CupsCode}
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

	// Médicos habilitados para ESTE CUPS (cups_medico). Restringe la agenda a quienes realmente
	// realizan el procedimiento; cierra el hueco de sis_asuntoMedico (que es a nivel asunto, no CUPS,
	// y por eso ofrecía a todos los médicos de un asunto compartido). ESTRICTO: si el CUPS no tiene
	// médicos configurados NO se ofrece (el bot no lo agenda con un médico arbitrario del asunto); se
	// devuelven 0 slots y el flujo lo enruta a lista de espera/agente. Solo ante ERROR de lookup se
	// hace fail-open (no dejar sin agenda por un fallo transitorio de BD). No aplica con sedación: la
	// agenda es de SOPORTE SEDACION (asunto 17), atendida por otros médicos.
	var allowedDoctors []int
	if !query.IsSedated {
		// Intersección de médicos habilitados para CADA CUP del grupo: el médico debe poder hacer
		// TODOS los procedimientos de la cita, no solo el ancla (cerraba el hueco de ofrecer un médico
		// que hace el tórax pero no el abdomen). CUPS sin mapeo (p.ej. NC sintético) no restringen.
		allowedDoctors, err = s.allowedDoctorsForGroup(ctx, groupCups)
		switch {
		case err != nil:
			slog.Warn("cups_medico_lookup_failed_fail_open", "cups_code", query.CupsCode, "error", err)
			allowedDoctors = nil
		case len(allowedDoctors) == 0:
			// Ningún médico habilitado para TODOS los CUPS del grupo (o CUP sin médico mapeado) → el
			// bot NO lo agenda; se enruta a lista de espera/agente (p.ej. PET, 891503, o tórax+abdomen
			// sin un médico que haga ambos).
			slog.Info("cups_medico_empty_strict_no_slots", "cups_code", query.CupsCode, "group", groupCups)
			return nil, nil
		default:
			slog.Debug("cups_medico_filter_applied", "cups_code", query.CupsCode, "group", groupCups, "doctors", allowedDoctors)
		}
	}

	// 2. Fetch all free slots for this subject (SQL already applies the time window,
	//    agenda eligibility, the booked/blocked filter, and pagination).
	rows, err := s.scheduleRepo.FindAvailableSlots(ctx, subjectType, query.AfterDate, allowedDoctors)
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
	minutesByAgendaDay := make(map[agendaDay][]int)
	for _, row := range rows {
		date := row.SlotTime.Format("2006-01-02")
		minutes := row.SlotTime.Hour()*60 + row.SlotTime.Minute()
		key := agendaDay{row.AgendaID, date}
		if freeByAgendaDay[key] == nil {
			freeByAgendaDay[key] = make(map[int]bool)
		}
		if !freeByAgendaDay[key][minutes] {
			freeByAgendaDay[key][minutes] = true
			minutesByAgendaDay[key] = append(minutesByAgendaDay[key], minutes)
		}
	}

	// Intervalo REAL (grilla) por (agenda, día). SIESA no almacena el intervalo
	// (pm.intervalo y pmd.intervalo son NULL); el real varía por agenda (8/10/20 min).
	//
	// Se calcula como el MCD de los gaps entre slots LIBRES, no el menor gap: si entre dos
	// libres hay un slot ya ocupado, el menor gap observado se infla (p.ej. grilla 10 con
	// 07:10 ocupado → libres 07:00 y 07:20 → gap 20). El MCD recupera la grilla real porque
	// todo gap es múltiplo de la grilla: MCD(20,30)=10 aun sin observar un gap de 10. Esto
	// hace que la verificación de consecutivos exija adyacencia FÍSICA (la misma que reclama
	// repo.Create por filas contiguas en Fecha), evitando ofrecer inicios que el claim rechaza.
	intervalByAgendaDay := make(map[agendaDay]int)
	for key, mins := range minutesByAgendaDay {
		sort.Ints(mins)
		grid := 0
		for i := 1; i < len(mins); i++ {
			if g := mins[i] - mins[i-1]; g > 0 {
				grid = gcd(grid, g)
			}
		}
		intervalByAgendaDay[key] = grid // 0 si solo hay un slot ese día
	}

	// If the preferred doctor has any slot, restrict to them; otherwise keep everyone.
	// N8: el preferido debe pasar también la restricción de edad — si está descalificado por
	// edad, NO debe activar preferredHasSlots (si no, filtraría a todos y dejaría 0 slots).
	preferredHasSlots := false
	if query.PreferredDoctor != "" {
		for _, row := range rows {
			if row.DoctorDocument != query.PreferredDoctor {
				continue
			}
			if minAge, _, exists := GetDoctorAgeRestriction(row.DoctorDocument); exists && query.PatientAge < minAge {
				continue
			}
			preferredHasSlots = true
			break
		}
	}

	// Ventana de prep = intersección (la más restrictiva) de las de todos los CUPS del grupo.
	cupMinHour, cupMaxHour, cupHasWindow := groupCupTimeRestriction(groupCups)
	// Abdomen de TAC: basta con que UN CUP del grupo sea abdomen para exigir la franja ≥10:00 a
	// TODO el bloque (un tórax+abdomen no puede caer antes de las 10:00 por la componente abdomen).
	groupHasAbdomenTAC := false
	for _, c := range groupCups {
		if isAbdomenTAC(c) {
			groupHasAbdomenTAC = true
			break
		}
	}
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

		// Ventana gestacional: solo mostrar slots dentro de la ventana clínica [MinDate, MaxDate].
		if !query.MinDate.IsZero() && dt.Before(query.MinDate) {
			continue
		}
		if !query.MaxDate.IsZero() && dt.After(query.MaxDate) {
			continue
		}

		// Intervalo REAL de esta agenda/día (fallback a DurationMin) y duración TOTAL de la cita =
		// nº de slots (Espacios) × intervalo. Se calcula ANTES del filtro de contraste porque las
		// franjas de contraste se validan por HORA DE FINALIZACIÓN (inicio + duración), no de inicio:
		// la cita completa debe TERMINAR dentro de la franja.
		interval := intervalByAgendaDay[agendaDay{row.AgendaID, date}]
		if interval <= 0 {
			interval = row.DurationMin
		}
		blocks := query.Espacios
		if blocks < 1 {
			blocks = 1
		}
		blockDuration := blocks * interval

		// Contrastados: nunca en sábado (cualquier modalidad), y solo si la cita COMPLETA cabe en una
		// franja permitida por modalidad (TAC vs RNM). El abdomen de TAC no admite contraste antes de
		// las 10:00. El tope superior de cada franja es de FINALIZACIÓN (la cita debe terminar ≤ tope).
		if query.IsContrasted {
			if dt.Weekday() == time.Saturday {
				continue
			}
			if !contrastWindowAllows(subjectType, groupHasAbdomenTAC, minutes, blockDuration) {
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
				slotMin := minutes + i*interval
				if !free[slotMin] {
					allFree = false
					break
				}
				// La franja de contraste ya se validó por finalización (inicio+duración) arriba;
				// aquí solo resta cuidar la ventana de preparación por-CUP (N9) en cada slot del bloque.
				if cupHasWindow && slotMin >= cupMaxHour*60 {
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
			Duration:        interval, // intervalo real de la agenda (no el default 30)
		})

		if len(out) >= query.MaxSlots {
			break
		}
	}

	slog.Debug("slot_search_complete", "cups_code", query.CupsCode, "slots_found", len(out), "espacios_required", query.Espacios)
	return out, nil
}

// gcd devuelve el máximo común divisor de a y b (no negativos). gcd(0,x)=x, por lo que
// acumular gcd(grid, gap) sobre todos los gaps recupera la grilla base de la agenda.
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// tacAbdomenCups son los ÚNICOS CUPS de TAC que cuentan como "abdomen" para la regla de contraste
// (no se agenda contrastado antes de las 10:00). Confirmado por la clínica: solo estos tres.
var tacAbdomenCups = map[string]bool{
	"879410": true, // TAC ABDOMEN SUPERIOR
	"879420": true, // TAC ABDOMEN Y PELVIS (ABDOMEN TOTAL)
	"879411": true, // TAC INTESTINO [ENTEROTC]
}

// isAbdomenTAC indica si el CUP (por su código base, sin sufijo) es un TAC de abdomen.
func isAbdomenTAC(cupsCode string) bool {
	return tacAbdomenCups[strings.SplitN(cupsCode, "-", 2)[0]]
}

// contrastWindowAllows indica si un estudio CONTRASTADO cabe COMPLETO en una franja permitida, para un
// bloque que empieza en startMin (minutos desde medianoche) y dura blockDuration (nº de slots × intervalo).
// Solo aplica L–V (el sábado se filtra aparte).
//
// Los TOPES SUPERIORES de cada franja son de FINALIZACIÓN: la cita debe TERMINAR (startMin+blockDuration)
// dentro de la franja, no solo empezar en ella; así una cita de varios slots no se sale del límite.
// Franjas confirmadas por la clínica (cota inferior = inicio; cota superior = fin):
//   - TAC (asunto 3): mañana inicio ≥ 07:40 y fin ≤ 13:00; tarde inicio ≥ 14:00 y fin ≤ 16:00.
//     Abdomen no antes de 10:00 → mañana inicio ≥ 10:00. (El tope de la tarde es 16:00, no 16:40: el
//     resumen del documento de reglas decía 16:40 pero la TABLA —la fuente— define 14:00–16:00.)
//   - RNM (asunto 4): mañana inicio ≥ 07:40 y fin ≤ 12:00; tarde inicio ≥ 14:00 y fin ≤ 16:20.
//   - Otras modalidades: RX y demás no tienen estudios contrastados; si aun así llegara uno, se mantiene
//     la regla amplia previa (inicio ≥ 07:00, fin ≤ 17:00) para no bloquear de más.
func contrastWindowAllows(subjectType int, abdomen bool, startMin, blockDuration int) bool {
	endMin := startMin + blockDuration
	switch subjectType {
	case 3: // TAC
		morningStart := 7*60 + 40
		if abdomen {
			morningStart = 10 * 60
		}
		return (startMin >= morningStart && endMin <= 13*60) ||
			(startMin >= 14*60 && endMin <= 16*60)
	case 4: // RNM
		return (startMin >= 7*60+40 && endMin <= 12*60) ||
			(startMin >= 14*60 && endMin <= 16*60+20)
	default:
		return startMin >= 7*60 && endMin <= 17*60
	}
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

// groupCupTimeRestriction devuelve la intersección (la ventana MÁS restrictiva) de las ventanas de
// prep de todos los CUPS del grupo: [max de los inicios, min de los fines). Así el slot elegido sirve
// para TODOS los CUPS que comparten la cita. ok=false si NINGÚN CUP del grupo tiene ventana propia.
func groupCupTimeRestriction(cups []string) (minHour, maxHour int, ok bool) {
	minHour, maxHour = 0, 24
	for _, c := range cups {
		if mn, mx, has := cupTimeRestriction(c); has {
			ok = true
			if mn > minHour {
				minHour = mn
			}
			if mx < maxHour {
				maxHour = mx
			}
		}
	}
	if !ok {
		return 0, 0, false
	}
	return minHour, maxHour, true
}

// allowedDoctorsForGroup devuelve la INTERSECCIÓN de médicos habilitados (cups_medico) para TODOS los
// CUPS del grupo: un médico solo es elegible si puede realizar cada procedimiento de la cita. Cierra
// el hueco de ofrecer/agendar un médico que hace uno de los CUPS pero no el otro (p.ej. tórax sí,
// abdomen no). Reglas:
//   - CUP con error de lookup → se omite (fail-open por CUP; no tumba todo el grupo por un fallo).
//   - CUP sin médicos mapeados → se omite (no restringe): puede ser un código sintético (NC 891509) o
//     un CUP no restringido a nivel médico; no debe vaciar la intersección.
//   - Si NINGÚN CUP tuvo médicos: se replica la semántica de CUP único — si hubo error se propaga
//     (el caller hace fail-open), si todos vinieron vacíos se devuelve lista vacía (el caller corta
//     estricto y no agenda). El orden se preserva del primer CUP mapeado (determinismo para tests/UX).
func (s *SlotService) allowedDoctorsForGroup(ctx context.Context, cups []string) ([]int, error) {
	var base []int             // lista ordenada del primer CUP con médicos
	var filters []map[int]bool // sets de los demás CUPS mapeados
	mapped := 0
	var lastErr error
	for _, c := range cups {
		docs, e := s.procedureRepo.FindMedicosForCups(ctx, c)
		if e != nil {
			lastErr = e
			continue
		}
		if len(docs) == 0 {
			continue
		}
		mapped++
		if base == nil {
			base = docs
			continue
		}
		set := make(map[int]bool, len(docs))
		for _, d := range docs {
			set[d] = true
		}
		filters = append(filters, set)
	}
	if mapped == 0 {
		// err → fail-open (caller); todos vacíos sin error → estricto, sin slots (caller).
		return nil, lastErr
	}
	out := make([]int, 0, len(base))
	for _, d := range base {
		inAll := true
		for _, f := range filters {
			if !f[d] {
				inAll = false
				break
			}
		}
		if inAll {
			out = append(out, d)
		}
	}
	return out, nil
}
