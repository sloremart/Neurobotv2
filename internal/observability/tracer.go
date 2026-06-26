// Package observability implementa la traza estructurada de flujos de negocio (flow_events).
// Es un emitter package-level (como slog): se inicializa una vez en el arranque con Init y se
// usa desde cualquier flujo con Emit. Ver docs/OBSERVABILIDAD.md.
package observability

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Level es el tier de registro/filtro. FLOW_TRACE_LEVEL fija el máximo: un evento se descarta si
// su nivel supera el configurado. Acumulativo (un nivel incluye los de abajo).
type Level int

// Niveles (acumulativos): un nivel incluye los de abajo. Ver §3A de docs/OBSERVABILIDAD.md.
const (
	LvOff       Level = iota // 0 — nada
	LvError                  // 1 — solo errores + anomalías
	LvOutcome                // 2 — + terminales de cada flujo
	LvMilestone              // 3 — + decisiones/hitos de negocio (default prod)
	LvFull                   // 4 — + detalle verboso
)

// ParseLevel convierte el valor de FLOW_TRACE_LEVEL. Vacío/ inválido → milestone.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return LvOff
	case "error":
		return LvError
	case "outcome":
		return LvOutcome
	case "full":
		return LvFull
	case "milestone", "":
		return LvMilestone
	default:
		slog.Warn("invalid FLOW_TRACE_LEVEL, using milestone", "value", s)
		return LvMilestone
	}
}

// stepSpec es la clasificación de un (flow, step): su nivel, su outcome por defecto y el tipo de
// artefacto que referencia. Es la ÚNICA fuente de verdad de la clasificación de §3A.
type stepSpec struct {
	level   Level
	outcome string
	refType string
}

// catalog mapea "flow/step" → clasificación. Toda emisión debe tener su entrada aquí (un test lo
// verifica). Fase 0: solo lista de espera; se amplía por fase.
var catalog = map[string]stepSpec{
	// Lista de espera (§3.1 / §3A)
	"lista_espera/enrolled":          {LvMilestone, "ok", ""},
	"lista_espera/slot_match":        {LvMilestone, "ok", "slot"},
	"lista_espera/skipped":           {LvFull, "info", ""},
	"lista_espera/claim_lost":        {LvFull, "info", ""},
	"lista_espera/duplicate_found":   {LvOutcome, "blocked", ""},
	"lista_espera/notified":          {LvMilestone, "ok", "bird_msg"},
	"lista_espera/notify_failed":     {LvError, "error", ""},
	"lista_espera/response_schedule": {LvMilestone, "ok", ""},
	"lista_espera/declined":          {LvOutcome, "info", ""},
	"lista_espera/booked":            {LvOutcome, "ok", "cita"},
	"lista_espera/expired":           {LvOutcome, "info", ""},

	// Agendamiento (§3.2 / §3A) — trace sess:<id>
	"agendar/ocr_ok":              {LvMilestone, "ok", ""},
	"agendar/ocr_failed":          {LvError, "error", ""},
	"agendar/cups_none":           {LvOutcome, "escalated", ""},
	"agendar/gfr_blocked":         {LvOutcome, "blocked", ""},
	"agendar/pregnancy_blocked":   {LvOutcome, "blocked", ""},
	"agendar/special_escalated":   {LvOutcome, "escalated", ""},
	"agendar/already_has_appt":    {LvOutcome, "blocked", ""},
	"agendar/coverage_particular": {LvMilestone, "ok", ""},
	"agendar/coverage_escalated":  {LvOutcome, "escalated", ""},
	"agendar/slots_found":         {LvMilestone, "ok", ""},
	"agendar/no_slots":            {LvOutcome, "blocked", ""},
	"agendar/booking_success":     {LvOutcome, "ok", "cita"},
	"agendar/booking_failed":      {LvError, "error", ""},

	// Recordatorio / IVR (§3.3 / §3A) — trace notif:<apptID>
	"notif_recordatorio/reminder_sent": {LvMilestone, "ok", "bird_msg"},
	"notif_recordatorio/ivr_placed":    {LvMilestone, "ok", "call"},
	"notif_recordatorio/confirmed":     {LvOutcome, "ok", ""},
	"notif_recordatorio/cancelled":     {LvOutcome, "ok", ""},
	"notif_recordatorio/escalated":     {LvOutcome, "escalated", ""},
	"notif_recordatorio/expired":       {LvOutcome, "info", ""},
	"notif_recordatorio/error":         {LvError, "error", ""},

	// Identificación (§3A) — trace sess:<id>
	"identificacion/patient_lookup":  {LvMilestone, "ok", ""},
	"identificacion/contact_updated": {LvMilestone, "ok", ""},
	"identificacion/lookup_failed":   {LvError, "error", ""},

	// Entidad / EPS / Contrato (§3A) — trace sess:<id>
	"entidad/entity_selected":      {LvMilestone, "ok", ""},
	"entidad/contract_resolved":    {LvMilestone, "ok", ""},
	"entidad/sanitas_muni_forced":  {LvMilestone, "ok", ""},
	"entidad/no_entities":          {LvOutcome, "escalated", ""},
	"entidad/entity_update_failed": {LvError, "error", ""},

	// Registro (§3A) — trace sess:<id>
	"registro/registration_started":   {LvMilestone, "ok", ""},
	"registro/patient_created":        {LvOutcome, "ok", ""},
	"registro/registration_abandoned": {LvOutcome, "info", ""},
	"registro/patient_create_failed":  {LvError, "error", ""},

	// Mis citas (§3A) — trace sess:<id>
	"mis_citas/appointments_listed": {LvMilestone, "ok", ""},
	"mis_citas/appt_confirmed":      {LvOutcome, "ok", "cita"},
	"mis_citas/appt_cancelled":      {LvOutcome, "ok", "cita"},
	"mis_citas/reschedule_started":  {LvMilestone, "ok", ""},
	"mis_citas/no_appointments":     {LvOutcome, "info", ""},

	// Escalación a agente (§3A) — trace sess:<id>
	"escalacion/escalated":           {LvOutcome, "escalated", ""},
	"escalacion/agent_resumed":       {LvMilestone, "ok", ""},
	"escalacion/agent_closed":        {LvOutcome, "ok", ""},
	"escalacion/agent_reminder_sent": {LvMilestone, "retry", ""},
	"escalacion/escalation_expired":  {LvOutcome, "info", ""},

	// Tareas del scheduler (§3A) — trace task:<name>:<date>
	"scheduler/task_completed": {LvOutcome, "ok", ""},
	"scheduler/task_failed":    {LvError, "error", ""},

	// Admin agenda (§3A) — trace agenda:<id>:<date>
	"admin_agenda/agenda_cancelled":   {LvOutcome, "ok", "agenda"},
	"admin_agenda/agenda_rescheduled": {LvOutcome, "ok", "agenda"},
	"admin_agenda/patients_notified":  {LvMilestone, "ok", ""},

	// Infra / transversal (§3A)
	"infra/session_abandoned":  {LvOutcome, "info", ""}, // trace sess:<id>
	"infra/phone_lock_timeout": {LvError, "error", ""},  // trace infra:phone_lock_timeout

	// Reconciliación de invariantes (§4) — anomalía de "mal comportamiento silencioso".
	"invariante/anomaly": {LvError, "error", ""},
}

// EmitOpts son los datos variables de un evento. Outcome/RefType vacíos usan los del catálogo.
type EmitOpts struct {
	Outcome string
	Reason  string
	Phone   string // se enmascara antes de persistir
	RefType string // override del refType del catálogo (p.ej. anomalías que referencian distintos artefactos)
	RefID   string
	Attrs   map[string]interface{}
}

// allowedAttrKeys es el vocabulario fijo de claves permitidas en attrs (§10.5). Cualquier clave
// fuera del set se descarta → imposible filtrar PII (documento/nombre) por descuido.
var allowedAttrKeys = map[string]bool{
	"cups": true, "asunto": true, "contrato": true, "espacios": true, "servicio": true,
	"trigger": true, "duration_ms": true, "n": true, "remaining": true, "count": true,
	"contrast": true, "sedation": true, "reason": true, "year": true, "month": true,
	"severity": true, "check": true,
}

func sanitizeAttrs(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		if allowedAttrKeys[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const (
	batchSize     = 200
	flushInterval = 2 * time.Second
	bufferSize    = 1024
)

// FlowSink persiste lotes de eventos (localrepo.FlowRepo lo satisface).
type FlowSink interface {
	InsertBatch(ctx context.Context, events []domain.FlowEvent) error
}

// Tracer es el escritor batched asíncrono propio (no reusa el de chat_events).
type Tracer struct {
	sink    FlowSink
	maxLvl  Level
	ch      chan domain.FlowEvent
	dropped atomic.Int64
}

// New crea un Tracer. Llamar Start(ctx) en una goroutine antes de usar.
func New(sink FlowSink, maxLvl Level) *Tracer {
	return &Tracer{sink: sink, maxLvl: maxLvl, ch: make(chan domain.FlowEvent, bufferSize)}
}

// emit aplica el gate de nivel, construye el evento (con PII enmascarada) y lo encola sin bloquear.
// maxReasonLen es el ancho de la columna flow_events.reason (VARCHAR(60)).
const maxReasonLen = 60

// truncateReason acota reason al ancho de columna y le quita saltos de línea (M6): un err.Error()
// crudo del driver podía exceder 60 chars y, con STRICT_TRANS_TABLES, tumbar el INSERT del lote
// completo; además recorta el detalle del driver que podría arrastrar PII.
func truncateReason(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
	r := []rune(s) // por-runa: no parte un carácter multibyte (acentos) y respeta el VARCHAR(60)
	if len(r) > maxReasonLen {
		return string(r[:maxReasonLen])
	}
	return s
}

func (t *Tracer) emit(traceID, flow, step string, opts EmitOpts) {
	if t.maxLvl == LvOff {
		return
	}
	spec, ok := catalog[flow+"/"+step]
	if !ok {
		slog.Warn("flow_events: unknown (flow, step) — defaulting to milestone", "flow", flow, "step", step)
		spec = stepSpec{level: LvMilestone, outcome: "info"}
	}
	if spec.level > t.maxLvl {
		return // más verboso que el máximo configurado
	}
	outcome := opts.Outcome
	if outcome == "" {
		outcome = spec.outcome
	}
	refType := spec.refType
	if opts.RefType != "" {
		refType = opts.RefType
	}
	phone := ""
	if opts.Phone != "" {
		phone = utils.MaskPhone(opts.Phone)
	}
	ev := domain.FlowEvent{
		TraceID: traceID, Flow: flow, Step: step, Level: int(spec.level),
		Outcome: outcome, Reason: truncateReason(opts.Reason), Phone: phone,
		RefType: refType, RefID: opts.RefID, Attrs: sanitizeAttrs(opts.Attrs),
		CreatedAt: time.Now(),
	}
	select {
	case t.ch <- ev:
	default:
		t.dropped.Add(1) // buffer lleno: dropear, nunca bloquear el flujo del paciente
	}
}

// Start vacía el buffer en lotes (cada batchSize eventos o flushInterval) y drena en el apagado.
func (t *Tracer) Start(ctx context.Context) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	buf := make([]domain.FlowEvent, 0, batchSize)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		// WithoutCancel: deriva del ctx pero NO se cancela en el apagado, para que el drain final
		// pueda persistir el último lote aunque el ctx ya esté cancelado.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := t.sink.InsertBatch(cctx, buf); err != nil {
			slog.Error("flow_events insert batch", "count", len(buf), "error", err)
		}
		cancel()
		buf = buf[:0]
		// L3: surfacear los eventos descartados por buffer lleno (drop-on-full intencional, pero
		// invisible hasta ahora). Se reporta y se resetea para no spamear.
		if n := t.dropped.Swap(0); n > 0 {
			slog.Warn("flow_events dropped (buffer full)", "count", n)
		}
	}

	for {
		select {
		case <-ctx.Done():
			for { // drenar lo que quede
				select {
				case ev := <-t.ch:
					buf = append(buf, ev)
					if len(buf) >= batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case ev := <-t.ch:
			buf = append(buf, ev)
			if len(buf) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// ── API package-level (como slog) ───────────────────────────────────────────────────────────

var std atomic.Pointer[Tracer]

// Init fija el tracer global. Llamar una vez en el arranque, antes de servir.
func Init(t *Tracer) { std.Store(t) }

// Emit registra un evento de flujo. No-op si no se ha inicializado (tests).
func Emit(traceID, flow, step string, opts EmitOpts) {
	if t := std.Load(); t != nil {
		t.emit(traceID, flow, step, opts)
	}
}

// ── Helpers de trace_id (§10.4) ─────────────────────────────────────────────────────────────

// TraceSession correlaciona por sesión conversacional.
func TraceSession(sessionID string) string { return "sess:" + sessionID }

// TraceWaitingList correlaciona el recorrido completo de una entrada de lista de espera.
func TraceWaitingList(entryID string) string { return "wl:" + entryID }

// TraceNotif correlaciona el recorrido de un recordatorio por cita (apptID ya es único).
func TraceNotif(apptID string) string { return "notif:" + apptID }

// TraceAgenda correlaciona una operación admin sobre una agenda en una fecha.
func TraceAgenda(agendaID, yyyymmdd string) string { return "agenda:" + agendaID + ":" + yyyymmdd }

// TraceTask correlaciona una corrida de tarea del scheduler (yyyymmdd = fecha de ejecución).
func TraceTask(name, yyyymmdd string) string { return "task:" + name + ":" + yyyymmdd }
