package main

import (
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
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
