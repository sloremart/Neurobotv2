package bird

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Contactos con whatsappusername (privacidad de número de WhatsApp): la Channels API exige un
// teléfono E.164 como receiver, así que para ellos la CONVERSACIÓN es el único canal. El fallback
// de listas/botones debe viajar por la conversación cuando existe; Channels queda como último
// recurso solo para contactos con teléfono real.

// Si la conversación rechaza la LISTA (contenido/tipo) pero está viva, el texto numerado debe
// salir por la MISMA conversación — no por Channels (que para un username es un 422 cobrado).
func TestSendList_FallbackTextViaConversation_WhenListRejected(t *testing.T) {
	var channelsHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/") && strings.HasSuffix(r.URL.Path, "/messages"):
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), `"type":"list"`) {
				w.WriteHeader(400) // lista no soportada para este destinatario (no es "not active")
				_, _ = w.Write([]byte(`{"code":"UnsupportedMessageType"}`))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-conv-text"}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/channels/"):
			channelsHits.Add(1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-channels"}`))
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
		t.Fatalf("el fallback por conversación debió entregar el texto: %v", err)
	}
	if msgID != "msg-conv-text" {
		t.Errorf("esperaba entrega por conversación (msg-conv-text), got %q", msgID)
	}
	if channelsHits.Load() != 0 {
		t.Errorf("no debe tocar Channels para un username, hubo %d llamadas", channelsHits.Load())
	}
}

// Conversación muerta y contacto sin E.164: último recurso = recrear la conversación (mismo
// mecanismo que el handoff de escalación) y entregar el texto por la nueva.
func TestSendText_NonE164_RecreatesConversationAsLastResort(t *testing.T) {
	var channelsHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/conv-dead/messages"):
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"code":"InvalidPayload","details":{".status":["conversation status is not active"]}}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/conv-new/messages"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-recreated"}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/conversations"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"conv-new"}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/channels/"):
			channelsHits.Add(1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-channels"}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	msgID, err := c.SendText("dorisbaquero", "conv-dead", "Hola")
	if err != nil {
		t.Fatalf("debió entregar recreando la conversación: %v", err)
	}
	if msgID != "msg-recreated" {
		t.Errorf("esperaba msg-recreated, got %q", msgID)
	}
	if channelsHits.Load() != 0 {
		t.Errorf("no debe tocar Channels para un username, hubo %d llamadas", channelsHits.Load())
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
