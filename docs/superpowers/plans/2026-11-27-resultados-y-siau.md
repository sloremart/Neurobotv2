# Resultados de estudios, imágenes y SIAU: plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** el bot ofrece en las dos líneas «Resultados de estudios» (con verificación por fecha de nacimiento y el PDF por Néctar), «Resultados de imágenes» (el enlace a Lumier) y «Atención al usuario (SIAU)» (escalación al equipo SIAU). Así se puede apagar el chatbot de n8n del número nuevo.

**Architecture:**
- Un cliente HTTP nuevo (`internal/results`) habla con la API de resultados (`RESULTS_API_URL`).
- El cliente de Néctar gana la subida y el envío de adjuntos (contrato del lote 11), y el router por línea los reparte.
- Pasos nuevos de la máquina de estados: tipo y número de documento, fecha de nacimiento verificada contra SIESA con un tope persistente (migración 050), lista y envío.
- El menú suma imágenes y SIAU.

**Tech Stack:** Go 1.26, MySQL 8.4 (golang-migrate), `internal/statemachine`.

**Spec:** `docs/superpowers/specs/2026-11-27-resultados-y-siau-design.md` (léela completa, incluida la sección «Decisiones mías tras la revisión»).

## Global Constraints

- **Rama:** `feat/resultados-bot` en `C:\laragon\www\CHATBOT\Neurobotv2`. Nunca `main`, `feat/migracion-nectar` ni `feat/nectar-linea-vieja`.
- **Hooks:** nunca `--no-verify` ni `LEFTHOOK=0`. Usa `git add <archivo>` explícito.
- **Nunca stagear:** `auditoria/`, `bin/`, `cmd/e2e/`, `docs/CASO-*`, `docs/MAPA-*`, `internal/api/test_handler.go`, `scripts/restore-local-db.sh`, `seed-kpis`, `.claude/`, `docs/superpowers/plans/2026-10-29-ivr-recordatorio-fallido.md`.
- **Código y comentarios:** identificadores en inglés, comentarios en español.
- **Alcance:**
  - no cambies los flujos de medicamentos ni de agendamiento;
  - no cambies el contrato de Néctar: el cliente sigue el contrato del lote 11 tal como está en la spec;
  - la API real `192.168.1.211:8095` no se llama desde pruebas; usa `httptest`.
- **Compatibilidad:** sin `RESULTS_API_URL` y con `RESULTS_PDF_ENABLED=false` (los valores por defecto), «Resultados» se comporta EXACTAMENTE como hoy (`showResultsHandler`). Sin `IMAGING_RESULTS_URL`, la opción de imágenes no aparece.
- **Privacidad:** nunca escribas en logs ni en `chat_events` el número de documento, la fecha de nacimiento, el nombre, `archivo` ni el contenido del PDF. El PDF vive solo en memoria.
- **Commits:** terminan con
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` y
  `Claude-Session: https://claude.ai/code/session_0161GETGGb472iRRuXqsjaE3`.
- **Integración local:** base `neuro_bot_test` en `127.0.0.1:13308`, usuario `botuser`. Tras la migración 050, recárgala desde `neuro_bot`:
  1. `docker compose up -d --build bot` (aplica la 050).
  2. `docker exec neuro_bot_db sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "DROP DATABASE IF EXISTS neuro_bot_test; CREATE DATABASE neuro_bot_test; GRANT ALL ON neuro_bot_test.* TO \"$MYSQL_USER\"@\"%\";" && mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --no-data "$MYSQL_DATABASE" | mysql -uroot -p"$MYSQL_ROOT_PASSWORD" neuro_bot_test && mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --no-create-info "$MYSQL_DATABASE" schema_migrations | mysql -uroot -p"$MYSQL_ROOT_PASSWORD" neuro_bot_test' 2>/dev/null`
  3. `LOCAL_TEST_DSN='botuser:botpass@tcp(127.0.0.1:13308)/neuro_bot_test' go test -tags integration -count=1 ./internal/repository/local/...`

## Review Focus

1. **Un tercero prueba fechas de nacimiento para una cédula conocida, entrando y saliendo con «menú»:** al tercer fallo queda bloqueado 24 h por documento y por teléfono, aunque cambie de sesión. Lo prueban las Tareas 4 y 6.
2. **Mismo número con otro tipo de documento:** solo se envían estudios cuyo nombre coincide con el paciente verificado. Si ninguno coincide, no se envía nada. Lo prueban las Tareas 2 y 7.
3. **Un nombre de archivo malicioso en la respuesta de la API (`../`, `/`):** no se arma la URL y se envía el enlace al portal. Lo prueba la Tarea 2.
4. **La API de resultados o Néctar caídos a mitad del envío:** el paciente recibe el enlace al portal con una disculpa, sin quedar sin respuesta, y el turno no pasa su presupuesto. Lo prueba la Tarea 7.
5. **Una conversación de Bird, o la función apagada:** el comportamiento de hoy, con enlace y video. Lo prueba la Tarea 7.

---

### Task 1: Configuración

**Files:**
- Modify: `internal/config/config.go`: campos nuevos y su carga; `parseNectarTeamByGroup` (líneas 742-762).
- Modify: `.env.example`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces en `config.Config`:
  - `ResultsAPIURL string`: sin barra final.
  - `ResultsAPIKey string`
  - `ResultsPDFEnabled bool`: con `RESULTS_PDF_ENABLED=true`; por defecto false.
  - `ImagingResultsURL string`
- Produces también:
  - `const NectarGroupSIAU = "siau"`;
  - el método `func (c *Config) SuggestedTeamForSIAU() *int64`: nil si `NECTAR_TEAM_BY_GROUP` no trae `siau`.

- [ ] **Step 1: Failing tests:**
  - `TestResultsConfig_Defaults`: todo vacío y `ResultsPDFEnabled == false`.
  - `TestResultsConfig_Loaded`
  - `TestNectarTeamByGroup_AcceptsSIAU`: `"imagenes:7,siau:12"` da `SuggestedTeamForSIAU()` = 12.
  - `TestNectarTeamByGroup_UnknownStillFails`: el mensaje de error lista `siau` entre los válidos.
- [ ] **Step 2: Run them.** `go test ./internal/config/`. Esperado: FAIL.
- [ ] **Step 3: Implement.** `siau` es una clave válida más del mapa; `SuggestedTeamForCups` no la usa. En `.env.example` van las cuatro variables vacías, con comentario en español, y el ejemplo de `siau` en el comentario de `NECTAR_TEAM_BY_GROUP`.
- [ ] **Step 4: Verify.** `go test ./internal/config/` en verde.
- [ ] **Step 5: Commit.** `feat(config): variables de resultados, imágenes y grupo SIAU`

### Task 2: Cliente de la API de resultados

**Files:**
- Create: `internal/results/client.go`, `internal/results/study.go`
- Test: `internal/results/client_test.go`, `internal/results/study_test.go`

**Interfaces:**
- Produces en `results`:
  - `type Study struct { PatientName, StudyType, Admission, File string; Date time.Time; RawDate string }`. `Date` es cero si `fecha` no se pudo leer.
  - `func NewClient(baseURL, apiKey string) *Client`, con los métodos:
    - `Search(ctx context.Context, documentNumber string) ([]Study, error)`
    - `DownloadPDF(ctx context.Context, file string) ([]byte, error)`
  - Errores y topes:
    - `var ErrInvalidFile = errors.New("results: nombre de archivo inválido")`
    - `var ErrTooLarge = errors.New("results: PDF mayor a 16 MiB")`
    - `const MaxPDFBytes = 16 << 20`
  - Funciones:
    - `func SortAndDedupe(studies []Study) []Study`: por `Date` descendente, con las fechas cero al final; quita los `File` repetidos.
    - `func NameMatches(firstName, firstSurname, apiName string) bool`: sin tildes ni mayúsculas, compara palabras completas. Exige que estén las dos palabras de SIESA, y si alguna viene vacía devuelve false.

- [ ] **Step 1: Failing tests,** con `httptest`:
  - `TestSearch_List`: un arreglo JSON con `nombre`, `fecha` = "20/11/2026 08:30", `admision`, `tipo_estudio` y `archivo`, mapeado a `Study`.
  - `TestSearch_SingleObject`: un objeto suelto da 1 estudio.
  - `TestSearch_EmptyAndErrors`: `[]`, 404, 500 y `400 {"error":"Cédula inválida."}` dan lista vacía sin error en los 400/404, y error en el 500.
  - `TestSearch_SendsAPIKeyHeaderWhenSet`: cabecera `X-API-Key`.
  - `TestSearch_Timeout10s`
  - `TestDownloadPDF_RejectsInvalidFile` (Review Focus 3): `"../x.pdf"`, `"a/b.pdf"` y `"a\\b.pdf"` devuelven `ErrInvalidFile` sin llamar al servidor.
  - `TestDownloadPDF_TooLarge`: más de 16 MiB devuelve `ErrTooLarge`.
  - `TestDownloadPDF_Timeout20s`
  - `TestSortAndDedupe`
  - `TestNameMatches`:
    - `("Ana","Pérez","ANA MARIA PEREZ GOMEZ")` → true;
    - `("Ana","Perez","ANAMARIA PEREZ")` → false;
    - `("","Perez","PEREZ")` → false.
- [ ] **Step 2: Run them.** `go test ./internal/results/`. Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - Plazos: 10 s en `Search` y 20 s en `DownloadPDF`, cada uno con su `context.WithTimeout`.
  - `archivo` se escapa con `url.PathEscape` después de validarlo.
  - Lectura con `io.LimitReader(MaxPDFBytes+1)`.
  - Ningún log lleva número, nombre ni archivo.
- [ ] **Step 4: Verify.** `go test ./internal/results/` en verde.
- [ ] **Step 5: Commit.** `feat(resultados): cliente de la API de resultados`

### Task 3: Adjuntos del bot en Néctar (cliente, puente y router)

**Files:**
- Create: `internal/nectar/attachments.go`
- Modify: `internal/nectar/bridge.go`, `internal/nectar/router.go`, `internal/bird/nectar_bridge.go` (interfaz `NectarBridge`), `internal/testutil/fakenectar/bridge.go`
- Modify: `internal/bird/client.go` (nuevo método público)
- Test: `internal/nectar/attachments_test.go`, `internal/nectar/router_test.go`, `internal/bird/*_test.go`

**Interfaces:**
- Produces en `nectar`:
  - `type Attachment struct { ID, MIME, SHA256, Filename string; Size int64; ExpiresAt time.Time }`
  - `func (c *Client) Upload(ctx context.Context, idempotencyKey, filename string, data []byte) (Attachment, error)`
  - `func (c *Client) SendAttachment(ctx context.Context, conversationID int64, attachmentID, caption string) (int64, error)`
  - Errores: `ErrUnsupportedMedia` (415), `ErrTooLarge` (413), `ErrAttachmentUnavailable` (422 `attachment_unavailable`), `ErrWindowClosed` (409 `window_closed`; si ya existe un error equivalente, reúsalo) y `ErrIdempotencyReused` (409 `idempotency_key_reused`).
- Produces en `bird`:
  - `NectarBridge.SendDocument(ctx context.Context, conversationID int64, filename string, data []byte, caption string) (string, error)`
  - `func (c *Client) SendDocumentCtx(ctx context.Context, phone, filename string, data []byte, caption string) (string, error)`: con una clave `nectar:` delega en el puente; con un teléfono de Bird devuelve `ErrDocumentNotSupported`.
- Consumes: `LineRouter` y la resolución por línea (rama base).

- [ ] **Step 1: Failing tests,** con un stub `httptest` que verifica la firma con el secreto de prueba:
  - `TestUpload_MultipartSignedOverRawBody`: campos `idempotency_key`, `filename` y `file`; la firma HMAC es la de `exchange`, calculada sobre `timestamp + URL + SHA-256(cuerpo multipart ya serializado)`.
  - `TestUpload_ParsesAttachment`
  - `TestUpload_TypedErrors`: 415, 413 y 409 `idempotency_key_reused`.
  - `TestSendAttachment_BodyAndErrors`: el cuerpo es `{"idempotency_key":…,"type":"attachment","attachment":{"id":…,"caption":…}}`; los 422 y 409 `window_closed` vienen tipados, y el 404 lo reconoce `IsNotMine`.
  - `TestLineRouter_SendDocumentUsesConversationLine`: una conversación legacy sube y envía con el stub legacy.
  - `TestBirdClient_SendDocumentCtx_BirdPhoneNotSupported`
- [ ] **Step 2: Run them.** `go test ./internal/nectar/ ./internal/bird/`. Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - `SendAttachment` reutiliza la misma ruta de envío e idempotencia que `send` (claves en duda incluidas).
  - `Bridge.SendDocument` hace `Upload` con una clave UUID nueva y luego `SendAttachment`.
  - El fake de `fakenectar` guarda lo enviado para las pruebas de la Tarea 7.
- [ ] **Step 4: Verify.** `go test ./internal/nectar/ ./internal/bird/ ./internal/testutil/...` en verde, y `go build ./...`.
- [ ] **Step 5: Commit.** `feat(nectar): el bot sube y envía adjuntos (contrato del lote 11)`

### Task 4: Migración 050 y tope de intentos

**Files:**
- Create: `migrations/050_results_verification_attempts.up.sql`, `.down.sql`
- Create: `internal/repository/local/results_attempts_repo.go`
- Test: `internal/repository/local/results_attempts_repo_integration_test.go`

**Interfaces:**
- Produces en `local`:
  - `func NewResultsAttemptsRepo(db *sql.DB) *ResultsAttemptsRepo`, con los métodos:
    - `IsLocked(ctx context.Context, docHash, phone string, now time.Time) (bool, error)`
    - `RecordFailure(ctx context.Context, docHash, phone string, now time.Time) (locked bool, err error)`
    - `Reset(ctx context.Context, docHash string) error`
  - `func HashDocument(docType, number string) string`: SHA-256 en hex de `docType + ":" + number`.

- [ ] **Step 1: Write the migration:**
  ```sql
  CREATE TABLE results_verification_attempts (
    subject VARCHAR(80) NOT NULL PRIMARY KEY,
    failures INT NOT NULL DEFAULT 0,
    window_start DATETIME NOT NULL,
    locked_until DATETIME NULL
  );
  ```
  `subject` vale `"doc:<hash>"` o `"phone:<E.164>"`.
- [ ] **Step 2: Failing integration tests:**
  - `TestAttempts_ThreeFailuresLock24h` (Review Focus 1): tres fallos en 24 h de un mismo documento con teléfonos distintos bloquean ese documento; tres fallos de un mismo teléfono con documentos distintos bloquean ese teléfono.
  - `TestAttempts_WindowResetsAfter24h`
  - `TestAttempts_LockExpires`
  - `TestAttempts_ResetClearsDoc`
- [ ] **Step 3: Run them.** Esperado: FAIL.
- [ ] **Step 4: Implement.**
  - `RecordFailure` suma, con upsert, en las dos filas (doc y phone). Si `window_start` tiene más de 24 h, reinicia el conteo.
  - Al llegar a 3 pone `locked_until = now + 24h` y devuelve `locked = true`.
  - `IsLocked` mira las dos filas.
  - El documento nunca se guarda en claro.
- [ ] **Step 5: Verify.** Recarga `neuro_bot_test` y corre la integración.
- [ ] **Step 6: Commit.** `feat(resultados): tope persistente de intentos de verificación (migración 050)`

### Task 5: Menú, imágenes y SIAU

**Files:**
- Modify: `internal/statemachine/handlers/greeting.go`:
  - `buildMainMenuList`;
  - el `RegisterStaticReturnMenu` de `MAIN_MENU` y sus `Options` (línea 44);
  - el handler del menú principal (línea ~384);
  - el menú fuera de horario (`OUT_OF_HOURS_MENU`).
- Modify: `internal/statemachine/handlers/results_locations.go`: nuevo `showImagingHandler`.
- Modify: `internal/statemachine/states.go`: `StateShowImaging`.
- Modify: `internal/statemachine/escalation_reasons.go`: `EscalationReasonSIAU = "siau"`, que sí cuenta para el tope.
- Modify: `internal/statemachine/handlers/escalation_nectar.go` (línea ~91): equipo sugerido para SIAU.
- Test: `greeting_test.go`, `results_locations_test.go`, `escalation*_test.go`

**Interfaces:**
- Consumes: `cfg.ImagingResultsURL` y `cfg.SuggestedTeamForSIAU()` (Tarea 1).
- Produces:
  - IDs de menú `imagenes` y `siau`;
  - `sm.StateShowImaging`;
  - `sm.EscalationReasonSIAU`.

- [ ] **Step 1: Failing tests:**
  - `TestMainMenu_HasImagingAndSIAU`: 8 filas, en el orden medicamentos, agendar, consultar, resultados, imagenes, siau, ubicacion, ayuda. «resultados» se titula `📋 Resultados de estudios`; las otras dos, `🩻 Resultados de imágenes` y `🧑‍💼 Atención al usuario (SIAU)`.
  - `TestMainMenu_NoImagingWithoutURL`: 7 filas.
  - `TestShowImaging_SendsLinkAndAutoCloses`: el texto exacto `"🩻 *Resultados de imágenes*\n\nConsulta y descarga tus resultados de imágenes diagnósticas en el portal: " + url`.
  - `TestMainMenu_SIAUEscalatesWithReason`: el paso siguiente es `ESCALATE_TO_AGENT`, con el motivo `siau` y el texto exacto `"🧑‍💼 *Atención al usuario (SIAU)*\nTe comunico con nuestra área de atención al usuario. Escribe tu consulta, sugerencia o queja y un asesor te responderá en breve. ⏳"`.
  - `TestEscalationCountsTowardCap_SIAU`: true.
  - `TestNectarEscalation_SIAUSuggestsSIAUTeam`
  - `TestOutOfHoursMenu_HasImagingAndResults`
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - **El handler de `ESCALATE_TO_AGENT` en Néctar:** si el motivo es `siau`, usa `cfg.SuggestedTeamForSIAU()`; si no, lo de hoy.
  - **Imágenes:** la opción va como texto con la URL.
  - **«resultados»:** por ahora sigue yendo a `SHOW_RESULTS`; la Tarea 6 lo cambia.
- [ ] **Step 4: Verify.** `go test ./internal/statemachine/...` en verde.
- [ ] **Step 5: Commit.** `feat(menu): resultados de imágenes y atención al usuario (SIAU)`

### Task 6: Resultados: documento y verificación

**Files:**
- Create: `internal/statemachine/handlers/results_flow.go` (registro y handlers de verificación)
- Modify: `internal/statemachine/states.go`: `StateResultsAskDocType`, `StateResultsAskDocNumber`, `StateResultsAskBirthDate` (interactivos), `StateResultsLookup` (automático).
- Modify: `internal/statemachine/helpers.go`:
  - `sensitiveInputStates` gana los dos pasos de documento;
  - `freeTextInputStates` gana el de fecha y el de número, que solo registran el largo.
- Modify: `internal/statemachine/handlers/greeting.go`: «resultados» entra a `RESULTS_ASK_DOC_TYPE` cuando `resultsFeatureOn(cfg, sess)`.
- Modify: el armado de handlers en `cmd/server/main.go`.
- Test: `internal/statemachine/handlers/results_flow_test.go`

**Interfaces:**
- Consumes: `results.Client` (Tarea 2), `local.ResultsAttemptsRepo` y `HashDocument` (Tarea 4), el `PatientRepository.FindByDocument` de SIESA, y `parseDocType`, `docTypeMenuText` y `parseBirthDate` de los handlers del registro.
- Produces:
  - `func resultsFeatureOn(cfg *config.Config, sess *session.Session) bool`: `cfg.ResultsAPIURL != "" && cfg.ResultsPDFEnabled && session.IsNectarSession(sess.PhoneNumber)`.
  - Las claves de contexto `results_doc_type` y `results_doc_hash`. El número va en `results_doc`, que se borra al salir del flujo.
  - Las interfaces internas `resultsSearcher` (`Search` y `DownloadPDF`) y `resultsAttempts` (`IsLocked`, `RecordFailure` y `Reset`), para usar fakes en las pruebas.
  - El patrón que usa la Tarea 7: el lookup guarda en contexto `results_first_name`, `results_first_surname` y los estudios en JSON (`results_studies`). Esta clave también se borra al salir.

- [ ] **Step 1: Failing tests,** con fakes de SIESA, de la API y del tope:
  - `TestResults_FeatureOffKeepsTodayBehavior` (Review Focus 5): sin URL, con la bandera en false o con una sesión de Bird, «resultados» va a `SHOW_RESULTS`.
  - `TestResults_DocTypeThenNumberThenBirthDate`
  - `TestResults_VerifiedGoesToLookup`
  - `TestResults_MismatchSameMessageAsNotFound`: una fecha distinta y un paciente inexistente dan el mismo texto exacto, `"Los datos no coinciden. Volvamos a intentarlo.\n\n" + docTypeMenuText()`, y el mismo paso siguiente.
  - `TestResults_ThirdFailureLocksAndOffersPortalAndSIAU`
  - `TestResults_LockedUpFront` (Review Focus 1): con el tope ya bloqueado, al pedir resultados responde directo `"Por seguridad, la consulta está bloqueada por hoy. Puedes consultar tus resultados en " + cfg.ResultsURL + " o escribir *menú* y elegir Atención al usuario (SIAU)."`.
  - `TestResults_SIESADownSendsPortalNoAttempt`
  - `TestResults_EventsDoNotLeakPII`: `chat_events` registra los cambios de paso sin el número ni la fecha.
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - **Comparación de fechas:** solo la fecha, con `time.Time.Format("2006-01-02")` en ambos lados.
  - **Teléfono para el tope:** `sess.ContactPhone()` si existe; si no, `sess.PhoneNumber`.
  - **Pasos escalables:** cada estado nuevo registra su menú de regreso (`RegisterStaticReturnMenu`) con «Reanudar» y «Volver al menú», más las universales.
  - **Eventos:**
    - `results_menu`;
    - `results_verified`;
    - `results_verification_failed` con `{"attempt": n}`;
    - `results_fallback_link` con `{"reason": "siesa_down|locked"}`.
- [ ] **Step 4: Verify.** `go test ./internal/statemachine/... ./cmd/...` en verde.
- [ ] **Step 5: Commit.** `feat(resultados): verificación por documento y fecha de nacimiento`

### Task 7: Resultados: lista y envío del PDF

**Files:**
- Modify: `internal/statemachine/handlers/results_flow.go`: `RESULTS_LOOKUP`, `RESULTS_LIST` (interactivo) y `RESULTS_SEND` (automático).
- Modify: `internal/statemachine/states.go`
- Test: `internal/statemachine/handlers/results_flow_test.go`

**Interfaces:**
- Consumes:
  - de la Tarea 2: `results.SortAndDedupe`, `results.NameMatches`, `DownloadPDF` y `ErrInvalidFile`/`ErrTooLarge`;
  - de la Tarea 3: un remitente con `SendDocumentCtx` y `SendTextCtx`, inyectado como interfaz `resultsSender`;
  - de la Tarea 6: el contexto del lookup.
- Produces: `const resultsSendBudget = 45 * time.Second`.

- [ ] **Step 1: Failing tests:**
  - `TestResults_FiltersByVerifiedName` (Review Focus 2): estudios con otro nombre se descartan. Si ninguno queda, el texto exacto es `"No encontramos resultados para ese documento. 🔍\nPuede que aún no estén disponibles; intenta mañana.\n\nTambién puedes consultarlos en " + cfg.ResultsURL`, con el evento `results_name_mismatch`.
  - `TestResults_OneStudySentDirectly`
  - `TestResults_ListUpTo10`:
    - una lista de WhatsApp;
    - título = `StudyType` recortado a 24 runas;
    - descripción = `"Fecha: dd/mm/aaaa | Adm: <admision>"` recortada a 72;
    - con más de 10, solo los 10 más recientes, más el texto `"Para resultados anteriores ingresa a " + cfg.ResultsURL`.
  - `TestResults_NoneFound`: el mismo texto de «no encontramos» y el evento `results_none`.
  - `TestResults_SendWaitsThenDocument`:
    1. primero `SendTextCtx` con `"Un momento, estoy preparando tu resultado… ⏳"`;
    2. después `SendDocumentCtx` con `filename` = `"Resultado-<StudyType saneado>-<aaaa-mm-dd>.pdf"` (≤ 200; saneado = letras, números y guiones, sin tildes);
    3. `caption` = `"📄 Hola " + primer nombre de SIESA + ", tu resultado de *" + StudyType + "*."`;
    4. el evento `results_sent` con `{"study_type": ...}`.
  - `TestResults_SendFailuresFallBackToPortal` (Review Focus 4):
    - `ErrInvalidFile`, `ErrTooLarge`, la API caída y los errores tipados `nectar.ErrUnsupportedMedia`/`ErrTooLarge`/`ErrAttachmentUnavailable`;
    - todos envían `"No pude enviarte el archivo en este momento. 🙏 Puedes descargarlo en " + cfg.ResultsURL`;
    - evento `results_fallback_link` con el motivo, y nunca un panic.
  - `TestResults_SendRespectsBudget`: una descarga que no responde se corta a los 45 s (pruébalo con un presupuesto inyectable corto) y pasa al portal.
  - `TestResults_ContextCleared`: al terminar, `results_doc` y `results_studies` quedan borradas.
- [ ] **Step 2: Run them.** Esperado: FAIL.
- [ ] **Step 3: Implement.**
  - `RESULTS_SEND` usa `context.WithTimeout(ctx, resultsSendBudget)`, siguiendo el patrón del OCR (`defaultAnalyzeTimeout`, holgadamente por debajo de `processMsgTimeout` de 2 min).
  - Al terminar pasa a `buildAutoCloseResult`.
  - La selección de la lista sigue el patrón de las listas interactivas existentes (ids de fila y `ValidateButtonResponse`).
- [ ] **Step 4: Verify.** `go test ./internal/statemachine/... ./internal/results/` en verde, y luego `go test ./...`.
- [ ] **Step 5: Commit.** `feat(resultados): lista de estudios y envío del PDF por Néctar`

### Task 8: Documentación

**Files:**
- Modify: `CLAUDE.md`: un bloque «RESULTADOS Y SIAU» con las variables, el flujo, el tope, el filtro por nombre, la bandera y la privacidad.
- Modify: `.env.example`, si falta algo de la Tarea 1.

- [ ] **Step 1:** Escribe el bloque en el estilo denso de CLAUDE.md. Incluye la operación: el equipo SIAU en `NECTAR_TEAM_BY_GROUP`; `RESULTS_PDF_ENABLED=true` solo con el lote 11 de Néctar desplegado; y desactivar en n8n el chatbot de resultados, «Re-engagement 24h» y «SIAU - Finalizar sesión».
- [ ] **Step 2: Verify.** `go test ./...` y `go test -count=2 ./...` en verde.
- [ ] **Step 3: Commit.** `docs(resultados): resultados, imágenes y SIAU en CLAUDE.md`

---

## Self-review

**Cobertura de la spec:**

| Sección de la spec | Tarea |
|---|---|
| Configuración y grupo SIAU | 1 |
| Cliente de la API (400 = vacío, validación de archivo, plazos, tope) | 2 |
| Adjuntos de Néctar y router | 3 |
| Tope persistente | 4 |
| Menú, imágenes y SIAU (motivo, equipo, tope) | 5 |
| Verificación (mismo mensaje, SIESA caída, bloqueo) | 6 |
| Filtro por nombre, lista, envío, fallas y presupuesto | 7 |
| Operación | 8 |

**Nombres que cruzan tareas:**

| Nombre | Definido en | Usado en |
|---|---|---|
| `SuggestedTeamForSIAU` | 1 | 5 |
| `Study`, `NameMatches`, `SortAndDedupe` | 2 | 7 |
| `SendDocumentCtx` | 3 | 7 |
| `HashDocument`, `IsLocked`, `RecordFailure`, `Reset` | 4 | 6 |
| `resultsFeatureOn` | 6 | 5 (cambio de destino de «resultados») y 7 |
