package bird

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Contactos con whatsappusername (privacidad de número de WhatsApp) — CONTRATO DE COSTO
// (13-ago-2026, validado en vivo): la ÚNICA vía que ENTREGA es Channels+BSUID (bsuid.go). Los
// POST a conversación devuelven 201 — Bird los COBRA — pero su entrega falla SIEMPRE (el puente
// usa el username como teléfono). Y la Channels API con el valor crudo es un 422 cobrado. Regla:
// BSUID o nada; jamás un mensaje cobrable que no entregue.

// La LISTA de un username con conversación viva se entrega por BSUID; la conversación NO se toca
// (cada POST ahí = cobrado y muerto).
func TestSendList_Username_EntregaPorBsuidSinTocarConversacion(t *testing.T) {
	var convPosts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && strings.Contains(r.URL.Path, "/contacts/identifiers/"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"c1","featuredIdentifiers":[{"key":"whatsapp_193596046348777","value":"CO.b1"}]}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/"):
			convPosts.Add(1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"paid-dead"}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/channels/"):
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"msg-bsuid-list"}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	sections := []ListSection{{Title: "S", Rows: []ListRow{{ID: "1", Title: "Op"}}}}
	msgID, err := c.SendList("dorisbaquero", "conv-live", "Elige una opción", "Ver", sections)
	if err != nil {
		t.Fatalf("la lista debió entregarse por BSUID: %v", err)
	}
	if msgID != "msg-bsuid-list" {
		t.Errorf("esperaba msg-bsuid-list, got %q", msgID)
	}
	if convPosts.Load() != 0 {
		t.Errorf("la conversación NO debe tocarse (201 cobrado sin entrega), hubo %d POSTs", convPosts.Load())
	}
}

// Conversación muerta y contacto sin E.164: ya NO se recrea conversación para reenviar (ese POST
// también se cobraba sin entregar); la entrega va por BSUID directamente.
func TestSendText_NonE164_EntregaPorBsuidConConversacionMuerta(t *testing.T) {
	var convPosts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && strings.Contains(r.URL.Path, "/contacts/identifiers/"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"c1","featuredIdentifiers":[{"key":"whatsapp_193596046348777","value":"CO.b2"}]}`))
		case r.Method == "POST" && (strings.Contains(r.URL.Path, "/conversations/") || strings.HasSuffix(r.URL.Path, "/conversations")):
			convPosts.Add(1)
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"code":"InvalidPayload"}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/channels/"):
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"msg-bsuid-text"}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	msgID, err := c.SendText("dorisbaquero", "conv-dead", "Hola")
	if err != nil {
		t.Fatalf("debió entregar por BSUID: %v", err)
	}
	if msgID != "msg-bsuid-text" {
		t.Errorf("esperaba msg-bsuid-text, got %q", msgID)
	}
	if convPosts.Load() != 0 {
		t.Errorf("no debe postear a conversaciones, hubo %d", convPosts.Load())
	}
}

// Si tampoco se puede recrear la conversación, el resultado es ErrNonContactable sin tocar Channels.
func TestSendText_NonE164_DeadConversationAndCreateFails_NonContactable(t *testing.T) {
	var channelsHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/channels/"):
			channelsHits.Add(1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"x"}`))
		case r.Method == "POST":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"code":"InvalidPayload"}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendText("dorisbaquero", "conv-dead", "Hola")
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("esperaba ErrNonContactable, got %v", err)
	}
	if channelsHits.Load() != 0 {
		t.Errorf("no debe tocar Channels, hubo %d llamadas", channelsHits.Load())
	}
}
