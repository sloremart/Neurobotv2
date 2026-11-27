# Resultados de estudios, imágenes y SIAU en el bot

Fecha: 2026-11-27. Rama: `feat/resultados-bot`, creada desde `feat/nectar-linea-vieja` porque usa su router por
línea. Solo cambia Neurobotv2.

## Intención

El número que hoy atiende el chatbot de n8n («Samuel Bot - Solo Resultados v2 prod») pasa a Néctar como **línea
nueva**. Desde ese momento los mensajes de ese número le llegan al bot y no a n8n. Lo que hacía ese chatbot y el bot
no tiene se lleva al bot, en las dos líneas:

| Opción de n8n | En el bot |
|---|---|
| Resultados de estudios (EMG, EEG, PSG…) por cédula, con el PDF | **Nuevo:** con verificación por fecha de nacimiento y el PDF por Néctar |
| Resultados de imágenes (portal de Lumier) | **Nuevo:** enlace al portal |
| SIAU (asesora en Chatwoot) | **Nuevo:** pasa al equipo SIAU de Néctar |
| Agendar medicamento | Se queda el flujo actual del bot (decisión del dueño: el de Lorena) |
| Agendar cita | Se queda el flujo actual del bot |

Por qué: apagar el chatbot de n8n y la bandeja de Chatwoot de ese número sin que los pacientes pierdan servicio, y
cerrar el hueco de privacidad de n8n, que entrega PDF clínicos a quien escriba cualquier cédula.

**Fuera de alcance:** ENVIOAUTO (`neurodx-wa-send`) y el cierre de tickets de GLPI siguen en n8n. ENVIOAUTO envía desde
otro número (`phone_number_id` …4428) y lo dispara otra aplicación.

## Decisiones del dueño

- Reparto de opciones del menú: el de la tabla de arriba.
- **El PDF se envía por Néctar** (opción B, contrato de adjuntos del bot, lote 11 de Néctar). Nunca directo a Meta.
- **Verificación por fecha de nacimiento**, comparada con la del paciente en SIESA.

## Metas

1. El menú principal, en las dos líneas, ofrece:
   - «Resultados de estudios»;
   - «Resultados de imágenes»;
   - «Atención al usuario (SIAU)».
2. Resultados de estudios:
   1. tipo y número de documento;
   2. fecha de nacimiento;
   3. si coincide con SIESA, la lista de estudios de la API de resultados;
   4. el PDF del elegido, por Néctar. Si solo hay uno, se envía directo.
3. Nadie recibe un resultado sin pasar la verificación.
4. Mientras Néctar no tenga los adjuntos, o con la función apagada, «Resultados de estudios» se comporta como hoy:
   el enlace a la página de resultados.
5. SIAU escala al equipo SIAU de Néctar con su propio motivo.
6. El bot no guarda el PDF en disco ni lo escribe en logs.

## No-metas

- Llevar ENVIOAUTO al bot.
- Cambiar el flujo de medicamentos o de agendamiento.
- Plantillas con encabezado de documento para enviar un resultado fuera de la ventana de 24 h (Néctar las trae en el
  mismo lote; se usan en otro proyecto).
- El portal de Lumier o el de resultados: solo se enlazan.
- Enviar PDF por Bird: en una conversación de Bird se manda el enlace.

## Anti-metas

- Entregar un PDF sin verificar, o el de otra persona.
- Que el número de documento, la fecha de nacimiento, el nombre o el PDF queden en logs o en `chat_events` sin
  enmascarar.
- Que una falla de la API de resultados o de Néctar deje al paciente sin respuesta.

## Enfoques

1. **El bot consulta la API de resultados y envía por Néctar (ELEGIDO).** Un cliente HTTP nuevo para
   `192.168.1.211:8095` y los pasos nuevos en la máquina de estados. Todo queda en un solo sistema, con pruebas,
   eventos y la seguridad del bot.
2. **n8n como servicio que el bot llama.** Se reutiliza el flujo, pero suma una dependencia, deja datos clínicos
   viajando entre dos sistemas y reparte la lógica. Descartado: la lógica es corta, dos GET.
3. **Enviar el PDF directo a Meta, como n8n.** Descartado por el dueño: queda fuera de Néctar y el asesor no lo ve.

## Diseño

### API de resultados (sistema externo, `RESULTS_API_URL`)

- `GET {base}/api/buscar?cedula=<número>`: devuelve una lista de estudios, o un objeto suelto si hay uno. Campos que
  usaba n8n: `nombre`, `fecha` («dd/mm/aaaa hh:mm»), `admision`, `tipo_estudio`, `archivo`. Cualquier error o una
  lista vacía cuenta como «sin resultados».
- `GET {base}/api/pdf/<archivo>`: el PDF en binario.
- La API no pide clave hoy. El cliente acepta una cabecera opcional (`RESULTS_API_KEY`, que viaja en
  `X-API-Key`) para cuando sistemas la agregue.
- **Plazos:** 10 s por consulta y 20 s por PDF.
- **Tope del PDF:** 16 MiB, el límite de Néctar. Si lo pasa, se manda el enlace al portal.
- `archivo` se valida (sin `/`, `\` ni `..`) antes de armar la URL.

### Configuración

| Variable | Uso |
|---|---|
| `RESULTS_API_URL` | Base de la API de resultados. Vacía = la función queda apagada, como hoy. |
| `RESULTS_API_KEY` | Opcional. |
| `RESULTS_PDF_ENABLED` | `true` cuando Néctar tenga desplegados los adjuntos del bot. Por defecto `false`. |
| `IMAGING_RESULTS_URL` | Enlace al portal de Lumier. Vacía = la opción no aparece en el menú. |
| `RESULTS_URL` | Ya existe: el enlace a la página de resultados, que sigue siendo el respaldo. |
| `NECTAR_TEAM_BY_GROUP` | Agrega el grupo `siau:<id del equipo>`. |

Ningún enlace ni token va escrito en el código.

### Menú principal

- Se cambia `buildMainMenuList` y su menú de regreso (`RegisterStaticReturnMenu` de `MAIN_MENU`):
  - «Consultar Resultados» pasa a ser **«📋 Resultados de estudios»** (`resultados`);
  - se agregan **«🩻 Resultados de imágenes»** (`imagenes`), solo si `IMAGING_RESULTS_URL` no está vacía, y
    **«🧑‍💼 Atención al usuario (SIAU)»** (`siau`).
- El tope de WhatsApp es 10 filas por lista, y quedan 8.
- El menú fuera de horario (`OUT_OF_HOURS_MENU`) gana las mismas dos opciones de autoservicio: los resultados y las
  imágenes no dependen del horario. SIAU fuera de horario sigue la regla de siempre de las escalaciones.

### Resultados de estudios (estados nuevos)

1. **`RESULTS_ASK_DOC_TYPE`:** el mismo menú de tipo de documento del registro, reutilizando `docTypeMenuText` y su
   validación.
2. **`RESULTS_ASK_DOC_NUMBER`:** el número; se valida igual que en el registro.
3. **`RESULTS_ASK_BIRTH_DATE`:** se lee con el `parseBirthDate` del registro.
   - Busca al paciente en SIESA con `FindByDocument(tipo, número)` y compara solo la fecha, sin la hora.
   - **Coincide:** pasa a la consulta.
   - **No coincide, o el paciente no está en SIESA:** «Los datos no coinciden», con el mismo mensaje en los dos casos
     para no revelar si la persona existe. Vuelve a pedir el documento.
   - **Tope:** 3 intentos fallidos por sesión, contando documento más fecha. Al tercero se envía el enlace al
     portal y la opción de SIAU, y se cierra el flujo. No escala a un asesor (decisión mía: un tercero que prueba fechas no
     debe ocupar a un asesor).
   - SIESA caída: se envía el enlace al portal, sin contar el intento.
4. **`RESULTS_LIST`:**
   - **Uno:** se envía directo.
   - **Varios:** lista ordenada por fecha, la más reciente primero. Cada fila lleva el `tipo_estudio` como título
     (máx. 24 caracteres) y la fecha con la admisión como descripción (máx. 72). Si hay más de 10, salen los 10 más
     recientes y un texto que remite al portal para los anteriores.
   - **Ninguno:** el mensaje de n8n, «No encontramos resultados… puede que aún no estén disponibles», más el enlace al
     portal.
5. **Envío:**
   - Baja el PDF a memoria y lo sube por Néctar (`POST /api/bot/uploads`) con la clave de la línea de la
     conversación.
   - Lo envía como mensaje con adjunto (`type: "attachment"`).
     - `filename`: `Resultado-<tipo_estudio sin caracteres raros>-<aaaa-mm-dd>.pdf`, como máximo 200 caracteres.
     - `caption`: «📄 Hola <nombre>, tu resultado de *<tipo_estudio>*.», como máximo 1024.
   - Después vuelve al menú de seguir o terminar, como los demás autoservicios (`buildAutoCloseResult`).
   - **Fallas:**
     - Subida o envío con error definitivo (415, 413, 422) o API de resultados caída: se envía el enlace al portal
       con la disculpa.
     - `409 window_closed`: no aplica, porque el paciente acaba de escribir.
     - En duda (5xx, falta de respuesta): no se reintenta en el mismo turno. Néctar reintenta solo lo que no es
       ambiguo, y lo dudoso queda `unknown_outcome`.
6. **Sin la función** (`RESULTS_API_URL` vacía, `RESULTS_PDF_ENABLED` en false, o una conversación de Bird): la
   opción hace lo de hoy (`showResultsHandler`: enlace y video).

Los estados nuevos registran su menú de regreso para el asesor («Reanudar», «Volver al menú»), como todo paso escalable
(regla de CLAUDE.md).

### Resultados de imágenes

- Opción `imagenes`: un texto con el enlace de `IMAGING_RESULTS_URL` y cierre automático, como el de resultados de hoy:
  «🩻 *Resultados de imágenes*\n\nConsulta y descarga tus resultados de imágenes diagnósticas en el portal: <url>».
- Si Néctar o Bird no admiten un botón con enlace, va como texto con la URL.

### SIAU

- Opción `siau`: escalación con el motivo nuevo `siau` (`EscalationReasonSIAU`).
  - Equipo sugerido: el del grupo `siau` de `NECTAR_TEAM_BY_GROUP`. Sin ese grupo, la escalación sigue sin equipo
    sugerido, como hoy.
  - Mensaje al paciente: «🧑‍💼 *Atención al usuario (SIAU)*\nTe comunico con nuestra área de atención al usuario.
    Escribe tu consulta, sugerencia o queja y un asesor te responderá en breve. ⏳».
- **Tope de escalaciones:** `siau` cuenta, como pedir un asesor: lo pidió el paciente
  (`EscalationCountsTowardCap`).
- En Bird: la escalación de siempre, con el motivo `siau` en el resumen para el agente.

### Cliente de adjuntos de Néctar (contrato del lote 11)

- **`Client.Upload(ctx, idempotencyKey, filename string, data []byte) (Attachment, error)`:**
  - `POST /api/bot/uploads` con `multipart/form-data` (campos `idempotency_key`, `filename` y `file`);
  - firma de siempre: HMAC sobre `timestamp + URL + SHA-256(cuerpo multipart ya serializado)`;
  - respuesta: `attachment_id`, `mime`, `size`, `sha256`, `filename` y `expires_at`.
- **`Client.SendAttachment(ctx, conversationID, attachmentID, caption string) (int64, error)`:** el cuerpo de
  `messages` lleva `type: "attachment"` y `attachment: {id, caption?}`, con la misma idempotencia y la misma
  política de reintentos que los demás envíos (`retrySend`).
- **Errores tipados:**

  | Error | HTTP | Código de Néctar |
  |---|---|---|
  | `ErrUnsupportedMedia` | 415 | `unsupported_media_type` |
  | `ErrTooLarge` | 413 | `too_large` |
  | `ErrAttachmentUnavailable` | 422 | `attachment_unavailable` |
  | `ErrWindowClosed` | 409 | `window_closed` |
  | `ErrIdempotencyReused` | 409 | `idempotency_key_reused` |

  El 404 sigue siendo «no es mía».
- **Puente y router:**
  - `bird.NectarBridge` gana `SendDocument(ctx, conversationID, filename string, data []byte, caption string)
    (string, error)`, que sube y envía.
  - `nectar.LineRouter` lo reparte por línea: sube y envía con la clave de la línea de la conversación.
  - El fake de pruebas lo implementa.
- **Hasta que Néctar despliegue el lote 11** se prueba contra un stub HTTP que sigue el contrato. El contrato
  definitivo estará en `PARA-EL-EQUIPO-DEL-BOT.md` de Néctar. La propuesta está en la sesión de Néctar
  (`CONTRATO-ADJUNTOS-DEL-BOT.md`, con su §5). Si el contrato cambia al implementarse, Néctar avisa antes.

### Privacidad y eventos

- **Eventos:** `results_menu`, `results_verified`, `results_verification_failed` (con el número de intento),
  `results_none`, `results_sent` (con el `tipo_estudio`, sin el nombre del archivo), `results_fallback_link` (con el
  motivo), `imaging_link_sent` y `siau_requested`.
- **Lo escrito por el paciente:**
  - el documento se enmascara en los pasos de tipo de documento, como hoy;
  - la fecha de nacimiento y el número **no se registran**: solo su largo, como en los pasos de datos personales
    (C-04);
  - los pasos nuevos se agregan a esas listas.
- **Logs:** nunca el número, la fecha, el nombre, `archivo` ni el contenido del PDF. El PDF vive en memoria durante el
  turno y no se cachea.

## Pruebas

- **Unitarias:**
  - el cliente de resultados: lista, objeto suelto, vacío, error, `archivo` inválido y PDF de más de 16 MiB;
  - la verificación: coincide, no coincide, no existe en SIESA con el mismo mensaje, SIESA caída y tope de 3;
  - la lista: 1 se envía directo, 2-10 en lista, más de 10 recorta y remite al portal;
  - el envío: con éxito, con 415, 413, 422 o la API caída (enlace al portal), y sin la función (comportamiento de hoy);
  - SIAU: motivo, equipo del grupo `siau`, cuenta para el tope;
  - el menú con y sin `IMAGING_RESULTS_URL`;
  - el cliente de Néctar: el multipart firmado contra un stub que verifica la firma, los errores tipados y la
    idempotencia;
  - el router: `SendDocument` con la clave de la línea correcta.
- **Integración local:** contra un stub de la API de resultados y uno de Néctar (`cmd/probe-notice` para los avisos).
  La API real de `192.168.1.211:8095` no se llama desde las pruebas.

## Operación

1. En Néctar, crear el equipo SIAU con sus asesores y poner su id en `NECTAR_TEAM_BY_GROUP` (`siau:<id>`). Lo mismo
   para el equipo de Medicamentos si se agrega como grupo; hoy medicamentos escala con su motivo.
2. En el `.env`, `RESULTS_API_URL=http://192.168.1.211:8095` e `IMAGING_RESULTS_URL` (el enlace de Lumier, que hoy está
   en n8n).
3. Cuando Néctar despliegue el lote 11, `RESULTS_PDF_ENABLED=true`.
4. En n8n, desactivar el chatbot de resultados, «Re-engagement 24h» y «SIAU - Finalizar sesión» al mover el número.
   ENVIOAUTO y GLPI siguen.

## Decisiones mías tras la revisión (el dueño puede cambiarlas)

1. **Tope de intentos persistente.**
   - Nueva tabla `results_verification_attempts` (migración 050): clave = hash SHA-256 del número de documento,
     teléfono de contacto (E.164; el `contact_identifier` en Néctar), intentos fallidos y `locked_until`.
   - Tres fallos en 24 h, por documento O por teléfono, bloquean la consulta 24 h para ese documento y para ese
     teléfono.
   - Ni «menú» ni una sesión nueva lo reinician. Una verificación correcta borra el contador de ese documento.
   - Bloqueado: «Por seguridad, la consulta está bloqueada por hoy», con el enlace al portal y la opción de SIAU.
   - El número no se guarda en claro.
2. **Mismo número con otro tipo de documento.**
   - La API busca solo por número. Tras verificar, se conservan solo los estudios cuyo `nombre` coincide con el
     paciente de SIESA: tras quitar tildes y mayúsculas, el primer nombre y el primer apellido de SIESA están entre
     las palabras del `nombre` de la API.
   - Si ninguno coincide: «no encontramos resultados» con el enlace al portal, y el evento
     `results_name_mismatch`. Nunca se envía un estudio cuyo nombre no coincide.
3. **Nombre del pie del mensaje:** el primer nombre del paciente en SIESA, no el de la API.
4. **Reintento:** tras un fallo, se vuelve a pedir desde el tipo de documento.
5. **Duración del turno.**
   - Antes de bajar el PDF se envía «Un momento, estoy preparando tu resultado… ⏳».
   - Descarga, subida y envío corren en el mismo turno con un presupuesto propio de 45 s, como el OCR con
     `defaultAnalyzeTimeout`, no con el plazo corto de los envíos.
   - `SendAttachment` sigue la política de `retrySend` y las reglas de `nectar_down` y `MarkTurnSend` de los demás
     envíos del turno.
6. **Grupo `siau`.** Se agrega como clave válida de `NECTAR_TEAM_BY_GROUP`, aparte de los grupos de CUPS, porque no
   es un grupo de procedimientos. Los equipos son de la entidad y cubren las dos líneas (requisito de operación de
   la línea vieja), así que el id es único.
7. **PDF o enlace.** Lo decide la sesión:
   - clave `nectar:` con `RESULTS_PDF_ENABLED=true` → PDF;
   - Bird → enlace.

   El lote 11 se despliega en toda la instancia de Néctar, así que una sola bandera alcanza para las dos líneas.
8. **SIAU y el tope de escalaciones.** SIAU cuenta para el tope, como pedir un asesor, y tras el corte rige la regla
   de siempre. El enlace al portal y el texto con la línea de atención siguen disponibles.
9. **Menú.** Hoy tiene 6 filas; con imágenes y SIAU quedan 8. Se actualizan:
   - `buildMainMenuList`;
   - `RegisterStaticReturnMenu(MAIN_MENU)` y su lista de opciones válidas;
   - el menú fuera de horario.
10. **Fechas de la API.** Una `fecha` vacía o ilegible se ordena al final, sin error. Estudios repetidos con el mismo
    `archivo` se muestran una sola vez.

## A criterio de quien implemente

- Nombres internos de los estados y del paquete del cliente (`internal/results`, por ejemplo).
- Cómo reutilizar el menú y la validación del tipo de documento del registro sin duplicar código.
- El formato exacto del texto de la lista, dentro de los topes de WhatsApp.
