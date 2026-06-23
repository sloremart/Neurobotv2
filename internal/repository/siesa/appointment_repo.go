package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

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
}

func NewAppointmentRepo(db *sql.DB) *AppointmentRepo {
	return &AppointmentRepo{db: db}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// slotToDateTimeComponents converts "YYYYMMDDHHmm" → date "YYYY-MM-DD", timeStr "HH:mm", meridiem "am"|"pm".
// SIESA may store PM slots (1-6 PM) as hours 1-6 in the datetime field (12h format).
// To maintain consistency with the meridiano column in citas, hours 1-6 and ≥12 are marked "pm".
func slotToDateTimeComponents(slot string) (date, timeStr, meridiem string) {
	if len(slot) < 12 {
		return "", "", ""
	}
	date = fmt.Sprintf("%s-%s-%s", slot[0:4], slot[4:6], slot[6:8])
	timeStr = fmt.Sprintf("%s:%s", slot[8:10], slot[10:12])
	hInt, _ := strconv.Atoi(slot[8:10])
	if hInt >= 12 || (hInt >= 1 && hInt <= 6) {
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
		err = db.QueryRowContext(ctx,
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
		err = db.QueryRowContext(ctx,
			`SELECT ISNULL(empresa,''), CAST(codigo AS VARCHAR(20)), ISNULL(regimen,1) FROM contratos WITH (NOLOCK) WHERE codigo = @p1`,
			codeInt,
		).Scan(&company, &contractCode, &regime)
	} else {
		// company code (e.g. "EPS005") → first active contract
		err = db.QueryRowContext(ctx,
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
	err := r.db.QueryRowContext(ctx, `
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

	// serviceType: NULL for consultations (subjects 7-11); for procedures look up from SIESA history.
	var serviceType interface{}
	if !isConsultationSubject(subjectType) && len(input.Procedures) > 0 {
		baseCups := strings.SplitN(input.Procedures[0].CupCode, "-", 2)[0]
		var svcID int
		_ = r.db.QueryRowContext(ctx, `
			SELECT TOP 1 ISNULL(cp.Servicio,0)
			FROM citas_procedimientos cp WITH (NOLOCK)
			JOIN citas c WITH (NOLOCK) ON c.id = cp.id_cita
			WHERE LEFT(cp.id_procedimiento, @p2) = @p1 AND cp.Servicio > 0
			ORDER BY c.fecha DESC`,
			baseCups, len(baseCups)).Scan(&svcID)
		if svcID == 0 {
			_ = r.db.QueryRowContext(ctx,
				`SELECT TOP 1 ISNULL(Servicio,0) FROM AsuntoPctos WITH (NOLOCK) WHERE CodProcedimiento = @p1`, baseCups,
			).Scan(&svcID)
		}
		if svcID > 0 {
			serviceType = svcID
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
	//   - cod_user_asigna_cita = '000000' → usuario "Procesos Automaticos" (usuario.id=10006,
	//     cedula='000000'). Identidad del bot para trazabilidad (la columna guarda la cédula del
	//     funcionario que asigna; antes quedaba en 0 sin atribución).
	//   - formaSolicitud = 4 → catálogo FormaSolicitudCitas: 1=Telefónica, 2=Presencial,
	//     3=Correo, 4=Chatbot. El bot es canal Chatbot.
	var newID int64
	err = tx.QueryRowContext(ctx, `
	INSERT INTO citas (
	    autoid, cod_medi, fecha, hora, meridiano, estado,
	    asunto, empresa, contrato, fecha_solicitud,
	    id_programacion, id_sede, cod_user_asigna_cita,
	    primera_vez_control, formaSolicitud, tipoUsuario,
	    es_terapia, Adicional, CodGrupo, EsCitaMultiple,
	    lugarAtencion, fecha_usuario_desea_cita, observacion,
	    tipo_servicio
	) OUTPUT INSERTED.id
	VALUES (
	    @p1, @p2, @p3, @p4, @p5, 'P',
	    @p6, @p7, @p8, GETDATE(),
	    @p9, @p10, '000000',
	    @p14, 4, @p11,
	    0, 0, 0, 0,
	    0, CAST(GETDATE() AS DATE), @p12,
	    @p13
	)`,
		patientIDInt, doctorCodeInt, fecha, hora, meridiano,
		subjectType, company, contractCode,
		input.AgendaID, locationID, userType,
		input.Observations, serviceType,
		primeraVezControl,
	).Scan(&newID)
	if err != nil {
		return nil, fmt.Errorf("insert citas: %w", err)
	}

	// Claim slot with optimistic lock (AND IdCita IS NULL)
	result, err := tx.ExecContext(ctx, `
	UPDATE programacion_medico_detalle
	SET IdCita = @p1
	WHERE IdProgramacionMedico = @p2
	  AND Fecha >= @p3 AND Fecha < DATEADD(DAY, 1, @p3)
	  AND CONVERT(VARCHAR(5), Fecha, 108) = @p4
	  AND IdCita IS NULL
	  AND Bloqueado = 0`,
		newID, input.AgendaID, fecha, hora,
	)
	if err != nil {
		return nil, fmt.Errorf("update pmd: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("slot_taken")
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Auditoría SIESA (best-effort, no rompe la reserva): registrar el alta de la cita.
	r.writeAuditLog(ctx, fmt.Sprintf("%d", newID), "APARTAR CITA", input.Observations)

	// Resolve doctor name from sis_medi (avoids returning cod_medi as DoctorName)
	var doctorName string
	_ = r.db.QueryRowContext(ctx,
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

func (r *AppointmentRepo) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	apptID := strconv.Itoa(input.AppointmentID)

	var subjectType int
	_ = r.db.QueryRowContext(ctx,
		`SELECT ISNULL(asunto,0) FROM citas WITH (NOLOCK) WHERE id = @p1`, apptID,
	).Scan(&subjectType)

	qty := input.Quantity
	if qty == 0 {
		qty = 1
	}

	if isConsultationSubject(subjectType) {
		// Look up Servicio and NomProcedimiento from SIESA history for this CUPS+subject.
		var serviceID int
		var procName string
		_ = r.db.QueryRowContext(ctx, `
			SELECT TOP 1 ISNULL(cpa.Servicio,0), ISNULL(cpa.NomProcedimiento,'')
			FROM citas_procedimientos_asuntos cpa WITH (NOLOCK)
			JOIN citas c WITH (NOLOCK) ON c.id = cpa.IdCita
			WHERE cpa.CodProcedimiento = @p1 AND c.asunto = @p2 AND cpa.Servicio > 0
			ORDER BY c.fecha DESC`,
			input.CupCode, subjectType).Scan(&serviceID, &procName)

		// Fallback: AsuntoPctos has canonical Servicio and NomProcedimiento (history-independent)
		if serviceID == 0 || procName == "" {
			var svcFb int
			var nameFb string
			_ = r.db.QueryRowContext(ctx,
				`SELECT TOP 1 ISNULL(Servicio,0), ISNULL(NomProcedimiento,'') FROM AsuntoPctos WITH (NOLOCK) WHERE Asunto = @p1`, subjectType,
			).Scan(&svcFb, &nameFb)
			if serviceID == 0 && svcFb > 0 {
				serviceID = svcFb
			}
			if procName == "" && nameFb != "" {
				procName = nameFb
			}
		}
		if procName == "" {
			procName = input.CupCode
		}

		_, err := r.db.ExecContext(ctx, `
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

	// Buscar Servicio desde historial de SIESA para este código de procedimiento.
	var servicio int
	_ = r.db.QueryRowContext(ctx, `
		SELECT TOP 1 ISNULL(cp.Servicio,0)
		FROM citas_procedimientos cp WITH (NOLOCK)
		JOIN citas c WITH (NOLOCK) ON c.id = cp.id_cita
		WHERE LEFT(cp.id_procedimiento, @p2) = @p1 AND cp.Servicio > 0
		ORDER BY c.fecha DESC`,
		baseCup, len(baseCup)).Scan(&servicio)
	if servicio == 0 {
		_ = r.db.QueryRowContext(ctx,
			`SELECT TOP 1 ISNULL(Servicio,0) FROM AsuntoPctos WITH (NOLOCK) WHERE CodProcedimiento = @p1`, baseCup,
		).Scan(&servicio)
	}

	// Construir código interno SIESA: "{base}-{qty}" solo cuando qty > 4.
	// Cantidades 1-4 usan el código base con el campo Cantidad.
	cupCode := baseCup
	if qty > 4 {
		cupCode = fmt.Sprintf("%s-%d", baseCup, qty)
	}

	_, err := r.db.ExecContext(ctx, `
	INSERT INTO citas_procedimientos (id_procedimiento, tipo, id_cita, Servicio, Cantidad)
	VALUES (@p1, '256', @p2, @p3, @p4)`,
		cupCode, apptID, servicio, qty)
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

// siesaBotUserID es el usuario.id de "Procesos Automaticos" (cedula '000000'), identidad del
// bot para las columnas que referencian usuario.id: citas.id_usuario_cancela,
// citas.IdUsuarioConfirmaAsistencia y log_citas.usuario_evento.
// OJO: es distinto de citas.cod_user_asigna_cita, que usa la CÉDULA ('000000'), no el id.
const siesaBotUserID = 10006

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
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("audit_log_panic", "appointment_id", id, "evento", evento, "recover", rec)
			}
		}()
		cctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
		defer cancel()
		if _, err := r.db.ExecContext(cctx, query, id, obs, evento, siesaBotUserID); err != nil {
			slog.Warn("audit_log_write_failed", "appointment_id", id, "evento", evento, "error", err)
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
	WHERE id = @p1`, id, obs, siesaBotUserID)
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
	_, err := r.db.ExecContext(ctx, `
	UPDATE citas
	SET estado             = 'C',
	    horacan            = CONVERT(VARCHAR(5), GETDATE(), 108),
	    motivo             = 2,
	    id_usuario_cancela = @p4,
	    motivo_cancela     = @p2,
	    observacion        = ISNULL(observacion,'') + @p3
	WHERE id = @p1`, id, reason, obs, siesaBotUserID)
	if err != nil {
		return err
	}
	// Liberar slot en programacion_medico_detalle (INTEG-04: loggear si falla — un slot
	// que no se libera queda bloqueado permanentemente y oculta ese horario a los pacientes).
	if idInt, err2 := strconv.ParseInt(id, 10, 64); err2 == nil {
		if _, relErr := r.db.ExecContext(ctx,
			`UPDATE programacion_medico_detalle SET IdCita = NULL WHERE IdCita = @p1`, idInt); relErr != nil {
			slog.Warn("slot_release_failed", "appointment_id", id, "error", relErr)
		}
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
	allArgs := append([]interface{}{obs, siesaBotUserID}, idArgs...)
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE citas
		SET AsistenciaConfirmada          = 1,
		    FechaConfirmaAsistencia        = GETDATE(),
		    FechaSistemaConfirmaAsistencia = GETDATE(),
		    IdTipoConfirmacionAsistencia   = 1,
		    IdUsuarioConfirmaAsistencia    = @p2,
		    ObservacionConfirmaAsistencia  = @p1
		WHERE id IN (%s)`, clause), allArgs...)
	if err != nil {
		return err
	}
	for _, id := range ids {
		r.writeAuditLog(ctx, id, "CITA MODIFICADA", fmt.Sprintf("Confirmado via WhatsApp [conv: %s]", channelID))
	}
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
	allArgs := append([]interface{}{reason, obs, siesaBotUserID}, idArgs...)
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE citas
		SET estado             = 'C',
		    horacan            = CONVERT(VARCHAR(5), GETDATE(), 108),
		    motivo             = 2,
		    id_usuario_cancela = @p3,
		    motivo_cancela     = @p1,
		    observacion        = ISNULL(observacion,'') + @p2
		WHERE id IN (%s)`, clause), allArgs...)
	if err != nil {
		return fmt.Errorf("siesa cancel batch: %w", err)
	}
	// Liberar slots (INTEG-04: loggear si falla)
	clause2, idArgs2 := inParamsAsInt(ids, 1)
	if _, relErr := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE programacion_medico_detalle SET IdCita = NULL WHERE IdCita IN (%s)`, clause2),
		idArgs2...); relErr != nil {
		slog.Warn("slot_release_failed_batch", "ids", ids, "error", relErr)
	}
	for _, id := range ids {
		r.writeAuditLog(ctx, id, "CANCELAR CITA", fmt.Sprintf("CANCELACION DE CITA - Motivo: %s [conv: %s]", reason, channelID))
	}
	return nil
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
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`
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
	//   - SUM(ISNULL(cp.Cantidad,1)): Cantidad is the real number of procedures. The CUPS suffix
	//     (e.g. 891901-16) is a billing VARIANT code, NOT a quantity multiplier (BUG-07).
	//   - LEFT(...CHARINDEX...) still matches the base code so all variants of a CUPS are counted.
	// citas.contrato is varchar in SIESA, hence the string literals.
	clause, cupsArgs := inParams(cupsCodes, 3)
	allArgs := append([]interface{}{startDate, endDate}, cupsArgs...)

	var count int
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`
	SELECT ISNULL(SUM(ISNULL(cp.Cantidad, 1)), 0)
	FROM citas c WITH (NOLOCK)
	JOIN citas_procedimientos cp WITH (NOLOCK) ON cp.id_cita = c.id
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

// RescheduleDate moves all non-cancelled appointments of an agenda+doctor from oldDate to
// newDate. INTEG-03: this must also manage slots, atomically — otherwise the old slot stays
// "occupied" forever (phantom) and the new slot is never claimed (another booking can take it).
// All three steps run in one transaction: release old slots → move citas → claim new slots.
// The new-slot claim is best-effort (matches by time on newDate); if no slot exists/is free,
// the citas still move, consistent with the prior behaviour.
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
	    AND CONVERT(VARCHAR(5), pmd.Fecha, 108) = c.hora
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
		_ = r.db.QueryRowContext(ctx, `
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
		_ = r.db.QueryRowContext(ctx, `
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
		_ = r.db.QueryRowContext(ctx,
			`SELECT TOP 1 Asunto FROM AsuntoPctos WITH (NOLOCK) WHERE CodProcedimiento = @p1`, baseCups,
		).Scan(&subjectType)
		if subjectType > 0 {
			return subjectType
		}
	}
	return 0 // unresolved: el caller (Create) falla y escala, en vez de asumir un asunto incorrecto
}
