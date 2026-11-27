package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// mockLocationReader implements LocationReader for tests.
type mockLocationReader struct {
	FindActiveFn func(ctx context.Context) ([]domain.CenterLocation, error)
}

func (m *mockLocationReader) FindActive(ctx context.Context) ([]domain.CenterLocation, error) {
	if m.FindActiveFn != nil {
		return m.FindActiveFn(ctx)
	}
	return nil, nil
}

// --- SHOW_RESULTS ---

func TestShowResults_WithURLAndVideo(t *testing.T) {
	cfg := &config.Config{
		ResultsURL:      "https://resultados.example.com",
		ResultsVideoURL: "https://video.example.com/tutorial",
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowResults, showResultsHandler(cfg))

	sess := testSess(sm.StateShowResults)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
	// Check message contains both URLs
	msg := extractText(result)
	if !strings.Contains(msg, "resultados.example.com") {
		t.Errorf("expected results URL in message, got: %s", msg)
	}
	if !strings.Contains(msg, "video.example.com") {
		t.Errorf("expected video URL in message, got: %s", msg)
	}
}

func TestShowResults_WithURLOnly(t *testing.T) {
	cfg := &config.Config{
		ResultsURL: "https://resultados.example.com",
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowResults, showResultsHandler(cfg))

	sess := testSess(sm.StateShowResults)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	msg := extractText(result)
	if !strings.Contains(msg, "resultados.example.com") {
		t.Errorf("expected results URL in message, got: %s", msg)
	}
	if strings.Contains(msg, "video") {
		t.Errorf("expected no video reference when VideoURL is empty, got: %s", msg)
	}
}

func TestShowResults_NoURL_Fallback(t *testing.T) {
	cfg := &config.Config{}

	m := sm.NewMachine()
	m.Register(sm.StateShowResults, showResultsHandler(cfg))

	sess := testSess(sm.StateShowResults)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	msg := extractText(result)
	if !strings.Contains(msg, "comunícate") {
		t.Errorf("expected fallback message, got: %s", msg)
	}
}

// --- SHOW_LOCATIONS ---

func TestShowLocations_WithLocations(t *testing.T) {
	locationRepo := &mockLocationReader{
		FindActiveFn: func(ctx context.Context) ([]domain.CenterLocation, error) {
			return []domain.CenterLocation{
				{Name: "Sede Norte", Address: "Calle 100 #15-20", Phone: "6011234567", GoogleMapsURL: "https://maps.google.com/norte"},
				{Name: "Sede Sur", Address: "Calle 10 #5-30", Phone: "", GoogleMapsURL: ""},
			}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowLocations, showLocationsHandler(locationRepo))

	sess := testSess(sm.StateShowLocations)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
	msg := extractText(result)
	if !strings.Contains(msg, "Sede Norte") {
		t.Errorf("expected 'Sede Norte' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "Sede Sur") {
		t.Errorf("expected 'Sede Sur' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "6011234567") {
		t.Errorf("expected phone in message, got: %s", msg)
	}
}

func TestShowLocations_Empty_Fallback(t *testing.T) {
	locationRepo := &mockLocationReader{
		FindActiveFn: func(ctx context.Context) ([]domain.CenterLocation, error) {
			return []domain.CenterLocation{}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowLocations, showLocationsHandler(locationRepo))

	sess := testSess(sm.StateShowLocations)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	msg := extractText(result)
	if !strings.Contains(msg, "no tenemos sedes configuradas") {
		t.Errorf("expected fallback message for empty locations, got: %s", msg)
	}
}

func TestShowLocations_NilRepo_Fallback(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowLocations, showLocationsHandler(nil))

	sess := testSess(sm.StateShowLocations)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	msg := extractText(result)
	if !strings.Contains(msg, "no tenemos sedes configuradas") {
		t.Errorf("expected fallback message for nil repo, got: %s", msg)
	}
}

// extractText joins all text messages from a result for assertion.
func extractText(result *sm.StateResult) string {
	var sb strings.Builder
	for _, m := range result.Messages {
		if txt, ok := m.(*sm.TextMessage); ok {
			sb.WriteString(txt.Text)
			sb.WriteString(" ")
		}
	}
	return sb.String()
}

// --- SHOW_IMAGING ---

func TestShowImaging_SendsLinkAndAutoCloses(t *testing.T) {
	url := "https://imagenes.example.com/portal"
	m := sm.NewMachine()
	m.Register(sm.StateShowImaging, showImagingHandler(&config.Config{ImagingResultsURL: url}))

	result, err := m.Process(context.Background(), testSess(sm.StateShowImaging), textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("esperaba TERMINATED, fue %s", result.NextState)
	}
	want := "🩻 *Resultados de imágenes*\n\nConsulta y descarga tus resultados de imágenes diagnósticas en el portal: " + url
	if got := extractText(result); !strings.HasPrefix(got, want) {
		t.Errorf("texto = %q", got)
	}
}
