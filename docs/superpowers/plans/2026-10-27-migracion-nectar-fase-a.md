# Migración a Néctar — Plan A: puente, escalación v1 y dashboard

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que Neurobotv2 reciba y conteste conversaciones de WhatsApp a través de Néctar, escale a agentes con el protocolo v1 y acepte el retorno al bot, sin cambiar nada cuando `NECTAR_BASE_URL` está vacía.

**Architecture:** Se porta el puente que ya corre en hdg-bot (`main`): un paquete `internal/nectar` (firma, avisos, cliente de las ocho rutas, capacidades), un desvío dentro de `bird.Client` que reconoce las sesiones `nectar:<id>` y las manda a Néctar, un webhook `POST /api/webhooks/nectar` con deduplicación por `event_id` sobre el WAL existente, y la adaptación del handler de escalación de Neuro al sobre v1 (motivo cerrado, resumen estructurado, descriptor de entrada, equipo sugerido por grupo de CUPS). Bird sigue intacto para voz y para toda sesión que no sea `nectar:*`.

**Tech Stack:** Go 1.26 (sin Go en el host: todo en contenedores), MySQL local (sin migraciones en este plan), Néctar `https://nectar.colibrixa.com` en producción, stub local `cmd/nectar-stub`.

**Spec:** `docs/superpowers/specs/2026-10-27-migracion-nectar-design.md` (fases 1, 2 y 5 del §12; las fases 3 y 4 —contacto D2 y salidas proactivas D1— van en un Plan B cuando Néctar publique los esquemas JSON de `open` y de `state.contact`).

## Global Constraints

- Módulo `github.com/neuro-bot/neuro-bot`. Todo lo que se porte de hdg-bot cambia `github.com/sloremart/hdg-bot` por ese módulo; identificadores en inglés, comentarios en español (regla del repo).
- Con `NECTAR_BASE_URL` vacía el binario se comporta **byte a byte como hoy**: cada desvío comprueba `c.nectar != nil` y el prefijo `nectar:`; un `conversation_id` numérico a secas NUNCA desvía (incidente hdg 2026-09-08).
- Dependencias entre paquetes: `internal/nectar` importa `internal/bird`; `internal/bird` NUNCA importa `internal/nectar` (la interfaz `NectarBridge` y los tipos `Escalation`/`Rejection` viven en `bird`). `internal/api` importa `nectar` y `worker`; `worker` no importa `api`.
- **Sin migraciones de base de datos.** La columna de contacto del §7.2 punto 5 es del Plan B.
- Los literales de cable (kinds de aviso, claves JSON `paso`, `opciones`, `rotulo`, `valor`, `salida`, `campos`, `facts`, `input`, `resolve_lock`, cabeceras `X-Nectar-Signature`, `X-Nectar-Request-Timestamp`, `X-Bot-Service-Key`) se copian EXACTOS de hdg-bot; el esquema de Néctar es `additionalProperties:false`.
- Pruebas y build en contenedor, con la caché del repo. Alias para todos los pasos «Run»:
  ```bash
  GOTEST='MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app -v neurobot-go-mod:/go/pkg/mod -v neurobot-go-build:/root/.cache/go-build golang:1.26-alpine go'
  # ejemplo: eval $GOTEST test ./internal/nectar/... -run TestVerifyNotice -v
  ```
  (la imagen es `golang:1.26-alpine`, la del `Dockerfile`; el `Makefile` aún dice 1.25 y descargaría la toolchain). `make lint` y `make fmt` antes de cada commit; el pre-commit de lefthook corre formato y lint de lo nuevo.
- Toda emisión nueva de `observability.Emit(flow, step)` se registra en el `catalog` de `internal/observability/tracer.go` (hay un test que cruza el árbol contra el catálogo; sin la entrada, rojo).
- Commits en español con prefijo convencional y el trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`. Un commit por tarea como mínimo.
- Fuera de este plan (Plan B): apertura iniciada por el bot (D1), contacto de la conversación (D2) y la columna `contact_identifier`, lista blanca para `nectar:*`, retiro de la supresión por fallos de entrega (D3), lector de horario de Néctar (`schedule.go`, requiere portar `config.BusinessHours`/`CachedSchedule` de hdg-bot y no aporta nada apagado), endpoint de agentes para el dashboard (D5), retiro del código de Bird.

## Review Focus

1. Un aviso firmado con `occurred_at` ilegible o con un cuerpo cercano a 1 MB → `200`, se procesa con el instante a cero y la traza no bloquea la goroutine (Tarea 3, `TestNoticeTime_UnreadableIsZeroAndLogged` y `TestTruncateForLog_OnePass`).
2. `NECTAR_BASE_URL` puesta pero sin `NECTAR_SERVICE_KEY` o `NECTAR_REQUEST_SECRET` → el bot arranca como Bird puro, con un `slog.Error` que dice qué falta; nunca un puente a medias (Tarea 11, `TestNectarWiring_PartialConfigDisablesBridge`).
3. Retorno del agente con un token que no pertenece al paso (menú viejo) → se reanuda sin decisión, sin inyectar nada al paciente y sin trazar el valor (Tarea 7c, `TestHandleAgentResume_UnknownTokenResumesWithoutDecision`).
4. Cierre por silencio de una sesión `nectar:*` cuya conversación ya no es del bot (`404` en `close`) → la sesión se cierra igual, sin error ni reintento (Tarea 8, `TestCloseEscalatedBySilence_NectarNotMineIsSuccess`).
5. Escalación desde una sesión `nectar:*` con mensajes del caller acumulados → salen ANTES del `escalate` y no se repiten después; en Bird el orden de hoy no cambia (Tarea 7b, `TestPreEscalationFlush_NectarSendsBeforeEscalate` y `TestPreEscalationFlush_BirdUnchanged`).

---

## Mapa de archivos

| Archivo | Responsabilidad | Tarea |
|---|---|---|
| `internal/bird/client.go` (mod) | firmas con `phone` primero, `ErrPermanentSend`, 11 puntos de desvío | 1, 2 |
| `internal/bird/nectar_bridge.go` (new) | `NectarBridge`, `Escalation`, `Rejection`, `SetNectar`, `nectarConversation`, `HasAvailableAgentsFor`, `ConversationState`, `EscalateToAgentWithSummary` | 2 |
| `internal/nectar/{signature,destino,notice,client,bridge,capabilities}.go` (new) | la frontera con Néctar | 3, 5 |
| `internal/config/{config,protocol}.go` (mod/new) | `NECTAR_*`, `PROTOCOL_ESCALADO_VERSION`, grupo de CUPS → equipo | 4 |
| `internal/api/{nectar_handler,nectar_capabilities}.go` (new), `webhook_handler.go` (mod) | webhook de avisos, capacidades | 5, 6 |
| `internal/statemachine/{return_menu,input_descriptor}.go` (new), `helpers.go`, `machine.go` (mod) | menú de retornos, descriptor, rechazo tipado, gancho pre-escalación | 7a, 7b |
| `internal/worker/{agent_command,pool}.go` (mod), `queue_expired.go` (new) | retorno del agente, cola vencida, guardas `nectar:*` | 7c |
| `internal/session/{types,manager}.go` (mod), `internal/repository/local/session_repo.go` (mod) | `IsNectarSession`, exención de la cola, checker sin no-show para Néctar | 8 |
| `internal/statemachine/handlers/{summary_groups,escalation}.go` (new/mod), registros de retorno en los handlers | resumen estructurado, sobre v1, cola, equipo por grupo | 9a, 9b, 9c |
| `internal/notifications/{manager,confirmation}.go` (mod) | escalaciones de notificaciones por la firma nueva | 10 |
| `internal/services/ocr_service.go`, `cmd/server/main.go`, `.env.example`, docs (mod) | credenciales de adjuntos, cableado, salud, documentación | 11 |
| `cmd/nectar-stub`, `cmd/probe-notice` (new) | pruebas locales sin Néctar real | 12 |
| `neuro-dashboard` (`internal/reports`, `frontend/src/pages/{Reports,Tickets}.tsx`) | «ver en Néctar» | 13 |

---

### Task 1: Firmas con `phone` primero y `ErrPermanentSend` en el cliente de Bird

El desvío decide por la clave de sesión, así que cada método del cliente que hoy recibe solo `conversationID` tiene que recibir también el `phone`. Es un refactor mecánico sin cambio de comportamiento.

**Files:**
- Modify: `internal/bird/client.go` (firmas de `SendInternalText`, `UnassignFeedItem`, `CloseFeedItems`, `MarkConversationEscalated`, `TagConversation`; `IsPermanentSendError`)
- Modify: `internal/worker/pool.go` (interfaz `MessageSender` y llamadas), `internal/session/manager.go` (interfaz `InactivityBirdClient` y llamadas), `internal/notifications/{confirmation,manager,reschedule,waiting_list}.go`, `internal/statemachine/handlers/escalation.go`, `internal/testutil/mocks.go` y todo `_test.go` que implemente esas interfaces.
- Test: `internal/bird/send_errors_test.go`

**Interfaces:**
- Produces:
  - `func (c *Client) SendInternalText(phone, conversationID, text string) (string, error)`
  - `func (c *Client) UnassignFeedItem(phone, conversationID string, closed bool) error`
  - `func (c *Client) CloseFeedItems(phone, conversationID string) error`
  - `func (c *Client) MarkConversationEscalated(phone, conversationID, teamName, patientName string) error`
  - `func (c *Client) TagConversation(phone, conversationID, tag string) error`
  - `var ErrPermanentSend = errors.New("envío rechazado de forma permanente")` en `internal/bird/client.go`; `IsPermanentSendError(err)` devuelve `true` también cuando `errors.Is(err, ErrPermanentSend)`.

- [ ] **Step 1: Test del error permanente**

```go
func TestIsPermanentSendError_WrapsErrPermanentSend(t *testing.T) {
	err := fmt.Errorf("nectar: 404: %w", ErrPermanentSend)
	if !IsPermanentSendError(err) { t.Fatal("ErrPermanentSend envuelto debe ser permanente") }
	if IsPermanentSendError(errors.New("timeout")) { t.Fatal("un error cualquiera no es permanente") }
}
```

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/bird/ -run TestIsPermanentSendError_WrapsErrPermanentSend -v` → FAIL (`ErrPermanentSend` no definido).

- [ ] **Step 3: Implementar** `ErrPermanentSend` y la rama en `IsPermanentSendError`; cambiar las cinco firmas en `client.go` (el `phone` se ignora en el cuerpo por ahora) y actualizar TODOS los llamadores: el `phone` disponible en cada sitio es `sess.PhoneNumber`, `cmd.Phone`, `s.PhoneNumber` (session), `pending.Phone` / `p.Phone` (notifications), `msg.Phone` (escalation). Las interfaces `worker.MessageSender` e `session.InactivityBirdClient` cambian a las firmas nuevas; los dobles de prueba también.

- [ ] **Step 4: Run** `eval $GOTEST build ./... && eval $GOTEST test ./...` → todo verde (sin cambio de comportamiento).

- [ ] **Step 5: Commit** `refactor(bird): phone primero en las firmas de Inbox y ErrPermanentSend`

---

### Task 2: Desvío a Néctar dentro de `bird.Client`

**Files:**
- Create: `internal/bird/nectar_bridge.go` (portar de hdg-bot `internal/bird/nectar_bridge.go` íntegro)
- Modify: `internal/bird/client.go` (campo `nectar NectarBridge` en `Client`; 11 puntos de desvío)
- Test: `internal/bird/nectar_bridge_test.go` (portar de hdg-bot y ajustar a las firmas de Neuro)

**Interfaces:**
- Consumes: Tarea 1.
- Produces (todas en `package bird`):
  - `type NectarBridge interface { SendText(ctx, id int64, text string) (string, error); SendButtons(ctx, id int64, text string, buttons []Button) (string, error); SendList(ctx, id int64, body, buttonLabel string, sections []ListSection) (string, error); SendTemplate(ctx, id int64, tmpl TemplateConfig) (string, error); Available(ctx, id int64) (bool, string, error); Escalate(ctx, id int64, esc Escalation) error; Close(ctx, id int64) error; ConversationState(ctx, id int64) (string, error); IsNotMine(err error) bool }`
  - `type Escalation struct { Protocol int; Reason, ReasonDetail, Summary string; Facts, ReturnMenu, Input []byte; ResolveLock string; SuggestedTeamID *int64; Rejection *Rejection }`
  - `type Rejection struct { Detail string; Attempt int; Echo, Field, Piece string }`
  - `var ErrNoConversationState`
  - `func (c *Client) SetNectar(b NectarBridge)`
  - `func (c *Client) nectarConversation(phone, conversationID string) (int64, bool)` (prefijo `nectar:`; id ≤ 0 → false; descuadre con `conversationID` → `slog.Warn`)
  - `func (c *Client) HasAvailableAgentsFor(phone, conversationID string) bool`
  - `func (c *Client) ConversationState(ctx context.Context, phone, conversationID string) (string, error)`
  - `func (c *Client) EscalateToAgentWithSummary(ctx context.Context, conversationID, phone, teamID, teamName, patientName, fallbackTeamID string, esc Escalation) (agentID, agentName string, err error)` — Néctar: `c.nectar.Escalate`; Bird: `EscalateToAgent` + `SendInternalText(phone, conversationID, esc.Summary)`.

- [ ] **Step 1: Tests del desvío** (portar `nectar_bridge_test.go` de hdg-bot; nombres que deben existir):
  - `TestNectarConversation_OnlySessionKeyDetours` (`"nectar:42"` → 42,true; `"+573001112233"` con conversationID `"42"` → false; `"nectar:0"` → false; `"nectar:x"` → false)
  - `TestRealPhonesStayOnBird` y `TestWithoutBridgeNothingChanges` (con `c.nectar == nil`, `SendText("nectar:42", ...)` sigue el camino de Bird)
  - `TestSendText_NectarRoutesBeforeBsuid` (con puente puesto, `SendText("nectar:42","","hola")` llama al puente y NO a `sendViaBsuid`: la clave no es E.164 y el desvío va ANTES de esa rama)
  - `TestCloseFeedItems_NectarNotMineIsSuccess` (`Close` devuelve un error que `IsNotMine` reconoce → `CloseFeedItems` devuelve nil)
  - `TestEscalateToAgentWithSummary_NectarSingleCall` / `_BirdSendsInternalNote`

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/bird/ -run 'TestNectarConversation|TestRealPhones|TestWithoutBridge|TestSendText_Nectar|TestCloseFeedItems_Nectar|TestEscalateToAgentWithSummary' -v` → FAIL (símbolos ausentes).

- [ ] **Step 3: Implementar** `nectar_bridge.go` y los desvíos en `client.go`, exactamente en estos puntos y con esta semántica (los de hdg-bot, mismo orden):
  1. `MarkConversationEscalated`: `nectar:*` → `return nil`.
  2. `FetchMessageText`: `c.nectar != nil && messageID == ""` → `""`.
  3. `CreateConversationForPhone`: `nectar:*` → devolver el id como cadena, nil.
  4. `SendText` / `SendButtons` / `SendList`: después de `allowSend` y ANTES de la rama `!utils.IsE164(to)`; en `SendButtons` truncar títulos (`truncateWhatsApp`) antes de pasar al puente; en `SendList` convertir secciones.
  5. `SendInternalText`: `nectar:*` → `slog.Debug("nectar: nota interna sin destino, descartada")`, `"", nil`.
  6. `SendTemplate`: `nectar:*` → puente, ANTES del kill switch `notifWADisabled` (el puente aplica su propia política).
  7. `UnassignFeedItem` y `TagConversation`: `nectar:*` → `nil`.
  8. `CloseFeedItems`: `nectar:*` → `c.nectar.Close`; error con `IsNotMine` → nil.
  9. `LookupConversationByPhone`: `nectar:*` → id como cadena.

- [ ] **Step 4: Run** `eval $GOTEST test ./internal/bird/...` → PASS.

- [ ] **Step 5: Commit** `feat(bird): desvío a Néctar para sesiones nectar:* (NectarBridge)`

---

### Task 3: Paquete `internal/nectar`: firma, avisos y cliente

**Files:**
- Create: `internal/nectar/signature.go`, `destino.go`, `notice.go`, `client.go`, `bridge.go` (portar de hdg-bot; en `notice.go` se conservan los CUATRO `Kind` y `MaskedForInbox`; se ELIMINAN de `client.go` `GetSchedule`/`doWithHeaders` si solo los usa el horario —dejar `do` sencillo— y de `notice.go` nada).
- Test: `internal/nectar/signature_test.go`, `destino_test.go`, `notice_test.go`, `notice_protocolo_v1_test.go`, `notice_retorno_v1_test.go`, `notice_volcado_nectar_test.go` (fixtures copiados de `C:\laragon\www\CHATBOT\docs\volcados` a `internal/nectar/testdata/`), `client_test.go`, `escalate_protocolo_v1_test.go`, `escalate_rechazo_test.go`.

**Interfaces:**
- Consumes: `bird.Escalation`, `bird.Rejection`, `bird.ErrPermanentSend`, `bird.VerifySignatureWithKey`, `bird.InboundMessage`, `utils.MaskPhone`.
- Produces:
  - `const SignatureHeader = "X-Nectar-Signature"`, `TimestampHeader = "X-Nectar-Request-Timestamp"`, `ServiceKeyHeader = "X-Bot-Service-Key"`
  - `func VerifyNotice(secret, signature, timestamp, requestURL string, body []byte) error` (ventana ±5 min; errores `ErrNotConfigured`, `ErrMissingHeader`, `ErrTimestamp`, `ErrWindow`, `ErrSignature`)
  - `func Sign(secret, timestamp, requestURL string, body []byte) string`, `func Timestamp(time.Time) string`, `func SignRequest(req *http.Request, secret string, at time.Time, body []byte)`
  - `func HostOf(baseURL string) string`, `func IsStubHost(baseURL string) bool`
  - `type Kind string`; `KindInboundMessage`, `KindConversationResolved`, `KindConversationReturned`, `KindConversationOpenedByAgent`; `func KnownKinds() []Kind`; `func (Kind) Known() bool`
  - `type Notice struct { EventID string; Protocol int; Kind Kind; ConversationID int64; OccurredAt NoticeTime; Message *NoticeMessage; ReturnValue *string; Return *V1Return; Motivo string; Opened *NoticeOpened }` con `ParseNotice([]byte) (Notice, error)`, `(Notice) ValidOpening() bool`, `(Notice) MaskedForInbox() ([]byte, error)`, `(Notice) ChosenReturnValue() (string, bool)`, `(Notice) V1Return() (V1Return, bool)`, `(Notice) ToInboundMessage(baseURL string) bird.InboundMessage`
  - `func SessionKey(conversationID int64) string`, `func ConversationIDFromSessionKey(key string) (int64, bool)`, `const ProtocolMax = 1`, `const UnusableAttachmentType = "adjunto_no_disponible"`
  - `type Client` con `NewClient(baseURL, serviceKey, requestSecret string) *Client`, `Configured() bool`, `BaseURL() string`, `GetState`, `GetAvailability`, `Resolve(ctx, phone string, channelID int64) (Resolved, error)`, `SendText`, `SendButtons`, `SendList`, `SendTemplate`, `Close`, `Escalate(ctx, id int64, esc Escalation, suggestedTeamID *int64) (EscalateResult, error)`, `EncodeEscalateBody(esc Escalation, suggestedTeamID *int64) ([]byte, error)`, `AttachmentURL(id string) string`; errores `ErrNotMine`, `ErrWindowClosed`, `ErrIdempotencyReused`, `ErrUnauthorized`, `ErrNotConfiguredAPI`
  - `type Escalation = bird.Escalation`
  - `type Bridge` con `NewBridge(c *Client, templateNames map[string]string) *Bridge` que cumple `bird.NectarBridge`.

- [ ] **Step 1: Portar los tests** con el módulo nuevo. Los que deben quedar y pasar: `TestVerifyNotice_*` (ventana, firma mala, secreto vacío), `TestSign_MatchesBirdScheme`, `TestIsStubHost`, `TestParseNotice_*` (sin event_id, sin conversación, kind desconocido pasa), `TestNoticeTime_UnreadableIsZeroAndLogged`, `TestTruncateForLog_OnePass` (un valor de 1 MB en `occurred_at` se trunca en < 100 ms), `TestToInboundMessage_*` (postback por `interactive_reply_id`, adjunto sin url ni id → `UnusableAttachmentType`, url compuesta desde `id`), `TestV1Return_*`, `TestClient_*` contra `httptest` (404 → `ErrNotMine`, 409 window_closed → `ErrWindowClosed`, 401 → `ErrUnauthorized`, cabeceras de firma presentes, `idempotency_key` UUID), `TestEscalateBody_*` (con `Protocol 0` el cuerpo no lleva `protocol` ni `reason_detail`; `reason_detail` con forma inválida no viaja; `rechazo` sin motivo no viaja), `TestNoticeVolcados_*` (los volcados reales parsean).

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/nectar/... -v` → FAIL (paquete vacío).

- [ ] **Step 3: Implementar** los cinco archivos portados. `bridge.go`: `SendTemplate` traduce `tmpl.ProjectID → TemplateNames[ProjectID]` y `tmpl.Locale` a `Language` (vacío → `"es"`), parámetros por `Key`.

- [ ] **Step 4: Run** `eval $GOTEST test ./internal/nectar/... -race` → PASS.

- [ ] **Step 5: Commit** `feat(nectar): frontera con Néctar — firma, avisos y cliente de las ocho rutas`

---

### Task 4: Configuración de Néctar, protocolo y equipo por grupo de CUPS

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/protocol.go` (portar de hdg-bot)
- Test: `internal/config/config_test.go`, `internal/config/nectar_config_test.go` (new)

**Interfaces:**
- Produces (campos de `Config`): `NectarBaseURL string` (sin barra final), `NectarServiceKey`, `NectarNoticeSecret`, `NectarRequestSecret string`, `NectarTemplateNames map[string]string`, `NectarTeamByGroup map[string]int64`, `ProtocolEscaladoVersion int`.
- `func (c *Config) ProtocolVersion() int`, `func (c *Config) EmitsProtocolV1() bool`, `const ProtocolEscaladoV0 = 0`, `ProtocolEscaladoV1 = 1`, `const ReceptionIsDual = true`, `func parseProtocolEscaladoVersion(raw string) (int, error)` (vacío → 0; otro valor que no sea 0/1 → `log.Fatalf`).
- `func parseKeyValueList(raw string) map[string]string` (`"a=b,c=d"`), `func parseNectarTeamByGroup(raw string) (map[string]int64, error)` (claves válidas SOLO `imagenes`, `neuro`, `callcenter`; id ≤ 0, par sin `:`, clave desconocida o repetida → error → `log.Fatalf` al arrancar).
- `func (c *Config) CupsGroup(cupsCode string) string` devuelve `"imagenes"` (881/882/883/871/879/870/873-878), `"neuro"` (291/930/892, 890274, 890374, 053105), `"callcenter"` (resto); `ResolveTeamForCups` se reescribe sobre `CupsGroup` (misma tabla de `TestResolveTeamForCups_Routing` como regresión).
- `func (c *Config) SuggestedTeamForCups(cupsCode string) *int64` (nil si el mapa está vacío o el grupo no tiene id).

- [ ] **Step 1: Tests**

```go
func TestCupsGroup(t *testing.T)            // tabla: "883100"→imagenes, "291100"→neuro, "890274"→neuro, "999999"→callcenter, "ab"→callcenter
func TestParseNectarTeamByGroup(t *testing.T) // "imagenes:7, neuro:8,callcenter:9" ok; "rx:1" → error; "neuro:0" → error; "" → mapa vacío
func TestSuggestedTeamForCups(t *testing.T)  // mapa {imagenes:7} → "883100" → 7; "890274" → nil
func TestParseKeyValueList(t *testing.T)     // "p1=recordatorio_cita, p2=lista_espera" → 2 entradas, espacios recortados
func TestParseProtocolEscaladoVersion(t *testing.T) // ""→0, "1"→1, "v1"→error, "2"→error
```

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/config/ -run 'TestCupsGroup|TestParseNectarTeamByGroup|TestSuggestedTeamForCups|TestParseKeyValueList|TestParseProtocolEscaladoVersion' -v` → FAIL.

- [ ] **Step 3: Implementar** en `Load()`: leer `NECTAR_BASE_URL` (TrimRight `/`), `NECTAR_SERVICE_KEY`, `NECTAR_NOTICE_SECRET`, `NECTAR_REQUEST_SECRET`, `NECTAR_TEMPLATE_NAMES`, `NECTAR_TEAM_BY_GROUP`, `PROTOCOL_ESCALADO_VERSION` (default 0). Ninguna entra en `validate()` como requerida.

- [ ] **Step 4: Run** `eval $GOTEST test ./internal/config/...` → PASS (incluida la tabla existente de `TestResolveTeamForCups_Routing`).

- [ ] **Step 5: Commit** `feat(config): variables de Néctar, versión del protocolo y equipo por grupo de CUPS`

---

### Task 5: Registro de capacidades

**Files:**
- Create: `internal/nectar/capabilities.go`, `internal/api/nectar_capabilities.go` (portar de hdg-bot)
- Test: `internal/nectar/capabilities_test.go`, `internal/api/nectar_capabilities_test.go`

**Interfaces:**
- Consumes: `config.ProtocolEscaladoV1`, `nectar.KnownKinds`, `nectar.VerifyNotice`, `reconstructFullURL` (ya existe en `webhook_handler.go`).
- Produces: `const ReasonBotCannotAnswer = "bot_cannot_answer"`, `ReasonPatientAskedForHuman = "patient_asked_for_human"`, `ResolveLockAliasKey = "candado_resolver"`; `func Capabilities(protocolVersion int) CapabilityRegistry`; `(CapabilityRegistry) Encode() ([]byte, string, error)`; `const NectarCapabilitiesPath = "/api/nectar/capabilities"`, `NectarCapabilitiesPattern = "GET " + NectarCapabilitiesPath`; `func (h *WebhookHandler) HandleNectarCapabilities(w, r)`.
- Diferencias con hdg-bot **[criterio del plan]**: `Plantillas: []string{}` (Neuro no tiene plantillas que un agente pueda abrir hacia el bot), `Input.Repeatable: false` siempre (sin formulario repetible, ver Tarea 7a), `inputKinds = {"texto","texto_largo"}` (los únicos que los pasos de Neuro declaran en 7a/9c), `inputExits` los tres.

- [ ] **Step 1: Tests**: `TestCapabilities_V0HidesEmissionOnlyFields` (con 0: `facts=false`, `reason_detail=false`, `input.tipos=[]`, `return.valores=true`, `resolve_lock=true`); `TestCapabilities_V1` (todo encendido salvo `repetible=false`); `TestCapabilities_NoticesMatchKnownKinds`; `TestCapabilities_EncodeVersionIsStable` (dos `Encode` iguales); `TestHandleNectarCapabilities_401WithoutSignature`, `_200WithETag`, `_304OnIfNoneMatch`.

- [ ] **Step 2: Run** → FAIL. **Step 3: Implementar.** **Step 4: Run** `eval $GOTEST test ./internal/nectar/ ./internal/api/ -run Capabilit -v` → PASS.

- [ ] **Step 5: Commit** `feat(nectar): registro de capacidades GET /api/nectar/capabilities`

---

### Task 6: Webhook `POST /api/webhooks/nectar` y replay del WAL

**Files:**
- Create: `internal/api/nectar_handler.go` (portar de hdg-bot SIN `templateOpener`: el cuarto aviso siempre responde `agent`)
- Modify: `internal/api/webhook_handler.go` (campos `nectarSink nectarDispatcher`, `nectarSeenIDs nectarSeen`; `SetNectarSink` para pruebas), `cmd/server/main.go` (ruta, replay de arranque, `inboxReplaySource.nectar`), `internal/observability/tracer.go` (catálogo: `protocolo/version_desconocida`, `apertura/aviso_invalido`, `apertura/solo_traza`, `apertura/replay_perdida`, `infra/nectar_notice_rejected` si se emite).
- Test: `internal/api/nectar_handler_test.go` (portar; quitar los casos de preapertura por plantilla; añadir `TestHandleNectarNotice_OpenedByAgentAlwaysAgent`).

**Interfaces:**
- Consumes: Tarea 3 (`nectar.VerifyNotice`, `ParseNotice`, `SessionKey`, `ToInboundMessage`, `MaskedForInbox`), `worker.AgentCommand` con `ReturnValue`/`ReturnV1`/`Reason` (Tarea 7c define los campos; para que esta tarea compile antes, añadirlos AQUÍ al struct `AgentCommand` como campos sin uso: `ReturnValue string; ReturnV1 *nectar.V1Return; Reason string` y el método `ChosenOption() string`).
- Produces: `const nectarInboxType = "nectar_notice"`; `func (h *WebhookHandler) HandleNectarNotice(w, r)`; `func (h *WebhookHandler) DispatchNectarNotice(ctx, rawBody string) error`; `func IsNectarInboxRow(msgType string) bool`; `type nectarDispatcher interface { Enqueue(bird.InboundMessage) bool; EnqueueAgentCommand(worker.AgentCommand) }`.
- Semántica (del contrato): firma → parseo → WAL `InsertIfNotExists(event_id, SessionKey, body, "nectar_notice")` → 200 → despacho. Duplicado → 200 sin despachar. Aviso inutilizable → 400. `protocol > ProtocolMax` → ERROR y 200. `mensaje_entrante` → `RecordInbound(key)` + `Enqueue`; `conversacion_resuelta` → `AgentCommand{Action:"close", Phone:key, Reason:n.Motivo}`; `conversacion_devuelta` → `AgentCommand{Action:"resume", Phone:key, ReturnValue, ReturnV1}`; `conversacion_abierta_por_agente` → WAL enmascarado, `MarkDone`, respuesta `{"respuesta_va_a":"agent"}`, traza `apertura/solo_traza`; en replay → `apertura/replay_perdida`. Las filas sin mensaje se cierran con `MarkDone` en el despacho.

- [ ] **Step 1: Tests** (nombres mínimos): `TestHandleNectarNotice_RejectsBadSignature` (401), `_DuplicateEventIsAcked` (dos POST iguales → 1 encolado), `_InboundMessageEnqueued`, `_ResolvedBecomesClose`, `_ReturnedBecomesResumeWithValue`, `_ReturnedV1`, `_UnknownKindAcked`, `_UnreadableNotice400`, `_OpenedByAgentAlwaysAgent` (cuerpo `{"respuesta_va_a":"agent"}`, WAL con `identifier` enmascarado), `TestDispatchNectarNotice_ReplayRoutesByKind`, `TestInboxReplay_NectarRowsBypassBirdParser` (en `cmd/server`: una fila `msg_type=nectar_notice` NO se parsea como `bird.WebhookEvent`).

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/api/ -run Nectar -v` → FAIL.

- [ ] **Step 3: Implementar** el handler, los campos del `WebhookHandler`, la ruta `mux.HandleFunc("POST /api/webhooks/nectar", webhookHandler.HandleNectarNotice)` y `mux.HandleFunc(api.NectarCapabilitiesPattern, webhookHandler.HandleNectarCapabilities)` en `main.go`; en los DOS replays (arranque y `inboxReplaySource.PendingOlderThan`) enrutar `IsNectarInboxRow(row.MsgType)` por `DispatchNectarNotice` (fila ilegible → `MarkDone`). El `webhookHandler` se construye ANTES del replay de arranque (hoy se construye después: moverlo).

- [ ] **Step 4: Run** `eval $GOTEST test ./internal/api/... ./internal/observability/... ./cmd/...` → PASS.

- [ ] **Step 5: Commit** `feat(api): webhook de avisos de Néctar con dedupe por event_id y replay del WAL`

---

### Task 7a: Menú de retornos, descriptor de entrada y rechazo tipado en `statemachine`

**Files:**
- Create: `internal/statemachine/return_menu.go`, `input_descriptor.go` (portar de hdg-bot SIN el formulario repetible: eliminar `ReturnMenuCitas`, `RegisterReturnCitas`, `returnCitasFor`, `ReturnCitasPrefix`, `InputRepeatable`, `InputUnscheduled`, `InputPiece`, `InputNote`, `InputRepeatRules`, `InputRepeatLimits`, `inputRepeatableFrom`, `CupsProgramable`, `ReturnMenuTopes`, `ReturnMenuSinAgenda`, y las constantes `InputRepeatKey`/`inputNote*`)
- Modify: `internal/statemachine/helpers.go` (`CtxAgentInputRejection`, `CtxAgentInputRejected`, `PersistedRejection`, `EncodeRejection`, `DecodeRejection`, `RejectedInputNote`), `internal/statemachine/states.go` (`CtxEscalationNextDay = "escalacion_proximo_dia"`, `CtxEscalationGate = "escalacion_desvio_gate"`, `CtxPatientAskedForAgent = "patient_asked_for_agent"`, `CtxPreAutoState = "_pre_auto_state"` si no existe con ese nombre: hoy el handler lee el literal `"_pre_auto_state"`).
- Test: `internal/statemachine/return_menu_test.go`, `input_descriptor_test.go` (portar, quitando los casos de citas/repetible).

**Interfaces:**
- Produces:
  - `type ReturnOption struct { Label, Value, Target, Data string; Close, NoInput bool }`; `const ReturnResume = "bot_reanudar"`, `ReturnClose = "bot_cerrar"`, `ReturnValueQueueExpired = "cola_vencida"`, `ReturnInputPrefix = "bot_dato:"`
  - `func RegisterReturnMenu(state string, build ReturnOptionsBuilder)`, `RegisterStaticReturnMenu(state string, options ...ReturnOption)`, `ReturnOptionsFor(sess, step) []ReturnOption`, `BuildReturnMenu(sess, step) ReturnMenu`, `ResolveReturnValue(sess, step, value string) (ReturnOption, bool)`, `RegisterResolveGuard(state, motivo string)`, `ResolveGuardFor(state) (string, bool)`, `ReturnValueForLog(token string) string`
  - `type ReturnInput struct { Kind string; Label, Help string; Target string; Required bool; MaxLen int; Pattern string; Parse ReturnValuesParser; Validate func(sess, value string) InputRejection; Command string }` — **`Command` es nuevo de Neuro [mi criterio]**: `"cups"` u `"orden"` hace que el worker entregue el dato escrito a `handleAgentCups`/`handleAgentOrder` en vez de inyectarlo como mensaje virtual (los dos comandos de hoy ya saben consumirlo).
  - `func RegisterReturnInput(state string, in ReturnInput)`, `type InputRejection`, `func Reject(reason string) InputRejection`, `type ReturnInputResult struct { Written bool; Value, Target, Problem, Field, Piece, Command string }` con `OK()`, `Rejected()`
  - `func (m *Machine) ReturnMenu(sess, step) ReturnMenu`, `(m *Machine) InputDescriptor(sess, step) InputDescriptor`, `(m *Machine) ResolveReturnInput(sess, step, value string) ReturnInputResult`, `(m *Machine) ResolveReturnV1(sess, step string, values json.RawMessage) ReturnInputResult`
  - `type InputDescriptor struct { Step string "paso"; Options []InputOption "opciones"; Fields []InputField "campos,omitempty"; MinFields int "minimo_campos,omitempty"; ResumesAt string "retoma_en,omitempty" }`; `InputOption{Label "rotulo", Value "valor", Exit "salida"}`; `InputField` con `clave, rotulo, tipo, formato?, requerido?, largo?, patron?, ayuda?`; `InputKindText = "texto"`, `InputKindLongText = "texto_largo"`.
  - `type PersistedRejection struct { Reason string; Attempt int; Echo, Field, Piece string }`; `EncodeRejection`/`DecodeRejection`; `RejectedInputNote(attempt int, received, reason string) string`.

- [ ] **Step 1: Tests portados** que deben pasar: `TestBuildReturnMenu_UniversalOptionsLast`, `TestBuildReturnMenu_DropsOptionsWithoutLabelOrValue`, `TestResolveReturnValue_UnknownTokenIsFalse`, `TestResolveReturnInput_PrefixedValueGoesToValidate`, `TestResolveReturnV1_ValuesParsedBySteps`, `TestInputDescriptor_ZeroFieldsOnlyOptions`, `TestInputDescriptor_ExitDerivedFromCloseAndNoInput`, `TestReturnValueForLog_NeverEchoesData`, `TestEncodeDecodeRejection_RoundTrip`; nuevo: `TestReturnInput_CommandSurvivesResolve` (un `ReturnInput{Command:"cups"}` devuelve `ReturnInputResult.Command == "cups"`).

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/statemachine/ -run 'ReturnMenu|ReturnValue|ReturnInput|InputDescriptor|Rejection' -v` → FAIL.

- [ ] **Step 3: Implementar.** `judge` (validación de lo escrito) usa la `HandlerConfig` registrada del paso (`m.configs[step]`) con el mismo validador que `Process` aplica al paciente; sin config declarativa, acepta y deja `Validate` del `ReturnInput` como única regla.

- [ ] **Step 4: Run** `eval $GOTEST test ./internal/statemachine/...` → PASS.

- [ ] **Step 5: Commit** `feat(statemachine): menú de retornos, descriptor de entrada y rechazo tipado del protocolo v1`

---

### Task 7b: Gancho de pre-escalación en la máquina

**Files:**
- Modify: `internal/statemachine/machine.go` (campo `preEscalationFlush PreEscalationFlush`, `SetPreEscalationFlush`, `CtxEscalationNoticeFlushed = "_escalation_notice_flushed"`, aplicación en el auto-encadenado justo antes de entrar a `StateEscalateToAgent`), `internal/worker/pool.go` (`PreEscalationFlush`).
- Test: `internal/statemachine/machine_test.go`, `internal/worker/pool_test.go` (o nuevo `pre_escalation_flush_test.go`).

**Interfaces:**
- Produces: `type PreEscalationFlush func(ctx, sess *session.Session, messages []OutboundMessage) bool`; `func (m *Machine) SetPreEscalationFlush(f PreEscalationFlush)`; `func (p *MessageWorkerPool) PreEscalationFlush(_ context.Context, sess *session.Session, messages []statemachine.OutboundMessage) bool` (devuelve `false` si `!session.IsNectarSession(sess.PhoneNumber)`; si es Néctar manda los mensajes coalescidos con `sendMessage` y devuelve `true`). `main.go` cablea `machine.SetPreEscalationFlush(workerPool.PreEscalationFlush)` tras construir el pool.
- Consumes: `session.IsNectarSession` (Tarea 8 lo define; para compilar antes, definirlo en ESTA tarea en `internal/session/types.go`: `func IsNectarSession(phone string) bool { return strings.HasPrefix(phone, "nectar:") }`).

- [ ] **Step 1: Tests**: `TestPreEscalationFlush_NectarSendsBeforeEscalate` (una cadena `X → ESCALATE_TO_AGENT` con un texto acumulado: el doble de envío recibe el texto ANTES de que corra el handler de escalación, `sess.GetContext(CtxEscalationNoticeFlushed) == "1"`, y el resultado final NO repite el texto); `TestPreEscalationFlush_BirdUnchanged` (con teléfono E.164 el gancho devuelve `false` y los mensajes salen al final, como hoy).

- [ ] **Step 2: Run** → FAIL. **Step 3: Implementar** (en `machine.go`, en el bucle de auto-encadenado: `if result.NextState == StateEscalateToAgent && m.preEscalationFlush != nil && len(prevMessages) > 0 { if m.preEscalationFlush(ctx, sess, prevMessages) { prevMessages = nil; sess.SetContext(CtxEscalationNoticeFlushed, "1") } }`; la marca se borra en memoria al terminar `Process`). **Step 4: Run** → PASS.

- [ ] **Step 5: Commit** `feat(statemachine): aviso al paciente antes del escalate en sesiones de Néctar`

---

### Task 7c: Retorno del agente, cola vencida y guardas `nectar:*` en el worker

**Files:**
- Modify: `internal/worker/agent_command.go` (campos definidos en 6 + doc), `internal/worker/pool.go` (`resolveAgentReturn`, rama de retorno en `handleAgentResume`, `reescalateRejectedAgentInput`, `clearNextDayQueueMark`, guardas `IsNectarSession` en los textos «Hemos retomado…», cierre SÍNCRONO en `handleAgentClose` para Néctar, `handleAgentNoShow` ignora sesiones Néctar), `internal/statemachine/machine.go` (interfaz `MessageProcessor` del pool gana `ResolveReturnInput`, `ResolveReturnV1`, `ReturnMenu`).
- Create: `internal/worker/queue_expired.go` (portar; `SetBusinessHoursText(func() string)`; el texto del horario lo da `handlers.BusinessHoursDescription()` de la Tarea 9b —hasta entonces, `main.go` cablea un literal `"Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m."`).
- Modify: `internal/observability/tracer.go` (catálogo: `escalacion/agent_input_rejected`, `escalacion/cola_vencida`, `escalacion/agent_resumed` ya existe).
- Test: `internal/worker/agent_return_test.go` (new), `internal/worker/queue_expired_test.go` (portar).

**Interfaces:**
- Consumes: 7a (`ResolveReturnValue`, `ReturnInputResult`, `EncodeRejection`, `RejectedInputNote`, `ReturnValueQueueExpired`, `CtxAgentInputRejected`, `CtxAgentInputRejection`, `CtxPreAutoState`, `CtxEscalationNextDay`), Tarea 2 (`CloseFeedItems(phone, conv)`), `nectar.ErrWindowClosed`.
- Produces: `func (c AgentCommand) ChosenOption() string`; `func (p *MessageWorkerPool) resolveAgentReturn(sess, step string, cmd AgentCommand) (statemachine.ReturnInputResult, string)`; `func (p *MessageWorkerPool) reescalateRejectedAgentInput(ctx, sess, cmd, step string, res statemachine.ReturnInputResult)`; `func (p *MessageWorkerPool) handleQueueExpired(ctx, sess)`; `func (p *MessageWorkerPool) clearNextDayQueueMark(ctx, sess)`; `func (p *MessageWorkerPool) SetBusinessHoursText(func() string)`.
- Reglas en `handleAgentResume` (orden): (1) `cmd.ChosenOption() == ReturnValueQueueExpired` → `handleQueueExpired`; (2) caso `NOTIF_PENDING` de hoy; (3) si `cmd.ReturnV1 != nil || cmd.ReturnValue != ""`: `step = pre_escalation_state`; `escrito, token := resolveAgentReturn`; `opt, ok := ResolveReturnValue(sess, step, token)`; `switch`: `escrito.OK()` → si `escrito.Command != ""` dispatch a `handleAgentCups`/`handleAgentOrder` con `cmd.Data = escrito.Value` y `return`; si no `cmd.Data = escrito.Value`, `cmd.State = escrito.Target` si no vacío; `escrito.Rejected()` → `reescalateRejectedAgentInput` y `return`; `!ok` → `slog.Warn` con `ReturnValueForLog(token)` y reanudar sin decisión; `opt.Close` → `handleAgentClose`; default → `cmd.State = opt.Target`, `cmd.Data = opt.Data`. (4) Resto igual que hoy, pero los textos «Hemos retomado tu atención…» SOLO para sesiones que no son Néctar.
- `handleAgentClose`: para Néctar el `CloseFeedItems` es síncrono (sin los 3 s de espera) y antes de `Complete` NO se manda «Tu consulta ha sido resuelta» si `cmd.Reason != ""` (resolución desde Néctar: el agente ya cerró con el paciente).
- `handleAgentNoShow`: si `IsNectarSession` → `slog.Info` y `return` (el no-show no existe para Néctar).

- [ ] **Step 1: Tests**: `TestHandleAgentResume_OptionTargetAndData`, `TestHandleAgentResume_UnknownTokenResumesWithoutDecision` (sin inyección, el doble de envío no recibe el token), `TestHandleAgentResume_WrittenValueInjected`, `TestHandleAgentResume_WrittenCupsGoesToHandleAgentCups`, `TestHandleAgentResume_RejectedReescalates` (sesión vuelve a `ESCALATE_TO_AGENT`, `CtxAgentInputRejected == "1"`, `escalation_note` con `RejectedInputNote`, el paciente NO recibe nada), `TestHandleAgentResume_QueueExpired` (texto `queueExpiredText`, `Complete`, `Close` síncrono, evento `escalacion/cola_vencida`), `TestHandleAgentResume_NectarNoInterstitialText`, `TestHandleAgentClose_NectarResolvedSkipsFarewell`, `TestHandleAgentNoShow_IgnoresNectar`, `TestNotifyQueueExpired_WindowClosedUsesTemplate`.

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/worker/ -run 'AgentResume|AgentClose|NoShow|QueueExpired' -v` → FAIL. **Step 3: Implementar.** **Step 4: Run** `eval $GOTEST test ./internal/worker/... -race` → PASS.

- [ ] **Step 5: Commit** `feat(worker): retorno del agente v1, cola vencida y guardas para sesiones de Néctar`

---

### Task 8: Sesiones de Néctar en el checker de escalaciones

**Files:**
- Modify: `internal/session/types.go` (`IsNectarSession` ya definido en 7b; `EscalatedSession.QueuedForNextDay bool`; `const EscalationQueuedForNextDayKey = "escalacion_proximo_dia"`; `func escalationExempt(s EscalatedSession, now time.Time, nextOpen func(time.Time) time.Time) bool`), `internal/session/manager.go` (`InactivityDeps.NextOpen func(time.Time) time.Time`; `InactivityBirdClient` NO cambia más allá de la Tarea 1 —el tope de la encolada sin atender de hdg-bot no se porta: el dueño aceptó que los casos en cola dentro de horario no vencen—; en `checkEscalatedSessions`: (a) sesión exenta por cola → `continue`; (b) `IsNectarSession` → solo cierre por silencio del paciente con `EscalationCloseMin`, `continue`; (c) resto como hoy), `closeEscalatedBySilence` extraído de la rama (1) actual para reusarlo; `internal/repository/local/session_repo.go` (`FindEscalatedSessions` lee `QueuedForNextDay` del `session_context` por la clave).
- Test: `internal/session/manager_test.go` (nuevos casos), `internal/session/types_test.go`.

**Interfaces:**
- Produces: lo de arriba. `escalationExempt`: `QueuedForNextDay && (nextOpen == nil || EscalatedAt.IsZero() || now.Before(nextOpen(EscalatedAt)))`.
- Consumes: `bird.ErrNoConversationState` (Bird), `nectar.ErrNotMine` vía `CloseFeedItems` (ya absorbido en la Tarea 2).

- [ ] **Step 1: Tests**: `TestCheckEscalated_NectarSkipsNoShowAndReminders` (sesión `nectar:7` sin respuesta del agente a los 400 min: NO se llama `Resumer`, NO se manda recordatorio), `TestCheckEscalated_NectarClosesByPatientSilence`, `TestCloseEscalatedBySilence_NectarNotMineIsSuccess` (el doble de `CloseFeedItems` devuelve nil tras un 404 interno: la sesión queda `abandoned`, `escalations.Expire` llamado), `TestEscalationExempt_QueuedUntilNextOpen`, `TestFindEscalatedSessions_ReadsQueuedMark` (integración, build tag `integration`).

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/session/ -run 'CheckEscalated|CloseEscalated|EscalationExempt' -v` → FAIL. **Step 3: Implementar.** **Step 4: Run** → PASS.

- [ ] **Step 5: Commit** `feat(session): escalaciones de Néctar sin no-show ni recordatorios; exención de la cola`

---

### Task 9a: Resumen del agente en dos formas (`summary_groups`)

El texto que hoy produce `buildAgentSummary(sess, cupsCode, teamName)` queda fijado por un dorado ANTES del refactor, y el refactor lo reproduce byte a byte además de producir `facts`.

**Files:**
- Create: `internal/statemachine/handlers/summary_groups.go` (portar íntegro de hdg-bot)
- Modify: `internal/statemachine/handlers/escalation.go` (`buildAgentSummary` → render de `summaryForAgent`)
- Test: `internal/statemachine/handlers/summary_golden_test.go` (new, con `testdata/summary_*.golden`), `summary_groups_test.go` (portar)

**Interfaces:**
- Produces: `func summaryForAgent(sess *session.Session, cupsCode, teamName string) (string, []*summaryGroup)`; `func buildAgentSummary(sess, cupsCode, teamName string) string` (misma firma que hoy). Grupos de Neuro, en este orden y con estas claves: `encabezado` (sin título: la línea «Transferencia…» de hoy), `paciente` (nombre, documento, teléfono enmascarado NO —el teléfono NO entra en `facts`—, edad, género), `entidad` (entidad, contrato, régimen), `procedimiento` (CUPS, nombre, grupo/servicio, cantidad, contraste/sedación cuando aplique), `estado` (paso, motivo interno `escalation_reason`, equipo), `nota` (sin título: `escalation_note` cuando exista), `pie` (sin título). Las líneas exactas salen de la implementación actual de `buildAgentSummary`.

- [ ] **Step 1: Dorados ANTES de tocar nada**: escribir `TestSummaryGolden_*` con tres sesiones fijas (paciente identificado con CUPS de imagen; paciente sin identificar escalado desde `MAIN_MENU`; sesión con `escalation_note`) que comparan `buildAgentSummary` contra `testdata/*.golden`; generar los dorados con `-update` desde el código ACTUAL y commitearlos.

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/statemachine/handlers/ -run SummaryGolden -v` → PASS (línea base).

- [ ] **Step 3: Tests del refactor**: `TestSummaryForAgent_FactsMatchText` (cada campo de `facts` aparece como línea del texto y viceversa, regla del campo vacío de hdg-bot), `TestSummaryForAgent_NoPhoneInFacts`.

- [ ] **Step 4: Implementar** `summary_groups.go` y `summaryForAgent`; `buildAgentSummary` pasa a ser `text, _ := summaryForAgent(...)`.

- [ ] **Step 5: Run** `eval $GOTEST test ./internal/statemachine/handlers/ -run 'Summary' -v` → PASS con los dorados INTACTOS (si un dorado cambia, el refactor está mal).

- [ ] **Step 6: Commit** `refactor(handlers): resumen del agente como grupos (texto + facts de una sola construcción)`

---

### Task 9b: Handler de escalación: sobre v1, equipo por grupo, cola y aviso previo

**Files:**
- Modify: `internal/statemachine/handlers/escalation.go`, `internal/statemachine/handlers/greeting.go` (`nextBusinessDayQueue`, `BusinessHoursDescription`, `nextBusinessOpen`), `internal/config/config.go` (`BusinessHoursCloseMarginMin int` ← `BUSINESS_HOURS_CLOSE_MARGIN_MIN` default 15; `BusinessHoursQueueMaxWaitMin int` ← `BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN` default 1200), `internal/statemachine/interceptors.go` y `handlers/post_action.go` (marcan `CtxPatientAskedForAgent = "1"` donde el PACIENTE pide agente: palabra clave y opción de menú), `internal/observability/tracer.go` (catálogo: `escalacion/equipo_sugerido`, `escalacion/encolada_proximo_dia`).
- Test: `internal/statemachine/handlers/escalation_test.go` (adaptar los 3 archivos existentes a la firma nueva), `escalation_nectar_test.go` (new), `escalation_next_day_test.go` (portar lo aplicable), `greeting_test.go` (`nextBusinessOpen`).

**Interfaces:**
- Consumes: 2, 4, 5, 7a, 9a.
- Produces (en `handlers`):
  - `func escalationEnvelope(cfg *config.Config, m *sm.Machine, sess *session.Session, step, reason, summary string, facts []*summaryGroup, resolveLock string) bird.Escalation` (portado; `SuggestedTeamID: cfg.SuggestedTeamForCups(sess.GetContext("cups_code"))`).
  - `func emitSuggestedTeam(sess, team *int64)`.
  - `type escalationQueue struct { window, queue bool; reason string }`; `func nextBusinessDayQueue(cfg *config.Config) escalationQueue` sobre el horario de Neuro (L-V 07:00-17:59, S 07:00-11:59; `TestingAlwaysOpen` lo apaga): `window` = cerrado o faltan ≤ `BusinessHoursCloseMarginMin` para cerrar; `queue` = `window && nextOpen - now ≤ BusinessHoursQueueMaxWaitMin`; `reason` ∈ {`"margen"`, `"fuera_de_horario"`}.
  - `func nextBusinessOpen(now time.Time) time.Time`; `func BusinessHoursDescription() string` (`"Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m."`).
  - `func escalationNoticeText(nextDay bool) string` (`sm.EscalationNoticeText` o «Tu solicitud quedó registrada ✅. Un asesor te atenderá el próximo día hábil y te escribirá por aquí. Te atendemos <horario>.»).
  - `func escalationExit(gate string, r *sm.StateResult) *sm.StateResult` (borra `CtxAgentInputRejection`; escribe/borra `CtxEscalationGate`).
  - `const gateOutOfHours = "out_of_hours"`, `queueReasonFarOpen = "apertura_lejana"`.
- Comportamiento del handler (sustituye al de hoy, pasos numerados como en hdg-bot):
  1. `teamID := cfg.ResolveTeamForCups(cupsCode)` (Bird) — se mantiene.
  2. Guardas anti-bucle de hoy, con el tope exento mientras `sess.GetContext(CtxEscalationNextDay) == "1"` y la espera siga abierta (`queuedWaitStillOpen`).
  3. Recuperación IA de hoy, NO cuando `CtxAgentInputRejected != ""`.
  4. Gate: `!birdClient.HasAvailableAgentsFor(msg.Phone, sess.ConversationID)` → `no_agents`; `!businessHoursOpen(cfg)` → `out_of_hours`; `queue.farOpen()` → `out_of_hours` con motivo `apertura_lejana`. **Si `gate != ""` y NO `queue.queue`** → desvío de hoy al menú con `BusinessHoursDescription()` (`escalation_no_agents`). Si `queue.queue` → se escala igual (cola de Néctar).
  5. Resolución del `conversationID` de hoy (solo corre para Bird: en Néctar `sess.ConversationID` siempre viene).
  6. Aviso al paciente ANTES del `escalate`, uno solo, SOLO en sesiones de Néctar (en Bird el aviso sigue viajando en el `StateResult` del caller como hoy, sin cambios): `notice := escalationNoticeText(queue.queue)`; si `CtxEscalationNoticeFlushed == "1" && !queue.queue` → `""` (el caller ya avisó por el gancho de 7b); si `!askedForAgent && !queue.queue` → no se avisa (el bot se atascó y el flujo continúa para el paciente cuando el agente devuelva el dato); si queda texto → `SendText` ahora, mientras la conversación aún es del bot.
  7. `summary, facts := summaryForAgent(sess, cupsCode, teamName)`; `reason := nectar.ReasonBotCannotAnswer`, `ReasonPatientAskedForHuman` si `CtxPatientAskedForAgent == "1"`; `resolveLock, _ := sm.ResolveGuardFor(preState)`; `envelope := escalationEnvelope(...)`; `EscalateToAgentWithSummary(ctx, conversationID, msg.Phone, teamID, teamName, sess.PatientName, cfg.BirdTeamFallback, envelope)` con los reintentos de hoy.
  8. En Bird (no Néctar): `SendInternalText(msg.Phone, conversationID, buildAgentCommands(sess, cupsCode))` como hoy. En Néctar no.
  9. Sellado de la sesión como hoy + `emitSuggestedTeam` + si `queue.queue`: `WithContext(CtxEscalationNextDay, "1")`, evento `encolada_proximo_dia`, `WithExpiryUntil(nextBusinessOpen(now)+8h)` (si `StateResult` no tiene `WithExpiryUntil`, añadirlo en `statemachine/result.go` y aplicarlo en `sendAndSave`); borrar `CtxPatientAskedForAgent` y `escalation_note` en la salida que escala; todas las salidas pasan por `escalationExit`.

- [ ] **Step 1: Tests nuevos**: `TestEscalate_NectarEnvelopeV1` (con `PROTOCOL_ESCALADO_VERSION=1` el puente doble recibe `Escalation{Protocol:1, Reason:"patient_asked_for_human" cuando la marca está, Facts no nil, Input no nil, ReturnMenu nil, SuggestedTeamID 7 para CUPS 883100 con `NECTAR_TEAM_BY_GROUP=imagenes:7`}`), `TestEscalate_V0EnvelopeKeepsReturnMenu` (`Protocol 0`: `Input nil`, `ReturnMenu` no nil, `Facts nil`), `TestEscalate_NectarNoticeSentBeforeEscalate` (orden de llamadas en el doble: `SendText` antes de `Escalate`), `TestEscalate_NectarNoAgentCommandsNote` (`SendInternalText` no se llama), `TestEscalate_OutOfHoursQueuesWhenOpenIsNear` (viernes 17:50 → escala, `CtxEscalationNextDay == "1"`, texto del próximo día hábil, evento `encolada_proximo_dia` motivo `margen`), `TestEscalate_OutOfHoursFarOpenDiverts` (sábado 13:00 con `BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN=1200` → desvío al menú con `apertura_lejana`), `TestEscalate_BirdPathUnchanged` (sesión E.164: nota interna + comandos como hoy), `TestNextBusinessOpen` (tabla: viernes 18:30 → lunes 07:00; sábado 12:30 → lunes 07:00; martes 10:00 → martes 10:00 si abierto), `TestEscalationSuppressedWhenPatientDidNotAskAndNotQueued` (sin marca ni cola: el paciente no recibe aviso en Néctar).

- [ ] **Step 2: Run** `eval $GOTEST test ./internal/statemachine/handlers/ -run 'Escalat|NextBusinessOpen' -v` → FAIL. **Step 3: Implementar.** **Step 4: Run** `eval $GOTEST test ./internal/statemachine/... ./internal/config/... ./internal/observability/...` → PASS (los dorados de 9a intactos).

- [ ] **Step 5: Commit** `feat(handlers): escalación con sobre v1, equipo por grupo de CUPS y cola del próximo día hábil`

---

### Task 9c: Menús de retorno y entradas de los pasos de Neuro

Cada `/bot resume ESTADO dato` que hoy ofrece `buildAgentCommands` se declara como opción o entrada de retorno, para que el agente de Néctar tenga los mismos botones.

**Files:**
- Modify: en el `Register*Handlers` de cada handler: `handlers/greeting.go` (`MAIN_MENU`: opciones estáticas «Agendar cita»→`Target: ASK_CLIENT_TYPE`, «Citas programadas»→`Target: ASK_DOCUMENT_TYPE`), `handlers/identification.go` (`ASK_DOCUMENT`: `ReturnInput{Kind: texto, Label: "Número de documento", Validate: validador de documento de hoy}`; `ASK_DOCUMENT_TYPE`: opciones por tipo), `handlers/medical_order.go` (`UPLOAD_MEDICAL_ORDER`, `VALIDATE_OCR`, `OCR_FAILED`, `ASK_MANUAL_CUPS`: `ReturnInput{Kind: texto, Label: "CUPS leídos de la orden (código[:cantidad], separados por coma)", Command: "cups", Validate: misma gramática que handleAgentCups}` y una opción «Orden descrita por el asesor» con `Command: "orden"`), `handlers/appointments.go` / notificaciones (`NOTIF_PENDING`: opciones estáticas `confirm`/`reschedule`/`cancel` con `Target: NOTIF_PENDING, Data: <acción>`), `handlers/slots.go` (`COVERAGE_NO_CONVENIO`: opciones «Continuar como particular» y «Volver al menú»).
- Test: `internal/statemachine/handlers/return_menus_test.go` (new): `TestReturnMenuValuesExistInTheStateMachine` (cada `Target` es un estado registrado), `TestReturnMenusCoverAgentCommands` (cada estado nombrado en `buildAgentCommands` tiene menú o entrada), `TestElRegistroDelDescriptorNoMiente` (los `tipos` y `salidas` que emiten los pasos ⊆ los que publica `nectar.Capabilities(1)`, y viceversa).

- [ ] **Step 1: Tests** → **Step 2: Run** → FAIL. **Step 3: Implementar** los registros. **Step 4: Run** `eval $GOTEST test ./internal/statemachine/... ./internal/nectar/...` → PASS.

- [ ] **Step 5: Commit** `feat(handlers): menús de retorno y entradas de los pasos escalables de Neuro`

---

### Task 10: Escalaciones de notificaciones por la firma nueva

Las escalaciones del `NotificationManager` siguen siendo de Bird hasta el Plan B, pero pasan por `EscalateToAgentWithSummary` para que el resumen viaje en el sobre cuando la sesión sea de Néctar.

**Files:**
- Modify: `internal/notifications/manager.go` (`escalateNotifToAgent`), `internal/notifications/confirmation.go` (`escalateToAgent`)
- Test: `internal/notifications/escalate_notif_existing_session_test.go`, `manager_test.go` (adaptar dobles).

**Interfaces:**
- Consumes: Tarea 2. En ambos sitios: `esc := bird.Escalation{Reason: nectar.ReasonBotCannotAnswer, Summary: note, ReasonDetail: "notificacion_sin_respuesta" | "notificacion_fallo_sistema" | "confirmacion_texto_libre"}`; si la sesión creada es de Néctar (`IsNectarSession(p.Phone)`), `Input` se arma SIN la máquina (el `NotificationManager` no la tiene) con `statemachine.StaticInputDescriptor(step string, options []InputOption) []byte` **[mi criterio]**, que serializa `InputDescriptor{Step: step, Options: options}` con las dos opciones universales al final, igual que `BuildReturnMenu`; para `NOTIF_PENDING` las opciones son las tres de la Tarea 9c.
- La nota interna de comandos `/bot resume NOTIF_PENDING …` se manda solo cuando no es Néctar.

- [ ] **Step 1: Tests**: `TestEscalateNotifToAgent_UsesSummaryEnvelope` (el doble recibe `Escalation.Summary == note`), `TestConfirmationEscalate_UsesSummaryEnvelope`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implementar.** **Step 4: Run** `eval $GOTEST test ./internal/notifications/...` → PASS.
- [ ] **Step 5: Commit** `refactor(notifications): escalaciones por EscalateToAgentWithSummary`

---

### Task 11: Cableado, adjuntos, salud, `.env.example` y documentación

**Files:**
- Modify: `cmd/server/main.go` (cliente y puente de Néctar; `ocrSvc.SetNectarCredentials`; `machine.SetPreEscalationFlush`; `workerPool.SetBusinessHoursText(handlers.BusinessHoursDescription)`; `/health` publica `nectar_host` (solo host, de `nectar.HostOf`); `/health/debug` publica `nectar_enabled` y `protocol_escalado_version`), `internal/services/ocr_service.go` (`SetNectarCredentials(baseURL, serviceKey, requestSecret string)`; en `downloadMedia` el caso del anfitrión de Néctar con `X-Bot-Service-Key` + `nectar.SignRequest`, ANTES del caso Bird), `.env.example` (bloque `NECTAR_*`, `PROTOCOL_ESCALADO_VERSION`, `BUSINESS_HOURS_CLOSE_MARGIN_MIN`, `BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN`, `NECTAR_TEAM_BY_GROUP`, `NECTAR_TEMPLATE_NAMES`), `README.md` (§Arquitectura: Néctar), `docs/FLUJOS.md` (A8: webhook de Néctar; E: retorno por menú), `docs/TROUBLESHOOTING-PROD.md` (§18 Néctar: 401 de firma, `nectar_host`, puente apagado por claves faltantes), `docs/GUIA-AUDITORIA.md` (endpoints nuevos y eventos `escalacion/*` nuevos), `CLAUDE.md` (sección corta «Néctar»).
- Test: `internal/services/ocr_service_test.go` (`TestDownloadMedia_NectarHostSignsRequest`, `_BirdHostUnchanged`, `_OtherHostNoCredential`), `cmd/server/main_test.go` o `internal/config`: `TestNectarWiring_PartialConfigDisablesBridge` (función pura `nectarBridgeFromConfig(cfg) (*nectar.Client, bird.NectarBridge, error)` extraída de `main` para poder probarla).

- [ ] **Step 1: Tests** → **Step 2: Run** → FAIL. **Step 3: Implementar** (`nectarBridgeFromConfig`: `NectarBaseURL == ""` → nil,nil,nil; configurado a medias → `nil, nil, err` y `main` hace `slog.Error` y sigue como Bird; completo → `NewClient` + `NewBridge(client, cfg.NectarTemplateNames)`). **Step 4: Run** `eval $GOTEST build ./... && eval $GOTEST test ./... -race` → PASS; `make lint` limpio.

- [ ] **Step 5: Commit** `feat: cableado de Néctar en el arranque, adjuntos firmados, salud y documentación`

---

### Task 12: Stub de Néctar, sonda de avisos y prueba de humo local

**Files:**
- Create: `cmd/nectar-stub/main.go`, `cmd/probe-notice/main.go` (portar de hdg-bot; ambos se NIEGAN a hablar con un host que no sea stub: `nectar.IsStubHost`), `docs/PRUEBA-LOCAL-NECTAR.md` (receta), `.claude/skills/e2e-bot/SKILL.md` (sección «contra el stub de Néctar»).
- Test: `cmd/nectar-stub/main_test.go` (`TestStub_RefusesNonStubBind` si aplica), y la receta manual de abajo.

- [ ] **Step 1: Portar y compilar**: `eval $GOTEST build ./cmd/nectar-stub ./cmd/probe-notice`.

- [ ] **Step 2: Humo local** (verificación manual, registrar salida en el PR):
  1. `docker compose up -d db` + `siesa_db` + bot con `NECTAR_BASE_URL=http://host.docker.internal:9999`, `NECTAR_SERVICE_KEY=stub`, `NECTAR_REQUEST_SECRET=stub`, `NECTAR_NOTICE_SECRET=stub`, `PROTOCOL_ESCALADO_VERSION=1`.
  2. Stub: `go run ./cmd/nectar-stub -addr :9999 -service-key stub -request-secret stub -scheme http` (en contenedor con `-p 9999:9999`).
  3. `go run ./cmd/probe-notice -url http://localhost:8090/api/webhooks/nectar -secret stub -kind mensaje_entrante -conversation 1 -text hola` → `200`; `GET /api/internal/sessions?phone=nectar:1` muestra `MAIN_MENU`; el stub registra un `POST /messages` tipo `list`.
  4. `probe-notice ... -text asesor` → el stub recibe `escalate` con `protocol:1`, `reason: patient_asked_for_human`, `facts`, `input`; la sesión queda `escalated`.
  5. `probe-notice -kind conversacion_devuelta -conversation 1 -retorno '{"opcion":"bot_reanudar"}'` → la sesión vuelve a `MAIN_MENU` y el stub recibe el menú.
  6. `probe-notice -kind conversacion_resuelta -conversation 1` → sesión `completed`.
  7. `GET /api/nectar/capabilities` firmado con `stub` → `200` con `protocol:1`; sin firma → `401`.
  8. Repetir el paso 3 con el MISMO `event_id` → `200` «duplicado», sin segundo `POST /messages`.

- [ ] **Step 3: Commit** `test(nectar): stub, sonda de avisos y receta de humo local`

---

### Task 13: Dashboard — agentes «ver en Néctar»

**Files (repo `C:\laragon\www\CHATBOT\neuro-dashboard`):**
- Modify: `internal/reports/reports.go` (`AgentReports` devuelve además `agentsSource string`: `"bird"` | `"nectar"`; `Report.AgentsSource`), `internal/reports/handler.go`, `frontend/src/pages/Reports.tsx` (si `agents_source === "nectar"`: encabezado «Agentes: estado en vivo y atención en Néctar», sin columnas Bird; llamadas de voz intactas), `frontend/src/pages/Tickets.tsx` (`bird_wa` → etiqueta «WhatsApp», `bird_voz` → «Voz (Bird)»).
- Modify (repo del bot): `internal/api/internal_handler.go` (`HandleBirdAgents`: si `h.cfg.NectarBaseURL != ""` → `409 {"reason":"agents_in_nectar"}`).
- Test: bot `internal/api/internal_handler_test.go` (`TestHandleBirdAgents_409WhenNectar`); dashboard `internal/reports/reports_test.go` (`TestAgentReports_NectarSource`).

- [ ] **Step 1: Tests** → **Step 2: Run** (dashboard: su propio `make test`) → FAIL. **Step 3: Implementar.** **Step 4: Run** → PASS; `npm run build` del frontend.
- [ ] **Step 5: Commit** en cada repo: `feat(reports): agentes en Néctar — el informe lo dice en vez de dejar columnas en blanco` / `feat(api): agentes de Bird responden 409 cuando el puente de Néctar está activo`

---

## Cierre del plan

- [ ] `eval $GOTEST test ./... -race -cover` verde; `make lint` limpio; `./scripts/pre-deploy-check.sh` sin FAIL.
- [ ] Prueba de humo de la Tarea 12 registrada.
- [ ] PR de `feat/migracion-nectar` contra `main` con el resumen por tarea y la lista de lo que queda para el Plan B (D1, D2, lista blanca, D3, horario, D5).
- [ ] Avisar a los chats `nectar-cd` y hdg-bot: el bot ya publica `GET /api/nectar/capabilities` y acepta los cuatro avisos; pedir a `nectar-cd` la fecha de la ventana de D1/D2 para abrir el Plan B.
