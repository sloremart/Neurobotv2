package siesa

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

// isUniqueViolation reporta si err es una violación de PK/índice único de SQL Server
// (2627 = PRIMARY KEY, 2601 = índice único). En `citas` esto significa que el médico ya
// tiene una cita activa a esa fecha/hora → el horario ya no está disponible.
func isUniqueViolation(err error) bool {
	var me mssql.Error
	if errors.As(err, &me) {
		return me.Number == 2627 || me.Number == 2601
	}
	return false
}

// queryer abstrae *sql.DB y *sql.Tx para reutilizar la misma lógica de inserción de un
// procedimiento (insertProcedureTx) en dos contextos: fuera de transacción (CreateAppointmentProcedure
// standalone, con r.db) y DENTRO de la transacción de Create (cita + slots + CUPS atómicos).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var _ repository.AppointmentRepository = (*AppointmentRepo)(nil)

// AppointmentRepo manages appointments in the external SQL Server database.
//
// Field mapping:
//
//	Appointment.ID             ← citas.id  (IDENTITY)
//	Appointment.PatientID      ← citas.autoid (int → string)
//	Appointment.DoctorID       ← citas.cod_medi (int → string, internal code)
//	Appointment.DoctorDocument ← sis_medi.cedula (real document number)
//	Appointment.DoctorName     ← sis_medi.nombre (full name)
//	Appointment.Entity         ← citas.contrato (int → string)
//	Appointment.AgendaID       ← citas.id_programacion
//	Appointment.Date           ← citas.fecha (DATE)
//	Appointment.TimeSlot       ← "YYYYMMDDHHmm" derived from fecha+hora
//	Appointment.Canceled       ← citas.estado = 'C'
//	Appointment.Confirmed      ← citas.AsistenciaConfirmada = 1 OR estado IN ('CC','A')
type AppointmentRepo struct {
	db *sql.DB
	// assignCedula → citas.cod_user_asigna_cita (la columna guarda la CÉDULA del funcionario).
	// botUserID    → usuario.id para usuario_evento / id_usuario_cancela / IdUsuarioConfirmaAsistencia.
	// Ambos identifican al bot para trazabilidad en SIESA; configurables, con fallback al usuario
	// de automatización (ver defaultSiesaBotCedula / defaultSiesaBotUserID).
	assignCedula string
	botUserID    int

	// #33 (auditoría): las escrituras de log_citas son fire-and-forget (goroutines detached con
	// context.WithoutCancel) y tocan la BD. bgWG las rastrea para que el apagado pueda esperarlas
	// (WaitBackground) ANTES de cerrar el pool — si no, una auditoría en vuelo veía "sql: database is
	// closed". Best-effort: si no terminan dentro del timeout, el apagado continúa igual.
	bgWG sync.WaitGroup
}

// NewAppointmentRepo construye el repo. assignCedula y botUserID definen la identidad del bot en
// SIESA (usuario PRINCIPAL). Si llegan vacío/cero, caen al usuario de automatización como FALLBACK.
func NewAppointmentRepo(db *sql.DB, assignCedula string, botUserID int) *AppointmentRepo {
	if assignCedula == "" {
		assignCedula = defaultSiesaBotCedula
	}
	if botUserID == 0 {
		botUserID = defaultSiesaBotUserID
	}
	return &AppointmentRepo{db: db, assignCedula: assignCedula, botUserID: botUserID}
}

// WaitBackground espera (hasta timeout) a que terminen las escrituras de auditoría fire-and-forget en
// vuelo (#33). Se llama en el apagado ANTES de cerrar el pool de BD, para que una auditoría a medio
// camino no falle con "sql: database is closed". Best-effort: si vence el timeout, retorna igual y el
// apagado continúa (la auditoría es best-effort; la cita ya está committeada).
func (r *AppointmentRepo) WaitBackground(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		r.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("apagado: timeout esperando auditorías SIESA en vuelo", "timeout", timeout)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// slotToDateTimeComponents converts "YYYYMMDDHHmm" → date "YYYY-MM-DD", timeStr "HH:mm", meridiem "am"|"pm".
// El slot SIEMPRE viene en 24h (lo arma SlotService desde pmd.Fecha), así que el meridiano se
// deriva directamente: <12 → am, ≥12 → pm. (N-4: la heurística previa "1-6 → pm" marcaba como
// PM los slots reales de 5–6 AM, p.ej. polisomnografía/EEG matutino, corrompiendo la UI/recordatorios.)
// NOTA pendiente (no es este fix): timeStr queda en 24h ("13:00") mientras SIESA/staff usan 12h
// ("01:00"+pm); es una discrepancia de convención separada, documentada para decisión.
func slotToDateTimeComponents(slot string) (date, timeStr, meridiem string) {
	if len(slot) < 12 {
		return "", "", ""
	}
	date = fmt.Sprintf("%s-%s-%s", slot[0:4], slot[4:6], slot[6:8])
	timeStr = fmt.Sprintf("%s:%s", slot[8:10], slot[10:12])
	hInt, _ := strconv.Atoi(slot[8:10])
	if hInt >= 12 {
		meridiem = "pm"
	} else {
		meridiem = "am"
	}
	return
}

// timecodeFromDateAndTime converts date + timeStr + meridiem → "YYYYMMDDHHmm" in 24h format.
// If SIESA stores the time in 12h format (e.g. "02:00" with meridiem "pm"), converts to 24h.
func timecodeFromDateAndTime(date time.Time, timeStr, meridiem string) string {
	clean := strings.ReplaceAll(timeStr, ":", "")
	if len(clean) < 4 {
		return date.Format("20060102") + "0000"
	}
	h, _ := strconv.Atoi(clean[:2])
	m, _ := strconv.Atoi(clean[2:4])
	mer := strings.ToLower(strings.TrimSpace(meridiem))
	if mer == "pm" && h < 12 {
		h += 12
	} else if mer == "am" && h == 12 {
		h = 0
	}
	return fmt.Sprintf("%s%02d%02d", date.Format("20060102"), h, m)
}

// timecodeFromAppointment builds the timecode "YYYYMMDDHHmm" prioritizing the 24h time
// obtained directly from programacion_medico_detalle.Fecha (time24h = HOUR*100+MINUTE).
// If time24h == -1 (slot not found), falls back to timeStr+meridiem calculation.
// This fixes appointments created from the SIESA UI where meridiano is NULL.
func timecodeFromAppointment(date time.Time, timeStr, meridiem string, time24h int) string {
	if time24h >= 0 {
		h := time24h / 100
		m := time24h % 100
		return fmt.Sprintf("%s%02d%02d", date.Format("20060102"), h, m)
	}
	return timecodeFromDateAndTime(date, timeStr, meridiem)
}

// locationIDForSubjectType returns the SIESA location (id_sede) for a given subject type.
// Validated against 5,549 real SIESA appointments:
//   - Subjects 2,3,4,5 (imaging: RX, CT, MRI, Mammo) → location 3
//   - Subject 1 (Consulta Fisiatría) → location 2 (83% of 375 real appointments)
//   - Subject 12 (PET/CT) → location 2 (100% of 18 real appointments)
//   - All others → location 2
//
// NOTE: This is now only a DEFENSIVE FALLBACK. The primary source for citas.id_sede is the
// selected slot's agenda (programacion_medico.id_sede), passed as CreateAppointmentInput.AgendaSede.
// This map is used only when that value is unset (0).
func locationIDForSubjectType(subjectType int) int {
	switch subjectType {
	case 2, 3, 4, 5:
		return 3
	default:
		return 2
	}
}

// isConsultationSubject returns true for subject types that insert into
// citas_procedimientos_asuntos (CPA) instead of citas_procedimientos (CP).
// Validated against SIESA data: subjects 1,7,8,9,10,11 → CPA (86-100% of real appointments).
func isConsultationSubject(subjectType int) bool {
	switch subjectType {
	case 1, 7, 8, 9, 10, 11:
		return true
	}
	return false
}

// inParams genera "@p{start}, @p{start+1}, ..." y los args correspondientes.
// Usar startAt=1 cuando los IDs son los únicos params; startAt=N para queries con params previos.
func inParams(values []string, startAt int) (clause string, args []interface{}) {
	parts := make([]string, len(values))
	args = make([]interface{}, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprintf("@p%d", startAt+i)
		args[i] = v
	}
	return strings.Join(parts, ","), args
}

// inParamsAsInt is like inParams but converts string IDs to int64.
// Use for comparisons with INT columns like IdCita/id_cita: avoids CAST on the column
// which destroys index usage and causes full table scans with prolonged locks.
func inParamsAsInt(ids []string, startAt int) (clause string, args []interface{}) {
	parts := make([]string, len(ids))
	args = make([]interface{}, len(ids))
	for i, v := range ids {
		parts[i] = fmt.Sprintf("@p%d", startAt+i)
		n, _ := strconv.ParseInt(v, 10, 64)
		args[i] = n
	}
	return strings.Join(parts, ","), args
}

// lookupContract resolves company code, contract code, and regime from:
//   - the patient's own contract (sis_paci.contrato, e.g. "6") → direct lookup (preferred)
//   - a numeric contract code ("4") → direct lookup by contratos.codigo
//   - a company code ("EPS005") → first active contract for that company
//
// BUG-10: preferring patientContract is what books MRC patients (contract 5/6, manual 8)
// correctly. The company-code fallback uses ORDER BY codigo, which always returned the
// lowest active contract (4, Evento, manual 11) — wrong billing manual for MRC patients.
func lookupContract(ctx context.Context, db *sql.DB, entityInput, patientContract string) (company, contractCode string, regime int, err error) {
	if patientContract != "" {
		err = db.QueryRowContext(
			ctx,
			`SELECT ISNULL(empresa,''), CAST(codigo AS VARCHAR(20)), ISNULL(regimen,1) FROM contratos WITH (NOLOCK) WHERE codigo = @p1 AND activo = 1`,
			patientContract,
		).Scan(&company, &contractCode, &regime)
		if err == nil {
			return
		}
		if err != sql.ErrNoRows {
			return
		}
		err = nil // contract not found/active → fall through to entity-based resolution
	}
	if codeInt, convErr := strconv.Atoi(entityInput); convErr == nil {
		err = db.QueryRowContext(
			ctx,
			`SELECT ISNULL(empresa,''), CAST(codigo AS VARCHAR(20)), ISNULL(regimen,1) FROM contratos WITH (NOLOCK) WHERE codigo = @p1`,
			codeInt,
		).Scan(&company, &contractCode, &regime)
	} else {
		// company code (e.g. "EPS005") → first active contract
		err = db.QueryRowContext(
			ctx,
			`SELECT TOP 1 ISNULL(empresa,''), CAST(codigo AS VARCHAR(20)), ISNULL(regimen,1) FROM contratos WITH (NOLOCK) WHERE empresa = @p1 AND activo = 1 ORDER BY codigo`,
			entityInput,
		).Scan(&company, &contractCode, &regime)
	}
	if err == sql.ErrNoRows {
		return entityInput, entityInput, 1, nil
	}
	return
}

// ────────────────────────────────────────────────────────────────────────────
// FindByID
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) FindByID(ctx context.Context, id string) (*domain.Appointment, error) {
	var appt domain.Appointment
	var fecha time.Time
	var hora, estado string
	var asistenciaConfirmada int

	var meridiano string
	var hhmm24 int
	err := r.db.QueryRowContext(
		ctx, `
	SELECT CAST(c.id AS VARCHAR(20)),
	       CAST(c.fecha AS DATE),
	       ISNULL(c.hora,''),
	       ISNULL(c.meridiano,''),
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       ISNULL(RTRIM((SELECT TOP 1 sm.nombre FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi)),
	           CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20))),
	       ISNULL(CAST((SELECT TOP 1 sm.cedula FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi) AS VARCHAR(20)),''),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT),
	       ISNULL((SELECT TOP 1 DATEPART(HOUR,pmd.Fecha)*100+DATEPART(MINUTE,pmd.Fecha)
	               FROM programacion_medico_detalle pmd WITH (NOLOCK) WHERE pmd.IdCita=c.id ORDER BY pmd.Fecha),-1)
	FROM citas c WITH (NOLOCK)
	WHERE c.id = @p1`, id,
	).Scan(
		&appt.ID, &fecha, &hora, &meridiano,
		&appt.DoctorID, &appt.DoctorName, &appt.DoctorDocument, &appt.PatientID, &appt.Entity, &appt.AgendaID,
		&estado, &appt.Observations, &asistenciaConfirmada, &hhmm24,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("siesa FindByID %s: %w", id, err)
	}

	appt.Date = fecha
	appt.TimeSlot = timecodeFromAppointment(fecha, hora, meridiano, hhmm24)
	appt.Canceled = strings.EqualFold(estado, "C")
	appt.Confirmed = asistenciaConfirmada == 1 || strings.EqualFold(estado, "CC") || strings.EqualFold(estado, "A")

	appt.Procedures, _ = r.fetchProcedures(ctx, appt.ID)
	return &appt, nil
}

// ────────────────────────────────────────────────────────────────────────────
// FindUpcomingByPatient
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) FindUpcomingByPatient(ctx context.Context, patientID string) ([]domain.Appointment, error) {
	rows, err := r.db.QueryContext(ctx, `
	SELECT CAST(c.id AS VARCHAR(20)),
	       CAST(c.fecha AS DATE),
	       ISNULL(c.hora,''),
	       ISNULL(c.meridiano,''),
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       ISNULL(RTRIM((SELECT TOP 1 sm.nombre FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi)),
	           CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20))),
	       ISNULL(CAST((SELECT TOP 1 sm.cedula FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi) AS VARCHAR(20)),''),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT),
	       ISNULL((SELECT TOP 1 DATEPART(HOUR,pmd.Fecha)*100+DATEPART(MINUTE,pmd.Fecha)
	               FROM programacion_medico_detalle pmd WITH (NOLOCK) WHERE pmd.IdCita=c.id ORDER BY pmd.Fecha),-1)
	FROM citas c WITH (NOLOCK)
	WHERE c.autoid = @p1
	  AND c.fecha >= CAST(GETDATE() AS DATE)
	  AND c.estado != 'C'
	ORDER BY c.fecha, c.hora`, patientID)
	if err != nil {
		return nil, fmt.Errorf("siesa FindUpcomingByPatient: %w", err)
	}
	defer rows.Close()

	return r.scanAppointments(ctx, rows)
}

// FindPendingEmgAppointment devuelve la cita futura PENDIENTE (estado 'P', fecha>=hoy) MÁS PRÓXIMA del
// paciente cuyos procedimientos (citas_procedimientos) incluyen al menos un CUP de EMG (Grupo 1). Es el
// ancla para consolidar órdenes EMG/NC separadas en una sola cita. nil si no hay. Los procedimientos y
// el horario vienen cargados por scanAppointments.
func (r *AppointmentRepo) FindPendingEmgAppointment(ctx context.Context, patientID string, emgCodes []string) (*domain.Appointment, error) {
	if patientID == "" || len(emgCodes) == 0 {
		return nil, nil
	}
	ph := make([]string, len(emgCodes))
	args := make([]interface{}, 0, len(emgCodes)+1)
	args = append(args, patientID)
	for i, c := range emgCodes {
		ph[i] = "@p" + strconv.Itoa(i+2)
		args = append(args, c)
	}
	//nolint:gosec // G202: ph = placeholders @pN fijos, valores parametrizados; sin input de usuario en el SQL.
	query := `
	SELECT TOP 1 CAST(c.id AS VARCHAR(20)),
	       CAST(c.fecha AS DATE),
	       ISNULL(c.hora,''),
	       ISNULL(c.meridiano,''),
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       ISNULL(RTRIM((SELECT TOP 1 sm.nombre FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi)),
	           CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20))),
	       ISNULL(CAST((SELECT TOP 1 sm.cedula FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi) AS VARCHAR(20)),''),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT),
	       ISNULL((SELECT TOP 1 DATEPART(HOUR,pmd.Fecha)*100+DATEPART(MINUTE,pmd.Fecha)
	               FROM programacion_medico_detalle pmd WITH (NOLOCK) WHERE pmd.IdCita=c.id ORDER BY pmd.Fecha),-1)
	FROM citas c WITH (NOLOCK)
	WHERE c.autoid = @p1
	  AND c.estado = 'P'
	  AND c.fecha >= CAST(GETDATE() AS DATE)
	  AND EXISTS (SELECT 1 FROM citas_procedimientos cp WITH (NOLOCK)
	              WHERE cp.id_cita = c.id AND cp.id_procedimiento IN (` + strings.Join(ph, ",") + `))
	ORDER BY c.fecha, c.hora`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("siesa FindPendingEmgAppointment: %w", err)
	}
	defer func() { _ = rows.Close() }()

	appts, err := r.scanAppointments(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(appts) == 0 {
		return nil, nil
	}
	return &appts[0], nil
}

// ────────────────────────────────────────────────────────────────────────────
// FindByAgendaAndDate
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
	rows, err := r.db.QueryContext(ctx, `
	SELECT CAST(c.id AS VARCHAR(20)),
	       CAST(c.fecha AS DATE),
	       ISNULL(c.hora,''),
	       ISNULL(c.meridiano,''),
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       ISNULL(RTRIM((SELECT TOP 1 sm.nombre FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi)),
	           CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20))),
	       ISNULL(CAST((SELECT TOP 1 sm.cedula FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi) AS VARCHAR(20)),''),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT),
	       ISNULL((SELECT TOP 1 DATEPART(HOUR,pmd.Fecha)*100+DATEPART(MINUTE,pmd.Fecha)
	               FROM programacion_medico_detalle pmd WITH (NOLOCK) WHERE pmd.IdCita=c.id ORDER BY pmd.Fecha),-1)
	FROM citas c WITH (NOLOCK)
	WHERE c.id_programacion = @p1
	  AND c.fecha = @p2
	  AND c.estado != 'C'
	ORDER BY c.hora`, agendaID, date)
	if err != nil {
		return nil, fmt.Errorf("siesa FindByAgendaAndDate: %w", err)
	}
	defer rows.Close()

	return r.scanAppointments(ctx, rows)
}

// scanAppointments scans appointment rows (without PatientName/Phone) and loads procedures.
func (r *AppointmentRepo) scanAppointments(ctx context.Context, rows *sql.Rows) ([]domain.Appointment, error) {
	var appointments []domain.Appointment
	var ids []string
	for rows.Next() {
		var appt domain.Appointment
		var fecha time.Time
		var hora, meridiano, estado string
		var asistenciaConfirmada, hhmm24 int
		if err := rows.Scan(
			&appt.ID, &fecha, &hora, &meridiano,
			&appt.DoctorID, &appt.DoctorName, &appt.DoctorDocument, &appt.PatientID, &appt.Entity, &appt.AgendaID,
			&estado, &appt.Observations, &asistenciaConfirmada, &hhmm24,
		); err != nil {
			return nil, err
		}
		appt.Date = fecha
		appt.TimeSlot = timecodeFromAppointment(fecha, hora, meridiano, hhmm24)
		appt.Canceled = strings.EqualFold(estado, "C")
		appt.Confirmed = asistenciaConfirmada == 1 || strings.EqualFold(estado, "CC") || strings.EqualFold(estado, "A")
		appointments = append(appointments, appt)
		ids = append(ids, appt.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		procMap, err := r.fetchProceduresBatch(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range appointments {
			appointments[i].Procedures = procMap[appointments[i].ID]
		}
	}
	return appointments, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Create
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) Create(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
	fecha, hora, meridiano := slotToDateTimeComponents(input.TimeSlot)
	if fecha == "" {
		return nil, fmt.Errorf("invalid timeslot: %q", input.TimeSlot)
	}

	company, contractCode, regime, err := lookupContract(ctx, r.db, input.Entity, input.ContractCode)
	if err != nil {
		return nil, fmt.Errorf("lookup contract %q: %w", input.Entity, err)
	}
	doctorCodeInt, _ := strconv.Atoi(input.DoctorID)
	patientIDInt, _ := strconv.Atoi(input.PatientID)

	// SIESA subject type (asunto). Primary source: the local CUPS catalog, resolved by
	// the caller and passed in (deterministic — cups_procedimientos.asunto_id, or 17 for
	// sedation). Defensive fallback when unresolved (CUPS absent from the catalog): the
	// legacy SIESA-history lookup, so we never insert citas.asunto = 0.
	subjectType := input.SubjectType
	if subjectType == 0 {
		subjectType = r.lookupSubjectTypeFromHistory(ctx, input.Procedures)
		slog.Warn("subject_type_fallback_to_history",
			"resolved_asunto", subjectType, "patient_id", input.PatientID, "agenda_id", input.AgendaID)
	}
	// Defensa: nunca agendar con un asunto adivinado. En el flujo normal esto no ocurre
	// (el CUPS se valida contra el catálogo antes de agendar, así que SubjectType siempre
	// llega > 0). Si aun así no se resolvió, fallar para que el handler escale en vez de
	// insertar un asunto incorrecto (históricamente esto causaba citas mal clasificadas).
	if subjectType == 0 {
		return nil, fmt.Errorf("no se pudo resolver el asunto para CUPS %v (patient=%s); no se agenda para evitar asunto incorrecto", input.Procedures, input.PatientID)
	}
	// id_sede comes from the selected slot's agenda (programacion_medico.id_sede), which is
	// ground truth. Fall back to the subject-based map only when the slot did not carry it (0).
	locationID := input.AgendaSede
	if locationID == 0 {
		locationID = locationIDForSubjectType(subjectType)
	}
	userType := "01"
	if regime == 2 {
		userType = "02"
	}

	// tipo_servicio (cabecera): NULL para consultas; para procedimientos/imágenes, el Servicio
	// CANÓNICO desde la tabla `servicios` por (contrato, cod_proc) — igual que el detalle
	// (ver resolveProcServicio). Reemplaza la derivación por historia (ORDER BY fecha DESC).
	var serviceType interface{}
	if !isConsultationSubject(subjectType) && len(input.Procedures) > 0 {
		baseCups := strings.SplitN(input.Procedures[0].CupCode, "-", 2)[0]
		contratoInt, _ := strconv.Atoi(contractCode)
		if svc := r.resolveProcServicio(ctx, r.db, contratoInt, baseCups, input.Procedures[0].CupCode); svc > 0 {
			serviceType = svc
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// primera_vez_control: regla DERIVADA DEL HISTÓRICO (no hay catálogo ni constraint en la
	// BD que defina 1/2). Datos: consultas de control (asuntos 7,9,11) → 98% valor 1;
	// primera vez (1,8,10) → ~75% valor 2. Por eso: 1 = control, 2 = primera vez/proc/imagen.
	// ⚠️ DUDA PENDIENTE: esto está INVERTIDO respecto al comentario de CLAUDE.md (que dice
	// 1=primera vez). Confirmar el significado real de 1 y 2 con los administrativos de SIESA;
	// si resulta al revés, intercambiar los valores aquí.
	primeraVezControl := 2
	if subjectType == 7 || subjectType == 9 || subjectType == 11 {
		primeraVezControl = 1
	}

	// company = company code (e.g. "EPS005"), contractCode = numeric contract (e.g. "4")
	//
	// Valores fijos en el INSERT (verificados contra la BD SIESA, 2026-06-23):
	//   - cod_user_asigna_cita = r.assignCedula (cédula del usuario autor configurado; principal
	//     SHERNANDEZ en prod, fallback '000000' = "Procesos Automaticos"). La columna guarda la
	//     cédula del funcionario que asigna; antes quedaba en 0 sin atribución.
	//   - formaSolicitud = 4 → catálogo FormaSolicitudCitas: 1=Telefónica, 2=Presencial,
	//     3=Correo, 4=Chatbot. El bot es canal Chatbot.
	//   - EntornoAtencion = '05' → entorno "Institucional" (catálogo RIPS/MinSalud). La columna NO
	//     tiene DEFAULT y la UI la llena en el 100% de las citas (verificado contra histórico 4d,
	//     ambas sedes = '05'). Sin esto quedaba NULL y se propagaba NULL al log_citas (que la copia).
	var newID int64
	err = tx.QueryRowContext(
		ctx, `
	INSERT INTO citas (
	    autoid, cod_medi, fecha, hora, meridiano, estado,
	    asunto, empresa, contrato, fecha_solicitud,
	    id_programacion, id_sede, cod_user_asigna_cita,
	    primera_vez_control, formaSolicitud, tipoUsuario,
	    es_terapia, Adicional, CodGrupo, EsCitaMultiple,
	    lugarAtencion, fecha_usuario_desea_cita, observacion,
	    tipo_servicio, EntornoAtencion
	) OUTPUT INSERTED.id
	VALUES (
	    @p1, @p2, @p3, @p4, @p5, 'P',
	    @p6, @p7, @p8, GETDATE(),
	    @p9, @p10, @p15,
	    @p14, 4, @p11,
	    0, 0, 0, 0,
	    0, CAST(GETDATE() AS DATE), @p12,
	    @p13, '05'
	)`,
		patientIDInt, doctorCodeInt, fecha, hora, meridiano,
		subjectType, company, contractCode,
		input.AgendaID, locationID, userType,
		input.Observations, serviceType,
		primeraVezControl, r.assignCedula,
	).Scan(&newID)
	if err != nil {
		// PK_citas(cod_medi,fecha,hora,meridiano,estado): el médico ya tiene una cita 'P' a esa
		// hora (otra reserva o un grupo previo del mismo paciente). La disponibilidad se valida por
		// fila de programacion_medico_detalle, pero la unicidad la impone la BD por médico+hora →
		// se trata como horario ya tomado (avisar + re-buscar), no como error genérico.
		if isUniqueViolation(err) {
			return nil, domain.ErrSlotTaken
		}
		return nil, fmt.Errorf("insert citas: %w", err)
	}

	// Claim slot with optimistic lock (AND IdCita IS NULL).
	// #7 (auditoría): se reclama el slot por agenda+fecha+hora, pero se EXIGE además que el slot sea
	// del mismo médico de la cita (Medico = @p5) y que esté programado (SinProgramacion = 0), y se
	// limita a UNA fila (TOP(1)). La invariante de SIESA es citas.cod_medi = pmd.Medico; hoy cada
	// agenda es 1:1 con un médico (verificado: 0/601 agendas mezclan médicos), así que esto no cambia
	// el comportamiento actual, pero blinda a nivel BD contra agendas compartidas o drift futuros
	// (evita marcar el slot de OTRO médico). El UPDATE ... FROM con subconsulta TOP(1) ORDER BY Fecha
	// asegura determinismo si la rejilla tuviera dos filas al mismo HH:MM.
	result, err := tx.ExecContext(
		ctx, `
	UPDATE pmd
	SET pmd.IdCita = @p1
	FROM programacion_medico_detalle pmd
	JOIN (
	    SELECT TOP (1) Id
	    FROM programacion_medico_detalle
	    WHERE IdProgramacionMedico = @p2
	      AND Fecha >= @p3 AND Fecha < DATEADD(DAY, 1, @p3)
	      AND CONVERT(VARCHAR(5), Fecha, 108) = @p4
	      AND Medico = @p5
	      AND IdCita IS NULL
	      AND Bloqueado = 0
	      AND SinProgramacion = 0
	    ORDER BY Fecha
	) pick ON pick.Id = pmd.Id`,
		newID, input.AgendaID, fecha, hora, doctorCodeInt,
	)
	if err != nil {
		return nil, fmt.Errorf("update pmd: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, domain.ErrSlotTaken
	}

	// Multi-slot (procedimiento que ocupa N slots por duración): vincular los espacios-1 slots
	// adicionales CONTIGUOS a ESTA misma cita, en la MISMA transacción (atómico). No se calcula
	// ningún "intervalo": se toman las próximas `Espacios` filas de slot por Fecha (la rejilla ya
	// existe con su espaciado real) y se exige que las adicionales estén libres. Si no caben todas
	// (alguna ocupada/bloqueada en medio), se retorna error y la tx hace rollback (no quedan slots
	// medio-reservados ni cita huérfana). Reemplaza el modelo viejo de crear N citas.
	if input.Espacios > 1 {
		start := fmt.Sprintf("%s %s:00", fecha, hora) // fecha=YYYY-MM-DD, hora=HH:MM (24h)
		// H2 (auditoría): la ventana se acota al MISMO día (Fecha < medianoche siguiente). Antes solo
		// exigía Fecha >= start sin cota superior, así que reservar cerca del fin del día tomaba filas
		// libres del día SIGUIENTE (mañana) marcándolas con "éxito" silencioso.
		// #24 (auditoría): la ventana se restringe también al MISMO médico (Medico = @p5), igual que el
		// claim principal — defensa en profundidad para la invariante citas.cod_medi = pmd.Medico.
		extra, err := tx.ExecContext(ctx, `
		WITH win AS (
		    SELECT TOP (@p3) Id
		    FROM programacion_medico_detalle
		    WHERE IdProgramacionMedico = @p2
		      AND Fecha >= @p4 AND Fecha < DATEADD(DAY, 1, CAST(@p4 AS DATE))
		      AND Medico = @p5
		    ORDER BY Fecha
		)
		UPDATE pmd SET IdCita = @p1
		FROM programacion_medico_detalle pmd
		JOIN win ON win.Id = pmd.Id
		WHERE pmd.IdCita IS NULL AND pmd.Bloqueado = 0 AND pmd.SinProgramacion = 0`,
			newID, input.AgendaID, input.Espacios, start, doctorCodeInt)
		if err != nil {
			return nil, fmt.Errorf("claim consecutive slots: %w", err)
		}
		// Se esperan Espacios-1 adicionales (la fila inicial ya quedó reclamada arriba, así que
		// dentro de la ventana de Espacios filas solo restan las Espacios-1 siguientes). Un slot
		// ocupado/bloqueado en medio rompe el conteo aquí (la rejilla lo incluye por Fecha pero el
		// WHERE IdCita IS NULL no lo actualiza) → rollback.
		if n, _ := extra.RowsAffected(); int(n) != input.Espacios-1 {
			return nil, fmt.Errorf("slots_consecutivos_insuficientes: reservados %d de %d", n+1, input.Espacios)
		}

		// H2: verificar contigüidad TEMPORAL real. El conteo no detecta un HUECO de rejilla (p. ej.
		// almuerzo, sin filas): las Espacios filas libres seguidas por Fecha quedarían reservadas pero
		// no equiespaciadas. Se exige que el gap entre slots consecutivos reservados sea uniforme.
		var minGap, maxGap sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
		SELECT MIN(d), MAX(d) FROM (
		    SELECT DATEDIFF(MINUTE, LAG(Fecha) OVER (ORDER BY Fecha), Fecha) AS d
		    FROM programacion_medico_detalle WHERE IdCita = @p1
		) t WHERE d IS NOT NULL`, newID).Scan(&minGap, &maxGap); err != nil {
			return nil, fmt.Errorf("verify slot contiguity: %w", err)
		}
		if minGap.Valid && maxGap.Valid && minGap.Int64 != maxGap.Int64 {
			return nil, fmt.Errorf("slots_no_contiguos: gaps min=%d max=%d min", minGap.Int64, maxGap.Int64)
		}
	}

	// Registrar los CUPS (citas_procedimientos / citas_procedimientos_asuntos) DENTRO de esta misma
	// transacción: cita + slots + procedimientos son atómicos, todo o nada (audit #26). Si algún
	// INSERT de CUPS falla, el defer tx.Rollback() deshace TODO — no quedan filas de CUPS huérfanas
	// apuntando a una cita inexistente, ni una cita sin sus procedimientos. asunto (subjectType) y
	// contrato ya están resueltos arriba; la cita aún NO está committeada, así que se pasan directos
	// a insertProcedureTx en vez de releerlos de `citas`.
	apptIDStr := strconv.FormatInt(newID, 10)
	contractInt, _ := strconv.Atoi(contractCode)
	for _, p := range input.Procedures {
		procInput := domain.CreateAppointmentProcedureInput{
			AppointmentID: int(newID),
			CupCode:       p.CupCode,
			Quantity:      p.Quantity,
			UnitValue:     p.UnitValue,
			ServiceID:     p.ServiceID,
		}
		if err := r.insertProcedureTx(ctx, tx, apptIDStr, subjectType, contractInt, procInput); err != nil {
			return nil, fmt.Errorf("insert appointment procedure %s: %w", p.CupCode, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Auditoría SIESA: se difiere a WriteCreationAudit, llamado por el servicio DESPUÉS del commit.
	// La UI registra log_citas + su detalle log_citas_procedimientos juntos, y el detalle necesita
	// que las filas de CUPS ya existan; tras este commit ya están persistidas (se insertaron arriba,
	// en la misma transacción que la cita).

	// Resolve doctor name from sis_medi (avoids returning cod_medi as DoctorName)
	var doctorName string
	_ = r.db.QueryRowContext(
		ctx,
		`SELECT ISNULL(RTRIM(nombre),'') FROM sis_medi WITH (NOLOCK) WHERE codigo = @p1`, doctorCodeInt,
	).Scan(&doctorName)
	if doctorName == "" {
		doctorName = input.DoctorID // fallback to cod_medi
	}

	apptDate, _ := time.Parse("2006-01-02", fecha)
	return &domain.Appointment{
		ID:           fmt.Sprintf("%d", newID),
		Date:         apptDate,
		TimeSlot:     input.TimeSlot,
		DoctorID:     input.DoctorID,
		DoctorName:   doctorName,
		PatientID:    input.PatientID,
		Entity:       input.Entity,
		AgendaID:     input.AgendaID,
		Observations: input.Observations,
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// CreateAppointmentProcedure / CreateAppointmentProcedureBatch
// ────────────────────────────────────────────────────────────────────────────

// chooseCupStorage decide cómo registrar un CUPS en citas_procedimientos.
//
// Regla VERIFICADA contra el histórico de SIESA (2026-06-24): la UI usa la variante con
// sufijo {base}-{qty} SOLO si está registrada en sis_proc_precios (variantExists), y en ese
// caso Cantidad=1; si no existe, usa el código base con Cantidad=qty. Ejemplos del mismo CUPS:
//
//	930860 qty=4 → "930860-4"/1   (930860-4 existe)
//	930860 qty=2 → "930860"/2     (930860-2 NO existe)
//	1005927 qty=3 → "1005927-3"/1 (existe; el viejo umbral qty>4 lo habría guardado mal)
//
// No hay umbral fijo: hay sufijos registrados para 2,3,4 y también 6,8,12,16,24,32,48,72 según
// el procedimiento. qty=1 siempre va como código base con Cantidad=1 (igual que la UI).
// cupVariantExistsQuery comprueba si un código {base}-{qty} está registrado en sis_proc_precios.
// @p1 = código con sufijo. Devuelve 1 si existe, 0 si no. (Lo usa CreateAppointmentProcedure.)
const cupVariantExistsQuery = `SELECT CASE WHEN EXISTS (
    SELECT 1 FROM sis_proc_precios WITH (NOLOCK) WHERE Codigo_proc = @p1
) THEN 1 ELSE 0 END`

func chooseCupStorage(baseCup string, qty int, variantExists bool) (code string, storedQty int) {
	if qty >= 2 && variantExists {
		return fmt.Sprintf("%s-%d", baseCup, qty), 1
	}
	return baseCup, qty
}

// catchAllServicio es el código de servicio "catch-all" (27) que SIESA insertó en masa sobre casi
// todo (contrato, cod_proc) — sus filas en `servicios` ocupan un bloque de id contiguo y no
// distinguen por ninguna columna. Corresponde a la línea de electrodiagnóstico, pero NUNCA es el
// servicio primario de un asunto; el servicio específico del procedimiento es siempre el otro.
//
// DECISIÓN DATA-DRIVEN (2026-06-24): la BD no define cómo desambiguar 18-vs-27 (las tablas
// médico-servicio/combo están vacías) y la clínica aún no tiene respuesta. Se decide excluir el 27
// porque coincide con 569/572 (99,5%) de las citas limpias de los últimos 4 días; los 3 casos que
// guardaron 27 son elecciones de operador minoritarias. Pendiente confirmación clínica (doc dudas).
const catchAllServicio = 27

// resolveProcServicio devuelve el Servicio canónico de un procedimiento/imagen desde la tabla
// `servicios` por (contrato, cod_proc), excluyendo catchAllServicio. Reemplaza la antigua
// derivación por historia (ORDER BY fecha DESC), que era frágil y podía heredar asignaciones
// contaminadas de citas viejas. Verificado contra la BD (2026-06-24): excluyendo el catch-all,
// (contrato, cod_proc) da un Servicio único (0 ambigüedad en 572 citas; corrige 3 registros
// históricos erróneos). Devuelve 0 si el CUPS no está cubierto por el contrato.
func (r *AppointmentRepo) resolveProcServicio(ctx context.Context, q queryer, contrato int, baseCup, fullCup string) int {
	if contrato == 0 {
		return 0
	}
	var servicio int
	_ = q.QueryRowContext(ctx, `
		SELECT TOP 1 codigo FROM servicios WITH (NOLOCK)
		WHERE contrato = @p1 AND codigo <> @p2 AND cod_proc IN (@p3, @p4)
		ORDER BY codigo`,
		contrato, catchAllServicio, baseCup, fullCup).Scan(&servicio)
	return servicio
}

func (r *AppointmentRepo) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	apptID := strconv.Itoa(input.AppointmentID)

	// Camino standalone: la cita ya está committeada, así que se releen asunto y contrato de `citas`.
	// (En el camino atómico de Create la cita aún NO está committeada y se pasan directos.)
	var subjectType, contrato int
	_ = r.db.QueryRowContext(
		ctx,
		`SELECT ISNULL(asunto,0), ISNULL(TRY_CONVERT(INT, contrato),0) FROM citas WITH (NOLOCK) WHERE id = @p1`, apptID,
	).Scan(&subjectType, &contrato)

	return r.insertProcedureTx(ctx, r.db, apptID, subjectType, contrato, input)
}

// insertProcedureTx registra UN procedimiento de la cita en SIESA usando el queryer dado: la
// transacción de Create (atómico) o r.db (camino standalone). Recibe asunto y contrato ya
// resueltos por el caller — en Create la cita aún no está committeada, así que no se pueden releer
// de `citas`. Decide la tabla destino por tipo de asunto:
//   - consulta    → citas_procedimientos_asuntos (con Valor)
//   - proc/imagen → citas_procedimientos
func (r *AppointmentRepo) insertProcedureTx(ctx context.Context, q queryer, apptID string, subjectType, contrato int, input domain.CreateAppointmentProcedureInput) error {
	qty := input.Quantity
	if qty == 0 {
		qty = 1
	}

	if isConsultationSubject(subjectType) {
		// Servicio y NomProcedimiento CANÓNICOS desde AsuntoPctos por asunto (relación 1:1 verificada
		// contra la BD). NO se derivan de citas pasadas: la historia puede estar contaminada (asuntos
		// mal asignados por la versión vieja del bot), y AsuntoPctos es la fuente oficial.
		var serviceID int
		var procName string
		_ = q.QueryRowContext(ctx,
			`SELECT TOP 1 ISNULL(Servicio,0), ISNULL(NomProcedimiento,'') FROM AsuntoPctos WITH (NOLOCK) WHERE Asunto = @p1`,
			subjectType).Scan(&serviceID, &procName)
		if procName == "" {
			procName = input.CupCode
		}

		_, err := q.ExecContext(ctx, `
		INSERT INTO citas_procedimientos_asuntos
		    (IdCita, IdSisDeta, Asunto, Servicio, TipoManual, CodProcedimiento, NomProcedimiento, Valor, FechaRegistro)
		VALUES (@p1, 0, @p2, @p3, '256', @p4, @p5, @p6, GETDATE())`,
			apptID, subjectType, serviceID, input.CupCode, procName, input.UnitValue)
		return err
	}

	// Extract base code (no suffix): the medical order always uses the base code
	// with quantity separate. SIESA internal code uses format "891509-16".
	baseCup := input.CupCode
	if idx := strings.LastIndex(baseCup, "-"); idx > 0 {
		if n, err2 := strconv.Atoi(baseCup[idx+1:]); err2 == nil && n > 0 {
			baseCup = baseCup[:idx]
			if qty == 1 {
				qty = n
			}
		}
	}

	// Servicio CANÓNICO desde la tabla `servicios` por (contrato, cod_proc), excluyendo el catch-all
	// (ver resolveProcServicio). Reemplaza la derivación por historia (ORDER BY fecha DESC), que era
	// frágil y podía heredar asignaciones contaminadas.
	servicio := r.resolveProcServicio(ctx, q, contrato, baseCup, input.CupCode)

	// ¿Existe la variante con sufijo {base}-{qty} en sis_proc_precios? (ver chooseCupStorage)
	variantExists := false
	if qty >= 2 {
		var found int
		_ = q.QueryRowContext(ctx, cupVariantExistsQuery,
			fmt.Sprintf("%s-%d", baseCup, qty)).Scan(&found)
		variantExists = found == 1
	}
	cupCode, storedQty := chooseCupStorage(baseCup, qty, variantExists)

	_, err := q.ExecContext(ctx, `
	INSERT INTO citas_procedimientos (id_procedimiento, tipo, id_cita, Servicio, Cantidad)
	VALUES (@p1, '256', @p2, @p3, @p4)`,
		cupCode, apptID, servicio, storedQty)
	return err
}

func (r *AppointmentRepo) CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error {
	for _, in := range inputs {
		if err := r.CreateAppointmentProcedure(ctx, in); err != nil {
			return fmt.Errorf("appointment procedure %s: %w", in.CupCode, err)
		}
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Confirm / Cancel / Batch
// ────────────────────────────────────────────────────────────────────────────

// Identidad FALLBACK del bot en SIESA: usuario "Procesos Automaticos" (usuario.id=10006,
// cedula='000000'). Se usa cuando no se configura un usuario principal (SIESA_ASSIGN_USER_*).
// OJO: son dos columnas distintas — cod_user_asigna_cita usa la CÉDULA, las columnas
// id_usuario_cancela / IdUsuarioConfirmaAsistencia / usuario_evento usan el usuario.id.
const (
	defaultSiesaBotUserID = 10006
	defaultSiesaBotCedula = "000000"
)

// writeAuditLog inserta una fila en log_citas replicando lo que hace la UI de SIESA al
// crear/cancelar/modificar una cita. No hay trigger sobre citas/log_citas: la auditoría la
// escribe la aplicación, así que el bot debe hacerlo también. Best-effort y ASÍNCRONO
// (fire-and-forget en goroutine, ver final de la función): no bloquea ni rompe el flujo.
//
// La UI usa DOS formas de fila distintas (verificado contra el histórico, 2026-06-23):
//   - APARTAR/CANCELAR → snapshot completo de la cita + fecha_evento (+ es_terapia solo en
//     cancelaciones; la UI deja es_terapia en blanco al crear).
//   - CITA MODIFICADA (confirmaciones) → NO copia autoid/cod_medi/fecha/estado (van en blanco)
//     y en su lugar llena las columnas *_anterior con el estado previo. Como confirmar no
//     cambia esos campos, anterior = valor actual y observacion lleva el mensaje nuevo.
//
// En ambos casos fecha_evento = CONVERT(VARCHAR(50), GETDATE(), 100) → 'Jun 23 2026  6:53AM',
// el mismo formato que la UI (style 100), que es lo que ese front usa para ordenar el historial.
func (r *AppointmentRepo) writeAuditLog(ctx context.Context, id, evento, obs string) {
	var query string
	if evento == "CITA MODIFICADA" {
		query = `
		INSERT INTO log_citas (
		    asunto, observacion, empresa, contrato, fecha_solicitud, fecha_evento,
		    tipo_servicio, id_sede, primera_vez_control, formaSolicitud, lugarAtencion,
		    tipoUsuario, EntornoAtencion,
		    asunto_anterior, observacion_anterior, empresa_anterior, contrato_anterior,
		    tipo_servicio_anterior, primera_vez_contrato_anterior,
		    tipo_evento, usuario_evento, id_cita_modificada
		)
		SELECT
		    asunto, @p2, empresa, contrato, fecha_solicitud, CONVERT(VARCHAR(50), GETDATE(), 100),
		    tipo_servicio, id_sede, primera_vez_control, formaSolicitud, lugarAtencion,
		    tipoUsuario, EntornoAtencion,
		    asunto, observacion, empresa, contrato,
		    tipo_servicio, primera_vez_control,
		    @p3, @p4, id
		FROM citas WHERE id = @p1`
	} else {
		query = `
		INSERT INTO log_citas (
		    autoid, cod_medi, fecha_usuario_desea_cita, fecha, hora, meridiano, estado,
		    asunto, observacion, empresa, fecha_solicitud, motivo, horacan,
		    cod_user_asigna_cita, tipo_servicio, id_sede, primera_vez_control, contrato,
		    formaSolicitud, lugarAtencion, tipoUsuario, EntornoAtencion, es_terapia, fecha_evento,
		    tipo_evento, usuario_evento, id_cita_modificada
		)
		SELECT
		    autoid, cod_medi, fecha_usuario_desea_cita, fecha, hora, meridiano, estado,
		    asunto, @p2, empresa, fecha_solicitud, motivo, horacan,
		    cod_user_asigna_cita, tipo_servicio, id_sede, primera_vez_control, contrato,
		    formaSolicitud, lugarAtencion, tipoUsuario, EntornoAtencion,
		    CASE WHEN @p3 = 'CANCELAR CITA' THEN CAST(es_terapia AS VARCHAR(50)) ELSE NULL END,
		    CONVERT(VARCHAR(50), GETDATE(), 100),
		    @p3, @p4, id
		FROM citas WHERE id = @p1`
	}
	// Fire-and-forget: la auditoría es best-effort y NO debe bloquear el flujo principal
	// (crear/cancelar/confirmar). Se ejecuta en segundo plano con un contexto DESACOPLADO del
	// request (context.WithoutCancel → no se cancela cuando la petición termina) y timeout
	// propio. recover() evita que un fallo del INSERT tumbe el proceso.
	bgCtx := context.WithoutCancel(ctx)
	r.bgWG.Add(1) // #33: rastreada para que el apagado la espere antes de cerrar la BD
	go func() {
		defer r.bgWG.Done()
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("audit_log_panic", "appointment_id", id, "evento", evento, "recover", rec)
			}
		}()
		cctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
		defer cancel()
		if _, err := r.db.ExecContext(cctx, query, id, obs, evento, r.botUserID); err != nil {
			slog.Warn("audit_log_write_failed", "appointment_id", id, "evento", evento, "error", err)
		}
	}()
}

// creationAuditQuery es el SQL exacto que ejecuta WriteCreationAudit (extraído como constante
// para que el test de integración valide el MISMO SQL de producción). Inserta la fila de
// log_citas (APARTAR CITA) y, en el mismo batch, su detalle en log_citas_procedimientos enlazado
// por id_log = SCOPE_IDENTITY(). @p1 = id de la cita, @p2 = observación, @p3 = usuario (bot).
const creationAuditQuery = `
	DECLARE @idlog BIGINT;

	INSERT INTO log_citas (
	    autoid, cod_medi, fecha_usuario_desea_cita, fecha, hora, meridiano, estado,
	    asunto, observacion, empresa, fecha_solicitud, motivo, horacan,
	    cod_user_asigna_cita, tipo_servicio, id_sede, primera_vez_control, contrato,
	    formaSolicitud, lugarAtencion, tipoUsuario, EntornoAtencion, es_terapia, fecha_evento,
	    tipo_evento, usuario_evento, id_cita_modificada
	)
	SELECT
	    autoid, cod_medi, fecha_usuario_desea_cita, fecha, hora, meridiano, estado,
	    asunto, @p2, empresa, fecha_solicitud, motivo, horacan,
	    cod_user_asigna_cita, tipo_servicio, id_sede, primera_vez_control, contrato,
	    formaSolicitud, lugarAtencion, tipoUsuario, EntornoAtencion, NULL,
	    CONVERT(VARCHAR(50), GETDATE(), 100),
	    'APARTAR CITA', @p3, id
	FROM citas WHERE id = @p1;

	SET @idlog = SCOPE_IDENTITY();

	INSERT INTO log_citas_procedimientos
	    (id_procedimiento, tipo, id_cita, estado, id_log, cantidad, Servicio)
	SELECT cp.id_procedimiento, cp.tipo, cp.id_cita, 0, @idlog,
	       ISNULL(cp.Cantidad, 1), cp.Servicio
	FROM citas_procedimientos cp WHERE cp.id_cita = @p1;`

// WriteCreationAudit registra el alta de la cita en SIESA replicando lo que hace la UI al
// crearla: UNA fila en log_citas (evento APARTAR CITA) + su DETALLE en log_citas_procedimientos
// (una fila por CUPS de citas_procedimientos, enlazada por id_log = log_citas.id recién creado).
//
// Verificado contra el histórico (2026-06-23): la UI puebla log_citas_procedimientos solo para
// citas de procedimiento/imagen (las consultas usan citas_procedimientos_asuntos y NO generan
// detalle → lcp=0). Por eso el INSERT del detalle lee de citas_procedimientos: en una consulta no
// hay filas y queda en 0, igual que la UI. No hay trigger sobre estas tablas; la auditoría la
// escribe la aplicación, así que el bot debe hacerlo.
//
// Se llama DESPUÉS de insertar los CUPS (no dentro de Create) porque el detalle necesita que las
// filas de citas_procedimientos ya existan. Best-effort y ASÍNCRONO (igual que writeAuditLog): no
// bloquea ni rompe el flujo de reserva. El header + detalle van en un mismo batch para que
// SCOPE_IDENTITY() devuelva el id_log correcto del log_citas recién insertado.
func (r *AppointmentRepo) WriteCreationAudit(ctx context.Context, appointmentID, observations string) {
	bgCtx := context.WithoutCancel(ctx)
	r.bgWG.Add(1) // #33: rastreada para que el apagado la espere antes de cerrar la BD
	go func() {
		defer r.bgWG.Done()
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("creation_audit_panic", "appointment_id", appointmentID, "recover", rec)
			}
		}()
		cctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
		defer cancel()
		if _, err := r.db.ExecContext(cctx, creationAuditQuery, appointmentID, observations, r.botUserID); err != nil {
			slog.Warn("creation_audit_write_failed", "appointment_id", appointmentID, "error", err)
		}
	}()
}

// writeAuditLogBatch es la versión batch de writeAuditLog (N-34): inserta la auditoría de
// VARIAS citas con UN solo INSERT ... SELECT ... WHERE id IN (...), en una única goroutine,
// en vez de N goroutines (una por id) que saturaban el pool en cancelaciones masivas. Mismo
// resultado observable (una fila de log por cita); obs/evento son uniformes en un batch.
func (r *AppointmentRepo) writeAuditLogBatch(ctx context.Context, ids []string, evento, obs string) {
	if len(ids) == 0 {
		return
	}
	// @p1=obs, @p2=evento, @p3=usuario del bot; los ids empiezan en @p4.
	clause, idArgs := inParams(ids, 4)
	allArgs := append([]interface{}{obs, evento, r.botUserID}, idArgs...)

	var query string
	if evento == "CITA MODIFICADA" {
		query = `
		INSERT INTO log_citas (
		    asunto, observacion, empresa, contrato, fecha_solicitud, fecha_evento,
		    tipo_servicio, id_sede, primera_vez_control, formaSolicitud, lugarAtencion,
		    tipoUsuario, EntornoAtencion,
		    asunto_anterior, observacion_anterior, empresa_anterior, contrato_anterior,
		    tipo_servicio_anterior, primera_vez_contrato_anterior,
		    tipo_evento, usuario_evento, id_cita_modificada
		)
		SELECT
		    asunto, @p1, empresa, contrato, fecha_solicitud, CONVERT(VARCHAR(50), GETDATE(), 100),
		    tipo_servicio, id_sede, primera_vez_control, formaSolicitud, lugarAtencion,
		    tipoUsuario, EntornoAtencion,
		    asunto, observacion, empresa, contrato,
		    tipo_servicio, primera_vez_control,
		    @p2, @p3, id
		FROM citas WHERE id IN (` + clause + `)`
	} else {
		query = `
		INSERT INTO log_citas (
		    autoid, cod_medi, fecha_usuario_desea_cita, fecha, hora, meridiano, estado,
		    asunto, observacion, empresa, fecha_solicitud, motivo, horacan,
		    cod_user_asigna_cita, tipo_servicio, id_sede, primera_vez_control, contrato,
		    formaSolicitud, lugarAtencion, tipoUsuario, EntornoAtencion, es_terapia, fecha_evento,
		    tipo_evento, usuario_evento, id_cita_modificada
		)
		SELECT
		    autoid, cod_medi, fecha_usuario_desea_cita, fecha, hora, meridiano, estado,
		    asunto, @p1, empresa, fecha_solicitud, motivo, horacan,
		    cod_user_asigna_cita, tipo_servicio, id_sede, primera_vez_control, contrato,
		    formaSolicitud, lugarAtencion, tipoUsuario, EntornoAtencion,
		    CASE WHEN @p2 = 'CANCELAR CITA' THEN CAST(es_terapia AS VARCHAR(50)) ELSE NULL END,
		    CONVERT(VARCHAR(50), GETDATE(), 100),
		    @p2, @p3, id
		FROM citas WHERE id IN (` + clause + `)`
	}
	bgCtx := context.WithoutCancel(ctx)
	r.bgWG.Add(1) // #33: rastreada para que el apagado la espere antes de cerrar la BD
	go func() {
		defer r.bgWG.Done()
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("audit_log_batch_panic", "evento", evento, "recover", rec)
			}
		}()
		cctx, cancel := context.WithTimeout(bgCtx, 15*time.Second)
		defer cancel()
		if _, err := r.db.ExecContext(cctx, query, allArgs...); err != nil {
			slog.Warn("audit_log_batch_write_failed", "evento", evento, "count", len(ids), "error", err)
		}
	}()
}

func (r *AppointmentRepo) Confirm(ctx context.Context, id string, channel, channelID string) error {
	obs := fmt.Sprintf("Confirmado via WhatsApp [%s]", channelID)
	_, err := r.db.ExecContext(ctx, `
	UPDATE citas
	SET AsistenciaConfirmada          = 1,
	    FechaConfirmaAsistencia        = GETDATE(),
	    FechaSistemaConfirmaAsistencia = GETDATE(),
	    IdTipoConfirmacionAsistencia   = 1,
	    IdUsuarioConfirmaAsistencia    = @p3,
	    ObservacionConfirmaAsistencia  = @p2
	WHERE id = @p1 AND estado <> 'C'`, id, obs, r.botUserID) // #11: no confirmar una cita ya cancelada
	if err != nil {
		return err
	}
	r.writeAuditLog(ctx, id, "CITA MODIFICADA", fmt.Sprintf("Confirmado via WhatsApp [conv: %s]", channelID))
	return nil
}

// NOTE — flujo de cancelación (VERIFICADO contra la BD/UI de SIESA, 2026-06-23):
// La UI cancela con un UPDATE en sitio sobre la MISMA fila (estado 'P'→'C', horacan=hora
// actual, id_usuario_cancela, motivo_cancela) y libera el cupo poniendo
// programacion_medico_detalle.IdCita = NULL. NO crea otra cita ni duplica el slot: el mismo
// registro de slot queda disponible de inmediato (0/796 citas canceladas conservan slot).
// Cancel/CancelBatch/DeleteBatch replican exactamente eso. Decisión de negocio aún abierta
// (no técnica): ¿hace falta una ventana de gracia antes de reabrir el cupo? Hoy se libera ya.
func (r *AppointmentRepo) Cancel(ctx context.Context, id string, reason, channel, channelID string) error {
	obs := fmt.Sprintf(" [Cancelada via WhatsApp: %s | conv: %s]", reason, channelID)
	// estado→'C' + horacan=hora actual (como la UI: deja la PK única y libera el cupo) +
	// id_usuario_cancela = usuario del bot.
	// motivo = 2 → vía CitasMotivoCancelaRES256 mapea a IdCancelaRES256=2 = "cancelada por el
	// paciente" (RES256/Resolución 256). Es el valor correcto porque desde el bot SOLO el
	// paciente puede cancelar. (Pendiente confirmar el significado exacto con SIESA — doc dudas §6.2.)
	// N-42: cancelar la cita y liberar el cupo van en UNA transacción. Si la liberación del cupo
	// falla, se hace ROLLBACK del cancelado: así nunca queda una cita 'C' con su slot todavía
	// ocupado (slot huérfano, invisible para futuros pacientes). El caller recibe error y reintenta.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
	UPDATE citas
	SET estado             = 'C',
	    horacan            = CONVERT(VARCHAR(5), GETDATE(), 108),
	    motivo             = 2,
	    id_usuario_cancela = @p4,
	    motivo_cancela     = @p2,
	    observacion        = ISNULL(observacion,'') + @p3
	WHERE id = @p1`, id, reason, obs, r.botUserID); err != nil {
		return err
	}
	// #10 (auditoría): si el id no es numérico, NO se puede liberar el cupo → no commitear (quedaría una
	// cita 'C' con su slot aún ocupado, huérfano). Se retorna error y la tx hace rollback.
	idInt, perr := strconv.ParseInt(id, 10, 64)
	if perr != nil {
		return fmt.Errorf("cancel: id de cita no numérico %q: %w", id, perr)
	}
	if _, relErr := tx.ExecContext(ctx,
		`UPDATE programacion_medico_detalle SET IdCita = NULL WHERE IdCita = @p1`, idInt); relErr != nil {
		return fmt.Errorf("liberar cupo: %w", relErr)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.writeAuditLog(ctx, id, "CANCELAR CITA", fmt.Sprintf("CANCELACION DE CITA - Motivo: %s [conv: %s]", reason, channelID))
	return nil
}

func (r *AppointmentRepo) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	if len(ids) == 0 {
		return nil
	}
	obs := fmt.Sprintf("Confirmado via WhatsApp [%s]", channelID)
	// @p1 = obs, @p2 = usuario del bot; IDs empiezan en @p3
	clause, idArgs := inParams(ids, 3)
	allArgs := append([]interface{}{obs, r.botUserID}, idArgs...)
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE citas
		SET AsistenciaConfirmada          = 1,
		    FechaConfirmaAsistencia        = GETDATE(),
		    FechaSistemaConfirmaAsistencia = GETDATE(),
		    IdTipoConfirmacionAsistencia   = 1,
		    IdUsuarioConfirmaAsistencia    = @p2,
		    ObservacionConfirmaAsistencia  = @p1
		WHERE estado <> 'C' AND id IN (%s)`, clause), allArgs...) // #11: no confirmar citas canceladas
	if err != nil {
		return err
	}
	r.writeAuditLogBatch(ctx, ids, "CITA MODIFICADA", fmt.Sprintf("Confirmado via WhatsApp [conv: %s]", channelID))
	return nil
}

func (r *AppointmentRepo) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	if len(ids) == 0 {
		return nil
	}

	// PK_citas es compuesta: (cod_medi, fecha, hora, meridiano, estado, horacan, CodGrupo).
	// Al cancelar seteamos horacan = hora actual (igual que la UI de SIESA), de modo que la
	// fila cancelada queda con una PK única y NO colisiona con una cancelación previa del
	// mismo slot. Esto reemplaza al antiguo DELETE de la "gemela cancelada", que era un
	// workaround necesario solo porque antes horacan quedaba en '--:--' en todas las cancelaciones.
	obs := fmt.Sprintf(" [Cancelada via WhatsApp: %s | conv: %s]", reason, channelID)
	// @p1 = reason, @p2 = obs, @p3 = usuario del bot; IDs empiezan en @p4
	// motivo = 2 → RES256 "cancelada por el paciente" (ver Cancel y doc dudas §6.2).
	clause, idArgs := inParams(ids, 4)
	allArgs := append([]interface{}{reason, obs, r.botUserID}, idArgs...)
	// N-42: cancelar las citas y liberar sus cupos van en UNA transacción. Si la liberación falla,
	// ROLLBACK del cancelado → nunca quedan citas 'C' con cupos huérfanos.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`UPDATE citas
		SET estado             = 'C',
		    horacan            = CONVERT(VARCHAR(5), GETDATE(), 108),
		    motivo             = 2,
		    id_usuario_cancela = @p3,
		    motivo_cancela     = @p1,
		    observacion        = ISNULL(observacion,'') + @p2
		WHERE id IN (%s)`, clause), allArgs...); err != nil {
		return fmt.Errorf("siesa cancel batch: %w", err)
	}
	clause2, idArgs2 := inParamsAsInt(ids, 1)
	if _, relErr := tx.ExecContext(ctx,
		fmt.Sprintf(`UPDATE programacion_medico_detalle SET IdCita = NULL WHERE IdCita IN (%s)`, clause2),
		idArgs2...); relErr != nil {
		return fmt.Errorf("siesa cancel batch liberar cupos: %w", relErr)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.writeAuditLogBatch(ctx, ids, "CANCELAR CITA", fmt.Sprintf("CANCELACION DE CITA - Motivo: %s [conv: %s]", reason, channelID))
	return nil
}

// SlotCountForAppointment cuenta cuántos slots de programacion_medico_detalle están
// asociados a la cita (IdCita = apptID). Es la fuente de verdad del número de espacios
// que ocupa una cita multi-slot: el modelo es 1 cita / N slots (cada slot apunta a la
// cita vía IdCita). Devuelve 0 si la cita no existe o no tiene slots asociados.
func (r *AppointmentRepo) SlotCountForAppointment(ctx context.Context, apptID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM programacion_medico_detalle WITH (NOLOCK) WHERE IdCita = @p1`,
		apptID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("siesa slot count for appointment %s: %w", apptID, err)
	}
	return n, nil
}

// DeleteBatch is a SOFT delete (INTEG-01): semantically it does NOT remove the citas rows.
// citas.id is referenced by FK constraints (CitasObservaciones, E_Payment, E_Payment_Logs,
// Recordatorio_mail_deta), so a physical DELETE fails at runtime whenever a child row exists,
// and SIESA itself never hard-deletes appointments. So this delegates to CancelBatch, which:
//  1. marks the target appointments estado='C' (+ motivo_cancela / observación) and sets
//     horacan = hora actual, which keeps the composite PK unique and avoids a collision with a
//     prior cancelled row of the same slot (this replaced the old "cancelled twin" DELETE);
//  2. releases their slots (programacion_medico_detalle.IdCita = NULL).
//
// Net effect: the appointment stays in SIESA marked cancelled, exactly like Cancel/CancelBatch.
// See the cancellation-flow NOTE above Cancel re: immediate slot release.
func (r *AppointmentRepo) DeleteBatch(ctx context.Context, ids []string) error {
	return r.CancelBatch(ctx, ids, "Eliminada por administrador", "system", "")
}

// ────────────────────────────────────────────────────────────────────────────
// Consultas auxiliares
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) HasFutureForCup(ctx context.Context, patientID, cupCode string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `
	SELECT TOP 1 1
	FROM citas c WITH (NOLOCK)
	JOIN citas_procedimientos cp WITH (NOLOCK) ON cp.id_cita = c.id
	WHERE c.autoid = @p1 AND cp.id_procedimiento = @p2
	  AND c.fecha >= CAST(GETDATE() AS DATE) AND c.estado != 'C'`,
		patientID, cupCode).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	// También revisar tabla de consultas
	err = r.db.QueryRowContext(ctx, `
	SELECT TOP 1 1
	FROM citas c WITH (NOLOCK)
	JOIN citas_procedimientos_asuntos cpa WITH (NOLOCK) ON cpa.IdCita = c.id
	WHERE c.autoid = @p1 AND cpa.CodProcedimiento = @p2
	  AND c.fecha >= CAST(GETDATE() AS DATE) AND c.estado != 'C'`,
		patientID, cupCode).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

func (r *AppointmentRepo) FindLastDoctorForCups(ctx context.Context, patientID string, cups []string) (string, error) {
	if len(cups) == 0 {
		return "", nil
	}
	// @p1 = patientID; cups en @p2, @p3, ...
	clause, cupArgs := inParams(cups, 2)
	allArgs := append([]interface{}{patientID}, cupArgs...)

	var doc string
	err := r.db.QueryRowContext(
		ctx, fmt.Sprintf(`
	SELECT TOP 1 CAST(m.cedula AS VARCHAR(20))
	FROM citas c WITH (NOLOCK)
	JOIN citas_procedimientos cp WITH (NOLOCK) ON cp.id_cita = c.id
	JOIN sis_medi m WITH (NOLOCK) ON m.codigo = c.cod_medi
	WHERE c.autoid = @p1 AND cp.id_procedimiento IN (%s) AND c.estado != 'C'
	ORDER BY c.fecha DESC`, clause), allArgs...,
	).Scan(&doc)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return doc, err
}

func (r *AppointmentRepo) CountMonthlyByGroup(ctx context.Context, cupsCodes []string, year, month int) (int, error) {
	if len(cupsCodes) == 0 {
		return 0, nil
	}
	startDate := fmt.Sprintf("%04d-%02d-01", year, month)
	endDate := time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")

	// MRC monthly count for the given CUPS group.
	//   - Filter c.contrato IN ('5','6'): only Sanitas MRC appointments count toward the cap
	//     (BUG-06). Without it the count mixed every contract (e.g. EEG jun-2026: 1060 vs 58).
	//   - Cantidad REAL (BUG-07 corregido; validado contra la BD 2026-06-24): el sufijo numérico SÍ
	//     es la cantidad. Ej.: 891509-8 = 8 neuroconducciones aunque cp.Cantidad=1. Evidencia: (a) el
	//     precio en sis_proc_precios es plano entre base y variantes (891509 = 891509-8) → es precio
	//     UNITARIO; (b) al facturar, la clínica expande 891509-8 en 8 filas del CUP base. Por eso se
	//     suma el SUFIJO numérico cuando existe, y cp.Cantidad solo cuando NO hay sufijo. El antiguo
	//     SUM(Cantidad) subcontaba (1 por una variante de 8) → riesgo de pasar el tope del contrato.
	//   - LEFT(...CHARINDEX...) matches the base code so all variants of a CUPS belong to the group.
	// citas.contrato is varchar in SIESA, hence the string literals.
	clause, cupsArgs := inParams(cupsCodes, 3)
	allArgs := append([]interface{}{startDate, endDate}, cupsArgs...)

	var count int
	err := r.db.QueryRowContext(
		ctx, fmt.Sprintf(`
	SELECT ISNULL(SUM(
	    CASE WHEN CHARINDEX('-', cp.id_procedimiento) > 0
	              AND TRY_CONVERT(INT, SUBSTRING(cp.id_procedimiento, CHARINDEX('-', cp.id_procedimiento) + 1, 10)) IS NOT NULL
	         THEN TRY_CONVERT(INT, SUBSTRING(cp.id_procedimiento, CHARINDEX('-', cp.id_procedimiento) + 1, 10))
	         ELSE ISNULL(cp.Cantidad, 1)
	    END), 0)
	-- #8 (auditoría): sin NOLOCK — es una decisión de CUPO (tope mensual MRC); una lectura sucia
	-- podría contar/omitir citas no confirmadas y pasar/bloquear el tope erróneamente.
	FROM citas c
	JOIN citas_procedimientos cp ON cp.id_cita = c.id
	WHERE c.fecha >= @p1 AND c.fecha < @p2
	  AND c.contrato IN ('5', '6')
	  AND LEFT(cp.id_procedimiento, CHARINDEX('-', cp.id_procedimiento + '-') - 1) IN (%s)
	  AND c.estado <> 'C'`, clause), allArgs...,
	).Scan(&count)
	return count, err
}

func (r *AppointmentRepo) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	rows, err := r.db.QueryContext(ctx, `
	SELECT CAST(c.id AS VARCHAR(20)),
	       CAST(c.fecha AS DATE),
	       ISNULL(c.hora,''),
	       ISNULL(c.meridiano,''),
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       ISNULL(RTRIM((SELECT TOP 1 sm.nombre FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi)),
	           CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20))),
	       ISNULL(CAST((SELECT TOP 1 sm.cedula FROM sis_medi sm WITH (NOLOCK) WHERE sm.codigo=c.cod_medi) AS VARCHAR(20)),''),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT),
	       ISNULL(LTRIM(RTRIM(CONCAT(
	           p.primer_nom,' ',ISNULL(p.segundo_nom,''),' ',
	           p.primer_ape,' ',ISNULL(p.segundo_ape,'')
	       ))), '') AS nombre_paciente,
	       ISNULL(NULLIF(LTRIM(RTRIM(p.telefono)),''), ISNULL(p.celular,'')) AS telefono_paciente,
	       ISNULL((SELECT TOP 1 DATEPART(HOUR,pmd.Fecha)*100+DATEPART(MINUTE,pmd.Fecha)
	               FROM programacion_medico_detalle pmd WITH (NOLOCK) WHERE pmd.IdCita=c.id ORDER BY pmd.Fecha),-1)
	FROM citas c WITH (NOLOCK)
	INNER JOIN sis_paci p WITH (NOLOCK) ON p.autoid = c.autoid
	WHERE c.fecha = @p1 AND c.estado != 'C'
	ORDER BY c.hora`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var appointments []domain.Appointment
	var ids []string
	for rows.Next() {
		var appt domain.Appointment
		var fecha time.Time
		var hora, meridiano, estado string
		var asistenciaConfirmada, hhmm24 int
		if err := rows.Scan(
			&appt.ID, &fecha, &hora, &meridiano,
			&appt.DoctorID, &appt.DoctorName, &appt.DoctorDocument, &appt.PatientID, &appt.Entity, &appt.AgendaID,
			&estado, &appt.Observations, &asistenciaConfirmada,
			&appt.PatientName, &appt.PatientPhone, &hhmm24,
		); err != nil {
			return nil, err
		}
		appt.Date = fecha
		appt.TimeSlot = timecodeFromAppointment(fecha, hora, meridiano, hhmm24)
		appt.Canceled = strings.EqualFold(estado, "C")
		appt.Confirmed = asistenciaConfirmada == 1 || strings.EqualFold(estado, "CC") || strings.EqualFold(estado, "A")
		appointments = append(appointments, appt)
		ids = append(ids, appt.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		procMap, err := r.fetchProceduresBatch(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range appointments {
			appointments[i].Procedures = procMap[appointments[i].ID]
		}
	}
	return appointments, nil
}

// FindAgendasByDoctor lista las agendas del médico con citas próximas no atendidas, para el selector
// del dashboard. Solo lectura con NOLOCK (no bloquea la UI de SIESA). `doctorCode` = cod_medi.
func (r *AppointmentRepo) FindAgendasByDoctor(ctx context.Context, doctorCode, from string) ([]domain.AgendaSummary, error) {
	if from == "" {
		from = time.Now().Format("2006-01-02")
	}
	rows, err := r.db.QueryContext(ctx, `
	SELECT a.id_programacion,
	       ISNULL(con.nombre, '') AS consultorio,
	       a.primera, a.ultima, a.fechas, a.citas
	FROM (
	    SELECT c.id_programacion,
	           MIN(CONVERT(VARCHAR(10), c.fecha, 23)) AS primera,
	           MAX(CONVERT(VARCHAR(10), c.fecha, 23)) AS ultima,
	           COUNT(DISTINCT c.fecha) AS fechas,
	           COUNT(*) AS citas
	    FROM citas c WITH (NOLOCK)
	    WHERE c.cod_medi = @doctor
	      AND c.estado NOT IN ('C','A')
	      AND c.fecha >= @from
	    GROUP BY c.id_programacion
	) a
	OUTER APPLY (
	    SELECT TOP 1 con.nombre
	    FROM programacion_medico_relacion pmr WITH (NOLOCK)
	    JOIN consultorios con WITH (NOLOCK) ON con.id = pmr.id_consultorio
	    WHERE pmr.id_programacion = a.id_programacion
	) con
	ORDER BY a.primera`,
		sql.Named("doctor", doctorCode), sql.Named("from", from))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AgendaSummary
	for rows.Next() {
		var a domain.AgendaSummary
		if err := rows.Scan(&a.AgendaID, &a.Consultorio, &a.FirstDate, &a.LastDate, &a.Dates, &a.Citas); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FindAgendaAppointmentsPaged lista, paginado y filtrable, las citas próximas NO atendidas de una
// agenda (o médico), ordenadas por fecha+hora del slot. Eficiencia/no-bloqueo de SIESA:
//   - NOLOCK en todas las tablas (no toma locks compartidos → no bloquea la UI de SIESA).
//   - Paginación real OFFSET/FETCH; page_size acotado a 100.
//   - Total en la MISMA query con COUNT(*) OVER() (un solo plan, sin segundo scan).
//   - Predicados sargables (id_programacion/cod_medi/fecha/estado). El filtro de agenda acota el set
//     antes del LIKE de nombre.
func (r *AppointmentRepo) FindAgendaAppointmentsPaged(ctx context.Context, f domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	size := f.PageSize
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	offset := (page - 1) * size

	conds := []string{"c.estado NOT IN ('C','A')", "c.fecha >= CAST(GETDATE() AS DATE)"}
	args := []interface{}{}
	if f.AgendaID != nil {
		conds = append(conds, "c.id_programacion = @agenda")
		args = append(args, sql.Named("agenda", *f.AgendaID))
	}
	if f.DoctorCode != "" {
		conds = append(conds, "c.cod_medi = @doctor")
		args = append(args, sql.Named("doctor", f.DoctorCode))
	}
	if f.Date != "" {
		conds = append(conds, "c.fecha = @date")
		args = append(args, sql.Named("date", f.Date))
	}
	if f.Name != "" {
		conds = append(conds, `LTRIM(RTRIM(CONCAT(p.primer_nom,' ',ISNULL(p.segundo_nom,''),' ',p.primer_ape,' ',ISNULL(p.segundo_ape,'')))) LIKE @name`)
		args = append(args, sql.Named("name", "%"+f.Name+"%"))
	}
	if f.Doc != "" {
		conds = append(conds, "p.num_id = @doc")
		args = append(args, sql.Named("doc", f.Doc))
	}
	args = append(args, sql.Named("off", offset), sql.Named("size", size))

	// Solo columnas visibles (hora del slot + nombre + cédula) + id (para acciones) + total (COUNT OVER).
	//nolint:gosec // G202: las condiciones son literales fijos; los valores del usuario van por sql.Named.
	query := `
	SELECT CAST(c.id AS VARCHAR(20)),
	       ISNULL((SELECT TOP 1 DATEPART(HOUR,pmd.Fecha)*100+DATEPART(MINUTE,pmd.Fecha)
	               FROM programacion_medico_detalle pmd WITH (NOLOCK) WHERE pmd.IdCita=c.id ORDER BY pmd.Fecha),-1) AS hhmm,
	       ISNULL(c.hora,'') AS hora_cita,
	       ISNULL(LTRIM(RTRIM(CONCAT(p.primer_nom,' ',ISNULL(p.segundo_nom,''),' ',
	           p.primer_ape,' ',ISNULL(p.segundo_ape,'')))),'') AS nombre_paciente,
	       ISNULL(p.num_id,'') AS doc_paciente,
	       COUNT(*) OVER() AS total_rows
	FROM citas c WITH (NOLOCK)
	INNER JOIN sis_paci p WITH (NOLOCK) ON p.autoid = c.autoid
	WHERE ` + strings.Join(conds, " AND ") + `
	ORDER BY c.fecha, c.hora
	OFFSET @off ROWS FETCH NEXT @size ROWS ONLY`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []domain.AgendaAppointmentRow
	total := 0
	for rows.Next() {
		var it domain.AgendaAppointmentRow
		var hhmm24, totalRows int
		var horaCita string
		if err := rows.Scan(&it.ID, &hhmm24, &horaCita, &it.PatientName, &it.PatientDoc, &totalRows); err != nil {
			return nil, err
		}
		total = totalRows
		if hhmm24 >= 0 {
			it.Hora = fmt.Sprintf("%02d:%02d", hhmm24/100, hhmm24%100)
		} else {
			it.Hora = horaCita // fallback: la hora almacenada en la cita
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pages := 0
	if total > 0 {
		pages = (total + size - 1) / size
	}
	return &domain.AgendaAppointmentsPage{Items: items, Total: total, Page: page, Pages: pages}, nil
}

// RescheduleDate moves all non-cancelled appointments of an agenda+doctor from oldDate to
// newDate. INTEG-03: this must also manage slots, atomically — otherwise the old slot stays
// "occupied" forever (phantom) and the new slot is never claimed (another booking can take it).
// All three steps run in one transaction: release old slots → move citas → claim new slots.
// The new-slot claim is best-effort (matches by time on newDate); if no slot exists/is free,
// the citas still move, consistent with the prior behaviour.
//
// CÓDIGO MUERTO (PR multi-slot): el único call-site (handleRescheduleSameAgenda) retorna 404
// antes de invocar esta función porque FindWorkingDayException es un stub que siempre devuelve
// (nil, nil). Además es INCOMPATIBLE con el modelo multi-slot: el paso 3 reclama solo el slot
// cuyo HH:MM coincide con citas.hora (el inicial), liberando los N-1 restantes de una cita
// multi-slot. Si esta vía administrativa se reactiva, el paso 3 debe reclamar TODOS los slots
// que la cita ocupaba en oldDate (conteo previo vía SlotCountForAppointment), no solo el inicial.
func (r *AppointmentRepo) RescheduleDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("siesa RescheduleDate begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Release the old slots (identified by IdCita = the appointments being moved).
	if _, err = tx.ExecContext(ctx, `
	UPDATE programacion_medico_detalle SET IdCita = NULL
	WHERE IdCita IN (
	    SELECT id FROM citas
	    WHERE id_programacion = @p1 AND CAST(cod_medi AS VARCHAR(20)) = @p2
	      AND fecha = @p3 AND estado <> 'C')`,
		agendaID, doctorDoc, oldDate); err != nil {
		return 0, fmt.Errorf("siesa RescheduleDate release old slots: %w", err)
	}

	// 2. Move the appointments to the new date.
	result, err := tx.ExecContext(ctx, `
	UPDATE citas SET fecha = @p1, estado = 'P'
	WHERE id_programacion = @p2 AND CAST(cod_medi AS VARCHAR(20)) = @p3
	  AND fecha = @p4 AND estado <> 'C'`,
		newDate, agendaID, doctorDoc, oldDate)
	if err != nil {
		return 0, fmt.Errorf("siesa RescheduleDate move citas: %w", err)
	}
	n, _ := result.RowsAffected()

	// 3. Claim the new slots on newDate, matched to each moved appointment's time.
	if _, err = tx.ExecContext(ctx, `
	UPDATE pmd SET IdCita = c.id
	FROM programacion_medico_detalle pmd
	JOIN citas c ON c.id_programacion = pmd.IdProgramacionMedico
	    -- N6: c.hora está en 12h ('07:15') y pmd.Fecha en 24h. Reconstruir la hora 24h
	    -- desde c.hora + c.meridiano para que el match no falle en horarios PM.
	    AND CONVERT(VARCHAR(5), pmd.Fecha, 108) =
	        RIGHT('0' + CONVERT(VARCHAR(2),
	            CASE WHEN c.meridiano = 'pm' AND CONVERT(INT, LEFT(c.hora, 2)) <> 12 THEN CONVERT(INT, LEFT(c.hora, 2)) + 12
	                 WHEN c.meridiano = 'am' AND CONVERT(INT, LEFT(c.hora, 2)) = 12 THEN 0
	                 ELSE CONVERT(INT, LEFT(c.hora, 2)) END), 2) + ':' + RIGHT(c.hora, 2)
	WHERE c.id_programacion = @p1 AND CAST(c.cod_medi AS VARCHAR(20)) = @p2
	  AND c.fecha = @p3 AND c.estado <> 'C'
	  AND pmd.Fecha >= @p3 AND pmd.Fecha < DATEADD(DAY, 1, @p3) AND pmd.IdCita IS NULL`,
		agendaID, doctorDoc, newDate); err != nil {
		return 0, fmt.Errorf("siesa RescheduleDate claim new slots: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("siesa RescheduleDate commit: %w", err)
	}
	return int(n), nil
}

// ────────────────────────────────────────────────────────────────────────────
// fetchProcedures / fetchProceduresBatch
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) fetchProcedures(ctx context.Context, apptID string) ([]domain.AppointmentProcedure, error) {
	var procs []domain.AppointmentProcedure
	apptIDInt, _ := strconv.ParseInt(apptID, 10, 64)

	// Tabla de procedimientos/imágenes
	rows, err := r.db.QueryContext(ctx, `
	SELECT CAST(cp.id AS VARCHAR(20)), @p1,
	       ISNULL(cp.id_procedimiento,''), ISNULL(cp.id_procedimiento,''),
	       ISNULL(cp.Cantidad,1), CAST(0.0 AS FLOAT), ISNULL(cp.Servicio,0)
	FROM citas_procedimientos cp WITH (NOLOCK) WHERE cp.id_cita = @p2`, apptID, apptIDInt)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p domain.AppointmentProcedure
		if err := rows.Scan(&p.ID, &p.AppointmentID, &p.CupCode, &p.CupName, &p.Quantity, &p.UnitValue, &p.ServiceID); err != nil {
			rows.Close()
			return nil, err
		}
		procs = append(procs, p)
	}
	rows.Close()

	// Tabla de consultas
	rows2, err := r.db.QueryContext(ctx, `
	SELECT CAST(cpa.id AS VARCHAR(20)), @p1,
	       ISNULL(cpa.CodProcedimiento,''), ISNULL(cpa.NomProcedimiento,''),
	       1, ISNULL(cpa.Valor,0.0), ISNULL(cpa.Servicio,0)
	FROM citas_procedimientos_asuntos cpa WITH (NOLOCK) WHERE cpa.IdCita = @p2`, apptID, apptIDInt)
	if err != nil {
		return procs, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var p domain.AppointmentProcedure
		if err := rows2.Scan(&p.ID, &p.AppointmentID, &p.CupCode, &p.CupName, &p.Quantity, &p.UnitValue, &p.ServiceID); err != nil {
			return procs, err
		}
		procs = append(procs, p)
	}
	return procs, rows2.Err()
}

func (r *AppointmentRepo) fetchProceduresBatch(ctx context.Context, apptIDs []string) (map[string][]domain.AppointmentProcedure, error) {
	result := make(map[string][]domain.AppointmentProcedure)
	if len(apptIDs) == 0 {
		return result, nil
	}

	fetchTable := func(query string, args []interface{}) error {
		rows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p domain.AppointmentProcedure
			if err := rows.Scan(&p.ID, &p.AppointmentID, &p.CupCode, &p.CupName, &p.Quantity, &p.UnitValue, &p.ServiceID); err != nil {
				return err
			}
			result[p.AppointmentID] = append(result[p.AppointmentID], p)
		}
		return rows.Err()
	}

	clause, args := inParamsAsInt(apptIDs, 1)

	q1 := fmt.Sprintf(`
	SELECT CAST(cp.id AS VARCHAR(20)), CAST(cp.id_cita AS VARCHAR(20)),
	       ISNULL(cp.id_procedimiento,''), ISNULL(cp.id_procedimiento,''),
	       ISNULL(cp.Cantidad,1), CAST(0.0 AS FLOAT), ISNULL(cp.Servicio,0)
	FROM citas_procedimientos cp WITH (NOLOCK)
	WHERE cp.id_cita IN (%s)`, clause)
	if err := fetchTable(q1, args); err != nil {
		return result, err
	}

	clause2, args2 := inParamsAsInt(apptIDs, 1)
	q2 := fmt.Sprintf(`
	SELECT CAST(cpa.id AS VARCHAR(20)), CAST(cpa.IdCita AS VARCHAR(20)),
	       ISNULL(cpa.CodProcedimiento,''), ISNULL(cpa.NomProcedimiento,''),
	       1, ISNULL(cpa.Valor,0.0), ISNULL(cpa.Servicio,0)
	FROM citas_procedimientos_asuntos cpa WITH (NOLOCK)
	WHERE cpa.IdCita IN (%s)`, clause2)
	if err := fetchTable(q2, args2); err != nil {
		return result, err
	}

	return result, nil
}

// lookupSubjectTypeFromHistory is the DEFENSIVE FALLBACK for resolving the subject type
// when the caller did not provide one (CUPS missing from the local catalog). The primary,
// deterministic source is cups_procedimientos.asunto_id (CreateAppointmentInput.SubjectType).
// It searches real appointment history, accepting only IDs present in sis_asunto:
// citas_procedimientos (imaging/procedures) first, then citas_procedimientos_asuntos (consultations),
// then the AsuntoPctos catalog. Can be removed once catalog coverage is confirmed 100%.
func (r *AppointmentRepo) lookupSubjectTypeFromHistory(ctx context.Context, procs []domain.CreateProcedureInput) int {
	for _, p := range procs {
		if p.CupCode == "" {
			continue
		}
		baseCups := strings.SplitN(p.CupCode, "-", 2)[0]

		// Search in procedures/imaging (citas_procedimientos)
		var subjectType int
		_ = r.db.QueryRowContext(
			ctx, `
		SELECT TOP 1 c.asunto
		FROM citas c WITH (NOLOCK)
		JOIN citas_procedimientos cp WITH (NOLOCK) ON cp.id_cita = c.id
		WHERE LEFT(cp.id_procedimiento, @p2) = @p1
		  AND c.asunto IN (SELECT id FROM sis_asunto WITH (NOLOCK))
		  AND c.estado != 'C'
		ORDER BY c.fecha DESC`,
			baseCups, len(baseCups),
		).Scan(&subjectType)
		if subjectType > 0 {
			return subjectType
		}

		// Search in consultations (citas_procedimientos_asuntos)
		_ = r.db.QueryRowContext(
			ctx, `
		SELECT TOP 1 c.asunto
		FROM citas c WITH (NOLOCK)
		JOIN citas_procedimientos_asuntos cpa WITH (NOLOCK) ON cpa.IdCita = c.id
		WHERE cpa.CodProcedimiento = @p1
		  AND c.asunto IN (SELECT id FROM sis_asunto WITH (NOLOCK))
		  AND c.estado != 'C'
		ORDER BY c.fecha DESC`,
			baseCups,
		).Scan(&subjectType)
		if subjectType > 0 {
			return subjectType
		}

		// No history: query AsuntoPctos (SIESA catalog).
		// Prevents incorrect default: 890274 → subject 8 (neurology), not 1 (physiatry).
		_ = r.db.QueryRowContext(
			ctx,
			`SELECT TOP 1 Asunto FROM AsuntoPctos WITH (NOLOCK) WHERE CodProcedimiento = @p1`, baseCups,
		).Scan(&subjectType)
		if subjectType > 0 {
			return subjectType
		}
	}
	return 0 // unresolved: el caller (Create) falla y escala, en vez de asumir un asunto incorrecto
}
