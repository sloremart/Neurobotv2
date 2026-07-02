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

// GetAsuntosByAgenda returns the distinct SIESA subjects (IdAsunto) that an ACTIVE agenda serves,
// via its consultorio relation. agendaID = programacion_medico.id (= domain Appointment.AgendaID).
// Se une por `pmr.id_programacion = pm.id_programacion` (NO `pm.id`) — misma nota de corrección que
// FindAvailableSlots (id e id_programacion divergen en ~64% de agendas).
func (r *ScheduleRepo) GetAsuntosByAgenda(ctx context.Context, agendaID int) ([]int, error) {
	const q = `SELECT DISTINCT pmra.IdAsunto
FROM programacion_medico pm WITH (NOLOCK)
JOIN programacion_medico_relacion pmr WITH (NOLOCK)
    ON pmr.id_programacion = pm.id_programacion
JOIN programacion_medico_relacion_asunto pmra WITH (NOLOCK)
    ON pmra.IdProgramacionMedicoRelacion = pmr.Id
WHERE pm.id = @p1 AND pm.activo = 1`
	rows, err := r.db.QueryContext(ctx, q, agendaID)
	if err != nil {
		return nil, fmt.Errorf("get asuntos by agenda: %w", err)
	}
	defer func() { _ = rows.Close() }()
	asuntos := make([]int, 0, 4)
	for rows.Next() {
		var a int
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		asuntos = append(asuntos, a)
	}
	return asuntos, rows.Err()
}

// FindAvailableSlots returns every free SIESA slot for agendas that serve the given
// subject (asunto_id), within the 3h..90-day window. Each row is one bookable slot.
//
// Agenda eligibility = agendas whose programacion_medico_relacion (el CONSULTORIO elegido al
// crear la agenda en SIESA) declara el @asuntoId en programacion_medico_relacion_asunto.
//
// ⚠️ CLAVE (validado contra BD): la relación se une por `pmr.id_programacion = pm.id_programacion`,
// NO por `pm.id`. En programacion_medico, `id` (PK del detalle/citas) e `id_programacion`
// DIVERGEN en el 64% de las agendas (349/544). Unir por `pm.id` agarra la relación de OTRA
// agenda vecina → asunto equivocado. Con el join correcto, el médico real atiende el asunto en
// el 100% de las agendas (532/532 vía sis_asuntoMedico) y coincide con el consultorio.
// NO se usa el historial de citas como fallback: con el join correcto hay 0 agendas huérfanas,
// y `citas.asunto` histórico es poco fiable (lo llenó el bot con un catálogo viejo).
//
// The 3h floor and 90-day ceiling are range predicates on pmd.Fecha (no CAST on the
// column) so the datetime index is preserved. afterDate (YYYY-MM-DD) paginates forward.
func (r *ScheduleRepo) FindAvailableSlots(ctx context.Context, asuntoID int, afterDate string, allowedDoctors []int) ([]domain.AvailableSlotRow, error) {
	var sb strings.Builder
	args := []interface{}{asuntoID}

	sb.WriteString(`
WITH eligible AS (
    SELECT pm.id AS aid
    FROM programacion_medico pm WITH (NOLOCK)
    JOIN programacion_medico_relacion pmr WITH (NOLOCK)
        ON pmr.id_programacion = pm.id_programacion
    JOIN programacion_medico_relacion_asunto pmra WITH (NOLOCK)
        ON pmra.IdProgramacionMedicoRelacion = pmr.Id
    WHERE pm.activo = 1 AND pmra.IdAsunto = @p1
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
  -- Consistencia con PK_citas(cod_medi,fecha,hora,meridiano,estado): no ofrecer un médico+hora
  -- que ya tiene una cita activa 'P'. Sin esto, un slot puede verse libre por su fila de detalle
  -- (IdCita IS NULL) mientras el médico ya está ocupado a esa hora en OTRA fila/agenda → el INSERT
  -- en citas colisionaría con la PK. El seek usa el prefijo de PK_citas (cod_medi -> fecha -> hora).
  AND NOT EXISTS (
      SELECT 1 FROM citas c WITH (NOLOCK)
      WHERE c.cod_medi = pmd.Medico
        AND c.fecha    = CAST(pmd.Fecha AS DATE)
        AND c.hora     = CONVERT(VARCHAR(5), pmd.Fecha, 108)
        AND c.estado   = 'P'
  )
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

	// Restricción por médicos que realizan ESTE CUPS (cups_medico). El médico del slot
	// (pmd.Medico = sis_medi.codigo) debe estar en la lista. Cierra el hueco de granularidad de
	// sis_asuntoMedico (asunto compartido por varios médicos que hacen procedimientos distintos).
	// Lista vacía = fail-open (no restringir): lo decide el llamador antes de invocar.
	if len(allowedDoctors) > 0 {
		sb.WriteString(` AND pmd.Medico IN (`)
		for i, doc := range allowedDoctors {
			if i > 0 {
				sb.WriteString(`, `)
			}
			args = append(args, doc)
			fmt.Fprintf(&sb, `@p%d`, len(args))
		}
		sb.WriteString(`)`)
	}

	if afterDate != "" {
		args = append(args, afterDate)
		fmt.Fprintf(&sb, ` AND pmd.Fecha >= DATEADD(DAY, 1, CAST(@p%d AS DATE))`, len(args))
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
	// Se trae también id_programacion: la relación (consultorio/asuntos) se une por
	// pm.id_programacion, NO por pm.id (divergen en el 64% de las agendas). Ver nota en
	// FindAvailableSlots.
	var s domain.Schedule
	var idProgramacion int
	err := r.db.QueryRowContext(ctx,
		`SELECT pm.id, pm.id_programacion, CAST(ISNULL(pm.id_medico, 0) AS VARCHAR(20))
		 FROM programacion_medico pm WITH (NOLOCK)
		 WHERE pm.id = @p1 AND pm.activo = 1`,
		scheduleID).Scan(&s.ID, &idProgramacion, &s.DoctorDocument)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("FindByScheduleID %d: %w", scheduleID, err)
	}

	if scheduleType == "" {
		return &s, nil
	}

	// Asuntos de esta agenda via programacion_medico_relacion_asunto (join por id_programacion).
	subjectRows, err := r.db.QueryContext(ctx,
		`SELECT pmra.IdAsunto
		 FROM programacion_medico_relacion pmr WITH (NOLOCK)
		 JOIN programacion_medico_relacion_asunto pmra WITH (NOLOCK) ON pmra.IdProgramacionMedicoRelacion = pmr.Id
		 WHERE pmr.id_programacion = @p1`,
		idProgramacion)
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

	// Con el join correcto no hay agendas huérfanas (toda agenda activa tiene relacion_asunto).
	// Si excepcionalmente no hubiera asuntos, no se asume tipo desde el historial (poco fiable):
	// se rechaza para no ofrecer un tipo equivocado.
	if len(subjectTypes) == 0 {
		return nil, nil
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
