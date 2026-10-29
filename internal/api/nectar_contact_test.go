package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
)

// El contacto de la conversación (D2): el bot pregunta a Néctar a quién le habla (GET …/state,
// bloque `contact`) UNA vez por conversación y lo lleva en cada mensaje hacia el worker.

func inboundBodyConv(eventID string, conversationID int64) string {
	return `{"event_id":"` + eventID + `","kind":"mensaje_entrante","conversation_id":` +
		strconv.FormatInt(conversationID, 10) + `,
	  "occurred_at":"2026-08-11T20:15:04Z",
	  "message":{"id":55,"external_id":"wamid.X","sequence":7,"kind":"text","text":"hola"}}`
}

// newNectarContactHandler es newNectarHandler con el puente de Néctar encendido sobre un falso.
func newNectarContactHandler(inbox InboxPersister, sink *sinkRecorder, fb *fakenectar.Bridge) *WebhookHandler {
	h := newNectarHandler(inbox, sink)
	h.birdClient.SetNectar(fb)
	return h
}

func TestNectarInbound_FetchesContactOncePerConversation(t *testing.T) {
	sink := &sinkRecorder{}
	fb := &fakenectar.Bridge{ContactValue: bird.NectarContact{Identifier: "573001112233"}}
	h := newNectarContactHandler(newMemInbox(), sink, fb)

	for _, id := range []string{"e-c1", "e-c2"} {
		if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody(id))); rec.Code != http.StatusOK {
			t.Fatalf("%s → %d", id, rec.Code)
		}
	}
	if asks := fb.ContactQuestions(); len(asks) != 1 || asks[0] != 1234 {
		t.Fatalf("dos avisos de la misma conversación → UNA pregunta por el contacto, got %v", asks)
	}
	if m, _ := sink.counts(); m != 2 {
		t.Fatalf("los dos mensajes siguen al worker, got %d", m)
	}
	for _, msg := range sink.messages {
		if msg.ContactIdentifier != "573001112233" {
			t.Fatalf("cada mensaje lleva el contacto, got %q", msg.ContactIdentifier)
		}
	}

	// Otra conversación es otra pregunta.
	if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBodyConv("e-c3", 99))); rec.Code != http.StatusOK {
		t.Fatalf("→ %d", rec.Code)
	}
	if asks := fb.ContactQuestions(); len(asks) != 2 || asks[1] != 99 {
		t.Fatalf("otra conversación pregunta de nuevo, got %v", asks)
	}

	// Número oculto: viaja el BSUID.
	sink2 := &sinkRecorder{}
	h2 := newNectarContactHandler(newMemInbox(), sink2, &fakenectar.Bridge{
		ContactValue: bird.NectarContact{BSUID: "CO.ABC123"},
	})
	postNotice(h2, signedNotice(nectarTestSecret, inboundBody("e-c4")))
	if m, _ := sink2.counts(); m != 1 || sink2.messages[0].ContactIdentifier != "CO.ABC123" {
		t.Fatalf("sin número el contacto es el BSUID: %+v", sink2.messages)
	}
}

func TestNectarInbound_ContactFailureStillReachesWorker(t *testing.T) {
	sink := &sinkRecorder{}
	fb := &fakenectar.Bridge{ContactErr: errors.New("néctar caída")}
	h := newNectarContactHandler(newMemInbox(), sink, fb)

	events := captureFlowEvents(t, func() {
		postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-f1")))
	})
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("un fallo del contacto no detiene el mensaje, got %d", m)
	}
	if got := sink.messages[0].ContactIdentifier; got != "" {
		t.Fatalf("sin contacto el campo va vacío, got %q", got)
	}
	mustFindEvent(t, events, "nectar", "contacto_no_disponible")

	// El fallo no se guarda en la caché: el siguiente mensaje vuelve a preguntar.
	fb.ContactErr = nil
	fb.ContactValue = bird.NectarContact{Identifier: "573001112233"}
	postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-f2")))
	if asks := fb.ContactQuestions(); len(asks) != 2 {
		t.Fatalf("tras un fallo se vuelve a preguntar, got %v", asks)
	}
	if got := sink.messages[1].ContactIdentifier; got != "573001112233" {
		t.Fatalf("got %q", got)
	}
}

func TestNectarInbound_Whitelist(t *testing.T) {
	cases := []struct {
		name      string
		whitelist []string
		contact   bird.NectarContact
		passes    bool
	}{
		{"contacto distinto se descarta", []string{"573001112233"}, bird.NectarContact{Identifier: "573009998877"}, false},
		{"contacto en la lista pasa", []string{"573001112233"}, bird.NectarContact{Identifier: "573001112233"}, true},
		{"la lista con «+» casa el contacto sin «+»", []string{"+573001112233"}, bird.NectarContact{Identifier: "573001112233"}, true},
		{"contacto vacío pasa (aísla el workspace de pruebas)", []string{"573001112233"}, bird.NectarContact{}, true},
		{"solo BSUID pasa", []string{"573001112233"}, bird.NectarContact{BSUID: "CO.ABC123"}, true},
		{"lista vacía: todo pasa", nil, bird.NectarContact{Identifier: "573009998877"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &sinkRecorder{}
			inbox := newMemInbox()
			h := newNectarContactHandler(inbox, sink, &fakenectar.Bridge{ContactValue: tc.contact})
			h.cfg.TestingWhitelistPhones = tc.whitelist

			var logged bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
			rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-w")))
			slog.SetDefault(previous)

			if rec.Code != http.StatusOK {
				t.Fatalf("→ %d", rec.Code)
			}
			m, _ := sink.counts()
			if tc.passes {
				if m != 1 {
					t.Fatalf("debía pasar al worker, got %d", m)
				}
				return
			}
			if m != 0 {
				t.Fatalf("debía descartarse, got %d mensajes", m)
			}
			if !inbox.isDone("e-w") {
				t.Fatal("el descarte cierra la fila del WAL (si no, el replay lo despacharía otra vez)")
			}
			out := logged.String()
			if !strings.Contains(out, "phone not whitelisted, ignoring") {
				t.Fatalf("mismo log que Bird: %s", out)
			}
			if strings.Contains(out, "573009998877") {
				t.Fatalf("el contacto va enmascarado en el log: %s", out)
			}
		})
	}
}
