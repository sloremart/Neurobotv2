package results

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxPDFBytes es el tope del PDF descargado (el PDF vive solo en memoria).
const MaxPDFBytes = 16 << 20

var (
	// ErrInvalidFile indica que el nombre de archivo trae separadores de ruta o está vacío.
	ErrInvalidFile = errors.New("results: nombre de archivo inválido")
	// ErrTooLarge indica que el PDF supera MaxPDFBytes.
	ErrTooLarge = errors.New("results: PDF mayor a 16 MiB")
)

const (
	searchTimeout   = 10 * time.Second
	downloadTimeout = 20 * time.Second
	dateLayout      = "02/01/2006 15:04"
)

// Client consulta la API de resultados. Nunca registra documentos, nombres ni
// archivos en logs ni en errores.
type Client struct {
	baseURL         string
	apiKey          string
	http            *http.Client
	searchTimeout   time.Duration
	downloadTimeout time.Duration
}

// NewClient crea el cliente; apiKey vacía no envía cabecera.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL:         strings.TrimRight(baseURL, "/"),
		apiKey:          apiKey,
		http:            &http.Client{CheckRedirect: noRedirects},
		searchTimeout:   searchTimeout,
		downloadTimeout: downloadTimeout,
	}
}

// noRedirects devuelve el 3xx tal cual: seguirlo reenviaría X-API-Key a otro anfitrión (Go solo
// quita Authorization y Cookie) y podría servir un PDF de una URL sin validar. El 3xx cae en el
// manejo de «código fuera de lo esperado» (error, nunca datos).
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

type studyJSON struct {
	Name      string `json:"nombre"`
	Date      string `json:"fecha"`
	Admission string `json:"admision"`
	StudyType string `json:"tipo_estudio"`
	File      string `json:"archivo"`
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, errors.New("results: solicitud inválida")
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// El error de net/http incluye la URL (con cédula o archivo): se descarta.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("results: %w", ctx.Err())
		}
		return nil, errors.New("results: fallo de red")
	}
	return resp, nil
}

// Search busca los estudios de un documento. 400 y 404 dan lista vacía.
func (c *Client) Search(ctx context.Context, documentNumber string) ([]Study, error) {
	ctx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()

	resp, err := c.get(ctx, "/api/buscar?cedula="+url.QueryEscape(documentNumber))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound:
		return nil, nil
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("results: la API respondió %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, errors.New("results: lectura de respuesta fallida")
	}
	body = bytes.TrimSpace(body)
	var raw []studyJSON
	if len(body) > 0 && body[0] == '{' {
		var one studyJSON
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, errors.New("results: respuesta ilegible")
		}
		raw = []studyJSON{one}
	} else if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("results: respuesta ilegible")
	}

	studies := make([]Study, 0, len(raw))
	for _, r := range raw {
		s := Study{
			PatientName: r.Name,
			StudyType:   r.StudyType,
			Admission:   r.Admission,
			File:        r.File,
			RawDate:     r.Date,
		}
		if t, err := time.ParseInLocation(dateLayout, strings.TrimSpace(r.Date), time.Local); err == nil {
			s.Date = t
		}
		studies = append(studies, s)
	}
	return studies, nil
}

// validFile rechaza vacíos y cualquier separador de ruta o ".." (path traversal).
func validFile(file string) bool {
	if file == "" || file == "." || file == ".." {
		return false
	}
	return !strings.ContainsAny(file, "/\\\x00") && !strings.Contains(file, "..")
}

// DownloadPDF descarga el PDF de un estudio; falla con ErrTooLarge si pasa de MaxPDFBytes.
func (c *Client) DownloadPDF(ctx context.Context, file string) ([]byte, error) {
	if !validFile(file) {
		return nil, ErrInvalidFile
	}
	ctx, cancel := context.WithTimeout(ctx, c.downloadTimeout)
	defer cancel()

	resp, err := c.get(ctx, "/api/pdf/"+url.PathEscape(file))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("results: la API respondió %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxPDFBytes+1))
	if err != nil {
		return nil, errors.New("results: lectura del PDF fallida")
	}
	if len(data) > MaxPDFBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}
