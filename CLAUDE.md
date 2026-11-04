# Neuro-Bot — Contexto del proyecto

Chatbot IA para agendamiento de citas médicas vía WhatsApp (Bird API v2).
Lee órdenes médicas (imagen/PDF), identifica CUPS, busca agenda disponible y registra la cita.

---

## MIGRACIÓN DE BASE DE DATOS

| | Sistema anterior | Sistema nuevo (activo) |
|---|---|---|
| **Software clínico** | Antares | **SIESA** |
| **Base de datos** | `datosipsndx` (MySQL) | **`zeussalud_neuro`** (SQL Server) |
| **Estado** | Deprecado — ya no se usa | ✅ Activo desde migración |

> **✅ MIGRACIÓN COMPLETADA.** El paquete `internal/repository/datosipsndx/` (Antares/MySQL) fue
> **eliminado** y reemplazado por `internal/repository/siesa/` (SQL Server). Las interfaces en
> `internal/repository/interfaces.go` no cambiaron; solo la implementación. El driver MySQL
> (`go-sql-driver/mysql`) sigue en uso, pero solo para la **BD local del bot** (`neuro_bot`).

---

## Base de datos: ZeusSalud_Neuro (SIESA)

- **Servidor local (desarrollo):** `LorenaM` (SQL Server, Windows Auth)
- **Base desarrollo:** `ZeusSalud_Neuro`
- **Base de pruebas:** `ZeusSalud_Prueba` (192.168.1.207, Sa/111)
- **Servidor producción:** `192.168.1.207` (SQL Server), base `ZeusSalud_Neuro`, usuario `sa`.
  Credenciales reales en el `.env` de prod (NO aquí — este archivo se commitea).
- **Conexión sqlcmd:** `sqlcmd -S LorenaM -d ZeusSalud_Neuro -E -No`
- **Drivers Go:** `github.com/microsoft/go-mssqldb` (SIESA/SQL Server) + `go-sql-driver/mysql`
  (BD local `neuro_bot`). El bot usa AMBOS.

---

## FLUJO GENERAL DEL CHATBOT

```
1. IA lee imagen/PDF de la orden médica
2. Extrae CUPS(s) de la orden
3. Tabla externa (propia del bot): CUPS → { cod_medi, id_asunto, tipo, id_consultorio }
4. SIESA: buscar slots disponibles filtrando por Medico = cod_medi + id_consultorio
5. Paciente elige fecha/hora
6. INSERT según tipo:
   - Consulta  → citas + citas_procedimientos_asuntos + UPDATE detalle
   - Procedimiento/Imagen → citas + citas_procedimientos (1 fila/CUPS) + UPDATE detalle
```

---

## TRES TIPOS DE CITA

| Campo | CONSULTA | PROCEDIMIENTO | IMAGEN |
|-------|----------|--------------|--------|
| `asunto` | 1,7,8,9,10,11 | 13,14,15,16 | 2,3,4,5,6,12 |
| `id_sede` | **2** | **2** | **3** |
| `primera_vez_control` | 1=1ªvez / 2=ctrl | **siempre 2** | **siempre 2** |
| `tipo_servicio` en citas | NULL | código servicio | código servicio |
| Tabla CUPS | `citas_procedimientos_asuntos` | `citas_procedimientos` | `citas_procedimientos` |
| Precio al agendar | **SÍ** (en cpa.Valor) | NO (se liquida al atender) | NO |
| Slots a bloquear | 1 | 1 por CUPS | 1-N según duración |

---

## MAPA DE ASUNTOS (sis_asunto)

| id | nombre | id_sede | id_consultorio | servicio_codigo |
|----|--------|---------|---------------|-----------------|
| 1 | CONSULTA PRIMERA VEZ FISIATRIA | 2 | 18-21 | 12 |
| 2 | RX | **3** | 2 | 4 |
| 3 | TOMOGRAFÍA | **3** | 3 | 5 |
| 4 | RESONANCIA | **3** | 4 | 6 |
| 5 | MAMOGRAFIA | **3** | 5 | — |
| 6 | ECOGRAFIAS | 2 | 1,6,7,8 | 7 |
| 7 | CONSULTA DE CONTROL FISIATRIA | 2 | 18-21 | 12 |
| 8 | CONSULTA 1ª VEZ NEUROLOGIA | 2 | 9,11,12,13 | 11 |
| 9 | CONSULTA CONTROL NEUROLOGIA | 2 | 9,11,12,13 | 11 |
| 10 | CONSULTA 1ª VEZ NEUROPEDIATRIA | 2 | 29 | 13 |
| 11 | CONSULTA CONTROL NEUROPEDIATRIA | 2 | 29 | 13 |
| 12 | PET/CT | **3** | 28 | 10 |
| 13 | POLISOMNOGRAFIAS | 2 | 26 | 16 |
| 14 | ELECTROENCEFALOGRAMAS-VIDEOTELEMETRIAS | 2 | 27 | 15 |
| 15 | PROCEDIMIENTOS FISIATRIA | 2 | 22-25 | 20 |
| 16 | PROCEDIMIENTOS NEUROLOGIA | 2 | 10,14 | 18 |
| 17 | SOPORTE SEDACION | 2 | 30 | 21 |

---

## CUPS DE NEUROLOGÍA (AsuntoPctos)

| Asunto | CUPS | Descripción |
|--------|------|-------------|
| 8 | **890274** | CONSULTA 1ª VEZ ESPECIALISTA EN NEUROLOGÍA |
| 9 | **890374** | CONSULTA CONTROL ESPECIALISTA EN NEUROLOGÍA |
| 10 | **890275** | CONSULTA 1ª VEZ NEUROLOGÍA PEDIÁTRICA |
| 11 | **890375** | CONSULTA CONTROL NEUROLOGÍA PEDIÁTRICA |

> Fuente: tabla `AsuntoPctos` (NO la tabla CUPS genérica)

---

## MÉDICOS Y TÉCNICOS CONFIRMADOS

| cod_medi | Nombre | Consultorio habitual | id_consultorio | id_sede | Tipo |
|----------|--------|---------------------|---------------|---------|------|
| 3 | WILLIAM GARCIA ROSSI | RESONANCIA SEDE 02 | 4 | 3 | Imagen |
| 4 | SILVIO LOPERA FERNANDEZ | TAC SEDE 02 | 3 | 3 | Imagen |
| 11 | FABIO PEREZ CABALLERO | RX SEDE 02 | 2 | 3 | Imagen |
| 17 | SEBASTIAN POSADA BUSTOS | NEUROLOGÍA PEDIÁTRICA 01 | 29 | 2 | Consulta |
| 19 | WILLINGTON CHONA SUAREZ | FISIATRÍA (rota) | 18/23/24 | 2 | Consulta/Proced |
| 20 | ROBERTO ORTEGA VILLALBA | NEUROLOGÍA 01 CONSULTA | 9 | 2 | Consulta |
| 22 | MARIO VELASCO MARQUEZ | NEUROLOGÍA 03 CONSULTA | 12 | 2 | Consulta/Proced |
| 23 | LORENA PLAZAS RUIZ | NEUROLOGÍA 02 CONSULTA | 11 | 2 | Consulta/Proced |

> Un médico puede rotar entre consultorios — no hay asignación fija. Siempre consultar la agenda activa.

---

## QUERY: BUSCAR SLOTS DISPONIBLES

```sql
SELECT
    pmd.IdProgramacionMedico,
    pm.id_programacion,
    pm.id_sede,
    pmr.id_consultorio,
    con.nombre                              AS consultorio,
    CAST(pmd.Fecha AS DATE)                 AS fecha,
    CONVERT(VARCHAR(5), pmd.Fecha, 108)     AS hora,
    CASE WHEN DATEPART(HOUR, pmd.Fecha) < 12
         THEN 'am' ELSE 'pm' END            AS meridiano
FROM programacion_medico_detalle pmd
JOIN programacion_medico pm
    ON pm.id = pmd.IdProgramacionMedico AND pm.activo = 1
JOIN programacion_medico_relacion pmr
    ON pmr.id_programacion = pm.id
JOIN consultorios con
    ON con.id = pmr.id_consultorio
WHERE pmd.Medico          = @cod_medi
  AND pmr.id_consultorio  = @id_consultorio
  AND pmd.IdCita          IS NULL
  AND pmd.Bloqueado       = 0
  AND pmd.SinProgramacion = 0
  AND CAST(pmd.Fecha AS DATE) >= CAST(GETDATE() AS DATE)
ORDER BY pmd.Fecha
```

---

## INSERT FLUJO A — CONSULTA MÉDICA

```sql
-- 1. Insertar cita
INSERT INTO citas (
    autoid, cod_medi, fecha, hora, meridiano, estado,
    asunto, empresa, contrato, fecha_solicitud,
    id_programacion, id_sede, cod_user_asigna_cita,
    primera_vez_control, formaSolicitud, tipoUsuario,
    es_terapia, Adicional, CodGrupo, EsCitaMultiple,
    lugarAtencion, fecha_usuario_desea_cita
) VALUES (
    @autoid, @cod_medi, @fecha, @hora, @meridiano, 'P',
    @asunto, @empresa, @contrato, GETDATE(),
    @id_programacion, 2, @cod_usuario,
    @primera_vez_control,   -- 1=primera vez, 2=control
    2, @tipoUsuario,        -- tipoUsuario: '01'=contributivo, '02'=subsidiado
    0, 0, 0, 0, 0, CAST(GETDATE() AS DATE)
);
DECLARE @nueva_cita INT = SCOPE_IDENTITY();

-- 2. Obtener CUPS y tarifa
DECLARE @cups VARCHAR(20), @nom VARCHAR(300), @valor DECIMAL(18,2), @svc INT;
SELECT @cups = ap.CodProcedimiento, @nom = ap.NomProcedimiento,
       @valor = spp.Precio, @svc = ap.Servicio
FROM AsuntoPctos ap
JOIN contratos ct ON ct.codigo = CAST(@contrato AS INT)
JOIN sis_proc_precios spp
    ON spp.Cod_manual = ct.manual AND spp.Codigo_proc = ap.CodProcedimiento AND spp.Tipo_proc = '256'
WHERE ap.Asunto = @asunto;

-- 3. Registrar CUPS y tarifa
INSERT INTO citas_procedimientos_asuntos (
    IdCita, IdSisDeta, Asunto, Servicio, TipoManual,
    CodProcedimiento, NomProcedimiento, Valor, FechaRegistro
) VALUES (@nueva_cita, 0, @asunto, @svc, '256', @cups, @nom, @valor, GETDATE());

-- 4. Marcar slot ocupado
UPDATE programacion_medico_detalle SET IdCita = @nueva_cita
WHERE IdProgramacion = @id_programacion
  AND CAST(Fecha AS DATE) = @fecha
  AND CONVERT(VARCHAR(5), Fecha, 108) = @hora;
```

---

## INSERT FLUJO B — PROCEDIMIENTO O IMAGEN

```sql
-- 1. Insertar cita
INSERT INTO citas (
    autoid, cod_medi, fecha, hora, meridiano, estado,
    asunto, empresa, contrato, fecha_solicitud,
    id_programacion, id_sede, cod_user_asigna_cita,
    primera_vez_control, formaSolicitud, tipoUsuario,
    es_terapia, Adicional, CodGrupo, EsCitaMultiple,
    lugarAtencion, fecha_usuario_desea_cita, tipo_servicio
) VALUES (
    @autoid, @cod_medi, @fecha, @hora, @meridiano, 'P',
    @asunto, @empresa, @contrato, GETDATE(),
    @id_programacion, @id_sede, @cod_usuario,
    2, 2, @tipoUsuario,
    0, 0, 0, 0, 0, CAST(GETDATE() AS DATE), @codigo_servicio
);
DECLARE @nueva_cita INT = SCOPE_IDENTITY();

-- 2. Insertar CUPS (repetir por cada procedimiento)
INSERT INTO citas_procedimientos (id_procedimiento, tipo, id_cita, Servicio, Cantidad)
VALUES (@cups, '256', @nueva_cita, @codigo_servicio, 1);

-- 3. Marcar slots ocupados
UPDATE programacion_medico_detalle SET IdCita = @nueva_cita
WHERE IdProgramacion = @id_programacion
  AND CAST(Fecha AS DATE) = @fecha
  AND Fecha >= CAST(CAST(@fecha AS VARCHAR) + ' ' + @hora + ':00' AS DATETIME)
  AND Fecha < DATEADD(MINUTE, @num_procedimientos * @intervalo,
                      CAST(CAST(@fecha AS VARCHAR) + ' ' + @hora + ':00' AS DATETIME));
```

---

## TARIFAS POR CONTRATO

### Para consultas (via AsuntoPctos)
```sql
SELECT ap.CodProcedimiento, ap.NomProcedimiento, spp.Precio AS tarifa
FROM contratos ct
JOIN AsuntoPctos ap ON ap.Asunto = @asunto
JOIN sis_proc_precios spp
    ON spp.Cod_manual = ct.manual AND spp.Codigo_proc = ap.CodProcedimiento AND spp.Tipo_proc = '256'
WHERE ct.codigo = @cod_contrato
```

### Para procedimientos/imágenes
```sql
DECLARE @cups_base VARCHAR(20) = LEFT(@cups, CHARINDEX('-', @cups + '-') - 1);
SELECT TOP 1 spp.Precio
FROM contratos ct
JOIN sis_proc_precios spp
    ON spp.Cod_manual = ct.manual
    AND spp.Codigo_proc IN (@cups, @cups_base)
    AND spp.Tipo_proc = '256'
WHERE ct.codigo = @cod_contrato
ORDER BY CASE WHEN spp.Codigo_proc = @cups THEN 0 ELSE 1 END;
```

### Validar cobertura de CUPS por contrato
```sql
SELECT s.cod_proc, spp.Precio, s.requiere_autorizacion
FROM contratos ct
JOIN servicios s ON s.contrato = ct.codigo AND s.cod_proc = @cups
JOIN sis_proc_precios spp
    ON spp.Cod_manual = ct.manual AND spp.Codigo_proc = s.cod_proc AND spp.Tipo_proc = '256'
WHERE ct.codigo = @cod_contrato
-- Sin filas = CUPS no cubierto por ese contrato
```

---

## CONTRATOS PRINCIPALES

| codigo | alias | manual | regimen |
|--------|-------|--------|---------|
| 4 | SANITAS EVENTO CONTRIBUTIVO | 11 | 1 |
| 5 | SANITAS MRC SUBSIDIADO | 8 | 2 |
| 7 | SANITAS EVENTO SUBSIDIADO | 11 | 2 |
| 8 | PARTICULAR TARIFA PLENA | 32 | 4 |
| 12 | SALUD TOTAL SUBSIDIADO | 15 | 2 |
| 13 | SALUD TOTAL CONTRIBUTIVO | 15 | 1 |
| 14 | CAPITAL SALUD CONTRIBUTIVO | 34 | 1 |
| 15 | CAPITAL SALUD SUBSIDIADO | 34 | 2 |
| 21 | FOMAG | 10 | 7 |
| 22 | COLSANITAS PLAN MODULAR | 21 | 5 |
| 24 | MEDISANITAS | 29 | 5 |

> `tipoUsuario` en citas: regimen=1 → '01', regimen=2 → '02', otros → '01'

---

## SEDE

- **"SEDE 01"** en SIESA UI = **`id_sede = 2`** en BD (NO es 1)
- **"SEDE 02"** (imágenes) = **`id_sede = 3`**

---

## TABLAS PARA CREAR AGENDA (programacion_medico)

Solo necesario si el bot debe crear agendas nuevas (normalmente las crea el admin de SIESA):

1. **`programacion_medico`** — INSERT sin `id`; luego `UPDATE id_programacion = SCOPE_IDENTITY()`
2. **`programacion_medico_relacion`** — vincula agenda a consultorio (`id_sede=2`, `tipo_empresa='ENTIDADES'`)
3. **`programacion_medico_detalle`** — un slot por cada intervalo; `IdProgramacionMedico = pm.id` (IDENTITY)
   - `Bloqueado=0, SinProgramacion=0, Adicional=0, Reservada=0, IdCita=NULL`
   - `PuntoAtencion=2` (SEDE 01) o `3` (SEDE 02)

---

## REGLAS CRÍTICAS

1. `citas.cod_medi` = `programacion_medico_detalle.Medico` — **siempre deben coincidir**
2. `programacion_medico_detalle.IdProgramacionMedico` = `programacion_medico.id` (el IDENTITY)
3. `id_sede = 2` para consultas y procedimientos; `id_sede = 3` para imágenes
4. `log_citas` se llena automático por SIESA — NO insertar manualmente
5. Los CUPS con sufijo (ej: `891901-72`) se buscan en `sis_proc_precios` por código base
6. `citas_procedimientos_asuntos` → solo consultas médicas
7. `citas_procedimientos` → solo procedimientos/imágenes (puede ser múltiple por cita)

---

## DATOS DE PRUEBA

- **Paciente prueba** (ZeusSalud_Prueba): `autoid=5`, empresa `EPS005`, contrato=4
- **Médico referencia**: `cod_medi=20` → ROBERTO MARIO ORTEGA VILLALBA

---

## MIGRACIÓN A SIESA — COMPLETADA ✅

- [x] Driver SQL Server `github.com/microsoft/go-mssqldb` agregado.
- [x] `internal/repository/siesa/` implementa todas las interfaces.
- [x] `internal/repository/datosipsndx/` (Antares/MySQL) eliminado.
- [x] Servidor SIESA de producción confirmado (`192.168.1.207` / `ZeusSalud_Neuro`).
- [x] Mapeo de paciente (`autoid`) y de entidad/contrato (`empresa` + `contrato`) implementado.

---

## NÉCTAR (mensajería)

- La mensajería migra de Bird a Néctar portando el puente de hdg-bot (`internal/nectar`, `bird.NectarBridge`);
  diseño en `docs/superpowers/specs/2026-10-27-migracion-nectar-design.md`.
- `NECTAR_BASE_URL` vacía = puente apagado (todo por Bird). Configuración a medias (faltan clave, secreto de ida o de avisos) = puente apagado + error en
  el arranque (`nectarBridgeFromConfig`). `/health` publica `nectar_host` (solo host).
- Secretos: `NECTAR_SERVICE_KEY` y `NECTAR_NOTICE_SECRET` por cuenta (`openssl rand -hex 32`);
  `NECTAR_REQUEST_SECRET` = `BOT_WEBHOOK_SECRET_INBOUND` compartido. Nunca valores reales en el repo.
- Apertura con plantilla (`POST /api/bot/conversations/open`, `NECTAR_CHANNEL_ID`): las plantillas se identifican por nombre+idioma
  (`NECTAR_TEMPLATE_NAMES`); el pending se llama `nectar:<conversation_id>` y guarda el teléfono en `ContactPhone` (el IVR de las 13:00
  sigue por Bird Voice). 409 `already_open` con `mine=false` no envía nada; ante duda (502, 504, `attempt_in_doubt`) no hay reintento
  (evento `notification_in_doubt`); en esos casos el recordatorio del día antes deja un pending `Unsent` para el IVR (abajo) y lo
  demás (mismo día incluido) no deja pending; solo el 503 o el 429 con `Retry-After` se reintentan (máx. 2, misma clave).
- Recordatorio del día antes que no salió → pending de confirmación `Unsent` bajo el teléfono (`+57…`, migración 045) para el IVR:
  solo un botón de esa plantilla (Confirmar/Reprogramar/Cancelar) cuenta como respuesta (con Néctar lo adopta por el contacto, `AdoptPhonePending`, no si la sesión está
  escalada); cualquier otro mensaje sigue el flujo normal y el pending queda para el IVR.
- Los botones de las plantillas en Meta deben titularse EXACTAMENTE (sin importar mayúsculas ni tildes; fuente
  `internal/notifications/template_buttons.go`): confirmación/reprogramación «Confirmar», «Reprogramar», «Cancelar»; cancelación admin
  «Entendido», «Reprogramar»; lista de espera «Agendar», «No, gracias». Néctar entrega el título como `interactive_reply_id` y el bot lo traduce a payload.
- Contacto (D2): el bot lee `contact` de `GET state` una vez por conversación (caché 24 h) y lo guarda en `contact_identifier`
  (migración 044); `TESTING_WHITELIST_PHONES` rige para los entrantes de Néctar con ese contacto (contacto vacío pasa).
- Horario: `NECTAR_SCHEDULE_ENABLED` (por defecto false) hace que el horario y festivos de Néctar manden en atención y escalación; encenderlo
  solo cuando los tres equipos tengan horario en Néctar (si no, publica «cerrado toda la semana» y las escalaciones se desvían).
- Lo que Neuro pide a Néctar es aditivo: no debe romper a hdg-bot.
- Informes de asesores (D5, `internal/nectarreports`): con puente, cada `NECTAR_REPORTS_SYNC_MIN` (5) el bot copia tramos
  (`waiting-cycles`, receta §3(a), desde `DataFloor`) y roster (`agents`) a las tablas de la migración 046 y completa las
  escalaciones `nectar:*` (`agent_id` = `nectar:<id>`, primera respuesta, mensajes, solo `expired` sin respuesta→`agent_no_show`; `completed` = un agente pulsó «Resolver», igual que en Bird; la cola vencida la marca el worker); exige la
  clave de CUENTA: con la global (403) se detiene con un solo error en el log.
- Menús del agente por paso: cada paso escalable registra sus opciones/campos (`sm.RegisterReturnMenu` y afines, llamado
  donde se registra el estado), más las universales `bot_reanudar`/`bot_reiniciar`/`bot_cerrar`; no hay lista de pasos aplazados.
- Transición (dos líneas): `NEW_LINE_ANNOUNCEMENT` (vacía = apagado) antepone el anuncio de la línea nueva al saludo
  (`GREETING` y `OUT_OF_HOURS`) solo en conversaciones de Bird y con el puente armado (`cfg.NectarBridgeArmed`, lo pone
  main); nunca en `nectar:*` ni con `post_confirmed`.
- `AGENTS_SOURCE=bird|nectar` (por defecto `bird`; otro valor tumba el arranque): fuente del informe de agentes. Con puente y
  `bird`, `/api/internal/agents/bird` da el roster de Bird; con `nectar`, 409 `agents_in_nectar`. Sin puente siempre Bird.
  Un despliegue con todos los agentes en Néctar debe poner `nectar`.
- Aviso «🚨 URGENTE — volver a Bird» (`internal/monitor/urgent.go`): solo con Telegram y puente encendido; disparadores
  `nectar_down`, `proactive_stopped`, `reminders_unsent`, `escalation_failing`; umbrales `URGENT_*`. La persona de turno
  sigue `docs/GUIA-VOLVER-A-BIRD.md` (vaciar `NECTAR_BASE_URL`, recomendado también `NEW_LINE_ANNOUNCEMENT`, `docker compose up -d bot`).
- Sin puente no se procesa nada de Néctar, ni por HTTP ni por el WAL (el replay cierra las filas `nectar_notice` con
  `bird.ErrNectarBridgeOff`). Por HTTP, con firma válida se acusa con 200 y se descarta; sin `NECTAR_NOTICE_SECRET`, 401 como
  antes (la alerta `bot_unreachable` de Néctar no salta con el 200): un supervisor
  de Néctar rescata una por una las conversaciones del bot («escalar», motivo `bot_down`). Requisito antes del arranque:
  `escalation_enabled` encendida para Neuro en Néctar (si no, 403).

## ROBUSTEZ (auditoría 2026-11-03)

- Apagado: el contenedor `bot` tiene `stop_grace_period: 75s` (el apagado ordenado suma hasta 60 s); se aplica al
  recrearlo (`docker compose up -d bot`).
- Candado por teléfono (`internal/worker/pool.go`): un mensaje del WAL que no lo consigue no se pierde; se reencola
  en memoria (un solo worker espera por teléfono, los demás se difieren 2 s) durante hasta 10 min, y el aviso
  «Tu mensaje anterior…» sale una vez y solo tras 30 s de espera. `virtual-`/`agent-` siguen como antes.
- Avisos resuelta/devuelta de Néctar: su fila del WAL se cierra al procesar el comando; un aviso anterior a la
  escalación vigente (−2 min de tolerancia) se ignora.
- Scheduler: una tarea que falla ANTES de enviar (`ErrNothingSent`) se reintenta cada 5 min, máx. 6 veces y nunca
  después de la hora programada + 30 min; un fallo tras un envío no se reintenta (cada envío se cobra). El
  reintento de `same_day_reminders_HH` calcula su franja desde la hora programada, no desde la suya.
- Bucles de fondo con MySQL: plazo por pasada (2 min) que solo impide empezar la fila siguiente; cada fila tiene su
  propio ctx (2 min) y la marca de un envío ya hecho usa uno nuevo (`utils.MarkContext`).
- Límite de la API interna: la IP del cliente sale de la última entrada de X-Forwarded-For solo si la conexión viene
  del proxy de confianza, reconocido por nombre (`TRUSTED_PROXY_HOSTS`, por defecto `ngrok`; definida vacía = no
  confiar en ninguno, con Warn al arrancar).
- Logs: la clave `text` (mensaje del paciente) se registra como «[redactado: N caracteres]».
- Recordatorio con SIESA caída (D-04, migración 047): «Confirmar»/«Cancelar» quedan pendientes en `notification_history`
  (`siesa_pending`, `siesa_outcome`, `appointment_slot`); el paciente recibe un acuse y el proceso de fondo
  (`SIESA_PENDING_RETRY_MIN`, 5) las aplica al volver: confirma con el mensaje de siempre o abre la confirmación de
  cancelación de siempre. Cita movida (otro `appointment_slot`), sesión ocupada, >6 h o cita a <2 h → agente
  (`notificacion_siesa_pendiente`).
- Topes: cantidad por CUPS >100 → agente (`cantidad_fuera_de_rango`; EMG/NC sin cambios); 3 lecturas fallidas
  seguidas de la orden → agente (`ocr_lecturas_fallidas`); los desvíos «no hay asesores» cuentan para el tope de
  3 escalaciones (persistido; `bot_disabled` exento; se reinicia cuando un agente atiende y devuelve). Tras el corte
  el bot calla salvo sus botones y «menú», y lo ignorado no renueva la sesión.

## OCR DE LA ORDEN (mejoras tomadas de hdg-bot, 2026-11-03; plan en `docs/PLAN-MEJORA-OCR-CUPS.md`)

- Toda imagen a Vision pide `detail: "high"` (`visionImagePart`).
- PDF: capa de texto primero (`gs -sDEVICE=txtwrite`, un archivo por hoja; ≥40 palabras en total Y ≥20 en CADA
  hoja; con varias hojas, la instrucción multipágina y «--- HOJA N ---»; el prompt avisa que puede venir del OCR del
  escáner). Si alguna hoja no llega, o del texto no sale ningún CUPS con código, o falla: imágenes, todas las hojas
  (máx. `maxOCRPages`) en UNA llamada y, si falla o no trae código, la 1.ª sola.
- `OPENAI_DOC_MODEL` (por defecto `gpt-4.1`): modelo de la segunda lectura del código (`OCRService.ReadCupsCode`,
  una sola tarea; confianza baja → vacío, nunca adivina).
- VALIDATE_OCR (`handlers/cups_resolve.go`): un código inactivo del catálogo se descarta sin más (es real). Uno
  fuera del catálogo o con otra especialidad pasa por la cascada: segunda lectura (con `ocr_order_url`, que vive solo
  en el paso encadenado; sin ella, sin nombre o con <25 s de mensaje se salta; si confirma el mismo código, se
  descarta) → el MISMO procedimiento por nombre (`services.CompareCupsNames`: palabras distintivas IGUALES en los dos
  sentidos tras quitar tildes, números incluidos; región, lateralidad, contraste, primera vez/control… cuentan; si
  solo coinciden por prefijo —singular/plural, bilateral/unilateral— es duda) con código a UN dígito (único) → si
  hay mismos nombres sin ese dígito, solo por prefijo, o el código corregido ya está en la orden, agente
  (`cups_posible_mala_lectura`) → si no, se descarta. Las correcciones (`ocr_cups_corrections`, «CUPS corregidos» en
  el resumen del agente, `agendar/cups_corregido`) se borran al leer una orden nueva.
- Sin código en la orden (decisión del dueño, 2026-11-03): el prompt de visión YA NO lleva el catálogo ni elige
  códigos; devuelve `cups_code` vacío con el nombre tal cual (y `is_interconsulta`). VALIDATE_OCR, al inicio,
  asigna por nombre (`handlers/cups_name_assign.go` + `services/cups_name.go`, sobre el nombre canónico: sin
  tildes, abreviaturas RM/TAC/ECO/RX/EMG/NC/PET y sinónimos CRÁNEO=CEREBRO, LUMBOSACRA=LUMBAR, TORÁCICA=DORSAL,
  MIEMBRO=EXTREMIDAD… expandidos). Nombre IGUAL a un único procedimiento → se asigna; si no, el modelo de
  `OPENAI_DOC_MODEL` (solo texto, `ChooseCupsByName`) solo puede confirmar, con confianza alta, al ÚNICO candidato
  que ENCAJA: las mismas palabras de contenido en los dos sentidos (salvo `orderNoise`, `catalogNoise` y lo que
  el catálogo pone entre paréntesis) y lo mismo en cada grupo de calificadores (contraste, primera vez/control,
  pediátrica/adultos, lado, superior/inferior, vía, marcador PET, modalidad, doppler, columna, región,
  números). Interconsulta → solo consultas de primera vez (asuntos 1, 8, 10). Con candidatos pero sin asignación
  segura → agente (`ocr_sin_codigo`, nota con nombre y candidatos); SIN candidatos → se descarta como antes
  (cuenta como omitido) y queda en `CtxOCRUnreadNames`; si la orden se queda sin CUPS, escala como la orden sin
  código. `/bot orden` con un procedimiento sin código se le rechaza al agente pidiendo el código. Lo asignado:
  `ocr_cups_name_assignments` («CUPS asignados por nombre»), `agendar/cups_asignado_nombre`, y el paciente ve
  «<catálogo> (en tu orden: «<leído, ≤60>»)» (cuerpo ≤1.024). «CON Y SIN CONTRASTE» (y sin y con, con/sin, c/s,
  gadolinio) es contrastado: queda «SIMPLE Y CONTRASTE» antes de leer «SIN CONTRASTE»; «C/C», «C/CTE», «C/MEDIO» →
  CONTRASTE y «S/C», «S/CONTRASTE» → SIMPLE; una abreviatura con barra desconocida («C/X») no vale como silencio
  del contraste (solo asigna el nombre igual). Guarda: un CUPS CON código
  cuyo nombre leído contradice EXPLÍCITAMENTE el del catálogo en un grupo excluyente (contraste, primera
  vez/control, pediátrica/adultos, lado, superior/inferior, vía, marcador PET, nivel de columna, región; los dos
  lo mencionan y no comparten ningún valor tras los sinónimos; NO modalidad) → agente (`cups_posible_mala_lectura`).
  Pares de prueba de la guarda en `internal/services/testdata/cups_guard_pairs.txt`.
