package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

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
