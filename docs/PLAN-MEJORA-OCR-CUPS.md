# Plan — mejora de la detección de CUPS en órdenes médicas

Plan vivo: se nutre de casos reales a medida que aparecen y sobre ellos se decide QUÉ y CÓMO ajustar el
OCR. No se cambia el prompt ni la metodología sin casos que lo justifiquen y sin medir antes y después.

**Restricciones (decididas por el dueño, 2026-11-03):** se sigue con OpenAI (visión) — el OCR local y la
GPU propia quedan descartados porque hay escaneos muy borrosos que solo la IA lee. El tratamiento de
datos ya se advierte en el saludo. Las reglas EMG/NC (`applyFisiatriaRules`) no se tocan.

**Privacidad:** este repo es público. Aquí NO van imágenes, nombres, documentos ni teléfonos: cada caso
se registra solo por identificadores internos (sesión, cita) y por los CUPS. Las imágenes de los casos
se guardan fuera del repo, en una carpeta privada del equipo.

## Estado actual (punto de partida)

- Modelo: `OPENAI_MODEL` (por defecto `gpt-4o-mini`), `internal/services/ocr_service.go`.
- Imagen: una llamada de visión. PDF: primera página y, si no hay CUPS, fallback con hasta 10 páginas
  (puede incluir historia clínica; el prompt pide ignorarla).
- Salida: JSON con `cups[{code,name,quantity}]`, `document`, etc. La cantidad sale de «(#N)».
- Validación posterior: el código se valida contra el catálogo; sin código → agente
  (`ocr_no_cups_code`); tope de cantidad 100 por CUPS (B-10); 3 lecturas fallidas por sesión → agente (B-05).
- El paciente confirma los CUPS leídos en `VALIDATE_OCR` («Sí, correcto» / «No, corregir»).

## Candidatos ya probados en hdg-bot (revisado 2026-11-03)

hdg-bot (`C:/laragon/www/CHATBOT/hdg-bot`, `internal/services/ocr_service.go`) nació de este mismo OCR
y lo ha reforzado con casos medidos. Son los primeros ajustes a evaluar contra los casos de aquí:

| # | Técnica en hdg-bot | Qué hace hoy Neuro | Ataca | Costo/riesgo |
|---|---|---|---|---|
| H1 | `detail: "high"` en cada imagen enviada a Vision (sin él OpenAI puede bajar la foto a 512 px y los dígitos quedan ilegibles; conv. 57 de hdg) | No lo pide | F1, F2, F6, F8 aparente | Más tokens por imagen; cambio de una línea |
| H2 | PDF con **capa de texto** primero (`gs -sDEVICE=txtwrite`, ≥40 palabras): se analiza el texto literal sin visión; solo los escaneos van como imagen | Siempre rasteriza a 300 DPI | F1, F2, F5 en PDFs generados por sistema | Más barato y rápido; sin dependencias nuevas (ya usa Ghostscript) |
| H3 | **Todas las hojas** de un PDF en una sola llamada (tope `maxOCRPages`); si falla, la primera | Hoja 1 y solo si no hay CUPS, las demás | F3 (procedimientos en hojas 2+ cuando la 1 ya trae alguno), F7 | Más imágenes por llamada en PDFs multipágina |
| H4 | **Segunda lectura dedicada del código** (`ReadCupsCode`) con un modelo más fuerte (`OPENAI_DOC_MODEL`, por defecto `gpt-4.1`) cuando el código leído no existe; prompt de una sola tarea con los dígitos que se confunden | No hay | F1, F6 | Una llamada extra solo en los casos dudosos |
| H5 | **Cascada de resolución** (`eligibility.ResolveCupsCode`): segunda lectura → nombre parecido en el catálogo a UN dígito del leído → si hay candidatos sin ese dígito, DUDA al agente con los candidatos; corrige solo con las DOS señales y lo deja anotado para el agente | El código se valida contra el catálogo; sin código, agente | F1 | Lógica nueva antes de agendar: toca flujo, requiere aprobación |
| H6 | **Segunda lectura del documento** del paciente (`ReadDocumentDigits`) cuando no coincide con la cédula confirmada | Compara `ocr_document` en VALIDATE_OCR | Falsos «la orden no es tuya» | Una llamada extra solo en el desacuerdo |
| H7 | **Pistas de prompt por entidad y tipo de documento** guardadas en BD (`PromptHints`), ajustables sin desplegar | Prompt fijo en el código | F9 | Tabla nueva + carga en el prompt |

Orden sugerido para medir (de menor a mayor esfuerzo): H1 → H3 → H2 → H4 → H5/H6 → H7. Cada uno se
adopta solo si mejora el set de casos sin romper ninguno que ya pasaba.

## Fuentes de casos

1. **Los que trae el dueño** (principal): cada caso que se presente en la operación.
2. **Señales en `flow_events`** para encontrar más, con la consulta de la sección «Consulta de apoyo»:
   - `ocr_failed` / `ocr_error` / `ocr_timeout` (no se pudo leer),
   - `ocr_no_cups_code` (leyó procedimientos sin código),
   - `ocr_rejected` / `ocr_incorrect` (el paciente dijo que lo leído estaba mal),
   - escalaciones con motivo de OCR (`ocr_escalate_to_agent`, `ocr_pidio_agente`).
3. **La verdad de cada caso:** lo que el agente terminó agendando en SIESA para ese paciente
   (`citas_procedimientos` / `citas_procedimientos_asuntos`), o lo que indique el dueño.

## Registro de casos

Una fila por caso. «Tipo de fallo» usa las categorías de abajo; «Hipótesis» se llena al analizarlo.

| # | Fecha | Sesión / cita | Formato (foto, escaneo, PDF n págs.) | CUPS esperados (cant.) | CUPS leídos (cant.) | Tipo de fallo | Hipótesis |
|---|---|---|---|---|---|---|---|
| — | — | — | — | — | — | — | — |

### Categorías de fallo

- **F1 Código mal leído:** un dígito cambiado (8↔3, 1↔7), código de otro procedimiento.
- **F2 Sufijo perdido o inventado:** `891901-72` leído como `891901` o al revés.
- **F3 Procedimiento omitido:** la orden trae N y se leyeron menos.
- **F4 Procedimiento inventado:** se leyó algo que no está en la orden (p. ej. de la historia clínica).
- **F5 Cantidad errada:** «(#N)» mal leído o ausente.
- **F6 Sin código:** se leyó el nombre pero no el código.
- **F7 Página equivocada:** en un PDF, la orden estaba en otra hoja o se tomó la historia clínica.
- **F8 Ilegible:** la imagen no permite leerla (ni un humano); no es un fallo del OCR.
- **F9 Formato de orden nuevo:** una EPS/IPS con una plantilla que el prompt no contempla.

## Método, paso a paso

1. **Registrar** cada caso que llegue en la tabla (sin datos personales) y guardar su imagen fuera del repo.
2. **Clasificar** el fallo (F1–F9) y anotar una hipótesis de causa.
3. **Reproducir**: correr el OCR actual sobre la imagen guardada y confirmar que falla igual.
4. **Agrupar**: cuando una categoría junte ~3 casos o un caso sea grave (agenda el procedimiento
   equivocado), se propone un ajuste concreto al dueño. Ajustes candidatos, según la categoría:
   - F1/F2/F6: contrastar código y nombre contra el catálogo (si no concuerdan, corregir por el catálogo
     o preguntar al paciente); reglas de forma del código (6 dígitos + sufijo opcional).
   - F3/F4/F7: primer paso que identifique las hojas de orden/formulación antes de extraer; no enviar
     la historia clínica cuando se identificó la hoja de orden.
   - F5: instrucción y ejemplos específicos de «(#N)»; señal de duda en la salida.
   - F9: ejemplos del formato nuevo en el prompt.
   - Casos dudosos en general: salida con nivel de confianza y segunda pasada con un modelo más fuerte
     solo cuando la confianza es baja (costo acotado).
5. **Medir antes y después** sobre TODO el set de casos acumulado (no solo el nuevo): aciertos exactos
   (código + cantidad) por caso y por categoría, y que ningún caso que antes pasaba empiece a fallar.
6. **Decidir con el dueño** qué ajuste se adopta; se implementa con TDD y se despliega.
7. **Seguir midiendo** en producción con las señales de `flow_events` (tasa de `ocr_rejected`,
   `ocr_no_cups_code` y escalaciones por OCR antes y después del ajuste).

## Consulta de apoyo (BD local `neuro_bot`)

Sesiones con señales de fallo del OCR en los últimos 14 días, sin datos personales en la salida. Los pasos
de `flow_events` (flujo `agendar`) están en el catálogo de `internal/observability/tracer.go`; el «No,
corregir» del paciente y la petición de agente quedan en `chat_events`. `flow_events` guarda 45 días.

```sql
SELECT fe.trace_id AS session_id, fe.step AS senal, fe.reason, MIN(fe.created_at) AS primera
FROM flow_events fe
WHERE fe.flow = 'agendar'
  AND fe.step IN ('ocr_failed', 'ocr_no_cups_code', 'ocr_append_failed', 'stashed_order_failed')
  AND fe.created_at >= NOW() - INTERVAL 14 DAY
GROUP BY fe.trace_id, fe.step, fe.reason
UNION ALL
SELECT ce.session_id, ce.event_type, NULL, MIN(ce.created_at)
FROM chat_events ce
WHERE ce.event_type IN ('ocr_rejected', 'ocr_escalate_to_agent')
  AND ce.created_at >= NOW() - INTERVAL 14 DAY
GROUP BY ce.session_id, ce.event_type
ORDER BY primera DESC;
```

## Bitácora de decisiones

| Fecha | Casos que la motivan | Ajuste | Resultado medido |
|---|---|---|---|
| 2026-11-03 | F1, F2, F6 (medido en hdg-bot, conv. 57) | **H1** `detail: "high"` en toda imagen enviada a Vision (`visionImagePart`) — f43f7f5 | Pendiente de medir en producción; sin casos propios aún |
| 2026-11-03 | F3, F7 | **H3** todas las hojas del PDF (máx. `maxOCRPages`) en UNA llamada; si falla, solo la 1.ª — 5f54fef | Pendiente de medir; tests: hoja 1 y hoja 2 con CUPS distintos → se leen los dos |
| 2026-11-03 | F1, F2, F5 en PDFs generados por sistema | **H2** capa de texto primero (`gs txtwrite`, ≥40 palabras); sin CUPS con código o con fallo → imágenes (salvaguarda del dueño) — 637c7bd | Pendiente de medir |
| 2026-11-03 | F1, F6 | **H4** segunda lectura dedicada del código (`ReadCupsCode`, `OPENAI_DOC_MODEL`=gpt-4.1) — 550967b | Solo corre dentro de H5 |
| 2026-11-03 | F1 (códigos descartados en silencio en VALIDATE_OCR) | **H5** cascada en VALIDATE_OCR: segunda lectura → nombre parecido a un dígito → duda al agente (`cups_posible_mala_lectura`) → descarte — 8e9ab3c | Medir con `agendar/cups_corregido` y `agendar/cups_duda` en `flow_events` |
| 2026-11-03 | H2 con PDF de varias hojas (revisión ronda 1) | **H2** capa de texto por hoja (txtwrite, un archivo por hoja) con la instrucción multipágina y «--- HOJA N ---» — 67baede | Tests; una hoja sin cambios |
| 2026-11-03 | Revisión ronda 2: H5 corregía a hermanos reales (7/52 inactivos, 46/268 ausentes, 4,8 % de dobles errores) | **H5** inactivo → descarte sin cascada; segunda lectura que confirma manda; mismo nombre simétrico y estricto (`CupsNamesMatch`); segunda lectura exige el nombre; código ya en la orden → duda; correcciones se borran por orden — cb9957c | Simulación sobre el catálogo real: inactivos corregidos 7→0, ausentes corregidos 46→0 (dudas 137→4), dos dígitos a otro 14.784→0, un dígito de un activo bien corregido 11.375→13.297 de 13.297 (dudas 1.922→0) |
| 2026-11-03 | Revisión ronda 2: PDF mixto y escaneo «buscable» | **H2** ≥20 palabras en CADA hoja (si no, imágenes); el prompt ya no da el texto por «literal y fiable». **H3** si la lectura conjunta no trae código, se prueba la 1.ª hoja — 1294fdc | Tests |
