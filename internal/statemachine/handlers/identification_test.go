package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/validators"
)

// --- Mock PatientRepository ---

type mockPatientRepo struct {
	findByDocumentFn    func(ctx context.Context, docType, doc string) (*domain.Patient, error)
	updateContactInfoFn func(ctx context.Context, patientID, phone, email string) error
}

func (m *mockPatientRepo) FindByDocument(ctx context.Context, docType, doc string) (*domain.Patient, error) {
	if m.findByDocumentFn != nil {
		return m.findByDocumentFn(ctx, docType, doc)
	}
	return nil, nil
}

func (m *mockPatientRepo) FindByID(ctx context.Context, id string) (*domain.Patient, error) {
	return nil, nil
}

func (m *mockPatientRepo) Create(ctx context.Context, input domain.CreatePatientInput) (string, error) {
	return "", nil
}

func (m *mockPatientRepo) UpdateEntity(ctx context.Context, patientID, entityCode string) error {
	return nil
}

func (m *mockPatientRepo) UpdateContract(ctx context.Context, patientID, contractCode string) error {
	return nil
}

func (m *mockPatientRepo) UpdateMunicipality(ctx context.Context, patientID, depCode, muniCode string) error {
	return nil
}

func (m *mockPatientRepo) UpdateContactInfo(ctx context.Context, patientID, phone, email string) error {
	if m.updateContactInfoFn != nil {
		return m.updateContactInfoFn(ctx, patientID, phone, email)
	}
	return nil
}

// ==================== AskDocumentType ====================

// TestAskDocumentType_AssumesCCOnDocNumber: el paciente escribe su NÚMERO de documento en el paso del
// TIPO (confusión común con el prompt). Se asume CC y se va DIRECTO al lookup con ese número, en vez de
// rechazar y terminar escalando por reintentos.
func TestAskDocumentType_AssumesCCOnDocNumber(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskDocumentType, askDocumentTypeHandler())

	sess := testSess(sm.StateAskDocumentType)
	result, err := m.Process(context.Background(), sess, textM("17318898"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StatePatientLookup {
		t.Errorf("esperaba PATIENT_LOOKUP (asume CC y busca), got %s", result.NextState)
	}
	if result.UpdateCtx["patient_doc_type"] != "CC" {
		t.Errorf("esperaba patient_doc_type=CC, got %q", result.UpdateCtx["patient_doc_type"])
	}
	if result.UpdateCtx["patient_doc"] != "17318898" {
		t.Errorf("esperaba patient_doc=17318898, got %q", result.UpdateCtx["patient_doc"])
	}
}

// ==================== AskDocument ====================

func registerAskDocumentConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateAskDocument, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: validators.Document,
		ErrorMsg:     "Por favor ingresa un número de documento válido (solo números, entre 5 y 15 dígitos).",
		Handler:      askDocumentHandler(),
	})
}

func TestAskDocument_ValidTenDigits(t *testing.T) {
	m := sm.NewMachine()
	registerAskDocumentConfig(m)

	sess := testSess(sm.StateAskDocument)
	result, err := m.Process(context.Background(), sess, textM("1234567890"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StatePatientLookup {
		t.Errorf("expected PATIENT_LOOKUP, got %s", result.NextState)
	}
	if result.UpdateCtx["patient_doc"] != "1234567890" {
		t.Error("expected patient_doc in context")
	}
}

func TestAskDocument_ValidFiveDigits(t *testing.T) {
	m := sm.NewMachine()
	registerAskDocumentConfig(m)

	sess := testSess(sm.StateAskDocument)
	result, _ := m.Process(context.Background(), sess, textM("12345"))
	if result.NextState != sm.StatePatientLookup {
		t.Errorf("expected PATIENT_LOOKUP for 5 digits, got %s", result.NextState)
	}
}

func TestAskDocument_ValidFifteenDigits(t *testing.T) {
	m := sm.NewMachine()
	registerAskDocumentConfig(m)

	sess := testSess(sm.StateAskDocument)
	result, _ := m.Process(context.Background(), sess, textM("123456789012345"))
	if result.NextState != sm.StatePatientLookup {
		t.Errorf("expected PATIENT_LOOKUP for 15 digits, got %s", result.NextState)
	}
}

func TestAskDocument_TooShort(t *testing.T) {
	m := sm.NewMachine()
	registerAskDocumentConfig(m)

	sess := testSess(sm.StateAskDocument)
	result, _ := m.Process(context.Background(), sess, textM("1234"))
	if result.NextState != sm.StateAskDocument {
		t.Errorf("expected ASK_DOCUMENT (retry), got %s", result.NextState)
	}
}

func TestAskDocument_Letters(t *testing.T) {
	m := sm.NewMachine()
	registerAskDocumentConfig(m)

	sess := testSess(sm.StateAskDocument)
	result, _ := m.Process(context.Background(), sess, textM("abc12345"))
	if result.NextState != sm.StateAskDocument {
		t.Errorf("expected ASK_DOCUMENT (retry), got %s", result.NextState)
	}
}

func TestAskDocument_MaxRetries(t *testing.T) {
	m := sm.NewMachine()
	registerAskDocumentConfig(m)

	sess := testSess(sm.StateAskDocument)
	sess.RetryCount = 2 // next invalid will be 3 = maxRetries

	result, _ := m.Process(context.Background(), sess, textM("x"))
	if result.NextState != sm.StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT after max retries, got %s", result.NextState)
	}
}

// ==================== PatientLookup ====================

func TestPatientLookup_Found(t *testing.T) {
	patient := &domain.Patient{
		ID: "PAT001", DocumentNumber: "1234567890",
		FirstName: "Juan", FirstSurname: "Perez",
	}
	repo := &mockPatientRepo{
		findByDocumentFn: func(ctx context.Context, docType, doc string) (*domain.Patient, error) {
			return patient, nil
		},
	}
	svc := services.NewPatientService(repo)

	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, svc)

	sess := testSess(sm.StatePatientLookup)
	sess.Context["patient_doc"] = "1234567890"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmIdentity {
		t.Errorf("expected CONFIRM_IDENTITY, got %s", result.NextState)
	}
	if result.UpdateCtx["patient_id"] != "PAT001" {
		t.Error("expected patient_id in context")
	}
}

func TestPatientLookup_NotFound_Agendar(t *testing.T) {
	repo := &mockPatientRepo{} // returns nil
	svc := services.NewPatientService(repo)

	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, svc)

	sess := testSess(sm.StatePatientLookup)
	sess.Context["patient_doc"] = "9999999999"
	sess.Context["menu_option"] = "agendar"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegistrationStart {
		t.Errorf("expected REGISTRATION_START, got %s", result.NextState)
	}
}

func TestPatientLookup_NotFound_Consultar(t *testing.T) {
	repo := &mockPatientRepo{}
	svc := services.NewPatientService(repo)

	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, svc)

	sess := testSess(sm.StatePatientLookup)
	sess.Context["patient_doc"] = "9999999999"
	sess.Context["menu_option"] = "consultar"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

func TestPatientLookup_DBError(t *testing.T) {
	repo := &mockPatientRepo{
		findByDocumentFn: func(ctx context.Context, docType, doc string) (*domain.Patient, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	svc := services.NewPatientService(repo)

	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, svc)

	sess := testSess(sm.StatePatientLookup)
	sess.Context["patient_doc"] = "1234567890"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED on error, got %s", result.NextState)
	}
}

// ==================== ConfirmIdentity ====================

func TestConfirmIdentity_Yes_GoesToContactInfo(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmIdentity, confirmIdentityHandler())

	sess := testSess(sm.StateConfirmIdentity)
	sess.Context["menu_option"] = "consultar"
	sess.Context["patient_id"] = "PAT001"

	result, err := m.Process(context.Background(), sess, postbackM("identity_yes"))
	if err != nil {
		t.Fatal(err)
	}
	// Always routes to SHOW_CONTACT_INFO first (regardless of menu_option)
	if result.NextState != sm.StateShowContactInfo {
		t.Errorf("expected SHOW_CONTACT_INFO, got %s", result.NextState)
	}
}

func TestConfirmIdentity_Yes_Agendar_GoesToContactInfo(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmIdentity, confirmIdentityHandler())

	sess := testSess(sm.StateConfirmIdentity)
	sess.Context["menu_option"] = "agendar"
	sess.Context["patient_id"] = "PAT001"

	result, err := m.Process(context.Background(), sess, postbackM("identity_yes"))
	if err != nil {
		t.Fatal(err)
	}
	// Always routes to SHOW_CONTACT_INFO first
	if result.NextState != sm.StateShowContactInfo {
		t.Errorf("expected SHOW_CONTACT_INFO, got %s", result.NextState)
	}
}

func TestConfirmIdentity_No(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmIdentity, confirmIdentityHandler())

	sess := testSess(sm.StateConfirmIdentity)
	result, err := m.Process(context.Background(), sess, postbackM("identity_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskDocument {
		t.Errorf("expected ASK_DOCUMENT, got %s", result.NextState)
	}
	if len(result.ClearCtx) == 0 {
		t.Error("expected patient context keys cleared")
	}
}

func TestConfirmIdentity_InvalidInput(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmIdentity, confirmIdentityHandler())

	sess := testSess(sm.StateConfirmIdentity)
	result, err := m.Process(context.Background(), sess, textM("maybe"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmIdentity {
		t.Errorf("expected CONFIRM_IDENTITY (retry), got %s", result.NextState)
	}
}

func TestConfirmIdentity_Yes_DefaultMenu(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmIdentity, confirmIdentityHandler())

	sess := testSess(sm.StateConfirmIdentity)
	sess.Context["patient_id"] = "PAT001"
	// No menu_option set → still goes to SHOW_CONTACT_INFO

	result, err := m.Process(context.Background(), sess, postbackM("identity_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateShowContactInfo {
		t.Errorf("expected SHOW_CONTACT_INFO, got %s", result.NextState)
	}
}

// ==================== ShowContactInfo ====================

func TestShowContactInfo_BothPresent(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowContactInfo, showContactInfoHandler())

	sess := testSess(sm.StateShowContactInfo)
	sess.Context["patient_phone"] = "3103343616"
	sess.Context["patient_email"] = "juan@email.com"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmContactInfo {
		t.Errorf("expected CONFIRM_CONTACT_INFO, got %s", result.NextState)
	}
}

func TestShowContactInfo_PhoneMissing(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowContactInfo, showContactInfoHandler())

	sess := testSess(sm.StateShowContactInfo)
	sess.Context["patient_phone"] = ""
	sess.Context["patient_email"] = "juan@email.com"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskUpdatePhone {
		t.Errorf("expected ASK_UPDATE_PHONE, got %s", result.NextState)
	}
}

func TestShowContactInfo_EmailMissing(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowContactInfo, showContactInfoHandler())

	sess := testSess(sm.StateShowContactInfo)
	sess.Context["patient_phone"] = "3103343616"
	sess.Context["patient_email"] = ""

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskUpdateEmail {
		t.Errorf("expected ASK_UPDATE_EMAIL, got %s", result.NextState)
	}
	if result.UpdateCtx["contact_new_phone"] != "+573103343616" {
		t.Errorf("expected parsed phone in contact_new_phone, got %q", result.UpdateCtx["contact_new_phone"])
	}
}

func TestShowContactInfo_InvalidPhone(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowContactInfo, showContactInfoHandler())

	sess := testSess(sm.StateShowContactInfo)
	sess.Context["patient_phone"] = "123" // not a valid colombian phone
	sess.Context["patient_email"] = "juan@email.com"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskUpdatePhone {
		t.Errorf("expected ASK_UPDATE_PHONE for invalid phone, got %s", result.NextState)
	}
}

func TestShowContactInfo_NullEmail(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateShowContactInfo, showContactInfoHandler())

	sess := testSess(sm.StateShowContactInfo)
	sess.Context["patient_phone"] = "3103343616"
	sess.Context["patient_email"] = "null"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskUpdateEmail {
		t.Errorf("expected ASK_UPDATE_EMAIL for null email, got %s", result.NextState)
	}
}

// ==================== ConfirmContactInfo ====================

func TestConfirmContactInfo_Ok_Consultar(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmContactInfo, confirmContactInfoHandler(nil))

	sess := testSess(sm.StateConfirmContactInfo)
	sess.Context["menu_option"] = "consultar"

	result, err := m.Process(context.Background(), sess, postbackM("contact_ok"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateFetchAppointments {
		t.Errorf("expected FETCH_APPOINTMENTS, got %s", result.NextState)
	}
}

func TestConfirmContactInfo_Ok_Agendar(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmContactInfo, confirmContactInfoHandler(nil))

	sess := testSess(sm.StateConfirmContactInfo)
	sess.Context["menu_option"] = "agendar"

	result, err := m.Process(context.Background(), sess, postbackM("contact_ok"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskMedicalOrder {
		t.Errorf("expected ASK_MEDICAL_ORDER, got %s", result.NextState)
	}
}

func TestConfirmContactInfo_Update(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmContactInfo, confirmContactInfoHandler(nil))

	sess := testSess(sm.StateConfirmContactInfo)
	result, err := m.Process(context.Background(), sess, postbackM("contact_update"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskUpdatePhone {
		t.Errorf("expected ASK_UPDATE_PHONE, got %s", result.NextState)
	}
}

// ==================== AskUpdatePhone ====================

func TestAskUpdatePhone_Valid(t *testing.T) {
	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, services.NewPatientService(&mockPatientRepo{}))

	sess := testSess(sm.StateAskUpdatePhone)
	result, err := m.Process(context.Background(), sess, textM("3209302716"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskUpdateEmail {
		t.Errorf("expected ASK_UPDATE_EMAIL, got %s", result.NextState)
	}
	if result.UpdateCtx["contact_new_phone"] != "+573209302716" {
		t.Errorf("expected +573209302716, got %q", result.UpdateCtx["contact_new_phone"])
	}
}

func TestAskUpdatePhone_Invalid(t *testing.T) {
	m := sm.NewMachine()
	RegisterIdentificationHandlers(m, services.NewPatientService(&mockPatientRepo{}))

	sess := testSess(sm.StateAskUpdatePhone)
	result, err := m.Process(context.Background(), sess, textM("123"))
	if err != nil {
		t.Fatal(err)
	}
	// Should stay on same state (retry via RegisterWithConfig validation)
	if result.NextState != sm.StateAskUpdatePhone {
		t.Errorf("expected ASK_UPDATE_PHONE (retry), got %s", result.NextState)
	}
}

// ==================== AskUpdateEmail ====================

func TestAskUpdateEmail_Valid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskUpdateEmail, askUpdateEmailHandler())

	sess := testSess(sm.StateAskUpdateEmail)
	result, err := m.Process(context.Background(), sess, textM("nuevo@email.com"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateUpdateContactInfo {
		t.Errorf("expected UPDATE_CONTACT_INFO, got %s", result.NextState)
	}
	if result.UpdateCtx["contact_new_email"] != "nuevo@email.com" {
		t.Errorf("expected email saved, got %q", result.UpdateCtx["contact_new_email"])
	}
}

func TestAskUpdateEmail_NA(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskUpdateEmail, askUpdateEmailHandler())

	sess := testSess(sm.StateAskUpdateEmail)
	result, err := m.Process(context.Background(), sess, textM("NA"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateUpdateContactInfo {
		t.Errorf("expected UPDATE_CONTACT_INFO, got %s", result.NextState)
	}
	if v, ok := result.UpdateCtx["contact_new_email"]; !ok || v != "" {
		t.Errorf("expected empty email for NA, got %q", v)
	}
}

func TestAskUpdateEmail_Invalid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskUpdateEmail, askUpdateEmailHandler())

	sess := testSess(sm.StateAskUpdateEmail)
	result, err := m.Process(context.Background(), sess, textM("notanemail"))
	if err != nil {
		t.Fatal(err)
	}
	// Should retry on same state
	if result.NextState != sm.StateAskUpdateEmail {
		t.Errorf("expected ASK_UPDATE_EMAIL (retry), got %s", result.NextState)
	}
}

// ==================== UpdateContactInfo ====================

func TestUpdateContactInfo_Success(t *testing.T) {
	var updatedPhone, updatedEmail string
	repo := &mockPatientRepo{
		updateContactInfoFn: func(ctx context.Context, patientID, phone, email string) error {
			updatedPhone = phone
			updatedEmail = email
			return nil
		},
	}
	svc := services.NewPatientService(repo)

	m := sm.NewMachine()
	m.Register(sm.StateUpdateContactInfo, updateContactInfoHandler(svc))

	sess := testSess(sm.StateUpdateContactInfo)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["contact_new_phone"] = "+573209302716"
	sess.Context["contact_new_email"] = "nuevo@email.com"
	sess.Context["menu_option"] = "consultar"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateFetchAppointments {
		t.Errorf("expected FETCH_APPOINTMENTS, got %s", result.NextState)
	}
	if updatedPhone != "+573209302716" {
		t.Errorf("expected phone +573209302716, got %q", updatedPhone)
	}
	if updatedEmail != "nuevo@email.com" {
		t.Errorf("expected email nuevo@email.com, got %q", updatedEmail)
	}
}

func TestUpdateContactInfo_DBError_ContinuesFlow(t *testing.T) {
	repo := &mockPatientRepo{
		updateContactInfoFn: func(ctx context.Context, patientID, phone, email string) error {
			return fmt.Errorf("connection refused")
		},
	}
	svc := services.NewPatientService(repo)

	m := sm.NewMachine()
	m.Register(sm.StateUpdateContactInfo, updateContactInfoHandler(svc))

	sess := testSess(sm.StateUpdateContactInfo)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["contact_new_phone"] = "+573209302716"
	sess.Context["contact_new_email"] = "nuevo@email.com"
	sess.Context["menu_option"] = "agendar"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	// Should continue flow even on error
	if result.NextState != sm.StateAskMedicalOrder {
		t.Errorf("expected ASK_MEDICAL_ORDER (continue on error), got %s", result.NextState)
	}
}

// TestConfirmIdentity_InvalidInput_RerendersButtons (L7): un input inválido re-muestra los botones
// Sí/No, no solo un texto.
func TestConfirmIdentity_InvalidInput_RerendersButtons(t *testing.T) {
	h := confirmIdentityHandler()
	sess := testSess(sm.StateConfirmIdentity)
	sess.Context["patient_name"] = "Juan Perez"
	sess.Context["patient_doc"] = "123"

	res, err := h(context.Background(), sess, textM("no entiendo"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateConfirmIdentity {
		t.Errorf("esperaba retry en CONFIRM_IDENTITY, got %s", res.NextState)
	}
	if len(res.Messages) == 0 {
		t.Fatal("esperaba un mensaje con botones")
	}
	if btn, ok := res.Messages[0].(*sm.ButtonMessage); !ok || len(btn.Buttons) != 2 {
		t.Errorf("esperaba ButtonMessage con 2 botones, got %T", res.Messages[0])
	}
}

// TestConfirmContactInfo_InvalidInput_RerendersButtons (L7).
func TestConfirmContactInfo_InvalidInput_RerendersButtons(t *testing.T) {
	h := confirmContactInfoHandler(nil)
	sess := testSess(sm.StateConfirmContactInfo)
	sess.Context["patient_phone"] = "+573001234567"
	sess.Context["patient_email"] = "x@y.com"

	res, err := h(context.Background(), sess, textM("???"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateConfirmContactInfo {
		t.Errorf("esperaba retry en CONFIRM_CONTACT_INFO, got %s", res.NextState)
	}
	if len(res.Messages) == 0 {
		t.Fatal("esperaba un mensaje con botones")
	}
	if btn, ok := res.Messages[0].(*sm.ButtonMessage); !ok || len(btn.Buttons) != 2 {
		t.Errorf("esperaba ButtonMessage con 2 botones, got %T", res.Messages[0])
	}
}
