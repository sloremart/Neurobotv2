package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
)

// El replay del WAL (arranque y atascados) lee filas de message_inbox. Una fila de Néctar
// (msg_type nectar_notice) lleva un cuerpo que NO es un bird.WebhookEvent: parseado como tal
// deserializa en cero y sin error, y acabaría encolada como un mensaje sin teléfono.

const nectarInboundBody = `{"event_id":"e-1","kind":"mensaje_entrante","conversation_id":1234,
 "occurred_at":"2026-08-11T20:15:04Z",
 "message":{"id":55,"kind":"text","text":"hola"}}`

// fakeReplay registra lo que el replay hizo con cada fila.
type fakeReplay struct {
	mu          sync.Mutex
	dispatched  []string // raw_body de lo despachado como aviso de Néctar
	dispatchErr error
	enqueued    []bird.InboundMessage
	done        []string

	pending           bool // HasPending: el paciente tiene una notificación proactiva viva
	invalidInput      bool // HandleInvalidInput: ya reintentó el prompt
	rejectPostback    bool // AcceptsPostback: recordatorio que no salió + botón de otra plantilla
	responded         chan string
	invalidInputCalls int
}

func (f *fakeReplay) DispatchNectarNotice(_ context.Context, rawBody string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, rawBody)
	return f.dispatchErr
}

func (f *fakeReplay) Enqueue(msg bird.InboundMessage) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, msg)
	return true
}

func (f *fakeReplay) MarkDone(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done = append(f.done, id)
	return nil
}

func (f *fakeReplay) HasPending(string) bool { return f.pending }

func (f *fakeReplay) HandleResponse(_, payload, _ string) { f.responded <- payload }

func (f *fakeReplay) HandleInvalidInput(string, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidInputCalls++
	return f.invalidInput
}

func (f *fakeReplay) AcceptsPostback(string, string) bool { return !f.rejectPostback }

func (f *fakeReplay) deps() replayDeps {
	return replayDeps{nectar: f, notify: f, pool: f, inbox: f}
}

// FindPendingOlderThan hace de fakeReplay un staleInboxRepo.
type staleRows struct {
	*fakeReplay
	rows []localrepo.InboxRow
}

func (s staleRows) FindPendingOlderThan(context.Context, int) ([]localrepo.InboxRow, error) {
	return s.rows, nil
}

// birdTextBody es un webhook de Bird de un mensaje de texto, tal como lo guarda el WAL.
func birdTextBody(t *testing.T, id, phone, text string) string {
	t.Helper()
	event := bird.WebhookEvent{Payload: bird.WebhookPayload{
		ID:        id,
		Direction: "incoming",
		Sender:    bird.SenderInfo{Contacts: []bird.Contact{{IdentifierValue: phone}}},
		Body:      bird.MessageBody{Type: "text", Text: bird.TextBody{Text: text}},
	}}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func birdPostbackBody(t *testing.T, id, phone, payload string) string {
	t.Helper()
	var event bird.WebhookEvent
	raw := `{"payload":{"id":"` + id + `","direction":"incoming",
	  "sender":{"contacts":[{"identifierValue":"` + phone + `"}]},
	  "body":{"type":"text","text":{"text":"x","actions":[{"type":"postback","postback":{"payload":"` + payload + `"}}]}}}}`
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// La premisa de todo lo demás: un cuerpo de Néctar leído como evento de Bird NO falla, y produce
// un mensaje vacío que el replay habría encolado.
func TestNectarBodyAsBirdEventSilentlyYieldsAnEmptyMessage(t *testing.T) {
	var event bird.WebhookEvent
	if err := json.Unmarshal([]byte(nectarInboundBody), &event); err != nil {
		t.Fatalf("la premisa del bypass: el cuerpo de Néctar deserializa sin error como Bird (%v)", err)
	}
	if msg := bird.ParseInboundMessage(event); msg.Phone != "" || msg.ID != "" {
		t.Fatalf("y queda en cero: %+v", msg)
	}
}

func TestInboxReplay_NectarRowsBypassBirdParser(t *testing.T) {
	f := &fakeReplay{}
	rows := []localrepo.InboxRow{
		{ID: "e-1", Phone: "nectar:1234", RawBody: nectarInboundBody, MsgType: "nectar_notice"},
	}

	replayPendingRows(context.Background(), rows, f.deps())

	if len(f.dispatched) != 1 || f.dispatched[0] != nectarInboundBody {
		t.Fatalf("la fila de Néctar se despacha como aviso, entera: %v", f.dispatched)
	}
	if len(f.enqueued) != 0 {
		t.Fatalf("y NO pasa por el parser de Bird (encolaría un mensaje sin teléfono): %+v", f.enqueued)
	}
	if len(f.done) != 0 {
		t.Fatalf("un aviso legible lo cierra su despacho, no el replay: %v", f.done)
	}
}

func TestInboxReplay_UnreadableNectarRowIsClosed(t *testing.T) {
	f := &fakeReplay{dispatchErr: errors.New("aviso ilegible")}
	rows := []localrepo.InboxRow{{ID: "e-bad", RawBody: `no es json`, MsgType: "nectar_notice"}}

	replayPendingRows(context.Background(), rows, f.deps())

	if len(f.done) != 1 || f.done[0] != "e-bad" {
		t.Fatalf("una fila ilegible se cierra para no repetirla en cada arranque: %v", f.done)
	}
	if len(f.enqueued) != 0 {
		t.Fatalf("%+v", f.enqueued)
	}
}

// Las filas de Bird siguen exactamente como antes: parser de Bird, cola del pool.
func TestInboxReplay_BirdRowsStillGoThroughTheBirdParser(t *testing.T) {
	f := &fakeReplay{}
	rows := []localrepo.InboxRow{
		{ID: "b-1", Phone: "573001112233", RawBody: birdTextBody(t, "b-1", "573001112233", "hola"), MsgType: "text"},
		// El msg_type de una fila de Bird es el tipo del mensaje, nunca nectar_notice.
		{ID: "b-2", Phone: "573001112233", RawBody: birdTextBody(t, "b-2", "573001112233", "otra"), MsgType: "image"},
	}

	replayPendingRows(context.Background(), rows, f.deps())

	if len(f.dispatched) != 0 {
		t.Fatalf("Bird no se despacha como aviso de Néctar: %v", f.dispatched)
	}
	if len(f.enqueued) != 2 || f.enqueued[0].ID != "b-1" || f.enqueued[0].Phone != "573001112233" ||
		f.enqueued[0].Text != "hola" || f.enqueued[1].ID != "b-2" {
		t.Fatalf("los mensajes de Bird se encolan parseados: %+v", f.enqueued)
	}
}

func TestInboxReplay_UnparsableBirdRowIsClosed(t *testing.T) {
	f := &fakeReplay{}
	rows := []localrepo.InboxRow{{ID: "b-bad", RawBody: `no es json`, MsgType: "text"}}

	replayPendingRows(context.Background(), rows, f.deps())

	if len(f.done) != 1 || f.done[0] != "b-bad" || len(f.enqueued) != 0 {
		t.Fatalf("done=%v enqueued=%+v", f.done, f.enqueued)
	}
}

// Con una notificación proactiva viva, el postback de respuesta va al NotificationManager y no al
// worker (comportamiento de Bird del replay, intacto).
func TestInboxReplay_NotificationPostbackRoutedToNotifier(t *testing.T) {
	f := &fakeReplay{pending: true, responded: make(chan string, 1)}
	rows := []localrepo.InboxRow{
		{ID: "b-pb", RawBody: birdPostbackBody(t, "b-pb", "573001112233", "confirm"), MsgType: "text"},
	}

	replayPendingRows(context.Background(), rows, f.deps())

	select {
	case payload := <-f.responded:
		if payload != "confirm" {
			t.Fatalf("payload = %q", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el postback de la notificación no llegó al NotificationManager")
	}
	if len(f.enqueued) != 0 || len(f.done) != 1 || f.done[0] != "b-pb" {
		t.Fatalf("enqueued=%+v done=%v", f.enqueued, f.done)
	}
}

// Recordatorio que no salió + botón de otra plantilla: no va a HandleResponse; HandleInvalidInput no
// lo consume (false) y sigue al pool, como en el camino HTTP.
func TestInboxReplay_RejectedPostbackGoesToPool(t *testing.T) {
	f := &fakeReplay{pending: true, rejectPostback: true, responded: make(chan string, 1)}
	rows := []localrepo.InboxRow{
		{ID: "b-wl", RawBody: birdPostbackBody(t, "b-wl", "573001112233", "wl_schedule"), MsgType: "text"},
	}

	replayPendingRows(context.Background(), rows, f.deps())

	select {
	case payload := <-f.responded:
		t.Fatalf("no debía llegar a HandleResponse: %q", payload)
	default:
	}
	if len(f.enqueued) != 1 || len(f.done) != 0 {
		t.Fatalf("enqueued=%+v done=%v", f.enqueued, f.done)
	}
}

func TestInboxReplay_FreeTextDuringNotificationIsRetriedNotEnqueued(t *testing.T) {
	f := &fakeReplay{pending: true, invalidInput: true}
	rows := []localrepo.InboxRow{
		{ID: "b-txt", RawBody: birdTextBody(t, "b-txt", "573001112233", "no entiendo"), MsgType: "text"},
	}

	replayPendingRows(context.Background(), rows, f.deps())

	if f.invalidInputCalls != 1 || len(f.enqueued) != 0 || len(f.done) != 1 {
		t.Fatalf("calls=%d enqueued=%+v done=%v", f.invalidInputCalls, f.enqueued, f.done)
	}
}

// Sin NotificationManager (arranque sin BD de SIESA) el replay encola sin clasificar.
func TestInboxReplay_WithoutNotifierEnqueuesBirdRows(t *testing.T) {
	f := &fakeReplay{}
	deps := f.deps()
	deps.notify = nil
	rows := []localrepo.InboxRow{
		{ID: "b-1", RawBody: birdTextBody(t, "b-1", "573001112233", "hola"), MsgType: "text"},
	}

	replayPendingRows(context.Background(), rows, deps)

	if len(f.enqueued) != 1 {
		t.Fatalf("%+v", f.enqueued)
	}
}

// El segundo replay (filas atascadas, StartStaleReplay) enruta igual que el de arranque.
func TestStaleReplay_NectarRowsBypassBirdParser(t *testing.T) {
	f := &fakeReplay{}
	src := inboxReplaySource{
		nectar: f,
		repo: staleRows{fakeReplay: f, rows: []localrepo.InboxRow{
			{ID: "e-1", RawBody: nectarInboundBody, MsgType: "nectar_notice"},
			{ID: "b-1", RawBody: birdTextBody(t, "b-1", "573001112233", "hola"), MsgType: "text"},
		}},
	}

	msgs, err := src.PendingOlderThan(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}

	if len(f.dispatched) != 1 || f.dispatched[0] != nectarInboundBody {
		t.Fatalf("la fila de Néctar se despacha como aviso: %v", f.dispatched)
	}
	if len(msgs) != 1 || msgs[0].ID != "b-1" || msgs[0].Phone != "573001112233" {
		t.Fatalf("solo la fila de Bird vuelve como mensaje del parser de Bird: %+v", msgs)
	}
	if len(f.done) != 0 {
		t.Fatalf("%v", f.done)
	}
}

func TestStaleReplay_UnreadableNectarRowIsClosed(t *testing.T) {
	f := &fakeReplay{dispatchErr: errors.New("aviso ilegible")}
	src := inboxReplaySource{
		nectar: f,
		repo: staleRows{fakeReplay: f, rows: []localrepo.InboxRow{
			{ID: "e-bad", RawBody: `no es json`, MsgType: "nectar_notice"},
		}},
	}

	msgs, err := src.PendingOlderThan(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 || len(f.done) != 1 || f.done[0] != "e-bad" {
		t.Fatalf("msgs=%+v done=%v", msgs, f.done)
	}
}

// captureLogs redirige slog a un búfer mientras corre fn.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	fn()
	return logged.String()
}

// Las dos rutas de replay despachan una fila de Néctar por el MISMO camino y lo dejan dicho en el
// registro con su prefijo: el barrido de atascados no puede ser invisible para las filas de Néctar,
// y el mensaje de error de cada ruta se conserva tal cual.
func TestReplayNectarRow_LogsTheRedispatchAndKeepsEachPathsErrorString(t *testing.T) {
	for _, c := range []struct {
		name   string
		prefix string
		run    func(f *fakeReplay, row localrepo.InboxRow)
	}{
		{"startup replay", "inbox replay", func(f *fakeReplay, row localrepo.InboxRow) {
			replayPendingRows(context.Background(), []localrepo.InboxRow{row}, f.deps())
		}},
		{"stale replay", "stale replay", func(f *fakeReplay, row localrepo.InboxRow) {
			src := inboxReplaySource{nectar: f, repo: staleRows{fakeReplay: f, rows: []localrepo.InboxRow{row}}}
			if _, err := src.PendingOlderThan(context.Background(), 5); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := localrepo.InboxRow{ID: "e-log", RawBody: nectarInboundBody, MsgType: "nectar_notice"}

			f := &fakeReplay{}
			logged := captureLogs(t, func() { c.run(f, row) })
			if !strings.Contains(logged, c.prefix+": re-despachando aviso de Néctar") || !strings.Contains(logged, "id=e-log") {
				t.Errorf("falta el Info del re-despacho con su prefijo %q y el id: %s", c.prefix, logged)
			}
			if strings.Contains(logged, "ilegible") {
				t.Errorf("un aviso legible no registra error: %s", logged)
			}

			bad := &fakeReplay{dispatchErr: errors.New("aviso ilegible")}
			logged = captureLogs(t, func() { c.run(bad, row) })
			if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, c.prefix+": aviso de Néctar ilegible") {
				t.Errorf("el error conserva su texto %q: %s", c.prefix+": aviso de Néctar ilegible", logged)
			}
			if len(bad.done) != 1 || bad.done[0] != "e-log" {
				t.Errorf("y la fila ilegible se cierra: %v", bad.done)
			}
		})
	}
}

// Con el puente apagado, DispatchNectarNotice no despacha y devuelve bird.ErrNectarBridgeOff: las
// dos rutas de replay cierran la fila (no se reintenta en cada corrida ni al volver el puente) y no
// la registran como error (el Warn bridge_off ya lo dejó el despachador; un Error subiría a Telegram).
func TestReplayNectarRow_BridgeOffClosesRowWithoutError(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(f *fakeReplay, row localrepo.InboxRow)
	}{
		{"startup replay", func(f *fakeReplay, row localrepo.InboxRow) {
			replayPendingRows(context.Background(), []localrepo.InboxRow{row}, f.deps())
		}},
		{"stale replay", func(f *fakeReplay, row localrepo.InboxRow) {
			src := inboxReplaySource{nectar: f, repo: staleRows{fakeReplay: f, rows: []localrepo.InboxRow{row}}}
			if msgs, err := src.PendingOlderThan(context.Background(), 5); err != nil || len(msgs) != 0 {
				t.Fatalf("msgs=%+v err=%v", msgs, err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeReplay{dispatchErr: bird.ErrNectarBridgeOff}
			row := localrepo.InboxRow{ID: "e-off", RawBody: nectarInboundBody, MsgType: "nectar_notice"}
			logged := captureLogs(t, func() { c.run(f, row) })
			if len(f.done) != 1 || f.done[0] != "e-off" {
				t.Fatalf("la fila se cierra: %v", f.done)
			}
			if len(f.enqueued) != 0 {
				t.Fatalf("nada al pool: %+v", f.enqueued)
			}
			if strings.Contains(logged, "level=ERROR") || strings.Contains(logged, "ilegible") {
				t.Errorf("puente apagado no es un aviso ilegible: %s", logged)
			}
		})
	}
}
