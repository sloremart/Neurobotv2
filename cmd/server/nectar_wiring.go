package main

import (
	"errors"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// nectarBridgeFromConfig arma el cliente y el puente de Néctar desde la configuración.
// NECTAR_BASE_URL vacía → (nil, nil, nil): el bot sigue exactamente como con Bird. URL puesta pero
// sin clave de servicio, sin secreto de ida o sin secreto de avisos → error y sin puente: main lo registra y sigue como
// Bird, en vez de arrancar con un desvío que fallaría en cada envío.
func nectarBridgeFromConfig(cfg *config.Config) (*nectar.Client, bird.NectarBridge, error) {
	if cfg.NectarBaseURL == "" {
		return nil, nil, nil
	}
	client := nectar.NewClient(cfg.NectarBaseURL, cfg.NectarServiceKey, cfg.NectarRequestSecret)
	// Sin NECTAR_NOTICE_SECRET todos los avisos de Néctar darían 401 sin que nadie se enterara.
	if !client.Configured() || cfg.NectarNoticeSecret == "" {
		return nil, nil, errors.New("NECTAR_BASE_URL puesta pero faltan NECTAR_SERVICE_KEY, NECTAR_REQUEST_SECRET o NECTAR_NOTICE_SECRET")
	}
	return client, nectar.NewBridge(client, cfg.NectarTemplateNames), nil
}

// nectarHealth es el valor de `nectar_host` en /health: SOLO el host (ni claves ni ruta), o "none".
// Permite a un arnés de pruebas negarse a martillear un bot cableado al Néctar real.
func nectarHealth(baseURL string) string {
	if h := nectar.HostOf(baseURL); h != "" {
		return h
	}
	return "none"
}
