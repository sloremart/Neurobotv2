package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
)

// Auditoría queries SIESA P3: un from/to arbitrario del query string no puede disparar una
// agregación sobre todo el histórico de citas. La amplitud se acota a maxAnalyticsRangeDays
// moviendo `from` hacia adelante, y toda query de analytics lleva deadline.

func TestClampAnalyticsRange(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"dentro_del_tope_no_cambia", "2026-06-01", "2026-06-30", "2026-06-01"},
		{"exacto_90_no_cambia", "2026-04-01", "2026-06-30", "2026-04-01"},
		{"rango_gigante_se_recorta", "1900-01-01", "2999-12-31", "2999-10-02"},
		{"from_vacio_pasa_igual", "", "2026-06-30", ""},
		{"to_vacio_pasa_igual", "2026-06-01", "", "2026-06-01"},
		{"from_invalido_pasa_igual", "no-fecha", "2026-06-30", "no-fecha"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clampAnalyticsRange(c.from, c.to)
			if got != c.want {
				t.Errorf("clampAnalyticsRange(%q,%q) = %q, want %q", c.from, c.to, got, c.want)
			}
		})
	}
}

func TestHandleSiesaCitasEstado_ClampsRangeAndSetsDeadline(t *testing.T) {
	var gotFrom, gotTo string
	var hadDeadline bool
	analytics := &mockSiesaAnalyticsReader{
		citasEstadoFn: func(ctx context.Context, from, to string) ([]domain.AppointmentStateRow, error) {
			gotFrom, gotTo = from, to
			_, hadDeadline = ctx.Deadline()
			return nil, nil
		},
	}
	h := &InternalHandler{siesaAnalytics: analytics}
	req := httptest.NewRequest("GET", "/api/internal/siesa/citas-estado?from=1900-01-01&to=2999-12-31", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaCitasEstado(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotFrom != "2999-10-02" || gotTo != "2999-12-31" {
		t.Errorf("rango recibido por el repo = [%s, %s], want [2999-10-02, 2999-12-31]", gotFrom, gotTo)
	}
	if !hadDeadline {
		t.Error("la query de analytics debe llevar context con deadline")
	}
}

func TestHandleSiesaNoShow_ClampsRangeAndSetsDeadline(t *testing.T) {
	var gotFrom string
	var hadDeadline bool
	analytics := &mockSiesaAnalyticsReader{
		noShowFn: func(ctx context.Context, from, _ string) ([]domain.NoShowRow, error) {
			gotFrom = from
			_, hadDeadline = ctx.Deadline()
			return nil, nil
		},
	}
	h := &InternalHandler{siesaAnalytics: analytics}
	req := httptest.NewRequest("GET", "/api/internal/siesa/no-show?from=1900-01-01&to=2999-12-31", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaNoShow(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotFrom != "2999-10-02" {
		t.Errorf("from recibido = %s, want 2999-10-02", gotFrom)
	}
	if !hadDeadline {
		t.Error("la query de analytics debe llevar context con deadline")
	}
}

func TestHandleSiesaBotShare_ClampsRange(t *testing.T) {
	var gotFrom string
	analytics := &mockSiesaAnalyticsReader{
		botCreatedFn: func(_ context.Context, _, from, _ string) ([]domain.BotCreatedRow, error) {
			gotFrom = from
			return nil, nil
		},
	}
	h := &InternalHandler{siesaAnalytics: analytics, cfg: &config.Config{SIESAAssignUserCedula: "123456"}}
	req := httptest.NewRequest("GET", "/api/internal/siesa/bot-share?from=1900-01-01&to=2026-06-30", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaBotShare(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotFrom != "2026-04-01" { // 2026-06-30 - 90 días
		t.Errorf("from recibido = %s, want 2026-04-01", gotFrom)
	}
}

func TestHandleSiesaConversion_ClampsRange(t *testing.T) {
	var gotFrom string
	var funnelFrom time.Time
	analytics := &mockSiesaAnalyticsReader{
		botCreatedFn: func(_ context.Context, _, from, _ string) ([]domain.BotCreatedRow, error) {
			gotFrom = from
			return nil, nil
		},
	}
	eventRepo := &mockEventKPIReader{
		funnelFn: func(_ context.Context, from, _ time.Time) (*localrepo.FunnelData, error) {
			funnelFrom = from
			return &localrepo.FunnelData{}, nil
		},
	}
	h := &InternalHandler{
		siesaAnalytics: analytics,
		eventRepo:      eventRepo,
		cfg:            &config.Config{SIESAAssignUserCedula: "123456"},
	}
	req := httptest.NewRequest("GET", "/api/internal/siesa/conversion?from=1900-01-01&to=2026-06-30", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaConversion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotFrom != "2026-04-01" {
		t.Errorf("from SIESA = %s, want 2026-04-01", gotFrom)
	}
	if got := funnelFrom.Format("2006-01-02"); got != "2026-04-01" {
		t.Errorf("from del funnel local = %s, want 2026-04-01 (ambas ventanas deben coincidir)", got)
	}
}
