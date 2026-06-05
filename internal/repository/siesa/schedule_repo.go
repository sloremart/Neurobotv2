package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

var _ repository.ScheduleRepository = (*ScheduleRepo)(nil)

// ScheduleRepo lee los slots de SIESA desde programacion_medico_detalle.
// En SIESA los slots ya están pre-calculados (un row = un slot de tiempo).
// AgendaID en el dominio = programacion_medico.id (IdProgramacionMedico).
// FindFutureWorkingDays recibe cédulas reales de médico (de cup_medico.doctor_documento)
// y hace JOIN con sis_medi para traducir cedula → codigo interno (Medico).
// DoctorDocument devuelto = CAST(m.cedula AS VARCHAR(20)) = cédula real del médico,
// igual que en Antares, para que docMap en SlotService funcione correctamente.
type ScheduleRepo struct {
	db *sql.DB
}

func NewScheduleRepo(db *sql.DB) *ScheduleRepo {
	return &ScheduleRepo{db: db}
}

// FindFutureWorkingDays devuelve días con slots libres por médico.
// Retorna un WorkingDay por combinación única (fecha, médico, agenda).
func (r *ScheduleRepo) FindFutureWorkingDays(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
	if len(doctorDocs) == 0 {
		return nil, nil
	}

	params := make([]string, len(doctorDocs))
	args := make([]interface{}, len(doctorDocs))
	for i, doc := range doctorDocs {
		params[i] = fmt.Sprintf("@p%d", i+1)
		args[i] = doc
	}

	// doctorDocs son cédulas reales (cup_medico.doctor_documento).
	// JOIN con sis_medi traduce cedula → codigo interno (pmd.Medico).
	// Retornamos cedula (m.cedula) como DoctorDocument para mantener consistencia
	// con el docMap del slot service, que está indexado por cedula de Antares.
	query := fmt.Sprintf(`
	SELECT fecha, cedula, siesa_code, agenda_id,
	       MAX(has_morning)   AS morning_enabled,
	       MAX(has_afternoon) AS afternoon_enabled
	FROM (
	    SELECT
	        CAST(pmd.Fecha AS DATE)         AS fecha,
	        CAST(m.cedula AS VARCHAR(20))   AS cedula,
	        CAST(m.codigo AS VARCHAR(20))   AS siesa_code,
	        pm.id                           AS agenda_id,
	        CASE WHEN DATEPART(HOUR, pmd.Fecha) < 12 THEN 1 ELSE 0 END  AS has_morning,
	        CASE WHEN DATEPART(HOUR, pmd.Fecha) >= 12 THEN 1 ELSE 0 END AS has_afternoon
	    FROM programacion_medico_detalle pmd
	    JOIN programacion_medico pm ON pm.id = pmd.IdProgramacionMedico AND pm.activo = 1
	    JOIN sis_medi m ON m.codigo = pmd.Medico
	    WHERE CAST(m.cedula AS VARCHAR(20)) IN (%s)
	      AND pmd.IdCita IS NULL
	      AND pmd.Bloqueado = 0
	      AND pmd.SinProgramacion = 0
	      AND CAST(pmd.Fecha AS DATE) >= CAST(GETDATE() AS DATE)
	) t
	GROUP BY fecha, cedula, siesa_code, agenda_id
	ORDER BY fecha, cedula, agenda_id`, strings.Join(params, ","))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("FindFutureWorkingDays: %w", err)
	}
	defer rows.Close()

	var days []domain.WorkingDay
	for rows.Next() {
		var d domain.WorkingDay
		var fecha time.Time
		var morningInt, afternoonInt int
		if err := rows.Scan(&fecha, &d.DoctorDocument, &d.DoctorSiesaCode, &d.AgendaID, &morningInt, &afternoonInt); err != nil {
			return nil, err
		}
		d.Date = fecha.Format("2006-01-02")
		d.MorningEnabled = (morningInt == 1)
		d.AfternoonEnabled = (afternoonInt == 1)
		days = append(days, d)
	}
	return days, rows.Err()
}

// FindScheduleConfig deriva la configuración de horario desde los slots reales de SIESA.
// Consulta los próximos 14 días para obtener horas de inicio/fin y duración de slot.
func (r *ScheduleRepo) FindScheduleConfig(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
	query := `
	WITH slot_data AS (
	    SELECT
	        pmd.Fecha,
	        LEAD(pmd.Fecha) OVER (PARTITION BY CAST(pmd.Fecha AS DATE) ORDER BY pmd.Fecha) AS next_fecha
	    FROM programacion_medico_detalle pmd
	    JOIN sis_medi m ON m.codigo = pmd.Medico
	    WHERE pmd.IdProgramacionMedico = @p1
	      AND CAST(m.cedula AS VARCHAR(20)) = @p2
	      AND CAST(pmd.Fecha AS DATE) BETWEEN CAST(GETDATE() AS DATE) AND DATEADD(DAY, 14, CAST(GETDATE() AS DATE))
	      AND pmd.Bloqueado = 0 AND pmd.SinProgramacion = 0
	)
	SELECT
	    ISNULL(MIN(CASE WHEN DATEPART(HOUR, Fecha) < 12 THEN CONVERT(VARCHAR(5), Fecha, 108) END), '') AS morning_first,
	    ISNULL(MAX(CASE WHEN DATEPART(HOUR, Fecha) < 12 THEN CONVERT(VARCHAR(5), Fecha, 108) END), '') AS morning_last,
	    ISNULL(MIN(CASE WHEN DATEPART(HOUR, Fecha) >= 12 THEN CONVERT(VARCHAR(5), Fecha, 108) END), '') AS afternoon_first,
	    ISNULL(MAX(CASE WHEN DATEPART(HOUR, Fecha) >= 12 THEN CONVERT(VARCHAR(5), Fecha, 108) END), '') AS afternoon_last,
	    ISNULL(MIN(CASE WHEN DATEDIFF(MINUTE, Fecha, next_fecha) > 0 THEN DATEDIFF(MINUTE, Fecha, next_fecha) END), 30) AS slot_duration
	FROM slot_data`

	var mFirst, mLast, aFirst, aLast string
	var duration int
	err := r.db.QueryRowContext(ctx, query, scheduleID, doctorDoc).Scan(
		&mFirst, &mLast, &aFirst, &aLast, &duration,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("FindScheduleConfig agenda=%d doc=%s: %w", scheduleID, doctorDoc, err)
	}
	if duration <= 0 {
		duration = 30
	}

	cfg := &domain.ScheduleConfig{
		AgendaID:               scheduleID,
		DoctorDocument:         doctorDoc,
		AppointmentDuration:    duration,
		SessionsPerAppointment: 1,
		IsActive:               true,
	}

	// Habilitar todos los días de la semana (FindFutureWorkingDays ya filtró los días laborales)
	for i := range cfg.WorkDays {
		cfg.WorkDays[i] = true
		cfg.MorningStart[i] = mFirst
		cfg.MorningEnd[i] = addMinutesToHHMM(mLast, duration)
		cfg.AfternoonStart[i] = aFirst
		cfg.AfternoonEnd[i] = addMinutesToHHMM(aLast, duration)
	}

	return cfg, nil
}

// FindByScheduleID busca una agenda en programacion_medico por ID y tipo de procedimiento.
// En SIESA no hay columna descripcion — el tipo se determina por los asuntos asignados:
//   - "procedimiento"/"nocturno": agenda tiene asuntos 13-16 (procedimientos) y ninguno 1-12
//   - "sedacion": agenda tiene asunto 17 (soporte sedación)
//   - default: agenda tiene al menos un asunto 1-12 (consultas/imágenes)
func (r *ScheduleRepo) FindByScheduleID(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error) {
	var s domain.Schedule
	err := r.db.QueryRowContext(ctx,
		`SELECT pm.id, CAST(ISNULL(pm.id_medico, 0) AS VARCHAR(20))
		 FROM programacion_medico pm
		 WHERE pm.id = @p1 AND pm.activo = 1`,
		scheduleID).Scan(&s.ID, &s.DoctorDocument)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("FindByScheduleID %d: %w", scheduleID, err)
	}

	if scheduleType == "" {
		return &s, nil
	}

	// Obtener asuntos asignados a esta agenda via programacion_medico_relacion_asunto
	asuntoRows, err := r.db.QueryContext(ctx,
		`SELECT pmra.IdAsunto
		 FROM programacion_medico_relacion pmr
		 JOIN programacion_medico_relacion_asunto pmra ON pmra.IdProgramacionMedicoRelacion = pmr.Id
		 WHERE pmr.id_programacion = @p1`,
		scheduleID)
	if err != nil {
		return nil, fmt.Errorf("FindByScheduleID asuntos %d: %w", scheduleID, err)
	}
	defer asuntoRows.Close()

	var asuntos []int
	for asuntoRows.Next() {
		var a int
		if err := asuntoRows.Scan(&a); err != nil {
			return nil, err
		}
		asuntos = append(asuntos, a)
	}
	if err := asuntoRows.Err(); err != nil {
		return nil, err
	}

	st := strings.ToLower(scheduleType)

	// Agenda sin asuntos configurados en programacion_medico_relacion_asunto:
	// consultar el asunto más reciente de citas reales para determinar el tipo real.
	// Agenda 254 (resonancias) tiene asunto=4 en citas → tipo consulta → se permite correctamente.
	// Agendas de procedimientos (asuntos 13-16 en citas) quedan excluidas de búsquedas de consulta.
	if len(asuntos) == 0 {
		var lastAsunto int
		_ = r.db.QueryRowContext(ctx, `
			SELECT TOP 1 asunto FROM citas
			WHERE id_programacion = @p1 AND asunto > 0 AND estado != 'C'
			ORDER BY fecha DESC`, scheduleID).Scan(&lastAsunto)

		isProcAgenda := lastAsunto >= 13
		switch st {
		case "procedimiento", "nocturno":
			if isProcAgenda {
				return &s, nil
			}
			return nil, nil
		case "sedacion":
			return nil, nil
		default: // consulta/imagen
			if isProcAgenda {
				return nil, nil
			}
			return &s, nil
		}
	}

	switch st {
	case "sedacion":
		// Requiere asunto 17 (SOPORTE SEDACION)
		for _, a := range asuntos {
			if a == 17 {
				return &s, nil
			}
		}
		return nil, nil

	case "procedimiento", "nocturno":
		// Agenda de procedimientos: asuntos en rango 13-16, sin asuntos de consulta (1-12)
		hasProcedure := false
		for _, a := range asuntos {
			if a >= 1 && a <= 12 {
				return nil, nil // tiene asunto de consulta → no es agenda de procedimientos
			}
			if a >= 13 && a <= 16 {
				hasProcedure = true
			}
		}
		if hasProcedure {
			return &s, nil
		}
		return nil, nil

	default:
		// Consultas/exámenes: requiere al menos un asunto 1-12.
		// Agendas con solo asuntos 13-16 (procedimientos puros) quedan excluidas,
		// evitando que CUPS de consulta aparezcan en agendas de procedimientos.
		for _, a := range asuntos {
			if a >= 1 && a <= 12 {
				return &s, nil
			}
		}
		return nil, nil
	}
}

// FindBookedSlots retorna los timecodes (YYYYMMDDHHmm) de slots ocupados o bloqueados
// para una agenda en una fecha dada.
func (r *ScheduleRepo) FindBookedSlots(ctx context.Context, agendaID int, date string) ([]string, error) {
	query := `
	SELECT CONVERT(VARCHAR(8), Fecha, 112)
	       + REPLACE(CONVERT(VARCHAR(5), Fecha, 108), ':', '') AS timecode
	FROM programacion_medico_detalle
	WHERE IdProgramacionMedico = @p1
	  AND CAST(Fecha AS DATE) = @p2
	  AND (IdCita IS NOT NULL OR Bloqueado = 1 OR SinProgramacion = 1)`

	rows, err := r.db.QueryContext(ctx, query, agendaID, date)
	if err != nil {
		return nil, fmt.Errorf("FindBookedSlots agenda=%d date=%s: %w", agendaID, date, err)
	}
	defer rows.Close()

	var slots []string
	for rows.Next() {
		var tc string
		if err := rows.Scan(&tc); err != nil {
			return nil, err
		}
		slots = append(slots, strings.TrimSpace(tc))
	}
	return slots, rows.Err()
}

// FindWorkingDayException no aplica en SIESA (los slots ya están pre-generados).
func (r *ScheduleRepo) FindWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (*domain.WorkingDay, error) {
	return nil, nil
}

// UpdateWorkingDayExceptionDate no aplica en SIESA.
func (r *ScheduleRepo) UpdateWorkingDayExceptionDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (bool, error) {
	return false, nil
}

// DeleteWorkingDayException no aplica en SIESA.
func (r *ScheduleRepo) DeleteWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (bool, error) {
	return false, nil
}

// FindAsuntoForCups retorna el asunto SIESA para un CUPS consultando AsuntoPctos.
// Fallback 1: historial en citas_procedimientos (procedimientos/imágenes).
// Fallback 2: historial en citas_procedimientos_asuntos (consultas).
// Retorna 0 si no se encuentra.
func (r *ScheduleRepo) FindAsuntoForCups(ctx context.Context, cupsCode string) int {
	var asunto int
	// Fuente primaria: catálogo AsuntoPctos
	_ = r.db.QueryRowContext(ctx,
		`SELECT TOP 1 Asunto FROM AsuntoPctos WHERE CodProcedimiento = @p1`, cupsCode,
	).Scan(&asunto)
	if asunto > 0 {
		return asunto
	}
	// Fallback 1: historial de procedimientos (citas_procedimientos)
	_ = r.db.QueryRowContext(ctx, `
		SELECT TOP 1 c.asunto
		FROM citas c
		JOIN citas_procedimientos cp ON cp.id_cita = c.id
		WHERE LEFT(cp.id_procedimiento, @p2) = @p1
		  AND c.asunto IN (SELECT id FROM sis_asunto)
		  AND c.estado != 'C'
		ORDER BY c.fecha DESC`, cupsCode, len(cupsCode),
	).Scan(&asunto)
	if asunto > 0 {
		return asunto
	}
	// Fallback 2: historial de consultas (citas_procedimientos_asuntos)
	_ = r.db.QueryRowContext(ctx, `
		SELECT TOP 1 c.asunto
		FROM citas c
		JOIN citas_procedimientos_asuntos cpa ON cpa.IdCita = c.id
		WHERE cpa.CodProcedimiento = @p1
		  AND c.asunto IN (SELECT id FROM sis_asunto)
		  AND c.estado != 'C'
		ORDER BY c.fecha DESC`, cupsCode,
	).Scan(&asunto)
	return asunto
}

// addMinutesToHHMM suma N minutos a una hora "HH:mm" y devuelve el resultado como "HH:mm".
func addMinutesToHHMM(hhmm string, minutes int) string {
	if len(hhmm) < 5 {
		return hhmm
	}
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	total := h*60 + m + minutes
	if total >= 24*60 {
		total = 24*60 - 1
	}
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}
