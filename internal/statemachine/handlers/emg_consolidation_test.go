package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

func emgPostback(payload string) bird.InboundMessage {
	return bird.InboundMessage{IsPostback: true, PostbackPayload: payload, MessageType: "postback"}
}

// Cita EMG pendiente encontrada → ofrece consolidar (CONFIRM_CONSOLIDATE) y guarda su id.
func TestCheckEmgConsolidation_FoundOffersConsolidate(t *testing.T) {
	repo := &testutil.MockAppointmentRepo{
		FindPendingEmgAppointmentFn: func(_ context.Context, _ string, _ []string) (*domain.Appointment, error) {
			return &domain.Appointment{ID: "18234", Date: time.Now().AddDate(0, 0, 3)}, nil
		},
	}
	apptSvc := services.NewAppointmentService(repo, &config.Config{})
	sess := testSess(sm.StateCheckEmgConsolidation)
	sess.SetContext("patient_id", "111349")

	res, err := checkEmgConsolidationHandler(apptSvc)(context.Background(), sess, bird.InboundMessage{})
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateConfirmConsolidate {
		t.Fatalf("esperaba CONFIRM_CONSOLIDATE, got %s", res.NextState)
	}
	if res.UpdateCtx["consolidate_appt_id"] != "18234" {
		t.Errorf("esperaba consolidate_appt_id=18234, got %q", res.UpdateCtx["consolidate_appt_id"])
	}
}

// Sin cita EMG pendiente → pregunta si tiene la orden de la EMG.
func TestCheckEmgConsolidation_NotFoundAsksEmgOrder(t *testing.T) {
	repo := &testutil.MockAppointmentRepo{
		FindPendingEmgAppointmentFn: func(_ context.Context, _ string, _ []string) (*domain.Appointment, error) {
			return nil, nil
		},
	}
	apptSvc := services.NewAppointmentService(repo, &config.Config{})
	sess := testSess(sm.StateCheckEmgConsolidation)
	sess.SetContext("patient_id", "111349")

	res, _ := checkEmgConsolidationHandler(apptSvc)(context.Background(), sess, bird.InboundMessage{})
	if res.NextState != sm.StateAskEmgOrder {
		t.Errorf("esperaba ASK_EMG_ORDER, got %s", res.NextState)
	}
}

// apptSvc nil → no puede buscar cita → pide la orden EMG.
func TestCheckEmgConsolidation_NilSvcAsksEmgOrder(t *testing.T) {
	sess := testSess(sm.StateCheckEmgConsolidation)
	sess.SetContext("patient_id", "111349")
	res, _ := checkEmgConsolidationHandler(nil)(context.Background(), sess, bird.InboundMessage{})
	if res.NextState != sm.StateAskEmgOrder {
		t.Errorf("esperaba ASK_EMG_ORDER, got %s", res.NextState)
	}
}

// "Sí tengo la orden EMG" → subir 2ª foto (UPLOAD_EMG_ORDER), guardando los dependientes.
func TestAskEmgOrder_YesGoesToUpload(t *testing.T) {
	sess := testSess(sm.StateAskEmgOrder)
	sess.SetContext("ocr_cups_json", `[{"cups_code":"891514","quantity":1}]`)
	res, _ := askEmgOrderHandler()(context.Background(), sess, emgPostback("emg_order_yes"))
	if res.NextState != sm.StateUploadEmgOrder {
		t.Fatalf("esperaba UPLOAD_EMG_ORDER, got %s", res.NextState)
	}
	if res.UpdateCtx["emg_dep_cups_json"] == "" {
		t.Error("esperaba guardar emg_dep_cups_json con los dependientes")
	}
}

// "No tengo la orden EMG" → avisar y cerrar.
func TestAskEmgOrder_NoWarnsAndCloses(t *testing.T) {
	sess := testSess(sm.StateAskEmgOrder)
	res, _ := askEmgOrderHandler()(context.Background(), sess, emgPostback("emg_order_no"))
	if res.NextState != sm.StateTerminated {
		t.Errorf("esperaba TERMINATED (aviso), got %s", res.NextState)
	}
}

// Rechazar consolidación → ofrecer subir la orden EMG.
func TestConfirmConsolidate_NoGoesToAskEmgOrder(t *testing.T) {
	apptSvc := services.NewAppointmentService(&testutil.MockAppointmentRepo{}, &config.Config{})
	sess := testSess(sm.StateConfirmConsolidate)
	res, _ := confirmConsolidateHandler(apptSvc)(context.Background(), sess, emgPostback("consolidate_no"))
	if res.NextState != sm.StateAskEmgOrder {
		t.Errorf("esperaba ASK_EMG_ORDER, got %s", res.NextState)
	}
}
