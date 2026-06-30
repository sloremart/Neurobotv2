# Reporte de Síntesis — Re-auditoría de KPIs Neuro-Bot + neuro-dashboard (post-remediación #6–#11)

_Fecha: 2026-06-29 · Base: 10 dominios verificados contra código (file:line) · 21 agentes_

## 1. Resumen ejecutivo

La remediación de #6–#11 está **confirmada y correcta** en los seis casos. No quedan regresiones sobre lo ya corregido.

Sin embargo, la auditoría profunda descubrió **hallazgos NUEVOS genuinos** no cubiertos por el ciclo #6–#11:

| Severidad | new_bug confirmados | new_bug (downgraded a low) |
|-----------|---------------------|----------------------------|
| **High**  | 2 | — |
| **Medium**| 7 | — |
| **Low**   | 4 | 3 |
| **Total** | **13** | **3** |

- **2 bugs HIGH** requieren atención prioritaria: corrupción de la lista de espera por contexto "pegado" y subconteo sistemático de la tasa de éxito OCR.
- **7 bugs MEDIUM**: en su mayoría inconsistencias de fuente/contrato JSON que vacían o distorsionan gráficas enteras (notificaciones, sesiones, pacientes).
- **1 falso positivo** (refutado): el path `escalateNotifToAgent` + `/bot resume` **no** infla la conversión.
- **22 mejoras (improvement)** confirmadas, en su mayoría cosméticas/etiquetado; 2 de impacto material (medium).

---

## 2. Confirmación de remediaciones #6–#11

| # | KPI | Veredicto | Nota |
|---|-----|-----------|------|
| **#6** | Conversión real bot→SIESA / discrepancia | ✅ CONFIRMADO | `CountAppointmentsCreated` = COUNT(*) filas-cita; `discrepancy=botCreated−siesaReal` con clamp ≥0; ventanas half-open `[from,to+1d)` alineadas. `bot_user_configured` expuesto. |
| **#7** | Reagendamientos por origen | ✅ CONFIRMADO | `admin` = PACIENTES (`SUM(cancelled+updated)` del JSON), `admin_operations` aparte; unidad coherente; `session_started` ya se emite en proactivas. |
| **#8** | Conciliación bot↔SIESA | ✅ CONFIRMADO | `UNION ALL` cubre consultas (cpa) + procedimientos (cp); cuenta CITAS distintas vía `map[int]`; pares `_cups`; `bot_user_configured` con banner. |
| **#9** | top_cups / appointment_breakdown | ✅ CONFIRMADO | Etiquetado "CUPS/servicio principal", nombre en tooltip; documentado en repo y UI. |
| **#10** | 'Sin respuesta' proactivas | ✅ CONFIRMADO | Derivado `GREATEST(0, sent(confirmation)−confirmed−cancel−reschedule)`; misma fórmula en KPI diario y por canal; `notification_timeout` excluido; `reschedule` expuesto. |
| **#11** | Escalaciones (tabla `escalations`) | ✅ CONFIRMADO | 1 fila/escalación; `from_state` capturado; outcomes returned/completed/expired en los 3 caminos; TTFR limpio de `/bot`; SLA global ≡ por-agente. |

---

## 3. Hallazgos NUEVOS confirmados (new_bug)

### 3.1 HIGH

| KPI | Ubicación | Problema | Recomendación |
|-----|-----------|----------|---------------|
| % conversión + tiempo-a-agendar + `waiting_list_scheduled` | `Neurobotv2/internal/statemachine/handlers/slots.go:85-92,700-717,831-848,1353-1366` | `waiting_list_entry_id` se escribe en sesión solo al avanzar de procedimiento, pero `advanceToNextProcedure` (`WithClearCtx`) **no la limpia**. En órdenes multi-procedimiento, si el procedimiento B agenda, lee el ID *stale* de A y marca A como `scheduled` + emite `waiting_list_booking_success(A)`. A sale de la lista activa sin que el paciente agendara → nunca será notificado al liberarse cupo, y conversión/tiempo-a-agendar quedan inflados. | Añadir `waiting_list_entry_id` al `WithClearCtx` de `advanceToNextProcedure`, o leerla/borrarla atómicamente al agendar. |
| `ocr_attempts` / tasa de éxito OCR | `neuro-dashboard/internal/kpi/repository.go:109-113`; `Neurobotv2/.../medical_order.go:148-160,169` | `mapEventToKPI` suma a `OCRAttempts` solo `ocr_success` y `ocr_failed`; **`ocr_error` y `ocr_timeout` están ausentes** del switch (se emiten a chat_events pero no se cuentan). El denominador queda subcontado → `rate=successes/attempts` (OCR.tsx:27,49) sistemáticamente sobreestimado. El trend (handlers.go:381-383) tiene el mismo undercount. | Incluir `ocr_error`/`ocr_timeout` en `OCRAttempts` (y en la query de trend). |

### 3.2 MEDIUM

| KPI | Ubicación | Problema | Recomendación |
|-----|-----------|----------|---------------|
| Gráfica "Por tipo de notificación" | `neuro-dashboard/internal/kpi/handlers.go:166` vs `frontend/src/pages/Notifications.tsx:20,89,142-144` | El handler responde la clave `breakdown` pero el frontend lee `notification_breakdown` → `b=undefined` → la ChartCard **siempre renderiza `<EmptyState/>`**. El dato subyacente es correcto. Overview/Appointments sí usan la clave esperada. | Renombrar la clave del handler a `notification_breakdown` (consistente con los otros endpoints). |
| Donut "Resultado de citas pasadas" vs StatCard "Total histórico" | `frontend/src/lib/date.ts:1-21`; `frontend/src/pages/Siesa.tsx:40-44`; `analytics_repo.go:131,177` | `today()/daysAgo()/daysAhead()` usan `toISOString()` (UTC). En Colombia (UTC-5) entre ~19:00 y medianoche local, `today()` devuelve el día siguiente. `AppointmentsByState` filtra con `p2=today()` (UTC) pero `NoShowByDay` añade `fecha < CAST(GETDATE() AS DATE)` (local) → `atendida+cancelada+no_show < histTotal`: el donut deja de reconciliar ~5h/día. | Construir las fechas en hora local (no UTC) o alinear ambos cortes a la misma zona. |
| Donut Escaladas (sessions) vs StatCard Escaladas (chat_events) | `Sessions.tsx:42-47,62-66`; `repository.go:97-98,683-695` | Misma etiqueta, dos fuentes: StatCard = `escalated_to_agent` (todas las iniciadas del día); donut = `SUM(status='escalated')` (solo las que siguen escaladas). `Resume/Complete/Expire` sacan la sesión de `escalated` → para fechas pasadas el donut tiende a ~0 mientras el StatCard es grande. Nada reconcilia ambas. | Unificar fuente o renombrar para distinguir "iniciadas" vs "abiertas ahora". |
| Donut Completadas / `avg_session_duration` | `pool.go:774,798`; `manager.go:228-229`; `repository.go:59,93-94` | `handleAgentClose` → `Complete()` pone `status='completed'` pero emite `escalation_closed`, **no** `session_completed`. → el donut Completadas la incluye; el StatCard (`session_completed`) y `avg_session_duration` (filtra `session_completed`) la excluyen. Igual con `CompleteActiveByPhone`. | Emitir también `session_completed` en cierres por agente, o incluir `escalation_closed` en los KPIs de finalización. |
| Total/Escaladas (StatCards y donut) + sesiones por hora | `Neurobotv2/internal/notifications/manager.go:1088-1195` | `escalateNotifToAgent` crea `sess` con `Status=StatusEscalated` vía `Create` pero **no emite ningún chat_event** (ni `session_started` ni `escalated_to_agent`) ni pasa por la tabla `escalations`. → el donut la cuenta, los StatCards y `GetSessionsByHour` no, y el SLA por-escalación (#11) no la captura. | Emitir `session_started`+`escalated_to_agent` y registrar en `escalations` también en este path. |
| `block_reasons.existing_appointment` | `repository.go:838-839`; `medical_validation.go:501-518` | La cadena automática emite **dos** eventos gemelos (`existing_appointment_found` + `appointment_exists_blocked`) y `GetBlockReasons` suma ambos → ~2× en "Motivos de bloqueo clínico". | Contar uno solo o `COUNT(DISTINCT session_id)`. |
| "Docs ingresados" (Patients) | `Patients.tsx:36`; `repository.go:91-92` | El StatCard "Docs ingresados" muestra `total_sessions` (`session_started`), no documentos. Una sesión puede no ingresar documento o ingresar varios. El evento real `document_entered` existe pero no se expone en `/api/patients`. | Exponer y usar `document_entered` para esta tarjeta. |

### 3.3 LOW (confirmados)

| KPI | Ubicación | Problema | Recomendación |
|-----|-----------|----------|---------------|
| `top_cups` | `repository.go:770-780` | `GetTopCups` no valida `code.Valid`/`!=""` (a diferencia de `GetAppointmentBreakdown`) → barra sin etiqueta si `cups_code=''`. | Replicar el guard `code.Valid && code.String!=""`. |
| Efectividad por canal | `repository.go:407-423` | Query agregada sin `GROUP BY`; con ventana vacía todos los `SUM(...)` son NULL y el `Scan` a `int` falla → **`/api/channels` responde 500** y tumba la sección. | `CAST(COALESCE(SUM(...),0) AS SIGNED)` como en `GetRescheduleSummary`. |
| % conversión por CUPS (lista espera) | `repository.go:1029-1036` | El bloque by-CUPS no aplica `AND status <> 'duplicate_found'` (sí lo hace la cabecera 3 líneas arriba) → denominador diluido, no cuadra con la conversión global. | Añadir el filtro `duplicate_found`. |
| `DailyKPIs.gfr_blocked` | `repository.go:116-117`; `medical_validation.go:242,411,425` | `case 'pregnant_blocked': k.GFRBlocked += count` → el campo cuenta EMBARAZADAS. El bloqueo real por GFR ya viaja en `block_reasons.gfr_not_eligible`. Campo redundante y mal nombrado; latente en UI. | Eliminar el campo o mapearlo al evento correcto. |

### 3.4 Reales pero degradados a LOW (impacto menor)

- **"Eligió agendar" fuera de orden cronológico** en el embudo de 11 pasos (`Conversion.tsx:62-73`): orden real es sesión→agendar→documento→paciente; el comentario "ORDEN REAL" es falso. Conteos correctos; defecto de presentación.
- **Conversión Overview mezcla unidades** (citas/sesiones, puede >100% en días multi-CUPS): `Overview.tsx:39`, `repository.go:101-102`. El embudo (fuente canónica) ya está correcto.
- **`appointment_confirmed/cancelled` cuentan ACCIONES, no citas** (`block_size>1` subcuenta): `appointments.go:919-975`, `repository.go:103-106`. `block_size` ya está en `event_data`, agregable sin tocar el bot.

---

## 4. Mejoras opcionales (improvement) priorizadas

**Prioridad media (impacto material en interpretación):**
1. **Embudo cuenta sesiones proactivas sin filtrar `proactive`** (`event_repo.go:167-176,219-221`): tras #7, las confirmaciones proactivas (alto volumen) entran en `TotalSessions` pero nunca recorren el embudo → inflan `DropAfterGreeting` y deflactan `ConversionRate`. El flag `proactive` ya está disponible para segmentar. **Recomendado filtrar.**
2. **No-show suma 'CC' pasadas como inasistencia** e ignora `AsistenciaConfirmada` (`analytics_repo.go:172-180`): si SIESA no transiciona CC→A fiablemente, el % no-show se infla. Separar "no-show puro" (P pasado) de "sin cerrar" (CC pasado), validando con negocio.

**Prioridad baja (etiquetado / reconciliación / UX):**
3. "Documento ingresado" del embudo de agendamiento incluye el flujo "consultar" (`repository.go:251-303`).
4. Tabla de pendientes no refleja "reprogramó" → pacientes que reprogramaron siguen como `pending` (`repository.go:1114-1180`).
5. Chart "Sesiones vs citas" y trends ignoran el selector de fecha (`CURDATE()` fijo): `repository.go:373`, `handlers.go:38-40`; mismo problema en "Sin slots"/top_cups con rótulos "últimos 30 días".
6. "Sin slots" cuenta por evento crudo (reintentos/multi-CUPS), no por sesión (`repository.go:158-159`).
7. `proactives_sent` cuenta los 4 tipos mientras respuestas/no-response filtran `type=confirmation` → no reconcilian (`repository.go:122-123`).
8. Trend "Abandonadas" (`session_closed_inactivity`) vs donut Abandonadas (`status='abandoned'`, incluye escalaciones expiradas): dos definiciones, misma etiqueta.
9. `% Conversión real`/`bot` (`internal_handler.go:500-505`, `Conversion.tsx:154`) son "citas por sesión", pueden >100% y coexisten con `funnelConv` (tasa real) bajo nombres similares — efecto colateral correcto de #6, solo etiquetar.
10. StatCards "Confirmadas/Canceladas" sin hint de que son autogestión de "Mis Citas" (población ≠ "Creadas") (`Appointments.tsx:69-70`).
11. Campo muerto `reschedule_self_service` (declarado, nunca renderizado) + reagendas por autogestión inflan "Creadas" (`Appointments.tsx:23`, `slots.go:1306-1348`).
12. `avg_session_duration` excluye cierres por agente y sesga por frontera de día (`repository.go:52-62`).
13. IVR `not_called` se pinta como badge "pendiente"; estado `no_answer` del modelo nunca se produce (`Notifications.tsx:48-52`).
14. Donut "Resultado" (escalación) muestra claves crudas en inglés y mezcla `escalated` con outcomes terminales (`Escalation.tsx:101,141-143`).
15. Escalación cerrada por `/bot cerrar` sin mensaje libre previo cae en "Sin atender" (`webhook_handler.go:292-298`).
16. `EscalationSLA` expone `resolved/expired/still_open/*_pct` sin uso en UI; `returned` fuera del denominador (`repository.go:613-615,636-640`).
17. Columna "Devol. bot (min)" por-agente sin la aclaración de que solo mide `resume` (`repository.go:524`).
18. Tabla de mal-asignaciones: `total_mal_cups` y key React pueden duplicar el par (cita,CUPS) sin dedupe (`internal_handler.go:338,350`).
19. Filas `duplicate_found` aparecen en tabla "Todos" con badge crudo, sin filtro (`repository.go:915-929`).
20. Ventanas mezcladas Patients/OCR (tarjetas 1 día vs gráficas 30 días) y trends `patient_found/not_found` muertos en el payload (`handlers.go:378-418`).
21. Doble fuente para "escalaciones por estado": `GetTopEscalationStates` (chat_events JSON) vs `GetEscalationByState` (tabla `escalations`, escritura best-effort) → divergen silenciosamente si falla `Create` (`repository.go:727-749` vs `649-659`).

---

## 5. Falsos positivos / ya resueltos

- **REFUTADO — `escalateNotifToAgent` + `/bot resume` infla conversión:** la premisa parcial es cierta (la sesión escalada se crea sin `session_started`), pero `handleAgentResume` (`pool.go:636-652`) **completa primero** la sesión escalada con `Complete()` y luego crea una sesión NUEVA en `startConfirmRescheduleSession` (`confirmation.go:443-449`) que **sí** emite `session_started` antes de llegar a `appointment_created`. Numerador y denominador quedan balanceados; no hay >100% por este path.
- **Conversión global >100% por sesiones proactivas sin `session_started`:** RESUELTO — los 4 paths proactivos emiten `session_started {proactive:true}` (`confirmation.go:448-450,546-548`, `self_reschedule.go:152-154`, `waiting_list.go:84-86`); numerador y denominador incluyen las proactivas.
- **Fix #5 (fugas/leaks por `by_step`):** correcto — `cups_none/already_has_appt/no_slots/booking_failed` se emiten como STEP del flow `agendar` y el frontend las lee de `data.by_step` (`Conversion.tsx:90-92`).
- **Definición de SITUACIÓN SIESA (no doble conteo):** correcto — `CASE` secuencial excluyente, mismo expr en SELECT y GROUP BY; `atendida+cancelada+no_show` complementarias (`analytics_repo.go:123-134,172-180`).
- **Donut de estado de sesión suma al total / `escalation_expired` end-to-end / lista de espera (`scheduled` con `resolved_at`, donut excluye `duplicate_found`):** todos verificados como correctos.

> Nota: varios "improvement" de §4 son efectos colaterales **esperados y correctos** de los fixes #6–#10 (p.ej. unidades citas-vs-sesiones para discrepancia, conversión que incluye proactivas de confirmar/cancelar). Son cuestiones de interpretación/etiquetado de KPI, no errores de cálculo.
