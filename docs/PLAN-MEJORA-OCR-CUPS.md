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
| — | — | — | — |
