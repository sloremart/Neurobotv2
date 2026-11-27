# Plan B Néctar: salidas proactivas (D1), contacto (D2) y horario: plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** que los recordatorios, la lista de espera y los avisos administrativos salgan por Néctar abriendo la conversación con plantilla, que la respuesta del paciente vuelva a `NotificationManager`, que las consultas «por paciente» sigan funcionando con sesiones `nectar:*` y que el horario pueda leerse de Néctar.

**Architecture:** el desvío sigue en `bird.Client` (patrón de `internal/bird/nectar_bridge.go`). Un método nuevo, `SendProactiveTemplate`, devuelve la **clave** con la que hay que registrar el pending: `nectar:<id>` con el puente o el teléfono con Bird. El pending guarda además el E.164 (`ContactPhone`) para el IVR y para la idempotencia. El contacto de una conversación de Néctar se lee una vez de `GET state` y viaja en el mensaje entrante.

**Tech Stack:** Go 1.26 (solo en Docker), MySQL local `neuro_bot` (migraciones en `migrations/`), cliente HTTP de `internal/nectar`.

**Spec:** `docs/superpowers/specs/2026-10-27-migracion-nectar-design.md` (§6.2, §7.2 puntos 4-6, §8, §12 fases 3-4). Contrato real medido en Néctar: `nectar/docs/superpowers/plans/2026-10-27-plan-44-el-bot-abre-la-conversacion.md` §1, más lo confirmado por `nectar-68` el 2026-10-29. Donde el spec y el Plan 44 difieren (por ejemplo, el spec dice `template_id` y lo correcto es nombre + idioma), manda el Plan 44.

## Global Constraints

- **Regla del dueño:** solo integración. El flujo del bot, qué se agenda o cancela y las validaciones por rama no cambian. Bird recibe exactamente lo mismo que hoy con `NECTAR_BASE_URL` vacía.
- **Go solo en Docker:**
  `gotest(){ MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app -v neurobot-go-mod:/go/pkg/mod -v neurobot-go-build:/root/.cache/go-build golang:1.26-alpine go "$@"; }`
  Para race: imagen `golang:1.26`, volumen `neurobot-go-build-debian`.
- **Línea base:** `go test ./...` falla SOLO en `TestAskClientType_Particular_SkipsListToDocument`, `TestMedicationCheck_SanitasNoChannel_Escalates` y `TestMedicationCheck_SanitasWithChannel_SendsLink`. Además: `go vet ./...` y gofmt.
- **Estilo:** identificadores en inglés y comentarios en español, con la densidad del código vecino.
- **Commits:**
  - Un commit por tarea, `feat(nectar): …` o `fix(nectar): …`, terminado con las líneas de atribución de la sesión.
  - Lefthook corre siempre: nunca `--no-verify` ni `LEFTHOOK=0`.
  - Nunca hacer stage de `auditoria/`, `bin/`, `cmd/e2e/`, `docs/CASO-*`, `docs/MAPA-*`, `internal/api/test_handler.go`, `scripts/`, `seed-kpis`, `.claude/`. Usar `git add <rutas exactas>`.
- **Contrato con Néctar:** no cambia. Nada de PII del paciente en logs (`utils.MaskPhone`). El bloque `contact` no se persiste en crudo fuera de las columnas y la sesión (Plan 44 §1.2).
- **Apertura (`POST /api/bot/conversations/open`):**
  - `idempotency_key` es un UUID v4 por envío, el mismo en sus reintentos.
  - Reintentos: solo el 503 con `Retry-After` (como mucho 2 reintentos, esperando lo que diga la cabecera, 2 s si falta). Nunca se reintenta ante 409 `attempt_in_doubt`, `attempt_sent_unrecorded` o `idempotency_key_reused`, ni ante un 502.
- **Variables nuevas:**
  - `NECTAR_CHANNEL_ID` (int64; 0 = la apertura no está configurada → error tipado, nada se envía).
  - `NECTAR_SCHEDULE_ENABLED` (por defecto false), `NECTAR_SCHEDULE_CHANNEL_ID` (0) y `NECTAR_SCHEDULE_REFRESH_MIN` (5).
- **Migración nueva:** `migrations/045_nectar_contact.sql` (era la 044; se renumeró al traer de main su `044_add_via_administracion`). La última hoy es la 043. Las columnas de teléfono ya son VARCHAR(63) desde la 040.

## Review Focus

1. **Re-ejecución del recordatorio después de una apertura** (el catch-up de las 07:00 relanzado): el pending está guardado con clave `nectar:<id>`. El segundo pase no debe abrir ni enviar otra plantilla. → test en la Tarea 4 (`HasPendingForContact`).
2. **409 `already_open` con `mine:false`** (la conversación la tiene un agente): no se envía nada, no se registra pending, se registra el evento `notification_skipped_agent_owned`. → test en las Tareas 1 y 4.
3. **El paciente pulsa el botón de la plantilla** y Néctar entrega el título («Confirmar») como `interactive_reply_id`: tiene que llegar a `HandleResponse` como `confirm`. Mientras Néctar no publique su arreglo, llega vacío y debe acabar en `HandleInvalidInput`, no en la máquina de estados. → tests en la Tarea 2.
4. **IVR de las 13:00 sobre un pending con clave `nectar:*`:** debe marcar al `ContactPhone` y emparejar la cita por ese número. → test en la Tarea 3.
5. **Entrada de lista de espera creada desde una sesión `nectar:*` antigua** (`waiting_list.phone_number = nectar:<id>` ya en BD): el envío no debe ir a esa conversación vieja, sino abrir por el teléfono del paciente en SIESA. → test en la Tarea 4.

---

### Task 1: Cliente de Néctar: apertura con plantilla y contacto en el estado

**Files:**
- Modify: `internal/nectar/client.go` (State, errores, `do`)
- Create: `internal/nectar/open.go`
- Modify: `internal/nectar/bridge.go`, `internal/bird/nectar_bridge.go` (interfaz)
- Test: `internal/nectar/open_test.go`, `internal/nectar/client_test.go`

**Interfaces:**
- Produces:
  - `type Contact struct{ Identifier, BSUID, ProfileName string }` (json `identifier`, `bsuid`, `profile_name`).
  - `State.Contact *Contact` (json `contact,omitempty`).
  - `type OpenRecipient struct{ Phone, BSUID string }`.
  - `func (c *Client) OpenConversation(ctx context.Context, channelID int64, to OpenRecipient, tmpl TemplateBody, name string) (Opened, error)`.
  - `type Opened struct{ ConversationID, ChannelID, MessageID int64 }`.
  - `type AlreadyOpenError struct{ ConversationID int64; Mine bool }`, que implementa `error`.
  - `var ErrOpenInDoubt` para `attempt_in_doubt`, `attempt_sent_unrecorded`, `meta_no_answer` y `sent_but_not_recorded`. Envuelve `bird.ErrPermanentSend`.
  - `var ErrTemplateRejected` para el 400 `template_parameters`, el 409 `template_not_offerable` / `template_other_account` y el 502 `meta_rejected`. También envuelve `bird.ErrPermanentSend`.
  - `func (b *Bridge) OpenWithTemplate(ctx context.Context, phone string, tmpl bird.TemplateConfig) (conversationID int64, messageID string, err error)` y `func (b *Bridge) Contact(ctx context.Context, conversationID int64) (bird.NectarContact, error)`.
  - En `bird.NectarBridge` se añaden esos dos métodos y `WindowOpen(ctx, conversationID int64) (bool, error)`. `bird.NectarContact{Identifier, BSUID string}` se declara en el paquete bird por la misma razón que `Escalation`.

- [ ] **Step 1: tests que fallan** (servidor `httptest`, con el estilo de `client_test.go`)
  - `TestOpenConversation_201`:
    - el cuerpo enviado tiene `idempotency_key` (UUID que `uuid.Parse` acepta), `channel_id`, `recipient.phone` y `template{name,language,parameters}` como objeto;
    - devuelve `Opened{ConversationID: 77, …}`.
  - `TestOpenConversation_AlreadyOpen`: 409 `{"reason":"already_open","conversation_id":55,"mine":false}` produce un `errors.As` a `*AlreadyOpenError{55, false}`. Con `mine:true` da `{55, true}`.
  - `TestOpenConversation_503RetryAfter`:
    - dos 503 con `Retry-After: 0` y luego 201 dan éxito tras 3 llamadas con la **misma** `idempotency_key`;
    - tres 503 dan error;
    - el reloj de espera se inyecta (`c.sleep`) para no dormir de verdad.
  - `TestOpenConversation_NoRetryOnDoubt`: el 409 `attempt_in_doubt` da `ErrOpenInDoubt` tras **una** llamada. Igual con el 502 `meta_no_answer`.
  - `TestOpenConversation_404`: el 404 sin cuerpo da un error que **no** es `ErrNotMine` (aquí significa «canal o BSUID ajeno»). Es el mismo cuidado que `ErrScheduleUnknownChannel` en hdg-bot.
  - `TestGetState_Contact`: el JSON del Plan 44 §1.2 rellena `Contact`; sin el bloque, `Contact == nil`.
  - `TestBridgeOpenWithTemplate`:
    - `NECTAR_TEMPLATE_NAMES` traduce `ProjectID` a nombre; los `Params` pasan a un mapa clave → valor;
    - el idioma es `Locale` con `-` cortado si así lo hace hoy `Bridge.SendTemplate`. Reusar su helper, no duplicarlo.
- [ ] **Step 2:** ejecutar `gotest test ./internal/nectar/ -run 'Open|Contact|GetState' -v`. Esperado: FAIL (no compila).
- [ ] **Step 3: implementar.**
  - `do` hoy traduce todo 404 a `ErrNotMine` y no lee `Retry-After`. Para la apertura, escribir un `doOpen` propio que reutilice firma y cabeceras (extraer el armado de la petición a un helper compartido, sin duplicarlo). El 409 se decodifica a `AlreadyOpenError` o al error tipado que toque.
  - `Bridge.Contact` devuelve lo de `GetState`, y vacío si `Contact == nil`.
  - `Bridge.WindowOpen` devuelve `State.WindowOpen`.
- [ ] **Step 4:** el mismo comando que en el Step 2. Esperado: PASS. Después `gotest test ./internal/nectar/ ./internal/bird/`: los fakes de `NectarBridge` en los tests de bird reciben los métodos nuevos.
- [ ] **Step 5:** commit `feat(nectar): cliente de la apertura con plantilla y del contacto en el estado`.

### Task 2: La respuesta del paciente por Néctar llega a NotificationManager

**Files:**
- Modify: `internal/api/nectar_handler.go` (en `dispatchNectar`, rama `KindInboundMessage`)
- Modify: `cmd/server/replay.go` (`replayNectarRow`)
- Create: `internal/notifications/template_buttons.go`
- Test: `internal/notifications/template_buttons_test.go`, `internal/api/nectar_handler_test.go`

**Interfaces:**
- Produces:
  - `func TemplateButtonPayload(id string) (string, bool)` en `notifications`. Traduce el título del botón de plantilla al payload.
  - `func (h *Handler) routeNectarInbound(msg bird.InboundMessage)` (o el receptor que ya use `nectar_handler.go`). Es la misma clasificación de `webhook_handler.go:234-250`.

- [ ] **Step 1: tests que fallan.**
  - `TestTemplateButtonPayload`:
    - `"Confirmar"`→`confirm`, `"Reprogramar"`→`reprogramar`, `"Cancelar"`→`cancelar`, `"Entendido"`→`understood`, `"Agendar"`→`wl_schedule`;
    - `"No, gracias"`, `"no gracias"` y `"NO GRACIAS"` → `wl_decline` (sin distinguir mayúsculas, tildes, espacios ni coma);
    - un payload que ya es payload (`"confirm"`) se devuelve igual con `true`;
    - `"hola"` da `false`.
    - Los pares salen de los botones de `HandleInvalidInput` (`manager.go:554-574`): una sola tabla, y `HandleInvalidInput` pasa a leer de ella.
  - `TestNectarInbound_PendingButton_GoesToHandleResponse`:
    - hay un pending para `nectar:77`;
    - llega un aviso con `interactive_reply_id:"Confirmar"`;
    - el fake de NotificationManager recibe `HandleResponse("nectar:77","confirm","77")`;
    - el sink del worker no recibe nada.
  - `TestNectarInbound_PendingEmptyButton_InvalidInput`: con pending, llega un mensaje vacío de kind `button` (lo de hoy, antes del arreglo de Néctar). Se llama a `HandleInvalidInput("nectar:77","77")` y no al sink.
  - `TestNectarInbound_NoPending_GoesToWorker`: sin cambio de comportamiento.
  - El mismo caso del primer test por el replay del WAL.
  - El handler necesita una interfaz pequeña (`HasPending`, `HandleResponse`, `HandleInvalidInput`) para el fake. Si `Handler` ya guarda un `notifyManager` concreto, introducir esa interfaz solo en el camino de Néctar.
- [ ] **Step 2:** ejecutar `gotest test ./internal/notifications/ ./internal/api/ -run 'TemplateButton|NectarInbound' -v`. Esperado: FAIL.
- [ ] **Step 3: implementar.**
  - La traducción del título se aplica **solo** cuando hay un pending para esa clave. Sin pending, el postback sigue tal cual a la máquina de estados: así los menús propios del bot, que ya devuelven su payload, no cambian.
  - El WAL se marca como hecho igual que en el camino de Bird.
- [ ] **Step 4:** ejecutar los tests del Step 2 y `gotest test ./...`. Esperado: solo la línea base.
- [ ] **Step 5:** commit `feat(nectar): la respuesta a una notificación por Néctar llega a NotificationManager`.

### Task 3: El pending sabe el teléfono del paciente (migración 045 + IVR)

**Files:**
- Create: `migrations/045_nectar_contact.sql`
- Modify: `internal/notifications/manager.go`, `internal/repository/local/notification_repo.go`, `internal/scheduler/tasks.go` (`sendVoiceReminders`)
- Test: `internal/notifications/manager_test.go` (o el archivo de tests vecino), `internal/scheduler/tasks_test.go`

**Interfaces:**
- Produces:
  - `PendingNotification.ContactPhone string`: E.164 del paciente; vacío = `Phone` ya es el teléfono (Bird).
  - `func (p PendingNotification) DialPhone() string`: `ContactPhone` si no está vacío, si no `Phone`.
  - `func (m *NotificationManager) HasPendingForContact(phone string) bool`: verdadero si algún pending tiene `Phone == phone` o `ContactPhone == phone`. Va con un índice en memoria contacto → clave, mantenido en Register, Delete, Restore y Resolve.
- Migración 045, todo aditivo y anulable:
  - `notification_pending.contact_phone VARCHAR(63) NULL` y `notification_history.contact_phone VARCHAR(63) NULL`;
  - `sessions.contact_identifier VARCHAR(63) NULL` con el índice `idx_sessions_contact`;
  - `chat_events.contact_identifier VARCHAR(63) NULL` y `escalations.contact_identifier VARCHAR(63) NULL`.
  - Seguir el formato de las migraciones vecinas: si usan `IF NOT EXISTS` o un procedimiento guardado, igual.

- [ ] **Step 1: tests que fallan.**
  - `TestHasPendingForContact`: se registra `{Phone:"nectar:77", ContactPhone:"573001112233"}`. `HasPendingForContact("573001112233")` y `HasPending("nectar:77")` son verdaderos. Tras `HandleResponse`, los dos son falsos.
  - `TestRestorePending_KeepsContact`: guardar y restaurar conserva `ContactPhone` y el índice.
  - `TestVoiceReminders_DialsContactPhone`: un pending `nectar:77` con `ContactPhone` y una cita cuyo `PatientPhone` normaliza a ese número hacen que `PlaceCall` reciba `"573001112233"`. `RegisterCallID` y `MarkIVRSent` siguen usando la clave del pending (`nectar:77`). El whitelist se evalúa sobre `DialPhone()`.
- [ ] **Step 2:** ejecutar `gotest test ./internal/notifications/ ./internal/scheduler/ -run 'Contact|VoiceReminders' -v`. Esperado: FAIL.
- [ ] **Step 3: implementar.**
  - `Upsert`, `FindAll`, `FindExpired` y `Resolve` leen y escriben `contact_phone`. La copia a history también.
  - El emparejamiento `apptByPhone[pending.Phone]` pasa a `apptByPhone[pending.DialPhone()]`.
- [ ] **Step 4:** ejecutar los tests y la suite. Con MySQL local levantado, aplicar la 045 con el mecanismo que ya use el repo y comprobar que arranca.
- [ ] **Step 5:** commit `feat(nectar): el pending guarda el teléfono del paciente y el IVR marca a ese número`.

### Task 4: Las plantillas proactivas abren la conversación en Néctar

**Files:**
- Modify: `internal/bird/client.go`, `internal/bird/nectar_bridge.go`, `internal/config/config.go` (`NectarChannelID`)
- Modify: `internal/scheduler/tasks.go` (:259, :441, preparación del examen y re-enganche), `internal/notifications/waiting_list_check.go` (:387), `internal/api/internal_handler.go` (:1372, :1537, :1646, :2015), `internal/notifications/manager.go` (`WindowOpen`)
- Test: `internal/bird/nectar_bridge_test.go` y los tests vecinos de cada sitio cambiado

**Interfaces:**
- Consumes: `Bridge.OpenWithTemplate`, `AlreadyOpenError`, `Bridge.WindowOpen` (Tarea 1); `ContactPhone` y `HasPendingForContact` (Tarea 3).
- Produces:
  - `type ProactiveSend struct{ MessageID, Key string }`.
  - `func (c *Client) SendProactiveTemplate(ctx context.Context, phone string, tmpl TemplateConfig) (ProactiveSend, error)`.
  - `var ErrConversationWithAgent`: el 409 con `mine:false`. Envuelve `ErrPermanentSend`.

- [ ] **Step 1: tests que fallan.**
  - `TestSendProactiveTemplate_BirdUnchanged`: sin puente, la petición a Bird es byte a byte la de `SendTemplate` y `Key == phone`.
  - `TestSendProactiveTemplate_NectarOpens`: con puente, se llama a `OpenWithTemplate("573…")` y da `Key == "nectar:77"`.
  - `TestSendProactiveTemplate_AlreadyOpenMine`: con `AlreadyOpenError{55,true}` se llama a `SendTemplate` del puente sobre 55 y da `Key == "nectar:55"`.
  - `TestSendProactiveTemplate_AlreadyOpenAgent`: con `{55,false}` da `ErrConversationWithAgent`, sin envío.
  - `TestSendProactiveTemplate_KillSwitchAndRateLimit`: `notifWADisabled` y `allowSend` se aplican antes de abrir, como en `SendTemplate`.
  - `TestSendProactiveTemplate_NoChannel`: con puente y `NECTAR_CHANNEL_ID=0` da un error permanente y no hay llamada HTTP.
  - Por sitio, tests con fakes:
    - el pending se registra con `Phone = Key` y `ContactPhone` = el E.164 cuando `Key != phone`;
    - el idempotente de `tasks.go` (:216, :397) usa `HasPendingForContact(phone)`;
    - `ErrConversationWithAgent` registra el evento `notification_skipped_agent_owned` y `continue`, sin pending;
    - el `ConversationID` del pending para Néctar es el id numérico, no un lookup de Bird.
  - Lista de espera:
    - si `entry.PhoneNumber` empieza por `nectar:`, el teléfono de envío sale del paciente en SIESA con el `patientRepo`, que ya usa el manager para el nombre (verificarlo). Si no hay teléfono válido: `skipped` + log, sin envío.
    - El whitelist se evalúa sobre ese E.164.
  - `WindowOpen(ctx, "nectar:77")` pregunta a `Bridge.WindowOpen` (error = cerrada) y no a `LastPatientMessageAt`. La preparación del examen y el re-enganche usan esa respuesta.
- [ ] **Step 2:** ejecutar `gotest test ./internal/bird/ ./internal/scheduler/ ./internal/notifications/ ./internal/api/ -v -run 'Proactive|Reminder|Waiting|Agenda|Reschedule|NotifyConfirmation|WindowOpen'`. Esperado: FAIL.
- [ ] **Step 3: implementar.**
  - Los siete sitios de `SendTemplate` (menos `queue_expired.go`, que ya trabaja sobre una conversación viva) pasan a `SendProactiveTemplate`.
  - El lookup de conversación de Bird (`GetCachedConversationID`/`LookupConversationByPhone`) queda solo para `Key == phone`.
  - Los eventos de `Tracker.LogEvent` se escriben con `Key` (es la clave de la sesión que responderá).
  - La primera conversación nueva de Néctar no tiene sesión, así que la preparación del examen sale solo si `WindowOpen` dice abierta.
  - Bird: `SendTemplate` queda tal cual para quien lo siga usando.
- [ ] **Step 4:** ejecutar los tests y la suite completa. Esperado: solo la línea base.
- [ ] **Step 5:** commit `feat(nectar): recordatorios, lista de espera y avisos administrativos abren la conversación en Néctar`.

### Task 5: El bot cierra en Néctar las conversaciones que abrió y nadie contestó

**Files:**
- Modify: `internal/notifications/manager.go` (el final de `handleTimeout` y de la cadena de vencimiento) o `confirmation.go`, donde el pending se resuelve como sin respuesta
- Test: el test vecino del vencimiento

**Interfaces:**
- Consumes: `bird.Client.CloseConversation` o el `Close` existente que desvía a Néctar con la clave `nectar:*`. Verificar el nombre en `bird/client.go`.

- [ ] **Step 1: test que falla.** `TestPendingExpired_NectarClosesConversation`:
  - un pending `nectar:77` vence sin respuesta, y ya pasó el IVR si aplicaba (respetar el orden actual: confirmación → `RetryCount` → `escalateToAgent`);
  - en el punto donde hoy se da por «sin respuesta» y **no** se escala, sin sesión activa para `nectar:77`, se llama `Close` sobre 77 una vez;
  - si hay sesión activa o se escaló, no se cierra;
  - con Bird no se llama nada.
- [ ] **Step 2:** ejecutarlo. Esperado: FAIL.
- [ ] **Step 3: implementar.** `ErrNotMine` al cerrar se ignora (la conversación ya no es del bot). Primero leer la cadena y anotar en el informe en qué ramas el pending termina sin escalar. Si **todas** escalan, la tarea se reduce a cerrar solo las ramas de lista de espera y cancelación, y se dice.
- [ ] **Step 4:** ejecutar el test y la suite.
- [ ] **Step 5:** commit `feat(nectar): el bot cierra las conversaciones que abrió con plantilla y nadie contestó`.

### Task 6: Contacto de la conversación (D2) y lista blanca

**Files:**
- Modify: `internal/bird/types.go` (o donde viva `InboundMessage`): campo `ContactIdentifier string`
- Modify: `internal/api/nectar_handler.go`: leer el contacto con caché por conversación
- Modify: el creador de sesión del worker o session manager (`sessions.contact_identifier` y la clave de contexto `contact_identifier`), `internal/repository/local/event_repo.go` (al insertar en `chat_events`) y `escalation_repo.go` (en `Create`)
- Modify: `internal/statemachine/handlers/slots.go:1219,:1345` y `medical_order.go:255`: la entrada de lista de espera guarda el E.164 cuando la sesión es `nectar:*`
- Modify: `session_repo.go` (`FindRecentByPhone`) y `event_repo.go` (`FindByPhone`): `WHERE phone_number=? OR contact_identifier=?`
- Test: los tests vecinos de cada uno

**Interfaces:**
- Consumes: `Bridge.Contact` (Tarea 1); columnas de la 045 (Tarea 3).
- Produces:
  - `const sm.CtxContactIdentifier = "contact_identifier"`.
  - `func ContactPhoneOf(sess *session.Session) string`: el E.164 del contacto, o `""` si es BSUID u oculto. Vive en `handlers` o en `session`; elegir según dónde no cree ciclo.

- [ ] **Step 1: tests que fallan.**
  - `TestNectarInbound_FetchesContactOncePerConversation`: dos avisos de la misma conversación hacen **una** sola llamada a `Contact`, y los dos mensajes llevan `ContactIdentifier`. Un fallo de `Contact` deja el campo vacío, emite el evento de observabilidad `nectar/contacto_no_disponible` y el mensaje **sigue** al worker.
  - `TestNectarInbound_Whitelist`:
    - con `TESTING_WHITELIST_PHONES=573001112233`, un contacto distinto se descarta igual que en `webhook_handler.go:167` (mismo log y WAL marcado);
    - un contacto vacío **pasa** (spec §7.2.6: aísla el workspace de pruebas);
    - con la lista vacía, todo pasa.
  - `TestSessionCreate_StoresContactIdentifier`: la columna y el contexto quedan escritos; `chat_events` y `escalations` copian el valor.
  - `TestWaitingListEntry_NectarStoresPhone`:
    - en una sesión `nectar:77` con contacto `573…`, la entrada guarda `573…`;
    - sin contacto, el teléfono del paciente en SIESA ya cargado en la sesión, si existe; si no, la clave como hoy, y la Tarea 4 resuelve al enviar.
    - Es un cambio de **dato** guardado, no de flujo: el handler sigue en el mismo estado.
  - `TestFindRecentByPhone_MatchesContact`, `TestEventsFindByPhone_MatchesContact`.
- [ ] **Step 2:** ejecutarlos. Esperado: FAIL.
- [ ] **Step 3: implementar.**
  - La caché es un `sync.Map` conversación → contacto con TTL de 24 h. No se persiste en el WAL: el replay vuelve a pedirlo.
  - El contacto no va a ningún log sin máscara.
- [ ] **Step 4:** ejecutar los tests y la suite.
- [ ] **Step 5:** commit `feat(nectar): el bot conoce el teléfono del paciente en Néctar (D2) y aplica la lista blanca`.

### Task 7: El horario se lee de Néctar (apagado por defecto en código; el dueño lo enciende)

> **Decisión del dueño (2026-10-29):** el bot debe trabajar con el horario de Néctar, festivos incluidos, para atender y para escalar. El código conserva `false` por defecto, con el horario de hoy como respaldo seguro. El `.env` de Neuro lo enciende (`NECTAR_SCHEDULE_ENABLED=true`) cuando los tres equipos tengan horario en Néctar (A2 del alta). Si no, Néctar publica «cerrado toda la semana».

**Files:**
- Port desde hdg-bot (`C:\laragon\www\CHATBOT\hdg-bot`, HEAD 2810172), por función y no por diff:
  - `internal/nectar/schedule.go` → `internal/nectar/schedule.go`
  - `internal/config/schedule.go`, `schedule_remote.go` y la parte de `business_hours.go` que convierte ventanas y festivos → `internal/config/`
  - `cmd/server/schedule_nectar.go` → `cmd/server/schedule_nectar.go`
- Modify: `internal/statemachine/handlers/greeting.go`: `businessHoursOpen`, `businessWindow`, `nextBusinessOpen`, `BusinessHoursDescription` y `minutesToClose` leen del proveedor
- Test: los tests portados con cada pieza, más `greeting_test.go`

**Interfaces:**
- Produces:
  - `config.ScheduleSource` con `Current() BusinessHours`, y `(*Config).Hours()`.
  - El respaldo es el horario de hoy, exacto: L-V 07:00-18:00 y S 07:00-12:00, con `To` exclusivo y sin festivos.

- [ ] **Step 1: tests que fallan.**
  - `TestBusinessHours_FallbackEqualsLegacy`: con `NECTAR_SCHEDULE_ENABLED=false`, para cada hora de una semana entera (paso de 15 min), `businessHoursOpen`, `nextBusinessOpen` y `minutesToClose` dan **lo mismo** que la implementación de hoy. Copiar la de hoy al test como oráculo antes de cambiarla.
  - `TestBusinessHours_FromNectar`: una publicación con L-V 08:00-17:00 más un festivo hace que el lunes festivo esté cerrado y que `BusinessHoursDescription` lo diga.
  - Los tests portados de hdg (304 / `If-None-Match`, 404 de canal, publicación inválida → se conserva la anterior).
- [ ] **Step 2:** ejecutarlos. Esperado: FAIL.
- [ ] **Step 3: implementar el port.**
  - Con la variable apagada no se hace ninguna llamada a Néctar y no cambia ningún texto.
  - `TESTING_ALWAYS_OPEN` sigue mandando.
- [ ] **Step 4:** ejecutar los tests y la suite.
- [ ] **Step 5:** commit `feat(nectar): el horario y los festivos pueden leerse de Néctar (NECTAR_SCHEDULE_ENABLED)`.

### Task 8: Documentación y configuración

**Files:** `.env.example`, `CLAUDE.md` (sección NÉCTAR), `docs/superpowers/specs/2026-10-27-migracion-nectar-design.md` (nota de lo construido).

- [ ] **Step 1:** `.env.example` documenta `NECTAR_CHANNEL_ID`, `NECTAR_SCHEDULE_ENABLED`, `NECTAR_SCHEDULE_CHANNEL_ID` y `NECTAR_SCHEDULE_REFRESH_MIN`, sin valores reales.
- [ ] **Step 2:** `CLAUDE.md` gana 3-4 líneas: apertura con plantilla y clave del pending; títulos exactos que deben tener los botones de las plantillas en Meta (la tabla de la Tarea 2); el contacto y la lista blanca; el horario.
- [ ] **Step 3:** en el spec, una nota «Construido el 2026-10-29» en §6.2, con lo que se apartó (nombre + idioma en vez de `template_id`; botones de plantilla traducidos por título; D5 pendiente en Néctar).
- [ ] **Step 4:** ejecutar `gotest vet ./...`.
- [ ] **Step 5:** commit `docs(nectar): plan B — apertura, contacto y horario`.

---

## Fuera de este plan
- **D5** (lectura de agentes): depende de Néctar; el dueño de Néctar lo agenda después del Plan 43.
- **Botón de plantilla en Néctar** (`type:"button"`): arreglo pedido a `nectar-68`, pendiente de la orden de su dueño. Hasta entonces, la Tarea 2 deja la respuesta vacía en `HandleInvalidInput`, que reenvía los botones interactivos (esos sí vuelven con su id).
- Pruebas en vivo: esperan al alta de Neuro en Néctar y a las variables `NECTAR_*`.
