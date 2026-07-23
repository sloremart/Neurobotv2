package notifications

import (
	"strings"
	"testing"
)

// GARANTÍA de no-regresión (§8.1 #2 / D1): con SameDay=false (todo el flujo preexistente del
// recordatorio del día antes) la palabra es EXACTAMENTE "mañana" — los textos de followup quedan
// carácter por carácter como siempre. "hoy" aplica solo al recordatorio de corta antelación.
func TestFollowupDayWord(t *testing.T) {
	if got := followupDayWord(false); got != "mañana" {
		t.Errorf(`followupDayWord(false) = %q; el flujo preexistente DEBE decir "mañana"`, got)
	}
	if got := followupDayWord(true); got != "hoy" {
		t.Errorf(`followupDayWord(true) = %q; esperaba "hoy"`, got)
	}
}

// Los textos completos del followup con SameDay=false deben ser EXACTAMENTE los históricos.
func TestFollowupTexts_PreexistingFlowUnchanged(t *testing.T) {
	day := followupDayWord(false)
	f1 := "¡Hola! Aún no recibimos tu respuesta sobre tu cita de " + day + ". " +
		"Por favor confirma, cancela o reprograma para que podamos gestionar tu espacio."
	f2 := "Recordatorio: Tu cita de " + day + " aún no ha sido confirmada. " +
		"Si no recibimos respuesta, te llamaremos para confirmar."
	esc := "Un asistente del centro se comunicará contigo para confirmar tu cita de " + day + "."

	// Strings históricos literales (pre-cambio) — si alguien altera la composición, esto truena.
	if f1 != "¡Hola! Aún no recibimos tu respuesta sobre tu cita de mañana. Por favor confirma, cancela o reprograma para que podamos gestionar tu espacio." {
		t.Error("followup 1 cambió para el flujo preexistente")
	}
	if f2 != "Recordatorio: Tu cita de mañana aún no ha sido confirmada. Si no recibimos respuesta, te llamaremos para confirmar." {
		t.Error("followup 2 cambió para el flujo preexistente")
	}
	if esc != "Un asistente del centro se comunicará contigo para confirmar tu cita de mañana." {
		t.Error("mensaje de escalación cambió para el flujo preexistente")
	}
}

// La nota interna de escalación por no-respuesta debe ser ACCIONABLE: instrucción explícita de llamar
// + teléfono (los agentes salen a las 18:00, no hay tiempo para inferir). Y conserva "manana"/"hoy".
func TestBuildNoResponseNote_CallActionAndPhone(t *testing.T) {
	apptInfo := "Paciente: ANA RUIZ\nFecha: hoy | Hora: 10:00 AM\nProcedimiento: RM\nCita ID: 99"

	note := buildNoResponseNote(false, "+573001112233", apptInfo)
	for _, must := range []string{
		"Paciente NO confirmo cita de manana.",
		"ACCION: LLAMAR al paciente",
		"Tel: +573001112233",
		"Cita ID: 99",
	} {
		if !strings.Contains(note, must) {
			t.Errorf("nota sin %q:\n%s", must, note)
		}
	}

	if !strings.Contains(buildNoResponseNote(true, "+57300", apptInfo), "cita de hoy") {
		t.Error(`nota same-day debe decir "cita de hoy"`)
	}
}
