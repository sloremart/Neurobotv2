package bird

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// Cuerpo de error de Bird con datos del contacto al principio, como los que repiten el receptor.
const contactErrorBody = `{"contact":{"displayName":"María López","identifierValue":"+573001234567"},` +
	`"code":"InvalidPayload","message":"contact archived"}`

func errorBodyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertMaskedBody(t *testing.T, label, got string) {
	t.Helper()
	for _, leak := range []string{"María", "López", "3001234567"} {
		if strings.Contains(got, leak) {
			t.Errorf("%s lleva %q sin enmascarar: %s", label, leak, got)
		}
	}
	for _, keep := range []string{"InvalidPayload", "contact archived"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s perdió %q, necesario para diagnosticar: %s", label, keep, got)
		}
	}
}

// M2: todo error que lleva el cuerpo de Bird sale enmascarado, con código y mensaje.
func TestBirdErrors_MaskContactBody(t *testing.T) {
	calls := map[string]func(c *Client) error{
		"conversations": func(c *Client) error {
			_, err := c.sendToConversation("conv-1", map[string]string{"type": "text"}, false)
			return err
		},
		"channels": func(c *Client) error {
			_, err := c.sendMessage(c.messagesURL(), map[string]string{"k": "v"})
			return err
		},
		"bsuid": func(c *Client) error {
			_, _, err := c.resolveBsuid(context.Background(), "maria.lopez")
			return err
		},
		"create conversation": func(c *Client) error {
			_, err := c.CreateConversationForPhone(context.Background(), "+573001234567")
			return err
		},
		"list agents": func(c *Client) error {
			_, err := c.ListAllAgents()
			return err
		},
		"search feed items": func(c *Client) error {
			_, _, _, err := c.searchFeedItem("conv-1")
			return err
		},
		"patch feed item": func(c *Client) error {
			_, err := c.doPatchFeedItem(c.conversationsBase()+"/x", []byte("{}"))
			return err
		},
		"lookup": func(c *Client) error {
			_, err := c.LookupConversationByPhone("+573001234567")
			return err
		},
		"get call": func(c *Client) error {
			_, err := c.GetCallDetails("call-1")
			return err
		},
	}
	for label, call := range calls {
		t.Run(label, func(t *testing.T) {
			c := NewClientForTest(errorBodyServer(t, http.StatusBadRequest, contactErrorBody).URL)
			err := call(c)
			if err == nil {
				t.Fatal("quería error")
			}
			assertMaskedBody(t, label, err.Error())
		})
	}

	t.Run("422 conversación cerrada", func(t *testing.T) {
		c := NewClientForTest(errorBodyServer(t, http.StatusUnprocessableEntity, contactErrorBody).URL)
		_, err := c.sendToConversation("conv-1", map[string]string{"type": "text"}, false)
		if !errors.Is(err, ErrConversationNotActive) {
			t.Fatalf("el 422 sigue siendo ErrConversationNotActive: %v", err)
		}
		assertMaskedBody(t, "422", err.Error())
	})
}

// M1: el WARN del sondeo de la conversación no lleva el nombre del contacto.
func TestFetchMessageConvEmpty_MasksContactBody(t *testing.T) {
	logged := captureDebugLogs(t)
	c := NewClientForTest(errorBodyServer(t, http.StatusForbidden, contactErrorBody).URL)
	if got := c.FetchMessageConversationID(context.Background(), "msg-1"); got != "" {
		t.Fatalf("no había conversación: %q", got)
	}
	assertMaskedBody(t, "fetch_message_conv_empty", logged.String())
}

// El enmascarado no le quita al bot lo que decide leyendo el cuerpo: el error de CONTENIDO de
// WhatsApp (dispara la alerta) se reconoce aunque el código tenga 6 dígitos o venga pasado el recorte.
func TestSendToConversation_ContentErrorSurvivesMasking(t *testing.T) {
	padding := strings.Repeat("x", warnBodyMax+50)
	cases := map[string]struct{ body, code string }{
		"código numérico":        {`{"code":131009,"message":"bad"}`, "131009"},
		"código con #":           {`{"message":"(#131008) falta un parámetro"}`, "131008"},
		"código tras el recorte": {`{"detail":"` + padding + `","code":131009}`, "131009"},
		"frase tras el recorte":  {`{"detail":"` + padding + `","message":"Row title is too long"}`, "content"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			c := NewClientForTest(errorBodyServer(t, http.StatusBadRequest, tc.body).URL)
			_, err := c.sendToConversation("conv-1", map[string]string{"type": "text"}, false)
			code, isContent := whatsAppContentError(err)
			if !isContent || code != tc.code {
				t.Fatalf("whatsAppContentError = (%q, %v), quiero %q; err: %v", code, isContent, tc.code, err)
			}
		})
	}
	t.Run("otro cuerpo no es de contenido", func(t *testing.T) {
		c := NewClientForTest(errorBodyServer(t, http.StatusBadRequest, contactErrorBody).URL)
		_, err := c.sendToConversation("conv-1", map[string]string{"type": "text"}, false)
		if _, isContent := whatsAppContentError(err); isContent {
			t.Fatalf("no es de contenido: %v", err)
		}
	})
}

// El 409 de contacto del reabrir se reconoce por el cuerpo crudo, aunque la frase caiga tras el recorte.
func TestDoPatchFeedItem_ContactConflictSurvivesMasking(t *testing.T) {
	body := `{"detail":"` + strings.Repeat("x", warnBodyMax+50) + `","message":"Another active conversation exists"}`
	c := NewClientForTest(errorBodyServer(t, http.StatusConflict, body).URL)
	_, err := c.doPatchFeedItem(c.conversationsBase()+"/x", []byte("{}"))
	if !isContactConversationConflict(err) {
		t.Fatalf("el 409 de contacto dejó de reconocerse: %v", err)
	}

	c = NewClientForTest(errorBodyServer(t, http.StatusConflict, `{"code":"Conflict"}`).URL)
	_, err = c.doPatchFeedItem(c.conversationsBase()+"/x", []byte("{}"))
	if isContactConversationConflict(err) {
		t.Fatalf("un 409 de concurrencia no es de contacto: %v", err)
	}
}

// warnBody corta por runas, enmascara los valores de `value` (identificadores de contacto) y
// conserva el código de WhatsApp de 6 dígitos pero no otro número de esa longitud.
func TestWarnBody_RunesAndDigits(t *testing.T) {
	got := warnBody([]byte(strings.Repeat("ñ", warnBodyMax+10)))
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != warnBodyMax {
		t.Errorf("recorte por runas: %d runas, válido=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	got = warnBody([]byte(`{"identifiers":[{"key":"emailaddress","value":"maria@example.com"}],"code":131026,"doc":"cedula 1234567"}`))
	if strings.Contains(got, "maria@example.com") || strings.Contains(got, "1234567") {
		t.Errorf("sin enmascarar: %s", got)
	}
	if !strings.Contains(got, "131026") {
		t.Errorf("perdió el código de WhatsApp: %s", got)
	}
	if got := maskDigitRuns(`code: 1234567890`); strings.Contains(got, "1234567890") {
		t.Errorf("un número largo tras «code» no es un código: %s", got)
	}
}
