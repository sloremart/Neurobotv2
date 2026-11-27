package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// LocationReader provides read-only access to center locations.
type LocationReader interface {
	FindActive(ctx context.Context) ([]domain.CenterLocation, error)
}

// RegisterResultsAndLocationHandlers registers SHOW_RESULTS and SHOW_LOCATIONS handlers.
func RegisterResultsAndLocationHandlers(m *sm.Machine, cfg *config.Config, locationRepo LocationReader) {
	m.Register(sm.StateShowResults, showResultsHandler(cfg))
	m.Register(sm.StateShowImaging, showImagingHandler(cfg))
	m.Register(sm.StateShowLocations, showLocationsHandler(locationRepo))
}

// SHOW_RESULTS (automático) — muestra enlace para consultar resultados + video instructivo.
func showResultsHandler(cfg *config.Config) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		var text string
		if cfg.ResultsURL != "" {
			text = fmt.Sprintf("Ingresa a nuestra página: %s", cfg.ResultsURL)
			if cfg.ResultsVideoURL != "" {
				text += fmt.Sprintf("\n\nEn el siguiente video encontrarás el paso a paso para descargar tus resultados. 👇\n%s", cfg.ResultsVideoURL)
			}
		} else {
			text = "Para consultar tus resultados médicos, por favor comunícate con nuestra línea de atención o acércate a nuestras instalaciones."
		}

		return buildAutoCloseResult(text).
			WithEvent("results_shown", nil), nil
	}
}

// imagingResultsText es el mensaje con el enlace al portal de resultados de imágenes.
func imagingResultsText(url string) string {
	return "🩻 *Resultados de imágenes*\n\nConsulta y descarga tus resultados de imágenes diagnósticas en el portal: " + url
}

// SHOW_IMAGING (automático) — envía el enlace al portal de resultados de imágenes y cierra.
func showImagingHandler(cfg *config.Config) sm.StateHandler {
	return func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		return buildAutoCloseResult(imagingResultsText(cfg.ImagingResultsURL)).
			WithEvent("imaging_link_sent", nil), nil
	}
}

// SHOW_LOCATIONS (automático) — muestra sedes desde BD local.
func showLocationsHandler(locationRepo LocationReader) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		var text string

		if locationRepo != nil {
			locations, err := locationRepo.FindActive(ctx)
			if err == nil && len(locations) > 0 {
				var sb strings.Builder
				sb.WriteString("📍 *Nuestras sedes:*\n")
				for i, loc := range locations {
					sb.WriteString(fmt.Sprintf("\n*%d. %s*\n", i+1, loc.Name))
					sb.WriteString(fmt.Sprintf("   📫 %s\n", loc.Address))
					if loc.Phone != "" {
						sb.WriteString(fmt.Sprintf("   📞 %s\n", loc.Phone))
					}
					if loc.GoogleMapsURL != "" {
						sb.WriteString(fmt.Sprintf("   🗺 %s\n", loc.GoogleMapsURL))
					}
				}
				text = sb.String()
			}
		}

		if text == "" {
			text = "📍 Actualmente no tenemos sedes configuradas. Comunícate con un agente para más información."
		}

		return buildAutoCloseResult(text).
			WithEvent("locations_shown", nil), nil
	}
}
