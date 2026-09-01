package services

import (
	"context"
	"errors"
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

// MRCGroupDemand describe la demanda de UNA cita sobre un grupo MRC: el CUP representativo del
// grupo dentro de la cita y la cantidad AGREGADA que la cita consume de ese grupo. Una cita
// multi-CUP puede demandar de varios grupos (o varios CUPS del mismo grupo: 891509-8 + 891515
// suman 9 en otros_procedimientos) — validar solo el CUP "principal" subcontaba la demanda.
type MRCGroupDemand struct {
	GroupName string
	RepCup    string
	Quantity  int
}

// MRCGroupDemands agrega las cantidades de los CUPS de una cita por grupo MRC. CUPS fuera de
// grupos MRC no aportan. Quantity<1 en la entrada cae al sufijo del código (CupQuantity).
func MRCGroupDemands(cups []CUPSEntry) []MRCGroupDemand {
	byGroup := map[string]*MRCGroupDemand{}
	var order []string
	for _, c := range cups {
		group, _, found := IsMRCGroupCups(c.Code)
		if !found {
			continue
		}
		d, ok := byGroup[group]
		if !ok {
			d = &MRCGroupDemand{GroupName: group, RepCup: c.Code}
			byGroup[group] = d
			order = append(order, group)
		}
		d.Quantity += orderQuantity(c.Code, c.Quantity)
	}
	out := make([]MRCGroupDemand, 0, len(order))
	for _, g := range order {
		out = append(out, *byGroup[g])
	}
	return out
}

// MRCGroupsCatalog expone el catálogo de grupos MRC (nombre → tope y CUPS) para auditoría y
// endpoints de solo lectura. Devuelve una copia superficial (los slices no deben mutarse).
func MRCGroupsCatalog() map[string]struct {
	MaxPerMonth int
	CupsCodes   []string
} {
	return mrcGroups
}

// ErrMRCLimitReached señala que la operación agregaría consumo de un grupo MRC por encima del tope mensual.
// Regla de negocio ABSOLUTA (12-ago-2026, reporte de la entidad: se agendó por encima del tope):
// NINGÚN camino — flujo principal, lista de espera, reprogramación, consolidación EMG — puede
// agendar superado el límite.
var ErrMRCLimitReached = errors.New("limite mensual MRC alcanzado")

// IsMRCPatient determina si un paciente está sujeto a validación MRC según su contrato.
// MRC = contratos 5 (SANITAS MRC SUBSIDIADO) y 6 (SANITAS MRC CONTRIBUTIVO).
// Reemplaza al antiguo IsMRCEntity, que comparaba el código de empresa (todos los
// contratos Sanitas comparten empresa "EPS005", así que nunca distinguía MRC de Evento).
// El contrato viene de sis_paci.contrato (domain.Patient.ContractCode → sesión patient_contract).
func IsMRCPatient(contractCode string) bool {
	return contractCode == "5" || contractCode == "6"
}

// EffectiveContractForCups ajusta el contrato SANITAS con el que queda la CITA según sus CUPS: el
// contrato MRC (5 subsidiado / 6 contributivo) SOLO aplica si al menos un CUP de la cita pertenece a un
// grupo MRC; si NINGÚN CUP es de un grupo MRC, la cita se agenda con el contrato EVENTO respetando el
// régimen (5→7 subsidiado, 6→4 contributivo). Contratos no-MRC (y no-SANITAS) se devuelven sin cambio.
func EffectiveContractForCups(contractCode string, cupsCodes []string) string {
	if !IsMRCPatient(contractCode) {
		return contractCode
	}
	for _, c := range cupsCodes {
		if _, _, found := IsMRCGroupCups(c); found {
			return contractCode // al menos un CUP MRC → mantiene el contrato MRC
		}
	}
	// Ningún CUP de grupo MRC → degradar a Evento (respetando el régimen).
	switch contractCode {
	case "5": // SANITAS MRC SUBSIDIADO → SANITAS EVENTO SUBSIDIADO
		return "7"
	case "6": // SANITAS MRC CONTRIBUTIVO → SANITAS EVENTO CONTRIBUTIVO
		return "4"
	}
	return contractCode
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

// bloqueoCups: CUPS de BLOQUEOS (neurología/dolor) que el bot NO debe agendar a pacientes SANITAS
// (regla de negocio: los bloqueos SANITAS se agendan manualmente por un agente). Familia 053101–053115
// (todos "BLOQUEO DE…") + 048101. Hoy solo 053105 está en el catálogo del bot, pero se lista la familia
// completa como defensa ante futuros mapeos o códigos que lleguen en una orden.
var bloqueoCups = map[string]bool{
	"048101": true,
	"053101": true, "053102": true, "053103": true, "053104": true, "053105": true,
	"053106": true, "053107": true, "053108": true, "053109": true, "053110": true,
	"053111": true, "053112": true, "053113": true, "053114": true, "053115": true,
}

// IsBloqueoCups indica si el CUP (por su código BASE, sin sufijo) es un bloqueo.
func IsBloqueoCups(cupsCode string) bool {
	return bloqueoCups[utils.BaseCupCode(cupsCode)]
}

// IsSanitasContract indica si el contrato es SANITAS (Evento 4/7 + MRC 5/6). Se usa para la regla de
// bloqueos: los bloqueos SANITAS no los agenda el bot.
func IsSanitasContract(contractCode string) bool {
	switch contractCode {
	case "4", "5", "6", "7":
		return true
	}
	return false
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
	count, err := s.repo.CountMonthlyByGroup(ctx, mrcGroups[groupName].CupsCodes, now.Year(), int(now.Month()), "")
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
// excludeApptID (si != "") descuenta esa cita del conteo del mes: se usa al REPROGRAMAR para no contar
// la cita que se está moviendo (mismo mes → su cantidad no cuenta; otro mes → no está en ese conteo).
// Devuelve tambien el CONSUMIDO del grupo en ese mes: sin ese numero, un evento de bloqueo dice
// "algo se freno" pero no cuanto nos pasamos del tope, que es justo lo que no se pudo responder en
// la auditoria del 01-sep-2026. consumed es 0 cuando la comprobacion no aplica (flag apagado, no
// MRC, o CUPS fuera de grupo).
func (s *AppointmentService) CheckMRCLimitForMonth(ctx context.Context, cupsCode, contractCode string, quantity, year, month int, excludeApptID string) (blocked bool, consumed int, err error) {
	if s.cfg != nil && !s.cfg.CupsGroupLimitsEnabled {
		return false, 0, nil
	}
	if !IsMRCPatient(contractCode) {
		return false, 0, nil
	}

	groupName, maxPerMonth, found := IsMRCGroupCups(cupsCode)
	if !found {
		return false, 0, nil
	}

	count, err := s.repo.CountMonthlyByGroup(ctx, mrcGroups[groupName].CupsCodes, year, month, excludeApptID)
	if err != nil {
		return false, 0, err
	}

	// Cabe en el mes solo si consumido + cantidad de esta orden no supera el tope (ver CheckMRCLimit).
	return count+orderQuantity(cupsCode, quantity) > maxPerMonth, count, nil
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

// AppointmentAsunto delega en el repo: asunto_id de la cita (17 = SOPORTE SEDACIÓN). Se usa al
// reprogramar/confirmar para detectar sedación por agenda cuando la observación no la trae.
func (s *AppointmentService) AppointmentAsunto(ctx context.Context, apptID string) (int, error) {
	return s.repo.AppointmentAsunto(ctx, apptID)
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
func (s *AppointmentService) ConsolidateIntoAppointment(ctx context.Context, appt *domain.Appointment, newCups []CUPSEntry, contractCode string) (ConsolidateResult, error) {
	if appt == nil {
		return ConsolidateResult{}, fmt.Errorf("cita nil")
	}
	apptID, err := strconv.Atoi(appt.ID)
	if err != nil {
		return ConsolidateResult{}, fmt.Errorf("id de cita inválido %q: %w", appt.ID, err)
	}

	// Recomputar el bloque combinado con la regla de Fisiatría. Los CUPS de la cita se normalizan al
	// código BASE con cantidad EFECTIVA (la NC se almacena como "891509-8" = 8; applyFisiatriaRules solo
	// reconoce la base "891509"). Sin esto la NC no se reconocería y se sintetizaría/duplicaría.
	combined := make([]CUPSEntry, 0, len(appt.Procedures)+len(newCups))
	for _, p := range appt.Procedures {
		qty := p.Quantity
		if sfx := utils.CupQuantity(p.CupCode); sfx > 1 {
			qty = sfx
		}
		combined = append(combined, CUPSEntry{Code: utils.BaseCupCode(p.CupCode), Name: p.CupName, Quantity: qty})
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

	// Índice de CUPS ya presentes, normalizado al código BASE con su cantidad EFECTIVA: la cita puede
	// almacenar la NC con sufijo (ej. "891509-8" = 8 unidades, Cantidad=1), mientras applyFisiatriaRules
	// produce el código base "891509" con Quantity=8. Sin normalizar se duplicaría la NC.
	existing := make(map[string]int, len(appt.Procedures))
	for _, p := range appt.Procedures {
		qty := p.Quantity
		if sfx := utils.CupQuantity(p.CupCode); sfx > 1 { // el sufijo ES la cantidad
			qty = sfx
		}
		existing[utils.BaseCupCode(p.CupCode)] = qty
	}

	var toAdd []domain.CreateAppointmentProcedureInput
	var added []string
	for _, c := range finalGroup.Cups {
		base := utils.BaseCupCode(c.Code)
		prevQty, ok := existing[base]
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

	// GATE MRC (H145: la consolidación agregaba cantidades — NC-8/16 — a la cita SIN validar el
	// tope mensual del grupo). La demanda son SOLO los CUPS que se van a AGREGAR (los existentes
	// ya cuentan en el consumo del mes); el mes evaluado es el de la CITA ancla. Regla absoluta:
	// superado el tope, no se agrega — el flujo informa al paciente.
	if IsMRCPatient(contractCode) {
		addEntries := make([]CUPSEntry, 0, len(toAdd))
		for _, p := range toAdd {
			addEntries = append(addEntries, CUPSEntry{Code: p.CupCode, Quantity: p.Quantity})
		}
		for _, d := range MRCGroupDemands(addEntries) {
			blocked, _, cerr := s.CheckMRCLimitForMonth(ctx, d.RepCup, contractCode, d.Quantity,
				appt.Date.Year(), int(appt.Date.Month()), "")
			if cerr != nil {
				// Fail-CLOSED: una consolidación es opcional (el paciente conserva su cita EMG);
				// ante la duda no se agrega consumo de un grupo con tope contractual.
				return ConsolidateResult{}, fmt.Errorf("verificando tope MRC (%s): %w", d.GroupName, cerr)
			}
			if blocked {
				return ConsolidateResult{}, fmt.Errorf("%w: grupo %s en %04d-%02d",
					ErrMRCLimitReached, d.GroupName, appt.Date.Year(), int(appt.Date.Month()))
			}
		}
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
