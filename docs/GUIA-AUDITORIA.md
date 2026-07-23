# Guía de Auditoría del Bot — logs, chat_events y flow_events

> Cómo auditar el comportamiento del Neuro-Bot en producción: qué mirar, con qué endpoint,
> para encontrar bugs, errores, warnings y caídas de flujo. Complementa
> [OBSERVABILIDAD.md](OBSERVABILIDAD.md) (diseño) y [FLUJOS.md](FLUJOS.md) (catálogo de flujos).

---

## 0. TL;DR — las tres tambien en algun doc gaurda el bug de cuando se reiicia el bot con esas conversaciones encoaldas recuerdas que lo planteaste hace un rato en el chat? para prfundizarluego en el y plantear el fixcapas y cuándo usar cada una

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
# BASE según entorno:
#   PRODUCCIÓN:  https://app.colibrixa.com     (host público / ngrok; es el que debes usar para auditar prod)
#   Local/pruebas: http://localhost:8085       (o el PORT del .env; 8090 en el perfil de pruebas)
BASE="https://app.colibrixa.com"          # ← PROD
KEY="<INTERNAL_API_KEY>"                   # valor de INTERNAL_API_KEY del .env de prod
curl -s -H "X-API-Key: $KEY" "$BASE/api/internal/flow-stats?flow=agendar" | jq
```

> Para auditar **producción** apunta `BASE` a **`https://app.colibrixa.com`**. `GET /health` es público;
> el resto exige `X-API-Key`. Si `BASE` apunta a prod pero las respuestas parecen de otra instancia
> (uptime/SIESA distintos), revisa el túnel ngrok (§13.1).

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
5. **¿Es técnico/infra?** → `/logs` + `/health` (P6) y el **catálogo §13**.
6. Sube `FLOW_TRACE_LEVEL=full` temporalmente si necesitas el máximo detalle de un flujo.

---

## 11. Niveles de log: qué se ve en `info` vs `debug` (LEER ANTES de rastrear un mensaje)

`LOG_LEVEL` decide qué líneas aparecen. **Varias líneas clave para seguir un mensaje entrante son `DEBUG` y NO se ven con `info` (el default de prod).** Esto confunde mucho: parece que "no pasa nada" cuando en realidad el motivo está oculto.

| Con `LOG_LEVEL=info` (prod normal) ves | Con `LOG_LEVEL=debug` ADEMÁS ves |
|---|---|
| `request` (cada POST/GET), `processing message`, `event` (greeting_sent…), `conversation_cached`, `conversation_lookup_success`, todos los `WARN`/`ERROR` | `webhook_parsed` (dirección + texto del inbound), `phone not whitelisted, ignoring`, `session_state`, `sending_message`, `send_list_payload`, `conversations_api_response`, `message_sent_ok`, `outbound_event_received` |

**Implicación crítica:** si en `info` ves `request POST /api/webhooks/whatsapp` pero **no** ves `processing message` después, el mensaje **se descartó en silencio** (whitelist, dirección outbound, o dedup) y **el motivo solo es visible en `debug`**. Para diagnosticar "no responde", sube a `debug` temporalmente (`LOG_LEVEL=debug` en `.env` → `docker compose up -d --force-recreate bot`), reproduce, y vuelve a `info` al terminar (debug llena disco y loguea más PII).

> Base URL: en prod los endpoints van contra `https://app.colibrixa.com` (el host/ngrok público), no `localhost`.

---

## 12. El ciclo de vida de UN mensaje (la secuencia EXACTA en logs)

Cuando un paciente escribe, el bot deja esta huella, en orden. Sirve para saber **exactamente dónde muere** un mensaje. Filtra con `GET /logs?phone=+57...` o `?search=<msg>`.

```
1.  request  POST /api/webhooks/whatsapp            ← Bird entregó un evento al bot
2.  webhook_parsed  direction:"incoming" body_type:"text" text_text:"hola"   [DEBUG]
        ↳ direction:"outgoing" → es un ECO de un saliente; se descarta (normal, 0ms)
3.  phone not whitelisted, ignoring                 [DEBUG]  ← STOP si la whitelist filtra (§13.2)
4.  (si pasa) → se inserta en el WAL (message_inbox) y se encola al worker
5.  processing message  state:"..." type:"text|postback" text:"hola"  [INFO]  ← LLEGÓ al motor
6.  conversation_lookup_success / conversation_id_refreshed   ← consiguió a quién responder
7.  event  type:"greeting_sent" (o invalid_input, out_of_hours, escalated_to_agent…)  [INFO]
8.  sending_message  type:"interactive_list" conversation_id:"..."   [DEBUG]  ← el bot ENVÍA
9.  conversations_api_response  status:201          [DEBUG]  ← Bird aceptó la respuesta (201 = ok)
10. message_sent_ok  bird_msg_id:"..."              [DEBUG]  ← entregado
```

**Cómo leer las AUSENCIAS (dónde murió):**
| Última línea "buena" que ves | Qué significa |
|---|---|
| Ni siquiera `request POST /api/webhooks/whatsapp` | El mensaje **no llegó al bot** → problema de ENTREGA (Bird no entrega, o el túnel apunta a OTRA instancia — §13.1). |
| `request` con POST a `0ms` y nada más | Retorno temprano: dirección `outgoing` (eco), **whitelist** (§13.2), o dedup. Sube a `debug` para ver el motivo. |
| `webhook_parsed` (incoming) → `phone not whitelisted` | La whitelist NO está vacía (§13.2). |
| `webhook_parsed` (incoming) pero NO `processing message` | Descartado tras parsear (whitelist, dedup, o `inbox persist failed`). |
| `processing message` pero NO `sending_message`/`event` | El handler no respondió, o `BOT_ENABLED=false` (`bot_disabled_escalating`), o panic (busca `ERROR`). |
| `sending_message` con `conversations_api_response status>=400` | Bird rechazó el envío (conversación cerrada / payload inválido). |
| `message_sent_ok` pero el paciente no lo ve | Respondió a un **conversation_id viejo/equivocado** (§13.3). |

---

## 13. Catálogo de modos de fallo conocidos ("no responde" / "responde mal")

Casos reales y cómo confirmarlos. **Primero siempre:** `GET /health` (¿`external_db`/`local_db` ok?) y mira si hay `request POST /api/webhooks/whatsapp` recientes.

### 13.1 — Dos ngrok peleando el mismo dominio (el inbound va a la instancia equivocada)
- **Síntoma:** prod "no recibe nada" aunque está sano; los webhooks llegan a OTRA instancia (p.ej. un PC de desarrollo) que corre ngrok con el MISMO `NGROK_HOSTNAME` reservado.
- **Por qué:** un dominio reservado de ngrok solo lo sirve UN agente; el último que conecta gana el túnel.
- **Confirmar:** `GET /health/debug` → `uptime` y `external_db` (la instancia con SIESA conectado y el uptime esperado = la real). En el server: `docker logs neuro_bot_ngrok | grep "started tunnel"`.
- **Fix:** **un solo agente por dominio.** Pruebas/local debe usar OTRO `NGROK_HOSTNAME`. Apagar el local o cambiarle el ngrok.

### 13.2 — Whitelist que NO está realmente vacía (trampa del comentario inline)
- **Síntoma:** `phone not whitelisted, ignoring` para CADA teléfono, **incluido tu número de prueba**; cero `processing message`.
- **Confirmar la config EFECTIVA (no el `.env` a ojo):**
  ```bash
  docker exec neuro_bot printenv TESTING_WHITELIST_PHONES   # si imprime algo → NO está vacía
  ```
- **Causa real:** comentario inline en una línea de valor vacío. `docker-compose` NO quita el comentario si el valor es vacío → el texto del comentario se vuelve el VALOR:
  ```
  TESTING_WHITELIST_PHONES=        # comentario   →  valor = "# comentario"  (¡no vacío!)
  ```
  `parsePhoneList` lo parte por comas → lista basura → filtra a TODOS.
- **Fix:** comentario en su propia línea; la variable sin nada después del `=`:
  ```
  # Lista blanca (coma). Vacío = todos.
  TESTING_WHITELIST_PHONES=
  ```
  Luego `docker compose up -d --force-recreate bot`.
- **Regla general:** en `env_file` de compose, **nunca** comentario inline en una variable cuyo valor vacío sea significativo.

### 13.3 — Responde a un conversation_id viejo (el paciente no ve la respuesta)
- **Síntoma:** el bot procesa y `message_sent_ok` (201), pero el paciente no recibe nada o le llega en otro hilo.
- **Por qué:** el inbound llega con `conversation_id:""`; el bot busca/cachea una conversación para el teléfono (`conversation_lookup_success`). Si ese teléfono tiene varios hilos (típico de un número de pruebas), agarra uno viejo.
- **Confirmar:** `/sessions?phone=` → mira `conversation_id`; compáralo con el `conversation_cached` reciente. Si difieren, responde al viejo.
- **Fix:** probar con un teléfono limpio (un solo hilo); o borrar la sesión de ese teléfono. En prod normal no pasa (cada paciente usa su número).

### 13.4 — `BOT_ENABLED=false` (escala todo, no auto-responde)
- **Síntoma:** `bot_disabled_escalating` por cada mensaje; el paciente queda con un agente, sin respuesta automática.
- **Confirmar:** `docker exec neuro_bot printenv BOT_ENABLED`.
- **Fix:** `BOT_ENABLED=true` + recrear (si se quiere autogestión).

### 13.5 — Modo degradado (SIESA inalcanzable)
- **Síntoma:** `external db not available, bot will start in degraded mode`; `/health` → `external_db != "ok"`. El bot saluda pero **no agenda** (no lee SIESA).
- **Confirmar:** `GET /health` + `docker compose logs bot | grep -i degraded`; `nc -zv <EXTERNAL_DB_HOST> 1433`.
- **Fix:** red/firewall a SIESA, `EXTERNAL_DB_*`, `EXTERNAL_DB_ENCRYPT`.

### 13.6 — Backpressure / avalancha del WAL (mensajes descartados)
- **Síntoma:** `backpressure: overflow limit reached, dropping message`; a veces precedido de `stale replay: re-encolando mensajes 'pending' atascados count:N` con N grande.
- **Por qué:** la cola (def 100) se llenó por carga alta o por un backlog del WAL re-encolado de golpe.
- **Confirmar:** `/health/debug` (`worker_queue_size` vs `worker_queue_cap`) + `/logs?search=backpressure`.
- **Fix:** escalar a high-load (`scale-up.sh`) si es carga sostenida; si es backlog viejo atascado, vaciar `message_inbox` (status='pending') o drenarlo con más workers.

### 13.7 — Fuera de horario de atención (NO es bug)
- **Síntoma:** responde un **menú corto** (no el flujo completo); `event type:"out_of_hours"`, estado `OUT_OF_HOURS`.
- **Horario (hardcodeado en `greeting.go`):** Lun–Vie 7:00–18:00, Sáb 7:00–12:00, Dom cerrado (zona `America/Bogota`). `TESTING_ALWAYS_OPEN=true` lo bypasea.
- Para probar el flujo completo: en horario, o con `TESTING_ALWAYS_OPEN=true`.

### 13.8 — Firma de webhook inválida
- **Síntoma:** `invalid webhook signature` (WARN) con `has_signature`/`url`/`body_preview`; el webhook se rechaza (401).
- **Por qué:** `BIRD_WEBHOOK_SECRET*` no coincide con Bird, o la URL reconstruida (host/proto) no es la que Bird firmó.
- **Confirmar:** `/logs?search=invalid webhook signature`.
- **Fix:** alinear el signing key en Bird, o revisar `X-Forwarded-Host/Proto` del proxy/ngrok.

---

## 14. Cómo ANOTAR hallazgos (para un agente sin contexto)

Por cada cosa rara, registra: **(1)** teléfono o `trace_id`, **(2)** timestamp, **(3)** última línea de log "buena" y la primera "mala" (usa §12), **(4)** el endpoint que lo evidencia, **(5)** el síntoma observable para el paciente. Y clasifícalo:

- **Bug** (el bot hizo algo incorrecto) → adjunta `trace_id` + `/flow-trace`.
- **Bloqueo legítimo** (regla de negocio: embarazo, GFR, sin convenio) → `outcome=blocked` + `reason`. **NO es bug.**
- **Inconsistencia / gap** (algo no cubierto o que se comporta raro) → describe el paso, el comportamiento esperado vs el real.
- **Infra / config** (no responde, degradado, túnel, whitelist) → usa §13.

**Señales de que algo está MAL aunque no haya `ERROR`:**
- `processing message` sin un `sending_message`/`event` posterior (se procesó pero no respondió).
- `conversations_api_response status>=400` repetido.
- `/anomalies` con filas nuevas (`orphan_slot`, `consulta_valor_cero`, `wl_stuck`, `zombie_escalated`).
- `/flow-stats` con caída brusca entre dos steps consecutivos (fuga del embudo).
- `message_sent_ok` pero el paciente dice que no llegó (conversation_id viejo, §13.3).
- Mismo `state` repetido muchas veces para un teléfono en `/events` (se quedó pegado en un paso).

---

## 15. Dónde se registran los hallazgos

§14 dice QUÉ anotar; aquí va DÓNDE se guarda.

**Auditoría manual:** lleva el tracking donde tu equipo trabaje (ticket / doc). Para bugs reproducibles
del bot, además documéntalos en **`docs/BUGS-CONOCIDOS.md`** (con su formato: análisis + fix propuesto).

**Auditoría automatizada (agente recurrente):** persiste en la carpeta **`auditoria/`** del repo:

| Archivo | Contenido |
|---|---|
| **`auditoria/hallazgos.jsonl`** | Un JSON por línea por hallazgo → **fuente principal**, consultable con `jq`. |
| `auditoria/hallazgos.md` | Versión legible append-only del mismo registro (para humanos). |
| `auditoria/snapshots/<ts>.json` | Snapshot de `/health` + `/kpis/health` por ciclo (histórico de salud/carga). |
| `auditoria/cursor.txt`, `auditoria/seen.txt` | Estado incremental (último ts procesado + firmas ya reportadas) para no duplicar. |

**Schema sugerido de cada hallazgo** (`.jsonl`):
```json
{"ts":"...","clase":"BUG|BLOQUEO-OK|GAP|INFRA","severidad":"alta|media|baja","flujo":"agendar",
 "trace_id":"sess:...","phone_masked":"+573***1234","evidencia_endpoint":"/flow-events?...",
 "sintoma":"...","ultima_linea_buena":"...","primera_linea_mala":"...","causa_probable":"...","fix_sugerido":"§13.x"}
```

**Persistencia entre corridas:** si el agente corre como **rutina programada** (entorno efímero), debe
**commitear `auditoria/` al repo** al cerrar cada corrida → la siguiente lee `cursor`/`seen` y continúa.
Si corre en bucle dentro de una sesión abierta, basta el disco local.

**Alertas activas:** los hallazgos **🚨 críticos** (bot caído, `external_db != ok` sostenido, drops masivos
por backpressure, "no entra ningún webhook", whitelist filtrando a todos) se envían además a un **chat de
Telegram APARTE** (no el de errores del bot, para no mezclar) vía
`https://api.telegram.org/bot<TOKEN>/sendMessage`.

## 16. Comportamientos desplegados jul-2026 (2ª tanda) — eventos nuevos y qué cazar

Cuatro sub-flujos nuevos dentro de `agendar`/`notif_recordatorio`. Sus steps aparecen solos en el
funnel de flow-stats; esto define el ciclo SANO y el hallazgo a cazar.

### A) Recordatorio de CORTA ANTELACIÓN (scheduler horario 06–16, flujo notif_recordatorio)
Cubre citas de HOY agendadas después de la corrida de las 07:00 (nunca les llegaba recordatorio).
- SANO: `same_day_reminder_sent` → respuesta del paciente (confirmed/cancel/reschedule) O
  `same_day_no_response` (vencimiento SILENCIOSO: sin followups ni escalación — es TERMINAL VÁLIDO,
  NO es flujo incompleto).
- CAZAR: [BUG] followups/escalación después de un `same_day_reminder_sent` (el pending SameDay debe
  morir en silencio) · [BUG] `same_day_reminder_sent` duplicado para la misma cita (falló el dedup
  WasAppointmentNotified) · logs "fail-closed" sostenidos (NotifHistory caído = tarea muda).
- `escalation_no_conversation` (flujo día-antes): escalación sin ConversationID → el agente NO se
  enteró; si crece, hay hueco de conversaciones Bird.

### B) STASH de la orden (foto como primer mensaje / en menú, flujo agendar)
- SANO: `photo_first_message` (o `photo_intent_scheduling`) → …identificación… →
  `stashed_order_used` (OCR directo, sin re-pedir foto) O `stashed_order_failed` (fallback: pide foto normal).
- CAZAR: [FLUJO-INCOMPLETO] sesión con `photo_first_message`/`photo_intent_scheduling` que llega a
  ASK_MEDICAL_ORDER SIN `stashed_order_used` NI `stashed_order_failed` = stash perdido.

### C) PÁGINAS ADICIONALES en confirmación OCR (flujo agendar)
Imagen durante CONFIRM_OCR_RESULT = página extra: fusión de CUPS (dedupe) y re-VALIDATE_OCR.
- SANO: `ocr_page_appended{added}` → re-muestra "¿Es correcto?". `ocr_append_failed` = no se pudo
  leer la página (reintento, no terminal). Tope 3 páginas.
- CAZAR: [BUG] `ocr_page_appended` con CUPS duplicados en la cita final · bucles de `ocr_append_failed`
  (>3 en la misma sesión = paciente atorado mandando fotos ilegibles).

### D) EPS por NOMBRE (flujo entidad)
`entity_matched_by_name` (chat_event) = selección rescatada por nombre; el funnel `entity_selected` no cambia.
- CAZAR: [BUG] `entity_matched_by_name` con código de entidad que NO estaba en `entity_list_codes`
  de esa sesión (matching eligió fuera de la lista mostrada). Estadística: invalid_input/escalaciones
  de ASK_ENTITY_NUMBER deben CAER tras el deploy.

### E) Acuse durante escalación (flujo escalacion)
`escalacion/ack_sent` = acuse ÚNICO al paciente que espera agente (solo su primer mensaje y solo si el
agente NO ha respondido; fail-quiet ante cualquier duda).
- CAZAR: [BUG] más de un `ack_sent` por escalación · [BUG] un `ack_sent` DESPUÉS de actividad del
  agente (first_agent_msg_at anterior al ack) — el bot se metió en una conversación humana.

### F) 3ª tanda jul-2026 (variantes de entrada + proactivos nuevos)
- `entidad/greeting_midflow_redirected` y tipo-doc: saludo/intención a mitad de flujo re-muestra la
  pregunta SIN gastar reintento (no es invalid_input). `parseDocType` acepta "1 cc"/"cédula de
  ciudadanía"; CAZAR: caída de invalid_input en ASK_DOCUMENT_TYPE tras el deploy.
- `reminder_prep_sent`: preparación del examen como texto tras la plantilla (solo CUPS con
  preparación en catálogo). CAZAR: prep enviada sin plantilla previa (huérfana).
- `reengagement_sent` (07:05): re-enganche a rebotados fuera-de-horario de ayer >=17h, UNO por
  teléfono. CAZAR: más de 1 al mismo teléfono el mismo día, o envíos sin out_of_hours previo.
