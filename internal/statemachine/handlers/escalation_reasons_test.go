package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

func must(res *sm.StateResult, err error) func(t *testing.T) *sm.StateResult {
	return func(t *testing.T) *sm.StateResult {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
}

// Cada sitio que manda a ESCALATE_TO_AGENT escribe su motivo (Néctar le muestra al agente el
// detalle y no el `reason`), y los que son el paciente pidiendo la persona lo marcan.
func TestEscalationSites_WriteTheirReason(t *testing.T) {
	ctx := context.Background()
	ocrSrv := newSequentialOCRServer(ocrCupsResponse("", "SIN CODIGO"))
	defer ocrSrv.Close()
	fileSrv := newMockFileServer()
	defer fileSrv.Close()

	tests := []struct {
		name      string
		want      string
		askedByPt bool
		run       func(t *testing.T) *sm.StateResult
	}{
		{"particular_sin_orden", sm.EscalationReasonParticularNoOrder, false, func(t *testing.T) *sm.StateResult {
			sess := testSess(sm.StateAskMedicalOrder)
			sess.SetContext("entity_category", "PARTICULAR")
			sess.SetContext("_prompted_has_order", "1")
			return must(askMedicalOrderHandler(nil, nil, nil)(ctx, sess, postbackM("has_order_no")))(t)
		}},
		{"ocr_sin_codigo", sm.EscalationReasonOCRNoCode, false, func(t *testing.T) *sm.StateResult {
			sess := testSess(sm.StateUploadMedicalOrder)
			return must(processOrderMedia(ctx, sess, services.NewOCRServiceForTest(ocrSrv.URL), nil, fileSrv.URL+"/p1.jpg"))(t)
		}},
		{"ocr_pidio_agente", sm.EscalationReasonOCRAskedForAgent, true, func(t *testing.T) *sm.StateResult {
			return must(uploadNonMedia(testSess(sm.StateUploadMedicalOrder), nil, postbackM("escalate_agent")))(t)
		}},
		{"ocr_json_invalido_validate", sm.EscalationReasonOCRInvalidJSON, false, func(t *testing.T) *sm.StateResult {
			sess := testSess(sm.StateValidateOCR)
			sess.SetContext("ocr_cups_json", "no-es-json")
			return must(validateOCRHandler(&mockProcedureRepo{})(ctx, sess, textM("")))(t)
		}},
		{"ocr_json_invalido_confirm", sm.EscalationReasonOCRInvalidJSON, false, func(t *testing.T) *sm.StateResult {
			sess := testSess(sm.StateConfirmOCRResult)
			sess.SetContext("ocr_cups_json", "no-es-json")
			return must(confirmOCRResultHandler(&mockProcedureRepo{}, nil)(ctx, sess, postbackM("ocr_correct")))(t)
		}},
		{"bloqueo_sanitas", sm.EscalationReasonSanitasBlock, false, func(*testing.T) *sm.StateResult {
			return sanitasBloqueoEscalation(testSess(sm.StateSearchSlots), "053105", "test")
		}},
		{"bloqueo_sanitas_validate", sm.EscalationReasonSanitasBlock, false, func(t *testing.T) *sm.StateResult {
			repo := &mockProcedureRepo{findByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
				return &domain.Procedure{ID: 1, Code: code, Name: "Proc " + code, IsActive: true}, nil
			}}
			raw, _ := json.Marshal([]services.CUPSEntry{{Code: "053105", Name: "Bloqueo", Quantity: 1}})
			sess := testSess(sm.StateValidateOCR)
			sess.SetContext("ocr_cups_json", string(raw))
			sess.SetContext("patient_contract", "6")
			return must(validateOCRHandler(repo)(ctx, sess, textM("")))(t)
		}},
		{"cups_no_validos", sm.EscalationReasonNoValidCups, false, func(t *testing.T) *sm.StateResult {
			raw, _ := json.Marshal([]services.CUPSEntry{{Code: "999999", Name: "No existe", Quantity: 1}})
			sess := testSess(sm.StateValidateOCR)
			sess.SetContext("ocr_cups_json", string(raw))
			return must(validateOCRHandler(&mockProcedureRepo{})(ctx, sess, textM("")))(t)
		}},
		{"sin_convenio", sm.EscalationReasonNoCoverage, true, func(t *testing.T) *sm.StateResult {
			return must(coverageNoConvenioHandler()(ctx, testSess(sm.StateCoverageNoConvenio), postbackM("cov_agente")))(t)
		}},
		{"estudio_sueno", sm.EscalationReasonSleepStudy, false, func(t *testing.T) *sm.StateResult {
			sess := testSess(sm.StateCheckSpecialCups)
			sess.SetContext("cups_code", "891901")
			return must(checkSpecialCupsHandler()(ctx, sess, textM("")))(t)
		}},
		{"sin_entidades", sm.EscalationReasonNoEntities, false, func(t *testing.T) *sm.StateResult {
			repo := &testutil.MockEntityRepo{
				FindActiveByCategoryFn: func(context.Context, string) ([]domain.Entity, error) { return nil, nil },
			}
			sess := testSess(sm.StateShowEntityList)
			sess.SetContext("entity_category", "EPS")
			return must(showEntityListHandler(repo)(ctx, sess, textM("")))(t)
		}},
		{"entidad_no_resuelta", sm.EscalationReasonEntityUnresolved, false, func(t *testing.T) *sm.StateResult {
			sess := testSess(sm.StateAskEntityNumber)
			sess.SetContext("entity_list_count", "3")
			return must(askEntityNumberHandler(nil)(ctx, sess, textM("2")))(t)
		}},
		{"particular_liga", sm.EscalationReasonParticularLiga, false, func(t *testing.T) *sm.StateResult {
			sess := testSess(sm.StateAskEntityNumber)
			sess.SetContext("entity_list_count", "1")
			sess.SetContext("entity_list_codes", "50001")
			return must(askEntityNumberHandler(nil)(ctx, sess, textM("1")))(t)
		}},
		{"consolidacion_emg_fallida", sm.EscalationReasonEmgConsolidationFailed, false, func(t *testing.T) *sm.StateResult {
			return must(confirmConsolidateHandler(nil)(ctx, testSess(sm.StateConfirmConsolidate), postbackM("consolidate_yes")))(t)
		}},
		{"emg_pidio_agente", sm.EscalationReasonEmgAskedForAgent, true, func(t *testing.T) *sm.StateResult {
			return must(uploadEmgOrderHandler(nil, nil)(ctx, testSess(sm.StateUploadEmgOrder), postbackM("escalate_agent")))(t)
		}},
		{"paciente_pidio_asesor_post_action", sm.EscalationReasonPatientAsked, true, func(t *testing.T) *sm.StateResult {
			return must(postActionMenuHandler(nil)(ctx, testSess(sm.StatePostActionMenu), textM("agente")))(t)
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.run(t)
			if res == nil || res.NextState != sm.StateEscalateToAgent {
				t.Fatalf("esperaba ESCALATE_TO_AGENT, got %+v", res)
			}
			if got := res.UpdateCtx[sm.CtxEscalationReason]; got != tc.want {
				t.Errorf("motivo = %q, quería %q", got, tc.want)
			}
			if got := res.UpdateCtx[sm.CtxPatientAskedForAgent] == "1"; got != tc.askedByPt {
				t.Errorf("marca del paciente = %v, quería %v", got, tc.askedByPt)
			}
		})
	}
}

// Sin salida que lo borre, el motivo de una escalación rotularía la siguiente.
func TestEscalationExit_ClearsReason(t *testing.T) {
	if !hasClear(escalationExit(sm.NewResult(sm.StateMainMenu)), sm.CtxEscalationReason) {
		t.Fatal("escalationExit debe borrar CtxEscalationReason")
	}
	// Por el handler real: el sobre ya leyó el motivo cuando la salida lo borra.
	m, nb := nectarMachine(t, withProtocolV1(testEscalationConfig()), nil)
	sess := nectarSession(true)
	sess.SetContext(sm.CtxEscalationReason, sm.EscalationReasonSanitasBlock)
	res := escalateOnce(t, m, sess)
	if !hasClear(res, sm.CtxEscalationReason) {
		t.Errorf("la salida del handler no borra el motivo: %v", res.ClearCtx)
	}
	if len(nb.escalations) != 1 || nb.escalations[0].ReasonDetail != "bloqueo_sanitas" {
		t.Errorf("el sobre lee el motivo antes de borrarlo: %+v", nb.escalations)
	}
}

func TestEscalate_NectarEnvelopeCarriesReasonDetail(t *testing.T) {
	m, nb := nectarMachine(t, withProtocolV1(testEscalationConfig()), nil)
	sess := nectarSession(false)
	sess.SetContext(sm.CtxEscalationReason, sm.EscalationReasonSanitasBlock)
	escalateOnce(t, m, sess)
	if len(nb.escalations) != 1 || nb.escalations[0].ReasonDetail != "bloqueo_sanitas" {
		t.Fatalf("ReasonDetail: %+v", nb.escalations)
	}
}

// La entidad «particular liga» escala sin texto para el paciente: en Néctar el aviso sale igual,
// antes del escalate.
func TestEscalate_NectarNotifiesPatientOnParticularLiga(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	sess := nectarSession(false)
	sess.SetContext(sm.CtxEscalationReason, sm.EscalationReasonParticularLiga)
	escalateOnce(t, m, sess)
	if len(nb.events) != 2 || nb.events[0] != "text:"+sm.EscalationNoticeText || nb.events[1] != "escalate" {
		t.Fatalf("el aviso va ANTES del escalate: %v", nb.events)
	}
}

func TestEscalate_NectarBotDisabledNotifiesPatient(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	sess := nectarSession(false)
	sess.SetContext(sm.CtxEscalationReason, sm.EscalationReasonBotDisabled)
	escalateOnce(t, m, sess)
	if len(nb.events) != 2 || nb.events[0] != "text:"+sm.EscalationNoticeText {
		t.Fatalf("el kill switch avisa al paciente de Néctar: %v", nb.events)
	}
}

// Un motivo que no notifica sigue callado si el paciente no pidió la persona.
func TestEscalate_NectarOtherReasonStaysSilent(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	sess := nectarSession(false)
	sess.SetContext(sm.CtxEscalationReason, sm.EscalationReasonSanitasBlock)
	escalateOnce(t, m, sess)
	if got := nb.texts(); len(got) != 0 {
		t.Fatalf("sin marca del paciente ni motivo que notifique no hay aviso: %v", got)
	}
}

// E.164: el aviso de Bird viaja en el resultado del caller; este handler no manda texto alguno.
func TestEscalate_BirdParticularLigaStaysSilent(t *testing.T) {
	rec := &birdRecorder{}
	srv := rec.server(t)
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	sess.SetContext(sm.CtxEscalationReason, sm.EscalationReasonParticularLiga)
	res, err := m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("Bird: ningún mensaje del handler: %v", res.Messages)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.patientTexts != 0 {
		t.Fatalf("Bird: ningún texto directo al paciente: %d", rec.patientTexts)
	}
}

// El botón «Hablar con agente» de la orden es el paciente pidiendo la persona: el sobre lo dice.
func TestEscalate_OCRButtonIsPatientAsked(t *testing.T) {
	res := must(uploadNonMedia(nectarSession(false), nil, postbackM("escalate_agent")))(t)
	sess := nectarSession(false)
	for k, v := range res.UpdateCtx {
		sess.SetContext(k, v)
	}
	m, nb := nectarMachine(t, withProtocolV1(testEscalationConfig()), nil)
	escalateOnce(t, m, sess)
	if len(nb.escalations) != 1 || nb.escalations[0].Reason != nectar.ReasonPatientAskedForHuman {
		t.Fatalf("Reason: %+v", nb.escalations)
	}
	if nb.escalations[0].ReasonDetail != "ocr_pidio_agente" {
		t.Fatalf("ReasonDetail = %q", nb.escalations[0].ReasonDetail)
	}
}
