package nectar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Un envío EN DUDA (timeout, 502/504 de un proxy) puede haber llegado a Néctar. El reintento del
// MISMO mensaje tiene que viajar con la MISMA idempotency_key: Néctar la encuentra y contesta con
// el mensaje original sin encolar otro (insertBotOutbound → idempotentTwin). Con una clave nueva el
// paciente lo recibiría dos veces.

// fakeSlowSend sirve POST …/1234/messages: cada llamada consume el siguiente paso del guion
// (status 0 = tardar más que el cliente; el último paso se repite) y anota la clave que llegó.
type fakeSlowSend struct {
	srv    *httptest.Server
	mu     sync.Mutex
	script []int
	keys   []string
}

func newFakeSlowSend(t *testing.T, script ...int) *fakeSlowSend {
	f := &fakeSlowSend{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Key string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.keys = append(f.keys, req.Key)
		step := f.script[len(f.script)-1]
		if len(f.keys) <= len(f.script) {
			step = f.script[len(f.keys)-1]
		}
		f.mu.Unlock()
		if step == 0 {
			time.Sleep(300 * time.Millisecond) // más que el timeout del cliente de la prueba
			step = http.StatusCreated
		}
		w.WriteHeader(step)
		if step < 300 {
			_, _ = w.Write([]byte(`{"id":55}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlowSend) sentKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

func newSlowSendClient(f *fakeSlowSend) *Client {
	c := NewClient(f.srv.URL, svcKey, reqSecret)
	c.http.Timeout = 100 * time.Millisecond
	return c
}

func TestSend_InDoubtRetryReusesIdempotencyKey(t *testing.T) {
	for name, first := range map[string]int{
		"timeout": 0,
		"502":     http.StatusBadGateway,
		"504":     http.StatusGatewayTimeout,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeSlowSend(t, first, http.StatusOK)
			c := newSlowSendClient(f)

			_, err := c.SendText(context.Background(), 1234, "lista de horarios")
			if err == nil || bird.IsPermanentSendError(err) {
				t.Fatalf("el primer envío tiene que fallar en duda (reintentable): %v", err)
			}
			if _, err := c.SendText(context.Background(), 1234, "lista de horarios"); err != nil {
				t.Fatalf("reintento: %v", err)
			}
			keys := f.sentKeys()
			if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
				t.Fatalf("el reintento del mismo mensaje tiene que reutilizar la clave: %v", keys)
			}
		})
	}
}

func TestSend_FreshKeyOutsideTheInDoubtRetry(t *testing.T) {
	t.Run("tras el éxito del reintento, el siguiente envío igual es otro mensaje", func(t *testing.T) {
		f := newFakeSlowSend(t, http.StatusGatewayTimeout, http.StatusOK)
		c := newSlowSendClient(f)
		_, _ = c.SendText(context.Background(), 1234, "hola")
		_, _ = c.SendText(context.Background(), 1234, "hola")
		_, _ = c.SendText(context.Background(), 1234, "hola")
		keys := f.sentKeys()
		if len(keys) != 3 || keys[1] != keys[0] || keys[2] == keys[1] {
			t.Fatalf("claves = %v", keys)
		}
	})

	t.Run("otro contenido no hereda la clave en duda", func(t *testing.T) {
		f := newFakeSlowSend(t, http.StatusGatewayTimeout, http.StatusOK)
		c := newSlowSendClient(f)
		_, _ = c.SendText(context.Background(), 1234, "hola")
		_, _ = c.SendText(context.Background(), 1234, "adiós")
		if keys := f.sentKeys(); len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("claves = %v", keys)
		}
	})

	t.Run("pasado el plazo, la clave en duda no se reutiliza", func(t *testing.T) {
		f := newFakeSlowSend(t, http.StatusGatewayTimeout, http.StatusOK)
		c := newSlowSendClient(f)
		now := time.Now()
		c.now = func() time.Time { return now }
		_, _ = c.SendText(context.Background(), 1234, "hola")
		now = now.Add(inDoubtKeyTTL + time.Second)
		_, _ = c.SendText(context.Background(), 1234, "hola")
		if keys := f.sentKeys(); len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("claves = %v", keys)
		}
	})

	t.Run("un rechazo permanente no deja clave en duda", func(t *testing.T) {
		f := newFakeSlowSend(t, http.StatusNotFound, http.StatusOK)
		c := newSlowSendClient(f)
		if _, err := c.SendText(context.Background(), 1234, "hola"); !bird.IsPermanentSendError(err) {
			t.Fatalf("404 es permanente: %v", err)
		}
		_, _ = c.SendText(context.Background(), 1234, "hola")
		if keys := f.sentKeys(); len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("claves = %v", keys)
		}
	})
}

// M-1: la clave en duda se reutiliza UNA vez (el único reintento del worker). Si ese reintento
// también queda en duda no se vuelve a guardar: un envío igual posterior es otro mensaje.
func TestSend_InDoubtKeyIsReusedOnlyOnce(t *testing.T) {
	f := newFakeSlowSend(t, http.StatusGatewayTimeout, http.StatusGatewayTimeout, http.StatusOK)
	c := newSlowSendClient(f)
	for i := 0; i < 3; i++ {
		_, _ = c.SendText(context.Background(), 1234, "hola")
	}
	keys := f.sentKeys()
	if len(keys) != 3 || keys[1] != keys[0] || keys[2] == keys[1] {
		t.Fatalf("claves = %v", keys)
	}
}
