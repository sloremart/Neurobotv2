# Informes de asesores con Néctar (D5) — Plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que contestaciones, abandonos e informe por asesor de la línea Néctar se midan igual que los de Bird y
el dashboard los muestre separados por línea mientras Bird tenga datos.

**Architecture:** El bot lee periódicamente `GET /api/bot/waiting-cycles` y `GET /api/bot/agents` de Néctar, los
guarda tal cual en tablas locales y, con un emparejamiento puro (escalación ↔ atención de tramos), completa las
columnas de `escalations` que hoy solo llena Bird. Dos marcas de desenlace se corrigen (cola vencida y cierre por
silencio sin respuesta → `agent_no_show`) y el aviso de resuelta guarda quién cerró y quién escaló. El dashboard,
que lee la misma base `neuro_bot`, calcula cada cifra por línea (`phone_number LIKE 'nectar:%'`).

**Tech Stack:** Go 1.26 (bot y dashboard), MySQL 8 (`neuro_bot`, migraciones golang-migrate), React + TypeScript +
Vite (frontend del dashboard).

**Spec:** `docs/superpowers/specs/2026-10-30-informes-nectar-design.md` (léase entero). Contrato de Néctar:
`C:\laragon\www\colibrixa-web\nectar\docs\PARA-EL-EQUIPO-DEL-BOT.md`, enmiendas Plan 45, 46 y 43 (al final).

## Global Constraints

- Rama `feat/migracion-nectar` en **ambos** repos (`C:\laragon\www\CHATBOT\Neurobotv2` y
  `C:\laragon\www\CHATBOT\neuro-dashboard`). Nunca merge a `main`, nunca push.
- Nunca `--no-verify` ni `LEFTHOOK=0`. Nunca secretos reales.
- No se suben: en el bot `auditoria/`, `bin/`, `cmd/e2e/`, `docs/CASO-*`, `docs/MAPA-*`,
  `internal/api/test_handler.go`, `scripts/`, `seed-kpis`, `.claude/`,
  `docs/superpowers/plans/2026-10-29-ivr-recordatorio-fallido.md`; en el dashboard `web/`.
- Con el puente de Néctar apagado (`NECTAR_BASE_URL` vacía) bot y dashboard se comportan **exactamente** como hoy.
- Nada de esto cambia lo que el paciente ve o recibe.
- Identificadores en inglés, comentarios en español (estilo del repo).
- Pruebas del bot y del dashboard (Go) en Docker:
  `gotest(){ MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app -v neurobot-go-mod:/go/pkg/mod -v neurobot-go-build:/root/.cache/go-build golang:1.26-alpine go "$@"; }`.
  Línea base del bot: exactamente 3 fallas conocidas (`TestAskClientType_Particular_SkipsListToDocument`,
  `TestMedicationCheck_SanitasNoChannel_Escalates`, `TestMedicationCheck_SanitasWithChannel_SendsLink`).
- Pruebas de repositorio contra MySQL real con build tag `integration` (patrón de
  `internal/repository/local/contact_identifier_integration_test.go`, `LOCAL_TEST_DSN`).
- Néctar: rutas firmadas igual que el resto, `?n=<uuid>` irrepetible en cada GET (si no, `401 replay`); la clave
  global da `403 {"error":"account_key_required"}`; `since` obligatorio; `until - since` ≤ 62 días; `limit` ≤ 200.
- Datos de Néctar válidos desde **2026-10-30T22:18:00Z** (o el `history_since` que devuelva Néctar, el más reciente).
- `agent_id` de Néctar en `escalations` va prefijado: `nectar:<id>` (no choca con los UUID de Bird).
- Tolerancia de reloj del emparejamiento: **2 minutos**.
- Periodicidad de la sincronización: `NECTAR_REPORTS_SYNC_MIN`, por defecto **5**; ≤0 cae al defecto.
- Margen de relectura de la receta §3(a): **10 minutos**.

## Rulings sobre el spec

- **§5.5 del spec (endpoint del roster de Néctar) se sustituye por lectura directa de la tabla**: el dashboard ya
  lee la base `neuro_bot` (`internal/config` del dashboard, `DB_DATABASE=neuro_bot`) y el roster de Néctar vive en
  ella; un endpoint sería una capa sin valor. Costo si es un error: añadir un handler fino más tarde.
- La corrección del cierre por silencio (§5.3 del spec) la hace **solo la sincronización**; el barrido no cambia.
- Las atenciones se marcan en la tabla de tramos (`escalation_id` en cada tramo de la atención); con eso «sin dato
  de Néctar» y «otros chats atendidos en Néctar» salen por SQL sin columnas extra en `escalations`.

## Review Focus

1. Un tramo que se re-lee con datos nuevos (antes abierto, ahora con `first_response_at`/`closed_at`) debe
   sobrescribir el guardado y volver a completar su escalación — prueba en Task 4.
2. Dos escalaciones del bot en la misma conversación con un re-escalado encadenado por `previous_cycle_id`: la
   segunda no hereda la respuesta de la primera — prueba en Task 3.
3. Clave global (403 `account_key_required`): la sincronización se detiene con **un** log de error, sin reintentar
   cada 5 minutos ni contar para `nectar_down` más de una vez — prueba en Task 4.
4. Puente apagado: ni tarea, ni lecturas, ni cambios en el dashboard (las columnas Néctar no aparecen) — pruebas
   en Task 4 y Task 8.
5. Un período con datos solo de Bird o solo de Néctar muestra **una** cifra; con las dos, dos columnas — prueba en
   Task 8.

---

## Parte A — Bot (`C:\laragon\www\CHATBOT\Neurobotv2`)

### Task 1: Lecturas de Néctar en el cliente

**Files:**
- Create: `internal/nectar/reports.go`
- Test: `internal/nectar/reports_test.go`

**Interfaces:**
- Produces:
  - `const WaitingCyclesPath = "/api/bot/waiting-cycles"`, `const AgentsPath = "/api/bot/agents"`
  - `var ErrAccountKeyRequired = errors.New("nectar: la ruta exige la clave de cuenta")`
  - `type WaitingCycle struct { ID, ConversationID int64; PreviousCycleID, TeamID, AgentID *int64; OpenedAt time.Time; AssignedAt, FirstResponseAt, ClosedAt *time.Time; CloseReason *string; Backfilled bool; ContactMessagesBeforeFirstResponse int }` con las etiquetas JSON del contrato.
  - `type WaitingCyclesPage struct { Cycles []WaitingCycle; NextCursor *string; HistorySince *time.Time }`
  - `type ReportAgent struct { ID int64; Name string; Active bool; Presence string; Teams []ReportTeam; OpenConversations int }`, `type ReportTeam struct { ID int64; Name string }`
  - `func (c *Client) GetWaitingCycles(ctx context.Context, since, until time.Time, cursor string) (WaitingCyclesPage, error)` — `limit=200` fijo.
  - `func (c *Client) GetAgents(ctx context.Context) ([]ReportAgent, error)`

- [ ] **Step 1: Pruebas que fallan** (httptest, patrón de `schedule_test.go`):
  - `TestGetWaitingCyclesSendsQuery`: la URL lleva `since`/`until` RFC3339 en UTC, `limit=200`, `cursor` solo si no
    es vacío, y un `n` distinto en dos llamadas seguidas; lleva la cabecera de clave de servicio y firma.
  - `TestGetWaitingCyclesDecodesContractShape`: el JSON de ejemplo del contrato (Plan 46, con
    `contact_messages_before_first_response: 2`) y otro tramo con `previous_cycle_id`, `team_id`,
    `first_response_at`, `closed_at`, `close_reason` en `null` → punteros nil; `next_cursor: null` → nil.
  - `TestGetWaitingCyclesAccountKeyRequired`: 403 `{"error":"account_key_required"}` → `errors.Is(err, ErrAccountKeyRequired)`.
  - `TestGetWaitingCyclesBadQuery`: 400 `{"error":"invalid_query"}` → error que no es `ErrAccountKeyRequired`; 401 → `ErrUnauthorized`.
  - `TestGetAgentsDecodes`: el ejemplo del contrato; `teams: []` queda slice vacío; 403 → `ErrAccountKeyRequired`.
- [ ] **Step 2:** `gotest test ./internal/nectar/ -run 'TestGetWaitingCycles|TestGetAgents'` → FAIL (no compila).
- [ ] **Step 3:** Implementar con `exchange` (GET sin cuerpo; la firma cubre la URL completa, query incluida),
  `uuid.NewString()` para `n`. Sin reintentos.
- [ ] **Step 4:** Mismo comando → PASS; `gotest vet ./internal/nectar/`.
- [ ] **Step 5: Commit** `feat(d5): lecturas de waiting-cycles y agents de Néctar`.

### Task 2: Migración 047 y repositorio de informes

**Files:**
- Create: `migrations/047_nectar_reports.up.sql`, `migrations/047_nectar_reports.down.sql`
- Create: `internal/repository/local/nectar_reports_repo.go`
- Modify: `internal/repository/local/escalation_repo.go`
- Test: `internal/repository/local/nectar_reports_repo_integration_test.go` (`//go:build integration`)

**Interfaces:**
- Consumes: `nectar.WaitingCycle`, `nectar.ReportAgent` (Task 1).
- Produces (esquema):
  - `nectar_waiting_cycles` (PK `id` BIGINT = id de Néctar; `conversation_id` BIGINT NOT NULL;
    `previous_cycle_id`, `team_id`, `agent_id` BIGINT NULL; `opened_at` DATETIME(0) NOT NULL; `assigned_at`,
    `first_response_at`, `closed_at` DATETIME NULL; `close_reason` VARCHAR(20) NULL; `backfilled` TINYINT(1);
    `contact_msgs_before_response` INT NOT NULL DEFAULT 0; `escalation_id` BIGINT UNSIGNED NULL; `synced_at`
    TIMESTAMP; índices por `conversation_id, opened_at`, `opened_at`, `escalation_id`). Horas en UTC.
  - `nectar_agents` (PK `id` BIGINT; `name` VARCHAR(150); `active` TINYINT(1); `presence` VARCHAR(20); `teams`
    JSON; `open_conversations` INT; `synced_at` TIMESTAMP).
  - `nectar_sync_state` (PK `name` VARCHAR(40); `value` VARCHAR(64); `updated_at`): filas `last_seen_opened_at`,
    `history_since`, `last_success_at`.
  - `escalations` + `contact_msgs_before_response` INT NULL, `resolved_by` VARCHAR(40) NULL, `escalated_by`
    VARCHAR(40) NULL (`ADD COLUMN ... NULL`, todas al final).
- Produces (Go, `NectarReportsRepo`):
  - `UpsertCycles(ctx, []nectar.WaitingCycle) error` — `INSERT ... ON DUPLICATE KEY UPDATE` de todo **menos** `escalation_id`.
  - `OldestOpenCycleOpenedAt(ctx) (*time.Time, error)` — tramo con `closed_at IS NULL` más viejo.
  - `CyclesForConversations(ctx, convIDs []int64) ([]nectar.WaitingCycle, map[int64]*int64 /*escalation_id*/, error)`
  - `SetCycleEscalation(ctx, cycleIDs []int64, escalationID uint64) error`
  - `ReplaceAgents(ctx, []nectar.ReportAgent) error` (upsert; no borra inactivos: Néctar ya los devuelve)
  - `GetState(ctx, name string) (string, error)` / `SetState(ctx, name, value string) error`
- Produces (Go, `EscalationRepo`):
  - `type NectarEscalation struct { ID uint64; SessionID, Phone string; EscalatedAt time.Time; Outcome *string; FirstAgentMsgAt *time.Time; AgentID *string }`
  - `ListNectarEscalationsSince(ctx, since time.Time) ([]NectarEscalation, error)` — `phone_number LIKE 'nectar:%'`.
  - `ApplyNectarAttention(ctx, id uint64, a NectarAttentionUpdate) error` donde
    `type NectarAttentionUpdate struct { FirstResponseAt *time.Time; AgentID, AgentName string; ContactMsgs *int; NoShow bool }`:
    solo de nulo a valor (`COALESCE(col, ?)`); `NoShow` cambia `outcome` `expired` → `agent_no_show` y nada más.
  - `SetResolution(ctx, phone, resolvedBy, escalatedBy string) error` — en la escalación abierta más reciente del
    teléfono, mismo patrón que `Close`; vacío se guarda NULL.

- [ ] **Step 1: Pruebas de integración que fallan**: upsert dos veces el mismo tramo con datos nuevos → una fila con
  los nuevos y `escalation_id` intacto; `OldestOpenCycleOpenedAt`; `ApplyNectarAttention` no pisa un valor existente
  y `NoShow` solo cambia `expired` (no `completed` ni NULL); `SetResolution` toca solo la abierta; estado
  set/get. Limpieza en `t.Cleanup` como el patrón.
- [ ] **Step 2:** Si hay MySQL de prueba: `LOCAL_TEST_DSN=... make docker-test-integration` → FAIL. Si no hay, el
  implementador levanta uno efímero (`mysql:8`, base `neuro_bot_test`, aplicando `migrations/*.up.sql`) y lo
  documenta en su reporte; **no** se toca el MySQL del bot.
- [ ] **Step 3:** Migración (con `down` que revierte todo) y repos. `gotest build ./...`.
- [ ] **Step 4:** Integración → PASS; `gotest test ./internal/repository/...` (unitarias) sin regresiones.
- [ ] **Step 5: Commit** `feat(d5): tablas locales de tramos y asesores de Néctar (migración 047)`.

### Task 3: Emparejamiento escalación ↔ atención (lógica pura)

**Files:**
- Create: `internal/nectarreports/pairing.go`
- Test: `internal/nectarreports/pairing_test.go`

**Interfaces:**
- Consumes: `nectar.WaitingCycle` (Task 1), `local.NectarEscalation` (Task 2) — o un tipo propio equivalente para no
  importar `local` (elección del implementador; sin ciclo de imports).
- Produces:
  - `const ClockTolerance = 2 * time.Minute`
  - `type Attention struct { EscalationID uint64; CycleIDs []int64; FirstResponseAt *time.Time; AgentID *int64; ContactMsgs int; Closed bool }`
  - `func Pair(escalations []Escalation, cycles []nectar.WaitingCycle) []Attention`
  - `func ConversationID(phone string) (int64, bool)` — `nectar:<id>` → id.

Reglas (spec §5.2/§5.3, sin interpretar):
- Para cada escalación, de su conversación: el **primer** tramo con `opened_at ≥ escalated_at − 2 min` y
  `opened_at <` la `escalated_at − 2 min` de la siguiente escalación de esa conversación; un tramo ya tomado por
  otra escalación no se reutiliza.
- La atención = ese tramo + sus sucesores por `previous_cycle_id`, cortando antes del primer sucesor que sea el
  tramo emparejado de otra escalación.
- `FirstResponseAt` = la menor `first_response_at` no nula de la atención; `AgentID` = el `agent_id` del tramo de esa
  respuesta, o si nadie respondió, el del último tramo con `agent_id`; `ContactMsgs` = suma de la atención hasta la
  primera respuesta (incluido el tramo que respondió); `Closed` = todos los tramos tienen `closed_at`.
- Escalación sin tramo → no aparece en el resultado.

- [ ] **Step 1: Pruebas que fallan** (tabla): un tramo exacto; tramo 90 s antes (entra) y 3 min antes (no);
  escalación encolada con tramo 12 h después (entra); dos escalaciones en la misma conversación con dos tramos
  (cada una el suyo); re-escalado encadenado (`previous_cycle_id` apunta al tramo de la primera) → la segunda NO
  hereda la respuesta de la primera; traspaso (cadena sin otra escalación) → misma atención y respuesta del segundo
  tramo; nadie respondió → `AgentID` del último asignado y `FirstResponseAt` nil; `ContactMsgs` sumado; sin tramo →
  ausente; `ConversationID("nectar:4410")` = 4410 y `"+57300…"` → false.
- [ ] **Step 2:** `gotest test ./internal/nectarreports/` → FAIL.
- [ ] **Step 3:** Implementar `Pair` (puro, sin E/S, orden determinista).
- [ ] **Step 4:** PASS + `gotest vet ./internal/nectarreports/`.
- [ ] **Step 5: Commit** `feat(d5): emparejamiento de escalaciones con atenciones de Néctar`.

### Task 4: Sincronizador y su arranque

**Files:**
- Create: `internal/nectarreports/syncer.go`
- Test: `internal/nectarreports/syncer_test.go`
- Modify: `internal/config/config.go` (+ `NectarReportsSyncMin`, `DefaultNectarReportsSyncMin = 5`)
- Modify: `cmd/server/nectar_wiring.go` (arranque junto a `startNectarSchedule`, con `safeGo`)
- Modify: `.env.example`, `CLAUDE.md` (sección NÉCTAR, una viñeta)

**Interfaces:**
- Consumes: Task 1 (`GetWaitingCycles`, `GetAgents`, `ErrAccountKeyRequired`), Task 2 (repos), Task 3 (`Pair`).
- Produces:
  - interfaces locales `reportsReader` (las dos lecturas), `cyclesStore`, `escalationsStore` para inyectar fakes.
  - `type Syncer struct{...}`; `func NewSyncer(r reportsReader, cycles cyclesStore, esc escalationsStore, every time.Duration, floor time.Time) *Syncer`
  - `func (s *Syncer) RunOnce(ctx context.Context) error`; `func (s *Syncer) Run(ctx context.Context)` (primera
    pasada al arrancar, luego cada `every`; termina al cancelar ctx o tras `ErrAccountKeyRequired`).
  - `var DataFloor = time.Date(2026, 10, 30, 22, 18, 0, 0, time.UTC)`

`RunOnce`, en orden:
1. `since` = min(tramo abierto más viejo guardado, `last_seen_opened_at` − 10 min); sin estado previo, `DataFloor`;
   nunca antes de `DataFloor`. Partir en ventanas de ≤62 días hasta ahora; paginar cada una con `next_cursor`.
2. `UpsertCycles` de todo lo leído; guardar `last_seen_opened_at` (máxima `opened_at` vista) y `history_since`.
3. `GetAgents` → `ReplaceAgents`.
4. Escalaciones `nectar:%` desde `since` − 1 día; tramos de sus conversaciones; `Pair`; por cada atención:
   `SetCycleEscalation` y `ApplyNectarAttention` con `AgentID = "nectar:<id>"`, `AgentName` del roster,
   `ContactMsgs`, y `NoShow = Closed && FirstResponseAt == nil && outcome == "expired"`.
5. `last_success_at` = ahora.
Errores: `ErrAccountKeyRequired` → un `slog.Error` explicando que hace falta la clave de cuenta y `Run` termina;
cualquier otro → `slog.Warn` y se reintenta en la siguiente vuelta (sin alerta propia).

Arranque: solo si el puente está encendido (`bridgeClient != nil && Configured()`); con puente apagado ni se crea.

Comprobación obligatoria (spec §5.2): llenar `first_agent_msg_at` de una escalación `nectar:%` no debe cambiar lo
que ve el paciente. `EscalationRepo.HasAgentResponded` gobierna el acuse «🙋 Seguimos aquí» (`worker/pool.go`).
El implementador verifica en el código si una sesión `nectar:*` escalada recibe mensajes del paciente por ese camino
y lo escribe en su reporte; si los recibe, añade una prueba que muestre que el único efecto es no mandar el acuse
cuando el asesor ya respondió.

- [ ] **Step 1: Pruebas que fallan** con fakes: primera pasada usa `DataFloor`; segunda usa el tramo abierto más
  viejo cuando es anterior a `last_seen − 10 min`; un rango de 70 días se parte en dos ventanas; pagina hasta
  `next_cursor` nil; re-lectura de un tramo que pasó a tener respuesta completa su escalación (Review Focus 1);
  `expired` + atención cerrada sin respuesta → `NoShow`; `expired` con respuesta → no; 403 → `Run` termina tras **una**
  lectura y un solo log (Review Focus 3); `NewSyncer` no se arranca con puente apagado (prueba del wiring, patrón de
  las pruebas de `nectar_wiring`).
- [ ] **Step 2:** `gotest test ./internal/nectarreports/ ./cmd/server/` → FAIL.
- [ ] **Step 3:** Implementar; config `NECTAR_REPORTS_SYNC_MIN` (≤0 → 5); `.env.example` y viñeta de `CLAUDE.md`.
- [ ] **Step 4:** PASS; suite completa del bot = solo las 3 fallas base.
- [ ] **Step 5: Commit** `feat(d5): sincronización periódica de tramos y asesores de Néctar`.

### Task 5: Cola vencida → «el asesor no contestó»

**Files:**
- Modify: `internal/session/manager.go` (+ `CompleteAsNoShow`)
- Modify: `internal/worker/queue_expired.go` (usarlo en vez de `Complete`)
- Test: `internal/worker/queue_expired_test.go` (o el archivo de pruebas existente de la cola vencida) y `internal/session/manager_test.go`

**Interfaces:**
- Produces: `func (m *SessionManager) CompleteAsNoShow(ctx context.Context, s *Session) error` — si la sesión está
  escalada marca la escalación con `escalations.NoShow(ctx, s.ID)` y luego completa la sesión igual que `Complete`
  (el `Close` posterior no encuentra escalación abierta y no pisa la marca).

- [ ] **Step 1: Pruebas que fallan:** `handleQueueExpired` deja la escalación en `agent_no_show` (fake de
  escalaciones: se llama `NoShow` y **no** `Close`); el aviso al paciente y el cierre de la conversación siguen
  ocurriendo igual (las aserciones existentes de esa prueba no cambian).
- [ ] **Step 2:** FAIL. **Step 3:** implementar. **Step 4:** PASS + suite completa = 3 fallas base.
- [ ] **Step 5: Commit** `fix(d5): la cola vencida de Néctar cuenta como asesor que no contestó`.

### Task 6: Quién cerró y quién escaló

**Files:**
- Modify: `internal/nectar/notice.go` (`Actor`, `EscalatedBy` en `Notice`)
- Modify: `internal/api/nectar_handler.go` (pasarlos en el `AgentCommand` de `KindConversationResolved`)
- Modify: `internal/worker/agent_command.go`, `internal/worker/agent_return.go` (guardar antes de cerrar)
- Test: `internal/nectar/notice_test.go`, `internal/api/nectar_handler_test.go`, prueba del worker del cierre

**Interfaces:**
- Produces:
  - `type NoticeActor struct { Class string \`json:"clase"\`; AgentID int64 \`json:"agent_id,omitempty"\` }`;
    `Notice.Actor *NoticeActor \`json:"actor,omitempty"\``; `Notice.EscalatedBy *NoticeActor \`json:"escalada_por,omitempty"\``.
  - `func (a *NoticeActor) Label() string` → `"agente:8"`, `"bot"`, `"sistema"`; nil → `""` (no consta).
  - `AgentCommand.ResolvedBy`, `AgentCommand.EscalatedBy string` (solo con `Action "close"` de resuelta).
  - El worker, al cerrar por resuelta, llama `SetResolution(ctx, phone, resolvedBy, escalatedBy)` (Task 2) antes de
    completar; fallo → `slog.Warn`, el cierre sigue.

- [ ] **Step 1: Pruebas que fallan:** el aviso del contrato (Plan 43) decodifica `actor` y `escalada_por`;
  `{"clase":"bot"}` sin `agent_id` → `"bot"`; sin `escalada_por` → `""`; el handler pasa las etiquetas al comando;
  el worker llama `SetResolution` con ellas y después cierra como hoy.
- [ ] **Step 2:** FAIL. **Step 3:** implementar. **Step 4:** PASS + suite completa = 3 fallas base.
- [ ] **Step 5: Commit** `feat(d5): la escalación guarda quién cerró y quién escaló en Néctar`.

## Parte B — Dashboard (`C:\laragon\www\CHATBOT\neuro-dashboard`, rama `feat/migracion-nectar`)

### Task 7: Cifras por línea en el backend

**Files:**
- Create: `internal/kpi/line.go`
- Modify: `internal/kpi/repository.go` (`GetEscalationSLA`, `GetEscalationSLAByAgent`, `GetAgentWindow`,
  `GetAgentEscalationsWindow`, `GetSessionStatusBreakdown`, `GetBreakdownWindow`/`GetSessionBreakdown`, y el conteo
  de abandonadas de `GetDailyKPIs`), `internal/kpi/models.go`, `internal/kpi/handlers.go`
- Test: `internal/kpi/line_test.go` + casos en `internal/kpi/repository_test.go` (sigue su patrón)

**Interfaces:**
- Produces:
  - `type Line string`; `const LineAll Line = ""`, `LineBird Line = "bird"`, `LineNectar Line = "nectar"`.
  - `func lineFilter(line Line, col string) string` → `""`, `" AND " + col + " NOT LIKE 'nectar:%'"`,
    `" AND " + col + " LIKE 'nectar:%'"` (constante, sin parámetros del usuario).
  - Cada función listada gana un parámetro `line Line` (la clave de caché lo incluye).
  - `type ByLine[T any] struct { All T \`json:"all"\`; Bird *T \`json:"bird,omitempty"\`; Nectar *T \`json:"nectar,omitempty"\`; Split bool \`json:"split"\` }`
    con `Split` = las dos líneas tienen al menos una fila en el período.
  - Los handlers de SLA, SLA por agente y desglose de sesiones responden además `"by_line": ByLine[...]`, sin
    quitar ni renombrar los campos actuales (el frontend viejo sigue funcionando).
  - En la línea Néctar: «mensajes sin respuesta» = `escalations.contact_msgs_before_response`; nuevo campo
    `NoNectarData` = escalaciones `nectar:%` sin ningún tramo con su `escalation_id`; nuevo
    `OtherNectarAttended` = tramos con `escalation_id IS NULL AND previous_cycle_id IS NULL` en el período;
    `NectarDataSince` = `history_since`/`last_success_at` de `nectar_sync_state` (si la tabla no existe, nulo).
  - Las sesiones se separan por `sessions.phone_number`; la «pérdida real» (que sale de `flow_events`) se une a
    `sessions` por el id del `trace_id` (`sess:<id>`) para saber su línea.

- [ ] **Step 1: Pruebas que fallan:** `lineFilter` para las tres líneas; `Split` falso con solo Bird, falso con
  solo Néctar, verdadero con las dos (Review Focus 5); con la base sin tablas de Néctar (puente nunca encendido) las
  consultas no fallan y `by_line.split` es falso (Review Focus 4).
- [ ] **Step 2:** `gotest test ./internal/kpi/` → FAIL.
- [ ] **Step 3:** Implementar; las fórmulas no cambian, solo se filtran por línea.
- [ ] **Step 4:** PASS + `gotest test ./...` del dashboard sin regresiones.
- [ ] **Step 5: Commit** (en el dashboard) `feat(d5): cifras de escalación y abandono por línea`.

### Task 8: Informe por asesor con los asesores de Néctar

**Files:**
- Modify: `internal/reports/reports.go`, `internal/reports/agent.go`, `internal/reports/handler.go`
- Test: `internal/reports/agents_source_test.go`, `internal/reports/reports_test.go`, `internal/reports/agent_test.go`

**Interfaces:**
- Consumes: Task 7 (`Line`, funciones con `line`).
- Produces:
  - `func (r *Repository) NectarAgents(ctx) ([]NectarAgentRow, time.Time /*synced_at*/, error)` en `internal/kpi`
    leyendo `nectar_agents` (tabla ausente → vacío, sin error).
  - Filas por asesor con `Platform` = `"bird"` | `"nectar"`; las de Néctar con nombre, presencia y chats abiertos
    del roster y sus cifras de `GetAgentWindow(..., LineNectar)` casadas por `agent_id = "nectar:<id>"`.
  - Llamadas por asesor: para filas Néctar el campo va `null` y el texto/Excel dice «no aplica».
  - `AgentsSource` deja de fijarse a `"bird"` en `BuildReport` (`reports.go:372`): lo pone el handler como hoy, y
    con asesores en las dos plataformas vale `"both"`.

- [ ] **Step 1: Pruebas que fallan:** con roster Bird (proxy fake) y filas `nectar_agents`, el informe lista las
  dos plataformas; un asesor Néctar sin escalaciones aparece con ceros y su presencia; llamadas Néctar = «no
  aplica» (nunca 0); sin tabla `nectar_agents` el informe es idéntico al de hoy; el 409 `agents_in_nectar` del bot
  sigue mostrando solo Néctar.
- [ ] **Step 2:** FAIL. **Step 3:** implementar. **Step 4:** PASS + `gotest test ./...` del dashboard.
- [ ] **Step 5: Commit** `feat(d5): informe por asesor con asesores de Néctar`.

### Task 9: Frontend — dos columnas mientras haya dos líneas

**Files:**
- Modify: `frontend/src/pages/Escalation.tsx`, `frontend/src/pages/Sessions.tsx`, `frontend/src/pages/Overview.tsx`,
  `frontend/src/pages/Reports.tsx` y los tipos/cliente de API que usen (`frontend/src/...`, según el repo)
- Create (si no existe uno equivalente): un componente pequeño `LineSplit` que, con `by_line.split`, pinta «Línea
  Bird» / «Línea Néctar» lado a lado y, sin él, la cifra única de siempre.

- [ ] **Step 1:** Con `by_line.split = true` las tarjetas de contestaciones (TTFR, % atendidas, tramos) y abandonos
  (donut, «pérdida real», `expired` vs `agent_no_show`) se ven por línea; con `false`, exactamente como hoy.
- [ ] **Step 2:** La columna Néctar muestra «Datos desde <fecha>» y «Última sincronización <hora>», «sin dato de
  Néctar» (`NoNectarData`) y «otros chats atendidos en Néctar» (`OtherNectarAttended`) como conteos aparte.
- [ ] **Step 3:** Informe por asesor: columna/etiqueta de plataforma; llamadas «no aplica» en Néctar.
- [ ] **Step 4:** `cd frontend && npm run build` → sin errores de TypeScript ni de Vite.
- [ ] **Step 5: Commit** `feat(d5): el dashboard separa Bird y Néctar mientras existan las dos líneas`.
