package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// EL HORARIO SE LEE DE NÉCTAR (port de hdg-bot, cmd/server/schedule_nectar.go).
//
// Néctar administra el horario por equipo y los festivos por workspace; el bot decide con ellos
// si atiende (CHECK_BUSINESS_HOURS) y qué hace con una escalación (gate de horario y cola del
// próximo día hábil). Esto lee cada pocos minutos y publica la lectura en el proveedor
// (config.CachedSchedule), al que cada gate pregunta en el momento de decidir (cfg.Hours()).
//
// Sin puente no se llama a Néctar y rige el horario de siempre (config.DefaultBusinessHours), minuto
// a minuto y letra por letra. Con puente:
//   - NECTAR_SCHEDULE_ENABLED apagada (default): de la lectura solo se toman los festivos, que en
//     Néctar son de la entidad, sin depender de equipo ni canal. La semana sigue siendo la de siempre,
//     así que unos equipos sin horario no pueden cerrar al bot.
//   - encendida: mandan las ventanas y los festivos publicados.

// scheduleReader es lo único que se necesita de Néctar: la lectura, y el roster para comprobar que la
// clave es de cuenta. Interfaz para probar el bucle con un servidor de mentira.
type scheduleReader interface {
	GetSchedule(ctx context.Context, channelID int64, version string) (nectar.Schedule, error)
	GetAgents(ctx context.Context) ([]nectar.ReportAgent, error)
}

// errScheduleGlobalKey detiene el refresco: con la clave global Néctar lee como la instalación
// entera, y mezclaría las ventanas y los festivos de TODAS las entidades (un cierre de otra cerraría a
// Neuro). Rige entonces la semana de siempre, sin festivos de Néctar.
var errScheduleGlobalKey = errors.New("horario de Néctar: NECTAR_SERVICE_KEY es la clave global, se necesita la clave de CUENTA")

// nectarScheduleRefresher relee el horario cada tanto y publica la última lectura VÁLIDA. Nunca
// borra lo publicado: una lectura fallida deja un aviso y el bot sigue con lo último bueno.
type nectarScheduleRefresher struct {
	read      scheduleReader
	cache     *config.CachedSchedule
	channelID int64
	every     time.Duration
	// holidaysOnly: de la lectura solo se toman los festivos (NECTAR_SCHEDULE_ENABLED apagada).
	holidaysOnly bool
	// accountKey: ya se comprobó que la clave es de cuenta.
	accountKey bool
}

func newNectarScheduleRefresher(read scheduleReader, cache *config.CachedSchedule,
	channelID int64, every time.Duration,
) *nectarScheduleRefresher {
	return &nectarScheduleRefresher{read: read, cache: cache, channelID: channelID, every: every}
}

// wireSchedule cablea el proveedor del horario en la configuración: una caché cuyo respaldo es el
// horario de siempre. Se cablea SIEMPRE (también apagado) para que todos los gates pregunten por
// el mismo camino; sin lecturas, la caché responde el respaldo.
func wireSchedule(cfg *config.Config) *config.CachedSchedule {
	cache := config.NewCachedSchedule(config.DefaultBusinessHours())
	cfg.Schedule = cache
	return cache
}

// Run lee AL ARRANCAR y luego en cada tic, hasta que se cancele el contexto.
func (r *nectarScheduleRefresher) Run(ctx context.Context) {
	if err := r.refresh(ctx); errors.Is(err, errScheduleGlobalKey) {
		r.stopForGlobalKey()
		return
	} else if err != nil {
		slog.Warn("no se pudo leer el horario de Néctar al arrancar: rige el horario de siempre",
			"error", err, "horario", r.cache.Current().String())
	}
	t := time.NewTicker(r.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.refresh(ctx); errors.Is(err, errScheduleGlobalKey) {
				r.stopForGlobalKey()
				return
			} else if err != nil {
				// Warn y no Error: el tic siguiente suele pasar, y el bot no se queda sin
				// horario (sigue la última lectura buena).
				slog.Warn("no se pudo refrescar el horario desde Néctar: sigue el último bueno",
					"error", err, "version", r.cache.Version(), "horario", r.cache.Current().String())
			}
		}
	}
}

// stopForGlobalKey deja el único error del refresco detenido por la clave global.
func (r *nectarScheduleRefresher) stopForGlobalKey() {
	slog.Error("horario: rige el de siempre sin festivos de Néctar, el refresco se detiene", "error", errScheduleGlobalKey,
		"horario", r.cache.Current().String())
}

// refresh es UNA pasada: leer (condicional), validar y publicar solo si todo cuadró. Un 304 no
// es un fallo: la fuente contestó, se marca viva (Touch) y se vuelve sin error. Hasta que lo logre,
// cada pasada comprueba antes que la clave sea de cuenta (nada se publica sin esa comprobación).
func (r *nectarScheduleRefresher) refresh(ctx context.Context) error {
	if !r.accountKey {
		if _, err := r.read.GetAgents(ctx); errors.Is(err, nectar.ErrAccountKeyRequired) {
			return errScheduleGlobalKey
		} else if err != nil {
			return fmt.Errorf("comprobar la clave de cuenta: %w", err)
		}
		r.accountKey = true
	}
	sch, err := r.read.GetSchedule(ctx, r.channelID, r.cache.Version())
	if errors.Is(err, nectar.ErrScheduleNotModified) {
		r.cache.Touch()
		slog.Debug("horario: sin cambios en Néctar (304)", "version", r.cache.Version())
		return nil
	}
	if err != nil {
		return err
	}
	hours, err := r.hoursFrom(sch)
	if err != nil {
		return err
	}
	if !r.holidaysOnly && len(sch.Windows) == 0 {
		// Válido según el contrato, pero es lo que se vería si alguien dejara a los equipos sin
		// horario: el bot queda cerrado para todos. Que se vea en voz alta.
		slog.Error("Néctar publicó un horario SIN ventanas: el bot queda CERRADO toda la semana",
			"version", sch.Version)
	}
	if sch.Version != r.cache.Version() {
		slog.Info("horario actualizado desde Néctar", "version", sch.Version,
			"anterior", r.cache.Version(), "horario", hours.String(),
			"festivos", len(sch.Holidays), "generado", sch.GeneratedAt)
	}
	r.cache.Set(hours, sch.Version)
	return nil
}

// hoursFrom valida una lectura y la convierte en el horario que se publica.
func (r *nectarScheduleRefresher) hoursFrom(sch nectar.Schedule) (config.BusinessHours, error) {
	if r.holidaysOnly {
		return config.DefaultHoursWithHolidays(scheduleSpec(sch))
	}
	return config.BusinessHoursFromSchedule(scheduleSpec(sch))
}

// scheduleSpec copia el cable a los tipos neutros de config (config no puede importar nectar:
// nectar ya importa config vía bird).
func scheduleSpec(sch nectar.Schedule) config.ScheduleSpec {
	spec := config.ScheduleSpec{Timezone: sch.Timezone}
	for _, w := range sch.Windows {
		spec.Windows = append(spec.Windows, config.ScheduleWindowSpec{
			Weekday: w.Weekday, From: w.From, To: w.To,
		})
	}
	for _, h := range sch.Holidays {
		spec.Holidays = append(spec.Holidays, h.Date)
	}
	return spec
}

// startNectarSchedule arranca el refresco si hay puente y dice en el log con qué horario decide el
// bot. Devuelve si arrancó. Sin cliente de Néctar no hace NINGUNA llamada.
func startNectarSchedule(ctx context.Context, cfg *config.Config, client *nectar.Client,
	cache *config.CachedSchedule,
) bool {
	if client == nil || !client.Configured() {
		if !cfg.NectarScheduleEnabled {
			slog.Info("horario: el de siempre, sin festivos (sin puente y NECTAR_SCHEDULE_ENABLED apagada)",
				"horario", cache.Current().String())
			return false
		}
		// Encendida sin con qué llamar (puente apagado): configuración a medias que hay que
		// ver, pero el bot no se queda sin horario.
		slog.Error("NECTAR_SCHEDULE_ENABLED encendida sin puente de Néctar: rige el horario de siempre",
			"horario", cache.Current().String())
		return false
	}
	every := time.Duration(cfg.NectarScheduleRefreshMin) * time.Minute
	if cfg.NectarScheduleRefreshMin <= 0 {
		// 0 o negativo no es «no refrescar»: sería un bucle sin pausa contra Néctar.
		every = config.DefaultScheduleRefreshMin * time.Minute
	}
	r := newNectarScheduleRefresher(client, cache, cfg.NectarScheduleChannelID, every)
	r.holidaysOnly = !cfg.NectarScheduleEnabled
	source := "horario: fuente NÉCTAR (GET /api/bot/schedule)"
	if r.holidaysOnly {
		source = "horario: el de siempre, con los festivos de NÉCTAR (GET /api/bot/schedule)"
	}
	slog.Info(source, "cada_min", int(every.Minutes()), "channel_id", cfg.NectarScheduleChannelID,
		"respaldo", cache.Current().String())
	safeGo("nectar-schedule", func() { r.Run(ctx) })
	return true
}
