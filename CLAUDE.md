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
