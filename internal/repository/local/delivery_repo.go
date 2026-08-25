package local

import (
	"context"
	"database/sql"
	"fmt"
)

// DeliveryRepo registra los fallos de ENTREGA de WhatsApp por teléfono (webhook outbound de
// Bird). Es la memoria que permite suprimir templates a números sin WhatsApp: cada envío a un
// número muerto se cobra y jamás llega.
type DeliveryRepo struct {
	db *sql.DB
}

// NewDeliveryRepo construye el repo sobre la BD local del bot.
func NewDeliveryRepo(db *sql.DB) *DeliveryRepo {
	return &DeliveryRepo{db: db}
}

// RecordFailure incrementa el contador de fallos consecutivos del teléfono.
func (r *DeliveryRepo) RecordFailure(ctx context.Context, phone, status string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO message_delivery_failures (phone_number, consecutive_failures, last_status, last_failure_at)
		 VALUES (?, 1, ?, NOW())
		 ON DUPLICATE KEY UPDATE consecutive_failures = consecutive_failures + 1,
		                         last_status = VALUES(last_status), last_failure_at = NOW()`,
		phone, status)
	if err != nil {
		return fmt.Errorf("record delivery failure: %w", err)
	}
	return nil
}

// RecordSuccess resetea el contador. Dos disparadores, y los DOS hacen falta: el delivered/read de
// un saliente, y un mensaje ENTRANTE del paciente (webhook_handler.recordInboundDeliveryProof) —
// que nos escriba prueba que su WhatsApp funciona y no cuesta ningún envío. Sin el segundo, un
// teléfono suprimido no vuelve nunca: al suprimirlo dejan de salir templates, así que ya no puede
// llegar ningún delivered que lo rescate.
// UPDATE puro: si el teléfono nunca falló no crea fila (la tabla solo guarda problemáticos).
func (r *DeliveryRepo) RecordSuccess(ctx context.Context, phone string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE message_delivery_failures SET consecutive_failures = 0, last_status = 'recovered'
		 WHERE phone_number = ? AND consecutive_failures > 0`, phone)
	if err != nil {
		return fmt.Errorf("record delivery success: %w", err)
	}
	return nil
}

// ConsecutiveFailures devuelve el contador actual del teléfono (0 si nunca falló).
func (r *DeliveryRepo) ConsecutiveFailures(ctx context.Context, phone string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT consecutive_failures FROM message_delivery_failures WHERE phone_number = ?", phone).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("consecutive failures: %w", err)
	}
	return n, nil
}
