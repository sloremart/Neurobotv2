# Informes de asesores con Néctar (D5) — diseño

Fecha: 2026-10-30. Rama: `feat/migracion-nectar` en **los dos** repos (Neurobotv2 y neuro-dashboard). Nunca se
mezcla a `main` ni se empuja sin el dueño.

## 1. Intención

Durante la transición hay dos líneas atendiendo a la vez: la de producción por Bird y la nueva por Néctar
(`docs/superpowers/specs/2026-10-30-transicion-doble-canal-design.md`). Al dueño le importan, por encima de todo,
dos cifras del dashboard:

- **Contestaciones**: cuando el bot pasa un chat a un asesor, si el asesor contestó y cuánto tardó.
- **Chats abandonados**: los que nadie atendió, o cuyo paciente se fue.

Hoy los chats de Néctar no rompen el dashboard, pero lo hacen **mentir**: salen «nunca atendidos», sin asesor, con
tiempo de respuesta vacío, desaparecen de los informes por asesor, y «el asesor no contestó» no existe para ellos. Y
peor (verificado en el código): cuando en Néctar la espera **vence sin que nadie tome el chat**, el bot registra la
escalación como **`completed`** (`internal/worker/queue_expired.go` → `SessionManager.Complete` →
`EscalationRepo.Close`), o sea, como atendida y resuelta.

Néctar 46a ya entrega lo que falta. Está en producción desde 2026-10-30 22:18 UTC (master `cde233eb`), según lo
confirmó la sesión de Néctar ese día; el contrato aún dice «pendientes de ventana» porque no se actualizó tras el
despliegue. Contrato completo: `C:\laragon\www\colibrixa-web\nectar\docs\PARA-EL-EQUIPO-DEL-BOT.md`, enmiendas de
los Planes 45, 46 y 43 (al final).

## 2. Decisiones del dueño

1. Las tres partes: el bot guarda lo de Néctar, el dashboard lo muestra, y el informe de asesores deja de quedar
   vacío con agentes en Néctar.
2. **Separado por línea (Bird / Néctar) para comparar, solo mientras exista Bird**; después, una sola cifra.
3. Refutar todo; código limpio; solo integración (no se toca la lógica del flujo del paciente).

## 3. Metas, no-metas, anti-metas

**Metas**
- Contestaciones y abandonos de la línea Néctar medidos con **el mismo significado** que los de Bird.
- Informe por asesor con los asesores de Néctar (nombre, presencia, chats abiertos, sus cifras).
- Separación por línea automática y que desaparezca sola cuando Bird no tenga datos en el período.
- Las cifras de Néctar dicen **desde cuándo cuentan**.

**No-metas**
- Llamadas por asesor (Bird Voice): siguen siendo solo de Bird; en la columna Néctar, «no aplica».
- La plantilla no entregada (D5-1 de Néctar) — no está en estas lecturas.
- Quién escaló cada tramo dentro de waiting-cycles (deuda P43-1 de Néctar).
- Rehacer el histórico anterior a la migración 144 de Néctar (no se puede: viene en `null`).

**Anti-metas (fracaso aunque funcione)**
- Que una cifra de Néctar y una de Bird se llamen igual pero midan distinto.
- Que la sincronización cambie algo que el paciente ve o recibe.
- Que con el puente apagado o sin clave de cuenta el bot falle o haga ruido (debe quedarse quieto).
- Que lo pedido a Néctar no sea aditivo (aquí no se pide nada nuevo: todo ya existe).

## 4. Lo que hay hoy (verificado)

- Fuente de verdad del SLA: tabla local `escalations` (migración 031, + `contact_identifier` en 044): una fila por
  escalación con `agent_id`, `agent_name`, `escalated_at`, `first_agent_msg_at`, `last_agent_msg_at`, `outcome` ∈
  {`returned`, `completed`, `expired`, `agent_no_show`, NULL=abierta}. En Néctar `phone_number` = `nectar:<id>`.
- Las marcas del agente solo las sella el webhook saliente de Bird (`TouchAgentActivity` → `EscalationRepo.TouchAgent`).
  En Néctar no hay señal por mensaje: el aviso resuelta/devuelta solo cierra (`Close`) o devuelve (`Resume`).
- `agent_no_show` lo pone el barrido de recordatorios de Bird; Néctar no lo tiene.
- Dashboard (`neuro-dashboard/internal/kpi/repository.go`):
  - SLA total `GetEscalationSLA`: atendidas = `last_agent_msg_at IS NOT NULL`; TTFR =
    `escalated_at → COALESCE(first_agent_msg_at, last_agent_msg_at)`; tramos <5/5-14/15-59/≥60.
  - Por asesor `GetEscalationSLAByAgent`, `GetAgentWindow`: `WHERE agent_id <> ''`.
  - «Mensajes sin respuesta»: `chat_events.msg_during_escalation` entre `escalated_at` y `first_agent_msg_at`.
  - Abandonos: `sessions.status='abandoned'` (donut: escalación expirada vs abandono puro), «pérdida real»
    (`flow_events`), y en escalaciones `expired` (se fue el paciente) vs `agent_no_show` (nunca contestó).
  - Roster: `GET /api/internal/agents/bird` del bot; con `AGENTS_SOURCE=nectar` responde 409 `agents_in_nectar` y
    el informe lo dice (commit `ba1db05` de la rama del dashboard).
  - Llamadas por asesor: `/api/internal/agents/bird-calls` (Bird).
- El bot **no** guarda copia del esquema de avisos de Néctar (el md5 que cambió en el Plan 43 no le afecta) y lee
  los avisos sin `DisallowUnknownFields`.

## 5. Qué se construye

### 5.1 Bot — sincronización de Néctar

Una tarea periódica del bot (con el puente encendido) que lee de Néctar:

- `GET /api/bot/waiting-cycles` siguiendo **al pie de la letra** la receta §3(a) del contrato (Plan 45): `since` =
  la más antigua entre la `opened_at` del tramo abierto más viejo guardado y la última hora vista menos ~10 min;
  paginar con `next_cursor` hasta `null`; **upsert por `id`**; ventanas de ≤62 días; `?n=<uuid>` irrepetible por
  petición (si no, `401 replay`).
- `GET /api/bot/agents` (roster completo, activos e inactivos).

Se guardan en tablas locales nuevas (migración 046), tal como llegan: los tramos (todos los campos, incluido
`contact_messages_before_first_response`) y los asesores (id, nombre, activo, presencia, equipos, chats abiertos,
hora de lectura). Son la materia prima; las cifras salen de ahí y de `escalations`.

Primera lectura: desde el `history_since` que devuelve Néctar o desde la primera escalación `nectar:%` del bot, lo
que sea más reciente. Período configurable con una variable (por defecto 5 min). Sin puente, o con
`403 account_key_required` (clave global), la tarea no corre / se detiene con **un** aviso claro en el log, sin
alerta repetida.

### 5.2 Bot — completar `escalations` de la línea Néctar

Cada escalación `nectar:<id>` se empareja con su tramo *(decisión de diseño, no del dueño)*: de la misma
conversación, el **primer** tramo con `opened_at` ≥ `escalated_at` menos una tolerancia de reloj de 2 minutos, y
anterior a la siguiente escalación de esa conversación en el bot. «El primero después» y no «el más cercano»: cubre
la escalación que Néctar abre tarde (p. ej. la encolada para el día siguiente) sin robarle el tramo a la atención
siguiente. Una escalación sin tramo emparejable se deja como está (sin datos del asesor) y el dashboard la cuenta
aparte como «sin dato de Néctar», nunca como «nunca atendida».

La atención es el tramo emparejado **más** los que le siguen por `previous_cycle_id` (traspasos), **cortando** en el
primer tramo que empareje con otra escalación del bot (devolución y re-escalado son dos atenciones, como en las dos
filas de `escalations`). Con eso se llenan las columnas que hoy llena Bird:

- `first_agent_msg_at` = la primera `first_response_at` no nula de esa atención; `last_agent_msg_at` también se llena
  (el dashboard cuenta «atendida» por esa columna).
- `agent_id` / `agent_name` = el asesor que respondió primero (si nadie respondió, el último asignado), con el
  `agent_id` prefijado para no chocar con los de Bird (p. ej. `nectar:31`) y el nombre del roster.
- Mensajes antes de la primera respuesta = `contact_messages_before_first_response` (columna nueva en
  `escalations`), que es el equivalente de Néctar a «mensajes sin respuesta».
- Si una escalación ya tiene sus valores, la sincronización solo los mejora (de nulo a valor), nunca los borra.

**Llenar `first_agent_msg_at` no debe cambiar nada que vea el paciente.** Hoy esa columna la lee también el acuse
«🙋 Seguimos aquí» (`EscalationRepo.HasAgentResponded`, `repository/local/escalation_repo.go`, invocado desde
`worker/pool.go`). Hay que comprobar si en Néctar ese camino recibe
mensajes del paciente durante la escalación; si los recibe, el único efecto posible es no mandar el acuse cuando el
asesor ya respondió, que es lo correcto, pero debe quedar comprobado y escrito.

### 5.3 Bot — «el asesor no contestó» en Néctar

La cola vencida (`cola_vencida`, `handleQueueExpired`) deja la escalación en **`agent_no_show`**, no en
`completed`. Es la misma clase de hecho que el no-show de Bird: nadie tomó al paciente a tiempo. Cambia solo la
marca de la escalación; el aviso al paciente y el cierre siguen igual. (Cuando la marca local falte o llegue tarde,
el tramo sincronizado con `close_reason = expired` y sin `first_response_at` sirve de respaldo.)

**El cierre por silencio de Néctar** (`DefaultNectarCloseMin`, 8 h, `session/manager.go` → `closeEscalatedBySilence`)
hoy marca `expired` («se fue el paciente») haya contestado un asesor o no. En Bird, `expired` exige que el asesor
haya contestado; si no, es `agent_no_show`. Para que signifiquen lo mismo *(decisión de diseño, sale de la anti-meta
«mismo nombre, mismo significado»)*: una escalación de Néctar cerrada por silencio **sin** primera respuesta del
asesor queda `agent_no_show`; **con** respuesta queda `expired`, como en Bird. Como la primera respuesta llega por
la sincronización y puede llegar después del cierre, la sincronización es la que corrige `expired` → `agent_no_show`
cuando el tramo de esa atención terminó sin `first_response_at` (y nunca al revés sin evidencia). El barrido y lo
que recibe el paciente no cambian.

### 5.4 Bot — quién cerró y quién escaló

El aviso `conversacion_resuelta` trae `actor` (siempre `{"clase":"agente","agent_id":N}` hoy) y `escalada_por`
(`agente` con id, `bot`, o ausente = **no consta**). Se guardan en la escalación que se cierra (columnas nuevas).
Ausente se guarda como desconocido, nunca como «nadie». Recordar la deuda P43-2: tras reabrir no viaja.

### 5.5 Bot — roster de Néctar para el dashboard

Endpoint interno nuevo, hermano de `/api/internal/agents/bird`, que sirve el roster de Néctar desde la tabla local
(con la hora de la última lectura). Con dos líneas, el dashboard pide los dos. `AGENTS_SOURCE` conserva su sentido:
`nectar` = Bird ya no tiene asesores (su endpoint sigue con 409).

### 5.6 Dashboard

- **Separación por línea**: toda cifra de contestaciones, abandonos y por asesor se calcula por línea
  (`phone_number LIKE 'nectar:%'` = Néctar). Si el período tiene datos de **las dos** líneas se muestran dos
  columnas «Línea Bird» / «Línea Néctar»; si solo de una, una cifra. Sin interruptor: cuando Bird se apague,
  desaparece sola.
- **Mismas fórmulas** en las dos líneas (TTFR, % atendidas, tramos, `expired` vs `agent_no_show`). «Mensajes sin
  respuesta» usa en Néctar la columna nueva y en Bird lo de siempre.
- **Informe por asesor**: asesores de las dos plataformas; los de Néctar con nombre, presencia y chats abiertos del
  roster sincronizado. Las cifras (contestaciones, abandonos) cuentan **solo las atenciones escaladas por el bot**
  (emparejadas con `escalations`), que es lo que se compara con Bird *(decisión de diseño: el dueño pidió
  «contestaciones al bot»)*. Los demás tramos de Néctar (rescates del supervisor, reaperturas `resolved → waiting`,
  aperturas con plantilla para el asesor) se muestran como un conteo informativo aparte, «otros chats atendidos en
  Néctar», sin mezclarlos en las cifras comparables.
- **Llamadas por asesor**: columna Néctar = «no aplica», nunca 0.
- **Desde cuándo**: las cifras de Néctar indican desde cuándo valen (2026-10-30 22:18 UTC o el `history_since`), y
  la hora de la última sincronización.

## 6. Restricciones

- Todo aditivo para Bird: con el puente apagado, el bot y el dashboard se comportan exactamente como hoy.
- Las dos rutas de Néctar exigen la **clave de cuenta** (la global da 403). Neuro tiene una sola cuenta.
- Las rutas no traen datos del paciente; el bot tampoco debe añadirlos a las tablas nuevas ni a los logs.
- Las tablas nuevas y columnas nuevas, en una migración (046) con su `down`; `ADD COLUMN NULL` en `escalations`.
- Pruebas en Docker (`golang:1.26-alpine`); línea base: exactamente las 3 fallas conocidas del bot.
- No hay tráfico real de Neuro en Néctar (falta la WABA): todo se prueba con respuestas simuladas del contrato.
- Reglas de siempre: nada de `--no-verify`, no se suben archivos ajenos (`auditoria/`, `bin/`, `web/` del
  dashboard, etc.), ningún secreto real.

## 7. Lo que queda al constructor

- Nombres de tablas, columnas, variable de periodicidad y endpoint, siguiendo los del repo.
- Dónde vive la tarea (scheduler existente) y cómo se reparte el upsert y el emparejamiento.
- La forma exacta en que el dashboard pinta las dos columnas, siguiendo sus componentes actuales.
- Cómo se cubre el respaldo de §5.3 sin duplicar la marca local.
