package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// newSequentialOCRServer devuelve una respuesta distinta por llamada (1ª hoja, 2ª hoja, …). La última
// se repite si hubiera más llamadas de las previstas, para que un test no dependa del conteo exacto.
func newSequentialOCRServer(responses ...string) *httptest.Server {
	var mu sync.Mutex
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		if i >= len(responses) {
			i = len(responses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(responses[i]))
	}))
}

func ocrCupsResponse(code, name string) string {
	return `{"choices":[{"message":{"content":"{\"cups\":[{\"cups_code\":\"` + code +
		`\",\"cups_name\":\"` + name + `\",\"quantity\":1}],\"entity\":\"\",\"error\":\"\"}"}}]}`
}

// TestAskMedicalOrder_StashedMultiPage_ReadsEveryPage: el paciente mandó su orden en dos fotos antes de
// llegar al paso de la orden (auditoría ciclo 133, sess:7cbdfb83). Al consumirlas, la cita debe salir con
// los CUPS de AMBAS hojas; leer solo la primera agenda media orden sin ninguna señal de error.
func TestAskMedicalOrder_StashedMultiPage_ReadsEveryPage(t *testing.T) {
	ocrSrv := newSequentialOCRServer(
		ocrCupsResponse("890271", "EMG"),
		ocrCupsResponse("883101", "RM"),
	)
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	ocrSvc := services.NewOCRServiceForTest(ocrSrv.URL)

	sess := testSess(sm.StateAskMedicalOrder)
	sess.Context["stashed_order_urls"] = `["` + fileSrv.URL + `/p1.jpg","` + fileSrv.URL + `/p2.jpg"]`

	res, err := askMedicalOrderHandler(nil, ocrSvc, nil)(context.Background(), sess, textM("x"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateValidateOCR {
		t.Fatalf("NextState = %s, esperaba VALIDATE_OCR", res.NextState)
	}
	cups := res.UpdateCtx["ocr_cups_json"]
	for _, must := range []string{"890271", "883101"} {
		t.Logf("cups=%s", cups)
		if !strings.Contains(cups, must) {
			t.Errorf("falta el CUP %s de una de las hojas guardadas; got %s", must, cups)
		}
	}
}

// TestAskMedicalOrder_StashConsumed_ClearsStashFromDB: consumida la orden guardada, la clave debe
// borrarse de VERDAD (ClearCtx → repo.ClearContext), no solo del mapa en memoria.
func TestAskMedicalOrder_StashConsumed_ClearsStashFromDB(t *testing.T) {
	ocrSrv := newSequentialOCRServer(ocrCupsResponse("890271", "EMG"))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	sess := testSess(sm.StateAskMedicalOrder)
	sess.Context["stashed_order_urls"] = `["` + fileSrv.URL + `/p1.jpg"]`

	res, err := askMedicalOrderHandler(nil, services.NewOCRServiceForTest(ocrSrv.URL), nil)(context.Background(), sess, textM("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(res.ClearCtx, "stashed_order_urls", "stashed_order_url") {
		t.Errorf("el stash consumido debe borrarse de la BD; ClearCtx=%v", res.ClearCtx)
	}
}

// TestAskMedicalOrder_StashFailure_ClearsStashFromDB cubre el bug medido el 2026-08-04 (auditoría ciclo
// 133): en sess:7cbdfb83 el MISMO stash falló dos veces (07:11:19 y 07:15:51) sin que el paciente hubiera
// enviado ninguna foto en el medio. La causa es que el camino de fallo limpiaba con sess.SetContext —
// que solo toca el mapa en memoria — mientras el de éxito usa ClearCtx, que sí llega a la BD. Como la
// sesión se recarga de BD en el mensaje siguiente, la URL rota vuelve y se reintenta (una descarga y un
// OCR desperdiciados por pasada, y un evento de fallo que infla la estadística).
//
// Se usa la clave LEGADA (stashed_order_url) a propósito: las sesiones vivas al desplegar la traen así.
func TestAskMedicalOrder_StashFailure_ClearsStashFromDB(t *testing.T) {
	ocrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("internal server error"))
	}))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	sess := testSess(sm.StateAskMedicalOrder)
	sess.Context["stashed_order_url"] = fileSrv.URL + "/rota.jpg"

	res, err := askMedicalOrderHandler(nil, services.NewOCRServiceForTest(ocrSrv.URL), nil)(context.Background(), sess, textM("x"))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("esperaba resultado (el flujo sigue pidiendo la foto)")
	}
	if !containsAll(res.ClearCtx, "stashed_order_urls", "stashed_order_url") {
		t.Errorf("el stash que falló debe borrarse de la BD, no solo de memoria; ClearCtx=%v", res.ClearCtx)
	}
}

// TestAskMedicalOrder_StashClearSurvivesAutoChain: el borrado del stash debe llegar al resultado FINAL,
// no solo al del handler. VALIDATE_OCR es automático, así que la máquina encadena y recombina
// (machine.go:207); si el ClearCtx se perdiera ahí, SaveState nunca borraría la clave y el bug seguiría
// vivo aunque el test del handler pasara.
func TestAskMedicalOrder_StashClearSurvivesAutoChain(t *testing.T) {
	ocrSrv := newSequentialOCRServer(ocrCupsResponse("890271", "EMG"))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	m := sm.NewMachine()
	m.Register(sm.StateAskMedicalOrder, askMedicalOrderHandler(nil, services.NewOCRServiceForTest(ocrSrv.URL), nil))
	m.Register(sm.StateValidateOCR, func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		return sm.NewResult(sm.StateConfirmOCRResult), nil
	})

	sess := testSess(sm.StateAskMedicalOrder)
	sess.Context["stashed_order_urls"] = `["` + fileSrv.URL + `/p1.jpg"]`

	res, err := m.Process(context.Background(), sess, textM("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(res.ClearCtx, "stashed_order_urls", "stashed_order_url") {
		t.Errorf("el borrado del stash debe sobrevivir al auto-chain; ClearCtx=%v", res.ClearCtx)
	}
}

func containsAll(haystack []string, needles ...string) bool {
	set := map[string]bool{}
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}
