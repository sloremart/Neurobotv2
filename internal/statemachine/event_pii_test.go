package statemachine

import (
	"encoding/json"
	"strings"
	"testing"
)

// eventsJSON junta los datos de los eventos de un resultado tal como irían a chat_events.
func eventsJSON(t *testing.T, r *StateResult) string {
	t.Helper()
	if r == nil {
		t.Fatal("se esperaba un resultado")
	}
	b, err := json.Marshal(r.Events)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// C-04: lo que el paciente escribe en un paso de datos personales (nombres, fecha de nacimiento,
// dirección, correo, celular) no se guarda en claro en invalid_input ni en max_retries_reached:
// solo su largo. Lo que ve el agente (CtxEscalationLastInput) no cambia.
func TestInvalidInputEvent_FreeTextStatesStoreOnlyLength(t *testing.T) {
	never := func(string) bool { return false }
	// La lista va escrita a mano (no se recorre freeTextInputStates): quitar un paso de esa lista
	// debe romper este test.
	cases := []struct{ state, input string }{
		{StateRegFirstSurname, "Pérez Gómez"},
		{StateRegSecondSurname, "Pérez Gómez"},
		{StateRegFirstName, "María José"},
		{StateRegSecondName, "María José"},
		{StateRegBirthDate, "31/02/1980"},
		{StateRegOccupation, "docente Colegio San José"},
		{StateRegAddress, "Cra 5 # 10-20"},
		{StateRegPhone, "300 123"},
		{StateRegPhone2, "300 123"},
		{StateRegEmail, "maria.jose@correo"},
		{StateRegBarrio, "barrio El Prado"},
		{StateAskUpdateEmail, "maria.jose@correo"},
		{StateAskUpdatePhone, "300 123"},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			sess := helperSess(c.state)
			got := eventsJSON(t, ValidateWithRetry(sess, c.input, never, "error"))
			if strings.Contains(got, c.input) {
				t.Fatalf("invalid_input guarda el texto en claro: %s", got)
			}
			if !strings.Contains(got, "[redactado: ") {
				t.Fatalf("invalid_input debe guardar el largo: %s", got)
			}

			sess.RetryCount = maxRetries - 1
			r := ValidateWithRetry(sess, c.input, never, "error")
			if got := eventsJSON(t, r); strings.Contains(got, c.input) {
				t.Fatalf("max_retries_reached guarda el texto en claro: %s", got)
			}
			if r.UpdateCtx[CtxEscalationLastInput] != c.input {
				t.Fatalf("lo que ve el agente no cambia: got %q", r.UpdateCtx[CtxEscalationLastInput])
			}
		})
	}
}

// Fuera de esos pasos el texto se sigue guardando: es la materia prima del análisis de fricción.
func TestInvalidInputEvent_OtherStatesKeepText(t *testing.T) {
	sess := helperSess(StateMainMenu)
	r, _ := ValidateButtonResponse(sess, textMsg("quiero una cita"), "agendar", "consultar")
	if got := eventsJSON(t, r); !strings.Contains(got, "quiero una cita") {
		t.Fatalf("invalid_input de un menú conserva el texto: %s", got)
	}
}

// Un paso de datos personales con botones también guarda solo el largo (ValidateButtonResponse).
func TestInvalidInputEvent_FreeTextStateWithButtonsStoresOnlyLength(t *testing.T) {
	sess := helperSess(StateRegAddress)
	r, _ := ValidateButtonResponse(sess, textMsg("Cra 5 # 10-20"), "ok")
	if got := eventsJSON(t, r); strings.Contains(got, "Cra 5") || !strings.Contains(got, "[redactado: ") {
		t.Fatalf("invalid_input de un paso de datos personales con botones: %s", got)
	}
}

// En los pasos del TIPO de documento el paciente suele escribir el número (con letras: «CC
// 1000000689»): el evento lo guarda enmascarado como documento, por las dos vías de validación.
func TestInvalidInputEvent_DocumentTypeStatesMaskDocument(t *testing.T) {
	never := func(string) bool { return false }
	for _, state := range []string{StateAskDocumentType, StateRegDocumentType} {
		t.Run(state, func(t *testing.T) {
			got := eventsJSON(t, ValidateWithRetry(helperSess(state), "CC 1000000689", never, "error"))
			if strings.Contains(got, "1000000689") || !strings.Contains(got, "CC***89") {
				t.Fatalf("invalid_input (texto) debe guardar el documento enmascarado: %s", got)
			}
			r, _ := ValidateButtonResponse(helperSess(state), textMsg("mi cedula 1000000689"), "CC", "TI")
			if got := eventsJSON(t, r); strings.Contains(got, "1000000689") {
				t.Fatalf("invalid_input (botones) debe guardar el documento enmascarado: %s", got)
			}
		})
	}
}
