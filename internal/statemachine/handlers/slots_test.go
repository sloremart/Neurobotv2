package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

func slotsJSON(slots []services.AvailableSlot) string {
	b, _ := json.Marshal(slots)
	return string(b)
}

func sampleSlots() []services.AvailableSlot {
	return []services.AvailableSlot{
		{
			TimeSlot:      "202603201000",
			Date:          "2026-03-20",
			TimeDisplay:   "10:00 AM",
			DoctorName:    "Garcia",
			DoctorDoc:     "DOC001",
			AgendaID:      1,
			Duration:      30,
			ClinicAddress: "Cra 10 #20-30",
		},
	}
}

func TestSearchSlots_Found(t *testing.T) {
	slots := sampleSlots()
	slotSvc := services.NewSlotService(
		&testutil.MockProcedureRepo{
			FindSubjectTypeForCupsFn: func(ctx context.Context, code string) (int, error) {
				return 8, nil
			},
		},
		&testutil.MockScheduleRepo{
			FindAvailableSlotsFn: func(ctx context.Context, asuntoID int, afterDate string) ([]domain.AvailableSlotRow, error) {
				ts, _ := time.Parse("2006-01-02 15:04", "2026-03-20 10:00")
				return []domain.AvailableSlotRow{{
					SlotTime:        ts,
					DoctorDocument:  "DOC001",
					DoctorName:      "Garcia",
					DoctorSiesaCode: "S1",
					AgendaID:        1,
					DurationMin:     30,
					AgendaSede:      2,
				}}, nil
			},
		},
	)

	m := sm.NewMachine()
	RegisterSlotHandlers(m, slotSvc, nil, nil, nil, nil, nil, nil, nil)

	sess := testSess(sm.StateSearchSlots)
	sess.Context["cups_code"] = "890271"
	sess.Context["patient_age"] = "30"
	sess.Context["espacios"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}

	// Should find slots and go to SHOW_SLOTS
	if result.NextState != sm.StateShowSlots {
		t.Errorf("expected SHOW_SLOTS, got %s", result.NextState)
	}
	_ = slots // reference
}

func TestSearchSlots_NotFound(t *testing.T) {
	slotSvc := services.NewSlotService(
		&testutil.MockProcedureRepo{
			FindSubjectTypeForCupsFn: func(ctx context.Context, code string) (int, error) {
				return 0, nil // no subject → no slots
			},
		},
		&testutil.MockScheduleRepo{},
	)

	m := sm.NewMachine()
	RegisterSlotHandlers(m, slotSvc, nil, nil, nil, nil, nil, nil, nil)

	sess := testSess(sm.StateSearchSlots)
	sess.Context["cups_code"] = "890271"
	sess.Context["patient_age"] = "30"
	sess.Context["espacios"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}

	// NO_SLOTS_AVAILABLE is automatic → auto-chains to OFFER_WAITING_LIST (interactive)
	if result.NextState != sm.StateOfferWaitingList {
		t.Errorf("expected OFFER_WAITING_LIST (auto-chained), got %s", result.NextState)
	}
}

func TestShowSlots_Selection(t *testing.T) {
	slots := sampleSlots()

	m := sm.NewMachine()
	m.Register(sm.StateShowSlots, showSlotsHandler(nil))

	sess := testSess(sm.StateShowSlots)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["cups_name"] = "Electromiografia"

	// showSlotsHandler now expects a number (1-based index), not a postback payload
	result, err := m.Process(context.Background(), sess, textM("1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmBooking {
		t.Errorf("expected CONFIRM_BOOKING, got %s", result.NextState)
	}
}

func TestShowSlots_MoreSlots(t *testing.T) {
	slots := sampleSlots()

	m := sm.NewMachine()
	m.Register(sm.StateShowSlots, showSlotsHandler(nil))

	sess := testSess(sm.StateShowSlots)
	sess.Context["available_slots_json"] = slotsJSON(slots)

	// "Ver más" = slot count + 1 = option "2" (1 slot available)
	result, err := m.Process(context.Background(), sess, textM("2"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateSearchSlots {
		t.Errorf("expected SEARCH_SLOTS for more_slots, got %s", result.NextState)
	}
}

func TestShowSlots_InvalidSelection(t *testing.T) {
	slots := sampleSlots()

	m := sm.NewMachine()
	m.Register(sm.StateShowSlots, showSlotsHandler(nil))

	sess := testSess(sm.StateShowSlots)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["cups_name"] = "EMG"

	result, err := m.Process(context.Background(), sess, postbackM("invalid_slot"))
	if err != nil {
		t.Fatal(err)
	}
	// Invalid → re-show list, stay in same state
	if result.NextState != sm.StateShowSlots {
		t.Errorf("expected SHOW_SLOTS (retry), got %s", result.NextState)
	}
}

func registerConfirmBookingConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateConfirmBooking, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"booking_confirm", "booking_change"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			slot := findSelectedSlot(sess)
			if slot == nil {
				result.NextState = sm.StateSearchSlots
				result.Messages = []sm.OutboundMessage{&sm.TextMessage{Text: "Slot no encontrado. Buscando nuevos horarios..."}}
				result.ClearCtx = append(result.ClearCtx, "selected_slot_id", "available_slots_json")
				return
			}
			summary := buildBookingSummary(sess, slot, nil)
			result.Messages = append(result.Messages, &sm.ButtonMessage{
				Text: summary,
				Buttons: []sm.Button{
					{Text: "Confirmar cita", Payload: "booking_confirm"},
					{Text: "Elegir otro", Payload: "booking_change"},
				},
			})
		},
		Handler: confirmBookingHandler(),
	})
}

func TestConfirmBooking_Confirm(t *testing.T) {
	slots := sampleSlots()

	m := sm.NewMachine()
	registerConfirmBookingConfig(m)

	sess := testSess(sm.StateConfirmBooking)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot
	sess.Context["cups_name"] = "EMG"

	result, err := m.Process(context.Background(), sess, postbackM("booking_confirm"))
	if err != nil {
		t.Fatal(err)
	}
	// Now goes to RECONFIRM_BOOKING instead of CREATE_APPOINTMENT
	if result.NextState != sm.StateReconfirmBooking {
		t.Errorf("expected RECONFIRM_BOOKING, got %s", result.NextState)
	}
}

func TestConfirmBooking_Change(t *testing.T) {
	slots := sampleSlots()

	m := sm.NewMachine()
	registerConfirmBookingConfig(m)

	sess := testSess(sm.StateConfirmBooking)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot

	result, err := m.Process(context.Background(), sess, postbackM("booking_change"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateSearchSlots {
		t.Errorf("expected SEARCH_SLOTS, got %s", result.NextState)
	}
}

func TestOfferWaitingList_Yes(t *testing.T) {
	wlRepo := &testutil.MockWaitingListCreator{}

	m := sm.NewMachine()
	m.Register(sm.StateOfferWaitingList, offerWaitingListHandler(wlRepo))

	sess := testSess(sm.StateOfferWaitingList)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["cups_code"] = "890271"
	sess.Context["cups_name"] = "EMG"
	sess.Context["patient_age"] = "30"

	result, err := m.Process(context.Background(), sess, postbackM("wl_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

func TestOfferWaitingList_No(t *testing.T) {
	wlRepo := &testutil.MockWaitingListCreator{}

	m := sm.NewMachine()
	m.Register(sm.StateOfferWaitingList, offerWaitingListHandler(wlRepo))

	sess := testSess(sm.StateOfferWaitingList)
	sess.Context["cups_code"] = "890271"

	result, err := m.Process(context.Background(), sess, postbackM("wl_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

func TestBookingFailed_SlotTaken(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateBookingFailed, bookingFailedHandler())

	sess := testSess(sm.StateBookingFailed)
	sess.Context["booking_failure_reason"] = "slot_taken"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateSearchSlots {
		t.Errorf("expected SEARCH_SLOTS, got %s", result.NextState)
	}
}

func TestBookingFailed_GenericError(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateBookingFailed, bookingFailedHandler())

	sess := testSess(sm.StateBookingFailed)
	sess.Context["booking_failure_reason"] = "error"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

// --- createAppointmentHandler tests ---

func TestCreateAppointment_Success(t *testing.T) {
	slots := sampleSlots()
	repo := &testutil.MockAppointmentRepo{
		CreateFn: func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
			return &domain.Appointment{ID: "100"}, nil
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, nil))

	// Build procedures_json for a single group
	groups := []services.CUPSGroup{
		{
			ServiceType: "Neurofisiologia",
			Cups:        []services.CUPSEntry{{Code: "890271", Name: "Electromiografia", Quantity: 1}},
			Espacios:    1,
		},
	}
	groupsJSON, _ := json.Marshal(groups)

	sess := testSess(sm.StateCreateAppointment)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot
	sess.Context["patient_id"] = "PAT001"
	sess.Context["patient_entity"] = "EPS005"
	sess.Context["cups_code"] = "890271"
	sess.Context["is_contrasted"] = "0"
	sess.Context["is_sedated"] = "0"
	sess.Context["espacios"] = "1"
	sess.Context["procedures_json"] = string(groupsJSON)
	sess.Context["current_procedure_idx"] = "0"
	sess.Context["total_procedures"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateBookingSuccess {
		t.Errorf("expected BOOKING_SUCCESS, got %s", result.NextState)
	}
	if result.UpdateCtx["created_appointment_id"] != "100" {
		t.Errorf("expected created_appointment_id=100, got %s", result.UpdateCtx["created_appointment_id"])
	}
}

func TestCreateAppointment_SlotNotFound(t *testing.T) {
	repo := &testutil.MockAppointmentRepo{}
	apptSvc := services.NewAppointmentService(repo, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, nil))

	sess := testSess(sm.StateCreateAppointment)
	// No available_slots_json or selected_slot_id -> slot not found
	sess.Context["cups_code"] = "890271"
	sess.Context["espacios"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateBookingFailed {
		t.Errorf("expected BOOKING_FAILED, got %s", result.NextState)
	}
	if result.UpdateCtx["booking_failure_reason"] != "slot_not_found" {
		t.Errorf("expected reason=slot_not_found, got %s", result.UpdateCtx["booking_failure_reason"])
	}
}

func TestCreateAppointment_SlotTakenError(t *testing.T) {
	slots := sampleSlots()
	repo := &testutil.MockAppointmentRepo{
		CreateFn: func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
			return nil, fmt.Errorf("slot_taken: already booked")
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, nil))

	// Build procedures_json for a single group
	groups := []services.CUPSGroup{
		{
			ServiceType: "Neurofisiologia",
			Cups:        []services.CUPSEntry{{Code: "890271", Name: "Electromiografia", Quantity: 1}},
			Espacios:    1,
		},
	}
	groupsJSON, _ := json.Marshal(groups)

	sess := testSess(sm.StateCreateAppointment)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot
	sess.Context["patient_id"] = "PAT001"
	sess.Context["patient_entity"] = "EPS005"
	sess.Context["cups_code"] = "890271"
	sess.Context["is_contrasted"] = "0"
	sess.Context["is_sedated"] = "0"
	sess.Context["espacios"] = "1"
	sess.Context["procedures_json"] = string(groupsJSON)
	sess.Context["current_procedure_idx"] = "0"
	sess.Context["total_procedures"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateBookingFailed {
		t.Errorf("expected BOOKING_FAILED, got %s", result.NextState)
	}
	if result.UpdateCtx["booking_failure_reason"] != "slot_taken" {
		t.Errorf("expected reason=slot_taken, got %s", result.UpdateCtx["booking_failure_reason"])
	}
}

func TestCreateAppointment_GenericError(t *testing.T) {
	slots := sampleSlots()
	repo := &testutil.MockAppointmentRepo{
		CreateFn: func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
			return nil, fmt.Errorf("database connection lost")
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, nil))

	// Build procedures_json for a single group
	groups := []services.CUPSGroup{
		{
			ServiceType: "Neurofisiologia",
			Cups:        []services.CUPSEntry{{Code: "890271", Name: "Electromiografia", Quantity: 1}},
			Espacios:    1,
		},
	}
	groupsJSON, _ := json.Marshal(groups)

	sess := testSess(sm.StateCreateAppointment)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot
	sess.Context["patient_id"] = "PAT001"
	sess.Context["patient_entity"] = "EPS005"
	sess.Context["cups_code"] = "890271"
	sess.Context["is_contrasted"] = "0"
	sess.Context["is_sedated"] = "0"
	sess.Context["espacios"] = "1"
	sess.Context["procedures_json"] = string(groupsJSON)
	sess.Context["current_procedure_idx"] = "0"
	sess.Context["total_procedures"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateBookingFailed {
		t.Errorf("expected BOOKING_FAILED, got %s", result.NextState)
	}
	if result.UpdateCtx["booking_failure_reason"] != "error" {
		t.Errorf("expected reason=error, got %s", result.UpdateCtx["booking_failure_reason"])
	}
}

// --- bookingSuccessHandler tests ---

func TestBookingSuccess_SingleProcedure(t *testing.T) {
	slots := sampleSlots()

	m := sm.NewMachine()
	m.Register(sm.StateBookingSuccess, bookingSuccessHandler(nil))

	sess := testSess(sm.StateBookingSuccess)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["created_appointment_id"] = "apt-100"
	sess.Context["total_procedures"] = "1"
	sess.Context["current_procedure_idx"] = "0"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

func TestBookingSuccess_MultiProcedure(t *testing.T) {
	slots := sampleSlots()

	groups := []services.CUPSGroup{
		{
			ServiceType: "Neurofisiologia",
			Cups:        []services.CUPSEntry{{Code: "890271", Name: "Electromiografia", Quantity: 1}},
			Espacios:    1,
		},
		{
			ServiceType: "Resonancia",
			Cups:        []services.CUPSEntry{{Code: "883533", Name: "Resonancia de rodilla", Quantity: 1}},
			Espacios:    2,
		},
	}
	groupsJSON, _ := json.Marshal(groups)

	m := sm.NewMachine()
	m.Register(sm.StateBookingSuccess, bookingSuccessHandler(nil))

	sess := testSess(sm.StateBookingSuccess)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"
	sess.Context["created_appointment_id"] = "apt-100"
	sess.Context["total_procedures"] = "2"
	sess.Context["current_procedure_idx"] = "0"
	sess.Context["procedures_json"] = string(groupsJSON)

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckSpecialCups {
		t.Errorf("expected CHECK_SPECIAL_CUPS, got %s", result.NextState)
	}
	if result.UpdateCtx["cups_code"] != "883533" {
		t.Errorf("expected cups_code=883533, got %s", result.UpdateCtx["cups_code"])
	}
	if result.UpdateCtx["cups_name"] != "Resonancia de rodilla" {
		t.Errorf("expected cups_name=Resonancia de rodilla, got %s", result.UpdateCtx["cups_name"])
	}
	if result.UpdateCtx["current_procedure_idx"] != "1" {
		t.Errorf("expected current_procedure_idx=1, got %s", result.UpdateCtx["current_procedure_idx"])
	}
	if result.UpdateCtx["espacios"] != "2" {
		t.Errorf("expected espacios=2, got %s", result.UpdateCtx["espacios"])
	}
}

// --- buildObservations tests ---

func TestBuildObservations_None(t *testing.T) {
	got := buildObservations(false, false)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestBuildObservations_ContrastedOnly(t *testing.T) {
	got := buildObservations(true, false)
	if got != "Contrastada" {
		t.Errorf("expected %q, got %q", "Contrastada", got)
	}
}

func TestBuildObservations_SedatedOnly(t *testing.T) {
	got := buildObservations(false, true)
	if got != "Bajo Sedación" {
		t.Errorf("expected %q, got %q", "Bajo Sedación", got)
	}
}

func TestBuildObservations_Both(t *testing.T) {
	got := buildObservations(true, true)
	if got != "Contrastada, Bajo Sedación" {
		t.Errorf("expected %q, got %q", "Contrastada, Bajo Sedación", got)
	}
}

// --- noSlotsHandler test ---

func TestNoSlots_GoesToOfferWaitingList(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateNoSlotsAvailable, noSlotsHandler(nil))

	sess := testSess(sm.StateNoSlotsAvailable)
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateOfferWaitingList {
		t.Errorf("expected OFFER_WAITING_LIST, got %s", result.NextState)
	}
}

// --- Cambio 12: autoAddToWaitingList tests ---

func TestNoSlots_AutoAddToWL_CancellationReschedule(t *testing.T) {
	var createdEntry *domain.WaitingListEntry
	wlRepo := &testutil.MockWaitingListCreator{
		CreateFn: func(ctx context.Context, entry *domain.WaitingListEntry) error {
			createdEntry = entry
			return nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateNoSlotsAvailable, noSlotsHandler(wlRepo))

	sess := testSess(sm.StateNoSlotsAvailable)
	sess.PhoneNumber = "+573001234567"
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"
	sess.Context["patient_id"] = "PAT001"
	sess.Context["patient_name"] = "Juan Perez"
	sess.Context["patient_age"] = "45"
	sess.Context["espacios"] = "2"
	sess.Context["reschedule_skip_cancel"] = "1" // Admin cancellation

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}

	if createdEntry == nil {
		t.Fatal("expected WL entry to be created")
	}

	if createdEntry.CupsCode != "890271" {
		t.Errorf("expected cups_code 890271, got %s", createdEntry.CupsCode)
	}
	if createdEntry.PatientID != "PAT001" {
		t.Errorf("expected patient_id PAT001, got %s", createdEntry.PatientID)
	}
	if createdEntry.Espacios != 2 {
		t.Errorf("expected espacios 2, got %d", createdEntry.Espacios)
	}
}

func TestNoSlots_AutoAddToWL_Duplicate(t *testing.T) {
	wlRepo := &testutil.MockWaitingListCreator{
		HasActiveForPatientAndCupsFn: func(ctx context.Context, patientID, cupsCode string) (bool, error) {
			return true, nil // Already in WL
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateNoSlotsAvailable, noSlotsHandler(wlRepo))

	sess := testSess(sm.StateNoSlotsAvailable)
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"
	sess.Context["patient_id"] = "PAT001"
	sess.Context["reschedule_skip_cancel"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

func TestNoSlots_ActiveReschedule_NoWL(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateNoSlotsAvailable, noSlotsHandler(nil))

	sess := testSess(sm.StateNoSlotsAvailable)
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"
	sess.Context["reschedule_appt_id"] = "APT-ACTIVE-1" // Has active appointment
	// reschedule_skip_cancel is NOT set (or "0") → appointment still active

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != sm.StateNotifRescheduleFallback {
		t.Errorf("expected NOTIF_RESCHEDULE_FALLBACK, got %s", result.NextState)
	}
	// Should show Confirmar/Cancelar buttons (patient still has their appointment)
}

func TestNoSlots_NormalFlow_NoAutoAdd(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateNoSlotsAvailable, noSlotsHandler(nil))

	sess := testSess(sm.StateNoSlotsAvailable)
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"
	// No reschedule_skip_cancel → normal flow

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != sm.StateOfferWaitingList {
		t.Errorf("expected OFFER_WAITING_LIST, got %s", result.NextState)
	}
}

func TestNoSlots_AutoAddToWL_CreateError(t *testing.T) {
	wlRepo := &testutil.MockWaitingListCreator{
		CreateFn: func(ctx context.Context, entry *domain.WaitingListEntry) error {
			return fmt.Errorf("db error")
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateNoSlotsAvailable, noSlotsHandler(wlRepo))

	sess := testSess(sm.StateNoSlotsAvailable)
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"
	sess.Context["patient_id"] = "PAT001"
	sess.Context["reschedule_skip_cancel"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != sm.StateMainMenu {
		t.Errorf("expected MAIN_MENU on error (patient can retry), got %s", result.NextState)
	}
}

// --- confirmBookingHandler invalid input path ---

func TestConfirmBooking_InvalidInput_SlotNotFound(t *testing.T) {
	m := sm.NewMachine()
	registerConfirmBookingConfig(m)

	sess := testSess(sm.StateConfirmBooking)
	// No slots or slot id -> slot not found on invalid input path
	sess.Context["cups_name"] = "EMG"

	result, err := m.Process(context.Background(), sess, textM("hola"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateSearchSlots {
		t.Errorf("expected SEARCH_SLOTS, got %s", result.NextState)
	}
}

// --- reconfirmBookingHandler tests ---

func TestReconfirmBooking_Yes(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateReconfirmBooking, reconfirmBookingHandler(nil))

	sess := testSess(sm.StateReconfirmBooking)

	result, err := m.Process(context.Background(), sess, postbackM("reconfirm_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCreateAppointment {
		t.Errorf("expected CREATE_APPOINTMENT, got %s", result.NextState)
	}
}

func TestReconfirmBooking_No(t *testing.T) {
	slots := sampleSlots()

	m := sm.NewMachine()
	m.Register(sm.StateReconfirmBooking, reconfirmBookingHandler(nil))

	sess := testSess(sm.StateReconfirmBooking)
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slots[0].TimeSlot
	sess.Context["cups_name"] = "Electromiografia"

	result, err := m.Process(context.Background(), sess, postbackM("reconfirm_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmBooking {
		t.Errorf("expected CONFIRM_BOOKING, got %s", result.NextState)
	}
}
