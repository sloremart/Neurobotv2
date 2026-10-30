package notifications

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakeurgent"
)

// La corrida de lista de espera detenida por configuración dispara el aviso urgente de volver a
// Bird (una vez, con la corrida y cuántas entradas quedaron), además de su único error de siempre.
func TestWaitingList_ConfigStopFiresUrgent(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenErr: nectar.ErrUnauthorized}
	first, second := wlEntry("+573005551234"), wlEntry("+573005559999")
	second.ID = "wl-perm-2"
	mgr, wl, _, _ := nectarWLRig(t, bridge, first)
	wl.getWaitingFn = func(context.Context, string, int) ([]domain.WaitingListEntry, error) {
		return []domain.WaitingListEntry{first, second}, nil
	}
	urgent := &fakeurgent.Recorder{}
	mgr.SetUrgentReporter(urgent)

	_, _ = mgr.CheckWaitingListForCupsInRun(context.Background(), "890271")

	fires := urgent.Fires()
	if len(fires) != 1 || fires[0].Trigger != monitor.TriggerProactiveStopped {
		t.Fatalf("un aviso de corrida detenida: %+v", fires)
	}
	if !strings.Contains(fires[0].Detail, "waiting_list") || !strings.Contains(fires[0].Detail, "2") {
		t.Fatalf("el detalle nombra la corrida y cuántas quedaron: %q", fires[0].Detail)
	}
	if strings.Contains(fires[0].Detail, "300555") {
		t.Fatalf("sin teléfonos: %q", fires[0].Detail)
	}
}
