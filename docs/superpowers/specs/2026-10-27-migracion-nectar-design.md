# Migración de Neurobotv2 de Bird a Néctar — diseño

**Fecha:** 2026-10-27 · **Rama:** `feat/migracion-nectar` · **Dueño:** Sneider Fuentes ·
**Chats que participan:** este repo (orquesta y codifica), `nectar-cd` (lado Néctar),
sesión hdg-bot (responde sobre el puente que ya corre; presta su túnel).

> Todo lo que aquí se afirma del código de Neurobotv2, de hdg-bot y de Néctar se midió contra
> los tres árboles el 2026-10-27. Lo que depende de una decisión del dueño va marcado **[D]**; lo
> que es criterio mío va marcado **[mi criterio]**; lo que todavía no está fijado por Néctar va
> marcado **[pendiente Néctar]**.

---

## 1. Intención y por qué

Neurobotv2 (bot de agendamiento de Neuro Electrodiagnóstico del Llano) hoy habla con WhatsApp a
través de **Bird** (API v2): recibe por webhooks de Bird, envía por la Channels/Conversations API,
escala a agentes humanos asignando feed items del Inbox de Bird, y los agentes devuelven el
control escribiendo comandos `/bot` que el bot lee por el eco de salientes.

**Néctar** es la plataforma propia de Colibrixa sobre la Cloud API de WhatsApp de Meta: bandeja
multiagente, multi-entidad, con un contrato para bots ya construido y en producción con hdg-bot
(Hospital de la Gente). La intención es que **Neurobotv2 deje de depender de Bird para toda la
mensajería y pase a Néctar**, en un workspace propio, con el mismo número de pruebas que hoy
está en Bird y que ya se está registrando directo en Meta.

Por qué: Bird cobra cada envío (fallido o no), es un intermediario que no controlamos, y
Néctar ya resuelve bandeja, escalado, retorno al bot y cola por horario para la otra entidad.
Sacar a Bird unifica los dos bots sobre la misma plataforma.

---

## 2. Alcance

### Objetivos
1. Toda la mensajería de WhatsApp de Neuro pasa por Néctar: recepción, envío (texto, botones,
   listas), adjuntos para el OCR, escalado a agente, retorno al bot, cierre.
2. **Las salidas proactivas también salen por Néctar** **[D1]**: recordatorio diario de citas,
   recordatorio de corta antelación, lista de espera, cancelación y reprogramación administrativa.
   «No más Bird» para WhatsApp.
3. La lógica clínica, SIESA, OCR, lista de espera, scheduler, horario y observabilidad del bot
   **no cambian**.
4. Bird y Néctar conviven en el mismo binario: con `NECTAR_BASE_URL` vacía el bot se comporta
   exactamente como hoy (es como lo hizo hdg-bot y permite probar canal por canal y volver atrás).

### No objetivos
- Reemplazar el IVR por voz. **Bird Voice se conserva** para la llamada de las 13:00 y sus dos
  webhooks **[D]**. Bird pasa de plataforma a proveedor de telefonía.
- Migrar historial de conversaciones de Bird. No hay nada almacenado en Bird que el bot necesite.
- Mover a Néctar la lógica de negocio del bot (reglas clínicas, SIESA, lista de espera).
- Leer el horario y los festivos desde Néctar. hdg-bot ya tiene el lector; se porta **apagado** y
  se enciende en una fase posterior si el dueño lo quiere.
- Unificar en el dashboard las métricas por agente con las de Néctar (ver §11; es deuda
  obligatoria pero posterior a esta migración).

### Anti objetivos (lo que haría fallar esto aunque «funcione»)
- Romper a hdg-bot con un cambio de contrato de Néctar pedido por Neuro. **Regla del dueño:**
  todo lo que Neuro pida a Néctar es aditivo; si no hay otro camino, se avisa a hdg-bot antes
  del despliegue para que se ajuste. «Que no se nos pase esto nunca.»
- Perder mensajes de pacientes en el corte (el WAL y la deduplicación por `event_id` existen para eso).
- Gastar envíos a ciegas: un POST que no puede entregar no se hace (principio que ya rige con Bird).
- Guardar datos personales nuevos en claro en el WAL por culpa de un campo nuevo del contrato.

---

## 3. Restricciones

- **Go 1.26**, sin Go en el host: todo se compila y prueba en contenedores (`make check`,
  `./scripts/dev.sh`). Lint y formato del pre-commit (lefthook) se respetan.
- Identificadores de contacto de 63 caracteres ya existen en toda la tubería local
  (migración 040). La clave de sesión `nectar:<conversation_id>` cabe. El puente en sí no
  necesita migraciones (hdg-bot no necesitó ninguna); la única migración de esta migración es la
  columna de contacto del §7.2 punto 5, aditiva y nula por defecto.
- Un proceso-bot sirve **una** entidad de Néctar con **su** clave de servicio (RLS por workspace).
  Neuro tendrá su cuenta de Meta, su clave de servicio y su secreto saliente propios; el secreto
  de firma de peticiones del bot hacia Néctar es uno por instalación y se comparte con hdg-bot.
- Néctar reintenta los avisos **indefinidamente** hasta recibir 2xx: el endpoint del bot tiene que
  estar alcanzable mientras haya tráfico, y la deduplicación por `event_id` es obligatoria.
- `404` en cualquier ruta `/api/bot/*` significa «la conversación ya no es tuya» y nunca se
  reintenta. `409 window_closed` significa «solo plantilla».
- Pruebas contra la **Néctar de producción** (`https://nectar.colibrixa.com`) en el workspace
  Neuro, aislado por su clave de servicio **[D4]**. No existe una Néctar de pruebas alcanzable.
  Nunca usar la clave de servicio global de la instalación.

---

## 4. Arquitectura resultante

```
                 ┌──────────────── Néctar (nectar.colibrixa.com) ────────────────┐
                 │  WhatsApp Cloud API (Meta)  ·  bandeja de asesores  ·  cola   │
                 └───────┬─────────────────────────────────────▲─────────────────┘
      avisos firmados    │ POST /api/webhooks/nectar            │ /api/bot/* firmados
      (4 kinds + event_id)│                                      │ (8 rutas + apertura D1)
                 ┌───────▼─────────────────────────────────────┴─────────────────┐
                 │  Neurobotv2                                                   │
                 │  webhook Néctar → WAL (message_inbox, msg_type nectar_notice) │
                 │  → worker pool → máquina de estados (sin cambios de flujo)    │
                 │  bird.Client con NectarBridge: sesión nectar:* → Néctar       │
                 │  scheduler → recordatorios / lista de espera → apertura D1    │
                 │  escalación → sobre v1 (reason, summary+facts, input, team)   │
                 │  retorno (conversacion_devuelta) → AgentCommand resume/close  │
                 └───────┬───────────────────────────────────────────────────────┘
                         │ Bird Voice (PlaceCall, /voice, /voice/dtmf)  ← se queda
                 ┌───────▼────────┐
                 │  Bird (voz)    │
                 └────────────────┘
```

**Principio de convivencia (heredado de hdg-bot):** `bird.Client` sigue siendo la interfaz que
usan handlers, worker, notificaciones y scheduler. Se le inyecta un `NectarBridge`; cada método
de salida mira si la sesión es de Néctar (prefijo `nectar:`) y desvía. Lo que no es `nectar:*`
sigue por Bird. Con el puente apagado nada cambia.

---

## 5. Decisiones del dueño (2026-10-27)

| # | Decisión | Consecuencia |
|---|---|---|
| D1 | Recordatorios, lista de espera y plantillas administrativas **salen por Néctar**. Bird solo para IVR. | Néctar construye una ruta nueva de **apertura iniciada por el bot** (§6.2). Hasta que exista, esas salidas no se pueden probar contra Néctar. |
| D2 | El bot necesita el **contacto** (E.164 o BSUID) de una conversación. | Néctar lo expone en `GET …/state` como bloque opcional (§6.2). Elegido sobre `mensaje_entrante` para no meter PII en el WAL de ninguno de los dos bots (petición explícita de hdg-bot). |
| D3 | Néctar no expone estados de entrega. | Se **retira** la supresión de plantillas por `delivery_failed` y todo lo que la alimentaba. No se pide nada a Néctar. Base verificada el 2026-10-27 en la página oficial de precios de Meta (developers.facebook.com/docs/whatsapp/pricing): «Businesses are only charged when a message is delivered. Meta does not charge for messages sent»; el motivo económico de la supresión (Bird cobraba el intento) desaparece. La señal operativa «a este paciente hay que llamarlo» queda del lado de Néctar (estado `failed` visible al asesor) y se incorpora al pedido D5. Nota de costo nueva: desde el 2026-10-01 Meta cobra los mensajes de servicio (texto libre dentro de la ventana de 24 h) a partir del 1.001 por número y mes, y los de utilidad dentro de la ventana; el principio «un mensaje por paso» sigue vigente con Néctar. |
| D4 | Pruebas contra Néctar de producción en workspace aislado. | Alta del workspace Neuro real; sin clientes reales aún. |
| D5 | Métricas por agente del dashboard viven en Néctar **por ahora**. | El dashboard deja de mostrar lo de Bird. Una ruta de lectura de agentes/atención por conversación es **deuda obligatoria** posterior (§11). |
| — | IVR por voz se conserva en Bird. | Se mantienen `BIRD_API_KEY_VOICE`, canal, número, flow y los dos webhooks de voz. |
| — | Cola de Néctar para escalaciones fuera de horario o sin agentes; no-show y recordatorios al agente del bot apagados para Néctar; vencimiento de 8 h hábiles tal cual. | §7.2 punto 1b. |
| — | Plantillas que disparan flujos del bot solo las envía el bot: marca `audience = bot` en el catálogo de Néctar (aditivo). El bot responde `agent` a toda apertura hecha por un agente. | §6.2 y §8. |
| — | Las 4 plantillas se crean en la WABA nueva; los nombres de Meta los fija el dueño. | El bot mapea `BIRD_TEMPLATE_*_PROJECT_ID` → nombre de Meta en `NECTAR_TEMPLATE_NAMES` (§8). |
| — | Los secretos los genera y teclea el dueño. | §9 trae comandos y dónde va cada uno. |
| — | Regla de compatibilidad con hdg-bot (anti objetivos). | Cada pedido a Néctar se declara aditivo; hdg-bot está avisado de los tres pedidos. |

---

## 6. Contrato con Néctar

### 6.1 Lo que ya existe y Neuro adopta tal cual
Fuente: `C:\laragon\www\CHATBOT\docs\PARA-EL-EQUIPO-DEL-BOT.md` y
`nectar/docs/AUDITORIA-ACOPLAMIENTO-BOT.md §8`. Esquemas JSON en `nectar/internal/protocol/schema/`.

**Rutas que el bot llama** (cabecera `X-Bot-Service-Key`, firma `X-Nectar-Signature` +
`X-Nectar-Request-Timestamp` = `base64(HMAC-SHA256(secret, ts + "\n" + URL + "\n" + sha256(body)))`):
`GET …/{id}/state` · `GET …/{id}/availability` · `GET /api/bot/attachments/{id}` ·
`GET /api/bot/schedule` · `POST /api/bot/conversations/resolve` (identificador → conversación;
**solo busca, nunca crea**) · `POST …/{id}/messages` (text | buttons | list | template, con
`idempotency_key`) · `POST …/{id}/close` · `POST …/{id}/escalate` (reason, reason_detail,
summary, facts, input, suggested_team_id, resolve_lock).

**Avisos que el bot recibe** en `POST /api/webhooks/nectar` (firma `X-Nectar-*` con el secreto
saliente de la cuenta; sobre mínimo `event_id`, `kind`, `conversation_id`, `occurred_at`;
deserialización tolerante obligatoria): `mensaje_entrante` (texto, `interactive_reply_id` de
botón/lista, adjuntos con `attachment.url`), `conversacion_resuelta`, `conversacion_devuelta`
(`return_value` / `retorno{opcion, valores}`), `conversacion_abierta_por_agente` (respuesta
síncrona `{"respuesta_va_a":"bot"|"agent"}`).

**Capacidades:** Néctar pide `GET /api/nectar/capabilities` al bot cada 5 min; declarando
`return.valores: true` Néctar emite protocolo v1 para la entidad.

### 6.2 Lo que Neuro pide a Néctar (aditivo) — forma refutada por `nectar-cd` el 2026-10-27

Pendiente solo de que el dueño lo confirme en el chat de Néctar; Néctar lo construye en un plan
propio de 3-4 tareas con su ventana, **después de la 44a, sin fecha**. La pantalla de alta y las
pruebas de entrada/escalado/retorno no dependen de esto y van antes.

1. **Apertura iniciada por el bot con plantilla (D1).** `POST /api/bot/conversations/open`,
   misma clave de cuenta y firma que las otras ocho rutas. Cuerpo:
   `{idempotency_key (UUID, obligatorio), channel_id (obligatorio, de la cuenta de la clave),
   recipient: {phone} | {bsuid} (exactamente una; phone se normaliza a E.164 sin «+»),
   template: {name, language, parameters: {"<marcador>": "<valor>"}} (igual que
   `POST {id}/messages`: plantilla por nombre+idioma dentro del catálogo de la cuenta y
   parámetros como OBJETO marcador → valor, la misma forma que `nectar.TemplateBody.Parameters
   map[string]string` del puente; correcciones de `nectar-cd` del 2026-10-27, Plan 44 de Néctar),
   name (opcional; solo se escribe en un contacto sin nombre)}`.
   Respuestas: `201 {conversation_id, channel_id, message_id}`; `400 reason`
   `idempotency_key_invalid | phone_invalid | recipient_required | recipient_ambiguous |
   template_parameters` (con `expected/received/placeholders`); `404` sin cuerpo si el canal no
   existe o no es de la cuenta; `409 reason channel_inactive | template_other_account |
   template_not_offerable | already_open {conversation_id, mine: true|false}`;
   `502 reason meta_rejected {meta_code}`.
   **Semántica que el bot debe respetar:** si el contacto ya tiene una conversación viva en ese
   canal, Néctar **no** manda la plantilla: responde `409 already_open` ANTES de gastar; si
   `mine` es `true`, el bot la manda él mismo por `POST {id}/messages` con cuerpo `template`
   (funciona con la ventana cerrada); si `mine` es `false`, la conversación es de un agente y
   el bot no la toca. Reintento con la misma `idempotency_key` → `409 already_open` con el id
   que creó (idempotente sin segundo gasto). La conversación nace en estado `bot`, sin dueño,
   sin cuarto aviso; la respuesta del paciente llega como `mensaje_entrante` normal con
   `interactive_reply_id` si pulsó un botón. Las plantillas se identifican por nombre+idioma
   dentro del catálogo de la cuenta, como en `messages` (ver §8).
   **Audiencia de plantillas (aditivo, mismo plan):** el catálogo gana una columna anulable
   `audience` (`bot` | `agent` | `both`, NULL = both) con override del superadmin; el filtro
   entra en lo que ven «Nueva conversación» y el envío desde el hilo, y en la apertura del
   agente; las rutas del bot no filtran. Las cuatro plantillas de Neuro se marcan `bot`; las de
   hdg-bot quedan `both` sin tocar nada. Elegido frente a duplicar plantillas (dos aprobaciones
   de Meta, dos nombres, dos cobros por el mismo texto).
2. **Contacto de la conversación (D2).** Bloque opcional en `GET …/state`:
   `contact: {identifier: E.164 sin «+» o vacío si el número es oculto, bsuid: cadena o vacía,
   profile_name: cadena o vacía}`. Los tres campos de hoy no cambian. **No va en
   `mensaje_entrante`** (petición de hdg-bot, aceptada: el WAL guarda el aviso crudo). Regla de
   datos personales: el bot de Neuro tampoco persiste ese bloque en crudo (va al contexto de
   sesión, que ya contiene teléfono y documento del paciente bajo las mismas reglas de hoy).
3. **Lectura de agentes para el dashboard (D5, posterior, obligatoria).** Néctar ya guarda por
   escalado un `waiting_cycle` (`opened_at, agent_id, first_response_at, closed_at`) y la
   presencia de agentes; lo que falta es una ruta de lectura con ámbito de workspace. Forma
   tentativa: `GET /api/bot/agents` (roster) y `GET /api/bot/escalations?since=` (por escalado:
   `conversation_id, team_id, agent_id, agent_name, escalated_at, first_agent_reply_at,
   returned_at, resolved_at`). Sin fecha; registrada como deuda comprometida.

**Construido el 2026-10-29 (Plan B).** (1) y (2) están en el bot. Se apartó de lo escrito arriba en:
- La plantilla va por **nombre + idioma** (`NECTAR_TEMPLATE_NAMES` traduce `project_id` → nombre en Meta), no por `template_id`.
- Los botones de plantilla se **traducen por título**: Néctar (versión 45b) entrega el toque como `interactive_reply_id` con el
  texto del botón y el bot lo mapea a su payload (`internal/notifications/template_buttons.go`); los títulos en Meta deben coincidir.
- **D5 sigue pendiente en Néctar** (su dueño lo agenda antes del Plan 43; la lectura usará `since` y luego un cursor opaco).
- `/open` excluye solo `audience='agent'`; el dueño de Néctar marca las cuatro plantillas de Neuro como `bot`.

hdg-bot confirmó por escrito que (1) y (3) no le afectan y que (2) en `state` tampoco.

---

## 7. Qué se porta, qué se adapta, qué se retira

### 7.1 Se porta desde hdg-bot (`main`) casi tal cual
| Pieza | Archivo en hdg-bot |
|---|---|
| Cliente de las rutas `/api/bot/*`, errores tipados (`ErrNotMine`, `ErrWindowClosed`, `ErrIdempotencyReused`) | `internal/nectar/client.go` |
| Firma y verificación `X-Nectar-*` (base64 estricto, ventana de 5 min) | `internal/nectar/signature.go` |
| Sobre de los 4 avisos, parseo tolerante, `SessionKey`, `ToInboundMessage` | `internal/nectar/notice.go` |
| Registro de capacidades calculado del binario | `internal/nectar/capabilities.go`, `internal/api/nectar_capabilities.go` |
| Lector de horario/festivos de Néctar (se porta apagado) | `internal/nectar/schedule.go`, `cmd/server/schedule_nectar.go` |
| Adaptador a tipos de Bird | `internal/nectar/bridge.go` |
| Desvío dentro de `bird.Client` (`NectarBridge`, `SetNectar`, `HasAvailableAgentsFor`, `ConversationState`, `EscalateToAgentWithSummary`) | `internal/bird/nectar_bridge.go` + los puntos de `client.go` que consultan `c.nectar` |
| Webhook `POST /api/webhooks/nectar` con dedupe por `event_id` en `message_inbox` (`msg_type = nectar_notice`) y replay del WAL por `DispatchNectarNotice` | `internal/api/nectar_handler.go` + cambios en `cmd/server/main.go` |
| Menú de retornos y descriptor de entrada por estado | `internal/statemachine/return_menu.go`, `input_descriptor.go` |
| Cola vencida y apertura por plantilla del cuarto aviso | `internal/worker/queue_expired.go`, `template_opener.go` |
| Adjuntos de Néctar en el OCR (`SetNectarCredentials`) | `internal/services/ocr_service.go` (solo ese caso) |
| Stub de Néctar y sonda de avisos para pruebas locales | `cmd/nectar-stub`, `cmd/probe-notice` |
| Config: `NECTAR_*`, `PROTOCOL_ESCALADO_VERSION` | `internal/config/config.go` |

Los tests de cada pieza se portan con ella (≈3.000 líneas). Los volcados reales de
`C:\laragon\www\CHATBOT\docs\volcados` sirven de fixtures de recepción.

### 7.2 Se adapta a Neuro
1. **Escalación (`handlers/escalation.go`).** Hoy: renombra conversación en Bird, asigna feed
   item a equipo/agente, nota interna con `buildAgentSummary` + `buildAgentCommands`. Pasa a:
   sobre v1 (`reason` del vocabulario cerrado de Néctar + `reason_detail` interno, `summary` y
   `facts` salidos de una sola construcción estructurada, `input` con el menú de retorno del
   estado, `suggested_team_id`, `resolve_lock`). El resumen de Neuro (paciente, documento,
   entidad/contrato, CUPS, grupo, estado, motivo) se reconstruye sobre el modelo de
   `summary_groups.go` de hdg-bot. Los comandos de texto `/bot` dejan de ofrecerse en Néctar;
   su información viaja en el `input`. **[mi criterio]** El vocabulario de motivos de Neuro se
   mapea a los motivos cerrados de Néctar en un solo sitio (el registro de capacidades), igual
   que en hdg-bot.
1b. **Escalación fuera de horario o sin agentes: modelo de cola de Néctar** **[D, 2026-10-27]**.
   Hoy Neuro no escala en ese caso (mensaje de horario + menú). Con Néctar el bot llama
   `escalate` igual: el caso queda en cola, el paciente recibe el texto que el bot le escriba
   antes de escalar («tu solicitud quedó registrada…»), y Néctar lo reparte cuando abre. Se
   porta `nextBusinessDayQueue`/`queue_expired.go` de hdg-bot. Reglas de Néctar, medidas:
   reparto cada 20 s; a los 15 min sube prioridad y avisa de desborde; aviso «paciente
   esperando» a los 75 min; con agente asignado que no contesta, aviso privado a los 75 min y
   supervisor 225 min después; sin latido del agente, liberación a la cola a los 8 min. Los
   casos que entraron **fuera de horario** vencen a las 8 horas hábiles (franja de los equipos
   del canal, con festivos de la entidad) y vuelven al bot con `retorno.opcion = cola_vencida`;
   el bot escribe al paciente («no alcanzamos a atenderte… escríbenos en nuestro horario», con
   plantilla si la ventana venció) y cierra. Los que entraron **dentro de horario no vencen**:
   se quedan en la bandeja hasta que un humano actúe. **Consecuencia aceptada por el dueño:**
   para sesiones `nectar:*` se apagan el no-show del agente (devolver al bot a las 5 h) y los
   recordatorios al agente del bot, porque se alimentaban del eco de salientes de Bird y Néctar
   los cubre con sus propios avisos; se conserva solo el cierre de la sesión del bot por
   silencio largo del paciente (que no toca la conversación en Néctar si ya no es del bot).
   Néctar nunca escribe al paciente por sí misma (no tiene remitente de sistema). El plazo de
   8 h hábiles es de la instalación (compartido con hdg-bot) y se deja como está.
2. **Retorno al bot.** `conversacion_devuelta` se traduce a `worker.AgentCommand{resume|close,
   ReturnValue}` y entra por `EnqueueAgentCommand`, el mismo camino que ya consumen
   `processAgentCommand` / `handleAgentResume` (así conviven Bird y Néctar). Cada estado
   escalable de Neuro registra su menú de retorno (`RegisterReturnMenu`) y, si pide un dato,
   su `ReturnInput` con validación; un dato inválido re-escala, nunca vuelve al paciente.
3. **Ruteo a equipos.** Neuro rutea por grupo de CUPS, no por entidad. Se generaliza el resolver
   de `suggested_team_id`: variable nueva `NECTAR_TEAM_BY_GROUP=imagenes:<id>,neuro:<id>,
   callcenter:<id>` **[mi criterio: nombre]**, resuelta con la misma clasificación de CUPS que
   hoy usa `ResolveTeamForCups`. Se conserva el evento de observabilidad `escalacion/equipo_sugerido`.
4. **Salidas proactivas (D1).** Scheduler (07:00, corta antelación, 06:00 lista de espera, 07:05
   re-enganche) y API interna (cancelación/reprogramación de agenda, notificar confirmación):
   cuando el puente está activo, en vez de `SendTemplate` a Bird se llama la apertura de Néctar
   con el identificador del paciente (E.164 de SIESA o el que tenga la lista de espera) y el
   `template_id`; ante `409 already_open` con `mine: true` se manda la plantilla por
   `POST {id}/messages`, con `mine: false` se registra el evento y no se envía (la conversación
   es de un agente); la sesión/pending se registra con la clave `nectar:<conversation_id>` que
   devuelve la apertura o el 409, y la respuesta del paciente llega como `mensaje_entrante` con
   `interactive_reply_id` (`confirm`, `cancelar`, `reprogramar`, `understood`, `reschedule`,
   `wl_schedule`, `wl_decline`), que `NotificationManager.HandleResponse` ya entiende. El
   re-enganche matutino y el aviso de preparación son texto libre: solo se envían con ventana
   abierta (`GET state.window_open`), si está cerrada se omiten sin gastar.
5. **Identidad del paciente (D2).** La sesión es por conversación: `sessions.phone_number`
   pasa a ser `nectar:<conversation_id>`, que solo es estable mientras la conversación vive
   (tras `conversacion_resuelta`, el siguiente mensaje del paciente abre otra). Pero en Neuro
   todo lo que distingue pacientes va por teléfono: sesiones recientes por teléfono, ventana de
   24 h por último mensaje del paciente, lista blanca, eventos por teléfono, la skill de auditoría
   y los KPIs del dashboard. **[mi criterio]** Se añade **una migración aditiva**: columna
   `contact_identifier VARCHAR(63) NULL` en `sessions`, `escalations` y `chat_events` (índice en
   `sessions`), que guarda el E.164 (sin «+») o el BSUID obtenido de `GET state.contact` una vez
   por sesión; el bot lo rellena al crear la sesión `nectar:*` y lo copia a cada evento y
   escalación. Las consultas «por teléfono» (recientes, ventana, eventos, lista blanca,
   deliverability mientras exista) miran `contact_identifier` cuando la fila es `nectar:*` y
   `phone_number` cuando es de Bird. Así el dashboard y la auditoría siguen contando por
   paciente, no por episodio. Es un dato que ya vive en las mismas tablas para Bird, bajo las
   mismas reglas de datos personales; el WAL sigue sin él. Si el contacto es un BSUID (número
   oculto), la lista de espera lo guarda tal cual y la apertura lo usa como `recipient.bsuid`.
   Si `contact` viene vacío o D2 aún no está desplegado, se cae al teléfono registrado en SIESA
   cuando el paciente ya se identificó, se registra un evento de observabilidad y nunca se
   bloquea el flujo.
5b. **Conversaciones que el bot abre y nadie contesta (D1).** **[mi criterio]** El bot es
   responsable de cerrarlas: cuando el pending de una plantilla vence sin respuesta (y tras el
   IVR, si aplicaba) y no hay sesión activa en esa conversación, se llama `POST {id}/close`.
   Con la forma final de D1 un `409 already_open mine:true` no bloquea nada (el bot manda la
   plantilla por `messages`), pero dejar abiertas conversaciones huérfanas confunde la bandeja
   y la ventana de 24 h. La clave del pending es `nectar:<conversation_id>` devuelto por la
   apertura o por el 409; el pending guarda además el E.164 para el IVR de Bird.
6. **Lista blanca de pruebas.** Para sesiones `nectar:*` el filtro de `TESTING_WHITELIST_PHONES`
   se aplica contra el contacto obtenido por D2; mientras D2 no esté desplegado, el aislamiento lo
   da el propio workspace de pruebas de Néctar (como hizo hdg-bot).
7. **Dashboard** (`neuro-dashboard`): ver §11.

### 7.3 Se retira (solo cuando la sesión es de Néctar; con el puente apagado sigue existiendo)
Lookup y caché de conversación por teléfono, `CreateConversationForPhone`, resolución de BSUID
contra Bird, `MarkConversationEscalated`/`AssignFeedItem`/`Unassign`/`CloseFeedItems`/`Tag`,
nota interna de escalación, comandos `/bot` por eco de salientes, pausa por intervención de agente
(`HandleAgentTakeover`), `FetchMessageText`, supresión por `delivery_failed` (D3) y su tabla de
apoyo deja de alimentarse, `/api/internal/agents/bird` para el dashboard (D5).
**[mi criterio]** En esta migración no se borra código de Bird: se deja inerte detrás del
desvío y se retira en un commit mecánico aparte cuando el canal de producción ya esté en Néctar.
Los tres webhooks de Bird de mensajería siguen registrados hasta ese momento.

### 7.4 Se conserva
Bird Voice completo (`PlaceCall`, `SendGather`, `GetGatherResult`, `HangupCall`,
`GetCallDetails`, `ListCalls`, webhooks `/voice` y `/voice/dtmf`, `BIRD_API_KEY_VOICE`,
`BIRD_VOICE_*`). El IVR de las 13:00 sigue llamando por Bird a quienes no respondieron el
recordatorio enviado por Néctar; la correlación es por `appointment_id`/`phone` del pending,
no por canal.

---

## 8. Plantillas

Cuatro plantillas, hoy proyectos de Bird, que el dueño crea y aprueba en la WABA nueva y Néctar
sincroniza al catálogo de la cuenta. El bot las sigue identificando por su variable
`BIRD_TEMPLATE_*_PROJECT_ID` y `NECTAR_TEMPLATE_NAMES` traduce `project_id → nombre de Meta`
(mecanismo ya existente en hdg-bot); el idioma sale de `BIRD_TEMPLATE_*_LOCALE` (`es-CO`).
Las cuatro se marcan `audience = bot` en el catálogo de Néctar (§6.2): ningún agente puede
enviarlas desde la consola, así que el bot responde `agent` a cualquier
`conversacion_abierta_por_agente` (las plantillas que un agente sí puede abrir son suyas y las
atiende él). Parámetros medidos en el código de Neuro (Néctar exige coincidencia exacta con
los marcadores del catálogo; un desajuste es `400 template_parameters`):

| Uso | Parámetros | Botones (payload) |
|---|---|---|
| Recordatorio / confirmación diaria y de corta antelación, y «notificar confirmación» desde el dashboard | `patient_name, clinic_name, appointment_date, appointment_time, procedures` | Confirmar (`confirm`), Reprogramar (`reprogramar`), Cancelar (`cancelar`) |
| Lista de espera (hay cupo) | `patient_name, procedure_name, cups_code, clinic_name` | Agendar (`wl_schedule`), No gracias (`wl_decline`) |
| Cancelación administrativa | `patient_name, appointment_date, appointment_time, reason` | Entendido (`understood`), Reprogramar (`reschedule`) |
| Reprogramación administrativa | `patient_name, appointment_date_cancel, appointment_time_cancel, appointment_date_new, appointment_time_new` | Confirmar (`confirm`), Reprogramar (`reprogramar`), Cancelar (`cancelar`) |

Los botones de respuesta rápida de Meta devuelven un `interactive_reply_id`; el bot necesita
que ese id sea el payload de la columna derecha (o un mapeo título → payload en el bot
**[lo fija el constructor]** según cómo Néctar exponga los ids de botón de plantilla).

---

## 9. Configuración y secretos

### 9.1 En el `.env` de Neurobotv2 (local primero, prod después)
```
NECTAR_BASE_URL=https://nectar.colibrixa.com          # sin barra final; vacío = puente apagado
NECTAR_SERVICE_KEY=<clave de servicio DE LA CUENTA Neuro>   # la genera el dueño (abajo)
NECTAR_NOTICE_SECRET=<secreto SALIENTE de la cuenta Neuro>  # la genera el dueño (abajo)
NECTAR_REQUEST_SECRET=<BOT_WEBHOOK_SECRET_INBOUND de la instalación Néctar>  # ya existe; compartido con hdg
NECTAR_TEMPLATE_NAMES=<project_id_confirm>=<nombre_meta>,<project_id_wl>=<nombre_meta>,...   # nombre de la plantilla en Meta
NECTAR_TEAM_BY_GROUP=imagenes:<id>,neuro:<id>,callcenter:<id>   # ids que nectar-cd lee de producción
NECTAR_CHANNEL_ID=<id del canal de pruebas en Néctar>           # para la apertura D1
PROTOCOL_ESCALADO_VERSION=1
NECTAR_SCHEDULE_ENABLED=false
```
Se conservan `BIRD_API_KEY_VOICE`, `BIRD_VOICE_CHANNEL_ID`, `BIRD_VOICE_NUMBER`,
`BIRD_VOICE_FLOW_ID`, `BIRD_WEBHOOK_SECRET_VOICE`. Las demás `BIRD_*` siguen presentes mientras
el código de Bird exista (config.validate las exige); se relajan en el commit de retirada.

### 9.2 Generar los secretos (los teclea el dueño; nunca viajan por chat)
En Git Bash o PowerShell:
```bash
openssl rand -hex 32     # clave de servicio de la cuenta Neuro  → NECTAR_SERVICE_KEY
openssl rand -hex 32     # secreto saliente de la cuenta Neuro   → NECTAR_NOTICE_SECRET
```
```powershell
-join ((1..32) | ForEach-Object { '{0:x2}' -f (Get-Random -Max 256) })   # equivalente sin openssl
```
`NECTAR_REQUEST_SECRET` no se genera: es el `BOT_WEBHOOK_SECRET_INBOUND` que ya tiene la
instalación de Néctar (el mismo que usa hdg-bot).

### 9.3 En Néctar (superadmin, por pantalla; `nectar/docs/ALTA-NEURO-ELECTRODIAGNOSTICO.md`, escrito por `nectar-cd` sobre `ALTA-DE-UN-WORKSPACE-REAL.md`)
Orden: cuenta de Meta de la entidad (WABA + App propia, token, verify token, webhook
`/webhook/{token}`) → entidad «Neuro Electrodiagnóstico del Llano» → canal con el
`phone_number_id` del número de pruebas → tres equipos (Grupo A imágenes, Grupo B
neurología/fisiatría/sueño, Call Center) con al menos un agente de pruebas → horario del equipo
L-V 07:00-18:00 / S 07:00-12:00 → **Clínicas → Cuentas de Meta → Configurar bot**:
`bot_notice_url = https://<túnel>/api/webhooks/nectar`, clave de servicio y secreto saliente (los
mismos valores del `.env`) → sincronizar plantillas al catálogo de la entidad.
⚠️ Los formularios nacen con la **primera entidad ya elegida** (la de HDG): hay que cambiar el
selector antes de guardar; una WABA o número atados a la entidad equivocada no se pueden mover.
Ese documento lista M1-M7 (Meta), S1-S8 (casa), A1-A6 (admin de entidad), B1-B5 (bot) y el
cierre del webhook, con la orden para generar cada secreto y SIN valores. Lo que teclea el dueño:
`app_secret`, `verify_token` y `access_token` de la cuenta de Meta; URL de avisos, clave de
servicio y secreto saliente en «Configurar bot»; y el `.env` del bot. Lo que `nectar-cd` lee de
producción cuando existan y entrega para el `.env`: id de cuenta, id del canal e ids de los tres
equipos; los nombres de las plantillas los conoce el dueño porque las crea él.

---

## 10. Entorno de pruebas

- **Bot local** en Docker (`docker compose up -d`), `PORT=8090`, MySQL local, SIESA local en el
  contenedor `siesa_db` (réplica de prod; ver memoria `local-prod-replica-setup`). El
  `docker-compose.override.yml` apaga WhatsApp/IVR autónomos hasta que se decida probarlos.
- **Túnel:** el de hdg-bot, `https://endodermal-prearticulate-teodora.ngrok-free.dev` → `:8090`.
  El `.env` local de Neuro ya tiene ese hostname y ese puerto. hdg-bot apaga su stack cuando este
  chat avise; ambos no pueden usar el hostname a la vez. En producción, el servidor de la
  clínica con `app.colibrixa.com` → `:8085`.
- **Orden de pruebas** (cada fase con su verificación antes de pasar a la siguiente):
  1. Sin red: tests unitarios portados + fixtures de `docs/volcados`.
  2. Local sin Néctar real: `cmd/nectar-stub` como destino (`NECTAR_BASE_URL=http://host:9999`) y
     `cmd/probe-notice` inyectando avisos firmados al webhook; el harness E2E existente
     (`cmd/e2e`, skill `e2e-bot`) ejercita la máquina de estados.
  3. Contra Néctar real con el túnel: un paciente de pruebas de la lista blanca escribe al número;
     recorrido de agendamiento completo contra SIESA local; escalado y retorno desde la consola
     de Néctar; cierre.
  4. Proactivo (requiere D1 desplegado): recordatorio manual con `POST /api/internal/send-reminders`,
     respuesta por botón, IVR de las 13:00 por Bird al que no respondió, lista de espera al
     cancelar una cita.
- **Corte a producción:** fuera de esta migración; se diseña cuando lo anterior esté verde.

---

## 11. Dashboard (`neuro-dashboard`)

Medido: agenda, catálogo, KPIs de SIESA, exportes y tickets no tocan Bird. Lo único afectado es
el **informe unificado de agentes** (`internal/reports`): roster en vivo de Bird
(`/api/internal/agents/bird`), llamadas por agente (`/api/internal/agents/bird-calls`, voz, sigue
válido) y la tabla `escalations` del bot (`agent_id`, `agent_name`, `first_agent_msg_at`, que
en Néctar el bot ya no ve).

Para esta migración: el informe marca las columnas de agentes como «ver en Néctar» cuando el
bot responda que el puente está activo; las llamadas de voz se mantienen; etiquetas
«Bird WhatsApp» en tickets pasan a «WhatsApp». **Deuda obligatoria (D5):** cuando Néctar exponga
la ruta de lectura de agentes, el informe vuelve a ser unificado leyendo de ahí.

---

## 12. Fases (para el plan)

1. **Port del puente** (paquete `internal/nectar`, desvío en `bird.Client`, webhook + WAL,
   capacidades, config) con sus tests; con `NECTAR_BASE_URL` vacía todo igual que hoy.
2. **Escalación v1 + retorno + ruteo por grupo** (lo de §7.2 puntos 1-3).
3. **Identidad (D2)** y lista blanca (§7.2 puntos 5-6). Depende de que Néctar despliegue `contact`.
4. **Proactivo por Néctar (D1)** (§7.2 punto 4) + plantillas (§8). Depende de la apertura de Néctar.
5. **Dashboard** (§11) y documentación (README, TROUBLESHOOTING, GUIA, FLUJOS, `.env.example`).
6. **Pruebas en vivo** (§10 fases 3 y 4) con el túnel.

Las fases 1, 2 y 5 no dependen de Néctar y pueden empezar ya. Las 3 y 4 esperan las dos rutas.

---

## 13. Riesgos

| Riesgo | Mitigación |
|---|---|
| D1 tarda o cambia de forma | Fases 1-2 avanzan igual; la capa proactiva se escribe contra la forma acordada y se ajusta. |
| Botones de plantilla de Meta no devuelven el payload que el bot espera | Mapeo título→payload en el bot; se verifica en la prueba 4 antes de mover nada más. |
| Néctar reintenta avisos sin fin si el túnel cae | Dedupe por `event_id` + WAL ya lo absorben; el túnel se enciende antes de configurar `bot_notice_url`. |
| Divergencia entre hdg-bot y Neuro en los archivos tocados (`pool.go`, `session/manager.go`, `escalation.go` difieren mucho) | El port es por función, no por diff: cada pieza se copia y se adapta con sus tests, no se hace merge. |
| PII en el WAL | `contact` va en `state`, no en avisos; el WAL de Neuro sigue sin datos nuevos. |
| Compartir hostname del túnel con hdg-bot | Aviso explícito a hdg antes de levantar; verificación de `/health/debug` para confirmar qué bot responde. |

---

## 14. Lo que queda al constructor

- Nombres finales de tipos/funciones al portar (seguir los de hdg-bot salvo que choquen con Neuro).
- Cómo registrar el menú de retorno y el `ReturnInput` de cada estado escalable de Neuro
  (lista de estados y datos que puede pedir cada uno).
- Mapeo exacto motivo interno de Neuro → `reason` cerrado de Néctar.
- Mapeo título de botón de plantilla → payload (§8) una vez vista la forma real del
  `interactive_reply_id` en una respuesta a plantilla.
- Formato de `NECTAR_TEMPLATE_NAMES` y `NECTAR_TEAM_BY_GROUP` (parsers ya existen en hdg-bot).
- Orden y tamaño de los commits dentro de cada fase; los tests de cada pieza van con ella.
