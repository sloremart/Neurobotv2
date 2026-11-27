package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/nectarreports"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
	tg "github.com/neuro-bot/neuro-bot/internal/telegram"
)

// nectarBridgeFromConfig arma los clientes y el puente de Néctar desde la configuración.
// NECTAR_BASE_URL vacía → todo nil: el bot sigue exactamente como con Bird. URL puesta pero
// sin clave de servicio, sin secreto de ida o sin secreto de avisos → error y sin puente: main lo registra y sigue como
// Bird, en vez de arrancar con un desvío que fallaría en cada envío. Igual con una URL sin https que no
// sea de un stub local: la clave de servicio viajaría en claro.
//
// La línea vieja (NECTAR_LEGACY_*) es un segundo cliente contra el MISMO Néctar con su propia clave.
// Solo se arma con el puente principal armado y con clave Y canal; a medias devuelve su error con
// legacyClient nil, pero la línea principal sigue armada (un descuido en la vieja no la apaga).
// El puente que se devuelve es el LineRouter: reparte por conversación según el registro (reg).
func nectarBridgeFromConfig(cfg *config.Config, store nectar.LineStore) (mainClient, legacyClient *nectar.Client,
	bridge bird.NectarBridge, reg *nectar.LineRegistry, err error,
) {
	if cfg.NectarBaseURL == "" {
		return nil, nil, nil, nil, nil
	}
	if !nectar.SafeBaseURL(cfg.NectarBaseURL) {
		return nil, nil, nil, nil, errors.New("NECTAR_BASE_URL tiene que usar https (en claro solo un host de pruebas local)")
	}
	client := nectar.NewClient(cfg.NectarBaseURL, cfg.NectarServiceKey, cfg.NectarRequestSecret)
	// Sin NECTAR_NOTICE_SECRET todos los avisos de Néctar darían 401 sin que nadie se enterara.
	if !client.Configured() || cfg.NectarNoticeSecret == "" {
		return nil, nil, nil, nil, errors.New("NECTAR_BASE_URL puesta pero faltan NECTAR_SERVICE_KEY, NECTAR_REQUEST_SECRET o NECTAR_NOTICE_SECRET")
	}
	mainBridge := nectar.NewBridge(client, cfg.NectarTemplateNames)
	mainBridge.ChannelID = cfg.NectarChannelID
	if mainBridge.ChannelID == 0 {
		// El resto de Néctar funciona, pero sin canal de apertura el bot no puede iniciar
		// conversaciones: cada plantilla proactiva fallaría en silencio. Que se vea al arrancar.
		slog.Error("nectar: NECTAR_CHANNEL_ID sin configurar — los recordatorios, la lista de espera y los avisos administrativos NO van a salir")
	}
	bridges := map[nectar.Line]*nectar.Bridge{nectar.LineMain: mainBridge}

	legacyOK, legacyErr := cfg.NectarLegacyConfigured()
	if legacyOK {
		legacyClient = nectar.NewClient(cfg.NectarBaseURL, cfg.NectarLegacyServiceKey, cfg.NectarLegacyRequestSecret)
		if !legacyClient.Configured() {
			legacyClient = nil
			legacyErr = errors.New("la línea vieja necesita NECTAR_LEGACY_REQUEST_SECRET (o NECTAR_REQUEST_SECRET de la principal)")
		}
	}
	if legacyClient != nil {
		legacyClient.SetUrgentReporter(newLegacyLineReporter(time.Now), nectar.UrgentLimits{
			DownCount:            cfg.UrgentNectarDownCount,
			DownWindow:           time.Duration(cfg.UrgentNectarDownWindowMin) * time.Minute,
			EscalationFailCount:  cfg.UrgentEscalationFailCount,
			EscalationFailWindow: time.Duration(cfg.UrgentEscalationFailWindowMin) * time.Minute,
		})
		// No abre conversaciones (ChannelID 0): las de esta línea las abre quien escribe al número viejo.
		bridges[nectar.LineLegacy] = nectar.NewBridge(legacyClient, cfg.NectarTemplateNames)
	}
	reg = nectar.NewLineRegistry(store)
	return client, legacyClient, nectar.NewLineRouter(reg, bridges), reg, legacyErr
}

// legacyLineReporter es el UrgentReporter de la línea vieja: sus fallos no disparan el URGENTE de
// volver a Bird (la línea vieja caída no deja muda a la principal); van a un ERROR del log, que
// llega a Telegram, a lo sumo una vez por disparador y ventana.
type legacyLineReporter struct {
	now  func() time.Time
	mu   sync.Mutex
	last map[monitor.UrgentTrigger]time.Time
}

func newLegacyLineReporter(now func() time.Time) *legacyLineReporter {
	return &legacyLineReporter{now: now, last: map[monitor.UrgentTrigger]time.Time{}}
}

func (r *legacyLineReporter) Count(_ context.Context, trigger monitor.UrgentTrigger, _ int, window time.Duration, detail string) {
	now := r.now()
	r.mu.Lock()
	prev, seen := r.last[trigger]
	if seen && now.Sub(prev) < window {
		r.mu.Unlock()
		return
	}
	r.last[trigger] = now
	r.mu.Unlock()
	slog.Error("nectar línea vieja: fallos", "trigger", string(trigger), "detail", detail)
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
	// Con la línea vieja armada el texto del aviso ya no manda volver a Bird.
	urgent.SetLegacyLineArmed(cfg.NectarLegacyArmed)
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

// newNectarReportsSyncer arma la sincronización de los informes de asesores (D5): tramos de espera
// y roster de Néctar a las tablas locales, y las escalaciones nectar:* completadas. Solo con el
// puente encendido (bridgeClient nil = apagado): sin puente devuelve nil y no se llama a Néctar.
// Con line = LineLegacy y el cliente de la línea vieja solo copia los tramos de su canal.
func newNectarReportsSyncer(cfg *config.Config, bridgeClient *nectar.Client, reports *localrepo.NectarReportsRepo,
	escalations *localrepo.EscalationRepo, line nectar.Line,
) *nectarreports.Syncer {
	if bridgeClient == nil || !bridgeClient.Configured() {
		return nil
	}
	every := time.Duration(cfg.NectarReportsSyncMin) * time.Minute
	if cfg.NectarReportsSyncMin <= 0 {
		every = config.DefaultNectarReportsSyncMin * time.Minute
	}
	return nectarreports.NewSyncer(bridgeClient, reports, escalations, every, nectarreports.DataFloor, line)
}

// startNectarReports arranca la sincronización de los informes si el puente está encendido y
// dice si arrancó. main la llama DESPUÉS de wireNectarRuntime: el aviso urgente ya está cableado
// en el cliente cuando sale la primera lectura (sin carrera con SetUrgentReporter). Con la línea
// vieja armada (legacyClient no nil) arranca una segunda sincronización con su clave: cada clave
// de canal solo ve los tramos de su canal. Una clave de cuenta que falta detiene solo a su instancia.
func startNectarReports(ctx context.Context, cfg *config.Config, bridgeClient, legacyClient *nectar.Client,
	reports *localrepo.NectarReportsRepo, escalations *localrepo.EscalationRepo,
) bool {
	s := newNectarReportsSyncer(cfg, bridgeClient, reports, escalations, nectar.LineMain)
	if s == nil {
		return false
	}
	slog.Info("informes de Néctar: sincronización de tramos y asesores activa",
		"cada_min", cfg.NectarReportsSyncMin, "desde", nectarreports.DataFloor.Format(time.RFC3339))
	safeGo("nectar-reports", func() { s.Run(ctx) })
	if legacy := newNectarReportsSyncer(cfg, legacyClient, reports, escalations, nectar.LineLegacy); legacy != nil {
		slog.Info("informes de Néctar: sincronización de tramos de la línea vieja activa")
		safeGo("nectar-reports-legacy", func() { legacy.Run(ctx) })
	}
	return true
}
