package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// maxOCRPages acota cuántas páginas de un PDF se mandan a Vision en su única llamada (costo/tamaño).
const maxOCRPages = 10

// defaultAnalyzeTimeout es el presupuesto propio de UN análisis OCR. DEBE quedar holgadamente por
// debajo del presupuesto por mensaje del worker (processMsgTimeout = 2 min).
//
// Bug de producción 2026-07-27: el OCR corría con el contexto COMPLETO del mensaje y sin cota propia
// podía consumirlo entero — el peor caso son 3 intentos × 60s de timeout HTTP, más el download, más el
// fallback multipágina. Cuando eso pasaba, TODO lo posterior (SaveState, chat_events, inbox MarkDone)
// fallaba en cascada con "context deadline exceeded": el paciente perdía el estado de su sesión y, como
// la goroutine seguía tomando el lock del teléfono los 2 minutos, su mensaje siguiente moría en
// phone_lock_timeout. Con cota propia el OCR se rinde solo, el handler emite ocr_timeout y le ofrece
// reintentar, y sobra contexto para persistir y responder.
const defaultAnalyzeTimeout = 75 * time.Second

type OCRService struct {
	apiKey string
	model  string // gpt-4o-mini
	// docModel es el modelo de la segunda lectura del código (ReadCupsCode, OPENAI_DOC_MODEL): corre
	// solo cuando el código leído no sirve, y por eso puede ser uno más fuerte. Vacío = model.
	docModel      string
	apiURL        string // default: https://api.openai.com/v1/chat/completions
	client        *http.Client
	cupsContext   string // CUPS reference table injected into the prompt
	birdAccessKey string // Bird API key for downloading media files
	// Néctar: adjuntos de GET /api/bot/attachments/{id}; solo se adjuntan si el anfitrión coincide.
	nectarHost          string
	nectarScheme        string // el esquema también cuenta: http al mismo host no recibe credenciales
	nectarServiceKey    string
	nectarRequestSecret string
	// analyzeTimeout es el techo de UN análisis completo (download + Vision + reintentos). 0 = default.
	analyzeTimeout time.Duration
	// mediaRetryDelays son las esperas entre reintentos de la descarga de un adjunto de Néctar ante
	// un 404 o un 500 (ver downloadMedia). nil = defaultMediaRetryDelays; inyectable para que los tests no
	// esperen los ~17 s reales.
	mediaRetryDelays []time.Duration
	// pdfToImages convierte hojas de un PDF a imágenes (Ghostscript en producción); inyectable para
	// probar sin gs qué hojas van a Vision.
	pdfToImages func(ctx context.Context, pdfPath string, first, last int) ([]string, error)
	// pdfText extrae la capa de texto de un PDF, una entrada por hoja (Ghostscript txtwrite en
	// producción); inyectable.
	pdfText func(ctx context.Context, pdfPath string) []string
}

func NewOCRService(apiKey, model, cupsContext, birdAccessKey string) *OCRService {
	s := &OCRService{
		apiKey:         apiKey,
		model:          model,
		apiURL:         "https://api.openai.com/v1/chat/completions",
		cupsContext:    cupsContext,
		birdAccessKey:  birdAccessKey,
		analyzeTimeout: defaultAnalyzeTimeout,
		pdfToImages:    pdfPagesToDataURIs,
		pdfText:        pdfTextLayer,
	}
	s.client = &http.Client{Timeout: 60 * time.Second, CheckRedirect: s.checkRedirect}
	return s
}

// NewOCRServiceForTest creates an OCRService pointing at a custom URL (for httptest).
func NewOCRServiceForTest(apiURL string) *OCRService {
	s := &OCRService{
		apiURL:         apiURL,
		model:          "test-model",
		analyzeTimeout: defaultAnalyzeTimeout,
		pdfToImages:    pdfPagesToDataURIs,
		pdfText:        pdfTextLayer,
	}
	s.client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: s.checkRedirect}
	return s
}

// checkRedirect no sigue un 3xx de Néctar: la petición lleva su clave de servicio y su firma, y Go
// solo quita Authorization y Cookie al cambiar de dominio, no las cabeceras propias. Néctar sirve
// el adjunto él mismo y no redirige. Lo demás sigue como siempre (la media de Bird puede redirigir
// a un almacenamiento pre-firmado; su Authorization la quita Go al salir de su dominio).
func (s *OCRService) checkRedirect(_ *http.Request, via []*http.Request) error {
	if s.isNectarURL(via[0].URL) {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// SetDocumentModel fija el modelo de la segunda lectura del código (OPENAI_DOC_MODEL).
func (s *OCRService) SetDocumentModel(model string) { s.docModel = strings.TrimSpace(model) }

// SetAnalyzeTimeout ajusta el presupuesto propio de un análisis (<=0 restaura el default).
func (s *OCRService) SetAnalyzeTimeout(d time.Duration) {
	if d <= 0 {
		d = defaultAnalyzeTimeout
	}
	s.analyzeTimeout = d
}

// withAnalyzeBudget acota el contexto de UN análisis. Es un TECHO, nunca una extensión: si al mensaje
// le queda menos tiempo que el presupuesto, manda el del mensaje (context.WithTimeout ya toma el
// mínimo con el deadline del padre).
func (s *OCRService) withAnalyzeBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	d := s.analyzeTimeout
	if d <= 0 {
		d = defaultAnalyzeTimeout
	}
	//nolint:gosec // G118: el cancel se DEVUELVE al llamador, que lo invoca con defer (patrón estándar
	// de un helper que acota un contexto).
	return context.WithTimeout(ctx, d)
}

type OCRResult struct {
	Success  bool
	Cups     []CUPSEntry
	Entity   string
	Notes    string
	Error    string
	Document string // Documento del paciente extraído (solo dígitos)
}

type CUPSEntry struct {
	Code         string `json:"cups_code"`
	Name         string `json:"cups_name"`
	Quantity     int    `json:"quantity"`
	IsSedated    bool   `json:"is_sedated"`
	IsContrasted bool   `json:"is_contrasted"`
	Observations string `json:"observations"`
}

type CUPSGroup struct {
	ServiceType string      `json:"service"`
	Cups        []CUPSEntry `json:"cups"`
	Espacios    int         `json:"espacios"`
}

// BuildCupsContext constructs the CUPS reference table string from a list of procedures.
// Call this once at startup and pass the result to NewOCRService.
func BuildCupsContext(codes []struct{ Code, Name string }) string {
	var sb strings.Builder
	for _, p := range codes {
		fmt.Fprintf(&sb, "- CUPS: %s, Descripción: %s\n", p.Code, p.Name)
	}
	return sb.String()
}

const ocrSystemPrompt = `Eres un extractor de datos de órdenes médicas colombianas. Devuelve SIEMPRE y SOLO un JSON válido (sin texto extra, sin bloques de código markdown).

FORMATO DE SALIDA:
{
  "documento": "<solo_digitos|null>",
  "cups": [
    {
      "cups_code": "<4-6_digitos>",
      "cups_name": "<descripcion_exacta_de_la_orden>",
      "quantity": <int>,
      "is_sedated": <boolean>,
      "is_contrasted": <boolean>,
      "observations": "<string>"
    }
  ],
  "entity": "<nombre_EPS_o_entidad|null>",
  "notes": "<observaciones_relevantes>"
}

REGLAS GENERALES:
- Devuelve SOLO JSON válido, sin texto adicional.
- Usa el texto tal como aparece en la orden; no inventes ni normalices descripciones salvo correcciones OCR mínimas.
- Si un dato no está visible, usa null (o 1 en quantity si no hay número).

DOCUMENTO DEL PACIENTE (solo dígitos):
- Extrae el número de identificación y deja SOLO dígitos.
- Elimina prefijos y texto como: CC, TI, CE, RC, N°, No., guiones y espacios.
- Ejemplos: "CC - 19262024" → "19262024"; "TI: 102-345" → "102345".
- Si no es visible, usa null.

PROCEDIMIENTOS — PROCESO DE DECISIÓN (OBLIGATORIO):
REGLA CERO — ANTI-ALUCINACIÓN (tiene prioridad sobre todo lo demás):
- Solo incluye en el resultado procedimientos que estén CLARAMENTE VISIBLES en la imagen.
- NUNCA uses el contenido de los ejemplos de estas instrucciones como si fuera contenido del documento.
- Si un código o nombre no es legible con certeza, OMITE ese procedimiento del resultado.
- Un código que NO está en la LISTA DE REFERENCIA NO es motivo para inventar otro que sí esté. Devuelve el código y nombre tal como aparecen en la imagen, aunque no estén en la lista.
- REGLA CRÍTICA: Si el código visible en la imagen NO existe en la LISTA DE REFERENCIA, devuélvelo EXACTAMENTE como aparece con el nombre de la orden. NUNCA lo reemplaces por otro código de la lista aunque la descripción parezca similar.

1) PRIORIDAD ABSOLUTA: CÓDIGO EN LA ORDEN
   - Si en la fila del procedimiento existe un número de 4 a 6 dígitos, ese ES el cups_code.
   - Si el código tiene cualquier sufijo tras un guion (ej: "891509-16", "891509-1", "891509-4"), usa SOLO los dígitos del código base: "891509". El número del sufijo indica cantidad; captúrala usando (#N) o la regla 2, nunca en cups_code.
   - Copia la descripción tal cual de la orden en cups_name.
   - VERIFICACIÓN CRÍTICA DE DÍGITOS: Los códigos CUPS son identificadores únicos donde cada dígito importa. Dígitos que suelen confundirse: 0/6, 3/8, 1/7, 2/4, 5/6. Verifica cada dígito individualmente.
     TABLA DE ESPECIALIDADES 890xxx — usa esta tabla para verificar que el código que leíste corresponde a la especialidad descrita en la orden:
       * 890264 = FISIATRIA (MEDICINA FISICA Y REHABILITACION) PRIMERA VEZ
       * 890274 = NEUROLOGÍA adultos PRIMERA VEZ  ← si la orden dice "NEUROLOGÍA" sin "PEDIÁTRICA", termina en 74, NO en 75
       * 890275 = NEUROLOGÍA PEDIÁTRICA PRIMERA VEZ  ← si la orden dice "NEUROLOGÍA PEDIÁTRICA", termina en 75, NO en 74
       * 890364 = FISIATRIA (MEDICINA FISICA Y REHABILITACION) CONTROL
       * 890374 = NEUROLOGÍA adultos CONTROL  ← si la orden dice "NEUROLOGÍA" sin "PEDIÁTRICA", termina en 74, NO en 75
       * 890375 = NEUROLOGÍA PEDIÁTRICA CONTROL  ← si la orden dice "NEUROLOGÍA PEDIÁTRICA", termina en 75, NO en 74
       * 890284 = PSIQUIATRÍA PRIMERA VEZ — código válido aunque NO esté en la lista de referencia, devuélvelo tal cual
       * 890308 = PSICOLOGÍA CONTROL — código válido aunque NO esté en la lista de referencia, devuélvelo tal cual
   - VERIFICACIÓN CRUZADA OBLIGATORIA: Después de leer el código, compara la especialidad del código con la especialidad mencionada en la descripción de la orden.
     * Si NO coinciden → has leído mal el código. Usa el código de la imagen tal cual Y cups_name = descripción exacta de la orden (no uses el nombre de la lista).
     * Ejemplo: si el código apunta a "MEDICINA FISICA" pero la descripción dice "PSICOLOGÍA" → el código correcto es 890308, devuélvelo con el nombre de la orden.

2) SOLO SI NO HAY CÓDIGO EN LA ORDEN:
   - Compara la descripción de la orden con la LISTA DE REFERENCIA (al final).
   - Elige el CUPS con la coincidencia más fuerte y específica.
   - cups_name se mantiene como la leída en la orden (no reemplazar por la de la lista).

3) SI NO HAY CÓDIGO NI COINCIDENCIA:
   - Pon cups_code vacío ("") y cups_name con la descripción leída en la orden.

DATOS POR PROCEDIMIENTO:
- quantity: entero. REGLAS DE CANTIDAD (en orden de prioridad):
  1. Si la fila contiene "(#N)" (ej: "(#4)", "(#16)"), usa ese N como quantity.
  2. Si no hay (#N) pero hay un número explícito de sesiones/extremidades/nervios al final (ej: "/ 4 EXTREMIDADES", "4 SESIONES"), usa ese número.
  3. Si no hay ningún indicador de cantidad, usa 1.
  IMPORTANTE: En órdenes colombianas, (#N) es la notación estándar para indicar la cantidad del procedimiento. Siempre tiene prioridad sobre cualquier otro texto numérico en la misma fila.
  CASO CRÍTICO EMG/NEUROCONDUCCIÓN: En filas de EMG y NC, "#N" y "extremidades" son conceptos distintos.
  - "ELECTROMIOGRAFÍA (#4) / 4 EXTREMIDADES" → quantity=4 (del #4). Las 4 extremidades indican dónde se aplica, no cuántas sesiones.
  - "NEUROCONDUCCIÓN (#16) / 4 EXTREMIDADES" → quantity=16 (del #16). Son 4 nervios × 4 extremidades = 16 estudios.
  NUNCA uses el número de extremidades como quantity cuando haya un (#N) en la misma fila.
- observations: texto adicional o marcadores del procedimiento como "AMB", "SUPERIORES", "BILATERAL", lateralidad, etc. Aplica corrección OCR mínima (ej: "CIN"→"SIN"). Si no hay observaciones, usa "".
- is_sedated: indica si ESE procedimiento se realiza BAJO SEDACIÓN/ANESTESIA. Decide en este orden:
  1) NEGACIÓN primero: si dice "SIN sedación", "SIN anestesia", "NO requiere sedación", "no sedado/sedada"
     (o equivalente) para ese procedimiento → false, AUNQUE luego aparezca la palabra suelta.
  2) Señales afirmativas en descripción u observaciones: "sedación", "sedacion", "bajo sedación",
     "bajo anestesia", "con anestesia", "anestesia general", "sedado", "sedada", "anestesiado", "anestesiada" → true.
  3) Si no hay ninguna señal → false.
- is_contrasted: indica si ESE examen usa MEDIO DE CONTRASTE YODADO O PARAMAGNÉTICO. Decide en este orden (lo de arriba manda):
  0) REGLA PREVIA ABSOLUTA — estudios que NUNCA usan contraste yodado, independientemente de lo que diga la orden:
     - PET/CT o PET-TC (tomografía por emisión de positrones): usan RADIOTRAZADORES (18-F FDG, PSMA, etc.), NO contraste.
       "CON 18-FLUOR", "CON PSMA", "CON FDG", "CON F18" en el nombre → is_contrasted = false SIEMPRE.
  1) POR NOMBRE DEL PROCEDIMIENTO (lo más confiable):
     - Contiene "CON CONTRASTE" o "SIMPLE Y CON CONTRASTE" → true (el estudio ya es contrastado por definición).
     - Contiene "SIMPLE" (sin "CON CONTRASTE"), o "SIN contraste", "SIN medio de contraste", "NO contrastado" → false.
  2) Si el nombre no lo define, busca señales de contraste en descripción u observaciones:
     "contraste", "contrastado", "contrastada", "con medio de contraste", "medio de contraste",
     "c/ contraste", "c/medio", "medio de contraste EV", "medio de contraste IV", "gadolinio", "gadolinio EV",
     "yodo", "yodado", "con gd" → true. Pero si vienen NEGADAS ("SIN contraste", "NO requiere contraste") → false.
  3) Si no hay ninguna señal → false.

DETECCIÓN DE ENTIDAD:
- Extraer del logo o texto el nombre de la EPS/entidad.
- Si detectas "Capital Salud" o "capitalsalud": entity = "Capital Salud".

REGLAS CAPITAL SALUD (solo si se detecta logo/texto "Capital Salud"):
- Documento: está en la línea "TRABAJADOR:" o "PACIENTE:".
  Buscar tipo-doc (CC|TI|CE|RC|AS|CD|CN|MS|NI|NU|NV|PA|PE|PT) seguido del número.
  Devolver SOLO dígitos, longitud 6-12. Nunca usar números del encabezado sin tipo-doc.
- Ignorar datos del pie de página (firmas, "VÁLIDO HASTA", "AUTORIZA", timestamps).
- EDAD: "xx MES/MESES" → considerar edad 0; "xx AÑO/AÑOS" → xx.

INTERCONSULTAS (solicitudes de remisión):
- Si la imagen es una SOLICITUD DE INTERCONSULTA o REMISIÓN (no una orden con tabla de CUPS):
  Busca el servicio solicitado en los campos "Interconsulta a:", "Servicio referente:", "Especialidad:" o similares.
  Compara ese servicio con la LISTA DE REFERENCIA y asigna el cups_code correspondiente.
  cups_name = el nombre del servicio tal como aparece en el documento.
  Si hay dos CUPS para la misma especialidad (primera vez y control), elige SIEMPRE el de "primera vez" o "1ª vez",
  ya que las interconsultas son casi siempre consultas iniciales.
  Si no hay coincidencia en la lista, pon cups_code = "" y cups_name con el servicio leído.
  NO devuelvas error "no_table_detected" por ser interconsulta — intenta extraer el servicio.

SIN TABLA DE PROCEDIMIENTOS:
- Si la imagen no es una orden médica ni una interconsulta, o no tiene ningún procedimiento o servicio identificable:
  Responder: {"cups": [], "error": "no_table_detected"}
- Si la imagen está borrosa o es ilegible:
  Responder: {"cups": [], "error": "imagen_borrosa"}

Recuerda: SOLO JSON válido como salida.`

// buildSystemPrompt constructs the system prompt with optional CUPS reference table.
func (s *OCRService) buildSystemPrompt() string {
	systemPrompt := ocrSystemPrompt
	if s.cupsContext != "" {
		systemPrompt += "\n\nLISTA DE REFERENCIA DE PROCEDIMIENTOS (usar para matching cuando no hay código visible):\n" + s.cupsContext
	}
	return systemPrompt
}

// callOpenAI sends a request to OpenAI and parses the OCR-style response.
// Shared by AnalyzeImage and AnalyzeText.
func (s *OCRService) callOpenAI(ctx context.Context, messages []map[string]interface{}) (*OCRResult, error) {
	content, err := s.callOpenAIRaw(ctx, s.model, messages)
	if err != nil {
		return nil, err
	}
	if content == "" {
		return &OCRResult{Success: false, Error: "sin respuesta de OpenAI"}, nil
	}
	return parseOrderResponse(content), nil
}

// callOpenAIRaw hace la llamada con el modelo elegido y devuelve el contenido de la respuesta tal cual
// ("" si no trajo ninguna opción). Comparte transporte, reintentos y manejo de errores entre la lectura
// de la orden y la segunda lectura del código, que tienen contratos de salida distintos.
func (s *OCRService) callOpenAIRaw(ctx context.Context, model string, messages []map[string]interface{}) (string, error) {
	reqBody := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"max_tokens":  1200,
		"temperature": 0,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	const maxRetries = 2 // 3 total attempts

	var resp *http.Response
	var respBody []byte

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			req, err = http.NewRequestWithContext(ctx, "POST", s.apiURL, bytes.NewReader(bodyBytes))
			if err != nil {
				return "", fmt.Errorf("create retry request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s.apiKey)
		}

		resp, err = s.client.Do(req)
		if err != nil {
			if attempt == maxRetries {
				return "", fmt.Errorf("openai request after %d attempts: %w", attempt+1, err)
			}
			delay := time.Duration((attempt+1)*(attempt+1)) * time.Second
			slog.Warn("openai_retry_network", "attempt", attempt+1, "delay", delay, "error", err)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return "", fmt.Errorf("openai retry cancelled: %w", ctx.Err())
			}
			continue
		}

		var readErr error
		respBody, readErr = io.ReadAll(resp.Body)
		resp.Body.Close()
		// L5: un body truncado (reset a mitad) con status 200 dejaba respBody parcial → fallo confuso
		// de Unmarshal. Tratarlo como error de transporte y reintentar, igual que el error de red.
		if readErr != nil {
			if attempt == maxRetries {
				return "", fmt.Errorf("openai read body after %d attempts: %w", attempt+1, readErr)
			}
			delay := time.Duration((attempt+1)*(attempt+1)) * time.Second
			slog.Warn("openai_retry_read_body", "attempt", attempt+1, "delay", delay, "error", readErr)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return "", fmt.Errorf("openai retry cancelled: %w", ctx.Err())
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		// 429 rate limit — respect Retry-After
		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt == maxRetries {
				slog.Error("openai rate limited after retries", "attempts", attempt+1)
				return "", fmt.Errorf("openai rate limited after %d attempts", attempt+1)
			}
			delay := parseRetryAfterHeader(resp.Header.Get("Retry-After"))
			slog.Warn("openai_retry_rate_limited", "attempt", attempt+1, "retry_after", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return "", fmt.Errorf("openai retry cancelled: %w", ctx.Err())
			}
			continue
		}

		// 5xx server error — retry with backoff
		if resp.StatusCode >= 500 {
			if attempt == maxRetries {
				slog.Error("openai server error after retries", "status", resp.StatusCode, "attempts", attempt+1)
				return "", fmt.Errorf("openai api status %d after %d attempts", resp.StatusCode, attempt+1)
			}
			delay := time.Duration((attempt+1)*(attempt+1)) * time.Second
			slog.Warn("openai_retry_server_error", "attempt", attempt+1, "status", resp.StatusCode, "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return "", fmt.Errorf("openai retry cancelled: %w", ctx.Err())
			}
			continue
		}

		// 4xx client error (not 429) — no retry. No logueamos el body crudo: puede traer PII de
		// salud (cédula/CUPS/EPS de la orden) y este ERROR se reenvía a Telegram (N-7).
		slog.Error("openai api error", "status", resp.StatusCode, "body_len", len(respBody))
		return "", fmt.Errorf("openai api status %d", resp.StatusCode)
	}

	// Parsear respuesta de OpenAI
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal api response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", nil
	}
	return apiResp.Choices[0].Message.Content, nil
}

// parseOrderResponse interpreta el JSON de la lectura de una orden.
func parseOrderResponse(content string) *OCRResult {
	// Extraer JSON del contenido (puede venir con markdown ```json ... ```)
	jsonStr := extractJSON(content)

	var parsed struct {
		Cups     []CUPSEntry `json:"cups"`
		Entity   string      `json:"entity"`
		Notes    string      `json:"notes"`
		Error    string      `json:"error"`
		Document *string     `json:"documento"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		// No logueamos `content` crudo (PII de la orden médica); solo su longitud para diagnóstico (N-7).
		slog.Warn("ocr json parse failed", "content_len", len(content), "error", err)
		return &OCRResult{Success: false, Error: "no se pudo interpretar la respuesta"}
	}

	if parsed.Error != "" {
		return &OCRResult{Success: false, Error: parsed.Error, Entity: parsed.Entity}
	}

	// Asegurar quantity >= 1
	for i := range parsed.Cups {
		if parsed.Cups[i].Quantity < 1 {
			parsed.Cups[i].Quantity = 1
		}
	}

	doc := ""
	if parsed.Document != nil {
		doc = *parsed.Document
	}

	return &OCRResult{
		Success:  len(parsed.Cups) > 0,
		Cups:     parsed.Cups,
		Entity:   parsed.Entity,
		Notes:    parsed.Notes,
		Document: doc,
	}
}

// visionImagePart es la parte image_url de un mensaje a Vision. Pide detail=high: sin él OpenAI elige el
// detalle y con el bajo reduce la foto a 512 px, y los dígitos del código CUPS quedan ilegibles (medido
// en hdg-bot, conv. 57). Cuesta más tokens por imagen; toda imagen que sale a Vision pasa por aquí.
func visionImagePart(url string) map[string]interface{} {
	return map[string]interface{}{
		"type":      "image_url",
		"image_url": map[string]string{"url": url, "detail": "high"},
	}
}

// AnalyzeImage envía una imagen a OpenAI Vision y extrae CUPS + documento
func (s *OCRService) AnalyzeImage(ctx context.Context, imageURL string) (*OCRResult, error) {
	ctx, cancel := s.withAnalyzeBudget(ctx)
	defer cancel()

	systemPrompt := s.buildSystemPrompt()
	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Analiza esta orden médica y extrae los datos según las reglas indicadas."},
				visionImagePart(imageURL),
			},
		},
	}
	return s.callOpenAI(ctx, messages)
}

// SetNectarCredentials habilita la descarga de adjuntos de Néctar: GET /api/bot/attachments/{id}
// exige clave de servicio + firma X-Nectar-*. baseURL es la raíz de Néctar; solo las URLs de ESE
// anfitrión llevan esas credenciales. Con baseURL vacío no cambia nada.
func (s *OCRService) SetNectarCredentials(baseURL, serviceKey, requestSecret string) {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		s.nectarHost = strings.ToLower(u.Host)
		s.nectarScheme = strings.ToLower(u.Scheme)
	}
	s.nectarServiceKey = serviceKey
	s.nectarRequestSecret = requestSecret
}

// defaultMediaRetryDelays son las esperas de producción entre reintentos ante un 404 o un 500 de un
// adjunto de Néctar: ~2 s, ~5 s, ~10 s (≈17 s en total, 4 GET como máximo).
var defaultMediaRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// isNectarURL dice si la URL es del Néctar configurado (mismo anfitrión Y mismo esquema).
func (s *OCRService) isNectarURL(u *url.URL) bool {
	return s.nectarHost != "" && strings.ToLower(u.Host) == s.nectarHost && strings.ToLower(u.Scheme) == s.nectarScheme
}

// downloadMedia baja el adjunto (cap 20 MB) con la credencial que corresponde a su anfitrión.
//
// Solo para adjuntos de Néctar se reintentan, con las mismas esperas, dos respuestas transitorias:
//   - 404: en los primeros segundos tras el aviso, Néctar puede seguir bajando el archivo del lado
//     de WhatsApp y su endpoint responde 404 aunque todo esté bien (contrato de avisos, ver
//     internal/nectar/notice.go). Normalmente queda listo en 1-5 s.
//   - 500 (sin cuerpo): Néctar no pudo escribir su bitácora de la descarga y pide reintentar con
//     espera (contrato del Plan 44).
//
// A los ~17 s sin resolver es un fallo permanente y reintentar más no ayuda. Los demás códigos (un
// 401 es de credencial) fallan al primer GET. Bird no cambia: nada suyo se reintenta.
func (s *OCRService) downloadMedia(ctx context.Context, documentURL string) ([]byte, error) {
	var delays []time.Duration
	if u, err := url.Parse(documentURL); err == nil && s.isNectarURL(u) {
		delays = s.mediaRetryDelays
		if delays == nil {
			delays = defaultMediaRetryDelays
		}
	}
	for attempt := 0; ; attempt++ {
		fileData, status, err := s.downloadMediaOnce(ctx, documentURL)
		if err == nil {
			return fileData, nil
		}
		if !retryableMediaStatus(status) || attempt >= len(delays) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delays[attempt]):
		}
	}
}

// retryableMediaStatus dice si un código de la descarga de un adjunto de Néctar es transitorio
// (ver downloadMedia).
func retryableMediaStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusInternalServerError
}

// downloadMediaOnce hace UN GET al adjunto. status queda en 0 cuando el error es previo a tener
// una respuesta (armar el request, fallo de red): esos casos no se reintentan.
func (s *OCRService) downloadMediaOnce(ctx context.Context, documentURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", documentURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create download request: %w", err)
	}
	// #3 (auditoría): cada credencial sale SOLO hacia su anfitrión. El documentURL viene del webhook
	// (firmado), pero por defensa en profundidad no se filtra ninguna a un host arbitrario
	// (SSRF/leak). Las media pre-firmadas de otros hosts no la necesitan.
	if u, perr := url.Parse(documentURL); perr == nil {
		host := strings.ToLower(u.Hostname())
		switch {
		case s.isNectarURL(u):
			// Néctar: SUS credenciales, nunca la de Bird. Se firma al enviar (ventana de 5 min del
			// otro lado); un GET sin cuerpo firma SHA256("").
			req.Header.Set(nectar.ServiceKeyHeader, s.nectarServiceKey)
			nectar.SignRequest(req, s.nectarRequestSecret, time.Now(), nil)
		case s.birdAccessKey != "" && (host == "bird.com" || strings.HasSuffix(host, ".bird.com")):
			req.Header.Set("Authorization", "AccessKey "+s.birdAccessKey)
		case s.birdAccessKey != "":
			slog.Warn("ocr: media URL host no es de Bird — no se adjunta credencial", "host", host)
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("download document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("download document status %d", resp.StatusCode)
	}

	// L6: acotar la lectura de la media (cap 20 MB). Sin límite, un archivo enorme infla memoria
	// (~1.33x al base64-ear) × sesiones concurrentes. +1 byte para detectar el desborde.
	const maxMediaBytes = 20 << 20 // 20 MB
	fileData, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read document body: %w", err)
	}
	if len(fileData) > maxMediaBytes {
		return nil, resp.StatusCode, fmt.Errorf("document too large (> %d MB)", maxMediaBytes>>20)
	}
	return fileData, resp.StatusCode, nil
}

// AnalyzeDocument baja el adjunto de la orden: una imagen va directa a Vision; un PDF se convierte con
// Ghostscript (300 DPI) y sus hojas van a Vision en una sola llamada (analyzePages).
func (s *OCRService) AnalyzeDocument(ctx context.Context, documentURL string) (*OCRResult, error) {
	// Presupuesto propio: cubre download + conversión de PDF + Vision (incluido el reintento con la
	// primera hoja). Sin él, un documento pesado o un Vision colgado agotaba el contexto del mensaje.
	ctx, cancel := s.withAnalyzeBudget(ctx)
	defer cancel()

	// 1. Download the file (credencial según el anfitrión: Bird o Néctar)
	fileData, err := s.downloadMedia(ctx, documentURL)
	if err != nil {
		return nil, err
	}

	// 2. Detect MIME type from magic bytes
	mimeType := http.DetectContentType(fileData)

	// If it's already an image, send directly as base64
	if strings.HasPrefix(mimeType, "image/") {
		dataURI := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(fileData)
		return s.AnalyzeImage(ctx, dataURI)
	}

	// 3. Si es PDF: su capa de texto si la trae y, si no, TODAS sus hojas a Vision en una sola llamada.
	if mimeType != "application/pdf" {
		return &OCRResult{Success: false, Error: "formato_no_soportado"}, nil
	}

	pdfPath, err := writeTempPDF(fileData)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(pdfPath) }()

	// Capa de texto primero: un PDF generado por un sistema trae el texto exacto, y leerlo sale más
	// barato, más rápido y sin el riesgo de que un dígito se lea mal en la imagen. Salvaguarda: si del
	// texto no sale ningún CUPS con código (o el análisis falla) se sigue con las imágenes, para que un
	// escaneo con una capa de texto mala no lea peor que antes.
	if pages := s.pdfText(ctx, pdfPath); usableTextLayer(pages) {
		res, err := s.analyzeText(ctx, pages)
		if err == nil && hasValidCUP(res) {
			return res, nil
		}
		slog.Info("ocr: la capa de texto del pdf no dio CUPS con código; se leen las imágenes",
			"pages", len(pages), "error", err)
	}

	// Antes se leía la hoja 1 y solo si no traía CUP se miraban las demás. Un PDF cuya primera hoja
	// trae procedimientos y cuyos demás siguen en las hojas 2, 3… se leía a medias, y el paciente
	// veía un resumen que parecía completo. Siguen siendo UNA llamada; en la orden de una hoja no
	// cambia nada (Ghostscript convierte la única que hay) y maxOCRPages acota las imágenes.
	uris, err := s.pdfToImages(ctx, pdfPath, 1, maxOCRPages)
	if err != nil {
		return nil, err
	}
	return s.analyzePages(ctx, uris)
}

// writeTempPDF deja el PDF en un archivo temporal para Ghostscript; quien llama lo borra.
func writeTempPDF(data []byte) (string, error) {
	f, err := os.CreateTemp("", "order-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temp pdf: %w", err)
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write temp pdf: %w", err)
	}
	return f.Name(), nil
}

// analyzePages lee las hojas YA convertidas de un PDF: una sola va como imagen suelta; varias, juntas
// con el prompt multipágina (AnalyzeImages), que sabe descartar la historia clínica y juntar los
// procedimientos de todas las hojas de orden. Si esa lectura falla o no trae ningún CUPS con código, se
// intenta solo la primera hoja, donde está la orden en la mayoría de los casos (antes iba primero).
func (s *OCRService) analyzePages(ctx context.Context, uris []string) (*OCRResult, error) {
	switch len(uris) {
	case 0:
		return nil, fmt.Errorf("pdf sin páginas convertidas")
	case 1:
		return s.AnalyzeImage(ctx, uris[0])
	}
	slog.Info("ocr: documento multipágina, se leen todas las hojas", "pages", len(uris))
	res, err := s.AnalyzeImages(ctx, uris)
	if err == nil && hasValidCUP(res) {
		return res, nil
	}
	slog.Warn("ocr multipágina falló o sin código; se reintenta solo con la primera hoja", "error", err)
	first, ferr := s.AnalyzeImage(ctx, uris[0])
	if err == nil && !hasValidCUP(first) {
		// La primera hoja no mejora lo leído (o falló): se queda la lectura conjunta, que sí respondió.
		return res, nil //nolint:nilerr // ferr se descarta a propósito: la lectura conjunta es válida
	}
	return first, ferr
}

// minPDFTextWords: por debajo de esto el PDF NO tiene una capa de texto útil (un escaneo trae cero o
// unas pocas palabras sueltas del membrete) y hay que mirarlo como imagen. Una orden real ronda las 100.
const minPDFTextWords = 40

// minPDFPageWords es el mínimo de CADA hoja. Una hoja escaneada dentro de un PDF de sistema (la orden
// escaneada detrás de una historia clínica digitada) no trae texto, o solo el membrete o un sello
// (menos de 10 palabras); la hoja de orden o formulación más corta de un sistema pasa de 60. 20 deja
// margen a los dos lados. Si una sola hoja no llega, el texto no es el documento completo y todo el PDF
// va como imágenes: si no, esa hoja —que puede ser la orden— nunca se miraría.
const minPDFPageWords = 20

// usableTextLayer dice si la capa de texto representa el PDF entero: el total y cada hoja tienen
// palabras suficientes.
func usableTextLayer(pages []string) bool {
	total := 0
	for _, p := range pages {
		n := len(strings.Fields(p))
		if n < minPDFPageWords {
			return false
		}
		total += n
	}
	return total >= minPDFTextWords
}

// pdfTextLayer extrae con Ghostscript (txtwrite, sin dependencias nuevas) el texto EMBEBIDO del PDF,
// una entrada por hoja (hasta maxOCRPages). txtwrite no separa las hojas en un solo archivo (no emite
// salto de página; verificado con el Ghostscript de Alpine), así que se le pide un archivo por hoja con
// %d en el nombre. nil si no se pudo; el umbral de palabras lo aplica quien llama.
func pdfTextLayer(ctx context.Context, pdfPath string) []string {
	outPattern := pdfPath + "-t%d.txt"
	args := []string{
		"-dSAFER", "-dBATCH", "-dNOPAUSE", "-sDEVICE=txtwrite",
		fmt.Sprintf("-dLastPage=%d", maxOCRPages), "-sOutputFile=" + outPattern, pdfPath,
	}
	//nolint:gosec // G204: binario fijo (gs); los argumentos son rutas temporales propias, no input externo
	out, err := exec.CommandContext(ctx, "gs", args...).CombinedOutput()
	matches, _ := filepath.Glob(pdfPath + "-t*.txt")
	for _, m := range matches {
		defer func(p string) { _ = os.Remove(p) }(m)
	}
	if err != nil {
		slog.Warn("no se pudo extraer la capa de texto del pdf", "error", err, "output_len", len(out))
		return nil
	}
	sort.Slice(matches, func(i, j int) bool { return pdfPageNum(matches[i]) < pdfPageNum(matches[j]) })
	pages := make([]string, 0, len(matches))
	for _, m := range matches {
		data, rerr := os.ReadFile(m) //nolint:gosec // archivo temporal propio (glob derivado de pdfPath), no input externo
		if rerr != nil {
			return nil
		}
		pages = append(pages, strings.TrimSpace(string(data)))
	}
	return pages
}

// multiPageInstruction es la instrucción de un documento de VARIAS hojas, igual para las imágenes
// (AnalyzeImages) y para la capa de texto (analyzeText).
const multiPageInstruction = "Este documento tiene VARIAS páginas y puede incluir HISTORIA CLÍNICA además de la(s) hoja(s) de FORMULACIÓN/orden. Extrae los CUPS SOLO de las hojas de orden/formulación (las que traen la tabla de procedimientos con su código). IGNORA la historia clínica. Si hay órdenes en varias páginas, inclúyelas todas."

// pdfTextLayerNotice presenta la capa de texto al modelo. No la da por literal: en un escaneo «buscable»
// la pone el reconocimiento del escáner y puede traer dígitos mal leídos.
const pdfTextLayerNotice = "TEXTO DEL DOCUMENTO (capa de texto del propio PDF; si el PDF es un escaneo, puede venir del " +
	"reconocimiento automático del escáner y traer caracteres mal leídos: transcribe los códigos tal como aparecen y " +
	"omite los que no se lean con certeza):"

// analyzeText corre el OCR de la orden sobre la capa de texto de un PDF (sin visión). Con varias hojas
// lleva la instrucción multipágina y cada hoja tras su marcador («--- HOJA N ---»), para que el modelo
// distinga la historia clínica de la hoja de orden; con una sola, el mensaje de siempre.
func (s *OCRService) analyzeText(ctx context.Context, pages []string) (*OCRResult, error) {
	header := "Analiza esta orden médica y extrae los datos según las reglas indicadas."
	body := strings.Join(pages, "\n")
	if len(pages) > 1 {
		header = multiPageInstruction
		var b strings.Builder
		for i, p := range pages {
			fmt.Fprintf(&b, "--- HOJA %d ---\n%s\n", i+1, p)
		}
		body = strings.TrimSuffix(b.String(), "\n")
	}
	messages := []map[string]interface{}{
		{"role": "system", "content": s.buildSystemPrompt()},
		{"role": "user", "content": header + "\n\n" + pdfTextLayerNotice + "\n" + body},
	}
	return s.callOpenAI(ctx, messages)
}

// hasValidCUP indica si el OCR extrajo al menos un CUP con código NO vacío (el código es la fuente
// autoritativa para cobertura/tarifa). Decide si la lectura de la capa de texto basta o hay que mirar
// las imágenes.
func hasValidCUP(res *OCRResult) bool {
	if res == nil {
		return false
	}
	for _, c := range res.Cups {
		if strings.TrimSpace(c.Code) != "" {
			return true
		}
	}
	return false
}

// pdfPagesToDataURIs convierte las páginas [first,last] del PDF a JPEG (300 DPI) con Ghostscript y las
// devuelve como data URIs base64 en orden de página. last<=0 = hasta el final.
func pdfPagesToDataURIs(ctx context.Context, pdfPath string, first, last int) ([]string, error) {
	outPattern := pdfPath + "-p%d.jpg"
	args := []string{"-dSAFER", "-dBATCH", "-dNOPAUSE", "-sDEVICE=jpeg", "-r300"}
	if first > 0 {
		args = append(args, fmt.Sprintf("-dFirstPage=%d", first))
	}
	if last > 0 {
		args = append(args, fmt.Sprintf("-dLastPage=%d", last))
	}
	args = append(args, "-sOutputFile="+outPattern, pdfPath)
	if output, err := exec.CommandContext(ctx, "gs", args...).CombinedOutput(); err != nil {
		slog.Error("ghostscript conversion failed", "error", err, "output", string(output))
		return nil, fmt.Errorf("pdf to image conversion failed: %w", err)
	}
	matches, _ := filepath.Glob(pdfPath + "-p*.jpg")
	sort.Slice(matches, func(i, j int) bool { return pdfPageNum(matches[i]) < pdfPageNum(matches[j]) })
	uris := make([]string, 0, len(matches))
	for _, m := range matches {
		defer func(p string) { _ = os.Remove(p) }(m)
		data, rerr := os.ReadFile(m) //nolint:gosec // JPEG temporal propio (glob derivado de pdfPath), no input externo
		if rerr != nil {
			continue
		}
		uris = append(uris, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(data))
	}
	return uris, nil
}

// pdfPageNum extrae el número de hoja del nombre "<pdf>-p<N>.jpg" (imagen) o "<pdf>-t<N>.txt" (texto),
// para ordenar las hojas.
func pdfPageNum(path string) int {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	i := strings.LastIndex(base, "-")
	if i < 0 || i+2 > len(base) {
		return 0
	}
	n, _ := strconv.Atoi(base[i+2:])
	return n
}

// AnalyzeImages envía VARIAS páginas de un PDF en UNA sola llamada a Vision. El
// documento puede mezclar historia clínica y hojas de orden; el prompt refuerza extraer CUPS SOLO de
// las hojas de FORMULACIÓN/orden e ignorar la historia clínica, e incluir todas las órdenes si hay varias.
func (s *OCRService) AnalyzeImages(ctx context.Context, imageURLs []string) (*OCRResult, error) {
	ctx, cancel := s.withAnalyzeBudget(ctx)
	defer cancel()

	content := []map[string]interface{}{
		{"type": "text", "text": multiPageInstruction},
	}
	for _, u := range imageURLs {
		content = append(content, visionImagePart(u))
	}
	messages := []map[string]interface{}{
		{"role": "system", "content": s.buildSystemPrompt()},
		{"role": "user", "content": content},
	}
	return s.callOpenAI(ctx, messages)
}

// AnalyzeText processes a text description of a medical order (from agent)
// and extracts CUPS data in the same format as AnalyzeImage.
func (s *OCRService) AnalyzeText(ctx context.Context, description string) (*OCRResult, error) {
	systemPrompt := s.buildSystemPrompt()
	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{
			"role": "user",
			"content": "Un agente humano describe la orden médica de un paciente. " +
				"Extrae los datos como si leyeras la orden físicamente:\n\n" + description,
		},
	}
	return s.callOpenAI(ctx, messages)
}

// ReadCupsCode es la SEGUNDA LECTURA dedicada del código CUPS de UN procedimiento (tomada de hdg-bot,
// donde el OCR general leyó 892262 donde decía 890262): se vuelve a mirar la orden con una sola tarea
// —el código del procedimiento cuyo nombre se da— y con el modelo de docModel. Una imagen va tal cual; un
// PDF, con todas sus hojas como en la lectura general (el procedimiento puede estar en cualquiera).
// Devuelve solo el código; vacío = no se pudo leer con seguridad (nunca se adivina).
func (s *OCRService) ReadCupsCode(ctx context.Context, documentURL, procedureName string) (string, error) {
	ctx, cancel := s.withAnalyzeBudget(ctx)
	defer cancel()
	fileData, err := s.downloadMedia(ctx, documentURL)
	if err != nil {
		return "", err
	}
	var uris []string
	switch mimeType := http.DetectContentType(fileData); {
	case strings.HasPrefix(mimeType, "image/"):
		uris = []string{"data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(fileData)}
	case mimeType == "application/pdf":
		pdfPath, err := writeTempPDF(fileData)
		if err != nil {
			return "", err
		}
		defer func() { _ = os.Remove(pdfPath) }()
		if uris, err = s.pdfToImages(ctx, pdfPath, 1, maxOCRPages); err != nil {
			return "", err
		}
	}
	if len(uris) == 0 {
		return "", nil
	}
	content := []map[string]interface{}{
		{"type": "text", "text": "Transcribe el código CUPS del procedimiento: " + strings.TrimSpace(procedureName)},
	}
	for _, u := range uris {
		content = append(content, visionImagePart(u))
	}
	model := s.docModel
	if model == "" {
		model = s.model
	}
	raw, err := s.callOpenAIRaw(ctx, model, []map[string]interface{}{
		{"role": "system", "content": cupsCodePrompt},
		{"role": "user", "content": content},
	})
	if err != nil {
		return "", err
	}
	return parseCupsCode(raw), nil
}

// cupsCodePrompt: una sola tarea, con las trampas típicas de una orden colombiana.
const cupsCodePrompt = `Eres un lector de órdenes médicas colombianas.
Tu ÚNICA tarea: transcribir el CÓDIGO CUPS del procedimiento que se te indica.
- El CUPS tiene 6 dígitos (p. ej. 890274, 883101) y va junto al nombre del procedimiento, en una columna "Código", "CUPS" o "Cód."; a veces va con puntos (89.02.74).
- NO es el número de la orden, NO la autorización, NO el documento del paciente, NO la cantidad, NO el código de diagnóstico (CIE-10, que lleva una letra: G40.9).
- Si el código trae un sufijo tras un guion (891509-16), transcribe solo el código base (891509).
- Léelo DÍGITO A DÍGITO. Dígitos que se confunden: 0/2/6/8, 1/7, 3/8, 5/6. Vuelve a mirar cada uno antes de responder.
Responde SOLO con JSON válido: {"cups": "<solo dígitos>", "como_aparece": "<texto literal>", "confianza": "alta|media|baja"}.
Si no lo ves con seguridad, "cups": null y "confianza": "baja". NUNCA adivines ni completes dígitos.`

// parseCupsCode saca el código del JSON de la segunda lectura; con confianza baja, sin código o con una
// respuesta ilegible devuelve vacío.
func parseCupsCode(content string) string {
	var out struct {
		Cups      *string `json:"cups"`
		Confianza string  `json:"confianza"`
	}
	if err := json.Unmarshal([]byte(extractJSON(content)), &out); err != nil || out.Cups == nil {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(out.Confianza), "baja") {
		return ""
	}
	var b strings.Builder
	// El sufijo tras el guion es cantidad, no parte del código (891509-16 → 891509).
	for _, c := range strings.ToUpper(utils.BaseCupCode(strings.TrimSpace(*out.Cups))) {
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// extractJSON extrae el JSON de una respuesta que puede contener markdown
func extractJSON(content string) string {
	content = strings.TrimSpace(content)

	// Intentar extraer de bloque ```json ... ```
	if idx := strings.Index(content, "```json"); idx >= 0 {
		start := idx + 7
		end := strings.Index(content[start:], "```")
		if end >= 0 {
			return strings.TrimSpace(content[start : start+end])
		}
	}

	// Intentar extraer de bloque ``` ... ```
	if idx := strings.Index(content, "```"); idx >= 0 {
		start := idx + 3
		// Saltar posible newline después de ```
		if start < len(content) && content[start] == '\n' {
			start++
		}
		end := strings.Index(content[start:], "```")
		if end >= 0 {
			return strings.TrimSpace(content[start : start+end])
		}
	}

	// Ya es JSON directo
	return content
}

// parseRetryAfterHeader parses the Retry-After header (integer seconds).
// Returns 2s default if missing or unparseable.
func parseRetryAfterHeader(value string) time.Duration {
	if value == "" {
		return 2 * time.Second
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 && seconds <= 120 {
		return time.Duration(seconds) * time.Second
	}
	return 2 * time.Second
}
