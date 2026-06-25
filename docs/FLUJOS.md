# Catálogo de Flujos — Neuro-Bot

> Mapa de **todos** los flujos implementados (recorrido completo del código, 2026-06-25).
> Motor conversacional: `internal/statemachine/machine.go`. Estado inicial de toda sesión:
> `CHECK_BUSINESS_HOURS`. Los estados **auto** se auto-encadenan (guard de 20 iteraciones); los
> **interactivos** esperan input del paciente.

---

## A. Entrada e infraestructura de mensajes

| # | Flujo | Entrada | Archivo |
|---|-------|---------|---------|
| A1 | **Webhook WhatsApp inbound** — verifica firma HMAC → WAL (`inbox`) antes de responder 200 → 3 ramas: (a) postback a notificación → `HandleResponse`; (b) texto libre con notif pendiente → `HandleInvalidInput`; (c) mensaje normal → `Enqueue` → state machine | `POST /api/webhooks/whatsapp` | `api/webhook_handler.go:101` |
| A2 | **Webhook outbound / comandos de agente** — cachea `conversationID`, detecta `/bot` de agentes | `POST /api/webhooks/whatsapp/outbound` | `webhook_handler.go:170` |
| A3 | **Webhook Conversations** — caché de `conversationID` (created/updated/deleted) | `POST /api/webhooks/conversations` | `webhook_handler.go:328` |
| A4 | **Webhook de Voz** — IVR 2 fases (gather al iniciar, DTMF al terminar) | `POST /api/webhooks/voice` | `webhook_handler.go:424` |
| A5 | **Webhook DTMF** — devuelve callFlow (TTS) y procesa la tecla | `POST /api/webhooks/voice/dtmf` | `webhook_handler.go:551` |
| A6 | **Worker pool** — 10 workers; `Enqueue`/`EnqueueVirtual`/`EnqueueAgentCommand`; **phone-lock** (30s); `processMessage` → convID → kill-switch `BOT_ENABLED` → state machine → `sendAndSave`; drain en apagado | — | `worker/pool.go` |
| A7 | **WAL replay** al arranque — re-procesa mensajes inbox no completados | startup | `main.go:286-318` |

---

## B. Flujos conversacionales del paciente (state machine)

### B1. Entrada / Saludo / Menú (`handlers/greeting.go`)
- `CHECK_BUSINESS_HOURS` → en horario `GREETING`→`MAIN_MENU`; fuera `OUT_OF_HOURS`→`OUT_OF_HOURS_MENU`.
- `MAIN_MENU`: `pet_ct` (escala), `agendar` (→B4 entidad), `consultar` (→B10 mis citas), `resultados`, `ubicacion`, `ayuda`.
- **Interceptores globales** (`interceptors.go`): mensaje no soportado, imagen fuera de contexto, palabras de escalación (agente/asesor/humano/ayuda), reset de menú (menu/inicio/0).

### B2. Identificación por documento (`handlers/identification.go`)
- `ASK_DOCUMENT_TYPE` → `ASK_DOCUMENT` → `PATIENT_LOOKUP`:
  - existe → `CONFIRM_IDENTITY` → verificación contacto (`SHOW_CONTACT_INFO`/`ASK_UPDATE_PHONE`/`ASK_UPDATE_EMAIL`/`UPDATE_CONTACT_INFO`)
  - no existe + agendar → `REGISTRATION_START`; no existe + consultar → cierra.
- `routeAfterContactInfo` (`identification.go:188`): bifurca a **mis citas** o **orden médica** (con resolución de contrato EPS).

### B3. Registro de paciente nuevo (`handlers/registration.go`) — 20 pasos
`REG_FIRST_SURNAME → SECOND_SURNAME → FIRST_NAME → SECOND_NAME → BIRTH_DATE → GENDER → BLOOD_TYPE → MARITAL_STATUS → PHONE → PHONE2 → EMAIL → USER_TYPE → AFFILIATION_TYPE → MUNICIPALITY → ZONE → ADDRESS → BARRIO → CONFIRM_REGISTRATION → (REG_SELECT_CORRECTION) → CREATE_PATIENT`. Crea fila completa en `sis_paci`.

### B4. Entidad / EPS / Contrato (`handlers/entity_management.go` + `eps_contract.go`)
- `ASK_CLIENT_TYPE` (Particular/EPS/Prepagada/Especial/ARL/Póliza) → `ASK_EPS_REGIMEN` → `SHOW_ENTITY_LIST` → `ASK_ENTITY_NUMBER`.
- **Contrato:** matriz Sanitas/Salud Total/Compensar/Capital por régimen; **Sanitas MRC vs Evento depende del municipio** (depto 50 + 12 municipios) → captura forzada de municipio (`CONFIRM_MUNICIPALITY` → `finalizeSanitasMunicipality`).
- Sub-flujo legacy paciente existente: `CHECK_ENTITY`/`CONFIRM_ENTITY`/`CHANGE_ENTITY`.

### B5. Orden médica / OCR (`handlers/medical_order.go` + `services/ocr_service.go`)
- `ASK_MEDICAL_ORDER` (Particular: ¿tiene orden? sin orden = escala) → `UPLOAD_MEDICAL_ORDER` (imagen/PDF→Ghostscript→GPT-4o-mini Vision) → extrae CUPS+cantidad+contraste+sedación+documento.
- `VALIDATE_OCR` (valida contra catálogo `cups_procedimientos` + fallback código base) → `CONFIRM_OCR_RESULT`.
- Alternas: `ASK_MANUAL_CUPS`/`SELECT_PROCEDURE` (manual), `AnalyzeText` (agente).

### B6. Agrupación por servicio (`services/procedure_grouper.go`)
Agrupa CUPS y calcula `Espacios` (slots contiguos) con reglas por servicio: **Fisiatría** (EMG/NC, máx 4), **Resonancia** (combinaciones, sedación 998702, bilateral ×2), **Radiografía**, **Tomografía** (879910→3), **Ecografía** (obstétrica, Doppler), **Neurología** (890274/890374 nunca juntos). Multi-procedimiento secuencial (`advanceToNextProcedure`).

### B7. Validación médica (`handlers/medical_validation.go`)
- `CHECK_SPECIAL_CUPS` (embarazo→`ASK_GESTATIONAL_WEEKS`; sueño→escala) → `ASK_CONTRASTED` → (mujer fértil `ASK_PREGNANCY`/`PREGNANCY_BLOCK`; bebé `ASK_BABY_WEIGHT`) → **GFR** (`GFR_CREATININE`/`GFR_HEIGHT`/`GFR_WEIGHT`/`GFR_DISEASE`/`GFR_RESULT`; **<30 = no apto contraste** → `GFR_NOT_ELIGIBLE`) → `ASK_SEDATION`.
- Elegibilidad: `CHECK_EXISTING` (ya tiene cita), `CHECK_PRIOR_CONSULT` (médico previo), `CHECK_MRC_LIMIT` (flag), `CHECK_AGE_RESTRICTION`.

### B8. Búsqueda de slots + cobertura (`handlers/slots.go` + `services/slot_service.go` + `repository/siesa/schedule_repo.go`)
- **Gate de cobertura:** precio del **contrato del paciente** en `sis_proc_precios`; precio 0/inexistente → `COVERAGE_NO_CONVENIO` (particular o agente).
- `SEARCH_SLOTS`: filtros médico preferido, restricción de edad por doctor, ventana contraste (7-17h sin sábados), ventana CUPS (TAC 879420 10-15h), **filtro mensual MRC** (sufijo=cantidad), slots contiguos. Asunto desde catálogo (sedación→17).
- Salidas: `SHOW_SLOTS` (paginado) / `SLOT_SEARCH_RETRY` / `NO_SLOTS_AVAILABLE` (→`OFFER_WAITING_LIST`).

### B9. Creación de cita (`repository/siesa/appointment_repo.go` — 1 cita = N slots, transaccional)
- `CONFIRM_BOOKING` → `RECONFIRM_BOOKING` → `CREATE_APPOINTMENT`:
  - **CONSULTA** (asuntos 1,7,8,9,10,11) → `citas` + `citas_procedimientos_asuntos` (con `Valor`) + UPDATE slot.
  - **PROCEDIMIENTO / IMAGEN** → `citas` + `citas_procedimientos` (1 fila/CUPS) + UPDATE N slots.
  - Reclamo optimista de slots; compensación `CancelBatch` si falla; auditoría `log_citas` async.
- Salidas: `BOOKING_SUCCESS` (preparación/video/audio; siguiente grupo o cierre), `BOOKING_FAILED` (slot tomado→re-busca).

### B10. Mis citas (`handlers/appointments.go`)
- `FETCH_APPOINTMENTS` → `LIST_APPOINTMENTS` → `APPOINTMENT_ACTION`:
  - **Confirmar** → `CONFIRM_APPOINTMENT` → `ConfirmBlock` (todas las citas del día).
  - **Cancelar** → `CANCEL_APPOINTMENT` → `CancelBlock` (+ dispara lista de espera).
  - **Reagendar** → `SEARCH_SLOTS` (cancela la vieja al crear la nueva).
  - **Preparación** / volver / menú.
- Sub-flujo notificaciones: `NOTIF_PENDING`, `CONFIRM_RESCHEDULE_NOTIF`, `CONFIRM_CANCEL_NOTIF`, `NOTIF_RESCHEDULE_FALLBACK`.

---

## C. Flujos proactivos (notificaciones / IVR / lista de espera)

### C1. Respuesta del paciente a un recordatorio (`notifications/manager.go` `HandleResponse`)
Claim atómico → despacha por tipo:
- **confirmation/reschedule** → confirmar / reprogramar (`CONFIRM_RESCHEDULE_NOTIF`) / cancelar (`CONFIRM_CANCEL_NOTIF`).
- **cancellation** → entendido / reprogramar auto-servicio.
- **waiting_list** → agendar (`SEARCH_SLOTS` precargado) / rechazar.

### C2. Cadena de seguimiento/escalación (`notifications/confirmation.go` `handleConfirmationTimeout`)
**Deshabilitada por defecto** (`CONFIRMATION_FOLLOWUP_ENABLED=false`): followup1 (3h) → followup2 → IVR → post-IVR (30min) → escala a agente. Con default: recordatorio 07:00 → IVR 13:00 → resuelto.

### C3. IVR (`HandleVoiceGatherResult`/`HandleVoiceCallCompleted`)
Oprime **1=confirma** / otra=cancela (todas las citas del día) / sin tecla=pendiente / no contesta=buzón. Notas internas al agente + KPI.

### C4. Reagendamiento auto-servicio (`notifications/self_reschedule.go`)
Tras cancelación admin, sesión `SEARCH_SLOTS` con perfil precargado.

### C5. Lista de espera (`notifications/waiting_list_check.go`)
- **Entrada:** auto al no haber slots (cancelación admin) o manual (`OFFER_WAITING_LIST`).
- **Match oferta↔demanda** (`CheckWaitingListForCups`): capacidad real, FIFO-con-skip, anti-duplicado, bloque contiguo, **claim-then-send**, descuento de capacidad. Disparado en tiempo real (al cancelar) y diario (06:00).
- **Salidas:** agendar / rechazar / expira 6h / `duplicate_found` / libera ofertas >24h / purga 30 días.

### C6. Historial de notificaciones (`notification_history`)
Toda resolución se archiva con estado + conversation_id (evidencia); limpieza 45 días.

---

## D. Tareas programadas (scheduler `internal/scheduler/tasks.go`)

| Tarea | Hora | Días | Qué hace |
|-------|------|------|----------|
| `data_cleanup` | 02:00 | diario | Purga WL (30d), libera ofertas (24h), WAL (24h), historial notif (45d) |
| `waiting_list_check_06` | 06:00 | L-V | Match oferta↔demanda por cada CUP |
| `whatsapp_reminders` | 07:00 | diario | Recordatorio a las citas de mañana (idempotente: salta a quien ya tiene pending) |
| `voice_reminders` (IVR) | **13:00** | diario | Llamada IVR a quienes no respondieron WhatsApp |
| Catch-up | arranque | — | Re-ejecuta tareas perdidas del día (idempotente) |
| Expiración notif | cada 1 min | — | Procesa pendientes vencidas |

---

## E. Comandos de agente (`/bot ...`, solo en sesión escalada)
`/bot` o `/bot reset` (reinicia), `/bot resume [ESTADO] [data]` (retoma; sub-rama `NOTIF_PENDING confirm|reschedule|cancel`), `/bot orden <texto>` (OCR de texto), `/bot cups <c1:q> ...` (inyecta CUPS), `/bot cerrar` (cierra), `/bot info` (resumen interno).

---

## F. Endpoints admin/internos (`/api/internal/*`, auth X-API-Key + rate-limit)
Cancelar agenda · Reagendar agenda (misma/nueva) · Chequeo manual lista de espera · Listar lista de espera · KPIs (diario/semanal/funnel/health) · Test-alert · Send-reminders manual · Send-agenda-confirmations · Test-voice-call · Logs · Events · Sessions · Session-context · `/health` (sin auth) · `/health/debug`.

---

## G. Ciclo de vida y transversales
- **Goroutines de fondo:** signal-handler, telegram-alerts, phone-mutex-cleanup, bird-cache-cleanup, notification-expiry, inactivity-checker, workers+dedup, capacity-monitor, scheduler, gather-cleanup, http-server.
- **Inactividad** (`session/manager.go`): recordatorio "¿Sigues ahí?" → cierre por inactividad; expiración de escaladas.
- **Escalación a agente** (`handlers/escalation.go`): resuelve team por CUPS, transfiere en Bird, resumen+comandos → `ESCALATED`.
- **Auth:** InternalAuth (fail-closed), RateLimiter (por IP host), HMAC webhooks (fail-open si no hay secreto), redacción PII global en logs.
- **Apagado ordenado:** HTTP (10s) → worker drain (20s) → scheduler drain (20s) → cierre DBs.

---

## ⚠️ Comentarios desactualizados en el código (no son bugs)
1. **IVR corre a las 13:00**, no 13:00/15:00 (comentarios "Task 15:00" / "15:00 IVR" obsoletos).
2. **`waiting_list_check` corre 06:00 L-V** (un comentario dice "Task 08:00").
3. **La cadena de followup está DESHABILITADA por defecto** (flujo real: recordatorio→IVR→fin).
4. **`RescheduleDate` NO es código muerto** — se usa en `reschedule-agenda` escenario "misma agenda".
