package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

func TestFetchAppointments_WithAppts(t *testing.T) {
	appts := []domain.Appointment{
		testutil.SampleAppointment(time.Now().AddDate(0, 0, 3)),
	}

	apptRepo := &testutil.MockAppointmentRepo{
		FindUpcomingByPatientFn: func(ctx context.Context, patientID string) ([]domain.Appointment, error) {
			return appts, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateFetchAppointments)
	sess.Context["patient_id"] = "PAT001"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateListAppointments {
		t.Errorf("expected LIST_APPOINTMENTS, got %s", result.NextState)
	}
}

func TestFetchAppointments_NoAppts(t *testing.T) {
	apptRepo := &testutil.MockAppointmentRepo{
		FindUpcomingByPatientFn: func(ctx context.Context, patientID string) ([]domain.Appointment, error) {
			return []domain.Appointment{}, nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateFetchAppointments)
	sess.Context["patient_id"] = "PAT001"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED (auto-close), got %s", result.NextState)
	}
}

func TestFetchAppointments_Error(t *testing.T) {
	apptRepo := &testutil.MockAppointmentRepo{
		FindUpcomingByPatientFn: func(ctx context.Context, patientID string) ([]domain.Appointment, error) {
			return nil, context.DeadlineExceeded
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateFetchAppointments)
	sess.Context["patient_id"] = "PAT001"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED on error, got %s", result.NextState)
	}
}

func TestListAppointments_Selection(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	apptRepo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateListAppointments)
	sess.Context["appointments_json"] = string(apptsJSON)

	result, err := m.Process(context.Background(), sess, postbackM(appt.ID))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAppointmentAction {
		t.Errorf("expected APPOINTMENT_ACTION, got %s", result.NextState)
	}
	// Should include unified list message (detail in body)
	if len(result.Messages) < 1 {
		t.Error("expected at least 1 message (list with detail)")
	}
}

func TestAppointmentAction_ConfirmFlow(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	apptRepo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateAppointmentAction)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	// Select confirm → should go to CONFIRM_APPOINTMENT (reconfirmation)
	result, err := m.Process(context.Background(), sess, postbackM("appt_confirm"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmAppointment {
		t.Errorf("expected CONFIRM_APPOINTMENT, got %s", result.NextState)
	}
}

func TestAppointmentAction_CancelFlow(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	apptRepo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateAppointmentAction)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	// Select cancel → should go to CANCEL_APPOINTMENT (reconfirmation)
	result, err := m.Process(context.Background(), sess, postbackM("appt_cancel"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCancelAppointment {
		t.Errorf("expected CANCEL_APPOINTMENT, got %s", result.NextState)
	}
}

func TestAppointmentAction_Back(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	apptRepo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateAppointmentAction)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	result, err := m.Process(context.Background(), sess, postbackM("appt_back"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateListAppointments {
		t.Errorf("expected LIST_APPOINTMENTS, got %s", result.NextState)
	}
}

func TestAppointmentAction_Menu(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	apptRepo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateAppointmentAction)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	result, err := m.Process(context.Background(), sess, postbackM("appt_menu"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}
}

func TestConfirmAppointment_Yes(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	confirmCalled := false
	apptRepo := &testutil.MockAppointmentRepo{
		ConfirmFn: func(ctx context.Context, id, source, chID string) error {
			confirmCalled = true
			return nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateConfirmAppointment)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	result, err := m.Process(context.Background(), sess, postbackM("confirm_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
	if !confirmCalled {
		t.Error("expected confirm to be called")
	}
}

func TestConfirmAppointment_No(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	apptRepo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateConfirmAppointment)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	result, err := m.Process(context.Background(), sess, postbackM("confirm_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAppointmentAction {
		t.Errorf("expected APPOINTMENT_ACTION, got %s", result.NextState)
	}
}

func TestCancelAppointment_Yes(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	cancelCalled := false
	apptRepo := &testutil.MockAppointmentRepo{
		CancelFn: func(ctx context.Context, id, reason, source, chID string) error {
			cancelCalled = true
			return nil
		},
	}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateCancelAppointment)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	result, err := m.Process(context.Background(), sess, postbackM("cancel_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
	if !cancelCalled {
		t.Error("expected cancel to be called")
	}
}

func TestCancelAppointment_No(t *testing.T) {
	appt := testutil.SampleAppointment(time.Now().AddDate(0, 0, 3))
	appts := []domain.Appointment{appt}
	apptsJSON, _ := json.Marshal(appts)

	apptRepo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(apptRepo, nil)

	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, apptSvc, nil, nil, nil)

	sess := testSess(sm.StateCancelAppointment)
	sess.Context["appointments_json"] = string(apptsJSON)
	sess.Context["selected_appointment_id"] = appt.ID

	result, err := m.Process(context.Background(), sess, postbackM("cancel_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAppointmentAction {
		t.Errorf("expected APPOINTMENT_ACTION, got %s", result.NextState)
	}
}

func TestNoAppointments_Menu(t *testing.T) {
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(&testutil.MockAppointmentRepo{}, nil), nil, nil, nil)

	sess := testSess(sm.StateNoAppointments)
	result, err := m.Process(context.Background(), sess, postbackM("no_appt_menu"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}
}

func TestNoAppointments_End(t *testing.T) {
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(&testutil.MockAppointmentRepo{}, nil), nil, nil, nil)

	sess := testSess(sm.StateNoAppointments)
	result, err := m.Process(context.Background(), sess, postbackM("no_appt_end"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateFarewell {
		t.Errorf("expected FAREWELL, got %s", result.NextState)
	}
}
