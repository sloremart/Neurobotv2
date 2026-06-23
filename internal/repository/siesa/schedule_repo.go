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
// AgendaID en el dominio = programacion_medico.id (= IdProgramacionMedico).
type ScheduleRepo struct {
	db *sql.DB
}

func NewScheduleRepo(db *sql.DB) *ScheduleRepo {
	return &ScheduleRepo{db: db}
}

// FindAvailableSlots returns every free SIESA slot for agendas that serve the given
// subject (asunto_id), within the 3h..90-day window. Each row is one bookable slot.
//
// Agenda eligibility is the UNION of two sets (validated against real SIESA data):
//  1. Agendas with an explicit programacion_medico_relacion_asunto row for @asuntoId.
//  2. "Orphan" agendas that have NO relacion_asunto rows at all (≈10 of 55 active
//     agendas) whose most recent non-cancelled cita has asunto = @asuntoId. Without
//     this fallback an INNER JOIN would silently drop those agendas (RX, resonancia,
//     EEG, procedures, etc.).
//
// The 3h floor and 90-day ceiling are range predicates on pmd.Fecha (no CAST on the
// column) so the datetime index is preserved. afterDate (YYYY-MM-DD) paginates forward.
func (r *ScheduleRepo) FindAvailableSlots(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
	var sb strings.Builder
	args := []interface{}{asuntoID}

	sb.WriteString(`
WITH eligible AS (
    SELECT pmr.id_programacion AS aid
    FROM programacion_medico_relacion pmr WITH (NOLOCK)
    JOIN programacion_medico_relacion_asunto pmra WITH (NOLOCK)
        ON pmra.IdProgramacionMedicoRelacion = pmr.Id
    WHERE pmra.IdAsunto = @p1
    UNION
    SELECT pm.id AS aid
    FROM programacion_medico pm WITH (NOLOCK)
    WHERE pm.activo = 1
      AND NOT EXISTS (
          SELECT 1 FROM programacion_medico_relacion pmr2 WITH (NOLOCK)
          JOIN programacion_medico_relacion_asunto pmra2 WITH (NOLOCK)
              ON pmra2.IdProgramacionMedicoRelacion = pmr2.Id
          WHERE pmr2.id_programacion = pm.id)
      AND (
          SELECT TOP 1 c.asunto FROM citas c WITH (NOLOCK)
          WHERE c.id_programacion = pm.id AND c.asunto > 0 AND c.estado <> 'C'
          ORDER BY c.fecha DESC) = @p1
)
SELECT
    pmd.Fecha,
    CAST(m.cedula AS VARCHAR(20)),
    RTRIM(m.nombre),
    CAST(m.codigo AS VARCHAR(20)),
    pm.id,
    ISNULL(NULLIF(pm.intervalo, 0), 30),
    pm.id_sede
FROM programacion_medico_detalle pmd WITH (NOLOCK)
JOIN programacion_medico pm WITH (NOLOCK) ON pm.id = pmd.IdProgramacionMedico AND pm.activo = 1
JOIN eligible e ON e.aid = pm.id
JOIN sis_medi m WITH (NOLOCK) ON m.codigo = pmd.Medico
WHERE pmd.IdCita IS NULL
  AND pmd.Bloqueado = 0
  AND pmd.SinProgramacion = 0
  AND pmd.Fecha >= DATEADD(HOUR, 3, GETDATE())
  AND pmd.Fecha <= DATEADD(DAY, 90, GETDATE())
  -- Doble validación (agenda Y médico deben coincidir en el asunto): el médico del slot
  -- debe atender el asunto buscado según sis_asuntoMedico. Esto descarta agendas mal
  -- configuradas (asunto asignado a una agenda cuyo médico no hace ese asunto). Fail-open:
  -- si el médico no está catalogado en sis_asuntoMedico, no se filtra (no ocultar cupos).
  AND (
      EXISTS (SELECT 1 FROM sis_asuntoMedico sam WITH (NOLOCK)
              WHERE sam.Medico = pmd.Medico AND sam.Asunto = @p1)
      OR NOT EXISTS (SELECT 1 FROM sis_asuntoMedico sam2 WITH (NOLOCK)
              WHERE sam2.Medico = pmd.Medico)
  )`)

	if afterDate != "" {
		sb.WriteString(` AND pmd.Fecha >= DATEADD(DAY, 1, CAST(@p2 AS DATE))`)
		args = append(args, afterDate)
	}
	sb.WriteString(` ORDER BY pmd.Fecha`)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("FindAvailableSlots asunto=%d: %w", asuntoID, err)
	}
	defer rows.Close()

	var slots []domain.AvailableSlotRow
	for rows.Next() {
		var s domain.AvailableSlotRow
		var fecha time.Time
		if err := rows.Scan(&fecha, &s.DoctorDocument, &s.DoctorName, &s.DoctorSiesaCode,
			&s.AgendaID, &s.DurationMin, &s.AgendaSede); err != nil {
			return nil, err
		}
		s.SlotTime = fecha
		slots = append(slots, s)
	}
	return slots, rows.Err()
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
		 FROM programacion_medico pm WITH (NOLOCK)
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
	subjectRows, err := r.db.QueryContext(ctx,
		`SELECT pmra.IdAsunto
		 FROM programacion_medico_relacion pmr WITH (NOLOCK)
		 JOIN programacion_medico_relacion_asunto pmra WITH (NOLOCK) ON pmra.IdProgramacionMedicoRelacion = pmr.Id
		 WHERE pmr.id_programacion = @p1`,
		scheduleID)
	if err != nil {
		return nil, fmt.Errorf("FindByScheduleID asuntos %d: %w", scheduleID, err)
	}
	defer subjectRows.Close()

	var subjectTypes []int
	for subjectRows.Next() {
		var a int
		if err := subjectRows.Scan(&a); err != nil {
			return nil, err
		}
		subjectTypes = append(subjectTypes, a)
	}
	if err := subjectRows.Err(); err != nil {
		return nil, err
	}

	st := strings.ToLower(scheduleType)

	// Agenda sin asuntos configurados en programacion_medico_relacion_asunto:
	// consultar el asunto más reciente de citas reales para determinar el tipo real.
	// Agenda 254 (resonancias) tiene asunto=4 en citas → tipo consulta → se permite correctamente.
	// Agendas de procedimientos (asuntos 13-16 en citas) quedan excluidas de búsquedas de consulta.
	if len(subjectTypes) == 0 {
		var lastAsunto int
		_ = r.db.QueryRowContext(ctx, `
			SELECT TOP 1 asunto FROM citas WITH (NOLOCK)
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
		for _, a := range subjectTypes {
			if a == 17 {
				return &s, nil
			}
		}
		return nil, nil

	case "procedimiento", "nocturno":
		// Agenda de procedimientos: asuntos en rango 13-16, sin asuntos de consulta (1-12)
		hasProcedure := false
		for _, a := range subjectTypes {
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
		for _, a := range subjectTypes {
			if a >= 1 && a <= 12 {
				return &s, nil
			}
		}
		return nil, nil
	}
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
