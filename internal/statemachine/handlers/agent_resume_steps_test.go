package handlers

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// «Volver a preguntarle» (retorno sin dato del agente de Néctar) en pasos de botones o listas:
// el paciente recibe la pregunta del paso con sus opciones, no un «selecciona…» suelto ni un
// error. Cada caso lleva su control de Bird: el mismo turno allí sigue dando el texto de siempre.

// resumeTurn corre el turno de retorno en Néctar y comprueba que el paso se quede sin gastar
// reintento.
func resumeTurn(t *testing.T, m *sm.Machine, sess *session.Session, wantState string) *sm.StateResult {
	t.Helper()
	sess.RetryCount = 1
	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != wantState || sess.RetryCount != 1 {
		t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
	}
	return result
}

// birdTurn corre el mismo `__resume__` como lo vive Bird (cuenta reintento).
func birdTurn(t *testing.T, m *sm.Machine, sess *session.Session) *sm.StateResult {
	t.Helper()
	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if sess.RetryCount != 1 {
		t.Errorf("Bird: RetryCount = %d, quería 1", sess.RetryCount)
	}
	return result
}

func onlyButtons(t *testing.T, result *sm.StateResult) *sm.ButtonMessage {
	t.Helper()
	for _, msg := range result.Messages {
		if bm, ok := msg.(*sm.ButtonMessage); ok {
			return bm
		}
	}
	t.Fatalf("el resultado no trae botones: %+v", result.Messages)
	return nil
}

func payloadsOf(bm *sm.ButtonMessage) []string {
	var p []string
	for _, b := range bm.Buttons {
		p = append(p, b.Payload)
	}
	return p
}

func onlyText(t *testing.T, result *sm.StateResult) string {
	t.Helper()
	if len(result.Messages) != 1 {
		t.Fatalf("quería un solo texto, hay %d mensajes: %+v", len(result.Messages), result.Messages)
	}
	tm, ok := result.Messages[0].(*sm.TextMessage)
	if !ok {
		t.Fatalf("quería texto, obtuve %T", result.Messages[0])
	}
	return tm.Text
}

const birdSelectOption = "Por favor selecciona una de las opciones disponibles."

func TestNotifRescheduleFallback_NectarResumeShowsTheQuestionAndButtons(t *testing.T) {
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(&testutil.MockAppointmentRepo{}, nil), nil, nil, nil, nil)
	sess := resumeTurnSess(sm.StateNotifRescheduleFallback, true)
	sess.Context["cups_name"] = "Electromiografia"

	bm := onlyButtons(t, resumeTurn(t, m, sess, sm.StateNotifRescheduleFallback))
	if !strings.Contains(bm.Text, "Electromiografia") || !strings.Contains(bm.Text, "¿Qué deseas hacer?") {
		t.Errorf("pregunta = %q", bm.Text)
	}
	// Los mismos 3 botones con los que noSlotsHandler ofrece el paso.
	if got := payloadsOf(bm); !slices.Equal(got, []string{"confirm", "reschedule", "cancel"}) {
		t.Errorf("botones = %v", got)
	}

	bird := resumeTurnSess(sm.StateNotifRescheduleFallback, false)
	if got := onlyText(t, birdTurn(t, m, bird)); got != birdSelectOption {
		t.Errorf("Bird cambió: %q", got)
	}
}

func TestCoverageNoConvenio_NectarResumeShowsTheQuestionAndButtons(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateCoverageNoConvenio, coverageNoConvenioHandler())

	t.Run("antes de mostrar horarios", func(t *testing.T) {
		sess := resumeTurnSess(sm.StateCoverageNoConvenio, true)
		sess.Context["cups_name"] = "Resonancia"
		bm := onlyButtons(t, resumeTurn(t, m, sess, sm.StateCoverageNoConvenio))
		if !strings.Contains(bm.Text, "*no tiene convenio* para *Resonancia*") {
			t.Errorf("pregunta = %q", bm.Text)
		}
		if got := payloadsOf(bm); !slices.Equal(got, []string{"cov_particular", "cov_agente"}) {
			t.Errorf("botones = %v", got)
		}
	})
	t.Run("al agendar el horario elegido", func(t *testing.T) {
		slot := services.AvailableSlot{Date: "2026-11-03", TimeSlot: "202611030730", AgendaID: 7, DoctorSiesaCode: "20"}
		slotsJSON, _ := json.Marshal([]services.AvailableSlot{slot})
		sess := resumeTurnSess(sm.StateCoverageNoConvenio, true)
		sess.Context["cups_name"] = "Resonancia"
		sess.Context["coverage_from_booking"] = "1"
		sess.Context["available_slots_json"] = string(slotsJSON)
		sess.Context["selected_slot_id"] = slotKey(&slot)
		bm := onlyButtons(t, resumeTurn(t, m, sess, sm.StateCoverageNoConvenio))
		if !strings.Contains(bm.Text, "03/11/2026") || !strings.Contains(bm.Text, "particular") {
			t.Errorf("pregunta = %q", bm.Text)
		}
		if got := payloadsOf(bm); !slices.Equal(got, []string{"cov_particular", "cov_agente"}) {
			t.Errorf("botones = %v", got)
		}
	})

	bird := resumeTurnSess(sm.StateCoverageNoConvenio, false)
	if got := onlyText(t, birdTurn(t, m, bird)); got != birdSelectOption {
		t.Errorf("Bird cambió: %q", got)
	}
}

func TestMedicationAskSymptoms_NectarResumeRepeatsTheQuestion(t *testing.T) {
	m := sm.NewMachine()
	RegisterMedicationHandlers(m, nil, nil)
	result := resumeTurn(t, m, resumeTurnSess(sm.StateMedicationAskSymptoms, true), sm.StateMedicationAskSymptoms)
	if len(result.Messages) != 2 {
		t.Fatalf("quería la pregunta y los botones, hay %d mensajes", len(result.Messages))
	}
	if tm, ok := result.Messages[0].(*sm.TextMessage); !ok || tm.Text != medicationSymptomsQuestion {
		t.Errorf("quería la pregunta de síntomas, obtuve %+v", result.Messages[0])
	}
	if got := payloadsOf(onlyButtons(t, result)); !slices.Equal(got, []string{"sin_sintomas", "con_sintomas"}) {
		t.Errorf("botones = %v", got)
	}

	bird := birdTurn(t, m, resumeTurnSess(sm.StateMedicationAskSymptoms, false))
	if len(bird.Messages) != 1 || onlyButtons(t, bird).Text != "Selecciona una opción:" {
		t.Errorf("Bird cambió: %+v", bird.Messages)
	}
}

func TestMedicationConfirmOrden_NectarResumeRepeatsTheMismatch(t *testing.T) {
	m := sm.NewMachine()
	RegisterMedicationHandlers(m, nil, nil)
	sess := resumeTurnSess(sm.StateMedicationConfirmOrden, true)
	sess.Context["med_order_found_name"] = "ADALIMUMAB"
	sess.Context["med_medicamento"] = "ETANERCEPT"
	result := resumeTurn(t, m, sess, sm.StateMedicationConfirmOrden)
	if len(result.Messages) != 2 {
		t.Fatalf("quería el aviso y los botones, hay %d mensajes", len(result.Messages))
	}
	if tm, ok := result.Messages[0].(*sm.TextMessage); !ok || tm.Text != medicationOrderMismatchText("ADALIMUMAB", "ETANERCEPT") {
		t.Errorf("quería el aviso de la orden, obtuve %+v", result.Messages[0])
	}
	if got := payloadsOf(onlyButtons(t, result)); !slices.Equal(got, []string{"med_confirm_ok", "med_confirm_retry"}) {
		t.Errorf("botones = %v", got)
	}

	bird := birdTurn(t, m, resumeTurnSess(sm.StateMedicationConfirmOrden, false))
	if len(bird.Messages) != 1 || onlyButtons(t, bird).Text != "¿Deseas continuar?" {
		t.Errorf("Bird cambió: %+v", bird.Messages)
	}
}

func TestConfirmOCRResult_NectarResumeRepeatsWhatWasRead(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(nil, nil))
	cups := []services.CUPSEntry{{Code: "930810", Name: "ELECTROMIOGRAFIA", Quantity: 4}, {Code: "891509", Name: "NEUROCONDUCCION", Quantity: 1}}
	cupsJSON, _ := json.Marshal(cups)
	sess := resumeTurnSess(sm.StateConfirmOCRResult, true)
	sess.Context["ocr_cups_json"] = string(cupsJSON)

	bm := onlyButtons(t, resumeTurn(t, m, sess, sm.StateConfirmOCRResult))
	if want := ocrSummaryMessages(cups, 0)[0].(*sm.ButtonMessage); bm.Text != want.Text {
		t.Errorf("quería el resumen de lo leído, obtuve %q", bm.Text)
	}
	if got := payloadsOf(bm); !slices.Equal(got, []string{"ocr_correct", "ocr_incorrect"}) {
		t.Errorf("botones = %v", got)
	}

	bird := resumeTurnSess(sm.StateConfirmOCRResult, false)
	bird.Context["ocr_cups_json"] = string(cupsJSON)
	if got := onlyButtons(t, birdTurn(t, m, bird)).Text; got != "¿Es correcto?" {
		t.Errorf("Bird cambió: %q", got)
	}
}

func TestSelectProcedure_NectarResumeShowsTheListAgain(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateSelectProcedure, selectProcedureHandler())
	procs := []searchedProcedure{
		{ID: 11, Code: "883101", Name: "RESONANCIA CEREBRO", ServiceName: "IMAGENES"},
		{ID: 12, Code: "883102", Name: "RESONANCIA COLUMNA"},
	}
	procsJSON, _ := json.Marshal(procs)
	sess := resumeTurnSess(sm.StateSelectProcedure, true)
	sess.Context["search_procedures_json"] = string(procsJSON)

	result := resumeTurn(t, m, sess, sm.StateSelectProcedure)
	if len(result.Messages) != 1 {
		t.Fatalf("quería solo la lista, hay %d mensajes", len(result.Messages))
	}
	lm, ok := result.Messages[0].(*sm.ListMessage)
	if !ok {
		t.Fatalf("quería la lista de procedimientos, obtuve %T", result.Messages[0])
	}
	var ids []string
	for _, r := range lm.Sections[0].Rows {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"11", "12"}) {
		t.Errorf("filas = %v", ids)
	}

	bird := resumeTurnSess(sm.StateSelectProcedure, false)
	bird.Context["search_procedures_json"] = string(procsJSON)
	// Bird pasa a ASK_MANUAL_CUPS (el cambio de paso reinicia el contador).
	result, err := m.Process(context.Background(), bird, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskManualCups ||
		onlyText(t, result) != "Por favor selecciona un procedimiento de la lista, o escribe otro nombre para buscar de nuevo." {
		t.Errorf("Bird cambió: next=%s %+v", result.NextState, result.Messages)
	}
}

func TestAskManualCups_NectarResumeAsksForTheName(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(&mockProcedureRepo{}))
	if got := onlyText(t, resumeTurn(t, m, resumeTurnSess(sm.StateAskManualCups, true), sm.StateAskManualCups)); got != manualCupsQuestion {
		t.Errorf("quería la pregunta del paso, obtuve %q", got)
	}

	bird := resumeTurnSess(sm.StateAskManualCups, false)
	result, err := m.Process(context.Background(), bird, textM("ab"))
	if err != nil {
		t.Fatal(err)
	}
	if got := onlyText(t, result); got != "Por favor escribe al menos 3 caracteres del nombre del procedimiento." || bird.RetryCount != 1 {
		t.Errorf("Bird cambió: %q retry=%d", got, bird.RetryCount)
	}
}

// SELECT_WAITING_LIST tomaba `__resume__` como un id de la lista: «No pude cargar esa cita en
// espera» y seguía con una orden nueva. En el retorno vuelve a mostrar la lista.
func TestSelectWaitingList_NectarResumeShowsTheListAgain(t *testing.T) {
	looked := 0
	repo := &testutil.MockWaitingListCreator{
		GetActiveByPatientFn: func(context.Context, string) ([]domain.WaitingListEntry, error) {
			return []domain.WaitingListEntry{{ID: "w1", CupsName: "RADIOGRAFIA", CreatedAt: time.Now()}}, nil
		},
		FindByIDFn: func(context.Context, string) (*domain.WaitingListEntry, error) {
			looked++
			return nil, nil
		},
	}
	handler := selectWaitingListHandler(repo)
	sess := resumeTurnSess(sm.StateSelectWaitingList, true)
	sess.Context["patient_id"] = "PAT001"

	result, err := handler(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if looked != 0 {
		t.Error("__resume__ se buscó como una entrada de la lista de espera")
	}
	if result.NextState != sm.StateSelectWaitingList || len(result.Messages) != 1 {
		t.Fatalf("next=%s, quería solo la lista: %+v", result.NextState, result.Messages)
	}
	lm, ok := result.Messages[0].(*sm.ListMessage)
	if !ok || lm.Sections[0].Rows[0].ID != "w1" {
		t.Fatalf("quería la lista de espera, obtuve %+v", result.Messages[0])
	}

	// Sin entradas activas (ya se agendaron) sigue con la orden nueva, sin aviso de error.
	repo.GetActiveByPatientFn = nil
	result, err = handler(context.Background(), resumeTurnSess(sm.StateSelectWaitingList, true), resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskMedicalOrder || result.UpdateCtx["wl_offer_done"] != "1" || len(result.Messages) != 0 {
		t.Errorf("next=%s ctx=%v msgs=%+v", result.NextState, result.UpdateCtx, result.Messages)
	}

	// Bird: igual que siempre.
	result, err = handler(context.Background(), resumeTurnSess(sm.StateSelectWaitingList, false), resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskMedicalOrder || onlyText(t, result) != "No pude cargar esa cita en espera. Continuemos con tu orden médica." {
		t.Errorf("Bird cambió: next=%s %+v", result.NextState, result.Messages)
	}
}

// El retorno de CONFIRM_OCR_RESULT repite también la nota de los procedimientos omitidos: el
// conteo lo guarda VALIDATE_OCR junto con lo leído.
func TestConfirmOCRResult_NectarResumeKeepsTheSkippedNote(t *testing.T) {
	procRepo := &mockProcedureRepo{
		findByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
			if code == "890271" {
				return &domain.Procedure{ID: 1, Code: "890271", Name: "ELECTROMIOGRAFIA", IsActive: true}, nil
			}
			return nil, nil
		},
	}
	read, _ := json.Marshal([]services.CUPSEntry{{Code: "890271", Name: "EMG", Quantity: 1}, {Code: "999999", Name: "OTRO", Quantity: 1}})
	m := sm.NewMachine()
	m.Register(sm.StateValidateOCR, validateOCRHandler(procRepo, nil, nil))
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(procRepo, nil))

	validate := testSess(sm.StateValidateOCR)
	validate.Context["ocr_cups_json"] = string(read)
	shown, err := m.Process(context.Background(), validate, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if shown.UpdateCtx["ocr_skipped_count"] != "1" {
		t.Fatalf("ocr_skipped_count = %q, quería 1", shown.UpdateCtx["ocr_skipped_count"])
	}

	sess := resumeTurnSess(sm.StateConfirmOCRResult, true)
	for k, v := range shown.UpdateCtx {
		sess.Context[k] = v
	}
	bm := onlyButtons(t, resumeTurn(t, m, sess, sm.StateConfirmOCRResult))
	if want := onlyButtons(t, shown).Text; bm.Text != want {
		t.Errorf("el retorno no repite lo que se mostró:\n%q\nquería\n%q", bm.Text, want)
	}
}
