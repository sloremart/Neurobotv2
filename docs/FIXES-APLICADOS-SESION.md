# Fixes aplicados — rama `fix/review-hallazgos-seguros`

Correcciones derivadas de la **auditoría general** (`docs/AUDITORIA-CODEBASE.md`) y el **review del diff** (`docs/REVIEW-DIFF-SESION.md`).
Criterio: **solo cambios seguros** (observabilidad, fixes que restauran el comportamiento intencionado, refactors equivalentes, comentarios/código muerto). Cada cambio con build+tests verdes; tests nuevos donde aplica.

> Excluidos a propósito (cambian diseño/comportamiento o son riesgosos, se abordan aparte): multi-slot 1cita/N-slots, GAP-3 precio MRC (entidad→contrato), `horacan` granularidad (N4), `RescheduleDate` 12h/24h (N6, código muerto), borrar estados del registro (N11), reorden de filtro médico-preferido (N8, inalcanzable).
> (Nota: el "batch audit single-INSERT" SÍ se aplicó después, como N-34 del Lote C.)

## Estado

| # | Hallazgo | Archivo | Tipo | Estado | Test |
|---|----------|---------|------|--------|------|
| N1+N9 | Ventanas horarias (contraste/CUPS) no se reaplican a slots consecutivos | `services/slot_service.go` | bug (restaura intención) | ✅ | ✅ nuevo |
| N7 | Búsqueda municipio rompe con "Bogotá, D.C." | `repository/siesa/municipality_repo.go` | bug (fallback aditivo) | ✅ | ✅ BD real |
| N2 | Errores tragados en UpdateContract/UpdateMunicipality | `handlers/identification.go`, `handlers/registration.go` | observabilidad | ✅ | n/a |
| N10 | Error tragado en FindSubjectTypeForCups | `handlers/slots.go` | observabilidad | ✅ | n/a |
| N13 | `redactValue` corta por bytes (rompe UTF-8) | `telegram/alert_handler.go` | fix cosmético | ✅ | ✅ nuevo |
| N3 | Docblock obsoleto en DeleteBatch (DELETE inexistente) | `repository/siesa/appointment_repo.go` | comentario | ✅ | n/a |
| N12 | Comentarios dicen "15 tipos" (catálogo tiene 12) | `handlers/registration.go` | comentario | ✅ | n/a |

## Verificación (Go 1.25)
- **Build:** `go build ./...` ✅
- **Vet:** `go vet ./...` ✅
- **Suite completa con `-race`:** ✅ todos los paquetes OK, sin data races.
- **Lint-new vs main:** ✅ 0 issues.
- **Tests nuevos:**
  - `TestGetAvailableSlots_ContrastWindowConsecutive` (N1) — prueba que un bloque contrastado 16:30+17:00 se rechaza, 16:00+16:30 se acepta.
  - `TestGetAvailableSlots_CupWindowConsecutive` (N9) — bloque TAC 879420 14:30+15:00 rechazado, 14:00+14:30 aceptado.
  - `TestRedactValue_UTF8Safe` (N13) — 4 casos, incl. acentos; invariante `utf8.ValidString`.
  - `TestFormatAttr_RedactsSensitiveKeys` — clave `doc` enmascarada, `appointment_id` intacta.
  - N7 validado contra la BD real: con filtro dept "Bogotá, D.C." → 0 filas; fallback solo-ciudad → recupera "BOGOTA, D.C.".

## Alineación de idioma (doc 29)
- ✅ Identificadores: YA en inglés (la migración los renombró; no quedan identificadores Go en español; "Antares" solo aparece en comentarios históricos).
- ⏸️ **Comentarios español→inglés (~529 líneas): PENDIENTE por decisión** (cosmético; menor valor/costo). Se hará en una pasada dedicada cuando convenga. SQL y strings de negocio quedan en español (exentos por doc 29).

## Lote 2 — Gaps de auditoría general (A+B+C+D+E) — APLICADOS
Todos verificados: build + `go test ./... -race` (14 paq. OK, sin races) + lint + format (Go 1.25).

**A — Seguridad/PII (Ley 1581):**
- N-7 OCR: ya no loguea `content`/`body` crudos (PII) → solo longitud.
- N-8 documento enmascarado en logs/eventos (`MaskDocument` nuevo en utils). +test.
- N-10 `PlaceCall`: `MaskPhone` en log + payload no se vuelca crudo.
- N-13 `HandleConversation`: firma obligatoria con fallback al secret principal (cierra cache-poisoning).
- N-14 RateLimiter keyea por HOST (no host:port). +test.
- N-11 cifrado SIESA configurable (`EXTERNAL_DB_ENCRYPT`, default "disable" = sin cambio).

**B — Observabilidad (logs a errores tragados):** N-26 (pool resume), N-27 (compensación consecutiva), N-28 (UpdateEntity ×3), N-29 (KPI avg session), N-30 (MRC month filter), N-31 (wl_check 3 ramas).

**C — Concurrencia:** N-32 (`WithoutCancel` en `go onCancel` ×3), N-34 (auditoría batch = 1 INSERT en vez de N goroutines), N-35 (drain con `safeProcess`).
- ⏸️ **N-17 (mutex en PendingNotification) y N-33 (claim-then-send): DIFERIDOS** — ver abajo.

**D — Recursos:** N-20 (timeout 30s en los 4 handlers de notificación con `context.Background()`).

**E — Error-handling de fallo:** N-15 (no falsa "confirmada" si falla ConfirmBlock → escala), N-16 (IVR confirm/cancel marcan error real, no éxito), N-25 (escala si entidad vacía), N-23 (preserva edad real en reschedule), N-24 (valida DateTo).

Tests nuevos: `MaskDocument` (+disabled), `RateLimiter_SameHostDifferentPorts`. Tests existentes: sin cambios necesarios (todos verdes).

### Diferidos por riesgo (requieren trabajo dedicado + stress test, chocan con "no romper")
- **N-17** mutex en `PendingNotification`: retrofit de concurrencia con riesgo de deadlock; la carrera es latente (la suite `-race` actual NO la dispara). Hacerlo bien exige proteger todos los accesos a campos y un stress test de carrera.
- **N-33** `MarkNotified` claim-then-send: introduce un nuevo modo de fallo (claim ok + envío falla → notificación perdida) que exige rollback del claim; cambia firma en 2 interfaces + 2 callers + mocks.
- También fuera (diseño): N-18 (phone_mutex deadlock), N-19 (TOCTOU timer), N-21 (FindConsecutiveBlock), N-22 (Valor=0), N-5/N-6 (clasificación contraste/resonancia).

## Lote 3 — Grupo C (seguros que faltaban) + bugs del ultrareview PR#2 — APLICADOS
Verificado Go 1.25: build + `go test ./... -race` (15 paq. OK, sin races) + lint + format.

**Grupo C (no afectan diseño):**
- **N-4** meridiano: derivar am/pm del valor 24h (la heurística "1-6→pm" marcaba 5–6 AM como PM). Validado contra BD (citas.hora es 12h+meridiano; el bot no lee citas.hora). +test `slotToDateTimeComponents`. *(Discrepancia separada documentada: el bot guarda hora en 24h y SIESA usa 12h — no se tocó, requiere decisión.)*
- **N-9** PII: input de documento enmascarado en chat_events (centralizado en `ValidateWithRetry` vía `sensitiveInputStates`).
- **K3** redacción PII global: `ReplaceAttr` en el handler JSON (stdout/archivo) + helper compartido `utils.RedactLogAttr`.
- **N8** filtro médico-preferido respeta restricción de edad (defensivo, inalcanzable hoy). +test.

**Bugs encontrados por la ultrareview del PR #2 (en mis fixes N-15/N-16), corregidos:**
- **bug_004** (N-15): `escalateToAgent` reusaba la nota de timeout ("paciente NO confirmó"), falsa cuando el paciente SÍ confirmó pero falló SIESA. Se parametrizó con `escalationReason` (NoResponse vs SystemFailure): nota correcta + se omite el 2º mensaje al paciente en el fallo de sistema.
- **bug_001** (N-16): el fix IVR no cubría `appt == nil` (no encontrada, sin error) → falso éxito. Ahora `appt==nil` se trata como error en confirm y cancel.

**No aplicados del grupo C (cero beneficio runtime / riesgo):**
- N11 (borrar estados huérfanos del registro) y RescheduleDate 12h/24h (código muerto admin): borrado de código que podría romper la máquina de estados sin poder validar corriendo la app. Documentados como limpieza pendiente.

## Pendientes (fuera de esta rama, requieren decisión)
- 🔵 Estructural: multi-slot 1cita/N-slots; GAP-3 precio MRC (entidad→contrato).
- 🟡 Latentes/código muerto: N4 (horacan granularidad), N6 (RescheduleDate 12h/24h), N8 (orden filtro médico-preferido), N11 (estados huérfanos del registro).
- ⚠️ Pre-despliegue: K1 — validar firma de webhooks de voz con una llamada IVR real.

Leyenda: ⏳ pendiente · 🔧 en curso · ✅ aplicado+verificado

## Notas de verificación
(se completa al final con resultados de build/test/govulncheck)
