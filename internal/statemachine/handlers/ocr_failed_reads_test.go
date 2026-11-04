package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// ocrNoCupsResponse es una lectura que el OCR completa sin ningún procedimiento.
const ocrNoCupsResponse = `{"choices":[{"message":{"content":"{\"cups\":[],\"entity\":\"\",\"error\":\"\"}"}}]}`

// scriptedOCRServer responde, en orden, con la lista dada: "error" es un 400 (AnalyzeDocument
// devuelve error), cualquier otra cosa es el cuerpo de un 200. Cuenta las llamadas.
func scriptedOCRServer(t *testing.T, calls *int32, responses ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(atomic.AddInt32(calls, 1)) - 1
		if i >= len(responses) {
			i = len(responses) - 1
		}
		if responses[i] == "error" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[i]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// persist aplica el resultado a la sesión como lo haría SaveState y el turno siguiente, que la
// recarga de la BD: primero los borrados, luego lo escrito, y el estado nuevo.
func persist(sess *session.Session, res *sm.StateResult) {
	for _, k := range res.ClearCtx {
		delete(sess.Context, k)
	}
	for k, v := range res.UpdateCtx {
		sess.Context[k] = v
	}
	sess.CurrentState = res.NextState
}

func hasRetryButton(res *sm.StateResult) bool {
	for _, m := range res.Messages {
		if bm, ok := m.(*sm.ButtonMessage); ok {
			for _, b := range bm.Buttons {
				if b.Payload == "retry_photo" {
					return true
				}
			}
		}
	}
	return false
}

// B-05: a la 3.ª lectura fallida de la orden en la sesión (error, sin éxito o sin CUPS) no se
// ofrece «Enviar de nuevo»: se escala.
func TestUploadOrder_ThirdFailedReadEscalates(t *testing.T) {
	var calls int32
	ocrSrv := scriptedOCRServer(t, &calls, "error", ocrNoCupsResponse, ocrNoCupsResponse, ocrNoCupsResponse)
	fileSrv := newMockFileServer()
	defer fileSrv.Close()
	handler := uploadMedicalOrderHandler(services.NewOCRServiceForTest(ocrSrv.URL), nil)

	sess := testSess(sm.StateUploadMedicalOrder)
	for i := 1; i <= 2; i++ {
		res, err := handler(context.Background(), sess, imageMsg(fileSrv.URL+"/orden.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		if res.NextState != sm.StateUploadMedicalOrder || !hasRetryButton(res) {
			t.Fatalf("lectura fallida %d: esperaba «Enviar de nuevo» en el mismo paso, got %s", i, res.NextState)
		}
		persist(sess, res)
	}

	res, err := handler(context.Background(), sess, imageMsg(fileSrv.URL+"/orden.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateEscalateToAgent {
		t.Fatalf("3.ª lectura fallida: esperaba ESCALATE_TO_AGENT, got %s", res.NextState)
	}
	if hasRetryButton(res) {
		t.Error("a la 3.ª no se ofrece «Enviar de nuevo»")
	}
	if got := res.UpdateCtx[sm.CtxEscalationReason]; got != sm.EscalationReasonOCRFailedReads {
		t.Errorf("motivo = %q, quería %q", got, sm.EscalationReasonOCRFailedReads)
	}
	if len(res.Messages) != 1 || res.Messages[0].(*sm.TextMessage).Text != ocrUnreadEscalationText {
		t.Errorf("texto al paciente: %+v", res.Messages)
	}
	if res.UpdateCtx[sm.CtxOCRFailureCause] == "" {
		t.Error("el agente debe ver la causa de la última lectura fallida")
	}
	persist(sess, escalationExit(res)) // la salida del paso de escalación, que sigue encadenado
	if got := sess.GetContext(sm.CtxOCRFailedReads); got != "" {
		t.Errorf("la escalación reinicia el contador (el agente puede pedir la foto otra vez): %q", got)
	}
}

// Una lectura buena reinicia el contador: tras ella, otras dos fallidas siguen ofreciendo reintento.
func TestUploadOrder_SuccessfulReadResetsFailedReads(t *testing.T) {
	var calls int32
	ocrSrv := scriptedOCRServer(t, &calls, "error", ocrNoCupsResponse, ocrCupsResponse("883141", "RM"),
		ocrNoCupsResponse, ocrNoCupsResponse)
	fileSrv := newMockFileServer()
	defer fileSrv.Close()
	handler := uploadMedicalOrderHandler(services.NewOCRServiceForTest(ocrSrv.URL), nil)

	sess := testSess(sm.StateUploadMedicalOrder)
	want := []string{
		sm.StateUploadMedicalOrder, sm.StateUploadMedicalOrder, sm.StateValidateOCR,
		sm.StateUploadMedicalOrder, sm.StateUploadMedicalOrder,
	}
	for i, next := range want {
		sess.CurrentState = sm.StateUploadMedicalOrder // tras la lectura buena el paciente vuelve a subir («No, corregir»)
		res, err := handler(context.Background(), sess, imageMsg(fileSrv.URL+"/orden.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		if res.NextState != next {
			t.Fatalf("lectura %d: got %s, quería %s", i+1, res.NextState, next)
		}
		persist(sess, res)
	}
}

// Las páginas añadidas en «¿Es correcto?» cuentan para el mismo tope: tras 3 fallidas seguidas no
// se vuelve a llamar al OCR y se sigue con lo leído (no se escala: la orden ya tiene CUPS válidos).
func TestPhotoAppend_FailedReadsStopCallingOCR(t *testing.T) {
	var calls int32
	ocrSrv := scriptedOCRServer(t, &calls, ocrNoCupsResponse)
	fileSrv := newMockFileServer()
	defer fileSrv.Close()
	intercept := PhotoAppendInterceptor(services.NewOCRServiceForTest(ocrSrv.URL))

	sess := testSess(sm.StateConfirmOCRResult)
	sess.SetContext("ocr_cups_json", `[{"cups_code":"883141","cups_name":"RM","quantity":1}]`)
	for i := 1; i <= 4; i++ {
		res, ok := intercept(context.Background(), sess, imageMsg(fileSrv.URL+"/hoja.jpg"))
		if !ok || res.NextState != sm.StateConfirmOCRResult {
			t.Fatalf("imagen %d: esperaba seguir en CONFIRM_OCR_RESULT, got ok=%v %+v", i, ok, res)
		}
		persist(sess, res)
	}
	if calls != int32(maxFailedOrderReads) {
		t.Errorf("llamadas al OCR = %d, quería %d (la 4.ª imagen no se lee)", calls, maxFailedOrderReads)
	}
}

// La orden guardada (stash) es un intento único y acotado: si falla no suma al contador.
func TestAskMedicalOrder_StashFailureDoesNotCount(t *testing.T) {
	var calls int32
	ocrSrv := scriptedOCRServer(t, &calls, ocrNoCupsResponse)
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	sess := testSess(sm.StateAskMedicalOrder)
	sess.SetContext("wl_offer_done", "1")
	sess.SetContext(sm.CtxOCRFailedReads, "2")
	sess.SetContext(sm.StashedOrderURLsKey, `["`+fileSrv.URL+`/p1.jpg"]`)
	res, err := askMedicalOrderHandler(nil, services.NewOCRServiceForTest(ocrSrv.URL), nil)(
		context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateUploadMedicalOrder {
		t.Fatalf("la orden guardada ilegible pide la foto como siempre, got %s", res.NextState)
	}
	if _, ok := res.UpdateCtx[sm.CtxOCRFailedReads]; ok {
		t.Error("el intento de la orden guardada no suma al contador")
	}
}

// Toda escalación borra el contador, no solo la del tope: si el paciente pide el agente tras dos
// fallos y el agente devuelve «Pedir de nuevo la foto», vuelve a tener sus intentos.
func TestEscalationExit_ClearsFailedReads(t *testing.T) {
	sess := testSess(sm.StateUploadMedicalOrder)
	sess.SetContext(sm.CtxOCRFailedReads, "2")
	res, err := uploadNonMedia(sess, nil, postbackM("escalate_agent"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateEscalateToAgent {
		t.Fatalf("esperaba ESCALATE_TO_AGENT, got %s", res.NextState)
	}
	if !hasClear(escalationExit(res), sm.CtxOCRFailedReads) {
		t.Error("escalationExit debe borrar CtxOCRFailedReads")
	}
}

// Confirmar lo leído («Sí, correcto») cierra la orden: los añadidos fallidos no pasan a la
// siguiente orden de la misma sesión.
func TestConfirmOCR_CorrectClearsFailedReads(t *testing.T) {
	sess := testSess(sm.StateConfirmOCRResult)
	sess.SetContext("ocr_cups_json", `[{"cups_code":"883141","cups_name":"RM","quantity":1}]`)
	sess.SetContext(sm.CtxOCRFailedReads, "2")
	res, err := confirmOCRResultHandler(activeCatalogRepo(), nil)(context.Background(), sess, postbackM("ocr_correct"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasClear(res, sm.CtxOCRFailedReads) {
		t.Errorf("ocr_correct debe borrar CtxOCRFailedReads: %v", res.ClearCtx)
	}
}

// La orden de la EMG (UPLOAD_EMG_ORDER) tiene el mismo tope: a la 3.ª lectura fallida seguida se
// escala con el motivo y el texto de B-05, sin otro «Enviar de nuevo».
func TestUploadEmgOrder_ThirdFailedReadEscalates(t *testing.T) {
	var calls int32
	ocrSrv := scriptedOCRServer(t, &calls, "error", ocrNoCupsResponse, ocrNoCupsResponse)
	fileSrv := newMockFileServer()
	defer fileSrv.Close()
	handler := uploadEmgOrderHandler(services.NewOCRServiceForTest(ocrSrv.URL), nil)

	sess := testSess(sm.StateUploadEmgOrder)
	sess.Context["emg_dep_cups_json"] = `[{"cups_code":"891509","cups_name":"NEUROCONDUCCION","quantity":4}]`
	for i := 1; i <= 2; i++ {
		res, err := handler(context.Background(), sess, imageMsg(fileSrv.URL+"/emg.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		if res.NextState != sm.StateUploadEmgOrder || !hasRetryButton(res) {
			t.Fatalf("lectura fallida %d: esperaba «Enviar de nuevo» en el mismo paso, got %s", i, res.NextState)
		}
		persist(sess, res)
	}

	res, err := handler(context.Background(), sess, imageMsg(fileSrv.URL+"/emg.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateEscalateToAgent || hasRetryButton(res) {
		t.Fatalf("3.ª lectura fallida: esperaba ESCALATE_TO_AGENT sin reintento, got %s", res.NextState)
	}
	if got := res.UpdateCtx[sm.CtxEscalationReason]; got != sm.EscalationReasonOCRFailedReads {
		t.Errorf("motivo = %q, quería %q", got, sm.EscalationReasonOCRFailedReads)
	}
	if len(res.Messages) != 1 || res.Messages[0].(*sm.TextMessage).Text != ocrUnreadEscalationText {
		t.Errorf("texto al paciente: %+v", res.Messages)
	}
	if res.UpdateCtx[sm.CtxOCRFailureCause] != sm.OCRFailureNoProcedures {
		t.Errorf("causa de la última lectura = %q", res.UpdateCtx[sm.CtxOCRFailureCause])
	}
	persist(sess, escalationExit(res))
	if got := sess.GetContext(sm.CtxOCRFailedReads); got != "" {
		t.Errorf("la escalación reinicia el contador: %q", got)
	}
}

// Una lectura buena de la orden de la EMG borra el contador y sigue a VALIDATE_OCR con la fusión de
// siempre.
func TestUploadEmgOrder_SuccessfulReadResetsFailedReads(t *testing.T) {
	var calls int32
	ocrSrv := scriptedOCRServer(t, &calls, "error", ocrNoCupsResponse, ocrCupsResponse("930810", "ELECTROMIOGRAFIA"))
	fileSrv := newMockFileServer()
	defer fileSrv.Close()
	handler := uploadEmgOrderHandler(services.NewOCRServiceForTest(ocrSrv.URL), nil)

	sess := testSess(sm.StateUploadEmgOrder)
	sess.Context["emg_dep_cups_json"] = `[{"cups_code":"891509","cups_name":"NEUROCONDUCCION","quantity":4}]`
	var res *sm.StateResult
	for i := 1; i <= 3; i++ {
		var err error
		if res, err = handler(context.Background(), sess, imageMsg(fileSrv.URL+"/emg.jpg")); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			persist(sess, res)
		}
	}
	if res.NextState != sm.StateValidateOCR {
		t.Fatalf("la lectura buena sigue a VALIDATE_OCR, got %s", res.NextState)
	}
	persist(sess, res)
	if got := sess.GetContext(sm.CtxOCRFailedReads); got != "" {
		t.Errorf("la lectura buena borra el contador: %q", got)
	}
	if got := sess.GetContext("ocr_cups_json"); !strings.Contains(got, "930810") || !strings.Contains(got, "891509") {
		t.Errorf("la fusión EMG + dependientes no cambia: %s", got)
	}
}
