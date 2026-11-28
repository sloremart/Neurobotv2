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

// RecordFailure suma un fallo (o reserva un intento) al documento y al teléfono, en una sola
// transacción. Si la ventana de uno pasó de 24 h, su conteo se reinicia; al llegar a 3 pone
// locked_until = now + 24 h. Con un bloqueo vigente el conteo sigue subiendo pero locked_until no
// se mueve (el bloqueo no se desliza). Devuelve attempt = el mayor conteo de las dos filas tras
// sumar (el que lee cada transacción es el suyo, aunque haya sesiones concurrentes) y locked =
// alguna de las dos queda bloqueada.
func (r *ResultsAttemptsRepo) RecordFailure(ctx context.Context, docHash, phone string, now time.Time) (attempt int, locked bool, err error) {
	cutoff := now.Add(-resultsAttemptsWindow)
	lockUntil := now.Add(resultsAttemptsWindow)
	// MySQL evalúa las asignaciones de izquierda a derecha: failures y window_start leen el
	// locked_until viejo (aún no asignado) y locked_until lee el failures nuevo.
	const upsert = `INSERT INTO results_verification_attempts (subject, failures, window_start, locked_until)
		VALUES (?, 1, ?, NULL)
		ON DUPLICATE KEY UPDATE
		  failures = IF(locked_until > ?, failures + 1, IF(window_start < ?, 1, failures + 1)),
		  window_start = IF(locked_until > ?, window_start, IF(window_start < ?, ?, window_start)),
		  locked_until = IF(locked_until > ?, locked_until, IF(failures >= ?, ?, locked_until))`
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Orden fijo (documento, luego teléfono): dos transacciones nunca se esperan en cruz.
	for _, subject := range attemptSubjects(docHash, phone) {
		if _, err := tx.ExecContext(ctx, upsert,
			subject, now, now, cutoff, now, cutoff, now, now, resultsMaxFailures, lockUntil); err != nil {
			return 0, false, err
		}
		// La fila quedó bloqueada por el upsert: este SELECT ve el valor de esta transacción.
		var failures int
		var lockedUntil sql.NullTime
		if err := tx.QueryRowContext(ctx,
			`SELECT failures, locked_until FROM results_verification_attempts WHERE subject = ?`,
			subject).Scan(&failures, &lockedUntil); err != nil {
			return 0, false, err
		}
		attempt = max(attempt, failures)
		if lockedUntil.Valid && lockedUntil.Time.After(now) {
			locked = true
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return attempt, locked, nil
}

// Release devuelve el intento reservado en el teléfono tras una verificación correcta: resta un
// fallo (nunca por debajo de 0) y, si queda por debajo del tope, quita el bloqueo. Un teléfono
// vacío no hace nada.
func (r *ResultsAttemptsRepo) Release(ctx context.Context, phone string, now time.Time) error {
	if phone == "" {
		return nil
	}
	// failures se asigna primero: locked_until lee el valor ya restado.
	_, err := r.db.ExecContext(ctx,
		`UPDATE results_verification_attempts
		 SET failures = GREATEST(failures - 1, 0),
		     locked_until = IF(failures < ?, NULL, locked_until)
		 WHERE subject = ? AND window_start >= ?`,
		resultsMaxFailures, "phone:"+phone, now.Add(-resultsAttemptsWindow))
	return err
}

// Reset borra el contador del documento (una verificación correcta). El del teléfono no se toca.
func (r *ResultsAttemptsRepo) Reset(ctx context.Context, docHash string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM results_verification_attempts WHERE subject = ?`, "doc:"+docHash)
	return err
}
