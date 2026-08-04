package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
)

// --- local mock for AppointmentRepository ---

type mockApptRepoAPI struct {
	findByAgendaAndDateFn   func(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error)
	cancelFn                func(ctx context.Context, id, reason, channel, channelID string) error
	cancelBatchFn           func(ctx context.Context, ids []string, reason, channel, channelID string) error
	findAgendasByDoctorFn   func(ctx context.Context, doctor, from string) ([]domain.AgendaSummary, error)
	findAgendaApptsPagedFn  func(ctx context.Context, f domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error)
	findByIDFn              func(ctx context.Context, id string) (*domain.Appointment, error)
	cancelBatchBlockFn      func(ctx context.Context, ids []string, reason, channel, channelID string) error
	RescheduleDayOfAgendaFn func(ctx context.Context, in domain.RescheduleDayInput) (domain.RescheduleDayResult, error)
	doctorAgendasOnDateFn   func(ctx context.Context, doctor, date string) ([]domain.DoctorAgendaOnDate, error)
}

func (m *mockApptRepoAPI) FindDoctorAgendasOnDate(ctx context.Context, doctor, date string) ([]domain.DoctorAgendaOnDate, error) {
	if m.doctorAgendasOnDateFn != nil {
		return m.doctorAgendasOnDateFn(ctx, doctor, date)
	}
	return nil, nil
}

func (m *mockApptRepoAPI) FindAgendasByDoctor(ctx context.Context, doctor, from string) ([]domain.AgendaSummary, error) {
	if m.findAgendasByDoctorFn != nil {
		return m.findAgendasByDoctorFn(ctx, doctor, from)
	}
	return nil, nil
}

func (m *mockApptRepoAPI) FindAgendaAppointmentsPaged(ctx context.Context, f domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error) {
	if m.findAgendaApptsPagedFn != nil {
		return m.findAgendaApptsPagedFn(ctx, f)
	}
	return &domain.AgendaAppointmentsPage{}, nil
}

func (m *mockApptRepoAPI) FindByID(ctx context.Context, id string) (*domain.Appointment, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *mockApptRepoAPI) FindUpcomingByPatient(ctx context.Context, pid string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoAPI) FindPendingEmgAppointment(_ context.Context, _ string, _ []string) (*domain.Appointment, error) {
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

func (m *mockApptRepoAPI) CountMonthlyByGroup(ctx context.Context, cups []string, year, month int, _ string) (int, error) {
	return 0, nil
}

func (m *mockApptRepoAPI) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepoAPI) RescheduleDayOfAgenda(ctx context.Context, in domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
	if m.RescheduleDayOfAgendaFn != nil {
		return m.RescheduleDayOfAgendaFn(ctx, in)
	}
	return domain.RescheduleDayResult{}, nil
}

func (m *mockApptRepoAPI) AppointmentAsunto(_ context.Context, _ string) (int, error) {
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
	funnelFn       func(ctx context.Context, from, to time.Time) (*localrepo.FunnelData, error)
	countCreatedFn func(ctx context.Context, from, to time.Time) (int, error)
	findByPhoneFn  func(ctx context.Context, phone string, from, to time.Time, eventType string, maxRows int) ([]localrepo.ChatEvent, error)
}

func (m *mockEventKPIReader) GetFunnel(ctx context.Context, from, to time.Time) (*localrepo.FunnelData, error) {
	if m.funnelFn != nil {
		return m.funnelFn(ctx, from, to)
	}
	return &localrepo.FunnelData{}, nil
}

func (m *mockEventKPIReader) CountAppointmentsCreated(ctx context.Context, from, to time.Time) (int, error) {
	if m.countCreatedFn != nil {
		return m.countCreatedFn(ctx, from, to)
	}
	return 0, nil
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

// --- mock SiesaAnalyticsReader (solo BotCreatedByDay configurable) ---

type mockSiesaAnalyticsReader struct {
	botCreatedFn      func(ctx context.Context, botCedula, from, to string) ([]domain.BotCreatedRow, error)
	botAppointmentsFn func(ctx context.Context, botCedula string, days int) ([]domain.BotAppointmentCup, error)
	citasEstadoFn     func(ctx context.Context, from, to string) ([]domain.AppointmentStateRow, error)
	noShowFn          func(ctx context.Context, from, to string) ([]domain.NoShowRow, error)
}

func (m *mockSiesaAnalyticsReader) Occupancy(_ context.Context, _ int) ([]domain.OccupancyRow, error) {
	return nil, nil
}

func (m *mockSiesaAnalyticsReader) AppointmentsByState(ctx context.Context, from, to string) ([]domain.AppointmentStateRow, error) {
	if m.citasEstadoFn != nil {
		return m.citasEstadoFn(ctx, from, to)
	}
	return nil, nil
}

func (m *mockSiesaAnalyticsReader) NoShowByDay(ctx context.Context, from, to string) ([]domain.NoShowRow, error) {
	if m.noShowFn != nil {
		return m.noShowFn(ctx, from, to)
	}
	return nil, nil
}

func (m *mockSiesaAnalyticsReader) NoShowByLeadTime(_ context.Context, _, _ string) ([]domain.NoShowLeadRow, error) {
	return nil, nil
}

func (m *mockSiesaAnalyticsReader) BotCreatedByDay(ctx context.Context, botCedula, from, to string) ([]domain.BotCreatedRow, error) {
	if m.botCreatedFn != nil {
		return m.botCreatedFn(ctx, botCedula, from, to)
	}
	return nil, nil
}

func (m *mockSiesaAnalyticsReader) CreatedByDay(_ context.Context, _, _ string) ([]domain.BotCreatedRow, error) {
	return nil, nil
}

func (m *mockSiesaAnalyticsReader) BotAppointmentsWithCups(ctx context.Context, botCedula string, days int) ([]domain.BotAppointmentCup, error) {
	if m.botAppointmentsFn != nil {
		return m.botAppointmentsFn(ctx, botCedula, days)
	}
	return nil, nil
}

// mockCupsMedicoReader devuelve los médicos permitidos por CUPS desde un mapa.
type mockCupsMedicoReader struct {
	medicos map[string][]int
}

func (m *mockCupsMedicoReader) FindMedicosForCups(_ context.Context, cups string) ([]int, error) {
	return m.medicos[cups], nil
}

// #6: la discrepancia debe comparar FILAS de citas (no sesiones distintas) y acotarse a >=0.
func TestHandleSiesaConversion_DiscrepancyUsesRowsAndClamps(t *testing.T) {
	cases := []struct {
		name          string
		botCreatedRow int // filas de appointment_created (CountAppointmentsCreated)
		siesaTotal    int // filas en citas SIESA
		wantBotCreate float64
		wantDiscrep   float64
	}{
		// multi_cups_no_loss: una sesión con varios CUPS genera varias filas en ambos lados → 0 (no pérdida).
		// real_loss: el bot creyó crear 12 pero solo 10 aterrizaron → 2 perdidas.
		// more_in_siesa_clamped: más filas en SIESA que eventos → se acota a 0 (no es pérdida).
		{"multi_cups_no_loss", 12, 12, 12, 0},
		{"real_loss", 12, 10, 12, 2},
		{"more_in_siesa_clamped", 8, 12, 8, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eventRepo := &mockEventKPIReader{
				funnelFn: func(_ context.Context, _, _ time.Time) (*localrepo.FunnelData, error) {
					return &localrepo.FunnelData{TotalSessions: 10, AppointmentCreated: 8}, nil
				},
				countCreatedFn: func(_ context.Context, _, _ time.Time) (int, error) {
					return tc.botCreatedRow, nil
				},
			}
			analytics := &mockSiesaAnalyticsReader{
				botCreatedFn: func(_ context.Context, _, _, _ string) ([]domain.BotCreatedRow, error) {
					return []domain.BotCreatedRow{{Total: tc.siesaTotal}}, nil
				},
			}
			h := &InternalHandler{
				eventRepo:      eventRepo,
				siesaAnalytics: analytics,
				cfg:            &config.Config{SIESAAssignUserCedula: "123456"},
			}

			req := httptest.NewRequest("GET", "/api/internal/siesa/conversion?from=2026-06-01&to=2026-06-30", nil)
			rec := httptest.NewRecorder()
			h.HandleSiesaConversion(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			var result map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result["bot_created"].(float64) != tc.wantBotCreate {
				t.Errorf("bot_created: want %v (filas), got %v", tc.wantBotCreate, result["bot_created"])
			}
			if result["discrepancy"].(float64) != tc.wantDiscrep {
				t.Errorf("discrepancy: want %v, got %v", tc.wantDiscrep, result["discrepancy"])
			}
		})
	}
}

// #8: la conciliación debe contar CITAS DISTINTAS (no filas cita-CUPS) en los KPIs titulares y
// distinguir 'usuario del bot no configurado' de un 0 real.
func TestHandleSiesaConciliacion_CountsDistinctCitas(t *testing.T) {
	// cita 100 tiene 3 CUPS (X ok, Y/Z mal); cita 200 tiene 1 CUPS (X mal). 4 filas, 2 citas.
	rows := []domain.BotAppointmentCup{
		{CitaID: 100, CodMedi: 5, Cups: "X", Fecha: "2026-06-28"},
		{CitaID: 100, CodMedi: 5, Cups: "Y", Fecha: "2026-06-28"},
		{CitaID: 100, CodMedi: 5, Cups: "Z", Fecha: "2026-06-28"},
		{CitaID: 200, CodMedi: 9, Cups: "X", Fecha: "2026-06-28"},
	}
	analytics := &mockSiesaAnalyticsReader{
		botAppointmentsFn: func(_ context.Context, _ string, _ int) ([]domain.BotAppointmentCup, error) {
			return rows, nil
		},
	}
	cups := &mockCupsMedicoReader{medicos: map[string][]int{
		"X": {5}, // cita100 X ok (medi 5); cita200 X mal (medi 9)
		"Y": {7}, // cita100 Y mal (medi 5)
		"Z": {7}, // cita100 Z mal (medi 5)
	}}
	h := &InternalHandler{
		siesaAnalytics: analytics,
		cupsMedico:     cups,
		cfg:            &config.Config{SIESAAssignUserCedula: "123456"},
	}

	req := httptest.NewRequest("GET", "/api/internal/siesa/conciliacion?dias=4", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaConciliacion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var res map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	// Titulares por CITAS distintas.
	assertNum(t, res, "bot_citas", 2)
	assertNum(t, res, "evaluadas", 2)
	assertNum(t, res, "total_mal", 2)
	// Detalle por par cita-CUPS.
	assertNum(t, res, "bot_cita_cups", 4)
	assertNum(t, res, "evaluadas_cups", 4)
	assertNum(t, res, "total_mal_cups", 3)
	if res["bot_user_configured"] != true {
		t.Errorf("bot_user_configured: want true, got %v", res["bot_user_configured"])
	}
}

func TestHandleSiesaConciliacion_BotUserNotConfigured(t *testing.T) {
	h := &InternalHandler{
		siesaAnalytics: &mockSiesaAnalyticsReader{},
		cupsMedico:     &mockCupsMedicoReader{},
		cfg:            &config.Config{SIESAAssignUserCedula: "000000"}, // placeholder = no configurado
	}
	req := httptest.NewRequest("GET", "/api/internal/siesa/conciliacion", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaConciliacion(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var res map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["bot_user_configured"] != false {
		t.Errorf("bot_user_configured: want false for placeholder cedula, got %v", res["bot_user_configured"])
	}
}

func assertNum(t *testing.T, res map[string]interface{}, key string, want float64) {
	t.Helper()
	if got, ok := res[key].(float64); !ok || got != want {
		t.Errorf("%s: want %v, got %v", key, want, res[key])
	}
}

// --- Tests: HandleRescheduleAgenda (mover un día de agenda) ---

func postReschedule(t *testing.T, h *InternalHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/internal/agenda/reschedule", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleRescheduleAgenda(rec, req)
	return rec
}

func TestRescheduleDay_Validations(t *testing.T) {
	h := &InternalHandler{appointmentRepo: &mockApptRepoAPI{}, cfg: &config.Config{}}
	cases := []struct {
		name, body string
		want       int
	}{
		{"faltan campos", `{"agenda_id":0,"old_date":"","new_date":""}`, http.StatusBadRequest},
		{"old_date mal formato", `{"agenda_id":1,"old_date":"01-01-2027","new_date":"2027-01-02"}`, http.StatusBadRequest},
		{"new_date mal formato", `{"agenda_id":1,"old_date":"2027-01-01","new_date":"nope"}`, http.StatusBadRequest},
		{"misma fecha misma agenda", `{"agenda_id":1,"old_date":"2027-01-01","new_date":"2027-01-01"}`, http.StatusBadRequest},
		{"new_date en el pasado", `{"agenda_id":1,"old_date":"2020-01-01","new_date":"2020-01-02"}`, http.StatusBadRequest},
		{"old_date día pasado", `{"agenda_id":1,"old_date":"2020-01-01","new_date":"2099-01-02"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := postReschedule(t, h, c.body); rec.Code != c.want {
				t.Errorf("%s: want %d, got %d (%s)", c.name, c.want, rec.Code, rec.Body.String())
			}
		})
	}
}

// Error de NEGOCIO del repo (destino incompatible, sin citas, etc.) → 409 con el mensaje.
func TestRescheduleDay_BusinessError_Returns409(t *testing.T) {
	repo := &mockApptRepoAPI{
		RescheduleDayOfAgendaFn: func(_ context.Context, _ domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
			return domain.RescheduleDayResult{}, domain.RescheduleInvalidError{Msg: "agenda destino incompatible"}
		},
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	body := `{"agenda_id":705,"old_date":"2027-01-01","new_date":"2027-01-08","dest_agenda_id":711}`
	rec := postReschedule(t, h, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "incompatible") {
		t.Errorf("esperaba el mensaje de negocio, got %q", rec.Body.String())
	}
}

// Error de INFRAESTRUCTURA (tx/consulta) → 500 genérico (no 409, no filtra detalle).
func TestRescheduleDay_InfraError_Returns500(t *testing.T) {
	repo := &mockApptRepoAPI{
		RescheduleDayOfAgendaFn: func(_ context.Context, _ domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
			return domain.RescheduleDayResult{}, errors.New("siesa tx failed")
		},
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	body := `{"agenda_id":705,"old_date":"2027-01-01","new_date":"2027-01-08","dest_agenda_id":711}`
	rec := postReschedule(t, h, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "tx failed") {
		t.Errorf("no debe filtrar el error interno: %q", rec.Body.String())
	}
}

// Éxito (crear duplicando): pasa DestAgendaID=0 al repo, responde 200 con moved + dest_agenda_id + created.
func TestRescheduleDay_Success_Create(t *testing.T) {
	var gotIn domain.RescheduleDayInput
	repo := &mockApptRepoAPI{
		RescheduleDayOfAgendaFn: func(_ context.Context, in domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
			gotIn = in
			return domain.RescheduleDayResult{DestAgendaID: 999, Created: true, Moved: 12, MovedIDs: []string{"1", "2"}}, nil
		},
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	body := `{"agenda_id":705,"old_date":"2027-01-07","new_date":"2027-01-14","notify_patients":false}`
	rec := postReschedule(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotIn.AgendaID != 705 || gotIn.DestAgendaID != 0 || gotIn.OldDate != "2027-01-07" || gotIn.NewDate != "2027-01-14" {
		t.Errorf("input al repo incorrecto: %+v", gotIn)
	}
	var res map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res["moved"].(float64) != 12 || res["dest_agenda_id"].(float64) != 999 || res["created_agenda"] != true {
		t.Errorf("respuesta inesperada: %v", res)
	}
}

// Éxito (mover a existente): dest_agenda_id se propaga al repo.
func TestRescheduleDay_Success_ExistingDest(t *testing.T) {
	var gotIn domain.RescheduleDayInput
	repo := &mockApptRepoAPI{
		RescheduleDayOfAgendaFn: func(_ context.Context, in domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
			gotIn = in
			return domain.RescheduleDayResult{DestAgendaID: 711, Created: false, Moved: 12}, nil
		},
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	body := `{"agenda_id":705,"old_date":"2027-01-07","new_date":"2027-01-14","dest_agenda_id":711}`
	rec := postReschedule(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotIn.DestAgendaID != 711 {
		t.Errorf("dest_agenda_id no se propagó: %+v", gotIn)
	}
}

// movedAppointmentsForNotify filtra las citas de (dest,newDate) a solo las movidas (por id).
func TestMovedAppointmentsForNotify_FiltersByMovedIDs(t *testing.T) {
	repo := &mockApptRepoAPI{
		findByAgendaAndDateFn: func(_ context.Context, _ int, _ string) ([]domain.Appointment, error) {
			return []domain.Appointment{{ID: "A1"}, {ID: "A2"}, {ID: "A3"}}, nil
		},
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{BirdTemplateRescheduleProjectID: "proj"}}
	got := h.movedAppointmentsForNotify(context.Background(), 711, "2027-01-14", []string{"A1", "A3"})
	if len(got) != 2 || got[0].ID != "A1" || got[1].ID != "A3" {
		t.Errorf("esperaba [A1 A3], got %+v", got)
	}
}

// --- Tests: SIESA agendas / agenda-appointments (Fase 0 módulo Agenda) ---

func TestHandleSiesaAgendas_RequiresDoctor(t *testing.T) {
	h := &InternalHandler{appointmentRepo: &mockApptRepoAPI{}, cfg: &config.Config{}}
	req := httptest.NewRequest("GET", "/api/internal/siesa/agendas", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaAgendas(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("esperaba 400 sin doctor, got %d", rec.Code)
	}
}

func TestHandleSiesaAgendas_OK(t *testing.T) {
	repo := &mockApptRepoAPI{
		findAgendasByDoctorFn: func(_ context.Context, doctor, _ string) ([]domain.AgendaSummary, error) {
			if doctor != "8" {
				t.Errorf("doctor esperado 8, got %s", doctor)
			}
			return []domain.AgendaSummary{{AgendaID: 704, Consultorio: "FISIATRIA 03", Citas: 14}}, nil
		},
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	req := httptest.NewRequest("GET", "/api/internal/siesa/agendas?doctor=8", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaAgendas(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d", rec.Code)
	}
	var resp struct {
		Agendas []domain.AgendaSummary `json:"agendas"`
		Count   int                    `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Agendas) != 1 || resp.Agendas[0].AgendaID != 704 {
		t.Errorf("respuesta inesperada: %+v", resp)
	}
}

func TestHandleSiesaAgendaAppointments_RequiresAgendaOrDoctor(t *testing.T) {
	h := &InternalHandler{appointmentRepo: &mockApptRepoAPI{}, cfg: &config.Config{}}
	req := httptest.NewRequest("GET", "/api/internal/siesa/agenda-appointments", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaAgendaAppointments(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("esperaba 400 sin agenda ni doctor, got %d", rec.Code)
	}
}

func TestHandleSiesaAgendaAppointments_ParsesFilters(t *testing.T) {
	var got domain.AgendaAppointmentsFilter
	repo := &mockApptRepoAPI{
		findAgendaApptsPagedFn: func(_ context.Context, f domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error) {
			got = f
			return &domain.AgendaAppointmentsPage{
				Items: []domain.AgendaAppointmentRow{{ID: "1", Hora: "08:00", PatientName: "X", PatientDoc: "123"}},
				Total: 1, Page: f.Page, Pages: 1,
			}, nil
		},
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	req := httptest.NewRequest("GET", "/api/internal/siesa/agenda-appointments?agenda_id=704&page=2&page_size=10&name=arroyo&doc=1120588384", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaAgendaAppointments(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got.AgendaID == nil || *got.AgendaID != 704 {
		t.Errorf("agenda_id no parseado: %v", got.AgendaID)
	}
	if got.Page != 2 || got.PageSize != 10 {
		t.Errorf("page/size: %d/%d", got.Page, got.PageSize)
	}
	if got.Name != "arroyo" || got.Doc != "1120588384" {
		t.Errorf("name/doc: %q/%q", got.Name, got.Doc)
	}
}

func (m *mockApptRepoAPI) CancelBatchAndBlockSlots(ctx context.Context, ids []string, reason, channel, channelID string) error {
	if m.cancelBatchBlockFn != nil {
		return m.cancelBatchBlockFn(ctx, ids, reason, channel, channelID)
	}
	return nil
}

// --- Tests: cancelación individual (Fase 1 módulo Agenda) ---

func apptForCancel(id string, canceled bool) *domain.Appointment {
	return &domain.Appointment{ID: id, DoctorID: "8", AgendaID: 704, PatientID: "111", Date: time.Now(), TimeSlot: "202607090800", Canceled: canceled}
}

func TestHandleCancelAppointment_BlocksSlotsWhenNoRelease(t *testing.T) {
	plain, blocked := false, false
	repo := &mockApptRepoAPI{
		findByIDFn:         func(_ context.Context, id string) (*domain.Appointment, error) { return apptForCancel(id, false), nil },
		cancelBatchFn:      func(_ context.Context, _ []string, _, _, _ string) error { plain = true; return nil },
		cancelBatchBlockFn: func(_ context.Context, _ []string, _, _, _ string) error { blocked = true; return nil },
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	body := bytes.NewBufferString(`{"notify_patient":false,"release_slots":false}`)
	req := httptest.NewRequest("POST", "/api/internal/appointment/7285/cancel", body)
	req.SetPathValue("id", "7285")
	rec := httptest.NewRecorder()
	h.HandleCancelAppointment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !blocked || plain {
		t.Errorf("release_slots=false debe BLOQUEAR (blocked=%v plain=%v)", blocked, plain)
	}
}

func TestHandleCancelAppointment_ReleasesSlots(t *testing.T) {
	plain, blocked := false, false
	repo := &mockApptRepoAPI{
		findByIDFn:         func(_ context.Context, id string) (*domain.Appointment, error) { return apptForCancel(id, false), nil },
		cancelBatchFn:      func(_ context.Context, _ []string, _, _, _ string) error { plain = true; return nil },
		cancelBatchBlockFn: func(_ context.Context, _ []string, _, _, _ string) error { blocked = true; return nil },
	}
	// notifyManager nil → el WL se salta sin panic (guardado).
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	body := bytes.NewBufferString(`{"notify_patient":false,"release_slots":true}`)
	req := httptest.NewRequest("POST", "/api/internal/appointment/7285/cancel", body)
	req.SetPathValue("id", "7285")
	rec := httptest.NewRecorder()
	h.HandleCancelAppointment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d", rec.Code)
	}
	if !plain || blocked {
		t.Errorf("release_slots=true debe LIBERAR (plain=%v blocked=%v)", plain, blocked)
	}
}

func TestHandleCancelAppointment_Idempotent(t *testing.T) {
	called := false
	repo := &mockApptRepoAPI{
		findByIDFn:         func(_ context.Context, id string) (*domain.Appointment, error) { return apptForCancel(id, true), nil }, // ya cancelada
		cancelBatchFn:      func(_ context.Context, _ []string, _, _, _ string) error { called = true; return nil },
		cancelBatchBlockFn: func(_ context.Context, _ []string, _, _, _ string) error { called = true; return nil },
	}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	req := httptest.NewRequest("POST", "/api/internal/appointment/7285/cancel", bytes.NewBufferString(`{}`))
	req.SetPathValue("id", "7285")
	rec := httptest.NewRecorder()
	h.HandleCancelAppointment(rec, req)
	if rec.Code != http.StatusOK || called {
		t.Errorf("cita ya cancelada: esperaba 200 sin re-cancelar (code=%d called=%v)", rec.Code, called)
	}
}

func TestHandleCancelAppointment_NotFound(t *testing.T) {
	repo := &mockApptRepoAPI{findByIDFn: func(_ context.Context, _ string) (*domain.Appointment, error) { return nil, nil }}
	h := &InternalHandler{appointmentRepo: repo, cfg: &config.Config{}}
	req := httptest.NewRequest("POST", "/api/internal/appointment/999/cancel", bytes.NewBufferString(`{}`))
	req.SetPathValue("id", "999")
	rec := httptest.NewRecorder()
	h.HandleCancelAppointment(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("esperaba 404, got %d", rec.Code)
	}
}
