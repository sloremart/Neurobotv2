package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// KPI "recuperación de cupos cancelados": cuando el agendamiento nace de la lista de espera, la
// entrada debe quedar 'scheduled' CON el ID de la cita creada (vínculo permanente — los flow_events
// se purgan a 45 días y sin esta columna la métrica pierde el numerador histórico).
func TestCreateAppointment_WaitingList_MarksScheduledWithAppointment(t *testing.T) {
	slots := sampleSlots()
	repo := &testutil.MockAppointmentRepo{
		CreateFn: func(_ context.Context, _ domain.CreateAppointmentInput) (*domain.Appointment, error) {
			return &domain.Appointment{ID: "100"}, nil
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	var gotWL, gotAppt string
	wlRepo := &testutil.MockWaitingListCreator{
		MarkScheduledFn: func(_ context.Context, id, appointmentID string) error {
			gotWL, gotAppt = id, appointmentID
			return nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, nil, nil, nil, wlRepo))

	groups := []services.CUPSGroup{{
		ServiceType: "Neurofisiologia",
		Cups:        []services.CUPSEntry{{Code: "890271", Name: "Electromiografia", Quantity: 1}},
		Espacios:    1,
	}}
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
	sess.Context["waiting_list_entry_id"] = "wl-perm-link"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateBookingSuccess {
		t.Fatalf("expected BOOKING_SUCCESS, got %s", result.NextState)
	}
	if gotWL != "wl-perm-link" || gotAppt != "100" {
		t.Errorf("la entrada WL debe marcarse scheduled con la cita creada: got wl=%q appt=%q", gotWL, gotAppt)
	}
}
