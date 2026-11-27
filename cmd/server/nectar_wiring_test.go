package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	tg "github.com/neuro-bot/neuro-bot/internal/telegram"
)

func TestNectarWiring_PartialConfigDisablesBridge(t *testing.T) {
	// Sin URL: Néctar apagado, sin error (el bot sigue exactamente como con Bird).
	c, _, b, _, err := nectarBridgeFromConfig(&config.Config{}, nil)
	if c != nil || b != nil || err != nil {
		t.Fatalf("sin URL: got %v %v %v", c, b, err)
	}

	// URL sin claves: configuración a medias → error, sin puente.
	partial := &config.Config{NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k"}
	c, _, b, _, err = nectarBridgeFromConfig(partial, nil)
	if c != nil || b != nil || err == nil {
		t.Fatalf("a medias: got %v %v %v", c, b, err)
	}

	// Sin secreto de avisos: también a medias (todos los avisos darían 401).
	noNotice := &config.Config{NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k", NectarRequestSecret: "s"}
	c, _, b, _, err = nectarBridgeFromConfig(noNotice, nil)
	if c != nil || b != nil || err == nil {
		t.Fatalf("sin secreto de avisos: got %v %v %v", c, b, err)
	}

	// Completa: cliente y puente.
	full := &config.Config{
		NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k", NectarRequestSecret: "s", NectarNoticeSecret: "n",
		NectarTemplateNames: map[string]string{"p": "n"}, NectarChannelID: 7,
	}
	c, _, b, _, err = nectarBridgeFromConfig(full, nil)
	if c == nil || b == nil || err != nil {
		t.Fatalf("completa: got %v %v %v", c, b, err)
	}
	// El puente es el router por línea (con solo la principal se comporta como su Bridge).
	if _, ok := b.(*nectar.LineRouter); !ok {
		t.Fatalf("el puente debe ser el LineRouter: %#v", b)
	}
}

// El canal de apertura llega al puente: sin él las plantillas proactivas no saldrían.
func TestNectarWiring_ChannelReachesBridge(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"conversation_id":77,"channel_id":7,"message_id":901}`))
	}))
	defer srv.Close()
	cfg := &config.Config{
		NectarBaseURL: srv.URL, NectarServiceKey: "k", NectarRequestSecret: "s", NectarNoticeSecret: "n",
		NectarTemplateNames: map[string]string{"p": "n"}, NectarChannelID: 7,
	}
	_, _, b, _, err := nectarBridgeFromConfig(cfg, &memLineStore{lines: map[int64]nectar.Line{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.OpenWithTemplate(context.Background(), "+573001112233", bird.TemplateConfig{ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"channel_id":7`) {
		t.Fatalf("NECTAR_CHANNEL_ID no llegó al puente: %s", body)
	}
}

// memLineStore es un LineStore en memoria para las pruebas de cableado.
type memLineStore struct{ lines map[int64]nectar.Line }

func (m *memLineStore) LineOf(_ context.Context, id int64) (nectar.Line, bool, error) {
	l, ok := m.lines[id]
	return l, ok, nil
}

func (m *memLineStore) Register(_ context.Context, id int64, l nectar.Line) error {
	m.lines[id] = l
	return nil
}

func legacyCfg() *config.Config {
	return &config.Config{
		NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k", NectarRequestSecret: "s", NectarNoticeSecret: "n",
		NectarChannelID: 7,
	}
}

func TestNectarWiring_LegacyArmedBuildsSecondClient(t *testing.T) {
	cfg := legacyCfg()
	cfg.NectarLegacyServiceKey, cfg.NectarLegacyChannelID = "kv", 9
	cfg.NectarLegacyRequestSecret, cfg.NectarLegacyNoticeSecret = "s", "n"
	c, lc, b, reg, err := nectarBridgeFromConfig(cfg, &memLineStore{lines: map[int64]nectar.Line{}})
	if err != nil || c == nil || lc == nil || b == nil || reg == nil || c == lc {
		t.Fatalf("got %v %v %v %v %v", c, lc, b, reg, err)
	}
}

// Línea vieja a medias: sin segundo cliente y con el error que nombra la variable que falta; la
// principal sigue armada.
func TestNectarWiring_LegacyHalfConfigured(t *testing.T) {
	cfg := legacyCfg()
	cfg.NectarLegacyServiceKey = "kv"
	c, lc, b, _, err := nectarBridgeFromConfig(cfg, &memLineStore{lines: map[int64]nectar.Line{}})
	if lc != nil || err == nil || !strings.Contains(err.Error(), "NECTAR_LEGACY_CHANNEL_ID") {
		t.Fatalf("falta el canal: lc=%v err=%v", lc, err)
	}
	if c == nil || b == nil {
		t.Fatalf("la principal sigue armada: %v %v", c, b)
	}
	cfg = legacyCfg()
	cfg.NectarLegacyChannelID = 9
	_, lc, _, _, err = nectarBridgeFromConfig(cfg, &memLineStore{lines: map[int64]nectar.Line{}})
	if lc != nil || err == nil || !strings.Contains(err.Error(), "NECTAR_LEGACY_SERVICE_KEY") {
		t.Fatalf("falta la clave: lc=%v err=%v", lc, err)
	}
}

// Con el puente principal apagado la línea vieja queda apagada aunque esté configurada.
func TestNectarWiring_MainOffMeansLegacyOff(t *testing.T) {
	cfg := &config.Config{NectarLegacyServiceKey: "kv", NectarLegacyChannelID: 9}
	c, lc, b, reg, err := nectarBridgeFromConfig(cfg, &memLineStore{lines: map[int64]nectar.Line{}})
	if c != nil || lc != nil || b != nil || reg != nil || err != nil {
		t.Fatalf("got %v %v %v %v %v", c, lc, b, reg, err)
	}
}

// Los fallos de la línea vieja no disparan el URGENTE: un ERROR en el log, una vez por ventana y disparador.
func TestLegacyLineReporter_OncePerWindowAndTrigger(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	r := newLegacyLineReporter(func() time.Time { return now })
	ctx := context.Background()
	r.Count(ctx, monitor.TriggerNectarDown, 5, 5*time.Minute, "a")
	r.Count(ctx, monitor.TriggerNectarDown, 5, 5*time.Minute, "b")
	r.Count(ctx, monitor.TriggerEscalationFailing, 3, 5*time.Minute, "c")
	if n := strings.Count(buf.String(), "level=ERROR"); n != 2 {
		t.Fatalf("errores = %d, quiero 2: %s", n, buf.String())
	}
	now = now.Add(6 * time.Minute)
	r.Count(ctx, monitor.TriggerNectarDown, 5, 5*time.Minute, "d")
	if n := strings.Count(buf.String(), "level=ERROR"); n != 3 {
		t.Fatalf("pasada la ventana vuelve a avisar: %d", n)
	}
}

// C-18: con una URL en claro hacia un host real, la clave de servicio y la firma viajarían sin cifrar.
func TestNectarWiring_InsecureURLDisablesBridge(t *testing.T) {
	complete := func(url string) *config.Config {
		return &config.Config{NectarBaseURL: url, NectarServiceKey: "k", NectarRequestSecret: "s", NectarNoticeSecret: "n"}
	}
	for _, url := range []string{
		"http://nectar.colibrixa.com",
		"http://192.168.1.50:8080",
		"ftp://nectar.colibrixa.com",
		"nectar.colibrixa.com",
		"nectar.colibrixa.com:443",
		"https://",
	} {
		c, _, b, _, err := nectarBridgeFromConfig(complete(url), nil)
		if c != nil || b != nil || err == nil {
			t.Errorf("%q: quería puente apagado y error: got %v %v %v", url, c, b, err)
		}
	}
	for _, url := range []string{
		"https://nectar.colibrixa.com",
		"HTTPS://nectar.colibrixa.com",
		"http://localhost:9999",
		"http://127.0.0.1:9999",
		"http://[::1]:9999",
		"http://nectar-stub:9999", // docs/PRUEBA-LOCAL-NECTAR.md
	} {
		c, _, b, _, err := nectarBridgeFromConfig(complete(url), nil)
		if c == nil || b == nil || err != nil {
			t.Errorf("%q: quería puente encendido: got %v %v %v", url, c, b, err)
		}
	}
}

func TestNectarHealth(t *testing.T) {
	if got := nectarHealth(""); got != "none" {
		t.Errorf("vacío = %q", got)
	}
	if got := nectarHealth("https://nectar.colibrixa.com:8443/x?k=v"); got != "nectar.colibrixa.com" {
		t.Errorf("host = %q", got)
	}
}

// pingConnector es un driver mínimo cuya conexión solo sabe responder Ping, respetando el ctx
// como lo hace un driver real (una petición cortada cancela el Ping).
type pingConnector struct{}

func (pingConnector) Connect(context.Context) (driver.Conn, error) { return pingConn{}, nil }
func (pingConnector) Driver() driver.Driver                        { return nil }

type pingConn struct{}

func (pingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (pingConn) Close() error                        { return nil }
func (pingConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (pingConn) Ping(ctx context.Context) error      { return ctx.Err() }

// TestHealth_CancelledRequestDoesNotPoisonCache: un cliente que corta su petición de /health no debe
// dejar cacheado un «critical» para quien pregunte después con las BD sanas (A-02).
func TestHealth_CancelledRequestDoesNotPoisonCache(t *testing.T) {
	localDB := sql.OpenDB(pingConnector{})
	externalDB := sql.OpenDB(pingConnector{})
	t.Cleanup(func() { _ = localDB.Close(); _ = externalDB.Close() })
	handler := healthHandler(localDB, externalDB, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/health", nil).WithContext(ctx))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health tras una petición cancelada: esperaba 200, got %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != `{"external_db":"ok","local_db":"ok","nectar_host":"none","status":"ok"}`+"\n" {
		t.Errorf("cuerpo inesperado: %s", body)
	}
}

// Con la línea vieja armada /health lo publica; apagada, la clave no existe (el otro cuerpo, byte a byte,
// lo fijan los tests de arriba).
func TestHealth_LegacyLineArmedIsPublished(t *testing.T) {
	localDB := sql.OpenDB(pingConnector{})
	externalDB := sql.OpenDB(pingConnector{})
	t.Cleanup(func() { _ = localDB.Close(); _ = externalDB.Close() })

	rec := httptest.NewRecorder()
	healthHandler(localDB, externalDB, "", true).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	want := `{"external_db":"ok","local_db":"ok","nectar_host":"none","nectar_legacy":"armed","status":"ok"}` + "\n"
	if body := rec.Body.String(); body != want {
		t.Errorf("armada: cuerpo = %s", body)
	}

	rec = httptest.NewRecorder()
	healthHandler(localDB, externalDB, "", false).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if strings.Contains(rec.Body.String(), "nectar_legacy") {
		t.Errorf("apagada: no debe publicar nectar_legacy: %s", rec.Body.String())
	}
}

// downConnector simula una BD caída: la conexión abre pero el Ping falla.
type downConnector struct{}

func (downConnector) Connect(context.Context) (driver.Conn, error) { return downConn{}, nil }
func (downConnector) Driver() driver.Driver                        { return nil }

type downConn struct{ pingConn }

func (downConn) Ping(context.Context) error { return errors.New("connection refused") }

// TestHealth_SiesaDownIsDegradedNot503: con SIESA caído /health responde 200 «degraded» y
// external_db «error». El healthcheck de Docker (wget falla con un 503) no marca al bot unhealthy por
// SIESA, y el monitor externo distingue el estado por el cuerpo (docs/MONITOREO-EXTERNO.md). Solo la
// BD local da 503 «critical».
func TestHealth_SiesaDownIsDegradedNot503(t *testing.T) {
	localDB := sql.OpenDB(pingConnector{})
	externalDB := sql.OpenDB(downConnector{})
	t.Cleanup(func() { _ = localDB.Close(); _ = externalDB.Close() })

	rec := httptest.NewRecorder()
	healthHandler(localDB, externalDB, "").ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("SIESA caído: esperaba 200, got %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != `{"external_db":"error","local_db":"ok","nectar_host":"none","status":"degraded"}`+"\n" {
		t.Errorf("cuerpo inesperado: %s", body)
	}

	localDown := sql.OpenDB(downConnector{})
	t.Cleanup(func() { _ = localDown.Close() })
	rec = httptest.NewRecorder()
	healthHandler(localDown, externalDB, "").ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"status":"critical"`) {
		t.Fatalf("BD local caída: esperaba 503 critical, got %d %s", rec.Code, rec.Body.String())
	}
}

// protectedMux monta /health/debug y la API interna como main, con un handler que responde 200.
func protectedMux() *http.ServeMux {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux := http.NewServeMux()
	mountProtected(mux, internalGuard(&config.Config{InternalAPIKey: "clave-de-prueba"}), ok, ok)
	return mux
}

// guardHit manda una petición desde una IP fija con la clave dada y devuelve el código.
func guardHit(mux http.Handler, path, key string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "203.0.113.7:40001"
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// TestInternalGuard_LimitsDebugAttempts: /health/debug pide la misma clave que la API interna, así que
// también tiene su límite de intentos, y con el MISMO cupo: alternar rutas no duplica los intentos (A-03).
func TestInternalGuard_LimitsDebugAttempts(t *testing.T) {
	mux := protectedMux()
	codes := map[int]int{}
	for i := range 31 {
		path := "/health/debug"
		if i%2 == 1 {
			path = "/api/internal/sessions"
		}
		codes[guardHit(mux, path, "mala")]++
	}
	if codes[http.StatusUnauthorized] != 30 || codes[http.StatusTooManyRequests] != 1 {
		t.Fatalf("31 claves malas alternando /health/debug y /api/internal: quiero 30×401 y 1×429, got %v", codes)
	}
	// Agotado el cupo, la IP queda cerrada también para la clave buena: si pasara, el 429/200 diría
	// cuándo se acertó y el límite dejaría de frenar la adivinación.
	if got := guardHit(mux, "/api/internal/sessions", "clave-de-prueba"); got != http.StatusTooManyRequests {
		t.Fatalf("clave buena tras agotar el cupo: código %d, quiero 429", got)
	}
}

// TestInternalGuard_AuthenticatedRequestsDoNotSpendQuota: el límite frena a quien adivina la clave, no
// al uso legítimo: las peticiones con la clave buena no gastan cupo (el auditor y el dashboard pasan
// de 30 por minuto sin tocarlo).
func TestInternalGuard_AuthenticatedRequestsDoNotSpendQuota(t *testing.T) {
	mux := protectedMux()
	for i := range 50 {
		path := "/api/internal/sessions"
		if i%5 == 0 {
			path = "/health/debug"
		}
		if got := guardHit(mux, path, "clave-de-prueba"); got != http.StatusOK {
			t.Fatalf("petición autenticada n.º %d: código %d, quiero 200", i+1, got)
		}
	}
	// Las autenticadas no dejaron cupo gastado: los 30 intentos fallidos siguen disponibles.
	for i := range 30 {
		if got := guardHit(mux, "/health/debug", "mala"); got != http.StatusUnauthorized {
			t.Fatalf("intento fallido n.º %d tras 50 autenticadas: código %d, quiero 401", i+1, got)
		}
	}
}

// hangConnector simula una BD colgada: su Ping no vuelve hasta que vence el plazo.
type hangConnector struct{}

func (hangConnector) Connect(context.Context) (driver.Conn, error) { return hangConn{}, nil }
func (hangConnector) Driver() driver.Driver                        { return nil }

type hangConn struct{ pingConn }

func (hangConn) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestHealth_HungLocalDBDoesNotFailSiesa: cada Ping tiene su propio plazo. Con la BD local colgada,
// SIESA sana sigue saliendo «ok» en vez de heredar un plazo ya vencido.
func TestHealth_HungLocalDBDoesNotFailSiesa(t *testing.T) {
	localDB := sql.OpenDB(hangConnector{})
	externalDB := sql.OpenDB(pingConnector{})
	t.Cleanup(func() { _ = localDB.Close(); _ = externalDB.Close() })

	rec := httptest.NewRecorder()
	healthHandler(localDB, externalDB, "").ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if body := rec.Body.String(); body != `{"external_db":"ok","local_db":"error","nectar_host":"none","status":"critical"}`+"\n" {
		t.Errorf("cuerpo inesperado: %s", body)
	}
}

// Puente puesto sin canal de apertura: arranca (el resto de Néctar funciona), pero lo dice en ERROR —
// ninguna plantilla proactiva (recordatorios, lista de espera, avisos) va a salir.
func TestNectarWiring_NoChannelLogsError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	cfg := &config.Config{
		NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k", NectarRequestSecret: "s", NectarNoticeSecret: "n",
	}
	if _, _, b, _, err := nectarBridgeFromConfig(cfg, nil); b == nil || err != nil {
		t.Fatalf("sin canal el puente se arma igual: %v %v", b, err)
	}
	if out := buf.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "NECTAR_CHANNEL_ID") {
		t.Fatalf("falta el error de arranque: %q", out)
	}

	buf.Reset()
	cfg.NectarChannelID = 7
	_, _, _, _, _ = nectarBridgeFromConfig(cfg, nil)
	if strings.Contains(buf.String(), "NECTAR_CHANNEL_ID") {
		t.Fatalf("con canal no hay aviso: %q", buf.String())
	}
}

// El aviso urgente de volver a Bird existe solo con Telegram configurado Y el puente encendido: con
// el puente apagado no hay nada a qué volver y no se dispara nada (el cliente de Néctar queda sin aviso).
func TestUrgentAlerter_OnlyWithTelegramAndBridge(t *testing.T) {
	cfg := &config.Config{
		UrgentAlertCooldownMin: 10, UrgentNectarDownCount: 5, UrgentNectarDownWindowMin: 5,
		UrgentEscalationFailCount: 3, UrgentEscalationFailWindowMin: 10,
	}
	tgClient := tg.NewClient("token-de-prueba", "chat-de-prueba")
	client := nectar.NewClient("https://nectar.example", "k", "s")

	if a := wireUrgentAlerter(cfg, nil, client); a != nil {
		t.Fatal("sin Telegram no hay aviso")
	}
	if a := wireUrgentAlerter(cfg, tgClient, nil); a != nil {
		t.Fatal("con el puente apagado no hay aviso")
	}
	if a := wireUrgentAlerter(cfg, tgClient, client); a == nil {
		t.Fatal("con Telegram y puente hay aviso")
	}
}

// El aviso urgente se cablea en el cliente de Néctar ANTES de la primera petición: la goroutine del
// horario lee al arrancar, y con Néctar caído ese primer 5xx tiene que contar (y sin carrera entre
// SetUrgentReporter y la lectura de c.urgent; go test -race).
func TestWireNectarRuntime_FirstScheduleFailureCounts(t *testing.T) {
	logs := captureSlog(t)
	f := newFakeScheduleServer(t, withSaturday)
	f.mu.Lock()
	f.code = http.StatusServiceUnavailable
	f.mu.Unlock()
	cfg := &config.Config{
		NectarScheduleEnabled: true, NectarScheduleRefreshMin: 60,
		UrgentAlertCooldownMin: 10, UrgentNectarDownCount: 1, UrgentNectarDownWindowMin: 5,
		UrgentEscalationFailCount: 3, UrgentEscalationFailWindowMin: 10,
	}
	client := nectar.NewClient(f.srv.URL, "clave", "secreto")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Sin Start: el aviso queda en la cola y no sale a Telegram; Fire lo deja en el log.
	if urgent := wireNectarRuntime(ctx, cfg, tg.NewClient("token-de-prueba", "chat-de-prueba"), client,
		wireSchedule(cfg)); urgent == nil {
		t.Fatal("con Telegram y puente hay aviso")
	}
	waitFor(t, "el primer fallo del horario cuenta para el aviso urgente", func() bool {
		return strings.Contains(logs.String(), "aviso urgente: volver a Bird") &&
			strings.Contains(logs.String(), "trigger=nectar_down")
	})
}

// La sincronización de los informes de Néctar existe solo con el puente encendido: con el puente
// apagado (cliente nil) o un cliente sin configurar ni se crea, y no se llama a Néctar.
func TestNectarReportsSyncer_OnlyWithBridge(t *testing.T) {
	cfg := &config.Config{NectarReportsSyncMin: 5}
	if s := newNectarReportsSyncer(cfg, nil, nil, nil, nectar.LineMain); s != nil {
		t.Fatal("con el puente apagado no hay sincronización")
	}
	if s := newNectarReportsSyncer(cfg, nectar.NewClient("https://nectar.example", "", ""), nil, nil, nectar.LineMain); s != nil {
		t.Fatal("con un cliente sin configurar no hay sincronización")
	}
	if s := newNectarReportsSyncer(cfg, nectar.NewClient("https://nectar.example", "k", "s"), nil, nil, nectar.LineMain); s == nil {
		t.Fatal("con el puente encendido hay sincronización")
	}
	if started := startNectarReports(context.Background(), cfg, nil, nil, nil, nil); started {
		t.Fatal("con el puente apagado no arranca nada")
	}
}
