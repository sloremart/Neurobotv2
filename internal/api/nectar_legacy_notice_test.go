package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// Línea vieja de Néctar: ruta propia de avisos y registro de la línea ANTES del WAL.

const (
	legacyNoticeSecret = "secreto-de-avisos-de-la-linea-vieja"
	nectarLegacyPath   = "/api/webhooks/nectar/legacy"
	nectarLegacyURL    = "https://" + nectarHost + nectarLegacyPath
)

// orderLog guarda, en orden, lo que el registro de línea y el WAL hicieron.
type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (o *orderLog) add(e string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func (o *orderLog) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

// recordingLineStore es un LineStore en memoria que anota cada Register en el orderLog.
type recordingLineStore struct {
	log   *orderLog
	lines map[int64]nectar.Line
	err   error
}

func (s *recordingLineStore) LineOf(_ context.Context, id int64) (nectar.Line, bool, error) {
	l, ok := s.lines[id]
	return l, ok, nil
}

func (s *recordingLineStore) Register(_ context.Context, id int64, l nectar.Line) error {
	if s.err != nil {
		return s.err
	}
	s.log.add("register:" + string(l))
	s.lines[id] = l
	return nil
}

// loggingInbox anota en el mismo orderLog cada insert al WAL.
type loggingInbox struct {
	*memInbox
	log *orderLog
}

func (i *loggingInbox) InsertIfNotExists(ctx context.Context, id, key, raw, msgType string, at time.Time) (bool, error) {
	i.log.add("wal")
	return i.memInbox.InsertIfNotExists(ctx, id, key, raw, msgType, at)
}

// newLegacyHandler arma un handler con la línea vieja configurada y el registro dado.
func newLegacyHandler(inbox InboxPersister, sink *sinkRecorder, reg *nectar.LineRegistry) *WebhookHandler {
	h := newNectarHandler(inbox, sink)
	h.cfg = &config.Config{
		NectarNoticeSecret: nectarTestSecret, NectarLegacyNoticeSecret: legacyNoticeSecret,
		NectarLegacyServiceKey: "kv", NectarLegacyChannelID: 9,
	}
	h.SetNectarLines(reg)
	return h
}

func signedLegacyNotice(secret, body string) *http.Request {
	ts := nectar.Timestamp(time.Now())
	req := httptest.NewRequest(http.MethodPost, nectarLegacyPath, strings.NewReader(body))
	req.Host = nectarHost
	req.Header.Set(nectar.TimestampHeader, ts)
	req.Header.Set(nectar.SignatureHeader, nectar.Sign(secret, ts, nectarLegacyURL, []byte(body)))
	return req
}

func postLegacyNotice(h *WebhookHandler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.HandleNectarLegacyNotice(rec, req)
	return rec
}

func TestLegacyNotice_NotConfiguredIs404(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink) // sin NECTAR_LEGACY_*
	if rec := postLegacyNotice(h, signedLegacyNotice(nectarTestSecret, inboundBody("l-1"))); rec.Code != http.StatusNotFound {
		t.Fatalf("sin línea vieja → 404, got %d", rec.Code)
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Fatal("la ruta apagada no despacha nada")
	}
}

func TestLegacyNotice_ConfiguredBridgeOffIs200Discard(t *testing.T) {
	inbox, sink := newMemInbox(), &sinkRecorder{}
	log := &orderLog{}
	store := &recordingLineStore{log: log, lines: map[int64]nectar.Line{}}
	h := newLegacyHandler(inbox, sink, nectar.NewLineRegistry(store))
	h.SetNectarBridgeOn(false)

	rec := postLegacyNotice(h, signedLegacyNotice(legacyNoticeSecret, inboundBody("l-off")))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "bridge_off") {
		t.Fatalf("puente apagado → 200 ignored, got %d %s", rec.Code, rec.Body.String())
	}
	if len(inbox.rows) != 0 || len(log.snapshot()) != 0 {
		t.Fatalf("nada en el WAL ni en el registro: rows=%v log=%v", inbox.rows, log.snapshot())
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Fatal("nada al pool")
	}
	// Y la firma se sigue verificando con el puente apagado.
	if rec := postLegacyNotice(h, signedLegacyNotice("ajeno", inboundBody("l-off2"))); rec.Code != http.StatusUnauthorized {
		t.Fatalf("firma ajena → 401, got %d", rec.Code)
	}
}

func TestLegacyNotice_RegistersLegacyBeforeWAL(t *testing.T) {
	log := &orderLog{}
	store := &recordingLineStore{log: log, lines: map[int64]nectar.Line{}}
	inbox := &loggingInbox{memInbox: newMemInbox(), log: log}
	h := newLegacyHandler(inbox, &sinkRecorder{}, nectar.NewLineRegistry(store))

	if rec := postLegacyNotice(h, signedLegacyNotice(legacyNoticeSecret, inboundBody("l-2"))); rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	got := log.snapshot()
	if len(got) != 2 || got[0] != "register:legacy" || got[1] != "wal" {
		t.Fatalf("orden = %v, quiero [register:legacy wal]", got)
	}
	if store.lines[1234] != nectar.LineLegacy {
		t.Fatalf("línea = %q", store.lines[1234])
	}
}

func TestLegacyNotice_RegistryFailureIs500AndNoWAL(t *testing.T) {
	log := &orderLog{}
	store := &recordingLineStore{log: log, lines: map[int64]nectar.Line{}, err: errors.New("bd caída")}
	inbox := &loggingInbox{memInbox: newMemInbox(), log: log}
	sink := &sinkRecorder{}
	h := newLegacyHandler(inbox, sink, nectar.NewLineRegistry(store))

	rec := postLegacyNotice(h, signedLegacyNotice(legacyNoticeSecret, inboundBody("l-3")))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("registro caído → 500, got %d", rec.Code)
	}
	if len(inbox.rows) != 0 || len(log.snapshot()) != 0 {
		t.Fatalf("sin registro no hay WAL: rows=%v log=%v", inbox.rows, log.snapshot())
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Fatal("sin registro no se despacha")
	}
}

func TestLegacyNotice_VerifiesWithLegacySecret(t *testing.T) {
	h := newLegacyHandler(newMemInbox(), &sinkRecorder{}, nil)
	if rec := postLegacyNotice(h, signedLegacyNotice(nectarTestSecret, inboundBody("l-4"))); rec.Code != http.StatusUnauthorized {
		t.Fatalf("el secreto principal no vale en la ruta vieja → 401, got %d", rec.Code)
	}
	if rec := postLegacyNotice(h, signedLegacyNotice(legacyNoticeSecret, inboundBody("l-5"))); rec.Code != http.StatusOK {
		t.Fatalf("el secreto viejo vale → 200, got %d", rec.Code)
	}
}

func TestMainNotice_RegistersMainWhenUnknown(t *testing.T) {
	log := &orderLog{}
	store := &recordingLineStore{log: log, lines: map[int64]nectar.Line{}}
	inbox := &loggingInbox{memInbox: newMemInbox(), log: log}
	h := newNectarHandler(inbox, &sinkRecorder{})
	h.SetNectarLines(nectar.NewLineRegistry(store))

	if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("m-1"))); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	got := log.snapshot()
	if len(got) != 2 || got[0] != "register:main" || got[1] != "wal" {
		t.Fatalf("orden = %v, quiero [register:main wal]", got)
	}
	// Una conversación ya conocida no vuelve a registrarse.
	if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("m-2"))); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if n := len(log.snapshot()); n != 3 { // +1 wal, ningún register nuevo
		t.Fatalf("el segundo aviso no debe registrar: %v", log.snapshot())
	}
}

// Con la línea vieja y su propio secreto de avisos, las capacidades valen con la firma de cualquiera
// de las dos líneas; con otra, 401.
func TestCapabilities_AcceptsEitherSecret(t *testing.T) {
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret, NectarLegacyNoticeSecret: "secreto-vieja"}
	mux := capabilitiesMux(cfg)
	for secret, want := range map[string]int{
		capabilitiesTestSecret: http.StatusOK,
		"secreto-vieja":        http.StatusOK,
		"otro":                 http.StatusUnauthorized,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, signedCapabilitiesRequest(http.MethodGet, secret))
		if rec.Code != want {
			t.Fatalf("secreto %q: got %d, quiero %d", secret, rec.Code, want)
		}
	}
}
