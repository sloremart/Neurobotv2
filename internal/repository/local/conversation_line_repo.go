package local

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// ConversationLineRepo guarda la línea de Néctar de cada conversación (migración 049).
type ConversationLineRepo struct {
	db *sql.DB
}

// NewConversationLineRepo crea el repo sobre la BD local del bot.
func NewConversationLineRepo(db *sql.DB) *ConversationLineRepo {
	return &ConversationLineRepo{db: db}
}

var _ nectar.LineStore = (*ConversationLineRepo)(nil)

// LineOf devuelve la línea registrada; false si la conversación no tiene fila.
func (r *ConversationLineRepo) LineOf(ctx context.Context, conversationID int64) (nectar.Line, bool, error) {
	var line string
	err := r.db.QueryRowContext(ctx,
		`SELECT line FROM nectar_conversation_line WHERE conversation_id = ?`, conversationID).Scan(&line)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return nectar.Line(line), true, nil
}

// Register inserta la línea; si la conversación ya tenía otra, conserva la primera (una
// conversación nunca cambia de canal) y lo deja en el log.
func (r *ConversationLineRepo) Register(ctx context.Context, conversationID int64, l nectar.Line) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT IGNORE INTO nectar_conversation_line (conversation_id, line) VALUES (?, ?)`,
		conversationID, string(l))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return err
	}
	existing, found, err := r.LineOf(ctx, conversationID)
	if err != nil {
		return err
	}
	if found && existing != l {
		slog.Error("nectar: la conversación ya tenía otra línea, se conserva la primera",
			"conversation_id", conversationID, "stored", existing, "attempted", l)
	}
	return nil
}
