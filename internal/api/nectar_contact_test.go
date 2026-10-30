package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

	// El fallo se recuerda poco (caché negativa): dentro de la ventana no se vuelve a preguntar
	// —un Néctar colgado no frena cada mensaje ni el replay de arranque— y el mensaje sigue igual.
	now := time.Now()
	h.nectarContacts.now = func() time.Time { return now }
	fb.ContactErr = nil
	fb.ContactValue = bird.NectarContact{Identifier: "573001112233"}
	postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-f2")))
	if asks := fb.ContactQuestions(); len(asks) != 1 {
		t.Fatalf("dentro de la ventana del fallo no se pregunta otra vez, got %v", asks)
	}
	if m, _ := sink.counts(); m != 2 || sink.messages[1].ContactIdentifier != "" {
		t.Fatalf("el mensaje sigue sin contacto: %+v", sink.messages)
	}

	// Pasada la ventana, se vuelve a preguntar.
	now = now.Add(nectarContactFailTTL + time.Second)
	postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-f3")))
	if asks := fb.ContactQuestions(); len(asks) != 2 {
		t.Fatalf("pasada la ventana se vuelve a preguntar, got %v", asks)
	}
	if got := sink.messages[2].ContactIdentifier; got != "573001112233" {
		t.Fatalf("got %q", got)
	}
}

// Néctar inalcanzable (fallo de transporte): durante la ventana no se pregunta por NINGUNA
// conversación. Sin esto, el replay de arranque —síncrono, antes de abrir el puerto— gastaría el
// timeout entero en cada fila de una conversación distinta.
func TestNectarInbound_UnreachableShortCircuitsOtherConversations(t *testing.T) {
	sink := &sinkRecorder{}
	fb := &fakenectar.Bridge{ContactErr: fmt.Errorf("nectar: GET state: %w",
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})}
	h := newNectarContactHandler(newMemInbox(), sink, fb)
	now := time.Now()
	h.nectarContacts.now = func() time.Time { return now }

	postNotice(h, signedNotice(nectarTestSecret, inboundBodyConv("e-u1", 1234)))
	postNotice(h, signedNotice(nectarTestSecret, inboundBodyConv("e-u2", 99)))
	if asks := fb.ContactQuestions(); len(asks) != 1 {
		t.Fatalf("Néctar inalcanzable: la segunda conversación no pregunta, got %v", asks)
	}
	if m, _ := sink.counts(); m != 2 {
		t.Fatalf("los mensajes siguen igual, got %d", m)
	}

	now = now.Add(nectarContactFailTTL + time.Second)
	postNotice(h, signedNotice(nectarTestSecret, inboundBodyConv("e-u3", 99)))
	if asks := fb.ContactQuestions(); len(asks) != 2 {
		t.Fatalf("pasada la ventana se vuelve a preguntar, got %v", asks)
	}
}

// blockingContactBridge retiene la PRIMERA pregunta por el contacto hasta que la prueba la suelta.
type blockingContactBridge struct {
	*fakenectar.Bridge
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (b *blockingContactBridge) Contact(_ context.Context, _ int64) (bird.NectarContact, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
		<-b.release
	}
	return bird.NectarContact{Identifier: "573001112233"}, nil
}

// Dos avisos seguidos de una conversación sin contacto en caché llegan al worker EN EL ORDEN EN
// QUE LLEGARON, aunque el primero espere su GET …/state: un «hola» y la foto de la orden no se
// pueden cruzar. Y el segundo aprovecha la respuesta del primero (una sola pregunta).
func TestNectarInbound_ConcurrentNoticesKeepArrivalOrder(t *testing.T) {
	sink := &sinkRecorder{}
	bb := &blockingContactBridge{Bridge: &fakenectar.Bridge{}, started: make(chan struct{}), release: make(chan struct{})}
	h := newNectarHandler(newMemInbox(), sink)
	h.birdClient.SetNectar(bb)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-A"))) }()
	<-bb.started // el primero está esperando a Néctar
	go func() { defer wg.Done(); postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-B"))) }()
	// El segundo ya hace fila detrás del primero.
	deadline := time.Now().Add(5 * time.Second)
	for h.nectarOrder.waiting(1234) < 2 {
		if time.Now().After(deadline) {
			close(bb.release)
			wg.Wait()
			t.Fatalf("el segundo aviso no hizo fila detrás del primero; llegaron: %+v", sink.messages)
		}
		time.Sleep(time.Millisecond)
	}
	close(bb.release)
	wg.Wait()

	if m, _ := sink.counts(); m != 2 || sink.messages[0].ID != "e-A" || sink.messages[1].ID != "e-B" {
		t.Fatalf("orden de llegada: %+v", sink.messages)
	}
	if n := bb.calls.Load(); n != 1 {
		t.Fatalf("una sola pregunta por el contacto, got %d", n)
	}
	for _, msg := range sink.messages {
		if msg.ContactIdentifier != "573001112233" {
			t.Fatalf("los dos llevan el contacto: %+v", msg)
		}
	}
	if n := h.nectarOrder.waiting(1234); n != 0 {
		t.Fatalf("la fila se limpia al terminar, quedan %d", n)
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

// Recordatorio que no salió (pending bajo el teléfono «+57…»): con Néctar, el mensaje del paciente
// llega bajo "nectar:<id>" y adopta ese pending por el contacto de la conversación, para que su
// respuesta cuente y el IVR no lo llame.

func phonePendingHandler(t *testing.T, contact bird.NectarContact) (*WebhookHandler, *sinkRecorder, *memInbox, *fakeNectarNotifier) {
	t.Helper()
	sink := &sinkRecorder{}
	inbox := newMemInbox()
	h := newNectarContactHandler(inbox, sink, &fakenectar.Bridge{ContactValue: contact})
	notify := newFakeNectarNotifier()
	notify.phonePending = map[string]bool{"+573001112233": true}
	h.SetNectarNotifier(notify)
	return h, sink, inbox, notify
}

func TestNectarInbound_PhonePendingAdopted_ButtonGoesToHandleResponse(t *testing.T) {
	h, sink, inbox, notify := phonePendingHandler(t, bird.NectarContact{Identifier: "573001112233"})

	if rec := postNotice(h, signedNotice(nectarTestSecret, buttonBody("e-ad-1", "Confirmar"))); rec.Code != http.StatusOK {
		t.Fatalf("→ %d", rec.Code)
	}
	if got := notify.adoptCalls(); len(got) != 1 || got[0] != [3]string{"+573001112233", "nectar:77", "77"} {
		t.Fatalf("AdoptPhonePending%v", got)
	}
	if got := notify.waitResponse(t); got != [3]string{"nectar:77", "confirm", "77"} {
		t.Fatalf("HandleResponse%v", got)
	}
	if m, _ := sink.counts(); m != 0 {
		t.Fatalf("el worker no recibe nada, got %d", m)
	}
	if !inbox.isDone("e-ad-1") {
		t.Error("la fila del WAL se cierra")
	}
}

// Recordatorio fallido: un texto libre sigue el flujo normal (regla del dueño). No se adopta el
// pending del teléfono —queda para el IVR— y el mensaje va al worker.
func TestNectarInbound_PhonePending_TextGoesToWorkerWithoutAdoption(t *testing.T) {
	h, sink, _, notify := phonePendingHandler(t, bird.NectarContact{Identifier: "573001112233"})

	postNotice(h, signedNotice(nectarTestSecret, inboundBodyConv("e-ad-2", 77)))
	if got := notify.adoptCalls(); len(got) != 0 {
		t.Fatalf("un texto libre no adopta, got %v", got)
	}
	if !notify.phonePending["+573001112233"] {
		t.Fatal("el pending sigue bajo el teléfono para el IVR")
	}
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("va al worker, got %d", m)
	}
}

// Un pending de recordatorio fallido ya adoptado bajo la conversación: el texto libre tampoco se
// consume (HandleInvalidInput devuelve false) y sigue al worker.
func TestNectarInbound_AdoptedUnsentPending_TextGoesToWorker(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarContactHandler(newMemInbox(), sink, &fakenectar.Bridge{ContactValue: bird.NectarContact{Identifier: "573001112233"}})
	notify := newFakeNectarNotifier("nectar:77")
	notify.unsent = map[string]bool{"nectar:77": true}
	h.SetNectarNotifier(notify)

	postNotice(h, signedNotice(nectarTestSecret, inboundBodyConv("e-ad-6", 77)))
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("el texto libre va al worker, got %d", m)
	}
}

// La guarda del manager (conversación con agente) rechaza: el mensaje sigue al worker, sin traducir.
func TestNectarInbound_PhonePendingRefused_GoesToWorker(t *testing.T) {
	h, sink, _, notify := phonePendingHandler(t, bird.NectarContact{Identifier: "573001112233"})
	notify.refuse = true

	postNotice(h, signedNotice(nectarTestSecret, buttonBody("e-ad-3", "Confirmar")))
	if m, _ := sink.counts(); m != 1 || sink.messages[0].PostbackPayload != "Confirmar" {
		t.Fatalf("sin adopción va al worker tal cual: %+v", sink.messages)
	}
	if len(notify.invalid) != 0 {
		t.Fatalf("sin adopción no hay entrada inválida: %v", notify.invalid)
	}
}

// Sin número de contacto (vacío o solo BSUID) no hay teléfono con qué buscar: no se intenta.
func TestNectarInbound_UnknownContact_NoAdoption(t *testing.T) {
	for _, c := range []bird.NectarContact{{}, {BSUID: "CO.ABC123"}} {
		h, sink, _, notify := phonePendingHandler(t, c)
		postNotice(h, signedNotice(nectarTestSecret, buttonBody("e-ad-4", "Confirmar")))
		if got := notify.adoptCalls(); len(got) != 0 {
			t.Fatalf("contacto %+v: no se intenta adoptar, got %v", c, got)
		}
		if m, _ := sink.counts(); m != 1 {
			t.Fatalf("contacto %+v: va al worker, got %d", c, m)
		}
	}
}

// Con pending bajo la conversación, el camino de hoy: ni se intenta adoptar.
func TestNectarInbound_SessionPending_NoAdoption(t *testing.T) {
	h, _, _, notify := phonePendingHandler(t, bird.NectarContact{Identifier: "573001112233"})
	notify.pending["nectar:77"] = true

	postNotice(h, signedNotice(nectarTestSecret, buttonBody("e-ad-5", "Confirmar")))
	if got := notify.waitResponse(t); got != [3]string{"nectar:77", "confirm", "77"} {
		t.Fatalf("HandleResponse%v", got)
	}
	if got := notify.adoptCalls(); len(got) != 0 {
		t.Fatalf("no se intenta adoptar, got %v", got)
	}
}
