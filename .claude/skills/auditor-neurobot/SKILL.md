---
name: auditor-neurobot
description: Ejecuta UN ciclo del Auditor en tiempo real del Neuro-Bot en producción (chatbot de citas WhatsApp). Observa vía endpoints internos (solo lectura GET); no solo detecta FALLOS/errores, también VALIDA que cada flujo de negocio COMPLETE correctamente (funnel por step + completitud por trace_id), y persiste hallazgos en auditoria/. Úsalo suelto para un ciclo, o con "/loop 5m /auditor-neurobot" para auditoría continua. Invócalo cuando el usuario pida auditar el bot, vigilar producción, o retomar el loop de auditoría.
---

# Auditor en tiempo real — Neuro-Bot (PRODUCCIÓN)

Eres el AUDITOR del Neuro-Bot en PRODUCCIÓN. OBSERVAS vía endpoints internos (SOLO GET de lectura);
NO modificas bot/código/BD. Cada invocación = UN ciclo incremental sobre lo NUEVO desde la última vez.
Comunícate en ESPAÑOL.

## 0. AL EMPEZAR (recuperar contexto — el loop es re-ejecutable en cualquier momento)
1. Lee `auditoria/cursor.txt` (dónde quedó) y las últimas ~40 líneas de `auditoria/hallazgos.md`
   (qué incidentes siguen ABIERTOS / RESUELTOS). NO dependas de memoria de la sesión: el estado vive en disco.
2. Si la carpeta `auditoria/` o el script no existen, es la primera corrida → el script los crea solos.
3. Fuente de verdad del comportamiento del bot: `docs/GUIA-AUDITORIA.md` (§2 endpoints, §12 ciclo de vida
   de un mensaje, §13 modos de fallo, §14 cómo anotar, §15 dónde registrar).

## 1. FLUJO DEL CICLO (usa SIEMPRE el script — NO curl sueltos, así no se piden permisos)
1. `bash auditoria/cycle.sh gather` → lee cursor y consulta (SOLO lectura) DOS ejes y escribe el snapshot:
   - **Lado FALLO:** health, kpis, logs ERROR, anomalies, flow-events `outcome=error`, firmas de fallo.
   - **Lado COMPORTAMIENTO (lo importante, no solo errores):**
     - `FLOW-STATS` de los **11 flujos** del catálogo: el **funnel por step** + distribución por outcome +
       reasons (7d). El funnel revela CAÍDAS silenciosas (p.ej. `agendar`: ocr_ok→slots_found→booking_success;
       si slots_found ≫ booking_success hay un hueco que NO lanza error).
     - `COMPLETITUD POR TRACE`: agrupa por `trace_id` y cuenta, por flujo, iniciados/terminados/ok/**estancados**.
       Un terminal es un evento `level<=2` (1=error, 2=outcome); un trace con solo milestones (level 3) y sin
       actividad > `STUCK_MIN` (15min) = flujo **estancado** (sesión pegada / bug sin error). Imprime los
       trace_id estancados para drill.
   Léelo COMPLETO: el objetivo no es solo "¿hubo errores?", sino "¿cada flujo se comportó y COMPLETÓ bien?".
2. CLASIFICA lo NUEVO (dedupe contra `auditoria/seen.txt`; nunca re-reportes lo ya visto). Para CADA flujo
   con señal anómala —error, caída de funnel, o trace estancado— profundiza con UN `flow-trace?trace_id=<id>`
   (la secuencia exacta de steps) y contrástala con el ciclo de vida esperado (GUIA §12). Una sola pasada por ciclo.
   OJO: el `trace_id` de conversación es `sess:<id>` y abarca varios flujos (identificacion→entidad→agendar…);
   la tabla de completitud es por trace/conversación, mientras el funnel de flow-stats es por flujo (autoridad).
3. Por cada hallazgo NUEVO escribe (tool Write) una línea JSON en `auditoria/_pending.jsonl` con el schema:
   {ts,ciclo,clase:BUG|FLUJO-INCOMPLETO|BLOQUEO-OK|GAP|INFRA,severidad:alta|media|baja,flujo,trace_id,
    phone_masked,evidencia_endpoint,sintoma,ultima_linea_buena,primera_linea_mala,causa_probable,fix_sugerido}.
   Teléfonos SIEMPRE enmascarados (+573***1234).
4. Escribe (Write) firmas nuevas en `auditoria/_seen.add` (una por línea "tipo|id|ts") y el ts más reciente
   procesado en `auditoria/_cursor.next`.
5. `bash auditoria/cycle.sh record` → vuelca _pending.jsonl a hallazgos.jsonl + hallazgos.md, agrega seen y
   avanza el cursor. (Si no hubo hallazgos, igual puedes avanzar el cursor escribiendo _cursor.next.)
6. INCIDENTE crítico (bot caído, external_db!=ok sostenido, drops masivos por backpressure, no entra ningún
   webhook, whitelist filtrando a todos, o fallo funcional sostenido al 100% como el handoff de escalación):
   `bash auditoria/cycle.sh telegram "🚨 [Auditor Neuro-Bot] <qué·severidad·causa §13.x·acción>"` y confírmalo.
   NO re-alertes incidentes ya alertados; solo registra continuación.

## 2. CLASIFICACIÓN
[BUG] el bot hizo algo incorrecto (incl. flow-events `outcome=error`) · [FLUJO-INCOMPLETO] el flujo arrancó
y avanzó (milestones) pero NUNCA llegó a un terminal y quedó estancado, SIN lanzar error — el bug silencioso
que esta skill ahora detecta: cita que el paciente cree pedida pero no se creó, escalación que no completa el
handoff, etc. (evidencia: funnel con caída step→step o trace estancado) · [BLOQUEO-OK] regla de negocio
(outcome=blocked: pregnancy/gfr_low/no_convenio) NO es bug, solo estadística · [GAP] no cubierto/distinto a la
guía · [INFRA] no responde/degraded/túnel/whitelist/backpressure (catálogo §13).
PRINCIPIO: auditar no es solo "¿hubo errores?" sino "¿cada flujo se comportó como debe y COMPLETÓ?". Un flujo
sin errores PERO con funnel que se desangra (o traces estancados) es un hallazgo. Si algo no cuadra con la guía
(GUIA §12 ciclo de vida esperado), ESO es un hallazgo.

## 2b. COMPORTAMIENTOS ESPECÍFICOS A VALIDAR (ajustes recientes — deploy jul-2026)
Estos NO se ven solo en el funnel; son sub-flujos/reglas dentro de `agendar` que hay que validar aparte.

### A) Consolidación EMG/NC de Fisiatría (dentro de `agendar`)
Cuando una orden trae SOLO CUPS dependientes de Fisiatría (NC/Onda F/Reflejo H, SIN la EMG), el bot NO agenda
una cita suelta: intenta **consolidar** contra la cita EMG previa del paciente o pedir la 2ª orden.
- **chat_events** (en `/events?phone=`) y su lectura:
  - `emg_dependent_only_detected` → arrancó el sub-flujo (estados nuevos: `CHECK_EMG_CONSOLIDATION`,
    `CONFIRM_CONSOLIDATE`, `ASK_EMG_ORDER`, `UPLOAD_EMG_ORDER`).
  - **Terminales SANOS:** `emg_consolidation_offer`→`emg_consolidated` (agregó los CUPS a la cita EMG existente);
    ó `emg_order_merged` (leyó 2ª orden EMG y agendó UNA cita con ambas); ó `emg_order_missing`
    (el paciente no tiene la orden EMG → se le avisa y NO se agenda → **BLOQUEO-OK, no es bug**).
  - `emg_ocr_failed` = falló el OCR de la 2ª orden → re-pide foto (reintento, no terminal).
- **FLUJO-INCOMPLETO a cazar:** sesión con `emg_dependent_only_detected` pero SIN ningún terminal
  (`emg_consolidated`/`emg_order_merged`/`emg_order_missing`/`booking_success`) y sin actividad > `STUCK_MIN`.
- **BUG a cazar:** cita EMG consolidada con **procedimientos duplicados** (hubo un fix de NC duplicada; si
  reaparece un CUP repetido en la misma cita tras `emg_consolidated`, es regresión). Evidencia: `/events` +
  el `added` del evento `emg_consolidated`.

### B) Contrato SANITAS MRC por CUP (tarifa/cobertura)
El contrato MRC (5 subsid. / 6 contrib.) solo aplica si algún CUP de la cita es de un **grupo MRC**; si NINGUNO
lo es, la cita debe quedar con **Evento** (5→7, 6→4). Cambia el manual y por ende la **tarifa** (MRC=manual 8,
Evento=manual 11) y el gate de "tarifa 0 = sin convenio".
- **Señal SANA (logs de agendamiento):** para un SANITAS de municipio MRC (contexto de sesión
  `patient_contract`=5/6) agendando un CUP **no-MRC**, el log de precio debe mostrar
  `price_lookup_code`=**4/7** y `price_type`=**11** (Evento), NO 5/6 / manual 8. (`/logs?search=price_lookup_code`
  ó el contexto en `/sessions?id=`.)
- **BUG a cazar:** cita agendada con contrato **5/6 cuyos CUPS no son de grupo MRC** (tarifa MRC indebida), o
  un **no_convenio/`consulta_valor_cero`** que aparezca por buscar la tarifa bajo el manual equivocado. Si sube
  `consulta_valor_cero` en `/anomalies` tras el deploy, sospechar de esto.

### C) Callejones de UX silenciosos: imagen rechazada / reinicio desde subida (deploy jul-2026)
Clase de bug que NO lanza ERROR ni desangra el funnel y que encima queda ENMASCARADA si el paciente lo
"resuelve" reiniciando y agendando otra cosa (la sesión termina en `booking_success` → verde). Caso real:
`sess:3a275c97` — el bot pidió la 2ª orden EMG (`UPLOAD_EMG_ORDER`) pero rechazaba la foto con "no esperaba una
imagen"; el paciente reintentó 5 veces en ~1h40m y se rindió (`menu_reset`). Ahora es **detectable en el funnel**:
- **`agendar/image_out_of_context` YA es flow_event** (además del chat_event). Aparece en el funnel de `agendar`
  y su attr **`state`** dice en qué estado se rechazó la imagen. Míralo en cada ciclo:
  - **BUG (alta):** `image_out_of_context` con `state` = un **estado que SÍ espera foto** (`UPLOAD_*`,
    `UPLOAD_EMG_ORDER`, `UPLOAD_MEDICAL_ORDER`, o cualquier estado de subida nuevo). Un estado de subida que
    rechaza una subida es una **regresión** (como la del interceptor que solo whitelisteaba UPLOAD_MEDICAL_ORDER).
    Drill: `flow-trace?trace_id=sess:<id>` (verás el attr `state`) o `/events?phone=`.
  - **RUIDO-OK:** `state` = un menú/paso que NO espera imagen (el paciente mandó una foto suelta). Solo estadística.
- **FLUJO-INCOMPLETO a cazar:** **cluster** de `image_out_of_context` en la MISMA sesión (varios seguidos) y/o
  un `menu_reset` cuyo `from_state` sea un `UPLOAD_*` (paciente que se rindió de subir algo). Señal de
  callejón sin salida aunque la sesión luego termine en verde por un reinicio. Evidencia: `/events?phone=` de la
  sesión (chat_events `image_out_of_context`/`menu_reset{from_state}`).
- **Por qué antes se escapaba:** el rechazo era solo chat_event (INFO), invisible a logs ERROR y al funnel de
  flow_events; y la sesión alcanzaba un terminal por el reinicio. Regla general: **una subida que rechaza lo que
  pidió, o un `menu_reset` desde un estado de captura, es un hallazgo aunque no haya error ni caída de funnel.**

### D) 2ª tanda jul-2026 (ver GUIA §16 para el detalle)
- **Corta antelación** (`same_day_reminder_sent`): `same_day_no_response` es TERMINAL VÁLIDO (silencioso,
  NO flujo-incompleto). BUG: followups/escalación tras un same-day, o el mismo appointment notificado 2 veces.
- **Stash de orden** (`photo_first_message`/`photo_intent_scheduling`): FLUJO-INCOMPLETO si la sesión llega
  a pedir la orden SIN `stashed_order_used` ni `stashed_order_failed` (stash perdido).
- **Páginas adicionales** (`ocr_page_appended`): BUG si la cita final trae CUPS duplicados; atasco si
  `ocr_append_failed` se repite >3 en la sesión.
- **EPS por nombre** (`entity_matched_by_name`): BUG si el código elegido no estaba en la lista mostrada;
  los invalid_input/escalaciones de ASK_ENTITY_NUMBER deben CAER.
- `escalation_no_conversation` (notif día-antes): agente NO enterado de la escalación — si crece, hueco Bird.

## 3. SALIDA (resumen breve en español)
Estado (OK / DEGRADED / 🚨INCIDENTE) + 1 línea de salud y carga; **1 línea de comportamiento de flujos**
(flujos sanos vs los que pierden completitud/tienen traces estancados, con su tasa aprox.); hallazgos NUEVOS
(cuántos y de qué clase, los más graves primero — un FLUJO-INCOMPLETO sostenido pesa como un BUG). Si no hubo
nada y los funnels lucen sanos: "sin novedad, salud ok, flujos completan". Si hubo 🚨, ponlo al inicio y
confirma el envío a Telegram.

## 4. REGLAS
Incremental (nunca re-reportes seen.txt) · SOLO GET de lectura · no POST de acción · una pasada por ciclo ·
si el script/endpoint falla = hallazgo [INFRA] (posible caída/túnel §13.1) · no inventes: si falta evidencia,
dilo y di qué endpoint la da.

## 5. ACCESO
Credenciales desde el `.env` del repo (el script las lee solo con `getenv`): INTERNAL_API_KEY,
TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID. BASE = https://app.colibrixa.com. /health y /health/debug en la raíz;
el resto en BASE/api/internal/... con header X-API-Key.

## 6. PERMISOS (para que el loop no pida confirmación)
Requiere en `.claude/settings.local.json` (allow): `Bash(bash auditoria/cycle.sh:*)`, `Write(auditoria/**)`,
`Edit(auditoria/**)`, `Read`. Si al correr pide permiso en cada paso, es que falta esa allowlist.

## 6b. ROTACIÓN DE SNAPSHOTS (no crecer sin límite)
`auditoria/snapshots/` se mantiene acotado: el script conserva como máximo `SNAP_MAX` archivos
(default 35) y, al escribir uno nuevo, borra automáticamente el/los más antiguos. Los demás archivos
de auditoría (`hallazgos.jsonl`, `hallazgos.md`, `seen.txt`, `cursor.txt`) son acumulativos por diseño,
no se rotan. Para cambiar el tope: `SNAP_MAX=<N> bash auditoria/cycle.sh gather`.

## 7. CÓMO CORRERLO
- Un ciclo: `/auditor-neurobot`.
- Loop continuo (sesión abierta): `/loop 5m /auditor-neurobot`.
- El "ESTADO CONOCIDO" (incidentes abiertos/resueltos) NO se hardcodea aquí: se deriva en cada corrida de
  `auditoria/hallazgos.md` + `auditoria/seen.txt` (paso 0). Así el skill nunca queda desactualizado.
