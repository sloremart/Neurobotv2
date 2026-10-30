package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/utils"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

// POST /api/webhooks/nectar — el manejador de avisos (PARA-EL-EQUIPO-DEL-BOT.md §1).
// Método de la casa: cada propiedad se provoca con peticiones reales contra el handler y
// se cuenta lo que llegó a la cola — no se confía en un comentario.

const nectarTestSecret = "secreto-de-avisos-de-prueba"

// sinkRecorder cuenta lo que el manejador despachó hacia el pool.
type sinkRecorder struct {
	mu       sync.Mutex
	messages []bird.InboundMessage
	commands []worker.AgentCommand
}

func (s *sinkRecorder) Enqueue(m bird.InboundMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
	return true
}

func (s *sinkRecorder) EnqueueAgentCommand(c worker.AgentCommand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, c)
}

func (s *sinkRecorder) counts() (messages, commands int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages), len(s.commands)
}

// memInbox imita message_inbox: INSERT … ON DUPLICATE KEY → inserted=false la segunda vez.
type memInbox struct {
	mu      sync.Mutex
	rows    map[string]string // id → msg_type
	raw     map[string]string // id → raw_body (el cuerpo EXACTO que se persistió, p. ej. enmascarado)
	done    []string
	failing error
}

func newMemInbox() *memInbox { return &memInbox{rows: map[string]string{}, raw: map[string]string{}} }

func (m *memInbox) InsertIfNotExists(_ context.Context, id, _, rawBody, msgType string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failing != nil {
		return false, m.failing
	}
	if _, dup := m.rows[id]; dup {
		return false, nil
	}
	m.rows[id] = msgType
	m.raw[id] = rawBody
	return true, nil
}

func (m *memInbox) MarkDone(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done = append(m.done, id)
	return nil
}

func (m *memInbox) isDone(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.done {
		if d == id {
			return true
		}
	}
	return false
}

func newNectarHandler(inbox InboxPersister, sink *sinkRecorder) *WebhookHandler {
	cfg := &config.Config{NectarNoticeSecret: nectarTestSecret}
	h := NewWebhookHandler(bird.NewClient(cfg), nil, nil, cfg)
	if inbox != nil {
		h.SetInboxRepo(inbox)
	}
	h.SetNectarSink(sink)
	h.SetNectarBridgeOn(true)
	return h
}

const (
	nectarHost = "bot.test"
	nectarPath = "/api/webhooks/nectar"
	// nectarURL es la cadena que Néctar firma: reconstructFullURL arma https por defecto (el
	// túnel por el que entra Néctar es siempre https) con el anfitrión de r.Host.
	nectarURL = "https://" + nectarHost + nectarPath
)

// newNoticeRequest imita lo que el servidor real entrega al handler: r.URL con SOLO la ruta
// y el anfitrión en r.Host. Con la URL absoluta en r.URL, reconstructFullURL la duplicaría.
func newNoticeRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, nectarPath, strings.NewReader(body))
	req.Host = nectarHost
	return req
}

// signedNotice arma la petición tal como la manda Néctar: firmada con su secreto sobre
// ts + "\n" + URL completa + "\n" + SHA256(cuerpo).
func signedNotice(secret, body string) *http.Request {
	ts := nectar.Timestamp(time.Now())
	req := newNoticeRequest(body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(nectar.TimestampHeader, ts)
	req.Header.Set(nectar.SignatureHeader, nectar.Sign(secret, ts, nectarURL, []byte(body)))
	return req
}

func inboundBody(eventID string) string {
	return `{"event_id":"` + eventID + `","kind":"mensaje_entrante","conversation_id":1234,
	  "occurred_at":"2026-08-11T20:15:04Z",
	  "message":{"id":55,"external_id":"wamid.X","sequence":7,"kind":"text","text":"hola"}}`
}

func postNotice(h *WebhookHandler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.HandleNectarNotice(rec, req)
	return rec
}

// fakeFlowSink/captureFlowEvents: monta un Tracer real con un sink en memoria como tracer global
// mientras corre fn, y lo desmonta al salir para no contaminar tests que corren después en el
// mismo binario.
type fakeFlowSink struct {
	mu     sync.Mutex
	events []domain.FlowEvent
}

func (f *fakeFlowSink) InsertBatch(_ context.Context, evs []domain.FlowEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evs...)
	return nil
}

func captureFlowEvents(t *testing.T, fn func()) []domain.FlowEvent {
	t.Helper()
	sink := &fakeFlowSink{}
	tr := observability.New(sink, observability.LvFull)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Start(ctx); close(done) }()
	observability.Init(tr)
	teardown := sync.OnceFunc(func() {
		cancel()
		<-done
		observability.Init(nil)
	})
	t.Cleanup(teardown)

	fn()
	teardown()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]domain.FlowEvent(nil), sink.events...)
}

func hasEvent(events []domain.FlowEvent, flow, step string) bool {
	for _, ev := range events {
		if ev.Flow == flow && ev.Step == step {
			return true
		}
	}
	return false
}

// mustFindEvent devuelve el evento flow/step o falla la prueba si no está.
func mustFindEvent(t *testing.T, events []domain.FlowEvent, flow, step string) domain.FlowEvent {
	t.Helper()
	for _, ev := range events {
		if ev.Flow == flow && ev.Step == step {
			return ev
		}
	}
	t.Fatalf("falta %s/%s: %+v", flow, step, events)
	return domain.FlowEvent{}
}

func TestHandleNectarNotice_RejectsBadSignature(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	rec := postNotice(h, signedNotice("otro-secreto", inboundBody("e-1")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("firma ajena → 401, got %d", rec.Code)
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Fatal("una petición no verificada no puede despachar nada")
	}
}

func TestHandleNectarNotice_RejectsMissingHeaders(t *testing.T) {
	h := newNectarHandler(newMemInbox(), &sinkRecorder{})
	if rec := postNotice(h, newNoticeRequest(inboundBody("e-1"))); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin cabeceras → 401, got %d", rec.Code)
	}
}

func TestHandleNectarNotice_WithoutSecretIsClosed(t *testing.T) {
	// Falla cerrado: sin NECTAR_NOTICE_SECRET ni una petición "firmada" con la cadena vacía entra.
	h := newNectarHandler(newMemInbox(), &sinkRecorder{})
	h.cfg = &config.Config{}
	if rec := postNotice(h, signedNotice("", inboundBody("e-1"))); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin secreto → 401, got %d", rec.Code)
	}
}

// La cadena firmada es la URL TAL COMO LA VIO NÉCTAR: si un proxy declara http en
// X-Forwarded-Proto, reconstructFullURL la arma en http y la firma sobre http:// es la válida.
func TestHandleNectarNotice_ForwardedProtoDecidesTheSignedURL(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)

	body := inboundBody("e-http")
	ts := nectar.Timestamp(time.Now())
	req := newNoticeRequest(body)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set(nectar.TimestampHeader, ts)
	req.Header.Set(nectar.SignatureHeader,
		nectar.Sign(nectarTestSecret, ts, "http://"+nectarHost+nectarPath, []byte(body)))

	if rec := postNotice(h, req); rec.Code != http.StatusOK {
		t.Fatalf("firmado sobre la URL que declara el proxy → 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("got %d", m)
	}
}

func TestHandleNectarNotice_InboundMessageEnqueued(t *testing.T) {
	sink := &sinkRecorder{}
	inbox := newMemInbox()
	h := newNectarHandler(inbox, sink)

	if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-1"))); rec.Code != http.StatusOK {
		t.Fatalf("aviso válido → 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := sink.counts()
	if m != 1 {
		t.Fatalf("un mensaje_entrante se encola una vez, got %d", m)
	}
	got := sink.messages[0]
	if got.ID != "e-1" || got.Phone != "nectar:1234" || got.Text != "hola" || got.ConversationID != "1234" {
		t.Fatalf("mensaje mal traducido: %+v", got)
	}
	if inbox.rows["e-1"] != nectarInboxType {
		t.Fatalf("la fila del WAL debe ir marcada como aviso de Néctar (replay), got %q", inbox.rows["e-1"])
	}
	// El mensaje lo cierra el worker al terminar (processMessage): el despacho no lo marca done.
	if inbox.isDone("e-1") {
		t.Fatal("la fila de un mensaje entrante la cierra el worker, no el despacho")
	}
}

func TestHandleNectarNotice_DuplicateEventIsAcked(t *testing.T) {
	// "Recibir el mismo aviso dos veces es lo normal, no un ataque": Néctar reintenta
	// indefinidamente hasta un 2xx. Dos peticiones idénticas → un solo despacho y dos 200.
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	for i := 0; i < 2; i++ {
		if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-dup"))); rec.Code != http.StatusOK {
			t.Fatalf("intento %d → 200, got %d", i+1, rec.Code)
		}
	}
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("dedupe por event_id: un despacho, got %d", m)
	}
}

// Tras un reinicio la memoria de event_id está vacía y Néctar reintenta lo que nunca recibió con
// 200: el WAL es entonces la ÚNICA guarda contra contestarle dos veces al paciente. Un handler
// NUEVO (sin nada visto) con la fila ya en el WAL acusa el aviso sin despacharlo.
func TestHandleNectarNotice_WALDuplicateAfterRestartIsAcked(t *testing.T) {
	inbox := newMemInbox()
	if _, err := inbox.InsertIfNotExists(context.Background(), "e-restart", "nectar:1234",
		inboundBody("e-restart"), nectarInboxType, time.Now()); err != nil {
		t.Fatal(err)
	}

	sink := &sinkRecorder{}
	h := newNectarHandler(inbox, sink) // handler fresco: su memoria local no ha visto nada

	if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-restart"))); rec.Code != http.StatusOK {
		t.Fatalf("el reintento de un aviso ya persistido → 200, got %d", rec.Code)
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Fatalf("y NO se despacha otra vez: %d mensajes, %d comandos", m, c)
	}
}

func TestHandleNectarNotice_DedupesEvenWithoutWAL(t *testing.T) {
	// Sin inboxRepo (arranque sin BD, pruebas) la memoria local sigue deduplicando: el
	// peor desenlace —contestarle dos veces al paciente— no depende de que haya BD.
	sink := &sinkRecorder{}
	h := newNectarHandler(nil, sink)
	postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-mem")))
	postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-mem")))
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("sin WAL también se deduplica, got %d", m)
	}
}

func TestHandleNectarNotice_WALFailureIs500SoNectarRetries(t *testing.T) {
	sink := &sinkRecorder{}
	inbox := newMemInbox()
	inbox.failing = errors.New("mysql down")
	h := newNectarHandler(inbox, sink)
	if rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-db"))); rec.Code != http.StatusInternalServerError {
		t.Fatalf("WAL caído → 500 (Néctar reintenta), got %d", rec.Code)
	}
	if m, _ := sink.counts(); m != 0 {
		t.Fatal("sin persistir no se despacha (se perdería si el bot cae)")
	}
}

func TestHandleNectarNotice_ResolvedBecomesClose(t *testing.T) {
	sink := &sinkRecorder{}
	inbox := newMemInbox()
	h := newNectarHandler(inbox, sink)
	body := `{"event_id":"e-res","kind":"conversacion_resuelta","conversation_id":77,"occurred_at":"2026-08-11T20:15:04Z"}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	_, c := sink.counts()
	if c != 1 || sink.commands[0].Action != "close" || sink.commands[0].Phone != "nectar:77" {
		t.Fatalf("resuelta → comando close sobre la sesión de la conversación, got %+v", sink.commands)
	}
	// Sin motivo también la resolvió Néctar: el centinela le dice al worker que no se despida ni
	// cierre (vacío queda para el «cerrar» del menú de retornos, que sí se despide).
	if sink.commands[0].Reason != worker.CloseReasonResolved {
		t.Fatalf("resuelta sin motivo viaja con el centinela %q, got %q", worker.CloseReasonResolved, sink.commands[0].Reason)
	}
	// Un aviso sin mensaje no pasa por el worker, así que su fila del WAL se cierra aquí:
	// si no, el replay de arranque la volvería a despachar cada reinicio.
	if len(inbox.done) != 1 || inbox.done[0] != "e-res" {
		t.Fatalf("la fila del WAL debe quedar done, got %v", inbox.done)
	}
}

// Néctar (opción B, 2026-09-12) resuelve por su cuenta los casos que vencen en cola fuera de
// horario y lo dice con `motivo: cola_vencida`. El motivo viaja en el comando de cierre para
// que el worker lo registre; ausente, el cierre es el de siempre.
func TestHandleNectarNotice_ResolvedCarriesReason(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"event_id":"e-res-2","kind":"conversacion_resuelta","conversation_id":79,"occurred_at":"2026-09-12T20:15:04Z","motivo":"cola_vencida"}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	_, c := sink.counts()
	if c != 1 || sink.commands[0].Action != "close" || sink.commands[0].Reason != "cola_vencida" {
		t.Fatalf("el motivo de Néctar viaja en el comando close, got %+v", sink.commands)
	}
}

func TestHandleNectarNotice_ReturnedBecomesResume(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	// Con un campo del futuro (§6.2) dentro: el manejador tiene que seguir funcionando.
	body := `{"event_id":"e-ret","kind":"conversacion_devuelta","conversation_id":78,
	          "occurred_at":"2026-08-11T20:15:04Z","unlock_value":"ASK_DOCUMENT"}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	_, c := sink.counts()
	if c != 1 || sink.commands[0].Action != "resume" || sink.commands[0].Phone != "nectar:78" {
		t.Fatalf("devuelta → comando resume, got %+v", sink.commands)
	}
	if sink.commands[0].State != "" || sink.commands[0].ReturnValue != "" {
		t.Fatalf("el valor de desbloqueo es FUTURO declarado y sin decisión se reanuda como siempre: %+v",
			sink.commands[0])
	}
}

// §6.2 punto 2: la decisión del agente llega en conversacion_devuelta.return_value y viaja
// al worker en el comando, que es quien la traduce contra el paso (necesita la sesión).
func TestHandleNectarNotice_ReturnedBecomesResumeWithValue(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"event_id":"e-dec","kind":"conversacion_devuelta","conversation_id":78,
	          "occurred_at":"2026-08-24T10:00:00Z","return_value":"elig_data_ok"}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if _, c := sink.counts(); c != 1 {
		t.Fatalf("un comando, got %d", c)
	}
	cmd := sink.commands[0]
	if cmd.Action != "resume" || cmd.Phone != "nectar:78" || cmd.ReturnValue != "elig_data_ok" || cmd.ReturnV1 != nil {
		t.Fatalf("%+v", cmd)
	}
	if cmd.ChosenOption() != "elig_data_ok" {
		t.Fatalf("sin retorno v1 la opción elegida es el return_value: %q", cmd.ChosenOption())
	}
}

// EL RETORNO v1: la clave `retorno` del aviso viaja al worker en el comando, que es quien la
// traduce contra el paso. La recepción está SIEMPRE encendida: no depende de
// PROTOCOL_ESCALADO_VERSION.
func TestHandleNectarNotice_ReturnedV1(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"protocol":1,"event_id":"e-v1","kind":"conversacion_devuelta","conversation_id":80,
	          "occurred_at":"2026-09-14T10:00:00Z",
	          "retorno":{"opcion":"bot_reanudar","valores":{"documento":"1000000001"}}}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	cmd := sink.commands[0]
	if cmd.Action != "resume" || cmd.Phone != "nectar:80" {
		t.Fatalf("%+v", cmd)
	}
	if cmd.ReturnV1 == nil || cmd.ReturnV1.Option != "bot_reanudar" {
		t.Fatalf("el retorno v1 tiene que llegar entero al worker: %+v", cmd.ReturnV1)
	}
	if !strings.Contains(string(cmd.ReturnV1.Values), "1000000001") {
		t.Fatalf("con sus valores crudos: %s", cmd.ReturnV1.Values)
	}
	if cmd.ChosenOption() != "bot_reanudar" {
		t.Fatalf("la opción elegida es la `opcion` del retorno v1: %q", cmd.ChosenOption())
	}
}

// El alias heredado sigue llegando por `return_value` aunque el aviso hable el protocolo: lo
// origina el barredor de Néctar, no un menú.
func TestHandleNectarNotice_ReturnedQueueExpiredWithProtocolStaysDialect(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"protocol":1,"event_id":"e-v1-cola","kind":"conversacion_devuelta","conversation_id":81,
	          "occurred_at":"2026-09-14T10:00:00Z","return_value":"cola_vencida"}`
	postNotice(h, signedNotice(nectarTestSecret, body))
	cmd := sink.commands[0]
	if cmd.ReturnV1 != nil || cmd.ReturnValue != "cola_vencida" {
		t.Fatalf("%+v", cmd)
	}
}

// LOS TRES RETORNOS REALES DE NÉCTAR (volcado del 2026-09-14, generado por SU emisor) por el
// manejador ENTERO: firma, dedupe y despacho. Lo que se fija aquí es el primer tramo del
// enrutamiento —aviso → AgentCommand—.
//
// El fixture vive en internal/nectar/testdata: UNA sola copia, leída por ruta relativa.
func TestHandleNectarNotice_ReturnedV1FromRealDump(t *testing.T) {
	raw, err := os.ReadFile("../nectar/testdata/nectar-bot-return-notice-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var dump map[string]json.RawMessage
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		option    string
		hasValues bool
	}{
		// El alias heredado llega como `opcion` y SIN `return_value`: el volcado corrige lo que
		// decía el mensaje del acta, y el bot acepta las dos formas.
		"retorno_del_alias_heredado":  {option: "cola_vencida"},
		"retorno_repetible_dos_citas": {option: "bot_reanudar", hasValues: true},
		"retorno_simple":              {option: "bot_reanudar", hasValues: true},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			var envelope struct {
				Aviso json.RawMessage `json:"aviso"`
			}
			if err := json.Unmarshal(dump[name], &envelope); err != nil {
				t.Fatal(err)
			}
			sink := &sinkRecorder{}
			h := newNectarHandler(newMemInbox(), sink)
			if rec := postNotice(h, signedNotice(nectarTestSecret, string(envelope.Aviso))); rec.Code != http.StatusOK {
				t.Fatalf("código %d", rec.Code)
			}
			if _, c := sink.counts(); c != 1 {
				t.Fatalf("un comando, got %d", c)
			}
			cmd := sink.commands[0]
			if cmd.Action != "resume" || cmd.Phone != "nectar:1" {
				t.Fatalf("%+v", cmd)
			}
			if cmd.ReturnV1 == nil || cmd.ReturnV1.Option != want.option {
				t.Fatalf("retorno v1 = %+v", cmd.ReturnV1)
			}
			if (len(cmd.ReturnV1.Values) > 0) != want.hasValues {
				t.Fatalf("valores presentes = %v", len(cmd.ReturnV1.Values) > 0)
			}
		})
	}
}

// Y un aviso sin mensaje REINTENTADO se despacha una vez: el bot deduplica por el ID —no por el
// cuerpo— en DOS capas, el WAL y la memoria con TTL de 24 h. Dos reanudaciones serían dos turnos
// del bot sobre la misma devolución.
func TestHandleNectarNotice_RetriedNoticeWithoutMessageDispatchedOnce(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"protocol":1,"event_id":"e-idem","kind":"conversacion_devuelta","conversation_id":82,
	          "occurred_at":"2026-09-14T15:02:11Z","retorno":{"opcion":"bot_reanudar"}}`
	for i := 0; i < 3; i++ {
		if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
			t.Fatalf("intento %d → 200 (Néctar reintenta hasta un 2xx), got %d", i+1, rec.Code)
		}
	}
	if _, c := sink.counts(); c != 1 {
		t.Fatalf("dedupe por event_id: un solo comando, got %d", c)
	}
	// Y la memoria local sola basta: el peor desenlace no puede depender de que haya BD.
	noWAL := &sinkRecorder{}
	h = newNectarHandler(nil, noWAL)
	postNotice(h, signedNotice(nectarTestSecret, body))
	postNotice(h, signedNotice(nectarTestSecret, body))
	if _, c := noWAL.counts(); c != 1 {
		t.Fatalf("sin WAL también se deduplica, got %d", c)
	}
}

func TestHandleNectarNotice_UnknownKindAcked(t *testing.T) {
	sink := &sinkRecorder{}
	inbox := newMemInbox()
	h := newNectarHandler(inbox, sink)
	body := `{"event_id":"e-new","kind":"algo_nuevo","conversation_id":1,"occurred_at":"2026-08-11T20:15:04Z"}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("un kind desconocido se acusa con 200 (si no, reintento eterno), got %d", rec.Code)
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Fatal("y no se despacha")
	}
	if !inbox.isDone("e-new") {
		t.Fatal("y su fila del WAL se cierra, o el replay la repetiría en cada arranque")
	}
}

func TestHandleNectarNotice_UnreadableNotice400(t *testing.T) {
	h := newNectarHandler(newMemInbox(), &sinkRecorder{})
	// Firmado correctamente (es Néctar quien lo manda) pero sin event_id: no hay con qué deduplicar.
	body := `{"kind":"mensaje_entrante","conversation_id":1}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusBadRequest {
		t.Fatalf("aviso inutilizable → 400, got %d", rec.Code)
	}
}

func TestHandleNectarNotice_ButtonPressArrivesAsPostbackAndDedupes(t *testing.T) {
	// JSON de la enmienda 2026-08-21 por el manejador entero: firma, dedupe y despacho.
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"event_id":"9c1d4b70-3f2a-4c88-9a51-6b0e2d7f4a11","kind":"mensaje_entrante","conversation_id":1234,
	  "occurred_at":"2026-08-21T15:42:07Z","message":{"id":77,"external_id":"wamid.X","sequence":12,
	  "kind":"interactive","text":"Agendar cita de control","interactive_reply_id":"AGENDAR_CONTROL"}}`
	for i := 0; i < 2; i++ {
		if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
			t.Fatalf("intento %d → %d", i+1, rec.Code)
		}
	}
	m, _ := sink.counts()
	if m != 1 {
		t.Fatalf("una pulsación reintentada se despacha una vez, got %d", m)
	}
	got := sink.messages[0]
	if !got.IsPostback || got.PostbackPayload != "AGENDAR_CONTROL" || got.Phone != "nectar:1234" {
		t.Fatalf("%+v", got)
	}
}

// Ruling 7: lo que ya traduce ToInboundMessage llega al pool con el tipo que espera el
// interceptor de no soportados de Neuro (sticker y contact), nunca como imagen.
func TestHandleNectarNotice_StickerReachesThePoolAsUnsupported(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"event_id":"e-sticker","kind":"mensaje_entrante","conversation_id":1234,
	  "occurred_at":"2026-08-21T15:42:07Z","message":{"id":78,"kind":"sticker",
	  "attachment":{"id":"s1","url":"https://nectar.test/api/bot/attachments/s1"}}}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("got %d", m)
	}
	if got := sink.messages[0]; got.MessageType != "sticker" || got.ImageURL != "" {
		t.Fatalf("un sticker no es una foto de orden: %+v", got)
	}
}

// `occurred_at` mal formado → 200 y el aviso SE PROCESA. Antes, el tipo `time.Time` tumbaba el
// `Unmarshal` del sobre entero y el manejador contestaba 400: Néctar reintentaba con espera
// exponencial sin corte y el paciente se quedaba sin respuesta por una fecha que el bot no usa.
func TestHandleNectarNotice_MalformedOccurredAtStillProcessed(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"event_id":"e-fecha","kind":"mensaje_entrante","conversation_id":1234,
	  "occurred_at":"ayer por la tarde",
	  "message":{"id":55,"kind":"text","text":"hola"}}`
	if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
		t.Fatalf("fecha ilegible → 200, nunca 400: got %d (%s)", rec.Code, rec.Body.String())
	}
	m, _ := sink.counts()
	if m != 1 {
		t.Fatalf("y el mensaje del paciente SÍ llega a la cola, got %d", m)
	}
	if sink.messages[0].Text != "hola" || sink.messages[0].Phone != "nectar:1234" {
		t.Fatalf("el resto del sobre se lee entero: %+v", sink.messages[0])
	}
}

// DESACUERDO DE VERSIÓN, dirección Néctar → bot: ALERTA Y 200, NUNCA 400. El buzón de Néctar
// reintenta con espera exponencial SIN CORTE: un 400 por una versión mayor reintentaría para
// siempre y el paciente se quedaría sin respuesta por un número que el bot ni siquiera necesita
// para leer el aviso.
func TestHandleNectarNotice_UnknownProtocolProcessedAnyway(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"event_id":"e-v9","kind":"mensaje_entrante","conversation_id":1234,"protocol":9,
	  "occurred_at":"2026-08-11T20:15:04Z",
	  "message":{"id":55,"kind":"text","text":"hola"}}`
	events := captureFlowEvents(t, func() {
		if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
			t.Fatalf("versión mayor → 200 con alerta, nunca 400: got %d", rec.Code)
		}
	})
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("y el mensaje SÍ se despacha: %d", m)
	}
	ev := mustFindEvent(t, events, "protocolo", "version_desconocida")
	if ev.TraceID != observability.TraceSession(nectar.SessionKey(1234)) {
		t.Errorf("la traza cuelga de la conversación: %q", ev.TraceID)
	}
	if ev.Attrs["protocol"] != 9 || ev.Attrs["soportada"] != nectar.ProtocolMax {
		t.Errorf("la versión del aviso y la soportada viajan como attrs: %+v", ev.Attrs)
	}
}

// Y la versión que el bot sí conoce no cambia absolutamente nada: ni alerta.
func TestHandleNectarNotice_KnownProtocolChangesNothing(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	body := `{"event_id":"e-v1","kind":"mensaje_entrante","conversation_id":1234,"protocol":1,
	  "occurred_at":"2026-08-11T20:15:04Z",
	  "message":{"id":55,"kind":"text","text":"hola"}}`
	events := captureFlowEvents(t, func() {
		if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
			t.Fatalf("got %d", rec.Code)
		}
	})
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("%d", m)
	}
	if hasEvent(events, "protocolo", "version_desconocida") {
		t.Fatalf("la versión conocida no alerta: %+v", events)
	}
}

// LO QUE EL AGENTE TECLEÓ NO ENTRA EN EL REGISTRO. En el dialecto el `return_value` es
// `bot_dato:<lo que escribió una persona sobre un paciente>` —en ASK_DOCUMENT, su documento— o
// `bot_citas:{…}` con la cita completa: el registro puede decir que hubo decisión, nunca cuál.
// El comando al worker, en cambio, lleva el valor ENTERO: lo necesita para reanudar.
func TestHandleNectarNotice_AgentDecisionNeverLoggedWithItsContent(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		forbidden string
		want      string
	}{
		{"dato", "bot_dato:1000000001", "1000000001", "bot_dato:"},
		// Un token SIN prefijo lo eligió el agente de un menú del bot: vocabulario nuestro, se traza entero.
		{"token de menú", "bot_reanudar", "", "bot_reanudar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logged bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(previous)

			sink := &sinkRecorder{}
			h := newNectarHandler(newMemInbox(), sink)
			value, err := json.Marshal(c.value)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"protocol":1,"event_id":"e-traza-` + c.name + `","kind":"conversacion_devuelta",
			  "conversation_id":77,"occurred_at":"2026-09-14T15:02:11Z","return_value":` + string(value) + `}`
			if rec := postNotice(h, signedNotice(nectarTestSecret, body)); rec.Code != http.StatusOK {
				t.Fatalf("código %d", rec.Code)
			}
			line := logged.String()
			if c.forbidden != "" && strings.Contains(line, c.forbidden) {
				t.Fatalf("el registro trae lo que tecleó el agente (%q): %s", c.forbidden, line)
			}
			if !strings.Contains(line, c.want) {
				t.Fatalf("el registro debe traer la CLASE del token (%q): %s", c.want, line)
			}
			if _, cmds := sink.counts(); cmds != 1 {
				t.Fatalf("un comando, got %d", cmds)
			}
			if sink.commands[0].ReturnValue != c.value {
				t.Fatalf("el worker recibe el valor entero, got %q", sink.commands[0].ReturnValue)
			}
		})
	}
}

// --- EL CUARTO AVISO: conversacion_abierta_por_agente, siempre «agent» ---

// openedBody: el cuerpo sintético del cuarto aviso (agente abre la conversación con una plantilla).
const openedBody = `{"event_id":"e-ap-1","kind":"conversacion_abierta_por_agente","conversation_id":9001,
 "occurred_at":"2026-09-18T14:00:00Z",
 "opened":{"contact":{"identifier":"+570000000000","profile_name":"Paciente Prueba"},
  "channel_id":"ch-1","template_name":"cita_pendiente_contacto",
  "variables":{"1":"Paciente Prueba","2":"18 de septiembre"},
  "agent":{"id":"ag-7","name":"Asesor Prueba"}}}`

// En Neuro el bot NUNCA preabre una sesión a partir de una plantilla de agente: la respuesta es
// siempre «agent», con cuerpo, y el aviso solo deja rastro (WAL enmascarado cerrado + traza).
func TestHandleNectarNotice_OpenedByAgentAlwaysAgent(t *testing.T) {
	inbox := newMemInbox()
	sink := &sinkRecorder{}
	h := newNectarHandler(inbox, sink)

	var rec *httptest.ResponseRecorder
	events := captureFlowEvents(t, func() {
		rec = postNotice(h, signedNotice(nectarTestSecret, openedBody))
	})

	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"respuesta_va_a":"agent"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if m, c := sink.counts(); m != 0 || c != 0 {
		t.Errorf("el cuarto aviso NUNCA pasa por dispatchNectar ni toca una sesión: mensajes=%d comandos=%d", m, c)
	}

	// WAL: persistido, con la marca de aviso de Néctar y la PII enmascarada (Ley 1581).
	raw := inbox.raw["e-ap-1"]
	if raw == "" {
		t.Fatal("el cuarto aviso debe persistirse en el WAL")
	}
	if inbox.rows["e-ap-1"] != nectarInboxType {
		t.Errorf("msg_type = %q", inbox.rows["e-ap-1"])
	}
	if strings.Contains(raw, "+570000000000") || strings.Contains(raw, "Paciente Prueba") ||
		strings.Contains(raw, "18 de septiembre") {
		t.Errorf("PII sin máscara en el WAL: %s", raw)
	}
	var stored nectar.Notice
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("el WAL guarda un aviso legible: %v", err)
	}
	if want := utils.MaskPhone("+570000000000"); stored.Opened == nil || stored.Opened.Contact.Identifier != want {
		t.Errorf("identifier = %+v, se esperaba el enmascarado %q", stored.Opened, want)
	}
	// Lo que no es PII sí queda reconocible: se guarda la FORMA, no el dato.
	if !strings.Contains(raw, "cita_pendiente_contacto") || !strings.Contains(raw, "Asesor Prueba") {
		t.Errorf("el WAL debe conservar la plantilla y el agente: %s", raw)
	}
	// Y la fila se cierra en el acto: si no, el replay de arranque lo volvería a despachar.
	if !inbox.isDone("e-ap-1") {
		t.Errorf("la fila del WAL debe quedar done, got %v", inbox.done)
	}

	// La traza: solo_traza, colgada de la conversación (no hay sesión), con plantilla y agente.
	ev := mustFindEvent(t, events, "apertura", "solo_traza")
	if ev.TraceID != observability.TraceSession(nectar.SessionKey(9001)) {
		t.Errorf("trace = %q, se esperaba el de la conversación", ev.TraceID)
	}
	if ev.Attrs["plantilla"] != "cita_pendiente_contacto" {
		t.Errorf("attrs = %+v", ev.Attrs)
	}
	// El nombre del agente NO entra a la traza: es dato de una persona.
	if len(ev.Attrs) != 1 {
		t.Errorf("solo la plantilla viaja como attr: %+v", ev.Attrs)
	}
}

// Un reintento (respuesta perdida, timeout de Néctar) recibe la MISMA respuesta sin repetir el
// trabajo: ni segunda fila, ni segunda traza.
func TestHandleNectarNotice_OpenedByAgentRetryGetsSameAnswer(t *testing.T) {
	inbox := newMemInbox()
	h := newNectarHandler(inbox, &sinkRecorder{})

	events := captureFlowEvents(t, func() {
		for i := 0; i < 2; i++ {
			rec := postNotice(h, signedNotice(nectarTestSecret, openedBody))
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"respuesta_va_a":"agent"}` {
				t.Fatalf("intento %d: %d %s", i+1, rec.Code, rec.Body.String())
			}
		}
	})
	n := 0
	for _, ev := range events {
		if ev.Flow == "apertura" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("el reintento no vuelve a trazar: %d eventos apertura/*", n)
	}
	if len(inbox.done) != 1 {
		t.Fatalf("ni vuelve a cerrar la fila: %v", inbox.done)
	}
}

// `opened` ausente (o sin template_name) no es una apertura legible: se responde igual «agent»
// —nunca se pierde una respuesta— y se traza como dato de contrato.
func TestHandleNectarNotice_OpenedByAgentWithoutOpenedIsAgentAndTraced(t *testing.T) {
	h := newNectarHandler(newMemInbox(), &sinkRecorder{})
	body := `{"event_id":"e-sin-opened","kind":"conversacion_abierta_por_agente","conversation_id":1,
	  "occurred_at":"2026-09-18T14:00:00Z"}`
	var rec *httptest.ResponseRecorder
	events := captureFlowEvents(t, func() {
		rec = postNotice(h, signedNotice(nectarTestSecret, body))
	})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"respuesta_va_a":"agent"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !hasEvent(events, "apertura", "aviso_invalido") {
		t.Fatalf("falta apertura/aviso_invalido: %+v", events)
	}
	if hasEvent(events, "apertura", "solo_traza") {
		t.Fatalf("un aviso ilegible no es una decisión de enrutamiento: %+v", events)
	}
}

// Sin BD el cuarto aviso se responde igual: el WAL es para recuperación, no para decidir.
func TestHandleNectarNotice_OpenedByAgentWithoutWALStillAnswers(t *testing.T) {
	h := newNectarHandler(nil, &sinkRecorder{})
	rec := postNotice(h, signedNotice(nectarTestSecret, openedBody))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"respuesta_va_a":"agent"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Los otros tres avisos siguen acusándose con 200 y SIN cuerpo.
func TestHandleNectarNotice_OtherNoticesHaveNoBody(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	rec := postNotice(h, signedNotice(nectarTestSecret, inboundBody("e-sin-cuerpo")))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("un mensaje_entrante sigue respondiendo 200 SIN cuerpo: %d %q", rec.Code, rec.Body.String())
	}
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("y sigue encolándose como siempre, got %d", m)
	}
}

// --- El replay del WAL: DispatchNectarNotice enruta por kind ---

func TestDispatchNectarNotice_ReplayRoutesByKind(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantMessages int
		wantAction   string // "" = ningún comando
		wantDone     bool   // el despacho cierra la fila (los mensajes los cierra el worker)
		wantStep     string // traza apertura/<step>, "" = ninguna
	}{
		{
			name:         "inbound message goes to the pool and the worker closes the row",
			body:         inboundBody("e-r-1"),
			wantMessages: 1,
		},
		{
			name:       "resolved becomes close",
			body:       `{"event_id":"e-r-2","kind":"conversacion_resuelta","conversation_id":5}`,
			wantAction: "close", wantDone: true,
		},
		{
			name:       "returned becomes resume",
			body:       `{"event_id":"e-r-3","kind":"conversacion_devuelta","conversation_id":5,"return_value":"bot_x"}`,
			wantAction: "resume", wantDone: true,
		},
		{
			// El valor entero del cuarto aviso era la respuesta síncrona, que murió con el
			// request: se acusa y se ignora, sin colarse por ninguno de los otros tres.
			name:     "opened by agent is acked and ignored",
			body:     openedBody,
			wantDone: true, wantStep: "replay_perdida",
		},
		{
			name:     "unknown kind is acked and ignored",
			body:     `{"event_id":"e-r-5","kind":"algo_nuevo","conversation_id":5}`,
			wantDone: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inbox := newMemInbox()
			sink := &sinkRecorder{}
			h := newNectarHandler(inbox, sink)
			n, err := nectar.ParseNotice([]byte(c.body))
			if err != nil {
				t.Fatal(err)
			}

			events := captureFlowEvents(t, func() {
				if err := h.DispatchNectarNotice(context.Background(), c.body); err != nil {
					t.Fatalf("el replay de un aviso legible no puede fallar: %v", err)
				}
			})

			m, cmds := sink.counts()
			if m != c.wantMessages {
				t.Errorf("mensajes = %d, quiero %d", m, c.wantMessages)
			}
			if (c.wantAction == "") != (cmds == 0) || cmds > 1 {
				t.Fatalf("comandos = %d, quiero acción %q", cmds, c.wantAction)
			}
			if c.wantAction != "" {
				if got := sink.commands[0]; got.Action != c.wantAction || got.Phone != nectar.SessionKey(n.ConversationID) {
					t.Errorf("comando = %+v", got)
				}
			}
			if inbox.isDone(n.EventID) != c.wantDone {
				t.Errorf("fila cerrada = %v, quiero %v", inbox.isDone(n.EventID), c.wantDone)
			}
			if c.wantStep != "" && !hasEvent(events, "apertura", c.wantStep) {
				t.Errorf("falta apertura/%s: %+v", c.wantStep, events)
			}
			if c.wantStep == "" && hasEvent(events, "apertura", "replay_perdida") {
				t.Errorf("solo el cuarto aviso deja apertura/replay_perdida: %+v", events)
			}
		})
	}
}

func TestDispatchNectarNotice_UnreadableRowReturnsError(t *testing.T) {
	sink := &sinkRecorder{}
	h := newNectarHandler(newMemInbox(), sink)
	for _, raw := range []string{`not json`, `{"kind":"mensaje_entrante","conversation_id":1}`} {
		if err := h.DispatchNectarNotice(context.Background(), raw); err == nil {
			t.Errorf("una fila ilegible (%q) debe devolver error para que el replay la cierre", raw)
		}
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Fatal("y no se despacha nada")
	}
}

func TestIsNectarInboxRow(t *testing.T) {
	if !IsNectarInboxRow("nectar_notice") {
		t.Error("nectar_notice es una fila de Néctar")
	}
	// Los msg_type de Bird (el tipo del mensaje del paciente) nunca lo son.
	for _, msgType := range []string{"text", "image", "postback", "document", ""} {
		if IsNectarInboxRow(msgType) {
			t.Errorf("%q es de Bird", msgType)
		}
	}
}

// fakeNectarNotifier imita la parte del NotificationManager que clasifica la respuesta a una
// notificación proactiva. HandleResponse corre en una goroutine (como en Bird): avisa por canal.
type fakeNectarNotifier struct {
	mu        sync.Mutex
	pending   map[string]bool
	responses chan [3]string
	invalid   [][2]string
	// phonePending: teléfonos («+57…») con el pending de un recordatorio que no salió; refuse
	// simula la guarda del manager (conversación con agente). adopted anota cada intento.
	phonePending map[string]bool
	refuse       bool
	adopted      [][4]string
	// unsent: claves cuyo pending es de un recordatorio fallido; HandleInvalidInput no las consume.
	unsent map[string]bool
	// rejectPostback: claves cuyo AcceptsPostback dice que no (recordatorio fallido + otra plantilla).
	rejectPostback map[string]bool
}

func newFakeNectarNotifier(pending ...string) *fakeNectarNotifier {
	f := &fakeNectarNotifier{pending: map[string]bool{}, responses: make(chan [3]string, 4)}
	for _, p := range pending {
		f.pending[p] = true
	}
	return f
}

func (f *fakeNectarNotifier) HasPending(phone string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending[phone]
}

func (f *fakeNectarNotifier) HandleResponse(phone, payload, conversationID string) {
	f.responses <- [3]string{phone, payload, conversationID}
}

func (f *fakeNectarNotifier) HandleInvalidInput(phone, conversationID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalid = append(f.invalid, [2]string{phone, conversationID})
	return !f.unsent[phone]
}

func (f *fakeNectarNotifier) AdoptPhonePending(contactPhone, sessionKey, conversationID, payload string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adopted = append(f.adopted, [4]string{contactPhone, sessionKey, conversationID, payload})
	if f.refuse || !f.phonePending[contactPhone] || f.pending[sessionKey] {
		return false
	}
	delete(f.phonePending, contactPhone)
	f.pending[sessionKey] = true
	return true
}

func (f *fakeNectarNotifier) adoptCalls() [][4]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][4]string(nil), f.adopted...)
}

func (f *fakeNectarNotifier) AcceptsPostback(phone, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.rejectPostback[phone]
}

func (f *fakeNectarNotifier) waitResponse(t *testing.T) [3]string {
	t.Helper()
	select {
	case r := <-f.responses:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("HandleResponse no se llamó")
		return [3]string{}
	}
}

func buttonBody(eventID, replyID string) string {
	reply := ""
	if replyID != "" {
		reply = `,"interactive_reply_id":"` + replyID + `"`
	}
	return `{"event_id":"` + eventID + `","kind":"mensaje_entrante","conversation_id":77,
	  "occurred_at":"2026-10-29T15:42:07Z","message":{"id":90,"external_id":"wamid.B","sequence":3,
	  "kind":"button","text":""` + reply + `}}`
}

func TestNectarInbound_PendingButton_GoesToHandleResponse(t *testing.T) {
	inbox := newMemInbox()
	sink := &sinkRecorder{}
	h := newNectarHandler(inbox, sink)
	notify := newFakeNectarNotifier("nectar:77")
	h.SetNectarNotifier(notify)

	if rec := postNotice(h, signedNotice(nectarTestSecret, buttonBody("e-btn-1", "Confirmar"))); rec.Code != http.StatusOK {
		t.Fatalf("→ %d", rec.Code)
	}
	if got := notify.waitResponse(t); got != [3]string{"nectar:77", "confirm", "77"} {
		t.Fatalf("HandleResponse%v", got)
	}
	if m, c := sink.counts(); m != 0 || c != 0 {
		t.Fatalf("el worker no recibe nada: mensajes=%d comandos=%d", m, c)
	}
	if !inbox.isDone("e-btn-1") {
		t.Error("la fila del WAL se cierra: el worker no la va a cerrar")
	}
}

func TestNectarInbound_PendingEmptyButton_InvalidInput(t *testing.T) {
	inbox := newMemInbox()
	sink := &sinkRecorder{}
	h := newNectarHandler(inbox, sink)
	notify := newFakeNectarNotifier("nectar:77")
	h.SetNectarNotifier(notify)

	if rec := postNotice(h, signedNotice(nectarTestSecret, buttonBody("e-btn-2", ""))); rec.Code != http.StatusOK {
		t.Fatalf("→ %d", rec.Code)
	}
	notify.mu.Lock()
	invalid := append([][2]string(nil), notify.invalid...)
	notify.mu.Unlock()
	if len(invalid) != 1 || invalid[0] != [2]string{"nectar:77", "77"} {
		t.Fatalf("HandleInvalidInput = %v", invalid)
	}
	if m, _ := sink.counts(); m != 0 {
		t.Fatalf("el worker no recibe nada, got %d", m)
	}
	if !inbox.isDone("e-btn-2") {
		t.Error("la fila del WAL se cierra")
	}
	select {
	case r := <-notify.responses:
		t.Fatalf("no es una respuesta: %v", r)
	default:
	}
}

func TestNectarInbound_NoPending_GoesToWorker(t *testing.T) {
	inbox := newMemInbox()
	sink := &sinkRecorder{}
	h := newNectarHandler(inbox, sink)
	notify := newFakeNectarNotifier() // sin pending
	h.SetNectarNotifier(notify)

	if rec := postNotice(h, signedNotice(nectarTestSecret, buttonBody("e-btn-3", "Confirmar"))); rec.Code != http.StatusOK {
		t.Fatalf("→ %d", rec.Code)
	}
	if m, _ := sink.counts(); m != 1 {
		t.Fatalf("sin pending va al worker, got %d", m)
	}
	// Sin pending NO se traduce el título: el postback llega tal cual a la máquina de estados.
	if got := sink.messages[0]; !got.IsPostback || got.PostbackPayload != "Confirmar" {
		t.Fatalf("%+v", got)
	}
	if len(notify.invalid) != 0 {
		t.Fatalf("sin pending no hay entrada inválida: %v", notify.invalid)
	}
	if inbox.isDone("e-btn-3") {
		t.Error("la fila la cierra el worker, no el despacho")
	}
}

func TestNectarInbound_ReplayPendingButton_GoesToHandleResponse(t *testing.T) {
	inbox := newMemInbox()
	sink := &sinkRecorder{}
	h := newNectarHandler(inbox, sink)
	notify := newFakeNectarNotifier("nectar:77")
	h.SetNectarNotifier(notify)

	if err := h.DispatchNectarNotice(context.Background(), buttonBody("e-btn-4", "Confirmar")); err != nil {
		t.Fatal(err)
	}
	if got := notify.waitResponse(t); got != [3]string{"nectar:77", "confirm", "77"} {
		t.Fatalf("HandleResponse%v", got)
	}
	if m, _ := sink.counts(); m != 0 {
		t.Fatalf("el worker no recibe nada, got %d", m)
	}
	if !inbox.isDone("e-btn-4") {
		t.Error("el replay cierra la fila como en el camino de Bird")
	}
}

// --- PUENTE APAGADO (vuelta a Bird): el aviso se acusa y se descarta ---

// nectarBridgeOffBody es la respuesta con el puente apagado. Es 200 y no un 5xx a propósito:
// Néctar reintenta todo no-2xx sin corte mientras dure la ventana de 24 h y, al volver el puente,
// entregaría el atraso entero (mensajes viejos incluidos) en tandas de 100.
const nectarBridgeOffBody = `{"status":"ignored","reason":"bridge_off"}`

// newBridgeOffHandler es newNectarHandler con el puente apagado (NECTAR_BASE_URL vacía).
func newBridgeOffHandler(inbox InboxPersister, sink *sinkRecorder) *WebhookHandler {
	h := newNectarHandler(inbox, sink)
	h.SetNectarBridgeOn(false)
	return h
}

// Con el puente apagado, ningún aviso —tampoco el cuarto, que se resuelve en la respuesta— se
// persiste en el WAL, llega al pool ni se procesa: se acusa con 200 y el cuerpo bridge_off.
func TestHandleNectarNotice_BridgeOffAcksAndDiscards(t *testing.T) {
	bodies := map[string]string{
		"mensaje_entrante":                inboundBody("e-off-1"),
		"conversacion_resuelta":           `{"event_id":"e-off-2","kind":"conversacion_resuelta","conversation_id":1234,"occurred_at":"2026-08-11T20:15:04Z"}`,
		"conversacion_abierta_por_agente": openedBody,
	}
	for kind, body := range bodies {
		t.Run(kind, func(t *testing.T) {
			var logged bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
			defer slog.SetDefault(previous)

			inbox := newMemInbox()
			sink := &sinkRecorder{}
			h := newBridgeOffHandler(inbox, sink)
			rec := postNotice(h, signedNotice(nectarTestSecret, body))
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != nectarBridgeOffBody {
				t.Fatalf("puente apagado → 200 %s, got %d %s", nectarBridgeOffBody, rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			if len(inbox.rows) != 0 {
				t.Errorf("con el puente apagado nada se escribe en el WAL: %v", inbox.rows)
			}
			if m, c := sink.counts(); m+c != 0 {
				t.Errorf("con el puente apagado nada llega al pool: mensajes=%d comandos=%d", m, c)
			}
			line := logged.String()
			if strings.Count(line, "level=WARN") != 1 || !strings.Contains(line, "bridge_off") {
				t.Errorf("un Warn por aviso: %s", line)
			}
			for _, pii := range []string{"+570000000000", "Paciente Prueba", "hola"} {
				if strings.Contains(line, pii) {
					t.Errorf("el registro trae datos del aviso (%q): %s", pii, line)
				}
			}
		})
	}
}

// Con el secreto configurado la firma se verifica ANTES: quien no firma no puede sondear si el
// puente está apagado.
func TestHandleNectarNotice_BridgeOffStillVerifiesSignature(t *testing.T) {
	inbox := newMemInbox()
	sink := &sinkRecorder{}
	h := newBridgeOffHandler(inbox, sink)
	rec := postNotice(h, signedNotice("otro-secreto", inboundBody("e-off-3")))
	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "bridge_off") {
		t.Fatalf("firma ajena → 401 sin delatar el puente, got %d %s", rec.Code, rec.Body.String())
	}
	if len(inbox.rows) != 0 {
		t.Errorf("nada en el WAL: %v", inbox.rows)
	}
}

// Sin NECTAR_NOTICE_SECRET y con el puente apagado no hay firma que verificar: el aviso se acusa y
// se descarta igual (el 200 no depende del secreto).
func TestHandleNectarNotice_BridgeOffWithoutSecretAcks(t *testing.T) {
	inbox := newMemInbox()
	sink := &sinkRecorder{}
	h := newBridgeOffHandler(inbox, sink)
	h.cfg = &config.Config{}
	rec := postNotice(h, newNoticeRequest(inboundBody("e-off-4")))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != nectarBridgeOffBody {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(inbox.rows) != 0 {
		t.Errorf("nada en el WAL: %v", inbox.rows)
	}
	if m, c := sink.counts(); m+c != 0 {
		t.Errorf("nada al pool: mensajes=%d comandos=%d", m, c)
	}
}
