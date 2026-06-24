# Review PR #3 — Correcciones de slots (Neuro-Bot)

Rama: `fix/agenda-eligibility-join` · Revisión adversarial confirmada · Fecha: 2026-06-23

---

## ✅ ESTADO DE RESOLUCIÓN (2026-06-23)

Todos los hallazgos fueron atendidos. `go build`, `go test -race ./...` y
`golangci-lint run --new-from-rev=HEAD` (0 issues nuevos) en verde.

| # | Severidad | Estado | Resolución |
|---|-----------|--------|------------|
| **A** | ALTA (bloqueante) | ✅ Corregido | Nuevo `AppointmentRepository.SlotCountForAppointment` (`SELECT COUNT(*) FROM programacion_medico_detalle WHERE IdCita=@p1`) + wrapper en `AppointmentService`. Los 3 call-sites (`confirmation.go`, `self_reschedule.go`, `handlers/appointments.go`) derivan `espacios` del conteo real de slots, no de `len(block)`. Tests: `TestSlotCountForAppointment`. |
| **B** | ALTA (admin) | ✅ Documentado (código muerto) | `RescheduleDate` es inalcanzable: `handleRescheduleSameAgenda` retorna 404 antes de invocarla porque `FindWorkingDayException` es un stub que siempre devuelve `(nil,nil)`. Se documentó en el código su estado muerto + la incompatibilidad multi-slot del paso 3 (reclama solo el slot inicial) para cuando se reactive la vía admin. |
| **C/D** | MEDIA | ✅ Corregido | La grilla por (agenda,día) se deriva con **MCD de los gaps** entre libres (no min-gap), recuperando la grilla real aun con slots ocupados intercalados (`MCD(20,30)=10`), de modo que la oferta exija adyacencia física igual que el claim. Se quitó `WITH (NOLOCK)` del CTE `win` del claim multi-slot. Tests: `TestGetAvailableSlots_GridIntervalGCD`, `TestGCD`. |
| **E** | MEDIA | ✅ Reforzado | El test de integración multi-slot ahora asevera **contigüidad física** (gaps iguales y positivos) de los N slots reclamados. |
| **F** | BAJA | ✅ Corregido | `slots.go` mapea `slots_consecutivos_insuficientes` a la rama `slot_taken` → el paciente re-busca en vez de auto-cerrar. |

---

## Resumen ejecutivo

El PR introduce un cambio de modelo de agendamiento: pasa de **N citas (una por slot)** a
**1 cita asociada a N slots** (vinculados por `programacion_medico_detalle.IdCita`). El nuevo
modelo de `Create` funciona, pero **rompió todos los caminos de reprogramación** y dejó una
**inconsistencia entre la oferta de cupos y el reclamo de slots**.

### Conteos

| | |
|---|---|
| Hallazgos confirmados (brutos) | 13 |
| Hallazgos tras deduplicar | **6** |
| Alta severidad | 2 |
| Media severidad | 3 |
| Baja severidad | 1 |
| Regresiones introducidas por el PR | 3 (A, B y la del path admin) |
| Gaps de test | 1 (consolidado) |

### Lo más crítico

- **Reprogramar una cita multi-slot la deja ocupando 1 solo slot** (sub-reserva). Afecta los
  tres flujos de reschedule (notificación de confirmación, auto-reprogramación del paciente,
  y reschedule en el state machine). SIESA puede solapar otra cita encima → sobre-agenda real
  sobre datos clínicos compartidos con la UI del personal.
- **El bot ofrece cupos que luego fallan al confirmar** en agendas de procedimientos
  parcialmente ocupadas (divergencia oferta-vs-reclamo). Fail-safe (rollback limpio) pero
  degrada disponibilidad/UX y reproducido contra la BD real.

---

## Tabla priorizada

| # | Sev | Categoría | Título | Archivo(s) | Subsume |
|---|-----|-----------|--------|-----------|---------|
| A | **ALTA** | regresión | Reschedule de cita multi-slot sub-reserva: `espacios` colapsa a 1 (3 call-sites) | `confirmation.go:359`, `self_reschedule.go:90`, `handlers/appointments.go:320` | #2, #3, #4 |
| B | **ALTA** | regresión | `RescheduleDate` (path admin) reclama solo el slot cuyo HH:MM = `citas.hora` → libera N-1 slots | `siesa/appointment_repo.go:1132` | #1 |
| C | media | bug | Divergencia oferta-vs-reclamo: `slot_service` usa gap entre libres; `repo.Create` reclama próximas-N-filas → `slots_consecutivos_insuficientes` tras elegir cupo | `services/slot_service.go:110-128,200-230`, `siesa/appointment_repo.go:499-521` | #5, #6, #8, #9, #11 |
| D | media | edge-case | `win` CTE de `repo.Create` no excluye filas Bloqueado/ocupado al tomar TOP(N) → aborta bloques reservables | `siesa/appointment_repo.go:503` | (faceta de C; ver nota) |
| E | media | test-gap | Tests multi-slot no verifican contigüidad temporal, cross-day, obstrucción intermedia, ni reschedule (`len(block)`) | `siesa/appointment_multislot_integration_test.go` | #7, #12, #10 |
| F | baja | edge-case | Error `slots_consecutivos_insuficientes` no se mapea a `slot_taken` → el paciente recibe auto-cierre en vez de re-búsqueda | `handlers/slots.go:1079` | #13 |

> Nota sobre D: técnicamente es la misma raíz que C (dos definiciones distintas de "los próximos N slots"). Se lista
> aparte porque su corrección concreta vive en el SQL del `win` CTE; la corrección de C vive en `slot_service`. Ambas
> deben resolverse de forma coordinada para que oferta y reclamo coincidan.

---

## Detalle y recomendaciones

### A — ALTA · Reschedule de cita multi-slot sub-reserva (`espacios` = `len(block)` colapsa a 1)
**Subsume #2 (confirmation.go), #3 (self_reschedule.go), #4 (handlers/appointments.go).**

**Raíz:** En el modelo nuevo, una cita de N slots es **una sola fila en `citas`**.
`FindByAgendaAndDate` (`appointment_repo.go:284`) devuelve 1 fila por cita, así que
`FindConsecutiveBlock` retorna `len(block)=1`. Los tres caminos de reschedule derivan
`espacios := len(block)` y lo propagan a `CreateWithConsecutive(ctx, input, 1)`, reservando
**1 slot en vez de N**. `SpacesForCUPS` solo corrige resonancias; EMG/Rx multi-slot/ecografías
quedan en 1. En `main` (modelo N-citas) `len(block)=N` era correcto → **regresión directa del PR**.

**Impacto:** la cita reprogramada reserva menos slots de los necesarios; SIESA agenda otra cita
encima → sobre-reserva/colisión sobre datos de salud compartidos.

**Recomendación concreta:**
1. Dejar de derivar `espacios` de `len(block)` en los tres call-sites.
2. Calcular el número real de slots de la cita: `SELECT COUNT(*) FROM programacion_medico_detalle WHERE IdCita = @apptID`,
   o recomputar desde los CUPS con el `procedure_grouper` (suma de `RequiredSpaces`). Preferible
   el conteo real en BD, que es la fuente de verdad del modelo nuevo.
3. Centralizar ese cálculo en un único helper (p.ej. `SlotCountForAppointment(apptID)`) y usarlo
   en `confirmation.go`, `self_reschedule.go` y `handlers/appointments.go` para no repetir el bug.
4. Añadir test que alimente un bloque de 1-cita/N-slots y verifique que el reschedule reserva N.

---

### B — ALTA · `RescheduleDate` reclama solo el slot HH:MM = `citas.hora` (path admin)
**Subsume #1.**

**Raíz:** En `RescheduleDate` (`appointment_repo.go:1110-1142`) el paso 1 libera correctamente
los N slots viejos por `IdCita`, pero el paso 3 reclama el nuevo slot con
`CONVERT(VARCHAR(5), pmd.Fecha, 108) = c.hora`. Como la cita tiene **un solo `hora`** (inicio del
bloque), solo reclama 1 slot en la fecha nueva; los N-1 restantes quedan `IdCita IS NULL`.
**Regresión del PR.** Alcanzable solo desde el endpoint admin `handleRescheduleSameAgenda`
(`internal_handler.go:648`) — el self-reschedule NO usa este método (crea sesión nueva vía `Create`,
que sí tiene el fix), lo que acota la reachability a operaciones de administrador.

**Impacto:** al mover una agenda con citas de imagen/procedimiento multi-slot a otra fecha, cada
procedimiento queda ocupando 1 slot y libera tiempo que otro paciente puede tomar.

**Recomendación concreta:**
1. En el paso 3 de `RescheduleDate`, reclamar **todos** los slots del bloque, no solo el de
   `c.hora`. Reservar la ventana `[hora_inicio, hora_inicio + N*intervalo)` (mismo criterio que
   `Create` para multi-slot), o reclamar por contigüidad las próximas `N` filas libres desde el
   nuevo inicio dentro de la agenda destino.
2. Obtener `N` con el mismo helper de conteo del hallazgo A (`COUNT(*) WHERE IdCita = @apptID`)
   ANTES de liberar los slots viejos, ya que tras liberar se pierde la referencia.
3. Validar que se reclamaron exactamente `N` slots; si no, abortar la transacción (fail-safe).
4. Añadir test de integración del path admin con cita multi-slot.

---

### C — MEDIA · Divergencia oferta-vs-reclamo (interval-derivation)
**Subsume #5, #6, #8, #9, #11.**

**Raíz:** `slot_service` deriva el intervalo como el **menor gap entre slots LIBRES** del día
(`intervalByAgendaDay`, `slot_service.go:118-128`) y valida consecutivos con
`free[minutes + i*interval]` (`:210`). `repo.Create` ignora ese intervalo y reclama las
**próximas N filas físicas por `Fecha`** (`TOP(@Espacios) ... ORDER BY Fecha`,
`appointment_repo.go:499-521`). Cuando hay slots ocupados intercalados, el menor gap entre libres
es un múltiplo de la rejilla real → `slot_service` ofrece un inicio que `repo.Create` no puede
satisfacer → `slots_consecutivos_insuficientes` + rollback **después** de que el paciente eligió.

**Reproducido en BD (ZeusSalud_Neuro):** agenda 326 / 2026-06-25 (rejilla 10 min, únicos libres
14:20 y 15:00 → interval derivado 40): se ofrece 14:20 para Espacios=2 pero 14:30 está ocupado →
falla. Casos análogos: agenda 447 (07:40), agenda 661 / 2026-07-06 (08:00 ocupado entre 07:40 y
08:40). Solo se manifiesta con `Espacios>1` (procedimientos: EMG≥4→2, TAC 3D→3, etc.).

**Impacto:** falsa disponibilidad → error de reserva tras elegir cupo; pacientes de lista de
espera notificados pero no agendables. Fail-safe (rollback atómico, sin corrupción ni daño a la UI).

**Recomendación concreta:**
1. Unificar la semántica de "próximos N slots" entre oferta y reclamo. La oferta debe validar
   **adyacencia física real** (la siguiente fila por `Fecha` que esté libre), no aritmética
   `minutes + i*interval`.
2. Implementación sugerida: en `slot_service`, para `Espacios>1`, recorrer las filas ordenadas por
   `Fecha` del día y exigir que las próximas `N` filas físicas estén todas libres y contiguas
   (gap == rejilla), exactamente como hará `repo.Create`. Así la oferta nunca propone un inicio
   que el reclamo rechazará.
3. Alternativa/complemento: hacer que `repo.Create` reciba la lista explícita de IDs de slot que
   `slot_service` ya validó (en vez de re-derivar por `TOP(N) ORDER BY Fecha`), eliminando la
   doble fuente de verdad.

---

### D — MEDIA · `win` CTE no excluye filas no-libres al tomar TOP(N)

**Raíz:** El `win` CTE (`appointment_repo.go:503`) hace
`SELECT TOP(@Espacios) Id ... WITH(NOLOCK) WHERE IdProgramacionMedico=@aid AND Fecha>=@start ORDER BY Fecha`
**sin** filtrar `Bloqueado`/`SinProgramacion`/`IdCita`. Ese filtro solo está en el `UPDATE`
exterior. Si una fila no-libre cae dentro de la ventana de N filas, consume un cupo de la ventana
pero no se actualiza → `RowsAffected < Espacios-1` → rechazo y rollback aunque haya suficientes
slots libres contiguos justo después de la obstrucción.

**Recomendación concreta:**
1. Mover el filtro de elegibilidad al `win` CTE: `... WHERE ... AND IdCita IS NULL AND Bloqueado=0 AND SinProgramacion=0`.
2. Tras seleccionar las N filas libres, validar adicionalmente que sean **contiguas** (gap entre
   `Fecha` consecutivas == intervalo de la rejilla), para no reclamar slots libres separados por
   un hueco ocupado (lo que sería sobre-reserva temporal).
3. Quitar `WITH(NOLOCK)` en la ruta de reclamo: una lectura sucia aquí puede permitir reclamar un
   slot que otra transacción está tomando. Usar el nivel de aislamiento de la transacción.
4. Esta corrección debe coordinarse con C (ambas redefinen "los próximos N slots").

---

### E — MEDIA · Gaps de test multi-slot (contigüidad, cross-day, obstrucción, reschedule)
**Subsume #7, #12, #10.**

**Raíz:** El único test de integración multi-slot
(`appointment_multislot_integration_test.go`) reserva desde `MIN(Fecha)` de la agenda 315
(rejilla densa uniforme, mejor caso) y solo asevera `len(horas)==espacios`. No verifica:
(a) **contigüidad temporal** real entre las horas reclamadas (el comentario "contiguos" es
engañoso); (b) inicio cerca de fin de jornada → **ventana cross-day** (TOP(N) cruza al día
siguiente y los reclama como "consecutivos"); (c) **obstrucción intermedia** dentro de la ventana
(camino de rollback no probado); (d) el camino de **reschedule** donde `espacios=len(block)`
(hallazgo A) — ningún test lo ejercita, por eso la regresión pasó desapercibida.

**Recomendación concreta:**
1. Añadir aserción de contigüidad: para cada par consecutivo, `horas[i]-horas[i-1] == intervalo_rejilla`
   y mismo día.
2. Test negativo: agenda con una fila ocupada/bloqueada dentro de la ventana → esperar
   `slots_consecutivos_insuficientes` y rollback (sin cita huérfana, sin slots medio-reservados).
3. Test de borde cross-day: inicio al final de la jornada con Espacios>1 → no debe reclamar filas
   del día siguiente.
4. Test del reschedule: alimentar un bloque de 1-cita/N-slots y verificar que reserva N (cubre A).
5. Test de concordancia: para una agenda con libres intercalados, afirmar que el conjunto ofrecido
   por `slot_service` == conjunto que `repo.Create` logra reclamar (cubre C/D).

---

### F — BAJA · `slots_consecutivos_insuficientes` no se mapea a `slot_taken`

**Raíz:** En `slots.go:1079` solo se tratan como cupo-tomado los errores con `slot_taken` o
`Duplicate entry`. El error `slots_consecutivos_insuficientes` (carrera multi-slot: un slot
intermedio fue tomado por la UI de SIESA entre búsqueda y reserva) cae en la rama genérica
`reason="error"` → `buildAutoCloseResult` **cierra la conversación** en vez de re-buscar horarios.
Sin corrupción de datos (rollback correcto); solo UX de reintento degradada.

**Recomendación concreta:**
1. Añadir `|| strings.Contains(errMsg, "slots_consecutivos_insuficientes")` a la condición de
   `slots.go:1079` para que enrute al flujo `slot_taken` (re-búsqueda automática).
2. Mejor aún: definir errores tipados/sentinela (`var ErrSlotTaken`, `var ErrConsecutiveSlots`) en
   el repo y comparar con `errors.Is` en vez de `strings.Contains`, para no depender del texto.

---

## Conclusión

El núcleo del PR (`Create` con modelo 1-cita/N-slots) es correcto, pero **el cambio de modelo no se
propagó a los caminos de reprogramación** (A, B) ni se reconcilió la **definición de contigüidad**
entre la capa de oferta y la de reclamo (C, D). A y B son regresiones de correctitud sobre
agendamiento clínico y deben corregirse antes de fusionar. C/D degradan disponibilidad de forma
fail-safe. E cubre los tests que habrían atrapado A, C y D. F es un fix trivial de UX.
