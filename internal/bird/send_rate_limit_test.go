package bird

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// srv que acepta cualquier envío (respuesta mínima válida).
func rateTestServer(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
	}))
	t.Cleanup(srv.Close)
	return NewClientForTest(srv.URL)
}

// TestSendRateLimit_BlocksRunawaySender: sin inbound del paciente, el envío N+1 se corta con
// ErrSendRateLimited (defensa contra la clase del incidente 11/12-ago-2026: 1 msg/min sin fin).
func TestSendRateLimit_BlocksRunawaySender(t *testing.T) {
	c := rateTestServer(t)
	c.SetSendRateLimit(3)

	for i := 0; i < 3; i++ {
		if _, err := c.SendText("+573001112233", "", "msg"); err != nil {
			t.Fatalf("envío %d dentro de cuota no debe fallar: %v", i+1, err)
		}
	}
	_, err := c.SendText("+573001112233", "", "msg 4")
	if !errors.Is(err, ErrSendRateLimited) {
		t.Fatalf("el 4º envío sin inbound debe bloquearse con ErrSendRateLimited, got %v", err)
	}
	// Otro teléfono NO se ve afectado (cuota por-teléfono).
	if _, err := c.SendText("+573009998877", "", "otro"); err != nil {
		t.Fatalf("otro teléfono no debe estar limitado: %v", err)
	}
}

// TestSendRateLimit_InboundResets: un mensaje entrante del paciente abre cuota nueva —
// una conversación real nunca choca con el tope.
func TestSendRateLimit_InboundResets(t *testing.T) {
	c := rateTestServer(t)
	c.SetSendRateLimit(2)

	phone := "+573001112233"
	for i := 0; i < 2; i++ {
		if _, err := c.SendText(phone, "", "msg"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.SendText(phone, "", "bloqueado"); !errors.Is(err, ErrSendRateLimited) {
		t.Fatalf("esperaba bloqueo, got %v", err)
	}

	c.RecordInbound(phone) // el paciente escribió

	if _, err := c.SendText(phone, "", "tras inbound"); err != nil {
		t.Fatalf("tras inbound la cuota debe estar reseteada: %v", err)
	}
	if n, ok := c.sendRateSnapshot(phone); !ok || n != 1 {
		t.Fatalf("esperaba contador 1 tras reset+envío, got %d ok=%v", n, ok)
	}
}

// TestSendRateLimit_DisabledByDefaultInTests: NewClientForTest no fija límite (0=off) —
// ningún test existente cambia de comportamiento.
func TestSendRateLimit_DisabledByDefaultInTests(t *testing.T) {
	c := rateTestServer(t)
	for i := 0; i < 50; i++ {
		if _, err := c.SendText("+573001112233", "", "msg"); err != nil {
			t.Fatalf("sin límite configurado no debe bloquear (envío %d): %v", i+1, err)
		}
	}
}

// TestSendRateLimit_WindowExpires: al vencer la ventana, la cuota se renueva sola.
func TestSendRateLimit_WindowExpires(t *testing.T) {
	c := rateTestServer(t)
	c.SetSendRateLimit(1)
	phone := "+573001112233"

	if _, err := c.SendText(phone, "", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendText(phone, "", "2"); !errors.Is(err, ErrSendRateLimited) {
		t.Fatalf("esperaba bloqueo, got %v", err)
	}
	// Simular ventana vencida.
	c.mu.Lock()
	c.sendRate[phone].windowStart = time.Now().Add(-2 * time.Hour)
	c.mu.Unlock()
	if _, err := c.SendText(phone, "", "3"); err != nil {
		t.Fatalf("ventana vencida debe renovar cuota: %v", err)
	}
}
