// Package local implementa los repositorios respaldados por la BD local del bot (neuro_bot, MySQL).
package local

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AIRecoveryCounterRepo implementa el tope mensual de recuperaciones asistidas por IA
// (AI_RECOVERY_MONTHLY_LIMIT). Cuenta por mes calendario en la BD local del bot. Satisface la
// interfaz recovery.MonthlyCounter sin importarla (duck typing).
type AIRecoveryCounterRepo struct {
	db    *sql.DB
	limit int
}

// NewAIRecoveryCounterRepo crea el contador. limit<=0 = sin tope (TryReserve siempre permite).
func NewAIRecoveryCounterRepo(db *sql.DB, limit int) *AIRecoveryCounterRepo {
	return &AIRecoveryCounterRepo{db: db, limit: limit}
}

// TryReserve incrementa el contador del mes actual y reporta si aún hay cupo. Lee primero para no
// escribir sin fin una vez alcanzado el tope (se tolera una carrera menor: es un tope suave). Ante
// error de BD hace fail-open (retorna err; el coordinador entonces NO bloquea la recuperación).
func (r *AIRecoveryCounterRepo) TryReserve(ctx context.Context) (bool, error) {
	if r.limit <= 0 {
		return true, nil // sin tope
	}
	period := time.Now().Format("2006-01")

	var cur int
	err := r.db.QueryRowContext(ctx, "SELECT count FROM ai_recovery_monthly WHERE period = ?", period).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return true, err // fail-open
	}
	if cur >= r.limit {
		return false, nil // tope alcanzado
	}
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO ai_recovery_monthly (period, count) VALUES (?, 1) ON DUPLICATE KEY UPDATE count = count + 1",
		period,
	); err != nil {
		return true, err // fail-open
	}
	return true, nil
}
