package notifications

import (
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

// D-10: la respuesta a una plantilla que corre en segundo plano (su fila del WAL ya está cerrada)
// la espera el apagado: WaitResponses no vuelve mientras siga en vuelo.
func TestWaitResponses_WaitsForInFlightResponse(t *testing.T) {
	mgr := NewNotificationManager(nil, nil, &config.Config{})
	const phone = "+573001234567"
	if !mgr.lockPhone(phone) { // la respuesta queda bloqueada en el candado del teléfono
		t.Fatal("no se tomó el candado")
	}
	mgr.GoHandleResponse(phone, "confirm", "")

	waited := make(chan struct{})
	go func() {
		mgr.WaitResponses(5 * time.Second)
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("el apagado no esperó la respuesta en vuelo")
	case <-time.After(100 * time.Millisecond):
	}

	mgr.unlockPhone(phone)
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("el apagado sigue esperando una respuesta que ya terminó")
	}
}

// Ya en el apagado, la respuesta corre en el llamador: nada se registra después de que WaitResponses
// empezó a esperar. Por eso la llamada no vuelve mientras la respuesta siga bloqueada en el candado.
func TestGoHandleResponse_AfterWaitRunsInCaller(t *testing.T) {
	mgr := NewNotificationManager(nil, nil, &config.Config{})
	mgr.WaitResponses(time.Second)
	const phone = "+573001234567"
	if !mgr.lockPhone(phone) {
		t.Fatal("no se tomó el candado")
	}

	done := make(chan struct{})
	go func() {
		mgr.GoHandleResponse(phone, "confirm", "")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("tras WaitResponses la respuesta debe correr en el llamador")
	case <-time.After(100 * time.Millisecond):
	}

	mgr.unlockPhone(phone)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("GoHandleResponse no volvió")
	}
}
