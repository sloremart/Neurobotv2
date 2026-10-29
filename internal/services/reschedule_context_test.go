package services

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// RescheduleContext es el armado ÚNICO del contexto para reprogramar una cita notificada: lo
// usan startConfirmRescheduleSession, startSelfReschedule y NOTIF_PENDING «Reprogramar». Esta
// prueba fija las claves y valores que esas sesiones ya guardaban.
func TestRescheduleContext_BuildsTheNotificationRescheduleContext(t *testing.T) {
	repo := &mockAppointmentRepo{
		slotCountFn: func(context.Context, string) (int, error) { return 3, nil },
	}
	svc := NewAppointmentService(repo, nil)
	appt := &domain.Appointment{
		ID: "A1", PatientID: "P1", PatientName: "ANA", Entity: "EPS005", DoctorDocument: "1234",
		Date: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), TimeSlot: "202611030730",
		Observations: "Contrastada",
		Procedures: []domain.AppointmentProcedure{
			{CupCode: "883101", CupName: "RESONANCIA"}, {CupCode: "883102", CupName: "RESONANCIA 2"},
		},
	}

	got := svc.RescheduleContext(context.Background(), appt, 1)

	groups, _ := json.Marshal([]CUPSGroup{{
		ServiceType: "general", Espacios: 3,
		Cups: []CUPSEntry{{Code: "883101", Name: "RESONANCIA", Quantity: 1}, {Code: "883102", Name: "RESONANCIA 2", Quantity: 1}},
	}})
	want := map[string]string{
		"patient_id": "P1", "patient_name": "ANA", "patient_entity": "EPS005", "patient_contract": "EPS005",
		"patient_age": "0",
		"cups_code":   "883101", "cups_name": "RESONANCIA", "is_contrasted": "1", "is_sedated": "0",
		"espacios": "3", "procedures_json": string(groups),
		"total_procedures": "1", "current_procedure_idx": "0", "menu_option": "agendar",
		"reschedule_appt_id": "A1", "reschedule_skip_cancel": "0",
		"preferred_doctor_doc": "1234",
	}
	if !maps.Equal(got, want) {
		t.Errorf("contexto =\n%v\nquería\n%v", got, want)
	}
}

// Los espacios son el mayor entre el bloque y los slots reales de la cita, mínimo 1.
func TestRescheduleContext_Espacios(t *testing.T) {
	cases := []struct {
		block, slots int
		want         string
	}{{0, 0, "1"}, {2, 0, "2"}, {1, 4, "4"}}
	for _, c := range cases {
		repo := &mockAppointmentRepo{slotCountFn: func(context.Context, string) (int, error) { return c.slots, nil }}
		got := NewAppointmentService(repo, nil).RescheduleContext(context.Background(), &domain.Appointment{ID: "A1"}, c.block)
		if got["espacios"] != c.want {
			t.Errorf("bloque=%d slots=%d: espacios=%s, quería %s", c.block, c.slots, got["espacios"], c.want)
		}
	}
}
