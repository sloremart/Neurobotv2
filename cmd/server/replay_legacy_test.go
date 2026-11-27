package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/api"
	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

// nectarStub cuenta las llamadas que recibe un falso Néctar (aquí, el GET …/state del contacto).
func nectarStub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"contact":{"identifier":"573001112233"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// countingSink cuenta lo que el replay encoló hacia el pool.
type countingSink struct{ messages atomic.Int32 }

func (s *countingSink) Enqueue(bird.InboundMessage) bool {
	s.messages.Add(1)
	return true
}
func (s *countingSink) EnqueueAgentCommand(worker.AgentCommand) bool { return true }

// Una fila del WAL de una conversación registrada como legacy se reproduce por el cliente de la línea
// vieja: el registro (escrito antes del WAL) decide el cliente, sin cambiar el replay.
func TestReplay_LegacyRowUsesLegacyClient(t *testing.T) {
	mainSrv, mainHits := nectarStub(t)
	legacySrv, legacyHits := nectarStub(t)

	reg := nectar.NewLineRegistry(&memLineStore{lines: map[int64]nectar.Line{}})
	if err := reg.Register(context.Background(), 1234, nectar.LineLegacy); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	router := nectar.NewLineRouter(reg, map[nectar.Line]*nectar.Bridge{
		nectar.LineMain:   nectar.NewBridge(nectar.NewClient(mainSrv.URL, "k-main", "s"), names),
		nectar.LineLegacy: nectar.NewBridge(nectar.NewClient(legacySrv.URL, "k-legacy", "s"), names),
	})

	cfg := &config.Config{NectarNoticeSecret: "n"}
	birdClient := bird.NewClient(cfg)
	birdClient.SetNectar(router)
	h := api.NewWebhookHandler(birdClient, nil, nil, cfg)
	sink := &countingSink{}
	h.SetNectarSink(sink)
	h.SetNectarBridgeOn(true)

	if err := h.DispatchNectarNotice(context.Background(), nectarInboundBody); err != nil {
		t.Fatal(err)
	}
	if legacyHits.Load() == 0 || mainHits.Load() != 0 {
		t.Fatalf("legacy=%d main=%d: la fila vieja debe salir por el cliente legacy", legacyHits.Load(), mainHits.Load())
	}
	if sink.messages.Load() != 1 {
		t.Fatalf("el mensaje debía encolarse una vez, got %d", sink.messages.Load())
	}
}
