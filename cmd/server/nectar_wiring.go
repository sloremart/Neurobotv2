package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	tg "github.com/neuro-bot/neuro-bot/internal/telegram"
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
	bridge := nectar.NewBridge(client, cfg.NectarTemplateNames)
	bridge.ChannelID = cfg.NectarChannelID
	if bridge.ChannelID == 0 {
		// El resto de Néctar funciona, pero sin canal de apertura el bot no puede iniciar
		// conversaciones: cada plantilla proactiva fallaría en silencio. Que se vea al arrancar.
		slog.Error("nectar: NECTAR_CHANNEL_ID sin configurar — los recordatorios, la lista de espera y los avisos administrativos NO van a salir")
	}
	return client, bridge, nil
}

// nectarHealth es el valor de `nectar_host` en /health: SOLO el host (ni claves ni ruta), o "none".
// Permite a un arnés de pruebas negarse a martillear un bot cableado al Néctar real.
func nectarHealth(baseURL string) string {
	if h := nectar.HostOf(baseURL); h != "" {
		return h
	}
	return "none"
}

// urgentGuidePath es la guía que sigue la persona de turno para devolver el bot a Bird.
const urgentGuidePath = "docs/GUIA-VOLVER-A-BIRD.md"

// wireUrgentAlerter arma el aviso urgente de volver a Bird y lo cablea en el cliente de Néctar
// (fallos de transporte/5xx y escalados). Solo con Telegram configurado Y el puente encendido
// (bridgeClient nil = puente apagado): sin puente no hay a qué volver. Devuelve nil en otro caso,
// y quien lo recibe no avisa nada; main corre su Start. El envío va directo a Telegram, NO por
// slog.Error (el AlertHandler lo agruparía con los errores comunes).
func wireUrgentAlerter(cfg *config.Config, tgClient *tg.Client, bridgeClient *nectar.Client) *monitor.UrgentAlerter {
	if tgClient == nil || bridgeClient == nil {
		return nil
	}
	urgent := monitor.NewUrgentAlerter(tgClient.SendMessage, urgentGuidePath,
		time.Duration(cfg.UrgentAlertCooldownMin)*time.Minute, time.Now)
	bridgeClient.SetUrgentReporter(urgent, nectar.UrgentLimits{
		DownCount:            cfg.UrgentNectarDownCount,
		DownWindow:           time.Duration(cfg.UrgentNectarDownWindowMin) * time.Minute,
		EscalationFailCount:  cfg.UrgentEscalationFailCount,
		EscalationFailWindow: time.Duration(cfg.UrgentEscalationFailWindowMin) * time.Minute,
	})
	return urgent
}

// wireNectarRuntime cablea lo que corre contra Néctar al arrancar y devuelve el aviso urgente (nil
// sin Telegram o sin puente; main corre su Start). bridgeClient es el cliente del puente (nil si
// está apagado).
//
// EL ORDEN IMPORTA: el aviso urgente se cablea en el cliente ANTES de arrancar el horario, cuya
// goroutine pide a Néctar de inmediato. Al revés, SetUrgentReporter escribiría c.urgent mientras
// esa petición lo lee (carrera) y el primer fallo del horario no contaría para el aviso.
func wireNectarRuntime(ctx context.Context, cfg *config.Config, tgClient *tg.Client, bridgeClient *nectar.Client,
	schedule *config.CachedSchedule,
) *monitor.UrgentAlerter {
	urgent := wireUrgentAlerter(cfg, tgClient, bridgeClient)
	startNectarSchedule(ctx, cfg, bridgeClient, schedule)
	return urgent
}
