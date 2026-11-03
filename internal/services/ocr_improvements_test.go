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
	if len(r.responses) > 0 {
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
