package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingOpenAI responde con `responses` en orden (la última se repite) y guarda el cuerpo de cada
// petición, para mirar qué se le mandó a OpenAI. Un status distinto de 200 en `statuses[i]` hace
// fallar esa llamada.
type recordingOpenAI struct {
	mu        sync.Mutex
	bodies    []map[string]interface{}
	responses []string
	statuses  []int
	// respond, si está, decide el contenido según la petición (manda sobre responses).
	respond func(body map[string]interface{}) string
}

func (r *recordingOpenAI) handler(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]interface{}
	_ = json.Unmarshal(raw, &body)
	r.mu.Lock()
	i := len(r.bodies)
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()
	if i < len(r.statuses) && r.statuses[i] != http.StatusOK {
		w.WriteHeader(r.statuses[i])
		return
	}
	content := `{"cups": []}`
	if r.respond != nil {
		content = r.respond(body)
	} else if len(r.responses) > 0 {
		content = r.responses[min(i, len(r.responses)-1)]
	}
	openAIHandler(content)(w, req)
}

func newRecordingOpenAI(t *testing.T, responses ...string) (*recordingOpenAI, *OCRService) {
	t.Helper()
	rec := &recordingOpenAI{responses: responses}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	t.Cleanup(srv.Close)
	return rec, NewOCRServiceForTest(srv.URL)
}

// imageParts devuelve las partes image_url del mensaje del usuario de una petición.
func imageParts(body map[string]interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	msgs, _ := body["messages"].([]interface{})
	for _, m := range msgs {
		content, ok := m.(map[string]interface{})["content"].([]interface{})
		if !ok {
			continue
		}
		for _, p := range content {
			part, _ := p.(map[string]interface{})
			if part["type"] == "image_url" {
				img, _ := part["image_url"].(map[string]interface{})
				out = append(out, img)
			}
		}
	}
	return out
}

// H1: sin pedir detail=high, OpenAI puede bajar la foto a 512 px y los dígitos del código quedan
// ilegibles. Toda imagen que sale a Vision lo pide, sea una hoja suelta o varias.
func TestVision_EveryImageAsksHighDetail(t *testing.T) {
	rec, svc := newRecordingOpenAI(t, `{"cups": [{"cups_code": "890274", "cups_name": "NEURO", "quantity": 1}]}`)
	ctx := context.Background()
	if _, err := svc.AnalyzeImage(ctx, "data:image/jpeg;base64,AAAA"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnalyzeImages(ctx, []string{"data:image/jpeg;base64,AAAA", "data:image/jpeg;base64,BBBB"}); err != nil {
		t.Fatal(err)
	}
	total := 0
	for i, body := range rec.bodies {
		for _, img := range imageParts(body) {
			total++
			if img["detail"] != "high" {
				t.Errorf("petición %d: imagen sin detail=high: %v", i, img["detail"])
			}
		}
	}
	if total != 3 {
		t.Fatalf("se esperaban 3 imágenes enviadas, hubo %d", total)
	}
}

// pdfFileServer sirve un PDF mínimo (basta la firma para que se detecte application/pdf).
func pdfFileServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.4 prueba"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/orden.pdf"
}

// fakePages simula a Ghostscript: n hojas convertidas, sin capa de texto.
func fakePages(svc *OCRService, n int) {
	svc.pdfToImages = func(_ context.Context, _ string, _, last int) ([]string, error) {
		pages := n
		if last > 0 && last < pages {
			pages = last
		}
		uris := make([]string, pages)
		for i := range uris {
			uris[i] = "data:image/jpeg;base64,PAGE" + string(rune('1'+i))
		}
		return uris, nil
	}
	svc.pdfText = func(context.Context, string) string { return "" }
}

const (
	cupsPage1     = `{"cups": [{"cups_code": "890274", "cups_name": "CONSULTA NEUROLOGIA", "quantity": 1}]}`
	cupsBothPages = `{"cups": [{"cups_code": "890274", "cups_name": "CONSULTA NEUROLOGIA", "quantity": 1}, {"cups_code": "883101", "cups_name": "RESONANCIA CEREBRO", "quantity": 1}]}`
)

// byImageCount responde con la hoja 1 si la petición trae una sola imagen y con las dos si trae más:
// así un test ve si el OCR miró todas las hojas o se quedó en la primera.
func byImageCount(body map[string]interface{}) string {
	if len(imageParts(body)) > 1 {
		return cupsBothPages
	}
	return cupsPage1
}

// H3: un PDF cuya hoja 1 trae un CUPS y la hoja 2 otro se lee COMPLETO. Antes se leía la hoja 1 y,
// como ya traía un código, la 2 nunca se miraba: el paciente veía un resumen que parecía completo.
func TestAnalyzeDocument_PDFReadsEveryPageInOneCall(t *testing.T) {
	rec, svc := newRecordingOpenAI(t)
	rec.respond = byImageCount
	fakePages(svc, 2)

	res, err := svc.AnalyzeDocument(context.Background(), pdfFileServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cups) != 2 {
		t.Fatalf("se esperaban los CUPS de las dos hojas, llegaron %d: %+v", len(res.Cups), res.Cups)
	}
	if len(rec.bodies) != 1 || len(imageParts(rec.bodies[0])) != 2 {
		t.Fatalf("las dos hojas van en UNA llamada: %d llamadas", len(rec.bodies))
	}
}

// H3: si la lectura conjunta falla, se reintenta solo con la primera hoja (donde suele estar la orden).
func TestAnalyzeDocument_PDFMultiPageFailureFallsBackToFirstPage(t *testing.T) {
	rec, svc := newRecordingOpenAI(t)
	rec.respond = byImageCount
	rec.statuses = []int{http.StatusBadRequest}
	fakePages(svc, 3)

	res, err := svc.AnalyzeDocument(context.Background(), pdfFileServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 2 {
		t.Fatalf("se esperaban 2 llamadas (todas las hojas y luego la primera), hubo %d", len(rec.bodies))
	}
	if got := imageParts(rec.bodies[1]); len(got) != 1 || got[0]["url"] != "data:image/jpeg;base64,PAGE1" {
		t.Fatalf("el reintento va solo con la primera hoja: %v", got)
	}
	if len(res.Cups) != 1 || res.Cups[0].Code != "890274" {
		t.Fatalf("resultado del reintento: %+v", res.Cups)
	}
}

// H3: con un PDF de una sola hoja nada cambia: una llamada con esa hoja.
func TestAnalyzeDocument_SinglePagePDFUnchanged(t *testing.T) {
	rec, svc := newRecordingOpenAI(t)
	rec.respond = byImageCount
	fakePages(svc, 1)

	if _, err := svc.AnalyzeDocument(context.Background(), pdfFileServer(t)); err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 1 || len(imageParts(rec.bodies[0])) != 1 {
		t.Fatalf("una hoja = una llamada con una imagen: %d llamadas", len(rec.bodies))
	}
}

// userText devuelve el contenido del mensaje del usuario cuando es texto plano (sin imágenes).
func userText(body map[string]interface{}) string {
	msgs, _ := body["messages"].([]interface{})
	for _, m := range msgs {
		msg, _ := m.(map[string]interface{})
		if msg["role"] == "user" {
			text, _ := msg["content"].(string)
			return text
		}
	}
	return ""
}

// orderText es la capa de texto de una orden generada por un sistema (más de 40 palabras).
var orderText = strings.Repeat("ORDEN MEDICA PACIENTE ", 15) + "890274 CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA"

// withTextLayer simula un PDF con capa de texto y cuenta cuántas veces se rasterizó.
func withTextLayer(svc *OCRService, text string) *int {
	rasterized := 0
	fakePages(svc, 2)
	images := svc.pdfToImages
	svc.pdfToImages = func(ctx context.Context, path string, first, last int) ([]string, error) {
		rasterized++
		return images(ctx, path, first, last)
	}
	svc.pdfText = func(context.Context, string) string { return text }
	return &rasterized
}

// H2: un PDF con capa de texto se lee del texto literal, sin visión y sin rasterizar.
func TestAnalyzeDocument_PDFTextLayerReadWithoutVision(t *testing.T) {
	rec, svc := newRecordingOpenAI(t)
	rec.respond = func(body map[string]interface{}) string {
		if strings.Contains(userText(body), "890274") {
			return cupsPage1
		}
		return `{"cups": []}`
	}
	rasterized := withTextLayer(svc, orderText)

	res, err := svc.AnalyzeDocument(context.Background(), pdfFileServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if *rasterized != 0 {
		t.Errorf("con capa de texto no se rasteriza: %d conversiones", *rasterized)
	}
	if len(rec.bodies) != 1 || len(imageParts(rec.bodies[0])) != 0 {
		t.Fatalf("una sola llamada, sin imágenes: %d llamadas", len(rec.bodies))
	}
	if len(res.Cups) != 1 || res.Cups[0].Code != "890274" {
		t.Fatalf("lo leído del texto: %+v", res.Cups)
	}
}

// H2, salvaguarda del dueño: si del texto no sale ningún CUPS con código, se cae al camino de las
// imágenes; un escaneo con una capa de texto mala no empeora.
func TestAnalyzeDocument_PDFTextWithoutCupsFallsBackToImages(t *testing.T) {
	rec, svc := newRecordingOpenAI(t)
	rec.respond = func(body map[string]interface{}) string {
		if len(imageParts(body)) == 0 {
			return `{"cups": [{"cups_code": "", "cups_name": "CONSULTA NEUROLOGIA", "quantity": 1}]}`
		}
		return byImageCount(body)
	}
	rasterized := withTextLayer(svc, orderText)

	res, err := svc.AnalyzeDocument(context.Background(), pdfFileServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if *rasterized != 1 || len(rec.bodies) != 2 {
		t.Fatalf("texto y luego imágenes: %d conversiones, %d llamadas", *rasterized, len(rec.bodies))
	}
	if len(res.Cups) != 2 {
		t.Fatalf("se usa la lectura de las imágenes: %+v", res.Cups)
	}
}

// H2: si el análisis del texto falla, también se cae a las imágenes.
func TestAnalyzeDocument_PDFTextAnalysisErrorFallsBackToImages(t *testing.T) {
	rec, svc := newRecordingOpenAI(t)
	rec.respond = byImageCount
	rec.statuses = []int{http.StatusBadRequest}
	withTextLayer(svc, orderText)

	res, err := svc.AnalyzeDocument(context.Background(), pdfFileServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 2 || len(res.Cups) != 2 {
		t.Fatalf("texto fallido y luego imágenes: %d llamadas, %+v", len(rec.bodies), res.Cups)
	}
}

// H2: menos de 40 palabras no es una capa de texto útil (el membrete de un escaneo): imágenes.
func TestAnalyzeDocument_PDFShortTextGoesToImages(t *testing.T) {
	rec, svc := newRecordingOpenAI(t)
	rec.respond = byImageCount
	rasterized := withTextLayer(svc, "CLINICA NEURO 890274 CONSULTA NEUROLOGIA")

	if _, err := svc.AnalyzeDocument(context.Background(), pdfFileServer(t)); err != nil {
		t.Fatal(err)
	}
	if *rasterized != 1 || len(rec.bodies) != 1 || len(imageParts(rec.bodies[0])) != 2 {
		t.Fatalf("directo a imágenes: %d conversiones, %d llamadas", *rasterized, len(rec.bodies))
	}
}

// jpegFileServer sirve una imagen mínima (firma JPEG).
func jpegFileServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/orden.jpg"
}

// H4: la segunda lectura pide UNA sola cosa —el código del procedimiento nombrado— con el modelo de
// OPENAI_DOC_MODEL y la imagen en detalle alto.
func TestReadCupsCode_DedicatedReadWithDocModel(t *testing.T) {
	rec, svc := newRecordingOpenAI(t, `{"cups": "890274", "como_aparece": "89.02.74", "confianza": "alta"}`)
	svc.SetDocumentModel("modelo-doc")

	code, err := svc.ReadCupsCode(context.Background(), jpegFileServer(t), "CONSULTA NEUROLOGIA")
	if err != nil {
		t.Fatal(err)
	}
	if code != "890274" {
		t.Fatalf("código = %q", code)
	}
	body := rec.bodies[0]
	if body["model"] != "modelo-doc" {
		t.Errorf("modelo = %v, quería el de OPENAI_DOC_MODEL", body["model"])
	}
	imgs := imageParts(body)
	if len(imgs) != 1 || imgs[0]["detail"] != "high" {
		t.Errorf("una imagen en detalle alto: %v", imgs)
	}
	raw, _ := json.Marshal(body["messages"])
	if !strings.Contains(string(raw), "CONSULTA NEUROLOGIA") {
		t.Error("el prompt nombra el procedimiento cuyo código se pide")
	}
}

// H4: sin modelo de documento configurado, la segunda lectura usa el modelo de siempre.
func TestReadCupsCode_DefaultsToMainModel(t *testing.T) {
	rec, svc := newRecordingOpenAI(t, `{"cups": "890274", "confianza": "media"}`)
	if _, err := svc.ReadCupsCode(context.Background(), jpegFileServer(t), "X"); err != nil {
		t.Fatal(err)
	}
	if rec.bodies[0]["model"] != "test-model" {
		t.Errorf("modelo = %v", rec.bodies[0]["model"])
	}
}

// H4: nunca se adivina. Confianza baja, código nulo o respuesta ilegible → vacío.
func TestReadCupsCode_NeverGuesses(t *testing.T) {
	for _, content := range []string{
		`{"cups": "890274", "confianza": "baja"}`,
		`{"cups": null, "confianza": "baja"}`,
		`no es json`,
	} {
		_, svc := newRecordingOpenAI(t, content)
		code, err := svc.ReadCupsCode(context.Background(), jpegFileServer(t), "X")
		if err != nil {
			t.Fatal(err)
		}
		if code != "" {
			t.Errorf("%s → %q, quería vacío", content, code)
		}
	}
}

// H4: en un PDF el procedimiento puede estar en cualquier hoja (H3): van todas, como en la lectura
// general.
func TestReadCupsCode_PDFSendsEveryPage(t *testing.T) {
	rec, svc := newRecordingOpenAI(t, `{"cups": "89.02.74", "confianza": "alta"}`)
	fakePages(svc, 2)
	code, err := svc.ReadCupsCode(context.Background(), pdfFileServer(t), "X")
	if err != nil {
		t.Fatal(err)
	}
	if code != "890274" {
		t.Errorf("los puntos no son parte del código: %q", code)
	}
	if len(imageParts(rec.bodies[0])) != 2 {
		t.Errorf("las dos hojas van en la segunda lectura")
	}
}
