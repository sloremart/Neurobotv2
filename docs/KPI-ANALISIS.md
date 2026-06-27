# Análisis de KPIs del dashboard + seeder de datos de validación

Fecha: 2026-06-27.

Este documento (1) analiza la coherencia de los KPIs que muestra el dashboard y propone KPIs
faltantes de alto valor, y (2) describe el seeder que llena la BD local con datos derivados de
SIESA para validar todo el tablero.

---

## 1. Coherencia de los KPIs actuales

Inventario: el dashboard calcula sus KPIs leyendo `chat_events` (≈150 `event_type` distintos),
`flow_events` (flows: agendar, escalacion, lista_espera, notif_recordatorio, registro, mis_citas,
admin_agenda, …), `sessions`, `waiting_list`, `notification_pending` y, para SIESA, los endpoints
del bot (ocupación, citas por situación, conciliación).

**Observaciones (a corregir, fuera del alcance de este trabajo):**

1. **KPIs "de hoy" volátiles.** Overview, Sesiones, Agendamiento, Notificaciones, OCR y Pacientes
   usan una fecha única (hoy por defecto). En un día lento o a primera hora, las tarjetas se ven
   casi vacías aunque las tendencias (7–30 días) tengan datos. → Recomendación: selector de rango
   o default de "últimos 7 días" para las tarjetas de cabecera.

2. **Dos embudos solapados.** Existen `/api/funnel` (sobre `chat_events`, 11 pasos:
   session_started→…→appointment_created) y `/api/flow-stats?flow=agendar` (sobre `flow_events`:
   ocr_ok→slots_found→booking_success + fugas). La vista Conversión usa el segundo; el endpoint
   `/api/funnel` (más detallado) **no lo consume ninguna página**. → Surface el detallado o eliminar
   el endpoint muerto para evitar divergencias.

3. **"Citas según el bot" vs verdad de SIESA.** `appointment_created/confirmed/cancelled`
   (`chat_events`) reflejan lo que el bot **creyó** hacer; la verdad son las `citas` de SIESA (lo
   mide la nueva vista de Conciliación). Pueden divergir si una creación falló tras loguear el
   evento. → Etiquetar la vista Agendamiento como "según el bot" y apoyarse en Conciliación para la
   verdad.

4. **Confirmación/cancelación: definición correcta es la de SIESA.** Ya migramos la vista "Citas por
   situación" a la definición real (confirmada = `AsistenciaConfirmada=1` o estado CC/A; cancelada =
   estado 'C'). Los eventos del bot (`appointment_confirmed`) son una señal paralela, no la verdad.

---

## 2. KPIs faltantes de alto valor (recomendados, para implementar después)

Priorizados por valor/esfuerzo:

1. ✅ **No-show real (SIESA) — IMPLEMENTADO (2026-06-27).** Citas pasadas no canceladas que nunca
   pasaron a Atendida (`estado='A'`). Cálculo en el servidor (no la heurística client-side anterior):
   solo `fecha < hoy` (las pendientes futuras no son inasistencia) y `estado` NULL cuenta como no-show.
   - Bot: `siesa.AnalyticsRepo.NoShowByDay` + `GET /api/internal/siesa/no-show?from&to`
     (NOLOCK, GROUP BY servidor, cache TTL, MAXDOP 1). Tipo `domain.NoShowRow`.
   - Dashboard: proxy `GET /api/siesa/no-show` → vista `Siesa.tsx` (tarjetas "No-show" y "% No-show"
     + tendencia diaria atendidas vs no-show). Reemplazó el cálculo `pendiente+confirmada` del cliente.
2. ✅ **Conversión real bot→SIESA — IMPLEMENTADO (2026-06-27).** Sesiones del bot vs citas **reales**
   en SIESA (`cod_user_asigna_cita` = cédula del bot, sobre `fecha_solicitud`), no solo el evento
   `appointment_created` (lo que el bot creyó). Expone `% conversión real`, `% según el bot` y la
   **discrepancia** (citas que el bot registró pero no aterrizaron en SIESA).
   - Bot: `siesa.AnalyticsRepo.BotCreatedByDay` + `GET /api/internal/siesa/conversion?from&to`
     (cruza `GetFunnel` local con el conteo real de SIESA). Tipo `domain.BotCreatedRow`.
   - Dashboard: proxy `GET /api/siesa/conversion` → sección "Conversión real bot→SIESA" en
     `Conversion.tsx` (sesiones, citas reales, % real vs % bot, discrepancia, citas reales por día).
     El embudo existente quedó etiquetado "según el bot".
3. ✅ **Efectividad de la lista de espera — IMPLEMENTADO (2026-06-27).** Conversión (agendados/inscritos),
   declinación, expiración y tiempo medio hasta agendar (`resolved_at - created_at`), más efectividad
   por CUPS. Solo dashboard (lee `waiting_list` en MySQL directo, sin tocar el bot).
   - `kpi.Repository.GetWaitingListEffectiveness` + `GET /api/waiting-list/effectiveness?days=30`.
   - Vista: sección "Efectividad · últimos 30 días" en `WaitingList.tsx` (% conversión/declinación/
     expiración, horas a agendar, donut por estado, barras de % conversión por CUPS).
4. ✅ **Reagendamientos consolidados — IMPLEMENTADO (2026-06-27).** Total por origen: autogestión del
   paciente (`notification_reschedule_self_service`), confirmados por notificación
   (`notification_reschedule_confirmed`) y admin (`admin_reschedule_agenda`), + tendencia diaria. Solo
   dashboard (agrega `chat_events` en MySQL directo).
   - `kpi.Repository.GetRescheduleSummary` + `GET /api/reschedules?days=30`.
   - Vista: sección "Reagendamientos consolidados · últimos 30 días" en `Notifications.tsx` (4 tarjetas
     por origen + donut por origen + tendencia diaria).
5. ✅ **SLA de escalación — IMPLEMENTADO (2026-06-27).** % atendidas por el agente, % resueltas vs
   expiradas (sobre las cerradas), tiempo de respuesta del agente y su distribución por buckets
   (<5 / 5–15 / 15–60 / >60 min). Solo dashboard (lee `sessions` en MySQL directo).
   - `kpi.Repository.GetEscalationSLA` + `GET /api/escalation/sla?days=30`.
   - Vista: sección "SLA de escalación · últimos 30 días" en `Escalation.tsx`.
   - Tiempo = `escalated_at → last_agent_msg_at`. CAVEAT: `last_agent_msg_at` es la actividad del
     agente (se sella en cada saliente humano); = primera respuesta en escalaciones de un intercambio,
     cota superior en conversaciones largas. Si se necesita la primera respuesta exacta, habría que
     agregar una columna `first_agent_msg_at` en el bot (no hecho).
6. ✅ **Efectividad por canal — IMPLEMENTADO (2026-06-27).** Tasa de confirmación y de respuesta
   WhatsApp vs IVR. WhatsApp cuenta solo envíos de tipo confirmación (`notification_sent` type=
   confirmation → `notification_confirmed`/`notification_cancel_confirmed`/`notification_timeout`);
   IVR usa `notification_ivr_sent`/`notification_confirmed_ivr`/`notification_cancelled_ivr` y deriva
   "sin respuesta". Solo dashboard (agrega `chat_events` en MySQL directo).
   - `kpi.Repository.GetChannelEffectiveness` + `GET /api/channels?days=30`.
   - Vista: sección "Efectividad por canal · WhatsApp vs IVR" en `Notifications.tsx`.

---

## 3. Seeder de validación (`cmd/seed-kpis`)

Herramienta **solo de desarrollo** (build tag `seed`; no entra al binario del server) que llena las
tablas de analítica de la BD local con datos **derivados de las citas reales de SIESA** + flujos
sintéticos, para ver todos los KPIs poblados y verificar el dashboard.

**Diseño:**
- Lee SIESA (SQL Server) y escribe la BD local (MySQL).
- **Backbone**: por cada cita real (ventana configurable) genera la sesión + secuencia coherente de
  `chat_events` (session_started→…→appointment_created con el CUPS/médico/fecha reales) +
  `flow_events`, y según el estado real de la cita (AsistenciaConfirmada / estado 'C') emite la
  confirmación o cancelación. Usa datos reales del paciente (sis_paci).
- **Sintéticos**: fugas del embudo, OCR fallido, escalaciones, registros, lista de espera (todos los
  status), notificaciones (timeouts, reagendas, admin), fuera de horario, bloqueos GFR/embarazo, y
  `notification_pending` en vivo + sesiones activas.
- Reparte en el tiempo con cobertura de **hoy** (KPIs "de hoy") y horario hábil (gráfico por hora).
- Al final imprime el resumen de totales esperados.

**Uso:**
```bash
LOCAL_DSN='root:***@tcp(host.docker.internal:13308)/neuro_bot' \
SIESA_DSN='sqlserver://sa:***@host.docker.internal:1433?database=ZeusSalud_Neuro&encrypt=disable' \
go run -tags seed ./cmd/seed-kpis --yes --days=45
```
Seguridad: exige `--yes` y aborta si el `LOCAL_DSN` parece de producción.

**Resultado de la validación (2026-06-27):** con 8.025 citas de SIESA como backbone + flujos
sintéticos (≈189k chat_events, 58k flow_events), **todas las vistas del dashboard quedaron
pobladas y coherentes**: hoy 178 citas / 1192 sesiones / 178 completadas; agendamiento por
especialidad (Neurología 39, Proc. Fisiatría 31, Resonancia 24…); notificaciones por tipo;
lista de espera por status; embudo agendar con fugas; escalaciones; IVR; entidades top. Nota: el
dashboard cachea los KPIs en memoria — tras re-sembrar hay que reiniciar el contenedor del
dashboard para refrescar.
