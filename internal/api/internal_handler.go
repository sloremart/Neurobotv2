package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/logging"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// WorkerPoolStats provides queue stats without importing the worker package.
type WorkerPoolStats interface {
	QueueStats() (size, capacity int)
}

// ReminderRunner can send WhatsApp reminders on demand.
type ReminderRunner interface {
	SendWhatsAppReminders(ctx context.Context) error
}

// NotificationCounter provides pending notification count.
type NotificationCounter interface {
	PendingCount() int
}

// EventKPIReader provides KPI and analytics queries (enables testing without DB).
type EventKPIReader interface {
	GetDailyKPIs(ctx context.Context, date time.Time) (*localrepo.DailyKPIs, error)
	GetNotificationBreakdown(ctx context.Context, date time.Time) (*localrepo.NotificationBreakdown, error)
	GetAppointmentBreakdown(ctx context.Context, date time.Time) (*localrepo.AppointmentBreakdown, error)
	GetFunnel(ctx context.Context, from, to time.Time) (*localrepo.FunnelData, error)
	GetHealthMetrics(ctx context.Context) (*localrepo.HealthMetrics, error)
	FindByPhone(ctx context.Context, phone string, from, to time.Time, eventType string, maxRows int) ([]localrepo.ChatEvent, error)
}

// WaitingListReader provides waiting list queries (enables testing without DB).
type WaitingListReader interface {
	GetDistinctWaitingCups(ctx context.Context) ([]string, error)
	GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	List(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error)
}

// SessionDebugReader provides read-only session queries for debug endpoints.
type SessionDebugReader interface {
	FindByID(ctx context.Context, sessionID string) (*session.Session, error)
	FindRecentByPhone(ctx context.Context, phone string, limit int) ([]session.Session, error)
	GetAllContext(ctx context.Context, sessionID string) (map[string]string, error)
}

// FlowTraceReader reads the business-flow trace (flow_events) for the observability endpoints.
type FlowTraceReader interface {
	FindByTrace(ctx context.Context, traceID string) ([]domain.FlowEvent, error)
	FindByFilter(ctx context.Context, flow, outcome, reason string, from, to time.Time, limit int) ([]domain.FlowEvent, error)
	Stats(ctx context.Context, flow string, from, to time.Time) (*domain.FlowStats, error)
}

// flowEventJSON da forma a un FlowEvent para las respuestas de los endpoints de observabilidad.
func flowEventJSON(e domain.FlowEvent) map[string]interface{} {
	return map[string]interface{}{
		"ts":       e.CreatedAt,
		"trace_id": e.TraceID,
		"flow":     e.Flow,
		"step":     e.Step,
		"level":    e.Level,
		"outcome":  e.Outcome,
		"reason":   e.Reason,
		"phone":    e.Phone,
		"ref_type": e.RefType,
		"ref_id":   e.RefID,
		"attrs":    e.Attrs,
	}
}

// InternalEventLogger logs events for auditing (matches tracking.EventTracker).
type InternalEventLogger interface {
	LogEvent(ctx context.Context, sessionID, phone, eventType string, data map[string]interface{})
}

// SiesaRefReader expone catálogos de referencia de SIESA (médicos, asuntos) — solo lectura,
// cacheados — para poblar los selectores del módulo de catálogo del dashboard.
type SiesaRefReader interface {
	Medicos(ctx context.Context) ([]domain.MedicoRef, error)
	Asuntos(ctx context.Context) ([]domain.AsuntoRef, error)
}

// SiesaAnalyticsReader expone KPIs agregados de SIESA (ocupación, citas por estado, citas del bot
// para conciliación) — solo lectura, cacheados, NOLOCK — para las vistas del dashboard.
type SiesaAnalyticsReader interface {
	Occupancy(ctx context.Context, windowDays int) ([]domain.OccupancyRow, error)
	AppointmentsByState(ctx context.Context, from, to string) ([]domain.AppointmentStateRow, error)
	BotAppointmentsWithCups(ctx context.Context, botCedula string, days int) ([]domain.BotAppointmentCup, error)
}

// CupsMedicoReader devuelve los médicos habilitados para un CUPS (cups_medico, catálogo local).
// Se usa para cruzar las citas del bot y detectar médico mal asignado (conciliación).
type CupsMedicoReader interface {
	FindMedicosForCups(ctx context.Context, cupsCode string) ([]int, error)
}

// InternalHandler handles admin/internal API endpoints.
type InternalHandler struct {
	appointmentRepo repository.AppointmentRepository
	scheduleRepo    repository.ScheduleRepository
	waitingListRepo WaitingListReader
	eventRepo       EventKPIReader
	birdClient      *bird.Client
	notifyManager   *notifications.NotificationManager
	notifyCounter   NotificationCounter
	workerStats     WorkerPoolStats
	tracker         InternalEventLogger
	cfg             *config.Config
	startTime       time.Time
	reminderRunner  ReminderRunner       // optional: manual trigger for WA reminders
	sessionReader   SessionDebugReader   // optional: session debug queries
	flowReader      FlowTraceReader      // optional: flow-events trace queries
	siesaRef        SiesaRefReader       // optional: SIESA reference catalogs (médicos, asuntos)
	siesaAnalytics  SiesaAnalyticsReader // optional: SIESA aggregated KPIs (ocupación, citas, conciliación)
	cupsMedico      CupsMedicoReader     // optional: cups_medico reader for the conciliación cross
}

// NewInternalHandler creates a new internal handler.
func NewInternalHandler(
	appointmentRepo repository.AppointmentRepository,
	scheduleRepo repository.ScheduleRepository,
	waitingListRepo WaitingListReader,
	eventRepo EventKPIReader,
	birdClient *bird.Client,
	notifyManager *notifications.NotificationManager,
	notifyCounter NotificationCounter,
	workerStats WorkerPoolStats,
	tracker InternalEventLogger,
	cfg *config.Config,
	startTime time.Time,
) *InternalHandler {
	return &InternalHandler{
		appointmentRepo: appointmentRepo,
		scheduleRepo:    scheduleRepo,
		waitingListRepo: waitingListRepo,
		eventRepo:       eventRepo,
		birdClient:      birdClient,
		notifyManager:   notifyManager,
		notifyCounter:   notifyCounter,
		workerStats:     workerStats,
		tracker:         tracker,
		cfg:             cfg,
		startTime:       startTime,
	}
}

// SetReminderRunner injects the task runner for manual reminder triggers.
func (h *InternalHandler) SetReminderRunner(r ReminderRunner) {
	h.reminderRunner = r
}

// SetSessionReader injects the session debug reader for session/context queries.
func (h *InternalHandler) SetSessionReader(r SessionDebugReader) {
	h.sessionReader = r
}

// SetFlowReader injects the flow-events reader for the observability endpoints.
func (h *InternalHandler) SetFlowReader(r FlowTraceReader) {
	h.flowReader = r
}

// SetSiesaRefReader injects the SIESA reference catalog reader (médicos, asuntos).
func (h *InternalHandler) SetSiesaRefReader(r SiesaRefReader) {
	h.siesaRef = r
}

// SetSiesaAnalyticsReader injects the SIESA aggregated KPI reader and the cups_medico reader
// (para el cruce de conciliación). Ambos son opcionales (solo si SIESA está disponible).
func (h *InternalHandler) SetSiesaAnalyticsReader(a SiesaAnalyticsReader, cm CupsMedicoReader) {
	h.siesaAnalytics = a
	h.cupsMedico = cm
}

// queryIntDefault lee un parámetro entero de la query string, con valor por defecto si falta o es inválido.
func queryIntDefault(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// HandleSiesaOcupacion devuelve la ocupación de agenda (slots ocupados vs libres) por médico/día.
// Agregado y cacheado. GET /api/internal/siesa/ocupacion?dias=14
func (h *InternalHandler) HandleSiesaOcupacion(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	dias := queryIntDefault(r, "dias", 14)
	rows, err := h.siesaAnalytics.Occupancy(r.Context(), dias)
	if err != nil {
		slog.Error("siesa ocupacion failed", "error", err)
		http.Error(w, "failed to read ocupación", http.StatusInternalServerError)
		return
	}
	var ocup, libre int
	for _, o := range rows {
		ocup += o.Ocupados
		libre += o.Libres
	}
	pct := 0.0
	if ocup+libre > 0 {
		pct = float64(ocup) / float64(ocup+libre) * 100
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"rows": rows, "ocupados": ocup, "libres": libre, "ocupacion_pct": pct, "dias": dias,
	})
}

// HandleSiesaCitasEstado devuelve el conteo de citas por día y estado (verdad de SIESA).
// Agregado y cacheado. GET /api/internal/siesa/citas-estado?from=YYYY-MM-DD&to=YYYY-MM-DD
func (h *InternalHandler) HandleSiesaCitasEstado(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	rows, err := h.siesaAnalytics.AppointmentsByState(r.Context(), from, to)
	if err != nil {
		slog.Error("siesa citas-estado failed", "error", err)
		http.Error(w, "failed to read citas por estado", http.StatusInternalServerError)
		return
	}
	totals := map[string]int{}
	for _, s := range rows {
		totals[s.Estado] += s.Total
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"rows": rows, "totales": totals})
}

// HandleSiesaConciliacion cruza las citas creadas por el bot (SIESA) con cups_medico (local) y
// reporta las que tienen un médico que NO realiza el CUPS (mide si el bug de mal-asignación reaparece).
// GET /api/internal/siesa/conciliacion?dias=4
func (h *InternalHandler) HandleSiesaConciliacion(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil || h.cupsMedico == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	dias := queryIntDefault(r, "dias", 4)
	botCedula := ""
	if h.cfg != nil {
		botCedula = h.cfg.SIESAAssignUserCedula
	}
	citas, err := h.siesaAnalytics.BotAppointmentsWithCups(r.Context(), botCedula, dias)
	if err != nil {
		slog.Error("siesa conciliacion failed", "error", err)
		http.Error(w, "failed to read conciliación", http.StatusInternalServerError)
		return
	}
	type misassigned struct {
		CitaID  int    `json:"cita_id"`
		CodMedi int    `json:"cod_medi"`
		Cups    string `json:"cups"`
		Fecha   string `json:"fecha"`
	}
	bad := make([]misassigned, 0)
	checked := 0
	for _, c := range citas {
		allowed, err := h.cupsMedico.FindMedicosForCups(r.Context(), c.Cups)
		if err != nil || len(allowed) == 0 {
			continue // fail-open: CUPS sin médicos configurados no se evalúa
		}
		checked++
		ok := false
		for _, m := range allowed {
			if m == c.CodMedi {
				ok = true
				break
			}
		}
		if !ok {
			bad = append(bad, misassigned{c.CitaID, c.CodMedi, c.Cups, c.Fecha})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"mal_asignadas": bad, "total_mal": len(bad), "evaluadas": checked, "bot_citas": len(citas), "dias": dias,
	})
}

// HandleSiesaMedicos devuelve la lista de médicos de SIESA (sis_medi) para el selector del
// dashboard. Solo lectura, cacheada. GET /api/internal/siesa/medicos
func (h *InternalHandler) HandleSiesaMedicos(w http.ResponseWriter, r *http.Request) {
	if h.siesaRef == nil {
		http.Error(w, "siesa reference not available", http.StatusServiceUnavailable)
		return
	}
	meds, err := h.siesaRef.Medicos(r.Context())
	if err != nil {
		slog.Error("siesa medicos failed", "error", err)
		http.Error(w, "failed to read médicos", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"medicos": meds, "count": len(meds)})
}

// HandleSiesaAsuntos devuelve la lista de asuntos de SIESA (sis_asunto) para el selector del
// dashboard. Solo lectura, cacheada. GET /api/internal/siesa/asuntos
func (h *InternalHandler) HandleSiesaAsuntos(w http.ResponseWriter, r *http.Request) {
	if h.siesaRef == nil {
		http.Error(w, "siesa reference not available", http.StatusServiceUnavailable)
		return
	}
	asuntos, err := h.siesaRef.Asuntos(r.Context())
	if err != nil {
		slog.Error("siesa asuntos failed", "error", err)
		http.Error(w, "failed to read asuntos", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"asuntos": asuntos, "count": len(asuntos)})
}

// HandleFlowTrace returns the ordered timeline of a flow run by trace_id.
// GET /api/internal/flow-trace?trace_id=wl:123
// isoWeekMonday devuelve el lunes (00:00, hora local) de la semana ISO indicada.
// M9: el offset del lunes se calcula con (weekday+6)%7 (días desde el lunes). El cálculo anterior
// usaba int(weekday-time.Monday), que da -1 cuando el 4 de enero cae en domingo (como 2026) →
// el lunes computado quedaba una semana tarde y la agregación de 7 días sumaba la semana equivocada.
func isoWeekMonday(year, weekNum int) time.Time {
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.Local)
	_, jan4Week := jan4.ISOWeek()
	daysSinceMonday := (int(jan4.Weekday()) + 6) % 7 // Domingo(0)→6, Lunes(1)→0, … Sábado(6)→5
	return jan4.AddDate(0, 0, (weekNum-jan4Week)*7-daysSinceMonday)
}

func (h *InternalHandler) HandleFlowTrace(w http.ResponseWriter, r *http.Request) {
	if h.flowReader == nil {
		http.Error(w, "flow tracing not configured", http.StatusServiceUnavailable)
		return
	}
	traceID := r.URL.Query().Get("trace_id")
	if traceID == "" {
		http.Error(w, "trace_id is required", http.StatusBadRequest)
		return
	}
	events, err := h.flowReader.FindByTrace(r.Context(), traceID)
	if err != nil {
		//nolint:gosec // G706: trace_id es un parámetro de un endpoint admin (tras API key), no input público.
		slog.Error("flow-trace query failed", "trace_id", traceID, "error", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]interface{}, 0, len(events))
	for _, e := range events {
		out = append(out, flowEventJSON(e))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"trace_id": traceID,
		"count":    len(out),
		"events":   out,
	})
}

// HandleFlowEvents consulta eventos de flujo por tipo, acotado por ventana temporal.
// GET /api/internal/flow-events?flow=&outcome=&reason=&from=YYYY-MM-DD&to=YYYY-MM-DD&limit=
func (h *InternalHandler) HandleFlowEvents(w http.ResponseWriter, r *http.Request) {
	if h.flowReader == nil {
		http.Error(w, "flow tracing not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	to := time.Now()
	from := to.Add(-24 * time.Hour) // default: últimas 24h
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t.AddDate(0, 0, 1) // half-open: incluye todo el día 'to'
		}
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	events, err := h.flowReader.FindByFilter(r.Context(), q.Get("flow"), q.Get("outcome"), q.Get("reason"), from, to, limit)
	if err != nil {
		slog.Error("flow-events query failed", "error", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]interface{}, 0, len(events))
	for _, e := range events {
		out = append(out, flowEventJSON(e))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"count":  len(out),
		"events": out,
	})
}

// HandleAnomalies lista las anomalías de reconciliación de invariantes (flow=invariante).
// GET /api/internal/anomalies?from=YYYY-MM-DD&to=YYYY-MM-DD&reason=&limit=
func (h *InternalHandler) HandleAnomalies(w http.ResponseWriter, r *http.Request) {
	if h.flowReader == nil {
		http.Error(w, "flow tracing not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	to := time.Now()
	from := to.AddDate(0, 0, -7) // default: últimos 7 días
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t.AddDate(0, 0, 1)
		}
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	events, err := h.flowReader.FindByFilter(r.Context(), "invariante", "", q.Get("reason"), from, to, limit)
	if err != nil {
		slog.Error("anomalies query failed", "error", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]interface{}, 0, len(events))
	for _, e := range events {
		out = append(out, flowEventJSON(e))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"count":     len(out),
		"anomalies": out,
	})
}

// HandleFlowStats devuelve el funnel (conteo por step), la distribución de terminales (por outcome)
// y el conteo por reason de un flujo en una ventana.
// GET /api/internal/flow-stats?flow=&from=YYYY-MM-DD&to=YYYY-MM-DD
func (h *InternalHandler) HandleFlowStats(w http.ResponseWriter, r *http.Request) {
	if h.flowReader == nil {
		http.Error(w, "flow tracing not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	to := time.Now()
	from := to.AddDate(0, 0, -7) // default: últimos 7 días
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t.AddDate(0, 0, 1)
		}
	}
	flow := q.Get("flow")
	stats, err := h.flowReader.Stats(r.Context(), flow, from, to)
	if err != nil {
		slog.Error("flow-stats query failed", "error", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"flow":  flow,
		"from":  from.Format("2006-01-02"),
		"to":    to.Format("2006-01-02"),
		"stats": stats,
	})
}

// HandleSendReminders manually triggers the WhatsApp confirmation reminders task.
// Useful for testing or catch-up without waiting for the 07:00 scheduler.
func (h *InternalHandler) HandleSendReminders(w http.ResponseWriter, r *http.Request) {
	if h.reminderRunner == nil {
		http.Error(w, "reminder runner not configured", http.StatusServiceUnavailable)
		return
	}

	slog.Info("manual send-reminders triggered", "remote", r.RemoteAddr)

	go func() {
		defer recoverLog("manual-send-reminders")
		if err := h.reminderRunner.SendWhatsAppReminders(context.Background()); err != nil {
			slog.Error("manual send-reminders failed", "error", err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "reminders dispatched in background"})
}

// --- Test Voice Call ---

// HandleTestVoiceCall places a real IVR call with custom test data.
// Useful for verifying the Bird Voice API integration and DTMF webhook flow.
// POST /api/internal/test-voice-call
// Body: { "phone": "+573001234567", "patient_name": "...", "appointment_date": "...", "appointment_time": "...", "clinic_address": "..." }
func (h *InternalHandler) HandleTestVoiceCall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone           string `json:"phone"`
		PatientName     string `json:"patient_name"`
		AppointmentDate string `json:"appointment_date"`
		AppointmentTime string `json:"appointment_time"`
		ClinicAddress   string `json:"clinic_address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Phone == "" {
		http.Error(w, "invalid request: phone is required", http.StatusBadRequest)
		return
	}

	// Apply defaults for optional fields
	if req.PatientName == "" {
		req.PatientName = "Paciente de prueba"
	}
	if req.AppointmentDate == "" {
		req.AppointmentDate = "mañana"
	}
	if req.AppointmentTime == "" {
		req.AppointmentTime = "8:00 AM"
	}
	if req.ClinicAddress == "" {
		req.ClinicAddress = h.cfg.CenterName
	}

	slog.Info("test voice call requested", "phone", utils.MaskPhone(req.Phone), "patient", req.PatientName)

	callID, err := h.birdClient.PlaceCall(req.Phone, map[string]string{
		"patient_name":     req.PatientName,
		"appointment_date": req.AppointmentDate,
		"appointment_time": req.AppointmentTime,
		"clinic_name":      h.cfg.CenterName,
		"clinic_address":   req.ClinicAddress,
	})
	if err != nil {
		slog.Error("test voice call failed", "phone", utils.MaskPhone(req.Phone), "error", err)
		http.Error(w, "call failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Register callId so the DTMF webhook can be processed
	if callID != "" && h.notifyManager != nil {
		h.notifyManager.RegisterCallID(callID, req.Phone)
	}

	slog.Info("test voice call placed", "phone", utils.MaskPhone(req.Phone), "callId", callID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"call_id": callID,
		"phone":   req.Phone,
	})
}

// --- Send Agenda Confirmations ---

// HandleSendAgendaConfirmations sends WhatsApp confirmation templates to all non-cancelled
// patients for a specific agenda ID and date.
// POST /api/internal/send-agenda-confirmations
// Body: { "agenda_id": 12, "date": "2026-04-25" }
func (h *InternalHandler) HandleSendAgendaConfirmations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgendaID int    `json:"agenda_id"`
		Date     string `json:"date"` // YYYY-MM-DD
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.AgendaID == 0 || req.Date == "" {
		http.Error(w, "agenda_id and date are required", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		http.Error(w, "date must be YYYY-MM-DD format", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	appointments, err := h.appointmentRepo.FindByAgendaAndDate(ctx, req.AgendaID, req.Date)
	if err != nil {
		slog.Error("send agenda confirmations: find appointments", "agenda_id", req.AgendaID, "date", req.Date, "error", err)
		http.Error(w, "error finding appointments", http.StatusInternalServerError)
		return
	}

	total := len(appointments)
	slog.Info("send agenda confirmations: dispatching", "agenda_id", req.AgendaID, "date", req.Date, "appointments", total)

	// Respond immediately, send in background
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "ok",
		"agenda_id":  req.AgendaID,
		"date":       req.Date,
		"total_appt": total,
		"message":    "confirmations dispatched in background",
	})

	go func() {
		defer recoverLog("send-agenda-confirmations")

		// N-45: no hay ctx de aplicación cancelable disponible aquí; acotamos el
		// envío con un timeout para que respete una parada razonable.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		patients := groupAppointmentsByPatientID(appointments)
		sent, skipped := 0, 0

		for _, group := range patients {
			// Detener envíos si el contexto fue cancelado (apagado/timeout)
			if ctx.Err() != nil {
				break
			}
			firstAppt := group[0]
			phone := utils.ParseColombianPhone(firstAppt.PatientPhone)
			if phone == "" {
				skipped++
				slog.Warn("agenda confirmation: invalid phone", "patient_id", firstAppt.PatientID)
				continue
			}

			seen := make(map[string]bool)
			var procedureNames []string
			for _, appt := range group {
				name := services.GetFirstCupName(appt)
				if !seen[name] {
					seen[name] = true
					procedureNames = append(procedureNames, name)
				}
			}
			proceduresText := strings.Join(procedureNames, " y ")

			tmpl := bird.TemplateConfig{
				ProjectID: h.cfg.BirdTemplateConfirmProjectID,
				VersionID: h.cfg.BirdTemplateConfirmVersionID,
				Locale:    h.cfg.BirdTemplateConfirmLocale,
				Params: []bird.TemplateParam{
					{Type: "string", Key: "patient_name", Value: firstAppt.PatientName},
					{Type: "string", Key: "clinic_name", Value: h.cfg.CenterName},
					{Type: "string", Key: "appointment_date", Value: utils.FormatFriendlyDateStr(req.Date)},
					{Type: "string", Key: "appointment_time", Value: services.FormatTimeSlot(firstAppt.TimeSlot)},
					{Type: "string", Key: "procedures", Value: proceduresText},
				},
			}

			msgID, err := h.birdClient.SendTemplate(phone, tmpl)
			if err != nil {
				slog.Error("agenda confirmation: send failed", "phone", utils.MaskPhone(phone), "error", err)
				skipped++
				continue
			}

			convID := h.birdClient.GetCachedConversationID(phone)
			if convID == "" {
				convID, _ = h.birdClient.LookupConversationByPhone(phone)
			}

			h.notifyManager.RegisterPending(notifications.PendingNotification{
				Type:           "confirmation",
				Phone:          phone,
				AppointmentID:  firstAppt.ID,
				BirdMessageID:  msgID,
				ConversationID: convID,
			})

			if h.tracker != nil {
				h.tracker.LogEvent(ctx, "", phone, "notification_sent", map[string]interface{}{ // N-45: ctx cancelable
					"type":            "confirmation",
					"appointment_id":  firstAppt.ID,
					"bird_msg_id":     msgID,
					"conversation_id": convID,
					"agenda_id":       req.AgendaID,
				})
			}

			sent++
			// Rate limit (respeta cancelación)
			if err := sleepWithContext(ctx, 2*time.Second); err != nil {
				break
			}
		}

		slog.Info("agenda confirmations complete",
			"agenda_id", req.AgendaID, "date", req.Date,
			"sent", sent, "skipped", skipped)
	}()
}

// --- Cancel Agenda ---

// CancelAgendaRequest is the request body for cancelling an agenda.
type CancelAgendaRequest struct {
	AgendaID       int    `json:"agenda_id"`
	DoctorDocument string `json:"doctor_document"`
	Date           string `json:"date"` // YYYY-MM-DD
	Reason         string `json:"reason"`
	NotifyPatients bool   `json:"notify_patients"`
}

// maxReasonLength limits the length of reason/observation fields to prevent oversized DB writes.
const maxReasonLength = 500

// HandleCancelAgenda cancels all appointments for a given agenda/date and optionally notifies patients.
func (h *InternalHandler) HandleCancelAgenda(w http.ResponseWriter, r *http.Request) {
	var req CancelAgendaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.AgendaID == 0 || req.Date == "" {
		http.Error(w, "agenda_id and date are required", http.StatusBadRequest)
		return
	}

	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		http.Error(w, "date must be YYYY-MM-DD format", http.StatusBadRequest)
		return
	}

	if len(req.Reason) > maxReasonLength {
		req.Reason = req.Reason[:maxReasonLength]
	}

	ctx := r.Context()

	// 1. Obtener citas afectadas
	appointments, err := h.appointmentRepo.FindByAgendaAndDate(ctx, req.AgendaID, req.Date)
	if err != nil {
		slog.Error("cancel agenda: find appointments", "error", err)
		http.Error(w, "error finding appointments", http.StatusInternalServerError)
		return
	}

	// 2. Cancelar todas las citas en batch (1 transaccion)
	ids := make([]string, len(appointments))
	for i, a := range appointments {
		ids[i] = a.ID
	}
	cancelled := len(ids)
	if len(ids) > 0 {
		if err := h.appointmentRepo.CancelBatch(ctx, ids, req.Reason, "admin_cancel_agenda", ""); err != nil {
			// M1: si la cancelación en SIESA falló, NO continuar a notificar — antes se enviaba
			// "tu cita fue cancelada" a pacientes con citas aún activas y la respuesta devolvía
			// status:ok, enmascarando el fallo. Retornar 500 (como handleRescheduleSameAgenda).
			slog.Error("cancel batch in agenda", "agenda_id", req.AgendaID, "date", req.Date, "error", err)
			http.Error(w, "error cancelling appointments — none cancelled, no notifications sent", http.StatusInternalServerError)
			return
		}
	}

	// Log admin action
	if h.tracker != nil {
		h.tracker.LogEvent(ctx, "", "", "admin_cancel_agenda", map[string]interface{}{
			"agenda_id":              req.AgendaID,
			"date":                   req.Date,
			"reason":                 req.Reason,
			"appointments_cancelled": cancelled,
		})
	}

	// 3. Respond immediately — notifications are sent in background
	toNotify := 0
	if req.NotifyPatients && h.cfg.BirdTemplateCancellationProjectID != "" {
		patients := groupAppointmentsByPatientID(appointments)
		toNotify = len(patients)

		// Send notifications in background goroutine to avoid blocking HTTP response
		go func() {
			defer recoverLog("cancel-agenda-notify")

			// N-45: no hay ctx de aplicación cancelable disponible aquí; acotamos el
			// envío con un timeout para que respete una parada razonable.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			notified := 0
			for _, group := range patients {
				// Detener envíos si el contexto fue cancelado (apagado/timeout)
				if ctx.Err() != nil {
					break
				}
				firstAppt := group[0]
				phone := utils.ParseColombianPhone(firstAppt.PatientPhone)
				if phone == "" {
					continue
				}

				tmpl := bird.TemplateConfig{
					ProjectID: h.cfg.BirdTemplateCancellationProjectID,
					VersionID: h.cfg.BirdTemplateCancellationVersionID,
					Locale:    h.cfg.BirdTemplateCancellationLocale,
					Params: []bird.TemplateParam{
						{Type: "string", Key: "patient_name", Value: firstAppt.PatientName},
						{Type: "string", Key: "appointment_date", Value: utils.FormatFriendlyDate(firstAppt.Date)},
						{Type: "string", Key: "appointment_time", Value: services.FormatTimeSlot(firstAppt.TimeSlot)},
						{Type: "string", Key: "reason", Value: req.Reason},
					},
				}

				msgID, err := h.birdClient.SendTemplate(phone, tmpl)
				if err != nil {
					slog.Error("send cancellation notification", "phone", utils.MaskPhone(phone), "error", err)
					continue
				}

				// Look up conversationID for Bird Inbox visibility
				convID := h.birdClient.GetCachedConversationID(phone)
				if convID == "" {
					convID, _ = h.birdClient.LookupConversationByPhone(phone)
				}

				h.notifyManager.RegisterPending(notifications.PendingNotification{
					Type:           "cancellation",
					Phone:          phone,
					AppointmentID:  firstAppt.ID,
					BirdMessageID:  msgID,
					ConversationID: convID,
				})

				if h.tracker != nil {
					h.tracker.LogEvent(ctx, "", phone, "notification_sent", map[string]interface{}{ // N-45: ctx cancelable
						"type":            "cancellation",
						"appointment_id":  firstAppt.ID,
						"bird_msg_id":     msgID,
						"conversation_id": convID,
					})
				}

				notified++
				// Rate limit between messages (respeta cancelación)
				if err := sleepWithContext(ctx, 2*time.Second); err != nil {
					break
				}
			}
			slog.Info("agenda cancellation notifications sent", "notified", notified, "total", len(patients))
			observability.Emit(agendaTrace(req.AgendaID, req.Date), "admin_agenda", "patients_notified",
				observability.EmitOpts{Attrs: map[string]interface{}{"n": notified, "count": len(patients)}})
		}()
	}

	slog.Info("agenda cancelled", "agenda_id", req.AgendaID, "date", req.Date,
		"cancelled", cancelled, "to_notify", toNotify)

	observability.Emit(agendaTrace(req.AgendaID, req.Date), "admin_agenda", "agenda_cancelled",
		observability.EmitOpts{
			Reason:  req.Reason,
			RefType: "agenda",
			RefID:   strconv.Itoa(req.AgendaID),
			Attrs:   map[string]interface{}{"n": cancelled},
		})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"cancelled": cancelled,
		"to_notify": toNotify,
	})
}

// agendaTrace builds the observability trace_id for an admin agenda action (yyyymmdd, no dashes).
func agendaTrace(agendaID int, date string) string {
	return observability.TraceAgenda(strconv.Itoa(agendaID), strings.ReplaceAll(date, "-", ""))
}

// --- Reschedule Agenda ---

// RescheduleAgendaRequest is the request body for rescheduling an agenda.
type RescheduleAgendaRequest struct {
	AgendaID       int    `json:"agenda_id"`
	DoctorDocument string `json:"doctor_document"`
	OldDate        string `json:"old_date"`      // YYYY-MM-DD
	NewDate        string `json:"new_date"`      // YYYY-MM-DD
	NewAgendaID    *int   `json:"new_agenda_id"` // nullable: if set → different-agenda reschedule
	Reason         string `json:"reason"`
	NotifyPatients bool   `json:"notify_patients"`
}

// HandleRescheduleAgenda handles rescheduling of an agenda with two scenarios:
// Scenario A (new_agenda_id provided): Cancel appointments + delete old working day exception.
// Scenario B (same agenda): Move appointments to new date + update working day exception.
func (h *InternalHandler) HandleRescheduleAgenda(w http.ResponseWriter, r *http.Request) {
	var req RescheduleAgendaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.AgendaID == 0 || req.DoctorDocument == "" || req.OldDate == "" || req.NewDate == "" {
		http.Error(w, "agenda_id, doctor_document, old_date and new_date are required", http.StatusBadRequest)
		return
	}

	if _, err := time.Parse("2006-01-02", req.OldDate); err != nil {
		http.Error(w, "old_date must be YYYY-MM-DD format", http.StatusBadRequest)
		return
	}

	newDate, err := time.Parse("2006-01-02", req.NewDate)
	if err != nil {
		http.Error(w, "new_date must be YYYY-MM-DD format", http.StatusBadRequest)
		return
	}
	today := time.Now().Truncate(24 * time.Hour)
	if newDate.Before(today) {
		http.Error(w, "new_date must be today or later", http.StatusBadRequest)
		return
	}

	if len(req.Reason) > maxReasonLength {
		req.Reason = req.Reason[:maxReasonLength]
	}

	ctx := r.Context()

	if req.NewAgendaID != nil && *req.NewAgendaID != req.AgendaID {
		h.handleRescheduleWithNewAgenda(ctx, w, req)
	} else {
		h.handleRescheduleSameAgenda(ctx, w, req)
	}
}

// handleRescheduleWithNewAgenda — Scenario A: cancels appointments and deletes old working day exception.
func (h *InternalHandler) handleRescheduleWithNewAgenda(ctx context.Context, w http.ResponseWriter, req RescheduleAgendaRequest) {
	// 1. Validate new agenda exists
	if h.scheduleRepo != nil {
		newAgenda, err := h.scheduleRepo.FindByScheduleID(ctx, *req.NewAgendaID, "")
		if err != nil || newAgenda == nil {
			http.Error(w, "nueva agenda no encontrada", http.StatusNotFound)
			return
		}

		// Validate working day exception exists for new agenda+doctor+new date
		wdNew, err := h.scheduleRepo.FindWorkingDayException(ctx, *req.NewAgendaID, req.DoctorDocument, req.NewDate)
		if err != nil || wdNew == nil {
			http.Error(w, "no existe disponibilidad para ese doctor en la nueva agenda en la fecha nueva", http.StatusNotFound)
			return
		}
	}

	// 2. Get affected appointments (for patient info/notifications)
	appointments, err := h.appointmentRepo.FindByAgendaAndDate(ctx, req.AgendaID, req.OldDate)
	if err != nil {
		slog.Error("reschedule new agenda: find appointments", "error", err)
		http.Error(w, "error finding appointments", http.StatusInternalServerError)
		return
	}

	// Filter by doctor
	var doctorAppts []domain.Appointment
	for _, a := range appointments {
		if a.DoctorID == req.DoctorDocument {
			doctorAppts = append(doctorAppts, a)
		}
	}

	// 3. Cancel appointments in batch (1 transaction)
	reason := req.Reason
	if reason == "" {
		reason = "Reagendamiento de agenda"
	}
	cancelIDs := make([]string, len(doctorAppts))
	for i, a := range doctorAppts {
		cancelIDs[i] = a.ID
	}
	cancelled := len(cancelIDs)
	if len(cancelIDs) > 0 {
		if err := h.appointmentRepo.CancelBatch(ctx, cancelIDs, reason, "admin_reschedule_agenda", ""); err != nil {
			slog.Error("cancel batch in reschedule", "error", err)
			cancelled = 0
		}
	}

	// 4. Delete working day exception for old agenda+doctor+old date
	wdDeleted := false
	if h.scheduleRepo != nil {
		wdDeleted, _ = h.scheduleRepo.DeleteWorkingDayException(ctx, req.AgendaID, req.DoctorDocument, req.OldDate)
	}

	// 5. Notify patients — query from NEW agenda/date (like Laravel).
	//    If no appointments exist on the new agenda/date, no notifications are sent.
	var newAppts []domain.Appointment
	if req.NotifyPatients {
		allNew, _ := h.appointmentRepo.FindByAgendaAndDate(ctx, *req.NewAgendaID, req.NewDate)
		for _, a := range allNew {
			if a.DoctorID == req.DoctorDocument {
				newAppts = append(newAppts, a)
			}
		}
	}
	toNotify := h.sendRescheduleNotifications(newAppts, req)

	if h.tracker != nil {
		h.tracker.LogEvent(ctx, "", "", "admin_reschedule_agenda", map[string]interface{}{
			"agenda_id":              req.AgendaID,
			"old_date":               req.OldDate,
			"new_date":               req.NewDate,
			"scenario":               "new_agenda",
			"new_agenda_id":          *req.NewAgendaID,
			"appointments_cancelled": cancelled,
			"patients_to_notify":     toNotify,
		})
	}

	slog.Info("agenda rescheduled (new agenda)", "old_agenda", req.AgendaID, "new_agenda", *req.NewAgendaID,
		"old_date", req.OldDate, "new_date", req.NewDate, "cancelled", cancelled,
		"wd_deleted", wdDeleted, "to_notify", toNotify)

	observability.Emit(agendaTrace(req.AgendaID, req.NewDate), "admin_agenda", "agenda_rescheduled",
		observability.EmitOpts{
			Reason:  req.Reason,
			RefType: "agenda",
			RefID:   strconv.Itoa(req.AgendaID),
			Attrs:   map[string]interface{}{"n": cancelled, "count": toNotify},
		})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"cancelled": cancelled,
		"to_notify": toNotify,
	})
}

// handleRescheduleSameAgenda — Scenario B: moves appointments to new date and updates working day exception.
func (h *InternalHandler) handleRescheduleSameAgenda(ctx context.Context, w http.ResponseWriter, req RescheduleAgendaRequest) {
	// 1. Validate working day exception exists for current date
	if h.scheduleRepo != nil {
		wd, err := h.scheduleRepo.FindWorkingDayException(ctx, req.AgendaID, req.DoctorDocument, req.OldDate)
		if err != nil || wd == nil {
			http.Error(w, "no existe registro de excepcion de dias para esa agenda, doctor y fecha", http.StatusNotFound)
			return
		}
	}

	// 2. Update working day exception date
	wdUpdated := false
	if h.scheduleRepo != nil {
		wdUpdated, _ = h.scheduleRepo.UpdateWorkingDayExceptionDate(ctx, req.AgendaID, req.DoctorDocument, req.OldDate, req.NewDate)
	}

	// 3. Move appointments to new date (preserving time slots)
	updated, err := h.appointmentRepo.RescheduleDate(ctx, req.AgendaID, req.DoctorDocument, req.OldDate, req.NewDate)
	if err != nil {
		slog.Error("reschedule same agenda: move appointments", "error", err)
		http.Error(w, "error moving appointments", http.StatusInternalServerError)
		return
	}

	// 4. Get affected appointments for notifications (now on new date)
	appointments, _ := h.appointmentRepo.FindByAgendaAndDate(ctx, req.AgendaID, req.NewDate)
	var doctorAppts []domain.Appointment
	for _, a := range appointments {
		if a.DoctorID == req.DoctorDocument {
			doctorAppts = append(doctorAppts, a)
		}
	}

	// 5. Notify patients
	toNotify := h.sendRescheduleNotifications(doctorAppts, req)

	if h.tracker != nil {
		h.tracker.LogEvent(ctx, "", "", "admin_reschedule_agenda", map[string]interface{}{
			"agenda_id":            req.AgendaID,
			"old_date":             req.OldDate,
			"new_date":             req.NewDate,
			"scenario":             "same_agenda",
			"appointments_updated": updated,
			"patients_to_notify":   toNotify,
		})
	}

	slog.Info("agenda rescheduled (same agenda)", "agenda_id", req.AgendaID,
		"old_date", req.OldDate, "new_date", req.NewDate, "updated", updated,
		"wd_updated", wdUpdated, "to_notify", toNotify)

	observability.Emit(agendaTrace(req.AgendaID, req.NewDate), "admin_agenda", "agenda_rescheduled",
		observability.EmitOpts{
			Reason:  req.Reason,
			RefType: "agenda",
			RefID:   strconv.Itoa(req.AgendaID),
			Attrs:   map[string]interface{}{"n": updated, "count": toNotify},
		})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"updated":   updated,
		"to_notify": toNotify,
	})
}

// sendRescheduleNotifications sends reschedule WhatsApp templates to affected patients in the background.
func (h *InternalHandler) sendRescheduleNotifications(appointments []domain.Appointment, req RescheduleAgendaRequest) int {
	if !req.NotifyPatients || h.cfg.BirdTemplateRescheduleProjectID == "" || len(appointments) == 0 {
		return 0
	}

	patients := groupAppointmentsByPatientID(appointments)
	toNotify := len(patients)

	go func() {
		defer recoverLog("reschedule-agenda-notify")

		// N-45: no hay ctx de aplicación cancelable disponible aquí; acotamos el
		// envío con un timeout para que respete una parada razonable.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		notified := 0
		for _, group := range patients {
			// Detener envíos si el contexto fue cancelado (apagado/timeout)
			if ctx.Err() != nil {
				break
			}
			firstAppt := group[0]
			phone := utils.ParseColombianPhone(firstAppt.PatientPhone)
			if phone == "" {
				continue
			}

			tmpl := bird.TemplateConfig{
				ProjectID: h.cfg.BirdTemplateRescheduleProjectID,
				VersionID: h.cfg.BirdTemplateRescheduleVersionID,
				Locale:    h.cfg.BirdTemplateRescheduleLocale,
				Params: []bird.TemplateParam{
					{Type: "string", Key: "patient_name", Value: firstAppt.PatientName},
					{Type: "string", Key: "appointment_date_cancel", Value: utils.FormatFriendlyDateStr(req.OldDate)},
					{Type: "string", Key: "appointment_time_cancel", Value: services.FormatTimeSlot(firstAppt.TimeSlot)},
					{Type: "string", Key: "appointment_date_new", Value: utils.FormatFriendlyDateStr(req.NewDate)},
					{Type: "string", Key: "appointment_time_new", Value: services.FormatTimeSlot(firstAppt.TimeSlot)},
				},
			}

			msgID, err := h.birdClient.SendTemplate(phone, tmpl)
			if err != nil {
				slog.Error("send reschedule notification", "phone", utils.MaskPhone(phone), "error", err)
				continue
			}

			// Look up conversationID for Bird Inbox visibility
			convID := h.birdClient.GetCachedConversationID(phone)
			if convID == "" {
				convID, _ = h.birdClient.LookupConversationByPhone(phone)
			}

			h.notifyManager.RegisterPending(notifications.PendingNotification{
				Type:           "reschedule",
				Phone:          phone,
				AppointmentID:  firstAppt.ID,
				BirdMessageID:  msgID,
				ConversationID: convID,
			})

			if h.tracker != nil {
				h.tracker.LogEvent(ctx, "", phone, "notification_sent", map[string]interface{}{ // N-45: ctx cancelable
					"type":            "reschedule",
					"appointment_id":  firstAppt.ID,
					"bird_msg_id":     msgID,
					"conversation_id": convID,
				})
			}

			notified++
			// Rate limit (respeta cancelación)
			if err := sleepWithContext(ctx, 2*time.Second); err != nil {
				break
			}
		}
		slog.Info("reschedule notifications sent", "notified", notified, "total", len(patients))
		observability.Emit(agendaTrace(req.AgendaID, req.NewDate), "admin_agenda", "patients_notified",
			observability.EmitOpts{Attrs: map[string]interface{}{"n": notified, "count": len(patients)}})
	}()

	return toNotify
}

// --- Waiting List Admin ---

// HandleWaitingListCheck triggers a manual waiting list status check.
func (h *InternalHandler) HandleWaitingListCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CupsCode string `json:"cups_code"`
		DryRun   bool   `json:"dry_run"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	ctx := r.Context()

	var cupsCodes []string
	if req.CupsCode != "" {
		cupsCodes = []string{req.CupsCode}
	} else {
		var err error
		cupsCodes, err = h.waitingListRepo.GetDistinctWaitingCups(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	type cupsInfo struct {
		CupsCode string `json:"cups_code"`
		Waiting  int    `json:"waiting"`
		Notified int    `json:"notified,omitempty"`
	}
	var results []cupsInfo
	totalNotified := 0
	for _, code := range cupsCodes {
		entries, err := h.waitingListRepo.GetWaitingByCups(ctx, code, 100)
		if err != nil {
			continue
		}
		info := cupsInfo{CupsCode: code, Waiting: len(entries)}
		if !req.DryRun && len(entries) > 0 && h.notifyManager != nil {
			info.Notified = h.notifyManager.CheckWaitingListForCups(ctx, code)
			totalNotified += info.Notified
		}
		results = append(results, info)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "ok",
		"dry_run":        req.DryRun,
		"cups":           results,
		"total":          len(cupsCodes),
		"total_notified": totalNotified,
	})
}

// HandleWaitingListGet returns paginated waiting list entries.
func (h *InternalHandler) HandleWaitingListGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	phone := strings.TrimSpace(q.Get("phone"))
	if phone != "" && !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	filters := domain.WaitingListFilters{
		Status:   q.Get("status"),
		CupsCode: q.Get("cups_code"),
		Phone:    phone,
		DateFrom: q.Get("from"),
		DateTo:   q.Get("to"),
	}

	page, _ := strconv.Atoi(q.Get("page"))
	if page == 0 {
		page = 1
	}
	pageSize := 20
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 100 {
		pageSize = v
	}

	entries, total, err := h.waitingListRepo.List(r.Context(), filters, page, pageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": entries,
		"total":   total,
		"page":    page,
		"pages":   (total + pageSize - 1) / pageSize,
	})
}

// --- KPI Endpoints ---

// HandleDailyKPIs returns aggregated KPI metrics for a single day.
func (h *InternalHandler) HandleDailyKPIs(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}

	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		http.Error(w, "invalid date format, use YYYY-MM-DD", http.StatusBadRequest)
		return
	}

	kpis, err := h.eventRepo.GetDailyKPIs(r.Context(), date)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	breakdown, _ := h.eventRepo.GetNotificationBreakdown(r.Context(), date)
	apptBreakdown, _ := h.eventRepo.GetAppointmentBreakdown(r.Context(), date)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"kpis":                   kpis,
		"notification_breakdown": breakdown,
		"appointment_breakdown":  apptBreakdown,
	})
}

// HandleWeeklyKPIs returns aggregated KPI metrics for an ISO week.
func (h *InternalHandler) HandleWeeklyKPIs(w http.ResponseWriter, r *http.Request) {
	weekStr := r.URL.Query().Get("week")
	if weekStr == "" {
		year, week := time.Now().ISOWeek()
		weekStr = fmt.Sprintf("%d-W%02d", year, week)
	}

	// Parse ISO week: YYYY-Wnn
	parts := strings.SplitN(weekStr, "-W", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid week format, use YYYY-Wnn (e.g. 2026-W08)", http.StatusBadRequest)
		return
	}

	year, err := strconv.Atoi(parts[0])
	if err != nil {
		http.Error(w, "invalid year in week", http.StatusBadRequest)
		return
	}
	weekNum, err := strconv.Atoi(parts[1])
	if err != nil || weekNum < 1 || weekNum > 53 {
		http.Error(w, "invalid week number", http.StatusBadRequest)
		return
	}

	// Find Monday of the ISO week
	monday := isoWeekMonday(year, weekNum)

	ctx := r.Context()

	// Aggregate 7 days
	aggregate := &localrepo.DailyKPIs{Date: weekStr}
	daysWithData := 0
	for i := 0; i < 7; i++ {
		day := monday.AddDate(0, 0, i)
		daily, err := h.eventRepo.GetDailyKPIs(ctx, day)
		if err != nil {
			continue
		}
		aggregate.TotalSessions += daily.TotalSessions
		aggregate.CompletedSessions += daily.CompletedSessions
		aggregate.AbandonedSessions += daily.AbandonedSessions
		aggregate.EscalatedSessions += daily.EscalatedSessions
		aggregate.AppointmentsCreated += daily.AppointmentsCreated
		aggregate.AppointmentsConfirmed += daily.AppointmentsConfirmed
		aggregate.AppointmentsCancelled += daily.AppointmentsCancelled
		aggregate.PatientsRegistered += daily.PatientsRegistered
		aggregate.OCRAttempts += daily.OCRAttempts
		aggregate.OCRSuccesses += daily.OCRSuccesses
		aggregate.GFRCalculations += daily.GFRCalculations
		aggregate.GFRBlocked += daily.GFRBlocked
		aggregate.OutOfHoursAttempts += daily.OutOfHoursAttempts
		aggregate.MaxRetriesReached += daily.MaxRetriesReached
		aggregate.ProactivesSent += daily.ProactivesSent
		aggregate.ProactivesConfirmed += daily.ProactivesConfirmed
		aggregate.ProactivesCancelled += daily.ProactivesCancelled
		aggregate.ProactivesNoResponse += daily.ProactivesNoResponse
		aggregate.IVRCallsSent += daily.IVRCallsSent
		aggregate.WaitingListJoined += daily.WaitingListJoined
		aggregate.WaitingListScheduled += daily.WaitingListScheduled
		aggregate.AdminAgendasCancelled += daily.AdminAgendasCancelled
		aggregate.AdminAgendasRescheduled += daily.AdminAgendasRescheduled
		aggregate.RescheduleConfirmed += daily.RescheduleConfirmed
		aggregate.CancelAcknowledged += daily.CancelAcknowledged
		aggregate.RescheduleSelfService += daily.RescheduleSelfService
		aggregate.NotificationEscalated += daily.NotificationEscalated
		aggregate.IVRConfirmed += daily.IVRConfirmed
		aggregate.IVRCancelled += daily.IVRCancelled
		aggregate.WaitingListAccepted += daily.WaitingListAccepted
		aggregate.WaitingListDeclined += daily.WaitingListDeclined
		aggregate.TotalMessagesSent += daily.TotalMessagesSent
		aggregate.NoSlotsFound += daily.NoSlotsFound
		aggregate.AvgSessionDuration += daily.AvgSessionDuration
		if daily.TotalSessions > 0 {
			daysWithData++
		}
	}
	if daysWithData > 0 {
		aggregate.AvgSessionDuration /= float64(daysWithData)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(aggregate)
}

// HandleFunnel returns conversion funnel data for a date range.
func (h *InternalHandler) HandleFunnel(w http.ResponseWriter, r *http.Request) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	if fromStr == "" || toStr == "" {
		http.Error(w, "'from' and 'to' query params required (YYYY-MM-DD)", http.StatusBadRequest)
		return
	}

	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		http.Error(w, "invalid 'from' date", http.StatusBadRequest)
		return
	}

	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		http.Error(w, "invalid 'to' date", http.StatusBadRequest)
		return
	}

	funnel, err := h.eventRepo.GetFunnel(r.Context(), from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(funnel)
}

// HandleHealthKPIs returns system health and runtime metrics.
func (h *InternalHandler) HandleHealthKPIs(w http.ResponseWriter, r *http.Request) {
	metrics, err := h.eventRepo.GetHealthMetrics(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	metrics.UptimeSeconds = int64(time.Since(h.startTime).Seconds())

	if h.workerStats != nil {
		metrics.WorkerQueueSize, metrics.WorkerQueueCap = h.workerStats.QueueStats()
	}
	if h.notifyCounter != nil {
		metrics.PendingNotifications = h.notifyCounter.PendingCount()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// --- Test Alert ---

// HandleTestAlert triggers a test slog.Error to verify Telegram alerting works end-to-end.
func (h *InternalHandler) HandleTestAlert(w http.ResponseWriter, r *http.Request) {
	//nolint:gosec // G706: r.RemoteAddr lo fija el servidor (host:port), no es input de usuario; endpoint admin.
	slog.Error(
		"test alert: telegram integration check",
		"source", "manual_test",
		"triggered_by", r.RemoteAddr,
	)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "test error logged — check Telegram"})
}

// HandleLogs serves log entries with optional filtering.
//
// Query params:
//
//	lines    — max lines to return (default 200, max 10000)
//	level    — filter by level: debug, info, warn, error
//	from     — start datetime: YYYY-MM-DD or YYYY-MM-DDTHH:MM
//	to       — end datetime: YYYY-MM-DD or YYYY-MM-DDTHH:MM
//	search   — substring search in log message
//	phone    — filter by phone number (matches anywhere in log line)
//	download — "true" to return as downloadable .log file
func (h *InternalHandler) HandleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	lines := 200
	if v := q.Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > 10000 {
		lines = 10000
	}

	logPhone := strings.TrimSpace(q.Get("phone"))
	if logPhone != "" && !strings.HasPrefix(logPhone, "+") {
		logPhone = "+" + logPhone
	}
	filter := logging.LogFilter{
		Lines:  lines,
		Level:  q.Get("level"),
		Search: q.Get("search"),
		Phone:  logPhone,
	}

	if v := q.Get("from"); v != "" {
		if t, err := parseFlexTime(v); err == nil {
			filter.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := parseFlexTime(v); err == nil {
			filter.To = t
		}
	}

	logDir := h.cfg.LogDir
	if logDir == "" {
		http.Error(w, "log files not configured", http.StatusServiceUnavailable)
		return
	}

	results, err := logging.ReadLogs(logDir, "neuro-bot", filter)
	if err != nil {
		slog.Error("read logs failed", "error", err)
		http.Error(w, "failed to read logs", http.StatusInternalServerError)
		return
	}

	body := strings.Join(results, "\n")
	if body == "" {
		body = "No log entries found matching the filter."
	}

	if q.Get("download") == "true" {
		filename := fmt.Sprintf("neuro-bot-logs-%s.log", time.Now().Format("2006-01-02_15-04"))
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
		w.Header().Set("Content-Type", "application/octet-stream")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Write([]byte(body))
}

// HandleEvents returns chat events filtered by phone and optional date range.
//
// Query params:
//
//	phone    — phone number to filter (required, e.g. +573105800556)
//	from     — start datetime: YYYY-MM-DD or YYYY-MM-DDTHH:MM (optional)
//	to       — end datetime: YYYY-MM-DD or YYYY-MM-DDTHH:MM (optional)
//	type     — event type filter (optional, e.g. escalated_to_agent)
//	limit    — max events to return (default 200, max 500)
func (h *InternalHandler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	phone := strings.TrimSpace(q.Get("phone"))
	if phone == "" {
		http.Error(w, "'phone' query param is required", http.StatusBadRequest)
		return
	}
	// Normalize: accept with or without +
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}

	var from, to time.Time
	if v := q.Get("from"); v != "" {
		if t, err := parseFlexTime(v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := parseFlexTime(v); err == nil {
			to = t
		}
	}

	eventType := q.Get("type")

	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	events, err := h.eventRepo.FindByPhone(r.Context(), phone, from, to, eventType, limit)
	if err != nil {
		slog.Error("find events by phone", "phone", utils.MaskPhone(phone), "error", err)
		http.Error(w, "error querying events", http.StatusInternalServerError)
		return
	}

	type eventJSON struct {
		ID        int64                  `json:"id"`
		SessionID string                 `json:"session_id"`
		Phone     string                 `json:"phone"`
		Type      string                 `json:"type"`
		Data      map[string]interface{} `json:"data,omitempty"`
		StateFrom string                 `json:"state_from,omitempty"`
		StateTo   string                 `json:"state_to,omitempty"`
		CreatedAt string                 `json:"created_at"`
	}

	result := make([]eventJSON, len(events))
	for i, e := range events {
		result[i] = eventJSON{
			ID:        e.ID,
			SessionID: e.SessionID,
			Phone:     e.PhoneNumber,
			Type:      e.EventType,
			Data:      e.EventData,
			StateFrom: e.StateFrom,
			StateTo:   e.StateTo,
			CreatedAt: e.CreatedAt.Format(time.RFC3339),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"phone":  phone,
		"count":  len(result),
		"events": result,
	})
}

// HandleSessions returns recent sessions for a phone or a single session by ID.
// GET /api/internal/sessions?phone=+573001234567&limit=10
// GET /api/internal/sessions?id=<session-uuid>
func (h *InternalHandler) HandleSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionReader == nil {
		http.Error(w, `{"error":"session reader not configured"}`, http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	// Query by ID (single session + context)
	if id := r.URL.Query().Get("id"); id != "" {
		sess, err := h.sessionReader.FindByID(ctx, id)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		ctxMap, _ := h.sessionReader.GetAllContext(ctx, id)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"session": sessionToJSON(sess),
			"context": ctxMap,
		})
		return
	}

	// Query by phone (list)
	phone := r.URL.Query().Get("phone")
	if phone == "" {
		http.Error(w, `{"error":"'phone' or 'id' query parameter required"}`, http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 10
	}

	sessions, err := h.sessionReader.FindRecentByPhone(ctx, phone, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, len(sessions))
	for i := range sessions {
		result[i] = sessionToJSON(&sessions[i])
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"phone":    phone,
		"count":    len(result),
		"sessions": result,
	})
}

// HandleSessionContext returns all context key-values for a given session ID.
// GET /api/internal/sessions/context?id=<session-uuid>
func (h *InternalHandler) HandleSessionContext(w http.ResponseWriter, r *http.Request) {
	if h.sessionReader == nil {
		http.Error(w, `{"error":"session reader not configured"}`, http.StatusServiceUnavailable)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, `{"error":"'id' query parameter required"}`, http.StatusBadRequest)
		return
	}

	ctxMap, err := h.sessionReader.GetAllContext(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"session_id": id,
		"count":      len(ctxMap),
		"context":    ctxMap,
	})
}

func sessionToJSON(s *session.Session) map[string]interface{} {
	m := map[string]interface{}{
		"id":              s.ID,
		"phone":           s.PhoneNumber,
		"current_state":   s.CurrentState,
		"status":          s.Status,
		"menu_option":     s.MenuOption,
		"patient_id":      s.PatientID,
		"patient_doc":     s.PatientDoc,
		"patient_name":    s.PatientName,
		"patient_entity":  s.PatientEntity,
		"retry_count":     s.RetryCount,
		"conversation_id": s.ConversationID,
		"last_activity":   s.LastActivity.Format(time.RFC3339),
		"expires_at":      s.ExpiresAt.Format(time.RFC3339),
		"created_at":      s.CreatedAt.Format(time.RFC3339),
	}
	if s.EscalatedAt != nil {
		m["escalated_at"] = s.EscalatedAt.Format(time.RFC3339)
		m["escalated_team"] = s.EscalatedTeam
	}
	if s.ResumedAt != nil {
		m["resumed_at"] = s.ResumedAt.Format(time.RFC3339)
	}
	return m
}

// parseFlexTime parses datetime in flexible formats.
func parseFlexTime(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %s", s)
}

// --- Helpers ---

func groupAppointmentsByPatientID(appointments []domain.Appointment) map[string][]domain.Appointment {
	groups := make(map[string][]domain.Appointment)
	for _, appt := range appointments {
		groups[appt.PatientID] = append(groups[appt.PatientID], appt)
	}
	return groups
}
