package notifications

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
)

// Auditoría queries P2: una cancelación dispara el chequeo de lista de espera con hasta 200
// entradas, y el código ejecutaba la query de slots (la más cara del bot) UNA VEZ POR ENTRADA
// aunque muchas compartan CUP y restricciones idénticas. Dentro de una misma corrida el resultado
// no cambia (notificar no reserva cupos), así que las consultas idénticas se memoizan, igual que
// HasFutureForCup por (paciente, CUP).
func TestNotifyWaitingEntries_MemoizesIdenticalQueries(t *testing.T) {
	birdClient, srv := newTestBirdClient()
	defer srv.Close()

	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
		BirdTemplateWaitingListVersionID: "ver-wl-456",
		BirdTemplateWaitingListLocale:    "es-CO",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)

	entries := []domain.WaitingListEntry{
		{ID: "wl-1", PhoneNumber: "+573005550001", PatientID: "PAT-1", CupsCode: "890271", PatientAge: 35, Espacios: 1},
		{ID: "wl-2", PhoneNumber: "+573005550002", PatientID: "PAT-2", CupsCode: "890271", PatientAge: 35, Espacios: 1},
		{ID: "wl-3", PhoneNumber: "+573005550003", PatientID: "PAT-3", CupsCode: "890271", PatientAge: 35, Espacios: 2},
		{ID: "wl-4", PhoneNumber: "+573005550001", PatientID: "PAT-1", CupsCode: "890271", PatientAge: 35, Espacios: 1},
	}
	wlChecker := &mockWLChecker{
		getWaitingFn: func(_ context.Context, _ string, _ int) ([]domain.WaitingListEntry, error) {
			return entries, nil
		},
	}

	var slotCalls, futureCalls atomic.Int32
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(_ context.Context, _ services.SlotQuery) ([]services.AvailableSlot, error) {
			slotCalls.Add(1)
			// 5 slots para que la llamada de capacidad no corte el loop antes de la 4ª entrada.
			return []services.AvailableSlot{
				{TimeSlot: "202603201000"},
				{TimeSlot: "202603201030"},
				{TimeSlot: "202603201100"},
				{TimeSlot: "202603201130"},
				{TimeSlot: "202603201200"},
			}, nil
		},
	}
	apptChecker := &mockFutureApptChecker{
		hasFutureFn: func(_ context.Context, _, _ string) (bool, error) {
			futureCalls.Add(1)
			return false, nil
		},
	}
	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, nil, nil)

	count := mgr.CheckWaitingListForCups(context.Background(), "890271")
	if count != 4 {
		t.Fatalf("notificados = %d, want 4", count)
	}

	// 1 llamada de capacidad + 2 queries distintas en el loop ((890271, esp 1) y (890271, esp 2)):
	// las entradas wl-2 y wl-4 reutilizan el resultado memoizado de wl-1.
	if got := slotCalls.Load(); got != 3 {
		t.Errorf("GetAvailableSlots llamadas = %d, want 3 (1 capacidad + 2 distintas)", got)
	}
	// (PAT-1, 890271) aparece dos veces (wl-1 y wl-4) → una sola consulta por par distinto.
	if got := futureCalls.Load(); got != 3 {
		t.Errorf("HasFutureForCup llamadas = %d, want 3 (pares paciente-CUP distintos)", got)
	}
}
