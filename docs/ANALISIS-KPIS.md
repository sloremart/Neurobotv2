# Análisis profundo de KPIs — bot + dashboard

Fecha: 2026-06-29. Método: análisis multi-agente end-to-end (captura → almacenamiento → query → endpoint → UI) con verificación adversarial por área + revisión transversal.

**Cobertura:** 71 KPIs analizados en 10 áreas; 46 con algún problema.


## 0. Estado de remediación (2026-06-29)

Resultado tras los lotes de fixes (quick wins + estructurales). Mapeo contra las 8 prioridades del §1:

| # Prioridad | Estado | Detalle |
|---|---|---|
| 1. Unificar los dos motores de KPI | 🟡 Parcial | Eliminado el motor **muerto** del bot (`/api/internal/kpis/*` + GetDailyKPIs/breakdowns/health + structs + tests, −862 líneas). `GetFunnel` se conserva (lo usa la conversión real). La doble `GetFunnel` (bot vs dashboard) no se puede dedupe sin re-arquitectura: ambos tienen consumidores. |
| 2. `waiting_list` 'scheduled' | ✅ Corregido | `createAppointmentHandler` marca `scheduled` (+`resolved_at`) al agendar desde la lista. |
| 3. Mismatches de clave (charts vacíos) | ✅ Corregido | `appointment_breakdown` y `top_entities` ahora coinciden con la UI. |
| 4. `no_response` inflado ~3× | ✅ Corregido | Se recuenta por cita distinta (`COUNT(DISTINCT appointment_id)`). |
| 5. Embudo: orden + filtro 'agendar' | 🟡 Parcial | Reordenado a secuencia monótona (sin `identified_patients`). Falta filtrar `menu_selected` por `option='agendar'` (hoy relabelado "Eligió en el menú"). |
| 6. Ventanas de fecha heterogéneas | 🟡 Parcial | Selector de fecha (default ayer) + ChartCards rotuladas (día vs 30d). Falta homogeneización total. |
| 7. `session_started` proactivas / abandono | 🟡 Parcial | `session_started` ✅ emitido en sesiones proactivas; `escalation_expired` ✅ visible como abandono. Falta mapear `waiting_list_auto_joined`. |
| 8. `db_latency` invertido / Salud real | 🟡 Parcial | `db_latency<0` → "BD caída" ✅. Falta que Salud consuma `/health` del bot (estado de SIESA). |

**Otros corregidos:** ✅ tendencias de 30 días ahora se renderizan (5 páginas); ✅ conciliación incluye consultas (`citas_procedimientos_asuntos`); ✅ embudo con drops acotados; ✅ ventana de efectividad de lista de espera unificada a `CURDATE`; ✅ embudo "Eligió agendar" filtra `option='agendar'`; ✅ **TTFR (1ª respuesta del agente)** vía `first_agent_msg_at` (migración 030); ✅ **donut de Sesiones por status** (excluyente, suma al total); + los 26 hallazgos de la auditoría de código (ver `neuro-dashboard/AUDITORIA-DASHBOARD.md`).

**Corregidos (2ª tanda — "todo lo conocido"):** ✅ cierre por inactividad ahora marca `status='abandoned'` (no 'completed') → donut/breakdown por status fiel; ✅ queries restantes sargables (`GetNotificationBreakdown`/`GetAppointmentBreakdown`/`GetSessionsByHour`/`GetTopEscalationStates`); ✅ `avg_session_duration` solo sobre `session_completed` (sin el padding del timeout de inactividad); ✅ efectividad de lista de espera excluye `duplicate_found` del total.

**No aplica / deferido con razón:**
- `ocr_attempts`: revisado, `attempts = success + failed` ya es correcto.
- `waiting_list_auto_joined`: no se mapea porque ese KPI no se muestra; los auto-inscritos ya cuentan en la efectividad (que lee la tabla, no el evento).
- Dedupe de las 2 `GetFunnel`: están en servicios distintos (bot vs dashboard) con consumidores distintos; no se pueden unificar sin acoplarlos por HTTP. Intencional.

**Residual (definicional/inherente, no son bugs):** discrepancia de conversión real puede salir negativa (DISTINCT sesiones vs filas-cita) — es informativa; reagendamientos mezclan 1/operación admin vs 1/paciente self-service en un COUNT; `appointment_breakdown`/`top_cups` subcuentan citas multi-CUPS (un `cups_code` por sesión en el evento). Requieren redefinir el KPI, no corregir código.


## 1. Resumen ejecutivo

### Prioridades (lo más importante)
- 1. [ALTA - estructural] Unificar los DOS motores de KPI (bot event_repo.go vs dashboard kpi/repository.go) en una única fuente de verdad. Ya divergen en mapeo de eventos y en ventanas de fecha (sargable vs DATE()=?); es la causa raíz de casi todas las inconsistencias y del drift futuro. También unificar las 4 definiciones de health.
- 2. [ALTA - credibilidad] Arreglar waiting_list status='scheduled': el booking nunca lo escribe (slots.go:1352 solo emite evento), así que efectividad, tiempo-a-agendar y conversión-por-CUPS son ~0% con datos reales y solo se ven bien por el seeder. Marcar la columna en el booking (UpdateStatus ya pone resolved_at).
- 3. [ALTA - bug de UI] Corregir los mismatches de clave que dejan gráficos SIEMPRE vacíos: 'breakdown'→'appointment_breakdown' (handlers.go:142 vs Appointments.tsx:24) y 'entities'→'top_entities' (handlers.go:396 vs Patients.tsx:25). Dos charts principales nunca renderizan datos.
- 4. [ALTA - métrica falsa] Eliminar el doble/triple conteo de proactives_no_response (notification_timeout se emite hasta 3x por followups y mezcla tipos, confirmation.go:184-237). Puede superar a 'Enviadas' y distorsiona el donut principal de Notificaciones. Derivarlo como sent_confirmation-confirmed-cancelled-reprogramados o COUNT(DISTINCT appointment_id) del timeout terminal.
- 5. [ALTA - embudo roto] Corregir el ORDEN de pasos del embudo (identified_patients está en posición 2 pero patient_identified se emite al final, tras patient_found/document_entered → el embudo CRECE entre pasos) y filtrar 'Eligió agendar' por menu_selected option='agendar' (hoy cuenta toda opción de menú). FunnelSteps.tsx solo dibuja caídas, así que se ve roto.
- 6. [MEDIA-ALTA - sistémico] Homogeneizar o ETIQUETAR las ventanas temporales en cada ChartCard/StatCard (1d vs 7d vs 30d coexisten sin marca en casi todas las páginas) y migrar todos los DATE(created_at)=? restantes al rango half-open sargable ya adoptado.
- 7. [MEDIA - denominadores cross-cutting] Emitir session_started también en las sesiones creadas proactivamente (confirmation/self_reschedule/waiting_list) para que total_sessions deje de subcontar y la Conversión no pueda superar 100%; y contar escalation_expired/waiting_list_auto_joined donde corresponde para no subreportar abandono y enrolamientos.
- 8. [MEDIA - riesgo operativo] Arreglar la inversión de señal de db_latency (-1 = BD caída cae en rama 'success' verde, Health.tsx:31) y hacer que la página Salud consuma el /health del bot (status/local_db/external_db SIESA). Hoy con la BD clínica caída la página muestra todo en verde.

### KPIs faltantes (de alto valor, no existen)
- TENDENCIAS GLOBALES: ningún gráfico de tendencia de 30 días se pinta en NINGUNA página, pese a que casi todos los endpoints ya las calculan y devuelven (sessions handlers.go:105-107, appointments :136-138, notifications :158-160, ivr :189-191, ocr :361-363, patients :391). El gráfico más útil de cada área (evolución diaria) está pago y se descarta.
- TASAS CON DENOMINADOR: el dashboard es casi todo conteos absolutos. Faltan KPIs de tasa: % búsquedas sin slots, ratio fallos/creadas, tasa de respuesta global del recordatorio (confirmadas+canceladas+reprogramadas)/enviadas, tasa de éxito de handoff (escalation_failed/(failed+escalated)). La tasa de éxito OCR existe pero con denominador mal definido.
- ABANDONO REAL: escalation_expired (cierre de chat escalado por silencio del paciente, manager.go:354 con MarkAbandoned) no se cuenta como abandono en ningún KPI ni se muestra en Sesiones; el abandono real queda subreportado. Tampoco se expone 'en curso/still_open' aunque se calcula (repository.go:573).
- WAITING LIST 'scheduled' INEXISTENTE: el booking nunca llama UpdateStatus(wlID,'scheduled') (slo emite el evento en slots.go:1352); falta un KPI real de 'agendados desde lista de espera' atado a la columna. Hoy la efectividad, tiempo-a-agendar y conversión-por-CUPS son ~0% con datos reales.
- OCUPACIÓN PASADA/REALIZADA: solo existe ocupación futura; no hay KPI de cuántos slots agendados terminaron en atención.
- CONCILIACIÓN DE CONSULTAS: la conciliación bot↔cups_medico solo cubre citas_procedimientos (procedimientos/imágenes); las CONSULTAS viven en citas_procedimientos_asuntos y quedan fuera (~2843 citas invisibles al KPI).
- SALUD DEL BOT Y SIESA: la página Salud no refleja salud del bot ni de SIESA; el bot ya expone /health robusto (cmd/server/main.go:599-629) que pinguea SIESA, pero el dashboard lee su propia MySQL. Si el bot o SIESA están caídos, la página sigue en verde.
- TTFR (primera respuesta del agente): avg_response usa last_agent_msg_at (última actividad, se sobrescribe), no la primera respuesta; falta el tiempo de PRIMERA respuesta.
- ENROLAMIENTOS SUBCONTADOS: waiting_list_auto_joined (slots.go:702,712) no se mapea; solo se cuenta waiting_list_joined. También falta emitir waiting_list_joined en la ruta particular sin orden (medical_order.go:88-122).

### Redundantes / duplicados
- DOS MOTORES DE KPI: bot internal/repository/local/event_repo.go (mapEventToKPI :210-278) y dashboard internal/kpi/repository.go (mapEventToKPI :68-146) consultan el mismo chat_events con queries casi idénticas. YA DIVERGEN: el dashboard mapea slot_taken/booking_failed/booking_timeout/escalation_failed y el bot se detiene en no_slots_found. Fuente #1 de drift.
- DOS GetFunnel: bot event_repo.go:416 (rango half-open) vs dashboard repository.go:217 (BETWEEN ...23:59:59). Lógica duplicada con manejo de fecha distinto sobre el mismo neuro_bot.
- DOS GetAppointmentBreakdown: bot (rango sargable [date,date+1d)) vs dashboard repository.go (DATE(created_at)=?). Ya difieren en ventana e índice.
- MÚLTIPLES DEFINICIONES DE HEALTH: GetHealth del dashboard (repository.go:751) + copia en bot event_repo.go:517 + /health del bot + endpoint interno /api/internal/kpis/health (HandleHealthKPIs). Cuatro nociones de 'salud' sin unificar.
- no_slots MEDIDO DOS VECES: flow_events (fuga del embudo, slots.go:409) vs chat_events no_slots_found/slot_taken (tarjeta session-outcomes, repository.go:538). Dos fuentes para el mismo concepto, riesgo de divergencia entre secciones.
- TRENDS Y BREAKDOWNS CALCULADOS Y NO CONSUMIDOS: /api/sessions trends, /api/overview (notification_breakdown, appointment_breakdown, top_escalations), /api/appointments trends, /api/notifications + /api/ivr trends, /api/pending stats (response_rate), /api/ocr trends, /api/patients trends, EscalationSLA Resolved/Expired/StillOpen — todos se calculan en cada request y el frontend los ignora. Trabajo de query desperdiciado de forma sistemática.
- DOBLE CONTEO POR EVENTOS GEMELOS: existing_appointment_found + appointment_exists_blocked siempre se emiten juntos (medical_validation.go:501-517) y el SQL los suma (repository.go:742). Igual patrón conceptual en el donut de sesiones (una sesión escalada-y-reanudada cuenta en 'Escaladas' y 'Completadas').
- proactives_no_response INFLADO ~3x: handleTimeout emite notification_timeout en cada reintento (followup1/2/escalación, confirmation.go:184-237) y el dashboard hace COUNT(*) (repository.go:105). Redundancia de eventos que rompe el denominador.
- reschedule_self_service y campos muertos: declarados en interfaces Resp del frontend (Appointments.tsx:22, EscalationSLA, pending.stats) pero nunca renderizados.

### Inconsistencias de definición
- VENTANAS DE FECHA HETEROGÉNEAS EN LA MISMA PÁGINA, sin etiquetar: StatCards 1 día vs breakdown/top_cups/top_entities 30 días vs embudo 7 días vs SLA 30 días. Pervasivo en Appointments, Patients, OCR, Conversion (tres StatCards 'Sesiones' con 30d/30d/7d) y Escalation (7d/today/30d). El usuario compara cifras no comparables.
- SARGABLE vs NO-SARGABLE: el mismo repo adoptó el rango half-open [date,date+1d) en unos sitios (repository.go:31-34) pero usa DATE(created_at)=? en GetAppointmentBreakdown (:185), GetSessionsByHour (:608), notification_breakdown (:154) y GetFunnel del dashboard. Inconsistencia interna y respecto al bot (que ya migró).
- ESTADO 'completed' AMBIGUO: el cierre por inactividad setea StatusCompleted (manager.go:293) aunque es conceptualmente abandono; abandoned_sessions proviene de un evento cuya fila tiene status='completed'. Riesgo latente para cualquier KPI basado en status.
- UNIDADES DISTINTAS EN EL MISMO KPI: (a) conversión real discrepancy = COUNT(DISTINCT session) bot vs COUNT(*) filas citas SIESA → discrepancia negativa sin pérdida real; (b) reagendamientos consolidados mezcla 1 evento/operación admin vs 1/paciente self-service en un COUNT(*); (c) conciliación cuenta filas cita-CUPS y las etiqueta 'citas'; (d) appointment_breakdown/top_cups subcuentan citas multi-CUPS (un cups_code por sesión).
- MISMO CONCEPTO, DOS MÉTODOS: no_response se DERIVA (sent-confirmed-cancelled) en GetChannelEffectiveness (repository.go:391, olvida restar reprogramados) pero se CUENTA crudo en proactives_no_response (timeouts). Resultados divergentes para 'sin respuesta'.
- DONUTS QUE NO SUMAN AL TOTAL: Sesiones (completed/abandoned/escalated son conteos de evento independientes vs total_sessions=session_started; faltan activas, cross-day, escalation_expired; doble conteo) y Waiting List (duplicate_found entra en el denominador total pero no en ningún bucket). Los porcentajes engañan.
- SESIONES PROACTIVAS SIN session_started: notificaciones (confirmation.go:399, self_reschedule.go:112, waiting_list.go:35) crean sesión sin emitir session_started, pero sí emiten appointment_created/session_completed. Denominador subcontado → la Conversión del Overview puede superar 100%.
- DEPENDENCIA SILENCIOSA DE SIESAAssignUserCedula: conversión real, bot-share y conciliación devuelven 0% cuando la cédula del bot no está configurada, indistinguible de un 0 real. UI lo pinta como pérdida total/danger.
- ASIMETRÍAS DE PROMEDIO: avg_session_duration mezcla duración real con padding de inactividad (+20min) y excluye escaladas; avg_return cuenta cierres 'completed' pero el promedio los excluye (solo resumed_at). Mismo concepto medido distinto en numerador y denominador.
- DESAJUSTES DE ETIQUETA vs DATO: 'Recordatorio' rotula el tipo reschedule (Notifications.tsx:90); buckets de tiempo UI 5-15/15-60 vs SQL 5-14/15-59; STEP_LABEL de escalación usa 'escalation_closed/ended' (chat_events) en vez de los steps reales de flow_events 'agent_resumed/agent_closed', perdiendo devueltas/cerradas.


---

## 2. Detalle por área (KPI por KPI)


### Área: Sesiones (Overview.tsx + Sessions.tsx)

#### [MEDIA] session_started / total_sessions
- **Captura:** Bot emite session_started SOLO para sesiones entrantes nuevas: internal/worker/pool.go:388 (if isNew). Las sesiones creadas proactivamente NO emiten session_started: internal/notifications/confirmation.go:399, internal/notifications/self_reschedule.go:112, internal/notifications/waiting_list.go:35 crean session.Session con StatusActive directamente y nunca llaman Tracker para session_started (grep en internal/notifications = 0 coincidencias).
- **Almacenamiento:** chat_events.event_type='session_started' (migrations/003_create_chat_events.up.sql). Correcto.
- **Query/agregación:** dashboard repository.go:32-47 GROUP BY event_type con rango sargable [date,date+1d). mapEventToKPI repository.go:70-71 -> TotalSessions. Correcto.
- **Endpoint:** GET /api/sessions y /api/overview exponen kpis.total_sessions (handlers.go:101, :26). OK.
- **UI:** StatCard 'Total' (Sessions.tsx:49) y 'Sesiones' (Overview.tsx:59). OK visualmente.
- **Problemas:**
  - total_sessions subcuenta: excluye las sesiones iniciadas por notificaciones proactivas (confirmacion/reagendamiento/lista de espera), que sí pueden emitir session_completed/appointment_created. Esto descuadra el denominador.
  - Consecuencia: la Conversión de Overview (appointments_created/total_sessions, Overview.tsx:39) puede pasar de 100% porque el numerador incluye citas de sesiones proactivas que el denominador no cuenta.
  - total_sessions nunca cuadra con la suma del donut (ver KPI distribución).
- **Recomendación:** Emitir session_started (o un event_type distinto 'session_started_proactive') al crear sesiones en internal/notifications/*, o documentar que total_sessions = solo conversaciones entrantes y cambiar la etiqueta a 'Sesiones entrantes'. Definir el denominador de Conversión de forma consistente con las citas que se cuentan.
- **Verificado (real, medium):** CONFIRMED. session_started emitted only in internal/worker/pool.go:388 on isNew inbound sessions. Proactive flows create sessions via sessionRepo.Create without that event (internal/notifications/waiting_list.go:80; same pattern in self_reschedule). appointments_created in GetDailyKPIs (kpi/repository.go:78) counts ALL appointment_created events including proactive ones, while TotalSessions maps f

#### [MEDIA] session_completed / completed_sessions
- **Captura:** Dos rutas distintas: (1) fin normal en terminatedHandler internal/statemachine/handlers/post_action.go:147 emite session_completed y pone sess.Status=Completed; (2) cierre por inactividad internal/session/manager.go:293 hace UpdateStatus(StatusCompleted) PERO emite el evento session_closed_inactivity (no session_completed). Bien separado a nivel de evento.
- **Almacenamiento:** chat_events event_type. Pero sessions.status='completed' agrupa AMBOS casos (fin real + cerrada por inactividad) -> la columna status NO distingue completada de abandonada.
- **Query/agregación:** repository.go:72-73 -> CompletedSessions = count(session_completed). Correcto a nivel de evento.
- **Endpoint:** /api/sessions, /api/overview kpis.completed_sessions. OK.
- **UI:** StatCard 'Completadas' + slice donut (Sessions.tsx:37,50).
- **Problemas:**
  - sessions.status='completed' es ambiguo: incluye sesiones cerradas por inactividad (manager.go:293) que conceptualmente son abandonadas. Cualquier KPI futuro basado en status (no en eventos) contaría mal. Latente.
  - Sesiones proactivas que llegan a TERMINATED emiten session_completed sin haber emitido session_started -> el donut puede tener 'Completadas' sin su correspondiente en 'Total'.
- **Recomendación:** En el cierre por inactividad usar un status propio (p.ej. 'abandoned' o nuevo 'closed_inactivity') en vez de 'completed', para que sessions.status sea fuente de verdad consistente con los eventos.
- **Verificado (real, medium):** CONFIRMED both parts. Inactivity close sets StatusCompleted at internal/session/manager.go:293. session_completed event is emitted in TERMINATED handler (internal/statemachine/handlers/post_action.go:147), reachable by proactive sessions that never emitted session_started. Note current completed_sessions KPI is event-based (kpi/repository.go:72), so the status ambiguity is genuinely latent, but fa

#### [MEDIA] session_closed_inactivity / abandoned_sessions
- **Captura:** internal/session/manager.go:301 emite session_closed_inactivity solo si elapsedMin>=CloseMin Y s.Reminders>=1, y solo sobre sesiones status='active' (FindInactiveSessions, session_repo.go:300). Las escaladas se manejan aparte.
- **Almacenamiento:** chat_events event_type='session_closed_inactivity'. OK.
- **Query/agregación:** repository.go:74-75 -> AbandonedSessions. OK.
- **Endpoint:** kpis.abandoned_sessions. OK.
- **UI:** StatCard 'Abandonadas' (intent warning) + slice donut.
- **Problemas:**
  - GAP: las sesiones ESCALADAS abandonadas por silencio del paciente emiten 'escalation_expired' y marcan status=abandoned (manager.go:344,354), NO se cuentan en abandoned_sessions ni se muestran en ninguna parte de la página Sesiones. abandoned_sessions subcuenta el abandono real.
  - Inconsistencia de nombres: 'abandoned_sessions' proviene de un evento cuyo status efectivo en la tabla es 'completed' (manager.go:293).
- **Recomendación:** Incluir/visualizar escalation_expired como parte del abandono (o como categoría propia 'Escaladas sin atender'). Alinear naming evento/status.
- **Verificado (real, medium):** CONFIRMED. escalation_expired logged at internal/session/manager.go:354 with MarkAbandoned (line 344). mapEventToKPI (kpi/repository.go:68-146) maps abandoned_sessions ONLY from session_closed_inactivity (line 74); escalation_expired is mapped to NOTHING. Sessions page (Sessions.tsx) never displays escalation_expired. Meanwhile session_closed_inactivity corresponds to status=StatusCompleted (manag

#### [MEDIA] avg_session_duration_min
- **Captura:** Derivado de eventos, no capturado directamente.
- **Almacenamiento:** chat_events created_at.
- **Query/agregación:** repository.go:53-61 AVG(TIMESTAMPDIFF(MINUTE, (SELECT MIN(ce2.created_at) por session_id), ce.created_at)) para event_type IN ('session_completed','session_closed_inactivity') en el rango del día. Bot duplica la misma lógica en internal/repository/local/event_repo.go:285-292.
- **Endpoint:** kpis.avg_session_duration_min en /api/sessions. OK.
- **UI:** StatCard 'Duración prom.' toFixed(0) min (Sessions.tsx:53).
- **Problemas:**
  - Mezcla duraciones reales (session_completed) con sesiones cerradas por inactividad, cuyo cierre ocurre ~CloseMin+ReminderMin DESPUÉS del último mensaje real (el seed lo confirma: cmd/seed-kpis/main.go:473 cierra a +20min). Esto INFLA la duración media con el tiempo de timeout de inactividad.
  - Excluye por completo las sesiones escaladas (terminan en escalation_expired, no en los dos event_type del query) -> la media solo refleja autoservicio.
  - La UI no indica que el valor incluye el padding de inactividad.
- **Recomendación:** Calcular duración hasta el ÚLTIMO mensaje real (p.ej. último message_sent/mensaje entrante) en vez del evento de cierre por inactividad, o separar 'duración completadas' vs 'tiempo hasta abandono'.
- **Verificado (real, medium):** CONFIRMED. Query at kpi/repository.go:53-59 averages TIMESTAMPDIFF from session MIN(created_at) to the close event, with event_type IN ('session_completed','session_closed_inactivity') only. Inactivity close timestamp is offset (seed cmd/seed-kpis/main.go:473 = +20min). escalation_expired is excluded from the IN-list, so escalated sessions are not counted. Sessions.tsx:53 shows the raw value with 

#### [MEDIA] distribución de estados (donut)
- **Captura:** Compuesto de 3 conteos de eventos independientes (Sessions.tsx:36-40): completed_sessions, abandoned_sessions, escalated_sessions.
- **Almacenamiento:** -
- **Query/agregación:** Tres counts por separado de mapEventToKPI; no hay agregación por sesión.
- **Endpoint:** Reusa kpis del /api/sessions.
- **UI:** DonutChart (charts.tsx:95) con leyenda + tooltip de valor crudo; sin etiquetas de porcentaje ni denominador.
- **Problemas:**
  - Las 3 porciones NO suman total_sessions: faltan las sesiones activas/en curso y las cerradas fuera del día. El donut implica una partición del total que no es tal.
  - Posible doble conteo (escalada+completada tras resume) y porciones de sesiones proactivas sin session_started.
  - No muestra denominador ni 'Activas', puede inducir a leer las proporciones como % del total de sesiones.
- **Recomendación:** Construir la distribución por sesión (una fila por session_id, estado final mutuamente excluyente) al estilo de GetSessionOutcomes (repository.go:520), añadir porción 'Activas/En curso' y mostrar el total como denominador.
- **Verificado (real, medium):** CONFIRMED. Sessions.tsx:36-40 builds the donut from independent event counts (completed_sessions, abandoned_sessions, escalated_sessions) while Total card uses total_sessions (session_started). These cannot reconcile (active sessions, cross-day sessions, excluded escalation_expired, proactive sessions, double counting). No 'Activas' slice and no denominator displayed. The per-session GetSessionOut

#### [BAJA] escalated_to_agent / escalated_sessions
- **Captura:** Emisión única por escalación en internal/statemachine/handlers/escalation.go:155 con event_data.from_state=preState. Correcto.
- **Almacenamiento:** chat_events; from_state viaja en event_data (no en columna state_from, que queda NULL). El query lo lee del JSON correctamente.
- **Query/agregación:** repository.go:76-77 count(escalated_to_agent). Top estados: GetTopEscalationStates repository.go:638 lee JSON_EXTRACT(event_data,'$.from_state') con fallback 'unknown' -> coincide con el emisor. Correcto.
- **Endpoint:** /api/sessions kpis.escalated_sessions + escalations (top 10). /api/overview top 5. OK.
- **UI:** StatCard 'Escaladas' + slice donut + tarjeta 'Top estados al escalar' (Sessions.tsx:66) con replace(/_/g,' '). Bien.
- **Problemas:**
  - Solapamiento en el donut: una sesión que escala, el agente devuelve con /bot (resume) y luego termina, cuenta en 'Escaladas' (escalated_to_agent) Y en 'Completadas' (session_completed) -> doble conteo en el donut. Caso borde pero real.
  - La página Sesiones no muestra el desenlace de las escalaciones (atendida/expirada); eso vive en otra página (SLA).
- **Recomendación:** Para el donut, derivar el estado final por sesión (una fila por session_id) en vez de contar eventos independientes, para garantizar exclusividad.
- **Verificado (real, low):** CONFIRMED. ResumeFromEscalation sets StatusActive (internal/session/manager.go:219), after which the session can reach TERMINATED and emit session_completed (post_action.go:147), while escalated_to_agent was already logged. Sessions.tsx donut counts completed_sessions and escalated_sessions as independent event counts (lines 37-39), so the same session_id can land in two slices. Escalation outcome

#### [BAJA] sesiones por hora
- **Captura:** Hereda el gap de session_started (no incluye sesiones proactivas).
- **Almacenamiento:** chat_events.
- **Query/agregación:** GetSessionsByHour repository.go:606-610 usa HOUR(created_at) con DATE(created_at)=? (NO sargable, inconsistente con el resto que ya migró a rango [date,date+1d)). Rellena 24 buckets. Correcto funcionalmente.
- **Endpoint:** /api/sessions by_hour. OK.
- **UI:** BarsChart 24 barras (Sessions.tsx:62). OK, aunque cada barra usa un color distinto color(i) -> ruido visual; sin etiquetas de valor.
- **Problemas:**
  - DATE(created_at)=? no usa el índice de created_at (inconsistente con el patrón sargable adoptado en el resto del repo).
  - Sensible a zona horaria del servidor MySQL (HOUR usa tz del servidor).
- **Recomendación:** Cambiar a rango sargable como las demás queries. Color único para todas las barras y mostrar valor.
- **Verificado (real, low):** CONFIRMED. GetSessionsByHour at kpi/repository.go:608-609 uses WHERE event_type='session_started' AND DATE(created_at)=? and GROUP BY HOUR(created_at). The repo explicitly adopted the sargable [date,date+1day) pattern elsewhere (kpi/repository.go:31-34 comment), so this is inconsistent. HOUR(created_at) buckets by stored time = server tz. Factual claims real; color/value rec is cosmetic.

**Notas del área:**
- _Faltantes:_ escalation_expired (abandono de chats escalados por silencio del paciente, manager.go:354) no se cuenta en ningún KPI ni se muestra en la página Sesiones: el abandono real queda subreportado.; session_started no se emite para sesiones creadas proactivamente (internal/notifications/confirmation.go:399, self_reschedule.go:112, waiting_list.go:35) -> total_sessions y sesiones-por-hora subcuentan.; La página Sesiones no muestra la tendencia de 30 días aunque el endpoint /api/sessions YA la devuelve (handlers.go:105-107: session_started/completed/closed_inactivity/escalated). Falta un TrendChart.; No se muestra el número de sesiones activas/en curso en la página Sesiones (existe en /api/health GetHealth, repository.go:753, pero no se surface aquí).; avg_session_duration no separa sesiones completadas (duración real) de abandonadas (duración + timeout de inactividad).
- _Redundantes:_ /api/sessions calcula y devuelve 'trends' (30 días, 4 series) pero Sessions.tsx no los usa (su interface Resp no incluye trends) -> trabajo de query desperdiciado en cada request.; /api/overview devuelve notification_breakdown, appointment_breakdown y top_escalations (handlers.go:33-35) que Overview.tsx ignora -> sobre-fetch.; Lógica de mapeo de KPIs duplicada: dashboard internal/kpi/repository.go mapEventToKPI y bot internal/repository/local/event_repo.go (líneas 210-278). Ya divergen: el dashboard mapea slot_taken/booking_failed/booking_timeout/escalation_failed; el bot se detiene en no_slots_found. Riesgo de drift entre ambas fuentes.
- _Mejoras UI:_ Donut 'Distribución de estados': añadir porción 'Activas/En curso', mostrar total como denominador y construirlo por sesión (estado final excluyente) para evitar doble conteo y que las proporciones no engañen.; Añadir el TrendChart de 30 días (datos ya disponibles en el endpoint) a Sessions.tsx para ver evolución started/completed/abandoned/escaladas.; StatCard 'Duración prom.': aclarar en el hint que incluye el tiempo de timeout de inactividad para sesiones abandonadas, o separarla.; Sesiones por hora: usar un único color de barra y mostrar etiquetas de valor; migrar el query a rango sargable.; Mostrar las escaladas-no-atendidas (escalation_expired) como categoría visible para reflejar el abandono real.


### Área: agendamiento-bot (Appointments.tsx — "Agendamiento según el bot")

#### [ALTA] appointment_breakdown.by_service (Citas por especialidad)
- **Captura:** appointment_created lleva service_type=currentGroup.ServiceType (slots.go:1341). Se captura, pero by_cups/by_service solo registran UN cups_code/service por evento aunque la cita agrupe varios CUPS (cups_codes=len(procedures) se ignora en la agregación). Subcuenta en citas multi-CUPS.
- **Almacenamiento:** event_data.service_type / cups_code. OK.
- **Query/agregación:** GetAppointmentBreakdown repository.go:182-213 usa DATE(created_at)=? (NO sargable) y solo del 'date' único, no del rango from/to.
- **Endpoint:** BUG: handlers.go:142 devuelve la clave 'breakdown', pero el frontend lee 'appointment_breakdown' (Appointments.tsx:24,37). api.get (lib/api.ts) no desenvuelve nada, así que data.appointment_breakdown es siempre undefined.
- **UI:** Resultado: el chart 'Citas por especialidad' (BarsChart) NUNCA recibe datos y siempre cae a <EmptyState>. Fallo silencioso. (En Overview.tsx el mismo dato SÍ funciona porque handlers.go:45 usa la clave correcta 'appointment_breakdown').
- **Problemas:**
  - Desajuste de clave endpoint('breakdown') vs frontend('appointment_breakdown') → chart 'Citas por especialidad' siempre vacío.
  - by_service/by_cups subcuentan citas con múltiples CUPS (solo cuentan el cups_code de sesión).
  - Query por 'date' único, inconsistente con el resto de la página que ya recibe from/to.
- **Recomendación:** Renombrar la clave a 'appointment_breakdown' en handlers.go:142 (o ajustar el frontend). Considerar agregar por rango from/to y desglosar todos los CUPS de la cita (no solo cups_code de sesión).
- **Verificado (real, high):** CONFIRMADO en tres puntos. (1) MISMATCH DE CLAVE: el handler Appointments devuelve la clave 'breakdown' (internal/kpi/handlers.go:142), pero el frontend lee data?.appointment_breakdown (Appointments.tsx:24 y :37); como appointment_breakdown nunca llega, byService=[] y el ChartCard 'Citas por especialidad' renderiza siempre <EmptyState/> (Appointments.tsx:71-72). Nota: el endpoint Overview SÍ usa l

#### [MEDIA] appointments_confirmed (Confirmadas)
- **Captura:** Se emite SOLO en el flujo "mis_citas" cuando el paciente confirma una cita YA EXISTENTE: internal/statemachine/handlers/appointments.go:921. NO se emite al crear la cita. Es decir, NO es "de las creadas, cuántas se confirmaron".
- **Almacenamiento:** chat_events event_type='appointment_confirmed', event_data={appointment_id, block_size}. OK.
- **Query/agregación:** repository.go:80-81 cuenta COUNT(*) del día. Correcto para lo que mide, pero el nombre y la colocación inducen a leerlo como subconjunto de las creadas.
- **Endpoint:** kpis.appointments_confirmed. OK técnicamente.
- **UI:** StatCard "Confirmadas" puesto inmediatamente a la derecha de "Creadas" (Appointments.tsx:58), sugiriendo una relación de embudo creadas→confirmadas que NO existe: son confirmaciones de citas preexistentes vía menú "mis citas", independientes de las creadas ese día. Además NO es la confirmación proactiva (eso es notification_confirmed, otra página).
- **Problemas:**
  - Semántica engañosa: 'Confirmadas' = confirmaciones self-service de citas existentes vía mis_citas, no confirmación de las citas creadas por el bot.
  - Colocación junto a 'Creadas' insinúa un funnel inexistente.
- **Recomendación:** Renombrar a 'Confirmadas (autogestión)' o moverla fuera del bloque de outcomes de creación, con hint explicando que proviene del flujo 'mis citas' y no es subconjunto de 'Creadas'.
- **Verificado (real, medium):** CONFIRMADO. El evento appointment_confirmed se emite SOLO en executeConfirmAppointment del flujo 'mis_citas' (internal/statemachine/handlers/appointments.go:921, observability flow='mis_citas' action='appt_confirmed'), que confirma una cita YA existente seleccionada por el paciente; no se emite en el flujo de creación (slots.go solo emite appointment_created). En el frontend (frontend/src/pages/Ap

#### [MEDIA] appointments_cancelled (Canceladas)
- **Captura:** Emitido en executeCancelAppointment, appointments.go:972, tras CancelBlock exitoso en el flujo mis_citas (cancelación self-service). NO cubre cancelaciones por notificación proactiva (notification_cancel_confirmed) ni admin.
- **Almacenamiento:** chat_events event_type='appointment_cancelled', event_data={appointment_id, block_size}. OK.
- **Query/agregación:** repository.go:82-83 COUNT(*) del día. Correcto.
- **Endpoint:** kpis.appointments_cancelled. OK.
- **UI:** StatCard "Canceladas" (intent=danger) junto a Creadas/Confirmadas. Igual que confirmadas, induce a leerlo como % de las creadas que se cancelaron, cuando es cancelación self-service de citas existentes.
- **Problemas:**
  - Mismo problema de semántica que appointments_confirmed: son cancelaciones self-service de citas previas, no de las creadas hoy.
  - intent=danger puede alarmar: una cancelación self-service correcta no es necesariamente negativa.
- **Recomendación:** Etiquetar 'Canceladas (autogestión)' con hint, y considerar intent neutro/warning en vez de danger.
- **Verificado (real, medium):** CONFIRMADO. appointment_cancelled se emite solo en executeCancelAppointment del flujo 'mis_citas' (appointments.go:972, observability action='appt_cancelled'), cancelando una cita existente a petición del paciente. En frontend/src/pages/Appointments.tsx:59 se pinta con intent='danger' y sin hint. Es una cancelación de autogestión, no de las citas creadas el día consultado. Recomendación válida: et

#### [MEDIA] top_cups (Top CUPS agendados)
- **Captura:** Deriva de appointment_created.cups_code/cups_name. Misma limitación: 1 CUPS por evento aunque la cita tenga varios procedimientos → subcuenta multi-CUPS.
- **Almacenamiento:** event_data.cups_code/cups_name. OK.
- **Query/agregación:** GetTopCups repository.go:660-686, BETWEEN from 00:00:00 AND to 23:59:59, GROUP BY code,name ORDER BY cnt DESC LIMIT 10. Correcto. Ventana = from=daysAgo(30)..to=date (30 días).
- **Endpoint:** handlers.go:134 top_cups con from/to. Clave 'top_cups' coincide con el frontend. OK.
- **UI:** HBarsChart muestra label=code (Appointments.tsx:41); el nombre del CUPS no se muestra (solo el código). INCONSISTENCIA DE VENTANA: top_cups usa 30 días mientras StatCards y 'Citas por especialidad' usan 1 día (yesterday), sin etiqueta que lo indique → induce a comparar manzanas con peras.
- **Problemas:**
  - Ventana de 30 días en este chart vs 1 día en el resto de la página, sin indicación visual.
  - Etiqueta solo el código CUPS; el nombre (disponible en top_cups[].name) no se aprovecha.
  - Subcuenta de citas multi-CUPS (igual que breakdown).
- **Recomendación:** Unificar la ventana (o etiquetar explícitamente 'últimos 30 días' en el título del ChartCard). Mostrar 'code - name' o name como label/tooltip.
- **Verificado (real, medium):** CONFIRMADO. (1) VENTANA INCONSISTENTE: el frontend pide /api/appointments con from=daysAgo(30) y to=date (Appointments.tsx:33); GetTopCups consulta ese rango de 30 días (repository.go:660-668), mientras los StatCards y el header ('Citas del día {date}') son de un solo día (handlers.go:132 GetDailyKPIs(date)). El ChartCard 'Top CUPS agendados' (Appointments.tsx:74) no indica que son 30 días. (2) NO

#### [BAJA] no_slots_found (Sin slots)
- **Captura:** slots.go:412 WithEvent("no_slots_found", {cups_code}) cuando len(slots)==0. Se emite CADA vez que la búsqueda devuelve vacío, por lo que una misma sesión que reintenta genera varios eventos.
- **Almacenamiento:** chat_events event_type='no_slots_found'. OK.
- **Query/agregación:** repository.go:135-136 COUNT(*) del día → cuenta eventos (intentos), no sesiones distintas.
- **Endpoint:** kpis.no_slots_found. OK.
- **UI:** StatCard "Sin slots" (warning). El número son intentos sin disponibilidad, no pacientes afectados.
- **Problemas:**
  - Cuenta intentos, no sesiones/pacientes únicos; puede sobreestimar el problema si hay reintentos en la misma sesión.
- **Recomendación:** Si se quiere medir impacto en pacientes, usar COUNT(DISTINCT session_id); o aclarar en hint que es 'intentos sin disponibilidad'. GetSessionOutcomes ya ofrece la versión por sesión (has_noslots).
- **Verificado (real, low):** CONFIRMADO. El evento no_slots_found se emite por cada búsqueda de agenda sin disponibilidad (internal/statemachine/handlers/slots.go:412, con cups_code), no por sesión. mapEventToKPI lo asigna como conteo crudo de eventos (repository.go:135-136, kpis.NoSlotsFound = count) y el StatCard 'Sin slots' (Appointments.tsx:60) lo muestra sin hint. Un mismo paciente que reintenta o consulta varios CUPS en

#### [OK] appointments_created (Creadas)
- **Captura:** Bot emite WithEvent("appointment_created") en internal/statemachine/handlers/slots.go:1336 justo tras crear la cita en SIESA y antes de pasar a StateBookingSuccess. Lleva cups_code, cups_name, service_type, cups_codes(=len), date, doctor, espacios, reschedule_from. Emitido una vez por cita creada.
- **Almacenamiento:** chat_events.event_type='appointment_created', payload en event_data JSON. Esquema en migrations/003_create_chat_events.up.sql.
- **Query/agregación:** kpi/repository.go:78-79 GetDailyKPIs mapea COUNT(*) GROUP BY event_type con rango sargable [date, date+1d). Correcto, sin doble conteo.
- **Endpoint:** neuro-dashboard handlers.go:132 /api/appointments → kpis.appointments_created (models.go:12). OK.
- **UI:** StatCard "Creadas" (Appointments.tsx:57). Muestra el conteo del día seleccionado. Correcto.
- **Recomendación:** Sin cambios. Nota menor: en reagendamiento se emite appointment_created (la cita nueva) además de cancelar la vieja; "Creadas" incluye reagendas, lo cual es correcto pero conviene documentarlo.

#### [OK] slot_taken (Horario tomado)
- **Captura:** slots.go:1443-1447 emite slot_taken cuando booking_failure_reason=='slot_taken' (colisión PK al confirmar). Emisión única por colisión.
- **Almacenamiento:** chat_events event_type='slot_taken'. OK.
- **Query/agregación:** repository.go:137-138 COUNT(*) del día. Correcto.
- **Endpoint:** kpis.slot_taken. OK.
- **UI:** StatCard "Horario tomado" con hint='colisión al confirmar' (Appointments.tsx:61). Claro y correcto.

#### [OK] booking_failed + booking_timeout (Booking falló)
- **Captura:** booking_timeout: slots.go:1453 (reason=='timeout'). booking_failed: slots.go:1459 (default, otros errores). Mutuamente excluyentes con slot_taken dentro de bookingFailedHandler. Sin huecos visibles.
- **Almacenamiento:** chat_events event_type IN ('booking_failed','booking_timeout'). OK.
- **Query/agregación:** repository.go:139-142 cuenta ambos por separado. Correcto.
- **Endpoint:** kpis.booking_failed y kpis.booking_timeout. OK.
- **UI:** StatCard "Booking falló" suma ambos (Appointments.tsx:64). Combinar es razonable; el detalle por tipo se pierde en la UI pero está en el payload.
- **Recomendación:** Opcional: tooltip que desglose failed vs timeout, ya que el payload los trae separados.

**Notas del área:**
- _Faltantes:_ No hay gráfico de tendencia pese a que el endpoint /api/appointments YA devuelve trends de 30 días para appointment_created/confirmed/cancelled (handlers.go:136-138); el frontend lo descarta. Una línea de tendencia de citas creadas sería el gráfico más informativo de esta página y ya está pago el costo de la query.; No se expone ninguna tasa: p.ej. % de búsquedas que terminan sin slots, o ratio fallos/creadas. Todo son conteos absolutos sin denominador.; Falta el desglose booking_failed vs booking_timeout en UI (se colapsan en 'Booking falló').; appointment_confirmed/cancelled no distinguen origen (self-service mis_citas vs proactivo vs admin); aquí solo se ve el self-service y no se aclara.
- _Redundantes:_ Doble implementación de los mismos KPIs: el bot (internal/repository/local/event_repo.go GetAppointmentBreakdown/GetDailyKPIs, expuesto en internal/api/internal_handler.go) y el dashboard (internal/kpi/repository.go) consultan el mismo chat_events con queries casi idénticas. Riesgo de drift (ya difieren: el bot usa rango sargable [date,date+1d) y el dashboard usa DATE(created_at)=? en GetAppointmentBreakdown).; El campo reschedule_self_service está declarado en la interfaz Resp del frontend (Appointments.tsx:22) pero nunca se renderiza en esta página.; trends de 30 días se calculan en el endpoint y se envían pero el frontend no los usa.
- _Mejoras UI:_ PRIORIDAD: corregir la clave 'breakdown'→'appointment_breakdown' para que 'Citas por especialidad' deje de estar siempre vacío.; Unificar/etiquetar las ventanas temporales: StatCards y breakdown son del día; top_cups es de 30 días. Mostrar el rango en cada ChartCard.; Renombrar 'Confirmadas'/'Canceladas' a '(autogestión)' con hint para evitar lectura de funnel creadas→confirmadas.; Agregar una línea de tendencia (creadas/confirmadas/canceladas) reutilizando los trends ya disponibles.; Mostrar nombre del CUPS además del código en 'Top CUPS agendados'.; En GetAppointmentBreakdown migrar de DATE(created_at)=? a rango half-open sargable como ya hace el bot, por consistencia y uso de índice.


### Área: conversion-embudo

#### [ALTA] GetFunnel — embudo detallado (11 pasos, chat_events DISTINCT)
- **Captura:** Eventos emitidos en handlers: session_started (worker/pool.go:388), menu_selected (greeting.go:223-247), document_entered (identification.go:91), patient_found (identification.go:153), patient_identified (identification.go:184), order_method_selected (medical_order.go:83,129), ocr_validated (medical_order.go:442), validations_complete (medical_validation.go:577), slots_found (slots.go:447), booking_confirmed (slots.go:921), appointment_created (slots.go:1336).
- **Almacenamiento:** chat_events(event_type, session_id). Insert en event_repo.go:36/54. Correcto.
- **Query/agregación:** Dashboard: repository.go:217-288 COUNT(DISTINCT session_id) por event_type, BETWEEN from 00:00:00 AND to 23:59:59. Bot duplica en event_repo.go:416 con rango half-open [from,to+1d).
- **Endpoint:** /api/funnel (handlers.go:76). Expone los 11 conteos + drops + conversion_rate.
- **UI:** Conversion.tsx:59-71 + FunnelSteps.tsx. Barras decrecientes con % vs primer paso y caída vs anterior.
- **Problemas:**
  - ORDEN DE PASOS INCORRECTO (rompe monotonía). El struct (models.go:65) y el frontend (Conversion.tsx:60-71) colocan identified_patients en la posición 2, pero patient_identified se emite AL FINAL del bloque de identificación (identification.go:184, tras document_entered:91 y patient_found:153). Cronología real: session_started -> menu_selected -> document_entered -> patient_found -> patient_identified. Por conteo: sessions >= menu_selected >= document_entered >= patient_found >= identified_patients. Mostrar 'Identificados' (el menor) en el paso 2, seguido de 'Eligió agendar'/'Documento'/'Paciente encontrado' (mayores) produce un embudo que CRECE del paso 2 al 3.
  - FunnelSteps.tsx solo dibuja caídas (línea 31-33: `drop > 0`) y el ancho es relativo al primer paso (línea 14). Un paso fuera de orden que es mayor que el anterior no se anota como subida; el paso 'Identificados' intercalado se ve como una caída enorme seguida de recuperación. Visualmente engañoso.
  - DISTINCT por evento independiente: no garantiza monotonía aun con orden correcto (sesiones que cruzan el límite de la ventana). pct() acota a [0,100] (repository.go:1156-1169) reconociéndolo, pero la UI no puede representar incrementos.
- **Recomendación:** Reordenar los pasos a la cronología real (identified_patients después de patient_found). Hacer el embudo monótono encadenando condiciones (sesiones que alcanzaron AL MENOS el paso N) o documentar que son conteos independientes. En FunnelSteps, manejar y mostrar incrementos/pasos fuera de secuencia en vez de ocultarlos.
- **Verificado (real, high):** CONFIRMADO en su totalidad. models.go:65 IdentifiedPatients es el 2º campo del struct (tras TotalSessions). Conversion.tsx:59-71 FUNNEL11 pone {key:'identified_patients', label:'Identificados'} en posición 2, seguido de 'Eligió agendar'(menu_selected), 'Documento'(document_entered), 'Paciente encontrado'(patient_found). Cronología real de emisión en el bot: document_entered en identification.go:91

#### [MEDIA] menu_selected como paso 'Eligió agendar'
- **Captura:** greeting.go:223-247 emite menu_selected para TODAS las opciones: pet_ct, consultar, agendar, resultados, ubicacion, ayuda (event_data.option).
- **Almacenamiento:** chat_events; la opción queda en event_data.option. Correcto.
- **Query/agregación:** repository.go:225-231 cuenta TODO menu_selected sin filtrar event_data.option='agendar'.
- **Endpoint:** /api/funnel.
- **UI:** Conversion.tsx:62 etiqueta el paso como 'Eligió agendar'.
- **Problemas:**
  - El paso 'Eligió agendar' del embudo de agendamiento cuenta toda selección de menú (ubicación, ayuda, resultados, etc.), no solo 'agendar'. Infla el paso y rompe la semántica del embudo (puede superar a document_entered por gente que eligió otra opción).
- **Recomendación:** Filtrar en la query por JSON_EXTRACT(event_data,'$.option')='agendar' para este paso, o usar un evento dedicado. Alternativamente reetiquetar el paso como 'Usó el menú'.
- **Verificado (real, medium):** CONFIRMADO. greeting.go emite menu_selected para múltiples opciones: pet_ct(:223), consultar(:228), agendar(:238), resultados(:241), ubicacion(:244), ayuda(:247) — más cambiar_paciente según EDITING_GUIDE. GetFunnel (repository.go:228,247-248) cuenta TODOS los menu_selected con COUNT(DISTINCT session_id) sin filtrar por event_data $.option='agendar'. Conversion.tsx:62 etiqueta el paso como 'Eligió

#### [MEDIA] Conversión real bot→SIESA (/api/siesa/conversion)
- **Captura:** sessions y appointment_created del GetFunnel local del bot (internal_handler.go:444, event_repo.go:416, half-open). siesaReal de BotCreatedByDay (analytics_repo.go:206) filtrando cod_user_asigna_cita=botCedula sobre fecha_solicitud.
- **Almacenamiento:** chat_events (sesiones/eventos) + SIESA citas (NOLOCK). OK.
- **Query/agregación:** Ventanas alineadas: ambas usan to exclusivo to+1d (internal_handler.go:456). convReal=siesaReal/sessions, convBot=botCreated/sessions, discrepancy=botCreated-siesaReal (internal_handler.go:470-481).
- **Endpoint:** /api/internal/siesa/conversion -> proxy dashboard /api/siesa/conversion (catalog/handlers.go:204).
- **UI:** Conversion.tsx:137-157 StatCards Sesiones/Citas reales/% real (hint % bot)/Discrepancia + BarsChart por día.
- **Problemas:**
  - UNIDADES DISTINTAS en discrepancy (internal_handler.go:480): botCreated = COUNT(DISTINCT session_id) de appointment_created (1 por sesión), siesaReal = COUNT(*) de filas en citas. Una sesión que agenda varios grupos/CUPS genera varias filas cita pero 1 sesión distinta => siesaReal > botCreated => discrepancy NEGATIVA aunque nada se perdió. La etiqueta 'Citas que el bot registró pero no aterrizaron en SIESA' (Conversion.tsx:151) induce a error y la StatCard pinta 'danger' con cualquier valor distinto de 0, incluido negativo.
  - Depende de SIESAAssignUserCedula. Si no está configurado, siesaReal=0 => conversion_real_pct=0 y discrepancy=botCreated (parece pérdida total). El EmptyState solo aparece si rows está vacío (Conversion.tsx:155); las StatCards igual muestran 0% y discrepancia engañosa.
  - Reagendamientos y reservas de lista de espera también emiten appointment_created (slots.go:1336) y cuentan como cita SIESA en ambos lados; consistente, pero la conversión cuenta reagendamientos como conversiones nuevas.
- **Recomendación:** Comparar manzanas con manzanas: contar appointment_created por filas/citas (no DISTINCT sesión) para la discrepancia, o contar citas SIESA distintas por sesión. Acotar discrepancy a >=0 y solo marcar danger si es positiva. Mostrar aviso explícito 'usuario del bot no configurado' cuando botCedula esté vacío.
- **Verificado (real, medium):** CONFIRMADO. internal_handler.go:469 botCreated=funnel.AppointmentCreated, que en GetFunnel es COUNT(DISTINCT session_id) del evento appointment_created (1 por sesión). siesaReal (internal_handler.go:463-466) suma BotCreatedByDay, cuyo SQL es SELECT ... COUNT(*) FROM citas (analytics_repo.go:221) → cuenta FILAS de cita. discrepancy=botCreated-siesaReal (internal_handler.go:480) compara unidades dis

#### [MEDIA] Destino de las sesiones (session-outcomes, /api/session-outcomes)
- **Captura:** Agregación por sesión en repository.go:520-543: has_start (session_started), has_appt (appointment_created), has_wl (waiting_list_joined), has_noslots (no_slots_found|slot_taken). Eventos emitidos: waiting_list_joined (slots.go:833,843), no_slots_found (slots.go:412), slot_taken (slots.go:1447).
- **Almacenamiento:** chat_events; subconsulta GROUP BY session_id con MAX(...). OK.
- **Query/agregación:** scheduled=has_appt; waiting=has_wl AND NOT has_appt; none=NOT appt AND NOT wl; categorías mutuamente excluyentes que suman total. Porcentajes con guard total>0 (repository.go:547-552). Correcto.
- **Endpoint:** /api/session-outcomes (handlers.go:266).
- **UI:** Conversion.tsx:178-202 StatCards + DonutChart (agendó/espera/sin nada) + tarjeta no_slots. Donut adecuado para destino mutuamente excluyente.
- **Problemas:**
  - HUECO DE CAPTURA en 'A lista de espera': la ruta particular SIN orden médica crea una entrada de waiting_list (medical_order.go:88-117, status pending_agent) pero NO emite waiting_list_joined; emite particular_no_order_escalated (línea 122). Esas inscripciones a lista de espera se cuentan como 'Sin cita/none' en vez de 'waiting' => subconteo de 'A lista de espera'.
  - 'no_slots' (tarjeta) usa chat_events mientras la fuga 'Sin disponibilidad' del embudo usa flow_events: dos fuentes para lo mismo (ver KPI flow-stats).
- **Recomendación:** Emitir waiting_list_joined también en la ruta particular sin orden (medical_order.go:112) o incluir su evento en has_wl, para no subcontar la lista de espera.
- **Verificado (real, medium):** CONFIRMADO. medical_order.go:88-117 crea WaitingListEntry (Status 'pending_agent') y emite particular_no_order_escalated (línea 122), NO waiting_list_joined. GetSessionOutcomes (repository.go:537) define has_wl=MAX(event_type='waiting_list_joined'); línea 530 'waiting'=has_start AND has_wl AND NOT has_appt; línea 531 'none'=has_start AND NOT has_appt AND NOT has_wl. Como esa ruta no emite waiting_

#### [OK] flow-stats flow=agendar — fugas (flow_events by_step)
- **Captura:** observability.Emit a flow_events: cups_none (medical_order.go:259), already_has_appt (medical_validation.go:502), no_slots (slots.go:409), booking_failed (slots.go:1456-1459). Niveles en catálogo tracer.go:76,80,84,86 son Outcome/Error (<= milestone) => se emiten en prod.
- **Almacenamiento:** flow_events.step (tracer.go:246-251). InsertBatch async con drop-on-full reportado (tracer.go:279).
- **Query/agregación:** repository.go:1125-1152 GROUP BY step, flow=? AND created_at BETWEEN from 00:00:00 AND to 23:59:59. Correcto y acotado.
- **Endpoint:** /api/flow-stats (handlers.go:53) devuelve by_step, by_outcome, by_reason.
- **UI:** Conversion.tsx:73-91 BarsChart de salidas terminales (no secuencial) — gráfico adecuado.
- **Problemas:**
  - Redundancia: 'no_slots' se captura aquí (flow_events) y TAMBIÉN en session-outcomes vía chat_events (no_slots_found/slot_taken). Dos fuentes para el mismo concepto => posible divergencia de números entre secciones.
  - El endpoint devuelve by_outcome y by_reason pero el frontend solo usa by_step (Conversion.tsx:88); se desperdicia el desglose por razón (reason) que enriquecería las fugas.
- **Recomendación:** Documentar/unificar la fuente de 'sin disponibilidad'. Opcional: usar by_reason para detallar las fugas (p.ej. razones de booking_failed).

#### [OK] Participación del bot (bot-share, /api/siesa/bot-share)
- **Captura:** BotCreatedByDay (bot) vs CreatedByDay (todos) sobre fecha_solicitud (analytics_repo.go:206,251).
- **Almacenamiento:** SIESA citas con NOLOCK, agregado servidor (días x1). OK.
- **Query/agregación:** internal_handler.go:383-407: mapa bot por día, salvaguarda bot>total (línea 397), bot_pct=botSum/totalSum con guard de cero. Correcto.
- **Endpoint:** /api/internal/siesa/bot-share -> proxy /api/siesa/bot-share (catalog/handlers.go:209).
- **UI:** Conversion.tsx:159-176 StatCards %bot/citas bot/otros + TrendChart bot vs otros por día. Adecuado.
- **Problemas:**
  - Depende de SIESAAssignUserCedula; si está vacío bot=0 => bot_pct=0 y otros=total, sin distinguir 'no configurado' de '0% real'. EmptyState solo si no hay filas (Conversion.tsx:173).
- **Recomendación:** Mostrar indicador 'usuario del bot no configurado' cuando botCedula esté vacío para no confundir con participación real del 0%.

**Notas del área:**
- _Faltantes:_ Filtrar menu_selected por event_data.option='agendar' en el embudo de agendamiento (hoy cuenta todas las opciones del menú).; Corregir el ORDEN de los pasos del embudo: identified_patients debe ir tras patient_found (cronología real en identification.go), no en la posición 2.; Emitir waiting_list_joined en la ruta particular sin orden médica (medical_order.go:88-122) para que session-outcomes cuente esas inscripciones como 'lista de espera'.; Arreglar la unidad de 'discrepancy' en conversión real (comparar filas-cita vs filas-cita, no DISTINCT sesión vs filas) y acotar a >=0.; Indicador explícito 'usuario del bot (SIESAAssignUserCedula) no configurado' en conversión y bot-share para no confundir con 0% real.
- _Redundantes:_ 'no_slots' se mide por duplicado: flow_events (fuga del embudo, slots.go:409) y chat_events no_slots_found/slot_taken (tarjeta de session-outcomes). Riesgo de divergencia entre secciones.; Dos implementaciones de GetFunnel: bot event_repo.go:416 (rango half-open) y dashboard repository.go:217 (BETWEEN ...23:59:59). Ambas leen el mismo neuro_bot; lógica duplicada con manejo de fecha distinto.; El endpoint /api/flow-stats devuelve by_outcome y by_reason pero el frontend solo consume by_step.
- _Mejoras UI:_ FunnelSteps.tsx: representar pasos fuera de secuencia / incrementos en vez de ocultarlos (solo dibuja caídas; ancho relativo al primer paso). Mientras el orden esté mal, el embudo se ve roto.; Conversión: la StatCard 'Discrepancia' marca 'danger' con cualquier valor !=0 incluido negativo; debe guardar negativos y explicar el denominador/unidad.; Hacer explícitas las VENTANAS distintas: hay tres StatCards 'Sesiones' (conversión 30d, destino 30d, embudo 7d) que pueden no coincidir; etiquetar el rango en cada tarjeta.; Aprovechar by_reason en la tarjeta de fugas para desglosar razones (p.ej. de booking_failed).


### Área: siesa-operacion

#### [MEDIA] No-show real (inasistencia por día, % no-show)
- **Captura:** Derivado de SIESA (citas pasadas no canceladas no atendidas), no de eventos del bot. Correcto: excluye citas futuras con fecha < CAST(GETDATE() AS DATE).
- **Almacenamiento:** N/A.
- **Query/agregación:** analytics_repo.go:172-180 NoShowByDay: atendidas=estado'A'; no_show=estado NOT IN('C','A') sobre fecha pasada. esperadas=atendidas+no_show (excluye canceladas) → % no-show = no_show/esperadas, denominador correcto. PROBLEMA verificado en BD: de 2093 no-show pasadas, 441 (21%) son estado 'CC' (confirmadas que nunca pasaron a 'A'). Cuenta como no-show toda cita CC/P pasada; si SIESA no transiciona CC→A de forma fiable al atender, el no-show queda inflado (rate observado ~40.7%). 'atendida' depende exclusivamente de estado='A' como única señal de asistencia.
- **Endpoint:** internal_handler.go:260-288 HandleSiesaNoShow: suma esperadas/atendidas/no_show con guarda /0. OK.
- **UI:** Siesa.tsx:122-134,142-144. StatCards No-show y %No-show con umbrales de color (>=20% danger) + hint 'X citas esperadas' + TrendChart. Buena presentación. El hint 'Citas pasadas no canceladas que nunca pasaron a atendida' es honesto.
- **Problemas:**
  - 441 citas 'CC' (confirmadas) pasadas se cuentan como no-show; pueden ser asistencias no marcadas como 'A' por el personal, no inasistencias reales → posible sobreestimación del ~21% del no-show.
  - 'atendida' usa solo estado='A'; si la asistencia se registra por otra vía en SIESA, se subcuenta atendidas.
- **Recomendación:** Validar con el negocio si CC pasada = no-show o = asistencia sin cerrar. Opción: separar en la UI 'no-show puro' (estado P pasado) de 'sin cerrar' (CC pasado), o excluir CC del numerador. Confirmar que estado='A' es la única marca de asistencia en SIESA.
- **Verificado (real, medium):** CONFIRMADO en código (BOT internal/repository/siesa/analytics_repo.go, NoShowByDay líneas 172-180). El numerador no_show = SUM(CASE WHEN ISNULL(estado,'') NOT IN ('C','A')...) sobre citas pasadas (fecha < CAST(GETDATE() AS DATE)), por lo que TODA cita pasada con estado='CC' cae en no_show. atendidas solo cuenta estado='A'. Refuerzo extra: esta query IGNORA AsistenciaConfirmada por completo, a dife

#### [MEDIA] Conciliación bot↔cups_medico (médico mal asignado)
- **Captura:** BotAppointmentsWithCups (analytics_repo.go:305-312) filtra citas por cod_user_asigna_cita=botCedula y fecha_solicitud>=hoy-days, JOIN citas_procedimientos.
- **Almacenamiento:** Cruce contra cups_medico (catálogo local) vía FindMedicosForCups; fail-open si el CUPS no tiene médicos configurados.
- **Query/agregación:** GAP estructural verificado en BD: la query SOLO hace JOIN a citas_procedimientos (Flujo B: procedimientos/imágenes). Las CONSULTAS (Flujo A) guardan su CUPS en citas_procedimientos_asuntos — 2843 citas existen únicamente ahí. Por tanto las consultas creadas por el bot NUNCA se evalúan para médico mal asignado. Además fecha de la cita usada para conciliar es c.fecha pero el filtro de ventana es sobre fecha_solicitud (correcto para 'creadas por el bot').
- **Endpoint:** internal_handler.go:293-338 HandleSiesaConciliacion: recorre filas, cuenta evaluadas/mal. 'bot_citas'=len(citas) cuenta FILAS cita-procedimiento, no citas distintas (una cita con N CUPS infla el conteo).
- **UI:** Siesa.tsx:148-192. StatCards 'Citas del bot evaluadas', 'Médico mal asignado', 'Total citas del bot' + tabla de mal-asignaciones. Etiquetas 'citas' cuando en realidad son filas cita-CUPS pueden inducir error leve.
- **Problemas:**
  - Las citas de tipo CONSULTA (citas_procedimientos_asuntos) quedan fuera de la conciliación: el JOIN solo cubre citas_procedimientos. ~2843 citas solo viven en cpa.
  - 'bot_citas'/'evaluadas' cuentan filas cita-procedimiento, no citas distintas; con CUPS múltiples por cita el total se infla y las etiquetas dicen 'citas'.
  - Si SIESA_ASSIGN_USER_CEDULA no está configurado (default '000000'), el endpoint devuelve nil silenciosamente (sin médico del bot no hay nada que conciliar) — la UI muestra 0 sin distinguir 'todo correcto' de 'no configurado'.
- **Recomendación:** Añadir un UNION/segunda fuente que incluya citas_procedimientos_asuntos.CodProcedimiento para conciliar también las consultas. Renombrar las etiquetas a 'cita-procedimiento' o contar DISTINCT cita. Mostrar en la UI un estado 'no configurado' cuando botCedula está vacío en vez de 0.
- **Verificado (real, medium):** DOS de tres sub-afirmaciones CONFIRMADAS, la tercera parcialmente incorrecta. (a) CONFIRMADO: BotAppointmentsWithCups (analytics_repo.go líneas 305-312) solo hace JOIN citas_procedimientos cp; las consultas viven en citas_procedimientos_asuntos (CLAUDE.md), por lo que quedan fuera de la conciliación. El conteo '~2843' no es verificable desde código pero la exclusión sí. (b) CONFIRMADO: internal_ha

#### [OK] Citas por situación — Próximas (% confirmación, confirmadas vs pendientes)
- **Captura:** Lee citas de SIESA directo; situación derivada en SQL, no de eventos del bot.
- **Almacenamiento:** N/A.
- **Query/agregación:** analytics_repo.go:123-134 AppointmentsByState con CASE: estado='C'→cancelada; 'A'→atendida; AsistenciaConfirmada=1 OR estado='CC'→confirmada; resto→pendiente. VERIFICADO contra la BD: cada fila cae en exactamente una rama (sin doble conteo); el orden C antes que la rama confirmada evita contar como confirmada una cita confirmada-y-luego-cancelada (153 filas C con AC=1 quedan correctamente en cancelada). La definición coincide con la documentada (confirmada vive en AsistenciaConfirmada, no solo en el estado).
- **Endpoint:** internal_handler.go:236-255 HandleSiesaCitasEstado: agrega totales por situación. Proxy handlers.go:193-195.
- **UI:** Siesa.tsx:101-114 con from=today,to=daysAhead(30). proxConfirmRate=confirmada/(confirmada+pendiente) — denominador correcto (ignora atendida/cancelada futuras, que no aplican al operativo de confirmación). Donut confirmadas vs pendientes adecuado.
- **Problemas:**
  - Borde de día: una cita de HOY atendida temprano (estado 'A') queda en la ventana 'próximas' (fecha>=today) pero el proxDonut solo muestra confirmada/pendiente, así que desaparece de ambos donuts (no está en histórico porque hist usa fecha<today). Volumen marginal.
- **Recomendación:** Aceptable. Si se quiere precisión, mover el corte de 'próximas' a fecha >= mañana o incluir 'atendida' como cuarta categoría informativa.

#### [OK] Ocupación de agenda (% ocupación, slots ocupados/libres, % por médico)
- **Captura:** No hay captura propia: lee programacion_medico_detalle de SIESA en tiempo real (no depende de eventos del bot).
- **Almacenamiento:** N/A (tabla operativa de SIESA).
- **Query/agregación:** analytics_repo.go:71-85 Occupancy(). ocupados=SUM(IdCita NOT NULL); libres=SUM(IdCita NULL AND Bloqueado=0 AND SinProgramacion=0). Ventana futura Fecha>=hoy AND Fecha<hoy+windowDays (sargable, sin CAST en la columna). El denominador (ocupados+libres) excluye slots bloqueados/sin-programación: es utilización de capacidad agendable, coherente con la query de slots de CLAUDE.md. INNER JOIN sis_medi: verificado 0 médicos sin fila → no descarta slots.
- **Endpoint:** internal_handler.go:207-232 HandleSiesaOcupacion: recalcula ocup/libre globales y ocupacion_pct con guarda /0 (ocup+libre>0). Proxy dashboard handlers.go:188-190 reenvía query string.
- **UI:** Siesa.tsx:86-99. 3 StatCards (% ocupación, ocupados, libres) + HBarsChart % por médico (agregando días por médico, mismo cálculo que el server). Gráfico de barras horizontal es el adecuado para comparar saturación entre médicos.
- **Recomendación:** Sin cambios. Opcional: el % por médico no distingue días con 0 capacidad; ya se maneja con guarda (value=0). Considerar mostrar el nº de slots junto al % para dar contexto de volumen.

#### [OK] Citas por situación — Histórico (atendidas, canceladas, total)
- **Captura:** Lee citas de SIESA directo.
- **Almacenamiento:** N/A.
- **Query/agregación:** Mismo AppointmentsByState, from=daysAgo(30),to=today(). VERIFICADO: el donut histórico (atendida + cancelada + no-show) suma exactamente histTotal porque no-show(=P+CC pasadas) = situación confirmada_pasada + pendiente_pasada; denominadores consistentes entre los dos endpoints (mismo corte fecha<hoy).
- **Endpoint:** internal_handler.go:236-255 (citas-estado) + 260-288 (no-show).
- **UI:** Siesa.tsx:116-146. 5 StatCards + DonutChart (resultado real) + TrendChart atendidas vs no-show. histCancelRate=cancelada/histTotal correcto. Combina dos endpoints de forma coherente.
- **Recomendación:** Sin cambios.

**Notas del área:**
- _Faltantes:_ Conciliación de CONSULTAS: el médico mal asignado solo se valida para procedimientos/imágenes (citas_procedimientos); falta cubrir citas_procedimientos_asuntos (Flujo A). Verificado: 2843 citas solo existen en cpa y hoy son invisibles al KPI.; No hay KPI de ocupación PASADA/realizada (la actual es solo futura): no se puede medir cuántos de los slots agendados terminaron en atención.; Falta exponer en esta página la 'participación del bot' (bot-share) y 'conversión real' (conversion); existen como endpoints (internal_handler.go:343,420) y viven en Conversion.tsx, pero la página Siesa de Operación no enlaza a ellos pese a ser parte del mismo dominio SIESA.
- _Redundantes:_ Ninguna redundancia de cálculo dañina: histórico (citas-estado) y no-show comparten denominador de forma consistente (verificado). El doble consumo de endpoints es intencional y coherente.
- _Mejoras UI:_ No-show: separar visualmente 'no-show puro' (estado P pasado) de 'confirmada sin cerrar' (CC pasado) para no atribuir toda la cifra a inasistencia del paciente; el rate observado (~40%) puede alarmar de más.; Conciliación: aclarar que el conteo es de filas cita-CUPS (no citas) y mostrar estado 'no configurado' cuando falta la cédula del bot.; Ocupación: añadir el nº absoluto de slots junto al % por médico para dar contexto de volumen (un médico con 100% sobre 2 slots no es comparable a uno con 80% sobre 40).; Añadir leyenda/tooltip con la definición exacta de cada situación (P/A/C/CC) en la cabecera de la sección, ya que la definición es deducida y no estándar.


### Área: notificaciones-canal

#### [ALTA] proactives_no_response (Sin respuesta)
- **Captura:** internal/notifications/manager.go:1011 emite notification_timeout dentro de handleTimeout, que es el callback del time.AfterFunc y se dispara en CADA paso de la cadena de reintentos.
- **Almacenamiento:** chat_events.event_type='notification_timeout', con event_data.type=pending.Type (todos los tipos, no solo confirmation).
- **Query/agregación:** repository.go:106 mapEventToKPI: ProactivesNoResponse = COUNT(notification_timeout) del día (GetDailyKPIs).
- **Endpoint:** /api/notifications -> kpis.proactives_no_response.
- **UI:** Notifications.tsx:119 StatCard 'Sin respuesta' y :83 donut 'Respuestas (WhatsApp)'.
- **Problemas:**
  - DOBLE/TRIPLE CONTEO: una sola cita sin confirmar emite notification_timeout en RetryCount 0 (followup1), 1 (followup2) y 2/3 (escalación) — ver confirmation.go:184-237 + manager.go:1020-1031. Con ConfirmFollowupEnabled un mismo paciente cuenta ~3 veces, así que proactives_no_response puede superar incluso a proactives_sent.
  - Mezcla tipos: notification_timeout lleva type=confirmation/reschedule/cancellation/waiting_list (manager.go:1013) pero el KPI los suma todos; 'Sin respuesta' no es solo de recordatorios de confirmación.
  - El donut 'Respuestas (WhatsApp)' queda distorsionado porque uno de sus tres segmentos (Sin respuesta) está inflado frente a confirmadas/canceladas que sí se cuentan 1 vez.
- **Recomendación:** Contar 'sin respuesta' como sesiones/citas distintas, no eventos: emitir un único evento terminal de timeout (solo en el paso final de escalación) o usar COUNT(DISTINCT appointment_id) filtrando type='confirmation' y retry=último. Alternativamente derivarlo igual que el canal (sent_confirmation - confirmed - cancelled).
- **Verificado (real, high):** Confirmado en confirmation.go:184-237, manager.go:1007-1031 y kpi/repository.go:33-34,99-106. Recomendacion: emitir un unico evento terminal de timeout (solo en escalacion) o COUNT(DISTINCT appointment_id) filtrando type='confirmation' y retry=ultimo; o derivarlo como sent_confirmation-confirmed-cancelled igual que el canal.

#### [MEDIA] notification_breakdown por tipo ($.type)
- **Captura:** notification_sent se emite con event_data.type en los 4 puntos: scheduler/tasks.go:217 (confirmation), internal_handler.go:882 (confirmation/agenda), :1037 (cancellation), :1371 (reschedule) y waiting_list_check.go:198 (waiting_list). Captura completa y correcta.
- **Almacenamiento:** chat_events JSON $.type. OK.
- **Query/agregación:** repository.go:150-177 GetNotificationBreakdown agrupa por JSON_EXTRACT($.type). Correcto, pero usa DATE(created_at)=? (no sargable) a diferencia del resto que usa rango half-open.
- **Endpoint:** /api/notifications -> breakdown. OK.
- **UI:** Notifications.tsx:86-92 BarsChart 'Por tipo de notificación'.
- **Problemas:**
  - MISLABEL: la barra b.reschedule se rotula 'Recordatorio' (Notifications.tsx:90). El tipo 'reschedule' es notificación de REAGENDAMIENTO de agenda, no recordatorio. El recordatorio diario es justamente el tipo 'confirmation'. Induce a error.
  - repository.go:154 usa DATE(created_at)=? no sargable (la copia del bot en event_repo.go:316 ya usa rango sargable). Bajo impacto.
- **Recomendación:** Renombrar la etiqueta de b.reschedule a 'Reagendamiento'. Cambiar el WHERE a rango half-open [date, date+1día).
- **Verificado (real, medium):** Confirmado: Notifications.tsx:88-91, kpi/repository.go:153-155, event_repo.go:310-317. Recomendacion: renombrar etiqueta a 'Reagendamiento' y cambiar el WHERE del dashboard a rango half-open.

#### [MEDIA] Efectividad por canal WhatsApp vs IVR
- **Captura:** WhatsApp: notification_sent(type=confirmation) + notification_confirmed (confirmation.go:144) + notification_cancel_confirmed (appointments.go:603). IVR: notification_ivr_sent (tasks.go:325) + notification_confirmed_ivr (manager.go:685) + notification_cancelled_ivr (manager.go:743). Bien ubicados.
- **Almacenamiento:** chat_events. OK.
- **Query/agregación:** repository.go:358-389 GetChannelEffectiveness. Acierta al restringir 'sent' a type=confirmation (no mezcla recordatorios de otros tipos). no_response se DERIVA = sent-confirmed-cancelled (fillChannelRates:391), evitando el doble-conteo de notification_timeout. Buen diseño.
- **Endpoint:** /api/channels?days=30. Expone sent/confirmed/cancelled/no_response/confirm_pct/response_pct por canal. Completo.
- **UI:** Notifications.tsx:143-163 StatCards + BarsChart '% Confirmación por canal'.
- **Problemas:**
  - El recordatorio type=confirmation tiene TRES desenlaces (confirm, reschedule, cancel) — ver appointments.go:533 notification_reschedule_confirmed. La query solo resta confirmed+cancelled, así que los pacientes que pulsaron 'Reprogramar' se cuentan erróneamente como 'sin respuesta' (no_response) e inflan ese segmento y bajan response_pct.
  - WhatsApp.Cancelled (notification_cancel_confirmed) procede solo del flujo de confirmación (appointments.go:603), correcto; pero conviene documentar que NO incluye notification_cancel_acknowledged (reschicate de cancelaciones type=cancellation), que es lo deseado.
  - La barra '% Confirmación por canal' no marca el eje como porcentaje y es redundante con los StatCards que ya muestran el %.
- **Recomendación:** Restar también notification_reschedule_confirmed (los originados desde el recordatorio de confirmación) al derivar no_response, o exponer un 4º segmento 'reprogramó'. Etiquetar el eje del gráfico como %.
- **Verificado (real, medium):** Confirmado en appointments.go:533,603 y kpi/repository.go:368-398. Recomendacion: restar notification_reschedule_confirmed al derivar no_response, o exponer un 4to segmento 'reprogramo'; etiquetar el eje como %.

#### [MEDIA] Reagendamientos consolidados
- **Captura:** self_service: self_reschedule.go:167 (por paciente). confirmed: appointments.go:533 (por cita). admin: internal_handler.go:1209 y :1278 (UNA vez por OPERACIÓN de agenda, phone vacío, con appointments_cancelled/updated y patients_to_notify en el JSON).
- **Almacenamiento:** chat_events. OK, sin doble emisión (1209 y 1278 son escenarios mutuamente excluyentes new_agenda/same_agenda).
- **Query/agregación:** repository.go:403-466 GetRescheduleSummary suma COUNT(*) de los 3 event_type y los totaliza.
- **Endpoint:** /api/reschedules?days=30. OK.
- **UI:** Notifications.tsx:172-184 StatCards + DonutChart 'Por origen' + TrendChart.
- **Problemas:**
  - MEZCLA DE UNIDADES: admin_reschedule_agenda cuenta OPERACIONES de agenda (1 evento = posiblemente 30 pacientes movidos), mientras self_service y confirmed cuentan citas/pacientes. 'Total reagendados' y el donut 'Por origen' suman peras con manzanas; 'Admin (agenda)' subestima groseramente el impacto real (las citas afectadas están en event_data.appointments_cancelled/updated, no en el COUNT).
  - La tendencia diaria (repository.go:445) hereda el mismo problema: un día con una operación admin masiva aporta solo +1.
- **Recomendación:** Para el origen admin, sumar SUM(JSON_EXTRACT(event_data,'$.appointments_cancelled')+'$.appointments_updated') en vez de COUNT(*); o renombrar la métrica admin a 'operaciones de agenda' y sacarla del total de pacientes reagendados.
- **Verificado (real, medium):** Confirmado: internal_handler.go:1209-1217,1278-1285 emiten 1 evento/operacion con appointments_cancelled/appointments_updated; kpi/repository.go:416-448 usa COUNT(*). Recomendacion: para admin sumar SUM(JSON_EXTRACT(...appointments_cancelled + appointments_updated)) en vez de COUNT(*), o renombrar a 'operaciones de agenda' y sacarla del total de pacientes.

#### [MEDIA] Confirmaciones pendientes (/api/pending)
- **Captura:** notification_pending poblada por RegisterPending (tasks.go:207, internal_handler.go:873/1028/1362). Respuestas vía chat_events. OK.
- **Almacenamiento:** notification_pending (migración 009) + chat_events. OK.
- **Query/agregación:** repository.go:1017-1098 GetPendingConfirmations. Filtra np.type='confirmation' y expires_at>NOW(). Correlaciona respuesta WA/IVR por teléfono y SOLO de HOY (CURDATE..+1d), sin appointment_id.
- **Endpoint:** /api/pending?status= . Devuelve entries + stats. OK.
- **UI:** Notifications.tsx:51-59 + 187-190 DataTable, refetch 60s.
- **Problemas:**
  - No detecta el desenlace 'reprogramó' (no consulta notification_reschedule_confirmed/declined): un paciente que pulsó Reprogramar aparece como 'pendiente'. final_status nunca refleja reagendamiento.
  - Correlación floja: WA/IVR se cruzan por phone + created_at de HOY, no por appointment_id; con varias citas o respuestas a caballo de medianoche puede mal-atribuir o mostrar 'pendiente' una ya resuelta.
  - IVRStatus 'not_called' (models.go:182) se pinta como badge 'pendiente' (statusBadge:48), confundiendo 'no se llamó aún' con 'sin respuesta'.
  - stats.ResponseRate (repository.go:1094) se calcula pero el frontend Notifications.tsx no muestra el bloque stats de /api/pending — dato calculado y no usado.
- **Recomendación:** Añadir el outcome 'reschedule' al cruce, correlacionar por appointment_id, y un badge distinto para 'not_called'. Mostrar stats.response_rate o quitarlo.
- **Verificado (real, medium):** Confirmado en kpi/repository.go:1017-1098 y Notifications.tsx:45-49,187-190. Recomendacion: anadir el outcome 'reschedule' al cruce, correlacionar por appointment_id, badge distinto para 'not_called' y mostrar (o quitar) stats.response_rate.

#### [BAJA] proactives_sent (Enviadas)
- **Captura:** notification_sent emitido 1 vez por envío inicial (no por followups, que son SendText). Captura correcta y sin huecos.
- **Almacenamiento:** chat_events. OK.
- **Query/agregación:** repository.go:100 ProactivesSent = COUNT(notification_sent) de TODOS los tipos.
- **Endpoint:** /api/notifications -> proactives_sent.
- **UI:** Notifications.tsx:116 StatCard 'Enviadas' junto a Confirmadas/Canceladas/Sin respuesta.
- **Problemas:**
  - Denominador inconsistente: 'Enviadas' incluye los 4 tipos (confirmation+reschedule+cancellation+waiting_list) pero 'Confirmadas'/'Canceladas' solo provienen del flujo confirmation. Yuxtaponerlas en la misma fila de StatCards sugiere una tasa de confirmación que no corresponde (a diferencia de la sección 'Efectividad por canal', que sí acota a confirmation).
- **Recomendación:** Mostrar 'Enviadas (confirmación)' o desglosar por tipo, para que el conjunto Enviadas/Confirmadas/Canceladas comparta denominador.
- **Verificado (real, low):** Confirmado: kpi/repository.go:99-100 vs 369 y Notifications.tsx:116-119. Recomendacion: mostrar 'Enviadas (confirmacion)' o desglosar por tipo para compartir denominador.

#### [OK] proactives_confirmed (Confirmadas)
- **Captura:** notification_confirmed 1 vez por confirmación (confirmation.go:144).
- **Almacenamiento:** chat_events. OK.
- **Query/agregación:** repository.go:101 COUNT(notification_confirmed). Correcto.
- **Endpoint:** /api/notifications.
- **UI:** Notifications.tsx:117 StatCard + donut.

#### [OK] proactives_cancelled (Canceladas)
- **Captura:** notification_cancel_confirmed 1 vez al cancelar desde el recordatorio (appointments.go:603). No se confunde con notification_cancel_acknowledged (type=cancellation, reschedule.go:37).
- **Almacenamiento:** chat_events. OK.
- **Query/agregación:** repository.go:103 COUNT(notification_cancel_confirmed). Correcto.
- **Endpoint:** /api/notifications.
- **UI:** Notifications.tsx:118 StatCard + donut.

#### [OK] notification_escalated (Escaladas)
- **Captura:** notification_escalated_agent 1 vez por escalación en escalateToAgent (confirmation.go:308).
- **Almacenamiento:** chat_events. OK.
- **Query/agregación:** repository.go:107 COUNT(notification_escalated_agent). Correcto.
- **Endpoint:** /api/notifications.
- **UI:** Notifications.tsx:120 StatCard 'Escaladas'.

#### [OK] IVR sent/confirmed/cancelled
- **Captura:** notification_ivr_sent (tasks.go:325), notification_confirmed_ivr (manager.go:685), notification_cancelled_ivr (manager.go:743). 1 evento por llamada/respuesta DTMF; IVR solo se usa en el flujo de confirmación.
- **Almacenamiento:** chat_events. OK.
- **Query/agregación:** repository.go:109-114 COUNT por event_type. Correcto.
- **Endpoint:** /api/ivr -> ivr_calls_sent/ivr_confirmed/ivr_cancelled.
- **UI:** Notifications.tsx:133-135 StatCards IVR.
- **Problemas:**
  - IVR no tiene evento de timeout/no_answer; el 'sin respuesta' IVR solo existe derivado en /api/channels (no en estos StatCards). Es una limitación conocida, no un bug.

**Notas del área:**
- _Faltantes:_ No hay KPI de 'reprogramados desde el recordatorio' (notification_reschedule_confirmed originado en el flujo de confirmación) en la vista de respuestas WhatsApp ni en /api/channels: ese desenlace se pierde y contamina el 'sin respuesta'.; No se expone tasa de respuesta global del recordatorio (confirmadas+canceladas+reprogramadas)/enviadas(confirmation) en los StatCards superiores; solo aparece por canal más abajo.; No hay métrica de 'no_answer' IVR (gather timeout sin DTMF, manager.go:750) como evento contable; solo nota interna.; notification_cancel_acknowledged (ack de cancelaciones type=cancellation, reschedule.go:37) se captura y mapea (CancelAcknowledged) pero no se muestra en Notifications.tsx.
- _Redundantes:_ /api/notifications y /api/ivr calculan trends de 30 días (handlers.go:158-160 y :189-191) que Notifications.tsx NO consume (NotifResp/IvrResp no tienen campo trends): trabajo de query desperdiciado en cada carga.; stats de /api/pending (response_rate, totales) se calculan (repository.go:1093) pero el frontend no los renderiza.; La barra '% Confirmación por canal' duplica la info de los StatCards de % por canal.
- _Mejoras UI:_ Corregir etiqueta 'Recordatorio' -> 'Reagendamiento' en el breakdown por tipo (Notifications.tsx:90).; Agrupar Enviadas/Confirmadas/Canceladas/Sin respuesta bajo un mismo denominador (solo type=confirmation) o etiquetar 'Enviadas' como total multi-tipo para no inducir una tasa falsa.; Badge propio para IVR 'not_called' distinto de 'pendiente' en la tabla de pendientes.; Marcar el eje/tooltip de los gráficos de porcentaje como %.; Mostrar la unidad real del reagendamiento admin (operaciones vs pacientes) para no engañar en 'Total reagendados'.


### Área: escalacion

#### [MEDIA] Handoff fallo (escalation_failed)
- **Captura:** escalation.go:116 WithEvent('escalation_failed') cuando EscalateToAgent devuelve error. NO se emite a flow_events (no hay observability.Emit en ese path) -> solo vive en chat_events. Correcto que no toque escalated_at.
- **Almacenamiento:** chat_events 'escalation_failed'; mapEventToKPI -> DailyKPIs.EscalationFailed (repository.go:143-144).
- **Query/agregación:** GetDailyKPIs por dia (repository.go:33) leido via /api/sessions.
- **Endpoint:** GET /api/sessions?date= (handlers.go:95-114) devuelve kpis.escalation_failed.
- **UI:** StatCard 'Handoff falló' usa date=today() (Escalation.tsx:64-66,108-114) mientras el resto del bloque superior es 7 dias y el SLA 30 dias. Ventana inconsistente: muestra solo HOY junto a Escaladas/Recordatorios/Expiradas de 7d.
- **Problemas:**
  - Inconsistencia de ventana: 'Handoff falló' es de HOY; las otras 3 StatCards del mismo grid son de 7 dias. Induce a comparar magnitudes no comparables.
  - escalation_failed no se emite a flow_events, asi que no aparece en el donut 'Resultado' ni puede agregarse por flujo.
- **Recomendación:** Tomar escalation_failed del mismo rango de 7 dias (sumar trend o agregar a flow_events). Alinear ventanas o etiquetar 'hoy' explicitamente.
- **Verificado (real, medium):** VERIFICADO. Escalation.tsx:64-66 consulta /api/sessions con date=today() y la StatCard 'Handoff fallo' (linea 110) usa sess.kpis.escalation_failed; las otras 3 StatCards (lineas 105-107) usan count(...) sobre flow (from=daysAgo(7)). Ventanas no comparables: confirmado. escalation_failed solo se emite via WithEvent (escalation.go:116) -> Tracker.LogEvent (chat_events), SIN observability.Emit, por l

#### [MEDIA] SLA - 3 escenarios (sin atender / atendio sin devolver / ideal)
- **Captura:** Derivado de sessions: answered=SUM(last_agent_msg_at IS NOT NULL); returned=SUM(last_agent_msg_at IS NOT NULL AND (resumed_at IS NOT NULL OR status='completed')) (repository.go:573-574). last_agent_msg_at se sella en webhook outbound (webhook_handler.go:292-293) excluyendo /bot y mensajes-puente del bot (IsBotInterstitialMessage, pool.go:1228).
- **Almacenamiento:** sessions.last_agent_msg_at, resumed_at, status. TouchAgentActivity (session_repo.go:233) condiciona a status='escalated' AND expires_at>NOW().
- **Query/agregación:** scenarioDonut: ideal=returned; atendio sin devolver=max(0,answered-returned); sin atender=max(0,total-answered) (Escalation.tsx:94-98). Coherente con los campos.
- **Endpoint:** GET /api/escalation/sla?days=30 (handlers.go:283).
- **UI:** DonutChart 'Cómo se maneja la escalación'. Visual adecuado. Pero still_open (status='escalated' en curso) se mete en 'sin atender' aunque sea una escalada reciente aun viva -> infla 'sin atender'.
- **Problemas:**
  - TouchAgentActivity exige expires_at>NOW() (session_repo.go:236): si la escalada ya expiró pero sigue status='escalated', la respuesta del agente NO sella last_agent_msg_at -> subconteo de 'atendidas'.
  - still_open (escaladas en curso) se clasifica como 'sin atender' en el donut (total-answered); mezcla 'aun esperando' con 'nunca atendida'.
  - 'returned' depende de last_agent_msg_at IS NOT NULL: un /bot cerrar resolviendo sin mensaje al paciente no cuenta como devuelta/atendida.
- **Recomendación:** Quitar el guard expires_at>NOW() de TouchAgentActivity (o ampliarlo) para no perder sellos. Separar 'en curso' (still_open) de 'sin atender' en el donut.
- **Verificado (real, medium):** VERIFICADO. (1) session_repo.go:233-237 TouchAgentActivity hace UPDATE ... WHERE status='escalated' AND expires_at > NOW(); si expiro pero sigue escalated, no sella last_agent_msg_at -> Answered (repository.go:570 SUM(last_agent_msg_at IS NOT NULL)) subcontado. (2) scenarioDonut 'Sin atender' = max(0,total-answered) (Escalation.tsx:97) y total incluye StillOpen=SUM(status='escalated') (repository.

#### [MEDIA] Grafico 'Estados de escalación' (flow by_step) y donut 'Resultado'
- **Captura:** flow_events reales emitidos para escalacion (tracer.go:124-128): 'escalated','agent_resumed','agent_closed','agent_reminder_sent','escalation_expired'.
- **Almacenamiento:** flow_events.step.
- **Query/agregación:** Escalation.tsx:46-52 STEP_LABEL mapea claves 'escalation_closed' y 'escalation_ended' que NO existen como steps de flow (son event_type de chat_events: pool.go:722,798). Los steps reales 'agent_resumed' y 'agent_closed' NO estan en STEP_LABEL, asi que steps.filter (Escalation.tsx:78-80) los DESCARTA.
- **Endpoint:** /api/flow-stats.
- **UI:** El BarsChart 'Estados de escalación' solo pinta Escaladas/Recordatorios/Expiradas y OMITE silenciosamente agent_resumed (devueltas) y agent_closed (cerradas). Las etiquetas 'Cerradas'/'Finalizadas' nunca se renderizan. En datos sembrados no se nota (seed-kpis no emite agent_resumed/agent_closed), pero en produccion si.
- **Problemas:**
  - BUG de mapeo: STEP_LABEL usa 'escalation_closed'/'escalation_ended' en vez de los steps reales 'agent_closed'/'agent_resumed' -> el grafico pierde resueltas/cerradas/devueltas.
  - Donut 'Resultado' (by_outcome) muestra claves crudas (escalated/ok/info/retry) sin traducir (Escalation.tsx:81), poco legible.
- **Recomendación:** Corregir STEP_LABEL: 'agent_resumed':'Devueltas', 'agent_closed':'Cerradas' (eliminar 'escalation_closed'/'escalation_ended'). Traducir las etiquetas del donut by_outcome.

#### [BAJA] Expiradas (escalation_expired)
- **Captura:** manager.go:354/358 emite chat_event y flow 'escalation_expired' cuando el PACIENTE lleva EscalationCloseMin en silencio (manager.go:343). Nombre 'expired' = realmente 'cerrada por silencio del paciente', no expiracion de SLA. Captura correcta para lo que mide.
- **Almacenamiento:** chat_events 'escalation_expired'; flow_events step 'escalation_expired' outcome 'info'; sessions.status='abandoned' (MarkAbandoned, session_repo.go:284).
- **Query/agregación:** StatCard usa flow.by_step 'escalation_expired' (7d, Escalation.tsx:107). La seccion SLA mide 'Expiradas' como status='abandoned' en 30d (repository.go:572) pero NO se muestra (still_open/resolved/expired se calculan y nunca se pintan).
- **Endpoint:** /api/flow-stats + /api/escalation/sla.
- **UI:** Dos definiciones de 'expirada' coexisten (flow 7d vs SLA 30d) con ventanas distintas; ademas sessions cerradas por FindExpiredEscalatedSessions (session_repo.go:195, expires_at<NOW) tambien quedan status sin evento 'escalation_expired'. Puede confundir.
- **Problemas:**
  - Doble noción de 'expirada': flow_events 'escalation_expired' (silencio del paciente, 7d) vs SLA status='abandoned' (30d). Ventanas y fuentes distintas.
  - FindExpiredEscalatedSessions cierra escaladas por expires_at sin emitir 'escalation_expired' -> hueco: esas no aparecen en el conteo de expiradas del flow.
- **Recomendación:** Unificar la definicion (un solo evento/fuente) o etiquetar claramente 'cerradas por silencio'. Emitir 'escalation_expired' tambien en FindExpiredEscalatedSessions.
- **Verificado (real, low):** VERIFICADO PARCIAL. Real: Escalation.tsx:55 from=daysAgo(7) y count('escalation_expired') (linea 107) lee flow_events (repository.go:1135 flowGroup sobre flow_events). El SLA Expired = SUM(status='abandoned') a 30 dias (repository.go:572, Escalation.tsx:69 days:30). Son fuentes y ventanas distintas: confirmado. El cierre real por silencio del paciente (manager.go:343-366 checkEscalatedSessions -> 

#### [BAJA] Tiempos (avg_response = escalated_at->last_agent_msg_at; avg_return = escalated_at->resumed_at)
- **Captura:** avg_response = AVG(TIMESTAMPDIFF(MIN, escalated_at, last_agent_msg_at)) sobre atendidas; avg_return = AVG(... escalated_at, resumed_at) solo cuando resumed_at NOT NULL (repository.go:575-576). resumed_at solo se sella en ResumeSession (/bot resume/reset, session_repo.go:94); Complete (/bot cerrar) NO sella resumed_at (manager.go:203-205).
- **Almacenamiento:** sessions.resumed_at via ResumeSession; status='completed' via UpdateStatus (sin resumed_at).
- **Query/agregación:** Buckets de respuesta <5/5-14/15-59/>=60 con TIMESTAMPDIFF truncado, sin solapes ni huecos (repository.go:577-580). Bien.
- **Endpoint:** /api/escalation/sla.
- **UI:** StatCards 'Resp. al cliente' y 'Devolución al bot' + BarsChart de distribucion. Etiquetas del front dicen '5-15'/'15-60' pero el SQL es 5-14/15-59 (off-by-one cosmetico).
- **Problemas:**
  - Asimetria: 'returned' (conteo) incluye /bot cerrar (status='completed') pero avg_return_min lo EXCLUYE (solo resumed_at NOT NULL). El tiempo medio de devolucion ignora los cierres -> sesga avg_return.
  - last_agent_msg_at es la actividad del agente mas reciente, no la PRIMERA respuesta: en chats largos avg_response es cota superior, no tiempo de primera respuesta (documentado en models.go:208-210 pero no en la UI).
  - Etiquetas de buckets en UI (5-15/15-60) no coinciden con el SQL (5-14/15-59).
- **Recomendación:** Sellar resumed_at tambien en Complete (/bot cerrar) o excluir cierres de 'returned' para que conteo y promedio sean consistentes. Considerar sellar 'primera respuesta' aparte si se quiere TTFR real.
- **Verificado (real, low):** VERIFICADO. (1) Returned = SUM(last_agent_msg_at IS NOT NULL AND (resumed_at IS NOT NULL OR status='completed')) (repository.go:574) incluye cierres completed; pero AvgReturnMin = AVG(CASE WHEN resumed_at IS NOT NULL ...) (repository.go:576) los excluye -> conteo y promedio inconsistentes. (2) models.go:208-210 documenta que last_agent_msg_at es 'la actividad del agente' (cota superior, no TTFR); 

#### [OK] SLA por agente (agent_id asignado al escalar)
- **Captura:** agent_id/agent_name se sellan en escalation.go:147-148 (pickLeastLoadedAgent) y persisten via Save. GetEscalationSLAByAgent agrupa por agent_id, excluye '' (repository.go:488-490).
- **Almacenamiento:** sessions.agent_id/agent_name (migracion 029). idx_agent.
- **Query/agregación:** never_answered=total-answered, answered_not_returned=answered-returned (repository.go:505-506); returned acotado a atendidas para no ser negativo. MAX(agent_name) como etiqueta. LIMIT 50. Correcto.
- **Endpoint:** GET /api/escalation/sla-by-agent?days=30 (handlers.go:249).
- **UI:** Tabla con nota clara de que solo cubre escaladas con agente asignado (Escalation.tsx:195-198). Buena transparencia del denominador.
- **Problemas:**
  - Mismo guard expires_at>NOW() en TouchAgentActivity afecta 'answered' por agente (subconteo).
  - Escaladas asignadas solo a equipo (sin agente concreto) no aparecen: la suma por agente puede ser < total global del SLA; explicado en la nota pero no reconciliado numericamente.
- **Recomendación:** Mostrar tambien fila 'sin agente asignado' o el total para que el usuario reconcilie con el SLA global.

#### [OK] Escaladas (escalated_to_agent / flow step "escalated")
- **Captura:** escalation.go:149 emite flow "escalated" (observability.Emit) y :155 chat_event "escalated_to_agent". Solo en el camino de exito: si EscalateToAgent falla (escalation.go:100-116) se retorna ANTES, por lo que escalated_at/escalated NUNCA se sellan en fallos -> el denominador del SLA queda limpio. Bien.
- **Almacenamiento:** chat_events.event_type='escalated_to_agent' (con from_state/team_id/cups_code en JSON); flow_events(flow='escalacion',step='escalated',outcome='escalated'); sessions.escalated_at/escalated_team/agent_id/agent_name via Save (session_repo.go:126,135).
- **Query/agregación:** StatCard usa flow.by_step key 'escalated' (Escalation.tsx:105,77). repository.go:1135 GROUP BY step. Coincide con el step real.
- **Endpoint:** GET /api/flow-stats?flow=escalacion (handlers.go:53).
- **UI:** StatCard 'Escaladas' ultimos 7 dias. Correcto.

#### [OK] Recordatorios al agente (agent_reminder_sent)
- **Captura:** manager.go:393 chat_event + :399 flow step 'agent_reminder_sent'. Se emite en checkEscalatedSessions cuando el paciente espera y last_agent_msg_at no frena el aviso (manager.go:373-407). Cada msg del paciente resetea agent_reminders_sent (session_repo.go:222), abriendo nueva ventana. Logica coherente.
- **Almacenamiento:** chat_events 'agent_reminder_sent'; flow_events step 'agent_reminder_sent' outcome 'retry'; sessions.agent_reminders_sent (contador).
- **Query/agregación:** flow.by_step key 'agent_reminder_sent' (Escalation.tsx:106).
- **Endpoint:** /api/flow-stats.
- **UI:** StatCard 'Recordatorios' (icono BellRing).

**Notas del área:**
- _Faltantes:_ No hay KPI/visual del tiempo de PRIMERA respuesta del agente (TTFR): last_agent_msg_at se sobrescribe en cada saliente humano (session_repo.go:235), por lo que avg_response es la ultima respuesta, no la primera.; escalation_failed no se reconcilia con escaladas exitosas: no hay tasa de exito de handoff (failed / (failed+escalated)).; No se expone el conteo de escaladas 'en curso' (still_open) pese a calcularse (repository.go:573) -> el usuario no ve cuantas siguen abiertas ahora.; No hay desglose por equipo (escalated_team) ni por estado pre-escalacion en esta pagina, aunque el dato existe (chat_events from_state; GetTopEscalationStates ya implementado en repo pero no usado aqui).
- _Redundantes:_ EscalationSLA.Resolved/ResolvedPct/Expired/ExpiredPct/StillOpen se calculan en repository.go:572-597 y viajan en la respuesta pero NUNCA se pintan en Escalation.tsx (campos muertos en la UI).; 'Expiradas' aparece dos veces conceptualmente: StatCard flow (7d) y la rama SLA (30d, no mostrada).
- _Mejoras UI:_ Unificar ventanas temporales del bloque superior: hoy mezcla 7d (Escaladas/Recordatorios/Expiradas) con today (Handoff falló) y 30d (SLA). Mostrar el rango en cada tarjeta o homogeneizar.; Corregir el mapeo STEP_LABEL para que el BarsChart incluya devueltas (agent_resumed) y cerradas (agent_closed).; Traducir etiquetas del donut 'Resultado' (by_outcome) y de los buckets de tiempo (front dice 5-15/15-60; el SQL es 5-14/15-59).; Separar en el donut de escenarios 'en curso' (still_open) de 'sin atender' para no inflar el segmento rojo.; Documentar en la UI que avg_response usa la ultima actividad del agente (no la primera respuesta), como ya advierte models.go:208-210.


### Área: lista-espera (waiting_list)

#### [ALTA] Tasa de conversión (efectividad) = scheduled/total
- **Captura:** BOT NUNCA marca la entrada como 'scheduled'. En el booking exitoso desde lista de espera (internal/statemachine/handlers/slots.go:1352-1360) sólo se emite el evento chat_events 'waiting_list_booking_success' y una traza de observabilidad; NO se llama a waitingListRepo.UpdateStatus(wlID,'scheduled'). El worker sólo persiste eventos (internal/worker/pool.go:1124-1135), sin side-effect sobre waiting_list. Grep en todo internal/+cmd: el único lugar que escribe status='scheduled' es el seeder cmd/seed-kpis/main.go:534. Es decir, en datos reales una fila jamás llega a 'scheduled'.
- **Almacenamiento:** waiting_list.status (ENUM). Sólo se escriben realmente: waiting (Create), notified (MarkNotified, repo:229), declined (waiting_list.go:112), expired (waiting_list.go:139 + ExpireStaleNotified repo:243 + ExpireOld repo:254), duplicate_found (waiting_list_check.go:74). 'scheduled' es inalcanzable en prod.
- **Query/agregación:** internal/kpi/repository.go:926 eff.ConversionPct = scheduled/total. Como scheduled≈0 siempre, la conversión real es ~0%.
- **Endpoint:** GET /api/waiting-list/effectiveness (handlers.go:317) expone el dato pero alimentado por una columna que nunca se setea.
- **UI:** WaitingList.tsx:101,132-137 muestra '% Conversión' con hint 'X de Y inscritos agendaron'. En prod mostrará 0% permanentemente, induciendo a creer que la lista de espera no convierte. Con datos del seeder se ve correcto, lo que ENMASCARA el bug.
- **Problemas:**
  - status='scheduled' nunca se asigna en producción; sólo lo escribe el seeder (cmd/seed-kpis/main.go:534)
  - Efecto agravado: la entrada agendada queda en 'notified' y ExpireStaleNotified(24h) (repo:243) la voltea a 'expired' → una conversión real se contabiliza como expiración
  - Conversión real ≈ 0% siempre; el seeder oculta el problema
- **Recomendación:** En slots.go (donde hoy se emite waiting_list_booking_success, ~1352) llamar waitingListRepo.UpdateStatus(wlID,'scheduled') (que ya setea resolved_at=NOW(), repo:216). Así conversión, snapshot 'Programados', tiempo-a-agendar y conversión por CUPS quedan correctos y se evita la mis-expiración por ExpireStaleNotified.
- **Verificado (real, high):** Confirmado en BOT slots.go:1352 (solo evento, sin UpdateStatus), waiting_list_repo.go:215/229/243, scheduler/tasks.go:360, seed-kpis/main.go:533/542; DASHBOARD repository.go:911-926.

#### [ALTA] Tiempo medio a agendar (resolved_at - created_at)
- **Captura:** resolved_at SÓLO se setea en UpdateStatus/ExpireStaleNotified/ExpireOld (repo:216,243,254). En el booking exitoso no se llama UpdateStatus, así que para una entrada agendada ni status='scheduled' ni resolved_at se establecen.
- **Almacenamiento:** waiting_list.resolved_at TIMESTAMP NULL.
- **Query/agregación:** internal/kpi/repository.go:915-916 AVG(CASE WHEN status='scheduled' AND resolved_at IS NOT NULL THEN TIMESTAMPDIFF(HOUR,created_at,resolved_at) END). Doble condición que nunca se cumple en prod → COALESCE(...,0)=0.
- **Endpoint:** effectiveness.avg_hours_to_schedule siempre 0.
- **UI:** WaitingList.tsx:140 'Tiempo a agendar … h' mostrará 0 h siempre.
- **Problemas:**
  - Mismo root-cause que conversión: sin status='scheduled'+resolved_at en el booking, el promedio es 0 permanente
- **Recomendación:** Se corrige automáticamente al marcar 'scheduled' con resolved_at en el booking exitoso (ver KPI conversión).
- **Verificado (real, high):** DASHBOARD repository.go:915-916, 919-920; depende del mismo fix que conversión.

#### [ALTA] Conversión por CUPS (by_cups)
- **Captura:** Igual root-cause: SUM(status='scheduled') por CUPS ≈ 0 en prod.
- **Almacenamiento:** waiting_list (cups_code, status).
- **Query/agregación:** internal/kpi/repository.go:932-953 conv_pct = scheduled/total por CUPS. Además GROUP BY cups_code, cups_name puede fragmentar un mismo cups_code si cups_name varía entre filas. ORDER BY total DESC LIMIT 8 sin desempate determinista.
- **Endpoint:** effectiveness.by_cups OK estructuralmente.
- **UI:** WaitingList.tsx:109,146-148 HBarsChart con value=round(conversion_pct), label=cups_code. Problema adicional de presentación: no muestra volumen (total), así un CUPS con total=1 y 1 agendado pinta 100% al nivel de uno con total=50; engaña por falta de denominador.
- **Problemas:**
  - Conversión por CUPS ≈ 0 en prod por el bug de 'scheduled'
  - UI no expone el volumen (total) por CUPS → 100% espurios de CUPS con n=1
  - GROUP BY incluye cups_name (puede fragmentar el mismo código)
- **Recomendación:** Corregir 'scheduled' (root-cause). En UI mostrar total junto al % (ej. label 'cups_code (n=total)') o tooltip con scheduled/total; agrupar sólo por cups_code y tomar MAX(cups_name).
- **Verificado (real, high):** DASHBOARD repository.go:932-952 (GROUP BY cups_code, cups_name; SUM status='scheduled'); WaitingList.tsx:38,109 (EffByCups tiene total pero la UI solo grafica conversion_pct).

#### [MEDIA] Distribución por estado (donut de efectividad)
- **Captura:** Mismo bug: el segmento 'Programados' será ~0 y los agendados aparecerán en 'Expirados'.
- **Almacenamiento:** waiting_list.status. ENUM incluye 'duplicate_found' pero la query de efectividad (repo:911-918) cuenta COUNT(*) en total pero NO lo suma a ninguno de los 5 buckets → suma de buckets < total; los % usan un denominador que incluye duplicate_found (dilución).
- **Query/agregación:** repository.go:911-918.
- **Endpoint:** effectiveness expone los 5 estados; duplicate_found no se expone.
- **UI:** WaitingList.tsx:102-108 DonutChart mezcla estados ABIERTOS (waiting/notified) con TERMINALES (scheduled/declined/expired). Para una vista de 'Resultado de los inscritos' los abiertos inflan el donut y no son outcomes.
- **Problemas:**
  - 'Programados' ~0 y agendados desviados a 'Expirados' (root-cause)
  - duplicate_found incluido en denominador de % pero ausente de buckets/donut
  - Donut mezcla estados abiertos con resultados terminales bajo subtítulo 'Resultado de los inscritos'
- **Recomendación:** Corregir 'scheduled'. Excluir duplicate_found del denominador (o sumarlo a un bucket 'duplicado'). Considerar separar 'abiertos' vs 'resueltos' o anotar que waiting/notified aún no tienen resultado.
- **Verificado (real, medium):** BOT waiting_list_check.go:74 (escribe 'duplicate_found'); DASHBOARD repository.go:911-918 (total=COUNT(*) incluye duplicate_found; buckets solo 5 status); WaitingList.tsx:102-108,143.

#### [MEDIA] Conteos por status (snapshot StatCards)
- **Captura:** OK para waiting/notified/declined/expired; 'Programados' afectado por el bug de 'scheduled'.
- **Almacenamiento:** waiting_list.status.
- **Query/agregación:** internal/kpi/repository.go:818-824 la query de stats usa el MISMO 'where' que la lista filtrada. Al aplicar filtro de estado/búsqueda, los SUM(status='...') de los demás estados quedan en 0.
- **Endpoint:** GET /api/waiting-list (handlers.go:200) devuelve stats junto con la página filtrada.
- **UI:** WaitingList.tsx:116-123 los 6 StatCards se alimentan de data.stats (filtrado). Al elegir Estado='Notificado' en el filtro, las tarjetas 'Esperando/Programados/Declinados' muestran 0, dando una foto global falsa. Además no hay tarjeta para 'Expirados' (sólo aparece en el donut).
- **Problemas:**
  - Las tarjetas de snapshot heredan el WHERE del filtro → seleccionar un estado pone en 0 los demás (foto global engañosa)
  - 'Programados' ~0 por el bug de 'scheduled'
  - No hay StatCard para 'Expirados'
- **Recomendación:** Calcular las stats de snapshot con un WHERE global (sin status/search/paginación) o en endpoint aparte; o relabelar como 'del filtro actual'. Agregar tarjeta Expirados.
- **Verificado (real, medium):** DASHBOARD repository.go:818-824 (stats comparten el where del filtro/paginación); WaitingList.tsx:90,117-122 (sin tarjeta Expirados).

#### [BAJA] Por expirar (expiring_soon < 7 días)
- **Captura:** OK.
- **Almacenamiento:** waiting_list.expires_at.
- **Query/agregación:** internal/kpi/repository.go:823 SUM(status='waiting' AND expires_at < NOW()+7d). La condición incluye expires_at YA en el pasado (también es < NOW()+7d), por lo que cuenta entradas que ya deberían estar 'expired' pero que ExpireOld aún no ha barrido.
- **Endpoint:** stats.expiring_soon.
- **UI:** WaitingList.tsx:122 hint '< 7 días' pero realmente es 'expira en <7d o ya venció'.
- **Problemas:**
  - Incluye expires_at ya vencido como 'por expirar' (ventana sin cota inferior NOW())
- **Recomendación:** Acotar con expires_at BETWEEN NOW() AND NOW()+7d para excluir vencidos.
- **Verificado (real, low):** DASHBOARD repository.go:823; el job ExpireOld corre diario (BOT tasks.go:351/419), dejando ventanas con waiting vencidos.

#### [BAJA] Días promedio en espera (avg_days_waiting) y columna Días
- **Captura:** OK.
- **Almacenamiento:** created_at.
- **Query/agregación:** avg: repository.go:822 AVG(CASE WHEN status='waiting' THEN DATEDIFF(NOW(),created_at)) — correcto (sólo activos). Lista: repository.go:863 days_waiting = DATEDIFF(NOW(),created_at) para TODAS las filas, incl. resueltas (declined/scheduled/expired), por lo que sigue creciendo eternamente para entradas ya cerradas.
- **Endpoint:** stats.avg_days_waiting y entry.days_waiting.
- **UI:** WaitingList.tsx:80 columna 'Días' y :121 tarjeta 'Días prom.'. La columna muestra días desde inscripción aunque la fila ya esté resuelta hace semanas.
- **Problemas:**
  - entry.days_waiting usa NOW() en vez de resolved_at para filas resueltas → valor sin sentido en resueltas
- **Recomendación:** Para filas resueltas usar DATEDIFF(COALESCE(resolved_at,NOW()),created_at).
- **Verificado (real, low):** DASHBOARD repository.go:863 (days_waiting con NOW() para todas las filas); WaitingList.tsx:28,80. El avg agregado (repository.go:822) solo cuenta status='waiting', por lo que esa parte concreta no está sesgada.

#### [BAJA] Métrica de joins por evento (waiting_list_joined, Overview/daily)
- **Captura:** Hay DOS rutas de inscripción: interactiva emite 'waiting_list_joined' (slots.go:833,843) y auto-inscripción (sin cupos) emite 'waiting_list_auto_joined' (slots.go:702,712). event_repo.GetDailyKPIs sólo cuenta 'waiting_list_joined' (event_repo.go:266) → subcuenta las auto-inscripciones.
- **Almacenamiento:** chat_events.event_type.
- **Query/agregación:** internal/repository/local/event_repo.go:266 y dashboard internal/kpi/repository.go:125.
- **Endpoint:** KPIs diarios (no la página WaitingList, que lee la TABLA y sí captura ambas rutas vía Create).
- **UI:** Afecta Overview, no WaitingList.tsx.
- **Problemas:**
  - El conteo de 'joined' por evento omite 'waiting_list_auto_joined' → subcuenta enrolamientos en KPIs diarios
- **Recomendación:** Sumar también 'waiting_list_auto_joined' al contador de joins, o unificar el event_type al inscribir.
- **Verificado (real, low):** BOT slots.go:702,712 (auto_joined) vs 833,843 (joined); event_repo.go:266 y DASHBOARD repository.go:537 solo mapean waiting_list_joined.

**Notas del área:**
- _Faltantes:_ No existe StatCard para 'Expirados' en el snapshot (sólo en el donut), aunque sí está en el filtro de estado; No se muestra el conteo 'duplicate_found' en ningún lado pese a ser un estado real del ENUM (waiting_list_check.go:74); La página no expone tendencia temporal (joins/conversión por día) — sólo snapshot + agregado de 30d; falta serie de tiempo para ver evolución; El endpoint /api/waiting-list/filters (handlers.go:334) provee cups/entidades para dropdowns pero la UI WaitingList.tsx no los usa (no hay filtros por CUPS ni entidad en la página, pese a existir en backend WaitingListFilters)
- _Redundantes:_ Coexisten dos fuentes para 'agendados desde lista de espera': el evento chat_events 'waiting_list_booking_success' (que SÍ se emite y funciona, usado en KPIs diarios) y la columna waiting_list.status='scheduled' (que nunca se escribe). La página WaitingList usa la columna rota; convendría unificar marcando la columna en el booking (preferible) para que ambas fuentes concuerden
- _Mejoras UI:_ Separar claramente el snapshot global (no filtrado) de la tabla filtrada; hoy las 6 tarjetas superiores cambian al filtrar y dan una foto global falsa; En 'Conversión por CUPS' mostrar el volumen (total) junto al %; un CUPS con n=1 no debe pintar igual que uno con n=50. Usar tooltip scheduled/total o label 'codigo (n)'; El donut de efectividad mezcla estados abiertos (waiting/notified) con resultados terminales; etiquetar o separar 'abiertos vs resueltos'; Aclarar la columna 'Días': para filas resueltas debería reflejar tiempo hasta la resolución, no días desde inscripción; Aprovechar /api/waiting-list/filters para añadir filtros por CUPS y entidad en la página (ya existen en backend); CRÍTICO de credibilidad: el seeder (cmd/seed-kpis) genera filas 'scheduled' que hacen ver la página correcta en demo, pero con datos reales la sección de Efectividad mostrará 0% en todo. Priorizar el fix de status='scheduled' en el booking antes de confiar en estas métricas


### Área: ocr-clinica

#### [ALTA] ocr_attempts
- **Captura:** Bot emite por intento UNO de: ocr_success (medical_order.go:177), ocr_failed (medical_order.go:169, solo cuando OCR corre pero no halla CUPS), o ocr_error/ocr_timeout (medical_order.go:151-160, cuando AnalyzeDocument falla o expira). Es decir, los fallos 'duros' del OCR se persisten en chat_events como event_type='ocr_error'/'ocr_timeout', NO como 'ocr_failed'.
- **Almacenamiento:** chat_events.event_type con 4 valores distintos: ocr_success, ocr_failed, ocr_error, ocr_timeout. Un evento por intento.
- **Query/agregación:** mapEventToKPI (repository.go:86-90 dashboard, espejo en event_repo.go:227-231 del bot): OCRAttempts += count solo para 'ocr_success' y 'ocr_failed'. ocr_error y ocr_timeout NO se suman. Por tanto ocr_attempts está SUBCONTADO: excluye todos los fallos de análisis/timeout.
- **Endpoint:** /api/ocr -> GetDailyKPIs(date) (handlers.go:358). Expone el dato subcontado.
- **UI:** Ocr.tsx:41 StatCard 'Intentos OCR' = k.ocr_attempts. Muestra un denominador incompleto.
- **Problemas:**
  - ocr_error/ocr_timeout (fallo real del OCR) nunca entran al conteo de attempts: el agregador solo conoce ocr_success+ocr_failed (repository.go:86-90).
  - Consecuencia: 'Intentos OCR' subestima la carga real de OCR y, peor, infla la tasa de éxito (ver KPI tasa).
  - El += sobre OCRAttempts en sí es correcto (acumula éxitos+fallos); el problema es la taxonomía de eventos incompleta, no el operador.
- **Recomendación:** Unificar la emisión: que el path de error/timeout emita 'ocr_failed' a chat_events (manteniendo el detalle en event_data.error/reason), o bien que mapEventToKPI sume también 'ocr_error' y 'ocr_timeout' a OCRAttempts. Aplicar el mismo arreglo en el bot (event_repo.go) y en los trends de /api/ocr (handlers.go:361 solo pide ocr_success/ocr_failed).
- **Verificado (real, high):** CONFIRMADO. El bot en medical_order.go:148-160 emite el chat_event con event_type='ocr_error' o 'ocr_timeout' (linea 151/154 via WithEvent(eventType,...)) cuando AnalyzeDocument devuelve err!=nil (excepcion/timeout real del OCR). El observability.Emit('ocr_failed') de la linea 156 va a flow_events (tracer), NO a chat_events. Ni el dashboard (repository.go:86-90) ni el bot (event_repo.go:227-231) m

#### [ALTA] tasa de éxito (rate)
- **Captura:** Derivada en frontend, no es evento.
- **Almacenamiento:** n/a (calculada).
- **Query/agregación:** Ocr.tsx:25: rate = round(ocr_successes / ocr_attempts * 100), con guardia ocr_attempts>0 (sin división por cero). Pero ocr_attempts NO incluye ocr_error/ocr_timeout (ver KPI ocr_attempts).
- **Endpoint:** n/a (cálculo en cliente).
- **UI:** Ocr.tsx:43 StatCard 'Tasa de éxito'.
- **Problemas:**
  - El denominador (ocr_attempts) excluye fallos de análisis y timeouts, por lo que la tasa de éxito queda SOBRESTIMADA: si hubo muchos ocr_error/timeout, el % mostrado es artificialmente alto.
  - Mismo periodo afectado por el mismatch de ventana (ver notas de área): éxitos/intentos son de 1 día.
- **Recomendación:** Corregir el denominador (incluir ocr_error/ocr_timeout en attempts). División por cero ya está protegida; el cálculo en sí es correcto una vez arreglada la captura.
- **Verificado (real, high):** CONFIRMADO. Ocr.tsx:25 calcula rate = ocr_successes / ocr_attempts. Como attempts = ocr_success + ocr_failed (sin ocr_error/ocr_timeout), el denominador es menor que la carga real y el % sale inflado. Division por cero protegida con 'k.ocr_attempts > 0'. Nota secundaria del problema tambien valida: en handlers.go OCR() los kpis (incl. exitos/intentos) son de un solo dia (GetDailyKPIs(date)) mientr

#### [MEDIA] gfr_blocked
- **Captura:** No existe evento chat_events 'gfr_blocked'. El bloqueo por GFR bajo se emite a flow_events como 'gfr_blocked' (medical_validation.go:411, observability.Emit) y a chat_events como 'gfr_not_eligible' (medical_validation.go:425). No hay WithEvent('gfr_blocked') a chat_events.
- **Almacenamiento:** El campo DailyKPIs.GFRBlocked se alimenta de event_type='pregnant_blocked' (mislabel).
- **Query/agregación:** repository.go:93-94 y event_repo.go:234-235: case 'pregnant_blocked': k.GFRBlocked += count. El campo llamado GFRBlocked en realidad cuenta EMBARAZADAS bloqueadas, no GFR.
- **Endpoint:** /api/ocr expone kpis.gfr_blocked (= conteo de pregnant_blocked).
- **UI:** No se renderiza en Ocr.tsx (la página muestra max_retries, no gfr_blocked), aunque está tipado en Resp.kpis.gfr_blocked (Ocr.tsx:14). Dato fetched, mal etiquetado y no usado.
- **Problemas:**
  - Campo GFRBlocked mapeado desde 'pregnant_blocked' (repository.go:93-94): nombre miente sobre su contenido.
  - Si algún consumidor usa kpis.gfr_blocked creyendo que es GFR, obtiene embarazos. Latente porque la página no lo pinta.
  - El verdadero bloqueo por GFR ya está en block_reasons.gfr_not_eligible, así que GFRBlocked es redundante además de mal nombrado.
- **Recomendación:** Eliminar el campo GFRBlocked o re-mapearlo a un evento real (gfr_not_eligible) y dejar pregnant_blocked exclusivamente en block_reasons. Aplicar en dashboard y bot.
- **Verificado (real, medium):** CONFIRMADO. repository.go:93-94 (y bot event_repo.go:234-235): case 'pregnant_blocked': k.GFRBlocked += count. El campo JSON es gfr_blocked pero lo alimenta el conteo de embarazo. El bloqueo real por GFR vive en block_reasons.gfr_not_eligible (repository.go:738-739), asi que GFRBlocked es ademas redundante. Latente: Ocr.tsx no pinta gfr_blocked (no esta en el interface Resp linea 14, ningun StatCa

#### [MEDIA] existing_appointment (cita existente)
- **Captura:** Un único bloqueo por cita existente emite DOS eventos en el mismo flujo: checkExistingHandler emite 'existing_appointment_found' (medical_validation.go:505) y transiciona a StateAppointmentExists, cuyo handler emite 'appointment_exists_blocked' (medical_validation.go:517).
- **Almacenamiento:** chat_events: dos filas por cada paciente bloqueado.
- **Query/agregación:** GetBlockReasons repository.go:742-743: case 'existing_appointment_found','appointment_exists_blocked': b.ExistingAppointment += cnt. Suma AMBOS => duplica el conteo real (~2x).
- **Endpoint:** /api/ocr block_reasons.existing_appointment (inflado 2x).
- **UI:** Ocr.tsx:32 barra 'Cita existente'. Muestra el doble del valor real.
- **Problemas:**
  - Doble conteo: ambos event_type se emiten siempre juntos en el mismo flujo y el SQL los suma (repository.go:742-743).
- **Recomendación:** Contar solo uno de los dos eventos (p.ej. 'existing_appointment_found', que es el disparador único), o usar COUNT(DISTINCT session_id). No sumar ambos.
- **Verificado (real, medium):** CONFIRMADO. repository.go:742-743 suma existing_appointment_found + appointment_exists_blocked en b.ExistingAppointment. En el bot medical_validation.go: checkExistingHandler (linea 501-505) emite 'existing_appointment_found' y transiciona a StateAppointmentExists; el handler de ese estado, appointmentExistsHandler (linea 513-517), es automatico y emite 'appointment_exists_blocked'. Por tanto siem

#### [OK] pregnant_blocked
- **Captura:** Emitido una vez en pregnancyBlockHandler (medical_validation.go:242, WithEvent('pregnant_blocked')).
- **Almacenamiento:** chat_events.event_type='pregnant_blocked'.
- **Query/agregación:** GetBlockReasons repository.go:736-737: b.PregnantBlocked = cnt (ventana from..to). Correcto.
- **Endpoint:** /api/ocr block_reasons.pregnant_blocked.
- **UI:** Ocr.tsx:29 barra 'Embarazo' en BarsChart. Correcto.
- **Problemas:**
  - Nota: el mismo evento se cuenta ADEMÁS en DailyKPIs.GFRBlocked (doble uso del evento), pero ese campo no se muestra; sin impacto visible en esta página.
- **Recomendación:** Sin cambio en el conteo de block_reasons. Resolver el doble uso al arreglar GFRBlocked.

#### [OK] coverage_no_convenio
- **Captura:** Emitido en dos paths del flujo de slots: slots.go:268 y slots.go:1188 (WithEvent('coverage_no_convenio',{cup})). Son ramas distintas (no se ejecutan ambas en el mismo intento).
- **Almacenamiento:** chat_events.event_type='coverage_no_convenio'.
- **Query/agregación:** GetBlockReasons repository.go:741-742: b.NoConvenio = cnt. Correcto.
- **Endpoint:** /api/ocr block_reasons.no_convenio.
- **UI:** Ocr.tsx:31 barra 'Sin convenio'. Correcto.
- **Problemas:**
  - El evento se emite por CUP (attr cup), no por sesión; si una orden tiene varios CUPS sin convenio podría sumar >1 por paciente. Es un conteo de 'motivos', así que es defendible, pero conviene tenerlo claro en la etiqueta.
- **Recomendación:** Sin cambio funcional. Opcional: documentar que el conteo es por CUP, no por paciente.

#### [OK] max_retries_reached
- **Captura:** Emitido al agotar reintentos en helpers.go:52, :106 y :129 (ValidateWithRetry / ValidateButtonResponse / RetryOrEscalate). Puede dispararse varias veces por sesión (en estados distintos).
- **Almacenamiento:** chat_events.event_type='max_retries_reached'.
- **Query/agregación:** repository.go:97-98 MaxRetriesReached = count. Correcto como conteo de eventos.
- **Endpoint:** /api/ocr kpis.max_retries_reached.
- **UI:** Ocr.tsx:45 StatCard 'Max reintentos' (intent warning).
- **Problemas:**
  - No es un KPI específico de OCR/clínico: es genérico de cualquier validación con reintentos. Su ubicación en la página de OCR es discutible (puede confundir; cuenta reintentos de cualquier estado, no de OCR).
- **Recomendación:** Mantener el conteo. Considerar moverlo a la página de sesiones/embudo o etiquetarlo como 'reintentos agotados (global)' para no sugerir que son reintentos de OCR.

#### [BAJA] out_of_hours
- **Captura:** Emitido en greeting.go:59 cuando la sesión entra fuera de horario. Correcto y una vez por entrada.
- **Almacenamiento:** chat_events.event_type='out_of_hours'; mapeado a DailyKPIs.OutOfHoursAttempts (repository.go:95-96).
- **Query/agregación:** OK, OutOfHoursAttempts = count.
- **Endpoint:** /api/ocr SÍ devuelve kpis.out_of_hours_attempts (va dentro de GetDailyKPIs).
- **UI:** NO se renderiza en Ocr.tsx (no hay StatCard para out_of_hours). El dato llega pero no se muestra.
- **Problemas:**
  - out_of_hours está en el alcance del área pero no se pinta en la página; además es un KPI de saludo/menú, no de OCR ni de validación clínica.
  - Dato presente en el payload pero huérfano en UI.
- **Recomendación:** Decidir su hogar: o se muestra (StatCard 'Fuera de horario') o se mueve a la página de sesiones/greeting. Hoy es payload muerto en esta vista.
- **Verificado (real, low):** CONFIRMADO. repository.go:95-96 (y bot event_repo.go:236-237) mapean out_of_hours -> OutOfHoursAttempts (json out_of_hours_attempts), que viaja en el payload kpis de /api/ocr (handlers.go:358-368). Ocr.tsx no lo declara en su interface Resp (linea 13-16) ni lo renderiza en ningun StatCard. Payload presente pero huerfano en esta vista, como afirma el problema.

#### [OK] ocr_success
- **Captura:** Emitido una vez por OCR exitoso con CUPS>0 (medical_order.go:177, WithEvent('ocr_success',{cups_count})).
- **Almacenamiento:** chat_events.event_type='ocr_success', 1 por intento exitoso.
- **Query/agregación:** repository.go:88 OCRSuccesses = count (asignación directa, correcta: solo hay un event_type 'ocr_success' por grupo, no requiere +=).
- **Endpoint:** /api/ocr kpis.ocr_successes.
- **UI:** Ocr.tsx:42 StatCard 'Éxitos'. Correcto.
- **Recomendación:** Sin cambios. El uso de '=' en OCRSuccesses (vs '+=' en OCRAttempts) es correcto: 'ocr_success' es un único event_type, no se acumula con otro.

#### [OK] gfr_calculations
- **Captura:** Emitido una vez por cada cálculo de GFR en medical_validation.go:403, ANTES del check de elegibilidad, así que cuenta todos los cálculos (elegibles y no).
- **Almacenamiento:** chat_events.event_type='gfr_calculated'.
- **Query/agregación:** repository.go:91-92 GFRCalculations = count. Correcto.
- **Endpoint:** /api/ocr kpis.gfr_calculations.
- **UI:** Ocr.tsx:44 StatCard 'GFR calculados'. Correcto.
- **Recomendación:** Sin cambios.

#### [OK] gfr_not_eligible
- **Captura:** Emitido una vez en gfrNotEligibleHandler (medical_validation.go:425) cuando GFR<30.
- **Almacenamiento:** chat_events.event_type='gfr_not_eligible'.
- **Query/agregación:** GetBlockReasons repository.go:739-740: b.GFRNotEligible = cnt. Correcto.
- **Endpoint:** /api/ocr block_reasons.gfr_not_eligible.
- **UI:** Ocr.tsx:30 barra 'GFR bajo'. Correcto.
- **Recomendación:** Sin cambios.

**Notas del área:**
- _Faltantes:_ Trend chart: /api/ocr ya devuelve trends de ocr_success/ocr_failed (handlers.go:361-363) pero Ocr.tsx NO los usa (solo consume kpis y block_reasons). Falta un gráfico de tendencia de OCR/tasa de éxito a 30 días, que es lo más útil para esta área.; Los trends, además, solo piden ocr_success/ocr_failed: aunque se pintaran, omitirían ocr_error/ocr_timeout (mismo defecto de taxonomía que ocr_attempts).; out_of_hours_attempts llega en el payload pero no tiene tarjeta en la página.; No hay desglose de fallos de OCR por causa (no-CUPS vs error vs timeout). Sería valioso para diagnosticar calidad de imágenes vs problemas del proveedor OCR.
- _Redundantes:_ kpis.gfr_blocked se fetchea y tipa en Resp (Ocr.tsx:14) pero no se renderiza y además está mal mapeado (= pregnant_blocked).; trends en la respuesta de /api/ocr nunca se consumen por el frontend.; max_retries_reached y out_of_hours son métricas globales/de saludo embebidas en la vista OCR; redundan con páginas de sesiones.
- _Mejoras UI:_ MISMATCH DE VENTANA TEMPORAL (medio): los StatCards (intentos/éxitos/tasa/gfr/max_retries) salen de GetDailyKPIs(date) = 1 SOLO DÍA, mientras el donut/barras de bloqueos usa GetBlockReasons(from=daysAgo(30), to=date) = 30 DÍAS (Ocr.tsx:22, handlers.go:358-359). El subtítulo dice 'Lectura de órdenes — {date}' (un día), induciendo a leer todo como del mismo día. Unificar el periodo o etiquetar explícitamente cada bloque con su ventana.; La tasa de éxito debería mostrar el denominador explícito (p.ej. '83% (45/54 intentos)') para que el usuario no la interprete sobre sesiones.; El gráfico de bloqueos es BarsChart simple; al ser categorías excluyentes de 'por qué se bloqueó', un donut o barras con total y % por motivo comunicaría mejor la proporción.; Añadir tendencia (línea) de tasa de éxito de OCR usando los trends ya disponibles (tras corregir la taxonomía de fallos).


### Área: pacientes (Patients.tsx)

#### [ALTA] total_sessions mostrado como "Docs ingresados"
- **Captura:** El dato es session_started, NO documentos. session_started se emite al iniciar sesión. El verdadero evento de documento ingresado existe y es document_entered (identification.go:91, askDocumentHandler) pero NO se usa aquí.
- **Almacenamiento:** chat_events.event_type='session_started'. document_entered se almacena pero nunca se agrega en daily KPIs (mapEventToKPI no tiene case para document_entered).
- **Query/agregación:** repository.go:70-71 mapea session_started -> TotalSessions. El SQL es correcto para 'sesiones', pero NO calcula lo que el rótulo promete (documentos).
- **Endpoint:** handlers.go devuelve kpis.total_sessions. El dato es íntegro pero semánticamente no es 'docs'.
- **UI:** Patients.tsx:33 StatCard label="Docs ingresados" icon=FileText value=k.total_sessions. Engañoso: muestra el total de sesiones iniciadas, no documentos. Además 'docs' es ambiguo (¿cédula? ¿orden médica/imagen OCR?).
- **Problemas:**
  - Rótulo "Docs ingresados" cableado a total_sessions (session_started) en Patients.tsx:33 — métrica equivocada y engañosa.
  - Existe document_entered (identification.go:91) que sería el conteo real de documentos (cédula) ingresados, pero no se agrega en mapEventToKPI ni se expone.
- **Recomendación:** Opción A (rápida): renombrar a "Sesiones iniciadas". Opción B (correcta): agregar case 'document_entered' en mapEventToKPI, exponer un campo nuevo (p.ej. documents_entered) y usarlo en la card. Aclarar si 'documentos' = cédula (document_entered) u órdenes médicas/imágenes (otro evento del flujo OCR).
- **Verificado (real, high):** CONFIRMADO en código real. Patients.tsx:33 → StatCard label="Docs ingresados" value={k?.total_sessions}. repository.go:70-71 → mapEventToKPI mapea 'session_started' a k.TotalSessions, así que total_sessions = sesiones iniciadas, no documentos. identification.go:91 → askDocumentHandler emite WithEvent("document_entered", {doc_length: len(input)}); 'input' es el número de documento (cédula) del paci

#### [ALTA] top_entities ("Top entidades / convenios")
- **Captura:** client_type_selected se emite en entity_management.go:76/91/98 con {type: label, category}. PROBLEMA semántico: label = EntityCategoryLabels (procedure.go:55-62) = PARTICULAR/EPS/PREPAGADA/REGIMEN ESPECIAL/ARL/POLIZA — es la CATEGORÍA, no la entidad/convenio específico (SANITAS, SALUD TOTAL...). La entidad real está en selected_entity_name / contract_resolved. Además puede emitirse varias veces por sesión si el usuario reingresa (3 puntos de emisión) -> leve doble conteo.
- **Almacenamiento:** chat_events.event_type='client_type_selected', $.type guardado en event_data JSON. Correcto a nivel de almacenamiento.
- **Query/agregación:** repository.go:690-713 GetTopEntities agrupa por JSON $.type de client_type_selected en ventana [from,to]. El SQL funciona, pero agrega CATEGORÍAS (6 valores fijos), no entidades/convenios específicos.
- **Endpoint:** handlers.go:388,394 devuelve la lista bajo la clave "entities".
- **UI:** Patients.tsx:13-16,25 el tipo Resp declara y lee data.top_entities; el backend devuelve "entities". MISMATCH de clave -> data.top_entities siempre undefined -> entities=[] -> el ChartCard SIEMPRE muestra <EmptyState/>. La gráfica está permanentemente vacía en producción.
- **Problemas:**
  - BUG bloqueante: clave de respuesta "entities" (handlers.go:394) vs lectura "top_entities" (Patients.tsx:15,25). La gráfica nunca renderiza datos.
  - BUG semántico: aun corrigiendo la clave, muestra TIPOS de entidad (PARTICULAR/EPS/...), no entidades/convenios concretos; el título "Top entidades / convenios" es incorrecto.
  - Desfase de ventana no señalizado: la gráfica usa from=daysAgo(30)..to=date (30 días) mientras las StatCards usan 1 día; el subtítulo solo muestra la fecha.
- **Recomendación:** 1) Alinear la clave: o renombrar el retorno del handler a top_entities, o leer data.entities en el frontend. 2) Para mostrar entidad/convenio real, alimentar la query desde el evento que lleva el nombre específico (selected_entity_name / contract_resolved) en vez de client_type_selected.$.type; si se quiere CATEGORÍA, retitular a "Top tipos de entidad". 3) Indicar en el título/subtítulo que la gráfica cubre 30 días.
- **Verificado (real, high):** CONFIRMADO. (1) Mismatch de clave: Patients.tsx:15 declara top_entities?, línea 25 lee data?.top_entities; el handler Patients en internal/kpi/handlers.go:388,396 retorna jsonOK con clave "entities" (no "top_entities"). La gráfica siempre cae en EmptyState. El issue citó handlers.go:394 (aprox.; la línea exacta del return es 396, dentro del handler Patients). (2) Semántica: GetTopEntities (reposit

#### [OK] out_of_hours_attempts ("Fuera de horario")
- **Captura:** greeting.go:59 emite out_of_hours una vez al saludar fuera de horario; greeting.go:90 emite out_of_hours_menu_shown (evento distinto, sin doble conteo).
- **Almacenamiento:** chat_events.event_type='out_of_hours'. Correcto.
- **Query/agregación:** repository.go:95-96 mapea out_of_hours -> OutOfHoursAttempts, COUNT(*) del día. Correcto.
- **Endpoint:** handlers.go devuelve kpis.out_of_hours_attempts. Correcto.
- **UI:** Patients.tsx:34 StatCard intent=warning. Correcto.
- **Problemas:**
  - Pertenece conceptualmente al área de sesiones/menú, no a 'pacientes'; su ubicación en esta página es discutible.
- **Recomendación:** Dejar el cálculo como está; evaluar mover la card a la página de Sesiones para coherencia temática.

#### [OK] no_slots_found ("Sin slots")
- **Captura:** slots.go emite no_slots_found cuando no hay disponibilidad. Correcto.
- **Almacenamiento:** chat_events.event_type='no_slots_found'. Correcto.
- **Query/agregación:** repository.go:135-136 mapea -> NoSlotsFound, COUNT(*) del día. Correcto.
- **Endpoint:** handlers.go devuelve kpis.no_slots_found. Correcto.
- **UI:** Patients.tsx:35 StatCard intent=warning. Correcto.
- **Problemas:**
  - Es un KPI de agendamiento/disponibilidad, no de 'pacientes'; también se refleja en SessionOutcomes.NoSlots (repository.go:532) -> presentación duplicada en distintas páginas.
- **Recomendación:** Mantener el cálculo; considerar moverlo a Agendamiento para evitar dispersión del mismo dato.

#### [OK] patients_registered ("Registrados")
- **Captura:** internal/statemachine/handlers/registration.go:891 emite WithEvent("registration_success", {patient_id}) SOLO tras patientSvc.Create exitoso (registration.go:849). Momento y lugar correctos; el camino de error emite registration_failed (registration.go:861). Sin huecos.
- **Almacenamiento:** chat_events.event_type='registration_success' en MySQL neuro_bot. Correcto.
- **Query/agregación:** neuro-dashboard/internal/kpi/repository.go:84-85 mapEventToKPI mapea registration_success -> PatientsRegistered; GetDailyKPIs (repository.go:32-34) usa rango sargable [date, date+1d) con COUNT(*). Correcto, sin doble conteo.
- **Endpoint:** handlers.go:387,394 /api/patients devuelve kpis.patients_registered. Correcto.
- **UI:** Patients.tsx:32 StatCard "Registrados" (1 día). Correcto.
- **Recomendación:** Solo claridad: el rótulo "Registrados" cuenta pacientes NUEVOS creados (registration_success), no pacientes ya existentes encontrados (patient_found). Considerar "Nuevos registros" para evitar que se interprete como total de pacientes atendidos.

**Notas del área:**
- _Faltantes:_ registration_started (registration.go:238) y registration_failed (registration.go:861) se capturan y almacenan pero NO se muestran en ninguna parte de Patients.tsx; falta un embudo de registro (started -> success/failed) que revele abandono y tasa de fallo de creación de paciente.; patient_found / patient_not_found: el endpoint /api/patients ya pide estas tendencias (handlers.go:391) pero el frontend las ignora por completo (Resp no tiene campo trends). La identificación (encontrado vs no encontrado) es central al área 'pacientes' y no se visualiza.; No existe un KPI real de 'documentos ingresados': document_entered (identification.go:91) no se agrega en mapEventToKPI ni se expone.; La página no muestra ninguna línea de tendencia pese a que el endpoint entrega 3 series de 30 días.
- _Redundantes:_ El endpoint /api/patients devuelve trends (registration_success, patient_found, patient_not_found) que Patients.tsx nunca consume -> payload muerto.; out_of_hours_attempts y no_slots_found son KPIs de sesión/agendamiento colocados en la página de pacientes; no_slots_found además ya se refleja en SessionOutcomes.
- _Mejoras UI:_ Corregir el mismatch de clave entities/top_entities para que la gráfica deje de salir vacía (fix de mayor impacto).; Re-rotular "Docs ingresados" para que refleje lo que realmente cuenta (sesiones) o cablearlo a document_entered.; Renombrar "Top entidades / convenios" a "Top tipos de entidad" o alimentarlo del evento de entidad específica.; Añadir un embudo/donut de registro (started/success/failed) y un donut paciente encontrado vs no encontrado usando las tendencias ya disponibles.; Hacer explícito que la gráfica de entidades es ventana de 30 días mientras las StatCards son de 1 día (hoy el subtítulo solo dice la fecha).


### Área: salud-sistema (Health)

#### [MEDIA] db_latency
- **Captura:** repository.go:760-767: mide PingContext contra la BD MySQL del DASHBOARD y devuelve ms; en fallo devuelve -1 (correccion ya aplicada, AUDITORIA-DASHBOARD.md:248). Mide solo la conexion del dashboard, NO la del bot ni SIESA.
- **Almacenamiento:** No se persiste; calculo en vivo. No hay historico para distinguir un pico puntual de degradacion sostenida.
- **Query/agregación:** PingContext con time.Since en ms (float64). Correcto. -1 como senal de caida es razonable.
- **Endpoint:** GET /api/health -> db_latency_ms. Correcto.
- **UI:** BUG en Health.tsx:31 -> intent={latency > 100 ? 'danger' : 'success'}. Cuando la BD esta caida el backend manda -1; como -1 > 100 es false, la tarjeta se pinta VERDE 'success' mostrando '-1.0 ms'. La senal de degradacion del backend se anula en la UI: una BD caida aparece sana. Ademas la etiqueta 'Latencia BD' no aclara que es la BD del dashboard, no SIESA ni el bot.
- **Problemas:**
  - UI invierte la senal: latency=-1 (BD caida) cae en la rama 'success' (verde) y muestra '-1.0 ms' como si estuviera sano (Health.tsx:31).
  - Solo mide la MySQL del dashboard; no cubre la BD del bot ni SIESA pese a que la pagina se titula salud del sistema.
  - Sin umbral intermedio ni historico: solo verde/rojo a 100 ms, sin tendencia.
- **Recomendación:** Tratar latency < 0 como danger explicito (p.ej. intent = latency < 0 ? 'danger' : latency > 100 ? 'danger' : 'success') y mostrar 'BD no disponible' en vez de '-1.0 ms'. Aclarar en la etiqueta que es la BD del dashboard.
- **Verificado (real, medium):** CONFIRMADO. (1) frontend/src/pages/Health.tsx:31 `intent={latency > 100 ? "danger" : "success"}`: con latency=-1, `-1 > 100` es false -> 'success' (verde); y linea 29 `value={`${latency.toFixed(1)} ms`}` -> muestra '-1.0 ms' como sano. El backend (repository.go:761-765) pone DBLatencyMs=-1 al fallar el Ping, justo para senalar degradacion, pero la UI la invierte. CONFIRMADO. (3) Un solo ternario, 

#### [BAJA] active_sessions
- **Captura:** El bot escribe en sessions (neuro_bot/MySQL). Las sesiones salen de 'active' por: cierre por inactividad cada 1 min (internal/session/manager.go:280-306 -> UpdateStatus StatusCompleted), MarkAbandoned solo para escaladas (session_repo.go:284-291), y CloseByPhone='completed' (session_repo.go:159). Hay sweeper de 1 minuto, asi que 'active' esta acotado en el tiempo. Captura OK.
- **Almacenamiento:** sessions.status ENUM('active','completed','abandoned','escalated') con idx_status (migrations/001_create_sessions.up.sql:5,22). Sin doble conteo (PK por id de sesion). OK.
- **Query/agregación:** neuro-dashboard/internal/kpi/repository.go:753 -> SELECT COUNT(*) FROM sessions WHERE status='active'. Dos desajustes: (1) EXCLUYE 'escalated', pero las escaladas son conversaciones vivas atendidas por un humano; el propio bot considera activo status IN ('active','escalated') (session_repo.go:29,159). 'Sesiones activas' por tanto subcuenta las conversaciones en curso. (2) NO filtra expires_at > NOW(), asi que una sesion vencida sigue contando como activa hasta que el sweeper la cierre (ventana ~CloseMin+1min) -> ligero sobreconteo transitorio. El Scan solo se loguea (linea 754): si la query falla muestra 0 sin senal de error.
- **Endpoint:** GET /api/health -> HealthMetrics.ActiveSessions (handlers.go:402, models.go:295). Expone int correcto.
- **UI:** Health.tsx:25 StatCard 'Sesiones activas' intent='info' (azul fijo). Numero suelto, sin contexto/tendencia. La etiqueta no aclara que excluye escaladas.
- **Problemas:**
  - Query excluye status='escalated' aunque son conversaciones vivas; inconsistente con la definicion de 'activo' del propio bot (status IN active,escalated). Subcuenta.
  - No filtra expires_at > NOW(): sesiones vencidas cuentan como activas hasta el barrido (sobreconteo transitorio).
  - Error del Scan solo se loguea (repository.go:754) -> en fallo de BD muestra 0 silencioso, sin senal.
- **Recomendación:** Definir explicitamente el KPI: si 'activas' = conversaciones en curso, usar status IN ('active','escalated') AND expires_at > NOW(); si solo bot-activas, anadir expires_at > NOW() y aclarar etiqueta. Propagar el error del COUNT a un campo de estado en HealthMetrics.
- **Verificado (real, low):** CONFIRMADO en neuro-dashboard/internal/kpi/repository.go:753 -> `SELECT COUNT(*) FROM sessions WHERE status = 'active'`. (1) Excluye 'escalated': el propio bot define conversacion viva como `status IN ('active','escalated') AND expires_at > NOW()` en Neurobotv2/internal/repository/local/session_repo.go:29, :159, :174 (GetActiveByPhone, CompleteActiveByPhone, update conversation_id). El schema (mig

#### [OK] pending_notifications
- **Captura:** notification_repo upsert con PK=phone (notification_repo.go:24-33); la fila se borra al confirmar/cancelar y FindExpired lee las vencidas (linea 81-90). Captura OK.
- **Almacenamiento:** notification_pending(phone PK, expires_at, idx_expires) migrations/009_create_notification_pending.up.sql. PK por telefono => maximo una pendiente por paciente, sin doble conteo. OK.
- **Query/agregación:** repository.go:756 -> SELECT COUNT(*) FROM notification_pending WHERE expires_at > NOW(). Calcula exactamente lo que promete: confirmaciones pendientes aun vigentes. Excluye vencidas correctamente. Correcto.
- **Endpoint:** GET /api/health -> HealthMetrics.PendingNotifications. Correcto.
- **UI:** Health.tsx:26 StatCard 'Notif. pendientes' intent default. Adecuado para un contador.
- **Problemas:**
  - Menor: el error del Scan solo se loguea (repository.go:757); en fallo de BD muestra 0 sin senal de error.
- **Recomendación:** Reflejar el error del COUNT en un campo de estado para no mostrar 0 enganoso si la BD falla. El calculo en si es correcto.

**Notas del área:**
- _Faltantes:_ No se refleja la salud del BOT ni de SIESA. El bot ya expone un /health robusto (Neurobotv2/cmd/server/main.go:599-629) que pinguea local_db y external_db/SIESA y devuelve status ok/degraded/critical, pero el dashboard /api/health (kpiHandler.Health) lee su propia MySQL directa y NO proxea ese /health. La pagina titulada 'Sistema / Salud' por tanto NO refleja 'salud del bot' como pide el alcance: si el proceso del bot esta caido o SIESA esta caido, la pagina sigue en verde.; No hay senal de error para los COUNT de active_sessions/pending_notifications: si fallan, GetHealth solo loguea (repository.go:754,757) y devuelve 0; HealthMetrics no tiene campo Status/Error (models.go:294-298), asi que un 0 por fallo es indistinguible de un 0 real.; Sin historico/tendencia: la pagina son 3 StatCards instantaneas (refetch 30s). No se puede distinguir un pico transitorio de db_latency de una degradacion sostenida, ni ver picos de sesiones/notificaciones.
- _Redundantes:_ Existe ademas un endpoint interno del bot /api/internal/kpis/health (HandleHealthKPIs) que el dashboard no consume; duplica el concepto de health pero la UI usa el GetHealth propio del dashboard. Conviene unificar para evitar dos definiciones de 'health'.
- _Mejoras UI:_ Corregir el mapeo de intent de db_latency para que -1/caida sea rojo y muestre texto 'BD no disponible' en lugar de '-1.0 ms' verde (Health.tsx:31).; Anadir tarjetas/estado del bot y de SIESA consumiendo el /health del bot (status, local_db, external_db) para que la pagina realmente cubra 'bot y dashboard'.; Mostrar un estado global degraded/critical y, si la respuesta de /api/health falla, indicar 'dashboard sin datos' en vez de mostrar ceros (data?? 0 oculta el fallo del endpoint).; Aclarar etiquetas: 'Latencia BD (dashboard)', y 'Sesiones activas' indicando si incluye escaladas.
