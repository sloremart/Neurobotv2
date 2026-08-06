package bird

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// El webhook outbound recibe TANTO los mensajes del bot como los de agentes humanos del Inbox.
// Para detectar la intervención manual de un agente sin falsos positivos, el cliente registra el
// ID de cada mensaje que ÉL envía: un evento outbound con ID desconocido = lo escribió un humano.

func TestIsOwnMessage_ChannelsSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-own-channels"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	if _, err := c.SendText("+573001234567", "", "hola"); err != nil {
		t.Fatal(err)
	}
	if !c.IsOwnMessage("msg-own-channels") {
		t.Error("el ID de un envío por Channels debe registrarse como propio")
	}
}

func TestIsOwnMessage_ConversationSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-own-conv"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	if _, err := c.SendText("+573001234567", "conv-1", "hola"); err != nil {
		t.Fatal(err)
	}
	if !c.IsOwnMessage("msg-own-conv") {
		t.Error("el ID de un envío por Conversations debe registrarse como propio")
	}
}

func TestIsOwnMessage_UnknownID(t *testing.T) {
	c := NewClientForTest("http://unused")
	if c.IsOwnMessage("msg-de-un-agente") {
		t.Error("un ID nunca enviado por el cliente no es propio")
	}
	if c.IsOwnMessage("") {
		t.Error("ID vacío nunca es propio")
	}
}
