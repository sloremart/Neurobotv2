package bird

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Cuando SendText/SendList/SendButtons no reciben conversationID, resuelven la conversación antes
// de caer a Channels API (9e9916d). Esa resolución iba DERECHA al lookup por lista, que barre hasta
// 10 páginas de 50 conversaciones — y es la vía que el propio código documenta como "poco fiable"
// frente a la caché (ver EscalateToAgent). Estos tests fijan las dos mejoras:
//   1. mirar la caché en memoria ANTES de salir a la red;
//   2. recordar el fallo, para no repetir el barrido de 10 páginas en cada envío al mismo número.

// lookupCounter cuenta las peticiones del lookup de conversaciones (GET .../conversations) y las
// separa de los envíos (POST .../messages).
type lookupCounter struct {
	mu      sync.Mutex
	lookups int
	sends   int
}

func (l *lookupCounter) server(t *testing.T, found string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/conversations"):
			l.mu.Lock()
			l.lookups++
			l.mu.Unlock()
			w.WriteHeader(200)
			if found != "" {
				_, _ = w.Write([]byte(`{"results":[{"id":"` + found +
					`","featuredParticipants":[{"identifierValue":"+573001234567"}]}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			l.mu.Lock()
			l.sends++
			l.mu.Unlock()
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (l *lookupCounter) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lookups, l.sends
}

// 1. La caché en memoria gana: si ya sabemos la conversación del teléfono, no se sale a la red.
func TestSendText_UsesCachedConversationBeforeLookup(t *testing.T) {
	lc := &lookupCounter{}
	c := NewClientForTest(lc.server(t, "conv-remote").URL)
	c.CacheConversationID("+573001234567", "conv-cacheada")

	if _, err := c.SendText("+573001234567", "", "hola"); err != nil {
		t.Fatal(err)
	}

	lookups, sends := lc.counts()
	if lookups != 0 {
		t.Errorf("con la conversación en caché no debe consultarse la lista: hubo %d lookup(s)", lookups)
	}
	if sends != 1 {
		t.Errorf("esperaba 1 envío, hubo %d", sends)
	}
}

// 2. Un fallo se recuerda: el segundo envío al mismo número no repite el barrido de 10 páginas.
func TestSendText_DoesNotRepeatFailedLookup(t *testing.T) {
	lc := &lookupCounter{}
	c := NewClientForTest(lc.server(t, "").URL) // la lista nunca encuentra la conversación

	for i := 0; i < 4; i++ {
		if _, err := c.SendText("+573009999999", "", "hola"); err != nil {
			t.Fatal(err)
		}
	}

	lookups, sends := lc.counts()
	if lookups != 1 {
		t.Errorf("el fallo debe recordarse: esperaba 1 lookup para 4 envíos, hubo %d", lookups)
	}
	if sends != 4 {
		t.Errorf("los 4 mensajes deben salir igual (por Channels API), hubo %d", sends)
	}
}

// 3. El recuerdo del fallo NO puede dejar ciego al bot: en cuanto la conversación aparece (webhook
// de Bird → CacheConversationID), el siguiente envío la usa.
func TestCacheConversationID_ClearsRememberedMiss(t *testing.T) {
	lc := &lookupCounter{}
	c := NewClientForTest(lc.server(t, "").URL)

	if _, err := c.SendText("+573008888888", "", "primero"); err != nil {
		t.Fatal(err)
	}
	// Llega el webhook de conversación creada.
	c.CacheConversationID("+573008888888", "conv-nueva")

	if _, err := c.SendText("+573008888888", "", "segundo"); err != nil {
		t.Fatal(err)
	}
	if got := c.GetCachedConversationID("+573008888888"); got != "conv-nueva" {
		t.Errorf("la conversación cacheada debe seguir siendo la nueva, got %q", got)
	}
	if lookups, _ := lc.counts(); lookups != 1 {
		t.Errorf("tras conocerse la conversación no debe volver a buscarse: %d lookups", lookups)
	}
}

// 4. La ESCALACIÓN no se toca: sigue consultando la lista siempre. Fue el origen del bug de
// "empty conversation ID"; un fallo recordado no puede impedirle encontrar la conversación.
func TestLookupConversationByPhone_AlwaysQueriesForEscalation(t *testing.T) {
	lc := &lookupCounter{}
	c := NewClientForTest(lc.server(t, "").URL)

	for i := 0; i < 3; i++ {
		if _, err := c.LookupConversationByPhone("+573007777777"); err != nil {
			t.Fatal(err)
		}
	}
	if lookups, _ := lc.counts(); lookups != 3 {
		t.Errorf("LookupConversationByPhone debe consultar SIEMPRE (vía de escalación): %d de 3", lookups)
	}
}
