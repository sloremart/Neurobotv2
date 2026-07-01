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
			FindAvailableSlotsFn: func(_ context.Context, _ int, _ string, _ []int) ([]domain.AvailableSlotRow, error) {
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
	// "Ver más" solo se ofrece con >=5 slots (#17): se arman 5 y se elige la opción 6 (len+1).
	slots := make([]services.AvailableSlot, 5)
	for i := range slots {
		slots[i] = services.AvailableSlot{
			TimeSlot: fmt.Sprintf("2026032010%02d", i), Date: "2026-03-20",
			TimeDisplay: fmt.Sprintf("1%d:00", i), DoctorName: "Garcia", DoctorDoc: "DOC001", AgendaID: 1, Duration: 30,
		}
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowSlots, showSlotsHandler(nil))

	sess := testSess(sm.StateShowSlots)
	sess.Context["available_slots_json"] = slotsJSON(slots)

	// "Ver más" = slot count + 1 = option "6" (5 slots → se ofrece "Ver más")
	result, err := m.Process(context.Background(), sess, textM("6"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateSearchSlots {
		t.Errorf("expected SEARCH_SLOTS for more_slots, got %s", result.NextState)
	}
}

// #17: con <5 slots no se ofrece "Ver más", así que la opción len+1 debe ser inválida.
func TestShowSlots_MoreNotOfferedBelow5(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowSlots, showSlotsHandler(nil))

	sess := testSess(sm.StateShowSlots)
	sess.Context["available_slots_json"] = slotsJSON(sampleSlots()) // 1 slot

	result, err := m.Process(context.Background(), sess, textM("2")) // len+1 = 2, no ofrecido
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sess.CurrentState {
		t.Errorf("expected to stay in %s (opción inválida), got %s", sess.CurrentState, result.NextState)
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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])

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
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, &testutil.MockWaitingListCreator{}))

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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
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
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, &testutil.MockWaitingListCreator{}))

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
			return nil, fmt.Errorf("%w: already booked", domain.ErrSlotTaken)
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, &testutil.MockWaitingListCreator{}))

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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
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
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, &testutil.MockWaitingListCreator{}))

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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
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
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
	sess.Context["cups_name"] = "Electromiografia"

	result, err := m.Process(context.Background(), sess, postbackM("reconfirm_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmBooking {
		t.Errorf("expected CONFIRM_BOOKING, got %s", result.NextState)
	}
}

// TestFindSelectedSlot_DisambiguatesSameTime (H1): si dos médicos del mismo asunto tienen libre la
// MISMA fecha+hora, findSelectedSlot debe devolver EXACTAMENTE el slot elegido (por su clave única),
// no el primero con esa hora. Antes mapeaba solo por TimeSlot → agendaba con el médico equivocado.
func TestFindSelectedSlot_DisambiguatesSameTime(t *testing.T) {
	slots := []services.AvailableSlot{
		{TimeSlot: "202603201000", AgendaID: 10, DoctorSiesaCode: "DOCA", DoctorName: "Garcia"},
		{TimeSlot: "202603201000", AgendaID: 20, DoctorSiesaCode: "DOCB", DoctorName: "Lopez"}, // misma hora, otro médico/agenda
	}
	sess := &session.Session{Context: map[string]string{
		"available_slots_json": slotsJSON(slots),
		"selected_slot_id":     slotKey(&slots[1]), // el paciente eligió el SEGUNDO (Dr. Lopez)
	}}

	got := findSelectedSlot(sess)
	if got == nil {
		t.Fatal("expected to find the selected slot")
	}
	if got.AgendaID != 20 || got.DoctorName != "Lopez" {
		t.Errorf("regresión H1: esperaba el slot elegido (Lopez, agenda 20), obtuvo %s agenda %d",
			got.DoctorName, got.AgendaID)
	}
}

func TestSlotKey_UniquePerDoctorSameTime(t *testing.T) {
	a := services.AvailableSlot{TimeSlot: "202603201000", AgendaID: 10, DoctorSiesaCode: "DOCA"}
	b := services.AvailableSlot{TimeSlot: "202603201000", AgendaID: 20, DoctorSiesaCode: "DOCB"}
	if slotKey(&a) == slotKey(&b) {
		t.Error("dos médicos a la misma hora deben tener claves de slot distintas")
	}
}

// twoProcSession arma una sesión multi-procedimiento (idx 0 de 2) para los tests de lista de espera.
func twoProcSession(state string) *session.Session {
	groups := []services.CUPSGroup{
		{ServiceType: "4", Espacios: 1, Cups: []services.CUPSEntry{{Code: "871121", Name: "RX TORAX"}}},
		{ServiceType: "5", Espacios: 1, Cups: []services.CUPSEntry{{Code: "881434", Name: "TAC CRANEO"}}},
	}
	pj, _ := json.Marshal(groups)
	sess := testSess(state)
	sess.Context["total_procedures"] = "2"
	sess.Context["current_procedure_idx"] = "0"
	sess.Context["procedures_json"] = string(pj)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["patient_age"] = "30"
	sess.Context["cups_code"] = "871121"
	sess.Context["cups_name"] = "RX TORAX"
	sess.Context["espacios"] = "1"
	return sess
}

// #9 (re-análisis): al avanzar al siguiente procedimiento se debe LIMPIAR waiting_list_entry_id, para
// que la entrada del procedimiento anterior (sin slots) no quede "pegada" y sea marcada 'scheduled' por
// error cuando el siguiente procedimiento agende.
func TestAdvanceToNextProcedure_ClearsWaitingListEntryID(t *testing.T) {
	sess := twoProcSession(sm.StateShowSlots)
	sess.Context["waiting_list_entry_id"] = "WL-STALE"

	res := advanceToNextProcedure(sess)
	if res == nil {
		t.Fatal("expected advance to next procedure, got nil")
	}
	found := false
	for _, k := range res.ClearCtx {
		if k == "waiting_list_entry_id" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("advanceToNextProcedure debe limpiar waiting_list_entry_id; ClearCtx=%v", res.ClearCtx)
	}
}

// El auto-join a lista de espera de un procedimiento sin slots NO debe propagar su waiting_list_entry_id
// al siguiente procedimiento (ni en UpdateCtx), y debe limpiarlo. Así el booking posterior del siguiente
// procedimiento no marca por error la entrada anterior como 'scheduled'.
func TestAutoAddToWaitingList_DoesNotLeakEntryIDToNextProcedure(t *testing.T) {
	var createdID string
	wlRepo := &testutil.MockWaitingListCreator{
		CreateFn: func(_ context.Context, entry *domain.WaitingListEntry) error {
			createdID = entry.ID
			return nil
		},
	}
	sess := twoProcSession(sm.StateNoSlotsAvailable)

	res, err := autoAddToWaitingList(context.Background(), sess, wlRepo, "RX TORAX")
	if err != nil {
		t.Fatal(err)
	}
	if createdID == "" {
		t.Fatal("expected a waiting list entry to be created")
	}
	// Debe avanzar al siguiente procedimiento.
	if res.NextState != sm.StateCheckSpecialCups {
		t.Errorf("expected advance to CHECK_SPECIAL_CUPS, got %s", res.NextState)
	}
	// NO debe setear waiting_list_entry_id en el contexto del siguiente procedimiento.
	if v, ok := res.UpdateCtx["waiting_list_entry_id"]; ok {
		t.Errorf("auto-join no debe propagar waiting_list_entry_id al siguiente procedimiento, got %q", v)
	}
	// Y debe limpiarla (defensa por si venía de antes).
	cleared := false
	for _, k := range res.ClearCtx {
		if k == "waiting_list_entry_id" {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Errorf("auto-join debe limpiar waiting_list_entry_id al avanzar; ClearCtx=%v", res.ClearCtx)
	}
}

// --- Tests deduplicación de lista de espera por conjunto de CUPS ---

func cupsSetJSON(codes ...string) string {
	cups := make([]services.CUPSEntry, 0, len(codes))
	for _, c := range codes {
		cups = append(cups, services.CUPSEntry{Code: c})
	}
	b, _ := json.Marshal([]services.CUPSGroup{{Cups: cups}})
	return string(b)
}

func TestCitaCupsSet(t *testing.T) {
	set := citaCupsSet(cupsSetJSON("A", "B"))
	if len(set) != 2 || !set["A"] || !set["B"] {
		t.Errorf("set=%v, esperaba {A,B}", set)
	}
}

func TestIsSubset(t *testing.T) {
	a := map[string]bool{"A": true}
	ab := map[string]bool{"A": true, "B": true}
	if !isSubset(a, ab) {
		t.Error("{A} debe ser subconjunto de {A,B}")
	}
	if isSubset(ab, a) {
		t.Error("{A,B} no es subconjunto de {A}")
	}
	if isSubset(map[string]bool{}, ab) {
		t.Error("conjunto vacío → false por diseño")
	}
}

func TestWaitingListDedup(t *testing.T) {
	activesFn := func(entries ...domain.WaitingListEntry) *testutil.MockWaitingListCreator {
		return &testutil.MockWaitingListCreator{
			GetActiveByPatientFn: func(_ context.Context, _ string) ([]domain.WaitingListEntry, error) {
				return entries, nil
			},
		}
	}

	// Duplicado: activo {A,B}, nueva {A,B} → no crear.
	dup, sup := waitingListDedup(context.Background(),
		activesFn(domain.WaitingListEntry{ID: "e1", ProceduresJSON: cupsSetJSON("A", "B")}),
		"p", citaCupsSet(cupsSetJSON("A", "B")))
	if !dup || len(sup) != 0 {
		t.Errorf("duplicado: dup=%v sup=%v", dup, sup)
	}

	// Supersede: activo {A}, nueva {A,B} → expira e1, crea la nueva.
	dup, sup = waitingListDedup(context.Background(),
		activesFn(domain.WaitingListEntry{ID: "e1", ProceduresJSON: cupsSetJSON("A")}),
		"p", citaCupsSet(cupsSetJSON("A", "B")))
	if dup || len(sup) != 1 || sup[0] != "e1" {
		t.Errorf("supersede: dup=%v sup=%v", dup, sup)
	}

	// Nueva sin overlap: activo {C}, nueva {A,B} → crear normal.
	dup, sup = waitingListDedup(context.Background(),
		activesFn(domain.WaitingListEntry{ID: "e1", ProceduresJSON: cupsSetJSON("C")}),
		"p", citaCupsSet(cupsSetJSON("A", "B")))
	if dup || len(sup) != 0 {
		t.Errorf("nueva: dup=%v sup=%v", dup, sup)
	}

	// Fallback datos viejos (procedures_json vacío) → usa cups_code.
	dup, _ = waitingListDedup(context.Background(),
		activesFn(domain.WaitingListEntry{ID: "e1", CupsCode: "A", ProceduresJSON: "[]"}),
		"p", citaCupsSet(cupsSetJSON("A")))
	if !dup {
		t.Errorf("fallback cups_code: dup=%v, esperaba true", dup)
	}
}

func TestCitaProceduresJSON_OnlyCurrentCita(t *testing.T) {
	pj, _ := json.Marshal([]services.CUPSGroup{
		{ServiceType: "RX", Cups: []services.CUPSEntry{{Code: "A"}}, Espacios: 1},
		{ServiceType: "Resonancia", Cups: []services.CUPSEntry{{Code: "B"}, {Code: "C"}}, Espacios: 2},
	})
	sess := testSess(sm.StateSearchSlots)
	sess.SetContext("procedures_json", string(pj))
	sess.SetContext("current_procedure_idx", "1")

	var groups []services.CUPSGroup
	if err := json.Unmarshal([]byte(citaProceduresJSON(sess)), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Cups) != 2 || groups[0].Cups[0].Code != "B" || groups[0].Espacios != 2 {
		t.Errorf("esperaba solo la cita idx=1 {B,C} espacios=2, got %+v", groups)
	}
}

func TestCitaProceduresJSON_BakesOcrQuantity(t *testing.T) {
	// Grupo agrupado con cantidad 1, pero el OCR original traía 4 y 2 → deben hornearse.
	pj, _ := json.Marshal([]services.CUPSGroup{
		{ServiceType: "Fisiatria", Cups: []services.CUPSEntry{{Code: "A", Quantity: 1}, {Code: "B", Quantity: 1}}, Espacios: 3},
	})
	ocr, _ := json.Marshal([]services.CUPSEntry{{Code: "A", Quantity: 4}, {Code: "B", Quantity: 2}})
	sess := testSess(sm.StateSearchSlots)
	sess.SetContext("procedures_json", string(pj))
	sess.SetContext("current_procedure_idx", "0")
	sess.SetContext("ocr_cups_json", string(ocr))

	var groups []services.CUPSGroup
	if err := json.Unmarshal([]byte(citaProceduresJSON(sess)), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Cups) != 2 {
		t.Fatalf("grupo inesperado: %+v", groups)
	}
	got := map[string]int{}
	for _, c := range groups[0].Cups {
		got[c.Code] = c.Quantity
	}
	if got["A"] != 4 || got["B"] != 2 {
		t.Errorf("esperaba cantidades del OCR A=4 B=2, got %v", got)
	}
	if groups[0].Espacios != 3 {
		t.Errorf("espacios debe preservarse=3, got %d", groups[0].Espacios)
	}
}
