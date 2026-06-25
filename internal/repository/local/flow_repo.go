package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// FlowRepo persiste y consulta la traza de flujos (flow_events). FlowEvent vive en domain.
type FlowRepo struct {
	db *sql.DB
}

// NewFlowRepo creates a new FlowRepo.
func NewFlowRepo(db *sql.DB) *FlowRepo {
	return &FlowRepo{db: db}
}

// InsertBatch persiste varios eventos en una sola transacción.
func (r *FlowRepo) InsertBatch(ctx context.Context, events []domain.FlowEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("flow_events begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO flow_events
		   (trace_id, flow, step, level, outcome, reason, phone, ref_type, ref_id, attrs, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("flow_events prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range events {
		e := &events[i]
		var attrsJSON interface{} // NULL si no hay attrs
		if len(e.Attrs) > 0 {
			if b, mErr := json.Marshal(e.Attrs); mErr == nil {
				attrsJSON = string(b)
			}
		}
		if _, err := stmt.ExecContext(ctx,
			e.TraceID, e.Flow, e.Step, e.Level, e.Outcome,
			nullString(e.Reason), nullString(e.Phone), nullString(e.RefType), nullString(e.RefID),
			attrsJSON, e.CreatedAt); err != nil {
			return fmt.Errorf("flow_events insert: %w", err)
		}
	}
	return tx.Commit()
}

// FindByTrace devuelve el timeline ordenado de un recorrido.
func (r *FlowRepo) FindByTrace(ctx context.Context, traceID string) ([]domain.FlowEvent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, trace_id, flow, step, level, outcome,
		        COALESCE(reason,''), COALESCE(phone,''), COALESCE(ref_type,''), COALESCE(ref_id,''),
		        COALESCE(attrs,''), created_at
		 FROM flow_events
		 WHERE trace_id = ?
		 ORDER BY created_at ASC, id ASC`, traceID)
	if err != nil {
		return nil, fmt.Errorf("flow_events find by trace: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanFlowRows(rows)
}

// FindByFilter consulta eventos por tipo, SIEMPRE acotada por la ventana [from, to). flow/outcome/
// reason vacíos = sin filtrar por ese campo. limit acota el resultado (default 200, máx 1000).
func (r *FlowRepo) FindByFilter(ctx context.Context, flow, outcome, reason string, from, to time.Time, limit int) ([]domain.FlowEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	where := "created_at >= ? AND created_at < ?"
	args := []interface{}{from, to}
	if flow != "" {
		where += " AND flow = ?"
		args = append(args, flow)
	}
	if outcome != "" {
		where += " AND outcome = ?"
		args = append(args, outcome)
	}
	if reason != "" {
		where += " AND reason = ?"
		args = append(args, reason)
	}
	args = append(args, limit)

	query := fmt.Sprintf(`SELECT id, trace_id, flow, step, level, outcome,
	        COALESCE(reason,''), COALESCE(phone,''), COALESCE(ref_type,''), COALESCE(ref_id,''),
	        COALESCE(attrs,''), created_at
	 FROM flow_events WHERE %s ORDER BY created_at DESC, id DESC LIMIT ?`, where)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("flow_events find by filter: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanFlowRows(rows)
}

// Stats agrega un flujo en [from, to): funnel por step, distribución por outcome y conteo por
// reason. flow vacío = todos los flujos.
func (r *FlowRepo) Stats(ctx context.Context, flow string, from, to time.Time) (*domain.FlowStats, error) {
	out := &domain.FlowStats{}
	var err error
	if out.ByStep, err = r.groupCount(ctx, "step", flow, from, to); err != nil {
		return nil, err
	}
	if out.ByOutcome, err = r.groupCount(ctx, "outcome", flow, from, to); err != nil {
		return nil, err
	}
	if out.ByReason, err = r.groupCount(ctx, "reason", flow, from, to); err != nil {
		return nil, err
	}
	return out, nil
}

// groupCount agrupa por una columna FIJA (step|outcome|reason — identificador interno, no input
// de usuario) sobre la ventana temporal.
func (r *FlowRepo) groupCount(ctx context.Context, col, flow string, from, to time.Time) ([]domain.FlowStatCount, error) {
	if col != "step" && col != "outcome" && col != "reason" {
		return nil, fmt.Errorf("flow_events stats: invalid column %q", col)
	}
	where := "created_at >= ? AND created_at < ?"
	args := []interface{}{from, to}
	if flow != "" {
		where += " AND flow = ?"
		args = append(args, flow)
	}
	if col == "reason" {
		where += " AND reason IS NOT NULL AND reason <> ''"
	}
	//nolint:gosec // G201: `col` es un valor interno validado arriba (no input de usuario).
	query := fmt.Sprintf("SELECT %s, COUNT(*) FROM flow_events WHERE %s GROUP BY %s ORDER BY COUNT(*) DESC", col, where, col)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("flow_events stats %s: %w", col, err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.FlowStatCount
	for rows.Next() {
		var c domain.FlowStatCount
		var key sql.NullString
		if err := rows.Scan(&key, &c.Count); err != nil {
			return nil, fmt.Errorf("flow_events stats scan: %w", err)
		}
		c.Key = key.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// RollupDay agrega los flow_events de `day` en flow_daily_stats (idempotente). Para correr de noche
// sobre el día anterior → los agregados sobreviven a la purga de los crudos.
func (r *FlowRepo) RollupDay(ctx context.Context, day time.Time) (int64, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	end := start.AddDate(0, 0, 1)
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO flow_daily_stats (day, flow, step, outcome, reason, cnt)
		SELECT DATE(created_at), flow, step, outcome, COALESCE(reason,''), COUNT(*)
		FROM flow_events
		WHERE created_at >= ? AND created_at < ?
		GROUP BY DATE(created_at), flow, step, outcome, COALESCE(reason,'')
		ON DUPLICATE KEY UPDATE cnt = VALUES(cnt)`, start, end)
	if err != nil {
		return 0, fmt.Errorf("flow rollup: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeOlderThan borra flow_events con más de `days` días (retención de crudos; el rollup conserva
// los agregados de largo plazo en flow_daily_stats).
func (r *FlowRepo) PurgeOlderThan(ctx context.Context, days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days)
	res, err := r.db.ExecContext(ctx, `DELETE FROM flow_events WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("flow purge: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// scanFlowRows materializa las filas (columnas en el orden de los SELECT de este repo).
func scanFlowRows(rows *sql.Rows) ([]domain.FlowEvent, error) {
	var out []domain.FlowEvent
	for rows.Next() {
		var e domain.FlowEvent
		var attrsJSON string
		if err := rows.Scan(&e.ID, &e.TraceID, &e.Flow, &e.Step, &e.Level, &e.Outcome,
			&e.Reason, &e.Phone, &e.RefType, &e.RefID, &attrsJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("flow_events scan: %w", err)
		}
		if attrsJSON != "" && attrsJSON != "{}" {
			_ = json.Unmarshal([]byte(attrsJSON), &e.Attrs)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
