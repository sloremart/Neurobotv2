# Transición de Bird a Néctar con dos líneas activas — diseño

Fecha: 2026-10-30. Rama: `feat/migracion-nectar` (la integración con Néctar NO se mezcla a main).
Antecede: `2026-10-27-migracion-nectar-design.md` (puente, D1–D5) y el plan del IVR para
recordatorios fallidos (`docs/superpowers/plans/2026-10-29-ivr-recordatorio-fallido.md`).

## 1. Intención y porqué

Hoy el cambio a Néctar es todo o nada (`NECTAR_BASE_URL` vacía = todo Bird; con valor = la
mensajería proactiva sale por Néctar). Mover el número de producción de Bird a la Cloud API de Meta
en Néctar tiene una duración desconocida, y Néctar no tiene historia en producción. El dueño no
quiere quedarse sin canal en ningún momento.

La salida: una **etapa de transición con dos líneas activas atendidas por el MISMO bot**:

- **Línea de producción** (el número de siempre): sigue en Bird, sin tocarse, como hoy.
- **Línea nueva** (número nuevo de la cuenta de Meta de Neuro, ya verificada): entra directo a
  Néctar por la Cloud API. Todos los recordatorios y avisos salen por ella, lo que la «fideliza»
  (calidad y tope de mensajería) más rápido.

Al cabo de un tiempo estable, el número de producción se mueve 100 % a Néctar (fuera de este diseño)
y la línea nueva queda de respaldo ya fidelizada.

Lo que el dueño quiere ganar, en sus palabras resumidas: agentes atendiendo en Néctar ya, el bot
funcionando sobre Néctar en producción real antes del corte final, y no quedarse incomunicado —
si Néctar falla el primer mes, Bird es el respaldo.

## 2. Objetivos

1. Un solo proceso del bot atiende las dos líneas y responde a cada paciente por la línea por la que
   le escribió.
2. Recordatorios (día antes y mismo día), lista de espera y avisos administrativos salen TODOS por la
   línea nueva vía Néctar desde el primer día.
3. La línea de producción (Bird) anuncia la línea nueva en el saludo inicial.
4. Uno o dos agentes atienden en Néctar; el resto sigue en Bird un par de semanas.
5. Aviso urgente por Telegram cuando haya que volver a Bird, y una guía manual para hacerlo.
6. El informe de agentes del dashboard muestra a los de Bird durante la transición.

## 3. No objetivos

- Mover el número de producción de Bird a Néctar (paso final, otro diseño).
- Que Néctar hable con Bird (descartado: trabajo desechable, más saltos y fallas, plantillas
  incompatibles, Bird cobrando igual).
- Vuelta automática a Bird (el dueño eligió guía manual precedida de aviso urgente).
- Repartir los envíos proactivos entre líneas por paciente o por porcentaje: todo proactivo va por la
  línea nueva.
- La lectura D5 de Néctar para el panel (otro trabajo, depende de los Planes 45/46 de Néctar).
- El IVR de voz: sigue por Bird Voice sin cambios.

## 4. Anti-objetivos (fallar aunque «funcione»)

- Que un paciente descarte la línea de producción creyendo que dejó de funcionar.
- Que un recordatorio no salga y el paciente no quede para la llamada IVR.
- Que una conversación entrada por Bird se responda por Néctar o al revés.
- Que dos procesos del bot corran los programadores (recordatorios duplicados).
- Tocar el flujo y las validaciones del bot más allá de lo que aquí se pide (regla del dueño).

## 5. Lo que ya existe (verificado en el árbol)

- **Ruteo de respuestas por conversación:** cada envío mira la clave de sesión; `nectar:<id>` sale por
  Néctar, cualquier otra por Bird (`internal/bird/client.go:804`, `nectar_bridge.go:26`). Las dos
  entradas (webhook de Bird y avisos de Néctar) llegan por rutas distintas.
- **Proactivo por Néctar:** con el puente encendido, `SendProactiveTemplate` abre la conversación con
  plantilla por `NECTAR_CHANNEL_ID` (D1). Es justo «todo proactivo por la línea nueva».
- **Recordatorio fallido → IVR:** un recordatorio del día antes que no sale deja un pending `Unsent`
  y el paciente entra a la llamada de las 13:00 (commits 2a6aa69..3a12611).
- **Plantillas por nombre:** `NECTAR_TEMPLATE_NAMES` mapea cada plantilla a su nombre en Meta.
- **Telegram:** `TELEGRAM_BOT_TOKEN`/`TELEGRAM_CHAT_ID` configurados; paquete `internal/telegram`;
  todo `slog.Error` sube a Telegram (`internal/telegram/alert_handler.go`, conectado en
  `cmd/server/main.go:101`, agrupa repetidos en 5 min), y `internal/monitor/capacity.go` ya alerta con
  enfriamiento. El aviso urgente debe distinguirse de esos mensajes y no quedar tapado por ellos.
- **Saludo:** `internal/statemachine/handlers/greeting.go` (estado `GREETING`).
- **Informe de agentes:** con el puente encendido, `GET /api/internal/agents/bird` responde
  `409 agents_in_nectar` (`internal/api/internal_handler.go:489`, `SetAgentsInNectar` en
  `cmd/server/main.go:518`) y el dashboard deja de mostrar a los de Bird.

## 6. Decisiones del dueño (tal cual)

1. **Un solo bot, dos líneas** (no dos procesos).
2. **Todos los recordatorios y avisos salen por la línea nueva** (Néctar), desde el inicio.
3. **Plantillas con anuncio:** cada plantilla de la línea nueva dice que el paciente nos escribe desde
   un número nuevo, que por él también se puede agendar y que ambas líneas siguen activas.
   Recomendación aceptada en la conversación: NO con una variable que agregue la frase (Meta la
   rechaza o la recategoriza a marketing), sino **dos versiones de cada plantilla** (con anuncio y
   normal) y se cambia el nombre en `NECTAR_TEMPLATE_NAMES` cuando se quiera quitar el anuncio.
   Configuración, sin código.
4. **Saludo de la línea de Bird actualizado** — SOLO en conversaciones de Bird, nunca en las de
   Néctar. Debe decir, muy claro: que existe la línea nueva, que los recordatorios llegarán desde
   ella, que también se puede agendar por ella, y que **la línea de siempre sigue funcionando
   normalmente para agendar** (para que no la descarten). Ambas líneas siguen atendiendo.
5. **Agentes:** uno o dos en Néctar, el resto en Bird un par de semanas.
6. **Vuelta a Bird:** guía manual, precedida de un **aviso urgente por Telegram** al chat ya
   configurado en el bot, para ejecutarla de inmediato.
7. **Tope de Meta:** el dueño cree que la cuenta está verificada (1 000 conversaciones/día iniciadas
   por la empresa). Se confirma en el administrador de WhatsApp antes del arranque.
8. **Difusión:** además del saludo, el número nuevo se publica a los pacientes en redes.
9. **Paciente activo en Bird cuando sale su recordatorio por Néctar** (2026-10-30): se acepta. El bot
   no cruza las líneas por paciente; puede quedar con dos conversaciones en paralelo, cada una
   funciona por su lado. No se toca el flujo de recordatorios para evitarlo.
10. **Dónde sale el anuncio** (2026-10-30): en el saludo normal (`GREETING`) Y en el saludo de fuera de
    horario (`OUT_OF_HOURS`, `greeting.go:205`), solo en conversaciones de Bird. NO al paciente que
    vuelve tras confirmar (`post_confirmed=1`, que omite la bienvenida).
11. **Exceso sobre el tope diario de Meta** (2026-10-30): se acepta que caiga al IVR de las 13:00; no
    se construye desvío a Bird. Antes del arranque el dueño confirma el tope real (1 000/día) en el
    administrador de WhatsApp.

## 7. El cómo

### 7.1 Saludo con anuncio (bot, flujo de Bird)

- En `GREETING` y en `OUT_OF_HOURS` (decisión 10), si la sesión NO es de Néctar (la clave no empieza
  por `nectar:`), se antepone o agrega el texto del anuncio al saludo. En sesiones `nectar:*` no
  cambia nada; con `post_confirmed=1` tampoco (no hay bienvenida).
- El texto vive en configuración (p. ej. `NEW_LINE_ANNOUNCEMENT`): vacío = sin anuncio, igual que
  hoy. Así se enciende el día del arranque y se apaga sin desplegar.
- Borrador del texto (el dueño lo ajusta; el número real va en la configuración):
  > 📢 Tenemos una nueva línea de WhatsApp: **<número nuevo>**. Desde ella te llegarán los
  > recordatorios de tus citas y también puedes agendar por allí. **Esta línea sigue funcionando
  > normalmente**: puedes seguir agendando aquí como siempre. Las dos líneas te atienden.
- Frecuencia: cada vez que se muestra el saludo de la línea de Bird (el saludo ya es por conversación
  nueva). [Decisión del constructor si conviene limitarlo; por defecto: siempre que sale el saludo.]

### 7.2 Proactivo y plantillas (configuración)

- Encender el puente (`NECTAR_BASE_URL` y demás) con `NECTAR_CHANNEL_ID` = canal de la línea nueva.
- `NECTAR_TEMPLATE_NAMES` apunta a las versiones «con anuncio».
- Plantillas en la WABA de la línea nueva: confirmación, reprogramación, cancelación admin y lista de
  espera, cada una en dos versiones, con los títulos de botón exactos de
  `internal/notifications/template_buttons.go`.

### 7.3 Escalación y agentes

- Una conversación de Néctar escala a Néctar (ya construido, ruteo por grupo con
  `NECTAR_TEAM_BY_GROUP`). Con uno o dos agentes, esos agentes pertenecen a los tres equipos de
  Néctar (propuesta del controlador; configuración en Néctar, no código).
- Una conversación de Bird escala a Bird como hoy, **con el puente encendido**. El constructor
  verifica que ningún camino de escalación, retorno, cierre o nota interna de una sesión de Bird
  pase por Néctar cuando el puente está encendido, y lo fija con pruebas.
- `NECTAR_SCHEDULE_ENABLED` sigue en `false` durante la transición: verificado, cambia `cfg.Hours()`,
  que es global, y mandaría también sobre las conversaciones de Bird.

### 7.4 Informe de agentes en transición

- `GET /api/internal/agents/bird` deja de responder `409 agents_in_nectar` solo por tener el puente
  encendido: mientras haya agentes en Bird, devuelve el roster de Bird. Se gobierna con una variable
  (p. ej. `AGENTS_SOURCE=bird|nectar`, por defecto `bird`) en vez de deducirlo del puente. Los
  agentes de Néctar se ven en Néctar hasta que exista D5.
- El dashboard ya sabe leer el 409; no cambia.

### 7.5 Aviso urgente y guía de vuelta

**Disparadores** (cada uno con enfriamiento para no inundar; se repite cada pocos minutos mientras
persista):

1. Néctar no responde: fallas de transporte o 5xx seguidas en los envíos o en las lecturas del puente
   en una ventana corta.
2. La línea nueva sin cupo o castigada por Meta. Decisión del controlador: no se pide nada nuevo a
   Néctar ni se cambia cómo la apertura clasifica `502 meta_rejected` (hoy `ErrTemplateRejected`, de
   configuración, que detiene la corrida; descarta el `meta_code`). Los rechazos por límite/calidad de
   Meta suelen llegar después, en estados de entrega que Néctar no avisa al bot (deuda D5-1). Por
   eso el disparador 2 es: corrida detenida por configuración (`LogProactiveConfigStop`) o tasa de
   recordatorios en duda/no enviados sobre el umbral (disparador 4). Si Néctar llegara a exponer el
   `meta_code`, separar «rechazo de un paciente» de «rechazo de la cuenta» es un trabajo aparte.
3. Escalaciones a Néctar fallando seguidas.
4. Recordatorios en duda o no enviados por encima de un umbral en una corrida.

[Umbrales y ventanas: decisión del constructor, documentados y configurables.]

**Forma:** mensaje propio a Telegram, distinguible de los errores comunes: «🚨 URGENTE — volver a
Bird», el disparador, desde cuándo, cuántos fallos, y la ruta de la guía.

**Guía** (`docs/GUIA-VOLVER-A-BIRD.md`), paso a paso para alguien del equipo:
1. Vaciar `NECTAR_BASE_URL` en el `.env` del servidor y reiniciar el bot (todo vuelve a Bird:
   proactivo por plantilla de Bird, informe de agentes de Bird).
2. Verificar en `/health` que `nectar_host` está vacío y que un recordatorio de prueba sale por Bird.
3. Qué pasa con las conversaciones abiertas en Néctar (pacientes de la línea nueva): el bot ya no las
   atiende; los agentes de Néctar las cierran a mano o las dejan, y el paciente escribe a la línea de
   siempre. Incluir el texto que el agente le envía al paciente.
4. Los pendings de confirmación con clave `nectar:*` siguen yendo al IVR (marcan al `ContactPhone`).
   [El constructor lo verifica con el puente apagado.]
5. Cómo volver a Néctar cuando se resuelva.

## 8. Restricciones

- Todo en `feat/migracion-nectar`; sin merge a main ni push sin el dueño.
- No se toca la lógica del flujo ni sus validaciones, salvo el saludo de la línea de Bird (decisión 4).
- Lo que se pida a Néctar es aditivo y no rompe a hdg-bot. Este diseño no pide nada nuevo a Néctar.
- Secretos y número nuevo los teclea el dueño; nunca en el repo.
- Identificadores en inglés, comentarios en español.
- Nada se reinicia contra Néctar hasta que el dueño confirme la cuenta (número nuevo vinculado,
  alta en Néctar, plantillas aprobadas).

## 9. Lo que queda al constructor

- Nombres exactos de las variables nuevas y su validación en `config`.
- Umbrales, ventanas y enfriamiento del aviso urgente, y en qué paquete vive el detector.
- La frecuencia del anuncio en el saludo, si hubiera razón para limitarla.
- Las verificaciones marcadas en 7.3 y 7.5.

## 10. Orden sugerido

1. Saludo con anuncio (configurable, apagado por defecto).
2. Informe de agentes desacoplado del puente.
3. Verificación de escalación de Bird con el puente encendido (+ arreglo si hiciera falta).
4. Aviso urgente + guía de vuelta.
5. Documentación (CLAUDE.md NÉCTAR, `.env.example`).
6. Arranque (dueño): número nuevo vinculado, plantillas aprobadas, alta en Néctar.
   **Prueba previa** (decisión del controlador): en el bot LOCAL por el túnel, como en el diseño del
   27-oct, con `TESTING_WHITELIST_PHONES` y la réplica local; el webhook de Bird de producción NO
   apunta al local. NO se prueba con lista blanca en producción: la lista blanca es global (Bird,
   Néctar, recordatorios, IVR, lista de espera) y dejaría muda la línea de producción. Luego `.env`
   de producción y encendido real.
