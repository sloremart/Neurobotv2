package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
)

// maxOCRPages acota cuántas páginas de un PDF multipágina se mandan a Vision en el fallback (costo/tamaño).
const maxOCRPages = 10

type OCRService struct {
	apiKey        string
	model         string // gpt-4o-mini
	apiURL        string // default: https://api.openai.com/v1/chat/completions
	client        *http.Client
	cupsContext   string // CUPS reference table injected into the prompt
	birdAccessKey string // Bird API key for downloading media files
}

func NewOCRService(apiKey, model, cupsContext, birdAccessKey string) *OCRService {
	return &OCRService{
		apiKey:        apiKey,
		model:         model,
		apiURL:        "https://api.openai.com/v1/chat/completions",
		client:        &http.Client{Timeout: 60 * time.Second},
		cupsContext:   cupsContext,
		birdAccessKey: birdAccessKey,
	}
}

// NewOCRServiceForTest creates an OCRService pointing at a custom URL (for httptest).
func NewOCRServiceForTest(apiURL string) *OCRService {
	return &OCRService{
		apiURL: apiURL,
		model:  "test-model",
		client: &http.Client{Timeout: 5 * time.Second},
	}
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
1) PRIORIDAD ABSOLUTA: CÓDIGO EN LA ORDEN
   - Si en la fila del procedimiento existe un número de 4 a 6 dígitos, ese ES el cups_code.
   - Si el código tiene cualquier sufijo tras un guion (ej: "891509-16", "891509-1", "891509-4"), usa SOLO los dígitos del código base: "891509". El número del sufijo indica cantidad; captúrala usando (#N) o la regla 2, nunca en cups_code.
   - Copia la descripción tal cual de la orden en cups_name.

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
- is_contrasted: indica si ESE examen usa MEDIO DE CONTRASTE. Decide en este orden (lo de arriba manda):
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

SIN TABLA DE PROCEDIMIENTOS:
- Si la imagen no es una orden médica o no tiene tabla de procedimientos:
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
	reqBody := map[string]interface{}{
		"model":       s.model,
		"messages":    messages,
		"max_tokens":  1200,
		"temperature": 0,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
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
				return nil, fmt.Errorf("create retry request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s.apiKey)
		}

		resp, err = s.client.Do(req)
		if err != nil {
			if attempt == maxRetries {
				return nil, fmt.Errorf("openai request after %d attempts: %w", attempt+1, err)
			}
			delay := time.Duration((attempt+1)*(attempt+1)) * time.Second
			slog.Warn("openai_retry_network", "attempt", attempt+1, "delay", delay, "error", err)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, fmt.Errorf("openai retry cancelled: %w", ctx.Err())
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
				return nil, fmt.Errorf("openai read body after %d attempts: %w", attempt+1, readErr)
			}
			delay := time.Duration((attempt+1)*(attempt+1)) * time.Second
			slog.Warn("openai_retry_read_body", "attempt", attempt+1, "delay", delay, "error", readErr)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, fmt.Errorf("openai retry cancelled: %w", ctx.Err())
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
				return nil, fmt.Errorf("openai rate limited after %d attempts", attempt+1)
			}
			delay := parseRetryAfterHeader(resp.Header.Get("Retry-After"))
			slog.Warn("openai_retry_rate_limited", "attempt", attempt+1, "retry_after", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, fmt.Errorf("openai retry cancelled: %w", ctx.Err())
			}
			continue
		}

		// 5xx server error — retry with backoff
		if resp.StatusCode >= 500 {
			if attempt == maxRetries {
				slog.Error("openai server error after retries", "status", resp.StatusCode, "attempts", attempt+1)
				return nil, fmt.Errorf("openai api status %d after %d attempts", resp.StatusCode, attempt+1)
			}
			delay := time.Duration((attempt+1)*(attempt+1)) * time.Second
			slog.Warn("openai_retry_server_error", "attempt", attempt+1, "status", resp.StatusCode, "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, fmt.Errorf("openai retry cancelled: %w", ctx.Err())
			}
			continue
		}

		// 4xx client error (not 429) — no retry. No logueamos el body crudo: puede traer PII de
		// salud (cédula/CUPS/EPS de la orden) y este ERROR se reenvía a Telegram (N-7).
		slog.Error("openai api error", "status", resp.StatusCode, "body_len", len(respBody))
		return nil, fmt.Errorf("openai api status %d", resp.StatusCode)
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
		return nil, fmt.Errorf("unmarshal api response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return &OCRResult{Success: false, Error: "sin respuesta de OpenAI"}, nil
	}

	content := apiResp.Choices[0].Message.Content

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
		return &OCRResult{Success: false, Error: "no se pudo interpretar la respuesta"}, nil
	}

	if parsed.Error != "" {
		return &OCRResult{Success: false, Error: parsed.Error, Entity: parsed.Entity}, nil
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
	}, nil
}

// AnalyzeImage envía una imagen a OpenAI Vision y extrae CUPS + documento
func (s *OCRService) AnalyzeImage(ctx context.Context, imageURL string) (*OCRResult, error) {
	systemPrompt := s.buildSystemPrompt()
	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Analiza esta orden médica y extrae los datos según las reglas indicadas."},
				{"type": "image_url", "image_url": map[string]string{"url": imageURL}},
			},
		},
	}
	return s.callOpenAI(ctx, messages)
}

// AnalyzeDocument downloads a document (PDF), converts the first page to JPEG
// using Ghostscript (300 DPI), and sends the image to OpenAI Vision for OCR.
// Same behavior as Laravel's VisionMedicalOrderService.
func (s *OCRService) AnalyzeDocument(ctx context.Context, documentURL string) (*OCRResult, error) {
	// 1. Download the file (with Bird auth if configured)
	req, err := http.NewRequestWithContext(ctx, "GET", documentURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	// #3 (auditoría): adjuntar la AccessKey de Bird SOLO si la URL es de Bird (*.bird.com). El
	// documentURL viene del webhook (firmado), pero por defensa en profundidad evitamos filtrar la
	// credencial a un host arbitrario (SSRF/leak). Las media pre-firmadas de otros hosts no la necesitan.
	if s.birdAccessKey != "" {
		if u, perr := url.Parse(documentURL); perr == nil {
			host := strings.ToLower(u.Hostname())
			if host == "bird.com" || strings.HasSuffix(host, ".bird.com") {
				req.Header.Set("Authorization", "AccessKey "+s.birdAccessKey)
			} else {
				slog.Warn("ocr: media URL host no es de Bird — no se adjunta credencial", "host", host)
			}
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download document status %d", resp.StatusCode)
	}

	// L6: acotar la lectura de la media (cap 20 MB). Sin límite, un archivo enorme infla memoria
	// (~1.33x al base64-ear) × sesiones concurrentes. La URL viene del CDN de Bird/WhatsApp, pero el
	// cap evita el peor caso. +1 byte para detectar el desborde.
	const maxMediaBytes = 20 << 20 // 20 MB
	fileData, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read document body: %w", err)
	}
	if len(fileData) > maxMediaBytes {
		return nil, fmt.Errorf("document too large (> %d MB)", maxMediaBytes>>20)
	}

	// 2. Detect MIME type from magic bytes
	mimeType := http.DetectContentType(fileData)

	// If it's already an image, send directly as base64
	if strings.HasPrefix(mimeType, "image/") {
		dataURI := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(fileData)
		return s.AnalyzeImage(ctx, dataURI)
	}

	// 3. If PDF: página 1 primero (cubre la orden de 1 hoja, la mayoría) y solo si no trae CUP se
	// analizan todas las páginas (órdenes multipágina: historia clínica primero, orden al final).
	if mimeType != "application/pdf" {
		return &OCRResult{Success: false, Error: "formato_no_soportado"}, nil
	}

	pdfFile, err := os.CreateTemp("", "order-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("create temp pdf: %w", err)
	}
	defer os.Remove(pdfFile.Name())

	if _, err := pdfFile.Write(fileData); err != nil {
		pdfFile.Close()
		return nil, fmt.Errorf("write temp pdf: %w", err)
	}
	pdfFile.Close()

	// Página 1: flujo normal, SIN costo/latencia extra para la orden de 1 hoja.
	page1URIs, err := s.pdfPagesToDataURIs(ctx, pdfFile.Name(), 1, 1)
	if err != nil {
		return nil, err
	}
	if len(page1URIs) == 0 {
		return nil, fmt.Errorf("pdf sin páginas convertidas")
	}
	res, err := s.AnalyzeImage(ctx, page1URIs[0])
	if err != nil {
		return nil, err
	}
	if hasValidCUP(res) {
		return res, nil // la 1ª hoja ya trae el CUP → sin cambios respecto al comportamiento previo
	}

	// Fallback multipágina: convertir TODAS las páginas (acotado) y mandarlas juntas en UNA llamada.
	allURIs, err := s.pdfPagesToDataURIs(ctx, pdfFile.Name(), 1, maxOCRPages)
	if err != nil {
		slog.Warn("ocr multipágina: conversión falló; se usa el resultado de la página 1", "error", err)
		return res, nil
	}
	if len(allURIs) <= 1 {
		return res, nil // documento de una sola página: nada más que intentar
	}
	slog.Info("ocr multipágina: página 1 sin CUP, analizando todas las páginas", "pages", len(allURIs))
	multi, err := s.AnalyzeImages(ctx, allURIs)
	if err != nil {
		return res, nil // fail-safe: si el multipágina falla, devolver lo de la página 1
	}
	return multi, nil
}

// pdfPagesToDataURIs convierte las páginas [first,last] del PDF a JPEG (300 DPI) con Ghostscript y las
// devuelve como data URIs base64 en orden de página. last<=0 = hasta el final.
func (s *OCRService) pdfPagesToDataURIs(ctx context.Context, pdfPath string, first, last int) ([]string, error) {
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

// pdfPageNum extrae el número de página del nombre "<pdf>-p<N>.jpg" (para ordenar las páginas).
func pdfPageNum(path string) int {
	base := filepath.Base(path)
	i := strings.LastIndex(base, "-p")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(base[i+2:], ".jpg"))
	return n
}

// hasValidCUP indica si el OCR extrajo al menos un CUP con código NO vacío (el código es la fuente
// autoritativa para cobertura/tarifa). Un resultado sin código válido NO debe tratarse como "sin
// convenio": dispara el fallback multipágina y, si aun así no hay código, la guarda del handler escala.
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

// AnalyzeImages envía VARIAS páginas de un PDF en UNA sola llamada a Vision (fallback multipágina). El
// documento puede mezclar historia clínica y hojas de orden; el prompt refuerza extraer CUPS SOLO de
// las hojas de FORMULACIÓN/orden e ignorar la historia clínica, e incluir todas las órdenes si hay varias.
func (s *OCRService) AnalyzeImages(ctx context.Context, imageURLs []string) (*OCRResult, error) {
	content := []map[string]interface{}{
		{"type": "text", "text": "Este documento tiene VARIAS páginas y puede incluir HISTORIA CLÍNICA además de la(s) hoja(s) de FORMULACIÓN/orden. Extrae los CUPS SOLO de las hojas de orden/formulación (las que traen la tabla de procedimientos con su código). IGNORA la historia clínica. Si hay órdenes en varias páginas, inclúyelas todas."},
	}
	for _, u := range imageURLs {
		content = append(content, map[string]interface{}{
			"type":      "image_url",
			"image_url": map[string]string{"url": u},
		})
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
