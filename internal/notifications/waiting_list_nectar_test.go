package notifications

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
)

// Con el puente de Néctar puesto, la oferta de lista de espera ABRE la conversación con la plantilla
// y el pending queda bajo la clave de la sesión que contestará. Una entrada que nació en una sesión
// de Néctar guarda "nectar:<id>" en phone_number: esa conversación ya no sirve días después, así que
// el teléfono de la oferta sale de la ficha del paciente en SIESA.

type keyedEvent struct {
	phone, eventType string
	data             map[string]interface{}
}

type keyedEventLogger struct {
	mu     sync.Mutex
	events []keyedEvent
}

func (l *keyedEventLogger) LogEvent(_ context.Context, _, phone, eventType string, data map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, keyedEvent{phone: phone, eventType: eventType, data: data})
}

func (l *keyedEventLogger) find(eventType string) (keyedEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.eventType == eventType {
			return e, true
		}
	}
	return keyedEvent{}, false
}

type patientStub struct {
	patient *domain.Patient
	err     error
	asks    []string
}

func (p *patientStub) FindByID(_ context.Context, id string) (*domain.Patient, error) {
	p.asks = append(p.asks, id)
	return p.patient, p.err
}

// nectarWLRig: manager con el puente puesto y un Bird que cuenta toda petición.
func nectarWLRig(t *testing.T, bridge *fakenectar.Bridge, entry domain.WaitingListEntry) (*NotificationManager, *mockWLChecker, *keyedEventLogger, *atomic.Int32) {
	t.Helper()
	var birdHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		birdHits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-bird"}`))
	}))
	t.Cleanup(srv.Close)
	client := bird.NewClientForTest(srv.URL)
	client.SetNectar(bridge)
	mgr, wl := wlTestManager(t, client, entry)
	events := &keyedEventLogger{}
	mgr.SetTracker(events)
	t.Cleanup(func() {
		for _, k := range []string{"nectar:77", "nectar:55", entry.PhoneNumber} {
			if p, ok := mgr.LoadPendingForTest(k); ok && p.Timer != nil {
				p.Timer.Stop()
			}
		}
	})
	return mgr, wl, events, &birdHits
}

func TestWaitingList_NectarOpensAndRegistersUnderKey(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77, OpenMsg: "901"}
	mgr, _, events, birdHits := nectarWLRig(t, bridge, wlEntry("+573005551234"))

	if n := mgr.CheckWaitingListForCups(context.Background(), "890271"); n != 1 {
		t.Fatalf("notificados = %d", n)
	}
	if got := bridge.Opens(); len(got) != 1 || got[0] != "+573005551234" {
		t.Fatalf("se abre al teléfono de la entrada: %v", got)
	}
	p, ok := mgr.LoadPendingForTest("nectar:77")
	if !ok || p.ContactPhone != "+573005551234" || p.ConversationID != "77" || p.WaitingListID != "wl-perm-1" {
		t.Fatalf("pending bajo nectar:77: %+v ok=%v", p, ok)
	}
	if ev, ok := events.find("notification_sent"); !ok || ev.phone != "nectar:77" {
		t.Fatalf("el evento va con la clave de la sesión: %+v", events.events)
	}
	if birdHits.Load() != 0 {
		t.Fatalf("Bird no recibe nada (ni plantilla ni lookup): %d", birdHits.Load())
	}
}

// Entrada nacida en Néctar: el teléfono sale de SIESA y el whitelist se evalúa sobre ese E.164.
func TestWaitingList_NectarEntryUsesSiesaPhone(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77}
	mgr, _, _, _ := nectarWLRig(t, bridge, wlEntry("nectar:12"))
	patients := &patientStub{patient: &domain.Patient{ID: "PAT-1", Phone: "300 555 1234"}}
	mgr.SetPatientReader(patients)
	mgr.cfg.TestingWhitelistPhones = []string{"+573005551234"}

	if n := mgr.CheckWaitingListForCups(context.Background(), "890271"); n != 1 {
		t.Fatalf("notificados = %d", n)
	}
	if len(patients.asks) != 1 || patients.asks[0] != "PAT-1" {
		t.Fatalf("se consulta la ficha del paciente de la entrada: %v", patients.asks)
	}
	if got := bridge.Opens(); len(got) != 1 || got[0] != "+573005551234" {
		t.Fatalf("la apertura va al teléfono de SIESA, nunca a la clave: %v", got)
	}
	if p, ok := mgr.LoadPendingForTest("nectar:77"); !ok || p.ContactPhone != "+573005551234" {
		t.Fatalf("pending: %+v ok=%v", p, ok)
	}
}

// Sin teléfono válido en SIESA (o sin poder leerlo): se salta, sin envío y sin tocar la entrada.
func TestWaitingList_NectarEntryWithoutPhoneSkipped(t *testing.T) {
	for name, patients := range map[string]*patientStub{
		"sin teléfono":   {patient: &domain.Patient{ID: "PAT-1", Phone: "no tiene"}},
		"error de SIESA": {err: errors.New("timeout")},
		"sin ficha":      {},
	} {
		t.Run(name, func(t *testing.T) {
			bridge := &fakenectar.Bridge{OpenID: 77}
			mgr, wl, _, _ := nectarWLRig(t, bridge, wlEntry("nectar:12"))
			mgr.SetPatientReader(patients)

			if n := mgr.CheckWaitingListForCups(context.Background(), "890271"); n != 0 {
				t.Fatalf("notificados = %d", n)
			}
			if got := bridge.Opens(); len(got) != 0 {
				t.Fatalf("sin teléfono no hay envío: %v", got)
			}
			wl.mu.Lock()
			defer wl.mu.Unlock()
			if wl.notifiedID != "" || wl.updatedStatus != "" {
				t.Fatalf("la entrada no se reclama ni cambia: notified=%q status=%q", wl.notifiedID, wl.updatedStatus)
			}
		})
	}
}

// El whitelist de pruebas se evalúa sobre el E.164 resuelto: otro teléfono no recibe nada.
func TestWaitingList_NectarEntryWhitelistOnResolvedPhone(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77}
	mgr, _, _, _ := nectarWLRig(t, bridge, wlEntry("nectar:12"))
	mgr.SetPatientReader(&patientStub{patient: &domain.Patient{ID: "PAT-1", Phone: "3005551234"}})
	mgr.cfg.TestingWhitelistPhones = []string{"+573009999999", "nectar:12"}

	mgr.CheckWaitingListForCups(context.Background(), "890271")
	if got := bridge.Opens(); len(got) != 0 {
		t.Fatalf("el teléfono de SIESA no está en el whitelist (la clave no cuenta): %v", got)
	}
}

// Con el paciente hablando con un agente: evento, sin pending, y la entrada vuelve a 'waiting'
// (no 'unreachable': es pasajero, el próximo ciclo la vuelve a ofrecer).
func TestWaitingList_AgentOwnedRevertsToWaiting(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenErr: &bird.AlreadyOpenError{ConversationID: 55, Mine: false}}
	mgr, wl, events, _ := nectarWLRig(t, bridge, wlEntry("+573005551234"))

	if n := mgr.CheckWaitingListForCups(context.Background(), "890271"); n != 0 {
		t.Fatalf("notificados = %d", n)
	}
	if _, ok := events.find("notification_skipped_agent_owned"); !ok {
		t.Fatalf("falta el evento del salto: %+v", events.events)
	}
	if mgr.PendingCount() != 0 {
		t.Fatal("sin pending")
	}
	wl.mu.Lock()
	defer wl.mu.Unlock()
	if wl.updatedStatus != "waiting" {
		t.Fatalf("la entrada vuelve a 'waiting', got %q", wl.updatedStatus)
	}
}

// Apertura en duda: la oferta pudo haber salido. La entrada queda 'notified' (la resuelve
// ExpireStaleNotified como hoy), sin pending, sin reintento y con el evento notification_in_doubt.
func TestWaitingList_InDoubtStaysNotified(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenErr: nectar.ErrOpenInDoubt}
	mgr, wl, events, _ := nectarWLRig(t, bridge, wlEntry("+573005551234"))

	if n := mgr.CheckWaitingListForCups(context.Background(), "890271"); n != 0 {
		t.Fatalf("notificados = %d", n)
	}
	ev, ok := events.find("notification_in_doubt")
	if !ok || ev.data["waiting_list_id"] != "wl-perm-1" {
		t.Fatalf("falta el evento en duda: %+v", events.events)
	}
	if mgr.PendingCount() != 0 {
		t.Fatal("sin pending")
	}
	if got := bridge.Opens(); len(got) != 1 {
		t.Fatalf("una sola apertura: %v", got)
	}
	wl.mu.Lock()
	defer wl.mu.Unlock()
	if wl.notifiedID != "wl-perm-1" || wl.updatedStatus != "" {
		t.Fatalf("la entrada queda 'notified' sin cambio de estado: notified=%q status=%q", wl.notifiedID, wl.updatedStatus)
	}
}

// Un username/BSUID con el puente puesto: ErrNonContactable antes de abrir, y la entrada queda
// 'unreachable' exactamente como con Bird hoy.
func TestWaitingList_NectarNonE164Unreachable(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenID: 77}
	mgr, wl, _, birdHits := nectarWLRig(t, bridge, wlEntry("laura.perez"))
	mgr.CheckWaitingListForCups(context.Background(), "890271")
	if got := bridge.Opens(); len(got) != 0 || birdHits.Load() != 0 {
		t.Fatalf("ninguna llamada: opens=%v bird=%d", got, birdHits.Load())
	}
	wl.mu.Lock()
	defer wl.mu.Unlock()
	if wl.updatedStatus != "unreachable" {
		t.Fatalf("esperaba 'unreachable', got %q", wl.updatedStatus)
	}
}

// Puente sin canal de apertura (NECTAR_CHANNEL_ID=0): es configuración, no el paciente. La
// entrada vuelve a 'waiting' para ofrecerse cuando el canal exista.
func TestWaitingList_OpenNotConfiguredRevertsToWaiting(t *testing.T) {
	bridge := &fakenectar.Bridge{OpenErr: nectar.ErrOpenNotConfigured}
	mgr, wl, _, _ := nectarWLRig(t, bridge, wlEntry("+573005551234"))
	mgr.CheckWaitingListForCups(context.Background(), "890271")
	wl.mu.Lock()
	defer wl.mu.Unlock()
	if wl.updatedStatus != "waiting" {
		t.Fatalf("esperaba 'waiting', got %q", wl.updatedStatus)
	}
}

// Un error transitorio de la apertura (Néctar caído: conexión rechazada) no está en duda — nada
// salió—: la entrada vuelve a 'waiting' por el camino de siempre y el próximo ciclo la reintenta.
func TestWaitingList_TransientOpenErrorRevertsToWaiting(t *testing.T) {
	dial := fmt.Errorf("nectar: POST /api/bot/conversations/open: %w",
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})
	bridge := &fakenectar.Bridge{OpenErr: dial}
	mgr, wl, events, _ := nectarWLRig(t, bridge, wlEntry("+573005551234"))
	mgr.CheckWaitingListForCups(context.Background(), "890271")

	if _, ok := events.find("notification_in_doubt"); ok {
		t.Fatal("un error de conexión no está en duda")
	}
	wl.mu.Lock()
	defer wl.mu.Unlock()
	if wl.updatedStatus != "waiting" {
		t.Fatalf("esperaba 'waiting', got %q", wl.updatedStatus)
	}
}
