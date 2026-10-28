# Migración a Néctar — Plan A.2: herramientas del agente en cada escalación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que el agente de Néctar tenga, en cada uno de los 26 escenarios de escalación de Neuro, el motivo, los datos del caso y una forma de devolver la conversación que el bot sepa ejecutar sin bucles ni pacientes tirados.

**Architecture:** Todo vive en la emisión de Neuro dentro del contrato v1 vigente (sin cambio de esquema en Néctar): (1) los dos P0 (estado fantasma `BOT_DISABLED` y escalaciones de notificación sin sesión); (2) lo transversal: `escalation_reason` escrito en cada sitio y nuevos grupos de `facts`; (3) la infraestructura del menú (tipos `fecha`/`numero`/`lista`, opciones que limpian contexto o llegan como postback, reinicio universal, ocultar «devolver» donde buclea); (4) los registros por paso (botones, texto, listas de sesión); (5) las trampas de pasos automáticos, el candado, el sobre de notificaciones, la fuente de disponibilidad y el pulido P2.

**Tech Stack:** Go 1.26 (sin Go en el host: todo en contenedor `golang:1.26-alpine`), MySQL local sin migraciones, Néctar protocolo de escalado v1.

**Spec:** `docs/superpowers/specs/2026-10-27-migracion-nectar-design.md` + las dos auditorías que este plan implementa: `.superpowers/sdd/2026-10-27-migracion-nectar-fase-a/audit-escalation-scenarios.md` (escenarios S01-S26, brechas P0/P1/P2) y `audit-nectar-escalation-side.md` (lo que la consola de Néctar dibuja y acepta). Decisiones del dueño del 2026-10-28 al final de `progress.md`.

## Global Constraints

- Rama `feat/migracion-nectar` (Fase A completa, sin merge ni push). Módulo `github.com/neuro-bot/neuro-bot`; identificadores en inglés, comentarios y prosa en español.
- **Bird byte a byte para el paciente**: con un teléfono E.164 ningún texto, botón ni transición que el paciente recibe cambia; los comandos `/bot …` de los agentes de Bird siguen funcionando igual (solo se AÑADEN acciones nuevas que antes no hacían nada, p. ej. `wl_schedule`). Excepciones declaradas, y SOLO estas: (a) un retorno a un estado SIN handler (hoy rompe la sesión con «ocurrió un error») cae a `GREETING` en los dos canales (Tarea 1); (b) **aprobadas por el dueño (2026-10-28)**, dos bugs que el paciente ve se arreglan también en Bird: P1-12 (al agotar reintentos en la pregunta de la orden PARTICULAR/consolidación/EMG el paciente recibe el aviso de escalación y no otra vez la pregunta — Tarea 14) y C12 (`__resume__` ya no se guarda como dato en REG_ADDRESS, REG_OCCUPATION, REG_BARRIO y MEDICATION_ASK_* — Tarea 8); los tests de esas dos tareas cubren Bird y Néctar. Todo lo demás que el paciente ve distinto queda condicionado a `session.IsNectarSession`.
- **Sin migraciones de base de datos**: todo dato nuevo viaja en `session_context` (clave-valor) o en el sobre; ninguna tarea toca el esquema.
- Toda emisión nueva de `observability.Emit(flow, step)` se registra en el `catalog` de `internal/observability/tracer.go` (`TestCatalog_AllEmittedStepsRegistered` lo cruza). Nuevas en este plan: `escalacion/retorno_estado_inexistente` (T1), `escalacion/siguiente_procedimiento` (T10), `escalacion/escalation_disabled` (T16).
- Dorados del resumen (`internal/statemachine/handlers/testdata/summary_*.golden`): los TRES existentes no cambian en ninguna tarea. Solo la Tarea 4 CREA uno nuevo (`summary_caso_completo.golden`) con `-update -run 'TestSummaryGolden/caso_completo'`; cualquier otra diferencia de dorado es un fallo.
- Contrato con Néctar: todo es emisión dentro de v1 (`lista`, `fecha`, `numero`, `formato:"iso"`, `opciones` de campo, varias salidas, `resolve_lock` opaco ≤64 runas) — **ningún [N] bloqueante**. `[N-info]`: los literales de candado `retorno_pendiente` y `cita_pendiente` son provisionales (constantes en un solo archivo) hasta que Néctar fije el vocabulario y le dé efecto; si Néctar lo activa, avisar a hdg antes (regla del dueño).
- Registro de capacidades veraz: `nectar.Capabilities(1).Input.Kinds` es IGUAL al conjunto de tipos que emiten los pasos (`TestElRegistroDelDescriptorNoMiente`, dos sentidos) y `Escalation.ResolveLock` es `true` solo si hay candados declarados. Cada tarea que estrena un tipo lo añade a `inputKinds` en el mismo commit.
- Reglas de Néctar que el bot respeta (confirmadas por nectar-57): solo la PRIMERA opción con `salida:"reanudar"` lleva los `valores`; `requerido`/`largo`/`patron`/`minimo_campos` se validan solo en la consola → **el bot revalida todo lo escrito**; `reason` (enum) no se muestra al agente, `reason_detail` sí (≤64 runas, `^[a-z_]{1,64}$`); `conversacion_resuelta` nunca trae motivo.
- Fuera de alcance: P0-3 (descartado: `fact_group` solo exige `clave`), P1-9 (orden en texto libre: el dueño la rechazó; Ruling 10 sigue, `texto_largo` no se publica) y los tres fallos de base de handlers (`TestAskClientType_Particular_SkipsListToDocument`, `TestMedicationCheck_SanitasNoChannel_Escalates`, `TestMedicationCheck_SanitasWithChannel_SendsLink`): cada tarea exige «sin fallos nuevos» respecto de esa línea.
- Herramientas en contenedor. Definir una vez por terminal (Git Bash) y usar en todos los pasos «Run» (no `eval`: rompe los `|` de `-run`):
  ```bash
  gotest() { MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app -v neurobot-go-mod:/go/pkg/mod -v neurobot-go-build:/root/.cache/go-build golang:1.26-alpine go "$@"; }
  # ejemplo: gotest test ./internal/statemachine/ -run 'TestA|TestB' -v
  ```
  `make fmt` y `make lint` antes de cada commit; el pre-commit de lefthook corre formato y lint. **Nunca `--no-verify`.**
- Commits en español con prefijo convencional, uno por tarea como mínimo, terminados en:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_0161GETGGb472iRRuXqsjaE3
  ```

## Review Focus

1. El agente llena el campo del paso y además pulsa una opción sin dato (`reanudar_sin_dato`/`cerrar`) → manda la opción; lo escrito se ignora y no se inyecta (Tarea 5, `TestApplyAgentReturn_NoInputOptionIgnoresWrittenValues`).
2. Retorno `reanudar` sin `valores` en un paso de campo requerido (Néctar no lo valida en servidor) → re-escala al agente con «Se devolvió el campo vacío.»; nunca un `__resume__` que re-ejecute un paso automático (Tarea 5, `TestApplyAgentReturn_ResumeWithoutValuesOnRequiredFieldReescalates`).
3. Opción de horario cuyo slot ya no está en la sesión (`available_slots_json` cambió) → el paso re-pregunta, no agenda otro horario (Tarea 9, `TestSlotReturnOptions_StaleSlotTokenRePrompts`).
4. Escalación de confirmación (`nectar:*`) con el paciente ya en una sesión ACTIVA → se reutiliza y se marca escalada, sin `Create` que choque con el índice único (Tarea 2, `TestConfirmationEscalate_NectarReusesActiveSession`).
5. Un literal de `escalation_reason` que no case `^[a-z_]{1,64}$` → `safeReasonDetail` lo tira con ERROR en producción; todos los literales se prueban contra la forma (Tarea 3, `TestEscalationReasons_MatchTheWireShape`).

---

## Correcciones a la auditoría (verificadas contra el árbol, HEAD e83561c)

| # | Lo que dice la auditoría | Lo que hay | Consecuencia en el plan |
|---|---|---|---|
| C1 | S04/P1-3: los pasos de recuperación IA «ya declaran TextValidate» | NO lo declaran GFR_CREATININE, GFR_EXAM_DATE, GFR_HEIGHT, ASK_GESTATIONAL_WEEKS, REG_SECOND_NAME/SURNAME, REG_BIRTH_DATE, REG_PHONE2; y el de REG_EMAIL (`validators.Email`) rechaza `NA`, que el handler acepta | T8 extrae validadores compartidos handler↔campo; T5 hace que `ReturnInput.Validate` SUSTITUYA al `TextValidate` del paso |
| C2 | P1-3: SELECT_PROCEDURE y los botones se resuelven con `Data` = payload | `selectProcedureHandler` y `confirmMunicipalityHandler` solo aceptan **postback**; el mensaje virtual del worker es texto (también roto en Bird) | T5: `AgentCommand.DataIsPostback` — el `Data` de una OPCIÓN viaja como postback y como texto a la vez |
| C3 | Tabla 3.2: CANCEL_REASON «re-mostrar» | `StateCancelReason` no tiene handler registrado: inalcanzable | T7 lo mueve a `unreachableAgentCommandSteps` (aserta `!IsRegistered`) |
| C4 | S24: el desajuste de NOTIF_PENDING es solo `waiting_list` | También `cancellation` (sus acciones son `understood`/`reschedule`); `HandleNotifPendingCommand` no tiene caso `waiting_list` (cae a confirmación) y la sesión no guarda el id de lista de espera | T2: menú de NOTIF_PENDING por tipo, caso `waiting_list`, clave `notif_wl_id` |
| C5 | S21: campo CUPS en UPLOAD_EMG_ORDER | `handleAgentCups` REEMPLAZA `ocr_cups_json` y perdería los dependientes de `emg_dep_cups_json` | T9 fusiona con `services.MergeEmgAndDependentCups` (movido desde handlers) |
| C6 | P1-5(c): ocultar U-reanudar | En un paso con campo, ocultarlo deja a Néctar sin opción que lleve datos (formulario bloqueado) | Solo se oculta en pasos SIN campo; en los de campo se re-rotula (`RegisterResumeLabel`) |
| C7 | (no lo trata) | Hoy, si el agente escribe y pulsa otra opción, gana lo escrito | T5 lo invierte (Review Focus 1) |
| C8 | T4: `resolve_lock` nunca viaja | Además `capabilities` publica `resolve_lock:true` sin ningún emisor: el registro miente hoy | T11 lo hace veraz con prueba |
| C9 | P1-1: añadir `motivo` al grupo `estado` | `reason_detail` ya se pinta como «Detalle del motivo» (Ruling 13) | Solo `reason_detail`; los dorados existentes no cambian |
| C10 | Números de línea (coordinator.go:152-160, medical_order.go:173-183, …) | Desplazados unas líneas | Se citan por símbolo |
| C11 | S14 «Corregir entidad → ASK_CLIENT_TYPE» | Un paciente ya identificado corrige entidad por el flujo legado CHECK_ENTITY (lo que Bird ofrecía con `/bot resume CHECK_ENTITY`) | T9: opción con `Target: CHECK_ENTITY` y `Clear: patient_contract, coverage_from_booking` (el dueño la aprobó para el agente de Néctar, igual que en Bird, aunque actualice la entidad en SIESA — 2026-10-28) |
| C12 | (no lo trata) | Varios handlers de texto libre GUARDAN `__resume__` como dato (REG_ADDRESS, REG_OCCUPATION, REG_BARRIO, MEDICATION_ASK_*) | T8 hace que esos handlers traten `__resume__` como entrada vacía (sin guardarla) en Bird y Néctar — excepción aprobada por el dueño — y no ofrece «volver a preguntar» en esos pasos |
| C13 | P2-8: preguntar a Néctar si hay tope de botones | Confirmado: la consola dibuja todas las opciones sin tope | Sin tarea; REG_SELECT_CORRECTION (10 + universales) y SHOW_SLOTS van como botones |

## Lista elegida: botones vs campo `lista`

- **Botones por opción** (SHOW_SLOTS, LIST_APPOINTMENTS, SELECT_PROCEDURE, catálogos de registro): pocas opciones (≤ 6 horarios, ≤ 10 citas, ≤ 12 tipos), cada una es una DECISIÓN completa que no exige formulario, y no obliga a publicar ningún tipo nuevo.
- **Campo `lista`** (ASK_ENTITY_NUMBER): la lista de EPS de una categoría llega a decenas; un `<select>` es la forma legible, el valor (índice mostrado) lo valida el bot y es exactamente lo que el paciente escribiría.

## Mapa de archivos

| Archivo | Responsabilidad | Tareas |
|---|---|---|
| `internal/statemachine/return_menu.go`, `input_descriptor.go` | infraestructura del menú y del descriptor | 1, 5, 6 |
| `internal/statemachine/escalation_reasons.go` (new), `resolve_guards.go` (new), `states.go`, `helpers.go`, `interceptors.go`, `machine.go` | vocabulario interno, marcas de sesión | 2, 3, 4, 10, 11, 15 |
| `internal/statemachine/handlers/*.go` (registros por paso), `summary_case.go` (new), `agent_inputs.go` (new), `escalation.go`, `escalation_nectar.go` | registros, hechos del caso, handler de escalación | 3-16 |
| `internal/worker/{pool,agent_return,agent_command}.go` | aplicar el retorno del agente | 1, 2, 5, 6, 9, 15 |
| `internal/notifications/{manager,confirmation,escalation_envelope}.go` | escalaciones de notificaciones | 2, 11, 12 |
| `internal/nectar/{capabilities,client,facts}.go` | capacidades, error 403, hechos compartidos | 8, 9, 12, 16 |
| `internal/recovery/coordinator.go`, `internal/services/{emg_merge}.go` | motivo IA, fusión EMG | 3, 9 |
| `README.md`, `docs/FLUJOS.md`, `docs/GUIA-AUDITORIA.md` | conteos y eventos | 16 |

---

### Task 1: P0-1 — «Devolver al bot» desde `BOT_DISABLED` deja la sesión rota

`worker/pool.go:processMessage` pone `CtxPreAutoState = "BOT_DISABLED"`; el retorno `bot_reanudar` llama `ResumeFromEscalation(…,"BOT_DISABLED")` y `machine.Process` falla «no handler registered». Se publica una salida real y se blinda el worker contra cualquier destino sin handler.

**Files:**
- Modify: `internal/statemachine/states.go` (constante), `internal/statemachine/return_menu.go` (`HidePlainResume`), `internal/statemachine/handlers/escalation.go` (`RegisterEscalationHandlers` registra el menú de `BOT_DISABLED`; `buildAgentCommands` usa la constante), `internal/worker/pool.go` (`processMessage`, `handleAgentResume`, interfaz `MessageProcessor`), dobles de `MessageProcessor` en `internal/worker/*_test.go`, `internal/observability/tracer.go`.
- Test: `internal/statemachine/return_menu_test.go`, `internal/statemachine/handlers/return_menus_test.go`, `internal/worker/agent_return_test.go`.

**Interfaces:**
- Produces:
  - `const BotDisabledStep = "BOT_DISABLED"` en `statemachine` (NO empieza por `State`: el lector de `agentCommandSteps` no debe contarlo).
  - `func HidePlainResume(state string)` — `ReturnOptionsFor` omite `bot_reanudar` en ese paso (las demás universales siguen). Mapa protegido por `returnMenusMu`.
  - `MessageProcessor.IsRegistered(state string) bool` (lo cumple `*statemachine.Machine`).
  - Opción de `BOT_DISABLED`: `ReturnOption{Label: "Iniciar la atención del bot desde el saludo", Value: "bot_iniciar", Target: sm.StateGreeting, NoInput: true}` + `HidePlainResume(sm.BotDisabledStep)`.
  - Catálogo: `"escalacion/retorno_estado_inexistente": {LvError, "error", ""}`.

- [ ] **Step 1: Tests**

```go
func TestReturnOptionsFor_HiddenPlainResume(t *testing.T) // HidePlainResume("T1_HIDE"): no hay Value==ReturnResume; sí ReturnClose
func TestBotDisabledReturnMenu_OffersGreetingAndHidesResume(t *testing.T) // tras RegisterEscalationHandlers(m, nil, nil, nil), ReturnOptionsFor(nil, sm.BotDisabledStep): contiene {Value:"bot_iniciar", Target:GREETING, NoInput:true}; no contiene ReturnResume
func TestHandleAgentResume_NectarBotDisabledResumesAtGreeting(t *testing.T) // sess "nectar:7", pre_escalation_state BOT_DISABLED, ReturnV1{Option:"bot_iniciar"} → ResumeFromEscalation(…, "GREETING") y Process llamado
func TestHandleAgentResume_UnregisteredTargetFallsBackToGreeting(t *testing.T) // comando Bird sin State, pre_escalation_state "BOT_DISABLED", doble IsRegistered=false → ResumeFromEscalation(…, "GREETING"); evento escalacion/retorno_estado_inexistente
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/worker/ -run 'HiddenPlainResume|BotDisabled|UnregisteredTarget' -v` → FAIL (símbolos ausentes).
- [ ] **Step 3: Implementar.** En `handleAgentResume`, justo después de calcular `targetState` y antes de `ResumeFromEscalation`: si `!p.machine.IsRegistered(targetState)` → `slog.Warn` + `observability.Emit(..., "escalacion", "retorno_estado_inexistente", EmitOpts{Reason: targetState})` + `targetState = statemachine.StateGreeting`. Sustituir los literales `"BOT_DISABLED"` de `pool.go` y del `case` de `buildAgentCommands`.
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/worker/... ./internal/observability/...` → PASS (salvo los 3 fallos de base).
- [ ] **Step 5: Commit** `fix(nectar): devolver desde BOT_DISABLED retoma en el saludo y ningún retorno cae en un estado sin handler`

---

### Task 2: P0-2 — escalaciones de notificación con sesión, y el menú de NOTIF_PENDING por tipo

`confirmation.go:escalateToAgent` no crea ni marca sesión: el retorno de Néctar se descarta («agent command but no escalated session»). El descriptor de NOTIF_PENDING es el mismo para los cuatro tipos y para el fallo de persistencia (S26), donde la acción útil es «ya la confirmé a mano».

**Files:**
- Create: `internal/statemachine/escalation_reasons.go` (clave y literales de notificación; T3 la amplía).
- Modify: `internal/notifications/manager.go` (extraer `escalatedNotifSession`; `escalateNotifToAgent` lo usa y añade claves; `HandleNotifPendingCommand` + caso `waiting_list`), `internal/notifications/confirmation.go` (`escalateToAgent`), `internal/notifications/escalation_envelope.go`, `internal/statemachine/input_descriptor.go` (`StaticInputDescriptor`), `internal/statemachine/states.go`, `internal/statemachine/handlers/appointments.go` (menú dinámico), `internal/worker/pool.go` (interfaz `NotificationResponder`, atajo de NOTIF_PENDING).
- Test: `internal/notifications/escalate_summary_envelope_test.go`, `escalate_notif_existing_session_test.go`, `internal/statemachine/input_descriptor_test.go`, `internal/statemachine/handlers/return_menus_test.go`, `internal/worker/agent_return_test.go`.

**Interfaces:**
- Produces (en `statemachine`):
  - `const CtxEscalationReason = "escalation_reason"`; `EscalationReasonNotifNoResponse = "notificacion_sin_respuesta"`, `EscalationReasonNotifSystemFailure = "notificacion_fallo_sistema"`, `EscalationReasonNotifFreeText = "confirmacion_texto_libre"` (los tres literales de `notifications/escalation_envelope.go` se mudan aquí: el menú de handlers los lee).
  - `const CtxNotifEscalationDetail = "notif_escalation_detail"`, `CtxNotifWaitingListID = "notif_wl_id"`, `CtxNotifCupsCode = "notif_cups_code"`.
  - `func StaticInputDescriptor(sess *session.Session, step string) []byte` (gana `sess`; `nil` = menú por defecto). Ruling 1 se mantiene: sin máquina, y nil para pasos con `ReturnInput`.
- Produces (en `handlers`): `func notifPendingReturnOptions(sess *session.Session) []sm.ReturnOption`, registrado con `sm.RegisterReturnMenu(sm.StateNotifPending, …)`:

| Condición (contexto) | Opciones (`Label` / `Value` = `Data`, todas `Target: NOTIF_PENDING` salvo indicación) |
|---|---|
| `notif_escalation_detail == notificacion_fallo_sistema` | «Reintentar la confirmación en SIESA» / `confirm`; «Ya la confirmé a mano en SIESA» / `confirmada_manual` con `Close: true` (sin Target/Data) |
| `notif_type == waiting_list` | «Agendar ahora» / `wl_schedule`; «No le interesa» / `wl_decline` |
| `notif_type == cancellation` | «Entendido, sin reprogramar» / `understood`; «Reprogramar» / `reschedule` |
| resto o `sess == nil` | las tres de hoy: `confirm`, `reschedule`, `cancel` |

- Produces (en `notifications`):
  - `func (m *NotificationManager) escalatedNotifSession(ctx context.Context, phone, convID string, sessCtx map[string]string) (sessionID string)` — el bloque «reutilizar la sesión actual y marcarla escalada, o crear una ESCALATED en NOTIF_PENDING» de hoy, sin cambio de lógica.
  - `func notifEscalation(cfg *config.Config, phone string, view *session.Session, detail, summary string) bird.Escalation` — `view` es `&session.Session{Context: sessCtx}`; el descriptor sale de `sm.StaticInputDescriptor(view, sm.StateNotifPending)`.
  - `HandleNotifPendingCommand(phone, action, convID, appointmentID, notifType, waitingListID string)`; caso `"waiting_list"` → `m.handleWaitingList(phone, normalizePostback(action), pending)` con `pending.WaitingListID = waitingListID`.
  - `sessCtx` de `escalateNotifToAgent` gana `notif_wl_id` (`p.WaitingListID`), `notif_cups_code` (`appt.Procedures[0].CupCode`), `notif_escalation_detail` (`confirmacion_texto_libre`).
  - `escalateToAgent` (confirmación): **solo si `session.IsNectarSession(pending.Phone)`** llama `escalatedNotifSession` con `notif_appointment_id`, `notif_type`, `notif_appt_date/time`, `notif_cups_name`, `notif_cups_code`, `notif_conv_id`, `patient_name`, `pre_escalation_state=NOTIF_PENDING`, `notif_escalation_detail` (`notificacion_sin_respuesta` | `notificacion_fallo_sistema`), y con `sessionID != ""` registra `escalated_to_agent` y la fila de `escalations` (como `escalateNotifToAgent`). Bird: sin cambio (no crea sesión, como hoy).
- Worker: atajo de NOTIF_PENDING acepta `action ∈ {confirm, reschedule, cancel, understood, wl_schedule, wl_decline}` y pasa `sess.GetContext(sm.CtxNotifWaitingListID)`.

- [ ] **Step 1: Tests**

```go
func TestConfirmationEscalate_NectarCreatesEscalatedSession(t *testing.T) // phone "nectar:42": Create con Status escalated, CurrentState NOTIF_PENDING; ctx pre_escalation_state=NOTIF_PENDING, notif_escalation_detail=notificacion_sin_respuesta
func TestConfirmationEscalate_NectarReusesActiveSession(t *testing.T)     // FindCurrentByPhone → sesión active: UpdateStatus(escalated) + SetContextBatch; Create NO llamado
func TestConfirmationEscalate_BirdCreatesNoSession(t *testing.T)          // "+573001112233": ni FindCurrentByPhone ni Create
func TestConfirmationEscalate_SystemFailureOffersRetryAndManual(t *testing.T) // v1 + nectar: esc.Input.opciones[].valor empieza por ["confirm","confirmada_manual"]; salida de confirmada_manual == "cerrar"
func TestEscalateNotifToAgent_WaitingListOffersScheduleAndDecline(t *testing.T) // p.Type waiting_list, p.WaitingListID "wl-1": opciones empiezan por wl_schedule, wl_decline; sessCtx notif_wl_id == "wl-1"
func TestNotifPendingReturnOptions_ByTypeAndDetail(t *testing.T) // tabla de las 4 filas de arriba (incluida sess nil → confirm/reschedule/cancel)
func TestStaticInputDescriptor_UsesTheSessionView(t *testing.T)   // igual a Machine.InputDescriptor(view, NOTIF_PENDING) byte a byte
func TestHandleNotifPendingCommand_WaitingListSchedules(t *testing.T) // action "wl_schedule", notifType waiting_list, wlID "wl-1" → waitingListRepo.FindByID("wl-1")
func TestHandleAgentResume_NotifPendingWaitingListSchedule(t *testing.T) // ReturnV1{Option:"wl_schedule"} → HandleNotifPendingCommand(phone,"wl_schedule",conv,appt,"waiting_list","wl-1"); sesión Complete
```

Adaptar `TestReturnMenus_NotifPendingStaticDescriptorMatchesTheMachine` a la firma nueva (`StaticInputDescriptor(nil, …)`).

- [ ] **Step 2: Run** `gotest test ./internal/notifications/ ./internal/statemachine/... ./internal/worker/ -run 'ConfirmationEscalate|EscalateNotifToAgent|NotifPending|StaticInputDescriptor|HandleNotifPendingCommand' -v` → FAIL.
- [ ] **Step 3: Implementar** según Interfaces (extracción sin cambio de lógica; el texto al paciente y la nota de Bird intactos).
- [ ] **Step 4: Run** `gotest test ./internal/notifications/... ./internal/statemachine/... ./internal/worker/...` → PASS.
- [ ] **Step 5: Commit** `fix(notifications): la escalación de confirmación en Néctar crea su sesión y NOTIF_PENDING ofrece las acciones de su tipo`

---

### Task 3: P1-1, P1-11, P2-1 — el motivo de cada escalación

Hoy `escalation_reason` lo escribe solo medicamentos y nunca se borra. Como `reason` no se le muestra al agente, el `reason_detail` es lo único que le dice POR QUÉ.

**Files:**
- Modify: `internal/statemachine/escalation_reasons.go`, `helpers.go` (`ValidateWithRetry`, `ValidateButtonResponse`, `RetryOrEscalate` al agotar), `interceptors.go`, `internal/recovery/coordinator.go` (`Handle`, presupuesto agotado), `internal/statemachine/handlers/{medical_order,slots,medical_validation,entity_management,medication,emg_consolidation,post_action,escalation,escalation_nectar}.go`, `internal/worker/{pool,agent_return}.go`.
- Test: `internal/statemachine/escalation_reasons_test.go` (new), `helpers_test.go`, `interceptors_test.go`, `internal/recovery/coordinator_test.go`, `internal/statemachine/handlers/escalation_reasons_test.go` (new), `escalation_nectar_test.go`, `internal/worker/agent_return_test.go`, `pool_test.go`.

**Interfaces:**
- Produces en `statemachine` (constantes `EscalationReason*` y `func EscalationReasons() []string` con TODAS, incluidas las tres de T2):

| Constante → literal | Escritor (sitio) |
|---|---|
| `PatientAsked` → `paciente_pidio_asesor` | `EscalationKeywordsInterceptor`, `postActionMenuHandler` («agente») |
| `MaxRetries` → `max_reintentos` | los tres helpers al agotar |
| `AIRecoveryExhausted` → `ia_recuperacion_agotada` | `recovery.Coordinator.Handle` |
| `ParticularNoOrder` → `particular_sin_orden` | `askMedicalOrderHandler` (`has_order_no`) |
| `OCRAskedForAgent` → `ocr_pidio_agente` | `uploadNonMedia` (`escalate_agent`) |
| `OCRNoCode` → `ocr_sin_codigo` | `processOrderMedia` (sin ningún código) |
| `OCRInvalidJSON` → `ocr_json_invalido` | `validateOCRHandler` y `confirmOCRResultHandler` (Unmarshal) |
| `SanitasBlock` → `bloqueo_sanitas` | `validateOCRHandler`, `sanitasBloqueoEscalation` |
| `NoValidCups` → `cups_no_validos` | `validateOCRHandler` (`len(cups)==0`) |
| `EmptyGrouping` → `agrupacion_vacia` | `confirmOCRResultHandler` (los dos «grupos vacíos») |
| `NoCoverage` → `sin_convenio` | `coverageNoConvenioHandler` (`cov_agente`) |
| `SleepStudy` → `estudio_sueno` | `checkSpecialCupsHandler` |
| `NoEntities` → `sin_entidades` | `showEntityListHandler` |
| `EntityUnresolved` → `entidad_no_resuelta` | `askEntityNumberHandler` (`entityCode == ""`) |
| `ParticularLiga` → `particular_liga` | `askEntityNumberHandler` (`50001`) |
| `MedicationError` → `medicamentos_error` | `medicationPrepareScheduleHandler` (sustituye el literal) |
| `EmgConsolidationFailed` → `consolidacion_emg_fallida` | `escalateEmg` |
| `EmgAskedForAgent` → `emg_pidio_agente` | `uploadEmgOrderHandler` (`escalate_agent`) |
| `BotDisabled` → `bot_desactivado` | `pool.processMessage` (kill switch) |
| `AgentInputRejected` → `dato_agente_rechazado` | `reescalateRejectedAgentInput` (`updateCtx`) |

- Cada sitio escribe con `WithContext(sm.CtxEscalationReason, sm.EscalationReasonX)` (en el worker, por el mapa de contexto que ya escribe).
- **P2-1**: `uploadNonMedia` (`escalate_agent`), `uploadEmgOrderHandler` (`escalate_agent`) y `coverageNoConvenioHandler` (`cov_agente`) añaden `WithContext(sm.CtxPatientAskedForAgent, "1")`: lo pidió el paciente.
- `escalationExit` borra también `sm.CtxEscalationReason` (todas las salidas; el evento `escalated` y el sobre lo leen antes).
- **P1-11**: en el bloque 4b del handler (solo Néctar), `notice` se fuerza a `sm.EscalationNoticeText` si `reasonNotifiesPatient(sess.GetContext(sm.CtxEscalationReason))`, con `func reasonNotifiesPatient(reason string) bool` verdadero para `particular_liga` y `bot_desactivado` (los callers que hoy no traen texto). Bird intacto.

- [ ] **Step 1: Tests**

```go
func TestEscalationReasons_MatchTheWireShape(t *testing.T) // cada EscalationReasons() casa ^[a-z_]{1,64}$ y no se repite
func TestValidateWithRetry_ExhaustedWritesMaxRetriesReason(t *testing.T) // RetryCount=2, inválido → NextState ESCALATE_TO_AGENT y UpdateCtx[CtxEscalationReason]=="max_reintentos"
func TestValidateButtonResponse_ExhaustedWritesMaxRetriesReason(t *testing.T)
func TestRetryOrEscalate_ExhaustedWritesMaxRetriesReason(t *testing.T)
func TestEscalationKeywordsInterceptor_WritesPatientAskedReason(t *testing.T) // "asesor" → reason paciente_pidio_asesor y CtxPatientAskedForAgent "1"
func TestHandle_BudgetExhaustedWritesReason(t *testing.T)                   // recovery: ia_recuperacion_agotada
func TestEscalationSites_WriteTheirReason(t *testing.T) // tabla con un caso por fila de la tabla de arriba alcanzable sin BD (particular_sin_orden con wlRepo nil, ocr_json_invalido, bloqueo_sanitas en sanitasBloqueoEscalation, cups_no_validos con procedureRepo doble vacío, estudio_sueno, particular_liga, sin_convenio, consolidacion_emg_fallida con apptSvc nil, ocr_pidio_agente, emg_pidio_agente)
func TestEscalationExit_ClearsReason(t *testing.T)
func TestEscalate_NectarEnvelopeCarriesReasonDetail(t *testing.T)   // v1 + reason bloqueo_sanitas → esc.ReasonDetail == "bloqueo_sanitas"
func TestEscalate_NectarNotifiesPatientOnParticularLiga(t *testing.T) // sin marca de paciente, reason particular_liga → SendText(EscalationNoticeText) ANTES de Escalate
func TestEscalate_BirdParticularLigaStaysSilent(t *testing.T)       // E.164: ningún SendText del handler (como hoy)
func TestEscalate_OCRButtonIsPatientAsked(t *testing.T)            // tras escalate_agent, Escalation.Reason == patient_asked_for_human
func TestProcessMessage_BotDisabledWritesReason(t *testing.T)
func TestReescalateRejected_WritesReason(t *testing.T)
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/recovery/... ./internal/worker/ -run 'EscalationReasons|ExhaustedWrites|WritesPatientAsked|BudgetExhausted|EscalationSites|EscalationExit|ReasonDetail|ParticularLiga|OCRButton|BotDisabledWrites|ReescalateRejected_Writes' -v` → FAIL.
- [ ] **Step 3: Implementar** (un `WithContext` por sitio; ninguna frase al paciente cambia).
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/recovery/... ./internal/worker/...` → PASS; dorados intactos.
- [ ] **Step 5: Commit** `feat(escalacion): cada escalación dice su motivo (reason_detail) y se borra al salir`

---

### Task 4: P1-2 — los datos del caso en `facts` (y en el texto)

**Files:**
- Create: `internal/statemachine/handlers/summary_case.go`, `testdata/summary_caso_completo.golden`.
- Modify: `internal/statemachine/handlers/summary_groups.go` (`optional`), `escalation.go` (`summaryForAgent` llama a `writeCaseGroups`), `escalation_nectar.go` (`escalationExit` borra las claves de escalación), `internal/statemachine/states.go`, `helpers.go`, `handlers/medical_order.go`.
- Test: `internal/statemachine/handlers/summary_golden_test.go`, `summary_groups_test.go`, `internal/statemachine/helpers_test.go`, `handlers/medical_order_test.go`.

**Interfaces:**
- Produces en `statemachine`: `CtxEscalationLastInput = "escalation_last_input"` (los helpers al agotar, ya enmascarado por `maskInputForLog` y recortado a 120 runas), `CtxOCRFailureCause = "ocr_failure_cause"` (`processOrderMedia` en fallo: `ocrResult.Error`, o `ocr_error`/`ocr_timeout`; se BORRA en el éxito), `CtxOCRUnreadNames = "ocr_unread_names"` (nombres de `ocrResult.Cups` sin código, unidos por `"; "`), `CtxEscalationWaitingListID = "escalation_waiting_list_id"` (`has_order_no`, `entry.ID`). `escalationExit` borra `CtxEscalationLastInput` y `CtxEscalationWaitingListID`.
- Produces en `handlers`: `func (g *summaryGroup) optional(key, label, value string)` (= `line` solo si `value != ""`); `func writeCaseGroups(doc *summaryDoc, sess *session.Session, cupsCode string)`. Orden total de grupos: `encabezado, paciente, nota, entidad, procedimiento, orden, grupo, agenda, cita, busqueda, intento, estado`. Todas las líneas con `optional` (un grupo sin líneas no se emite: los tres dorados existentes no cambian).

| Grupo | clave → rótulo ← origen |
|---|---|
| `entidad` | `tipo_cliente` «Tipo de cliente» ← `client_type`; `entidad` «Entidad» ← `selected_entity_name`, si no `patient_entity`; `codigo_entidad` «Código de entidad» ← `selected_entity_code`; `contrato` «Contrato» ← `patient_contract`; `regimen` «Régimen» ← `eps_regimen` |
| `orden` (solo con `cupsCode == ""`) | `cups_leidos` «CUPS leídos» ← `ocr_cups_json` en la gramática del campo (`883141:1 930810:2`, copiable); `sin_codigo` «Procedimientos sin código» ← `CtxOCRUnreadNames`; `causa_ocr` «Causa del fallo de lectura» ← `CtxOCRFailureCause`; `lista_espera` «Lista de espera» ← `CtxEscalationWaitingListID` |
| `grupo` (solo con `total_procedures ≥ 2`) | `actual` «Grupo» ← `"<idx+1> de <total>"`; `pendientes` «Pendientes» ← grupos posteriores de `procedures_json`, `"<servicio> (<nombres>)"` unidos por `"; "` |
| `agenda` | `horario_elegido` «Horario elegido» ← `findSelectedSlot` (`<fecha> <hora> con Dr. <x>`); `horarios_ofrecidos` «Horarios ofrecidos» ← `available_slots_json` (`1) … ; 2) …`) si no hay elegido |
| `cita` | `id` «Cita» ← `notif_appointment_id`, si no `selected_appointment_id`, si no `consolidate_appt_id`; `fecha` «Fecha» ← `notif_appt_date`; `hora` «Hora» ← `notif_appt_time`; `procedimiento` «Procedimiento» ← `notif_cups_name`; `tipo_notificacion` «Notificación» ← `notif_type` |
| `busqueda` | `resultados` «Resultados de la búsqueda» ← `search_procedures_json` (`<Code> <Name>` unidos por `"; "`) |
| `intento` | `ultimo_mensaje` «Último mensaje del paciente» ← `CtxEscalationLastInput` |

- [ ] **Step 1: Tests** — añadir a `summaryCases()` el caso `caso_completo` (sesión con `client_type`, `selected_entity_name`, `patient_contract`, `ocr_cups_json` de dos CUPS sin `cups_code`, `total_procedures=3`/`current_procedure_idx=0`/`procedures_json`, `notif_*`, `CtxEscalationLastInput`) y:

```go
func TestSummaryForAgent_CaseGroupsOmittedWhenEmpty(t *testing.T) // sesión con solo paciente: claves de grupos == [encabezado paciente estado]
func TestSummaryForAgent_OrderGroupOnlyBeforeGrouping(t *testing.T) // con cups_code: no hay grupo "orden"
func TestSummaryForAgent_CupsReadInAgentGrammar(t *testing.T)     // valor de cups_leidos == "883141:1 930810:2" y len(services.ParseAgentCups(valor)) == 2
func TestValidateWithRetry_ExhaustedKeepsMaskedLastInput(t *testing.T) // en ASK_DOCUMENT el valor guardado es utils.MaskDocument(input)
func TestProcessOrderMedia_FailureKeepsCauseAndSuccessClearsIt(t *testing.T)
func TestProcessOrderMedia_NoCodeKeepsUnreadNames(t *testing.T)
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... -run 'Summary|ExhaustedKeepsMasked|ProcessOrderMedia_' -v` → FAIL (falta el dorado nuevo y los símbolos).
- [ ] **Step 3: Implementar**; luego crear SOLO el dorado nuevo: `gotest test ./internal/statemachine/handlers/ -run 'TestSummaryGolden/caso_completo' -update`, revisarlo a mano (cada línea con su rótulo de la tabla) y añadirlo al commit.
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/...` → PASS; `git diff --stat -- internal/statemachine/handlers/testdata/` muestra SOLO el archivo nuevo.
- [ ] **Step 5: Commit** `feat(escalacion): facts con entidad, orden leída, grupos pendientes, agenda, cita y último intento`

---

### Task 5: Infraestructura del menú — tipos nuevos, opciones con efecto y reglas del retorno

**Files:**
- Modify: `internal/statemachine/return_menu.go`, `input_descriptor.go`, `internal/worker/agent_command.go`, `agent_return.go`, `pool.go`, `internal/statemachine/handlers/identification.go` (comentario de ASK_DOCUMENT a la semántica nueva).
- Test: `internal/statemachine/return_menu_test.go`, `input_descriptor_test.go`, `internal/statemachine/handlers/return_menus_test.go`, `internal/worker/agent_return_test.go`.

**Interfaces:**
- Produces en `statemachine`:
  - `const InputKindDate = "fecha"`, `InputKindNumber = "numero"`, `InputKindList = "lista"`, `InputFormatISO = "iso"`.
  - `type InputChoice struct { Label string \`json:"rotulo"\`; Value string \`json:"valor"\` }`; `InputField.Options []InputChoice \`json:"opciones,omitempty"\``.
  - `ReturnInput.Format string` (publica `formato`) y `ReturnInput.Options func(sess *session.Session) []InputChoice` (solo para `lista`; con `sess == nil` devuelve nil).
  - Semántica nueva de `judge`: **si la entrada declara `Validate`, ésa es la regla y el `TextValidate` del paso NO se aplica**; sin `Validate`, se aplica el del paso (ASK_DOCUMENT sigue igual). Con `Kind == lista` y opciones no vacías, un valor fuera de `Options(sess)` se rechaza con `"El valor elegido no está en la lista de este paso."` antes de `Validate`.
  - `ReturnOption.Clear []string` (claves de contexto que se borran al retomar por esa opción).
  - `func RegisterResumeLabel(state, label string)` (re-rotula `bot_reanudar` en ese paso), `func ReturnInputRequired(step string) bool`, `func RegisteredReturnSteps() []string` (pasos con menú o entrada, ordenados), `func IsUniversalReturnValue(v string) bool`.
- Produces en `worker`:
  - `AgentCommand.ClearCtx []string`, `AgentCommand.DataIsPostback bool`.
  - `applyAgentReturn`, en este orden: (a) si `ok && (opt.Close || opt.NoInput)` → lo escrito se ignora (`slog.Info` sin valores) y manda la opción; (b) `written.OK()`/`Rejected()` como hoy; (c) si `!written.Written && ok && !opt.Close && !opt.NoInput && sm.ReturnInputRequired(step)` → `reescalateRejectedAgentInput` con `Problem: "Se devolvió el campo vacío."`; (d) rama `default`: `cmd.ClearCtx = opt.Clear`, `cmd.DataIsPostback = opt.Data != ""`.
  - `handleAgentResume`: tras `ResumeFromEscalation`, si `len(cmd.ClearCtx) > 0` → `SaveState(ctx, sess, targetState, nil, cmd.ClearCtx)` y borrado en memoria; el mensaje virtual con dato lleva `IsPostback: cmd.DataIsPostback`, `PostbackPayload: cmd.Data` además de `Text: cmd.Data`. Un comando de Bird nunca pone `DataIsPostback` (texto, como hoy).
- Test helper de `return_menus_test.go`: `hasReturnEntry` cuenta opciones con `!sm.IsUniversalReturnValue(o.Value)`; las pruebas que iteraban `declaredSteps` iteran `sm.RegisteredReturnSteps()`.

- [ ] **Step 1: Tests**

```go
func TestInputDescriptor_ListFieldCarriesOptionsFromTheSession(t *testing.T) // Kind lista + Options(sess) → campos[0].tipo=="lista", opciones == [{rotulo,valor}…]
func TestInputDescriptor_DateFieldDeclaresIsoFormat(t *testing.T)            // Kind fecha + Format iso → "formato":"iso"
func TestResolveReturnV1_ListValueOutsideOptionsIsRejected(t *testing.T)
func TestResolveReturnInput_ValidateReplacesTheStepTextValidate(t *testing.T) // paso con TextValidate que rechaza "NA" + Validate que lo acepta → OK
func TestReturnOptionsFor_ResumeLabelOverride(t *testing.T)                  // mismo Value bot_reanudar, rótulo nuevo
func TestRegisteredReturnSteps_ListsMenusAndInputs(t *testing.T)
func TestApplyAgentReturn_NoInputOptionIgnoresWrittenValues(t *testing.T)    // ReturnV1{Option:"opt_sin_dato", Values:{"dato":"x"}} → cmd.Data == opción.Data, nada inyectado de "x"
func TestApplyAgentReturn_ResumeWithoutValuesOnRequiredFieldReescalates(t *testing.T) // paso con ReturnInput Required, ReturnV1{Option:"bot_reanudar"} sin valores → re-escala, rechazo "Se devolvió el campo vacío.", Process recibe "__agent_input_rejected__"
func TestHandleAgentResume_OptionClearsContext(t *testing.T)                 // opción con Clear{"patient_contract"} → SaveState con clearCtx ["patient_contract"]
func TestHandleAgentResume_OptionDataArrivesAsPostback(t *testing.T)         // msg virtual IsPostback && PostbackPayload == Text == Data
func TestHandleAgentResume_BirdCommandStaysText(t *testing.T)                // AgentCommand{Action:"resume",State:"X",Data:"y"} sin retorno → IsPostback false
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/worker/ -run 'ListField|DateField|ListValueOutside|ValidateReplaces|ResumeLabel|RegisteredReturnSteps|NoInputOptionIgnores|ResumeWithoutValues|OptionClears|OptionDataArrives|BirdCommandStaysText' -v` → FAIL.
- [ ] **Step 3: Implementar.** `nectar.Capabilities` NO cambia aquí (ningún paso emite aún los tipos nuevos).
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/worker/... ./internal/nectar/...` → PASS.
- [ ] **Step 5: Commit** `feat(statemachine): tipos fecha/numero/lista, opciones que limpian o llegan como postback y reglas del retorno`

---

### Task 6: P1-10 — reiniciar la conversación desde cualquier paso (el `/bot` de Bird)

**Files:**
- Modify: `internal/statemachine/return_menu.go`, `internal/worker/agent_return.go`, `internal/worker/pool.go` (`handleAgentReset`).
- Test: `internal/statemachine/return_menu_test.go`, `input_descriptor_test.go`, `internal/worker/agent_return_test.go`.

**Interfaces:**
- Produces: `const ReturnReset = "bot_reiniciar"`; `ReturnOption.Reset bool`; `ReturnOptionsFor` añade, entre `bot_reanudar` y `bot_cerrar`: `{Label: "Reiniciar la conversación desde el saludo (se pierde lo avanzado)", Value: ReturnReset, Reset: true, NoInput: true}`; `IsUniversalReturnValue` lo incluye.
- `applyAgentReturn`: `ok && opt.Reset` → `p.handleAgentReset(ctx, sess, cmd)`; `done`.
- `handleAgentReset`: en sesiones Néctar NO manda «Hemos retomado tu atención. Vamos a comenzar de nuevo.» (el saludo sale en el mismo turno); Bird intacto.

- [ ] **Step 1: Tests**

```go
func TestBuildReturnMenu_UniversalOptionsLast(t *testing.T)      // (actualizar) las tres últimas: bot_reanudar, bot_reiniciar, bot_cerrar
func TestInputDescriptor_ResetExitIsResumeWithoutData(t *testing.T) // salida de bot_reiniciar == "reanudar_sin_dato"
func TestApplyAgentReturn_ResetRestartsFromGreeting(t *testing.T)   // ReturnV1{Option:"bot_reiniciar"} → ResumeFromEscalation(GREETING) + ClearAllContext
func TestHandleAgentReset_NectarNoInterstitialText(t *testing.T)
func TestHandleAgentReset_BirdUnchanged(t *testing.T)               // E.164: SendText con el texto de hoy
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/worker/ -run 'UniversalOptionsLast|ResetExit|ResetRestarts|HandleAgentReset' -v` → FAIL.
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/worker/... ./internal/notifications/...` → PASS (el descriptor estático de NOTIF_PENDING sigue igual al de la máquina).
- [ ] **Step 5: Commit** `feat(nectar): opción universal «reiniciar desde el saludo» (equivalente de /bot)`

---

### Task 7: P1-3 (botones) y P2-3 — menús estáticos de los pasos de elección

Cada opción es `{Label: <texto del botón del paciente>, Value: <payload>, Data: <payload>}` (llega como postback y texto por la Tarea 5). Donde el paso ya tiene filas de lista (`*ListRows()`), las opciones se derivan de ellas con un helper para no duplicar el catálogo.

**Files:**
- Create: helper en `internal/statemachine/handlers/agent_inputs.go` (new): `func optionsFromRows(rows []sm.ListRow) []sm.ReturnOption`.
- Modify (registros junto a cada paso): `identification.go`, `entity_management.go`, `registration.go`, `medical_validation.go`, `slots.go`, `appointments.go`, `post_action.go`, `greeting.go`, `medication.go`, `emg_consolidation.go`.
- Test: `internal/statemachine/handlers/return_menus_test.go`.

**Interfaces:**
- Pasos y payloads (de `buildAgentCommands` y del handler; los `Target` extra entre corchetes):
  - CONFIRM_IDENTITY `identity_yes`, `identity_no` · CONFIRM_CONTACT_INFO `contact_ok`, `contact_update` · ASK_EPS_REGIMEN `regimen_1`, `regimen_2` · ASK_CLIENT_TYPE `ct_1`…`ct_6` (rótulos `domain.EntityCategoryLabels`) · CONFIRM_ENTITY `entity_ok`, `entity_change`
  - REGISTRATION_START `register_yes`, `register_no` · CONFIRM_REGISTRATION `reg_confirm`, `reg_correct` · REG_SELECT_CORRECTION (de `correctionFields()`) · REG_DOCUMENT_TYPE (de `documentTypeCatalog`, igual que ASK_DOCUMENT_TYPE) · REG_GENDER `M`, `F` · REG_ZONE `U`, `R` · REG_BLOOD_TYPE / REG_MARITAL_STATUS / REG_EDUCATION_LEVEL / REG_USER_TYPE / REG_AFFILIATION_TYPE (de sus `*ListRows()`) · CONFIRM_MUNICIPALITY `muni_ok`, `muni_change`
  - ASK_CONTRASTED `contrast_yes`, `contrast_no` · ASK_PREGNANCY `pregnant_no`, `pregnant_yes` · ASK_BABY_WEIGHT `baby_normal`, `baby_low` · GFR_DISEASE `disease_none`, `disease_renal`, `disease_diabetica` · ASK_SEDATION `sedated_yes`, `sedated_no`
  - OFFER_WAITING_LIST `wl_yes`, `wl_no`, [«Buscar horarios de nuevo» `buscar_horarios` → SEARCH_SLOTS] · CONFIRM_BOOKING `booking_confirm`, `booking_change`, [`buscar_horarios`] · RECONFIRM_BOOKING `reconfirm_yes`, `reconfirm_no` · SLOT_SEARCH_RETRY `retry`, `menu`
  - NO_APPOINTMENTS `no_appt_menu`, `no_appt_end` · APPOINTMENT_ACTION `appt_confirm`, `appt_cancel`, `appt_reschedule`, `appt_preparation`, `appt_back`, `appt_menu` · CONFIRM_APPOINTMENT `confirm_yes`, `confirm_no` · CANCEL_APPOINTMENT `cancel_yes`, `cancel_no` · CONFIRM_RESCHEDULE_NOTIF `reschedule_yes`, `reschedule_no` · CONFIRM_CANCEL_NOTIF `cancel_yes`, `cancel_no` · NOTIF_RESCHEDULE_FALLBACK `confirm`, `cancel`
  - POST_ACTION_MENU `ver_citas`, `menu_principal`, `terminar_chat` · FALLBACK_MENU `action:restart`, `action:end` · OUT_OF_HOURS_MENU `ooh_resultados`, `ooh_ubicacion`, `ooh_ayuda` · MAIN_MENU (P2-3) añade `medicamentos`, `resultados`, `ubicacion`, `ayuda` a las dos de hoy
  - MEDICATION_ASK_SYMPTOMS `sin_sintomas`, `con_sintomas` · MEDICATION_CONFIRM_ORDEN `med_confirm_ok`, `med_confirm_retry` · CONFIRM_CONSOLIDATE `consolidate_yes`, `consolidate_no` · ASK_EMG_ORDER `emg_order_yes`, `emg_order_no`
- `returnMenuMachine()` registra además `RegisterRegistrationHandlers(m, nil, nil)`, `RegisterEntityManagementHandlers(m, nil, nil)`, `RegisterMedicalValidationHandlers(m, nil, nil, nil)`, `RegisterPostActionHandlers(m, nil)`, `RegisterMedicationHandlers(m, nil, nil)`, `RegisterResultsAndLocationHandlers(m, sampleConfig(), nil)` (verificar que ninguno toca sus dependencias al registrar; si alguno lo hace, envolverlo en el test con dobles mínimos).
- `deferredAgentCommandSteps` pierde todos los de esta lista; CANCEL_REASON pasa a `unreachableAgentCommandSteps = []string{"CANCEL_REASON"}` y `TestReturnMenusCoverAgentCommands` lo exime aserteando `!m.IsRegistered("CANCEL_REASON")`.

- [ ] **Step 1: Tests**

```go
func TestReturnMenus_ButtonStepsOfferTheHandlerPayloads(t *testing.T)
// para cada paso con config InputButton: conjunto de Data de sus opciones propias CON Data == conjunto de cfg.Options (dos sentidos;
// las opciones solo-Target como buscar_horarios no cuentan); para los de ValidateButtonResponse directo, tabla fija paso → payloads de la lista de arriba
func TestReturnMenus_CancelReasonIsUnreachable(t *testing.T)
func TestReturnMenus_MainMenuOffersEveryMenuOption(t *testing.T) // Data == {medicamentos agendar consultar resultados ubicacion ayuda}
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/handlers/ -run 'ReturnMenu|ElRegistroDelDescriptor' -v` → FAIL.
- [ ] **Step 3: Implementar** los registros (pegados al `Register` de cada paso).
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/nectar/...` → PASS.
- [ ] **Step 5: Commit** `feat(handlers): el agente de Néctar contesta los pasos de elección con los mismos payloads que el paciente`

---

### Task 8: P1-3 (texto) y P1-4 — campos escritos y salidas sin dato

**Files:**
- Modify: `internal/statemachine/handlers/agent_inputs.go` (validadores compartidos y constructores), `identification.go`, `entity_management.go`, `registration.go`, `medical_validation.go`, `medication.go`, `medical_order.go`, `internal/nectar/capabilities.go` (`inputKinds = {"texto","fecha","numero"}`), `internal/nectar/capabilities_test.go`.
- Test: `internal/statemachine/handlers/agent_inputs_test.go` (new), `return_menus_test.go`.

**Interfaces:**
- Produces en `handlers` (cada handler pasa a usar el MISMO validador que su campo):
  - `func validCreatinine(s string) bool` (coma→punto; `0 < v ≤ 30`), `validHeightCm` (`30 ≤ v ≤ 250`), `validExamDate` (`parseBirthDate` y no futura), `validGestationalWeeks` (`v > 0`), `validBirthDate` (la regla completa de `regBirthDateHandler`), `optionalName` (`NA`/`noResponses` o `validateName`), `optionalPhone` (`NA` o `utils.ParseColombianPhone != ""`), `emailOrNA` (`NA` o `validators.Email` en minúsculas).
  - `func agentTextInput(label, kind string, validate func(string) bool, problem string) sm.ReturnInput` (`Required: true`; `kind == fecha` ⇒ `Format: iso`).
  - `func askAgainOption() sm.ReturnOption` → `{Label: "Volver a preguntarle al paciente", Value: "repreguntar", NoInput: true}`.
- Registros (`RegisterResumeLabel(step, "Enviar el dato escrito al bot")` en todos):

| Paso | Tipo | Validate | `repreguntar` |
|---|---|---|---|
| ASK_UPDATE_PHONE | numero | (TextValidate del paso) | sí |
| ASK_UPDATE_EMAIL | texto | `emailOrNA` | sí |
| CHANGE_ENTITY | texto | `validateNotEmpty` | no |
| REG_FIRST_SURNAME, REG_FIRST_NAME | texto | `validateName` | sí |
| REG_SECOND_SURNAME, REG_SECOND_NAME | texto | `optionalName` | sí |
| REG_BIRTH_DATE | fecha (iso) | `validBirthDate` | sí |
| REG_PHONE | numero | `validators.ColombianPhone` | sí |
| REG_PHONE2 | texto | `optionalPhone` | sí |
| REG_EMAIL | texto | `emailOrNA` | sí |
| REG_ADDRESS, REG_OCCUPATION, REG_BARRIO | texto | `freeTextAnswer` | **no** (C12) |
| REG_MUNICIPALITY | texto | `validateNotEmpty` | no |
| GFR_CREATININE | numero | `validCreatinine` | sí |
| GFR_EXAM_DATE | fecha (iso) | `validExamDate` | sí |
| GFR_HEIGHT | numero | `validHeightCm` | sí |
| GFR_WEIGHT | numero | (TextValidate del paso) | sí |
| ASK_GESTATIONAL_WEEKS | numero | `validGestationalWeeks` | sí, + `{«Sí está en el rango», weeks_yes, NoInput}`, `{«No está en el rango», weeks_no, NoInput}` |
| MEDICATION_ASK_LAST_DATE, _DOSE, _FREQUENCY | texto | `freeTextAnswer` | **no** (C12) |
| ASK_DOCUMENT (P1-4) | (ya existe) | — | sí |

- C12 (Bird y Néctar, excepción aprobada): `func freeTextAnswer(s string) bool` = `validateNotEmpty(s) && !sm.IsReservedKeyword(strings.TrimSpace(s))`; los handlers de REG_ADDRESS (`regFieldHandler` con ese validador), REG_OCCUPATION, REG_BARRIO y MEDICATION_ASK_LAST_DATE/_DOSE/_FREQUENCY tratan una palabra reservada exactamente como hoy tratan la entrada vacía (mismo texto de re-pregunta, nada se guarda); el campo de esos pasos usa el mismo `freeTextAnswer` como Validate.
- P1-4 en la orden: UPLOAD_MEDICAL_ORDER, VALIDATE_OCR, OCR_FAILED, CONFIRM_OCR_RESULT ganan `{Label: "Pedir de nuevo la foto de la orden", Value: "pedir_orden", Target: StateUploadMedicalOrder, NoInput: true}`; CONFIRM_OCR_RESULT además `{«Los procedimientos leídos son correctos», ocr_correct, Data ocr_correct, NoInput}` y `{«Los procedimientos leídos están mal», ocr_incorrect, Data ocr_incorrect, NoInput}`; los seis pasos de CUPS: `RegisterResumeLabel(step, "Enviar los CUPS escritos al bot")`.

- [ ] **Step 1: Tests**

```go
func TestAgentTextInputs_AcceptWhatTheHandlerAccepts(t *testing.T) // tabla por paso: valores que el handler acepta → ResolveReturnInput OK; los que rechaza → Rejected (incluye REG_EMAIL "NA" OK, GFR_CREATININE "0,96" OK y "31" rechazado)
func TestAgentTextInputs_DateFieldsAreIso(t *testing.T)              // REG_BIRTH_DATE y GFR_EXAM_DATE: tipo fecha, formato iso; "2026-07-05" OK
func TestAskAgainOnlyWhereTheHandlerRejectsReservedKeywords(t *testing.T) // todo paso con "repreguntar": su validador rechaza "__resume__"; REG_ADDRESS/OCCUPATION/MUNICIPALITY/BARRIO y MEDICATION_ASK_* NO lo ofrecen
func TestFreeTextHandlers_ReservedKeywordNotStored(t *testing.T) // tabla paso × canal ("+573001112233" y "nectar:7"): msg "__resume__" → el contexto del dato (reg_address, reg_occupation, reg_barrio, med_last_date, med_dose, med_frequency) NO se escribe y el paso sigue en el mismo estado con el texto de la entrada vacía
func TestReturnMenus_FieldStepsHaveExactlyOneCarryingOption(t *testing.T) // en todo paso con ReturnInput: exactamente una opción con salida "reanudar" y es la primera reanudar
func TestCapabilities_V1(t *testing.T) // (actualizar) Kinds == ["texto","fecha","numero"]
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/nectar/ -run 'AgentTextInputs|AskAgain|ReservedKeywordNotStored|ExactlyOneCarrying|ElRegistroDelDescriptor|Capabilities' -v` → FAIL.
- [ ] **Step 3: Implementar** (extraer cada validador del cuerpo de su handler sin cambiar su veredicto; los mensajes al paciente no cambian, salvo la excepción C12).
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/nectar/... ./internal/api/...` → PASS.
- [ ] **Step 5: Commit** `feat(handlers): campos escritos validados como al paciente y salidas sin dato en los pasos de campo`

---

### Task 9: P1-3 (listas de sesión), S14 y S21 — horarios, citas, entidades, búsqueda y EMG

**Files:**
- Create: `internal/services/emg_merge.go` (mueve `mergeEmgAndDependentCups` de handlers como `MergeEmgAndDependentCups`, sin cambio).
- Modify: `internal/statemachine/handlers/{slots,appointments,entity_management,medical_order,emg_consolidation}.go`, `internal/worker/pool.go` (`handleAgentCups`), `internal/nectar/capabilities.go` (`inputKinds` + `"lista"`), `capabilities_test.go`.
- Test: `internal/statemachine/handlers/return_menus_test.go`, `slots_test.go`, `internal/worker/agent_return_test.go`, `internal/services/emg_merge_test.go`.

**Interfaces:**
- `func slotReturnOptions(sess *session.Session) []sm.ReturnOption` (SHOW_SLOTS): por cada slot de `available_slots_json`, `{Label: "<FormatFriendlyDateShortStr> <TimeDisplay>[ con Dr. <x>]", Value: "slot_<n>", Data: "<n>"}`; con `len ≥ 5`, `{«Ver más horarios», slot_mas, Data "<len+1>"}`; siempre `{«Buscar horarios de nuevo», buscar_horarios, Target SEARCH_SLOTS}`.
- `func appointmentReturnOptions(sess *session.Session) []sm.ReturnOption` (LIST_APPOINTMENTS): por cita de `appointments_json`, `{Label: "<fecha> <hora> — <Procedures[0].CupName>", Value: "cita_<n>", Data: <a.ID>}` (postback que el handler reconoce); `{«Recargar citas desde el sistema», recargar_citas, Target FETCH_APPOINTMENTS}`.
- ASK_ENTITY_NUMBER: `sm.ReturnInput{Kind: lista, Label: "Entidad del paciente", Required: true, Options: entityChoices, Validate: entero en 1..entity_list_count}` con `func entityChoices(sess) []sm.InputChoice` (de `entity_list_names`, valor = índice mostrado); `RegisterResumeLabel` «Usar la entidad elegida»; `{«Elegir otro tipo de entidad», cambiar_tipo_entidad, Target ASK_CLIENT_TYPE, NoInput}`.
- SHOW_ENTITY_LIST: `HidePlainResume` + `{«Elegir otro tipo de entidad», cambiar_tipo_entidad, Target ASK_CLIENT_TYPE}`.
- SELECT_PROCEDURE: `func procedureReturnOptions(sess) []sm.ReturnOption` de `search_procedures_json`: `{Label: "<Code> — <Name>", Value: "proc_<ID>", Data: "<ID>"}`.
- UPLOAD_EMG_ORDER: `agentCupsReturnInput()` con `Label: "CUPS de la orden de electromiografía (se suman a los dependientes ya leídos)"`, `RegisterResumeLabel` «Enviar los CUPS escritos al bot», `{«Pedir de nuevo la foto de la orden de EMG», pedir_orden_emg, Target UPLOAD_EMG_ORDER, NoInput}`. `handleAgentCups`: si `pre_escalation_state == UPLOAD_EMG_ORDER` y `emg_dep_cups_json != ""` → `ocr_cups_json = services.MergeEmgAndDependentCups(cups, deps)` y se borra `emg_dep_cups_json` (Bird `/bot cups` en ese paso gana la misma fusión: acción de agente, no texto al paciente).
- COVERAGE_NO_CONVENIO (S14, C11): `{Label: "Corregir la entidad con el paciente", Value: "cov_entidad", Target: sm.StateCheckEntity, Clear: []string{"patient_contract", "coverage_from_booking"}}`.

- [ ] **Step 1: Tests**

```go
func TestSlotReturnOptions_OnePerOfferedSlot(t *testing.T)      // 3 slots → slot_1..3 + buscar_horarios; 5 slots → + slot_mas con Data "6"
func TestSlotReturnOptions_StaleSlotTokenRePrompts(t *testing.T) // sesión con 2 slots, msg virtual Text "3" → showSlotsHandler queda en SHOW_SLOTS con "Opción inválida…"
func TestAppointmentReturnOptions_DataIsTheAppointmentID(t *testing.T) // y listAppointmentsHandler con postback de ese ID → APPOINTMENT_ACTION
func TestEntityNumberInput_ListFromTheShownNames(t *testing.T)   // descriptor: campo lista con opciones [{"SANITAS","1"},…]; "2" OK; "9" rechazado
func TestEntityNumberInput_EmptyListDegradesAndValidates(t *testing.T) // sin entity_list_names: opciones vacías, "abc" rechazado
func TestSelectProcedureReturnOptions_PostbackIDs(t *testing.T)  // y selectProcedureHandler con ese postback avanza
func TestCoverageReturn_CorrectEntityClearsContract(t *testing.T) // ResolveReturnValue(...,"cov_entidad") → Target CHECK_ENTITY, Clear con las dos claves
func TestHandleAgentCups_EmgMergesDependents(t *testing.T)       // ocr_cups_json final contiene EMG escrita + dependientes; emg_dep_cups_json borrada
func TestCapabilities_V1(t *testing.T) // (actualizar) Kinds == ["texto","fecha","numero","lista"]
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/worker/ ./internal/services/ ./internal/nectar/ -run 'SlotReturnOptions|AppointmentReturnOptions|EntityNumberInput|SelectProcedureReturn|CoverageReturn|EmgMerges|ElRegistroDelDescriptor|Capabilities|MergeEmg' -v` → FAIL.
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/worker/... ./internal/services/... ./internal/nectar/...` → PASS.
- [ ] **Step 5: Commit** `feat(handlers): horarios, citas, entidades y procedimientos como opciones de la sesión; CUPS de EMG fusionados`

---

### Task 10: P1-5 — trampas de los pasos automáticos y «seguir con el siguiente procedimiento»

**Files:**
- Modify: `internal/statemachine/states.go` (`StateAdvanceNextProcedure`), `internal/statemachine/handlers/{slots,medical_validation,medication,escalation}.go`, `internal/observability/tracer.go`.
- Test: `internal/statemachine/handlers/slots_test.go`, `return_menus_test.go`, `escalation_nectar_test.go`.

**Interfaces:**
- `StateAdvanceNextProcedure = "ADVANCE_NEXT_PROCEDURE"` (automático). Handler en `RegisterSlotHandlers`: `next := advanceToNextProcedure(sess)`; con `next != nil` emite `escalacion/siguiente_procedimiento` y lo devuelve; con nil → `buildAutoCloseResult("Tu solicitud quedó gestionada por nuestro equipo.")`. Catálogo: `"escalacion/siguiente_procedimiento": {LvMilestone, "ok", ""}`.
- `func nextProcedureOption(sess *session.Session) []sm.ReturnOption` — SOLO si `current_procedure_idx+1 < total_procedures`: `{Label: "Ya quedó gestionado: seguir con <nombre del siguiente grupo>", Value: "siguiente_procedimiento", Target: StateAdvanceNextProcedure, NoInput: true}`.
- SEARCH_SLOTS y CHECK_SPECIAL_CUPS: `RegisterReturnInput(agentCupsReturnInput())`, `RegisterResumeLabel` «Enviar los CUPS escritos al bot», `RegisterReturnMenu(nextProcedureOption)`.
- CREATE_APPOINTMENT: `HidePlainResume` + `RegisterReturnMenu(nextProcedureOption)`.
- MEDICATION_PREPARE_SCHEDULE: `HidePlainResume` + `{«Volver al menú principal», menu_principal, Target MAIN_MENU}`.
- P1-5(d) en el corte `__resume__` del handler: en sesión Néctar se antepone `resumeSuppressedNectarText = "Sigamos por aquí 👍 Elige una opción del menú para continuar."` al menú (Bird: el menú solo, como hoy).
- `deferredAgentCommandSteps` queda VACÍO: se elimina la variable y la rama `deferred` de `TestReturnMenusCoverAgentCommands` (todo estado nombrado debe estar cubierto o en `unreachableAgentCommandSteps`).

- [ ] **Step 1: Tests**

```go
func TestAdvanceNextProcedure_MovesToNextGroup(t *testing.T)   // idx 0 de 2 → CHECK_SPECIAL_CUPS, current_procedure_idx "1"
func TestAdvanceNextProcedure_NothingLeftCloses(t *testing.T)  // → TERMINATED
func TestNextProcedureOption_OnlyWhenGroupsRemain(t *testing.T)
func TestReturnMenus_NoPlainResumeIntoADeterministicAutomaticStep(t *testing.T)
// para SEARCH_SLOTS, CREATE_APPOINTMENT, CHECK_SPECIAL_CUPS, VALIDATE_OCR, OCR_FAILED, ASK_MEDICAL_ORDER, SHOW_ENTITY_LIST, MEDICATION_PREPARE_SCHEDULE:
// o no ofrece bot_reanudar, o ReturnInputRequired(step); y ningún paso con HidePlainResume tiene ReturnInput
func TestEscalate_NectarResumeSuppressionTellsThePatient(t *testing.T)
func TestEscalate_BirdResumeSuppressionUnchanged(t *testing.T)
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/observability/ -run 'AdvanceNextProcedure|NextProcedureOption|NoPlainResume|ResumeSuppression|ReturnMenusCover|Catalog' -v` → FAIL.
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/observability/...` → PASS.
- [ ] **Step 5: Commit** `fix(nectar): ningún «devolver» re-ejecuta un paso automático que vuelve a escalar; seguir con el siguiente procedimiento`

---

### Task 11: P1-6 — candado de «Resolver» declarado donde el bot tiene trabajo pendiente

Néctar guarda `resolve_lock` opaco y hoy nadie lo lee; el dueño pidió a Néctar darle efecto (propuesta pendiente). Neuro declara literales provisionales en UN archivo para poder renombrarlos.

**Files:**
- Create: `internal/statemachine/resolve_guards.go`.
- Modify: `internal/statemachine/return_menu.go` (`RegisterResolveGuard` valida la forma), registros en `handlers/{identification,medical_order,emg_consolidation,appointments,slots,medical_validation}.go`, `internal/notifications/escalation_envelope.go` (`ResolveLock: sm.ResolveGuardFor(NOTIF_PENDING)` en Néctar).
- Test: `internal/statemachine/return_menu_test.go`, `internal/statemachine/handlers/return_menus_test.go`, `escalation_nectar_test.go`, `internal/notifications/escalate_summary_envelope_test.go`.

**Interfaces:**
- `const ResolveGuardPendingReturn = "retorno_pendiente"` (el flujo sigue cuando el agente devuelve el dato) y `ResolveGuardPendingAppointment = "cita_pendiente"` (queda una cita o una acción de cita por hacer; literal ya usado por hdg). `[N-info]` provisionales.
- `func DeclaredResolveGuards() map[string]string` (copia). `RegisterResolveGuard(state, motivo)` hace panic si `motivo != ""` y no casa `^[a-z_]{1,64}$`.
- Declaraciones: `retorno_pendiente` → ASK_DOCUMENT, ASK_MEDICAL_ORDER, UPLOAD_MEDICAL_ORDER, VALIDATE_OCR, OCR_FAILED, CONFIRM_OCR_RESULT, ASK_MANUAL_CUPS, SELECT_PROCEDURE, UPLOAD_EMG_ORDER; `cita_pendiente` → NOTIF_PENDING, SEARCH_SLOTS, CREATE_APPOINTMENT, CHECK_SPECIAL_CUPS.

- [ ] **Step 1: Tests**

```go
func TestResolveGuard_ShapeIsEnforced(t *testing.T)            // "Retorno" y 65 runas → panic; "" no
func TestResolveGuards_DeclaredWherePendingWork(t *testing.T)   // DeclaredResolveGuards() == mapa exacto de arriba
func TestCapabilities_ResolveLockIsTruthful(t *testing.T)       // nectar.Capabilities(1).Capabilities.Escalation.ResolveLock == (len(DeclaredResolveGuards()) > 0)
func TestEscalate_EnvelopeCarriesResolveLock(t *testing.T)      // pre-estado UPLOAD_MEDICAL_ORDER → esc.ResolveLock == "retorno_pendiente"
func TestNotifEscalation_CarriesResolveLock(t *testing.T)       // nectar → "cita_pendiente"; Bird → ""
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/notifications/ -run 'ResolveGuard|ResolveLock' -v` → FAIL.
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/notifications/... ./internal/nectar/...` → PASS.
- [ ] **Step 5: Commit** `feat(nectar): candado de Resolver en los pasos con trabajo pendiente (literales provisionales)`

---

### Task 12: P1-8 — sobre de las notificaciones con hechos y equipo sugerido

**Files:**
- Create: `internal/nectar/facts.go`.
- Modify: `internal/notifications/escalation_envelope.go`, `manager.go`, `confirmation.go`.
- Test: `internal/notifications/escalate_summary_envelope_test.go`, `internal/statemachine/handlers/summary_groups_test.go`.

**Interfaces:**
- `nectar.FactGroup{Key string "clave"; Title *string "titulo"; Fields []FactField "campos,omitempty"}`, `nectar.FactField{Key "clave"; Label "rotulo,omitempty"; Value "valor"}` (las mismas etiquetas que `handlers.summaryGroup`).
- `func notifFacts(view *session.Session) []nectar.FactGroup` → `paciente{nombre «Paciente» ← patient_name}` y `cita{id, fecha, hora, procedimiento, tipo_notificacion}` con las MISMAS claves y rótulos que el grupo `cita` de la Tarea 4; campos vacíos omitidos.
- `notifEscalation`: en Néctar `SuggestedTeamID = cfg.SuggestedTeamForCups(view.GetContext(sm.CtxNotifCupsCode))` (v0 y v1, como el handler); en v1 además `Facts`. Bird: igual que hoy (solo `Reason` y `Summary`).

- [ ] **Step 1: Tests**

```go
func TestNotifEscalation_NectarV1CarriesFactsAndTeam(t *testing.T) // NECTAR_TEAM_BY_GROUP imagenes:7, notif_cups_code "883101" → SuggestedTeamID 7; facts con grupo "cita" y su id
func TestNotifEscalation_V0NoFactsButTeam(t *testing.T)
func TestNotifEscalation_BirdOnlySummary(t *testing.T)             // E.164: Facts nil, Input nil, SuggestedTeamID nil
func TestSummaryGroupWireMatchesNectarFactGroup(t *testing.T)      // json de un summaryGroup y de un nectar.FactGroup equivalentes son idénticos
```

- [ ] **Step 2: Run** `gotest test ./internal/notifications/ ./internal/statemachine/handlers/ -run 'NotifEscalation|WireMatchesNectarFactGroup' -v` → FAIL.
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: Run** `gotest test ./internal/notifications/... ./internal/nectar/... ./internal/statemachine/...` → PASS.
- [ ] **Step 5: Commit** `feat(notifications): escalaciones de notificación con facts de la cita y equipo sugerido`

---

### Task 13: P1-7 — disponibilidad de asesores por la fuente de Néctar

**Files:**
- Modify: `internal/statemachine/handlers/medical_order.go` (`ocrRetryButtons`, `uploadNonMedia`), `emg_consolidation.go` (su llamada a `ocrRetryButtons`), `post_action.go` (`postActionMenuHandler`, los dos `HasAvailableAgents`).
- Test: `internal/statemachine/handlers/medical_order_test.go`, `post_action_test.go`.

**Interfaces:**
- `func ocrRetryButtons(birdClient *bird.Client, sess *session.Session) []sm.Button` usa `birdClient.HasAvailableAgentsFor(sess.PhoneNumber, sess.ConversationID)`; igual los dos chequeos de `uploadNonMedia` y de `postActionMenuHandler`. Para E.164 `HasAvailableAgentsFor` cae en `HasAvailableAgents` → Bird idéntico.

- [ ] **Step 1: Tests** (con el doble de puente de `escalation_nectar_test.go`: Néctar disponible, presencia de Bird apagada)

```go
func TestOCRRetryButtons_NectarUsesConversationAvailability(t *testing.T) // incluye "escalate_agent"
func TestPostActionAgente_NectarUsesConversationAvailability(t *testing.T) // "agente" → ESCALATE_TO_AGENT
func TestOCRRetryButtons_BirdUnchanged(t *testing.T)
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/handlers/ -run 'OCRRetryButtons|PostActionAgente' -v` → FAIL.
- [ ] **Step 3: Implementar.** **Step 4: Run** `gotest test ./internal/statemachine/...` → PASS.
- [ ] **Step 5: Commit** `fix(nectar): el botón «Hablar con agente» y el menú post-acción miran la disponibilidad de Néctar`

---

### Task 14: P1-12 — la pregunta no pisa el aviso de una escalación en curso (Bird y Néctar)

En `askMedicalOrderHandler` (PARTICULAR), `confirmConsolidateHandler` y `askEmgOrderHandler` el caller sobrescribe `result.Messages` aunque `result.NextState == ESCALATE_TO_AGENT`. El arreglo aplica a **los dos canales**: excepción a «Bird byte a byte» aprobada por el dueño (2026-10-28, Global Constraints).

**Files:**
- Modify: `internal/statemachine/handlers/medical_order.go`, `emg_consolidation.go`.
- Test: `internal/statemachine/handlers/medical_order_test.go`, `emg_consolidation_test.go`.

- [ ] **Step 1: Tests**

```go
func TestAskMedicalOrderParticular_NectarExhaustedRetriesKeepsNotice(t *testing.T) // RetryCount 2 + texto inválido → NextState ESCALATE_TO_AGENT y Messages == [EscalationNoticeText]
func TestAskMedicalOrderParticular_BirdExhaustedRetriesKeepsNotice(t *testing.T)   // E.164: igual que Néctar, Messages == [EscalationNoticeText]
func TestConfirmConsolidate_ExhaustedRetriesKeepsNotice(t *testing.T) // subtests bird y nectar
func TestAskEmgOrder_ExhaustedRetriesKeepsNotice(t *testing.T)      // subtests bird y nectar
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/handlers/ -run 'ExhaustedRetriesKeepsNotice' -v` → FAIL.
- [ ] **Step 3: Implementar** `if result.NextState == sm.StateEscalateToAgent { return result, nil }` antes de sobrescribir (como ya hacen `slotSearchRetryHandler` y `offerWaitingListHandler`).
- [ ] **Step 4: Run** `gotest test ./internal/statemachine/...` → PASS.
- [ ] **Step 5: Commit** `fix(handlers): al agotar reintentos el paciente recibe el aviso, no otra vez la pregunta`

---

### Task 15: P2-4 y P2-5 — un retorno sin dato re-pregunta sin gastar reintento (Néctar)

**Files:**
- Modify: `internal/statemachine/machine.go` (marca de turno), `helpers.go`, `internal/worker/pool.go` (`handleAgentResume`, rama sin dato).
- Test: `internal/statemachine/helpers_test.go`, `machine_test.go`, `internal/worker/agent_return_test.go`.

**Interfaces:**
- `const CtxAgentResumeTurn = "_agent_resume_turn"` — vive en memoria y `Process` la borra con `defer` (como `CtxEscalationNoticeFlushed`). La pone el worker SOLO en sesiones Néctar antes de procesar el `__resume__` de un retorno del agente.
- Con la marca, `ValidateWithRetry`, `ValidateButtonResponse` y `RetryOrEscalate` NO incrementan `RetryCount` ni escalan: devuelven `NewResult(sess.CurrentState)` con su texto de siempre (los callers con `RetryPrompt`/UI propia la re-dibujan como hoy) y el evento `agent_resume_reprompt`. Bird nunca la pone.

- [ ] **Step 1: Tests**

```go
func TestValidateButtonResponse_AgentResumeTurnDoesNotCountRetry(t *testing.T) // RetryCount 1 + marca → sigue 1, NextState actual
func TestValidateWithRetry_AgentResumeTurnDoesNotEscalate(t *testing.T)        // RetryCount 2 + marca → no ESCALATE_TO_AGENT
func TestProcess_AgentResumeMarkerIsTurnScoped(t *testing.T)                   // tras Process la clave no está en sess.Context
func TestHandleAgentResume_NectarSetsResumeMarker(t *testing.T)
func TestHandleAgentResume_BirdNoMarker(t *testing.T)
func TestCoverageMenuReturn_MainMenuRePromptsWithoutRetry(t *testing.T)        // P2-5: cov_menu → MAIN_MENU re-pregunta y RetryCount no sube
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/... ./internal/worker/ -run 'AgentResumeTurn|AgentResumeMarker|ResumeMarker|CoverageMenuReturn' -v` → FAIL.
- [ ] **Step 3: Implementar.** **Step 4: Run** `gotest test ./internal/statemachine/... ./internal/worker/...` → PASS.
- [ ] **Step 5: Commit** `fix(nectar): devolver sin dato re-pregunta el paso sin gastar reintentos del paciente`

---

### Task 16: P2-2, P2-6, P2-7, P2-9 — pulido y documentación

**Files:**
- Modify: `internal/statemachine/handlers/escalation_nectar.go`, `escalation.go`, `internal/nectar/client.go`, `internal/observability/tracer.go`, `README.md` (l.103), `docs/FLUJOS.md` (l.126), `docs/GUIA-AUDITORIA.md`, `CLAUDE.md` (sección Néctar: una línea sobre menús por paso).
- Test: `internal/statemachine/handlers/escalation_nectar_test.go`, `internal/nectar/client_test.go`.

**Interfaces:**
- P2-2: `func teamCupsCode(sess *session.Session) string` = `cups_code`, o el primer código de `ocr_cups_json`; lo usan `escalationEnvelope` y `emitSuggestedTeam`.
- P2-6: el gate del handler no desvía cuando `sess.GetContext(sm.CtxAgentInputRejection) != ""` (como ya se exime del tope y de la IA).
- P2-7: `nectar.ErrEscalationDisabled = fmt.Errorf("nectar: escalado apagado para la entidad (403): %w", bird.ErrPermanentSend)`, devuelto por `do` con `403` y `reason == "escalation_disabled"`. En el handler: el bucle de reintentos se corta con `errors.Is(err, nectar.ErrEscalationDisabled)`; la salida a FALLBACK_MENU usa `escalationDisabledText = "En este momento no podemos comunicarte con un asesor por este medio. ¿Deseas volver al inicio o terminar el chat?"` (mismos botones) y emite `escalacion/escalation_disabled` (catálogo `{LvOutcome, "blocked", ""}`); cualquier otro error, el texto de hoy.
- P2-9 / docs: README y FLUJOS dejan de decir «39 estados escalables vuelven solo con reanudar/cerrar» y describen que todo paso nombrado tiene menú, entrada o candado; GUIA-AUDITORIA lista `retorno_estado_inexistente`, `siguiente_procedimiento`, `escalation_disabled` y los literales de `EscalationReasons()`.

- [ ] **Step 1: Tests**

```go
func TestEscalationEnvelope_TeamFromOCRBeforeGrouping(t *testing.T) // sin cups_code, ocr_cups_json [883101…], imagenes:7 → SuggestedTeamID 7
func TestEscalate_RejectedReescalationSkipsGate(t *testing.T)       // sin asesores + CtxAgentInputRejection → escala igual
func TestClientEscalate_403DisabledIsTyped(t *testing.T)            // httptest 403 {"reason":"escalation_disabled"} → errors.Is(err, ErrEscalationDisabled)
func TestEscalate_DisabledDoesNotPromiseAnAgent(t *testing.T)       // un solo intento; texto == escalationDisabledText
```

- [ ] **Step 2: Run** `gotest test ./internal/statemachine/handlers/ ./internal/nectar/ ./internal/observability/ -run 'TeamFromOCR|RejectedReescalationSkipsGate|403Disabled|DisabledDoesNotPromise|Catalog' -v` → FAIL.
- [ ] **Step 3: Implementar** código y docs.
- [ ] **Step 4: Run** `gotest build ./... && gotest test ./...` → PASS salvo los 3 fallos de base; `make lint` limpio; `git diff --stat -- internal/statemachine/handlers/testdata/` sin cambios.
- [ ] **Step 5: Commit** `fix(nectar): equipo sugerido antes de agrupar, re-escalada sin gate, 403 sin prometer asesor y documentación`

---

## Cierre del plan

- [ ] `gotest test ./... -cover` con solo los 3 fallos de base; `make lint` limpio; los tres dorados de hoy intactos y uno nuevo (`summary_caso_completo.golden`).
- [ ] Humo local (receta `docs/PRUEBA-LOCAL-NECTAR.md`, stub en :9999, bot en :8091): escalar desde ASK_DOCUMENT, SHOW_SLOTS (con `available_slots_json`) y ASK_ENTITY_NUMBER y guardar el `input` que recibe el stub; devolver `{"opcion":"bot_reanudar","valores":{"dato":""}}` en ASK_DOCUMENT → re-escala con rechazo; devolver `{"opcion":"bot_iniciar"}` en una sesión con `BOT_ENABLED=false` → saludo.
- [ ] Ledger (`progress.md`): registrar las correcciones C1-C13 y las decisiones del dueño del 2026-10-28.
- [ ] Avisar a `nectar-cd` los literales provisionales de candado (`retorno_pendiente`, `cita_pendiente`) para su propuesta de vocabulario; avisar a hdg si Néctar decide darle efecto.

## Decisiones del dueño (2026-10-28)

1. P1-12 y C12 se arreglan también en Bird (excepción documentada en Global Constraints; Tareas 14 y 8).
2. «Corregir la entidad» de S14 (flujo CHECK_ENTITY, que actualiza la entidad en SIESA) queda permitido para el agente de Néctar, igual que en Bird (Tarea 9).
