package notifications

import (
	"context"
	"time"
)

// Respuesta al recordatorio pendiente de SIESA (D-04). Si el paciente pulsa «Confirmar» o
// «Cancelar» y SIESA no responde, la respuesta queda marcada en su fila de notification_history
// (migración 047) y un proceso de fondo la aplica cuando SIESA vuelve.

// SiesaPendingRow es una respuesta al recordatorio pendiente de aplicar en SIESA.
type SiesaPendingRow struct {
	ID             int64 // id de la fila de notification_history
	Phone          string
	Type           string
	AppointmentID  string
	BirdMessageID  string
	ConversationID string
	ContactPhone   string
	Action         string        // "confirm" | "cancel"
	Attempts       int           // intentos fallidos del proceso de fondo
	PendingFor     time.Duration // tiempo desde que quedó pendiente (medido en la BD)
}

// SiesaPendingStore guarda las respuestas pendientes de SIESA (implementado por local.NotificationRepo).
type SiesaPendingStore interface {
	// MarkSiesaPending marca como pendiente la última fila del historial de ese teléfono y cita, con
	// la conversación si llega; si no hay fila (el Resolve falló) inserta una con status.
	MarkSiesaPending(ctx context.Context, row SiesaPendingRow, status string) error
	// FindSiesaPending devuelve hasta limit respuestas pendientes, la más vieja primero.
	FindSiesaPending(ctx context.Context, limit int) ([]SiesaPendingRow, error)
	// RecordSiesaAttempt suma un intento fallido a una respuesta que sigue pendiente.
	RecordSiesaAttempt(ctx context.Context, id int64) error
	// FinishSiesaPending saca la respuesta de pendientes con su desenlace. Devuelve false si ya no
	// estaba pendiente.
	FinishSiesaPending(ctx context.Context, id int64, outcome string) (bool, error)
}
