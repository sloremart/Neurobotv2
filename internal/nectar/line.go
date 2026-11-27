package nectar

import (
	"context"
	"log/slog"
	"sync"
)

// Line es la línea de Néctar por la que entra una conversación.
type Line string

const (
	// LineMain es la línea actual (y el valor de todo lo que no tiene registro).
	LineMain Line = "main"
	// LineLegacy es la línea vieja.
	LineLegacy Line = "legacy"
)

// LineStore persiste la línea de cada conversación.
type LineStore interface {
	// LineOf devuelve la línea registrada; false si la conversación no tiene registro.
	LineOf(ctx context.Context, conversationID int64) (Line, bool, error)
	// Register guarda la línea; si ya hay una, conserva la primera.
	Register(ctx context.Context, conversationID int64, l Line) error
}

// LineRegistry resuelve la línea de una conversación con caché en memoria.
type LineRegistry struct {
	store LineStore
	cache sync.Map // int64 -> Line; sin tope: una fila por conversación
}

// NewLineRegistry crea el registro sobre el store dado.
func NewLineRegistry(store LineStore) *LineRegistry {
	return &LineRegistry{store: store}
}

// Lookup devuelve la línea de la conversación: sin registro, LineMain; si el store no responde,
// el error (quien reparte envíos no debe suponer main: una conversación vieja saldría con la clave
// equivocada).
func (r *LineRegistry) Lookup(ctx context.Context, id int64) (Line, error) {
	if v, ok := r.cache.Load(id); ok {
		return v.(Line), nil
	}
	l, found, err := r.store.LineOf(ctx, id)
	if err != nil {
		return "", err
	}
	if !found {
		return LineMain, nil
	}
	r.cache.Store(id, l)
	return l, nil
}

// Line devuelve la línea de la conversación; sin registro o ante un error de lectura, LineMain.
func (r *LineRegistry) Line(ctx context.Context, id int64) Line {
	l, err := r.Lookup(ctx, id)
	if err != nil {
		slog.Warn("nectar: no se pudo leer la línea de la conversación, se asume main",
			"conversation_id", id, "error", err)
		return LineMain
	}
	return l
}

// Register guarda la línea y la deja en caché solo si el store lo aceptó. No relee el store:
// quien registra lo hace al ver la conversación por primera vez.
func (r *LineRegistry) Register(ctx context.Context, id int64, l Line) error {
	if err := r.store.Register(ctx, id, l); err != nil {
		return err
	}
	r.cache.Store(id, l)
	return nil
}

// Known dice si la línea ya está en memoria (no consulta el store).
func (r *LineRegistry) Known(id int64) bool {
	_, ok := r.cache.Load(id)
	return ok
}
