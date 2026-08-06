package notifications

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
)

// KPI "recuperación de cupos cancelados": cada notificación de lista de espera debe llevar su
// ORIGEN — disparo en tiempo real por liberación de slot (slot_release) vs tarea diaria
// (daily_check) — para que el dashboard separe la efectividad de cada mecanismo. El origen viaja
// en el pending (y de ahí al contexto de la sesión y al evento booked).

func TestCheckWaitingListForCups_PendingCarriesDailyTrigger(t *testing.T) {
	mgr, _ := wlTestManager(t, wlTestBird(t), wlEntry("+573005551234"))

	if got := mgr.CheckWaitingListForCups(context.Background(), "890271"); got != 1 {
		t.Fatalf("esperaba 1 notificado, got %d", got)
	}
	val, ok := mgr.pending.Load("+573005551234")
	if !ok {
		t.Fatal("esperaba pending registrado")
	}
	if p := val.(*PendingNotification); p.WLTrigger != "daily_check" {
		t.Errorf("esperaba WLTrigger=daily_check, got %q", p.WLTrigger)
	}
}

func TestCheckWaitingListForSlot_PendingCarriesSlotReleaseTrigger(t *testing.T) {
	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
		BirdTemplateWaitingListVersionID: "ver-wl-456",
		BirdTemplateWaitingListLocale:    "es-CO",
	}
	mgr := NewNotificationManager(wlTestBird(t), nil, cfg)

	wlChecker := &mockWLChecker{
		getWaitingInFn: func(_ context.Context, _ []string, _ int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{{
				ID: "wl-slotrel-1", PhoneNumber: "+573005559999", PatientID: "PAT-1",
				CupsCode: "883221", CupsName: "RM", Espacios: 1,
			}}, nil
		},
	}
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(_ context.Context, _ services.SlotQuery) ([]services.AvailableSlot, error) {
			return []services.AvailableSlot{{DoctorSiesaCode: "3", AgendaID: 100, Date: "2026-03-20", TimeSlot: "202603201000"}}, nil
		},
	}
	apptChecker := &mockFutureApptChecker{
		hasFutureFn: func(_ context.Context, _, _ string) (bool, error) { return false, nil },
	}
	agendaResolver := &mockAgendaResolver{fn: func(_ context.Context, _ int) ([]int, error) { return []int{4}, nil }}
	cupsResolver := &mockCupsResolver{fn: func(_ context.Context, _ int, _ []int) ([]string, error) { return []string{"883221"}, nil }}
	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, agendaResolver, cupsResolver)

	if got := mgr.CheckWaitingListForSlot(context.Background(), 3, 100); got != 1 {
		t.Fatalf("esperaba 1 notificado, got %d", got)
	}
	val, ok := mgr.pending.Load("+573005559999")
	if !ok {
		t.Fatal("esperaba pending registrado")
	}
	if p := val.(*PendingNotification); p.WLTrigger != "slot_release" {
		t.Errorf("esperaba WLTrigger=slot_release, got %q", p.WLTrigger)
	}
}

// wlTestBird devuelve un cliente Bird de prueba con servidor que siempre acepta.
func wlTestBird(t *testing.T) *bird.Client {
	t.Helper()
	c, srv := newTestBirdClient()
	t.Cleanup(srv.Close)
	return c
}
