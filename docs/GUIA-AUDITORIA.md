# Guía de Auditoría del Bot — logs, chat_events y flow_events

> Cómo auditar el comportamiento del Neuro-Bot en producción: qué mirar, con qué endpoint,
> para encontrar bugs, errores, warnings y caídas de flujo. Complementa
> [OBSERVABILIDAD.md](OBSERVABILIDAD.md) (diseño) y [FLUJOS.md](FLUJOS.md) (catálogo de flujos).

---

## 0. TL;DR — las tres capas y cuándo usar cada una

El bot deja rastro en **tres capas distintas**. No compiten: cada una responde una pregunta diferente.

| Capa | Pregunta que responde | Indexada por | Endpoint | Retención |
|------|----------------------|--------------|----------|-----------|
| **logs** (slog JSON) | *¿Qué pasó técnicamente?* (requests, errores, panics, Bird API, firmas, timeouts) | tiempo / texto | `GET /api/internal/logs` | archivos en `LOG_DIR`, rotados |
| **chat_events** (`events`) | *¿Qué hizo ESTE paciente, paso a paso?* | teléfono + sesión | `GET /api/internal/events` | tabla MySQL (histórica) |
| **flow_events** (observabilidad) | *¿Cómo se comporta ESTE flujo? ¿dónde se cae? ¿hay anomalías?* | `trace_id`, flow, step | `GET /api/internal/flow-trace`, `/flow-events`, `/flow-stats`, `/anomalies` | 45 días crudos + rollup diario indefinido |

**Regla rápida de decisión:**
- *Un paciente concreto reporta un problema* → **chat_events** (`/events?phone=`) + **sessions**; si hay error técnico, baja a **logs**.
- *¿Dónde se atascan los agendamientos? ¿la lista de espera funciona? ¿hay mal comportamiento?* → **flow_events** (`/flow-stats`, `/flow-events`, `/anomalies`).
- *Algo falló técnicamente y puntual* (firma de webhook, timeout, panic, error de Bird/SIESA) → **logs**.
- *Seguir un caso de punta a punta correlacionado* (oferta de cupo → notificación → cita) → **flow-trace** por `trace_id`.
- *Detectar problemas que NO lanzan excepción* (cupo huérfano, consulta con valor 0, escalada zombie) → **anomalies**.

---

## 1. Acceso y autenticación

Todos los endpoints `/api/internal/*` exigen la cabecera **`X-API-Key`** (valor de `INTERNAL_API_KEY`) y pasan por rate-limit por IP. `GET /health` es público; `GET /health/debug` requiere la key.

```bash
# Plantilla. En testing local: BASE=http://localhost:8090 ; en prod usar su host/ngrok.
BASE="http://localhost:8090"
KEY="<INTERNAL_API_KEY>"
curl -s -H "X-API-Key: $KEY" "$BASE/api/internal/flow-stats?flow=agendar" | jq
```

> Los teléfonos salen **enmascarados** (`+573***3616`) en logs y flow_events por PII. En `chat_events`
> el teléfono se guarda completo (es la clave de búsqueda) — tratar esa salida como sensible.

---

## 2. Catálogo de endpoints

### 2.1 Observabilidad de flujos (flow_events) — el núcleo de auditoría

| Endpoint | Parámetros | Para qué |
|----------|-----------|----------|
| `GET /flow-trace` | `trace_id` (**obligatorio**) | Recorrido completo de UN caso correlacionado, en orden. El punto de entrada cuando ya tienes un `trace_id` (p.ej. de una alerta de Telegram). |
| `GET /flow-events` | `flow`, `outcome`, `reason`, `from=YYYY-MM-DD`, `to=YYYY-MM-DD`, `limit` (def 200; ventana def últimas 24h) | Buscar eventos por **tipo**: todos los `booking_failed`, todos los `outcome=error`, todos los `reason=gfr_low`, etc. |
| `GET /flow-stats` | `flow`, `from`, `to` (def últimos 7d) | **Funnel** (conteo por step) + distribución por **outcome** + conteo por **reason**. Para ver dónde se caen los pacientes y la tasa de éxito de un flujo. |
| `GET /anomalies` | `reason`, `from`, `to` (def últimos 7d), `limit` | Anomalías de invariantes detectadas por el **reconciliador** (mal comportamiento silencioso). |

**Flujos disponibles** (`flow=`): `lista_espera`, `agendar`, `notif_recordatorio`, `identificacion`,
`entidad`, `registro`, `mis_citas`, `escalacion`, `scheduler`, `admin_agenda`, `infra`, `invariante`.
(Catálogo completo de steps por flujo en `internal/observability/tracer.go` → `catalog`.)

### 2.2 Conversación / sesión (chat_events + logs + estado)

| Endpoint | Parámetros | Para qué |
|----------|-----------|----------|
| `GET /events` | `phone` (**obligatorio**), `from`, `to`, `type`, `limit` (def 200, máx 500) | Eventos de negocio de **un paciente**, con `state_from`/`state_to`. Reconstruye qué hizo. |
| `GET /sessions` | `phone`+`limit` (def 10) **o** `id=<uuid>` | Sesiones recientes de un teléfono, o una sesión + su contexto completo por ID. |
| `GET /sessions/context` | `id=<uuid>` (**obligatorio**) | Solo el `session_context` (clave/valor) de una sesión. Útil para ver qué datos llevaba el bot (cups, contrato, slots, etc.). |
| `GET /logs` | `lines` (def 200, máx 10000), `level`, `phone`, `search`, `from`, `to`, `download=true` | Logs slog crudos. Filtra por nivel/teléfono/texto. `download=true` baja un `.log`. |

### 2.3 KPIs (métricas de negocio agregadas)

| Endpoint | Parámetros | Para qué |
|----------|-----------|----------|
| `GET /kpis/daily` | `date=YYYY-MM-DD` | KPIs del día (citas, confirmaciones, cancelaciones, escalaciones…). |
| `GET /kpis/weekly` | `date` (semana que contiene) | KPIs de la semana. |
| `GET /kpis/funnel` | rango | Embudo de conversión de alto nivel. |
| `GET /kpis/health` | — | Salud operativa (volúmenes, tasas). |

### 2.4 Salud / infraestructura

| Endpoint | Auth | Para qué |
|----------|------|----------|
| `GET /health` | no | Liveness + ping a ambas BD (MySQL local y SIESA). |
| `GET /health/debug` | sí | Estado detallado: cola del worker, conexiones DB, etc. |

### 2.5 Acciones (no auditoría, pero útiles al investigar)
`POST /test-alert` (probar Telegram), `POST /send-reminders` (disparar recordatorios sin esperar 07:00),
`POST /test-voice-call` (probar IVR), `POST /send-agenda-confirmations`, `POST /cancel-agenda`,
`POST /reschedule-agenda`, `POST /waiting-list/check`, `GET /waiting-list`.

---

## 3. Las tres capas en detalle

### 3.1 logs (slog JSON)
- **Qué es:** todo lo que el código loguea con `slog`. Formato JSON por línea: `time, level, msg, ...campos`.
- **Niveles:** `DEBUG` (detalle: payloads Bird, transiciones) · `INFO` (operación) · `WARN` (algo raro no fatal: firma inválida, teléfono no whitelisted, slot tomado) · `ERROR` (fallo: panic recuperado, error de BD/Bird/SIESA, state_machine_error).
- **Cuándo:** depurar el *cómo* técnico — por qué Bird devolvió 4xx, por qué falló una firma, un timeout de lock, un panic, un error de SIESA. Es la capa más verbosa y de **menor retención** (archivos rotados).
- **Clave:** los `slog.Error` además se enrutan a **Telegram** (ver §6) y, cuando aplica, llevan el `trace_id` para saltar a flow_events.

### 3.2 chat_events (tabla `events`, vía `EventTracker`)
- **Qué es:** eventos de negocio de la **conversación**, por teléfono y sesión, con `state_from`/`state_to` y un `data` JSON. Ejemplos: `session_started`, `menu_selected`, `client_type_selected`, `contract_resolved`, `escalated_to_agent`, `escalation_ended`, `notification_sent`, `state_machine_error`, `msg_during_escalation`.
- **Cuándo:** reconstruir **qué hizo un paciente concreto** paso a paso (la "película" de su sesión). Es la fuente para "este número dice que el bot se quedó pegado en X".
- **Diferencia con flow_events:** chat_events es **por teléfono/sesión** (la conversación cruda); flow_events es **por flujo/trace** (el comportamiento de negocio, clasificado y agregable). Un mismo hecho puede aparecer en ambas con distinto propósito.

### 3.3 flow_events (observabilidad de flujos)
- **Qué es:** traza de **negocio** correlacionada por `trace_id`, clasificada por `flow` + `step`, con `level` (1-4), `outcome`, `reason`, `phone` (enmascarado), `ref_type`/`ref_id` (para pivotar a la entidad: cita, agenda, wl) y `attrs` JSON. Se agrega a `flow_daily_stats`.
- **Cuándo:** auditar **el comportamiento de un flujo** (no de un teléfono): ¿la lista de espera empareja y notifica?, ¿dónde se bloquean los agendamientos?, ¿qué % escala?, ¿hay anomalías? Y seguir un caso E2E por `trace_id`.
- **Nivel de detalle:** controlado por `FLOW_TRACE_LEVEL` (`off` < `error` < `outcome` < `milestone` < `full`; default `milestone`). Subir a `full` para depurar a fondo; bajar a `error`/`outcome` si hay que reducir volumen.

---

## 4. `trace_id` — convenciones

El `trace_id` es la llave para `/flow-trace`. Se construye según la entidad (ver `tracer.go`):

| Prefijo | Construcción | Flujos que lo usan |
|---------|-------------|--------------------|
| `sess:<session_uuid>` | `TraceSession(sess.ID)` | agendar, identificacion, entidad, registro, mis_citas, escalacion, infra (session_abandoned) |
| `wl:<entry_id>` | `TraceWaitingList(id)` | lista_espera |
| `notif:<appointment_id>` | `TraceNotif(apptID)` | notif_recordatorio (recordatorio/IVR) |
| `agenda:<id>:<yyyymmdd>` | `TraceAgenda(id, date)` | admin_agenda (cancelar/reagendar) |
| `task:<name>:<yyyymmdd>` | `TraceTask(name, date)` | scheduler |
| `infra:phone_lock_timeout` | fijo | infra (degradación) |

Para pasar de un teléfono a su `trace_id`: busca la sesión con `/sessions?phone=` → toma el `id` → `trace_id = sess:<id>`.

---

## 5. `outcome` y niveles (cómo leer un flow_event)

- **`outcome`** (resultado del step): `ok` · `blocked` (regla de negocio lo frenó: embarazo, GFR bajo, sin convenio) · `error` (fallo técnico) · `escalated` (pasó a agente) · `retry` (reintento/recordatorio) · `info` (informativo).
- **`level`** (tier de registro): `1=error` · `2=outcome` (resultado terminal) · `3=milestone` (hito) · `4=full` (detalle).
- **`reason`**: el "por qué" en una palabra (`too_big`, `no_block`, `gfr_low`, `pregnancy`, `no_convenio`, `slot_taken`…). Es lo que se agrupa en `/flow-stats` y se filtra en `/flow-events`.

Mirar primero `outcome` (¿bien, bloqueado, error, escalado?) y luego `reason` (¿por qué?).

---

## 6. Cómo entran los errores (punto de partida)

1. **Telegram** — todo `slog.Error` se envía al chat de alertas (`TELEGRAM_BOT_TOKEN`/`CHAT_ID`). El mensaje incluye el `trace_id` cuando existe. **Es el disparador principal de una investigación.**
2. **`/anomalies`** — el reconciliador (corre en `data_cleanup` 02:00) detecta *mal comportamiento silencioso* que NO lanza excepción: `orphan_slot` (cupo marcado ocupado sin cita), `consulta_valor_cero` (consulta agendada con valor 0), `wl_stuck` (lista de espera atascada), `zombie_escalated` (escalada sin cerrar). Emite `flow=invariante`, `outcome=error`.
3. **`/flow-events?outcome=error`** — todos los fallos de negocio de la ventana, por flujo.
4. **`/logs?level=ERROR`** — los errores técnicos crudos (con stack en panics).

---

## 7. Playbooks de auditoría

### P1 — "Llegó una alerta de error por Telegram"
1. Toma el **`trace_id`** del mensaje.
2. `GET /flow-trace?trace_id=<...>` → ve el recorrido completo: en qué `step` se cortó, con qué `outcome`/`reason`.
3. Si es `sess:<id>` y necesitas el detalle técnico: `GET /logs?search=<session_id>` (o `?phone=`) acotando `from`/`to` al timestamp de la alerta.
4. Si necesitas el contexto del bot en ese momento: `GET /sessions?id=<id>` (incluye `context`).

### P2 — "Un paciente dice que el bot falló / se quedó pegado"
1. `GET /events?phone=+57...` → la película de su sesión (qué seleccionó, transiciones, dónde se detuvo, si escaló).
2. `GET /sessions?phone=+57...&limit=5` → estado/expiración de sus sesiones recientes; toma el `id`.
3. `GET /flow-trace?trace_id=sess:<id>` → el comportamiento de negocio (bloqueos, errores).
4. Si no aparece NADA al escribir: es entrega/firma → `GET /logs?phone=+57...&search=webhook` (busca `invalid webhook signature` o `phone not whitelisted`).

### P3 — "¿Dónde se caen los pacientes al agendar?"
1. `GET /flow-stats?flow=agendar&from=...&to=...` → funnel por step (`ocr_ok` → `slots_found` → `booking_success`) + outcomes + reasons.
2. Donde el conteo se desploma entre steps consecutivos = el punto de fuga.
3. `GET /flow-events?flow=agendar&outcome=blocked` (o `=error`) → casos concretos y su `reason` (p.ej. muchos `no_convenio` o `gfr_low`).

### P4 — "Auditar la lista de espera de punta a punta"
1. `GET /flow-stats?flow=lista_espera` → ¿cuántos `enrolled` vs `slot_match` vs `notified` vs `booked`? ¿muchos `skipped`/`expired`?
2. Para un caso: `GET /flow-trace?trace_id=wl:<entry_id>` → `enrolled → slot_match/skipped → notified → response_schedule → booked` (o dónde murió: `claim_lost`, `expired`, `duplicate_found`).
3. `GET /flow-events?flow=lista_espera&reason=no_block` → entradas que no encuentran bloque contiguo.

### P5 — "Detectar bugs que no lanzan error"
1. `GET /anomalies?from=...&to=...` → la lista de invariantes violados.
2. Por cada anomalía, usa su `ref_type`/`ref_id` o `trace_id` para ir al caso (`/flow-trace`) y entender el origen.
3. Si `consulta_valor_cero` o `orphan_slot` crecen → revisar el flujo de creación de cita (`flow=agendar`, steps `booking_*`).

### P6 — "El bot no responde a NADIE / responde a unos sí y a otros no"
1. `GET /logs?search=invalid webhook signature&lines=500` → si hay firmas fallando, es config de webhooks en Bird (suscripción duplicada / secreto que no coincide). Mira la URL y `outbound` del log.
2. `GET /health` → ¿BD arriba? `GET /health/debug` → ¿cola del worker saturada?
3. `GET /logs?level=ERROR` → panics o errores de arranque.

### P7 — "¿Las tareas programadas corrieron?"
1. `GET /flow-events?flow=scheduler&from=...` → `task_completed` / `task_failed` con `duration_ms` por tarea.
2. `GET /logs?search=scheduler&from=...` → detalle (catch-up, skips por kill switch).

---

## 8. Detección por clase de problema (qué señal mirar)

| Síntoma | Señal / dónde |
|---------|---------------|
| **Error técnico** (panic, Bird 4xx/5xx, SIESA caído) | Telegram → `/logs?level=ERROR` |
| **Fallo de negocio** (no se creó la cita, etc.) | `/flow-events?outcome=error` · `/flow-trace` |
| **Bloqueo legítimo** (embarazo, GFR, sin convenio) | `/flow-events?outcome=blocked` + `reason` |
| **Fuga en el embudo** (caen muchos en un paso) | `/flow-stats?flow=...` (saltos entre steps) |
| **Mal comportamiento silencioso** | `/anomalies` |
| **Entrega/firma de WhatsApp** | `/logs?search=signature` / `phone not whitelisted` |
| **Saturación / degradación** | `/health/debug` · `/flow-events?flow=infra` (`phone_lock_timeout`) |
| **Escaladas sin atender** | `/flow-events?flow=escalacion` (`escalation_expired`, `agent_reminder_sent`) |
| **Notificaciones no salieron** | `/flow-events?flow=notif_recordatorio` + `/logs?search=notifications disabled` (kill switch) |

---

## 9. Cheatsheet (curl)

```bash
BASE="http://localhost:8090"; KEY="<INTERNAL_API_KEY>"
H=(-H "X-API-Key: $KEY")

# Un caso por trace_id
curl -s "${H[@]}" "$BASE/api/internal/flow-trace?trace_id=sess:<uuid>" | jq

# Embudo y tasas de un flujo (últimos 7d)
curl -s "${H[@]}" "$BASE/api/internal/flow-stats?flow=agendar" | jq

# Todos los errores de negocio de hoy
curl -s "${H[@]}" "$BASE/api/internal/flow-events?outcome=error&from=$(date +%F)" | jq

# Anomalías de la última semana
curl -s "${H[@]}" "$BASE/api/internal/anomalies" | jq

# La conversación de un paciente
curl -s "${H[@]}" "$BASE/api/internal/events?phone=+573103343616" | jq

# Sesiones recientes + contexto de una
curl -s "${H[@]}" "$BASE/api/internal/sessions?phone=+573103343616&limit=5" | jq
curl -s "${H[@]}" "$BASE/api/internal/sessions?id=<uuid>" | jq

# Logs técnicos filtrados
curl -s "${H[@]}" "$BASE/api/internal/logs?level=ERROR&lines=300"
curl -s "${H[@]}" "$BASE/api/internal/logs?phone=+573103343616&search=signature"

# Salud
curl -s "$BASE/health" | jq
curl -s "${H[@]}" "$BASE/api/internal/health/debug" | jq   # nota: ruta real es /health/debug
```

---

## 10. Orden de trabajo recomendado (resumen)

1. **¿Hay alerta de Telegram?** → empieza por su `trace_id` en `/flow-trace` (P1).
2. **¿Es un paciente puntual?** → `/events?phone=` + `/sessions` (P2).
3. **¿Es un flujo/comportamiento agregado?** → `/flow-stats` → `/flow-events` (P3, P4).
4. **¿Sospecha de bug silencioso?** → `/anomalies` (P5).
5. **¿Es técnico/infra?** → `/logs` + `/health` (P6).
6. Sube `FLOW_TRACE_LEVEL=full` temporalmente si necesitas el máximo detalle de un flujo.
