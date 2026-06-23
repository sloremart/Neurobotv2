# Review del diff de la sesión — Neuro-Bot

Rango revisado: `44f2a28..0bbf328`
Fecha: 2026-06-23

## Resumen ejecutivo

Se revisaron 19 hallazgos confirmados adversarialmente. Tras deduplicar quedan **18 hallazgos efectivos** (1 par fusionado: la mis-tarificación de municipio vacío de SANITAS aparecía dos veces).

| Severidad | Total | Nuevos | Ya conocidos |
|-----------|:-----:|:------:|:------------:|
| High      | 0     | 0      | 0            |
| Medium    | 3     | 2      | 1            |
| Low       | 15    | 12     | 3            |
| **Total** | **18**| **14** | **4**        |

Notas clave:
- **0 hallazgos high** y **0 vulnerabilidades introducidas**. Los cambios de seguridad (firma de webhook, redacción PII) *mejoran* la postura; sus riesgos son de disponibilidad/cobertura, no de explotación.
- Las 2 regresiones medium/low de `slot_service.go` (ventana de contraste y ventana de CUPS) comparten **una sola causa raíz**: el bucle de slots consecutivos no reaplica los filtros de ventana horaria. Un solo fix las cierra ambas.
- Varios hallazgos low son **deuda/observabilidad** (errores tragados, comentarios obsoletos, código muerto) sin impacto en runtime ni en datos.
- Dos cambios verificados son **fixes correctos** (no defectos): `self_reschedule` preferred_doctor_doc → cédula, y migración MRC entidad→contrato en lista de espera.

---

## Hallazgos NUEVOS (requieren acción)

### Medium

#### N1 — Bloque de contraste/TAC multi-espacio puede pasar de las 17:00
- **Archivo:** `internal/services/slot_service.go:173`
- **Categoría:** bug / correctitud de agenda
- **Qué pasa:** Para `Espacios>1` (RM/TAC contrastados), los slots 2..N solo se validan contra el mapa de libres `freeByAgendaDay`, no contra las ventanas de contraste (7–17h) ni de CUPS. Un bloque que arranca ~16:50 puede extenderse más allá de las 17:00, violando la ventana de preparación de contraste. Es regresión: el antiguo `calculateDaySlots` recortaba el rango ANTES de generar slots.
- **Recomendación:** Reaplicar los filtros de ventana (contraste 7–17h y CUPS-específico) a **cada** slot del bloque consecutivo, no solo al inicial. Validar dentro del bucle de `i` en líneas 173–185: si `minutes+i*DurationMin` cae fuera de la ventana aplicable, descartar el slot inicial. Cierra también el hallazgo N12.

#### N2 — Errores silenciados en escrituras de contrato/municipio (datos clínico-administrativos)
- **Archivo:** `internal/statemachine/handlers/identification.go:257` (y `registration.go:~748`)
- **Categoría:** error-handling / observabilidad
- **Qué pasa:** `_ = patientSvc.UpdateContract(...)` y `_ = patientSvc.UpdateMunicipality(...)` descartan el error sin slog. La cita actual no se rompe (usa el contexto en memoria), pero un UPDATE fallido a `sis_paci` no persiste la corrección ni deja traza. Relevante para Ley 1581 y para diagnosticar deadlocks/timeouts con la UI de SIESA.
- **Recomendación:** Capturar el error y emitir `slog.Warn`/`slog.Error` con `patient_id`, contrato/municipio resueltos y el error envuelto. No es necesario abortar el flujo; basta con loguear para auditabilidad.

### Low

#### N3 — Comentario obsoleto en `DeleteBatch` describe un DELETE de "gemela cancelada" que ya no existe
- **Archivo:** `internal/repository/siesa/appointment_repo.go:778`
- **Categoría:** bug (documentación)
- **Recomendación:** Actualizar el docblock para reflejar que la unicidad de PK ahora descansa en `horacan = CONVERT(VARCHAR(5), GETDATE(), 108)` y eliminar la mención al DELETE inexistente.

#### N4 — Riesgo de colisión de PK al cancelar dos citas del mismo slot en el mismo minuto
- **Archivo:** `internal/repository/siesa/appointment_repo.go:808`
- **Categoría:** sql
- **Qué pasa:** La unicidad de la PK compuesta depende de `horacan` con granularidad de minuto. Si una gemela 'C' previa tiene el mismo minuto que la cancelación nueva, el UPDATE viola `PK_citas` y todo el batch hace rollback (ninguna cita se cancela). Probabilidad baja; mejora neta frente al workaround eliminado.
- **Recomendación:** Usar mayor granularidad en `horacan` (incluir segundos vía formato `108` completo o `114`), o detectar la violación de PK y reintentar con un sufijo de desempate. Como mínimo, documentar el caso borde.

#### N5 — Auditoría `log_citas` en batch lanza una goroutine por id
- **Archivo:** `internal/repository/siesa/appointment_repo.go:796`
- **Categoría:** concurrency
- **Qué pasa:** `ConfirmBatch`/`CancelBatch` invocan `writeAuditLog` (fire-and-forget, conexión propia) en bucle. Con N grande (cancelación de agenda completa, ~65) se genera fan-out acotado por el pool (10 conexiones), que puede demorar transitoriamente otras operaciones del bot. (Mismo defecto que N-34 de la auditoría previa.)
- **Recomendación:** Reemplazar por un único `INSERT INTO log_citas ... SELECT ... WHERE id IN (...)` por batch, o limitar el fan-out con un semáforo. Best-effort, no urgente.

#### N6 — `RescheduleDate` paso 3 hace match de slot por `c.hora` que puede estar en 12h
- **Archivo:** `internal/repository/siesa/appointment_repo.go:1042`
- **Categoría:** bug
- **Qué pasa:** El JOIN `CONVERT(VARCHAR(5), pmd.Fecha, 108) = c.hora` falla para citas PM (citas.hora en 12h vs pmd.Fecha en 24h). ~91% de las citas PM no harían match → el slot nuevo queda libre (slot fantasma reclamable, riesgo de doble-booking). La cita sí se mueve.
- **Recomendación:** Usar la conversión 24h ya existente en el archivo (`hhmm24`/`timecodeFromAppointment`) para construir el valor de comparación, en lugar de comparar contra `c.hora` cruda.

#### N7 — Búsqueda de municipio trata el primer '-'/',' como separador de departamento
- **Archivo:** `internal/repository/siesa/municipality_repo.go:31`
- **Categoría:** bug
- **Qué pasa:** El split en city/dept convierte el LEFT JOIN en efectivamente INNER. "Bogotá, D.C." (formato que el propio prompt sugiere) o "Miriti - Parana" devuelven 0 filas aunque la ciudad exista. Regresión vs el `LIKE %input completo%` anterior.
- **Recomendación:** Si la consulta con filtro de departamento devuelve 0 filas, reintentar sin el filtro de departamento (fallback). Bogotá es alto volumen.

#### N8 — Filtro de médico preferido se calcula antes del filtro de edad
- **Archivo:** `internal/services/slot_service.go:110`
- **Categoría:** bug
- **Qué pasa:** `preferredHasSlots` ignora la restricción de edad; un médico preferido descalificado por edad podría anular todos los resultados. Impacto práctico **inalcanzable** hoy (fallback en `slots.go:215-253` y el preferido siempre es el médico previo del paciente), pero el code smell de ordenamiento es real y latente.
- **Recomendación:** Excluir al preferido restringido por edad al calcular `preferredHasSlots` (aplicar el filtro de edad antes, como el código previo), para blindar futuros callers sin fallback.

#### N9 — Ventana de CUPS (879420 TAC 10–15h) no se aplica a slots consecutivos
- **Archivo:** `internal/services/slot_service.go:151`
- **Categoría:** bug
- **Qué pasa:** Mismo patrón que N1 pero para la ventana de CUPS. Un TAC 879420 contrastado (Espacios=2) que arranque ~14:30 ocuparía 15:00, fuera de ventana. Impacto acotado al borde de la ventana.
- **Recomendación:** **Se cierra junto con N1** — reaplicar el filtro de ventana de CUPS a cada slot del bloque consecutivo.

#### N10 — Error de `FindSubjectTypeForCups` tragado sin log
- **Archivo:** `internal/statemachine/handlers/slots.go:1032`
- **Categoría:** error-handling
- **Qué pasa:** La rama `aerr != nil` se descarta; un fallo de BD se convierte en "no se pudo resolver el asunto" sin diagnóstico. Comportamiento fail-safe pero opaco.
- **Recomendación:** Loguear `aerr` (slog.Warn con el CUPS) antes de saltar la iteración.

#### N11 — Código muerto: `REG_DOCUMENT_ISSUE_PLACE` inalcanzable pero aún poblado en `Create`
- **Archivo:** `internal/statemachine/handlers/registration.go:842`
- **Categoría:** bug (deuda técnica)
- **Qué pasa:** `regDocumentTypeHandler` salta a `StateRegFirstSurname`; `StateRegDocumentIssuePlace`, `StateRegBirthPlace` y `StateRegOccupation` quedan huérfanos. `createPatientHandler` aún pasa `DocumentIssuePlace` (siempre vacío). El campo no se persiste a SIESA, así que no hay pérdida de datos real.
- **Recomendación:** Eliminar el `Register` y el handler de `StateRegDocumentIssuePlace`, las constantes/StateTypes huérfanas y las entradas residuales en `escalation.go`; quitar `DocumentIssuePlace` de `createPatientHandler` si no se usa.

#### N12 — Comentario de `parseDocType` desincronizado (dice 1..15, catálogo tiene 12)
- **Archivo:** `internal/statemachine/handlers/registration.go:170`
- **Categoría:** bug (documentación)
- **Qué pasa:** Comentarios dicen "15 tipos" / "1..15" pero `documentTypeCatalog` tiene 12 entradas. La validación usa `len(...)` (segura), pero el desfase puede inducir a reintroducir un literal `15`.
- **Recomendación:** Alinear comentarios a 12 (o "12 de los 15 de SIESA, se excluyen AS/MS/SI").

#### N13 — `redactValue` corta por bytes y puede partir runas UTF-8 multibyte
- **Archivo:** `internal/telegram/alert_handler.go:192`
- **Categoría:** bug (cosmético)
- **Qué pasa:** `s[:2]`/`s[len(s)-2:]` sobre bytes puede partir una runa (ej. "Iván") y producir UTF-8 inválido en el mensaje de Telegram. No hay panic (guard `len<=4`). No es exposición de PII (corrompe, no revela).
- **Recomendación:** Operar sobre `[]rune(s)` en lugar de bytes para el slicing del enmascarado.

---

## Hallazgos NUEVOS — verificaciones positivas (sin acción, son fixes correctos)

#### N14 — `preferred_doctor_doc` corregido a cédula (DoctorDocument) en self_reschedule
- **Archivo:** `internal/notifications/self_reschedule.go:129`
- Fix válido: antes enviaba `cod_medi`, que nunca casaba con `sis_medi.cedula`. No es regresión.
- *Nota fuera de alcance:* `confirmation.go:377`, `slots.go:170-177`, `appointments.go:356` aún usan `appt.DoctorID` para la misma clave — candidatos a corregir en un diff futuro.

#### N15 — Migración MRC entidad→contrato en lista de espera (contratos 5/6)
- **Archivo:** `internal/notifications/waiting_list_check.go:70`
- Fix válido: el antiguo `IsMRCEntity` nunca distinguía MRC de Evento (todos los Sanitas comparten EPS005). El flujo WL ahora propaga `ContractCode` correctamente.

---

## Hallazgos YA CONOCIDOS (sin acción nueva; trazados en auditoría/memoria)

| # | Archivo | Título | Severidad | Referencia |
|---|---------|--------|-----------|------------|
| K1 | `webhook_handler.go:553` | Endpoint DTMF ahora fail-closed sin firma — validar IVR real antes de desplegar | medium | N-1 firma voz |
| K2 | `registration.go:677` / `:740` | Sanitas con municipio vacío asigna Evento en vez de MRC (silencioso, financiero) | low | (fusión de 2 hallazgos) |
| K3 | `alert_handler.go:197` | Redacción PII por allowlist no cubre `r.Message` ni claves no listadas | low | N-2 fuga PII |

> **K2** fusiona los dos hallazgos que describían el mismo defecto (`isSanitasMRC("","") → Evento`). En la BD real 0/59.019 Sanitas tienen geo vacía, por eso es inalcanzable hoy; conviene un guard/log defensivo cuando se aborde N2.
> **K1** es el riesgo más alto a vigilar antes de un despliegue: si Bird no firma `fetchCallFlow`, todas las confirmaciones IVR caerán con 401. Validar con una llamada real.

---

## Tabla priorizada (acción recomendada)

| Prioridad | # | Archivo:línea | Severidad | Acción |
|:---------:|---|---------------|:---------:|--------|
| 1 | N1 + N9 | `slot_service.go:173/151` | medium/low | Reaplicar filtros de ventana a cada slot del bloque consecutivo (un solo fix) |
| 2 | K1 | `webhook_handler.go:553` | medium | Validar firma de Bird en IVR real antes de desplegar; plan B = token en URL |
| 3 | N2 | `identification.go:257` | medium | Loguear errores de UpdateContract/UpdateMunicipality |
| 4 | N6 | `appointment_repo.go:1042` | low | Match de slot con conversión 24h (`hhmm24`), no `c.hora` |
| 5 | N7 | `municipality_repo.go:31` | low | Fallback sin filtro de departamento si 0 filas |
| 6 | N4 | `appointment_repo.go:808` | low | Mayor granularidad en `horacan` o retry ante PK viol |
| 7 | N8 | `slot_service.go:110` | low | Excluir preferido restringido por edad en `preferredHasSlots` |
| 8 | N5 | `appointment_repo.go:796` | low | Auditoría batch en un solo INSERT...SELECT...IN(...) |
| 9 | N10 | `slots.go:1032` | low | Loguear `aerr` de FindSubjectTypeForCups |
| 10 | N13 | `alert_handler.go:192` | low | Slicing sobre `[]rune`, no bytes |
| 11 | N11 | `registration.go:842` | low | Eliminar estados/campos huérfanos del registro |
| 12 | N3 / N12 | `appointment_repo.go:778` / `registration.go:170` | low | Corregir comentarios obsoletos |
| 13 | K2 | `registration.go:677` | low | Guard/log para geo vacía de Sanitas (al abordar N2) |
| 14 | K3 | `alert_handler.go:197` | low | Follow-up: ReplaceAttr global (ya documentado) |
