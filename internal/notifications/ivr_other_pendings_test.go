package notifications

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
)

// M-4: el DTMF resuelve los otros pendings de las citas que aplica. Aquí lo que la prueba del scheduler
// no fija: que un fallo de SIESA no resuelve nada, con qué desenlace se resuelven y que se relee el
// pending bajo su candado.

const (
	ivrKeyCalled = "+573001112233" // el pending que recibió la llamada (Bird)
	ivrKeyOther  = "nectar:1"      // otra cita del mismo paciente ese día
)

// resolvingPersister anota el desenlace con que se resuelve cada clave.
type resolvingPersister struct {
	mockPersister
	mu       sync.Mutex
	outcomes map[string]string
}

func (p *resolvingPersister) Resolve(_ context.Context, phone, status string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.outcomes == nil {
		p.outcomes = map[string]string{}
	}
	p.outcomes[phone] = status
	return nil
}

func (p *resolvingPersister) outcome(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.outcomes[key]
}

// ivrDayRepo es SIESA con dos citas del mismo paciente ese día; writeErr hace fallar las escrituras.
type ivrDayRepo struct {
	*mockApptRepoNotif
	day []domain.Appointment
}

func (r *ivrDayRepo) FindUpcomingByPatient(context.Context, string) ([]domain.Appointment, error) {
	return r.day, nil
}

// ivrDTMFHarness registra el pending llamado y el de la otra cita, y ata la llamada al primero.
func ivrDTMFHarness(t *testing.T, writeErr error, written chan struct{}) (*NotificationManager, *resolvingPersister) {
	t.Helper()
	first := sampleAppt()
	second := first
	second.ID, second.TimeSlot = "APT002", "202603201100"
	write := func() error {
		if written != nil {
			close(written)
			written = nil
		}
		return writeErr
	}
	repo := &ivrDayRepo{
		mockApptRepoNotif: &mockApptRepoNotif{
			findByIDFn: func(context.Context, string) (*domain.Appointment, error) { return &first, nil },
			confirmFn:  func(context.Context, string, string, string) error { return write() },
			cancelFn:   func(context.Context, string, string, string, string) error { return write() },
		},
		day: []domain.Appointment{first, second},
	}
	client, srv := newTestBirdClient()
	t.Cleanup(srv.Close)
	mgr := NewNotificationManager(client, services.NewAppointmentService(repo, nil), &config.Config{})
	persister := &resolvingPersister{}
	mgr.SetPersister(persister)
	mgr.RegisterPending(PendingNotification{Type: "confirmation", Phone: ivrKeyCalled, AppointmentID: first.ID})
	mgr.RegisterPending(PendingNotification{
		Type: "confirmation", Phone: ivrKeyOther, ContactPhone: ivrKeyCalled, AppointmentID: second.ID,
	})
	t.Cleanup(func() {
		for _, k := range []string{ivrKeyCalled, ivrKeyOther} {
			if p, ok := mgr.LoadPendingForTest(k); ok && p.Timer != nil {
				p.Timer.Stop()
			}
		}
	})
	mgr.callIDMap.Store("call-1", ivrKeyCalled)
	return mgr, persister
}

// El otro pending se resuelve con el desenlace del DTMF: confirmada o cancelada.
func TestIVRDTMF_OtherPendingsResolvedWithOutcome(t *testing.T) {
	for _, tc := range []struct{ keys, outcome string }{{"1", "confirmed"}, {"2", "cancelled"}} {
		t.Run(tc.outcome, func(t *testing.T) {
			mgr, persister := ivrDTMFHarness(t, nil, nil)
			mgr.HandleVoiceGatherResult("call-1", tc.keys)
			if _, ok := mgr.LoadPendingForTest(ivrKeyOther); ok {
				t.Fatal("el pending de la otra cita queda resuelto")
			}
			if got := persister.outcome(ivrKeyOther); got != tc.outcome {
				t.Errorf("desenlace persistido = %q, want %q", got, tc.outcome)
			}
		})
	}
}

// Si SIESA no aplicó el DTMF, ningún otro pending se resuelve: su cita sigue como estaba.
func TestIVRDTMF_FailedWriteResolvesNoOtherPending(t *testing.T) {
	for _, keys := range []string{"1", "2"} {
		t.Run(keys, func(t *testing.T) {
			mgr, persister := ivrDTMFHarness(t, errors.New("siesa down"), nil)
			mgr.HandleVoiceGatherResult("call-1", keys)
			if _, ok := mgr.LoadPendingForTest(ivrKeyOther); !ok {
				t.Error("el pending de la otra cita sigue vivo")
			}
			if got := persister.outcome(ivrKeyOther); got != "" {
				t.Errorf("no se persiste ningún desenlace, got %q", got)
			}
		})
	}
}

// El pending se relee bajo su candado: si mientras esperaba lo reemplazó el de otra cita, no se toca.
func TestIVRDTMF_RereadsPendingUnderItsLock(t *testing.T) {
	written := make(chan struct{})
	mgr, persister := ivrDTMFHarness(t, nil, written)
	if err := mgr.phoneLock.Lock(context.Background(), ivrKeyOther); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { mgr.HandleVoiceGatherResult("call-1", "1"); close(done) }()
	<-written
	time.Sleep(50 * time.Millisecond) // el DTMF ya listó la clave y espera su candado
	mgr.storePending(ivrKeyOther, &PendingNotification{Type: "confirmation", Phone: ivrKeyOther, AppointmentID: "APT-OTRA"})
	mgr.phoneLock.Unlock(ivrKeyOther)
	<-done

	p, ok := mgr.LoadPendingForTest(ivrKeyOther)
	if !ok || p.AppointmentID != "APT-OTRA" {
		t.Errorf("el pending de otra cita no se toca: %+v", p)
	}
	if got := persister.outcome(ivrKeyOther); got != "" {
		t.Errorf("no se persiste ningún desenlace, got %q", got)
	}
}
