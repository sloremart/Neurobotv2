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
