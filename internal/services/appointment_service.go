package services

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// ConfirmationLogger persiste registros estructurados de confirmación localmente.
// Implementado por local.ConfirmationLogRepo.
type ConfirmationLogger interface {
	InsertBatch(ctx context.Context, entries []domain.ConfirmationEntry) error
}

type AppointmentService struct {
	repo       repository.AppointmentRepository
	cfg        *config.Config
	confirmLog ConfirmationLogger
}

func NewAppointmentService(repo repository.AppointmentRepository, cfg *config.Config) *AppointmentService {
	return &AppointmentService{repo: repo, cfg: cfg}
}

// SetConfirmationLog conecta el log local de confirmaciones. Llamar una vez tras construir el servicio.
func (s *AppointmentService) SetConfirmationLog(logger ConfirmationLogger) {
	s.confirmLog = logger
}

// GetUpcomingAppointments retorna las citas futuras no canceladas del paciente
func (s *AppointmentService) GetUpcomingAppointments(ctx context.Context, patientID string) ([]domain.Appointment, error) {
	return s.repo.FindUpcomingByPatient(ctx, patientID)
}

// GetPatientAppointmentsForDate returns all non-canceled appointments for a patient on a specific date.
func (s *AppointmentService) GetPatientAppointmentsForDate(ctx context.Context, patientID string, date time.Time) ([]domain.Appointment, error) {
	all, err := s.repo.FindUpcomingByPatient(ctx, patientID)
	if err != nil {
		return nil, err
	}
	dateStr := date.Format("2006-01-02")
	var result []domain.Appointment
	for _, a := range all {
		if a.Date.Format("2006-01-02") == dateStr {
			result = append(result, a)
		}
	}
	return result, nil
}

// ConfirmBlock confirma todas las citas del bloque atómicamente.
// Also writes a structured record to the local confirmation_log if configured —
// this replaces the Antares citas.MedioConfirmacion / FechaConfirmacion fields
// that SIESA's citas table does not have.
func (s *AppointmentService) ConfirmBlock(ctx context.Context, block []domain.Appointment, channel, channelID string) error {
	ids := make([]string, len(block))
	for i, appt := range block {
		ids[i] = appt.ID
	}
	if err := s.repo.ConfirmBatch(ctx, ids, channel, channelID); err != nil {
		return err
	}
	if s.confirmLog != nil {
		entries := make([]domain.ConfirmationEntry, len(block))
		now := time.Now()
		for i, appt := range block {
			entries[i] = domain.ConfirmationEntry{
				AppointmentID: appt.ID,
				PatientID:     appt.PatientID,
				ConfirmedAt:   now,
				Channel:       channel,
				ChannelID:     channelID,
			}
		}
		if err := s.confirmLog.InsertBatch(ctx, entries); err != nil {
			slog.Warn("confirmation log insert failed", "error", err, "ids", ids)
		}
	}
	return nil
}

// CancelBlock cancela todas las citas del bloque atómicamente
func (s *AppointmentService) CancelBlock(ctx context.Context, block []domain.Appointment, reason, channel, channelID string) error {
	ids := make([]string, len(block))
	for i, appt := range block {
		ids[i] = appt.ID
	}
	return s.repo.CancelBatch(ctx, ids, reason, channel, channelID)
}

// CancelByIDs cancela citas directamente por sus IDs (para flujos de notificación multi-bloque)
func (s *AppointmentService) CancelByIDs(ctx context.Context, ids []string, reason, channel, channelID string) error {
	return s.repo.CancelBatch(ctx, ids, reason, channel, channelID)
}

// ParseTimeSlotToMinutes convierte "YYYYMMDD0730" → 450 (7*60+30)
func ParseTimeSlotToMinutes(timeSlot string) int {
	if len(timeSlot) < 12 {
		return 0
	}
	hour, _ := strconv.Atoi(timeSlot[8:10])
	minute, _ := strconv.Atoi(timeSlot[10:12])
	return hour*60 + minute
}

// FormatTimeSlot convierte "YYYYMMDD0730" → "7:30 AM"
func FormatTimeSlot(timeSlot string) string {
	if len(timeSlot) < 12 {
		return "Hora no disponible"
	}
	hour, _ := strconv.Atoi(timeSlot[8:10])
	minute, _ := strconv.Atoi(timeSlot[10:12])

	ampm := "AM"
	displayHour := hour
	if hour >= 12 {
		ampm = "PM"
		if hour > 12 {
			displayHour = hour - 12
		}
	}
	if hour == 0 {
		displayHour = 12
	}

	return fmt.Sprintf("%d:%02d %s", displayHour, minute, ampm)
}

// --- Validaciones Médicas (Fase 9) ---

// CUPS que requieren consulta previa con doctor específico
var cupsRequiresPreviousDoctor = map[string][]string{
	"053105": {"890374", "890274"}, // Requiere cita previa de tipo 890374 o 890274
	"861402": {"890264", "890364"}, // Requiere cita previa de tipo 890264 o 890364
}

// MRC group limits (máximo mensual por grupo — Modelo de Riesgo Compartido, aplica solo a contratos 5/6)
// Per R-PROC-09 in 02-BUSINESS-RULES.md
var mrcGroups = map[string]struct {
	MaxPerMonth int
	CupsCodes   []string
}{
	"consulta_neurologia":  {MaxPerMonth: 397, CupsCodes: []string{"890274", "890374"}},
	"electroencefalograma": {MaxPerMonth: 172, CupsCodes: []string{"891402", "891901", "891402-1", "891402PED", "891901-1", "891901PED", "891401", "891401PED"}},
	"bloqueos":             {MaxPerMonth: 67, CupsCodes: []string{"053106", "053105", "053111"}},
	"aplicacion_sustancia": {MaxPerMonth: 20, CupsCodes: []string{"861411", "48201"}},
	"polisomnografia":      {MaxPerMonth: 57, CupsCodes: []string{"891704", "891703", "891704-1", "891704PED", "891703-1", "891703PED"}},
	"otros_procedimientos": {MaxPerMonth: 932, CupsCodes: []string{"891515", "891514", "930820", "891511", "891509", "930860", "891530", "952303", "954626", "952302", "930103", "930821", "954624", "954625", "952301", "930801", "891503", "891508"}},
}

// IsMRCPatient determina si un paciente está sujeto a validación MRC según su contrato.
// MRC = contratos 5 (SANITAS MRC SUBSIDIADO) y 6 (SANITAS MRC CONTRIBUTIVO).
// Reemplaza al antiguo IsMRCEntity, que comparaba el código de empresa (todos los
// contratos Sanitas comparten empresa "EPS005", así que nunca distinguía MRC de Evento).
// El contrato viene de sis_paci.contrato (domain.Patient.ContractCode → sesión patient_contract).
func IsMRCPatient(contractCode string) bool {
	return contractCode == "5" || contractCode == "6"
}

// IsMRCGroupCups returns the group name, max per month, and whether the CUPS code belongs to an MRC group.
// El match es por CUP BASE (sin sufijo): una orden "891509-8" pertenece al grupo de "891509". Sin
// esto, un CUP con sufijo no se reconocería como del grupo (los grupos listan en su mayoría el base)
// y se saltaría la validación de tope. Espeja el conteo de CountMonthlyByGroup, que agrupa por base.
func IsMRCGroupCups(cupsCode string) (groupName string, maxPerMonth int, found bool) {
	base := utils.BaseCupCode(cupsCode)
	for name, group := range mrcGroups {
		for _, code := range group.CupsCodes {
			if code == cupsCode || utils.BaseCupCode(code) == base {
				return name, group.MaxPerMonth, true
			}
		}
	}
	return "", 0, false
}

// Restricciones de edad por doctor (hardcoded por negocio)
var doctorAgeRestrictions = map[string]struct {
	MinAge int
	Reason string
}{
	"74372158": {MinAge: 5, Reason: "Este doctor solo atiende pacientes mayores de 5 años"},
	"7178922":  {MinAge: 18, Reason: "Este doctor solo atiende pacientes mayores de 18 años"},
}

// GetConsultCupsFor returns the prior-consultation CUPS codes required before the given procedure.
// Returns nil if no prior consultation is required.
func GetConsultCupsFor(cupsCode string) []string {
	codes, ok := cupsRequiresPreviousDoctor[cupsCode]
	if !ok {
		return nil
	}
	return codes
}

// CheckPriorConsultation verifica si el CUPS requiere consulta previa y busca el médico
// de la última consulta pasada del paciente.
// Retorna (blocked, doctorDoc, message, error).
func (s *AppointmentService) CheckPriorConsultation(ctx context.Context, cupsCode, patientID string) (bool, string, string, error) {
	consultCups := GetConsultCupsFor(cupsCode)
	if consultCups == nil {
		return false, "", "", nil
	}

	doctor, err := s.repo.FindLastDoctorForCups(ctx, patientID, consultCups)
	if err != nil {
		return false, "", "", err
	}

	if doctor == "" {
		return true, "", "Este procedimiento requiere una *consulta previa* con el especialista. Por favor agenda primero la consulta y luego el examen.", nil
	}

	return false, doctor, "", nil
}

// orderQuantity resuelve la cantidad de la orden en curso. La orden normalmente llega con el CUP
// BASE (sin sufijo) y la cantidad se define antes, en el OCR (notación "(#N)", ver ocr_service.go),
// así que el caller la pasa en `quantity`. Si no se pasó (<1) se cae al sufijo del CUP (CupQuantity),
// que cubre el caso en que el código sí trae la variante embebida.
func orderQuantity(cupsCode string, quantity int) int {
	if quantity >= 1 {
		return quantity
	}
	return utils.CupQuantity(cupsCode)
}

// CheckMRCLimit verifica si el grupo CUPS ha alcanzado el límite mensual (mes actual).
// Solo aplica a pacientes MRC (contrato 5/6). Deshabilitado con CUPS_GROUP_LIMITS_ENABLED=false.
// quantity = cantidad del procedimiento actual (del OCR); 0 → se deriva del sufijo del CUP.
func (s *AppointmentService) CheckMRCLimit(ctx context.Context, cupsCode, contractCode string, quantity int) (bool, string, error) {
	if s.cfg != nil && !s.cfg.CupsGroupLimitsEnabled {
		return false, "", nil
	}
	if !IsMRCPatient(contractCode) {
		return false, "", nil
	}

	groupName, maxPerMonth, found := IsMRCGroupCups(cupsCode)
	if !found {
		return false, "", nil
	}

	now := time.Now()
	count, err := s.repo.CountMonthlyByGroup(ctx, mrcGroups[groupName].CupsCodes, now.Year(), int(now.Month()))
	if err != nil {
		return false, "", err
	}

	// Se evalúa si la NUEVA orden cabe en el cupo del mes: consumido + cantidad de esta orden.
	// Bloquear con `count >= max` dejaba pasar la cita que CRUZA el tope (ej: 930/932 + orden de 8 → 938).
	if count+orderQuantity(cupsCode, quantity) > maxPerMonth {
		return true, fmt.Sprintf("Se ha alcanzado el límite mensual de %d para %s (MRC). Por favor contacta a la clínica.", maxPerMonth, groupName), nil
	}

	return false, "", nil
}

// CheckMRCLimitForMonth verifica si el grupo CUPS ha alcanzado el límite MRC para un mes específico.
// Retorna true si está bloqueado (al límite). Solo aplica a pacientes MRC (contrato 5/6).
func (s *AppointmentService) CheckMRCLimitForMonth(ctx context.Context, cupsCode, contractCode string, quantity, year, month int) (bool, error) {
	if s.cfg != nil && !s.cfg.CupsGroupLimitsEnabled {
		return false, nil
	}
	if !IsMRCPatient(contractCode) {
		return false, nil
	}

	groupName, maxPerMonth, found := IsMRCGroupCups(cupsCode)
	if !found {
		return false, nil
	}

	count, err := s.repo.CountMonthlyByGroup(ctx, mrcGroups[groupName].CupsCodes, year, month)
	if err != nil {
		return false, err
	}

	// Cabe en el mes solo si consumido + cantidad de esta orden no supera el tope (ver CheckMRCLimit).
	return count+orderQuantity(cupsCode, quantity) > maxPerMonth, nil
}

// HasExistingAppointment verifica si el paciente ya tiene una cita futura para el CUPS.
func (s *AppointmentService) HasExistingAppointment(ctx context.Context, patientID, cupCode string) (bool, error) {
	return s.repo.HasFutureForCup(ctx, patientID, cupCode)
}

// GetDoctorAgeRestriction retorna la restricción de edad para un doctor, si existe.
func GetDoctorAgeRestriction(doctorDoc string) (minAge int, reason string, exists bool) {
	r, ok := doctorAgeRestrictions[doctorDoc]
	if !ok {
		return 0, "", false
	}
	return r.MinAge, r.Reason, true
}

// CreateWithConsecutive crea UNA sola cita que ocupa `espacios` slots contiguos.
// Modelo correcto de SIESA (validado contra BD): una cita se asocia a N slots vía
// programacion_medico_detalle.IdCita; NO se crean N citas. repo.Create reclama el slot inicial
// y los espacios-1 adicionales de forma atómica (input.Espacios). No requiere intervalo: los
// slots ya existen con su espaciado real y se toman los próximos N por Fecha.
func (s *AppointmentService) CreateWithConsecutive(ctx context.Context, input domain.CreateAppointmentInput, espacios int) (string, error) {
	if espacios < 1 {
		espacios = 1
	}
	input.Espacios = espacios

	// repo.Create inserta la cita, reclama los N slots contiguos Y registra TODOS los CUPS
	// (citas_procedimientos / citas_procedimientos_asuntos) en UNA sola transacción de SIESA: todo
	// o nada (audit #26). Antes los CUPS se insertaban en un segundo paso FUERA de la tx y un fallo
	// parcial dejaba filas huérfanas (CUPS apuntando a una cita cancelada) o una cita sin
	// procedimientos; la compensación por CancelBatch además podía fallar dejando una cita activa
	// huérfana. Con la inserción atómica dentro de Create eso ya no puede ocurrir.
	appt, err := s.repo.Create(ctx, input)
	if err != nil {
		return "", err
	}

	// Auditoría SIESA: con la cita y sus CUPS ya committeados, registrar log_citas +
	// log_citas_procedimientos (paridad con la UI). Best-effort/asíncrono — no afecta el resultado.
	s.repo.WriteCreationAudit(ctx, appt.ID, input.Observations)

	return appt.ID, nil
}

// FindLastDoctorForCups returns the document of the last doctor who attended the patient for any of the given CUPS codes.
func (s *AppointmentService) FindLastDoctorForCups(ctx context.Context, patientID string, cups []string) (string, error) {
	return s.repo.FindLastDoctorForCups(ctx, patientID, cups)
}

// SlotCountForAppointment devuelve el número real de slots que ocupa la cita en SIESA
// (1 cita / N slots). Es la fuente de verdad para re-reservar la misma cantidad de
// espacios al reagendar; NO debe derivarse de len(block), que con el modelo multi-slot
// vale 1 sin importar cuántos slots ocupe la cita.
func (s *AppointmentService) SlotCountForAppointment(ctx context.Context, apptID string) (int, error) {
	return s.repo.SlotCountForAppointment(ctx, apptID)
}

// FindPendingEmgAppointment delega en el repo: cita EMG futura pendiente más próxima del paciente
// (ancla para consolidar órdenes EMG/NC separadas).
func (s *AppointmentService) FindPendingEmgAppointment(ctx context.Context, patientID string, emgCodes []string) (*domain.Appointment, error) {
	return s.repo.FindPendingEmgAppointment(ctx, patientID, emgCodes)
}

// ConsolidateResult describe el resultado de consolidar CUPS en una cita EMG existente.
type ConsolidateResult struct {
	AddedCups       []string // CUPS agregados a la cita (camino in-place)
	NeedsReschedule bool     // true si el bloque combinado requiere más slots o cambiar cantidades → reprogramar
	Espacios        int      // espacios del bloque combinado según la regla de Fisiatría
}

// ConsolidateIntoAppointment agrega los CUPS de una orden nueva (dependientes/NC del bloque de Fisiatría)
// a una cita EMG existente, reproduciendo applyFisiatriaRules sobre el conjunto combinado (CUPS de la
// cita + nuevos). Camino IN-PLACE: si el bloque combinado cabe en los slots ya bloqueados y no cambia la
// cantidad de ningún CUP ya presente, inserta solo las filas de citas_procedimientos que faltan (misma
// cita y horario). Si el bloque crece en slots o cambiaría una cantidad existente (p.ej. la orden nueva
// trae EMG y sube el conteo) → NO muta nada y devuelve NeedsReschedule=true; el flujo de agendamiento
// reprograma el bloque completo con la maquinaria de reserva. El Servicio/tabla de cada CUP lo resuelve
// el repo (resolveProcServicio).
func (s *AppointmentService) ConsolidateIntoAppointment(ctx context.Context, appt *domain.Appointment, newCups []CUPSEntry) (ConsolidateResult, error) {
	if appt == nil {
		return ConsolidateResult{}, fmt.Errorf("cita nil")
	}
	apptID, err := strconv.Atoi(appt.ID)
	if err != nil {
		return ConsolidateResult{}, fmt.Errorf("id de cita inválido %q: %w", appt.ID, err)
	}

	// Recomputar el bloque combinado con la regla de Fisiatría.
	combined := make([]CUPSEntry, 0, len(appt.Procedures)+len(newCups))
	for _, p := range appt.Procedures {
		combined = append(combined, CUPSEntry{Code: p.CupCode, Name: p.CupName, Quantity: p.Quantity})
	}
	combined = append(combined, newCups...)
	finalGroup := applyFisiatriaRules(CUPSGroup{ServiceType: "Fisiatria", Cups: combined, Espacios: 1})

	currentSlots, err := s.repo.SlotCountForAppointment(ctx, appt.ID)
	if err != nil {
		return ConsolidateResult{}, fmt.Errorf("conteo de slots: %w", err)
	}
	if currentSlots < 1 {
		currentSlots = 1
	}

	// El bloque combinado crece en slots → reprogramar (no in-place).
	if finalGroup.Espacios > currentSlots {
		return ConsolidateResult{NeedsReschedule: true, Espacios: finalGroup.Espacios}, nil
	}

	existing := make(map[string]int, len(appt.Procedures))
	for _, p := range appt.Procedures {
		existing[p.CupCode] = p.Quantity
	}

	var toAdd []domain.CreateAppointmentProcedureInput
	var added []string
	for _, c := range finalGroup.Cups {
		prevQty, ok := existing[c.Code]
		if ok {
			// Un CUP ya presente cambiaría de cantidad (p.ej. NC recalculada) → requiere rebuild.
			if prevQty != c.Quantity {
				return ConsolidateResult{NeedsReschedule: true, Espacios: finalGroup.Espacios}, nil
			}
			continue // ya está con la misma cantidad
		}
		toAdd = append(toAdd, domain.CreateAppointmentProcedureInput{
			AppointmentID: apptID,
			CupCode:       c.Code,
			Quantity:      c.Quantity,
		})
		added = append(added, c.Code)
	}

	if len(toAdd) == 0 {
		// El bloque ya cubría todos los CUPS nuevos (p.ej. la NC ya estaba sintetizada).
		return ConsolidateResult{Espacios: finalGroup.Espacios}, nil
	}
	if err := s.repo.CreateAppointmentProcedureBatch(ctx, toAdd); err != nil {
		return ConsolidateResult{}, fmt.Errorf("agregar procedimientos a la cita %d: %w", apptID, err)
	}
	return ConsolidateResult{AddedCups: added, Espacios: finalGroup.Espacios}, nil
}

// FindBlockByAppointmentID devuelve la cita y TODAS las citas del paciente ese día
// (modelo 1 cita = N slots; ya no se agrupan citas consecutivas estilo Antares).
func (s *AppointmentService) FindBlockByAppointmentID(ctx context.Context, apptID string) (*domain.Appointment, []domain.Appointment, error) {
	appt, err := s.repo.FindByID(ctx, apptID)
	if err != nil || appt == nil {
		return nil, nil, err
	}
	dayAppts, derr := s.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
	if derr != nil {
		// Degradación intencional: si falla la consulta del día, actuar al menos sobre la cita.
		return appt, []domain.Appointment{*appt}, nil //nolint:nilerr
	}
	if len(dayAppts) == 0 {
		return appt, []domain.Appointment{*appt}, nil
	}
	return appt, dayAppts, nil
}

// GetFirstCupName retorna el nombre del primer procedimiento de una cita
func GetFirstCupName(appt domain.Appointment) string {
	if len(appt.Procedures) > 0 && appt.Procedures[0].CupName != "" {
		return appt.Procedures[0].CupName
	}
	if len(appt.Procedures) > 0 {
		return appt.Procedures[0].CupCode
	}
	return "Procedimiento"
}

// GetFirstCupCode retorna el código del primer procedimiento de una cita
func GetFirstCupCode(appt domain.Appointment) string {
	if len(appt.Procedures) > 0 {
		return appt.Procedures[0].CupCode
	}
	return ""
}
