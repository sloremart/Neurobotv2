# Incidente 28-jul-2026 (recursión "Asesor") — datos falsos y plan de limpieza

> Bug: recursión mutua `TryStart↔inject` disparada por la palabra "Asesor" durante el registro.
> Fix en commit `eb8c3f1` (28-jul 11:24). Ventana del incidente en prod: **09:46–11:18 (-05)**.
> Este doc inventaría los datos falsos que dejó el bucle y el SQL para limpiarlos.
> **Verificado contra prod** vía endpoints internos (flow-trace, events, sessions) el 04-ago-2026.

---

## 1. Qué pasó con los datos (verificado)

Caso afectado — **una sola conversación**:

| Dato | Valor |
|---|---|
| Sesión / trace | `005d66e4-894f-4cae-ab57-398427d18331` (`trace_id = sess:005d66e4-…`) |
| Teléfono | `+573104762530` |
| Paciente (REAL) | ALEJANDRO BAQUERO NARIÑO, CC `19224046`, autoid final **113980** |

Línea de tiempo (hora local -05):

- **09:35–09:57** — el paciente navega y llena el registro normalmente (paciente real, datos reales).
- **09:58:55–10:03:48** — el mensaje venenoso queda pending en el WAL; cada crash+restart (~10-25s)
  lo re-procesa y **completa el registro otra vez**: **20 eventos `patient_created`**.
  ✅ **VERIFICADO (05-ago, backup de prod del 04-ago): NO se crearon duplicados en `sis_paci`.**
  El get-or-create de `PatientRepo.Create` (`patient_repo.go:153-162`, clave única
  `nro_historia+tipo_id`) reutilizó el `autoid=113980` en los 20 replays siguientes; la secuencia
  de autoids 113970–113992 es continua (nada se insertó ni se borró). Los 20 eventos son humo en
  `flow_events`, no filas en SIESA.
- **10:04–10:15** — el paciente reintenta manualmente entre crashes; a las 10:15:11 `patient_found`
  (su cédula ya existe: el PRIMER replay sí insertó la fila real) y queda identificado con `autoid=113980`.
- **10:16:56–10:30:11** — el bucle sigue con el paciente ya identificado: **60 eventos
  `contract_resolved`** duplicados (flujo `entidad`), ya sin tocar SIESA.
- **10:30:26** — escalación real a agente (`escalated_to_agent`), expira a las 12:40. Legítima.

Colaterales del apagón (NO son datos falsos, no borrar):

- **121 `session_abandoned`** (flujo `infra`) en la ventana: otros pacientes reales abandonados
  porque el bot estaba caído. Reflejan la realidad del incidente.
- `chat_events` del teléfono afectado (162 en el día, ~37 `message_sent` en ventana): mezcla de
  interacción real del paciente con ecos de reinicio. **Recomendación: no tocarlos** — son la
  "película" forense y su peso en KPIs es marginal.
- `ai_recovery_monthly`: ✅ verificado 05-ago — la tabla está VACÍA (con `AI_RECOVERY_MONTHLY_LIMIT`
  sin tope, `TryReserve` retorna sin escribir). El bucle no tocó las métricas de recuperación IA.
- `escalations`: ✅ verificado — 1 sola fila para el teléfono (la escalación real de las 10:30,
  outcome expired). El bucle no infló las métricas de escalación.
- No se creó **ninguna cita** falsa (cero eventos de `agendar` en la traza del bucle).
- ⚠️ **06-ago**: se descubrió una SEGUNDA traza afectada — la del paciente que escribió "Asesor"
  (`sess:002b2acb…`) — con **10.32 MILLONES** de `ai_recovery_started` falsos. Ver §4b.

## 2. Impacto en KPIs si no se limpia

- ~~`sis_paci` (SIESA)~~ — **descartado 05-ago**: verificado contra el backup de prod del 04-ago,
  no hay duplicados (ver §1). SIESA no necesita limpieza.
- **`flow_events` + `flow_daily_stats` (neuro_bot)**: el 28-jul aparece con +19 registros
  completados (funnel `registro`) y +59 resoluciones de contrato (funnel `entidad`) → infla la
  conversión de registro del día y las series del dashboard. **Es lo único a limpiar.**

---

## 3. SIESA — ✅ SIN ACCIÓN NECESARIA

Verificado el 05-ago contra el backup de prod `2026_08_04_180000`: la cédula `19224046` tiene UNA
sola fila (`autoid=113980`, nro_historia `CC19224046`) con 1 cita, y la secuencia de autoids
alrededor es continua. El get-or-create del bot absorbió los 20 re-registros del bucle.
Query de re-verificación (solo lectura), por si se quiere confirmar en prod directamente:

```sql
SELECT autoid, nro_historia, primer_nom, primer_ape
FROM sis_paci WITH (NOLOCK) WHERE num_id = '19224046' ORDER BY autoid;
-- esperado: 1 fila (113980)
```

## 4. Limpieza — neuro_bot (MySQL, en el server de prod)

```sql
-- Paso 1: VERIFICAR (esperado EXACTO, confirmado en la réplica del 04-ago:
--          patient_created 20, contract_resolved 60)
SET @tr = 'sess:005d66e4-894f-4cae-ab57-398427d18331';
SELECT step, COUNT(*) FROM flow_events
WHERE trace_id = @tr
  AND created_at >= '2026-07-28 09:58:50' AND created_at <= '2026-07-28 10:30:15'
GROUP BY step;

-- Paso 2: BORRAR duplicados conservando 1 patient_created (el registro real ocurrió)
-- y 1 contract_resolved
SELECT MIN(id) INTO @keep_pc FROM flow_events WHERE trace_id=@tr AND step='patient_created';
SELECT MIN(id) INTO @keep_cr FROM flow_events WHERE trace_id=@tr AND step='contract_resolved';
DELETE FROM flow_events
WHERE trace_id = @tr
  AND created_at >= '2026-07-28 09:58:50' AND created_at <= '2026-07-28 10:30:15'
  AND step IN ('patient_created','contract_resolved')
  AND id NOT IN (@keep_pc, @keep_cr);

-- Paso 3: recalcular el rollup del día (RollupDay es idempotente, pero hay que borrar
-- primero el agregado viejo para que desaparezcan los combos que quedaron en cero)
DELETE FROM flow_daily_stats WHERE day = '2026-07-28';
INSERT INTO flow_daily_stats (day, flow, step, outcome, reason, cnt)
SELECT DATE(created_at), flow, step, outcome, COALESCE(reason,''), COUNT(*)
FROM flow_events
WHERE created_at >= '2026-07-28 00:00:00' AND created_at < '2026-07-29 00:00:00'
GROUP BY DATE(created_at), flow, step, outcome, COALESCE(reason,'');

-- Opcional (cosmético, ya sin efecto): ver el contador inflado de julio
SELECT * FROM ai_recovery_monthly WHERE period = '2026-07';
```

> ✅ **ENSAYADO en la réplica local (05-ago, dump de prod del 04-ago)**: el DELETE borra
> exactamente **78 filas** (19 `patient_created` + 59 `contract_resolved`), la traza conserva 1 de
> cada, y el rollup recalculado queda `patient_created 106→87` y `contract_resolved 418→359`.
> En prod deben salir los mismos números; si difieren, parar y revisar.

> **Plazo**: los crudos de `flow_events` se purgan a los 45 días → el paso 2-3 debe correrse
> **antes del ~11-sep-2026**; después solo quedará el agregado (y habría que ajustar
> `flow_daily_stats` a mano restando los conteos de la sección 1).

## 4b. Limpieza — la BOMBA de `flow_events` (descubierta 06-ago): 10.32M eventos de `recuperacion`

**El hallazgo más grande del incidente, detectado por la lentitud del módulo Recovery del
dashboard.** La recursión emitía `ai_recovery_started` EN CADA FRAME antes de recursar:

| Dato | Valor |
|---|---|
| Traza-bomba | `sess:002b2acb-b57d-4834-a2b8-c765cab0bd28` (teléfono `+573***3118` — la 2ª víctima, la que escribió "Asesor") |
| Eventos falsos | **10,319,979 `ai_recovery_started`** entre 09:44:37 y 11:18:37 del 28-jul |
| A conservar | El PRIMER `ai_recovery_started` (intento real) + `ai_clarified` + `ai_recovered` + los 9 eventos normales de la traza |
| Rollup envenenado | `flow_daily_stats (2026-07-28, recuperacion, ai_recovery_started) = 10,319,989` |
| Impacto | El 99% de `flow_events` (10.4M filas donde debería haber ~110k) → módulo Recovery del dashboard tarda minutos (3 agregaciones con JSON_EXTRACT sobre 10.3M filas), y toda query sobre `flow_events` paga la tabla inflada |

**➡️ TODO EL PROCESO (A+B+C) está automatizado en `scripts/limpieza-incidente-28jul.sh`** —
verificación previa con abort si los conteos no coinciden, borrado por lotes e idempotencia.
En el server de prod: `bash scripts/limpieza-incidente-28jul.sh` (verificar) y luego `--yes`.
Ensayado en la réplica local: los 10,319,978 borrados tomaron **~40 min** (52 lotes de 200k,
online, sin bloquear la tabla). El SQL equivalente, por si se prefiere manual:

```sql
-- 1. VERIFICAR (esperado: ~10,319,981 en flow=recuperacion para esa traza)
SELECT step, COUNT(*) FROM flow_events
WHERE trace_id = 'sess:002b2acb-b57d-4834-a2b8-c765cab0bd28' GROUP BY step;

-- 2. Conservar el primer intento real
SELECT MIN(id) FROM flow_events
WHERE trace_id = 'sess:002b2acb-b57d-4834-a2b8-c765cab0bd28' AND step = 'ai_recovery_started';
-- anotar ese id como @KEEP y repetir hasta que borre 0 filas:
DELETE FROM flow_events
WHERE trace_id = 'sess:002b2acb-b57d-4834-a2b8-c765cab0bd28'
  AND step = 'ai_recovery_started' AND id <> @KEEP
LIMIT 200000;

-- 3. Tras llegar a 0: recalcular el rollup del día (mismo paso 3 de la sección 4).
-- 4. Opcional: OPTIMIZE TABLE flow_events; para devolver el espacio en disco
--    (bloquea brevemente; correr en horario valle o dejarlo — InnoDB reutiliza el espacio).
```

## 5. Cómo se verificó

- **04-ago, contra prod vía endpoints internos**: `flow-trace` del caso (89 eventos), `events` del
  teléfono (162 chat_events, timeline §1), `sessions` (contexto con `patient_id=113980`),
  `flow-events?flow=infra` (121 `session_abandoned` en la ventana).
- **05-ago, contra la réplica local restaurada del backup de prod del 04-ago** (posterior al
  incidente): conteos exactos en BD (`patient_created` 20, `contract_resolved` 60 en ventana),
  `sis_paci` sin duplicados (secuencia de autoids continua), y **ensayo completo de la limpieza**
  de §4 con los resultados anotados arriba.
