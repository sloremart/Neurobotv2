package nectar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// La clave en duda acompaña a los MaxSendRetries reintentos del worker aunque cada uno vuelva a quedar
// en duda; pasado el tope ya no se guarda: un envío igual posterior es otro mensaje.
func TestSend_InDoubtKeyCoversEveryRetry(t *testing.T) {
	f := newFakeSlowSend(t, http.StatusGatewayTimeout, http.StatusBadGateway, http.StatusTooManyRequests, http.StatusOK)
	c := newSlowSendClient(f)
	for i := 0; i < MaxSendRetries+2; i++ {
		_, _ = c.SendText(context.Background(), 1234, "hola")
	}
	keys := f.sentKeys()
	if len(keys) != MaxSendRetries+2 {
		t.Fatalf("claves = %v", keys)
	}
	for i := 1; i <= MaxSendRetries; i++ {
		if keys[i] != keys[0] {
			t.Fatalf("el reintento %d cambió de clave: %v", i, keys)
		}
	}
	if keys[MaxSendRetries+1] == keys[0] {
		t.Fatalf("pasado el tope es otro mensaje: %v", keys)
	}
}

// Solo es reintentable lo que pudo no salir: transporte, 5xx y 429. Un 4xx no deja clave en duda.
func TestRetryableSend_Classification(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
		http.StatusGatewayTimeout:      true,
		http.StatusTooManyRequests:     true,
		http.StatusBadRequest:          false,
		http.StatusForbidden:           false,
		http.StatusConflict:            false,
		http.StatusNotFound:            false,
		http.StatusFound:               false,
		http.StatusOK:                  false, // cuerpo ilegible: ya salió
	} {
		f := newFakeSlowSend(t, status)
		c := newSlowSendClient(f)
		_, err := c.SendText(context.Background(), 1234, "hola")
		if status == http.StatusOK {
			err = errors.New("nectar: leer respuesta")
		}
		if got := RetryableSend(err); got != want {
			t.Errorf("status %d: RetryableSend = %v (err %v)", status, got, err)
		}
		if !want && status != http.StatusOK {
			_, _ = c.SendText(context.Background(), 1234, "hola")
			if keys := f.sentKeys(); keys[0] == keys[1] {
				t.Errorf("status %d: no debía dejar clave en duda: %v", status, keys)
			}
		}
	}
	f := newFakeSlowSend(t, 0) // tarda más que el timeout del cliente
	if _, err := newSlowSendClient(f).SendText(context.Background(), 1234, "hola"); !RetryableSend(err) {
		t.Errorf("el timeout es reintentable: %v", err)
	}
}

// B-04: el error del 429 expone la espera de su Retry-After, en segundos o como fecha.
func TestRetryAfter_ExposedOn429(t *testing.T) {
	inTwoMinutes := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	cases := []struct {
		name, header string
		status       int
		min, max     time.Duration
	}{
		{"segundos", "30", http.StatusTooManyRequests, 30 * time.Second, 30 * time.Second},
		{"fecha", inTwoMinutes, http.StatusTooManyRequests, 100 * time.Second, 2 * time.Minute},
		{"sin cabecera", "", http.StatusTooManyRequests, 0, 0},
		{"ilegible", "pronto", http.StatusTooManyRequests, 0, 0},
		{"negativo", "-5", http.StatusTooManyRequests, 0, 0},
		{"enorme", "9223372036854775807", http.StatusTooManyRequests, maxParsedRetryAfter, maxParsedRetryAfter},
		{"no cabe en int", "99999999999999999999999", http.StatusTooManyRequests, maxParsedRetryAfter, maxParsedRetryAfter},
		{"negativo que no cabe", "-99999999999999999999999", http.StatusTooManyRequests, 0, 0},
		{"un 503 no cuenta", "30", http.StatusServiceUnavailable, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			_, err := NewClient(srv.URL, svcKey, reqSecret).SendText(context.Background(), 1234, "hola")
			if got := RetryAfter(fmt.Errorf("envuelto: %w", err)); got < tc.min || got > tc.max {
				t.Fatalf("RetryAfter = %v, quiero [%v, %v]", got, tc.min, tc.max)
			}
		})
	}
}

// m13: un 429 de una ruta de mensajes llega marcado como bird.ErrLineRateLimited, también el de la
// plantilla que sale por el already_open mine=true de una apertura; sigue sin ser permanente.
func TestSend429_MarkedAsLineRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == openPath {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"reason":"already_open","conversation_id":55,"mine":true}`))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	bridge := NewBridge(NewClient(srv.URL, svcKey, reqSecret), map[string]string{"p": "plantilla"})
	bridge.ChannelID = 7
	bc := bird.NewClientForTest(srv.URL)
	bc.SetNectar(bridge)

	_, err := bc.SendProactiveTemplate(context.Background(), "+573001234567", bird.TemplateConfig{ProjectID: "p"})
	if !errors.Is(err, bird.ErrLineRateLimited) {
		t.Fatalf("el 429 de la plantilla tras already_open va marcado: %v", err)
	}
	if bird.IsPermanentSendError(err) {
		t.Fatalf("un 429 no es permanente: %v", err)
	}

	_, err = NewClient(srv.URL, svcKey, reqSecret).SendText(context.Background(), 1234, "hola")
	if !errors.Is(err, bird.ErrLineRateLimited) {
		t.Fatalf("el 429 del envío va marcado: %v", err)
	}
}
