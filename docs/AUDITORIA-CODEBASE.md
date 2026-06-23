# Auditoría del Código Base — Neuro-Bot

> Informe consolidado de hallazgos confirmados (verificados adversarialmente) sobre el chatbot de agendamiento médico Neuro-Bot (WhatsApp + SIESA/SQL Server).
> Cada hallazgo fue validado contra el código real y, donde aplica, contra la BD `ZeusSalud_Neuro`.

---

## 1. Resumen ejecutivo

### Conteo por severidad (tras deduplicación)

| Severidad | Total | Conocidos (en curso) | Nuevos |
|-----------|:-----:|:--------------------:|:------:|
| Critical  | 0     | 0                    | 0      |
| High      | 4     | 1                    | 3      |
| Medium    | 14    | 1                    | 13     |
| Low       | 22    | 0                    | 22     |
| **Total** | **40**| **2**                | **38** |

> Se partió de 49 entradas. Tras deduplicar familias equivalentes (multi-slot N-citas, voice webhook fail-open, RateLimiter, contraste/sedación frágil, $0 pricing, onCancel ctx, PII en logs) quedan 40 hallazgos únicos.

### Top riesgos (acción prioritaria)

1. **[HIGH · seguridad] Webhooks de voz/IVR con verificación HMAC fail-open** — omitir el header de firma evade por completo la validación y permite confirmar/cancelar citas (datos de salud) sin autenticación. Fix trivial y de alto impacto.
2. **[HIGH · seguridad] Cédula del paciente (Ley 1581) enviada a Telegram y a logs en texto plano** — el `AlertHandler` global vuelca todos los atributos de cada log de nivel ERROR a un chat de Telegram externo; varios logs incluyen `DocumentNumber` sin enmascarar.
3. **[HIGH · bug, CONOCIDO] Reserva multi-cupo crea N citas en vez de 1 cita ocupando N cupos** — corrompe conteos MRC, liquidación y la vista del personal en la UI de SIESA.
4. **[MEDIUM · bug] Reagendar desde "Mis citas" cancela TODO el bloque consecutivo pero recrea solo 1 cita** — pérdida silenciosa de procedimientos/consultas hermanas del paciente.
5. **[MEDIUM · bug] Detección de contraste/sedación por `strings.Contains` sensible a mayúsculas/acentos** — ~49% de citas de contraste reales no coinciden, afectando filtrado de slots y seguridad clínica del estudio.

### Temas transversales

- **Privacidad (Ley 1581):** múltiples fugas de PII de salud (cédula, CUPS, EPS, teléfono) a logs de 30 días y a Telegram. El proyecto enmascara teléfonos (`utils.MaskPhone`) pero no documentos ni nombres.
- **Manejo de errores fail-open/silencioso:** varios caminos confirman/cancelan citas o continúan el flujo descartando el error de la escritura a SIESA, dejando estado divergente sin traza.
- **Propagación de contexto:** uso recurrente de `context.Background()` sin timeout y goroutines fire-and-forget que ignoran la cancelación en el apagado.
- **Concurrencia:** races latentes en el manager de notificaciones y en el mutex por teléfono.

---

## 2. Hallazgos CONOCIDOS (ya documentados / en curso)

> No requieren re-triage; se listan para trazabilidad. Las recomendaciones ya están alineadas con el diseño objetivo de `CLAUDE.md`.

### K-1 · [HIGH · bug] Reserva multi-cupo crea N citas (una por slot) en vez de 1 cita ocupando N cupos
- **Archivos:** `internal/repository/siesa/appointment_repo.go:472`, `internal/services/appointment_service.go:390`
- **Resumen:** `Create()` reclama exactamente un cupo por llamada y `CreateWithConsecutive()` lo invoca una vez por espacio (`for i:=0;i<espacios;i++`), generando N filas en `citas`; los CUPS solo se adjuntan a la primera (i==0), dejando N-1 citas reales sin procedimientos. Contradice `CLAUDE.md` (FLUJO B: una sola cita + UPDATE por rango `DATEADD`).
- **Impacto:** distorsiona conteos MRC (`CountMonthlyByGroup` cuenta filas), liquidación y la vista del personal en SIESA.
- **Estado:** ítem conocido #1 de la lista en curso.

### K-2 · [MEDIUM · bug] WL resume fuerza `total_procedures=1` con `espacios` multi-slot
- **Archivo:** `internal/notifications/waiting_list.go:49`
- **Resumen:** `handleWaitingList` fija `total_procedures="1"` / `current_procedure_idx="0"` mientras pasa `entry.Espacios` y `entry.ProceduresJSON` completos. El defecto real es una **discordancia de índice/CUPS**: una entrada de lista de espera creada para un grupo con índice >0 guarda `CupsCode=gN` pero `ProceduresJSON=[g0,g1,...]`; al reanudar busca slots para `gN` (correcto) pero **inserta `groups[0]`** (CUPS/asunto/precio equivocados).
- **Recomendación:** derivar `total_procedures`/`current_procedure_idx` de la entrada, o reducir `procedures_json` al grupo relevante al crear la entrada (que `groups[0]` siempre sea el CUPS de la entrada).
- **Estado:** familia del conocido #1, pero el alcance específico (WL-resume) lo trata el prompt como separado.

---

## 3. Hallazgos NUEVOS por severidad y categoría

### 3.1 — HIGH

#### N-1 · [seguridad] Voice webhooks omiten verificación HMAC cuando falta el header de firma (auth bypass)
- **Archivos:** `internal/api/webhook_handler.go:417` (`HandleVoiceWebhook`) y `:542` (`HandleVoiceDTMF`)
- **Detalle:** ambos handlers verifican la firma solo dentro de `if signature != "" { ... }`. Omitir el header `MessageBird-Signature` salta la validación por completo y alcanza `notifyManager.HandleVoiceGatherResult` / `HandleVoiceCallCompleted`, que confirman/cancelan citas reales (datos de salud) por `callID`. Las rutas están montadas en el mux público sin `InternalAuth` (`cmd/server/main.go:380`). Los handlers de WhatsApp/conversations sí verifican de forma obligatoria (fail-closed), probando que la postura correcta es conocida.
- **Atenuante real:** la mutación requiere que el `callID` (UUID generado por Bird) exista en `callIDMap`, que solo vive durante la ventana de la llamada activa — reduce la explotabilidad ciega, pero el defecto de auth sigue siendo real.
- **Recomendación concreta:** hacer la verificación de firma **obligatoria** en ambos handlers de voz, tratando firma vacía como inválida → `401`, idéntico a los handlers de WhatsApp/conversations (`VerifySignatureWithKey` ya retorna `false` para firma vacía). Eliminar el guard `if signature != ""`.

#### N-2 · [seguridad] Cédula del paciente (Ley 1581) registrada en ERROR y reenviada a Telegram en claro
- **Archivos:** `internal/telegram/alert_handler.go:177` (sink); fuentes en `internal/repository/siesa/patient_repo.go:176-183` y `internal/statemachine/handlers/registration.go:867-872`
- **Detalle:** `AlertHandler` es el handler global de slog cuando Telegram está configurado (`cmd/server/main.go:78-80`). `formatMessage` vuelca **todos** los atributos de cada record de nivel ERROR a Telegram (`formatAttr` solo HTML-escapa el valor). Varios `slog.Error` cargan `"doc", input.DocumentNumber` (cédula). No existe lista de permitidos/denegados ni enmascarado para `doc`/`cedula`/`name`; sí existe `MaskPhone` para teléfonos, evidenciando la inconsistencia.
- **Recomendación concreta:** en `formatMessage`/`formatAttr` aplicar una **lista de claves sensibles** (`doc`, `cedula`, `document`, `documento`, `name`, `nombre`, `phone`) y enmascararlas/omitirlas antes de enviar a Telegram, replicando la disciplina de `MaskPhone`. Idealmente, agregar un `ReplaceAttr` global en el handler base que redacte estas claves para TODOS los sinks (stdout, archivo y Telegram).

#### N-3 · [bug] Reagendar desde "Mis citas" fuerza `total_procedures=1` y cancela todo el bloque consecutivo
- **Archivo:** `internal/statemachine/handlers/appointments.go:357` (con cadena en `slots.go:1065/1119-1132`)
- **Detalle:** en `appt_reschedule` se construye `procedures_json` solo desde `selectedAppt.Procedures` (una cita) y se fija `total_procedures="1"`, `current_procedure_idx="0"`, aun cuando `FindConsecutiveBlock` detectó un bloque de varias citas con CUPS distintos. Como `reschedule_appt_id` está presente y `reschedule_skip_cancel != "1"`, `createAppointmentHandler` reconstruye el **bloque completo** vía `FindBlockByAppointmentID` y lo cancela entero, pero recrea solo 1 cita. Resultado: se pierden los procedimientos/consultas hermanas. Verificado contra la BD: existen bloques consecutivos con CUPS distintos por paciente.
- **Mismo antipatrón:** `notifications/self_reschedule.go:118`, `notifications/confirmation.go:368`.
- **Recomendación concreta:** al reagendar un bloque multi-CUPS, o bien (a) migrar todas las citas del bloque (recrear cada grupo en el nuevo horario consecutivo y cancelar el viejo bloque), o (b) cancelar **solo la cita seleccionada** (`reschedule_skip_cancel="1"` + cancelación dirigida a `selectedAppt.ID`) preservando las hermanas. Derivar `total_procedures`/`procedures_json` del bloque real, no hardcodear `"1"`.

---

### 3.2 — MEDIUM

#### Categoría: bug / correctitud de agendamiento

##### N-4 · `slotToDateTimeComponents` marca como 'pm' slots reales de 05:00–06:00 AM
- **Archivo:** `internal/repository/siesa/appointment_repo.go:55`
- **Detalle:** la heurística "horas 1–6 → pm" escribe `citas.meridiano='pm'` para slots genuinos de 5/6 AM (polisomnografía/EEG matutino, agendas activas verificadas). El cupo se reclama bien (UPDATE empareja por cadena 24h), pero la UI de SIESA y los recordatorios muestran 5 PM en vez de 5 AM; `timecodeFromDateAndTime` también auto-corrompe (h=5 + pm → 17:40).
- **Recomendación concreta:** no inferir meridiano por heurística. Derivarlo directamente del valor 24h ya presente en el slot string: `meridiano = "am" if hInt < 12 else "pm"` (con `hInt` de la hora 24h real), descartando el rango 1–6→pm. Conservar la información 24h inequívoca en lugar de descartarla.

##### N-5 · Detección de contraste/sedación por `strings.Contains` frágil (case/acento)
- **Archivos:** `internal/statemachine/handlers/appointments.go:311`, `internal/notifications/self_reschedule.go:61`, `internal/notifications/confirmation.go:311`
- **Detalle:** `isContrasted`/`isSedated` se derivan de `strings.Contains(appt.Observations, "Contrastada")` / `"Sedaci"` sobre texto libre compartido con la UI de SIESA. Verificado en BD: de 242 citas de contraste, solo ~123 contienen el substring exacto → ~49% mal clasificadas como no-contrastadas. Impacto clínico: `IsContrasted` controla elegibilidad de slots (sin sábados, 7AM–5PM) y `SpacesForCUPS`; `is_sedated` fuerza asunto 17. Una mala detección agenda el estudio en slot sin manejo de contraste y `buildObservations` regenera la observación sin el tag.
- **Recomendación concreta:** comparación robusta case/acento-insensible (`strings.Contains(strings.ToLower(quitarTildes(obs)), "contrast")` / `"sedaci"`). **Mejor aún:** leer contraste/sedación de campos estructurados de la cita (no del texto libre) si existen en SIESA, o persistir un flag propio del bot.

##### N-6 · `applyResonanciaRules`: `inCombo` recorre TODAS las combinaciones, no la coincidente
- **Archivo:** `internal/services/procedure_grouper.go:373`
- **Detalle:** dentro del bloque de combinación coincidente, para decidir `inCombo` se itera todo `resonanciaCombinations` en vez de solo la combinación que satisfizo `allPresent`. El guard `if combinationSpaces >= 0` (L374) es invariante. Caso real alcanzable: orden `883401+883440+883902` → combo1 matchea (spaces=2/3), y `883902` se marca `inCombo` por pertenecer a combo2 (que NO matcheó), excluyendo su slot → sub-reserva de 1 slot → sobre-agendamiento.
- **Recomendación concreta:** filtrar `inCombo` **únicamente** contra la combinación que efectivamente coincidió (guardar referencia a la combo matcheada y comprobar membresía solo en ella), eliminando el bucle sobre todo el slice y el guard invariante.

#### Categoría: seguridad / privacidad (Ley 1581)

##### N-7 · PII de salud (documento, CUPS, entidad) escrita en logs en texto plano (OCR)
- **Archivo:** `internal/services/ocr_service.go:297` y `:264`
- **Detalle:** al fallar el parseo del JSON del LLM se loguea el `content` completo (cédula, CUPS, EPS). `:264` (`slog.Error "openai api error" ... "body"`) vuelca el cuerpo de error de OpenAI a nivel ERROR → además se reenvía a Telegram vía `AlertHandler`. Logs con retención de 30 días en disco. (La cita a `procedure_grouper.go:549` del hallazgo original es atribución incorrecta / código muerto.)
- **Recomendación concreta:** no loguear `content`/`body` crudos; reemplazar por un resumen redactado (longitud, error de parseo, sin PII). Si se requiere el body para diagnóstico, aplicar el mismo `ReplaceAttr` de redacción de N-2 y bajar a nivel Debug detrás de `slog.Enabled`.

##### N-8 · Documento de identidad en logs/eventos sin enmascarar (registro/identificación)
- **Archivos:** `internal/statemachine/handlers/registration.go:868`, `internal/statemachine/handlers/identification.go:116` y `:121`
- **Detalle:** `slog.Error("patient_create_failed", "doc", input.DocumentNumber, ...)` y `WithEvent("patient_not_found", {"doc": doc, ...})`. Los eventos se persisten en `chat_events.event_data` (vía `tracker.LogBatch` → `InsertBatch`) y se vuelcan a slog (`pool.go:1027-1037`), ampliando la superficie de PII. El teléfono sí se enmascara en el mismo log; el documento no.
- **Recomendación concreta:** enmascarar el documento antes de loguear/persistir (p.ej. `MaskDocument` análogo a `MaskPhone`), o registrar solo `doc_length`/hash, tanto en `slog` como en el campo `Data` de los eventos.

##### N-9 · Input crudo del paciente (documentos) persistido en `chat_events`
- **Archivo:** `internal/statemachine/helpers.go:38` (`last_input`) y `:47` (`input`)
- **Detalle:** `ValidateWithRetry`/`ValidateButtonResponse` adjuntan el texto crudo del usuario a eventos. En `ASK_DOCUMENT`, un número de documento rechazado/tipeado se almacena verbatim en `chat_events.event_data` (MySQL local del bot), sin enmascarar, junto al teléfono. (Nota: los valores clínicos —creatinina, peso, semanas— se pasan como string vacío deliberadamente, así que NO se filtran; el alcance real es solo documentos rechazados.)
- **Recomendación concreta:** para estados con PII (documento), pasar input vacío o enmascarado a `ValidateWithRetry`, igual que ya se hace para valores clínicos. Alternativamente, redactar claves `input`/`last_input` en el tracker para estados sensibles.

##### N-10 · `PlaceCall` loguea payload de voz completo y teléfono sin enmascarar
- **Archivo:** `internal/bird/client.go:687` (Debug, payload con nombre/fecha/clínica) y `:721` (Info, teléfono en claro)
- **Detalle:** `:721` (`slog.Info "voice call placed" "to", to`) loguea el teléfono del paciente en claro a nivel Info (default de producción) en cada llamada exitosa; resto del archivo usa `MaskPhone`. `:687` (Debug) vuelca el payload con `patient_name`/fecha/clínica (solo si `LOG_LEVEL=debug`).
- **Recomendación concreta:** usar `utils.MaskPhone(to)` en `:721`; en `:687` redactar/omitir el payload o gatear con `slog.Enabled` y enmascarar los campos PII.

##### N-11 · SIESA con cifrado deshabilitado (`encrypt=disable`) — datos de salud en texto plano
- **Archivo:** `internal/database/mysql.go:53`
- **Detalle:** el DSN de SQL Server fija `query.Set("encrypt", "disable")`, todo el tráfico TDS (PII clínica) viaja sin cifrar. El comentario adyacente menciona `TrustServerCertificate=true` (que mantiene cifrado) pero el código lo desactiva. Servidor de pruebas remoto en LAN (192.168.1.207) → escenario cross-host plausible.
- **Recomendación concreta:** usar `encrypt=true` + `TrustServerCertificate=true` (o certificado válido) y hacerlo **configurable por entorno** (variable, p.ej. `EXTERNAL_DB_ENCRYPT`) para no exigir validación de CA en dev pero cifrar siempre el canal.

##### N-12 · Voice webhooks — auth bypass (PHI mutation)
- **Duplicado de N-1** (consolidado). Ver N-1.

##### N-13 · `HandleConversation` procesa eventos sin verificar firma cuando el secret está vacío
- **Archivo:** `internal/api/webhook_handler.go:326`
- **Detalle:** si `BirdWebhookSecretConversations` está vacío (default inseguro, `.env.example:36`, no exigido por `config.validate()`), se salta toda la verificación y se mutan caches keyed por teléfono atacante-suministrado: `CacheConversationID`/`UpdateConversationID` (envenenamiento) e `InvalidateCachedConversationID`. El envenenamiento puede redirigir `MarkConversationEscalated`/`sendToConversation` a un conversationID arbitrario (mis-delivery de PHI).
- **Recomendación concreta:** verificar la firma **siempre que haya header de firma** (como el handler de voz tras el fix N-1) o hacer obligatorio `BIRD_WEBHOOK_SECRET_CONVERSATIONS` en `config.validate()`.

##### N-14 · `RateLimiter` keyea por `r.RemoteAddr` (incluye puerto efímero) → bypass trivial + inefectivo tras proxy
- **Archivo:** `internal/api/middleware.go:94`
- **Detalle:** `ip := r.RemoteAddr` es `host:port`; cada conexión TCP nueva cae en otro bucket → el límite de 30/min nunca se alcanza abriendo conexiones nuevas. Tras el proxy ngrok (`app.colibrixa.com`, los operadores acceden vía túnel) todos comparten la IP del proxy → un único bucket agregado que puede bloquear operadores legítimos. El acceso real está protegido por la API key (fail-closed), así que esto es defensa-en-profundidad.
- **Recomendación concreta:** extraer el host con `net.SplitHostPort(r.RemoteAddr)` para la clave, y honrar `X-Forwarded-For`/`X-Real-IP` **solo si proviene de un proxy de confianza** configurado. El código ya parsea `X-Forwarded-Host` en `webhook_handler.go:192`, reutilizar ese conocimiento.

#### Categoría: error-handling (fail-open/silencioso)

##### N-15 · Paciente recibe "cita confirmada" aunque `ConfirmBlock` falle (error tragado)
- **Archivo:** `internal/notifications/confirmation.go:38`
- **Detalle:** en la rama "confirm" el error de `ConfirmBlock` solo se loguea (sin `return`); el flujo continúa y envía "Tu cita ha sido confirmada!", cierra conversación y sesión, y registra `notification_confirmed`. Si el UPDATE a SIESA falló, la cita queda sin confirmar pero el paciente cree que sí, sin reintento (el pending ya fue borrado).
- **Recomendación concreta:** en error de `ConfirmBlock`, hacer `return` con mensaje de disculpa + escalar a agente (no enviar confirmación definitiva ni cerrar la sesión), espejando los early-returns de las líneas 27-30/34.

##### N-16 · IVR confirm/cancel ignoran el retorno de `ConfirmBlock`/`CancelBlock`; la nota al agente afirma éxito siempre
- **Archivo:** `internal/notifications/manager.go:553` (confirm) y `:591` (cancel)
- **Detalle:** se descartan los errores; el pending ya fue `LoadAndDelete`'d. Se escribe nota interna "queda confirmada/fue cancelada en el sistema" y `UpdateCallResult("confirmed"/"cancelled")` sin importar el resultado de la BD. En la ruta de cancelación, un slot que el paciente cree liberado queda ocupado en SIESA sin reintento. Todos los demás callers de estos métodos sí verifican el error.
- **Recomendación concreta:** capturar el error de ambos métodos; en fallo, emitir una nota de error al agente (no la de éxito) y registrar el resultado real (p.ej. `error`/`failed`) en `UpdateCallResult` en lugar de `confirmed`/`cancelled`.

#### Categoría: concurrencia

##### N-17 · Data race sobre campos de `*PendingNotification` (sync.Map protege el slot, no el pointee)
- **Archivo:** `internal/notifications/manager.go:442`
- **Detalle:** `pending` es un `sync.Map` de punteros; `sync.Map` no sincroniza los campos del struct apuntado. `MarkIVRSent` (`p.RetryCount=3`, `p.Timer=`), `RegisterCallID` (`p.CallID=`) usan `Load` simple (no `LoadAndDelete`) y mutan un puntero que otras goroutines aún sostienen; `handleConfirmationTimeout` (timer goroutine) y los webhooks (lectura de `p.ConversationID`) corren en goroutines distintas. Race detectable con `-race`: corrompe `RetryCount` (mis-ruteo de escalación) o lee `Timer` parcialmente.
- **Recomendación concreta:** añadir un `sync.Mutex` a `PendingNotification` y protegerlo en toda lectura/escritura de campos, o mover el struct a valores inmutables + reemplazo atómico vía `LoadAndDelete`/`Store`. Ejecutar la suite con `-race` para validar.

##### N-18 · Deadlock permanente de un teléfono en la ruta abandon del lock-timeout
- **Archivo:** `internal/session/phone_mutex.go:60`
- **Detalle:** al expirar el timeout, la goroutine interna sigue bloqueada en `pl.mu.Lock()`; tras 60s la goroutine de limpieza abandona **sin** `Unlock`. La interna eventualmente adquiere `mu` y nunca lo libera → ese `phoneLock` queda con `mu` retenido para siempre. Como `refCount` ya es 0, `StartCleanup` borra el entry del mapa; el siguiente `Lock()` crea un `phoneLock` FRESCO → dos mensajes del MISMO teléfono corren concurrentes, rompiendo la serialización (riesgo de doble reserva en la BD compartida). Requiere retención del lock >~90s.
- **Recomendación concreta:** reemplazar el `sync.Mutex` por un primitivo cancelable (canal/semáforo buffer=1) que permita adquisición con `context`/timeout sin dejar goroutines huérfanas, y nunca borrar del mapa un `phoneLock` cuyo `mu` pueda seguir retenido. Si se mantiene el mutex, garantizar que la goroutine huérfana haga `Unlock` inmediato al observar que el waiter desistió.

##### N-19 · TOCTOU entre `checkExpired`/`handleTimeout` y re-arme del timer
- **Archivo:** `internal/notifications/manager.go:768`
- **Detalle:** `checkExpired` hace `Load` para decidir llamar `handleTimeout`, pero el `Load` y el `LoadAndDelete` interno no son atómicos. Entre ambos, `MarkIVRSent`/`handleConfirmationTimeout` pueden re-`Store` una entrada fresca no expirada; `handleTimeout` la procesa como expirada, avanzando la cadena de escalación prematuramente (~30 min antes). `PendingNotification` no tiene `ExpiresAt` para re-validar.
- **Recomendación concreta:** añadir `ExpiresAt` a `PendingNotification` y re-validar `time.Now() < p.ExpiresAt` dentro de `handleTimeout` antes de actuar (descartar si fue re-armada), o hacer la reclamación atómica.

#### Categoría: resource

##### N-20 · Operaciones de BD en handlers de notificación con `context.Background()` sin timeout
- **Archivo:** `internal/notifications/confirmation.go:22` (y `:227`, `:286`, `:404`)
- **Detalle:** `handleConfirmation`, `escalateToAgent`, `startConfirmRescheduleSession`, `startConfirmCancelSession` usan `ctx := context.Background()` sin deadline para llamadas a SIESA (`FindBlockByAppointmentID`, `GetPatientAppointmentsForDate`, `ConfirmBlock`/`CancelBatch`). `manager.go` sí envuelve en `WithTimeout(30s)` en 11 sitios. Sin DSN statement-timeout, un lock contendido en `citas`/`programacion_medico_detalle` (compartidas con la UI de SIESA) puede colgar la goroutine indefinidamente.
- **Recomendación concreta:** envolver cada `ctx` de estos handlers en `context.WithTimeout(context.Background(), 30*time.Second)` con `defer cancel()`, espejando `manager.go`. Considerar un command timeout a nivel DSN como backstop.

---

### 3.3 — LOW

> Defectos reales de bajo impacto (higiene, robustez, observabilidad). No tocan integridad de datos clínicos críticos ni exponen PHI de forma directa, salvo donde se indique. Agrupados por categoría.

#### bug / correctitud

| ID | Archivo:línea | Resumen | Recomendación |
|----|---------------|---------|---------------|
| N-21 | `services/appointment_service.go:95` | `FindConsecutiveBlock` infiere el gap del primer par y agrupa por igualdad exacta de minutos → puede unir/partir bloques mal (sobre-agrupar y confirmar/cancelar citas no relacionadas). | Usar el intervalo real de la agenda + tolerancia, y validar contiguidad/`EsCitaMultiple`/`CodGrupo` antes de confirmar/cancelar el bloque. |
| N-22 | `statemachine/handlers/slots.go:973` | Cita creada con `UnitValue=0` cuando falla la tarifa SOAT (consulta persiste `Valor=0` en CPA); solo se mitiga con nota "TARIFA PENDIENTE". | Evitar persistir 0; escalar/abortar la creación de consultas sin tarifa resuelta, o marcar con un valor centinela y bloquear facturación. |
| N-23 | `statemachine/handlers/appointments.go:360` | Reschedule inyecta `patient_age=0` (clobber del valor real), filtrando slots de médicos con restricción de edad. | Preservar el `patient_age` real de la sesión a través del reschedule en vez de sobreescribir con "0". |
| N-24 | `local/waiting_list_repo.go:266` | `DateTo+" 23:59:59"` sin validar formato → datetime inválido coercionado a NULL/0 silenciosamente. | Validar/parsear `DateTo` (formato fecha) antes de concatenar; rechazar valores con hora ya incluida. |
| N-25 | `entity_management.go:243` | `askEntityNumberHandler` avanza con `selected_entity_code` vacío en la ruta de error de query, creando paciente/cita sin entidad/contrato. | Añadir `else` que escale a agente cuando `entityCode==""`, en vez de continuar a `StateAskDocumentType`. |

#### error-handling / observabilidad

| ID | Archivo:línea | Resumen | Recomendación |
|----|---------------|---------|---------------|
| N-26 | `worker/pool.go:656` | `handleAgentResume`(no-data)/`Reset`/`Order`/`Cups` evalúan `if err==nil && result!=nil` y descartan el error sin log ni notificación → paciente colgado sin traza. | Loguear el error y notificar (mensaje genérico) como hace la rama con-data (`pool.go:637-640`) y `processMessage`. |
| N-27 | `services/appointment_service.go:386` | `cancelCreated` descarta el error de `CancelBatch` (`_ =`); la ruta hermana sí loguea → citas en 'P' huérfanas sin traza si la compensación falla. | Loguear el error de la compensación (espejando la ruta de cita única). |
| N-28 | `identification.go:256` | Errores de `UpdateContract`/`UpdateMunicipality`/`UpdateEntity` tragados con `_ =` → divergencia momentánea `sis_paci` vs sesión (auto-corregible). | Loguear (`slog.Error`) el fallo en vez de descartarlo. |
| N-29 | `local/event_repo.go:274` | KPIs/health metrics descartan errores de `Scan`/`PingContext` → reportan 0/healthy aunque la BD falle (dashboard interno). | Propagar/loguear el error; reflejar estado real en el endpoint de health. |
| N-30 | `scheduler/tasks.go:405` | `MonthFilter` MRC retorna `(true, nil)` ante error (fail-open) sin log → mes a tope puede ofrecerse. | Mantener fail-open si se desea, pero **loguear** el error (igual que el gate `CheckMRCLimit` de 5b). |
| N-31 | `scheduler/tasks.go:385` | `checkWaitingList` descarta errores con `continue` sin log (`GetWaitingByCups`, `HasFutureForCup`) → outage parcial invisible. | Añadir `slog.Warn/Error` separando error de resultado vacío en cada rama (L385/L423/L435). |

#### concurrencia

| ID | Archivo:línea | Resumen | Recomendación |
|----|---------------|---------|---------------|
| N-32 | `statemachine/handlers/appointments.go:554` (y `:690`, `:884`) | `go onCancel(ctx, code)` usa el ctx del handler; en la ruta de overflow (`WithTimeout 2m` + `defer cancel`) la goroutine puede correr con ctx cancelado, abortando la notificación a lista de espera. Existe fallback diario (no es pérdida total). | Lanzar la goroutine con `context.WithoutCancel(ctx)` o `context.Background()` con su propio timeout. |
| N-33 | `notifications/waiting_list_check.go:111` | `MarkNotified` es UPDATE incondicional sin `AND status='waiting'`; SELECT y UPDATE separados → dos cancelaciones del mismo CUPS notifican 2 veces al mismo paciente. El comentario "atomic" es falso. | `MarkNotified` condicional (`WHERE id=? AND status='waiting'`) y enviar el template solo si `RowsAffected==1` (claim-then-send). |
| N-34 | `siesa/appointment_repo.go:782` (y `:822`) | `ConfirmBatch`/`CancelBatch` lanzan una goroutine de auditoría por cada id (`go func` en `writeAuditLog`) → fan-out no acotado (~65 en cancelación masiva) compartiendo pool de 10. Acotado por el propio pool. | Un único `INSERT log_citas ... SELECT WHERE id IN (...)` (patrón batch ya usado en el UPDATE), o un semáforo que limite el fan-out. |
| N-35 | `worker/pool.go:197` | Drain de apagado usa `processMessage` directo (no `safeProcess`) → un panic durante el drain crashea el proceso sin recover (mensajes se replayan vía WAL al reiniciar). | Usar `p.safeProcess` en el drain y/o añadir `recover` en la goroutine de `Stop` de `main.go`. |
| N-36 | `worker/pool.go:240` (y `:1117`) | `wg.Add(1)` desde productores (overflow/`EnqueueVirtual`) concurrente con `wg.Wait()` en `Stop()` → posible panic "WaitGroup reused" o missed-wait durante apagado. | Aquietar productores antes de `Wait` (cerrar `Enqueue` en `Stop` con flag/mutex), o usar contador atómico + canal en vez del WaitGroup para overflow. |
| N-37 | `scheduler/scheduler.go:178` (y `:117`) | Tareas como fire-and-forget sin `WaitGroup` ni drain; `Start()` retorna en `ctx.Done()` sin esperar goroutines en vuelo. Idempotencia/catch-up mitiga (re-run al reiniciar). | Añadir `sync.WaitGroup` para join en apagado, o documentar la idempotencia como garantía suficiente. |
| N-38 | `scheduler/tasks.go:227` (y `:317`, `:510`) | `time.Sleep` de rate-limit no consulta `ctx` → tras shutdown sigue enviando WhatsApp/IVR hasta ~20s. | `sleepWithContext(ctx, ...)` y `ctx.Err()` al inicio de cada iteración del bucle de destinatarios. |
| N-39 | `scheduler/scheduler.go:178` | Tareas corren con el ctx de app sin timeout por-tarea; un read SIESA bloqueado puede colgar 1 conexión sin deadline (Bird ya está acotado a 30s). | Envolver `t.Fn` en `context.WithTimeout` por ejecución y/o fijar command timeout en el DSN de SIESA. |
| N-40 | `session/phone_mutex.go:42` | Cada `Lock()` lanza una goroutine incluso en el happy path; bajo retención >~90s la rama abandon deja goroutines huérfanas (caso límite estrecho — es el mismo defecto raíz que N-18). | Primitivo de lock con adquisición timeout-aware (canal/semáforo) que no spawnee goroutine por adquisición. Resuelto junto con N-18. |
| N-41 | `bird/client.go:1024` | `AssignFeedItem` reintenta con `time.Sleep` (hasta ~6s) sin context → no cancelable en escalación/apagado. | Propagar un `context` por la cadena `EscalateToAgent→AssignFeedItem` y usar `sleepWithContext`. |

#### resource / infra

| ID | Archivo:línea | Resumen | Recomendación |
|----|---------------|---------|---------------|
| N-42 | `siesa/appointment_repo.go:752` | `Cancel` libera el cupo fuera de la transacción del UPDATE de `citas`; si la liberación falla solo loguea Warn y retorna nil → cupo con `IdCita` apuntando a cita cancelada, oculto para futuros pacientes (documentado como INTEG-04). | Envolver ambos UPDATEs en una transacción (como `Create`) o, mínimo, propagar `relErr`. |
| N-43 | `statemachine/handlers/appointments.go:1012` (y `:1071`) | `buildAppointmentDetail`/`List` usan `context.Background()` para `FindByCode` en bucle → ignoran timeout/cancelación de la petición (BD local del bot, no SIESA). | Propagar el `ctx` de la petición a las funciones helper y a `FindByCode`. |
| N-44 | `local/event_repo.go:185` | KPIs con `WHERE DATE(created_at)=?` + subconsulta correlacionada → full scan del índice (BD local del bot, endpoint admin). | Usar rango half-open `created_at >= ? AND created_at < ? + 1 día` para aprovechar el índice. |
| N-45 | `api/internal_handler.go:255` (y `:410`, `:699`) | Goroutines de notificación admin con `context.Background()` + `Sleep(2s)`/destinatario, sin cancelación en apagado (tras `InternalAuth`+rate-limit). | Usar el ctx de app cancelable y/o un límite global; drenar en apagado. |
| N-46 | `notifications/self_reschedule.go:22` (y `waiting_list.go:16/128`) | `context.Background()` sin timeout para writes a la BD local del bot (no SIESA). | Envolver en `WithTimeout` espejando `RegisterPending`. |
| N-47 | `cmd/server/main.go:527` | `/health` público (sin auth/rate-limit) hace `Ping` a SIESA en cada hit; `RequestLogger` lo short-circuitea (invisible). Acotado por pool de 10. | Cachear el resultado del health-check unos segundos, o usar un endpoint liviano sin Ping a SIESA en cada hit; opcionalmente rate-limit. |
| N-48 | `database/mysql.go:72` | `db.Ping()` sin `PingContext`/timeout en startup y health; DSN sin `connection timeout`. | Usar `PingContext` con `WithTimeout(1-2s)` en health checks y fijar `connection timeout` en el DSN. |
| N-49 | `telegram/client.go:62` | Token de bot Telegram embebido en la URL; un `*url.Error` de transporte propaga la URL (con token) a `slog.Error` (`capacity.go:196/225`). | Redactar/eliminar la URL (y por tanto el token) del error antes de loguear. |

---

## 4. Tabla priorizada (orden de remediación recomendado)

| # | ID | Sev | Cat | Archivo:línea | Esfuerzo | Conocido |
|---|----|-----|-----|---------------|----------|:--------:|
| 1 | N-1 | High | seguridad | `api/webhook_handler.go:417,542` | Bajo | No |
| 2 | N-2 | High | seguridad | `telegram/alert_handler.go:177` | Medio | No |
| 3 | K-1 | High | bug | `siesa/appointment_repo.go:472` + `services/appointment_service.go:390` | Alto | **Sí** |
| 4 | N-3 | High | bug | `statemachine/handlers/appointments.go:357` | Medio | No |
| 5 | N-5 | Med | bug | `appointments.go:311` + `self_reschedule.go:61` + `confirmation.go:311` | Bajo | No |
| 6 | N-15 | Med | error-handling | `notifications/confirmation.go:38` | Bajo | No |
| 7 | N-16 | Med | error-handling | `notifications/manager.go:553,591` | Bajo | No |
| 8 | N-13 | Med | seguridad | `api/webhook_handler.go:326` | Bajo | No |
| 9 | N-4 | Med | bug | `siesa/appointment_repo.go:55` | Bajo | No |
| 10 | N-6 | Med | bug | `services/procedure_grouper.go:373` | Bajo | No |
| 11 | N-7 | Med | seguridad | `services/ocr_service.go:264,297` | Bajo | No |
| 12 | N-8 | Med | seguridad | `registration.go:868` + `identification.go:116,121` | Bajo | No |
| 13 | N-11 | Med | seguridad | `database/mysql.go:53` | Bajo | No |
| 14 | N-14 | Med | seguridad | `api/middleware.go:94` | Bajo | No |
| 15 | N-17 | Med | concurrencia | `notifications/manager.go:442` | Medio | No |
| 16 | N-18 | Med | concurrencia | `session/phone_mutex.go:60` | Medio | No |
| 17 | N-20 | Med | resource | `notifications/confirmation.go:22,227,286,404` | Bajo | No |
| 18 | N-19 | Med | concurrencia | `notifications/manager.go:768` | Medio | No |
| 19 | N-9 | Med | seguridad | `statemachine/helpers.go:38,47` | Bajo | No |
| 20 | N-10 | Med | seguridad | `bird/client.go:687,721` | Bajo | No |
| 21 | K-2 | Med | bug | `notifications/waiting_list.go:49` | Medio | **Sí** |
| 22+ | N-21…N-49 | Low | varios | (ver §3.3) | Bajo | No |

---

*Informe generado a partir de hallazgos confirmados adversarialmente. Las líneas referenciadas corresponden al snapshot del repositorio en la rama `main` al momento de la auditoría.*
