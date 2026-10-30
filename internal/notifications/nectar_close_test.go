package notifications

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
)

// Una conversación que el bot abrió en Néctar con una plantilla y nadie contestó se cierra SOLO
// donde la lógica de siempre ya terminó con ella en silencio (sin escalar y sin sesión viva). La
// cita, la lista de espera y las escalaciones no cambian: el cierre va después, y Bird no recibe nada.

type closeRig struct {
	mgr      *NotificationManager
	bridge   *fakenectar.Bridge
	sessions *mockSessionCreator
	wl       *mockWaitingListFinder
	birdHits *atomic.Int32
}

func newCloseRig(t *testing.T, cfg *config.Config, withBridge bool) *closeRig {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-bird"}`))
	}))
	t.Cleanup(srv.Close)
	client := bird.NewClientForTest(srv.URL)
	bridge := &fakenectar.Bridge{}
	if withBridge {
		client.SetNectar(bridge)
	}
	apptSvc := services.NewAppointmentService(&mockApptRepoNotif{
		findByIDFn: func(_ context.Context, _ string) (*domain.Appointment, error) {
			appt := sampleAppt()
			return &appt, nil
		},
	}, nil)
	mgr := NewNotificationManager(client, apptSvc, cfg)
	sessions := &mockSessionCreator{}
	wl := &mockWaitingListFinder{}
	mgr.SetWaitingListDeps(wl, sessions, &mockVirtualEnqueuer{})
	t.Cleanup(func() {
		mgr.pending.Range(func(_, v interface{}) bool {
			if p, _ := v.(*PendingNotification); p != nil && p.Timer != nil {
				p.Timer.Stop()
			}
			return true
		})
	})
	return &closeRig{mgr: mgr, bridge: bridge, sessions: sessions, wl: wl, birdHits: &hits}
}

// expire guarda el pending bajo su clave y lo vence como lo haría el timer.
func (r *closeRig) expire(p PendingNotification) {
	r.mgr.storePending(p.Phone, &p)
	r.mgr.handleTimeout(p.Phone)
}

func nectarPending(nType string) PendingNotification {
	return PendingNotification{
		Type: nType, Phone: "nectar:77", ContactPhone: "+573005551234", ConversationID: "77",
		AppointmentID: "APT001", WaitingListID: "wl-1",
	}
}

func assertClosed(t *testing.T, r *closeRig, want []int64) {
	t.Helper()
	got := r.bridge.Closes()
	if len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
		t.Fatalf("cierres en Néctar = %v, esperado %v", got, want)
	}
	if n := r.birdHits.Load(); n != 0 {
		t.Fatalf("Bird no recibe nada: %d peticiones", n)
	}
}

func TestPendingExpired_NectarClosesConversation(t *testing.T) {
	silent := map[string]struct {
		cfg *config.Config
		p   func() PendingNotification
	}{
		// Sin cadena de seguimiento: el pending vence tras el IVR de las 13:00 y se limpia en silencio.
		"confirmación sin seguimiento": {
			cfg: &config.Config{ConfirmFollowupEnabled: false},
			p:   func() PendingNotification { p := nectarPending("confirmation"); p.RetryCount = 3; return p },
		},
		"reprogramación sin seguimiento": {
			cfg: &config.Config{ConfirmFollowupEnabled: false},
			p:   func() PendingNotification { return nectarPending("reschedule") },
		},
		// Recordatorio del mismo día: un solo disparo, sin escalar.
		"recordatorio del mismo día": {
			cfg: &config.Config{ConfirmFollowupEnabled: true},
			p:   func() PendingNotification { p := nectarPending("confirmation"); p.SameDay = true; return p },
		},
		"cancelación": {
			cfg: &config.Config{},
			p:   func() PendingNotification { return nectarPending("cancellation") },
		},
		"lista de espera vencida": {
			cfg: &config.Config{},
			p:   func() PendingNotification { return nectarPending("waiting_list") },
		},
	}
	for name, tc := range silent {
		t.Run(name, func(t *testing.T) {
			r := newCloseRig(t, tc.cfg, true)
			r.expire(tc.p())
			assertClosed(t, r, []int64{77})
			if r.mgr.HasPending("nectar:77") {
				t.Fatal("el pending ya terminó")
			}
		})
	}
}

// La lista de espera que el paciente ya agendó (la entrada dejó de estar 'notified') no se cierra:
// el vencimiento no la resolvió.
func TestPendingExpired_NectarWaitingListAlreadyScheduled_NoClose(t *testing.T) {
	r := newCloseRig(t, &config.Config{}, true)
	r.wl.resolveIfNotifiedFn = func(context.Context, string, string) (bool, error) { return false, nil }
	r.expire(nectarPending("waiting_list"))
	assertClosed(t, r, nil)
}

// Con sesión viva (activa o escalada) bajo la clave, la conversación sigue en uso: no se cierra.
// Si la consulta falla, tampoco: ante la duda, no se cierra.
func TestPendingExpired_NectarSessionAlive_NoClose(t *testing.T) {
	for name, find := range map[string]func(context.Context, string) (*session.Session, error){
		"sesión activa": func(_ context.Context, phone string) (*session.Session, error) {
			return &session.Session{ID: "s1", PhoneNumber: phone, Status: session.StatusActive}, nil
		},
		"sesión escalada": func(_ context.Context, phone string) (*session.Session, error) {
			return &session.Session{ID: "s1", PhoneNumber: phone, Status: session.StatusEscalated}, nil
		},
		"error al consultar": func(context.Context, string) (*session.Session, error) {
			return nil, errors.New("db caída")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newCloseRig(t, &config.Config{}, true)
			var asked []string
			r.sessions.findCurrentFn = func(ctx context.Context, phone string) (*session.Session, error) {
				asked = append(asked, phone)
				return find(ctx, phone)
			}
			r.expire(nectarPending("cancellation"))
			assertClosed(t, r, nil)
			if len(asked) != 1 || asked[0] != "nectar:77" {
				t.Fatalf("se consulta la sesión de la clave: %v", asked)
			}
		})
	}
}

// Las ramas que escalan o siguen la cadena no cierran: la escalación se queda la conversación y
// la cadena todavía espera respuesta (o el IVR).
func TestPendingExpired_NectarEscalatesOrContinues_NoClose(t *testing.T) {
	cfg := func() *config.Config {
		return &config.Config{ConfirmFollowupEnabled: true, ConfirmFollowup2Hours: 3, ConfirmPostIVRMinutes: 30, BirdTeamFallback: "team-fb"}
	}
	for _, retry := range []int{0, 1, 2, 3} {
		r := newCloseRig(t, cfg(), true)
		p := nectarPending("confirmation")
		p.RetryCount = retry
		r.expire(p)
		if got := r.bridge.Closes(); len(got) != 0 {
			t.Fatalf("retry %d: no se cierra (cadena o escalación): %v", retry, got)
		}
	}
}

// Un pending de Bird (clave = teléfono) nunca cierra nada, ni en Bird ni en Néctar.
func TestPendingExpired_BirdKey_NoClose(t *testing.T) {
	r := newCloseRig(t, &config.Config{}, true)
	r.expire(PendingNotification{Type: "cancellation", Phone: "+573005551234", ConversationID: "conv-1"})
	assertClosed(t, r, nil)
}

// Con el puente apagado, una clave "nectar:" no manda el cierre a Bird, y no es una falla: no hay
// nada que cerrar (sin Warn).
func TestPendingExpired_NectarKeyBridgeOff_NothingToBird(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(previous)

	r := newCloseRig(t, &config.Config{}, false)
	r.expire(nectarPending("cancellation"))
	assertClosed(t, r, nil)
	if strings.Contains(logged.String(), "level=WARN") {
		t.Errorf("sin puente no hay nada que cerrar, no es una falla: %s", logged.String())
	}
}

// «Ya no es mía» al cerrar: la conversación ya no es del bot, se ignora. Se intentó una vez.
func TestPendingExpired_NectarCloseNotMine_Ignored(t *testing.T) {
	r := newCloseRig(t, &config.Config{}, true)
	notMine := errors.New("404 not mine")
	r.bridge.CloseErr, r.bridge.NotMineErr = notMine, notMine
	r.expire(nectarPending("cancellation"))
	assertClosed(t, r, []int64{77})
}

// Ventana de gracia post-IVR (seguimiento deshabilitado) vencida sin DTMF: termina en silencio y cierra.
func TestIVRGraceExpired_NectarClosesConversation(t *testing.T) {
	r := newCloseRig(t, &config.Config{ConfirmFollowupEnabled: false, ConfirmPostIVRMinutes: 30}, true)
	p := nectarPending("confirmation")
	p.RetryCount = 3
	r.mgr.storePending(p.Phone, &p)
	r.mgr.expireIVRGrace(p.Phone)
	assertClosed(t, r, []int64{77})
	if r.mgr.HasPending("nectar:77") {
		t.Fatal("la ventana de gracia limpia el pending")
	}
}
