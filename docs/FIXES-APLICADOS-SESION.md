# Fixes aplicados — rama `fix/review-hallazgos-seguros`

Correcciones derivadas de la **auditoría general** (`docs/AUDITORIA-CODEBASE.md`) y el **review del diff** (`docs/REVIEW-DIFF-SESION.md`).
Criterio: **solo cambios seguros** (observabilidad, fixes que restauran el comportamiento intencionado, refactors equivalentes, comentarios/código muerto). Cada cambio con build+tests verdes; tests nuevos donde aplica.

> Excluidos a propósito (cambian diseño/comportamiento o son riesgosos, se abordan aparte): multi-slot 1cita/N-slots, GAP-3 precio MRC (entidad→contrato), `horacan` granularidad (N4), `RescheduleDate` 12h/24h (N6, código muerto), borrar estados del registro (N11), reorden de filtro médico-preferido (N8, inalcanzable), batch audit single-INSERT (N5, refactor no urgente).

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

## Pendientes (fuera de esta rama, requieren decisión)
- 🔵 Estructural: multi-slot 1cita/N-slots; GAP-3 precio MRC (entidad→contrato).
- 🟡 Latentes/código muerto: N4 (horacan granularidad), N6 (RescheduleDate 12h/24h), N8 (orden filtro médico-preferido), N11 (estados huérfanos del registro), N5 (batch audit single-INSERT).
- ⚠️ Pre-despliegue: K1 — validar firma de webhooks de voz con una llamada IVR real.

Leyenda: ⏳ pendiente · 🔧 en curso · ✅ aplicado+verificado

## Notas de verificación
(se completa al final con resultados de build/test/govulncheck)
