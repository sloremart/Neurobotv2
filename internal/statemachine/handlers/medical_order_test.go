package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// --- Mock OCR service ---
type mockOCRServer struct {
	handler http.HandlerFunc
}

func newMockOCRServer(response string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(response))
	}))
}

// --- Mock procedure repo ---
type mockProcedureRepo struct {
	findByCodeFn   func(ctx context.Context, code string) (*domain.Procedure, error)
	searchByNameFn func(ctx context.Context, name string) ([]domain.Procedure, error)
}

func (m *mockProcedureRepo) FindByCode(ctx context.Context, code string) (*domain.Procedure, error) {
	if m.findByCodeFn != nil {
		return m.findByCodeFn(ctx, code)
	}
	return nil, nil
}

func (m *mockProcedureRepo) SearchByName(ctx context.Context, name string) ([]domain.Procedure, error) {
	if m.searchByNameFn != nil {
		return m.searchByNameFn(ctx, name)
	}
	return nil, nil
}

func (m *mockProcedureRepo) FindByID(ctx context.Context, id int) (*domain.Procedure, error) {
	return nil, nil
}

func (m *mockProcedureRepo) FindAllActive(ctx context.Context) ([]domain.Procedure, error) {
	return nil, nil
}

func (m *mockProcedureRepo) FindSubjectTypeForCups(ctx context.Context, cupsCode string) (int, error) {
	return 0, nil
}

func (m *mockProcedureRepo) FindMedicosForCups(_ context.Context, _ string) ([]int, error) {
	return nil, nil
}

// wrapOpenAIResponse wraps a content string into a full OpenAI API response JSON.
func wrapOpenAIResponse(content string) string {
	resp := map[string]interface{}{
		"choices": []map[string]interface{}{
			{"message": map[string]string{"content": content}},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// --- Tests ---

func TestAskMedicalOrder_Automatic(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskMedicalOrder, askMedicalOrderHandler(nil))

	sess := testSess(sm.StateAskMedicalOrder)
	result, err := m.Process(context.Background(), sess, textM("any"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER, got %s", result.NextState)
	}
}

func TestUploadMedicalOrder_TextReceived(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(nil, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, textM("hello"))
	if err != nil {
		t.Fatal(err)
	}
	// Should stay in same state asking for image
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER, got %s", result.NextState)
	}
}

func TestUploadMedicalOrder_RetryPostback(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(nil, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, postbackM("retry_photo"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER, got %s", result.NextState)
	}
}

func TestUploadMedicalOrder_EscalateAgentPostback(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(nil, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, postbackM("escalate_agent"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
}

func TestAskManualCups_TooShort(t *testing.T) {
	procRepo := &mockProcedureRepo{}
	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))

	sess := testSess(sm.StateAskManualCups)
	result, err := m.Process(context.Background(), sess, textM("ab"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskManualCups {
		t.Errorf("expected ASK_MANUAL_CUPS (retry), got %s", result.NextState)
	}
}

func TestAskManualCups_SingleResult(t *testing.T) {
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(ctx context.Context, name string) ([]domain.Procedure, error) {
			return []domain.Procedure{
				{ID: 1, Code: "890271", Name: "Electromiografia", RequiredSpaces: 2},
			}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))

	sess := testSess(sm.StateAskManualCups)
	result, err := m.Process(context.Background(), sess, textM("electro"))
	if err != nil {
		t.Fatal(err)
	}
	// Single result → auto-select → next state
	if result.NextState != sm.StateCheckSpecialCups {
		t.Errorf("expected CHECK_SPECIAL_CUPS, got %s", result.NextState)
	}
	// Check context was set
	found := false
	for k, v := range result.UpdateCtx {
		if k == "cups_code" && v == "890271" {
			found = true
		}
	}
	if !found {
		t.Error("expected cups_code=890271 in UpdateCtx")
	}
}

func TestAskManualCups_MultipleResults(t *testing.T) {
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(ctx context.Context, name string) ([]domain.Procedure, error) {
			return []domain.Procedure{
				{ID: 1, Code: "890271", Name: "Electromiografia"},
				{ID: 2, Code: "890272", Name: "Electroencefalograma"},
			}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))

	sess := testSess(sm.StateAskManualCups)
	result, err := m.Process(context.Background(), sess, textM("electro"))
	if err != nil {
		t.Fatal(err)
	}
	// Multiple results → show list → SELECT_PROCEDURE
	if result.NextState != sm.StateSelectProcedure {
		t.Errorf("expected SELECT_PROCEDURE, got %s", result.NextState)
	}
}

func TestAskManualCups_NoResults(t *testing.T) {
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(ctx context.Context, name string) ([]domain.Procedure, error) {
			return []domain.Procedure{}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))

	sess := testSess(sm.StateAskManualCups)
	result, err := m.Process(context.Background(), sess, textM("xyzabc"))
	if err != nil {
		t.Fatal(err)
	}
	// No results → stay in same state
	if result.NextState != sm.StateAskManualCups {
		t.Errorf("expected ASK_MANUAL_CUPS, got %s", result.NextState)
	}
}

func TestSelectProcedure_ValidPostback(t *testing.T) {
	procs := []struct {
		ID             int    `json:"ID"`
		Code           string `json:"Code"`
		Name           string `json:"Name"`
		ServiceName    string `json:"ServiceName"`
		RequiredSpaces int    `json:"RequiredSpaces"`
	}{
		{ID: 10, Code: "890271", Name: "Electromiografia", ServiceName: "Neurofisiologia", RequiredSpaces: 2},
	}
	procsJSON, _ := json.Marshal(procs)

	m := sm.NewMachine()
	m.Register(sm.StateSelectProcedure, selectProcedureHandler())

	sess := testSess(sm.StateSelectProcedure)
	sess.Context["search_procedures_json"] = string(procsJSON)

	msg := bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", IsPostback: true,
		PostbackPayload: fmt.Sprintf("%d", 10), Text: "10",
	}
	result, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckSpecialCups {
		t.Errorf("expected CHECK_SPECIAL_CUPS, got %s", result.NextState)
	}
}

func TestSelectProcedure_TextFallback(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateSelectProcedure, selectProcedureHandler())

	sess := testSess(sm.StateSelectProcedure)
	result, err := m.Process(context.Background(), sess, textM("resonancia"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskManualCups {
		t.Errorf("expected ASK_MANUAL_CUPS, got %s", result.NextState)
	}
}

func TestConfirmOCRResult_Correct(t *testing.T) {
	cups := []services.CUPSEntry{{Code: "890271", Name: "Electromiografia", Quantity: 1}}
	cupsJSON, _ := json.Marshal(cups)

	m := sm.NewMachine()
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(&mockProcedureRepo{}, nil))

	sess := testSess(sm.StateConfirmOCRResult)
	sess.Context["ocr_cups_json"] = string(cupsJSON)

	result, err := m.Process(context.Background(), sess, postbackM("ocr_correct"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckSpecialCups {
		t.Errorf("expected CHECK_SPECIAL_CUPS, got %s", result.NextState)
	}
}

func TestConfirmOCRResult_Incorrect(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(&mockProcedureRepo{}, nil))

	sess := testSess(sm.StateConfirmOCRResult)
	result, err := m.Process(context.Background(), sess, postbackM("ocr_incorrect"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER, got %s", result.NextState)
	}
}

// --- uploadMedicalOrderHandler image tests ---

// newMockFileServer creates a test server that serves JPEG bytes (for AnalyzeDocument download).
func newMockFileServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}) // JPEG magic bytes
	}))
}

func imageMsg(url string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:          "msg-img",
		Phone:       "+573001234567",
		MessageType: "image",
		ImageURL:    url,
		ReceivedAt:  time.Now(),
	}
}

func documentMsg(url string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:          "msg-doc",
		Phone:       "+573001234567",
		MessageType: "document",
		DocumentURL: url,
		ReceivedAt:  time.Now(),
	}
}

func TestUploadMedicalOrder_ImageSuccess(t *testing.T) {
	ocrResponse := `{"choices":[{"message":{"content":"{\"cups\":[{\"cups_code\":\"890271\",\"cups_name\":\"EMG\",\"quantity\":1}],\"entity\":\"\",\"error\":\"\"}"}}]}`
	ocrSrv := newMockOCRServer(ocrResponse)
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	ocrSvc := services.NewOCRServiceForTest(ocrSrv.URL)

	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, imageMsg(fileSrv.URL+"/order.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateValidateOCR {
		t.Errorf("expected VALIDATE_OCR, got %s", result.NextState)
	}
	if result.UpdateCtx["ocr_cups_json"] == "" {
		t.Error("expected ocr_cups_json to be set")
	}
}

func TestUploadMedicalOrder_DocumentSuccess(t *testing.T) {
	ocrResponse := `{"choices":[{"message":{"content":"{\"cups\":[{\"cups_code\":\"890271\",\"cups_name\":\"EMG\",\"quantity\":1}],\"entity\":\"\",\"error\":\"\"}"}}]}`
	ocrSrv := newMockOCRServer(ocrResponse)
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	ocrSvc := services.NewOCRServiceForTest(ocrSrv.URL)

	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, documentMsg(fileSrv.URL+"/order.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateValidateOCR {
		t.Errorf("expected VALIDATE_OCR, got %s", result.NextState)
	}
	if result.UpdateCtx["ocr_cups_json"] == "" {
		t.Error("expected ocr_cups_json to be set")
	}
}

func TestUploadMedicalOrder_DocumentEmptyURL(t *testing.T) {
	ocrSvc := services.NewOCRServiceForTest("http://unused")

	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, documentMsg(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER (retry), got %s", result.NextState)
	}
}

func TestUploadMedicalOrder_ImageEmptyURL(t *testing.T) {
	ocrSvc := services.NewOCRServiceForTest("http://unused")

	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, imageMsg(""))
	if err != nil {
		t.Fatal(err)
	}
	// Should stay in same state
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER (retry), got %s", result.NextState)
	}
}

func TestUploadMedicalOrder_ImageOCRError(t *testing.T) {
	// OCR server returns 500 to simulate OCR error
	ocrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
	}))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	ocrSvc := services.NewOCRServiceForTest(ocrSrv.URL)

	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, imageMsg(fileSrv.URL+"/order.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	// Should stay in same state with buttons
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER (retry), got %s", result.NextState)
	}
}

func TestUploadMedicalOrder_ImageNoCups(t *testing.T) {
	ocrResponse := `{"choices":[{"message":{"content":"{\"cups\":[],\"entity\":\"\",\"error\":\"\"}"}}]}`
	ocrSrv := newMockOCRServer(ocrResponse)
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	ocrSvc := services.NewOCRServiceForTest(ocrSrv.URL)

	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, imageMsg(fileSrv.URL+"/order.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	// No CUPS found -> stay in same state with buttons
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER (retry), got %s", result.NextState)
	}
}

func TestUploadMedicalOrder_StoresDocument(t *testing.T) {
	// OCR returns document "19262024" in the response
	ocrResponse := `{"choices":[{"message":{"content":"{\"cups\":[{\"cups_code\":\"890271\",\"cups_name\":\"EMG\",\"quantity\":1}],\"entity\":\"\",\"error\":\"\",\"documento\":\"19262024\"}"}}]}`
	ocrSrv := newMockOCRServer(ocrResponse)
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	ocrSvc := services.NewOCRServiceForTest(ocrSrv.URL)

	m := sm.NewMachine()
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, nil))

	sess := testSess(sm.StateUploadMedicalOrder)
	result, err := m.Process(context.Background(), sess, imageMsg(fileSrv.URL+"/order.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateValidateOCR {
		t.Errorf("expected VALIDATE_OCR, got %s", result.NextState)
	}
	if result.UpdateCtx["ocr_document"] != "19262024" {
		t.Errorf("expected ocr_document=19262024, got %q", result.UpdateCtx["ocr_document"])
	}
}

// --- validateOCRHandler tests ---

func TestValidateOCR_EnrichesFromDB(t *testing.T) {
	procRepo := &mockProcedureRepo{
		findByCodeFn: func(ctx context.Context, code string) (*domain.Procedure, error) {
			if code == "890271" {
				return &domain.Procedure{ID: 1, Code: "890271", Name: "Electromiografia de 4 extremidades", IsActive: true}, nil
			}
			return nil, nil
		},
	}

	cups := []services.CUPSEntry{{Code: "890271", Name: "EMG", Quantity: 1}}
	cupsJSON, _ := json.Marshal(cups)

	m := sm.NewMachine()
	m.Register(sm.StateValidateOCR, validateOCRHandler(procRepo))

	sess := testSess(sm.StateValidateOCR)
	sess.Context["ocr_cups_json"] = string(cupsJSON)

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmOCRResult {
		t.Errorf("expected CONFIRM_OCR_RESULT, got %s", result.NextState)
	}
	// Verify the name was enriched from DB
	var enriched []services.CUPSEntry
	if err := json.Unmarshal([]byte(result.UpdateCtx["ocr_cups_json"]), &enriched); err != nil {
		t.Fatalf("failed to unmarshal enriched cups: %v", err)
	}
	if len(enriched) != 1 || enriched[0].Name != "Electromiografia de 4 extremidades" {
		t.Errorf("expected enriched name='Electromiografia de 4 extremidades', got %q", enriched[0].Name)
	}
}

func TestValidateOCR_DocumentMismatch_NoWarning(t *testing.T) {
	procRepo := &mockProcedureRepo{
		findByCodeFn: func(ctx context.Context, code string) (*domain.Procedure, error) {
			return &domain.Procedure{ID: 1, Code: code, Name: "Procedimiento Test", IsActive: true}, nil
		},
	}

	cups := []services.CUPSEntry{{Code: "890271", Name: "Test", Quantity: 1}}
	cupsJSON, _ := json.Marshal(cups)

	m := sm.NewMachine()
	m.Register(sm.StateValidateOCR, validateOCRHandler(procRepo))

	sess := testSess(sm.StateValidateOCR)
	sess.Context["ocr_cups_json"] = string(cupsJSON)
	sess.Context["ocr_document"] = "19262024"
	sess.Context["patient_document"] = "98765432"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmOCRResult {
		t.Errorf("expected CONFIRM_OCR_RESULT, got %s", result.NextState)
	}
	// Document mismatch warning was removed — OCR document is unreliable
	for _, msg := range result.Messages {
		if bm, ok := msg.(*sm.ButtonMessage); ok {
			if strings.Contains(bm.Text, "no coincide") {
				t.Error("document mismatch warning should not appear (feature removed)")
			}
		}
	}
}

func TestValidateOCR_DocumentMatch(t *testing.T) {
	procRepo := &mockProcedureRepo{
		findByCodeFn: func(ctx context.Context, code string) (*domain.Procedure, error) {
			return &domain.Procedure{ID: 1, Code: code, Name: "Procedimiento Test", IsActive: true}, nil
		},
	}

	cups := []services.CUPSEntry{{Code: "890271", Name: "Test", Quantity: 1}}
	cupsJSON, _ := json.Marshal(cups)

	m := sm.NewMachine()
	m.Register(sm.StateValidateOCR, validateOCRHandler(procRepo))

	sess := testSess(sm.StateValidateOCR)
	sess.Context["ocr_cups_json"] = string(cupsJSON)
	sess.Context["ocr_document"] = "19262024"
	sess.Context["patient_document"] = "19262024"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmOCRResult {
		t.Errorf("expected CONFIRM_OCR_RESULT, got %s", result.NextState)
	}
	// Check that NO warning about document mismatch appears
	for _, msg := range result.Messages {
		if bm, ok := msg.(*sm.ButtonMessage); ok {
			if strings.Contains(bm.Text, "no coincide") {
				t.Error("did not expect 'no coincide' warning when documents match")
			}
		}
	}
}

// --- ocrFailedHandler test ---

func TestOCRFailed_RedirectsToUpload(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateOCRFailed, ocrFailedHandler(nil))

	sess := testSess(sm.StateOCRFailed)

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateUploadMedicalOrder {
		t.Errorf("expected UPLOAD_MEDICAL_ORDER, got %s", result.NextState)
	}
}

// --- askManualCupsHandler search error test ---

func TestAskManualCups_SearchError(t *testing.T) {
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(ctx context.Context, name string) ([]domain.Procedure, error) {
			return nil, fmt.Errorf("database timeout")
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))

	sess := testSess(sm.StateAskManualCups)
	result, err := m.Process(context.Background(), sess, textM("electro"))
	if err != nil {
		t.Fatal(err)
	}
	// Error -> stays in same state
	if result.NextState != sm.StateAskManualCups {
		t.Errorf("expected ASK_MANUAL_CUPS (retry), got %s", result.NextState)
	}
}

func TestConfirmOCRResult_PreservesSedation(t *testing.T) {
	cups := []services.CUPSEntry{
		{Code: "883210", Name: "RM cerebro bajo sedacion", Quantity: 1, IsSedated: true},
	}
	cupsJSON, _ := json.Marshal(cups)

	m := sm.NewMachine()
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(&mockProcedureRepo{}, nil))

	sess := testSess(sm.StateConfirmOCRResult)
	sess.Context["ocr_cups_json"] = string(cupsJSON)

	result, err := m.Process(context.Background(), sess, postbackM("ocr_correct"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckSpecialCups {
		t.Errorf("expected CHECK_SPECIAL_CUPS, got %s", result.NextState)
	}
	// ocr_is_sedated should be propagated from the original OCR data
	if result.UpdateCtx["ocr_is_sedated"] != "1" {
		t.Errorf("expected ocr_is_sedated=1, got %q", result.UpdateCtx["ocr_is_sedated"])
	}
	// procedures_json should also have is_sedated preserved
	var groups []services.CUPSGroup
	if err := json.Unmarshal([]byte(result.UpdateCtx["procedures_json"]), &groups); err != nil {
		t.Fatalf("failed to unmarshal procedures_json: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Cups) != 1 {
		t.Fatalf("expected 1 group with 1 cup")
	}
	if !groups[0].Cups[0].IsSedated {
		t.Error("expected IsSedated=true in procedures_json after GroupByService")
	}
}
