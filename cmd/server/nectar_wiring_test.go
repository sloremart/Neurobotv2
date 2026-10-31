package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	tg "github.com/neuro-bot/neuro-bot/internal/telegram"
)

func TestNectarWiring_PartialConfigDisablesBridge(t *testing.T) {
	// Sin URL: Néctar apagado, sin error (el bot sigue exactamente como con Bird).
	c, b, err := nectarBridgeFromConfig(&config.Config{})
	if c != nil || b != nil || err != nil {
		t.Fatalf("sin URL: got %v %v %v", c, b, err)
	}

	// URL sin claves: configuración a medias → error, sin puente.
	partial := &config.Config{NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k"}
	c, b, err = nectarBridgeFromConfig(partial)
	if c != nil || b != nil || err == nil {
		t.Fatalf("a medias: got %v %v %v", c, b, err)
	}

	// Sin secreto de avisos: también a medias (todos los avisos darían 401).
	noNotice := &config.Config{NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k", NectarRequestSecret: "s"}
	c, b, err = nectarBridgeFromConfig(noNotice)
	if c != nil || b != nil || err == nil {
		t.Fatalf("sin secreto de avisos: got %v %v %v", c, b, err)
	}

	// Completa: cliente y puente.
	full := &config.Config{
		NectarBaseURL: "https://nectar.colibrixa.com", NectarServiceKey: "k", NectarRequestSecret: "s", NectarNoticeSecret: "n",
		NectarTemplateNames: map[string]string{"p": "n"}, NectarChannelID: 7,
	}
	c, b, err = nectarBridgeFromConfig(full)
	if c == nil || b == nil || err != nil {
		t.Fatalf("completa: got %v %v %v", c, b, err)
	}
	// El canal de apertura llega al puente: sin él las plantillas proactivas no saldrían.
	if nb, ok := b.(*nectar.Bridge); !ok || nb.ChannelID != 7 {
		t.Fatalf("NECTAR_CHANNEL_ID no llegó al puente: %#v", b)
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
	if _, b, err := nectarBridgeFromConfig(cfg); b == nil || err != nil {
		t.Fatalf("sin canal el puente se arma igual: %v %v", b, err)
	}
	if out := buf.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "NECTAR_CHANNEL_ID") {
		t.Fatalf("falta el error de arranque: %q", out)
	}

	buf.Reset()
	cfg.NectarChannelID = 7
	_, _, _ = nectarBridgeFromConfig(cfg)
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
	if s := newNectarReportsSyncer(cfg, nil, nil, nil); s != nil {
		t.Fatal("con el puente apagado no hay sincronización")
	}
	if s := newNectarReportsSyncer(cfg, nectar.NewClient("https://nectar.example", "", ""), nil, nil); s != nil {
		t.Fatal("con un cliente sin configurar no hay sincronización")
	}
	if s := newNectarReportsSyncer(cfg, nectar.NewClient("https://nectar.example", "k", "s"), nil, nil); s == nil {
		t.Fatal("con el puente encendido hay sincronización")
	}
	if started := startNectarReports(context.Background(), cfg, nil, nil, nil); started {
		t.Fatal("con el puente apagado no arranca nada")
	}
}
