package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// fakePages simula a Ghostscript: n hojas convertidas.
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
