package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

var _ repository.AppointmentRepository = (*AppointmentRepo)(nil)

// AppointmentRepo gestiona citas en SIESA (SQL Server ZeusSalud_Neuro).
//
// Mapeo de campos:
//   Appointment.ID         ← citas.id  (IDENTITY)
//   Appointment.PatientID  ← citas.autoid (int → string)
//   Appointment.DoctorID   ← citas.cod_medi (int → string)
//   Appointment.Entity     ← citas.contrato (int → string)
//   Appointment.AgendaID   ← citas.id_programacion
//   Appointment.Date       ← citas.fecha (DATE)
//   Appointment.TimeSlot   ← "YYYYMMDDHHmm" derivado de fecha+hora
//   Appointment.Canceled   ← citas.estado = 'C'
//   Appointment.Confirmed  ← citas.AsistenciaConfirmada = 1 OR estado IN ('CC','A')
type AppointmentRepo struct {
	db *sql.DB
}

func NewAppointmentRepo(db *sql.DB) *AppointmentRepo {
	return &AppointmentRepo{db: db}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// slotToDateHora convierte "YYYYMMDDHHmm" → fecha "YYYY-MM-DD", hora "HH:mm", meridiano "am"|"pm"
func slotToDateHora(slot string) (fecha, hora, meridiano string) {
	if len(slot) < 12 {
		return "", "", ""
	}
	fecha = fmt.Sprintf("%s-%s-%s", slot[0:4], slot[4:6], slot[6:8])
	hora = fmt.Sprintf("%s:%s", slot[8:10], slot[10:12])
	hInt, _ := strconv.Atoi(slot[8:10])
	if hInt < 12 {
		meridiano = "am"
	} else {
		meridiano = "pm"
	}
	return
}

// timecodeFromFechaHora convierte fecha (time.Time) + hora ("HH:mm") → "YYYYMMDDHHmm"
func timecodeFromFechaHora(fecha time.Time, hora string) string {
	return fecha.Format("20060102") + strings.ReplaceAll(hora, ":", "")
}

// idSedeForAsunto: asuntos 1,2,3,4,5,12 = imágenes/RX (sede 3); resto = sede 2.
func idSedeForAsunto(asunto int) int {
	switch asunto {
	case 1, 2, 3, 4, 5, 12:
		return 3
	default:
		return 2
	}
}

// isConsultaAsunto: asuntos 7,8,9,10,11 → citas_procedimientos_asuntos.
// Asunto 1 = RX (imágenes), va a citas_procedimientos.
func isConsultaAsunto(asunto int) bool {
	switch asunto {
	case 7, 8, 9, 10, 11:
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

// lookupContrato resuelve empresa, codigo de contrato y regimen a partir de:
//   - un código numérico de contrato ("4") → busca directamente por contratos.codigo
//   - un código de empresa ("EPS005") → toma el primer contrato activo de esa empresa
func lookupContrato(ctx context.Context, db *sql.DB, entityInput string) (empresa, contrato string, regimen int, err error) {
	if codigoInt, convErr := strconv.Atoi(entityInput); convErr == nil {
		err = db.QueryRowContext(ctx,
			`SELECT ISNULL(empresa,''), CAST(codigo AS VARCHAR(20)), ISNULL(regimen,1) FROM contratos WHERE codigo = @p1`,
			codigoInt,
		).Scan(&empresa, &contrato, &regimen)
	} else {
		// empresa code (ej: "EPS005") → primer contrato activo
		err = db.QueryRowContext(ctx,
			`SELECT TOP 1 ISNULL(empresa,''), CAST(codigo AS VARCHAR(20)), ISNULL(regimen,1) FROM contratos WHERE empresa = @p1 AND activo = 1 ORDER BY codigo`,
			entityInput,
		).Scan(&empresa, &contrato, &regimen)
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

	err := r.db.QueryRowContext(ctx, `
	SELECT CAST(c.id AS VARCHAR(20)),
	       CAST(c.fecha AS DATE),
	       ISNULL(c.hora,''),
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT)
	FROM citas c
	WHERE c.id = @p1`, id,
	).Scan(
		&appt.ID, &fecha, &hora,
		&appt.DoctorID, &appt.PatientID, &appt.Entity, &appt.AgendaID,
		&estado, &appt.Observations, &asistenciaConfirmada,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("siesa FindByID %s: %w", id, err)
	}

	appt.Date = fecha
	appt.DoctorName = appt.DoctorID
	appt.TimeSlot = timecodeFromFechaHora(fecha, hora)
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
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT)
	FROM citas c
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
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
	       CAST(c.autoid AS VARCHAR(20)),
	       ISNULL(c.contrato,''),
	       ISNULL(c.id_programacion,0),
	       ISNULL(c.estado,'P'),
	       ISNULL(c.observacion,''),
	       CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT)
	FROM citas c
	WHERE c.id_programacion = @p1
	  AND CAST(c.fecha AS DATE) = @p2
	  AND c.estado != 'C'
	ORDER BY c.hora`, agendaID, date)
	if err != nil {
		return nil, fmt.Errorf("siesa FindByAgendaAndDate: %w", err)
	}
	defer rows.Close()

	return r.scanAppointments(ctx, rows)
}

// scanAppointments escanea rows de citas (sin PatientName/Phone) y carga procedimientos.
func (r *AppointmentRepo) scanAppointments(ctx context.Context, rows *sql.Rows) ([]domain.Appointment, error) {
	var appointments []domain.Appointment
	var ids []string
	for rows.Next() {
		var appt domain.Appointment
		var fecha time.Time
		var hora, estado string
		var asistenciaConfirmada int
		if err := rows.Scan(
			&appt.ID, &fecha, &hora,
			&appt.DoctorID, &appt.PatientID, &appt.Entity, &appt.AgendaID,
			&estado, &appt.Observations, &asistenciaConfirmada,
		); err != nil {
			return nil, err
		}
		appt.Date = fecha
		appt.DoctorName = appt.DoctorID
		appt.TimeSlot = timecodeFromFechaHora(fecha, hora)
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
	fecha, hora, meridiano := slotToDateHora(input.TimeSlot)
	if fecha == "" {
		return nil, fmt.Errorf("timeslot inválido: %q", input.TimeSlot)
	}

	empresa, contrato, regimen, err := lookupContrato(ctx, r.db, input.Entity)
	if err != nil {
		return nil, fmt.Errorf("lookup contrato %q: %w", input.Entity, err)
	}
	codMediInt, _ := strconv.Atoi(input.DoctorID)
	patientInt, _ := strconv.Atoi(input.PatientID)

	// Determinar asunto SIESA consultando el historial de citas.
	// Filtra a IDs válidos en sis_asunto para excluir valores incorrectos (ej: 28 del bot).
	// Prueba primero citas_procedimientos (imágenes/procedimientos), luego citas_procedimientos_asuntos (consultas).
	asunto := r.lookupAsuntoFromHistory(ctx, input.Procedures)
	idSede := idSedeForAsunto(asunto)
	tipoUsuario := "01"
	if regimen == 2 {
		tipoUsuario = "02"
	}

	// tipo_servicio: NULL para consultas (asuntos 7-11); para procedimientos buscar en historial SIESA.
	var tipoServicio interface{}
	if !isConsultaAsunto(asunto) && len(input.Procedures) > 0 {
		baseCupTS := strings.SplitN(input.Procedures[0].CupCode, "-", 2)[0]
		var svcID int
		_ = r.db.QueryRowContext(ctx, `
			SELECT TOP 1 ISNULL(cp.Servicio,0)
			FROM citas_procedimientos cp
			JOIN citas c ON c.id = cp.id_cita
			WHERE LEFT(cp.id_procedimiento, @p2) = @p1 AND cp.Servicio > 0
			ORDER BY c.fecha DESC`,
			baseCupTS, len(baseCupTS)).Scan(&svcID)
		if svcID == 0 {
			_ = r.db.QueryRowContext(ctx,
				`SELECT TOP 1 ISNULL(Servicio,0) FROM AsuntoPctos WHERE CodProcedimiento = @p1`, baseCupTS,
			).Scan(&svcID)
		}
		if svcID > 0 {
			tipoServicio = svcID
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// empresa = código empresa (ej: "EPS005"), contrato = codigo numérico (ej: "4")
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
	    @p9, @p10, 0,
	    2, 2, @p11,
	    0, 0, 0, 0,
	    0, CAST(GETDATE() AS DATE), @p12,
	    @p13
	)`,
		patientInt, codMediInt, fecha, hora, meridiano,
		asunto, empresa, contrato,
		input.AgendaID, idSede, tipoUsuario,
		input.Observations, tipoServicio,
	).Scan(&newID)
	if err != nil {
		return nil, fmt.Errorf("insert citas: %w", err)
	}

	// Marcar slot como ocupado (bloqueo optimista: AND IdCita IS NULL)
	result, err := tx.ExecContext(ctx, `
	UPDATE programacion_medico_detalle
	SET IdCita = @p1
	WHERE IdProgramacionMedico = @p2
	  AND CAST(Fecha AS DATE) = @p3
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

	apptDate, _ := time.Parse("2006-01-02", fecha)
	return &domain.Appointment{
		ID:           fmt.Sprintf("%d", newID),
		Date:         apptDate,
		TimeSlot:     input.TimeSlot,
		DoctorID:     input.DoctorID,
		DoctorName:   input.DoctorID,
		PatientID:    input.PatientID,
		Entity:       input.Entity,
		AgendaID:     input.AgendaID,
		Observations: input.Observations,
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// CreatePxCita / CreatePxCitaBatch
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) CreatePxCita(ctx context.Context, input domain.CreatePxCitaInput) error {
	apptID := strconv.Itoa(input.AppointmentID)

	var asunto int
	_ = r.db.QueryRowContext(ctx,
		`SELECT ISNULL(asunto,0) FROM citas WHERE id = @p1`, apptID,
	).Scan(&asunto)

	qty := input.Quantity
	if qty == 0 {
		qty = 1
	}

	if isConsultaAsunto(asunto) {
		// Buscar Servicio y NomProcedimiento desde historial de SIESA para este CUPS+asunto.
		var servicio int
		var nomProc string
		_ = r.db.QueryRowContext(ctx, `
			SELECT TOP 1 ISNULL(cpa.Servicio,0), ISNULL(cpa.NomProcedimiento,'')
			FROM citas_procedimientos_asuntos cpa
			JOIN citas c ON c.id = cpa.IdCita
			WHERE cpa.CodProcedimiento = @p1 AND c.asunto = @p2 AND cpa.Servicio > 0
			ORDER BY c.fecha DESC`,
			input.CupCode, asunto).Scan(&servicio, &nomProc)

		// Fallback: AsuntoPctos tiene Servicio y NomProcedimiento canónicos (sin depender de historial)
		if servicio == 0 || nomProc == "" {
			var svcFb int
			var nomFb string
			_ = r.db.QueryRowContext(ctx,
				`SELECT TOP 1 ISNULL(Servicio,0), ISNULL(NomProcedimiento,'') FROM AsuntoPctos WHERE Asunto = @p1`, asunto,
			).Scan(&svcFb, &nomFb)
			if servicio == 0 && svcFb > 0 {
				servicio = svcFb
			}
			if nomProc == "" && nomFb != "" {
				nomProc = nomFb
			}
		}
		if nomProc == "" {
			nomProc = input.CupCode
		}

		_, err := r.db.ExecContext(ctx, `
		INSERT INTO citas_procedimientos_asuntos
		    (IdCita, IdSisDeta, Asunto, Servicio, TipoManual, CodProcedimiento, NomProcedimiento, Valor, FechaRegistro)
		VALUES (@p1, 0, @p2, @p3, '256', @p4, @p5, @p6, GETDATE())`,
			apptID, asunto, servicio, input.CupCode, nomProc, input.UnitValue)
		return err
	}

	// Extraer código base (sin sufijo): la orden médica siempre trae el código base
	// y la cantidad por separado. El código interno SIESA usa el formato "891509-16".
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
		FROM citas_procedimientos cp
		JOIN citas c ON c.id = cp.id_cita
		WHERE LEFT(cp.id_procedimiento, @p2) = @p1 AND cp.Servicio > 0
		ORDER BY c.fecha DESC`,
		baseCup, len(baseCup)).Scan(&servicio)
	if servicio == 0 {
		_ = r.db.QueryRowContext(ctx,
			`SELECT TOP 1 ISNULL(Servicio,0) FROM AsuntoPctos WHERE CodProcedimiento = @p1`, baseCup,
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

func (r *AppointmentRepo) CreatePxCitaBatch(ctx context.Context, inputs []domain.CreatePxCitaInput) error {
	for _, in := range inputs {
		if err := r.CreatePxCita(ctx, in); err != nil {
			return fmt.Errorf("pxcita %s: %w", in.CupCode, err)
		}
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Confirm / Cancel / Batch
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) Confirm(ctx context.Context, id string, channel, channelID string) error {
	obs := fmt.Sprintf("Confirmado via WhatsApp [%s]", channelID)
	_, err := r.db.ExecContext(ctx, `
	UPDATE citas
	SET AsistenciaConfirmada          = 1,
	    FechaConfirmaAsistencia        = GETDATE(),
	    FechaSistemaConfirmaAsistencia = GETDATE(),
	    IdTipoConfirmacionAsistencia   = 1,
	    ObservacionConfirmaAsistencia  = @p2
	WHERE id = @p1`, id, obs)
	return err
}

func (r *AppointmentRepo) Cancel(ctx context.Context, id string, reason, channel, channelID string) error {
	obs := fmt.Sprintf(" [Cancelada via WhatsApp: %s]", reason)
	_, err := r.db.ExecContext(ctx, `
	UPDATE citas
	SET estado        = 'C',
	    motivo_cancela = @p2,
	    observacion   = ISNULL(observacion,'') + @p3
	WHERE id = @p1`, id, reason, obs)
	if err != nil {
		return err
	}
	// Liberar slot en programacion_medico_detalle
	_, _ = r.db.ExecContext(ctx,
		`UPDATE programacion_medico_detalle SET IdCita = NULL WHERE CAST(IdCita AS VARCHAR(20)) = @p1`, id)
	return nil
}

func (r *AppointmentRepo) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	if len(ids) == 0 {
		return nil
	}
	obs := fmt.Sprintf("Confirmado via WhatsApp [%s]", channelID)
	// @p1 = obs; IDs empiezan en @p2
	clause, idArgs := inParams(ids, 2)
	allArgs := append([]interface{}{obs}, idArgs...)
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE citas
		SET AsistenciaConfirmada          = 1,
		    FechaConfirmaAsistencia        = GETDATE(),
		    FechaSistemaConfirmaAsistencia = GETDATE(),
		    IdTipoConfirmacionAsistencia   = 1,
		    ObservacionConfirmaAsistencia  = @p1
		WHERE id IN (%s)`, clause), allArgs...)
	return err
}

func (r *AppointmentRepo) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	if len(ids) == 0 {
		return nil
	}

	// PK_citas es compuesta: (cod_medi, fecha, hora, meridiano, estado, horacan, CodGrupo).
	// Al cambiar estado 'P'→'C', si ya existe una fila con estado='C' en ese mismo slot
	// (cita previa cancelada y reagendada), SQL Server viola la PK.
	// Solución: eliminar la fila cancelada previa del mismo slot antes de actualizar.
	clause0, idArgs0 := inParams(ids, 1)
	_, _ = r.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE c_old
		FROM citas c_old
		INNER JOIN citas c_new
		    ON c_new.cod_medi  = c_old.cod_medi
		   AND c_new.fecha     = c_old.fecha
		   AND c_new.hora      = c_old.hora
		   AND c_new.meridiano = c_old.meridiano
		   AND c_new.horacan   = c_old.horacan
		   AND c_new.CodGrupo  = c_old.CodGrupo
		   AND c_old.estado    = 'C'
		WHERE c_new.id IN (%s) AND c_new.estado = 'P'`, clause0),
		idArgs0...)

	obs := fmt.Sprintf(" [Cancelada via WhatsApp: %s]", reason)
	// @p1 = reason, @p2 = obs; IDs empiezan en @p3
	clause, idArgs := inParams(ids, 3)
	allArgs := append([]interface{}{reason, obs}, idArgs...)
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE citas
		SET estado        = 'C',
		    motivo_cancela = @p1,
		    observacion   = ISNULL(observacion,'') + @p2
		WHERE id IN (%s)`, clause), allArgs...)
	if err != nil {
		return fmt.Errorf("siesa cancel batch: %w", err)
	}
	// Liberar slots
	clause2, idArgs2 := inParams(ids, 1)
	_, _ = r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE programacion_medico_detalle SET IdCita = NULL WHERE CAST(IdCita AS VARCHAR(20)) IN (%s)`, clause2),
		idArgs2...)
	return nil
}

func (r *AppointmentRepo) DeleteBatch(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	clause, args := inParams(ids, 1)
	_, _ = r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE programacion_medico_detalle SET IdCita = NULL WHERE CAST(IdCita AS VARCHAR(20)) IN (%s)`, clause),
		args...)
	clause2, args2 := inParams(ids, 1)
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM citas WHERE id IN (%s)`, clause2), args2...)
	return err
}

// ────────────────────────────────────────────────────────────────────────────
// Consultas auxiliares
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) HasFutureForCup(ctx context.Context, patientID, cupCode string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `
	SELECT TOP 1 1
	FROM citas c
	JOIN citas_procedimientos cp ON cp.id_cita = c.id
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
	FROM citas c
	JOIN citas_procedimientos_asuntos cpa ON cpa.IdCita = c.id
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
	FROM citas c
	JOIN citas_procedimientos cp ON cp.id_cita = c.id
	JOIN sis_medi m ON m.codigo = c.cod_medi
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

	// Busca tanto el código base como sus alternos con sufijo numérico (ej: 891901, 891901-16).
	// LEFT(id_procedimiento, CHARINDEX('-', id_procedimiento+'-') - 1) extrae el código base:
	//   '891901'    → '891901'
	//   '891901-16' → '891901'
	// La cantidad efectiva usa el sufijo cuando es numérico (891901-16 = 16 unidades),
	// y Cantidad en caso contrario.
	clause, cupsArgs := inParams(cupsCodes, 3)
	allArgs := append([]interface{}{startDate, endDate}, cupsArgs...)

	var count int
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`
	SELECT ISNULL(SUM(
	    CASE
	        WHEN CHARINDEX('-', cp.id_procedimiento) > 0
	             AND ISNUMERIC(SUBSTRING(cp.id_procedimiento, CHARINDEX('-', cp.id_procedimiento)+1, 20)) = 1
	        THEN CAST(SUBSTRING(cp.id_procedimiento, CHARINDEX('-', cp.id_procedimiento)+1, 20) AS INT)
	        ELSE ISNULL(cp.Cantidad, 1)
	    END
	), 0)
	FROM citas c
	JOIN citas_procedimientos cp ON cp.id_cita = c.id
	WHERE c.fecha >= @p1 AND c.fecha < @p2
	  AND LEFT(cp.id_procedimiento, CHARINDEX('-', cp.id_procedimiento + '-') - 1) IN (%s)
	  AND c.estado != 'C'`, clause), allArgs...,
	).Scan(&count)
	return count, err
}

func (r *AppointmentRepo) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	rows, err := r.db.QueryContext(ctx, `
	SELECT CAST(c.id AS VARCHAR(20)),
	       CAST(c.fecha AS DATE),
	       ISNULL(c.hora,''),
	       CAST(ISNULL(c.cod_medi,0) AS VARCHAR(20)),
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
	       ISNULL(NULLIF(LTRIM(RTRIM(p.telefono)),''), ISNULL(p.celular,'')) AS telefono_paciente
	FROM citas c
	INNER JOIN sis_paci p ON p.autoid = c.autoid
	WHERE CAST(c.fecha AS DATE) = @p1 AND c.estado != 'C'
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
		var hora, estado string
		var asistenciaConfirmada int
		if err := rows.Scan(
			&appt.ID, &fecha, &hora,
			&appt.DoctorID, &appt.PatientID, &appt.Entity, &appt.AgendaID,
			&estado, &appt.Observations, &asistenciaConfirmada,
			&appt.PatientName, &appt.PatientPhone,
		); err != nil {
			return nil, err
		}
		appt.Date = fecha
		appt.DoctorName = appt.DoctorID
		appt.TimeSlot = timecodeFromFechaHora(fecha, hora)
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

func (r *AppointmentRepo) RescheduleDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (int, error) {
	result, err := r.db.ExecContext(ctx, `
	UPDATE citas SET fecha = @p1, estado = 'P'
	WHERE id_programacion = @p2
	  AND CAST(cod_medi AS VARCHAR(20)) = @p3
	  AND CAST(fecha AS DATE) = @p4
	  AND estado != 'C'`,
		newDate, agendaID, doctorDoc, oldDate)
	if err != nil {
		return 0, fmt.Errorf("siesa RescheduleDate: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ────────────────────────────────────────────────────────────────────────────
// fetchProcedures / fetchProceduresBatch
// ────────────────────────────────────────────────────────────────────────────

func (r *AppointmentRepo) fetchProcedures(ctx context.Context, apptID string) ([]domain.AppointmentProcedure, error) {
	var procs []domain.AppointmentProcedure

	// Tabla de procedimientos/imágenes
	rows, err := r.db.QueryContext(ctx, `
	SELECT CAST(cp.id AS VARCHAR(20)), @p1,
	       ISNULL(cp.id_procedimiento,''), ISNULL(cp.id_procedimiento,''),
	       ISNULL(cp.Cantidad,1), CAST(0.0 AS FLOAT), ISNULL(cp.Servicio,0)
	FROM citas_procedimientos cp WHERE CAST(cp.id_cita AS VARCHAR(20)) = @p2`, apptID, apptID)
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
	FROM citas_procedimientos_asuntos cpa WHERE CAST(cpa.IdCita AS VARCHAR(20)) = @p2`, apptID, apptID)
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

	clause, args := inParams(apptIDs, 1)

	q1 := fmt.Sprintf(`
	SELECT CAST(cp.id AS VARCHAR(20)), CAST(cp.id_cita AS VARCHAR(20)),
	       ISNULL(cp.id_procedimiento,''), ISNULL(cp.id_procedimiento,''),
	       ISNULL(cp.Cantidad,1), CAST(0.0 AS FLOAT), ISNULL(cp.Servicio,0)
	FROM citas_procedimientos cp
	WHERE CAST(cp.id_cita AS VARCHAR(20)) IN (%s)`, clause)
	if err := fetchTable(q1, args); err != nil {
		return result, err
	}

	clause2, args2 := inParams(apptIDs, 1)
	q2 := fmt.Sprintf(`
	SELECT CAST(cpa.id AS VARCHAR(20)), CAST(cpa.IdCita AS VARCHAR(20)),
	       ISNULL(cpa.CodProcedimiento,''), ISNULL(cpa.NomProcedimiento,''),
	       1, ISNULL(cpa.Valor,0.0), ISNULL(cpa.Servicio,0)
	FROM citas_procedimientos_asuntos cpa
	WHERE CAST(cpa.IdCita AS VARCHAR(20)) IN (%s)`, clause2)
	if err := fetchTable(q2, args2); err != nil {
		return result, err
	}

	return result, nil
}

// lookupAsuntoFromHistory determina el asunto SIESA correcto buscando en el historial
// de citas reales. Solo acepta IDs que existen en sis_asunto (excluye valores incorrectos
// como 28, que no corresponde a ningún asunto válido).
// Busca primero en citas_procedimientos (imágenes/procedimientos), luego en
// citas_procedimientos_asuntos (consultas).
func (r *AppointmentRepo) lookupAsuntoFromHistory(ctx context.Context, procs []domain.CreateProcedureInput) int {
	for _, p := range procs {
		if p.CupCode == "" {
			continue
		}
		baseCup := strings.SplitN(p.CupCode, "-", 2)[0]

		// Buscar en procedimientos/imágenes (citas_procedimientos)
		var asunto int
		_ = r.db.QueryRowContext(ctx, `
		SELECT TOP 1 c.asunto
		FROM citas c
		JOIN citas_procedimientos cp ON cp.id_cita = c.id
		WHERE LEFT(cp.id_procedimiento, @p2) = @p1
		  AND c.asunto IN (SELECT id FROM sis_asunto)
		  AND c.estado != 'C'
		ORDER BY c.fecha DESC`,
			baseCup, len(baseCup),
		).Scan(&asunto)
		if asunto > 0 {
			return asunto
		}

		// Buscar en consultas (citas_procedimientos_asuntos)
		_ = r.db.QueryRowContext(ctx, `
		SELECT TOP 1 c.asunto
		FROM citas c
		JOIN citas_procedimientos_asuntos cpa ON cpa.IdCita = c.id
		WHERE cpa.CodProcedimiento = @p1
		  AND c.asunto IN (SELECT id FROM sis_asunto)
		  AND c.estado != 'C'
		ORDER BY c.fecha DESC`,
			baseCup,
		).Scan(&asunto)
		if asunto > 0 {
			return asunto
		}

		// Sin historial: consultar AsuntoPctos (catálogo SIESA).
		// Evita el default incorrecto: 890274 → asunto 8 (neurología), no 1 (fisiatría).
		_ = r.db.QueryRowContext(ctx,
			`SELECT TOP 1 Asunto FROM AsuntoPctos WHERE CodProcedimiento = @p1`, baseCup,
		).Scan(&asunto)
		if asunto > 0 {
			return asunto
		}
	}
	return 1 // default: fisiatría (solo cuando no hay historial ni entrada en AsuntoPctos)
}
