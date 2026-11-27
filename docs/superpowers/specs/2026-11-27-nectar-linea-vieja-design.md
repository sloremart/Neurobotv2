# Línea vieja en Néctar: dos canales de WhatsApp en el mismo bot

Fecha: 2026-11-27. Rama: `feat/nectar-linea-vieja` (desde `feat/migracion-nectar`), en Neurobotv2 y en neuro-dashboard.

## Intención

Neuro tendrá dos números de WhatsApp en Néctar, en la misma WABA y la misma app de Meta:

- **Línea nueva (principal):** el número que hoy usa n8n. Entra a Néctar el fin de semana del 2026-11-28 con la
  versión actual del bot, sin este ajuste. Por ella salen TODAS las notificaciones (recordatorios, confirmaciones,
  lista de espera).
- **Línea vieja:** el número que hoy está en Bird. Lo alquila Bird, que también lleva la voz (el IVR sigue en Bird
  Voice con ese mismo número). Tras una semana de transición, su WhatsApp pasa a la API de Meta y a Néctar como
  segundo canal. Solo atiende a quien escriba ahí, y cada saludo lleva la nota del número nuevo
  (`NEW_LINE_ANNOUNCEMENT`), como hoy en Bird.

Por qué: quitar la relación con Bird sobre el WhatsApp sin perder a los pacientes que tienen guardado el número viejo,
y conservar los KPI de la línea vieja (Bird hasta el traslado y Néctar después).

## Lo que Néctar ya soporta (confirmado por la sesión de Néctar sobre el código desplegado, 2026-11-27)

- Dos canales en una cuenta de Meta. La entrada se enruta por `phone_number_id`, y cada mensaje sale por el número de
  su conversación. Las plantillas son de la WABA y abren por cualquiera de los dos canales.
- **Un bot por canal (`channel_bot`):** clave de servicio, `notice_url`, secreto saliente y secreto entrante propios.
  La clave de un canal NO opera conversaciones del otro (404, Plan 50).
- **Qué ve la clave de un canal:**

  | Lectura | Alcance |
  |---|---|
  | `GET agents` | Toda la entidad (`open_conversations` cuenta los dos canales) |
  | `GET waiting-cycles` | Solo su canal (`history_since` es de la entidad) |
  | `GET schedule` | Horario de su canal; festivos de la entidad |
  | `availability`, adjuntos, `resolve`, `open` | Solo su canal |

- Los avisos NO traen `channel_id` (salvo `opened`). El canal se deduce de la `notice_url`: la línea vieja apunta a
  `https://<bot>/api/webhooks/nectar/legacy`.
- **Capacidades:** Néctar las pide a `<esquema+anfitrión de la notice_url>/api/nectar/capabilities`, la misma URL para
  los dos canales, firmadas con el secreto saliente del canal. En lo desplegado, su caché va por raíz de URL: **los dos
  canales deben usar el MISMO secreto saliente**.
- Las conversaciones son una por canal y el contacto es uno por entidad. La apertura no mira el otro canal: un paciente
  puede quedar con dos hilos.

## Metas

1. El bot opera cada conversación con la clave de su línea.
2. Todos los mensajes proactivos abren por la línea nueva, también si el paciente conversó antes por la vieja.
3. Toda conversación de la línea vieja lleva la nota del número nuevo en el saludo (`GREETING` y `OUT_OF_HOURS`).
4. Los KPI se agrupan **por número** (decisión del dueño, opción A): línea vieja = Bird antes del traslado + canal viejo
   de Néctar después; línea nueva = canal principal de Néctar; más el total. Las cifras de asesores siguen separadas
   por plataforma (los agentes de Bird y los de Néctar son personas y rosters distintos).
5. Desplegar con la línea vieja sin configurar no cambia nada (mismo comportamiento que `feat/migracion-nectar`).

## No-metas

- N canales genéricos (enfoque 2, descartado). Por dentro se arma un mapa línea → cliente para no impedirlo después.
- Horario distinto por línea. Se exige operativamente que los equipos cubran los dos canales (ver Operación).
- Cambiar el contrato de Néctar (`channel_id` en los avisos), que obligaría a avisar a hdg-bot.
- Llevar el IVR a Néctar o los flujos de n8n al bot (proyectos aparte).
- Evitar los dos hilos del mismo paciente (decisión: se aceptan; el recordatorio sale siempre).

## Anti-metas (sería un fracaso aunque funcione)

- Que un recordatorio salga por la línea vieja.
- Que un mensaje de la línea vieja se pierda o quede sin respuesta porque el bot usó la clave equivocada (404 de Néctar
  tratado como «conversación ajena»).
- Que el histórico de Bird desaparezca o cambie de cifra en el dashboard.
- Tocar el flujo o las validaciones del bot (regla del dueño: el trabajo de Néctar es solo integración).

## Enfoques

1. **Dos líneas fijas, nueva y vieja (ELEGIDO por el dueño).** Segunda clave por configuración, ruta de avisos propia,
   registro conversación → línea y un puente que reparte por línea. Poco código, fácil de probar.
2. **Lista genérica de N canales.** Configuración con canales, claves, rutas y papeles. Más preparado para el futuro,
   pero más configuración y más casos para un tercer número que no está en el plan.
3. **Pedir a Néctar `channel_id` en cada aviso.** Cambio de contrato con aviso a hdg-bot, y de todos modos harían falta
   las dos claves (Plan 50). Descartado.

## Diseño

### Línea de una conversación

- Valores: `main` y `legacy`. La clave de sesión sigue siendo `nectar:<conversation_id>`: hay cinco parsers de esa
  forma y cambiarla rompería claves, WAL, pendings y trazas.
- **Registro persistente** `nectar_conversation_line` (migración 049): `conversation_id BIGINT PK`, `line VARCHAR(10)
  NOT NULL`, `created_at`. Tiene una caché en memoria delante (lectura en cada envío).
  - Se escribe al recibir un aviso, según la ruta, ANTES de insertar en el WAL y de responder 200. Si falla la
    escritura, responde 500 y Néctar reintenta.
  - Se escribe al abrir con plantilla (siempre `main`).
  - Una conversación sin registro es `main`: todo lo anterior a este cambio es de la línea principal. Esa lectura deja
    un `Warn` solo si llega por la ruta legacy.
- **Confirmado por Néctar (2026-11-27):** `conversation.id` es un `BIGSERIAL` de una sola tabla para toda la
  instancia, y una conversación nunca cambia de canal. `event_id` es un UUID con restricción única global, y los
  reintentos conservan el mismo. El registro, la caché de contacto, la deduplicación y el WAL siguen valiendo tal cual.
- El WAL no cambia: al reproducir, la línea sale del registro, que se escribió antes del WAL.

### Configuración

| Variable | Uso |
|---|---|
| `NECTAR_LEGACY_SERVICE_KEY` | Clave del `channel_bot` del canal viejo. Vacía = línea vieja apagada. |
| `NECTAR_LEGACY_CHANNEL_ID` | Canal viejo en Néctar: validación al arrancar y trazas. Obligatoria si hay clave. |
| `NECTAR_LEGACY_REQUEST_SECRET` | Opcional; por defecto `NECTAR_REQUEST_SECRET` (el secreto entrante del canal, o el global si es NULL). |
| `NECTAR_LEGACY_NOTICE_SECRET` | Opcional; por defecto `NECTAR_NOTICE_SECRET`. Se recomienda el mismo valor (caché de capacidades). |

- Configuración a medias (clave sin canal, o canal sin clave): la línea vieja queda apagada, con un error al arrancar,
  como `nectarBridgeFromConfig` hoy. Sin puente principal, no hay línea vieja.
- `/health` publica `nectar_legacy: armed|off`, sin valores.

### Puente que reparte (`nectar.LineRouter`)

- Implementa `bird.NectarBridge` y guarda `map[line]*nectar.Bridge`. `bird.Client.SetNectar` recibe el router.
  - Cada método con `conversationID` toma la línea del registro y delega en el puente de esa línea.
  - `OpenWithTemplate` usa siempre `main`.
- Cada línea tiene su `*nectar.Client`, con su mapa de claves en duda y sus contadores de urgencia.
  `SetUrgentReporter` se llama en los dos con el mismo `UrgentAlerter`.
- Sin línea vieja configurada, el router tiene solo `main` y se comporta exactamente como hoy.

### Avisos

- Nueva ruta `POST /api/webhooks/nectar/legacy`, con el mismo handler y la línea como parámetro. La firma se verifica
  con el secreto de esa línea, sobre la URL completa (ya incluye la ruta).
- **Respuestas de la ruta legacy** (decisión mía):
  - **Línea vieja nunca configurada** (sin `NECTAR_LEGACY_*`): 404. Es un error de alta que Néctar debe ver.
  - **Línea vieja configurada pero puente apagado** (vuelta atrás con `NECTAR_BASE_URL` vacía): 200 y descarte, con
    firma válida, igual que la ruta principal. Un no-2xx hace que Néctar reintente sin tope y reproduzca todo lo
    acumulado al volver el puente.
- `handleOpenedByAgent`, `dispatchNectar` y la reproducción no cambian: la línea ya está en el registro.
- **Capacidades** (`GET /api/nectar/capabilities`): aceptan la firma con cualquiera de los dos secretos. Los menús
  dependen del paso, no del número.

### Mensajes proactivos (siempre por la línea nueva)

- **Plantilla sobre una conversación de la línea vieja.** Pasa en `worker/queue_expired.go`: con la ventana de 24 h
  cerrada, se manda `SendTemplate(sess.PhoneNumber, …)` sobre la conversación de la propia sesión. Ningún llamador
  actual de `SendProactiveTemplate` pasa una clave `nectar:`: el programador, la lista de espera y el dashboard
  pasan el teléfono de SIESA.
- **Decisión mía, coherente con la meta 2:** toda plantilla cuya conversación de destino es `legacy` NO sale por la
  línea vieja. Se reabre por `main` con el teléfono del contacto (`contact_identifier` de la sesión o `ContactPhone`).
  - Se hace en un solo punto de `bird.Client`, usado tanto por `SendTemplate` como por la rama `nectar:` de
    `SendProactiveTemplate`.
  - El router además se niega a mandar una plantilla por `legacy` (error explícito), como red de seguridad.
  - Sin teléfono conocido, falla como un envío sin destino y sigue la ruta de error de hoy.
- **409 `mine` de una apertura:** el id viene del canal `main`, porque la apertura usó la clave principal.
- La lista de espera ya resuelve el teléfono de SIESA antes de abrir: no cambia.
- `AdoptPhonePending` no cambia. Si el paciente responde por la vieja, el pending pasa a esa conversación y el router
  usa la clave vieja.
- **Limitación aceptada:** `contactIndex` guarda un pending por teléfono, y con dos hilos activos gana el último.

### Nota del número nuevo

- `announcementFor` devuelve el anuncio en las sesiones de Bird (como hoy) y también en las sesiones `nectar:*` cuya
  línea es `legacy`.
- La línea llega al handler por un campo de sesión (`Session.NectarLine`), que se carga de `sessions.nectar_line`.

### Columna `nectar_line` para los KPI (migración 049)

- `sessions.nectar_line VARCHAR(10) NULL`, escrita al crear la sesión a partir del registro.
- `chat_events` y `escalations` la copian con una subconsulta a `sessions`, igual que `contact_identifier`
  (`event_repo.go`, `escalation_repo.go`).
- `nectar_waiting_cycles.line VARCHAR(10) NULL`. NULL = `main`, que es todo lo anterior.
- **Regla de agrupación (bot y dashboard):**
  - línea vieja = `phone_number NOT LIKE 'nectar:%'` (Bird) **o** `nectar_line = 'legacy'`;
  - línea nueva = `phone_number LIKE 'nectar:%' AND (nectar_line IS NULL OR nectar_line = 'main')`.
- `ADD COLUMN NULL` al final, instantáneo en MySQL 8.4, también en `chat_events`.
- **Eventos de notificaciones e IVR** (decisión mía). Con el puente encendido, el bot registra varios eventos con el
  teléfono de SIESA y no con la clave `nectar:`:
  - recordatorios: `reminder_prep_sent`, `reminder_suppressed_no_whatsapp`;
  - resultado de los envíos: `notification_send_failed`, `notification_in_doubt`, `notification_skipped_agent_owned`;
  - IVR: `notification_confirmed_ivr`, `notification_cancelled_ivr`, `notification_timeout`.

  Con la regla de arriba caerían en «línea anterior».
  - **Al escribirlos:** el repositorio de eventos pone `nectar_line = 'main'` en esos tipos cuando el puente está
    encendido, porque todo lo proactivo abre por la línea nueva. Los tipos salen de una lista cerrada en un solo
    sitio, junto a quien escribe el evento. Con el puente apagado quedan NULL, o sea Bird, como hoy.
  - **Lo ya escrito:** un script idempotente de una sola vez
    (`UPDATE chat_events SET nectar_line='main' WHERE nectar_line IS NULL AND event_type IN (…) AND created_at >= <fecha de
    encendido del puente en producción>`). Corrige lo registrado entre la entrada de la línea nueva (2026-11-28) y el
    despliegue de este ajuste. La fecha la pone quien despliega; el script la recibe como argumento.
  - El IVR es voz y no es de ninguna de las dos líneas. Se cuenta con la línea nueva porque responde a recordatorios que
    salieron por ella.

### Adjuntos (OCR)

- Hoy `ocrSvc.SetNectarCredentials` guarda una sola clave por anfitrión, y un adjunto de la línea vieja con la clave
  principal da 404.
- **Decisión mía:** el servicio de OCR guarda las credenciales de las dos líneas para el anfitrión de Néctar. Prueba con
  la principal y, ante 404, con la vieja. No hay que pasar la línea por el descargador, que solo recibe la URL.

### Informes de asesores (D5)

- El syncer lee los tramos con las dos claves. Cada lectura guarda su `line` en `nectar_waiting_cycles` y tiene su
  propio estado.
  - Los nombres de `main` no cambian (`last_seen_opened_at`, `history_since`, `last_success_at`), porque el dashboard
    los lee.
  - Los de la vieja llevan el prefijo `legacy:`.
- `OldestOpenCycleOpenedAt` se calcula por línea.
- El roster de agentes se lee una vez, con la clave principal: es de toda la entidad.
- `applyAttentions` empareja escalaciones y tramos por conversación, sin importar la línea.
- `ErrAccountKeyRequired` en una línea detiene solo esa línea.

### Aviso «URGENTE — volver a Bird»

- Hoy cuenta fallos por disparador (`nectar_down`, etc.) en una ventana común.
- **Decisión mía:** solo cuenta la línea `main`. Los fallos de la línea vieja (red, 5xx, clave rechazada) van al log y
  a un ERROR de Telegram, una vez por ventana, sin disparar el URGENTE: la línea vieja caída no deja muda la línea
  principal.
- Cuando el número viejo salga de Bird, «volver a Bird» ya no devuelve ninguna línea de WhatsApp. Con el traslado se
  actualizan `docs/GUIA-VOLVER-A-BIRD.md` y el texto del aviso: después del traslado, la contingencia es que los
  supervisores de Néctar rescaten las conversaciones (motivo `bot_down`), no volver a Bird. El aviso deja de
  mencionar Bird cuando la línea vieja está configurada.

### Horario y caché de contacto

- El horario se sigue leyendo del canal principal. Los festivos son de la entidad.
- El corte de la caché de contacto (`unreachableUntil`) pasa a ser por línea: una clave vieja rechazada no apaga los
  contactos de la línea nueva.

### Dashboard (neuro-dashboard)

- `internal/kpi/line.go`:
  - `Line` pasa de `bird|nectar` a `old|new` (con el total).
  - `lineFilter` usa la regla de agrupación de arriba, para tablas con `nectar_line` o uniendo con `sessions`.
  - Las cifras de asesores (`sla-by-agent`, informes por agente, roster) siguen separando por plataforma, por el
    prefijo `nectar:` de `agent_id`.
- **Etiquetas:** «Línea anterior» y «Línea nueva». Antes del traslado la línea anterior es solo Bird y no hace falta
  explicación.
- `nectarSyncState` lee el estado de `main` (nombres sin cambio). Agregar el de `legacy` es opcional, solo para la
  marca «sin dato de Néctar» por línea.
- El frontend (`byLine.ts`, `LineSplit.tsx` y sus páginas) cambia solo las etiquetas y las claves.
- **Compatibilidad:** el dashboard debe tolerar una base sin la migración 049 (columna ausente = todo `main`), igual
  que hoy tolera la ausencia del esquema de Néctar (`hasNectarSchema`).

## Operación (fuera del código, en el orden del traslado)

1. **Néctar:**
   - canal viejo con su `phone_number_id`;
   - `team_channel` de los TRES equipos en los dos canales: sin esto, la línea vieja se ve cerrada o un agente queda
     reservado sin ver el canal;
   - `channel_bot` del canal viejo con clave nueva, `notice_url = https://app.colibrixa.com/api/webhooks/nectar/legacy`
     y el MISMO secreto saliente de la línea nueva.
2. **Bot:** las variables `NECTAR_LEGACY_*` en `.env`, y `docker compose up -d bot`.
3. **Meta:** el número se registra en la WABA de Neuro con PIN de verificación en dos pasos. El código lo recibe el agente
   de voz de Bird.
4. **Verificación:**
   - un mensaje al número viejo recibe la nota y el menú;
   - un recordatorio de prueba sale por el número nuevo;
   - el dashboard muestra el mensaje en «Línea anterior».

## Pruebas

- **Unitarias:**
  - el router delega por línea, y sin registro usa `main`;
  - `OpenWithTemplate` usa siempre `main`;
  - `SendProactiveTemplate` con una clave legacy abre por `main` con el teléfono;
  - el aviso por la ruta legacy registra la línea antes del WAL, y si falla el registro responde 500;
  - la reproducción usa el registro;
  - las capacidades aceptan los dos secretos;
  - `announcementFor` sale en Bird y en `nectar:*` legacy, y no en `main`;
  - el OCR reintenta con la clave vieja ante 404;
  - el syncer lleva el estado por línea y conserva los nombres de `main`;
  - con la configuración a medias, la línea vieja queda apagada con error;
  - la ruta legacy: 404 sin configurar, y 200 con descarte si está configurada pero el puente está apagado;
  - `SendTemplate` sobre una conversación legacy (ventana vencida) reabre por `main`, y el router rechaza una
    plantilla por `legacy`;
  - los eventos de notificación e IVR quedan con `nectar_line = 'main'` con el puente encendido;
  - el URGENTE no cuenta los fallos de `legacy`;
  - sin línea vieja, todo igual (las pruebas actuales siguen verdes).
- **Integración (MySQL):** migración 049; la copia de `nectar_line` a `chat_events` y `escalations`; el registro y su
  caché.
- **Dashboard:** la regla de agrupación sobre filas de Bird, de `main` NULL, de `main` y de `legacy`; y una base sin
  049.
- **Local:** `cmd/probe-notice` firma avisos contra las dos rutas, sin un segundo número real.

## Lo que queda a criterio de quien implemente

- Nombres internos (tipo `Line`, constantes) y dónde vive el registro (`internal/nectar` o `repository/local`).
- La forma de la caché del registro (tamaño y expiración), siempre que una conversación nueva quede registrada antes
  de su primer envío.
- Si `NECTAR_LEGACY_CHANNEL_ID` se valida contra Néctar al arrancar o solo se registra en las trazas.
