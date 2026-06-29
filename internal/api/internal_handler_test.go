package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
)

// --- local mock for AppointmentRepository ---

type mockApptRepoAPI struct {
	findByAgendaAndDateFn func(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error)
	cancelFn              func(ctx context.Context, id, reason, channel, channelID string) error
	cancelBatchFn         func(ctx context.Context, ids []string, reason, channel, channelID string) error
}

func (m *mockApptRepoAPI) FindByID(ctx context.Context, id string) (*domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoAPI) FindUpcomingByPatient(ctx context.Context, pid string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoAPI) FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
	if m.findByAgendaAndDateFn != nil {
		return m.findByAgendaAndDateFn(ctx, agendaID, date)
	}
	return nil, nil
}

func (m *mockApptRepoAPI) Create(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoAPI) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	return nil
}

func (m *mockApptRepoAPI) CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error {
	return nil
}
func (m *mockApptRepoAPI) Confirm(ctx context.Context, id, ch, chID string) error { return nil }
func (m *mockApptRepoAPI) Cancel(ctx context.Context, id, reason, ch, chID string) error {
	if m.cancelFn != nil {
		return m.cancelFn(ctx, id, reason, ch, chID)
	}
	return nil
}

func (m *mockApptRepoAPI) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	return nil
}

func (m *mockApptRepoAPI) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	if m.cancelBatchFn != nil {
		return m.cancelBatchFn(ctx, ids, reason, channel, channelID)
	}
	return nil
}

func (m *mockApptRepoAPI) DeleteBatch(ctx context.Context, ids []string) error {
	return nil
}

func (m *mockApptRepoAPI) HasFutureForCup(ctx context.Context, pid, cup string) (bool, error) {
	return false, nil
}

func (m *mockApptRepoAPI) FindLastDoctorForCups(ctx context.Context, pid string, cups []string) (string, error) {
	return "", nil
}

func (m *mockApptRepoAPI) CountMonthlyByGroup(ctx context.Context, cups []string, year, month int) (int, error) {
	return 0, nil
}

func (m *mockApptRepoAPI) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoAPI) RescheduleDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (int, error) {
	return 0, nil
}

func (m *mockApptRepoAPI) SlotCountForAppointment(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *mockApptRepoAPI) WriteCreationAudit(_ context.Context, _, _ string) {}

// --- mock event logger ---

type mockEventLoggerAPI struct {
	events []mockLoggedEvent
}

type mockLoggedEvent struct {
	eventType string
	phone     string
	data      map[string]interface{}
}

func (m *mockEventLoggerAPI) LogEvent(_ context.Context, _, phone, eventType string, data map[string]interface{}) {
	m.events = append(m.events, mockLoggedEvent{eventType: eventType, phone: phone, data: data})
}

// --- mock interfaces ---

type mockWorkerStatsAPI struct {
	size, cap int
}

func (m *mockWorkerStatsAPI) QueueStats() (int, int) { return m.size, m.cap }

type mockNotifCounterAPI struct {
	count int
}

func (m *mockNotifCounterAPI) PendingCount() int { return m.count }

// --- mock EventKPIReader ---

type mockEventKPIReader struct {
	funnelFn      func(ctx context.Context, from, to time.Time) (*localrepo.FunnelData, error)
	findByPhoneFn func(ctx context.Context, phone string, from, to time.Time, eventType string, maxRows int) ([]localrepo.ChatEvent, error)
}

func (m *mockEventKPIReader) GetFunnel(ctx context.Context, from, to time.Time) (*localrepo.FunnelData, error) {
	if m.funnelFn != nil {
		return m.funnelFn(ctx, from, to)
	}
	return &localrepo.FunnelData{}, nil
}

func (m *mockEventKPIReader) FindByPhone(ctx context.Context, phone string, from, to time.Time, eventType string, maxRows int) ([]localrepo.ChatEvent, error) {
	if m.findByPhoneFn != nil {
		return m.findByPhoneFn(ctx, phone, from, to, eventType, maxRows)
	}
	return nil, nil
}

// --- mock WaitingListReader ---

type mockWaitingListReader struct {
	getDistinctCupsFn  func(ctx context.Context) ([]string, error)
	getWaitingByCupsFn func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	listFn             func(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error)
}

func (m *mockWaitingListReader) GetDistinctWaitingCups(ctx context.Context) ([]string, error) {
	if m.getDistinctCupsFn != nil {
		return m.getDistinctCupsFn(ctx)
	}
	return nil, nil
}

func (m *mockWaitingListReader) GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
	if m.getWaitingByCupsFn != nil {
		return m.getWaitingByCupsFn(ctx, cupsCode, limit)
	}
	return nil, nil
}

func (m *mockWaitingListReader) List(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filters, page, pageSize)
	}
	return nil, 0, nil
}

// --- Tests: CancelAgenda ---

func TestHandleCancelAgenda_BadJSON(t *testing.T) {
	h := &InternalHandler{cfg: &config.Config{}}

	req := httptest.NewRequest("POST", "/api/internal/cancel-agenda", bytes.NewBufferString("not json"))
	rec := httptest.NewRecorder()
	h.HandleCancelAgenda(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON, got %d", rec.Code)
	}
}

func TestHandleCancelAgenda_MissingFields(t *testing.T) {
	h := &InternalHandler{cfg: &config.Config{}}

	// Missing agenda_id
	body := `{"date":"2026-03-20"}`
	req := httptest.NewRequest("POST", "/api/internal/cancel-agenda", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleCancelAgenda(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing agenda_id, got %d", rec.Code)
	}

	// Missing date
	body2 := `{"agenda_id":1}`
	req2 := httptest.NewRequest("POST", "/api/internal/cancel-agenda", bytes.NewBufferString(body2))
	rec2 := httptest.NewRecorder()
	h.HandleCancelAgenda(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing date, got %d", rec2.Code)
	}
}

func TestHandleCancelAgenda_Success(t *testing.T) {
	var batchIDs []string
	tracker := &mockEventLoggerAPI{}
	repo := &mockApptRepoAPI{
		findByAgendaAndDateFn: func(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
			return []domain.Appointment{
				{ID: "APT001", PatientID: "P1"},
				{ID: "APT002", PatientID: "P2"},
			}, nil
		},
		cancelBatchFn: func(ctx context.Context, ids []string, reason, ch, chID string) error {
			batchIDs = ids
			return nil
		},
	}

	h := &InternalHandler{
		appointmentRepo: repo,
		tracker:         tracker,
		cfg:             &config.Config{},
		startTime:       time.Now(),
	}

	body := `{"agenda_id":1,"date":"2026-03-20","reason":"doctor unavailable"}`
	req := httptest.NewRequest("POST", "/api/internal/cancel-agenda", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleCancelAgenda(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)
	if result["cancelled"].(float64) != 2 {
		t.Errorf("expected 2 cancelled, got %v", result["cancelled"])
	}
	if len(batchIDs) != 2 {
		t.Errorf("expected CancelBatch with 2 IDs, got %d", len(batchIDs))
	}

	// Verify admin event was tracked
	if len(tracker.events) != 1 {
		t.Fatalf("expected 1 tracked event, got %d", len(tracker.events))
	}
	if tracker.events[0].eventType != "admin_cancel_agenda" {
		t.Errorf("expected event type admin_cancel_agenda, got %s", tracker.events[0].eventType)
	}
	if tracker.events[0].data["appointments_cancelled"] != 2 {
		t.Errorf("expected 2 appointments_cancelled in event data, got %v", tracker.events[0].data["appointments_cancelled"])
	}
}

func TestHandleCancelAgenda_InvalidDate(t *testing.T) {
	h := &InternalHandler{
		appointmentRepo: &mockApptRepoAPI{},
		cfg:             &config.Config{},
	}

	body := `{"agenda_id":1,"date":"invalid-date","reason":"test"}`
	req := httptest.NewRequest("POST", "/api/internal/cancel-agenda", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleCancelAgenda(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid date format, got %d", rec.Code)
	}
}

// --- Tests: WaitingList ---

func TestHandleWaitingListCheck_Success(t *testing.T) {
	wlRepo := &mockWaitingListReader{
		getDistinctCupsFn: func(ctx context.Context) ([]string, error) {
			return []string{"87031", "87041"}, nil
		},
		getWaitingByCupsFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			if cupsCode == "87031" {
				return []domain.WaitingListEntry{{ID: "wl-1"}, {ID: "wl-2"}}, nil
			}
			return []domain.WaitingListEntry{{ID: "wl-3"}}, nil
		},
	}

	h := &InternalHandler{waitingListRepo: wlRepo, cfg: &config.Config{}}

	body := `{}`
	req := httptest.NewRequest("POST", "/api/internal/waiting-list/check", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleWaitingListCheck(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)
	if result["total"].(float64) != 2 {
		t.Errorf("expected 2 cups, got %v", result["total"])
	}
}

func TestHandleWaitingListCheck_SpecificCups(t *testing.T) {
	wlRepo := &mockWaitingListReader{
		getWaitingByCupsFn: func(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{{ID: "wl-1"}}, nil
		},
	}

	h := &InternalHandler{waitingListRepo: wlRepo, cfg: &config.Config{}}

	body := `{"cups_code":"87031"}`
	req := httptest.NewRequest("POST", "/api/internal/waiting-list/check", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleWaitingListCheck(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)
	if result["total"].(float64) != 1 {
		t.Errorf("expected 1 cups, got %v", result["total"])
	}
}

func TestHandleWaitingListCheck_RepoError(t *testing.T) {
	wlRepo := &mockWaitingListReader{
		getDistinctCupsFn: func(ctx context.Context) ([]string, error) {
			return nil, errors.New("db error")
		},
	}

	h := &InternalHandler{waitingListRepo: wlRepo, cfg: &config.Config{}}

	body := `{}`
	req := httptest.NewRequest("POST", "/api/internal/waiting-list/check", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleWaitingListCheck(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleWaitingListGet_Success(t *testing.T) {
	wlRepo := &mockWaitingListReader{
		listFn: func(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error) {
			return []domain.WaitingListEntry{
				{ID: "wl-1", CupsCode: "87031"},
				{ID: "wl-2", CupsCode: "87031"},
			}, 5, nil
		},
	}

	h := &InternalHandler{waitingListRepo: wlRepo, cfg: &config.Config{}}

	req := httptest.NewRequest("GET", "/api/internal/waiting-list?status=waiting&page=1", nil)
	rec := httptest.NewRecorder()
	h.HandleWaitingListGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)
	if result["total"].(float64) != 5 {
		t.Errorf("expected total 5, got %v", result["total"])
	}
	if result["page"].(float64) != 1 {
		t.Errorf("expected page 1, got %v", result["page"])
	}
}

func TestHandleWaitingListGet_DefaultPage(t *testing.T) {
	var capturedPage int
	wlRepo := &mockWaitingListReader{
		listFn: func(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error) {
			capturedPage = page
			return nil, 0, nil
		},
	}

	h := &InternalHandler{waitingListRepo: wlRepo, cfg: &config.Config{}}

	// No page param → defaults to 1
	req := httptest.NewRequest("GET", "/api/internal/waiting-list", nil)
	rec := httptest.NewRecorder()
	h.HandleWaitingListGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedPage != 1 {
		t.Errorf("expected default page 1, got %d", capturedPage)
	}
}

func TestHandleWaitingListGet_RepoError(t *testing.T) {
	wlRepo := &mockWaitingListReader{
		listFn: func(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error) {
			return nil, 0, errors.New("db error")
		},
	}

	h := &InternalHandler{waitingListRepo: wlRepo, cfg: &config.Config{}}

	req := httptest.NewRequest("GET", "/api/internal/waiting-list", nil)
	rec := httptest.NewRecorder()
	h.HandleWaitingListGet(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// --- Tests: Helpers ---

func TestGroupAppointmentsByPatientID(t *testing.T) {
	appointments := []domain.Appointment{
		{ID: "A1", PatientID: "P1"},
		{ID: "A2", PatientID: "P1"},
		{ID: "A3", PatientID: "P2"},
	}

	groups := groupAppointmentsByPatientID(appointments)

	if len(groups) != 2 {
		t.Errorf("expected 2 groups, got %d", len(groups))
	}
	if len(groups["P1"]) != 2 {
		t.Errorf("expected 2 appts for P1, got %d", len(groups["P1"]))
	}
	if len(groups["P2"]) != 1 {
		t.Errorf("expected 1 appt for P2, got %d", len(groups["P2"]))
	}
}

func TestGroupAppointmentsByPatientID_Empty(t *testing.T) {
	groups := groupAppointmentsByPatientID(nil)
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}
}

// --- flow-events endpoint ---

type fakeFlowReader struct {
	gotFlow, gotOutcome, gotReason string
	gotLimit                       int
	events                         []domain.FlowEvent
}

func (f *fakeFlowReader) FindByTrace(_ context.Context, _ string) ([]domain.FlowEvent, error) {
	return f.events, nil
}

func (f *fakeFlowReader) FindByFilter(_ context.Context, flow, outcome, reason string, _, _ time.Time, limit int) ([]domain.FlowEvent, error) {
	f.gotFlow, f.gotOutcome, f.gotReason, f.gotLimit = flow, outcome, reason, limit
	return f.events, nil
}

func (f *fakeFlowReader) Stats(_ context.Context, flow string, _, _ time.Time) (*domain.FlowStats, error) {
	f.gotFlow = flow
	return &domain.FlowStats{
		ByStep:    []domain.FlowStatCount{{Key: "ocr_ok", Count: 10}, {Key: "booking_success", Count: 4}},
		ByOutcome: []domain.FlowStatCount{{Key: "ok", Count: 4}, {Key: "blocked", Count: 3}},
		ByReason:  []domain.FlowStatCount{{Key: "gfr_low", Count: 2}},
	}, nil
}

func TestHandleFlowEvents_PassesFilters(t *testing.T) {
	fake := &fakeFlowReader{events: []domain.FlowEvent{{Flow: "agendar", Step: "gfr_blocked", Outcome: "blocked"}}}
	h := &InternalHandler{}
	h.SetFlowReader(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/flow-events?flow=agendar&outcome=blocked&limit=50", nil)
	w := httptest.NewRecorder()
	h.HandleFlowEvents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if fake.gotFlow != "agendar" || fake.gotOutcome != "blocked" || fake.gotLimit != 50 {
		t.Errorf("filtros mal pasados: flow=%q outcome=%q limit=%d", fake.gotFlow, fake.gotOutcome, fake.gotLimit)
	}
	var resp struct {
		Count  int                      `json:"count"`
		Events []map[string]interface{} `json:"events"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Events) != 1 {
		t.Fatalf("count=%d events=%d, want 1/1", resp.Count, len(resp.Events))
	}
	if resp.Events[0]["outcome"] != "blocked" {
		t.Errorf("event outcome = %v, want blocked", resp.Events[0]["outcome"])
	}
}

func TestHandleFlowEvents_NotConfigured(t *testing.T) {
	h := &InternalHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/internal/flow-events", nil)
	w := httptest.NewRecorder()
	h.HandleFlowEvents(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleFlowStats(t *testing.T) {
	fake := &fakeFlowReader{}
	h := &InternalHandler{}
	h.SetFlowReader(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/flow-stats?flow=agendar", nil)
	w := httptest.NewRecorder()
	h.HandleFlowStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if fake.gotFlow != "agendar" {
		t.Errorf("flow = %q, want agendar", fake.gotFlow)
	}
	var resp struct {
		Flow  string `json:"flow"`
		Stats struct {
			ByStep    []map[string]interface{} `json:"by_step"`
			ByOutcome []map[string]interface{} `json:"by_outcome"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Flow != "agendar" {
		t.Errorf("resp flow = %q, want agendar", resp.Flow)
	}
	if len(resp.Stats.ByStep) != 2 || len(resp.Stats.ByOutcome) != 2 {
		t.Errorf("steps=%d outcomes=%d, want 2/2", len(resp.Stats.ByStep), len(resp.Stats.ByOutcome))
	}
}

func TestHandleAnomalies_FiltersInvariante(t *testing.T) {
	fake := &fakeFlowReader{events: []domain.FlowEvent{{Flow: "invariante", Step: "anomaly", Outcome: "error", Reason: "orphan_slot"}}}
	h := &InternalHandler{}
	h.SetFlowReader(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/anomalies?reason=orphan_slot", nil)
	w := httptest.NewRecorder()
	h.HandleAnomalies(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if fake.gotFlow != "invariante" || fake.gotReason != "orphan_slot" {
		t.Errorf("filtros: flow=%q reason=%q, want invariante/orphan_slot", fake.gotFlow, fake.gotReason)
	}
	var resp struct {
		Count     int                      `json:"count"`
		Anomalies []map[string]interface{} `json:"anomalies"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Anomalies) != 1 {
		t.Errorf("count=%d anomalies=%d, want 1/1", resp.Count, len(resp.Anomalies))
	}
}

// TestIsoWeekMonday (M9): el lunes ISO se calcula bien incluso cuando el 4 de enero cae en domingo.
func TestIsoWeekMonday(t *testing.T) {
	cases := []struct {
		year, week int
		want       string // YYYY-MM-DD del lunes esperado
	}{
		{2026, 1, "2025-12-29"}, // 4-ene-2026 = domingo (el caso del bug); lunes de la W01 = 29-dic-2025
		{2024, 1, "2024-01-01"}, // 4-ene-2024 = jueves; lunes de la W01 = 1-ene-2024
		{2026, 10, "2026-03-02"},
	}
	for _, c := range cases {
		got := isoWeekMonday(c.year, c.week)
		if got.Weekday() != time.Monday {
			t.Errorf("isoWeekMonday(%d,%d) no es lunes: %s (%s)", c.year, c.week, got.Format("2006-01-02"), got.Weekday())
		}
		if got.Format("2006-01-02") != c.want {
			t.Errorf("isoWeekMonday(%d,%d) = %s, want %s", c.year, c.week, got.Format("2006-01-02"), c.want)
		}
	}
}
