// Package fakeurgent es un aviso urgente de prueba (monitor.UrgentAlerter). Va aparte de testutil por
// la misma razón que fakenectar: desde los tests de notifications, testutil sería un ciclo.
package fakeurgent

import (
	"context"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/monitor"
)

// Call es una llamada registrada por Recorder: Fire (Threshold 0) o Count.
type Call struct {
	Trigger   monitor.UrgentTrigger
	Detail    string
	Threshold int
	Window    time.Duration
}

// Recorder es un aviso urgente falso: guarda cada Fire y cada Count, sin enfriamiento.
// Seguro para goroutines (los lotes administrativos corren en segundo plano).
type Recorder struct {
	mu     sync.Mutex
	fires  []Call
	counts []Call
}

// Fire registra el aviso.
func (r *Recorder) Fire(_ context.Context, trigger monitor.UrgentTrigger, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fires = append(r.fires, Call{Trigger: trigger, Detail: detail})
}

// Count registra la ocurrencia.
func (r *Recorder) Count(_ context.Context, trigger monitor.UrgentTrigger, threshold int, window time.Duration, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts = append(r.counts, Call{Trigger: trigger, Detail: detail, Threshold: threshold, Window: window})
}

// Fires devuelve una copia de los Fire registrados.
func (r *Recorder) Fires() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.fires...)
}

// Counts devuelve una copia de los Count registrados.
func (r *Recorder) Counts() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.counts...)
}
