package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// fakeCupsReader es la segunda lectura del código (H4) y la elección por nombre, guionizadas.
type fakeCupsReader struct {
	code       string
	calls      int
	imageCalls int   // cuántas veces se bajó y convirtió la orden (OrderImages)
	noImages   bool  // OrderImages no da imágenes (ni imagen ni PDF)
	imagesErr  error // OrderImages falla
	lastURL    string
	lastName   string

	choice         string // lo que devuelve la elección por nombre ("" = sin confianza alta)
	choiceCalls    int
	lastCandidates []services.CupsCandidate
}

func (f *fakeCupsReader) OrderImages(_ context.Context, documentURL string) ([]string, error) {
	f.imageCalls++
	f.lastURL = documentURL
	if f.imagesErr != nil || f.noImages {
		return nil, f.imagesErr
	}
	return []string{"data:image/jpeg;base64,orden"}, nil
}

func (f *fakeCupsReader) ReadCupsCode(_ context.Context, _ []string, procedureName string) (string, error) {
	f.calls++
	f.lastName = procedureName
	return f.code, nil
}

func (f *fakeCupsReader) ChooseCupsByName(_ context.Context, _ string, candidates []services.CupsCandidate) (string, error) {
	f.choiceCalls++
	f.lastCandidates = candidates
	return f.choice, nil
}

// resolveCatalog es un catálogo pequeño con nombres como los de cups_procedimientos.
var resolveCatalog = []domain.Procedure{
	{ID: 1, Code: "890274", Name: "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA", IsActive: true},
	{ID: 2, Code: "890374", Name: "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGIA", IsActive: true},
	{ID: 3, Code: "883101", Name: "RESONANCIA MAGNETICA DE CEREBRO", IsActive: true},
	{ID: 4, Code: "881431", Name: "ECOGRAFIA OBSTETRICA TRANSABDOMINAL", IsActive: true},
	{ID: 5, Code: "881436", Name: "ECOGRAFIA OBSTETRICA CON TRASLUCENCIA NUCAL", IsActive: true},
}

// catalogRepo responde con ese catálogo (FindByCode ve también los inactivos, FindAllActive solo los
// activos) y cuenta cuántas veces se pidió el catálogo entero.
func catalogRepo(catalog []domain.Procedure, allCalls *int) *mockProcedureRepo {
	return &mockProcedureRepo{
		findByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
			for i := range catalog {
				if catalog[i].Code == code {
					p := catalog[i]
					return &p, nil
				}
			}
			return nil, nil
		},
		findAllActiveFn: func(context.Context) ([]domain.Procedure, error) {
			*allCalls++
			var active []domain.Procedure
			for _, p := range catalog {
				if p.IsActive {
					active = append(active, p)
				}
			}
			return active, nil
		},
	}
}

const testOrderURL = "https://media.example/orden.jpg"

// runValidate corre VALIDATE_OCR con esos CUPS leídos; orderURL vacío = sin imagen de la orden.
func runValidate(t *testing.T, cups []services.CUPSEntry, orderURL string, reader CupsCodeReader) (*sm.StateResult, *observability.CaptureSink, int) {
	t.Helper()
	return runValidateWith(t, resolveCatalog, cups, orderURL, reader)
}

// runValidateWith es runValidate con otro catálogo.
func runValidateWith(t *testing.T, catalog []domain.Procedure, cups []services.CUPSEntry, orderURL string, reader CupsCodeReader) (*sm.StateResult, *observability.CaptureSink, int) {
	t.Helper()
	raw, _ := json.Marshal(cups)
	sess := testSess(sm.StateValidateOCR)
	sess.SetContext("ocr_cups_json", string(raw))
	if orderURL != "" {
		sess.SetContext(ctxOCROrderURL, orderURL)
	}
	allCalls := 0
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	res, err := validateOCRHandler(catalogRepo(catalog, &allCalls), reader)(context.Background(), sess, textM(""))
	stop()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.ClearCtx, ctxOCROrderURL) {
		t.Errorf("la URL de la orden vive solo durante VALIDATE_OCR: %v", res.ClearCtx)
	}
	return res, sink, allCalls
}

func validatedCodes(t *testing.T, res *sm.StateResult) []string {
	t.Helper()
	var cups []services.CUPSEntry
	if err := json.Unmarshal([]byte(res.UpdateCtx["ocr_cups_json"]), &cups); err != nil {
		t.Fatalf("ocr_cups_json: %v", err)
	}
	codes := make([]string, 0, len(cups))
	for _, c := range cups {
		codes = append(codes, c.Code)
	}
	return codes
}

func corrections(t *testing.T, res *sm.StateResult) []cupsCorrection {
	t.Helper()
	var list []cupsCorrection
	if raw := res.UpdateCtx[ctxCupsCorrections]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			t.Fatalf("correcciones: %v", err)
		}
	}
	return list
}

// H5, rama 1: el código leído no está en el catálogo, la segunda lectura devuelve otro que sí está y
// pasa la validación de especialidad → se corrige, queda anotado y el paciente confirma la lista.
func TestValidateOCR_CorrectedBySecondReading(t *testing.T) {
	reader := &fakeCupsReader{code: "890274"}
	res, sink, _ := runValidate(t, []services.CUPSEntry{
		{Code: "890279", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
	}, testOrderURL, reader)

	if res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("el paciente sigue confirmando la lista: %s", res.NextState)
	}
	if reader.calls != 1 || reader.lastURL != testOrderURL || reader.lastName != "CONSULTA PRIMERA VEZ NEUROLOGIA" {
		t.Errorf("segunda lectura con la URL y el nombre leídos: %+v", reader)
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"890274"}) {
		t.Errorf("CUPS validados = %v", got)
	}
	if got := corrections(t, res); len(got) != 1 || got[0].Read != "890279" || got[0].Used != "890274" || got[0].Via != cupsCorrectedBySecondReading {
		t.Errorf("corrección anotada para el agente: %+v", got)
	}
	if ev := sink.FindStep("agendar", "cups_corregido"); ev == nil || ev.Reason != cupsCorrectedBySecondReading {
		t.Errorf("falta el flow_event cups_corregido: %+v", ev)
	}
	if res.UpdateCtx["ocr_skipped_count"] != "0" {
		t.Errorf("lo corregido no cuenta como omitido: %q", res.UpdateCtx["ocr_skipped_count"])
	}
}

// Varios códigos dudosos en la misma orden: la orden se baja y se convierte UNA vez por pasada y cada
// segunda lectura reutiliza esas imágenes.
func TestValidateOCR_SecondReadingReusesOrderImages(t *testing.T) {
	reader := &fakeCupsReader{code: ""}
	runValidate(t, []services.CUPSEntry{
		{Code: "890279", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
		{Code: "883109", Name: "RESONANCIA MAGNETICA DE CEREBRO", Quantity: 1},
	}, testOrderURL, reader)

	if reader.calls != 2 {
		t.Fatalf("una segunda lectura por código dudoso: %d", reader.calls)
	}
	if reader.imageCalls != 1 || reader.lastURL != testOrderURL {
		t.Errorf("la orden se baja y se convierte una sola vez por pasada: %d veces (%q)", reader.imageCalls, reader.lastURL)
	}
}

// Un fallo al bajar la orden no se guarda: el código siguiente vuelve a intentarlo, como antes.
func TestValidateOCR_OrderImagesFailureIsRetried(t *testing.T) {
	reader := &fakeCupsReader{imagesErr: errors.New("descarga caída")}
	runValidate(t, []services.CUPSEntry{
		{Code: "890279", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
		{Code: "883109", Name: "RESONANCIA MAGNETICA DE CEREBRO", Quantity: 1},
	}, testOrderURL, reader)
	if reader.imageCalls != 2 || reader.calls != 0 {
		t.Errorf("cada código reintenta la descarga y no lee sin imágenes: descargas=%d lecturas=%d", reader.imageCalls, reader.calls)
	}
}

// Si la orden no dio imágenes no hay lectura ni se gasta una llamada del tope.
func TestValidateOCR_NoOrderImagesSpendsNoModelCall(t *testing.T) {
	reader := &fakeCupsReader{noImages: true}
	runValidate(t, []services.CUPSEntry{
		{Code: "890279", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
	}, testOrderURL, reader)
	if reader.imageCalls != 1 || reader.calls != 0 {
		t.Errorf("sin imágenes no se llama al modelo: descargas=%d lecturas=%d", reader.imageCalls, reader.calls)
	}
}

// H5, rama 2: sin imagen no hay segunda lectura; un ÚNICO candidato de nombre parecido a un dígito
// del leído → se corrige.
func TestValidateOCR_CorrectedByOneDigitNameMatch(t *testing.T) {
	reader := &fakeCupsReader{code: "890274"}
	res, sink, _ := runValidate(t, []services.CUPSEntry{
		{Code: "890279", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
	}, "", reader)

	if reader.calls != 0 {
		t.Errorf("sin URL de la orden no hay segunda lectura: %d", reader.calls)
	}
	if res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("estado = %s", res.NextState)
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"890274"}) {
		t.Errorf("CUPS validados = %v", got)
	}
	if got := corrections(t, res); len(got) != 1 || got[0].Via != cupsCorrectedByName {
		t.Errorf("corrección anotada: %+v", got)
	}
	if sink.FindStep("agendar", "cups_corregido") == nil {
		t.Error("falta el flow_event cups_corregido")
	}
}

// H5: un código que hoy se descarta por especialidad (está en el catálogo, pero la orden dice otra
// cosa) también entra a la cascada: el procedimiento de la orden, a un dígito, se corrige.
func TestValidateOCR_SpecialtyMismatchCorrectedByName(t *testing.T) {
	res, _, _ := runValidate(t, []services.CUPSEntry{
		{Code: "881431", Name: "ECOGRAFIA OBSTETRICA CON TRASLUCENCIA NUCAL", Quantity: 1},
	}, "", nil)
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"881436"}) {
		t.Errorf("CUPS validados = %v", got)
	}
}

// H5, rama 3: hay nombres parecidos pero ninguno a un dígito → DUDA: agente con motivo propio, la nota
// con lo leído y los candidatos, y el aviso que ya existía para VALIDATE_OCR.
func TestValidateOCR_DoubtEscalatesWithCandidates(t *testing.T) {
	res, sink, _ := runValidate(t, []services.CUPSEntry{
		{Code: "890274", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
		{Code: "890999", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
	}, "", nil)

	if res.NextState != sm.StateEscalateToAgent {
		t.Fatalf("la duda va a un agente: %s", res.NextState)
	}
	if got := res.UpdateCtx[sm.CtxEscalationReason]; got != sm.EscalationReasonCupsPossibleMisread {
		t.Errorf("motivo = %q", got)
	}
	note := res.UpdateCtx["escalation_note"]
	for _, want := range []string{"890999", "890274"} {
		if !strings.Contains(note, want) {
			t.Errorf("la nota debe traer %s: %q", want, note)
		}
	}
	if got := firstText(t, res); got != orderUnprocessableEscalationText {
		t.Errorf("el paciente recibe el aviso de siempre: %q", got)
	}
	if sink.FindStep("agendar", "cups_duda") == nil {
		t.Error("falta el flow_event cups_duda")
	}
}

// H5, rama 4: ningún nombre parecido en el catálogo → la conducta de hoy: se descarta.
func TestValidateOCR_NoSimilarNameIsDiscardedAsToday(t *testing.T) {
	reader := &fakeCupsReader{code: ""}
	res, sink, _ := runValidate(t, []services.CUPSEntry{
		{Code: "883101", Name: "RESONANCIA MAGNETICA DE CEREBRO", Quantity: 1},
		{Code: "890282", Name: "CONSULTA PRIMERA VEZ OTORRINOLARINGOLOGIA", Quantity: 1},
	}, testOrderURL, reader)

	if res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("estado = %s", res.NextState)
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"883101"}) {
		t.Errorf("CUPS validados = %v", got)
	}
	if res.UpdateCtx["ocr_skipped_count"] != "1" {
		t.Errorf("se omite como hoy: %q", res.UpdateCtx["ocr_skipped_count"])
	}
	if len(corrections(t, res)) != 0 || sink.FindStep("agendar", "cups_corregido") != nil || sink.FindStep("agendar", "cups_duda") != nil {
		t.Error("sin parecido no hay corrección ni duda")
	}
}

// H5: un código válido no dispara nada: ni segunda lectura ni catálogo entero.
func TestValidateOCR_ValidCodeTriggersNothing(t *testing.T) {
	reader := &fakeCupsReader{code: "883101"}
	res, sink, allCalls := runValidate(t, []services.CUPSEntry{
		{Code: "890274", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA", Quantity: 1},
	}, testOrderURL, reader)

	if reader.calls != 0 || allCalls != 0 {
		t.Errorf("un código válido no activa la cascada: lecturas=%d catálogo=%d", reader.calls, allCalls)
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"890274"}) {
		t.Errorf("CUPS validados = %v", got)
	}
	if _, ok := res.UpdateCtx[ctxCupsCorrections]; ok || sink.FindStep("agendar", "cups_corregido") != nil {
		t.Error("sin corrección no se anota nada")
	}
}

// La URL de la orden la deja la lectura de la foto para VALIDATE_OCR; con varias fotos guardadas no se
// sabe de cuál salió cada CUPS y no se deja.
func TestProcessOrderMedia_LeavesOrderURLForSecondReading(t *testing.T) {
	ocrSrv := newSequentialOCRServer(ocrCupsResponse("890274", "NEURO"), ocrCupsResponse("883101", "RM"))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()
	ocrSvc := services.NewOCRServiceForTest(ocrSrv.URL)

	url := fileSrv.URL + "/p1.jpg"
	res, err := processOrderMedia(context.Background(), testSess(sm.StateUploadMedicalOrder), ocrSvc, nil, url)
	if err != nil {
		t.Fatal(err)
	}
	if res.UpdateCtx[ctxOCROrderURL] != url {
		t.Errorf("una foto: su URL queda para la segunda lectura: %q", res.UpdateCtx[ctxOCROrderURL])
	}

	pages := []string{fileSrv.URL + "/p1.jpg", fileSrv.URL + "/p2.jpg"}
	res, err = processStashedOrder(context.Background(), testSess(sm.StateAskMedicalOrder), ocrSvc, nil, pages)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.UpdateCtx[ctxOCROrderURL]; ok {
		t.Errorf("varias fotos: sin URL (no se sabe de cuál salió cada CUPS): %q", res.UpdateCtx[ctxOCROrderURL])
	}
}

// Los pasos de subida empiezan sin la URL de una orden anterior: si un encadenado cortado la dejó
// guardada, no llega a la VALIDATE_OCR de otra imagen. La que deja la foto de este paso sí sigue.
func TestUploadSteps_DropStaleOrderURL(t *testing.T) {
	const stale = "https://media.example/orden-anterior.jpg"
	ctx := context.Background()
	for name, run := range map[string]func(*session.Session) (*sm.StateResult, error){
		"upload": func(s *session.Session) (*sm.StateResult, error) {
			return uploadMedicalOrderHandler(nil, nil)(ctx, s, textM("hola"))
		},
		"upload_emg": func(s *session.Session) (*sm.StateResult, error) {
			return uploadEmgOrderHandler(nil, nil)(ctx, s, textM("hola"))
		},
		"ask_order": func(s *session.Session) (*sm.StateResult, error) {
			s.SetContext("entity_category", "PARTICULAR")
			s.SetContext("_prompted_has_order", "1")
			return askMedicalOrderHandler(nil, nil, nil)(ctx, s, postbackM("has_order_yes"))
		},
	} {
		sess := testSess(sm.StateUploadMedicalOrder)
		sess.SetContext(ctxOCROrderURL, stale)
		res, err := run(sess)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Contains(res.ClearCtx, ctxOCROrderURL) || sess.GetContext(ctxOCROrderURL) != "" {
			t.Errorf("%s: la URL anterior se borra al entrar al paso: clear=%v", name, res.ClearCtx)
		}
	}

	ocrSrv := newSequentialOCRServer(ocrCupsResponse("890274", "NEURO"))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()
	sess := testSess(sm.StateUploadMedicalOrder)
	sess.SetContext(ctxOCROrderURL, stale)
	url := fileSrv.URL + "/p1.jpg"
	res, err := uploadMedicalOrderHandler(services.NewOCRServiceForTest(ocrSrv.URL), nil)(ctx, sess, imageMsg(url))
	if err != nil {
		t.Fatal(err)
	}
	if res.UpdateCtx[ctxOCROrderURL] != url || slices.Contains(res.ClearCtx, ctxOCROrderURL) {
		t.Errorf("la URL de la foto de este paso sigue para VALIDATE_OCR: %q clear=%v", res.UpdateCtx[ctxOCROrderURL], res.ClearCtx)
	}
}

// Nunca se corrige en silencio: el resumen del agente trae las correcciones, antes y después de
// agrupar la orden.
func TestAgentSummary_ShowsCupsCorrections(t *testing.T) {
	sess := testSess(sm.StateConfirmOCRResult)
	raw, _ := json.Marshal([]cupsCorrection{{Read: "890279", Used: "890274", Via: cupsCorrectedBySecondReading, Name: "CONSULTA NEUROLOGIA"}})
	sess.SetContext(ctxCupsCorrections, string(raw))
	const want = "CUPS corregidos: 890279 leído → 890274 (por segunda lectura) · CONSULTA NEUROLOGIA"
	for _, cupsCode := range []string{"", "890274"} {
		if got := buildAgentSummary(sess, cupsCode, "equipo"); !strings.Contains(got, want) {
			t.Errorf("cupsCode=%q: el resumen no trae la corrección: %s", cupsCode, got)
		}
	}
}

// Una página añadida vuelve a pasar por VALIDATE_OCR con el código ya corregido: la corrección se
// conserva mientras el código siga en la orden.
func TestValidateOCR_KeepsPreviousCorrectionsOfCodesStillInOrder(t *testing.T) {
	raw, _ := json.Marshal([]services.CUPSEntry{
		{Code: "890274", Name: "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA", Quantity: 1},
		{Code: "883101", Name: "RESONANCIA MAGNETICA DE CEREBRO", Quantity: 1},
	})
	prev, _ := json.Marshal([]cupsCorrection{
		{Read: "890279", Used: "890274", Via: cupsCorrectedByName, Name: "NEURO"},
		{Read: "881439", Used: "881436", Via: cupsCorrectedByName, Name: "ECO"}, // ya no está en la orden
	})
	sess := testSess(sm.StateValidateOCR)
	sess.SetContext("ocr_cups_json", string(raw))
	sess.SetContext(ctxCupsCorrections, string(prev))
	allCalls := 0
	res, err := validateOCRHandler(catalogRepo(resolveCatalog, &allCalls), nil)(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if got := corrections(t, res); len(got) != 1 || got[0].Used != "890274" {
		t.Errorf("se conserva solo la corrección de un código que sigue en la orden: %+v", got)
	}
}

// Las correcciones son de UNA orden: al leer una orden nueva se borran, para que el resumen del
// agente no muestre como corregido un código de la orden anterior.
func TestProcessOrderMedia_NewOrderClearsPreviousCorrections(t *testing.T) {
	ocrSrv := newSequentialOCRServer(ocrCupsResponse("890274", "NEURO"))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	sess := testSess(sm.StateUploadMedicalOrder)
	sess.SetContext(ctxCupsCorrections, `[{"leido":"890279","usado":"890274","via":"nombre","nombre":"NEURO"}]`)
	res, err := processOrderMedia(context.Background(), sess, services.NewOCRServiceForTest(ocrSrv.URL), nil, fileSrv.URL+"/p1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.ClearCtx, ctxCupsCorrections) {
		t.Errorf("una orden nueva borra las correcciones de la anterior: %v", res.ClearCtx)
	}
}
