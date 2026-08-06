package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
)

func wlTestManager(t *testing.T, birdClient *bird.Client, entry domain.WaitingListEntry) (*NotificationManager, *mockWLChecker) {
	t.Helper()
	cfg := &config.Config{
		BirdTemplateWaitingListProjectID: "proj-wl-123",
		BirdTemplateWaitingListVersionID: "ver-wl-456",
		BirdTemplateWaitingListLocale:    "es-CO",
	}
	mgr := NewNotificationManager(birdClient, nil, cfg)
	wlChecker := &mockWLChecker{
		getWaitingFn: func(_ context.Context, _ string, _ int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{entry}, nil
		},
	}
	slotSearcher := &mockSlotSearcher{
		getSlotsFn: func(_ context.Context, _ services.SlotQuery) ([]services.AvailableSlot, error) {
			return []services.AvailableSlot{{TimeSlot: "202603201000"}}, nil
		},
	}
	apptChecker := &mockFutureApptChecker{
		hasFutureFn: func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		},
	}
	mgr.SetWaitingListCheckDeps(slotSearcher, apptChecker, wlChecker, nil, nil)
	return mgr, wlChecker
}

func wlEntry(phone string) domain.WaitingListEntry {
	return domain.WaitingListEntry{
		ID: "wl-perm-1", PhoneNumber: phone, PatientID: "PAT-1",
		PatientName: "Maria Lopez", CupsCode: "890271", CupsName: "EMG",
		PatientAge: 35, Espacios: 1,
	}
}

// Un 4xx permanente de Bird NO debe devolver la entrada a 'waiting': el chequeo diario la
// reintentaría para siempre, pagando un envío condenado cada día. Debe quedar 'unreachable'.
func TestCheckWaitingListForCups_Permanent422_MarksUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"code":"InvalidPayload"}`))
	}))
	defer srv.Close()

	mgr, wlChecker := wlTestManager(t, bird.NewClientForTest(srv.URL), wlEntry("+573005551234"))
	count := mgr.CheckWaitingListForCups(context.Background(), "890271")

	if count != 0 {
		t.Errorf("no debió contar notificados, got %d", count)
	}
	wlChecker.mu.Lock()
	defer wlChecker.mu.Unlock()
	if wlChecker.updatedStatus != "unreachable" {
		t.Errorf("esperaba status 'unreachable', got %q", wlChecker.updatedStatus)
	}
}

// Identificador no-E.164 en la lista: ni siquiera debe llamar a Bird, y la entrada queda 'unreachable'.
func TestCheckWaitingListForCups_NonE164_MarksUnreachableWithoutHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	mgr, wlChecker := wlTestManager(t, bird.NewClientForTest(srv.URL), wlEntry("laura.perez"))
	mgr.CheckWaitingListForCups(context.Background(), "890271")

	if hits.Load() != 0 {
		t.Errorf("no debe haber llamada HTTP para identificador no-E.164, hubo %d", hits.Load())
	}
	wlChecker.mu.Lock()
	defer wlChecker.mu.Unlock()
	if wlChecker.updatedStatus != "unreachable" {
		t.Errorf("esperaba status 'unreachable', got %q", wlChecker.updatedStatus)
	}
}

// Un error transitorio (429 sostenido) conserva el comportamiento actual: revertir a 'waiting'
// para que el siguiente ciclo lo reintente.
func TestCheckWaitingListForCups_Transient429_RevertsToWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
	}))
	defer srv.Close()

	mgr, wlChecker := wlTestManager(t, bird.NewClientForTest(srv.URL), wlEntry("+573005551234"))
	count := mgr.CheckWaitingListForCups(context.Background(), "890271")

	if count != 0 {
		t.Errorf("no debió contar notificados, got %d", count)
	}
	wlChecker.mu.Lock()
	defer wlChecker.mu.Unlock()
	if wlChecker.updatedStatus != "waiting" {
		t.Errorf("esperaba revert a 'waiting', got %q", wlChecker.updatedStatus)
	}
}
