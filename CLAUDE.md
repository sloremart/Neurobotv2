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
  el arranque (`nectarBridgeFromConfig`); lo mismo con una URL sin https que no sea de un stub local (`nectar.SafeBaseURL`). `/health` publica `nectar_host` (solo host).
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
  Apagada y con puente rige la semana de siempre con los festivos de Néctar (`GET /api/bot/schedule`; son de la entidad, no de equipo ni
  canal, y por eso exigen la clave de CUENTA: la global lee los de todas), sin mirar sus ventanas: nunca «cerrado toda la semana». Si Néctar no
  responde, sigue la última lista buena con un Warn. Sin puente, sin festivos. El texto de ayuda con el horario sigue igual (`SameAsDefault`).
  En los dos modos el refresco comprueba una vez la clave con `GET agents`: con la global (403 `account_key_required`) no publica nada (rige la
  semana de siempre sin festivos de Néctar, nunca «cerrado toda la semana») y se detiene con un solo error; si Néctar no responde, como siempre.
- Lo que Neuro pide a Néctar es aditivo: no debe romper a hdg-bot.
- Informes de asesores (D5, `internal/nectarreports`): con puente, cada `NECTAR_REPORTS_SYNC_MIN` (5) el bot copia tramos
  (`waiting-cycles`, receta §3(a), desde `DataFloor`) y roster (`agents`) a las tablas de la migración 046 y completa las
  escalaciones `nectar:*` (`agent_id` = `nectar:<id>`, primera respuesta, mensajes, solo `expired` sin respuesta→`agent_no_show`; `completed` = un agente pulsó «Resolver», igual que en Bird; la cola vencida la marca el worker); exige la
  clave de CUENTA: con la global (403) se detiene con un solo error en el log. La atención sigue `previous_cycle_id` salvo
  desde un tramo `resolved`: desde el Plan 49 una reapertura apunta al resuelto y es otra atención.
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

- Apagado: el contenedor `bot` tiene `stop_grace_period: 75s` (el apagado ordenado suma hasta 67 s, `cmd/server/shutdown.go`); se aplica al
  recrearlo (`docker compose up -d bot`). Espera primero las respuestas a plantillas en vuelo (hasta 32 s: cubre `ConfirmBlock`, no 30 s
  más de candado del teléfono) y luego para el pool, cuyo drenado (hasta 15 s) toma los mensajes virtuales que encolaron.
  Residual: el pool espera a los workers en vuelo sin plazo (`wg.Wait`) antes de drenar; si un mensaje en vuelo pasa de
  47 s (`workersStopTimeout`), el drenado no corre y puede perderse el mensaje virtual encolado, que no está en el WAL.
  Una respuesta cortada tras sacar el pending pierde la pulsación, y si el proceso sale antes de que falle no queda ni el error en el log.
- Candado por teléfono (`internal/worker/pool.go`): un mensaje del WAL que no lo consigue no se pierde; se reencola
  en memoria (un solo worker espera por teléfono, los demás se difieren 2 s) durante hasta 10 min, y el aviso
  «Tu mensaje anterior…» sale una vez y solo tras 30 s de espera. `virtual-`/`agent-` siguen como antes.
- Reintentos de envío (`retrySend`, solo turnos del worker, nunca aperturas ni Bird, que sigue con su único reintento
  a los 10 s): en Néctar hasta 2 (10 s, 30 s) solo ante 5xx, transporte o 429, con la MISMA clave en duda; el 429 espera
  su Retry-After si es mayor (≤60 s; más: no se reintenta, evento `infra/send_retry_after_too_long`; 429 tras los dos reintentos: `infra/send_rate_limited_undelivered`). Corren fuera del
  candado y no dependen del plazo del turno. Ante 5xx, transporte o abandono por plazo nada queda peor que con un solo reintento (un 4xx no
  permanente o un Retry-After >60 s ya no se reintentan, a propósito): el primero a los 10 s sale siempre; el 2.º, y
  el 1.º que un Retry-After alarga, se descartan (con log) si ya EMPEZÓ un turno posterior de ese teléfono (`beginTurn`, al tomar el
  candado), entregado o no. Los mensajes de un mismo turno no se suprimen entre sí.
  Para `nectar_down` cuentan el envío y el primer reintento (el último no: el aviso salta con los mismos mensajes que con un solo
  reintento), y un envío del turno abandonado por su plazo (`nectar.MarkTurnSend`) no cuenta; cualquier otro plazo corto sí.
  Los envíos del turno por Néctar llevan el plazo que le queda al mensaje (mínimo 5 s, para que un turno que lo gastó aún
  responda); lo abandonado por plazo se reintenta fuera del turno como cualquier fallo, con su clave en duda. Quedan sin plazo propio (15 s del cliente):
  `SendTemplate`, `Available` y los envíos directos con `SendText`.
- Avisos resuelta/devuelta de Néctar: su fila del WAL se cierra al procesar el comando; un aviso anterior a la
  escalación vigente (−2 min de tolerancia) se ignora.
- Scheduler: una tarea que falla ANTES de enviar (`ErrNothingSent`) se reintenta cada 5 min, máx. 6 veces y nunca
  después de la hora programada + 30 min; un fallo tras un envío no se reintenta (cada envío se cobra). El
  reintento de `same_day_reminders_HH` calcula su franja desde la hora programada, no desde la suya. Un salto del
  reloj que se come el minuto de una tarea: corre en el primer tic, hasta 10 min tarde y una vez al día (por fecha), si su
  política de catch-up lo permite a esa hora (nunca `CatchupNever`). Lo programado antes del arranque es de
  `RunMissedTasks`, no de este arranque tardío.
- IVR: una llamada por teléfono (`DialPhone`), a la cita más próxima entre sus pendings; el callId va al pending de esa
  cita. La llamada anuncia una sola cita, pero el DTMF confirma o cancela TODAS las del paciente en esa fecha y resuelve
  también sus otros pendings (otra clave de la misma persona, otra cita del día). Los de otros pacientes con el mismo
  teléfono siguen su flujo normal. Un pending llama solo si su cita sigue en la lectura con ese teléfono.
- Bucles de fondo con MySQL: plazo por pasada (2 min) que solo impide empezar la fila siguiente; cada fila tiene su
  propio ctx (2 min) y la marca de un envío ya hecho usa uno nuevo (`utils.MarkContext`).
- Límite de la API interna: la IP del cliente sale de la última entrada de X-Forwarded-For solo si la conexión viene
  del proxy de confianza, reconocido por nombre (`TRUSTED_PROXY_HOSTS`, por defecto `ngrok`; definida vacía = no
  confiar en ninguno, con Warn al arrancar).
- Logs: la clave `text` (mensaje del paciente) se registra como «[redactado: N caracteres]».
- `chat_events` (C-04): sin nombre, con celular/correo/documento enmascarados y solo el largo de lo escrito en escalación y en los pasos de datos personales; a los 30 días `chat_events_pii_retention` (02:30) enmascara el teléfono (no `nectar:`) y quita las claves de PII de `event_data` sin borrar filas (`docs/OBSERVABILIDAD.md`).
- Recordatorio con SIESA caída (D-04, migración 047): «Confirmar»/«Cancelar» quedan pendientes en `notification_history`
  (`siesa_pending`, `siesa_outcome`, `appointment_slot`); el paciente recibe un acuse y el proceso de fondo
  (`SIESA_PENDING_RETRY_MIN`, 5) las aplica al volver: confirma con el mensaje de siempre o abre la confirmación de
  cancelación de siempre, solo en la ventana de avisos (7-20 h, `config.PatientNoticeWindow`; fuera, la fila espera
  sin sumar intento, salvo que esperar a las 07:00 deje la cita a <2 h: entonces se aplica y se avisa de noche). El
  tope de >6 h solo cuenta el tiempo con la ventana abierta y solo se mira con ella abierta; la cita a <2 h (la leída o, con SIESA
  caída, la recordada en `appointment_slot`) va al agente a cualquier hora. Cita movida (otro `appointment_slot`), sesión ocupada, >6 h o cita a <2 h → agente
  (`notificacion_siesa_pendiente`).
- Topes: cantidad por CUPS >100 → agente (`cantidad_fuera_de_rango`; EMG/NC sin cambios; `/bot cups` de Bird y el
  campo de CUPS de Néctar se le rechazan al asesor con `services.AgentCupsQuantityProblem`); 3 lecturas fallidas
  seguidas de la orden (también la de la EMG, UPLOAD_EMG_ORDER) → agente (`ocr_lecturas_fallidas`); al tope de 3
  escalaciones (persistido; se reinicia cuando un agente atiende y devuelve) suman SOLO los ciclos del paciente,
  entregados o desviados por «no hay asesores»: pedir asesor y agotar reintentos (`sm.EscalationCountsTowardCap`,
  tabla en `escalation_reasons.go`; motivo vacío o nuevo sin clasificar suma). Las que dispara el bot (orden dudosa,
  B-05, B-10, SANITAS, fallos) ni suman ni cortan; `bot_disabled` exento. Tras el corte el bot calla salvo sus
  botones y «menú», y lo ignorado no renueva la sesión.
- Llamadas extra al modelo por orden (VALIDATE_OCR): máx. `maxExtraModelCallsPerOrder` (6) entre segundas lecturas
  y elecciones por nombre; pasado el tope, lo que pediría una llamada es duda → agente (un código sin ningún
  procedimiento de nombre parecido se descarta, como con la lectura). La orden se baja y convierte una vez por pasada
  (`OCRService.OrderImages`). `ocr_order_url` se borra al entrar a ASK_MEDICAL_ORDER/UPLOAD_MEDICAL_ORDER/
  UPLOAD_EMG_ORDER (salvo la de su propia foto); `escalation_note` se borra en la escalación entregada y en la
  desviada, no en la salida a la recuperación IA.
- Recuperación IA: el mensaje aclaratorio del modelo no se envía si pasa de 300 caracteres o trae enlace, correo,
  teléfono o número de 7+ dígitos (fechas aparte; el prompt le pide describir los ejemplos con palabras). Si no
  pasa, se reenvía el último mensaje del bot (`CtxLastBotMsg`) y cuenta como un intento; escala solo al agotar los
  intentos, o si no hay último mensaje.
- «¿Es correcto?» (CONFIRM_OCR_RESULT): si la lista no cabe en el cuerpo de botones (1.024), sale COMPLETA antes en
  texto (trozos ≤4.096) y los botones llevan solo el aviso de omitidos y la pregunta; nunca se recorta con «…».
- Imagen (`docker/Dockerfile`): `alpine:3.24.3`, `ghostscript~10.08` (gs en `community`: revisar la fijación, la
  de `bot-logs-init` y `GS_MIN_VERSION` del CI en cada versión de Alpine); el bot corre como `bot` (UID/GID
  10001), no root. El job `image` del CI falla si la imagen corre como root o gs < `GS_MIN_VERSION`. El servicio
  `bot-logs-init` del compose (`chown` de `bot-logs` a 10001, en cada `up`; el watcher lo ignora) deja escribible
  el volumen creado cuando corría como root: sin él no hay logs en archivo ni `/api/internal/logs`.
  `.dockerignore`: sin `**/.env*`, backups, `auditoria/` ni docs. `.git` está en el contexto pero no en ninguna
  capa (`COPY --exclude=.git` + bind mount solo para el `git rev-parse`; `# syntax=docker/dockerfile:1.19`, que
  BuildKit baja de Docker Hub).
- Despliegue de la imagen sin root (primera vez): 1) `docker compose up -d watcher` ANTES que el bot (toma el nuevo
  `IGNORE_CONTAINERS`; si no, avisa «DETENIDO: neuro_bot_logs_init»); 2) `docker compose up -d --build bot`
  (arrastra el init); 3) `docker compose exec bot id -u` = 10001 y `/api/internal/logs` con líneas de hoy. Tras un
  rollback a la imagen vieja (root), volver adelante con `docker compose up -d bot`, NUNCA `restart`: solo `up`
  corre el init y la vieja deja el log del día de root.
- Ghostscript (OCR del PDF): `-dSAFER`, `-dMaxBitmap=10000000`, `-dLastPage=maxOCRPages`, toda la conversión
  de un documento (todos sus intentos) acotada a `pdfRenderBudget` (20 s; agotado = fallo de gs, el resto de
  `defaultAnalyzeTimeout` queda para Vision). Si un JPEG convertido pasa 10 MB, o el total 32 MB, se reconvierte a
  la resolución que el tamaño medido a 300 dice que cabe (150 o directo a 100; la foto pasada a PDF a 72-96 DPI);
  solo si ni así cabe se rechaza. Carta/oficio siguen a 300 DPI, una sola conversión.
- `/health`: SIESA caído = 200 `degraded` (Docker no lo marca unhealthy); BD local caída = 503 `critical`. Monitor
  externo (keyword `"status":"ok"` cada 1 min): `docs/MONITOREO-EXTERNO.md`; el watcher solo ve eventos de Docker.
- Aviso del mismo día: tras leer SIESA se descartan las citas cuya hora no es posterior a la hora real (la franja
  sigue saliendo de la hora programada).
- Despliegue: al arrancar, `WARN insecure configuration` (no fatal, sin valores) si `DB_PASSWORD`/`DB_ROOT_PASSWORD`
  son las de `.env.example` (root vacía también), `INTERNAL_API_KEY` < 32 caracteres o `EXTERNAL_DB_ENCRYPT` ≠ `true`
  (`config.SecurityWarnings`). `GOMEMLIMIT=1536MiB` va en el compose junto a `memory: 2048M` (cambiar los dos). La
  zona horaria se fija antes de la primera goroutine. Cerrar el MySQL publicado (13308; el dashboard entra por él),
  activar el cifrado a SIESA, el riesgo aceptado de la API interna por el túnel y cómo comprobar en logs si Bird firma
  el DTMF: `docs/MONITOREO-EXTERNO.md` («Revisiones de despliegue»).

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
  «<catálogo> (en tu orden: «<leído, ≤60>»)».
  Contraste (`services/cups_contrast.go`, rondas 4 y 5, cerrado a fallos): el nombre se normaliza antes
  (`foldProcedureText`: siglas con punto unidas —P.A., M.D.—, un solo espacio, la barra pegada a sus letras, «+»
  como Y, N0→NO, C0N→CON, 5IN→SIN; la marca de cantidad «NO» solo vale con signo o pegada a la cifra; los signos y
  casillas —«(-)», «(+)», «( )», «[ ]», «Ø», «✗», «✓», un guion o raya que no une dos palabras, un 0 suelto, y el
  campo vacío «CONTRASTE:» (dos puntos o igual al final sin valor)— quedan como la ficha «?», `contrastSigns`) y UN
  clasificador (`readContrast`; su gramática ES la tabla de
  `TestReadContrast_Grammar`) lee la frase ENTERA del contraste como SIMPLE («SIMPLE»; «SIN|NO [CON] [MEDIO [DE]]
  CONTRASTE|CONTRASTADO|GADOLINIO|CTE»; «S/C», «S/CONTRASTE»; «CONTRASTE: NO» al final y SIN el CON delante: «CON
  CONTRASTE NO» es un nombre cortado, duda), CONTRASTE («[CON] CONTRASTE», «CONTRASTADO», «C/C», «C/CTE», «C/MEDIO
  DE CONTRASTE»; «CONTRASTE: SI» al final) o AMBOS («CON Y|O SIN», «SIN Y|O CON», «CON/SIN», «C/S» seguidos del
  objeto, con o sin «MEDIO DE»; «SIN CONTRASTE O|Y CON CONTRASTE»; «SIMPLE Y [CON] CONTRASTE|CONTRASTADO»). Dos o
  más frases de la gramática seguidas (con Y u O entre ellas) valen si TODAS dicen lo mismo («SIMPLE (SIN
  CONTRASTE)», «SIN CONTRASTE Y SIN CONTRASTE»). Cualquier otra mención —CONTRAST*, GADOLINIO, «C/S» sola, barra con
  C o S («C/X», «C/U», «S/N», «N/A» tras CONTRASTE; las abreviaturas de columna «L/S», «D/L» y «C/D» NO son el
  contraste), «CC»/«SC», «SIMPLE O CONTRASTADO», «CON Y/O SIN», dos frases distintas, una negación suelta junto a la
  mención (NO, NI, NEGATIVO, SIN: «NO REQUIERE CONTRASTE»), un signo o casilla pegado a la mención («CONTRASTE
  (-)», «Ø CONTRASTE», «CONTRASTE: 0», «CON CONTRASTE ( )»: nunca vale CONTRASTE), un nombre cortado en CON o SIN—
  es DESCONOCIDO: nunca se asigna por nombre igual ni por el modelo y va al agente
  (`ocr_sin_codigo` con sus candidatos, o `cups_posible_mala_lectura` con nota propia si trae código). AMBOS solo es
  igual a la fila «SIMPLE Y CON CONTRASTE» (879113): donde no la hay es duda, no el simple ni el contrastado; en la
  guarda, AMBOS frente al código con contraste no es contradicción (está documentado), frente al simple sí. Al
  descartar entre hojas, DESCONOCIDO o CONTRASTE frente a AMBOS es duda, nunca igual. Las palabras
  cortas cuentan (`procedureWords`), salvo `genericProcedureWords` (conectores, AP, PA): el lado en corto (D, OD, MD,
  I, OI, MI, también con puntos) vale lo mismo que la palabra, salvo en un nombre de columna («CERVICAL Y D» es la
  dorsal: duda); el lado no busca candidatos (`retrievalWords`: «EMG MMII D» los tiene). Al ASIGNAR, el nivel de
  columna (L4, C5) y la marca de cantidad (#2, X2, N 2, No. 1) son silencio si el catálogo no los menciona, y si lo
  menciona y es otro nivel, contradicción; también lo es, en la asignación y en la guarda, el nivel que no cabe en la
  región escrita («COLUMNA CERVICAL L4»; C cervical, T o D dorsal (TORACICA y TORACICO), L lumbar y lumbosacra hasta
  S1 —S2 en adelante es duda—, S sacro y coxis, y los discos C7-T1, T12-L1, L5-S1 caben en las dos; DORSOLUMBAR y
  TORACOLUMBAR son «DORSAL LUMBAR» y SACROCOXIGEA es «SACRO COXIS», `orderAbbreviations`; los segmentos viven en
  un solo sitio, `spineGroup`). Una lectura sin región ni segmento frente a DOS o más filas que son ese procedimiento
  MÁS una región —las mismas palabras de contenido, sin lo que el catálogo pone entre paréntesis, sin
  `catalogNoise` ni calificadores— es duda aunque exista la fila a secas («RX COMPARATIVAS» → agente, por PELVIS y
  MASTOIDES; «EMG» sigue a 930860: la de cara es una sola; «RX MIEMBRO INFERIOR AP Y LATERAL» es 873340: MAXILAR
  INFERIOR es otra anatomía). Si el ÚNICO candidato que encaja pone entre paréntesis lo que lo distingue de otra fila
  de su familia (misma modalidad y región, sus palabras de contenido) y el leído no lo dice, es duda («TAC MIEMBROS
  INFERIORES PEDIATRICA» frente a 879523-1 «(AXIALES DE ROTULA O LONGITUD…)»; `annotationDistinguishes`); «(54)»,
  «(TEJIDOS BLANDOS)» o «(UNO O MAS MUSCULOS)» no tienen esa otra fila y siguen sin obligar. Al asignar, el plazo
  («EN 1 MES», «6 MESES», «2 SEMANAS», «1 SESION»), la cantidad escrita («CANT 1», «CANTIDAD: 2»), el código del
  diagnóstico («CIE10 M545», un código CIE suelto que no es nivel de columna), la EPS de la lista CERRADA
  `epsNames` con la palabra EPS delante o detrás («EPS SANITAS», «SANITAS EPS», «NUEVA EPS»; cualquier otra
  palabra tras EPS se queda), «ORDEN <n>», «SEGUN EVOLUCION», «NO POS» y CITA PRIORITARIA son ruido
  (`withoutOrderTails`, `orderNoise`); «DX» y el diagnóstico escrito se cortan SOLO hasta la primera palabra del
  procedimiento (`withoutDiagnosis`/`procedureWord`: abreviatura, lado, contraste o negación, CONSULTA, nivel o
  valor de un grupo: «TAC CRANEO DX TCE CON CONTRASTE» conserva el contraste, «CONSULTA DX CEFALEA CONTROL
  NEUROLOGIA» el control). INVARIANTE (ronda 6): el contraste y los grupos excluyentes se leen sobre la lectura
  COMPLETA; si sin colas dicen otra cosa (`tailsChangeMeaning`, «TAC CRANEO NO POS CONTRASTE»), es duda para un
  agente, nunca una asignación. Si sin colas no hay candidatos y la lectura acaba en «POR <motivo>», se quita
  desde el último POR y sigue el camino normal; si aun así no hay candidatos, se reintenta con la lectura ENTERA
  antes de descartar (un descarte silencioso solo queda para lo que no tiene candidatos ni entero ni sin colas:
  EEG/PSG no están en el catálogo de prueba; «PET CT CUERPO ENTERO FDG» es el PET, `orderPhrases`). Al descartar
  entre hojas el plazo, el CIE, la EPS y el DX siguen contando.
  Para ENCAJAR y para que una región sea la misma solo vale el plural (`pluralWord`); el prefijo de 5+ letras
  (`prefixWord`) solo busca candidatos y sospecha («NEURO», «RX ESTERNOCLAVICULAR», «RM CEREBELO» → agente). «VEZ»
  sola no es primera vez («2 VEZ» → agente; «1 VEZ», «1A VEZ» sí). Al
  DESCARTAR una lectura (`compareReadNames`) cualquier diferencia de los dos es duda. PROCEDIMIENTO, EXAMEN y ESTUDIO
  no son una consulta: se comparan, salvo si el nombre escribe la modalidad («ESTUDIO DE RM CEREBRO» = «RM CEREBRO»).
  OBLICUAS sí distingue (873444, de extremidades; en una columna es ruido al asignar); «3 PROYECCIONES» es ruido al
  asignar y se compara al descartar; EXTREMIDADES solo es ruido en EMG, NC y potenciales; al conservar nombres de otras hojas (`addUncodedFrom`) y al fusionar páginas (`mergeCupsJSON`, `CompareUncodedReadings`), lo igual con otra cantidad, sedación, contraste u observaciones es duda (nunca se pierde); un doppler sin decir VASOS encaja también con el de vasos, y si encajan dos es duda.
  Guarda: un CUPS CON código
  cuyo nombre leído contradice EXPLÍCITAMENTE el del catálogo en un grupo excluyente (contraste, primera
  vez/control, pediátrica/adultos, lado, superior/inferior, vía, marcador PET, nivel de columna, región; los dos
  lo mencionan y no comparten ningún valor tras los sinónimos; NO modalidad) → agente (`cups_posible_mala_lectura`).
  ESTERNOCLAVICULAR es región (frente a TORAX y ESTERNON), y SACRO/COXIS son segmento y además región propia
  (871050 «SACRO COXIS» se escribe sin COLUMNA: frente a «COLUMNA VERTEBRAL TOTAL» o «DINÁMICA DE COLUMNA» es
  contradicción, como «RX COLUMNA» a secas con ese código). Pares de prueba de la guarda en
  `internal/services/testdata/cups_guard_pairs.txt`; barrido catálogo × catálogo a un dígito (el nombre exacto de
  uno con el código del otro) en `TestCupsNamesConflict_OneDigitSiblingsSweep` con sus pares de oro en
  `testdata/cups_one_digit_conflicts.txt` (regenerar con `CUPS_SWEEP_WRITE=1`, que falla si algún par del oro se
  perdería y solo con `CUPS_SWEEP_WRITE=force` los borra, diciendo cuáles) sobre el catálogo activo
  `testdata/cups_catalog_active.tsv`.
