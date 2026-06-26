# Auditoría de Código Base — Neuro-Bot

**Fecha:** 2026-06-26
**Alcance:** Código Go del chatbot (worker, sesión, state machine, notificaciones, scheduler, repos SIESA/local, API interna, observabilidad, OCR, infra).
**Método:** Hallazgos verificados adversarialmente contra el código real y la BD `ZeusSalud_Neuro` (snapshot Docker). Este informe NO sobrescribe `AUDITORIA-CODEBASE.md` (auditoría previa).

---

## Resumen ejecutivo

Se confirmaron **31 hallazgos** reales (tras deduplicación). Ninguno es `critical`. La distribución es:

| Severidad | Total | Nuevos | Conocidos (`known=true`) |
|-----------|-------|--------|--------------------------|
| Critical  | 0     | 0      | 0 |
| High      | 2     | 2      | 0 |
| Medium    | 10    | 9      | 1 |
| Low       | 19    | 16     | 3 |
| **Total** | **31**| **27** | **4** |

### Top riesgos (acción prioritaria)

1. **[H1] Colisión de `selected_slot_id`** (`handlers/slots.go:1426`) — el paciente puede ser agendado y confirmado con un **médico/consultorio/sede distinto** al que eligió, cuando dos médicos del mismo asunto tienen libre la misma fecha+hora. Colisiones reales verificadas en BD. Afecta integridad clínica y consentimiento mostrado.
2. **[H2] Lost update de sesión vía `UpdateConversationID`** (`session/manager.go:228`) — el webhook OUTBOUND escribe la fila completa de la sesión **fuera del phone-lock**, pudiendo revertir `current_state` y campos PII del paciente a mitad de agendamiento, eludiendo la serialización por teléfono del worker.
3. **[M] Notificaciones de cancelación falsas** (`api/internal_handler.go:584`) — si `CancelBatch` falla, igual se envían plantillas "tu cita fue cancelada" a pacientes cuyas citas siguen activas en SIESA, y la respuesta HTTP enmascara el fallo con `status:ok`.
4. **[M] Reagendamiento no transaccional** (`handlers/slots.go:1278`) — crea la cita nueva antes de cancelar la vieja; si la cancelación falla, el paciente queda con **doble cita** ocupando dos bloques de cupos.
5. **[M] Grabación IVR forzada sin consentimiento** (`bird/client.go:651`) — toda llamada de recordatorio graba datos de salud en servidores de Bird (`record:true` incondicional), sin flag ni kill switch — exposición regulatoria Ley 1581.

### Temas transversales detectados

- **Falta de atomicidad/compensación entre unidades de trabajo** (crear-vs-cancelar cita, cita-vs-procedimientos, notificar-vs-resolver): varios hallazgos comparten el patrón "commit parcial + fallo solo logueado".
- **Errores de manejo de tiempo/zona** en endpoints internos (KPIs ISO-week, filtros de logs UTC vs local, rangos `BETWEEN`).
- **Inconsistencias de manejo de errores** (`error` descartado): repetidas en notificaciones, OCR y tracker de llamadas — varias secuelas de remediaciones previas (N-15, N-16, N-46).
- **Datos de salud (Ley 1581):** grabación IVR, PII en `flow_events.reason`, TDS sin cifrar — exposiciones regulatorias de hardening.

---

## Deduplicación aplicada

No hay hallazgos duplicados entre módulos que reporten el mismo defecto. Sí se agruparon patrones afines para lectura, pero cada entrada corresponde a un defecto y archivo distintos. Notas de relación con la auditoría previa:

- **EXTERNAL_DB_ENCRYPT** (infra) mapea sobre N-11 (remediado parcialmente): el residual nuevo es solo la ausencia de la variable en `.env.example`.
- **`citas.hora` 24h vs 12h** (siesa-repo) ya está documentado en comentario del código (líneas 49-50) → `known`.
- **Sobre-cupo concurrente en lista de espera** (notif-rest) coincide con la garantía documentada de "no sobre-cupo" → `known`.
- **`UpdateCallResult` sin captura de error** es secuela de N-16 (ramas timeout/no_answer quedaron fuera de alcance).

---

## HALLAZGOS NUEVOS

### Severidad HIGH

#### [H1] `selected_slot_id` colisiona: agendamiento con médico/agenda equivocados
- **Archivo:** `internal/statemachine/handlers/slots.go:1426`
- **Categoría:** bug
- **Descripción:** `findSelectedSlot` compara solo por `slot.TimeSlot` (`YYYYMMDDhhmm`, sin médico/agenda/sede). La lista mostrada al paciente (MaxSlots=5, ordenada por `Fecha`) puede contener varios slots con idéntico TimeSlot pero distinto médico. Al elegir, se guarda `selected_slot_id = selected.TimeSlot` (pierde el índice→médico) y `findSelectedSlot` devuelve el **primero** que coincide. `createAppointmentHandler` usa `DoctorSiesaCode`/`AgendaID`/`AgendaSede` del slot devuelto → la cita queda con médico/consultorio/sede distintos a los elegidos. Además el primer resumen de confirmación muestra el médico correcto, pero la creación y reconfirmación usan otro. Verificado: colisiones reales en BD (asunto 15 el 2026-06-26; asuntos 10/11 en julio).
- **Recomendación concreta:** Incorporar agenda/médico a la clave del slot seleccionado. Construir `selected_slot_id = fmt.Sprintf("%d|%s", selected.AgendaID, selected.TimeSlot)` (o el índice de la opción mostrada) al guardar en `showSlotsHandler`, y modificar `findSelectedSlot` para hacer match por esa clave compuesta (`AgendaID` + `TimeSlot`, o por índice directo de la lista persistida). Añadir test en `slots_test.go` con dos slots de igual TimeSlot y distinto médico que verifique que se selecciona el correcto.

#### [H2] `UpdateConversationID` fuera del phone-lock revierte el estado de sesión (lost update)
- **Archivo:** `internal/session/manager.go:228`
- **Categoría:** concurrency
- **Descripción:** `UpdateConversationID` hace read-modify-write: `FindActiveByPhone` lee la fila completa, muta solo `ConversationID` y llama `repo.Save(s)`, que reescribe **todas** las columnas (`current_state`, `status`, `patient_*`, `retry_count`, `escalated_*`, `resumed_at`). Se invoca desde el webhook OUTBOUND (`webhook_handler.go:273` y `:412`) **sin** `PhoneMutex().Lock`, a diferencia del worker que sí serializa. Si el webhook lee antes de que el worker persista una transición pero su `Save` aterriza después, el snapshot viejo pisa el estado y revierte el flujo del paciente. El no-op guard (línea 236) lo mitiga en estado estable; la ventana de daño requiere que `conversation_id` cambie + transición concurrente del worker.
- **Recomendación concreta:** Reemplazar el read-modify-write por un UPDATE dirigido: `UPDATE sessions SET conversation_id=? WHERE id=?` (nuevo método estrecho en el repo), de modo que no pueda pisar otras columnas. Alternativa equivalente: adquirir `PhoneMutex().Lock(phone)` dentro de `UpdateConversationID` antes del read-modify-write. Preferir el UPDATE estrecho por menor contención.

---

### Severidad MEDIUM

#### [M1] Notificaciones de cancelación/reagendamiento se disparan aunque `CancelBatch`/`RescheduleDate` fallaron
- **Archivo:** `internal/api/internal_handler.go:584`
- **Categoría:** error-handling
- **Descripción:** En `HandleCancelAgenda`, si `CancelBatch` retorna error se loguea y se fija `cancelled=0`, pero el flujo continúa a la fase de notificación usando la **misma** lista de citas no canceladas. Con `NotifyPatients=true` envía plantillas "tu cita fue cancelada" a pacientes cuyas citas siguen activas en SIESA. La respuesta HTTP devuelve `status:ok, cancelled:0`, enmascarando el fallo de BD. (Nota: el camino de reschedule `handleRescheduleWithNewAgenda` re-consulta otro dataset, por lo que NO sufre el mismo defecto; corrección al hallazgo original.)
- **Recomendación concreta:** Gatear la fase de notificación con `if cancelled > 0` (y/o retornar HTTP 500 cuando `CancelBatch` falla, como ya hace `handleRescheduleSameAgenda`). No reportar `status:ok` cuando la operación de BD falló.

#### [M2] Reagendamiento crea la nueva cita antes de cancelar la vieja; fallo de cancelación deja cita duplicada
- **Archivo:** `internal/statemachine/handlers/slots.go:1278`
- **Categoría:** error-handling
- **Descripción:** En `createAppointmentHandler` el reagendamiento self-service crea la cita nueva (`CreateWithConsecutive`, línea 1234) y **después** intenta cancelar la vieja (`CancelBatch`, línea 1280). Si la cancelación falla, solo se loguea (`slog.Error`, reenviado a Telegram si está configurado) y el flujo continúa a `BOOKING_SUCCESS`: el paciente queda con dos citas activas ocupando dos bloques de slots, sin reintento ni compensación. (`CancelBatch` es atómico internamente, pero no hay atomicidad entre crear-nueva y cancelar-vieja.)
- **Recomendación concreta:** Reordenar o compensar. Opción robusta: cancelar la vieja **antes** de crear la nueva (si la creación luego falla, el paciente queda sin cita pero sin duplicado — estado más fácil de recuperar). Si se mantiene el orden actual, al fallar `CancelBatch` no avanzar a `BOOKING_SUCCESS`: marcar la sesión como inconsistente, escalar de forma estructurada a un agente con los IDs de ambas citas, y/o encolar un reintento de cancelación.

#### [M3] Recordatorios/IVR notifican a pacientes con cita ya confirmada (filtro estado solo excluye 'C')
- **Archivo:** `internal/scheduler/tasks.go:123`
- **Categoría:** bug
- **Descripción:** `sendWhatsAppReminders` (123) y `sendVoiceReminders` (258) usan `FindPendingByDate`, cuyo WHERE es `c.fecha = @p1 AND c.estado != 'C'` (excluye solo canceladas, no confirmadas). El repo sí calcula `appt.Confirmed` (estado 'CC'/'A' o `AsistenciaConfirmada=1`) pero el loop nunca lo consulta. Resultado: pacientes que ya confirmaron reciben de nuevo la plantilla de confirmación. El bot se autoinflige el caso: `Confirm` hace `UPDATE citas SET AsistenciaConfirmada=1` sin cambiar estado (sigue 'P'). (El caso 'A' del día siguiente NO es alcanzable empíricamente.)
- **Recomendación concreta:** Gatear el envío en el loop con `if firstAppt.Confirmed { continue }`. Adicionalmente endurecer el WHERE de `FindPendingByDate` para excluir `AsistenciaConfirmada=1` y estados `IN ('CC','A')`.

#### [M4] Handler de semanas gestacionales malinterpreta respuestas de botón "1"/"2" y bloquea al paciente
- **Archivo:** `internal/statemachine/handlers/medical_validation.go:659`
- **Categoría:** bug
- **Descripción:** En `askGestationalWeeksHandler` la rama numérica `ParseFloat` (para resume del agente con número de semana) corre **antes** de `ValidateButtonResponse`. Un paciente que responde los botones Sí/No tecleando "1"/"2" (comportamiento común en WhatsApp) cae en línea 659: `weeks=1→weeksInt=10`, `weeks=2→weeksInt=20`, ambos bajo `gr.min` (110/180) → rama fuera de rango → autocierre "vuelve cuando estés en ese rango". Un paciente que SÍ está en rango queda desviado hasta que toca el chip. Afecta solo CUPS 881436/881437 (ecografía de embarazo). (No es "permanente": tocar el chip o reiniciar funciona.)
- **Recomendación concreta:** Invertir el orden: ejecutar `ValidateButtonResponse` primero (para resolver la respuesta de botón) y solo si NO es una respuesta de botón válida intentar `ParseFloat` para el número de semana del agente. Alternativa: distinguir el resume del agente con un marcador de contexto (p.ej. `agent_resume=1`) en vez de heurística numérica.

#### [M5] CUPS no contrastable descarta señal OCR de contraste y salta el chequeo renal (TFG)
- **Archivo:** `internal/statemachine/handlers/medical_validation.go:70`
- **Categoría:** bug
- **Descripción:** `askContrastedHandler` decide la vía de contraste/TFG solo por el prefijo del CUPS (`isContrastable`: 883/871/879). Si `isContrastable(cupsCode)` es false retorna de inmediato, fija `is_contrasted=0` y limpia `ocr_is_contrasted` — **aunque** el OCR haya marcado la orden como contrastada (`ocr_is_contrasted=="1"`). Disparador concreto verificado: orden de resonancia contrastada donde el OCR lista el código de sedación 998702 antes del código RM → `cups_code=998702` (no contrastable) + `ocr_is_contrasted=1` → se saltan los gates renal (creatinina/TFG) y de embarazo, y se sub-reserva slots. Es fail-open en un gate de seguridad clínica.
- **Recomendación concreta:** Mantener la vía de contraste/TFG cuando `ocr_is_contrasted=="1"` aunque `isContrastable(cupsCode)` sea false. Cambiar la guarda temprana a: `if !isContrastable(cupsCode) && ctx["ocr_is_contrasted"] != "1" { return non-contrast path }`. Considerar también no usar el código de sedación 998702 como `cups_code` representativo del grupo.

#### [M6] `task_failed` graba `err.Error()` crudo en `flow_events.reason` (VARCHAR 60): rompe el batch y puede filtrar PII
- **Archivo:** `internal/scheduler/scheduler.go:110`
- **Categoría:** resource
- **Descripción:** `observability.Emit(... Reason: err.Error())` escribe el mensaje de error completo del driver/SIESA en `reason VARCHAR(60)`. El tracer no recorta ni redacta `Reason`. Con `STRICT_TRANS_TABLES` activo (verificado en el contenedor MySQL 8), un error >60 chars provoca `Data too long for column reason`; como `InsertBatch` corre todo el lote en una transacción y retorna al primer `ExecContext` con error, **un solo `task_failed` largo tumba el batch completo** (hasta 200 eventos). Además errores de SQL Server pueden contener PII y se sirven sin redactar por `/api/internal/anomalies` y `/flow-events`. (Los autores ya añadieron `maxReasonLength` en la ruta de agenda-cancel; esta ruta no lo tiene.)
- **Recomendación concreta:** En `tracer.emit` (o en el emisor) truncar `Reason` a 60 chars y stripear el detalle del driver, reutilizando el `maxReasonLength` ya existente. Defensa adicional: que `InsertBatch` haga truncado/skip por fila en vez de abortar el lote completo.

#### [M7] Dedup almacenado antes de confirmar el encolado: mensajes descartados por overflow quedan deduplicados
- **Archivo:** `internal/worker/pool.go:222`
- **Categoría:** concurrency
- **Descripción:** `Enqueue()` registra el ID en `recentMessages` (LoadOrStore) e incrementa `dedupCount` **antes** de intentar encolar. Si el canal está lleno Y `activeOverflow >= maxOverflowGoroutines`, el mensaje se descarta (`return false`) pero la entrada de dedup queda por `dedupTTL` (5 min). En el retry de Bird (mismo `msg.ID`), `InsertIfNotExists` ve la fila ya existente y hace ACK **sin** re-encolar; y aunque re-encolara, la entrada de dedup lo descartaría. La fila inbox queda `pending` y solo se reproduce en el **reinicio** del proceso (no hay re-replay periódico). Requiere sobrecarga sostenida (~120 mensajes concurrentes vs 10 workers).
- **Recomendación concreta:** Registrar la marca de dedup **solo después** de encolar con éxito (o de lanzar la goroutine de overflow). En el camino de drop hacer `recentMessages.Delete(msg.ID)` y revertir `dedupCount`. Opcional: añadir un re-replay periódico de filas inbox `pending` antiguas en el scheduler.

#### [M8] Grabación de llamadas IVR forzada (`record:true`) sin consentimiento ni kill switch — dato de salud
- **Archivo:** `internal/bird/client.go:651`
- **Categoría:** security
- **Descripción:** `PlaceCall` fija incondicionalmente `record:true` / `recordStart:record-from-answer`. Cada recordatorio telefónico graba en servidores de Bird una locución con datos personales y de salud (nombre, fecha/hora de cita, clínica y dirección). No hay flag de configuración ni gate de consentimiento; `IVR_NOTIFICATIONS_ENABLED` solo decide si se llama, no si se graba. La función DTMF (confirmar/cancelar) NO requiere `record:true`. Exposición regulatoria Ley 1581 (datos sensibles, almacenados en un tercero procesador, default-on).
- **Recomendación concreta:** Hacer la grabación configurable y por defecto **desactivada**: añadir `IVR_RECORD_CALLS` (default false) y condicionar `record`/`recordStart` a esa flag. Documentar la variable en `.env.example`. Si se mantiene grabación, exigir base legal/consentimiento explícito y control operativo (kill switch).

#### [M9] `HandleWeeklyKPIs` calcula el lunes equivocado cuando el 4 de enero es domingo (off-by-one en 2026)
- **Archivo:** `internal/api/internal_handler.go:1170`
- **Categoría:** bug
- **Descripción:** El lunes ISO se calcula con `... - int(jan4.Weekday()-time.Monday)`. `time.Weekday()` da Domingo=0, pero ISO trata Domingo como día 7. Cuando el 4 de enero cae en domingo (como **2026**), `jan4.Weekday()=0` y el offset queda `-1` en vez de `+6`, así que el lunes computado queda una semana tarde y la agregación de 7 días suma la semana equivocada. Afecta **todas** las consultas KPI semanales de 2026. Endpoint interno de dashboard; no afecta agendamiento ni datos clínicos.
- **Recomendación concreta:** Usar `daysSinceMonday := (int(jan4.Weekday())+6)%7` y restar ese valor: `monday := jan4.AddDate(0, 0, (weekNum-jan4Week)*7 - daysSinceMonday)`. Añadir test que verifique el lunes para un año con 4-ene domingo (2026) y otro normal (2024).

---

### Severidad LOW

#### [L1] Gate de cobertura evalúa solo el CUP primario, ignora códigos alternativos
- **Archivo:** `internal/statemachine/handlers/slots.go:231`
- **Categoría:** bug
- **Descripción:** `isCupCovered` se evalúa solo con `cupsCode` (primario), pero la búsqueda de slots prueba `cupsCode + alternativeCodes`. En grupos multi-CUP (EMG/NC de Fisiatría), el código EMG primario suele tener `Precio=0` (sin convenio) mientras el NC del mismo grupo sí tiene precio. Verificado en BD (manuales 08/15/10). El gate corta con "tu EPS no tiene convenio" antes de probar el alternativo cubierto, desviando al paciente a particular/agente. Existe válvula de escape (hablar con agente).
- **Recomendación concreta:** Evaluar `isCupCovered` sobre `cupsCode + alternativeCodes` (cubierto si cualquiera tiene precio>0), o mover el gate **después** de resolver `successfulCupsCode` que efectivamente encontró slots.

#### [L2] `GetFunnel` usa `BETWEEN` inclusivo en lugar del rango half-open del resto del repo
- **Archivo:** `internal/repository/local/event_repo.go:425`
- **Categoría:** sql
- **Descripción:** `GetFunnel` filtra con `created_at BETWEEN ? AND ?`, mientras el resto del archivo usa `>= ? AND < ?` (N-44). El caller parsea `from`/`to` a medianoche; con `created_at` TIMESTAMP, `BETWEEN` excluye todos los eventos del último día (00:00:01–23:59:59). El funnel omite silenciosamente el día final. (La premisa de "sargabilidad" del hallazgo original es incorrecta; el defecto real es la cota superior inclusiva.)
- **Recomendación concreta:** Sustituir por el rango half-open consistente: computar `nextDay := to.AddDate(0,0,1)` y usar `created_at >= from AND created_at < nextDay`.

#### [L3] El contador de eventos descartados (`t.dropped`) nunca se lee, loguea ni expone
- **Archivo:** `internal/observability/tracer.go:239`
- **Categoría:** error-handling
- **Descripción:** `emit()` incrementa `t.dropped.Add(1)` cuando el buffer (1024) está lleno, pero `dropped` nunca se lee en ningún lado. No hay métrica, log periódico ni endpoint que reporte pérdida de traza. El drop-on-full es intencional; lo ausente es surfacear el contador. Impacto muy bajo (pérdida de traza diagnóstica).
- **Recomendación concreta:** Loguear `dropped` a nivel Warn periódicamente (p.ej. en el flush) cuando sea >0, o exponerlo en un `HealthMetrics`/endpoint de salud.

#### [L4] Cita huérfana posible: compensación de inserción de procedimientos es best-effort y solo logueada
- **Archivo:** `internal/services/appointment_service.go:323`
- **Categoría:** resource — **known: parcialmente (limitación estructural documentada)**
- **Descripción:** `CreateWithConsecutive` comitea la cita + N slots atómicamente, luego inserta procedimientos en un paso separado no transaccional. Si la inserción falla intenta `CancelBatch`; si ese cancel también falla, solo loguea "orphan appointment": queda una cita 'P' ocupando N slots con cero procedimientos, invisible para los checks de reconciliación existentes. Requiere doble fallo (rara).
- **Recomendación concreta:** Añadir un check de reconciliación que detecte citas 'P' activas sin filas en `citas_procedimientos`/`citas_procedimientos_asuntos` (orphan-zero-procedures) y las marque para revisión/cancelación. Encolar un reintento de la compensación en vez de solo loguear.

#### [L5] Error de `io.ReadAll` en el body de respuesta de OpenAI se descarta silenciosamente
- **Archivo:** `internal/services/ocr_service.go:224`
- **Categoría:** error-handling
- **Descripción:** `respBody, _ = io.ReadAll(resp.Body)` descarta el error. En un read truncado (reset mid-body) con status 200, `respBody` queda parcial y produce un fallo confuso de `json.Unmarshal` en vez de un error de red claro que dispararía el retry. (Matiz: el síntoma exacto es `unmarshal api response`, no el mensaje de usuario citado en el hallazgo original.)
- **Recomendación concreta:** Capturar el error de `io.ReadAll` y tratarlo como error de transporte (logueando y entrando a la ruta de retry) en vez de continuar a `Unmarshal` con un body posiblemente parcial.

#### [L6] `io.ReadAll` sin límite sobre media descargada puede agotar memoria
- **Archivo:** `internal/services/ocr_service.go:366`
- **Categoría:** resource
- **Descripción:** `AnalyzeDocument` descarga el media y lo lee con `io.ReadAll(resp.Body)` sin cap, luego lo codifica base64 (~1.33x). Sin `MaxBytesReader`/guard de Content-Length. Mitigado porque la URL proviene del CDN de Bird/WhatsApp (no arbitraria) con límites upstream y timeout de 60s; el peor caso realista es un PDF ~100MB inflando ~233MB transitorio × sesiones concurrentes.
- **Recomendación concreta:** Envolver `resp.Body` en `io.LimitReader` a un máximo razonable (15–20 MB) y rechazar payloads que excedan, con mensaje al paciente para reenviar un archivo más pequeño.

#### [L7] Retry de input inválido en `confirmIdentity`/`confirmContactInfo` pierde el re-prompt de botones
- **Archivo:** `internal/statemachine/handlers/identification.go:164`
- **Categoría:** error-handling
- **Descripción:** Ambos handlers retornan el `*StateResult` crudo de `ValidateButtonResponse` en input inválido, cuyo único mensaje es "Por favor selecciona una de las opciones disponibles." **sin** botones. Todos los demás handlers de botón del módulo re-renderizan los botones Sí/No. El paciente recibe un nudge solo-texto. Mitigado: los botones del estado anterior siguen tocables en el hilo de WhatsApp; tras maxRetries escala.
- **Recomendación concreta:** En la rama de input inválido, sobrescribir `result.Messages` para re-renderizar los botones Sí/No, espejando el patrón de `confirmOCRResultHandler`/`askEpsRegimenHandler`.

#### [L8] `HandleResponse` archiva reschedule/cancel a `notification_history` antes de que el paciente complete la confirmación 1/2
- **Archivo:** `internal/notifications/manager.go:322`
- **Categoría:** bug
- **Descripción:** Tras `LoadAndDelete` se llama `persister.Resolve(..., responseStatus(normalized))` **antes** del switch. Para 'reschedule'/'cancel' esto mueve la notificación a `notification_history` con estado 'rescheduled'/'cancelled' aunque solo se pulsó el botón; la sesión de confirmación 1/2 puede abortarse/expirar. Nada vuelve a tocar `notification_history`, así que el registro de auditoría sobre-declara el desenlace. Único consumidor de la tabla: limpieza a 45 días.
- **Recomendación concreta:** Mover la llamada a `Resolve` a un estado más cercano al desenlace real (tras confirmar 1/2 en los handlers `CONFIRM_RESCHEDULE_NOTIF`/`CONFIRM_CANCEL_NOTIF`), o registrar un estado intermedio ('reschedule_pending'/'cancel_pending') al pulsar el botón y actualizar a 'rescheduled'/'cancelled' solo al completar.

#### [L9] `escalateToAgent` ignora el error de `EscalateToAgent`
- **Archivo:** `internal/notifications/confirmation.go:292`
- **Categoría:** error-handling
- **Descripción:** La llamada a `m.birdClient.EscalateToAgent` descarta el error, a diferencia de la hermana `escalateNotifToAgent` (manager.go:1149) que lo verifica, loguea y retorna. (Matiz: `EscalateToAgent` ya loguea internamente en rutas degradadas y asigna al equipo vía `AssignFeedItem`, así que no es totalmente silencioso; el defecto real es no distinguir éxito/fallo para la propia traza y emitir el evento 'escalated' incondicionalmente.)
- **Recomendación concreta:** Capturar el error como en `escalateNotifToAgent` (loguear `slog.Error` y propagar) y condicionar el `LogEvent`/observability 'escalated' al éxito de la asignación.

#### [L10] `RestorePending` procesa timeouts vencidos de forma síncrona en el arranque
- **Archivo:** `internal/notifications/manager.go:865`
- **Categoría:** resource
- **Descripción:** `RestorePending` recorre todas las filas y por cada vencida llama `handleTimeout` síncronamente dentro del bucle (sin LIMIT, sin goroutine, sin backoff). Cada `handleTimeout` adquiere phoneLock + ctx 30s y, con `ConfirmFollowupEnabled=true`, hace llamadas de red bloqueantes. Se invoca bloqueante en el arranque (main.go:250). (Mitigado: `ConfirmFollowupEnabled` default false; kill switches gatean envíos; backlog realista de decenas.)
- **Recomendación concreta:** Procesar las entradas vencidas en una goroutine acotada (worker pool con límite de concurrencia y/o backoff) en vez de inline, para no serializar el startup. Considerar `LIMIT` por corrida en `FindAll`.

#### [L11] `HandleVoiceGatherResult`: `UpdateCallResult` en ramas 'no_dtmf'/'no_answer' sin capturar error
- **Archivo:** `internal/notifications/manager.go:759`
- **Categoría:** error-handling
- **Descripción:** En la rama default (sin tecla) y en `HandleVoiceCallCompleted`, `UpdateCallResult` se invoca descartando el error de forma desnuda (sin `_ =`), a diferencia de las ramas confirm/cancel (secuela de N-16). Viola errcheck latente. Impacto solo en KPI `communication_calls` (tabla local).
- **Recomendación concreta:** Anteponer `_ =` (o capturar y loguear) en ambas líneas (759 y 792), espejando las ramas hermanas ya remediadas.

#### [L12] `sendWaitingNotification` reporta éxito aunque `RegisterPending` falle (respuesta del paciente irrecuperable)
- **Archivo:** `internal/notifications/waiting_list_check.go:181`
- **Categoría:** concurrency
- **Descripción:** La entrada queda `notified` (claim) antes de `RegisterPending`. `RegisterPending` es void y aborta en silencio si `lockPhone` da timeout; en ese caso no se guarda el pending y `sendWaitingNotification` igual retorna true, así que el caller no revierte el claim. Al tocar el botón, `HandleResponse` no encuentra pending → respuesta descartada. (Matiz: un fallo solo de `Upsert` SÍ deja el pending en memoria; solo el timeout de `lockPhone` pierde memoria+BD. Baja probabilidad.)
- **Recomendación concreta:** Hacer que `RegisterPending` retorne `error` y que `sendWaitingNotification` revierta el claim (`MarkWaiting`/des-`MarkNotified`) y retorne false si falla, para que el caller pueda recuperar la entrada.

#### [L13] `handleCancellation` usa `context.Background()` sin timeout (rompe patrón N-46)
- **Archivo:** `internal/notifications/reschedule.go:20`
- **Categoría:** resource
- **Descripción:** `handleCancellation` crea `ctx := context.Background()` sin deadline, a diferencia de los hermanos que acotan a 30s (N-46). El ctx alimenta `m.tracker.LogEvent` (síncrono, `ExecContext` contra BD local). Si ese write se cuelga, la goroutine queda bloqueada indefinidamente mientras sostiene el phoneLock del paciente, bloqueando futuras operaciones para ese teléfono.
- **Recomendación concreta:** Acotar el ctx a 30s como los hermanos: `ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second); defer cancel()`.

#### [L14] El catch-up ejecuta tareas que envían WhatsApp/IVR a cualquier hora del reinicio
- **Archivo:** `internal/scheduler/scheduler.go:164`
- **Categoría:** error-handling
- **Descripción:** `RunMissedTasks` (invocado en el arranque, main.go:373) corre cualquier tarea cuyo horario ya pasó hoy sin last-run, incluyendo `whatsapp_reminders`, `waiting_list_check` y `voice_reminders`. Un reinicio a las 22:00–23:00 dispararía envíos de WhatsApp y llamadas IVR a pacientes en horario nocturno. No hay ventana horaria que limite el catch-up de notificaciones salientes.
- **Recomendación concreta:** Añadir una ventana horaria permitida para el catch-up de tareas de notificación saliente (p.ej. solo entre 07:00–20:00), o excluir del catch-up las tareas marcadas como "notificación a paciente" y dejarlas solo en su horario programado.

#### [L15] Goroutines de overflow y virtuales usan `context.Background()`, desligándose del shutdown
- **Archivo:** `internal/worker/pool.go:250`
- **Categoría:** concurrency
- **Descripción:** Las goroutines de overflow (`Enqueue`, 250) y `EnqueueVirtual` (1215) usan `context.WithTimeout(context.Background(), 2*time.Minute)` en vez de `p.ctx`. Son wg-tracked, así que `Stop()` espera hasta 2 min, pero main solo concede 20s y luego cierra las DBs → una goroutine aún corriendo ejecuta queries contra un pool cerrado (`sql: database is closed`). El código declara `p.ctx` justo para esto. Impacto graceful (error logueado, no panic; mensajes reales se reproducen por WAL).
- **Recomendación concreta:** Derivar el ctx de overflow de `p.ctx`: `context.WithTimeout(p.ctx, 2*time.Minute)`, para que el cancel de app-ctx interrumpa estas goroutines y `Stop()` no compita con el cierre de DB.

#### [L16] `retrySend` y goroutines de close-feed no son WaitGroup-tracked e ignoran app context
- **Archivo:** `internal/worker/pool.go:996`
- **Categoría:** resource
- **Descripción:** `go p.retrySend(...)` (996) duerme 10s y llama a Bird; las goroutines close-feed (760, 1075) duermen 3s y llaman `CloseFeedItems`. Ninguna se añade a `p.wg` ni observa `p.ctx`, así que `Stop()` no las espera y pueden disparar tras el teardown. `retrySend` puede re-entregar un mensaje ~10s post-shutdown. Solo llamadas HTTP a Bird (no tocan SIESA); recovery de panic presente; caudal autolimitado.
- **Recomendación concreta:** Rastrear estas goroutines con `p.wg.Add(1)`/`defer p.wg.Done()` y/o respetar `p.ctx.Done()` durante el sleep (usar `select` con timer + `p.ctx.Done()`), para que el shutdown las espere o las cancele.

#### [L17] Invariante `CloseMin > ReminderMin` no validada; `formatMinutes` puede emitir minutos negativos
- **Archivo:** `internal/session/manager.go:320`
- **Categoría:** bug
- **Descripción:** El comentario dice `CloseMin must be > ReminderMin` pero no hay validación. `closeIn := CloseMin - ReminderMin`; si un override deja `INACTIVITY_CLOSE_MIN <= INACTIVITY_REMINDER_MIN`, `closeIn` es negativo y el paciente recibe "...se cerrará la sesión en -5 minutos". `formatMinutes` no acota negativos. Defaults (120 vs 20) seguros; requiere override del operador.
- **Recomendación concreta:** Validar el invariante en `config.go` (función `Validate` que falle el arranque o ajuste a un default seguro si `CloseMin <= ReminderMin`), y añadir un clamp `if m < 0 { m = 0 }` en `formatMinutes`.

#### [L18] El filtro por teléfono de `/api/internal/logs` nunca coincide porque los teléfonos se almacenan enmascarados
- **Archivo:** `internal/logging/reader.go:128`
- **Categoría:** bug
- **Descripción:** `readAndFilter` filtra con `strings.Contains(line, phone)` usando el número completo (`+57...`), pero la política de redacción escribe los teléfonos enmascarados en disco (`+573***3616`). La substring del número completo nunca aparece, así que el filtro devuelve siempre vacío silenciosamente (salvo `LOG_MASK_PHONES=false`). La herramienta de soporte por teléfono no sirve en producción y puede inducir a creer que "no hubo actividad". Falla cerrado (no expone PII).
- **Recomendación concreta:** Enmascarar el query con `utils.MaskPhone` antes de filtrar (`strings.Contains(line, MaskPhone(phone))`), o buscar la forma enmascarada del número.

#### [L19] `findLogFiles` compara fechas de archivo (UTC medianoche) contra from/to en zona local
- **Archivo:** `internal/logging/reader.go:80`
- **Categoría:** bug
- **Descripción:** `extractDate` parsea `neuro-bot-YYYY-MM-DD.log` con `time.Parse` (UTC), mientras `from`/`to` se construyen con `ParseInLocation(time.Local)`. Con offset negativo (Colombia UTC-5) y un `from` con hora-del-día posterior a las 19:00 local, el archivo del propio día se excluye por error y sus entradas (22:00–23:59 local) se pierden porque `readAndFilter` ni abre el archivo. Endpoint interno de diagnóstico; sin corrupción ni fuga.
- **Recomendación concreta:** Parsear la fecha del nombre en la misma Location que `from`/`to`: `time.ParseInLocation("2006-01-02", base, time.Local)`.

---

## HALLAZGOS CONOCIDOS (`known=true` — ya documentados o limitación estructural)

> No requieren recomendación nueva; se listan para trazabilidad. Confirmar si deben mantenerse o cerrarse.

#### [K1] `EXTERNAL_DB_ENCRYPT` por defecto en 'disable' y ausente de `.env.example` (PII sobre TDS en claro)
- **Archivo:** `internal/database/mysql.go:64` — **medium / security**
- Mapea sobre N-11 (remediado parcialmente: encriptación se hizo configurable pero default sigue 'disable'). Residual nuevo: la variable no está en `.env.example`, así que un deploy que copie el ejemplo corre TDS sin cifrar. Explotabilidad condicional (topología cross-host de prod sin confirmar + atacante en red). **Sugerencia:** default `encrypt=true` con `TrustServerCertificate` para LAN interna, y documentar la variable en `.env.example`.

#### [K2] `citas.hora` escrito en 24h por el bot pero la UI de SIESA usa 12h
- **Archivo:** `internal/repository/siesa/appointment_repo.go:56` — **low / bug**
- Documentado en comentario del propio código (líneas 49-50) como discrepancia de convención pendiente. Divergencia cosmética latente en pantallas de personal/recordatorios para citas PM; sin corrupción ni crash. **Sugerencia:** decidir la convención y, si se adopta 12h, transformar `timeStr` a 12h antes del insert en `citas.hora`.

#### [K3] `CheckWaitingListForCups` concurrente sobre el mismo CUP puede sobre-notificar
- **Archivo:** `internal/notifications/waiting_list_check.go:30` — **low / concurrency**
- Coincide con la garantía documentada de "no sobre-cupo". Solo serializa por entrada (`MarkNotified`), no por slot; dos corridas concurrentes del mismo CUP pueden notificar más pacientes que cupos. Benigno: la notificación es invitación, el booking usa optimistic lock en SIESA (no hay doble agenda real). **Sugerencia:** serializar por `cupsCode` (lock o singleflight) en el camino en tiempo real.

#### [K4] Cita huérfana por compensación best-effort (limitación estructural)
- **Archivo:** `internal/services/appointment_service.go:323` — **low / resource**
- Ver [L4] arriba (se mantiene en la sección de nuevos por su recomendación accionable de reconciliación; la limitación estructural de "procedure insert fuera de la tx de `repo.Create`" es conocida).

---

## Tabla priorizada

| # | Sev | Cat | Módulo | Archivo:línea | Nuevo | Título corto |
|---|-----|-----|--------|---------------|-------|--------------|
| H1 | High | bug | handlers-appt | slots.go:1426 | Sí | Slot seleccionado colisiona → médico/agenda equivocados |
| H2 | High | concurrency | session | manager.go:228 | Sí | `UpdateConversationID` fuera del lock revierte sesión |
| M1 | Medium | error-handling | api | internal_handler.go:584 | Sí | Notif. de cancelación falsa cuando `CancelBatch` falla |
| M2 | Medium | error-handling | handlers-appt | slots.go:1278 | Sí | Reagenda no transaccional → doble cita |
| M3 | Medium | bug | scheduler | tasks.go:123 | Sí | Recordatorios a pacientes ya confirmados |
| M4 | Medium | bug | handlers-reg | medical_validation.go:659 | Sí | Botón "1"/"2" malinterpretado en semanas gestacionales |
| M5 | Medium | bug | handlers-reg | medical_validation.go:70 | Sí | CUPS no contrastable salta chequeo renal (TFG) |
| M6 | Medium | resource | observability | scheduler.go:110 | Sí | `err.Error()` crudo rompe batch de `flow_events` + PII |
| M7 | Medium | concurrency | worker | pool.go:222 | Sí | Dedup antes del encolado pierde mensajes en overflow |
| M8 | Medium | security | bird | client.go:651 | Sí | Grabación IVR forzada sin consentimiento (Ley 1581) |
| M9 | Medium | bug | api | internal_handler.go:1170 | Sí | KPI semanal off-by-one (4-ene domingo, 2026) |
| K1 | Medium | security | infra | mysql.go:64 | No | TDS sin cifrar por default + falta en `.env.example` |
| L1 | Low | bug | handlers-appt | slots.go:231 | Sí | Gate de cobertura ignora códigos alternativos |
| L2 | Low | sql | local-repo | event_repo.go:425 | Sí | `GetFunnel` `BETWEEN` excluye el día final |
| L3 | Low | error-handling | observability | tracer.go:239 | Sí | Contador `dropped` nunca surfaceado |
| L4 | Low | resource | services-appt | appointment_service.go:323 | Sí | Cita huérfana sin procedimientos (doble fallo) |
| L5 | Low | error-handling | services-ocr | ocr_service.go:224 | Sí | Error de `io.ReadAll` (OpenAI) descartado |
| L6 | Low | resource | handlers-reg | ocr_service.go:366 | Sí | `io.ReadAll` sin límite sobre media |
| L7 | Low | error-handling | handlers-reg | identification.go:164 | Sí | Re-prompt sin botones en confirmación identidad |
| L8 | Low | bug | notif-core | manager.go:322 | Sí | Auditoría sobre-declara reschedule/cancel |
| L9 | Low | error-handling | notif-core | confirmation.go:292 | Sí | `EscalateToAgent` error ignorado |
| L10 | Low | resource | notif-core | manager.go:865 | Sí | `RestorePending` síncrono bloquea startup |
| L11 | Low | error-handling | notif-core | manager.go:759 | Sí | `UpdateCallResult` (no_dtmf/no_answer) sin `_ =` |
| L12 | Low | concurrency | notif-rest | waiting_list_check.go:181 | Sí | `RegisterPending` falla silenciosa → respuesta perdida |
| L13 | Low | resource | notif-rest | reschedule.go:20 | Sí | `context.Background()` sin timeout (N-46) |
| L14 | Low | error-handling | scheduler | scheduler.go:164 | Sí | Catch-up envía WhatsApp/IVR en horario nocturno |
| L15 | Low | concurrency | worker | pool.go:250 | Sí | Overflow ctx desligado del shutdown |
| L16 | Low | resource | worker | pool.go:996 | Sí | `retrySend`/close-feed no wg-tracked |
| L17 | Low | bug | session | manager.go:320 | Sí | Minutos negativos por invariante no validada |
| L18 | Low | bug | misc | reader.go:128 | Sí | Filtro de logs por teléfono nunca coincide (enmascarado) |
| L19 | Low | bug | misc | reader.go:80 | Sí | Filtro de archivos de log UTC vs local (off-by-one) |
| K2 | Low | bug | siesa-repo | appointment_repo.go:56 | No | `citas.hora` 24h vs 12h (UI SIESA) |
| K3 | Low | concurrency | notif-rest | waiting_list_check.go:30 | No | Sobre-notificación concurrente lista de espera |

---

*Informe generado a partir de hallazgos verificados adversarialmente. Para el detalle de razonamiento de verificación de cada ítem, consultar el dataset de hallazgos confirmados de esta sesión.*
