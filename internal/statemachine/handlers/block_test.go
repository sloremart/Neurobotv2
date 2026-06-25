package handlers

import (
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// TestSelectedAppointments verifica que "Mis citas" actúa SOLO sobre la cita seleccionada
// (modelo 1 cita = N slots; se eliminó el bloque consecutivo Antares).
func TestSelectedAppointments(t *testing.T) {
	appts := []domain.Appointment{{ID: "a1"}, {ID: "a2"}, {ID: "a3"}}

	got := selectedAppointments(appts, "a2")
	if len(got) != 1 || got[0].ID != "a2" {
		t.Fatalf("esperaba solo [a2], got %+v", got)
	}

	if got := selectedAppointments(appts, "noexiste"); got != nil {
		t.Errorf("esperaba nil para id inexistente, got %+v", got)
	}

	if got := selectedAppointments(nil, "a1"); got != nil {
		t.Errorf("esperaba nil para lista vacía, got %+v", got)
	}
}
