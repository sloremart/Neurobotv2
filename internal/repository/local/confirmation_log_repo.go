package local

import (
	"context"
	"database/sql"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// ConfirmationLogRepo persiste eventos de confirmación en la tabla local confirmation_log.
// Reemplaza los campos estructurados que tenía Antares (MedioConfirmacion, IdMedioConfirmacion)
// que no existen en la tabla citas de SIESA.
type ConfirmationLogRepo struct {
	db *sql.DB
}

func NewConfirmationLogRepo(db *sql.DB) *ConfirmationLogRepo {
	return &ConfirmationLogRepo{db: db}
}

// InsertBatch escribe una fila por entrada. Los fallos no son fatales — el llamador debe loguear y continuar.
func (r *ConfirmationLogRepo) InsertBatch(ctx context.Context, entries []domain.ConfirmationEntry) error {
	if len(entries) == 0 {
		return nil
	}
	placeholders := make([]string, len(entries))
	args := make([]interface{}, 0, len(entries)*5)
	for i, e := range entries {
		placeholders[i] = "(?, ?, ?, ?, ?)"
		args = append(args, e.AppointmentID, e.PatientID, e.ConfirmedAt, e.Channel, e.ChannelID)
	}
	query := "INSERT INTO confirmation_log (appointment_id, patient_id, confirmed_at, channel, channel_id) VALUES " +
		strings.Join(placeholders, ", ")
	_, err := r.db.ExecContext(ctx, query, args...)
	return err
}
