package observability

import (
	"context"
	"log/slog"
)

// Violation es una infracción de invariante detectada por un Check (§4 de docs/OBSERVABILIDAD.md).
type Violation struct {
	TraceID string                 // correlación: "wl:<id>" / "sess:<id>" / "invariante:<check>"
	Reason  string                 // nombre del check (queda como reason de la anomalía)
	RefType string                 // "slot" | "cita" | "wl_entry" | "session"
	RefID   string                 // id del artefacto en infracción
	Attrs   map[string]interface{} // detalle mínimo (sin PII)
}

// Check ejecuta una verificación de invariante sobre una ventana reciente y devuelve infracciones.
type Check func(ctx context.Context) ([]Violation, error)

// Reconciler corre un conjunto de Checks y emite las infracciones como anomalías consultables
// (flow=invariante, level=error) y las loguea a nivel Error (→ alerta a Telegram con su trace_id).
// Pensado para correr 1×/día (dentro de data_cleanup 02:00), NO cada hora: SIESA es sensible a
// contención con la UI. Las queries van con WITH(NOLOCK) y acotadas por fecha.
type Reconciler struct {
	checks map[string]Check
}

// NewReconciler crea un Reconciler vacío.
func NewReconciler() *Reconciler {
	return &Reconciler{checks: make(map[string]Check)}
}

// Register agrega un check con su nombre (no-op si c es nil).
func (r *Reconciler) Register(name string, c Check) {
	if c != nil {
		r.checks[name] = c
	}
}

// Run ejecuta todos los checks y emite las anomalías encontradas.
func (r *Reconciler) Run(ctx context.Context) {
	for name, c := range r.checks {
		violations, err := c(ctx)
		if err != nil {
			slog.Error("reconcile check failed", "check", name, "error", err)
			continue
		}
		for _, v := range violations {
			Emit(v.TraceID, "invariante", "anomaly", EmitOpts{
				Outcome: "error", Reason: v.Reason, RefType: v.RefType, RefID: v.RefID, Attrs: v.Attrs,
			})
		}
		if len(violations) > 0 {
			// Error level → el AlertHandler lo envía a Telegram, con el trace_id para investigar.
			slog.Error("reconcile anomalies detected",
				"check", name, "count", len(violations), "trace_id", "invariante:"+name)
		}
	}
}
