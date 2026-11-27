# Línea vieja en Néctar: plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** que el bot opere un segundo canal de Néctar (la línea vieja, el ex número de Bird) con su propia clave. Toda notificación sale por la línea nueva, la vieja lleva siempre la nota del número nuevo, y los KPI se agrupan por número.

**Architecture:**
- Un registro persistente `conversation_id → línea` (migración 049) se llena desde la ruta de avisos (`/api/webhooks/nectar` = main, `/api/webhooks/nectar/legacy` = legacy) y desde las aperturas (siempre main).
- Un `nectar.LineRouter` implementa `bird.NectarBridge` y delega en el puente (cliente y clave) de la línea de cada conversación.
- `sessions`, `chat_events` y `escalations` copian la línea en SQL, igual que hoy copian `contact_identifier`. El dashboard agrupa con esa columna.

**Tech Stack:** Go 1.26 (bot y dashboard), MySQL 8.4 (golang-migrate), React y TypeScript (frontend del dashboard).

**Spec:** `docs/superpowers/specs/2026-11-27-nectar-linea-vieja-design.md` (léela completa antes de cada tarea).

## Global Constraints

- **Ramas:** `feat/nectar-linea-vieja` en `C:\laragon\www\CHATBOT\Neurobotv2` y en `C:\laragon\www\CHATBOT\neuro-dashboard`. Nunca `main` ni `feat/migracion-nectar`.
- **Hooks:** nunca `--no-verify` ni `LEFTHOOK=0`.
- **Nunca stagear:** `auditoria/`, `bin/`, `cmd/e2e/`, `docs/CASO-*`, `docs/MAPA-*`, `internal/api/test_handler.go`, `scripts/restore-local-db.sh`, `seed-kpis`, `.claude/`, `docs/superpowers/plans/2026-10-29-ivr-recordatorio-fallido.md`, ni el `web/` del dashboard. Usa `git add <archivo>` explícito, nunca `git add -A`.
- **Código y comentarios:** identificadores en inglés, comentarios en español.
- **Alcance:** no cambies el flujo ni las validaciones del bot; esto es solo integración. Tampoco el contrato de Néctar.
- **Compatibilidad:** sin `NECTAR_LEGACY_*` el bot se comporta EXACTAMENTE como hoy. Todas las pruebas actuales siguen verdes sin tocarlas, salvo las que este plan cambia explícitamente.
- **Valores de línea:** `"main"` y `"legacy"`. NULL en una columna = `main`.
- **Clave de sesión:** sigue siendo `nectar:<conversation_id>`; no se cambia su forma.
- **Commits:** terminan con
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` y
  `Claude-Session: https://claude.ai/code/session_0161GETGGb472iRRuXqsjaE3`.
- **Integración local:** la base de pruebas es `neuro_bot_test` en `127.0.0.1:13308` (usuario `botuser`). Tras crear la migración 049, recárgala desde `neuro_bot`:
  1. `docker compose up -d --build bot` (aplica la 049 a `neuro_bot`).
  2. `docker exec neuro_bot_db sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "DROP DATABASE IF EXISTS neuro_bot_test; CREATE DATABASE neuro_bot_test; GRANT ALL ON neuro_bot_test.* TO \"$MYSQL_USER\"@\"%\";" && mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --no-data "$MYSQL_DATABASE" | mysql -uroot -p"$MYSQL_ROOT_PASSWORD" neuro_bot_test && mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --no-create-info "$MYSQL_DATABASE" schema_migrations | mysql -uroot -p"$MYSQL_ROOT_PASSWORD" neuro_bot_test' 2>/dev/null`
  3. `LOCAL_TEST_DSN='botuser:botpass@tcp(127.0.0.1:13308)/neuro_bot_test' go test -tags integration -count=1 ./internal/repository/local/...`

## Review Focus

1. **Aviso legacy con la base caída:** debe responder 500 (Néctar reintenta), nunca 200. Un 200 sin registro dejaría la conversación como `main` y el bot respondería con la clave equivocada. Lo prueba la Tarea 4.
2. **Reinicio con filas legacy pendientes en el WAL:** la reproducción usa la clave legacy, porque la línea sale del registro persistido y no de la memoria. Lo prueba la Tarea 4.
3. **Ventana de 24 h vencida en una conversación legacy sin teléfono de contacto:** no envía nada por la línea vieja y falla como un envío sin destino. Lo prueba la Tarea 6.
4. **Clave legacy rechazada (401/403/5xx):** solo cae la línea vieja. Los contactos de la línea nueva siguen, y no salta el URGENTE. Lo prueban las Tareas 3 y 9.
5. **Dashboard contra una base sin la 049:** sin error de SQL; todo cuenta como Bird o línea nueva, como hoy. Lo prueba la Tarea 12.

---

### Task 1: Migración 049 y registro de líneas

**Files:**
- Create: `migrations/049_nectar_line.up.sql`, `migrations/049_nectar_line.down.sql`
- Create: `internal/nectar/line.go` (tipo `Line` y caché del registro)
- Create: `internal/repository/local/conversation_line_repo.go`
- Test: `internal/nectar/line_test.go`, `internal/repository/local/conversation_line_repo_integration_test.go`

**Interfaces:**
- Produces:
  - En `internal/nectar`:
    - `type Line string`, con `const LineMain Line = "main"` y `LineLegacy Line = "legacy"`.
    - `type LineStore interface { LineOf(ctx context.Context, conversationID int64) (Line, bool, error); Register(ctx context.Context, conversationID int64, l Line) error }`.
    - `func NewLineRegistry(store LineStore) *LineRegistry`, con los métodos:
      - `Line(ctx, id) Line`: sin registro o con error = `LineMain`; un error de lectura deja `slog.Warn`.
      - `Register(ctx, id, l) error`
      - `Known(id) bool`: solo memoria.
  - En `local`:
    - `func NewConversationLineRepo(db *sql.DB) *ConversationLineRepo`, que implementa `nectar.LineStore`.

- [ ] **Step 1: Write the migration.** `049_nectar_line.up.sql`, con el estilo de comentarios de `045_nectar_contact.up.sql`:
  ```sql
  CREATE TABLE nectar_conversation_line (
    conversation_id BIGINT NOT NULL PRIMARY KEY,
    line VARCHAR(10) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  ALTER TABLE sessions ADD COLUMN nectar_line VARCHAR(10) NULL;
  ALTER TABLE chat_events ADD COLUMN nectar_line VARCHAR(10) NULL;
  ALTER TABLE escalations ADD COLUMN nectar_line VARCHAR(10) NULL;
  ALTER TABLE nectar_waiting_cycles ADD COLUMN line VARCHAR(10) NULL;
  ```
  El `.down.sql` hace los `DROP` en orden inverso.
- [ ] **Step 2: Write failing tests.**
  - `line_test.go`, con un `LineStore` falso:
    - `TestLineRegistry_UnknownIsMain`
    - `TestLineRegistry_RegisterThenLineFromCache`: tras `Register` hay cero lecturas al store.
    - `TestLineRegistry_StoreErrorIsMainNotPanic`
    - `TestLineRegistry_KnownOnlyAfterRegisterOrLookup`
  - Integración:
    - `TestConversationLineRepo_RegisterAndRead`
    - `TestConversationLineRepo_RegisterIsIdempotentAndKeepsFirst`: registrar `legacy` y luego `main` deja `legacy`, sin error, porque una conversación nunca cambia de canal.
- [ ] **Step 3: Run them.**
  - `go test ./internal/nectar/ -run TestLineRegistry`. Esperado: FAIL (no compila).
- [ ] **Step 4: Implement.**
  - **Repo:**
    - `Register` hace `INSERT IGNORE`.
    - Si la fila existe con otra línea, `slog.Error` y devuelve nil.
    - `LineOf` hace `SELECT line … WHERE conversation_id=?`.
  - **Registro:**
    - Usa `sync.Map` como caché, sin tope: es una fila por conversación, igual que la caché de contacto de hoy.
- [ ] **Step 5: Verify.**
  - Las unitarias pasan.
  - Recarga `neuro_bot_test` (Global Constraints) y pasa la integración.
- [ ] **Step 6: Commit.** `feat(nectar): registro de línea por conversación (migración 049)`

### Task 2: Configuración de la línea vieja

**Files:**
- Modify: `internal/config/config.go`, cerca de los campos de Néctar (líneas 103-126) y de su carga.
- Modify: `.env.example`
- Modify: `cmd/server/main.go`, en el armado de `/health` (línea ~844).
- Test: `internal/config/config_test.go`, `cmd/server/nectar_wiring_test.go`

**Interfaces:**
- Produces en `config.Config`:
  - `NectarLegacyServiceKey string`
  - `NectarLegacyChannelID int64`
  - `NectarLegacyRequestSecret string`: por defecto `NectarRequestSecret`.
  - `NectarLegacyNoticeSecret string`: por defecto `NectarNoticeSecret`.
  - `NectarLegacyArmed bool`: lo pone main, como `NectarBridgeArmed`.
  - El método `func (c *Config) NectarLegacyConfigured() (ok bool, err error)`.

- [ ] **Step 1: Failing tests.**
  - `TestNectarLegacy_DefaultsSecretsToMain`
  - `TestNectarLegacy_KeyWithoutChannelIsError`
  - `TestNectarLegacy_ChannelWithoutKeyIsError`
  - `TestNectarLegacy_EmptyIsOffNoError`
  - En `nectar_wiring_test.go`:
    - Con la línea vieja apagada, el cuerpo de `/health` es EXACTAMENTE el de hoy. Las pruebas de las líneas 129 y 158 no se tocan.
    - Con la línea vieja armada, el cuerpo incluye `"nectar_legacy":"armed"`.
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - La clave `nectar_legacy` solo aparece en `/health` cuando la línea está armada.
  - En `.env.example` van las cuatro variables, vacías, con su comentario en español. Sin valores reales.
- [ ] **Step 4: Verify.** `go test ./internal/config/ ./cmd/server/` en verde.
- [ ] **Step 5: Commit.** `feat(config): variables NECTAR_LEGACY_* de la línea vieja`

### Task 3: LineRouter y cableado de los dos clientes

**Files:**
- Create: `internal/nectar/router.go`
- Modify: `cmd/server/nectar_wiring.go` (`nectarBridgeFromConfig`, y el aviso urgente entre las líneas 62 y 75)
- Modify: `cmd/server/main.go`, líneas 165-195: `SetNectar` recibe el router y se pone `cfg.NectarLegacyArmed`.
- Modify: `internal/monitor/urgent.go`: el texto del aviso cuando la línea vieja está armada.
- Test: `internal/nectar/router_test.go`, `cmd/server/nectar_wiring_test.go`, `internal/monitor/urgent_test.go`

**Interfaces:**
- Consumes: `LineRegistry` y `Line` (Tarea 1); `Config.NectarLegacy*` (Tarea 2).
- Produces en `internal/nectar`:
  - `func NewLineRouter(reg *LineRegistry, bridges map[Line]*Bridge) *LineRouter`. Exige `bridges[LineMain]`.
  - `*LineRouter` implementa `bird.NectarBridge` y además `IsLegacy(ctx context.Context, conversationID int64) bool`.
  - `var ErrTemplateOnLegacy = errors.New("nectar: plantilla por la línea vieja: las notificaciones salen por la línea nueva")`.
  - En `bird` (`internal/bird/nectar_bridge.go`), la interfaz opcional `type NectarLines interface { IsLegacy(ctx context.Context, conversationID int64) bool }`.
  - En `cmd/server`, `nectarBridgeFromConfig` devuelve `(mainClient *nectar.Client, legacyClient *nectar.Client, bridge bird.NectarBridge, reg *nectar.LineRegistry, err error)`. `legacyClient` es nil si la línea está apagada.

- [ ] **Step 1: Failing tests** en `router_test.go`, con dos `Bridge` sobre stubs HTTP distintos (`httptest`) o con puentes falsos:
  - `TestLineRouter_SendUsesConversationLine`: una conversación registrada `legacy` sale por el stub legacy.
  - `TestLineRouter_UnregisteredUsesMain`
  - `TestLineRouter_OpenWithTemplateAlwaysMainAndRegistersMain`
  - `TestLineRouter_SendTemplateOnLegacyRefuses`: devuelve `errors.Is(err, ErrTemplateOnLegacy)` sin llamar a ningún stub.
  - `TestLineRouter_IsNotMineDelegates`
  - `TestLineRouter_OnlyMainBehavesAsBridge`: con un solo puente, cada método da el mismo resultado que el `Bridge` directo.
  - En `nectar_wiring_test.go`:
    - Línea vieja a medias: `legacyClient` nil y error que nombra `NECTAR_LEGACY_SERVICE_KEY` o `NECTAR_LEGACY_CHANNEL_ID`.
    - Puente principal apagado: la línea vieja queda apagada aunque esté configurada.
  - En `urgent_test.go`, `TestUrgent_LegacyArmedTextDoesNotSayBird`: con la línea vieja armada, el aviso NO contiene «volver a Bird» y SÍ contiene `Pide a un supervisor de Néctar que rescate las conversaciones del bot (motivo bot_down).`
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - El legacy `Bridge` usa `NewClient(NectarBaseURL, NectarLegacyServiceKey, NectarLegacyRequestSecret)` con las mismas `TemplateNames`, y `ChannelID` = 0, porque no abre.
  - **Aviso urgente:**
    - el cliente legacy recibe un `UrgentReporter` propio, que hace `slog.Error("nectar línea vieja: fallos", "trigger", t, "detail", detail)` como máximo una vez por `window` y por disparador (los ERROR ya van a Telegram);
    - el principal sigue con el `UrgentAlerter` de hoy;
    - `UrgentAlerter` recibe `SetLegacyLineArmed(bool)` para cambiar el texto.
- [ ] **Step 4: Verify.** `go test ./internal/nectar/ ./internal/monitor/ ./cmd/server/ ./internal/bird/` en verde.
- [ ] **Step 5: Commit.** `feat(nectar): router por línea y segundo cliente para la línea vieja`

### Task 4: Ruta de avisos legacy, registro antes del WAL y capacidades

**Files:**
- Modify: `internal/api/nectar_handler.go`:
  - `HandleNectarNotice`, líneas 343-457, pasa a `handleNectarNotice(w, r, line)`;
  - nuevo `HandleNectarLegacyNotice`;
  - `SetNectarLines(reg *nectar.LineRegistry)`.
- Modify: `internal/api/nectar_capabilities.go` (líneas 40-75)
- Modify: `cmd/server/main.go:588`, para registrar `POST /api/webhooks/nectar/legacy`.
- Test: `internal/api/nectar_handler_test.go`, `internal/api/nectar_capabilities_test.go`, `cmd/server/replay_test.go`

**Interfaces:**
- Consumes: `LineRegistry` (Tarea 1); `NectarLegacyArmed` y `NectarLegacyNoticeSecret` (Tarea 2).
- Produces: `func (h *WebhookHandler) HandleNectarLegacyNotice(w http.ResponseWriter, r *http.Request)`.

- [ ] **Step 1: Failing tests:**
  - `TestLegacyNotice_NotConfiguredIs404`
  - `TestLegacyNotice_ConfiguredBridgeOffIs200Discard`: firma válida, nada en el WAL, como la ruta principal.
  - `TestLegacyNotice_RegistersLegacyBeforeWAL`: el registro queda en `legacy` antes de que el insert al WAL ocurra. Usa un store que guarde el orden.
  - `TestLegacyNotice_RegistryFailureIs500AndNoWAL` (Review Focus 1).
  - `TestLegacyNotice_VerifiesWithLegacySecret`: una firma con el secreto principal falla si los secretos difieren; con el secreto legacy, pasa.
  - `TestMainNotice_RegistersMainWhenUnknown`
  - `TestCapabilities_AcceptsEitherSecret`
  - `TestReplay_LegacyRowUsesLegacyClient` (Review Focus 2): una fila del WAL de una conversación registrada `legacy` se reproduce y el envío sale por el stub legacy.
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - El registro va después de verificar la firma y de leer el `conversation_id`, y antes de `inboxRepo.InsertIfNotExists`. Se omite si `reg.Known(id)`.
  - `handleOpenedByAgent` también registra la línea de su ruta.
  - El WAL y `DispatchNectarNotice` no cambian.
- [ ] **Step 4: Verify.** `go test ./internal/api/ ./cmd/server/` en verde.
- [ ] **Step 5: Commit.** `feat(nectar): avisos de la línea vieja por su propia ruta`

### Task 5: Línea en sesiones, eventos y escalaciones, y script de corrección

**Files:**
- Modify: `internal/session/types.go`: campo `NectarLine string`.
- Modify: `internal/repository/local/session_repo.go`: INSERT (líneas 139-146) y las lecturas que escanean `ContactIdentifier` (línea 81).
- Modify: `internal/repository/local/event_repo.go` (`chatEventInsertSQL`, líneas 79-82, e `Insert`)
- Modify: `internal/repository/local/escalation_repo.go` (`Create`, líneas 25-29)
- Modify: `cmd/server/main.go`, donde se crea `EventRepo`.
- Create: `internal/repository/local/proactive_event_types.go`
- Create: `scripts/backfill-nectar-line-notifications.sql`
- Test: `internal/repository/local/*_integration_test.go` (sesión, evento y escalación); unitaria de la lista de tipos.

**Interfaces:**
- Produces:
  - `(*EventRepo).SetProactiveLineTagging(on bool)`
  - `func isProactiveEventType(t string) bool`
  - `Session.NectarLine`, leído de `sessions.nectar_line`.

- [ ] **Step 1: Failing integration tests:**
  - `TestSessionCreate_CopiesLineFromRegistry`: con `nectar_conversation_line(42, 'legacy')`, crear la sesión `nectar:42` deja `sessions.nectar_line='legacy'` y la lectura trae `NectarLine=="legacy"`.
  - `TestSessionCreate_BirdPhoneLineNull`
  - `TestChatEvent_CopiesSessionLine`
  - `TestEscalation_CopiesSessionLine`
  - `TestChatEvent_ProactiveTypeTaggedMainWhenOn`: `reminder_prep_sent` con teléfono `+57…` y tagging encendido queda `'main'`; con tagging apagado queda NULL.
  - `TestChatEvent_NonProactiveRawPhoneStaysNull`: un evento de conversación de Bird.
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - **INSERT de `sessions`:**
    `nectar_line = (SELECT l.line FROM nectar_conversation_line l WHERE ? LIKE 'nectar:%' AND l.conversation_id = CAST(SUBSTRING(?, 8) AS UNSIGNED))`, pasando el id de la sesión dos veces.
  - **`chat_events` y `escalations`:**
    `nectar_line = COALESCE((SELECT s.nectar_line FROM sessions s WHERE s.id = ?), ?)`.
    - En escalaciones el segundo parámetro es NULL.
    - En eventos es `'main'` si `tagging && isProactiveEventType(type)`, y si no, NULL.
  - **Lista cerrada en `proactive_event_types.go`:**
    - `reminder_prep_sent`, `reminder_suppressed_no_whatsapp`;
    - `notification_send_failed`, `notification_in_doubt`, `notification_skipped_agent_owned`;
    - `notification_confirmed_ivr`, `notification_cancelled_ivr`, `notification_timeout`.

    Agrega cualquier otro tipo que hoy se registre con el teléfono de SIESA en `internal/scheduler/tasks.go`, `internal/notifications/manager.go` o `internal/api/internal_handler.go` y que sea del envío proactivo o del IVR. Revisa `notification_confirmed`, `notification_cancel_acknowledged` y `waiting_list_schedule_declined`: entran solo si su `phone` es el teléfono de SIESA y no la clave de sesión. Deja anotado en el reporte qué decidiste para cada uno.
  - **main:** llama `SetProactiveLineTagging(nectarBridge != nil)`.
  - **Script:** idempotente, con `SET @desde = '<YYYY-MM-DD HH:MM:SS>';` arriba y un comentario que diga quién la pone. Hace:
    `UPDATE chat_events SET nectar_line='main' WHERE nectar_line IS NULL AND event_type IN (<la misma lista>) AND created_at >= @desde;`
    y termina con un `SELECT ROW_COUNT()`.
- [ ] **Step 4: Verify.** Recarga `neuro_bot_test` y pasa la integración; `go test ./internal/repository/local/ ./internal/session/` en verde.
- [ ] **Step 5: Commit.** `feat(kpi): línea de cada conversación en sesiones, eventos y escalaciones`

### Task 6: Las plantillas sobre una conversación legacy salen por la línea nueva

**Files:**
- Modify: `internal/bird/client.go`, `SendTemplate` (líneas 1146-1153)
- Modify: `internal/bird/proactive.go`, `SendProactiveTemplate` y su rama `nectar:` (líneas 117-158)
- Test: `internal/bird/proactive_test.go`, `internal/worker/queue_expired_test.go`

**Interfaces:**
- Consumes: `bird.NectarLines` y `ErrTemplateOnLegacy` (Tarea 3); `NectarBridge.Contact`.
- Produces: `func (c *Client) reopenOnMainIfLegacy(ctx context.Context, convID int64, tmpl TemplateConfig) (handled bool, messageID string, err error)`, usado por las dos rutas.

- [ ] **Step 1: Failing tests:**
  - `TestSendTemplate_LegacyConversationReopensOnMain`: con un router falso (`IsLegacy` = true) y `Contact` = `573001112233`, se llama `OpenWithTemplate("+573001112233", …)` y NO `SendTemplate(convID)`.
  - `TestSendTemplate_LegacyWithoutContactFailsNoSend` (Review Focus 3): devuelve error y no hay ninguna llamada de envío.
  - `TestSendTemplate_MainConversationUnchanged`
  - `TestQueueExpired_LegacySessionTemplateOpensMain`
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - `IsLegacy` se consulta por aserción de tipo sobre `c.nectar`. El fake de `internal/testutil/fakenectar` no la implementa, así que el comportamiento de hoy se mantiene.
  - El teléfono sale de `Contact(convID).Identifier`, normalizado a E.164 con `+`, igual que hace hoy la adopción de pendings.
- [ ] **Step 4: Verify.** `go test ./internal/bird/ ./internal/worker/` en verde.
- [ ] **Step 5: Commit.** `feat(nectar): las plantillas de la línea vieja se reabren por la nueva`

### Task 7: Nota del número nuevo en la línea vieja

**Files:**
- Modify: `internal/statemachine/handlers/greeting.go`, `announcementFor` (líneas 313-322)
- Test: `internal/statemachine/handlers/greeting_test.go`

- [ ] **Step 1: Failing tests:**
  - `TestAnnouncement_BirdSession`: igual que hoy.
  - `TestAnnouncement_NectarLegacySession`: `sess.PhoneNumber="nectar:9"` y `sess.NectarLine="legacy"` → anuncio.
  - `TestAnnouncement_NectarMainSession`: sin anuncio.
  - `TestAnnouncement_NectarNullLineSession`: sin anuncio.
  - `TestAnnouncement_PostConfirmedNever`: igual que hoy.
- [ ] **Step 2: Run them.** Esperado: FAIL en el caso legacy.
- [ ] **Step 3: Implement.** La condición de Néctar pasa de «siempre sin anuncio» a «sin anuncio salvo `sess.NectarLine == string(nectar.LineLegacy)`». Si importar `nectar` desde handlers crea un ciclo, usa la constante en `session`.
- [ ] **Step 4: Verify.** `go test ./internal/statemachine/...` en verde.
- [ ] **Step 5: Commit.** `feat(transicion): la línea vieja en Néctar también anuncia el número nuevo`

### Task 8: Adjuntos de la línea vieja (OCR)

**Files:**
- Modify: `internal/services/ocr_service.go` (líneas 545-563 y el bucle de reintentos de descarga, líneas 579 y 620-624)
- Modify: `cmd/server/main.go:207`
- Test: `internal/services/ocr_service_test.go`

**Interfaces:**
- Produces: `func (s *OCRService) SetNectarFallbackCredentials(serviceKey, requestSecret string)`.

- [ ] **Step 1: Failing tests:**
  - `TestNectarMedia_404WithMainRetriesWithLegacyInSameAttempt`: un stub que responde 404 a la clave principal y 200 a la legacy; la descarga funciona con dos GET, sin esperar los `defaultMediaRetryDelays`.
  - `TestNectarMedia_NoFallbackBehavesAsToday`
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.** En cada intento: primero la principal; ante 404 y con fallback configurado, la legacy. Solo si las dos fallan, se aplica la espera de hoy.
- [ ] **Step 4: Verify.** `go test ./internal/services/ -run NectarMedia` en verde.
- [ ] **Step 5: Commit.** `feat(ocr): adjuntos de la línea vieja con su clave`

### Task 9: Corte de la caché de contacto por línea

**Files:**
- Modify: `internal/api/nectar_handler.go` (líneas 89-103, 162-279; `unreachableUntil` en las líneas 228-238)
- Test: `internal/api/nectar_handler_test.go`

- [ ] **Step 1: Failing test:** `TestContactCircuit_LegacyFailureDoesNotBlockMain` (Review Focus 4): fallos del contacto de una conversación `legacy` abren el corte solo para `legacy`; una conversación `main` sigue consultando.
- [ ] **Step 2: Run it.** Esperado: FAIL.
- [ ] **Step 3: Implement.** `unreachableUntil` pasa a ser por línea: `map[nectar.Line]time.Time` con el mismo mutex. La línea sale de `reg.Line(ctx, convID)`.
- [ ] **Step 4: Verify.** `go test ./internal/api/` en verde.
- [ ] **Step 5: Commit.** `fix(nectar): una clave vieja rechazada no corta los contactos de la nueva`

### Task 10: Informes de asesores con las dos claves

**Files:**
- Modify: `internal/nectarreports/syncer.go`: constantes de estado (líneas 43-46), `NewSyncer` (línea 85) y `RunOnce`.
- Modify: `internal/repository/local/nectar_reports_repo.go`: `UpsertCycles` (línea 75) y `OldestOpenCycleOpenedAt` (línea 114).
- Modify: `cmd/server/nectar_wiring.go` (`newNectarReportsSyncer` y `startNectarReports`, líneas 95-122)
- Test: `internal/nectarreports/syncer_test.go`, `internal/repository/local/nectar_reports_repo_integration_test.go`

**Interfaces:**
- Produces:
  - `NewSyncer(r reportsReader, cycles cyclesStore, esc escalationsStore, every time.Duration, floor time.Time, line nectar.Line) *Syncer`
  - `cyclesStore.UpsertCycles(ctx context.Context, line nectar.Line, cycles []nectar.WaitingCycle) error`
  - `cyclesStore.OldestOpenCycleOpenedAt(ctx context.Context, line nectar.Line) (*time.Time, error)`, donde `main` incluye las filas con `line` NULL.

- [ ] **Step 1: Failing tests:**
  - `TestSyncer_LegacyUsesPrefixedStateAndSkipsAgentsAndAttentions`: los estados se llaman `legacy:last_seen_opened_at`, etc., y no hay llamadas a `GetAgents` ni a `applyAttentions`.
  - `TestSyncer_MainStateNamesUnchanged`
  - Integración: `TestUpsertCycles_WritesLine` y `TestOldestOpenCycle_PerLine`.
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.** main arranca un segundo syncer con el cliente legacy cuando está armado. Un `ErrAccountKeyRequired` detiene solo su instancia.
- [ ] **Step 4: Verify.** Unitarias e integración en verde.
- [ ] **Step 5: Commit.** `feat(informes): tramos de la línea vieja con su propia clave`

### Task 11: Documentación

**Files:**
- Modify: `CLAUDE.md`, sección «NÉCTAR (mensajería)»: un párrafo «Línea vieja» con las variables, la ruta, el registro, las plantillas siempre por main, la nota, la columna `nectar_line`, la regla de agrupación y el script.
- Modify: `docs/GUIA-VOLVER-A-BIRD.md`: arriba, que tras el traslado del número viejo NO aplica y que la contingencia es que los supervisores de Néctar rescaten las conversaciones (motivo `bot_down`).
- Modify: `docs/MONITOREO-EXTERNO.md`: `nectar_legacy` en `/health`.

- [ ] **Step 1:** Escribe los tres cambios. El CLAUDE.md va en el estilo denso de esa sección.
- [ ] **Step 2: Verify.** `go test ./...` completo y `go test -count=2 ./...` en verde.
- [ ] **Step 3: Commit.** `docs(nectar): línea vieja en CLAUDE.md, guía de vuelta y monitoreo`

### Task 12: Dashboard por número (neuro-dashboard)

**Files:**
- Modify: `internal/kpi/line.go` (todo el archivo)
- Modify: `internal/kpi/repository.go`, en estos llamadores de `lineFilter`:

  | Línea | Función | Pasa a |
  |---|---|---|
  | 35 | `GetDailyKPIs` | línea |
  | 69 | eventos (`ce.`) | línea |
  | 605 | `GetEscalationSLAByAgent` | plataforma |
  | 758 | `GetSessionBreakdown` | línea |
  | 876 | `GetAgentWindow` | plataforma |
  | 902 | `NectarAgents` | plataforma |
  | 962 | `GetAgentEscalationsWindow` | plataforma |
  | 999-1040 | `GetEscalationSLA` | línea |
  | 1091 | `GetSessionStatusBreakdown` | línea |

- Modify: `internal/reports/reports.go` y `agent.go`: siguen por plataforma; solo cambia el nombre del tipo si hace falta.
- Modify: `frontend/src/lib/byLine.ts`, `frontend/src/components/LineSplit.tsx` y las páginas que usan `bird` y `nectar` para totales (`Escalation.tsx`, `Overview.tsx`, `Sessions.tsx`, `Recovery.tsx`, `Reports.tsx`, `lib/reportPdf.tsx`).
- Test: `internal/kpi/line_test.go`, más las pruebas de repositorio existentes.

**Interfaces:**
- Produces en `kpi`:
  - `type Line string` con `LineAll = ""`, `LineOld = "old"` y `LineNew = "new"`.
  - `func lineFilter(line Line, alias string, lineSchema bool) string`.
  - `type Platform string` con `PlatformBird = "bird"` y `PlatformNectar = "nectar"`.
  - `func platformFilter(p Platform, col string) string`: es el `lineFilter` de hoy, renombrado.
  - `ByLine[T]` con JSON `{"all","old","new","split"}`.
  - `ByPlatform[T]` con JSON `{"all","bird","nectar","split"}`, para las cifras de asesores. Hoy esas usan `ByLine`; cambia solo el nombre del tipo y su JSON se mantiene.
  - `func (r *Repository) hasLineSchema(ctx context.Context) bool`: mira `sessions.nectar_line` en `information_schema` y se cachea 5 minutos, como `hasNectarSchema`.

- [ ] **Step 1: Failing tests** en `line_test.go`:
  - `TestLineFilter_OldIsBirdOrLegacy`: con schema, `lineFilter(LineOld,"ce",true)` = `" AND (ce.phone_number NOT LIKE 'nectar:%' OR ce.nectar_line = 'legacy')"`.
  - `TestLineFilter_NewIsNectarMainOrNull`: con schema, `" AND (ce.phone_number LIKE 'nectar:%' AND COALESCE(ce.nectar_line,'main') = 'main')"`.
  - `TestLineFilter_NoSchemaFallsBackToPrefix` (Review Focus 5): sin schema, old = `NOT LIKE 'nectar:%'` y new = `LIKE 'nectar:%'`, sin mencionar `nectar_line`.
  - `TestLineFilter_EmptyAliasUnqualified`
  - `TestPlatformFilter_SameAsOldLineFilter`
- [ ] **Step 2: Run them.** `go test ./internal/kpi/ -run 'LineFilter|PlatformFilter'`. Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - En el backend, cada llamador de la tabla pasa a línea o a plataforma.
  - **Frontend:**
    - `LineKey = "old" | "new"`, con `LINE_LABEL = { old: "Línea anterior", new: "Línea nueva" }`;
    - las vistas de asesores siguen con las etiquetas `Bird` y `Néctar`, por plataforma;
    - `isSplit` pasa a mirar `old` y `new`.
- [ ] **Step 4: Verify.**
  - `go build ./... && go vet ./... && go test ./...` en verde.
  - `cd frontend && npm run build` sin errores. NO stagees `web/`.
  - Con la base local de la Tarea 1 (049 aplicada), levanta el dashboard (`BOT_PORT=8090 docker compose up -d --build`) y confirma que `/api/escalation/sla` y `/api/sessions` traen `by_line` con `old`/`new` sin error de SQL.
- [ ] **Step 5: Commit** en neuro-dashboard: `feat(kpi): cifras por número (línea anterior y nueva); asesores por plataforma`

---

## Self-review (hecho al escribir el plan)

**Cobertura de la spec:**

| Sección de la spec | Tarea |
|---|---|
| Registro y 049 | 1 |
| Configuración y `/health` | 2 |
| Router, dos clientes, urgente y su texto | 3 |
| Ruta legacy (404 / 200 descarte), registro antes del WAL, capacidades | 4 |
| `nectar_line`, eventos proactivos y script | 5 |
| Plantillas legacy por main | 6 |
| Nota | 7 |
| OCR | 8 |
| Corte de contacto | 9 |
| Informes | 10 |
| Docs y guía | 11 |
| Dashboard | 12 |

Operación (Néctar, `.env`, Meta) queda fuera del código, como dice la spec.

**Nombres que cruzan tareas:**

| Nombre | Definido en | Usado en |
|---|---|---|
| `Line` / `LineMain` / `LineLegacy`, `LineRegistry` (Line, Register, Known) | 1 | 3, 4, 9, 10 |
| `IsLegacy`, `ErrTemplateOnLegacy` | 3 | 6 |
| `Session.NectarLine` | 5 | 7 |
