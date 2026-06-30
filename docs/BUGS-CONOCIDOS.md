# Bugs conocidos y fixes propuestos

> Registro de bugs detectados pero **aún no corregidos**, con análisis a fondo y el fix propuesto
> para implementarlos después. Cada entrada es autocontenida (un agente sin contexto debe poder
> entender el problema y aplicar el fix). Al corregir uno, mover su estado a ✅ y enlazar el PR.

---

## BUG-001 — El replay del WAL re-encola TODO el backlog de golpe → backpressure → thrashing

- **Estado:** 🔴 Pendiente (documentado, sin corregir)
- **Severidad:** Alta — provoca **pérdida de mensajes de pacientes** al arrancar con un backlog grande.
- **Detectado:** 2026-06-26, depurando "el bot no responde" en prod tras un reinicio con la BD viva.
- **Componentes:** `internal/worker/pool.go` (`StartStaleReplay` / `replayStale` / `Enqueue`),
  `internal/repository/local/inbox_repo.go` (`FindPendingOlderThan`), tabla `message_inbox` (WAL).

### Síntoma observable
Tras un reinicio (o un deploy) cuando hay muchos mensajes `pending` acumulados en el WAL, en los logs aparece:
```
WARN  "stale replay: re-encolando mensajes 'pending' atascados"  count:963
ERROR "backpressure: overflow limit reached, dropping message"   (cientos de veces)
```
y los mensajes de los pacientes (incluidos los que escriben en ese momento) **se descartan en silencio**
→ "el bot no responde". El problema **se repite cada 5 minutos** y no converge solo.

### Cuándo aparece
Cuando la cantidad de mensajes `pending` en `message_inbox` supera la capacidad efectiva de la cola.
El backlog se acumula si en algún periodo el bot **recibió pero no procesó** mensajes: bot caído/degradado,
túnel ngrok robado (§13.1 de la guía de auditoría), o un episodio previo de backpressure (que es
auto-reforzante: dropear deja el mensaje en `pending`, que alimenta el siguiente flood).

### Análisis a fondo (la mecánica exacta)
1. `StartStaleReplay` corre un ticker cada `staleReplayInterval = 5 min` y llama a `replayStale`.
2. `replayStale` hace `src.PendingOlderThan(ctx, staleReplayMinutes=10)`, que ejecuta
   `SELECT ... FROM message_inbox WHERE status='pending' AND received_at < NOW()-10min` **SIN `LIMIT`** →
   devuelve **TODO** el backlog (p.ej. 963 filas).
3. Luego hace un loop tight: `for _, m := range msgs { p.Enqueue(m) }` — encola **todos** de inmediato.
4. `Enqueue` es no-bloqueante: si la cola (`WORKER_QUEUE_SIZE`, default **100**) está llena, lanza una
   goroutine de overflow, pero el overflow está topado en `maxOverflowGoroutines = 20`. Al llegar al tope,
   **descarta** el mensaje (`backpressure: overflow limit reached, dropping message`).
5. Con 963 mensajes y capacidad efectiva ~120 (100 cola + 20 overflow), **~840 se descartan**.
6. Los descartados **NO se procesan** → siguen en estado `pending` en el WAL (solo `processMessage`
   marca `done`). El **siguiente ciclo (5 min)** los vuelve a traer y a volcar → se vuelven a dropear.
   **Thrashing:** nunca drena, y cada ciclo inunda la cola y dropea también los mensajes vivos del momento.

> Causa raíz en una frase: **`replayStale` no tiene control de flujo** — re-encola el backlog completo en
> una ráfaga, ignorando la capacidad de la cola; con backlog > cola se auto-DoSea y no converge.

### Impacto
- Pérdida de mensajes entrantes (los del backlog y los vivos atrapados en el flood) → pacientes sin respuesta.
- Se auto-perpetúa cada 5 min hasta intervención manual.
- Empeora con más carga.

### Mitigación temporal (qué hacer AHORA si ocurre)
1. **Vaciar el backlog atascado** (detiene el flood):
   ```sql
   UPDATE message_inbox SET status='done', processed_at=NOW() WHERE status='pending';
   ```
   (o `DELETE FROM message_inbox WHERE status='pending'`). Se pierden esos mensajes viejos, pero se corta el thrashing.
2. O **escalar a high-load** (`scale-up.sh`): cola 500 + 50 workers absorbe mejor, pero **no es la solución**
   (un backlog > 500 volvería a dropear). Es paliativo.

### Fix propuesto (correcto)
Hacer el replay **acotado y consciente de la capacidad**, para que **drene el backlog gradualmente sin
descartar** y **converja**. Tres piezas:

1. **Limitar por ciclo + respetar el espacio libre de la cola.** Antes de encolar, calcular cupo libre
   con `QueueStats()` (`free = capacity - size`, menos un margen para el tráfico vivo). Encolar **solo
   hasta `free`** mensajes; el resto se queda `pending` y se toma en el siguiente ciclo. Garantiza **cero
   drops** desde el replay y convergencia monótona.
   - Requiere que `FindPendingOlderThan` acepte un `limit` → `... ORDER BY received_at ASC LIMIT ?`
     (oldest-first = FIFO justo). Firma nueva: `PendingOlderThan(ctx, minutes, limit int)`.
2. **Drenado más ágil mientras haya backlog (opcional).** Si en un ciclo se encoló el máximo (había más de
   los que cabían), reprogramar el próximo replay en ~30 s en vez de 5 min, hasta vaciar; luego volver a 5 min.
   Acelera la recuperación sin ráfagas.
3. **Tope de antigüedad / descartar lo realmente viejo (opcional pero recomendado).** Mensajes `pending`
   con `received_at` mayor a, p.ej., **2–3 horas** casi seguro son de pacientes que ya se fueron; marcarlos
   `discarded` para que un incidente puntual no deje un backlog que persiga al bot indefinidamente.
   (Decisión de negocio: definir el umbral.)

**Por qué es correcto:** la pieza (1) elimina la causa raíz (los drops del replay) porque nunca se encola
más de lo que cabe; el backlog drena en lotes a lo largo de varios ciclos. (2) y (3) son robustez:
recuperación más rápida y cota superior al backlog histórico.

### Archivos a tocar (esbozo)
- `internal/repository/local/inbox_repo.go` — `FindPendingOlderThan(ctx, minutes, limit)` con `LIMIT ?`
  y `ORDER BY received_at ASC`; (opcional) un método `DiscardPendingOlderThan(ctx, hours)`.
- `internal/worker/pool.go` — `replayStale`: leer `free` de `QueueStats`, pedir `min(free, batchMax)` al
  repo, encolar solo eso; (opcional) lógica de intervalo adaptativo en `StartStaleReplay`. Nueva const
  `staleReplayBatchMax` (p.ej. 50). Ajustar la interfaz `StalePendingSource`.
- `cmd/server/main.go` — el adaptador `inboxReplaySource.PendingOlderThan` debe pasar el `limit`.

### Tests a agregar
- `replayStale` con backlog > capacidad: encola solo `free`, **no dropea**, y en N ciclos drena todo.
- Idempotencia: re-encolar los mismos IDs no duplica (dedup + WAL `done`).
- (Si se agrega) descarte por antigüedad: marca `discarded` los > umbral y no los vuelve a tomar.

### Riesgos / notas
- No cambiar la semántica de `Enqueue` (sigue siendo no-bloqueante para el webhook en caliente).
- Mantener oldest-first para no postergar indefinidamente los más viejos.
- El dedup map (`recentMessages`) ya libera el claim al dropear (fix M7), así que el re-encolado funciona.

---

## BUG-002 — Colisión `PK_citas` al agendar: se ofrece un médico+hora ya ocupado

- **Estado:** ✅ Resuelto — implementado (Capas 1-3). Antes: 🔴 recurrente (2 casos en <1h el 2026-06-26).
- **Severidad:** Alta — el paciente **no obtiene la cita** (parcial en multi-grupo, o ninguna) y recibía
  un error genérico de **callejón sin salida**.
- **Detectado:** 2026-06-26 por el auditor; confirmado en 4 fuentes (código, BD del bot, Bird, schema SIESA).
- **Componentes:** `internal/repository/siesa/schedule_repo.go` (`FindAvailableSlots`),
  `internal/repository/siesa/appointment_repo.go` (`Create`), `internal/statemachine/handlers/slots.go`
  (booking + `bookingFailedHandler`), `internal/domain/errors.go` (`ErrSlotTaken`).

### Síntoma observable
```
ERROR create_appointment_create_failed: Violation of PRIMARY KEY 'PK_citas' duplicate (60,2026-07-03,13:00,pm,P,...)
```
y al paciente: *"Ocurrió un error al crear la cita. Por favor intenta más tarde."* + auto-cierre.

### Causa raíz
`PK_citas` es única por **(cod_medi, fecha, hora, meridiano, estado, horacan, CodGrupo)**; para una cita
activa `estado='P'`, `horacan='--:--'` y `CodGrupo=0` son constantes → la PK se reduce de hecho a
**(cod_medi, fecha, hora, meridiano)**. Pero la búsqueda de slots (`FindAvailableSlots`) solo validaba
`pmd.IdCita IS NULL` — **por fila** de `programacion_medico_detalle`. Cuando un médico tiene la misma
hora libre en más de una fila de detalle (agendas/consultorios solapados, o el mismo paciente eligiendo
el mismo médico+hora para dos grupos), el slot se ve libre pero el `INSERT` en `citas` colisiona con la PK.

Dos disparadores, mismo mecanismo:
- **Caso 1 (multi-grupo, misma sesión):** se agenda el grupo 1 (médico 60 @ 03/07 13:00); al buscar el
  grupo 2 se vuelve a ofrecer el MISMO médico+hora → 2º INSERT choca. (Bird conv `cf402578`, sess `c39bd23a`.)
- **Caso 2 (cita ajena):** el médico 16 ya tenía una 'P' a las 09/07 14:40 en otra fila/agenda; el slot
  se ofreció libre y el INSERT chocó. (Bird conv `e821ddd8`, sess `717f3634`.)

Además, el orden en `Create` es **INSERT citas → luego UPDATE pmd**: la colisión aflora como violación de
PK en el INSERT (no como `slot_taken` del UPDATE), y el handler no la reconocía → caía al error genérico.

### Fix implementado (3 capas)
1. **PREVENIR — `FindAvailableSlots`:** `AND NOT EXISTS (SELECT 1 FROM citas c WHERE c.cod_medi=pmd.Medico
   AND c.fecha=CAST(pmd.Fecha AS DATE) AND c.hora=CONVERT(VARCHAR(5),pmd.Fecha,108) AND c.estado='P')`.
   Alinea la disponibilidad con la PK. *Seek* por el prefijo de `PK_citas` (cod_medi→fecha→hora) — sin
   índice nuevo. Validado contra la BD local (excluye el slot en cuanto el médico tiene la 'P').
2. **DETECTAR — `Create`:** la violación de PK (mssql 2627/2601, `isUniqueViolation`) y el `UPDATE pmd`
   con `RowsAffected==0` devuelven ambos `domain.ErrSlotTaken` (sentinel tipado, en vez de strings frágiles).
3. **RECUPERAR — handler:** `errors.Is(err, domain.ErrSlotTaken)` → `booking_failure_reason="slot_taken"`
   → `bookingFailedHandler` **avisa al paciente y re-busca horarios frescos** (`StateSearchSlots`, ahora
   con el filtro de la Capa 1) → *"Ese horario ya no está disponible. Te muestro los horarios actualizados..."*.

### Por qué es correcto
La Capa 1 elimina la causa raíz (no se ofrecen slots imposibles). Las Capas 2-3 cubren la **carrera
residual** (otro paciente reserva entre que se muestra la lista y se confirma) con la UX correcta: aviso
+ lista actualizada, sin callejón. Reusa estados existentes (`StateSearchSlots`, `bookingFailedHandler`);
sin estados ni índices nuevos. `Create` ya corre en transacción con `defer tx.Rollback()`, así que al
devolver `ErrSlotTaken` antes del commit no queda cita ni slot a medias.

### Tests
- `TestIsUniqueViolation` (2627/2601 → true; envuelto con %w; otros/nil → false) y `TestErrSlotTaken_Identity`.
- `TestCreateAppointment_SlotTakenError`: el handler enruta `ErrSlotTaken` → `slot_taken` → re-búsqueda.
- Validación SQL del filtro contra la BD SIESA local (demostración ANTES/DESPUÉS con `ROLLBACK`).

---

## BUG-003 — Escalación "empty conversation ID": el lookup por teléfono solo busca 50 conversaciones

- **Estado:** ✅ Resuelto — implementado. Antes: 🔴 sistémico (handoff a agente roto al ~93%).
- **Severidad:** Alta — la transferencia a agente humano **falla**; el paciente que pide hablar con una
  persona no es atendido.
- **Detectado:** 2026-06-26 por el auditor; causa raíz confirmada vía API de Bird.
- **Componentes:** `internal/bird/client.go` (`LookupConversationByPhone`), cadena de resolución de
  `conversation_id` en `internal/statemachine/handlers/escalation.go`.

### Síntoma observable
```
ERROR escalation failed error='empty conversation ID' conversation_id=''
```
y en prod: `conversation_lookup_not_found` = 368 vs `conversation_lookup_success` = 28 (~93% de fallos).

### Causa raíz (confirmada con la API de Bird)
`LookupConversationByPhone` pagina `/workspaces/{wid}/conversations?channelId=...&limit=50` y empareja por
teléfono. Leía el cursor de paginación de un objeto **anidado** `pagination.nextPageToken`, pero Bird lo
devuelve **en la RAÍZ** de la respuesta (`top keys: ['nextPageToken','results']`). Como el campo anidado
siempre venía vacío, el loop cortaba tras la **página 1** → solo se buscaba en las **50 conversaciones más
recientes** (ordenadas por `createdAt` DESC, ~50% de ellas cerradas e irrelevantes). Un paciente
recurrente, o cualquiera en un pico de tráfico, queda fuera de esas 50 → no se encuentra su conversación.

Agravante: el cache de `conversation_id` es **in-memory con TTL 4h** → un reinicio del bot lo vacía, y a
partir de ahí toda escalación depende del lookup (roto).

### Descartado (vía API de Bird, para no perder tiempo a futuro)
- `/conversations` **no** acepta filtro por teléfono (`identifier=`/`phonenumber=`/`participant=` se ignoran).
- Los mensajes del canal (`/channels/{cid}/messages?phonenumber=`) **no** traen `conversationId` (ni en
  `context`, `reference` ni `meta`). → La única vía es paginar `/conversations` correctamente.

### Fix implementado
- Leer `nextPageToken` de la **raíz** de la respuesta (no de `pagination`) → la paginación funciona.
- Ampliar el límite de páginas de 5 a **10** (500 conversaciones) para cubrir picos; cada página es 1
  llamada y la escalación no es sensible a latencia. Al primer acierto se cachea (`CacheConversationID`),
  así que repeticiones del mismo teléfono no vuelven a paginar.

### Tests
- `TestLookupConversationByPhone_Pagination`: la conversación buscada solo está en la página 2 y solo se
  alcanza siguiendo el `nextPageToken` de la raíz → verifica que se piden 2 páginas y se encuentra.

### Actualización 2026-06-30 — SEGUNDA causa (también resuelta)
Tras desplegar el fix de paginación, el auditor confirmó que "empty conversation ID" **seguía** de forma
**intermitente** (sesiones 5823 ×5, 8637, 4491, 7283). Causa raíz adicional: el emparejamiento del lookup
usaba **igualdad exacta de string** (`p.IdentifierValue == phone`). Bird puede devolver el `identifierValue`
**sin `+`** (o sin prefijo de país), así que en los casos que llegan al lookup (caché y sesión vacías: tras
reinicio, sesión reabierta o flujo proactivo) el match fallaba. **Fix (commit `3c4d087`):** `utils.SamePhone`
compara solo dígitos y por los últimos 10 (número nacional, único). Tests: `TestSamePhone`,
`TestLookupConversationByPhone_PhoneFormatTolerant`. **Requiere deploy** (prod corría build de 08:53 sin este fix).

### Mejora futura (opcional, no bloqueante) → ver BUG-006
- Persistir el `conversation_id` (p. ej. en la sesión/BD local) para sobrevivir reinicios y evitar el
  lookup por completo; o cachearlo en cada inbound si Bird llega a incluirlo en el webhook.
- Si aun así el lookup no encuentra conversación (paciente sin conversación indexable), **crear/abrir la
  conversación explícitamente** antes del handoff y emitir un terminal `escalacion/escalation_no_channel`
  para medir el residual en vez de fallar en silencio. (Pendiente — ver BUG-006.)

---

## BUG-004 — Víctimas de reinicio: estado de sesión no persistido al apagar/redeploy

- **Estado:** 🔴 Pendiente (documentado, sin corregir)
- **Severidad:** Media — un paciente a mitad de flujo pierde su avance y/o tarda 10-15 min en ser re-atendido.
- **Detectado:** 2026-06-30 por el auditor (ciclo 28), tras el redeploy ~08:53.
- **Componentes:** `internal/worker/pool.go` (`worker`, `processMessage`), `cmd/server/main.go` (shutdown),
  `internal/worker/pool.go` (`StartStaleReplay` — ver también BUG-001).

### Síntoma observable
Al redeploy/apagado, ráfaga simultánea de:
```
ERROR log event failed / save_state_error / save_state_fallback_complete_failed / inbox mark done failed
      error="context canceled"
```
Víctima ejemplo: +57***5143 (session 914734c9) en `UPLOAD_MEDICAL_ORDER` → su estado **pudo no persistir**.

### Causa raíz
El worker procesa cada mensaje con el **`ctx` cancelable de la app** (`worker(ctx)` → `safeProcess(ctx,msg)`,
`pool.go`). El `select { case <-ctx.Done(): return }` evita tomar mensajes **nuevos**, pero al mensaje que
**ya está a medio procesar** le aborta TODAS las escrituras en vuelo (`RenewTimeout`, `SaveState`, `MarkDone`)
con `context canceled` → el cambio de estado no se persiste.

Red de seguridad actual y su hueco: el WAL re-encola los `pending` no marcados `done`, PERO el replay es
**solo periódico** (ticker cada 5 min, edad > 10 min) — **no hay replay inmediato al arrancar** → la víctima
recupera **10-15 min después**, no al instante. Riesgo adicional: respuesta ya enviada pero estado no guardado
(inconsistencia).

### Fix propuesto (A + B)
- **A (prevención):** procesar cada mensaje con un contexto **desacoplado del shutdown** para la sección
  crítica: `context.WithoutCancel(parentCtx)` + timeout acotado (~45s). El worker sigue dejando de tomar
  mensajes nuevos al `ctx.Done()`, pero el que ya empezó **completa** `SaveState`/`MarkDone` dentro de la
  ventana de gracia (`Stop` ya espera al `wg`; `main` da 20s). Convierte "context canceled" en "completado".
- **B (recuperación rápida):** barrido de replay **one-shot al arrancar** que re-encole los `pending` sin
  esperar 10-15 min. **OJO:** hacerlo **por lotes/throttle** para no reproducir BUG-001 (re-encolar todo de
  golpe → backpressure). Idempotente (dedup + MarkDone).
- Test: "shutdown a mitad de mensaje" → el estado SÍ persiste; replay de arranque por lotes sin drops.

---

## BUG-005 — `agent reminder send failed: conversation not active` (recordatorio a conversación cerrada)

- **Estado:** ✅ Resuelto — implementado (commit `456e274`). Requiere deploy.
- **Severidad:** Media — el recordatorio al agente (SLA de escalación) no se entrega; ruido de error recurrente.
- **Detectado:** 2026-06-30 por el auditor (al actualizar el script: nuevo stream de error visible).
- **Componentes:** envío del recordatorio de escalación (NotificationManager / worker), `internal/bird/client.go`
  (`trySendToConversation` / Conversations API).

### Síntoma observable
```
ERROR agent reminder send failed  session_id=bc365c59... conversation_id=6f9528b2...
      error="conversation not active: {...\".status\":[\"conversation status is not active\"]}"
```
Se repite cada ~1 min sobre la misma sesión.

### Causa raíz (confirmada)
`checkEscalatedSessions` (`internal/session/manager.go`) enviaba el recordatorio con
`deps.BirdClient.SendInternalText(...)`; al fallar hacía `continue` **sin incrementar `RemindersSent`**, así
que la misma sesión seguía elegible y reintentaba en cada tick (1/min). La conversación de Bird estaba
**cerrada** (el agente la cerró) → `sendToConversation` devuelve `ErrConversationNotActive` (HTTP 422) →
el recordatorio **nunca** tendría éxito → bucle hasta la expiración a las 6h.

### Fix implementado (commit `456e274`)
- Ante `errors.Is(err, bird.ErrConversationNotActive)`, dar por terminada la escalación: `MarkAbandoned`
  + `escalations.Expire` + emit `escalacion/agent_closed` (reason `conversation_inactive`) — la conversación
  cerrada por el agente ES la señal de que el chat terminó. Detiene el bucle y refleja la realidad.
- Test `TestCheckEscalatedSessions_ClosesOnInactiveConversation` (la sesión se cierra, no se reintenta).

---

## BUG-006 — Escalación residual: garantizar canal antes del handoff (hardening)

- **Estado:** 🔴 Pendiente (documentado) — complementa BUG-003 (ya resuelto en sus dos causas).
- **Severidad:** Baja/Media — caso borde tras los fixes de BUG-003; hoy cae a `FallbackMenu` (no crash).
- **Componentes:** `internal/statemachine/handlers/escalation.go`, `internal/bird/client.go`,
  `internal/observability/tracer.go` (catálogo).

### Qué falta
Si tras caché + sesión + `LookupConversationByPhone` (con paginación y match tolerante ya corregidos) el
`conversation_id` **sigue vacío** (paciente sin conversación indexable en Bird), la escalación no completa.

### Fix propuesto
- **Crear/abrir explícitamente** la conversación en Bird (Conversations API) cuando el lookup falle, y usar
  ese id en el handoff.
- Emitir un terminal nuevo `escalacion/escalation_no_channel` (catálogo en `tracer.go`) para **medir** el
  residual en el funnel en vez de que sea invisible.
- Requiere verificación contra la API real de Bird (qué endpoint crea conversación en este workspace/canal).

---

## BUG-007 — Handoff de escalación falla en conversaciones REABIERTAS (feed item cerrado)

- **Estado:** ✅ Resuelto — implementado (commit `2b226c2`). Requiere deploy.
- **Severidad:** Media — la transferencia a agente **falla** para pacientes que ya tuvieron un chat cerrado y vuelven a escribir.
- **Detectado:** 2026-06-30 por el auditor (ciclo 37, post-deploy del fix de teléfono). Causa raíz confirmada contra la API de Bird (read-only).
- **Componentes:** `internal/bird/client.go` (`searchFeedItem`, `AssignFeedItem`).

### Síntoma observable
```
ERROR escalation failed error='assign feed item: no feed item found after retries' conversation_id=7e92988c... (NO vacío)
```
Distinto de "empty conversation ID": aquí el `conversation_id` SÍ se resolvió.

### Causa raíz (confirmada contra Bird)
El handoff busca el "feed item" (ticket de inbox) por `conversationId` y solo aceptaba los **NO cerrados**
(`searchFeedItem`: `if !item.Closed`). Consulta read-only a Bird de la conversación afectada:
`total feed items: 1, closed=True`. Es una **conversación reabierta**: el ticket viejo se cerró al
completar el chat anterior (`CloseFeedItems`), y al re-escalar `searchFeedItem` devolvía `""` → tras 4
reintentos → error. **No era race/latencia** (la hipótesis inicial): más reintentos nunca ayudarían
porque el ticket existe pero está cerrado, y Bird no permite asignar un feed item cerrado a un equipo.
Nota: el fix de teléfono (BUG-003) destapó este modo — antes estos casos morían en "empty conversation ID".

### Fix implementado (commit `2b226c2`)
- `searchFeedItem` ahora, si no hay ticket abierto pero sí uno cerrado, lo devuelve como fallback con
  `closed=true`.
- `AssignFeedItem`, cuando el ticket está cerrado, lo **REABRE** (`PATCH closed:false`) en el MISMO PATCH
  de asignación (mismo endpoint/campo que `CloseFeedItems` usa para cerrar).
- Test `TestAssignFeedItem_ReopensClosed`.

### Relacionado (pendiente)
- **BUG-006 / "empty conversation ID" residual:** el auditor confirmó (ciclo 37, sesión f1f140ad) que
  AÚN ocurre el caso donde NO se encuentra ninguna conversación (ni reabierta) → hay que crear/abrir la
  conversación antes del handoff. Sigue ABIERTO.
- **FLUJO-INCOMPLETO (ciclo 37):** cuando el handoff falla, la escalación queda estancada en
  `agent_reminder_sent` sin terminal. Verificar si es real o artefacto de la ventana del detector
  (escalaciones de días previos cuyo terminal `escalated` queda fuera del `from=hoy`); si es real,
  garantizar que TODO fallo de handoff lleve la sesión a un terminal.
