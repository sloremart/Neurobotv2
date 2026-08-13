package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// EventKPIReader provides the funnel (para la conversión real bot→SIESA) y la búsqueda de eventos.
type EventKPIReader interface {
	GetFunnel(ctx context.Context, from, to time.Time) (*localrepo.FunnelData, error)
	// CountAppointmentsCreated cuenta las FILAS del evento appointment_created (1 por cita creada),
	// no sesiones distintas — para comparar manzanas con manzanas contra las filas de citas en SIESA.
	CountAppointmentsCreated(ctx context.Context, from, to time.Time) (int, error)
	FindByPhone(ctx context.Context, phone string, from, to time.Time, eventType string, maxRows int) ([]localrepo.ChatEvent, error)
}

// WaitingListReader provides waiting list queries (enables testing without DB).
type WaitingListReader interface {
	GetDistinctWaitingCups(ctx context.Context) ([]string, error)
	GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	List(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error)
	// CountWLBookings: cuántos de esos IDs de cita nacieron de la lista de espera (KPI recuperación de cupos).
	CountWLBookings(ctx context.Context, apptIDs []string) (int, error)
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
	NoShowByDay(ctx context.Context, from, to string) ([]domain.NoShowRow, error)
	NoShowByLeadTime(ctx context.Context, from, to string) ([]domain.NoShowLeadRow, error)
	BotCreatedByDay(ctx context.Context, botCedula, from, to string) ([]domain.BotCreatedRow, error)
	CreatedByDay(ctx context.Context, from, to string) ([]domain.BotCreatedRow, error)
	BotAppointmentsWithCups(ctx context.Context, botCedula string, days int) ([]domain.BotAppointmentCup, error)
	// SlotRecovery: KPI recuperación de cupos cancelados (serie por día + IDs de citas que re-ocuparon).
	SlotRecovery(ctx context.Context, days int) (domain.SlotRecoveryData, error)
	// MRCGroupMonthlyConsumption: consumo del mes de un grupo MRC (misma métrica del gate del bot)
	// + cuánto de ese consumo creó el BOT (cod_user_asigna_cita = cédula del bot). Para validar
	// reportes de sobrecupo de la entidad y para el monitoreo del auditor (H145).
	MRCGroupMonthlyConsumption(ctx context.Context, cupsCodes []string, year, month int, botCedula string) (total, bot int, err error)
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
	patientRepo     PatientReader        // optional: resolver teléfono/nombre del paciente al notificar
}

// PatientReader resuelve datos del paciente (teléfono/nombre) por su id (autoid), para las
// notificaciones individuales del módulo Agenda. Lo inyecta SetPatientReader.
type PatientReader interface {
	FindByID(ctx context.Context, id string) (*domain.Patient, error)
}

// SetPatientReader inyecta el lector de pacientes (opcional).
func (h *InternalHandler) SetPatientReader(p PatientReader) { h.patientRepo = p }

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
// maxAnalyticsRangeDays acota la amplitud de from/to en los endpoints de analytics: sin este tope,
// un query string arbitrario (?from=1900-01-01) dispara una agregación sobre TODO el histórico de
// citas en la BD SIESA compartida con la UI clínica (auditoría queries P3). 90 días: la vista más
// amplia del dashboard usa ventanas de 30-60 días; nada legítimo pide más de un trimestre.
const maxAnalyticsRangeDays = 90

// analyticsQueryTimeout es el deadline de toda query de analytics contra SIESA. Los handlers de
// agenda ya usan 8s; analytics agrega sobre tablas más grandes, se le da un poco más.
const analyticsQueryTimeout = 10 * time.Second

// clampAnalyticsRange devuelve el `from` ajustado para que [from, to] no supere
// maxAnalyticsRangeDays. Fechas vacías o mal formadas pasan tal cual (los repos aplican sus
// defaults y los handlers que validan formato lo hacen antes de llamar aquí).
func clampAnalyticsRange(from, to string) string {
	f, errF := time.Parse("2006-01-02", from)
	t, errT := time.Parse("2006-01-02", to)
	if errF != nil || errT != nil {
		return from
	}
	if t.Sub(f) > maxAnalyticsRangeDays*24*time.Hour {
		return t.AddDate(0, 0, -maxAnalyticsRangeDays).Format("2006-01-02")
	}
	return from
}

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
	ctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()
	rows, err := h.siesaAnalytics.Occupancy(ctx, dias)
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
	to := r.URL.Query().Get("to")
	from := clampAnalyticsRange(r.URL.Query().Get("from"), to)
	ctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()
	rows, err := h.siesaAnalytics.AppointmentsByState(ctx, from, to)
	if err != nil {
		slog.Error("siesa citas-estado failed", "error", err)
		http.Error(w, "failed to read citas por estado", http.StatusInternalServerError)
		return
	}
	totals := map[string]int{}
	for _, s := range rows {
		totals[s.Situacion] += s.Total
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"rows": rows, "totales": totals})
}

// HandleSiesaNoShow devuelve la inasistencia (no-show) real por día (verdad de SIESA): de las citas
// pasadas no canceladas, cuántas se atendieron vs cuántas quedaron sin finalizar. Agregado y cacheado.
// AgentCallStats son las metricas de LLAMADAS de un agente en la ventana (F3): la pierna del agente
// es la unidad (no se doble-cuenta la llamada puenteada). "Ofrecida" = pierna hacia el agente
// (to=client:uuid); no-answer ahi = EL AGENTE no contesto.
type AgentCallStats struct {
	Made         int `json:"made"`          // el agente llamo (from=client)
	Offered      int `json:"offered"`       // llamadas ofrecidas al agente (to=client)
	Answered     int `json:"answered"`      // ofrecidas completadas
	NoAnswer     int `json:"no_answer"`     // ofrecidas que NO contesto
	Cancelled    int `json:"cancelled"`     // ofrecidas colgadas antes de contestar
	TotalSeconds int `json:"total_seconds"` // segundos en llamada (piernas completadas, made+answered)
}

// aggregateAgentCalls agrega las piernas de llamada por agente dentro de [from, to] (pura, testeable).
// Excluye ongoing/ringing de los tiempos. Devuelve tambien si alguna fila quedo dentro de la ventana
// (para saber cuando dejar de paginar: el listado viene de mas reciente a mas antiguo).
func aggregateAgentCalls(calls []bird.CallRecord, from, to time.Time) (map[string]*AgentCallStats, bool) {
	out := map[string]*AgentCallStats{}
	anyInWindow := false
	get := func(id string) *AgentCallStats {
		if out[id] == nil {
			out[id] = &AgentCallStats{}
		}
		return out[id]
	}
	for _, c := range calls {
		ts, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil || ts.Before(from) || ts.After(to) {
			continue
		}
		anyInWindow = true
		terminal := c.Status == "completed" || c.Status == "no-answer" || c.Status == "cancelled"
		if !terminal {
			continue
		}
		if strings.HasPrefix(c.From, "client:") {
			a := get(strings.TrimPrefix(c.From, "client:"))
			a.Made++
			if c.Status == "completed" {
				a.TotalSeconds += c.Duration
			}
			continue
		}
		if strings.HasPrefix(c.To, "client:") {
			a := get(strings.TrimPrefix(c.To, "client:"))
			a.Offered++
			switch c.Status {
			case "completed":
				a.Answered++
				a.TotalSeconds += c.Duration
			case "no-answer":
				a.NoAnswer++
			case "cancelled":
				a.Cancelled++
			}
		}
	}
	return out, anyInWindow
}

// HandleBirdAgentCalls agrega las metricas de llamadas por agente en la ventana [from, to]
// (fechas YYYY-MM-DD en hora local). Pagina el listado de Bird (mas reciente primero) hasta salir
// de la ventana o tocar el tope de seguridad. Best-effort: 503/502 si Bird no esta disponible.
// GET /api/internal/agents/bird-calls?from=YYYY-MM-DD&to=YYYY-MM-DD
func (h *InternalHandler) HandleBirdAgentCalls(w http.ResponseWriter, r *http.Request) {
	if h.birdClient == nil {
		http.Error(w, "bird client not available", http.StatusServiceUnavailable)
		return
	}
	fromS, toS := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	from, err1 := time.ParseInLocation("2006-01-02", fromS, time.Local)
	toD, err2 := time.ParseInLocation("2006-01-02", toS, time.Local)
	if err1 != nil || err2 != nil {
		http.Error(w, "from/to requeridos (YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	to := toD.Add(24*time.Hour - time.Second)

	// Cache en memoria (TTL corto): el informe pide la misma ventana varias veces (general +
	// detalle actual y anterior) y el paginado contra Bird es lento (~100 piernas/pagina).
	cacheKey := fromS + "|" + toS
	if v, ok := agentCallsCache.get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(v)
		return
	}

	const maxPages = 80 // tope de seguridad (~8.000 piernas)
	totals := map[string]*AgentCallStats{}
	truncated := false
	pageToken := ""
	for page := 0; page < maxPages; page++ {
		pg, err := h.birdClient.ListCalls(pageToken)
		if err != nil {
			// Un reintento ante fallos transitorios de Bird (504 GATEWAY_TIMEOUT observado).
			time.Sleep(700 * time.Millisecond)
			pg, err = h.birdClient.ListCalls(pageToken)
		}
		if err != nil {
			slog.Error("bird calls list failed", "error", err)
			http.Error(w, "failed to list bird calls", http.StatusBadGateway)
			return
		}
		agg, anyIn := aggregateAgentCalls(pg.Results, from, to)
		for id, a := range agg {
			t := totals[id]
			if t == nil {
				totals[id] = a
				continue
			}
			t.Made += a.Made
			t.Offered += a.Offered
			t.Answered += a.Answered
			t.NoAnswer += a.NoAnswer
			t.Cancelled += a.Cancelled
			t.TotalSeconds += a.TotalSeconds
		}
		// Parar cuando la pagina ya quedo TODA por debajo de la ventana (listado descendente).
		olderThanWindow := len(pg.Results) > 0 && !anyIn
		if olderThanWindow {
			if ts, err := time.Parse(time.RFC3339, pg.Results[len(pg.Results)-1].CreatedAt); err == nil && ts.Before(from) {
				break
			}
		}
		if pg.NextPageToken == "" {
			break
		}
		pageToken = pg.NextPageToken
		if page == maxPages-1 {
			truncated = true
		}
	}
	payload, _ := json.Marshal(map[string]interface{}{"agents": totals, "truncated": truncated})
	agentCallsCache.set(cacheKey, payload)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

// agentCallsCache: cache simple con TTL para las metricas de llamadas (ver arriba).
var agentCallsCache = callsCache{entries: map[string]callsCacheEntry{}}

type callsCacheEntry struct {
	data   []byte
	expiry time.Time
}

type callsCache struct {
	mu      sync.Mutex
	entries map[string]callsCacheEntry
}

func (c *callsCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiry) {
		return nil, false
	}
	return e.data, true
}

func (c *callsCache) set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = callsCacheEntry{data: data, expiry: time.Now().Add(10 * time.Minute)}
}

// HandleBirdAgents expone las métricas DIRECTAS de Bird por agente (estado/actividad, carga actual de
// conversaciones asignadas, equipos) para el informe unificado de agentes del dashboard (F3 §9).
// Solo lectura contra la API de Bird; sin Bird configurado responde 503.
// GET /api/internal/agents/bird
func (h *InternalHandler) HandleBirdAgents(w http.ResponseWriter, _ *http.Request) {
	if h.birdClient == nil {
		http.Error(w, "bird client not available", http.StatusServiceUnavailable)
		return
	}
	agents, err := h.birdClient.ListAllAgents()
	if err != nil {
		slog.Error("bird agents list failed", "error", err)
		http.Error(w, "failed to list bird agents", http.StatusBadGateway)
		return
	}
	type agentOut struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Status    string   `json:"status"`
		Activity  string   `json:"activity"`
		OpenItems int      `json:"open_items"`
		Teams     []string `json:"teams"`
	}
	out := make([]agentOut, 0, len(agents))
	for _, a := range agents {
		teams := make([]string, 0, len(a.Teams))
		for _, t := range a.Teams {
			teams = append(teams, t.Name)
		}
		out = append(out, agentOut{
			ID: a.ID, Name: a.DisplayName,
			Status: a.Availability.Status, Activity: a.Availability.Activity,
			OpenItems: a.RootItemAssignedCount, Teams: teams,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"agents": out})
}

// GET /api/internal/siesa/no-show?from=YYYY-MM-DD&to=YYYY-MM-DD
func (h *InternalHandler) HandleSiesaNoShow(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	to := r.URL.Query().Get("to")
	from := clampAnalyticsRange(r.URL.Query().Get("from"), to)
	ctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()
	rows, err := h.siesaAnalytics.NoShowByDay(ctx, from, to)
	if err != nil {
		slog.Error("siesa no-show failed", "error", err)
		http.Error(w, "failed to read no-show", http.StatusInternalServerError)
		return
	}
	var esperadas, atendidas, noShow, sinCerrar int
	for _, n := range rows {
		esperadas += n.Esperadas
		atendidas += n.Atendidas
		noShow += n.NoShow
		sinCerrar += n.SinCerrar
	}
	// % no-show calculado SOLO sobre el no-show puro (no incluye 'sin cerrar', que pudo asistir).
	pct := 0.0
	if esperadas > 0 {
		pct = float64(noShow) / float64(esperadas) * 100
	}
	// KPI de vigilancia del hallazgo 8.1 #2 (no-show por antelacion). Best-effort: si falla, el
	// resto del payload sale igual.
	leadRows, leadErr := h.siesaAnalytics.NoShowByLeadTime(ctx, from, to)
	if leadErr != nil {
		slog.Warn("siesa no-show lead failed", "error", leadErr)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"rows": rows, "esperadas": esperadas, "atendidas": atendidas,
		"sin_cerrar": sinCerrar, "no_show": noShow, "no_show_pct": pct,
		"by_lead_time": leadRows,
	})
}

// HandleSiesaConciliacion cruza las citas creadas por el bot (SIESA) con cups_medico (local) y
// reporta las que tienen un médico que NO realiza el CUPS (mide si el bug de mal-asignación reaparece).
// HandleSiesaSlotRecovery — GET /api/internal/siesa/slot-recovery?dias=30
// KPI "recuperación de cupos cancelados": slots con cita cancelada (SIESA, cualquier canal de
// cancelación incluida la UI), cuántos se re-ocuparon con una cita nueva, y cuántos de esos
// re-llenados nacieron de la lista de espera (cruce con la BD local). El dashboard lo proxea.
func (h *InternalHandler) HandleSiesaSlotRecovery(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	dias := queryIntDefault(r, "dias", 30)
	if dias < 1 {
		dias = 1
	} else if dias > 180 {
		dias = 180
	}
	ctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()

	data, err := h.siesaAnalytics.SlotRecovery(ctx, dias)
	if err != nil {
		slog.Error("siesa slot recovery failed", "error", err)
		http.Error(w, "failed to read slot recovery", http.StatusInternalServerError)
		return
	}

	canceladas, rellenadas := 0, 0
	for _, d := range data.PorDia {
		canceladas += d.Canceladas
		rellenadas += d.Rellenadas
	}
	rellenadasWL := 0
	if len(data.RefillCitaIDs) > 0 && h.waitingListRepo != nil {
		n, werr := h.waitingListRepo.CountWLBookings(ctx, data.RefillCitaIDs)
		if werr != nil {
			// Cruce best-effort: el agregado SIESA se entrega igual, con el numerador WL en 0 y log.
			slog.Error("slot recovery wl cross failed", "error", werr)
		} else {
			rellenadasWL = n
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"dias":          dias,
		"canceladas":    canceladas,
		"rellenadas":    rellenadas,
		"rellenadas_wl": rellenadasWL,
		"por_dia":       data.PorDia,
	})
}

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
	ctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()
	citas, err := h.siesaAnalytics.BotAppointmentsWithCups(ctx, botCedula, dias)
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
	// La fuente trae UNA fila por (cita, CUPS): una cita con varios CUPS aparece varias veces. Los KPIs
	// titulares se cuentan por CITAS DISTINTAS (no filas) y los pares cita-CUPS se exponen aparte (_cups)
	// para el detalle. mal_asignadas conserva el detalle por par cita-CUPS.
	bad := make([]misassigned, 0)
	checkedRows := 0
	distinctCitas := make(map[int]struct{})
	evaluatedCitas := make(map[int]struct{})
	badCitas := make(map[int]struct{})
	// Dedupe del par (cita, CUPS): la fuente UNION ALL podría repetir el mismo par; sin dedupe se
	// inflarían total_mal_cups/bot_cita_cups y la key de la tabla en la UI colisionaría.
	seenPairs := make(map[string]struct{})
	// M5 (auditoría queries): una consulta a cups_medico POR CUPS DISTINTO, no por par cita-CUPS
	// (eran miles de round-trips a MySQL por carga). Un error se memoiza como nil = fail-open.
	medicosByCups := make(map[string][]int)
	for _, c := range citas {
		distinctCitas[c.CitaID] = struct{}{}
		pairKey := fmt.Sprintf("%d|%s", c.CitaID, c.Cups)
		if _, dup := seenPairs[pairKey]; dup {
			continue
		}
		seenPairs[pairKey] = struct{}{}
		allowed, seen := medicosByCups[c.Cups]
		if !seen {
			var lerr error
			allowed, lerr = h.cupsMedico.FindMedicosForCups(r.Context(), c.Cups)
			if lerr != nil {
				allowed = nil
			}
			medicosByCups[c.Cups] = allowed
		}
		if len(allowed) == 0 {
			continue // fail-open: CUPS sin médicos configurados no se evalúa
		}
		checkedRows++
		evaluatedCitas[c.CitaID] = struct{}{}
		ok := false
		for _, m := range allowed {
			if m == c.CodMedi {
				ok = true
				break
			}
		}
		if !ok {
			bad = append(bad, misassigned{c.CitaID, c.CodMedi, c.Cups, c.Fecha})
			badCitas[c.CitaID] = struct{}{}
		}
	}
	// La cédula del bot sin configurar (vacía o el placeholder '000000') deja la conciliación en 0 sin
	// que sea un 0 real; la UI lo distingue con esta bandera.
	botConfigured := botCedula != "" && botCedula != "000000"
	w.Header().Set("Content-Type", "application/json")
	// Titulares por CITAS distintas; los pares cita-CUPS (filas) se exponen con sufijo _cups.
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"mal_asignadas":       bad,
		"total_mal":           len(badCitas),
		"total_mal_cups":      len(bad),
		"evaluadas":           len(evaluatedCitas),
		"evaluadas_cups":      checkedRows,
		"bot_citas":           len(distinctCitas),
		"bot_cita_cups":       len(seenPairs),
		"bot_user_configured": botConfigured,
		"dias":                dias,
	})
}

// HandleSiesaBotShare compara las citas creadas por el bot vs las del resto de usuarios en SIESA
// (por día y total) para medir la participación del bot en el agendamiento.
// GET /api/internal/siesa/bot-share?from&to
func (h *InternalHandler) HandleSiesaBotShare(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		now := time.Now()
		fromStr = now.AddDate(0, 0, -30).Format("2006-01-02")
		toStr = now.Format("2006-01-02")
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		http.Error(w, "invalid 'to' date", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse("2006-01-02", fromStr); err != nil {
		http.Error(w, "invalid 'from' date", http.StatusBadRequest)
		return
	}
	fromStr = clampAnalyticsRange(fromStr, toStr)
	toExclusive := to.AddDate(0, 0, 1).Format("2006-01-02")

	botCedula := ""
	if h.cfg != nil {
		botCedula = h.cfg.SIESAAssignUserCedula
	}
	ctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()
	botRows, err := h.siesaAnalytics.BotCreatedByDay(ctx, botCedula, fromStr, toExclusive)
	if err != nil {
		slog.Error("siesa bot-share bot failed", "error", err)
		http.Error(w, "failed to read bot citas", http.StatusInternalServerError)
		return
	}
	totalRows, err := h.siesaAnalytics.CreatedByDay(ctx, fromStr, toExclusive)
	if err != nil {
		slog.Error("siesa bot-share total failed", "error", err)
		http.Error(w, "failed to read total citas", http.StatusInternalServerError)
		return
	}

	botByDay := make(map[string]int, len(botRows))
	for _, b := range botRows {
		botByDay[b.Fecha] = b.Total
	}
	type shareRow struct {
		Fecha string `json:"fecha"`
		Total int    `json:"total"`
		Bot   int    `json:"bot"`
		Otros int    `json:"otros"`
	}
	rows := make([]shareRow, 0, len(totalRows))
	totalSum, botSum := 0, 0
	for _, t := range totalRows {
		bot := botByDay[t.Fecha]
		if bot > t.Total {
			bot = t.Total // salvaguarda
		}
		rows = append(rows, shareRow{Fecha: t.Fecha, Total: t.Total, Bot: bot, Otros: t.Total - bot})
		totalSum += t.Total
		botSum += bot
	}
	botPct := 0.0
	if totalSum > 0 {
		botPct = float64(botSum) / float64(totalSum) * 100
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"from": fromStr, "to": toStr,
		"total": totalSum, "bot": botSum, "otros": totalSum - botSum, "bot_pct": botPct,
		"bot_user_configured": botCedula != "" && botCedula != "000000",
		"rows":                rows,
	})
}

// HandleSiesaConversion mide la conversión REAL bot→SIESA: sesiones del bot (chat_events) vs citas
// REALES creadas por el bot en SIESA (cod_user_asigna_cita = cédula del bot), no el evento
// appointment_created (que es solo lo que el bot CREYÓ hacer). Expone ambas tasas y la discrepancia
// (citas registradas por el bot que no aterrizaron en SIESA). GET /api/internal/siesa/conversion?from&to
// isClientAbort reporta si el error viene de que el CLIENTE cerró la request (context.Canceled
// del r.Context() al desconectarse el dashboard: pestaña cerrada, navegación, refresh o timeout
// del lado del cliente). No es un fallo del servidor: la query se aborta porque ya nadie espera
// la respuesta. Se loguea en Info — como ERROR generaba una alerta Telegram falsa por cada
// abandono del módulo Conversión (firma 'siesa conversion funnel failed: context canceled',
// ciclos 100/126/133/143 de auditoría). OJO: context.DeadlineExceeded NO es abort del cliente
// (es el timeout propio del servidor = query realmente lenta) y sigue siendo ERROR.
func isClientAbort(err error) bool {
	return errors.Is(err, context.Canceled)
}

func (h *InternalHandler) HandleSiesaConversion(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil || h.eventRepo == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		now := time.Now()
		fromStr = now.AddDate(0, 0, -30).Format("2006-01-02")
		toStr = now.Format("2006-01-02")
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
	// Misma ventana acotada para el funnel local y para SIESA (si divergen, la discrepancia miente).
	fromStr = clampAnalyticsRange(fromStr, toStr)
	from, _ = time.Parse("2006-01-02", fromStr)

	// Funnel local: sesiones (denominador) y appointment_created (lo que el bot creyó).
	funnel, err := h.eventRepo.GetFunnel(r.Context(), from, to)
	if err != nil {
		if isClientAbort(err) {
			slog.Info("siesa conversion funnel abortado por el cliente (dashboard cerró la request)")
			return
		}
		slog.Error("siesa conversion funnel failed", "error", err)
		http.Error(w, "failed to read funnel", http.StatusInternalServerError)
		return
	}

	// SIESA: citas reales del bot en la MISMA ventana (to exclusivo = to+1 día, como GetFunnel).
	botCedula := ""
	if h.cfg != nil {
		botCedula = h.cfg.SIESAAssignUserCedula
	}
	toExclusive := to.AddDate(0, 0, 1).Format("2006-01-02")
	sctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()
	rows, err := h.siesaAnalytics.BotCreatedByDay(sctx, botCedula, fromStr, toExclusive)
	if err != nil {
		if isClientAbort(err) {
			slog.Info("siesa conversion created abortado por el cliente (dashboard cerró la request)")
			return
		}
		slog.Error("siesa conversion created failed", "error", err)
		http.Error(w, "failed to read citas reales", http.StatusInternalServerError)
		return
	}
	siesaReal := 0
	for _, d := range rows {
		siesaReal += d.Total
	}

	// botCreated se cuenta por FILAS de appointment_created (1 por cita), no por sesiones distintas,
	// para que la unidad coincida con siesaReal (filas de la tabla citas). Así la discrepancia compara
	// citas-vs-citas: una sesión que agenda varios CUPS genera varias filas en ambos lados y ya no
	// produce una discrepancia negativa espuria.
	botCreated, err := h.eventRepo.CountAppointmentsCreated(r.Context(), from, to)
	if err != nil {
		if isClientAbort(err) {
			slog.Info("siesa conversion count abortado por el cliente (dashboard cerró la request)")
			return
		}
		slog.Error("siesa conversion count created failed", "error", err)
		http.Error(w, "failed to count appointments created", http.StatusInternalServerError)
		return
	}

	sessions := funnel.TotalSessions
	convReal, convBot := 0.0, 0.0
	if sessions > 0 {
		convReal = float64(siesaReal) / float64(sessions) * 100
		convBot = float64(botCreated) / float64(sessions) * 100
	}
	// La discrepancia solo es real cuando el bot creyó crear MÁS citas de las que aterrizaron en SIESA
	// (INSERT perdido). Se acota a >=0: un valor negativo significaría más filas en SIESA que eventos,
	// lo cual no es una pérdida y solo confundiría (la UI la pinta 'danger' solo si es positiva).
	discrepancy := botCreated - siesaReal
	if discrepancy < 0 {
		discrepancy = 0
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"from": fromStr, "to": toStr,
		"sessions": sessions, "bot_created": botCreated, "siesa_real": siesaReal,
		"conversion_real_pct": convReal, "conversion_bot_pct": convBot,
		"discrepancy":         discrepancy,
		"bot_user_configured": botCedula != "" && botCedula != "000000",
		"rows":                rows,
	})
}

// HandleSiesaAgendas lista las agendas próximas (con citas no atendidas) de un médico, para el
// selector del dashboard. Solo lectura NOLOCK. GET /api/internal/siesa/agendas?doctor=<cod_medi>&from=YYYY-MM-DD
func (h *InternalHandler) HandleSiesaAgendas(w http.ResponseWriter, r *http.Request) {
	doctor := strings.TrimSpace(r.URL.Query().Get("doctor"))
	if doctor == "" {
		http.Error(w, "'doctor' (cod_medi) requerido", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	agendas, err := h.appointmentRepo.FindAgendasByDoctor(ctx, doctor, strings.TrimSpace(r.URL.Query().Get("from")))
	if err != nil {
		//nolint:gosec // G706: 'doctor' es un parámetro de un endpoint admin (tras API key), no input público.
		slog.Error("siesa agendas failed", "doctor", doctor, "error", err)
		http.Error(w, "error consultando agendas", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"agendas": agendas, "count": len(agendas)})
}

// HandleSiesaDoctorAgendasOnDate lista las agendas del médico con slots en una fecha (incluye reservas
// vacías, que no salen en /siesa/agendas), para elegir la agenda destino de una reprogramación. Solo
// lectura NOLOCK. GET /api/internal/siesa/doctor-agendas-on-date?doctor=<cod_medi>&date=YYYY-MM-DD
func (h *InternalHandler) HandleSiesaDoctorAgendasOnDate(w http.ResponseWriter, r *http.Request) {
	doctor := strings.TrimSpace(r.URL.Query().Get("doctor"))
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if doctor == "" || date == "" {
		http.Error(w, "'doctor' (cod_medi) y 'date' (YYYY-MM-DD) requeridos", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		http.Error(w, "'date' debe ser YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	agendas, err := h.appointmentRepo.FindDoctorAgendasOnDate(ctx, doctor, date)
	if err != nil {
		slog.Error("siesa doctor-agendas-on-date failed", "doctor", doctor, "date", date, "error", err) //nolint:gosec // G706: endpoint admin (X-API-Key); slog estructura los valores.
		http.Error(w, "error consultando agendas del médico", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"agendas": agendas, "count": len(agendas)})
}

// HandleSiesaAgendaAppointments lista, paginado y filtrable, las citas próximas NO atendidas de una
// agenda (o médico), ordenadas por fecha+hora. Solo lectura NOLOCK, paginación server-side.
// GET /api/internal/siesa/agenda-appointments?agenda_id=&doctor=&date=&name=&doc=&page=&page_size=
func (h *InternalHandler) HandleSiesaAgendaAppointments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.AgendaAppointmentsFilter{
		DoctorCode: strings.TrimSpace(q.Get("doctor")),
		Date:       strings.TrimSpace(q.Get("date")),
		Name:       strings.TrimSpace(q.Get("name")),
		Doc:        strings.TrimSpace(q.Get("doc")),
		Page:       1,
		PageSize:   20,
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 0 {
		f.Page = n
	}
	if n, err := strconv.Atoi(q.Get("page_size")); err == nil && n > 0 {
		f.PageSize = n
	}
	if a := strings.TrimSpace(q.Get("agenda_id")); a != "" {
		if n, err := strconv.Atoi(a); err == nil {
			f.AgendaID = &n
		}
	}
	if f.AgendaID == nil && f.DoctorCode == "" {
		http.Error(w, "requiere 'agenda_id' o 'doctor'", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	pageData, err := h.appointmentRepo.FindAgendaAppointmentsPaged(ctx, f)
	if err != nil {
		slog.Error("siesa agenda-appointments failed", "error", err)
		http.Error(w, "error consultando citas", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pageData)
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

// truncateReason recorta a maxReasonLength BYTES sin partir un carácter UTF-8 multibyte (#29).
func truncateReason(s string) string {
	if len(s) <= maxReasonLength {
		return s
	}
	b := s[:maxReasonLength]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

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

	req.Reason = truncateReason(req.Reason)

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

// --- Acciones individuales por cita (módulo Agenda del dashboard) ---

// CancelAppointmentRequest es el body de POST /api/internal/appointment/{id}/cancel.
type CancelAppointmentRequest struct {
	Reason        string `json:"reason"`         // opcional; default "Cancelada por operador"
	NotifyPatient bool   `json:"notify_patient"` // enviar plantilla de cancelación al paciente
	ReleaseSlots  bool   `json:"release_slots"`  // true: liberar cupos + lista de espera; false: bloquear cupos
}

// proceduresText une los nombres de los procedimientos de una cita (para la plantilla de confirmación).
func proceduresText(procs []domain.AppointmentProcedure) string {
	names := make([]string, 0, len(procs))
	for _, p := range procs {
		if p.CupName != "" {
			names = append(names, p.CupName)
		}
	}
	return strings.Join(names, ", ")
}

// resolvePatientNamePhone resuelve nombre + teléfono del paciente. FindByID de la cita no los trae, así
// que se consulta el PatientRepo (autoid). Devuelve phone "" si no hay número válido.
func (h *InternalHandler) resolvePatientNamePhone(ctx context.Context, appt *domain.Appointment) (name, phone string) {
	name = appt.PatientName
	phone = utils.ParseColombianPhone(appt.PatientPhone)
	if (name == "" || phone == "") && h.patientRepo != nil && appt.PatientID != "" {
		if p, err := h.patientRepo.FindByID(ctx, appt.PatientID); err == nil && p != nil {
			if name == "" {
				name = p.FullName
			}
			if phone == "" {
				phone = utils.ParseColombianPhone(p.Phone)
			}
		}
	}
	return name, phone
}

// sendTemplateAndRegister envía una plantilla de WhatsApp y registra el pending para capturar la
// respuesta del paciente. Devuelve (messageID, enviado). SendTemplate/RegisterPending son APIs sin
// ctx (heredadas del bot); el envío es best-effort → se suprime contextcheck aquí.
//
//nolint:contextcheck
func (h *InternalHandler) sendTemplateAndRegister(phone string, tmpl bird.TemplateConfig, notifType, apptID string) (string, bool) {
	msgID, err := h.birdClient.SendTemplate(phone, tmpl)
	if err != nil {
		slog.Error("dashboard notif", "type", notifType, "phone", utils.MaskPhone(phone), "error", err) //nolint:gosec // G706: endpoint admin (X-API-Key); slog estructura los valores.
		return "", false
	}
	convID := h.birdClient.GetCachedConversationID(phone)
	if convID == "" {
		convID, _ = h.birdClient.LookupConversationByPhone(phone)
	}
	h.notifyManager.RegisterPending(notifications.PendingNotification{
		Type: notifType, Phone: phone, AppointmentID: apptID, BirdMessageID: msgID, ConversationID: convID,
	})
	return msgID, true
}

// sendCancellationTemplate envía la plantilla de cancelación al paciente. Devuelve true si se envió.
func (h *InternalHandler) sendCancellationTemplate(ctx context.Context, appt *domain.Appointment, reason string) bool {
	name, phone := h.resolvePatientNamePhone(ctx, appt)
	if phone == "" {
		slog.Warn("dashboard cancel: paciente sin teléfono")
		return false
	}
	tmpl := bird.TemplateConfig{
		ProjectID: h.cfg.BirdTemplateCancellationProjectID,
		VersionID: h.cfg.BirdTemplateCancellationVersionID,
		Locale:    h.cfg.BirdTemplateCancellationLocale,
		Params: []bird.TemplateParam{
			{Type: "string", Key: "patient_name", Value: name},
			{Type: "string", Key: "appointment_date", Value: utils.FormatFriendlyDate(appt.Date)},
			{Type: "string", Key: "appointment_time", Value: services.FormatTimeSlot(appt.TimeSlot)},
			{Type: "string", Key: "reason", Value: reason},
		},
	}
	_, ok := h.sendTemplateAndRegister(phone, tmpl, "cancellation", appt.ID)
	return ok
}

// HandleCancelAppointment cancela UNA cita (y todos sus slots) en SIESA con la lógica existente.
// Banderas: notify_patient (plantilla de cancelación) y release_slots (liberar cupos + lista de espera
// vs bloquearlos). POST /api/internal/appointment/{id}/cancel
func (h *InternalHandler) HandleCancelAppointment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "appointment id requerido", http.StatusBadRequest)
		return
	}
	var req CancelAppointmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = "Cancelada por operador"
	}
	req.Reason = truncateReason(req.Reason)

	ctx := r.Context()
	appt, err := h.appointmentRepo.FindByID(ctx, id)
	if err != nil {
		slog.Error("cancel appt: find", "id", id, "error", err) //nolint:gosec // G706: endpoint admin (X-API-Key); slog estructura los valores.
		http.Error(w, "error consultando la cita", http.StatusInternalServerError)
		return
	}
	if appt == nil {
		http.Error(w, "cita no encontrada", http.StatusNotFound)
		return
	}
	if appt.Canceled { // idempotencia
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "already_cancelled": true})
		return
	}

	// 1. Cancelar en SIESA (la cita y sus N slots). release_slots decide si además se bloquean los cupos.
	ids := []string{id}
	if req.ReleaseSlots {
		err = h.appointmentRepo.CancelBatch(ctx, ids, req.Reason, "dashboard_cancel", "")
	} else {
		err = h.appointmentRepo.CancelBatchAndBlockSlots(ctx, ids, req.Reason, "dashboard_cancel", "")
	}
	if err != nil {
		slog.Error("cancel appt: siesa", "id", id, "error", err) //nolint:gosec // G706: endpoint admin (X-API-Key); slog estructura los valores.
		http.Error(w, "error cancelando la cita", http.StatusInternalServerError)
		return
	}

	// 2. Cupos liberados → lanzar el protocolo de lista de espera (médico+agenda) en background.
	if req.ReleaseSlots && h.notifyManager != nil {
		if cod, e := strconv.Atoi(appt.DoctorID); e == nil && cod > 0 && appt.AgendaID > 0 {
			go h.notifyManager.CheckWaitingListForSlot(context.WithoutCancel(ctx), cod, appt.AgendaID)
		}
	}

	// 3. Notificar al paciente (plantilla de cancelación), si corresponde.
	notified := false
	if req.NotifyPatient && h.cfg.BirdTemplateCancellationProjectID != "" {
		notified = h.sendCancellationTemplate(ctx, appt, req.Reason)
	}

	// 4. Auditoría: flow_event admin_agenda (log_citas ya lo escribió Cancel; + slog).
	observability.Emit(agendaTrace(appt.AgendaID, appt.Date.Format("2006-01-02")), "admin_agenda", "appointment_cancelled",
		observability.EmitOpts{
			Reason:  req.Reason,
			RefType: "appointment",
			RefID:   id,
			Attrs:   map[string]interface{}{"notify_patient": req.NotifyPatient, "release_slots": req.ReleaseSlots, "notified": notified},
		})
	slog.Info("appointment cancelled (dashboard)", "id", id, "notify", req.NotifyPatient, "release_slots", req.ReleaseSlots) //nolint:gosec // G706: endpoint admin; slog estructura los valores.

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok", "cancelled_id": id, "notified": notified, "slots_released": req.ReleaseSlots,
	})
}

// HandleNotifyConfirmation envía al paciente la plantilla de CONFIRMACIÓN de una cita. NO cambia SIESA:
// el paciente confirma respondiendo (flujo existente del bot, capturado por RegisterPending).
// POST /api/internal/appointment/{id}/notify-confirmation
func (h *InternalHandler) HandleNotifyConfirmation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "appointment id requerido", http.StatusBadRequest)
		return
	}
	if h.cfg.BirdTemplateConfirmProjectID == "" {
		http.Error(w, "plantilla de confirmación no configurada", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	appt, err := h.appointmentRepo.FindByID(ctx, id)
	if err != nil {
		slog.Error("notify confirm: find", "id", id, "error", err) //nolint:gosec // G706: endpoint admin; slog estructura los valores.
		http.Error(w, "error consultando la cita", http.StatusInternalServerError)
		return
	}
	if appt == nil || appt.Canceled {
		http.Error(w, "cita no encontrada o cancelada", http.StatusNotFound)
		return
	}
	name, phone := h.resolvePatientNamePhone(ctx, appt)
	if phone == "" {
		http.Error(w, "paciente sin teléfono válido", http.StatusUnprocessableEntity)
		return
	}
	tmpl := bird.TemplateConfig{
		ProjectID: h.cfg.BirdTemplateConfirmProjectID,
		VersionID: h.cfg.BirdTemplateConfirmVersionID,
		Locale:    h.cfg.BirdTemplateConfirmLocale,
		Params: []bird.TemplateParam{
			{Type: "string", Key: "patient_name", Value: name},
			{Type: "string", Key: "clinic_name", Value: h.cfg.CenterName},
			{Type: "string", Key: "appointment_date", Value: utils.FormatFriendlyDate(appt.Date)},
			{Type: "string", Key: "appointment_time", Value: services.FormatTimeSlot(appt.TimeSlot)},
			{Type: "string", Key: "procedures", Value: proceduresText(appt.Procedures)},
		},
	}
	msgID, ok := h.sendTemplateAndRegister(phone, tmpl, "confirmation", appt.ID)
	if !ok {
		http.Error(w, "error enviando la confirmación", http.StatusBadGateway)
		return
	}
	observability.Emit(agendaTrace(appt.AgendaID, appt.Date.Format("2006-01-02")), "admin_agenda", "confirmation_sent",
		observability.EmitOpts{Phone: phone, RefType: "appointment", RefID: id})
	slog.Info("confirmation sent (dashboard)", "id", id) //nolint:gosec // G706: endpoint admin; slog estructura los valores.

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "notified": true, "message_id": msgID})
}

// --- Reschedule Agenda ---

// RescheduleAgendaRequest is the request body for moving ONE day of an agenda to another date.
type RescheduleAgendaRequest struct {
	AgendaID       int    `json:"agenda_id"`
	OldDate        string `json:"old_date"`        // YYYY-MM-DD (día a vaciar)
	NewDate        string `json:"new_date"`        // YYYY-MM-DD (día destino)
	DestAgendaID   *int   `json:"dest_agenda_id"`  // nil/0 → crear duplicando; >0 → mover a esa agenda existente
	Reason         string `json:"reason"`          // motivo (para plantilla + auditoría)
	NotifyPatients bool   `json:"notify_patients"` // enviar plantilla de reprogramación
	DryRun         bool   `json:"dry_run"`         // solo validar + calcular resumen, sin mutar (vista previa)
	// DoctorDocument es opcional (compatibilidad): el médico se deriva de la agenda origen.
	DoctorDocument string `json:"doctor_document"`
}

// HandleRescheduleAgenda mueve TODAS las citas de UN día (old_date) de una agenda a otra fecha (new_date),
// en una sola transacción en SIESA. Dos escenarios (los distingue dest_agenda_id):
//   - dest_agenda_id ausente/0 → crea la agenda destino DUPLICANDO la grilla del día origen sobre new_date.
//   - dest_agenda_id > 0        → mueve a esa agenda existente (mismo médico, misma grilla libre).
//
// Los slots del día origen quedan BLOQUEADOS (no liberados): el día queda cerrado. Si notify_patients,
// envía la plantilla de reprogramación a los pacientes movidos. POST /api/internal/reschedule-agenda
func (h *InternalHandler) HandleRescheduleAgenda(w http.ResponseWriter, r *http.Request) {
	var req RescheduleAgendaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.AgendaID == 0 || req.OldDate == "" || req.NewDate == "" {
		http.Error(w, "agenda_id, old_date and new_date are required", http.StatusBadRequest)
		return
	}
	oldDate, err := time.Parse("2006-01-02", req.OldDate)
	if err != nil {
		http.Error(w, "old_date must be YYYY-MM-DD format", http.StatusBadRequest)
		return
	}
	newDate, err := time.Parse("2006-01-02", req.NewDate)
	if err != nil {
		http.Error(w, "new_date must be YYYY-MM-DD format", http.StatusBadRequest)
		return
	}
	if req.OldDate == req.NewDate && (req.DestAgendaID == nil || *req.DestAgendaID == req.AgendaID) {
		http.Error(w, "new_date debe diferir de old_date (o indicar otra agenda destino)", http.StatusBadRequest)
		return
	}
	// #30 (auditoría): comparar contra HOY en hora de Colombia (UTC-5, sin DST).
	bogota := time.FixedZone("America/Bogota", -5*3600)
	todayCO, _ := time.Parse("2006-01-02", time.Now().In(bogota).Format("2006-01-02"))
	// old_date solo puede ser HOY o posterior: no se reprograman días ya pasados. NO se validan las horas
	// del día en curso a propósito — ante una cancelación imprevista hay que poder mover TODAS las citas
	// del día de hoy, incluidas las de horas ya transcurridas.
	if oldDate.Before(todayCO) {
		http.Error(w, "old_date no puede ser un día ya pasado (solo hoy o posterior)", http.StatusBadRequest)
		return
	}
	if newDate.Before(todayCO) {
		http.Error(w, "new_date must be today or later", http.StatusBadRequest)
		return
	}

	req.Reason = truncateReason(req.Reason)
	ctx := r.Context()

	in := domain.RescheduleDayInput{AgendaID: req.AgendaID, OldDate: req.OldDate, NewDate: req.NewDate, DryRun: req.DryRun}
	if req.DestAgendaID != nil {
		in.DestAgendaID = *req.DestAgendaID
	}

	res, err := h.appointmentRepo.RescheduleDayOfAgenda(ctx, in)
	if err != nil {
		slog.Error("reschedule day: move", "agenda_id", req.AgendaID, "old_date", req.OldDate, "new_date", req.NewDate, "dry_run", req.DryRun, "error", err) //nolint:gosec // G706: endpoint admin (X-API-Key); slog estructura los valores.
		// Reglas de negocio (agenda inexistente, sin citas, destino incompatible, conflicto de horario) →
		// 409 con el mensaje; fallos de infraestructura (tx/consulta) → 500 genérico.
		var invalid domain.RescheduleInvalidError
		if errors.As(err, &invalid) {
			http.Error(w, invalid.Msg, http.StatusConflict)
		} else {
			http.Error(w, "error reprogramando la agenda", http.StatusInternalServerError)
		}
		return
	}

	// Vista previa: el repo ya validó y calculó el resumen sin mutar nada → responder y salir (no notificar).
	if req.DryRun {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok", "dry_run": true,
			"moved": res.Moved, "dest_agenda_id": res.DestAgendaID, "created_agenda": res.Created,
		})
		return
	}

	// Notificar SOLO a los pacientes movidos (no a los que ya estaban en la agenda destino).
	toNotify := 0
	if req.NotifyPatients && len(res.MovedIDs) > 0 {
		moved := h.movedAppointmentsForNotify(ctx, res.DestAgendaID, req.NewDate, res.MovedIDs)
		toNotify = h.sendRescheduleNotifications(moved, req) //nolint:contextcheck // envío en background con su propio timeout (patrón existente)
	}

	if h.tracker != nil {
		h.tracker.LogEvent(ctx, "", "", "admin_reschedule_agenda", map[string]interface{}{
			"agenda_id":          req.AgendaID,
			"old_date":           req.OldDate,
			"new_date":           req.NewDate,
			"dest_agenda_id":     res.DestAgendaID,
			"created_agenda":     res.Created,
			"appointments_moved": res.Moved,
			"patients_to_notify": toNotify,
		})
	}

	slog.Info("agenda day rescheduled", "agenda_id", req.AgendaID, "dest_agenda_id", res.DestAgendaID,
		"created", res.Created, "old_date", req.OldDate, "new_date", req.NewDate, "moved", res.Moved, "to_notify", toNotify)

	observability.Emit(agendaTrace(req.AgendaID, req.NewDate), "admin_agenda", "agenda_rescheduled",
		observability.EmitOpts{
			Reason:  req.Reason,
			RefType: "agenda",
			RefID:   strconv.Itoa(req.AgendaID),
			Attrs:   map[string]interface{}{"n": res.Moved, "count": toNotify, "dest_agenda_id": res.DestAgendaID, "created": res.Created},
		})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "ok",
		"moved":          res.Moved,
		"dest_agenda_id": res.DestAgendaID,
		"created_agenda": res.Created,
		"to_notify":      toNotify,
	})
}

// movedAppointmentsForNotify devuelve, de las citas ahora en (destAgenda,newDate), solo las cuyo id está
// en movedIDs (las que efectivamente se movieron), para notificar únicamente a esos pacientes.
func (h *InternalHandler) movedAppointmentsForNotify(ctx context.Context, destAgenda int, newDate string, movedIDs []string) []domain.Appointment {
	if h.cfg.BirdTemplateRescheduleProjectID == "" {
		return nil
	}
	all, err := h.appointmentRepo.FindByAgendaAndDate(ctx, destAgenda, newDate)
	if err != nil {
		slog.Error("reschedule day: fetch moved for notify", "dest_agenda_id", destAgenda, "error", err) //nolint:gosec // G706: endpoint admin; slog estructura los valores.
		return nil
	}
	want := make(map[string]struct{}, len(movedIDs))
	for _, id := range movedIDs {
		want[id] = struct{}{}
	}
	var out []domain.Appointment
	for _, a := range all {
		if _, ok := want[a.ID]; ok {
			out = append(out, a)
		}
	}
	return out
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
	// #28 (auditoría): rechazar JSON malformado. Antes el error se ignoraba y req quedaba en cero
	// (dry_run=false) → un body inválido disparaba un BARRIDO real de toda la lista. Body vacío (EOF)
	// se permite (= barrido de todos los CUPS).
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

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
//	from     — start datetime: YYYY-MM-DD or YYYY-MM-DDTHH:MM (sin from/to: últimas 24 h)
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
	// Sin ventana explícita, mirar solo las últimas 24 h: si no, findLogFiles hace glob de TODOS los
	// archivos del directorio (meses de logs, varios GB) para responder un "últimas 200 líneas".
	if filter.From.IsZero() && filter.To.IsZero() {
		filter.From = time.Now().Add(-24 * time.Hour)
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

// HandleMRCConsumption expone el consumo mensual por grupo MRC con la MISMA métrica del gate del
// bot, desglosado en total vs creado-por-el-bot. Valida reportes de sobrecupo de la entidad
// (¿sucedió? ¿cuánto fue del bot?) sin entrar a la BD a mano, y le da al auditor una vigilancia
// permanente del tope (H145). GET /api/internal/siesa/mrc-consumption?year=YYYY&month=M
func (h *InternalHandler) HandleMRCConsumption(w http.ResponseWriter, r *http.Request) {
	if h.siesaAnalytics == nil {
		http.Error(w, "siesa analytics not available", http.StatusServiceUnavailable)
		return
	}
	now := time.Now()
	year, month := now.Year(), int(now.Month())
	if y := r.URL.Query().Get("year"); y != "" {
		if n, err := strconv.Atoi(y); err == nil && n >= 2020 && n <= 2100 {
			year = n
		}
	}
	if m := r.URL.Query().Get("month"); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n >= 1 && n <= 12 {
			month = n
		}
	}
	botCedula := ""
	if h.cfg != nil {
		botCedula = h.cfg.SIESAAssignUserCedula
	}

	type groupOut struct {
		Group    string `json:"group"`
		Limit    int    `json:"limit"`
		Consumed int    `json:"consumed"`
		Bot      int    `json:"bot_consumed"`
		Others   int    `json:"others_consumed"`
		Over     int    `json:"over"` // consumido - tope (0 si no hay exceso)
	}
	catalog := services.MRCGroupsCatalog()
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]groupOut, 0, len(names))
	sctx, cancel := context.WithTimeout(r.Context(), analyticsQueryTimeout)
	defer cancel()
	for _, name := range names {
		g := catalog[name]
		total, bot, err := h.siesaAnalytics.MRCGroupMonthlyConsumption(sctx, g.CupsCodes, year, month, botCedula)
		if err != nil {
			if isClientAbort(err) {
				slog.Info("mrc consumption abortado por el cliente")
				return
			}
			slog.Error("mrc consumption failed", "group", name, "error", err)
			http.Error(w, "failed to read mrc consumption", http.StatusInternalServerError)
			return
		}
		over := total - g.MaxPerMonth
		if over < 0 {
			over = 0
		}
		out = append(out, groupOut{
			Group: name, Limit: g.MaxPerMonth, Consumed: total,
			Bot: bot, Others: total - bot, Over: over,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"year": year, "month": month, "bot_cedula_masked": utils.MaskPhone(botCedula), "groups": out,
	})
}
