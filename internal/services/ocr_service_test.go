package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// capturingTransport responde 200 a todo y guarda la última petición: permite probar qué
// credencial sale hacia cada anfitrión sin red.
type capturingTransport struct{ last *http.Request }

func (c *capturingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.last = r
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data")),
		Header:     http.Header{},
		Request:    r,
	}, nil
}

func newDownloadSvc(rt http.RoundTripper) *OCRService {
	s := NewOCRService("k", "m", "", "bird-key")
	s.client = &http.Client{Transport: rt}
	return s
}

func TestDownloadMedia_NectarHostSignsRequest(t *testing.T) {
	rt := &capturingTransport{}
	s := newDownloadSvc(rt)
	s.SetNectarCredentials("https://nectar.colibrixa.com", "service-key", "req-secret")

	if _, err := s.downloadMedia(context.Background(), "https://nectar.colibrixa.com/api/bot/attachments/7"); err != nil {
		t.Fatalf("downloadMedia: %v", err)
	}
	if got := rt.last.Header.Get("X-Bot-Service-Key"); got != "service-key" {
		t.Errorf("X-Bot-Service-Key = %q", got)
	}
	if rt.last.Header.Get("X-Nectar-Signature") == "" || rt.last.Header.Get("X-Nectar-Request-Timestamp") == "" {
		t.Errorf("falta la firma X-Nectar-*: %v", rt.last.Header)
	}
	if rt.last.Header.Get("Authorization") != "" {
		t.Error("la credencial de Bird no debe salir hacia Néctar")
	}
}

func TestDownloadMedia_NectarHostWrongSchemeNoCredential(t *testing.T) {
	rt := &capturingTransport{}
	s := newDownloadSvc(rt)
	s.SetNectarCredentials("https://nectar.colibrixa.com", "service-key", "req-secret")

	if _, err := s.downloadMedia(context.Background(), "http://nectar.colibrixa.com/api/bot/attachments/7"); err != nil {
		t.Fatalf("downloadMedia: %v", err)
	}
	h := rt.last.Header
	if h.Get("X-Bot-Service-Key") != "" || h.Get("X-Nectar-Signature") != "" || h.Get("Authorization") != "" {
		t.Errorf("http no debe recibir credenciales: %v", h)
	}
}

func TestDownloadMedia_BirdHostUnchanged(t *testing.T) {
	rt := &capturingTransport{}
	s := newDownloadSvc(rt)
	s.SetNectarCredentials("https://nectar.colibrixa.com", "service-key", "req-secret")

	if _, err := s.downloadMedia(context.Background(), "https://media.api.bird.com/x.jpg"); err != nil {
		t.Fatalf("downloadMedia: %v", err)
	}
	if got := rt.last.Header.Get("Authorization"); got != "AccessKey bird-key" {
		t.Errorf("Authorization = %q", got)
	}
	if rt.last.Header.Get("X-Bot-Service-Key") != "" || rt.last.Header.Get("X-Nectar-Signature") != "" {
		t.Error("las credenciales de Néctar no deben salir hacia Bird")
	}
}

func TestDownloadMedia_OtherHostNoCredential(t *testing.T) {
	rt := &capturingTransport{}
	s := newDownloadSvc(rt)
	s.SetNectarCredentials("https://nectar.colibrixa.com", "service-key", "req-secret")

	if _, err := s.downloadMedia(context.Background(), "https://evil.example.com/x.jpg"); err != nil {
		t.Fatalf("downloadMedia: %v", err)
	}
	h := rt.last.Header
	if h.Get("Authorization") != "" || h.Get("X-Bot-Service-Key") != "" || h.Get("X-Nectar-Signature") != "" {
		t.Errorf("ningún anfitrión ajeno recibe credencial: %v", h)
	}
}

// statusSeqTransport responde la secuencia de códigos dada (el último se repite) y cuenta los GET.
type statusSeqTransport struct {
	statuses []int
	calls    int
}

func (c *statusSeqTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	status := c.statuses[len(c.statuses)-1]
	if c.calls < len(c.statuses) {
		status = c.statuses[c.calls]
	}
	c.calls++
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader("data")),
		Header:     http.Header{},
		Request:    r,
	}, nil
}

// newRetrySvc arma el servicio con esperas nulas entre reintentos (los tests no esperan ~17 s).
func newRetrySvc(rt http.RoundTripper) *OCRService {
	s := newDownloadSvc(rt)
	s.SetNectarCredentials("https://nectar.colibrixa.com", "service-key", "req-secret")
	s.mediaRetryDelays = []time.Duration{0, 0, 0}
	return s
}

// TestDownloadMedia_NectarRetriesOn404: Néctar responde 404 mientras termina de bajar el archivo
// de WhatsApp; el bot reintenta y se queda con el adjunto cuando aparece.
func TestDownloadMedia_NectarRetriesOn404(t *testing.T) {
	rt := &statusSeqTransport{statuses: []int{404, 404, 200}}
	s := newRetrySvc(rt)

	data, err := s.downloadMedia(context.Background(), "https://nectar.colibrixa.com/api/bot/attachments/7")
	if err != nil {
		t.Fatalf("debía recuperarse tras dos 404: %v", err)
	}
	if string(data) != "data" || rt.calls != 3 {
		t.Fatalf("data=%q calls=%d, want data y 3 GET", data, rt.calls)
	}
}

// TestDownloadMedia_NectarGivesUpAfterRetries: 404 persistente = fallo permanente; el número de
// GET queda acotado a 1 + len(delays).
func TestDownloadMedia_NectarGivesUpAfterRetries(t *testing.T) {
	rt := &statusSeqTransport{statuses: []int{404}}
	s := newRetrySvc(rt)

	if _, err := s.downloadMedia(context.Background(), "https://nectar.colibrixa.com/api/bot/attachments/7"); err == nil {
		t.Fatal("un 404 persistente debe terminar en error")
	}
	if rt.calls != 4 {
		t.Fatalf("calls=%d, want 4 (1 + 3 reintentos)", rt.calls)
	}
}

// TestDownloadMedia_NectarNoRetryOnOtherStatus: solo el 404 se reintenta (un 401 es de credencial).
func TestDownloadMedia_NectarNoRetryOnOtherStatus(t *testing.T) {
	rt := &statusSeqTransport{statuses: []int{401, 200}}
	s := newRetrySvc(rt)

	if _, err := s.downloadMedia(context.Background(), "https://nectar.colibrixa.com/api/bot/attachments/7"); err == nil {
		t.Fatal("un 401 debe fallar sin reintentar")
	}
	if rt.calls != 1 {
		t.Fatalf("calls=%d, want 1", rt.calls)
	}
}

// TestDownloadMedia_BirdNoRetryOn404: Bird queda como estaba, un 404 falla al primer GET.
func TestDownloadMedia_BirdNoRetryOn404(t *testing.T) {
	rt := &statusSeqTransport{statuses: []int{404, 200}}
	s := newRetrySvc(rt)

	if _, err := s.downloadMedia(context.Background(), "https://media.api.bird.com/x.jpg"); err == nil {
		t.Fatal("en Bird un 404 debe fallar sin reintentar")
	}
	if rt.calls != 1 {
		t.Fatalf("calls=%d, want 1", rt.calls)
	}
}

// TestDownloadMedia_NectarRetriesOn500: Néctar responde 500 sin cuerpo si no puede escribir su
// bitácora (contrato del Plan 44: reintentar con espera); el bot reintenta como con el 404.
func TestDownloadMedia_NectarRetriesOn500(t *testing.T) {
	rt := &statusSeqTransport{statuses: []int{500, 200}}
	s := newRetrySvc(rt)

	data, err := s.downloadMedia(context.Background(), "https://nectar.colibrixa.com/api/bot/attachments/7")
	if err != nil {
		t.Fatalf("debía recuperarse tras el 500: %v", err)
	}
	if string(data) != "data" || rt.calls != 2 {
		t.Fatalf("data=%q calls=%d, want data y 2 GET", data, rt.calls)
	}
}

// TestDownloadMedia_NectarGivesUpOnPersistent500: un 500 persistente agota las mismas esperas.
func TestDownloadMedia_NectarGivesUpOnPersistent500(t *testing.T) {
	rt := &statusSeqTransport{statuses: []int{500}}
	s := newRetrySvc(rt)

	if _, err := s.downloadMedia(context.Background(), "https://nectar.colibrixa.com/api/bot/attachments/7"); err == nil {
		t.Fatal("un 500 persistente debe terminar en error")
	}
	if rt.calls != 4 {
		t.Fatalf("calls=%d, want 4 (1 + 3 reintentos)", rt.calls)
	}
}

// TestDownloadMedia_BirdNoRetryOn500: Bird no cambia, un 500 falla al primer GET.
func TestDownloadMedia_BirdNoRetryOn500(t *testing.T) {
	rt := &statusSeqTransport{statuses: []int{500, 200}}
	s := newRetrySvc(rt)

	if _, err := s.downloadMedia(context.Background(), "https://media.api.bird.com/x.jpg"); err == nil {
		t.Fatal("en Bird un 500 debe fallar sin reintentar")
	}
	if rt.calls != 1 {
		t.Fatalf("calls=%d, want 1", rt.calls)
	}
}
