package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// C-04: lo que va a chat_events no lleva el nombre, el correo, el celular ni el documento del
// paciente en claro. Cada caso corre el handler y busca el dato crudo en sus eventos.

func eventsOf(t *testing.T, r *sm.StateResult) string {
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

func assertNoneIn(t *testing.T, events string, raw ...string) {
	t.Helper()
	for _, v := range raw {
		if strings.Contains(events, v) {
			t.Errorf("el evento guarda %q en claro: %s", v, events)
		}
	}
}

func TestPatientFoundEvent_HasNoName(t *testing.T) {
	repo := &mockPatientRepo{findByDocumentFn: func(context.Context, string, string) (*domain.Patient, error) {
		return &domain.Patient{ID: "PAT001", DocumentNumber: "1234567890", FirstName: "Juan", FirstSurname: "Perez"}, nil
	}}
	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, services.NewPatientService(repo))
	sess := testSess(sm.StatePatientLookup)
	sess.Context["patient_doc"] = "1234567890"
	r, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	ev := eventsOf(t, r)
	assertNoneIn(t, ev, "Juan", "Perez", "1234567890")
	if !strings.Contains(ev, "patient_found") || !strings.Contains(ev, "PAT001") {
		t.Fatalf("patient_found sigue saliendo con su patient_id (el dashboard lo cuenta): %s", ev)
	}
}

func TestContactInfoShownEvent_Masked(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowContactInfo, showContactInfoHandler())
	sess := testSess(sm.StateShowContactInfo)
	sess.Context["patient_phone"] = "3103343616"
	sess.Context["patient_email"] = "juan.perez@email.com"
	r, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	assertNoneIn(t, eventsOf(t, r), "334 3616", "3103343616", "juan.perez@")
}

func TestContactPhoneUpdatedEvent_Masked(t *testing.T) {
	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, services.NewPatientService(&mockPatientRepo{}))
	sess := testSess(sm.StateAskUpdatePhone)
	r, err := m.Process(context.Background(), sess, textM("3209302716"))
	if err != nil {
		t.Fatal(err)
	}
	if r.UpdateCtx["contact_new_phone"] != "+573209302716" {
		t.Fatalf("el flujo sigue guardando el celular completo: %q", r.UpdateCtx["contact_new_phone"])
	}
	assertNoneIn(t, eventsOf(t, r), "3209302716")
}

func TestContactEmailUpdatedEvent_Masked(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskUpdateEmail, askUpdateEmailHandler())
	r, err := m.Process(context.Background(), testSess(sm.StateAskUpdateEmail), textM("nuevo.correo@email.com"))
	if err != nil {
		t.Fatal(err)
	}
	if r.UpdateCtx["contact_new_email"] != "nuevo.correo@email.com" {
		t.Fatalf("el flujo sigue guardando el correo completo: %q", r.UpdateCtx["contact_new_email"])
	}
	assertNoneIn(t, eventsOf(t, r), "nuevo.correo@")
}

func TestContactInfoUpdatedEvent_Masked(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateUpdateContactInfo, updateContactInfoHandler(services.NewPatientService(&mockPatientRepo{})))
	sess := testSess(sm.StateUpdateContactInfo)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["contact_new_phone"] = "+573209302716"
	sess.Context["contact_new_email"] = "nuevo.correo@email.com"
	sess.Context["menu_option"] = "consultar"
	r, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	assertNoneIn(t, eventsOf(t, r), "3209302716", "nuevo.correo@")
}

func TestGreetingMidflowEvent_InputMaskedAsDocument(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskDocumentType, askDocumentTypeHandler())
	r, err := m.Process(context.Background(), testSess(sm.StateAskDocumentType), textM("hola"))
	if err != nil {
		t.Fatal(err)
	}
	ev := eventsOf(t, r)
	if !strings.Contains(ev, "greeting_midflow_redirected") {
		t.Fatalf("se esperaba greeting_midflow_redirected: %s", ev)
	}
	assertNoneIn(t, ev, `"hola"`)
}

func TestMedicationNotInListEvent_DocMasked(t *testing.T) {
	sess := testSess(sm.StateMedicationCheckExcel)
	sess.Context["patient_doc"] = "1000000689"
	assertNoneIn(t, eventsOf(t, backToMenuNoRecord(sess)), "1000000689")
}

func TestMedicationWithoutAuthorizationEvent_DocMasked(t *testing.T) {
	sess := testSess(sm.StateMedicationCheckExcel)
	sess.Context["patient_doc"] = "1000000689"
	r := medicationWithoutAuthorization(sess, &local.MedicationPendingRecord{Medicamento: "TOXINA BOTULINICA"})
	ev := eventsOf(t, r)
	assertNoneIn(t, ev, "1000000689")
	if !strings.Contains(ev, "medication_sin_autorizacion") || !strings.Contains(ev, "TOXINA BOTULINICA") {
		t.Fatalf("el evento sigue saliendo con el medicamento: %s", ev)
	}
}
