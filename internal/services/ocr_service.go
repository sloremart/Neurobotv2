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

// Topes de los JPEG que deja Ghostscript, antes de leerlos a memoria y pasarlos a base64. Una hoja
// carta escaneada a 300 DPI pesa 1-3 MB; una con un MediaBox desmesurado (200" son 60.000 × 60.000 px)
// deja decenas de MB aun en blanco, y diez de ellas tumbarían al bot por memoria. Pasarse de cualquiera
// de los dos repite la conversión a la resolución que cabe (ocrRenderDPIs) y, si ni así cabe, la rechaza igual
// que un fallo de Ghostscript. El total (32 MB, ~43 MB en base64) queda bajo el límite de 50 MB por
// petición de OpenAI y sobre los ~24 MB de diez hojas escaneadas con ruido.
const (
	maxOCRPageImageBytes   = 10 << 20
	maxOCRImagesTotalBytes = 32 << 20
)

// errPageImagesTooLarge: los JPEG convertidos pasan maxOCRPageImageBytes o maxOCRImagesTotalBytes.
var errPageImagesTooLarge = errors.New("pdf page images too large")

// pageImagesTooLargeError es errPageImagesTooLarge con lo que midieron las imágenes: la hoja más pesada
// y el total, para elegir la resolución del intento siguiente.
type pageImagesTooLargeError struct {
	page, total int64
}

func (e *pageImagesTooLargeError) Error() string {
	return fmt.Sprintf("%v (page %d bytes, total %d bytes)", errPageImagesTooLarge, e.page, e.total)
}

func (e *pageImagesTooLargeError) Unwrap() error { return errPageImagesTooLarge }

// overflow dice cuántas veces pasan las imágenes el más apretado de los dos topes.
func (e *pageImagesTooLargeError) overflow() float64 {
	return max(float64(e.page)/maxOCRPageImageBytes, float64(e.total)/maxOCRImagesTotalBytes)
}

// gsMaxBitmap fija el raster que Ghostscript tiene en memoria por banda (10 MB): una hoja más grande se
// pinta por bandas en vez de entera. Ghostscript 10.05 ya divide por bandas por defecto (medido: unos
// 33 MB de RSS con la hoja de 60.000 px); el valor explícito evita depender del de la versión instalada.
const gsMaxBitmap = "-dMaxBitmap=10000000"

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

// NewOCRService crea el OCR de órdenes con el modelo de la lectura general y la credencial de Bird.
func NewOCRService(apiKey, model, birdAccessKey string) *OCRService {
	s := &OCRService{
		apiKey:         apiKey,
		model:          model,
		apiURL:         "https://api.openai.com/v1/chat/completions",
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
	// IsInterconsulta: el OCR lo leyó de una solicitud de interconsulta o remisión (sin tabla de CUPS);
	// la asignación por nombre solo le ofrece consultas de primera vez.
	IsInterconsulta bool `json:"is_interconsulta,omitempty"`
	// OrderName es el nombre tal como venía en la orden cuando el código se asignó por nombre (Name pasa
	// a ser el del catálogo); vacío si la orden traía el código.
	OrderName string `json:"order_name,omitempty"`
}

type CUPSGroup struct {
	ServiceType string      `json:"service"`
	Cups        []CUPSEntry `json:"cups"`
	Espacios    int         `json:"espacios"`
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
      "observations": "<string>",
      "is_interconsulta": <boolean>
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
- NUNCA inventes ni deduzcas un código: devuelve el código y el nombre tal como aparecen en la imagen.

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
       * 890284 = PSIQUIATRÍA PRIMERA VEZ — código válido, devuélvelo tal cual
       * 890308 = PSICOLOGÍA CONTROL — código válido, devuélvelo tal cual
   - VERIFICACIÓN CRUZADA OBLIGATORIA: Después de leer el código, compara la especialidad del código con la especialidad mencionada en la descripción de la orden.
     * Si NO coinciden → has leído mal el código. Usa el código de la imagen tal cual Y cups_name = descripción exacta de la orden.
     * Ejemplo: si el código apunta a "MEDICINA FISICA" pero la descripción dice "PSICOLOGÍA" → el código correcto es 890308, devuélvelo con el nombre de la orden.

2) SI NO HAY CÓDIGO EN LA ORDEN:
   - Pon cups_code vacío ("") y cups_name con la descripción tal como aparece en la orden.
   - NUNCA elijas ni deduzcas un código por la descripción: el código lo asigna el sistema después.

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
- is_interconsulta: true solo si el procedimiento sale de una SOLICITUD DE INTERCONSULTA o REMISIÓN (ver abajo); si no, false.
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
  Devuelve UN procedimiento con cups_code = "" (salvo que el documento traiga el código visible), cups_name = el servicio
  tal como aparece en el documento e is_interconsulta = true. NUNCA elijas un código por el nombre del servicio.
  NO devuelvas error "no_table_detected" por ser interconsulta — extrae el servicio.

SIN TABLA DE PROCEDIMIENTOS:
- Si la imagen no es una orden médica ni una interconsulta, o no tiene ningún procedimiento o servicio identificable:
  Responder: {"cups": [], "error": "no_table_detected"}
- Si la imagen está borrosa o es ilegible:
  Responder: {"cups": [], "error": "imagen_borrosa"}

Recuerda: SOLO JSON válido como salida.`

// buildSystemPrompt es el prompt de la lectura de la orden. No lleva el catálogo: sin código visible el
// OCR devuelve el nombre y el código lo asigna el bot (cups_name.go), nunca el modelo de visión.
func (s *OCRService) buildSystemPrompt() string {
	return ocrSystemPrompt
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

// pdfPagesToDataURIs convierte las páginas [first,last] del PDF a JPEG con Ghostscript (300 DPI, o menos
// si no caben en los topes; ver renderPDFPages) y las devuelve como data URIs base64 en orden de página.
// last<=0 = hasta el final.
func pdfPagesToDataURIs(ctx context.Context, pdfPath string, first, last int) ([]string, error) {
	return renderPDFPages(ctx, execGhostscript, pdfPath, first, last)
}

// gsRunner corre Ghostscript con esos argumentos y devuelve su salida combinada; inyectable en tests.
type gsRunner func(ctx context.Context, args []string) ([]byte, error)

func execGhostscript(ctx context.Context, args []string) ([]byte, error) {
	//nolint:gosec // G204: binario fijo (gs); los argumentos son rutas temporales propias, no input externo
	return exec.CommandContext(ctx, "gs", args...).CombinedOutput()
}

// ocrRenderDPIs son las resoluciones de conversión, de mayor a menor. Las hojas carta u oficio caben a
// 300 DPI y salen igual que siempre. Una foto pasada a PDF con la página del tamaño de los píxeles (72-96
// DPI: hojas de 40-56") deja a 300 DPI un JPEG de 15-45 MB; se vuelve a convertir más bajo en vez de
// rechazarla, sin perder nada: Vision baja toda imagen a 2048 px de lado mayor.
var ocrRenderDPIs = []int{300, 150, 100}

// pdfRenderBudget acota TODA la conversión de un documento con Ghostscript, con todos sus intentos. Lo
// legítimo más lento medido tarda 5,9 s (foto con ruido duro, tres pasadas); sin este límite, una hoja
// desmesurada se comía hasta el plazo entero del análisis (defaultAnalyzeTimeout) y a Vision no le
// quedaba tiempo. Agotado, la conversión falla como un fallo de Ghostscript.
const pdfRenderBudget = 20 * time.Second

// renderPDFPages convierte las hojas dentro de pdfRenderBudget (ver renderPDFPagesWithin).
func renderPDFPages(ctx context.Context, run gsRunner, pdfPath string, first, last int) ([]string, error) {
	return renderPDFPagesWithin(ctx, run, pdfRenderBudget, pdfPath, first, last)
}

// renderPDFPagesWithin convierte a 300 DPI y, si las imágenes no caben en los topes, vuelve a convertir
// a la resolución que el tamaño medido dice que cabe (nextRenderDPI), sin probar las intermedias. Si ni
// a la menor cabe, devuelve errPageImagesTooLarge.
func renderPDFPagesWithin(ctx context.Context, run gsRunner, budget time.Duration, pdfPath string, first, last int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	dpi := ocrRenderDPIs[0]
	for {
		uris, err := renderPDFPagesAt(ctx, run, pdfPath, first, last, dpi)
		var tooLarge *pageImagesTooLargeError
		if !errors.As(err, &tooLarge) {
			return uris, err
		}
		next, ok := nextRenderDPI(dpi, tooLarge.overflow())
		if !ok {
			return nil, err
		}
		slog.Info("ocr: imágenes del pdf fuera de tope; se convierte a menor resolución", "dpi", dpi, "next_dpi", next)
		dpi = next
	}
}

// nextRenderDPI elige, entre las resoluciones menores que dpi, la mayor en la que las imágenes caben si
// su peso baja con el cuadrado de la resolución (overflow = cuántas veces se pasaron del tope a dpi). Si
// ninguna cabe según esa cuenta, la menor. ok=false: no queda ninguna menor.
func nextRenderDPI(dpi int, overflow float64) (int, bool) {
	var lower []int
	for _, d := range ocrRenderDPIs {
		if d < dpi {
			lower = append(lower, d)
		}
	}
	if len(lower) == 0 {
		return 0, false
	}
	for _, d := range lower {
		if scale := float64(d) / float64(dpi); overflow*scale*scale <= 1 {
			return d, true
		}
	}
	return lower[len(lower)-1], true
}

// renderPDFPagesAt convierte a la resolución dpi; los JPEG temporales se borran al salir, pase lo que pase.
func renderPDFPagesAt(ctx context.Context, run gsRunner, pdfPath string, first, last, dpi int) ([]string, error) {
	output, err := run(ctx, jpegRenderArgs(first, last, dpi, pdfPath+"-p%d.jpg", pdfPath))
	matches, _ := filepath.Glob(pdfPath + "-p*.jpg")
	for _, m := range matches {
		defer func(p string) { _ = os.Remove(p) }(m)
	}
	if err != nil {
		slog.Error("ghostscript conversion failed", "error", err, "output", string(output))
		return nil, fmt.Errorf("pdf to image conversion failed: %w", err)
	}
	sort.Slice(matches, func(i, j int) bool { return pdfPageNum(matches[i]) < pdfPageNum(matches[j]) })
	return readPageImages(matches)
}

// jpegRenderArgs arma los argumentos de Ghostscript para pasar las hojas [first,last] a JPEG. El tiempo
// lo acota el contexto (pdfRenderBudget) y las hojas, last (maxOCRPages).
func jpegRenderArgs(first, last, dpi int, outPattern, pdfPath string) []string {
	args := []string{"-dSAFER", "-dBATCH", "-dNOPAUSE", "-sDEVICE=jpeg", fmt.Sprintf("-r%d", dpi), gsMaxBitmap}
	if first > 0 {
		args = append(args, fmt.Sprintf("-dFirstPage=%d", first))
	}
	if last > 0 {
		args = append(args, fmt.Sprintf("-dLastPage=%d", last))
	}
	return append(args, "-sOutputFile="+outPattern, pdfPath)
}

// readPageImages lee los JPEG ya convertidos (en orden) como data URIs base64. Mira los tamaños antes
// de leer nada: con una hoja o un total fuera de tope no carga ninguna.
func readPageImages(paths []string) ([]string, error) {
	var page, total int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		total += info.Size()
		page = max(page, info.Size())
	}
	if page > maxOCRPageImageBytes || total > maxOCRImagesTotalBytes {
		slog.Warn("ocr: imagen del pdf fuera de tope; no se lee", "page_bytes", page, "total_bytes", total)
		return nil, &pageImagesTooLargeError{page: page, total: total}
	}
	uris := make([]string, 0, len(paths))
	for _, p := range paths {
		data, rerr := os.ReadFile(p) //nolint:gosec // JPEG temporal propio (glob derivado de pdfPath), no input externo
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

// OrderImages baja el adjunto de la orden y lo deja como lo ve Vision: una imagen va tal cual; un PDF,
// con todas sus hojas como en la lectura general (el procedimiento puede estar en cualquiera). Vacío si
// no es imagen ni PDF. Es la entrada de ReadCupsCode: quien lee varios códigos de la misma orden la pide
// una vez y la reutiliza, sin volver a bajar ni a convertir el archivo.
func (s *OCRService) OrderImages(ctx context.Context, documentURL string) ([]string, error) {
	ctx, cancel := s.withAnalyzeBudget(ctx)
	defer cancel()
	fileData, err := s.downloadMedia(ctx, documentURL)
	if err != nil {
		return nil, err
	}
	switch mimeType := http.DetectContentType(fileData); {
	case strings.HasPrefix(mimeType, "image/"):
		return []string{"data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(fileData)}, nil
	case mimeType == "application/pdf":
		pdfPath, err := writeTempPDF(fileData)
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(pdfPath) }()
		return s.pdfToImages(ctx, pdfPath, 1, maxOCRPages)
	}
	return nil, nil
}

// ReadCupsCode es la SEGUNDA LECTURA dedicada del código CUPS de UN procedimiento (tomada de hdg-bot,
// donde el OCR general leyó 892262 donde decía 890262): se vuelve a mirar la orden (sus imágenes, de
// OrderImages) con una sola tarea —el código del procedimiento cuyo nombre se da— y con el modelo de
// docModel. Devuelve solo el código; vacío = no se pudo leer con seguridad (nunca se adivina).
func (s *OCRService) ReadCupsCode(ctx context.Context, uris []string, procedureName string) (string, error) {
	if len(uris) == 0 {
		return "", nil
	}
	ctx, cancel := s.withAnalyzeBudget(ctx)
	defer cancel()
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

// ChooseCupsByName es la elección por nombre de la asignación de código (cups_name.go): una llamada de
// SOLO TEXTO con el modelo de docModel que recibe el nombre leído y la lista corta del catálogo y
// devuelve el código elegido SOLO con confianza alta; vacío en cualquier otro caso (nunca se adivina).
// Quien llama comprueba además que el código esté en la lista y que no contradiga el nombre leído.
func (s *OCRService) ChooseCupsByName(ctx context.Context, readName string, candidates []CupsCandidate) (string, error) {
	ctx, cancel := s.withAnalyzeBudget(ctx)
	defer cancel()
	var b strings.Builder
	fmt.Fprintf(&b, "Procedimiento leído en la orden: %s\n\nCandidatos del catálogo:\n", strings.TrimSpace(readName))
	for _, c := range candidates {
		fmt.Fprintf(&b, "- %s: %s\n", c.Code, c.Name)
	}
	model := s.docModel
	if model == "" {
		model = s.model
	}
	raw, err := s.callOpenAIRaw(ctx, model, []map[string]interface{}{
		{"role": "system", "content": cupsChoicePrompt},
		{"role": "user", "content": b.String()},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Cups      *string `json:"cups"`
		Confianza string  `json:"confianza"`
	}
	if json.Unmarshal([]byte(extractJSON(raw)), &out) != nil || out.Cups == nil ||
		!strings.EqualFold(strings.TrimSpace(out.Confianza), "alta") {
		return "", nil
	}
	return strings.TrimSpace(*out.Cups), nil
}

// cupsChoicePrompt: elegir de una lista cerrada o no elegir.
const cupsChoicePrompt = `Eres un asistente que relaciona procedimientos de órdenes médicas colombianas con el catálogo de una clínica.
Te doy el nombre de UN procedimiento tal como aparece en la orden (puede venir abreviado: RM, TAC, ECO, RX, EMG, NC) y una lista de candidatos del catálogo con su código.
Elige el candidato que es EXACTAMENTE ese procedimiento. Si ninguno lo es, o dudas, no elijas.
- No elijas un candidato que difiera en contraste (simple / con contraste), primera vez / control, pediátrica, lado, superior / inferior, región del cuerpo, vía (transrectal / transabdominal) o marcador del PET.
- Solo puedes responder un código de la lista.
Responde SOLO con JSON válido: {"cups": "<código de la lista>" o null, "confianza": "alta|media|baja"}.`

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
