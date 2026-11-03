package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// prepFinder devuelve la ficha de cada CUPS que tenga.
type prepFinder map[string]*domain.Procedure

func (p prepFinder) FindByCode(_ context.Context, code string) (*domain.Procedure, error) {
	return p[code], nil
}

// El mensaje de «cita confirmada» queda fijado texto a texto: es el mismo de antes de compartirlo
// con el proceso de fondo de D-04 (fecha y hora de la cita recordada, procedimientos de todas las
// citas del día sin repetir, la primera dirección y las preparaciones con su video).
func TestBuildConfirmationMessage_Golden(t *testing.T) {
	m := &NotificationManager{procRepo: prepFinder{
		"890271": {Address: "Calle 1 # 2-3", MapsURL: "https://maps/1", Preparation: "Ayuno de 8 horas", VideoURL: "https://v/1"},
		"891509": {Address: "Otra sede", Preparation: "Traer exámenes"},
	}}
	appt := domain.Appointment{
		ID: "A1", Date: time.Date(2026, 11, 5, 0, 0, 0, 0, time.Local), TimeSlot: "202611051030",
		Procedures: []domain.AppointmentProcedure{{CupCode: "890271", CupName: "EMG"}},
	}
	other := domain.Appointment{
		ID: "A2", Procedures: []domain.AppointmentProcedure{{CupCode: "891509", CupName: "NC"}, {CupCode: "890271", CupName: "EMG"}},
	}

	got := m.buildConfirmationMessage(context.Background(), &appt, []domain.Appointment{appt, other})

	want := "Tu cita ha sido confirmada!\n\n" +
		"*Fecha:* Jueves 5 de Noviembre de 2026\n" +
		"*Hora:* 10:30 AM\n" +
		"*Procedimiento:* EMG y NC\n" +
		"*Dirección:* Calle 1 # 2-3\n\n" +
		"*Preparación:*\n" +
		"- Para *EMG*: Ayuno de 8 horas\n" +
		"  📹 Video: https://v/1\n" +
		"- Para *NC*: Traer exámenes\n\n" +
		"Recuerda presentarte 30 minutos antes para realizar el proceso de facturación. Te esperamos!"
	if got != want {
		t.Fatalf("mensaje =\n%q\nwant\n%q", got, want)
	}
}
