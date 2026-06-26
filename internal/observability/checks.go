package observability

import (
	"context"
	"database/sql"
	"time"
)

// scanIDs materializa una sola columna de ids (string) de un resultado.
func scanIDs(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// citaViolations construye violations para una lista de ids de cita (trace agrupado por check).
func citaViolations(check string, ids []string) []Violation {
	out := make([]Violation, 0, len(ids))
	for _, id := range ids {
		out = append(out, Violation{TraceID: "invariante:" + check, Reason: check, RefType: "cita", RefID: id})
	}
	return out
}

// OrphanSlotCheck (SIESA): cupos que siguen apuntando a una cita CANCELADA creada por el bot
// (cupo bloqueado e invisible para futuros pacientes — riesgo de N-42). Solo citas del bot
// (cod_user_asigna_cita = principal configurado o '000000' fallback), ventana últimos `days`.
// Verificado vs BD: 0 en baseline.
func OrphanSlotCheck(siesa *sql.DB, days int, botCedula string) Check {
	return func(ctx context.Context) ([]Violation, error) {
		if siesa == nil {
			return nil, nil
		}
		cutoff := time.Now().AddDate(0, 0, -days)
		rows, err := siesa.QueryContext(ctx, `
			SELECT DISTINCT CAST(c.id AS VARCHAR(20))
			FROM programacion_medico_detalle pmd WITH(NOLOCK)
			INNER JOIN citas c WITH(NOLOCK) ON c.id = pmd.IdCita
			WHERE c.estado = 'C' AND c.cod_user_asigna_cita IN (@p2, '000000')
			  AND pmd.Fecha >= @p1`, cutoff, botCedula)
		if err != nil {
			return nil, err
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return nil, err
		}
		return citaViolations("orphan_slot", ids), nil
	}
}

// ConsultaValorCeroCheck (SIESA): consultas del bot con Valor<=0 en CPA (TARIFA PENDIENTE
// silenciosa — la consulta debe tener precio al agendar). Solo citas del bot, ventana últimos
// `days`. Verificado vs BD: 0 en baseline con filtro bot.
func ConsultaValorCeroCheck(siesa *sql.DB, days int, botCedula string) Check {
	return func(ctx context.Context) ([]Violation, error) {
		if siesa == nil {
			return nil, nil
		}
		cutoff := time.Now().AddDate(0, 0, -days)
		rows, err := siesa.QueryContext(ctx, `
			SELECT DISTINCT CAST(c.id AS VARCHAR(20))
			FROM citas_procedimientos_asuntos cpa WITH(NOLOCK)
			INNER JOIN citas c WITH(NOLOCK) ON c.id = cpa.IdCita
			WHERE (cpa.Valor IS NULL OR cpa.Valor <= 0) AND c.estado <> 'C'
			  AND c.cod_user_asigna_cita IN (@p2, '000000') AND c.fecha >= @p1`, cutoff, botCedula)
		if err != nil {
			return nil, err
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return nil, err
		}
		return citaViolations("consulta_valor_cero", ids), nil
	}
}

// StuckWaitingListCheck (local): ofertas de lista de espera en estado 'notified' por más de
// `hours` (la limpieza ExpireStaleNotified(24h) debió liberarlas → si siguen ahí, está atascada).
// trace = wl:<id> para que aparezca en el recorrido de esa lista de espera.
func StuckWaitingListCheck(local *sql.DB, hours int) Check {
	return func(ctx context.Context) ([]Violation, error) {
		if local == nil {
			return nil, nil
		}
		cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
		rows, err := local.QueryContext(ctx,
			`SELECT id FROM waiting_list WHERE status = 'notified' AND notified_at < ?`, cutoff)
		if err != nil {
			return nil, err
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return nil, err
		}
		out := make([]Violation, 0, len(ids))
		for _, id := range ids {
			out = append(out, Violation{TraceID: "wl:" + id, Reason: "wl_stuck", RefType: "wl_entry", RefID: id})
		}
		return out, nil
	}
}

// ZombieEscalatedCheck (local): sesiones escaladas a agente cuyo expires_at ya pasó hace más de
// `graceHours` y siguen en estado 'escalated' (el inactivity checker debió marcarlas 'abandoned').
// trace = sess:<id>.
func ZombieEscalatedCheck(local *sql.DB, graceHours int) Check {
	return func(ctx context.Context) ([]Violation, error) {
		if local == nil {
			return nil, nil
		}
		cutoff := time.Now().Add(-time.Duration(graceHours) * time.Hour)
		rows, err := local.QueryContext(ctx,
			`SELECT id FROM sessions WHERE status = 'escalated' AND expires_at < ?`, cutoff)
		if err != nil {
			return nil, err
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return nil, err
		}
		out := make([]Violation, 0, len(ids))
		for _, id := range ids {
			out = append(out, Violation{TraceID: "sess:" + id, Reason: "zombie_escalated", RefType: "session", RefID: id})
		}
		return out, nil
	}
}
