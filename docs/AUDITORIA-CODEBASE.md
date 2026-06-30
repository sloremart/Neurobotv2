# Reporte de Auditoría — Neuro-Bot (Go / WhatsApp / SIESA)

_Auditoría multi-agente del código (10 áreas, 21 agentes) con verificación adversarial · 2026-06-30_

## 1. Resumen ejecutivo

Hallazgos verificados (confirmados + degradados, excluyendo refutados): **44 reales** + **4 falsos positivos**.

| Severidad | Confirmados/reales |
|-----------|--------------------|
| Critical  | 0 |
| High      | 3 |
| Medium    | 7 |
| Low       | 34 |

**Riesgos más urgentes:**

1. **El DTMF del IVR (confirmar/cancelar cita por voz) se descarta con la config por defecto** — las cancelaciones telefónicas se ignoran, el cupo queda ocupado y se garantiza un no-show (HIGH).
2. **El reclamo de slots multi-espacio puede cruzar al día siguiente o tomar huecos no contiguos** — marca como ocupados slots equivocados (p. ej. la mañana siguiente) sin error (HIGH).
3. **El reagendamiento a nueva agenda se traga el error de `CancelBatch`, borra la disponibilidad del día y responde `status:ok`** — citas activas + día "libre" ⇒ doble reserva e inconsistencia destructiva (HIGH).
4. **PHI clínico viaja en claro**: cifrado TDS deshabilitado por defecto hacia la BD SIESA y body crudo del webhook (teléfono/nombre/texto del paciente) logueado sin redactar al fallar la firma (MEDIUM x2).
5. **Un blip transitorio de SQL Server en el arranque deja al bot vivo pero permanentemente inútil** (modo degradado sin reintento, `/health` responde 200) (MEDIUM).

---

## 1.b Estado de remediación (FINAL — 2026-06-30)

Todo lo accionable quedó corregido, verificado (varios contra la BD SIESA real) y en `main`.

| Severidad | Total | Corregidos | Omitidos (decisión) | No-acción (documentada) |
|-----------|-------|------------|---------------------|--------------------------|
| Critical  | 0  | —  | — | — |
| High      | 3  | **3** ✅ | — | — |
| Medium    | 7  | **7** ✅ | — | — |
| Low       | 34 | **26** ✅ | 2 | 6 |

**High (3) — commit `a3e075d`** (tests H1/H3 + build H2): H1 DTMF del IVR ya no se descarta (pending con timer de gracia); H2 claim multi-espacio acota el día + verifica contigüidad; H3 reagendar a nueva agenda retorna 500 si falla `CancelBatch`.

**Medium (7):** M1, M2, M4, M5, M6, M7 — commit `7b65d20`; **M3** (1 sesión activa por teléfono a nivel BD, migración 032 + dedup) — commit `4d2afd5`.

**Low — 26 de 34 corregidos:**
- Lote inicial (21): commits `e8ad6bc`, `528566a`, `d69c3ba`.
- **#26** (CUPS huérfanas → inserción atómica cita+slots+CUPS en una tx) — commit `e342aa3`, verificado vs `siesa-db`.
- **#7/#24** (filtro `Medico`+`SinProgramacion`+`TOP(1)` en el claim de slot) — commit `07df1b1`, verificado vs `siesa-db` (0/601 agendas mezclan médicos).
- **#5/#33** (apagado: race `wg.Add`/`wg.Wait` con flag `closing`; auditorías SIESA esperadas antes de cerrar la BD) — commit `f1feac0`, tests `-race`.
- Extra: `TestCreateMultiSlot` robusto (busca tramo contiguo libre) — commit `8addf6f`.

**Low omitidos por decisión (2):**
- **#25** grilla por MCD — ya mitigado: el enfoque MCD recupera la grilla real y es conservador (nunca ofrece inicios que el claim rechace); solo podría sub-ofrecer en días de grilla mixta (cobertura/UX), no es bug de reserva.
- **#27** bilateral ×2 en tomografía — requiere la **lista de códigos del negocio** (qué CUPS de TAC duplican tiempo siendo bilaterales) antes de condicionar el ×2; diferido hasta tenerla.

**Low no-acción documentada (6):**
- **#2** dedup voice/outbound — la firma del webhook (HMAC + timestamp) ya protege contra replay.
- **#9** `primera_vez_control` — el código está alineado al histórico real de SIESA; la "discrepancia" es con CLAUDE.md (documental), a confirmar con los administrativos, no un bug.
- **#12** `RescheduleDate` multi-slot — **código muerto**: el único call-site corta con 404 porque `FindWorkingDayException` es un stub (no aplica en SIESA). Bug latente documentado en el código; se atenderá si se reactiva el reagendamiento administrativo de agendas.
- **#15** migración 023 no idempotente — ya aplicada en todos los entornos; MySQL no soporta `ADD COLUMN IF NOT EXISTS` y golang-migrate no re-ejecuta migraciones aplicadas.
- **#19** revalidar pending IVR antes de `PlaceCall` — el filtro previo se quitó por estar roto; peor caso = una llamada de recordatorio de más (molestia menor), no error de agenda.
- **#20** `defer resp.Body.Close()` en bucle de paginación — refactor cosmético, sin fuga real en bucles cortos.

> Las correcciones que tocan SIESA se verificaron contra la BD dockerizada `siesa-db` (no solo unit tests): inserción atómica con rollback, claim por médico, y reclamo multi-slot contiguo.

---

## 2. Críticos y Altos

### H1 — El resultado DTMF del IVR (confirmar/cancelar) se descarta con la config por defecto
- **Categoría:** business-logic
- **Ubicación:** `internal/notifications/manager.go:549-558` (`MarkIVRSent`), `:650-653` y `:707-710` (`HandleVoiceGatherResult`); `internal/scheduler/tasks.go:304-322`
- **Problema:** En `sendVoiceReminders` el orden es `PlaceCall` → `RegisterCallID` → `MarkIVRSent`. Con `ConfirmFollowupEnabled=false` (default de fábrica, `config.go:281`), `MarkIVRSent` ejecuta `m.pending.Delete(phone)` + `Resolve("escalated_to_ivr")` de inmediato, antes de que el paciente conteste. Cuando llega el webhook de voz, `HandleVoiceGatherResult` hace `pending.LoadAndDelete(phone); if !ok { return }`: el pending ya no existe (el `AppointmentID`/`ConversationID` solo vivían ahí), así que retorna sin confirmar ni cancelar.
- **Impacto:** Confirmaciones perdidas y, peor, **cancelaciones por DTMF ignoradas**: el cupo en `programacion_medico_detalle` queda ocupado ⇒ no-show garantizado. Alcanzable en producción con la configuración por defecto (`IVRNotificationsEnabled=true`, `ConfirmFollowupEnabled=false`).
- **Fix:** En `MarkIVRSent`, cuando `ConfirmFollowupEnabled=false`, NO borrar el pending de inmediato: mantenerlo vivo con un timer de gracia (`ConfirmPostIVRMinutes`) para que `HandleVoiceGatherResult` consuma el DTMF y recién entonces resolver. Alternativa: reconstruir la cita desde `callIDMap`/persister cuando el pending ya no exista, y limpiar `callIDMap` en la rama deshabilitada para evitar el leak.

### H2 — Claim de slots multi-espacio cruza el límite del día y no verifica contigüidad real
- **Categoría:** business-logic
- **Ubicación:** `internal/repository/siesa/appointment_repo.go:533-555`
- **Problema:** El CTE `win` (536-541) hace `SELECT TOP (@p3) Id ... WHERE IdProgramacionMedico=@p2 AND Fecha>=@p4 ORDER BY Fecha` sin cota superior de fecha ni verificación de espaciado. El UPDATE (542-545) solo exige `IdCita IS NULL AND Bloqueado=0 AND SinProgramacion=0`, y el chequeo de éxito (552) compara solo la cantidad (`n != Espacios-1`).
- **Impacto:** Para imágenes/procedimientos que ocupan N intervalos: (a) reservando cerca del fin del día, la ventana toma filas libres del **día siguiente** y marca `IdCita` en slots de la mañana siguiente con "éxito" silencioso; (b) con huecos intra-día (almuerzo) reclama filas no contiguas en el tiempo. El caso de un slot ocupado intermedio sí se maneja con rollback, pero el cruce de día y los huecos no.
- **Fix:** Añadir cota de día al CTE (`AND Fecha < DATEADD(DAY,1,@start)`) y validar contigüidad real contra el intervalo de la agenda (LAG/diferencias de `Fecha`, exigir que la última fila esté a `< Espacios*intervalo` minutos del inicio) antes de confirmar; si no, rollback.

### H3 — Reagendamiento a nueva agenda traga el error de `CancelBatch`, borra la excepción de día y responde `status:ok`
- **Categoría:** data
- **Ubicación:** `internal/api/internal_handler.go:1232-1243` (`handleRescheduleWithNewAgenda`)
- **Problema:** En 1232-1237 hace `if err := h.appointmentRepo.CancelBatch(...); err != nil { slog.Error(...); cancelled = 0 }` **sin return**. El flujo continúa: 1240-1242 ejecuta `DeleteWorkingDayException(...)` (e ignora su propio error), consulta la nueva agenda, notifica y responde HTTP 200 `status:ok, cancelled:0`. El contraste es explícito: `HandleCancelAgenda` (1007-1014, fix M1) SÍ retorna 500 cuando `CancelBatch` falla.
- **Impacto:** Si `CancelBatch` falla (error de BD/tx), las citas quedan **activas** en SIESA pero se elimina la excepción de día ⇒ el sistema cree el día libre, habilitando **doble reserva**, y el admin recibe `status:ok`. Acción destructiva con pérdida de integridad, alcanzable vía endpoint admin autenticado.
- **Fix:** Replicar el patrón M1: si `CancelBatch` falla, `http.Error(w, "...none cancelled", 500); return` ANTES de borrar la excepción y notificar. Idealmente envolver cancelación + `DeleteWorkingDayException` en una transacción (o, si es cross-DB, ordenar para que el borrado de disponibilidad solo ocurra tras una cancelación confirmada) y dejar de ignorar el error de `DeleteWorkingDayException`.

---

## 3. Medios

| Título | Categoría | Ubicación | Fix |
|--------|-----------|-----------|-----|
| Body crudo del webhook (PII de salud) logueado sin redactar al fallar la firma | security | `internal/api/webhook_handler.go:222-235` | No loguear el body crudo; solo metadatos no-PII (hash, content-type) o redacción por contenido en `ReplaceAttr`; marcar `body_preview`/`raw` como claves siempre enmascaradas. |
| Cifrado TDS deshabilitado por defecto para BD clínica con PHI; `TrustServerCertificate=true` al activarse | security | `internal/config/config.go:186`; `internal/database/mysql.go:60-67` | Default `encrypt='true'`; verificación de hostname/pinning en vez de `TrustServerCertificate=true`; Warn de arranque si `encrypt=='disable'` en prod. |
| Worker y NotificationManager usan PhoneMutex distintos: escrituras concurrentes a la misma fila de sesión | concurrency | `internal/notifications/manager.go:147,159`; `confirmation.go:136,443`; `internal/worker/pool.go:364,1074`; `webhook_handler.go:151-155` | Inyectar el MISMO `PhoneMutex` del `SessionManager` en el `NotificationManager`; índice único parcial sobre `(phone_number)` where `status='active'` y/o `SaveState` con UPDATE condicional. |
| `EnqueueVirtual` omite el tope de overflow goroutines (sin backpressure) | resource-leak | `internal/worker/pool.go:1253-1267` | Replicar el guard de `Enqueue`: comprobar `activeOverflow.Load() >= maxOverflowGoroutines` antes de la rama default; extraer helper compartido. |
| SIESA caído al arrancar deja al bot en modo degradado PERMANENTE (sin reintento) | error-handling | `cmd/server/main.go:103-119,353,394`; `internal/database/mysql.go:86-91` | Reconexión lazy (abrir pool sin Ping fatal, reintentar por query) o health-checker que re-inicialice repos al volver SIESA; o fail-fast para que el orquestador reinicie. |
| Gate de cobertura (ANY cubierto) diverge del gate de precio (todos con precio) en grupos multi-CUP | business-logic | `internal/statemachine/handlers/slots.go:166-207,262-278,1106-1198` | Tratar `Precio==0` igual que `nil` (`pricingFailed` si `price==nil \|\| *price<=0`); el gate de cobertura debe exigir que TODOS los CUPS que se persisten tengan convenio en bundles reales. |
| Reagendamiento misma agenda no transaccional: mueve la excepción de día antes de mover las citas e ignora error de `UpdateWorkingDayExceptionDate` | data | `internal/api/internal_handler.go:1301-1313` | Chequear el error de `UpdateWorkingDayExceptionDate` y abortar con 500; mover primero las citas y actualizar la excepción solo tras confirmar; envolver en tx con rollback. |

---

## 4. Bajos

- **PII en logs Debug** (`webhook_handler.go:420-424`; `bird/webhook.go:123-135`): `raw`/`text_text`/`list_text`/`interactive_text` en claro con `LOG_LEVEL=debug`. Loguear solo longitud/hash.
- **Ventana anti-replay 24h sin dedup en voice/outbound/conversation** (`bird/webhook.go:22`; `webhook_handler.go:557-634,170-185`): el dedup (`InsertIfNotExists`) solo cubre inbound.
- **`AnalyzeDocument` adjunta la Bird AccessKey a una URL del payload** (`ocr_service.go:363-376`): riesgo SSRF/fuga. Validar host contra allowlist del CDN de Bird.
- **Rate limiter por IP del proxy (ignora X-Forwarded-For)** (`middleware.go:93-122`; `main.go:457-464`): límite agregado para todos. Derivar IP del primer salto confiable o limitar por API key.
- **`wg.Add` concurrente con `wg.Wait` en el apagado** (`pool.go:196,268,1259`; `main.go:499-518`): posible panic "WaitGroup is reused". Flag `closing` atómico antes de `wg.Add`.
- **`RegisterPending` sobreescribe un pending previo sin `Timer.Stop()`** (`manager.go:248-279`): fuga de timer. `Load`+`Stop` antes del `Store`.
- **Claim del slot principal sin TOP ni filtros Medico/SinProgramacion** (`appointment_repo.go:509-525`): añadir `AND Medico=@cod_medi AND SinProgramacion=0` y `TOP(1)`.
- **Conteo del tope mensual MRC usa NOLOCK** (`appointment_repo.go:1162-1200`): lectura sucia en decisión de cupo. Usar READ COMMITTED; idealmente en la misma tx del `Create`.
- **`primera_vez_control` posible inversión vs CLAUDE.md** (`appointment_repo.go:448-457`): confirmar significado de 1/2 con administrativos de SIESA.
- **Cancel: id no numérico libera silenciosamente sin cupo y confirma** (`appointment_repo.go:989-997`): retornar error en vez de commitear con slot huérfano.
- **`Confirm`/`ConfirmBatch` no filtran por estado** (`appointment_repo.go:938-954,1002-1024`): pueden confirmar una cita cancelada. Añadir `AND estado='P'`.
- **`RescheduleDate` incompatible con multi-slot** (`appointment_repo.go:1284-1336`): bug latente (hoy código muerto por stub `FindWorkingDayException`).
- **`ExpireOld` ignora su parámetro `days`** (`waiting_list_repo.go:251-259`): alinear SQL con la firma o eliminar el parámetro.
- **`INSERT IGNORE` en el WAL conflata error con duplicado** (`inbox_repo.go:28-38`): usar `ON DUPLICATE KEY UPDATE id=id` o detectar 1062.
- **Migración 023 no idempotente** (`migrations.go:27-32`; `023_...up.sql:5-11`): `CREATE INDEX IF NOT EXISTS` o fusionar en el ALTER.
- **`cups_maps_url` nunca se persiste** (`slots.go:292,574`): añadir la clave al bloque de persistencia (`slots.go:458-466`).
- **`showSlotsHandler` acepta "Ver más" aunque no se ofreció** (`slots.go:445-448,535-551`): tratar `optionNum==len+1` como "Ver más" solo si `len>=5`.
- **Posible nil-pointer panic en `HandleResponse`** (`manager.go:310-311` vs `882/891`): entradas restauradas vencidas tienen `Timer==nil`. Guard `if pending.Timer != nil`.
- **`sendVoiceReminders` no revalida el pending antes de `PlaceCall`** (`tasks.go:253-322`): re-verificar `m.HasPending` antes de llamar.
- **`defer resp.Body.Close()` dentro del bucle de paginación** (`bird/client.go:1377-1430`): refactorizar a función auxiliar.
- **`EscalateToAgent` ignora el context en el sleep de reintento** (`bird/client.go:1455-1470`): usar `sleepWithContext`.
- **`parseRetryAfter` no soporta HTTP-date** (`bird/client.go:1765-1775`): añadir rama con `http.ParseTime`.
- **Métodos de caché de conversación panic con struct literal (mapas nil)** (`bird/client.go:125-147`): init perezoso con guard o constructor para tests.
- **Reclamo de slots contiguos no acota día ni médico** (`appointment_repo.go:533-555`): defensa en profundidad — `AND pmd.Medico=... AND pmd.Fecha < DATEADD(DAY,1,@p4)`.
- **Intervalo derivado por MCD sobreestima la grilla** (`slot_service.go:142-152,230-254`): ofrece inicios que el claim rechaza (UX). Derivar la grilla con las filas físicas contiguas reales.
- **Compensación tras fallo de inserción deja `citas_procedimientos` huérfanas** (`appointment_service.go:323-368`): insertar todos en la misma tx, o borrarlos en la compensación.
- **Bilateral ×2 en Tomografía se aplica a cualquier código** (`procedure_grouper.go:609-612`): introducir `tomografiaBilateralCodes` y condicionar el ×2.
- **`HandleWaitingListCheck` ignora el error de decode del body** (`internal_handler.go:1446-1495`): JSON inválido ⇒ barrido real. Chequear (400) y/o invertir el default de `dry_run`.
- **Truncado de `Reason` por bytes parte runes UTF-8** (`internal_handler.go:986-988,1175-1177`): truncar por runes.
- **Validación de fecha futura usa `Truncate(24h)` (frontera UTC)** (`internal_handler.go:1169-1173,976-984`): off-by-one nocturno en Colombia. Usar `America/Bogota` + `ParseInLocation`.
- **Parsing frágil de flags booleanos (`LOG_MASK_PHONES`)** (`config.go:267-281`): usar `getEnvBool`.
- **Limpieza de logs antiguos solo corre al arranque** (`file_writer.go:38,78-113`): llamar `cleanup()` en `rotateLocked` o un ticker diario.
- **Apagado no espera goroutines de fondo que usan la BD** (`main.go:489-519`): posible `sql: database is closed`. Agrupar en WaitGroup/errgroup antes de cerrar la BD.
- **`*sql.DB` sin cerrar al fallar el Ping de arranque** (`mysql.go:33-35,89-91`): `db.Close()` antes de retornar el error.

---

## 5. Refutados / falsos positivos

- **Flag `mrc_limit_check` queda stale entre grupos** (`medical_validation.go:556-569`; `slots.go:333-388`): el flag stale es real, pero `CheckMRCLimitForMonth` re-valida con `IsMRCGroupCups(code)` y retorna `false` para grupos no-MRC ⇒ ningún mes se oculta. Sin impacto; solo limpieza latente.
- **`VerifySignatureWithKey` acepta firmas con secreto vacío (fail-open)** (`bird/webhook.go:43-82`): `BIRD_WEBHOOK_SECRET` es obligatorio en `config.validate()` y los handlers envuelven con `if secret != ""`. Ningún caller lo invoca con secreto vacío.
- **`primera_vez_control` invertido** (`appointment_repo.go:454-457`): el valor se derivó del histórico real de SIESA (control 7/9/11 → 98% valor 1); el peso probatorio favorece al código; CLAUDE.md probablemente desactualizado.
- **Tope MRC validado por CUP individual y no por orden completa** (`appointment_service.go:258,280-286`): la máquina de estados agenda secuencialmente y commitea cada cita antes del siguiente CUP, así que `CountMonthlyByGroup` ya incluye los previos. No hay vía concurrente (conversación lineal por sesión).
