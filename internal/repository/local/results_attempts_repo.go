package local

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

const (
	// resultsMaxFailures son los fallos de verificación que bloquean la consulta.
	resultsMaxFailures = 3
	// resultsAttemptsWindow es la ventana de conteo y también la duración del bloqueo.
	resultsAttemptsWindow = 24 * time.Hour
)

// ResultsAttemptsRepo guarda el tope persistente de intentos de verificación de «Resultados»
// (migración 050), por documento y por teléfono.
type ResultsAttemptsRepo struct {
	db *sql.DB
}

// NewResultsAttemptsRepo crea el repo sobre la BD local del bot.
func NewResultsAttemptsRepo(db *sql.DB) *ResultsAttemptsRepo {
	return &ResultsAttemptsRepo{db: db}
}

// HashDocument devuelve el SHA-256 en hex de «tipo:número»; el número nunca se guarda en claro.
func HashDocument(docType, number string) string {
	sum := sha256.Sum256([]byte(docType + ":" + number))
	return hex.EncodeToString(sum[:])
}

// attemptSubjects devuelve las claves a mirar; un teléfono vacío no cuenta.
func attemptSubjects(docHash, phone string) []string {
	s := []string{"doc:" + docHash}
	if phone != "" {
		s = append(s, "phone:"+phone)
	}
	return s
}

// IsLocked dice si el documento o el teléfono tienen un bloqueo vigente en `now`.
func (r *ResultsAttemptsRepo) IsLocked(ctx context.Context, docHash, phone string, now time.Time) (bool, error) {
	for _, subject := range attemptSubjects(docHash, phone) {
		var n int
		err := r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM results_verification_attempts WHERE subject = ? AND locked_until > ?`,
			subject, now).Scan(&n)
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// RecordFailure suma un fallo al documento y al teléfono. Si la ventana de uno pasó de 24 h,
// su conteo se reinicia; al llegar a 3 pone locked_until = now + 24 h. Devuelve true si
// cualquiera de los dos queda bloqueado.
func (r *ResultsAttemptsRepo) RecordFailure(ctx context.Context, docHash, phone string, now time.Time) (bool, error) {
	cutoff := now.Add(-resultsAttemptsWindow)
	lockUntil := now.Add(resultsAttemptsWindow)
	// MySQL evalúa las asignaciones de izquierda a derecha: failures ya vale el valor nuevo al
	// calcular locked_until, y window_start va después de failures porque este lo compara.
	const upsert = `INSERT INTO results_verification_attempts (subject, failures, window_start, locked_until)
		VALUES (?, 1, ?, NULL)
		ON DUPLICATE KEY UPDATE
		  failures = IF(window_start < ?, 1, failures + 1),
		  window_start = IF(window_start < ?, ?, window_start),
		  locked_until = IF(failures >= ?, ?, locked_until)`
	for _, subject := range attemptSubjects(docHash, phone) {
		if _, err := r.db.ExecContext(ctx, upsert,
			subject, now, cutoff, cutoff, now, resultsMaxFailures, lockUntil); err != nil {
			return false, err
		}
	}
	return r.IsLocked(ctx, docHash, phone, now)
}

// Reset borra el contador del documento (una verificación correcta). El del teléfono no se toca.
func (r *ResultsAttemptsRepo) Reset(ctx context.Context, docHash string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM results_verification_attempts WHERE subject = ?`, "doc:"+docHash)
	return err
}
