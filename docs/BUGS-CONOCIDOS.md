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
