package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func openAIHandler(response string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": response}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func TestAnalyzeImage_Success(t *testing.T) {
	jsonResp := `{"cups": [{"cups_code": "890271", "cups_name": "Electromiografia", "quantity": 1}], "entity": "Sura", "notes": ""}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if len(result.Cups) != 1 {
		t.Fatalf("expected 1 CUP, got %d", len(result.Cups))
	}
	if result.Cups[0].Code != "890271" {
		t.Errorf("expected 890271, got %s", result.Cups[0].Code)
	}
	if result.Entity != "Sura" {
		t.Errorf("expected Sura, got %s", result.Entity)
	}
}

func TestAnalyzeImage_NoCups(t *testing.T) {
	jsonResp := `{"cups": [], "entity": "", "notes": ""}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/blurry.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Error("expected not success when no cups")
	}
}

func TestAnalyzeImage_ErrorResponse(t *testing.T) {
	jsonResp := `{"cups": [], "error": "Imagen borrosa"}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/blurry.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Error("expected not success when error in response")
	}
	if result.Error != "Imagen borrosa" {
		t.Errorf("expected error message, got %q", result.Error)
	}
}

func TestAnalyzeImage_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	_, err := svc.AnalyzeImage(context.Background(), "https://example.com/image.jpg")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestAnalyzeImage_CapitalSalud(t *testing.T) {
	jsonResp := `{"cups": [{"cups_code": "890271", "cups_name": "EMG", "quantity": 1}], "entity": "Capital Salud"}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/order.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if result.Entity != "Capital Salud" {
		t.Errorf("expected Capital Salud, got %q", result.Entity)
	}
}

func TestAnalyzeImage_QuantityDefault(t *testing.T) {
	// quantity=0 should be normalized to 1
	jsonResp := `{"cups": [{"cups_code": "890271", "cups_name": "EMG", "quantity": 0}]}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if result.Cups[0].Quantity != 1 {
		t.Errorf("expected quantity 1 (default), got %d", result.Cups[0].Quantity)
	}
}

func TestAnalyzeImage_WithDocument(t *testing.T) {
	jsonResp := `{"documento": "19262024", "cups": [{"cups_code": "890271", "cups_name": "EMG", "quantity": 1}], "entity": "Sura"}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/order.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if result.Document != "19262024" {
		t.Errorf("expected document 19262024, got %q", result.Document)
	}
}

func TestAnalyzeImage_WithSedation(t *testing.T) {
	jsonResp := `{"cups": [{"cups_code": "890271", "cups_name": "EMG bajo sedacion", "quantity": 1, "is_sedated": true}], "entity": "Sura"}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/order.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if len(result.Cups) != 1 {
		t.Fatalf("expected 1 CUP, got %d", len(result.Cups))
	}
	if result.Cups[0].IsSedated != true {
		t.Error("expected IsSedated=true for sedated procedure")
	}
}

func TestAnalyzeImage_NoTableDetected(t *testing.T) {
	jsonResp := `{"cups": [], "error": "no_table_detected"}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/not-an-order.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Error("expected Success=false when no table detected")
	}
	if result.Error != "no_table_detected" {
		t.Errorf("expected error 'no_table_detected', got %q", result.Error)
	}
}

func TestExtractJSON_MarkdownBlock(t *testing.T) {
	input := "```json\n{\"cups\": []}\n```"
	result := extractJSON(input)
	if result != `{"cups": []}` {
		t.Errorf("expected clean JSON, got %q", result)
	}
}

func TestExtractJSON_PlainJSON(t *testing.T) {
	input := `{"cups": [{"code": "890271"}]}`
	result := extractJSON(input)
	if result != input {
		t.Errorf("expected unchanged JSON, got %q", result)
	}
}

func TestExtractJSON_GenericCodeBlock(t *testing.T) {
	input := "```\n{\"key\": \"val\"}\n```"
	result := extractJSON(input)
	if result != `{"key": "val"}` {
		t.Errorf("expected extracted JSON, got %q", result)
	}
}

func TestAnalyzeImage_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(openAIHandler("not valid json at all"))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/image.jpg")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Success {
		t.Error("expected Success=false for malformed JSON")
	}
}

// --- AnalyzeText tests ---

func TestAnalyzeText_Success(t *testing.T) {
	jsonResp := `{"cups": [{"cups_code": "883141", "cups_name": "Resonancia cerebral simple", "quantity": 1}], "entity": null, "notes": ""}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeText(context.Background(), "Resonancia cerebral simple codigo 883141 cantidad 1")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if len(result.Cups) != 1 {
		t.Fatalf("expected 1 CUP, got %d", len(result.Cups))
	}
	if result.Cups[0].Code != "883141" {
		t.Errorf("expected 883141, got %s", result.Cups[0].Code)
	}
	if result.Cups[0].Name != "Resonancia cerebral simple" {
		t.Errorf("expected 'Resonancia cerebral simple', got %s", result.Cups[0].Name)
	}
}

func TestAnalyzeText_MultipleCups(t *testing.T) {
	jsonResp := `{"cups": [{"cups_code": "930810", "cups_name": "EMG", "quantity": 1}, {"cups_code": "883210", "cups_name": "RM columna lumbar", "quantity": 1}]}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeText(context.Background(), "EMG codigo 930810, Resonancia columna lumbar codigo 883210")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if len(result.Cups) != 2 {
		t.Fatalf("expected 2 CUPs, got %d", len(result.Cups))
	}
}

func TestAnalyzeText_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"internal"}`))
	}))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	_, err := svc.AnalyzeText(context.Background(), "Resonancia cerebral")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestAnalyzeText_NoResults(t *testing.T) {
	jsonResp := `{"cups": [], "error": "no se detectaron procedimientos"}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeText(context.Background(), "esto no es una orden medica")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Error("expected not success when no cups detected")
	}
}

func TestAnalyzeImage_WithObservations(t *testing.T) {
	jsonResp := `{"cups": [{"cups_code": "883512", "cups_name": "RM Articulacion MS", "quantity": 1, "is_sedated": false, "is_contrasted": false, "observations": "bilateral AMB"}], "entity": "Sura", "notes": ""}`
	srv := httptest.NewServer(openAIHandler(jsonResp))
	defer srv.Close()

	svc := NewOCRServiceForTest(srv.URL)
	result, err := svc.AnalyzeImage(context.Background(), "https://example.com/image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if len(result.Cups) != 1 {
		t.Fatalf("expected 1 CUP, got %d", len(result.Cups))
	}
	if result.Cups[0].Observations != "bilateral AMB" {
		t.Errorf("expected observations 'bilateral AMB', got %q", result.Cups[0].Observations)
	}
}
