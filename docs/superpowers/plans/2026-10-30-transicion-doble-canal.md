# Transición Bird→Néctar con dos líneas activas — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que un solo bot atienda la línea de producción (Bird) y la línea nueva (Néctar) a la vez, anuncie la línea nueva en los saludos de Bird, muestre los agentes de Bird en el informe aunque el puente esté encendido, y avise por Telegram con urgencia cuando haya que volver a Bird.

**Architecture:** El ruteo por clave de sesión ya existe (`nectar:<id>` → Néctar, lo demás → Bird; `internal/bird/nectar_bridge.go:160`). Se añaden: un texto de anuncio configurable que los saludos de Bird emiten como mensaje de texto antes del menú (el coalescer lo fusiona si cabe); una variable que decide la fuente del informe de agentes en vez de deducirla del puente; un vigilante de urgencia que cuenta fallos de Néctar y de los recordatorios y manda un aviso propio a Telegram con enfriamiento; y la guía manual de vuelta a Bird.

**Tech Stack:** Go 1.26, tests con `go test` en Docker.

**Spec:** `docs/superpowers/specs/2026-10-30-transicion-doble-canal-design.md` (commit 8f36501).

## Global Constraints

- Rama `feat/migracion-nectar`; sin merge a main ni push.
- No se toca la lógica del flujo ni sus validaciones, salvo el anuncio en los saludos de Bird (decisiones 4 y 10 del spec).
- Con las variables nuevas vacías o en su valor por defecto, el comportamiento es EXACTAMENTE el de hoy.
- Nada pide cambios a Néctar.
- Identificadores en inglés, comentarios en español, con la densidad del código vecino.
- Línea base de tests: solo fallan TestAskClientType_Particular_SkipsListToDocument, TestMedicationCheck_SanitasNoChannel_Escalates, TestMedicationCheck_SanitasWithChannel_SendsLink.
- Nunca `--no-verify` ni `LEFTHOOK=0`. No agregar al stage archivos sin seguimiento ajenos (`auditoria/`, `bin/`, `cmd/e2e/`, `docs/CASO-*`, `docs/MAPA-*`, `internal/api/test_handler.go`, `scripts/`, `seed-kpis`, `.claude/`, `docs/superpowers/plans/2026-10-29-ivr-recordatorio-fallido.md`).
- Ningún secreto ni número real en el repo; en ejemplos, marcadores.

## Review Focus

1. El anuncio NUNCA sale en una sesión `nectar:*`, ni con `post_confirmed=1`, ni con la variable vacía.
2. Saludo + anuncio no rompe el límite de 1024 del cuerpo interactivo: si no cabe, van separados (el coalescer ya lo decide; un test lo fija con un anuncio largo).
3. Con el puente encendido, la escalación, el retorno, el cierre y las notas internas de una sesión de Bird (clave `+57…`) no tocan Néctar.
4. El aviso urgente no inunda Telegram (enfriamiento) ni se pierde entre los `slog.Error` (formato propio, no pasa por el AlertHandler).
5. Con el puente apagado (vuelta a Bird), nada del vigilante dispara.

---

### Task 1: Anuncio de la línea nueva en los saludos de Bird

**Files:**
- Modify: `internal/config/config.go` (campo + lectura de `NEW_LINE_ANNOUNCEMENT`)
- Modify: `internal/statemachine/handlers/greeting.go` (`greetingHandler`, `outOfHoursHandler`)
- Test: `internal/statemachine/handlers/greeting_test.go`; test del coalescer con anuncio largo donde ya se prueba `CoalesceMessages`

**Interfaces:**
- Produces: `Config.NewLineAnnouncement string` (texto tal cual de `NEW_LINE_ANNOUNCEMENT`, recortado de espacios; vacío = apagado). Helper en `greeting.go`: `announcementFor(cfg *config.Config, sess *session.Session) string` que devuelve `""` si la variable está vacía o `session.IsNectarSession(sess.PhoneNumber)`.

Comportamiento:
- `greetingHandler`: si `post_confirmed != "1"` y `announcementFor` no es vacío, añade ANTES del `ListMessage` un `*sm.TextMessage{Text: anuncio}`. Con `post_confirmed=1` no hay anuncio.
- `outOfHoursHandler`: igual, `TextMessage` con el anuncio antes del `ListMessage`.
- No se toca el texto de bienvenida ni el menú. La fusión la hace `CoalesceMessages` (texto antes de lista → se antepone al body si cabe en 950; si no, quedan dos mensajes).

- [ ] **Step 1: Tests que fallan** — (a) sesión de Bird (`+573001112233`), anuncio puesto → los mensajes del resultado son `[TextMessage(anuncio), ListMessage]` en GREETING y en OUT_OF_HOURS; (b) sesión `nectar:55` → sin TextMessage, igual que hoy; (c) variable vacía → igual que hoy; (d) `post_confirmed=1` → sin anuncio; (e) `CoalesceMessages([Text(anuncio 300 runas), List(body = bienvenida real + "¿En qué puedo ayudarte hoy?")])` → 1 mensaje cuyo body empieza con el anuncio; con un anuncio de 600 runas → 2 mensajes; (f) config: `NEW_LINE_ANNOUNCEMENT` con espacios alrededor se recorta.
- [ ] **Step 2: Correrlos y ver que fallan.**
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: `./internal/statemachine/...` y `./internal/config/...` verdes; suite completa con solo la línea base fallando.**
- [ ] **Step 5: Commit** `feat(transicion): los saludos de Bird anuncian la línea nueva`

### Task 2: El informe de agentes no se deduce del puente

**Files:**
- Modify: `internal/config/config.go` (`AGENTS_SOURCE`)
- Modify: `cmd/server/main.go:518` (`SetAgentsInNectar`)
- Test: `internal/config/*_test.go`; test existente de `HandleBirdAgents` si lo hay, si no uno nuevo en `internal/api`

**Interfaces:**
- Produces: `Config.AgentsSource string` ∈ {`"bird"`, `"nectar"`}; vacío → `"bird"`; otro valor → error de arranque (mismo patrón que `parseProtocolEscaladoVersion`). Método `Config.AgentsInNectar() bool` = `AgentsSource == "nectar"`.
- `main.go`: `internalHandler.SetAgentsInNectar(nectarBridge != nil && cfg.AgentsInNectar())`.

- [ ] **Step 1: Tests que fallan** — config: vacío → bird; `nectar` → nectar; `NECTAR` (mayúsculas) → nectar; `foo` → error. Handler: con `agentsInNectar=false` y cliente de Bird falso, `GET /api/internal/agents/bird` NO devuelve 409.
- [ ] **Step 2: Correrlos y ver que fallan.**
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: Paquetes tocados verdes; suite completa con solo la línea base.**
- [ ] **Step 5: Commit** `feat(transicion): el informe de agentes muestra Bird aunque el puente esté encendido`

### Task 3: Fijar que una sesión de Bird no toca Néctar con el puente encendido

**Files:**
- Test: `internal/bird/nectar_bridge_test.go` (o el archivo vecino que ya prueba el desvío)
- Modify: solo si un test descubre una fuga (en ese caso, el arreglo mínimo en el archivo donde está la fuga)

**Interfaces:**
- Consumes: `bird.Client.SetNectar(NectarBridge)`, los métodos de `bird.Client` que tienen rama Néctar (todos los que llaman `nectarConversation`).

- [ ] **Step 1: Tests** — con un `NectarBridge` falso que registra llamadas y FALLA el test si lo llaman, y un servidor HTTP falso de Bird: `SendText`, `SendButtons`, `SendList`, `SendInternalText`, `SendTemplate`, la escalación (`EscalateToAgentWithSummary` o el nombre real), el retorno al bot y el cierre, todos con clave `+573001112233` → cero llamadas al puente. Además, `HasAvailableAgentsFor("+57…", "")` no consulta Néctar. Enumerar los métodos leyendo el código (todos los que llaman `nectarConversation`), no de memoria.
- [ ] **Step 2: Correrlos.** Si pasan, son tests de regresión: commit. Si alguno falla, es una fuga: arreglo mínimo, test verde.
- [ ] **Step 3: Suite completa con solo la línea base.**
- [ ] **Step 4: Commit** `test(transicion): una sesión de Bird no toca Néctar con el puente encendido` (o `fix(transicion): …` si hubo arreglo)

### Task 4: Aviso urgente por Telegram

**Files:**
- Create: `internal/monitor/urgent.go`, `internal/monitor/urgent_test.go`
- Modify: `internal/bird/proactive.go` (`LogProactiveConfigStop`), `internal/scheduler/tasks.go` (`logInDoubtSummary` y el conteo de recordatorios no enviados), el punto donde la escalación a Néctar devuelve error, y el cableado en `cmd/server/main.go`
- Modify: `internal/config/config.go` (umbrales)

**Interfaces:**
- Produces, en `internal/monitor`:
  - `type UrgentTrigger string` con constantes `TriggerNectarDown`, `TriggerProactiveStopped`, `TriggerRemindersUnsent`, `TriggerEscalationFailing`.
  - `type UrgentAlerter struct` con `NewUrgentAlerter(send func(ctx context.Context, text string) error, guidePath string, cooldown time.Duration, now func() time.Time) *UrgentAlerter`.
  - `(*UrgentAlerter) Fire(ctx context.Context, trigger UrgentTrigger, detail string)` — envía «🚨 URGENTE — volver a Bird», el disparador, el detalle y la ruta de la guía; por disparador, a lo sumo un aviso por `cooldown` (repite mientras siga disparando). Nil-safe (receptor nil = no hace nada).
  - `(*UrgentAlerter) Count(ctx context.Context, trigger UrgentTrigger, threshold int, window time.Duration, detail string)` — cuenta ocurrencias en una ventana deslizante y llama a `Fire` al llegar al umbral.
- El envío NO pasa por `slog.Error` (no lo agrupa el AlertHandler): usa `tg.Client.SendMessage` directo. Sin Telegram configurado o con el puente apagado, el alerter es nil.
- Enganche sin acoplar paquetes y SIN variables globales mutables (en el árbol no hay un patrón de hooks globales; no se inventa): una interfaz mínima `UrgentReporter` declarada donde se consume (`Fire`/`Count`), inyectada por los structs que ya existen y se cablean en `cmd/server/main.go` (`scheduler.Tasks`, `bird.Client` con un setter como `SetNectar`, el manejador de escalación). `LogProactiveConfigStop` es función de paquete: su llamador (que sí tiene el struct) dispara el aviso. Puntos:
  - `LogProactiveConfigStop` → `TriggerProactiveStopped` (Fire directo).
  - `logInDoubtSummary` y el registro de recordatorios no enviados de la corrida del día antes → `TriggerRemindersUnsent` si la corrida acumula ≥ `URGENT_UNSENT_THRESHOLD` (por defecto 10).
  - fallos de transporte o 5xx del cliente de Néctar → `TriggerNectarDown` con `Count` (por defecto 5 en 5 min).
  - error de escalación a Néctar → `TriggerEscalationFailing` con `Count` (por defecto 3 en 10 min).
- Config: `URGENT_ALERT_COOLDOWN_MIN` (por defecto 10), `URGENT_UNSENT_THRESHOLD` (10), `URGENT_NECTAR_DOWN_COUNT` (5), `URGENT_NECTAR_DOWN_WINDOW_MIN` (5), `URGENT_ESCALATION_FAIL_COUNT` (3), `URGENT_ESCALATION_FAIL_WINDOW_MIN` (10). Guía: `docs/GUIA-VOLVER-A-BIRD.md`.

- [ ] **Step 1: Tests que fallan** — `UrgentAlerter`: Fire envía un texto que contiene «URGENTE», el disparador y la guía; dos Fire del mismo disparador dentro del cooldown → 1 envío; tras el cooldown → 2; disparadores distintos no se bloquean entre sí; Count con umbral 3 en 5 min: 2 ocurrencias → 0 envíos, 3 → 1, ocurrencias viejas fuera de la ventana no cuentan; receptor nil no entra en pánico. Enganches: `LogProactiveConfigStop` llama al hook con `TriggerProactiveStopped`; la corrida del día antes con N ≥ umbral de no enviados llama al hook; con el hook sin poner nada cambia.
- [ ] **Step 2: Correrlos y ver que fallan.**
- [ ] **Step 3: Implementar** (el implementador localiza el punto exacto de error de transporte del cliente de Néctar y de la escalación leyendo el código, y lo reporta).
- [ ] **Step 4: Paquetes tocados verdes, `-race` en `./internal/monitor/...`, suite completa con solo la línea base.**
- [ ] **Step 5: Commit** `feat(transicion): aviso urgente por Telegram para volver a Bird`

### Task 5: Guía de vuelta a Bird y documentación

**Files:**
- Create: `docs/GUIA-VOLVER-A-BIRD.md`
- Modify: `CLAUDE.md` (sección NÉCTAR), `.env.example`

Contenido de la guía (para alguien del equipo, paso a paso, en español):
1. Cuándo usarla: al recibir «🚨 URGENTE — volver a Bird».
2. Vaciar `NECTAR_BASE_URL` en el `.env` del servidor y reiniciar el bot (comando real del despliegue: el implementador lo toma de `docs/` o `Makefile`/compose existentes; si no lo encuentra, lo deja como marcador explícito).
3. Verificar: `/health` sin `nectar_host`; en logs, el arranque NO dice «Néctar activo».
4. Qué pasa con lo abierto en Néctar: el bot ya no responde esas conversaciones (sus claves `nectar:*` no son teléfonos: el envío por Bird se corta sin costo, `utils.IsE164` en `internal/bird/client.go`); los agentes de Néctar avisan al paciente con el texto sugerido (incluirlo) que escriba a la línea de siempre; los pendings de confirmación `nectar:*` siguen a la llamada IVR por su `ContactPhone`. [El implementador VERIFICA esto último en el código con el puente apagado y, si no se cumple, lo reporta como concern en vez de afirmarlo.]
5. Recordatorios: con el puente apagado salen por las plantillas de Bird de siempre.
6. Volver a Néctar cuando se resuelva.

- `CLAUDE.md` NÉCTAR: líneas cortas sobre `NEW_LINE_ANNOUNCEMENT`, `AGENTS_SOURCE`, el aviso urgente y la guía.
- `.env.example`: las variables nuevas con su valor por defecto y comentario.

- [ ] **Step 1: Escribir la guía, verificando cada afirmación contra el código.**
- [ ] **Step 2: CLAUDE.md y .env.example.**
- [ ] **Step 3: Commit** `docs(transicion): guía para volver a Bird y variables nuevas`

### Task 6: Con el puente apagado, una conversación de Néctar no toca Bird ni se procesa

Añadida por el controlador (Ruling 2 del ledger): al volver a Bird (`NECTAR_BASE_URL` vacía) las claves `nectar:*`
siguen llegando a Bird — `sendViaBsuid` hace un PATCH upsert de contacto con `whatsappusername=nectar:<id>`
(`internal/bird/bsuid.go:52`), `CreateConversationForPhone` crea y asigna una conversación de Bird
(`internal/bird/client.go:631`), y `POST /api/webhooks/nectar` sigue procesando avisos (`cmd/server/main.go:583`): el
bot puede confirmar en SIESA y no tiene cómo contestar.

**Files:**
- Modify: `internal/bird/nectar_bridge.go` (y los métodos de `client.go`/`proactive.go` que hoy caen al camino de Bird con una clave `nectar:*` sin puente)
- Modify: `internal/api/nectar_handler.go` (`HandleNectarNotice`), cableado en `cmd/server/main.go`
- Test: `internal/bird/nectar_bridge_test.go`, `internal/api/*nectar*_test.go`

**Interfaces:**
- Produces: `bird.ErrNectarBridgeOff` — envuelve `bird.ErrPermanentSend` (quien llama ya suprime reintentos ante un permanente). Todo método de `bird.Client` que reciba una clave con prefijo `nectar:` y `c.nectar == nil` devuelve `ErrNectarBridgeOff` (o el cero/false que corresponda en los que no devuelven error) SIN ninguna petición HTTP a Bird. Un solo predicado decide (junto a `nectarConversation`), no una copia por método.
- `HandleNectarNotice` con el puente apagado: responde `503 {"error":"bridge_off"}` sin verificar más ni procesar ni escribir en el WAL, y loguea Warn una vez por aviso (sin PII). Con el puente encendido, igual que hoy. El handler conoce el estado del puente por inyección (como `SetAgentsInNectar`).
- El replay del WAL de filas de Néctar ya guardadas, con el puente apagado: sus respuestas caen en `ErrNectarBridgeOff`; documentar en el reporte qué ocurre (no se cambia el replay).
- No cambia nada para claves `+57…` ni con el puente encendido.

- [ ] **Step 1: Tests que fallan** — puente nil + clave `nectar:55`: cada método de `bird.Client` con rama Néctar (la misma lista de 15 de `TestBirdSessionNeverTouchesNectarWithBridgeOn`, más `SendProactiveTemplate` con clave `nectar:55`) hace CERO peticiones al stub de Bird y, los que devuelven error, devuelven un error con `errors.Is(err, bird.ErrNectarBridgeOff)` y `bird.IsPermanentSendError(err)`. Clave `+573001112233` con puente nil: va a Bird como hoy. Handler: puente apagado → 503 `bridge_off`, nada en el WAL ni en el pool; puente encendido → camino de hoy.
- [ ] **Step 2: Correrlos y ver que fallan.**
- [ ] **Step 3: Implementar.**
- [ ] **Step 4: Paquetes tocados verdes, `-race` en `./internal/bird/...`, suite completa con solo la línea base.**
- [ ] **Step 5: Commit** `fix(transicion): sin puente, una conversación de Néctar no toca Bird ni se procesa`
