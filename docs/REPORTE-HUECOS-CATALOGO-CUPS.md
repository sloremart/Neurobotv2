# Reporte — Huecos del catálogo CUPS del bot (cruce exhaustivo)

> **Objetivo:** encontrar CUPS que los pacientes traen y el bot NO puede agendar (los escala), pero que **SÍ son
> agendables** (humanos los agendan en SIESA). Esos son **huecos del catálogo del bot** (`cups_procedimientos` +
> `cups_medico`) que, al llenarse, convierten escalaciones en citas — arreglo de **dato/config, no código**.
>
> **Método:** TODOS los CUPS descartados por el bot (`ocr_no_valid_cups.skipped`, últimos 30 días, freq ≥ 3) ×
> citas reales en SIESA por **humanos** (con su médico/asunto). Fuente: réplica local de prod. Ventana: 30 días.

> **IMPORTANTE (aclaración de la clínica):** que un CUP se agende en SIESA **NO** implica que sea un hueco del
> bot. Varios se escalan **por diseño** porque requieren un **flujo distinto** (piden información extra que la
> clínica decidió NO meterle al bot). Por eso este cruce separa **"especiales por diseño"** de **"candidatos a
> hueco"** — y hasta los candidatos requieren validación de la clínica.

## 1a. ESPECIALES POR DISEÑO — se escalan a propósito (NO son huecos)

Requieren un flujo/coordinación distinta (más datos que el bot no pide). Se agendan en SIESA por humanos, pero el
bot NO debe agendarlos hasta que (si acaso) se construya ese flujo especial. La mejora aquí es el **handoff**.

| CUP | Nombre | Escala/mes (bot) | Citas/mes SIESA | Médico / Asunto | Por qué especial |
|---|---|---|---|---|---|
| 891704, 891703 | Polisomnografía (sueño) | 396 | 794 | 25 / as 13 | Flujo distinto (info extra) |
| 891402, 891401, 891901 | EEG / videotelemetría | 227 | 147 | 24 / as 14 | Flujo distinto (info extra) |
| (PET/CT, asunto 12) | PET | — | — | — | **Módulo aparte** |

**Acción:** NO agregar al catálogo. **Mejorar el handoff** (mensaje por tipo + captura de los datos extra que
necesita el agente + promesa de callback) para que el paciente no quede esperando en un chat que expira.

## 1b. CANDIDATOS A HUECO — validar con la clínica si son de flujo normal

CUP que humanos agendan en SIESA y NO parecen requerir flujo especial → **posibles** huecos del catálogo. Antes
de agregar, confirmar con la clínica que son de flujo normal (no como sueño/EEG).

| CUP | Nombre | Escala/mes (bot) | **Citas/mes SIESA** | Médico / Asunto | Nota |
|---|---|---|---|---|---|
| **992990** | (confirmar) | 76 | **161** | 62 / as 20 | Validar flujo |
| **861411** | Aplicación de sustancia | 7 | 34 | 23 / as 16 | Es de grupo MRC — validar |
| **998702** | (RNM — confirmar) | 4 | 21 | 3 / as 4 | Imagen |
| **879304** | (TAC — confirmar) | 9 | 7 | 4 / as 3 | Imagen |
| **890364** | Consulta control Fisiatría | 16 (leído `8903640100`) | **334** | 14 / as 15 | + fix normalización (§2) |

## 2. BUG de normalización de código

El bot leyó **890364** como **`8903640100`** (base + modificador concatenado, sin guion) → el cruce automático
da 0 en SIESA porque busca `8903640100`. La base **890364 tiene 334 citas/mes**. El stripping de
`validateOCRHandler` solo reduce sufijos con `-` (`891509-16`→`891509`), no los concatenados. **Fix:** probar
también el prefijo de 6 dígitos contra el catálogo (`8903640100`→`890364`). Otros posibles afectados por lo mismo:
`891901000`, `891420000`, `8902640200`, `691901000` → normalizar antes de cruzar/agendar.

## 3. NO agendables por flujo normal de cita (0 citas humanas) — dejar como escalación

CUP que el bot escala Y que humanos **tampoco** agendan por el flujo de cita (0 citas) → coordinación aparte,
interconsultas, o no son "cita" agendable: `890380, 890276, 890211, 890283, 890328, 890373, 890402, 890363,
890302, 890464, 890243, 890624, 879240, 879110, 879205 (inactivo), 306001, 306007, 931000, 931001, 930880,
952626, 954301, 954107, 954103, 895004, 881202, 903867, 891702, 891101, 221401, 940701, 010151, 6305, 871704…`

**Acción:** no tocar el catálogo; **mejorar el handoff** (mensaje claro + captura de datos + devolución de
llamada, no un handoff en vivo que expira). Algunos podrían ser huecos si la clínica confirma que son agendables.

## 4. BASURA de OCR — ignorar

`000000`, `null`, `Z20030` (diagnóstico Dx), `ORTOPED024` (texto), códigos malformados. Errores de lectura, no
huecos. El fix multipágina + mejores mensajes de OCR los reducen.

## 5. Recomendaciones

1. **Sueño/EEG/PET (especiales por diseño, §1a): NO agregar al catálogo.** Se escalan a propósito (flujo distinto
   con info extra; PET tiene módulo aparte). La mejora es el **handoff**: mensaje por tipo + **captura de los datos
   extra** que el agente necesita + promesa de callback, para que el paciente no expire en silencio. *(Si la
   clínica algún día quiere, se podría construir el flujo especial en el bot — esfuerzo mayor.)*
2. **Validar con la clínica los candidatos a hueco (§1b)** — `992990, 998702, 879304, 890364` (y `861411`, que es
   MRC): confirmar que son de **flujo normal** (no como sueño/EEG). Los que lo sean → **llenar** en
   `cups_procedimientos` + `cups_medico`.
3. **Fix de normalización de código** (§2) — antes de agregar `890364` y similares.
4. **Automatizar este cruce** como **vista de dashboard** ("CUPS que el bot no pudo agendar" × citas-humanas-SIESA)
   con la clasificación especial/candidato/basura, para detectar huecos nuevos de forma continua.
5. Confirmar nombres de `992990`, `998702`, `879304` con la clínica.
