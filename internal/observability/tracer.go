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
	// slot_released: se disparó el chequeo de lista de espera por una LIBERACIÓN de slot
	// (cancelación con release). Terminal de un solo evento en su propia traza (slotrel:...) con
	// attrs {cod_medi, agenda_id, notified} — denominador del KPI de recuperación de cupos.
	"lista_espera/slot_released": {LvOutcome, "info", ""},
	"lista_espera/expired":       {LvOutcome, "info", ""},

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
	"agendar/reschedule_orphan":   {LvError, "error", "cita"}, // M2: vieja sin cancelar tras reagendar
	// Callejón de UX: el bot rechazó una imagen/documento "fuera de contexto". Se emite como
	// flow_event (además del chat_event) para que sea VISIBLE en el funnel de la skill auditora; el
	// attr `state` distingue el caso benigno (imagen suelta en un menú) del BUG (rechazo en un estado
	// que SÍ espera foto, p.ej. un UPLOAD_* nuevo sin whitelistear). Milestone: no es terminal.
	"agendar/image_out_of_context": {LvMilestone, "info", ""},

	// Auditoría 2026-08-01 (H131-1): steps que ya se emitían pero NUNCA estuvieron en el catálogo, así
	// que caían en el default milestone/info. El nivel se asigna por lo que HACE cada uno en el código:
	// terminal (LvOutcome) si cierra el flujo, milestone si el paciente sigue dentro.
	"agendar/stashed_order_used":        {LvMilestone, "ok", ""},      // se consumió la orden guardada
	"agendar/stashed_order_failed":      {LvMilestone, "retry", ""},   // no se pudo leer → se re-pide
	"agendar/photo_first_message":       {LvMilestone, "info", ""},    // foto como 1er mensaje
	"agendar/photo_intent_scheduling":   {LvMilestone, "info", ""},    // foto = intención de agendar
	"agendar/ocr_page_appended":         {LvMilestone, "ok", ""},      // página adicional fusionada
	"agendar/ocr_append_failed":         {LvMilestone, "retry", ""},   // no se pudo anexar → se re-pide
	"agendar/ocr_no_cups_code":          {LvOutcome, "escalated", ""}, // sin CUPS válidos → agente
	"agendar/bloqueo_sanitas_escalated": {LvOutcome, "escalated", ""}, // CUP de bloqueo SANITAS → agente

	// Recordatorio / IVR (§3.3 / §3A) — trace notif:<apptID>
	"notif_recordatorio/reminder_sent": {LvMilestone, "ok", "bird_msg"},
	"notif_recordatorio/ivr_placed":    {LvMilestone, "ok", "call"},
	"notif_recordatorio/confirmed":     {LvOutcome, "ok", ""},
	"notif_recordatorio/cancelled":     {LvOutcome, "ok", ""},
	"notif_recordatorio/escalated":     {LvOutcome, "escalated", ""},
	"notif_recordatorio/expired":       {LvOutcome, "info", ""},
	"notif_recordatorio/error":         {LvError, "error", ""},
	// H131-1. same_day_no_response es TERMINAL válido (GUIA §16): la cita es de hoy, se avisó y el
	// paciente no contestó; no hay más que hacer. Como milestone, su trace quedaba "sin cerrar" para
	// siempre y aparecía como estancado en cada ciclo de auditoría.
	"notif_recordatorio/same_day_reminder_sent": {LvMilestone, "ok", "bird_msg"},
	"notif_recordatorio/same_day_no_response":   {LvOutcome, "info", ""},
	// escalation_no_conversation: sin conversation_id NO se manda la nota ni se asigna el agente, así
	// que el handoff FALLÓ aunque nada lance excepción. Mismo trato que escalacion/escalation_no_channel,
	// su equivalente exacto: terminal con outcome de error, para que deje de ser un fallo invisible.
	"notif_recordatorio/escalation_no_conversation": {LvOutcome, "error", ""},
	// suppressed_no_whatsapp: el número acumula >=2 fallos de ENTREGA confirmados por el webhook
	// de Bird — el template se cobraría sin llegar. Terminal tipo bloqueo (decisión deliberada,
	// no un fallo); el call center debe contactar al paciente por otra vía.
	"notif_recordatorio/suppressed_no_whatsapp": {LvOutcome, "blocked", ""},

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
	// agent_takeover: un agente respondió manualmente una sesión ACTIVA (sin escalación previa) y
	// el bot se pausó solo para no interferir. Terminal escalated: la sesión queda en manos humanas.
	"escalacion/agent_takeover": {LvOutcome, "escalated", ""},
	// retorno_estado_inexistente: un retorno apuntaba a un estado sin handler y se retomó en el
	// saludo. Es un fallo del lado del bot (menú o comando mal armado), no del paciente.
	"escalacion/retorno_estado_inexistente": {LvError, "error", ""},
	"escalacion/escalation_expired":         {LvOutcome, "info", ""},
	"escalacion/agent_no_show":              {LvOutcome, "no_show", ""}, // agente nunca atendió → bot retoma
	"escalacion/escalation_no_channel":      {LvOutcome, "error", ""},   // no se pudo resolver conversación en Bird
	// H131-1. ack_sent: acuse al paciente de que un agente lo atenderá (sigue esperando → milestone).
	// escalation_no_agents: el gate de disponibilidad CIERRA el intento y devuelve al paciente al menú;
	// es terminal y es un bloqueo, no un 'info' (como milestone salía "estancado" en cada ciclo — ya
	// reportado como cosmético en el ciclo 121).
	"escalacion/ack_sent":             {LvMilestone, "info", ""},
	"escalacion/escalation_no_agents": {LvOutcome, "blocked", ""},
	// Cortacircuitos anti-bucle (incidente 11/12-ago-2026, no-show↔re-escalación). Ambos son
	// terminales del intento de escalar: blocked = regla de protección, no error del paciente.
	// Si suppressed_resume crece sostenido = hay estados automáticos re-escalando tras no-show
	// (el bucle que el guard corta); si cap_reached aparece = una sesión pidió >3 escalaciones.
	"escalacion/escalation_suppressed_resume": {LvOutcome, "blocked", ""},
	"escalacion/escalation_cap_reached":       {LvOutcome, "blocked", ""},
	// agent_input_rejected: el agente devolvió la conversación (Néctar) con un dato ESCRITO que el
	// bot no pudo usar. No es terminal: el caso vuelve a una persona con el motivo. Es la métrica
	// de los errores del lado humano —si sube, hay que revisar la pantalla del agente o el texto
	// de ayuda, no el bot—.
	"escalacion/agent_input_rejected": {LvError, "retry", ""},
	// cola_vencida: nadie tomó el caso en la cola de Néctar a tiempo; Néctar lo devolvió y el bot
	// se lo dijo al paciente y cerró. No es un error del bot, pero sí una promesa incumplida: el
	// número que dice si la cola da abasto.
	"escalacion/cola_vencida": {LvOutcome, "info", ""},
	// encolada_proximo_dia: la escalación SÍ se ejecutó (va junto a `escalated`, no en su lugar),
	// pero entró cuando ya no le cabía a nadie en el día — dentro del margen de cierre (attr
	// motivo=margen) o con el horario cerrado (motivo=fuera_de_horario). Es un hito: el caso está
	// vivo en la cola de Néctar. Mide cuánta demanda cae fuera del día y cuánta la produce el
	// margen, que es la mitad que se puede mover con una variable.
	"escalacion/encolada_proximo_dia": {LvMilestone, "queued", ""},
	// equipo_sugerido: el bot le pidió a Néctar que el caso vaya al equipo del grupo de CUPS
	// (`suggested_team_id`, NECTAR_TEAM_BY_GROUP). Va JUNTO al escalado y solo cuando hay mapeo
	// para el grupo; es lo único que permite comprobar a qué equipo se pidió cada caso.
	"escalacion/equipo_sugerido": {LvMilestone, "ok", ""},
	// siguiente_procedimiento: el agente de Néctar dio por gestionado el procedimiento actual y el
	// bot siguió con el siguiente grupo de la orden (ADVANCE_NEXT_PROCEDURE).
	"escalacion/siguiente_procedimiento": {LvMilestone, "ok", ""},
	// Restricción de edad en neurología: menor intenta neurología adultos o adulto intenta pediátrica.
	// Terminal: se regresa al menú principal para que el paciente elija la especialidad correcta.
	"agendar/age_restriction_blocked": {LvOutcome, "blocked", ""},
	// H145 (12-ago-2026, reporte de la entidad): tope mensual MRC. Es el CANDADO FINAL disparándose
	// (create/consolidación) — cada ocurrencia con reason=grupo; si crece sostenido con
	// from_waiting_list, algún camino upstream sigue entrando sin filtro de búsqueda.
	"agendar/mrc_limit_blocked": {LvOutcome, "blocked", ""},
	// mrc_month_filtered: el filtro de busqueda DESCARTA un mes por tope MRC alcanzado. No es
	// terminal (el paciente sigue viendo otros meses, o acaba en no_slots): milestone. Es el unico
	// rastro de que el tope esta trabajando — sin el, la proteccion era invisible y no se podia
	// auditar cuanto frena ni si algun camino agendo con el cupo cruzado.
	"agendar/mrc_month_filtered": {LvMilestone, "blocked", ""},

	// Recuperación asistida por IA (§11) — trace sess:<id>. Concepto distinto de escalación.
	"recuperacion/ai_recovery_started":  {LvMilestone, "info", ""},    // 3.ª falla del bot → arranca IA
	"recuperacion/ai_recovered":         {LvOutcome, "ok", ""},        // la IA formateó y el bot avanzó
	"recuperacion/ai_resolved_by_bot":   {LvOutcome, "ok", ""},        // resolvió el validador puro (sin LLM)
	"recuperacion/ai_clarified":         {LvMilestone, "retry", ""},   // la IA envió mensaje aclaratorio
	"recuperacion/ai_failed":            {LvOutcome, "escalated", ""}, // agotó presupuesto IA → escala
	"recuperacion/ai_domain_block":      {LvOutcome, "blocked", ""},   // regla de dominio impidió inferir
	"recuperacion/ai_month_cap_reached": {LvOutcome, "info", ""},      // tope mensual → directo a agente

	// Tareas del scheduler (§3A) — trace task:<name>:<date>
	"scheduler/task_completed": {LvOutcome, "ok", ""},
	"scheduler/task_failed":    {LvError, "error", ""},

	// Admin agenda (§3A) — trace agenda:<id>:<date>
	"admin_agenda/agenda_cancelled":      {LvOutcome, "ok", "agenda"},
	"admin_agenda/agenda_rescheduled":    {LvOutcome, "ok", "agenda"},
	"admin_agenda/patients_notified":     {LvMilestone, "ok", ""},
	"admin_agenda/appointment_cancelled": {LvOutcome, "ok", "appointment"},   // cancelación individual desde el dashboard
	"admin_agenda/confirmation_sent":     {LvMilestone, "ok", "appointment"}, // envío de confirmación individual

	// Infra / transversal (§3A)
	"infra/session_abandoned":  {LvOutcome, "info", ""}, // trace sess:<id>
	"infra/phone_lock_timeout": {LvError, "error", ""},  // trace infra:phone_lock_timeout
	// non_contactable_identifier: el identificador del contacto no es un teléfono E.164 (nombres del
	// workspace de Bird); ningún envío por Channels puede entregársele. Terminal con error: el
	// paciente NO recibe respuesta por ese canal y el origen debe corregirse en Bird.
	"infra/non_contactable_identifier": {LvOutcome, "error", ""}, // trace sess:<id>, 1 vez por sesión

	// Néctar (webhook POST /api/webhooks/nectar) — trace sess:nectar:<conversation_id>.
	// protocolo/version_desconocida: Néctar mandó un aviso con una versión de protocolo MAYOR que
	// la que este binario sabe leer (nectar.ProtocolMax). El aviso se procesa igual —el lector es
	// tolerante y lo desconocido se ignora—, así que NO es un paciente colgado: es que el otro
	// lado desplegó antes que nosotros y hay que ponerse al día. Va como error porque es lo que
	// sube a Telegram, el único sitio donde alguien lo va a ver a tiempo.
	"protocolo/version_desconocida": {LvError, "error", ""},
	// apertura/*: el aviso `conversacion_abierta_por_agente` (un agente abrió la conversación con
	// una plantilla). En Neuro la respuesta es SIEMPRE «agent»: no se preabre ninguna sesión, y
	// estos pasos son el único rastro de que Néctar preguntó. Info (no warn): el vocabulario de
	// outcome de Neuro no tiene «warn» y ninguno de los tres es un fallo del paciente.
	//   solo_traza: aviso legible; se respondió «agent» y quedó la traza (plantilla).
	//   aviso_invalido: `opened` ausente o sin template_name; se respondió «agent» igual, pero
	//     algo en el lado de Néctar no mandó lo que el contrato promete.
	//   replay_perdida: el aviso volvió por el REPLAY del WAL (arranque tras una caída, o el
	//     barredor de filas atascadas): su valor era la respuesta síncrona, que murió con el
	//     request. Se acusa y se ignora; cada una es una conversación que se quedó del lado del
	//     agente por una caída del bot.
	"apertura/solo_traza":     {LvMilestone, "info", ""},
	"apertura/aviso_invalido": {LvMilestone, "info", ""},
	"apertura/replay_perdida": {LvMilestone, "info", ""},

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
	// Recuperación IA: conteo de tokens/llamadas del LLM (sin PII) para KPIs de costo,
	// y "state" = estado bloqueado (para el KPI de recuperación por estado).
	"tokens_in": true, "tokens_out": true, "llm_calls": true, "state": true,
	// Escalación (ciclo 98): diagnóstico por-capa de la resolución del conversation_id cuando el
	// handoff falla con "empty conversation ID". Todo booleano/enum, sin PII. Permite saber en la
	// PRÓXIMA ocurrencia qué capa cayó (webhook/caché/send/fetch/lookup) sin suponer.
	"msg_conv_id": true, "cache_hit": true, "sent_ok": true, "fetch_ok": true, "lookup": true,
	// Callejones de foto: distingue el rechazo REAL (stashed=false, la foto se perdió) de la
	// recuperación (stashed=true, quedó guardada para el paso de la orden). Sin esta clave el attr se
	// descartaba en silencio y el evento no permitía separar ambos casos.
	"stashed": true,
	// stash_reason: por qué NO se guardó (no_media / already_read / already_stashed). Con `stashed`
	// solo se sabe QUE no se guardó; el motivo era una inferencia desde fuera (ciclo 130).
	"stash_reason": true,
	// Tope MRC (auditoría 01-sep-2026). `consumed`/`limit`/`qty` convierten un "algo se bloqueó"
	// en "el grupo va 1372/932 y esta cita pedía 8": sin ellos no se puede medir cuánto protege el
	// tope. `date` y `mrc_groups` van en booking_success para saber a qué mes y a qué grupo con
	// tope fue cada cita SIN tener que cruzar con SIESA. Todo agregado, sin PII.
	"consumed": true, "limit": true, "qty": true, "date": true, "mrc_groups": true,
	// Néctar: `plantilla` es el nombre de la plantilla de Meta con la que un agente abrió la
	// conversación (apertura/solo_traza); `protocol`/`soportada` son la versión de protocolo del
	// aviso y la más alta que este binario lee (protocolo/version_desconocida). Ninguno es PII: el
	// identificador del paciente y las variables de la plantilla NO pasan por aquí (van
	// enmascarados solo en el WAL). Sin estas claves el attr se descartaba en silencio.
	"plantilla": true, "protocol": true, "soportada": true,
	// Escalación por Néctar. "motivo" es vocabulario cerrado del bot: por qué una escalación quedó
	// encolada para el próximo día hábil (margen / fuera_de_horario) o, en el desvío
	// (escalation_no_agents), apertura_lejana. "gate" es cuál freno cortó la escalación (no_agents
	// / out_of_hours): se emitía ya y se descartaba en silencio. "team_id" es el equipo de Néctar
	// sugerido y "grupo" el grupo de CUPS que lo decidió (imagenes / neuro / callcenter). Nada PII.
	"motivo": true, "gate": true, "team_id": true, "grupo": true,
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
