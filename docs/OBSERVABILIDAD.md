# Observabilidad de Flujos — Diseño

> Objetivo: poder **reconstruir, auditar y verificar** el recorrido completo de cualquier flujo
> (lista de espera, agendamiento, recordatorios, registro, admin…) por medio de endpoints, detectar
> errores y "mal comportamiento" (lógica que no se cumplió), con **bajo consumo de servidor**.
>
> Principio rector: **un solo store de eventos estructurados y correlacionados** alimenta los tres
> consumos — Telegram (push de errores/anomalías), endpoints (pull por tipo/flujo/traza) y agregados
> (funnels/tasas). No se multiplican logs de texto ni se duplica infraestructura.

---

## 0. Por qué eventos estructurados y no "más logs"

| | Logs de texto (hoy) | Eventos estructurados (propuesta) |
|---|---|---|
| Consultar "¿cuántos flujos fallaron hoy?" | grep + contar a mano | query por `outcome=error` |
| Reconstruir el recorrido de un paciente | grep por teléfono, ordenar a mano | `GET /flow-trace?trace_id=...` |
| Verificar que la lógica se cumplió | imposible (el log dice "pasó", no "fue correcto") | reconciliación de invariantes |
| Volumen | N líneas por paso | 1 evento tipado por **decisión** |
| Agregar (funnels, tasas) | no | sí, nativo |

Ya existe la base: tabla `chat_events`, escritura **batched asíncrona** (`tracker.LogBatch`), endpoints
`/events` y `/kpis`, y `AlertHandler` (Telegram). Esto **refuerza** esa base, no la reemplaza.

---

## 1. Modelo de datos

Tabla dedicada `flow_events` (BD local del bot). Separada de `chat_events` (que sigue siendo el log
conversacional crudo): `flow_events` es la **columna vertebral de la traza**, con esquema e índices
hechos a propósito y su propia retención.

**Dos ejes distintos, no confundir** (resuelve la colisión de nombres):
- **`level`** = *tier de registro/filtro* (1=error, 2=outcome, 3=milestone, 4=full). Decide si el evento
  se graba según `FLOW_TRACE_LEVEL`. Lo asigna el **registro central** (§10.3), no el call-site.
- **`outcome`** = *resultado semántico* del evento (`ok|blocked|error|escalated|retry|info`). Para
  consultas de negocio ("todos los `blocked`", tasa de éxito). Un evento `level=error` casi siempre tiene
  `outcome=error`; uno `level=outcome` puede ser `ok` (cita creada) o `blocked` (GFR<30).

```sql
CREATE TABLE flow_events (
  id          BIGINT AUTO_INCREMENT PRIMARY KEY,
  trace_id    VARCHAR(64)  NOT NULL,   -- correlación del recorrido (ver §2)
  flow        VARCHAR(40)  NOT NULL,   -- "lista_espera" | "agendar" | "notif_recordatorio" | ...
  step        VARCHAR(60)  NOT NULL,   -- hito ("enrolled","slot_match","notified","booked",...)
  level       TINYINT      NOT NULL,   -- 1=error 2=outcome 3=milestone 4=full (tier de registro)
  outcome     VARCHAR(20)  NOT NULL,   -- resultado: ok|blocked|error|escalated|retry|info
  reason      VARCHAR(60)  NULL,       -- enum de razón ("too_big","gfr_low","slot_taken",...)
  phone       VARCHAR(20)  NULL,       -- SIEMPRE enmascarado (utils.MaskPhone)
  ref_type    VARCHAR(30)  NULL,       -- "slot" | "bird_msg" | "cita" | "wl_entry" | "agenda"
  ref_id      VARCHAR(64)  NULL,       -- id del artefacto referenciado (para pivotar entre trazas)
  attrs       JSON         NULL,       -- payload mínimo SIN PII (claves de vocabulario fijo, §10.5)
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  INDEX idx_trace   (trace_id, created_at),  -- flow-trace (timeline de un recorrido)
  INDEX idx_flow    (flow, created_at),       -- flow-events / flow-stats por flujo+ventana
  INDEX idx_phone   (phone, created_at),      -- trazas de un paciente
  INDEX idx_created (created_at)              -- retención + "errores recientes" por ventana
);
```
> Índices deliberadamente **pocos** (tabla de alta escritura): las consultas por `outcome`/`level`/`reason`
> filtran sobre el rango temporal de `idx_flow`/`idx_created` (consultas siempre acotadas por `from/to`).

**Tabla de rollup** (agregados diarios; sobreviven a la purga de los crudos → tendencias de largo plazo):
```sql
CREATE TABLE flow_daily_stats (
  day      DATE        NOT NULL,
  flow     VARCHAR(40) NOT NULL,
  step     VARCHAR(60) NOT NULL,
  outcome  VARCHAR(20) NOT NULL,
  reason   VARCHAR(60) NOT NULL DEFAULT '',
  cnt      INT         NOT NULL,
  PRIMARY KEY (day, flow, step, outcome, reason)
);
```

Reglas de contenido:
- **Nunca PII en claro**: `phone` siempre por `MaskPhone`; `attrs` solo claves del **vocabulario fijo**
  (§10.5); nunca documento ni nombre.
- `attrs` es payload **mínimo** y tipado por hito (no volcar structs enteros).
- `ref_type`/`ref_id` permiten **pivotar entre trazas** (p.ej. `wl_booked` y `booking_success` comparten
  `ref_id = cita_id`).

---

## 2. Correlación: `trace_id` por flujo

Cada flujo declara su **llave de correlación** estable para todo el recorrido (aunque cruce sesiones,
tiempos y subsistemas):

| Flujo | `trace_id` | Por qué |
|-------|-----------|---------|
| Lista de espera | `wl:<waiting_list_id>` | hilvana inscripción → match → notif → cita → expiración, aun a través de sesiones distintas |
| Agendamiento (paciente) | `sess:<session_id>` | una sesión conversacional = un intento de agendar |
| Recordatorio/confirmación | `notif:<appointment_id>:<yyyymmdd>` | estable por cita+día, sobrevive reinicios |
| Registro de paciente | `sess:<session_id>` | parte de la sesión |
| Mis citas (confirmar/cancelar) | `sess:<session_id>` | — |
| Cancelación/reagendamiento admin | `agenda:<agenda_id>:<yyyymmdd>` | una operación admin sobre una agenda |
| Escalación a agente | `sess:<session_id>` | — |

**Cruce entre trazas (sin duplicar):** cuando un recorrido entra en otro (p.ej. lista de espera que
termina agendando), el hito se emite en **ambas** trazas o se pivota por `ref_id`. Ejemplo lista de
espera: al crear la cita en la sesión reanudada se emite `wl_booked` en `wl:<id>` **y** el
`booking_success` normal en `sess:<id>`, ambos con `ref_id = cita_id`. Hoy ya existe parcialmente este
patrón (`waiting_list_booking_success` en `slots.go:1289`).

---

## 3. Taxonomía de hitos por flujo

Cada evento se emite con un **nivel** (ver §3A); el `FlowTracer` lo descarta si supera el máximo
configurado en `.env`. La clasificación completa de **todas** las acciones del proyecto está en §3A.

### 3.1 Lista de espera (`flow=lista_espera`, `trace=wl:<id>`)
| step | outcome | reason / ref | dónde |
|------|---------|--------------|-------|
| `enrolled` | ok | attrs: cups, espacios, contraste, sedación, contrato; trigger: auto/manual | `slots.go` autoAddToWaitingList / OFFER_WAITING_LIST |
| `slot_match` | ok | ref slot(s); attrs: trigger=cancel_realtime\|daily_06, remaining_capacity | `waiting_list_check.go` |
| `skipped` | info | reason: too_big \| no_block \| not_whitelisted | `waiting_list_check.go` |
| `duplicate_found` | blocked | — | `waiting_list_check.go:68` |
| `claim_lost` | info | (otra corrida concurrente reclamó) | `waiting_list_check.go:89` |
| `notified` | ok | ref bird_msg | `sendWaitingNotification` |
| `notify_failed` | error | — | `sendWaitingNotification` |
| `response` | ok | reason: schedule \| decline | `handleWaitingList` |
| `booked` | ok | ref cita_id | `slots.go` CREATE_APPOINTMENT (wlID en ctx) |
| `expired` | info | reason: timeout_6h \| stale_24h \| old_30d | `handleWaitingListTimeout` / cleanup |

### 3.2 Agendamiento (`flow=agendar`, `trace=sess:<id>`)
`order_received` → `ocr_ok` / `ocr_failed`(reason) → `cups_validated` / `cups_none` → `grouped`
(attrs: servicios, espacios) → [`gfr_blocked`(low) | `pregnancy_blocked` | `special_escalated`(sleep) |
`already_has_appt`] → [`coverage_no_convenio` | `mrc_limit_hit`] → `slots_found` / `no_slots` →
`booking_confirmed` → `booking_success`(ref cita_id) / `booking_failed`(reason: slot_taken\|price_0\|error).

### 3.3 Recordatorio / confirmación (`flow=notif_recordatorio`, `trace=notif:<appt>:<date>`)
`reminder_sent`(ref bird_msg) → [`response`(confirm\|cancel\|reschedule) | `followup1` | `followup2` |
`ivr_placed`(ref call_id) → `ivr_result`(reason: confirm\|cancel\|no_key\|no_answer) ] →
[`confirmed` | `cancelled` | `escalated`(reason) | `expired`]. Errores de persistencia SIESA →
`outcome=error` (p.ej. N-15/N-16).

### 3.4 Registro (`flow=registro`): `registration_started` → `registration_confirmed` → `patient_created` / `patient_create_failed`(error).
### 3.5 Mis citas (`flow=mis_citas`): `appointments_listed` → `appt_confirmed` / `appt_cancelled` / `appt_reschedule_started`.
### 3.6 Admin (`flow=admin_agenda`, `trace=agenda:<id>:<date>`): `agenda_cancelled`(attrs: n_citas) → `patients_notified`(attrs: n) → errores por paciente.
### 3.7 Escalación (`flow=escalacion`): `escalated`(reason) → `agent_resumed` / `agent_closed` / `escalation_expired`.

---

## 3A. Niveles de detalle y clasificación de acciones (todo el proyecto)

El máximo nivel a registrar se fija con la variable de entorno **`FLOW_TRACE_LEVEL`**. Cada evento se
emite con su nivel; si supera el máximo configurado, se descarta antes de encolar (comparación barata).
Niveles **acumulativos** (cada uno incluye los de abajo):

| Nivel | `.env` | Registra | Responde | Costo |
|:---:|---|---|---|:---:|
| 0 | `off` | nada | — | cero |
| 1 | `error` | solo errores + anomalías | "¿algo se rompió o se comportó mal?" | mínimo |
| 2 | `outcome` | + terminales de cada flujo | "¿cómo terminó cada flujo?" (funnels) | bajo |
| 3 | `milestone` **(default prod)** | + decisiones/hitos de negocio | "**el recorrido completo**" | bajo-medio |
| 4 | `full` | + detalle verboso (micro-pasos) | "¿qué hizo paso a paso?" | alto |

**Override opcional por teléfono** (`trace=full`): sube el nivel efectivo a `full` solo para un teléfono,
para depurar un caso puntual sin elevar el costo global. Ortogonal a `FLOW_TRACE_LEVEL`.

### Regla de clasificación (para cualquier acción, presente o futura)

| Nivel | Pregunta que lo define | Qué entra |
|---|---|---|
| **ERROR** | "¿Esto NO debería haber pasado?" | **Fallas**, no "no" de negocio: excepción técnica (BD/SIESA/Bird/OCR, timeout, panic), persistencia a medias (cita sin CPA, slot huérfano), comportamiento silencioso incorrecto (Valor=0, claim revertido), anomalía de reconciliación. |
| **OUTCOME** | "¿En qué **terminó**?" | El **desenlace** (uno por corrida): éxitos *y* bloqueos de negocio legítimos (sin convenio→escala, GFR<30, sin slots, ya tiene cita, escalado, rechazado, expirado, duplicado). |
| **MILESTONE** | "¿Por **dónde va** el recorrido?" | **Pasos intermedios significativos**: entrada a una fase, decisión que ramifica pero no termina, avance clave. |
| **FULL** | "¿Qué hizo **paso a paso**?" | Mecánica fina: loops, "por qué no" de cada candidato, resultados intermedios de query, reintentos dentro de un paso, sub-envíos, caché, mutaciones de contexto. |

**Regla de casos duales:** si un punto puede ser intermedio o terminal según la rama, se clasifica por el
resultado de *esa* emisión: termina el flujo → **OUTCOME**; continúa → **MILESTONE**.

**No se duplica `chat_events`:** el log conversacional crudo (cada mensaje, cada `state_from→state_to`)
ya vive en `chat_events`. `flow_events` registra solo el **recorrido de negocio**; una "transición de
estado genérica" NO es `flow_event`, solo los estados con significado de negocio lo son.

### Clasificación por flujo

**Menú / entrada (`menu`, trace `sess`)** — `out_of_hours` MILESTONE · opción de menú / saludo FULL.

**Identificación (`identificacion`, trace `sess`)**
| Acción | Nivel |
|--------|:---:|
| Paciente encontrado / no existe (`patient_lookup`) | MILESTONE |
| Datos de contacto actualizados en `sis_paci` | MILESTONE |
| Confirmar identidad sí/no | FULL |
| Falla de lookup / `UpdateContactInfo` (BD) | ERROR |

**Entidad / EPS / Contrato (`entidad`, trace `sess`)**
| Acción | Nivel |
|--------|:---:|
| Entidad seleccionada (`entity_selected`) | MILESTONE |
| Contrato resuelto (`contract_resolved`, MRC vs Evento) | MILESTONE |
| Sanitas sin municipio → captura forzada (`sanitas_muni_forced`) | MILESTONE |
| Sin entidades / entidad no resuelta → escala | OUTCOME |
| Falla `UpdateEntity`/`UpdateContract` | ERROR |

**Registro (`registro`, trace `sess`)**
| Acción | Nivel |
|--------|:---:|
| Inicio (`registration_started`) / resumen confirmado | MILESTONE |
| **Paciente creado** (`patient_created`) | OUTCOME |
| Registro abandonado (no quiso) | OUTCOME |
| Cada corrección de campo | FULL |
| Falla al crear en `sis_paci` | ERROR |

**Agendamiento (`agendar`, trace `sess`)**
| Acción | Nivel |
|--------|:---:|
| `order_received` · `ocr_ok` · `cups_validated` · `grouped` · `slots_found` · `booking_confirmed` | MILESTONE |
| Sin convenio→particular · filtro tope MRC | MILESTONE |
| **`booking_success`** (cita creada) | OUTCOME |
| `no_slots` · `gfr_blocked` · `pregnancy_blocked` · `special_escalated` (sueño) · `already_has_appt` · `coverage_escalated` · `cups_none` · semanas fuera de rango | OUTCOME |
| Reintento por slot tomado · código alternativo · enriquecer/descartar CUP en agrupación | FULL |
| Error motor OCR · cita sin CPA/CP (compensación) · error genérico de creación | ERROR |

**Mis citas (`mis_citas`, trace `sess`)**
| Acción | Nivel |
|--------|:---:|
| `appointments_listed` · `reschedule_started` | MILESTONE |
| **`appt_confirmed`** · **`appt_cancelled`** · `no_appointments` | OUTCOME |
| Mostrar preparación / volver | FULL |
| Falla `ConfirmBlock`/`CancelBlock` (SIESA) | ERROR |

**Lista de espera (`lista_espera`, trace `wl:<id>`)**
| Acción | Nivel |
|--------|:---:|
| `enrolled` · `slot_match` · `notified` · `response_schedule` | MILESTONE |
| **`booked`** · `declined` · `expired` · `duplicate_found` | OUTCOME |
| `skipped` (no cabe/sin bloque) · `claim_lost` | FULL |
| `notify_failed` · falla capacidad/query | ERROR |

**Recordatorio / IVR (`notif_recordatorio`, trace `notif:<appt>:<date>`)**
| Acción | Nivel |
|--------|:---:|
| `reminder_sent` · `ivr_placed` · `ivr_result` (tecla) · `ivr_no_key`/`no_answer` | MILESTONE |
| **`confirmed`** · **`cancelled`** · `escalated` · `expired` | OUTCOME |
| Follow-up 1/2 · skip por idempotencia | FULL |
| Falla persistencia SIESA tras confirmar (N-15/N-16) · falla envío/PlaceCall | ERROR |

**Admin agenda (`admin_agenda`, trace `agenda:<id>:<date>`)**
| Acción | Nivel |
|--------|:---:|
| `agenda_cancelled` (n) · `agenda_rescheduled` | OUTCOME |
| `patients_notified` (n) | MILESTONE |
| Notificación por-paciente individual | FULL |
| Falla `CancelBatch`/`RescheduleDate` | ERROR |

**Escalación (`escalacion`, trace `sess`)**
| Acción | Nivel |
|--------|:---:|
| `escalated` (reason) · `agent_closed` · `escalation_expired` | OUTCOME |
| `agent_resumed` · fallback (agente no disponible) | MILESTONE |

**Comandos de agente (`agente`, trace `sess`)**
| Acción | Nivel |
|--------|:---:|
| Comando recibido (`resume`/`reset`/`orden`/`cups`/`close`/`info`) · OCR de texto | MILESTONE |
| Comando en sesión no escalada (rechazado) | FULL |
| Falla de procesamiento del comando | ERROR |

**Tareas del scheduler (`scheduler`, trace `task:<name>:<date>`)**
| Acción | Nivel |
|--------|:---:|
| `task_completed` (sent/skipped/notified/cleaned counts) | OUTCOME |
| Catch-up ejecutó tarea perdida | MILESTONE |
| Tarea iniciada | FULL |
| `task_failed` / panic | ERROR |

**Infra / transversal**
| Acción | Nivel |
|--------|:---:|
| Sesión cerrada por inactividad (`session_abandoned`) | OUTCOME |
| Kill-switch `BOT_ENABLED=false` fuerza escalado | MILESTONE |
| Recordatorio de inactividad · webhook recibido · WAL insert/markdone · `session_started` · resolución convID | FULL |
| **Phone-lock timeout** (mensaje no procesado) · drop por overflow del worker · **anomalía de reconciliación** | ERROR |

---

## 4. Pilar 2 — Reconciliación de invariantes (detección de "mal comportamiento")

Lo que un log de pasos **no** detecta: lógica que se saltó silenciosamente sin lanzar excepción. Un job
barato (corre cada hora o en el cleanup 02:00) cruza acciones del bot vs estado real (BD local + SIESA)
**solo sobre los últimos 4–7 días** (queries sargables) y emite `flow_events` con `flow=invariante`,
`outcome=error`, `step=anomaly` + severidad cuando algo no cuadra. Catálogo inicial (los mismos checks
de la auditoría, ahora automatizados):

| Invariante | Detecta |
|-----------|---------|
| Toda cita creada tiene su(s) fila(s) CPA/CP y su(s) slot(s) con `IdCita` | cita a medias |
| Toda cita cancelada liberó su slot (`IdCita NULL`) | slot huérfano (N-42) |
| Toda CONSULTA tiene `Valor>0` en CPA | TARIFA PENDIENTE silenciosa |
| `cod_medi == pmd.Medico`; `id_sede` correcto por asunto | inconsistencia de agenda |
| Ninguna combinación MRC supera el tope mensual | sobre-cupo MRC |
| Toda notificación enviada tiene resolución dentro de su ventana | notificación colgada |
| Toda sesión escalada tiene desenlace (cerrada/expirada) | sesión zombi |
| Toda entrada de lista de espera `notified` tiene respuesta o expiró en 24h | oferta atascada |

Las anomalías se consultan por endpoint **y** se alertan a Telegram (con su `trace_id`).

---

## 5. Pilar 3 — Capa de consulta (endpoints)

Bajo `/api/internal/*` (auth X-API-Key + rate-limit, como el resto):

| Endpoint | Devuelve |
|----------|----------|
| `GET /flow-trace?trace_id=wl:123` | **timeline ordenado** completo de un recorrido |
| `GET /flow-trace?phone=+57...&flow=&from=&to=` | trazas de un paciente (lista de `trace_id`) |
| `GET /flow-events?flow=&outcome=&reason=&from=&to=&limit=` | **consulta por tipo** (p.ej. todos los `error`, todos los `wl_skipped`) |
| `GET /flow-stats?flow=&from=&to=` | **funnel** (caída por paso) + distribución de terminales + tasa de error por `reason` |
| `GET /anomalies?from=&to=&severity=` | anomalías abiertas (Pilar 2) |

Telegram: el `AlertHandler` incluye el `trace_id` en cada alerta de error/anomalía → un clic y se tiene
el recorrido completo (push = aviso, pull = investigación).

**Endpoints NUEVOS vs existentes** (no se solapan — cada uno consulta su capa, ver §11):

| Endpoint | Capa que consulta | Estado |
|----------|-------------------|--------|
| `GET /flow-trace` · `/flow-events` · `/flow-stats` · `/anomalies` | `flow_events` (recorrido de negocio) | **NUEVOS** (este diseño) |
| `GET /logs` | `slog` (texto técnico) | ya existe |
| `GET /events` | `chat_events` (conversación cruda) | ya existe |
| `GET /kpis/*` | `chat_events` agregado | ya existe |

---

## 6. Eficiencia (bajo consumo de servidor)

| Palanca | Efecto |
|---------|--------|
| Escritor **batched + asíncrono propio** (canal con buffer + flush; §10.2) | 0 I/O por evento; se vacía en lotes |
| **Gate de nivel ANTES de encolar** (`FLOW_TRACE_LEVEL`) | el detalle caro (`full`) ni se construye |
| Eventos **tipados** (1 por decisión) vs N líneas de texto | menor volumen |
| Índices **mínimos** + **retención 45d** + **rollup diario** persistido | inserts rápidos, tabla acotada |
| Reconciliación sobre **ventana reciente** (4-7d), **diaria**, con `NOLOCK` | no presiona la UI de SIESA |
| Todo sobre el **store local que ya corre** (sin APM externo) | sin infra nueva |

Estimación de volumen (§10.8) confirma que en `milestone` la tabla queda en el orden de **miles de filas/día**.

> Opcional futuro: exportar a OpenTelemetry → Grafana/Loki/Tempo para dashboards visuales. No necesario;
> el enfoque local cubre el 90% sin costo de infra.

---

## 7. Componentes a construir
Especificación concreta en §10. Resumen:
1. **Migración** `flow_events` + `flow_daily_stats` (§10.1).
2. **`FlowTracer`** — escritor batched asíncrono **propio** + gate de nivel (§10.2).
3. **Registro central** `(flow, step) → {level, outcome, ref_type}` (§10.3): única fuente de verdad de §3A.
4. **Helpers de `trace_id`** por flujo (§10.4).
5. **Sanitizer de PII** para `attrs`/`phone` (§10.5).
6. **`flow_repo`** (queries) + **endpoints** §5 en `internal_handler.go`.
7. **Job de reconciliación** (`internal/observability/reconcile.go`) en `data_cleanup` 02:00 (§10.6).
8. **`trace_id` en el `AlertHandler`** (Telegram).
9. **Retención + rollup** en `data_cleanup` 02:00.

---

## 8. Plan por fases — **cortes verticales** con criterios de aceptación

Cada fase es un **corte vertical** (emit → store → query) que se prueba de punta a punta antes de la
siguiente. Cada una se valida con `go test -race` + lint y se commitea por separado.

### Fase 0 — Pipeline base + piloto lista de espera (vertical completo) — ✅ HECHA (2026-06-25)
- Migración `flow_events` (+ rollup). `FlowTracer` (§10.2) + registro central (§10.3) + helpers
  trace_id (§10.4) + sanitizer (§10.5). `flow_repo` + endpoint `GET /flow-trace?trace_id=`.
  Instrumentar **solo lista de espera** (los hitos de §3.1) de punta a punta.
- **Aceptación:** test que emite el recorrido completo de una lista de espera
  (`enrolled→slot_match→notified→response→booked`) y `GET /flow-trace?trace_id=wl:X` los devuelve
  **ordenados**; con `FLOW_TRACE_LEVEL=outcome` solo aparece `booked`; con `=off`, ninguno; el `phone`
  sale enmascarado.

### Fase 1 — Consulta por tipo + flujos núcleo restantes — ✅ HECHA (2026-06-25)
- Endpoint `GET /flow-events?flow=&outcome=&reason=&from=&to=`. Instrumentar **agendamiento** y
  **recordatorio/IVR** (§3.2, §3.3).
- **Aceptación:** un flujo de agendamiento que termina en `gfr_blocked` aparece en
  `/flow-events?flow=agendar&outcome=blocked`; un error de OCR aparece en `?outcome=error`.

### Fase 2 — Reconciliación de invariantes + Telegram — ✅ HECHA (2026-06-25)
- Job §10.6 en `data_cleanup`; emite `flow=invariante outcome=error`. `trace_id` en `AlertHandler`.
- **Aceptación:** sembrando un slot huérfano de prueba, el job emite una anomalía consultable por
  `GET /anomalies`; la alerta de Telegram incluye el `trace_id`.

### Fase 3 — Agregación + rollup — ✅ HECHA (2026-06-25)
- Endpoint `GET /flow-stats` (funnel + distribución de terminales + tasa de error). Rollup nocturno
  `flow_events → flow_daily_stats`. Retención 45d de crudos.
- **Aceptación:** `/flow-stats?flow=agendar` devuelve el funnel con la caída por paso; el rollup del día
  anterior cuadra con el conteo de crudos.

### Fase 4 — Resto de flujos — ✅ HECHA (2026-06-25)
- Instrumentar registro, mis citas, entidad/EPS, identificación, admin, escalación, agente, scheduler,
  infra (según §3A). Ejecutada en sub-lotes para minimizar riesgo:
  - **Lote A** — identificación + entidad/EPS + registro. ✅
  - **Lote B** — mis citas + escalación (incluye agente: resume/reset/close, escalation_expired). ✅
  - **Lote C** — scheduler (`task_completed`/`task_failed`), admin_agenda (`agenda_cancelled`/
    `agenda_rescheduled`/`patients_notified`), infra (`session_abandoned`, `phone_lock_timeout`). ✅
- **Aceptación:** cada flujo tiene su recorrido consultable por `trace_id` en `milestone`. El test
  `TestCatalog_AllEmittedStepsRegistered` verifica que todo step emitido tiene entrada en el catálogo §3A.

---

## 9. Decisiones de diseño
1. **Tabla dedicada `flow_events`** (recomendado) vs columna `trace_id` en `chat_events`. La dedicada da
   esquema/índices/retención propios y separa traza de log conversacional.
2. **Retención**: 45 días crudos + rollup diario indefinido (alineado con `notification_history`).
3. **Nivel de detalle por `.env`** (`FLOW_TRACE_LEVEL`, ver §3A): `off` < `error` < `outcome` <
   `milestone` (default prod) < `full`. La clasificación de cada acción del proyecto está en §3A.
   Override por teléfono (`trace=full`) para depuración puntual.

---

## 10. Especificación de implementación

### 10.1 Migraciones
Dos archivos `golang-migrate` (`migrations/0NN_create_flow_events.{up,down}.sql`) con los DDL de §1
(`flow_events` + `flow_daily_stats`). `up` crea; `down` hace `DROP TABLE`. Numerar tras la última
migración existente.

### 10.2 `FlowTracer` — escritor batched asíncrono **propio**
NO reusa el batcher de `chat_events` (otra tabla). Es un componente nuevo, espejo del patrón
no-bloqueante del `AlertHandler`:

```go
type FlowTracer struct {
    repo    FlowRepo            // InsertBatch([]FlowEvent) error
    ch      chan FlowEvent      // buffer (p.ej. 1024)
    maxLvl  int                 // de FLOW_TRACE_LEVEL
    fullFor func(phone string) bool // override por-teléfono (Fase 0: siempre false)
}

// Emit es no-bloqueante y barato. El gate de nivel ocurre ANTES de construir/encolar.
func (t *FlowTracer) Emit(ctx, traceID, flow, step string, o EmitOpts) {
    spec, ok := catalog[key(flow, step)]      // §10.3
    if !ok { spec = defaultSpec; logUnknownStep(flow, step) }
    lvl := spec.Level
    if lvl > t.maxLvl && !(o.Phone != "" && t.fullFor(o.Phone)) {
        return // descartado por nivel
    }
    ev := FlowEvent{TraceID: traceID, Flow: flow, Step: step, Level: lvl,
        Outcome: pick(o.Outcome, spec.Outcome), Reason: o.Reason,
        Phone: utils.MaskPhone(o.Phone), RefType: spec.RefType, RefID: o.RefID,
        Attrs: sanitizeAttrs(o.Attrs)}        // §10.5
    select { case t.ch <- ev: default: /* buffer lleno: dropear + contador, no bloquear */ }
}
```
- Goroutine `Start(ctx)`: lee `ch`, acumula y hace `InsertBatch` cada **N=200 eventos** o **2 s** (lo que
  ocurra primero); en `ctx.Done()` **drena** el buffer (igual que el worker pool y el scheduler).
- Buffer lleno → se dropea con un contador (nunca bloquea el flujo del paciente).
- `Emit` se llama desde los handlers; en tests sin tracer, un `nil`-guard lo hace no-op.

### 10.3 Registro central `(flow, step) → {level, outcome, ref_type}`
**Única fuente de verdad de §3A en código** — evita etiquetar a mano y mantiene consistencia:

```go
var catalog = map[string]stepSpec{
    "lista_espera/enrolled":   {Level: Milestone, Outcome: "ok"},
    "lista_espera/slot_match": {Level: Milestone, Outcome: "ok", RefType: "slot"},
    "lista_espera/notified":   {Level: Milestone, Outcome: "ok", RefType: "bird_msg"},
    "lista_espera/booked":     {Level: Outcome,   Outcome: "ok", RefType: "cita"},
    "lista_espera/expired":    {Level: Outcome,   Outcome: "info"},
    "lista_espera/skipped":    {Level: Full,      Outcome: "info"},
    "lista_espera/notify_failed": {Level: Error,  Outcome: "error"},
    // ... una entrada por cada fila de §3A (test de cobertura: toda fila de §3A tiene su entrada)
}
```
Un **test** verifica que cada `step` usado en el código existe en `catalog` (y viceversa) → §3A y el
código nunca se desincronizan. `step` desconocido en runtime → default `Milestone/info` + warn (fail-safe).

### 10.4 Derivación de `trace_id` (helpers, sin ambigüedad)
```go
TraceSession(sessID)            => "sess:" + sessID
TraceWaitingList(wlID)          => "wl:" + wlID
TraceNotif(apptID, apptDate)    => "notif:" + apptID + ":" + apptDate.Format("20060102")  // FECHA DE LA CITA
TraceAgenda(agendaID, date)     => "agenda:" + agendaID + ":" + date
TraceTask(name, runDate)        => "task:" + name + ":" + runDate.Format("20060102")
```
- En handlers de la máquina de estados el `trace_id` se deriva de `sess.ID`.
- **Cruce de trazas** (lista de espera que agenda): en `CREATE_APPOINTMENT`, si hay
  `waiting_list_entry_id` en la sesión, se emite **dos** veces — `booked` en `wl:<id>` y `booking_success`
  en `sess:<id>` — **ambos con `ref_id = cita_id`** (pivote). Patrón ya esbozado en `slots.go:1288`.

### 10.5 PII en `attrs` (mecanismo, no solo regla)
- `phone` SIEMPRE vía `utils.MaskPhone` dentro de `Emit`.
- `attrs` se filtra por un **vocabulario fijo de claves permitidas** (`cups`, `asunto`, `contrato`,
  `espacios`, `servicio`, `trigger`, `duration_ms`, `n`, `remaining`, `count`, …). `sanitizeAttrs`
  descarta cualquier clave fuera del set → imposible filtrar `nombre`/`documento` por descuido.
- Prohibido en `attrs`: documento, nombre, dirección, texto libre del paciente.

### 10.6 Job de reconciliación (`internal/observability/reconcile.go`)
- Corre dentro de `data_cleanup` (**02:00, diario**); frecuencia configurable (`RECONCILE_HOURS`,
  default 24). **NO** cada hora por defecto: SIESA es sensible a contención con la UI.
- Ventana: **últimos 4-7 días** (`RECONCILE_WINDOW_DAYS`, default 4 — alineado con la regla de validación
  del proyecto). Todas las queries con `WITH (NOLOCK)` y acotadas por fecha (sargables).
- Cada invariante de §4 = una query que devuelve filas en violación → por cada una, `tracer.Emit(...,
  "invariante", "anomaly", {Outcome:"error", Reason:<check>, Attrs:{severity, count}})`.
- Las anomalías quedan consultables (`/anomalies`) y disparan Telegram con su `trace_id`.

### 10.7 Configuración `FLOW_TRACE_LEVEL`
- Se lee en `config.Load` al arranque (cambiar nivel = reinicio). Valores: `off|error|outcome|milestone|full`;
  default **`milestone`**; inválido → `milestone` + warn.
- **`off` apaga `flow_events`, NO el path de error de slog/Telegram** (el `AlertHandler` sigue avisando;
  son sistemas separados).
- **Override por-teléfono** (`trace=full`): Fase 0 lo deja como no-op (`fullFor` retorna false); cuando se
  active, vivirá como un set en memoria poblado por un endpoint admin (`POST /flow-trace/full?phone=`).

### 10.8 Estimación de volumen (valida "bajo consumo")
Supuesto conservador: ~300 conversaciones/día × ~12 eventos `milestone` + ~200 notificaciones × ~4 +
overhead ≈ **~5.000 filas/día**. ×45 días retención ≈ **~225 k filas** (decenas de MB con los 4 índices).
En `full`, súbelo ~5-10× **solo para los teléfonos/sesiones bajo `trace=full`** (no global). Conclusión:
en `milestone` el costo es trivial para MySQL local; `full` se usa acotado.

---

## 11. Relación con los logs existentes (no se reemplazan)

Tres **capas** complementarias, con audiencia y endpoint propios. `flow_events` **no** sustituye a las
otras dos (no guarda cada mensaje, ni *stack traces*, ni errores crudos; y Telegram depende de `slog`).

| Capa | Pregunta que responde | Audiencia | Endpoint |
|------|-----------------------|-----------|----------|
| **`slog`** (texto/archivo + Telegram) | "¿qué pasó **técnicamente**?" (errores, panics, HTTP, terceros, debug) | dev/infra | `/logs` |
| **`chat_events`** (BD) | "¿qué se **dijo exactamente**?" (cada mensaje, cada transición) | soporte/forense | `/events`, `/kpis` |
| **`flow_events`** (BD) | "¿cómo fue el **recorrido** y se cumplió la lógica?" | producto/QA/soporte | `/flow-*`, `/anomalies` |

**Qué se mantiene y qué se ajusta al instrumentar un flujo:**
- `slog.Error` → **se queda siempre** (alimenta Telegram + detalle técnico). El `flow_event` de error es
  su compañero consultable, no su sustituto.
- `slog.Debug` (verboso) → **se queda** (depuración a nivel de código).
- `slog.Info` de un **punto de decisión** que se promueve a `flow_event` → se **degrada a `slog.Debug`**
  (no se borra): deja de inflar Info pero sigue disponible para depurar en vivo.
- `chat_events` → **intacto** (granularidad mensaje-a-mensaje, distinta de los hitos de negocio).

**Regla de rollout:** durante las fases NO se quita nada; `flow_events` se construye en paralelo. El
*downgrade* Info→Debug de un `slog.Info` promovido se hace **solo** cuando ese flujo ya quedó
instrumentado y probado — nunca antes, y nunca a los `Error`.
