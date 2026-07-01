# Capa de recuperación con IA — Especificación e instrucciones de implementación

> **Estado:** v3 — decisiones cerradas. Sin tests automatizados (verificación por datos reales); IA solo
> en estados de **texto libre** ambiguo; dashboard enfocado en la **mejor visualización por KPI**.
> **Alcance:** insertar una capa de recuperación con IA **antes** de la escalación humana, **100% interna
> al proyecto**. NO toca agendamiento, OCR ni reglas MRC.
> **Convenciones del proyecto:** identificadores en inglés, comentarios en español
> (ver [[code-identifiers-in-english]]); errores envueltos con `fmt.Errorf("...: %w", err)`;
> DI por constructores (este módulo NO lleva tests automatizados — ver §14). Los nombres/estructuras de este documento
> son **ilustrativos**; se adaptan al código real. El **comportamiento** es obligatorio.

---

## 1. Objetivo

Cuando el paciente responde algo que no encaja en el paso actual de la FSM, una capa de IA intenta
**desbloquear el input** con una conversación corta y acotada. La escalación a un agente humano deja de
ser el primer recurso y pasa a ser el **fallback**: solo se usa si la IA no resuelve.

**Principio rector:** la IA es un **recuperador de input acotado, no un agente autónomo**. No agenda,
no da consejo médico, no inventa datos. Elige entre las opciones que el bot ya calculó o pide una
aclaración concisa. Devuelve una **decisión estructurada** y es el código determinista quien actúa.

---

## 2. Arquitectura: capa 100% interna (decisión clave)

La recuperación con IA **no** usa el canal de comandos `/bot`, **no** escala a Bird y **no** asigna el
ticket a un agente. Todo ocurre dentro del proyecto. Detalle:

- **Inyección del valor recuperado:** mediante el **núcleo de validación existente**,
  `machine.Process(ctx, sess, virtualMsg)` — que pasa el valor por el **mismo handler/validación** que
  si el paciente lo hubiera escrito. **De ahí viene la garantía de seguridad** ("misma validación, sin
  atajos"), NO del string `/bot`. **No** se usa `ParseAgentCommand` ni `handleAgentResume` (esos están
  acoplados a la escalación: `ResumeFromEscalation`, `UnassignFeedItem`, mensaje "Hemos retomado tu
  atención"). Esos artefactos de escalar/retomar **no se emiten** durante la recuperación IA.
- **Mensajes al paciente:** la pregunta aclaratoria de la IA **sí se envía al paciente por WhatsApp**
  (vía `SendText`, igual que cualquier prompt del bot). Como WhatsApp fluye por Bird, **esos mensajes
  aparecen naturalmente en el ticket de Bird** → queda evidenciado allí sin trabajo extra. **No** se
  agrega una nota interna adicional en Bird (se evaluó y no aporta).
- **Validación "del bot primero" sin efectos colaterales:** se llama al **validador puro** del paso
  (`HandlerConfig.TextValidate` / `HandlerConfig.Options`) directamente. NO se re-corre `machine.Process`
  para "probar" (avanzaría / incrementaría retry / enviaría mensajes).
- **Punto de inserción (chokepoint único):** el disparo de escalación vive en 3 validadores
  (`ValidateWithRetry`, `ValidateButtonResponse`, `RetryOrEscalate` en `statemachine/helpers.go`), todos
  terminando en `NewResult(StateEscalateToAgent)`. Como `ESCALATE_TO_AGENT` es **estado automático**
  (`states.go:279`), `Process` lo **auto-encadena** y guarda el estado de origen en `_pre_auto_state`
  (`machine.go:104`). Por eso el enganche limpio es **un guard a la ENTRADA del handler
  `ESCALATE_TO_AGENT`** (un solo punto, cubre los 3 validadores y los handlers que escalan a mano), que
  lee `_pre_auto_state` y decide: derivar al coordinador IA o escalar de verdad. Ver §2.1.

> La escalación real a humano (Bird: asignar feed item, mensajes por default) **sigue intacta** y solo
> se dispara cuando la IA **agota su presupuesto** (fallback final).

### 2.1 Modo de recuperación (cómo "frena" el bot, multi-turno)
Se reutilizan dos mecanismos que YA existen en la FSM: los **interceptores** (`machine.go:48`,
`interceptors.go`) y el hecho de que `ESCALATE_TO_AGENT` es **automático**. El "freno" = un flag de
sesión + dos enganches:

- **Enganche A — primer disparo (3.ª falla):** guard a la **entrada del handler `ESCALATE_TO_AGENT`**.
  Lee `_pre_auto_state` (el estado bloqueado). Condición para derivar a IA (si NO se cumple → escala
  normal):
  `AI_RECOVERY_ENABLED=true` **y** estado de origen es opt-in (`HandlerConfig.AIRecovery`) **y** queda
  presupuesto de la conversación (`ai_recovery_attempts`) **y** **no se alcanzó el tope mensual**
  (`AI_RECOVERY_MONTHLY_LIMIT`, §10/§11) **y** no estamos ya en recovery. Si deriva: pone el flag
  `ai_recovery_active`, **resetea `CurrentState` al estado bloqueado** (`_pre_auto_state`) y corre el
  **consumo #1** (inline, +1 llamada ≤ ~3.5s con reintentos). Si NO deriva: escala de verdad (fila en
  `escalations` + evento `escalated`).
- **Enganche B — turnos siguientes (mientras está congelado):** un **interceptor nuevo
  `AIRecoveryInterceptor`**, registrado **después** de `EscalationKeywordsInterceptor`. Mientras
  `ai_recovery_active` esté puesto, **se queda con el mensaje** → corre el coordinador (validador puro →
  LLM) y devuelve su resultado, de modo que **el handler normal del estado NO se ejecuta** = bot
  "congelado". **`CurrentState` NO cambia** (sigue en el estado bloqueado), así la inyección corre el
  handler correcto.

**Descongelar (lo decide el coordinador):**
- resuelve (validador puro o IA) → inyecta `v` por `machine.Process`, **limpia `ai_recovery_active`** → la
  FSM avanza normal.
- no resuelve y queda presupuesto → envía aclaración, **sigue congelado** (consume intento).
- agota presupuesto → limpia el flag y pasa a `StatusEscalated` → cae en el freno "duro" de hoy
  (`pool.go:535`) + Bird.

**Robustez:** `EscalationKeywordsInterceptor` corre **antes**, así que si el paciente pide "un agente" en
medio del recovery, escala igual. Con `AI_RECOVERY_ENABLED=false` o tope mensual alcanzado, el guard no
deriva → comportamiento **idéntico al actual** (escala a los 3 intentos).

---

## 3. Comportamiento NO negociable

1. **Misma validación, sin atajos.** El valor que la IA propone se inyecta por `machine.Process` y pasa
   por el mismo handler/validación que un input del paciente. Si es inválido, se rechaza igual.
2. **Dos presupuestos separados** (no uno): bot = 3 (actual, no se toca) e IA = N intentos del paciente
   (default 2, configurable por env). El de la IA solo arranca cuando se agota el del bot.
3. **Validación del bot SIEMPRE primero** en cada respuesta del paciente (validador puro, gratis). La IA
   solo entra si la validación determinista falla.
4. **Regla de dominio de seguridad:** la IA nunca infiere datos clínicos sensibles (p. ej. el tipo de
   documento a partir del número). Ante la duda: aclara o deja que se agote el presupuesto y escale.
5. **Capa interna:** sin comandos `/bot`, sin tocar feed items/asignación de Bird, sin los mensajes por
   default de escalar/retomar. (Los mensajes aclaratorios al paciente sí van por WhatsApp.)
6. **La escalación humana sigue funcionando como fallback final** sin cambios de comportamiento.

Todo lo demás (nombres, structs, organización) es **adaptable** a las convenciones del proyecto.

---

## 4. Flujo exacto (dos presupuestos)

### Fase 1 — el bot solo (como hoy, no se modifica)
El paciente tiene **3 intentos** con la validación normal del bot. Si pasa → avanza la FSM. Si falla el
**3.º intento** → muere la fase del bot y arranca la IA (chokepoint de §2).

### Fase 2 — la IA (presupuesto propio: 2 intentos del paciente, configurable)

**Primer consumo de IA — dispara con la 3.ª respuesta errónea.** En **UNA sola llamada** la IA:
- intenta **formatear/interpretar** esa 3.ª respuesta al formato que el bot espera, y
- **redacta un mensaje** más claro que queda **"en reserva"**.

Resultado:
- **Formateó** → el mensaje se **descarta**, se inyecta el valor por `machine.Process` y el bot avanza.
  **Transparente: el paciente nunca ve un mensaje extra.** **NO consume intento.**
- **No formateó** → se **envía** el mensaje redactado al paciente.

**Intento 1 del paciente con la IA.** Respuesta del paciente → (1) **validador puro del bot** (gratis);
(2) si falla, **interpretar con IA**. Si cualquiera resuelve → inyecta y avanza. Si ambas fallan →
**segundo consumo de IA** (mensaje aún más claro, se envía).

**Intento 2 del paciente con la IA.** Respuesta → validador puro primero, luego interpretar con IA. Si
resuelve → avanza. Si **ambas fallan → muere la IA y se escala a agente humano** (fallback real).

**Peor caso antes del humano:** 3 (bot) + 2 (IA) = **5 oportunidades**; la IA hace ≤ 2 llamadas que
redactan mensaje, más las interpretaciones de cada respuesta.

```
3.ª respuesta errónea del bot  →  [Consumo IA #1] interpreta+redacta (1 llamada)
   ├── formateó → descarta msg → machine.Process(valor) → AVANZA (transparente, no cuenta intento)
   └── no formateó → envía msg en reserva (WhatsApp)
        (Intento IA 1) resp paciente → validador puro → IA → ok: AVANZA | falla ambas:
             [Consumo IA #2] redacta msg más claro → envía
             (Intento IA 2) resp paciente → validador puro → IA → ok: AVANZA | falla ambas:
                  ESCALA A HUMANO (Bird, como hoy)
```

---

## 5. Regla de dominio en el formateo (crítica)

Ejemplo `ASK_DOCUMENT_TYPE` (catálogo real `documentTypeCatalog`: 1=CC, 2=TI, 3=RC, 4=CE, 5=PA, …):
- Paciente responde el tipo en palabras ("cédula", "CC") → la IA mapea a la opción válida. ✅
- Paciente responde su **número** → la IA **NO** infiere el tipo desde el número (CC y CE pueden ser
  numéricos; equivocar el tipo en historia clínica es inaceptable). Reconoce que es un número, lo
  **guarda** (`dato_adelantado`) para el paso posterior, y el mensaje pide **primero el tipo**. Ante
  duda: aclara o escala.

> Reutilizar `looksLikeDocNumber()` (ya existe) como pre-filtro para **detectar** que es un número está
> bien; **inferir el tipo** a partir de él, no.

---

## 6. Contrato de decisión (estructura ilustrativa, adaptable)

JSON real que devuelve el LLM (claves cortas, §7.1). Ejemplo "el paciente envió su número en
`ASK_DOCUMENT_TYPE`":

```json
{
  "ok": false,
  "v": "",
  "c": { "numero_documento": "1023456789" },
  "m": "Veo que enviaste tu número de documento, lo guardo. Primero dime el tipo: 1) CC  2) TI  3) CE",
  "r": "num_doc"
}
```

Forma sugerida en Go (adaptar; en la capa interna `comando` es simplemente el **valor a inyectar**, no un
string `/bot`). **Claves cortas para ahorrar tokens de salida** (ver §7.1):

```go
// RecoveryDecision es la salida estructurada del LLM por cada intento de recuperación.
// Tags JSON cortos: cada token de salida cuesta 4× un token de entrada.
type RecoveryDecision struct {
    OK    bool              `json:"ok"`          // formateó con éxito
    Value string            `json:"v"`           // valor a inyectar por machine.Process ("" si no)
    Carry map[string]string `json:"c,omitempty"` // dato_adelantado (opcional)
    Msg   string            `json:"m"`           // mensaje al paciente (claro y breve)
    Reason string           `json:"r"`           // CÓDIGO corto, no prosa: "num_doc"|"ambiguous"|"off_topic"|"empty"|...
}
```

Reglas: `ok == true` → inyectar `v` por `machine.Process`, **descartar** `m`. `ok == false` → **enviar**
`m`. `c` guarda datos extra para pasos posteriores. `r` es un **código** (enum), nunca texto libre — se
usa para KPIs/auditoría y no debe inflar la salida ni contener PII.

---

## 7. Decisiones técnicas

- **Modelo: `gpt-4.1-nano`** (decisión fija) — **distinto al del OCR**. El OCR usa `OPENAI_MODEL`
  (`gpt-4o-mini`); la recuperación usa su **propia** `AI_RECOVERY_MODEL` (`gpt-4.1-nano`). Solo se
  comparten la **API key** (`OPENAI_API_KEY`) y el patrón HTTP del OCR (`ocr_service.go`: `http.Client` a
  `/v1/chat/completions`). **No hay SDK** — es HTTP crudo. **No reutilizar `OPENAI_MODEL`.**
- **Estructura: patrón de estrategia.** Interfaz `RecoveryStrategy` con `AIRecovery` y `HumanEscalation`,
  y un `RecoveryCoordinator` que las prueba en orden (IA → humano fallback). Mantiene la FSM limpia y la
  dependencia del LLM aislada e inyectable.
- **Reutilizar `HandlerConfig` (`config.go`) como fuente de verdad por paso.** Ya expone el validador
  puro (`TextValidate`) y las opciones (`Options`). Se **extiende** con campos de IA (ver §8). Estados
  opt-in que hoy no usan `RegisterWithConfig` se **migran** a él (reestructuración aceptada).

### Caching — DECISIÓN: prompt mínimo, sin caching
Investigado con datos actuales. El prompt caching de OpenAI es automático **pero solo para prefijos
≥ 1024 tokens** (incrementos de 128). `gpt-4.1-nano`: input $0.10/1M, **output $0.40/1M**, input cacheado
$0.025/1M (−75%). Cálculo para nuestro caso (prompt corto, bajo volumen):

| Escenario | Total/llamada aprox. |
|---|---|
| Prompt mínimo (~300 tok in + ~150 out), sin cache | **~$0.00009** |
| Prompt inflado a ~1.250 tok para cachear (con hit) | ~$0.00011 |

**Inflar el prompt para poder cachear sale más caro** que un prompt mínimo (igual pagas el prefijo
grande, solo con descuento). El caching abarata **contexto grande inevitable**, no este caso. Además el
**output cuesta 4× el input**: el ahorro real está en **acortar la salida** (JSON compacto + `max_tokens`
bajo). A volumen realista el gasto total es de **centavos/mes**; los controles son por
**previsibilidad/seguridad**, no por dólares.

### 7.1 Salida mínima + mensaje claro al paciente (ahorro de tokens)
El output cuesta 4× el input, así que se exprime la salida **sin sacrificar claridad del mensaje al
paciente** (lo único que el paciente lee). Reglas del prompt/contrato:

- **JSON compacto:** claves de 1–2 letras (`ok`/`v`/`c`/`m`/`r`), sin campos decorativos. `r` es un
  **código enum**, no prosa. Instruir explícitamente "responde **solo** el JSON, sin texto adicional".
- **`m` (mensaje al paciente): breve pero claro.** Instrucción al modelo: *una sola pregunta*, máximo
  ~2 líneas / ~280 caracteres, en español neutro y amable, que **incluya el formato/opciones exactas
  esperadas** (p. ej. "1) CC  2) TI  3) CE") para maximizar la probabilidad de respuesta correcta. Nada
  de explicaciones largas ni disculpas extensas. La claridad manda sobre la brevedad en `m`; la brevedad
  manda en el resto del JSON.
- **`max_tokens` ~200:** suficiente para un `m` claro + el resto del JSON, pero impide que el modelo se
  extienda.
- **No pedir `razon` en prosa** ni "cadena de pensamiento": solo el código `r`. El razonamiento ocurre
  internamente; no se factura como salida.
- **Pre-filtro y validador puro** evitan llamadas: cuando no hay LLM, no hay output que pagar.

### Reintentos del LLM (fallo/timeout)
Ante error o timeout de la llamada: reintentar **máx. 3 veces** con backoff **0.5s → 1s → 2s** (≤ ~3.5s,
dentro de `processMsgTimeout=2min`). Si los 3 fallan → **escala a humano**.

---

## 8. Controles de costo y tokens (obligatorios)

1. **Dos presupuestos separados.** Bot = 3 (actual). IA = `AI_RECOVERY_MAX_PATIENT_ATTEMPTS` (default 2).
   El 1.er consumo IA (sobre la 3.ª resp del bot) **no cuenta intento si formatea**; cuenta solo cuando
   el paciente responde a un mensaje de la IA y sigue sin lograrse.
2. **`max_tokens` de salida bajo** (~200). JSON corto.
3. **Contexto de entrada mínimo:** contrato del paso (instrucción + opciones válidas) + últimos 1–2
   mensajes. No mandar toda la conversación. (Sin inflar para cachear — ver §7.)
4. **Pre-filtro determinista antes del LLM:** si un match simple (número exacto, keyword, fuzzy) resuelve,
   NO se llama al modelo. Reutilizar validadores/helpers existentes (`looksLikeDocNumber`, parseo de
   opciones).
5. **JSON mode:** `response_format: {type:"json_object"}` para salida compacta y parseable.
6. **Opt-in por paso** (ver §11). Solo los pasos marcados usan IA; sensibles/OCR van directo a humano.
7. **Logging de tokens por recuperación** (entrada, salida, nº de llamadas, resultado, `razon`) vía la
   observabilidad existente (`observability.Emit` / `flow_events`), respetando que **`flow_events` no
   guarda PII** (teléfono enmascarado, sin documento, sin input crudo). Considerar alarma de presupuesto
   mensual en OpenAI.
8. **Tope mensual global (`AI_RECOVERY_MONTHLY_LIMIT`).** Corta el gasto de forma dura: alcanzado el número
   de recuperaciones del mes, la IA deja de actuar y se va directo a agente hasta el siguiente mes
   (contador persistido por `YYYY-MM`; evento `ai_month_cap_reached`). Es independiente del presupuesto
   por conversación (control #1). `0` = sin límite.

---

## 9. Privacidad (Ley 1581)

- Enviar el input del paciente (incl. número de documento) a OpenAI **está autorizado** (mismo proveedor
  que ya procesa imágenes de órdenes en el OCR).
- **Debe evidenciarse en el chat:** ampliar el aviso de tratamiento de datos del **mensaje inicial /
  saludo** para incluir que "podemos usar IA para ayudarte a completar tus datos". En Fase 0 verificar si
  ya existe un aviso de habeas data en `GREETING` para **ampliarlo** (no duplicar). Confirmar si se
  requiere aceptación explícita o basta el aviso informativo.
- El logging (`flow_events`) registra **conteos y `razon`**, nunca el input crudo ni PII.

---

## 10. Variables de entorno (propuesta)

| Variable | Default | Descripción |
|---|---|---|
Solo **3 variables** son configurables (las operativas). El resto son constantes internas fijas en
código (no se tunean en prod).

| Variable | Default | Descripción |
|---|---|---|
| `AI_RECOVERY_ENABLED` | **`true`** | Master switch. `true` = intentar la IA antes de escalar; `false` = pasar **directo al agente** (comportamiento actual). |
| `AI_RECOVERY_MAX_PATIENT_ATTEMPTS` | `2` | Presupuesto de intentos del paciente con la IA (por conversación). |
| `AI_RECOVERY_MONTHLY_LIMIT` | `0` | Tope de **recuperaciones que toma la IA por mes calendario**. Al alcanzarlo, directo al agente hasta el siguiente mes. `0` = sin límite. |

**Fijas en código** (`recovery`): modelo `gpt-4.1-nano` (`DefaultModel`, distinto al del OCR),
`max_tokens` de salida `200` (`DefaultMaxOutputTokens`), y los reintentos del LLM (backoff 0.5/1/2s en
el `LLMClient`). El presupuesto del bot (`BOT_MAX_RETRIES` / el env de reintentos actual) **no se toca**.

> **Dos topes distintos:** `AI_RECOVERY_MAX_PATIENT_ATTEMPTS` limita los intentos **por conversación**;
> `AI_RECOVERY_MONTHLY_LIMIT` limita el total de recuperaciones **por mes** (control de gasto global).

---

## 11. KPIs — "Recuperación asistida por IA" (concepto propio, ≠ escalación)

Es un concepto distinto de la escalación. Nombre interno `ai_recovery`; flujo de observabilidad
`recuperacion`. **Nuevo bloque en el catálogo de `tracer.go`** con outcomes propios:

| Evento | Outcome | Significado |
|---|---|---|
| `ai_recovery_started` | info | arrancó la recuperación (3.º fallo del bot) |
| `ai_recovered` | ok | la IA formateó/interpretó y el bot avanzó |
| `ai_resolved_by_bot` | ok | en fase IA, resolvió el **validador puro** (sin LLM) |
| `ai_clarified` | retry | la IA envió mensaje aclaratorio (no formateó) |
| `ai_failed` | escalated | agotó presupuesto (por conversación) IA → escala a humano |
| `ai_domain_block` | blocked | regla de dominio impidió inferir (p. ej. doc number) |
| `ai_month_cap_reached` | info | se alcanzó `AI_RECOVERY_MONTHLY_LIMIT` → la IA no actúa, va directo a agente |

### Separación de KPIs: IA vs escalación (clean)
Una escalación real se registra en dos lugares, ambos **dentro del handler `ESCALATE_TO_AGENT`**: fila en
la tabla `escalations` (`escalation_repo.go`) y el flow_event `escalated` (flujo `escalacion`). El guard de
la IA va **antes** de ambos, así que la separación es automática:

| Caso | Tabla `escalations` | flow `escalacion` | flow `recuperacion` |
|---|---|---|---|
| IA toma y resuelve | ❌ no | ❌ no | `ai_recovery_started` → `ai_recovered`/`ai_resolved_by_bot` |
| IA intenta y falla → escala | ✅ sí | ✅ `escalated` | `ai_recovery_started` → `ai_failed` |
| Escala directa (no opt-in, OCR/sensible, capa OFF o tope mensual) | ✅ sí | ✅ `escalated` | ❌ (o `ai_month_cap_reached` si fue por tope) |

- **Cuando la IA toma**, NO hay fila en `escalations` ni evento `escalated` → **no contamina** los KPIs de
  escalación / SLA agente / no-show; estos simplemente **bajan** (ese descenso = "escalaciones evitadas").
- **"Intentó IA y falló"** es atribuible: cuenta como escalación real **y** lleva `ai_failed` en el mismo
  `trace_id` (`sess:<id>`) → el dashboard distingue *escalaciones directas* vs *tras intento IA*.
- **Cuidado (Fase 0):** confirmar que ningún KPI de escalación cuente el evento `max_retries_reached`
  (se emite en los validadores **antes** del desvío). Los KPIs de escalación usan la tabla `escalations`,
  así que deberían estar bien.

### Dashboard — mejor visualización por KPI
La meta del panel es **demostrar el valor de la capa** (cuántas escalaciones evita y a qué costo). Para
cada dato, la visualización que mejor lo comunica:

| KPI | Qué queremos demostrar | Visualización recomendada |
|---|---|---|
| **Escalaciones evitadas** (recuperaciones que habrían escalado) | El impacto principal de la capa | **KPI card grande** con total del periodo + **sparkline** de tendencia. Es el número estrella. |
| **Embudo de recuperación**: disparos → resuelto por validador-puro → resuelto por IA → escalado | Cómo se reparte cada disparo y dónde se "cae" | **Funnel chart** (o barras apiladas por etapa). Cuenta la historia completa de un vistazo. |
| **Tasa de recuperación** (recuperadas / disparos) | Eficacia global en el tiempo | **Línea temporal** (%) con meta de referencia. |
| **% resuelto por validador-puro vs LLM** | Cuánto resuelve gratis el pre-filtro vs cuánto cuesta el LLM | **Donut / barra apilada** (composición). |
| **Costo / tokens por recuperación** | Gasto y previsibilidad | **Línea temporal** de tokens (in/out) + **KPI card** de costo estimado total. |
| **Top estados que más disparan / más escalan** | Dónde enfocar mejoras de UX o prompt | **Barras horizontales** (ranking). |

> Principio: cada dato con **una sola** visualización que le dé sentido; nada de gráficas decorativas.
> Card grande para el número estrella, funnel para el reparto, líneas para tendencias, barras para
> rankings, donut para composición.

Implementación: nuevos campos en `internal/kpi` (models/repository/handlers) + página/sección en el
frontend del dashboard, leyendo de `flow_events` (sin PII).

---

## 12. Inventario de estados y opt-in

**Regla de alcance (decisión):** la IA aplica **solo a estados de TEXTO LIBRE donde la respuesta puede
ser ambigua o malinterpretada**. Los estados de **botones/listas seleccionables NO usan IA** (las
opciones son claras y `ValidateButtonResponse` ya acepta número/sinónimo; no dan problema al paciente).
Más los automáticos/internos y los excluidos del negocio.

### NO aplica IA — automáticos/internos y de despliegue (sin input libre)
`CHECK_BUSINESS_HOURS`, `GREETING`, `PATIENT_LOOKUP`, `UPDATE_CONTACT_INFO`, `CHECK_ENTITY`,
`SHOW_ENTITY_LIST`, `FETCH_APPOINTMENTS`, `VALIDATE_OCR`, `CHECK_SPECIAL_CUPS`, `CHECK_EXISTING`,
`CHECK_PRIOR_CONSULTATION`, `CHECK_MRC_LIMIT`, `CHECK_AGE_RESTRICTION`, `SEARCH_SLOTS`,
`CREATE_APPOINTMENT`, `CREATE_PATIENT`, `GFR_RESULT`, `SHOW_RESULTS/LOCATIONS/HELP/CONTACT_INFO`,
`NO_APPOINTMENTS`, `APPOINTMENT_EXISTS`, `*_CONFIRMED/_CANCELLED`, `BOOKING_SUCCESS/FAILED`,
`PREGNANCY_BLOCK`, `GFR_NOT_ELIGIBLE`, `COVERAGE_NO_CONVENIO`, `NO_SLOTS_AVAILABLE`, `SLOT_SEARCH_RETRY`,
`FAREWELL`, `TERMINATED`, `OUT_OF_HOURS`, `ESCALATE_TO_AGENT`, `ESCALATED`.

### EXCLUIDOS explícitamente → directo a humano (decisión del negocio)
- **Orden médica / OCR:** `ASK_MEDICAL_ORDER`, `UPLOAD_MEDICAL_ORDER` (pedir imagen/archivo),
  `OCR_FAILED` (no se pudo leer), `CONFIRM_OCR_RESULT` (el paciente dice que el OCR está mal). **La IA
  NO atiende la orden**; si no se lee o el paciente indica que está mal → **agente humano directo.**
- **Sensibles (IA off, decidido):** `ASK_DOCUMENT` (número de documento — PII, no ambiguo) y
  `CONFIRM_IDENTITY` (identidad; además es botón). → camino actual / humano.

### INCLUIDOS — IA aplica (texto libre ambiguo) · 24 CONFIRMADOS
`ASK_DOCUMENT_TYPE` (**piloto**), `REG_DOCUMENT_TYPE`, `REG_FIRST_NAME`, `REG_SECOND_NAME`,
`REG_FIRST_SURNAME`, `REG_SECOND_SURNAME`, `REG_BIRTH_DATE`, `REG_ADDRESS`, `REG_PHONE`, `REG_PHONE2`,
`REG_EMAIL`, `REG_MUNICIPALITY`, `REG_BARRIO`, `CONFIRM_MUNICIPALITY`, `ASK_UPDATE_PHONE`,
`ASK_UPDATE_EMAIL`, `ASK_GESTATIONAL_WEEKS`, `ASK_BABY_WEIGHT`, `GFR_CREATININE`, `GFR_HEIGHT`,
`GFR_WEIGHT`, `ASK_MANUAL_CUPS`, `MANUAL_PROCEDURE_INPUT`, `CANCEL_REASON`.

#### Formato esperado por estado (la IA debe producir un valor que **pase el validador del bot**)
El valor que devuelve la IA (`v`) se inyecta por `machine.Process` y debe cumplir EXACTAMENTE el formato
que el handler valida. La descripción de abajo va en `HandlerConfig.AIInputHint` y es la **fuente para el
prompt**. **El validador del handler es la fuente de verdad** — estos formatos se confirman/transcriben
contra cada validador en Fase 2 (o se leen en Fase 0).

| Estado | Formato esperado (a inyectar) | Ejemplo | Dato adelantado típico |
|---|---|---|---|
| `ASK_DOCUMENT_TYPE` / `REG_DOCUMENT_TYPE` | número de opción del catálogo **1–12** (mapea a CC/TI/RC/CE/…) | `1` | número de doc → paso posterior |
| `REG_FIRST_NAME` / `REG_FIRST_SURNAME` | una palabra alfabética, sin números/símbolos/espacios | `MARIA` | — |
| `REG_SECOND_NAME` / `REG_SECOND_SURNAME` | una palabra alfabética **o** `NA` | `NA` | — |
| `REG_BIRTH_DATE` | fecha `AAAA-MM-DD` | `1992-04-17` | — |
| `REG_ADDRESS` | texto no vacío (calle y número) | `Cra 10 # 5-20` | — |
| `REG_PHONE` / `ASK_UPDATE_PHONE` | celular de 10 dígitos | `3001234567` | — |
| `REG_PHONE2` | 10 dígitos **o** `NA` | `NA` | — |
| `REG_EMAIL` / `ASK_UPDATE_EMAIL` | email válido **o** `NA` | `ana@mail.com` | — |
| `REG_MUNICIPALITY` / `CONFIRM_MUNICIPALITY` | `Municipio - Departamento` | `Villavicencio - Meta` | — |
| `REG_BARRIO` | nombre de barrio **o** `NA` | `La Esperanza` | — |
| `ASK_GESTATIONAL_WEEKS` | entero de semanas (rango válido) | `28` | — |
| `ASK_BABY_WEIGHT` | número (unidad según handler) | `3.4` | — |
| `GFR_CREATININE` / `GFR_HEIGHT` / `GFR_WEIGHT` | número decimal (unidad del handler) | `1.1` / `170` / `68` | — |
| `ASK_MANUAL_CUPS` | código(s) CUPS numéricos, separados por espacio (`:cantidad` opcional) | `883141 930810:2` | — |
| `MANUAL_PROCEDURE_INPUT` | descripción de procedimiento (texto) | `resonancia de rodilla` | — |
| `CANCEL_REASON` | texto del motivo | `no puedo asistir` | — |

> Implementación: `HandlerConfig.AIInputHint` (descripción + ejemplo) + `AICarryKeys` (dato adelantado →
> contexto de sesión). **Garantía:** si la IA produce un `v` que no cumple, `machine.Process` lo rechaza
> (mismo validador) y se trata como "no formateó" (ver §13, seguridad de inyección).

### EXCLUIDOS — botón/lista seleccionable (NO usan IA, por decisión)
Las opciones ya son claras y `ValidateButtonResponse` acepta número/sinónimo; ante fallo siguen el
camino actual (retry → escalación humana). NO entran a la capa IA:
`MAIN_MENU`, `CONFIRM_CONTACT_INFO`, `REG_GENDER`, `REG_BLOOD_TYPE`,
`REG_MARITAL_STATUS`, `REG_ZONE`, `REG_USER_TYPE`, `REG_AFFILIATION_TYPE`, `REG_CLIENT_TYPE`,
`ASK_CLIENT_TYPE`, `ASK_EPS_REGIMEN`, `REG_SELECT_CORRECTION`, `CONFIRM_REGISTRATION`, `SELECT_PROCEDURE`,
`ASK_CONTRASTED`, `ASK_PREGNANCY`, `ASK_SEDATION`, `CONFIRM_BOOKING`, `RECONFIRM_BOOKING`,
`OFFER_WAITING_LIST`, `LIST_APPOINTMENTS`, `APPOINTMENT_ACTION`, `CONFIRM_APPOINTMENT`,
`CANCEL_APPOINTMENT`, `CONFIRM_ENTITY`, `CHANGE_ENTITY`, `ASK_ENTITY_NUMBER`, `POST_ACTION_MENU`,
`OUT_OF_HOURS_MENU`, `CONFIRM_RESCHEDULE_NOTIF`, `CONFIRM_CANCEL_NOTIF`, `NOTIF_PENDING`,
`NOTIF_RESCHEDULE_FALLBACK`.

> **Único grupo en alcance: texto libre (Grupo B).** El opt-in real se controla por estado vía
> `HandlerConfig.AIRecovery`. Se arranca **solo con `ASK_DOCUMENT_TYPE`** end-to-end y luego se replica a
> los demás de texto libre.

### Extensión propuesta de `HandlerConfig`
```go
type HandlerConfig struct {
    // ... campos actuales ...
    AIRecovery  bool                       // opt-in de la capa IA para este estado
    AIInputHint string                     // descripción de input válido (para el prompt)
    AICarryKeys map[string]string          // claves de dato_adelantado → context de sesión
}
```

---

## 13. Notas de implementación contra el código real

- **`/bot` real es `/bot resume PASO valor`** (`ParseAgentCommand`). **No se usa** en esta capa (interna).
- **`handleAgentResume` está acoplado a escalación** (`ResumeFromEscalation`, `UnassignFeedItem`, mensaje
  "retomado"). **No reutilizar.** La inyección limpia es `machine.Process(virtualMsg)`.
- **El presupuesto IA NO puede usar `sess.RetryCount`** (se comparte/resetea entre estados). Usar contador
  propio en contexto de sesión (`ai_recovery_attempts`, sin migración) — confirmado.
- **Chokepoint = guard a la ENTRADA del handler `ESCALATE_TO_AGENT`** (no editar los 3 validadores).
  Lee `_pre_auto_state` (origen). Al derivar a IA: **resetear `sess.CurrentState = _pre_auto_state`**
  (deshacer el cambio que hizo el auto-chain en `machine.go:107`) para quedar congelado en el estado
  bloqueado. Turnos siguientes: **`AIRecoveryInterceptor`** registrado en `RegisterInterceptors`
  **después** de `EscalationKeywordsInterceptor`.
- **Validador puro** desde `HandlerConfig.TextValidate` / `Options` (sin re-correr `Process`).
- **Contador mensual (`AI_RECOVERY_MONTHLY_LIMIT`):** se cuenta por **mes calendario** las recuperaciones
  que toma la IA (incrementar al activar recovery = `ai_recovery_started`). Para no escanear `flow_events`
  en el hot-path, mantener un **contador persistido atómico por `YYYY-MM`** (tabla pequeña o fila de
  config) que se consulta en el guard; se reconcilia con `flow_events`. Al alcanzar el tope: emitir
  `ai_month_cap_reached` y escalar directo (no se activa recovery).
- **Seguridad de inyección (anti-loop):** tras inyectar `v` por `machine.Process`, **verificar que el
  estado avanzó** (NextState ≠ estado bloqueado). Si la IA produjo un `v` que igual NO pasó el validador
  (alucinación), el resultado NO avanza → se trata como **"no formateó"** (consume intento / envía
  aclaración), nunca se reintenta en bucle ni se confía ciegamente en `ok=true`.
- **Dato adelantado:** se guarda en contexto de sesión según `HandlerConfig.AICarryKeys` (p. ej. número
  de documento que el paciente escribió en `ASK_DOCUMENT_TYPE` → pre-llena el paso posterior). Es
  **dato provisto por el paciente**, no inferido (no viola la regla de dominio). No se loguea en
  `flow_events`. **Caveat:** si el paso posterior es sensible (`ASK_DOCUMENT`), el dato pre-llena pero
  **NO auto-salta la confirmación** de ese paso — el bot lo presenta para que el paciente lo confirme
  (decisión a afinar en Fase 2).
- **Conteo de "escalaciones evitadas" (KPI):** se cuenta **+1 por cada recuperación que termina en
  avance** (`ai_recovered` o `ai_resolved_by_bot` en fase IA), porque el presupuesto del bot ya estaba
  agotado y sin la capa habría ido a `ESCALATE_TO_AGENT`. `ai_failed` NO cuenta (sí escaló).
- **trace_id:** los eventos de recuperación usan `TraceSession(sess.ID)` (mismo hilo de la sesión).

---

## 14. Verificación (sin tests automatizados)

**Decisión:** no se escriben tests automatizados para este módulo. La validación es por **observación de
datos reales** tras implementar y desplegar (filosofía "verify": correr el flujo real y observar la
salida). Lo único que sigue siendo obligatorio es que el **hook de pre-commit (gofumpt + lint-new)** quede
verde.

**Cómo se verifica que funciona y que tenemos datos:**
1. **Driblar el flujo real** en el estado piloto (`ASK_DOCUMENT_TYPE`): provocar las respuestas ambiguas
   (escribir "cédula", escribir el número de documento, texto sin sentido) y observar por WhatsApp que la
   IA formatea/avanza transparente o envía el mensaje aclaratorio, y que al agotar presupuesto escala.
2. **Comprobar la persistencia de KPIs:** consultar `flow_events` (endpoints internos
   `/api/internal/flow-events`, `/flow-trace`) y confirmar que se registran los eventos del catálogo
   `recuperacion` con su outcome, **sin PII**, y con los tokens logueados.
3. **Comprobar el dashboard:** confirmar que cada KPI aparece con su visualización (§11) y que el número
   mostrado **coincide** con lo que hay en `flow_events` (verificación extremo a extremo, manual).
4. Dejar registro de la verificación (capturas / muestras de `flow_events`) al cerrar cada fase.

> Si más adelante se quiere blindar la regla de dominio (no inferir tipo desde número), puede añadirse un
> test puntual; por ahora, fuera de alcance por decisión.

---

## 15. Fases de trabajo (con puntos de parada)

**Fase 0 — Descubrimiento (NO escribir código).** Confirmar contra el código: chokepoint exacto, dónde
colocar el coordinador, contador de presupuesto IA en sesión, aviso de habeas data en `GREETING`, cliente
OpenAI del OCR a reutilizar. **Detenerse y confirmar el plan.**

**Fase 1 — Interfaz y coordinador.** `RecoveryStrategy`, `RecoveryCoordinator`, `RecoveryDecision`,
extensión de `HandlerConfig` (opt-in + hint + carry-keys). Sin IA: esqueleto + `HumanEscalation` que
envuelve el comportamiento actual **sin cambiarlo**. **Detenerse.**

**Fase 2 — Estrategia de IA.** `AIRecovery` con el flujo de dos presupuestos: una llamada que interpreta +
redacta en reserva; descarta si formatea, envía si no; presupuesto IA propio; **validador puro primero**
en cada respuesta. `gpt-4.1-nano` con JSON mode y `max_tokens` bajo, salida mínima (§7.1). Implementar
`ASK_DOCUMENT_TYPE` con su regla de dominio. **Verificar driblando el flujo real** (§14): formatea-avance
transparente, envía-mensaje, resuelve-por-bot, resuelve-por-IA, escala, regla-de-dominio. **Detenerse.**

**Fase 3 — Integración, KPIs y controles.** Conectar el coordinador al chokepoint real. Logging de tokens
+ bloque de catálogo `recuperacion` en `tracer.go` + KPIs en `internal/kpi`. Verificar todos los controles
de §8. Ampliar el aviso de habeas data. **Verificar en `flow_events`** (§14): eventos con su outcome, sin
PII, tokens logueados. **Detenerse y mostrar resumen de lo conectado.**

**Fase 4 — Dashboard y gráficas.** Endpoints/handlers de KPIs + página/sección en el frontend que
**grafica cada KPI con la visualización recomendada (§11)**. **Verificar extremo a extremo** que el número
de cada gráfica coincide con `flow_events`. **Detenerse y mostrar el dashboard.**

---

## 16. Reglas

- No cambiar agendamiento, OCR ni reglas MRC. Solo la capa de recuperación.
- La IA nunca infiere datos clínicos sensibles. Ante la duda, aclara o escala.
- La orden médica (imagen/OCR ilegible/OCR marcado como erróneo por el paciente) **no la atiende la IA**:
  va directo a humano.
- Cualquier acción destructiva o cambio de dependencias requiere confirmación del usuario.
- Mantener `gofumpt`/lint **verde** en cada cambio (hook: format-check + lint-new).

---

## 17. Definición de "terminado"

- Coordinador con dos estrategias; `AIRecovery` implementa el flujo de dos presupuestos (3 bot + 2 IA),
  una llamada que interpreta + redacta en reserva, descarta al formatear (avance transparente) y envía si
  no; valida con el validador puro del bot antes de interpretar; inyecta por `machine.Process` (sin
  comandos, sin Bird, sin mensajes de escalar/retomar).
- Usa `gpt-4.1-nano` con JSON mode; respeta la regla de dominio (no inferir tipo desde número).
- Reintentos del LLM (3×, backoff 0.5/1/2s) y fallback a humano.
- Todos los controles de §8; prompt mínimo sin caching (§7).
- **Switches por env:** `AI_RECOVERY_ENABLED` (default `true`; `false` = directo a agente) y
  `AI_RECOVERY_MONTHLY_LIMIT` (tope mensual de recuperaciones; al alcanzarlo, directo a agente) —
  ambos verificados, con el contador mensual persistido y el evento `ai_month_cap_reached`.
- Estados excluidos respetados (orden/OCR y sensibles → humano directo).
- Escalación humana intacta como fallback final.
- **Salida del LLM mínima** (JSON de claves cortas, `r` como código, `max_tokens` bajo) **y mensaje `m`
  claro** (una pregunta, opciones/formato exactos) — §7.1.
- **Alcance respetado:** IA solo en estados de **texto libre** ambiguo; botones/listas, automáticos,
  orden/OCR y sensibles **no usan IA** (§12).
- **Sin tests automatizados** (decisión); **verificación por datos reales** hecha y registrada (§14):
  flujo piloto driblado, eventos en `flow_events` (con outcome, sin PII, tokens), y dashboard cuadrando
  extremo a extremo con `flow_events`.
- **Hook de pre-commit (gofumpt + lint-new) verde.**
- KPIs de "Recuperación asistida por IA" en observabilidad y dashboard, **cada uno con la visualización
  que mejor lo demuestra** (§11).
- No se tocó la lógica de negocio fuera de la capa de recuperación.
