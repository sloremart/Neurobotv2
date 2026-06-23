package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

func registerRegistrationStartConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateRegistrationStart, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"register_yes", "register_no"},
		Handler:   registrationStartHandler(),
	})
}

func TestRegistrationStart_Yes(t *testing.T) {
	m := sm.NewMachine()
	registerRegistrationStartConfig(m)

	sess := testSess(sm.StateRegistrationStart)
	result, err := m.Process(context.Background(), sess, postbackM("register_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegFirstSurname {
		t.Errorf("expected REG_FIRST_SURNAME (doc type + issue place removed), got %s", result.NextState)
	}
}

func TestRegistrationStart_No(t *testing.T) {
	m := sm.NewMachine()
	registerRegistrationStartConfig(m)

	sess := testSess(sm.StateRegistrationStart)
	result, err := m.Process(context.Background(), sess, postbackM("register_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

func registerDocumentTypeConfig(m *sm.Machine) {
	// Doc type is now a numbered text menu (12 catalog types), not an interactive button list.
	m.Register(sm.StateRegDocumentType, regDocumentTypeHandler())
}

func TestRegDocumentType_Valid(t *testing.T) {
	m := sm.NewMachine()
	registerDocumentTypeConfig(m)

	sess := testSess(sm.StateRegDocumentType)
	result, err := m.Process(context.Background(), sess, textM("CC"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegFirstSurname {
		t.Errorf("expected REG_FIRST_SURNAME, got %s", result.NextState)
	}
}

func TestRegDocumentType_Invalid(t *testing.T) {
	m := sm.NewMachine()
	registerDocumentTypeConfig(m)

	sess := testSess(sm.StateRegDocumentType)
	result, err := m.Process(context.Background(), sess, textM("XX"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegDocumentType {
		t.Errorf("expected REG_DOCUMENT_TYPE (retry), got %s", result.NextState)
	}
}

func TestRegFirstSurname_Valid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegFirstSurname, regFieldHandler("reg_first_surname", "Ingresa tu primer apellido:", validateName, sm.StateRegSecondSurname, ""))

	sess := testSess(sm.StateRegFirstSurname)
	result, err := m.Process(context.Background(), sess, textM("Garcia"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegSecondSurname {
		t.Errorf("expected REG_SECOND_SURNAME, got %s", result.NextState)
	}
}

func TestRegFirstSurname_Invalid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegFirstSurname, regFieldHandler("reg_first_surname", "Ingresa tu primer apellido:", validateName, sm.StateRegSecondSurname, ""))

	sess := testSess(sm.StateRegFirstSurname)
	result, err := m.Process(context.Background(), sess, textM("123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegFirstSurname {
		t.Errorf("expected REG_FIRST_SURNAME (retry), got %s", result.NextState)
	}
}

func TestRegBirthDate_Valid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegBirthDate, regBirthDateHandler())

	sess := testSess(sm.StateRegBirthDate)
	result, err := m.Process(context.Background(), sess, textM("1990-03-15"))
	if err != nil {
		t.Fatal(err)
	}
	// Birth place was removed (no SIESA column); birth date now goes straight to gender.
	if result.NextState != sm.StateRegGender {
		t.Errorf("expected REG_GENDER, got %s", result.NextState)
	}
}

func TestRegBirthDate_Future(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegBirthDate, regBirthDateHandler())

	sess := testSess(sm.StateRegBirthDate)
	result, err := m.Process(context.Background(), sess, textM("15/03/2030"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegBirthDate {
		t.Errorf("expected REG_BIRTH_DATE (retry), got %s", result.NextState)
	}
}

func TestRegBirthDate_InvalidFormat(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegBirthDate, regBirthDateHandler())

	sess := testSess(sm.StateRegBirthDate)
	result, err := m.Process(context.Background(), sess, textM("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegBirthDate {
		t.Errorf("expected REG_BIRTH_DATE (retry), got %s", result.NextState)
	}
}

func TestRegPhone_Valid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegPhone, regPhoneHandler())

	sess := testSess(sm.StateRegPhone)
	result, err := m.Process(context.Background(), sess, textM("3001234567"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegPhone2 {
		t.Errorf("expected REG_PHONE2, got %s", result.NextState)
	}
}

func TestRegPhone_Invalid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegPhone, regPhoneHandler())

	sess := testSess(sm.StateRegPhone)
	result, err := m.Process(context.Background(), sess, textM("12345"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegPhone {
		t.Errorf("expected REG_PHONE (retry), got %s", result.NextState)
	}
}

func registerConfirmRegistrationConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateConfirmRegistration, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"reg_confirm", "reg_correct"},
		Handler:   confirmRegistrationHandler(),
	})
}

func TestConfirmRegistration_Confirm(t *testing.T) {
	m := sm.NewMachine()
	registerConfirmRegistrationConfig(m)

	sess := testSess(sm.StateConfirmRegistration)
	sess.Context["reg_document_type"] = "CC"
	sess.Context["patient_doc"] = "1234567890"
	sess.Context["reg_first_name"] = "JUAN"
	sess.Context["reg_second_name"] = ""
	sess.Context["reg_first_surname"] = "GARCIA"
	sess.Context["reg_second_surname"] = "LOPEZ"
	sess.Context["reg_birth_date"] = "1990-03-15"
	sess.Context["patient_age"] = "35"
	sess.Context["reg_gender"] = "M"
	sess.Context["reg_marital_status"] = "1"
	sess.Context["reg_address"] = "CRA 10 #20-30"
	sess.Context["reg_phone"] = "3001234567"
	sess.Context["reg_email"] = "juan@test.com"
	sess.Context["reg_occupation"] = "INGENIERO"
	sess.Context["reg_municipality"] = "11001"
	sess.Context["reg_entity"] = "EPS001"

	result, err := m.Process(context.Background(), sess, postbackM("reg_confirm"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCreatePatient {
		t.Errorf("expected CREATE_PATIENT, got %s", result.NextState)
	}
}

func TestConfirmRegistration_Correct(t *testing.T) {
	m := sm.NewMachine()
	registerConfirmRegistrationConfig(m)

	sess := testSess(sm.StateConfirmRegistration)
	result, err := m.Process(context.Background(), sess, postbackM("reg_correct"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegSelectCorrection {
		t.Errorf("expected REG_SELECT_CORRECTION, got %s", result.NextState)
	}
}

func TestCreatePatient_Success(t *testing.T) {
	patientRepo := &testutil.MockPatientRepo{
		CreateFn: func(ctx context.Context, input domain.CreatePatientInput) (string, error) {
			return "PAT-NEW-001", nil
		},
	}
	patientSvc := services.NewPatientService(patientRepo)

	m := sm.NewMachine()
	m.Register(sm.StateCreatePatient, createPatientHandler(patientSvc))

	sess := testSess(sm.StateCreatePatient)
	sess.Context["reg_document_type"] = "CC"
	sess.Context["patient_doc"] = "1234567890"
	sess.Context["reg_first_name"] = "JUAN"
	sess.Context["reg_second_name"] = ""
	sess.Context["reg_first_surname"] = "GARCIA"
	sess.Context["reg_second_surname"] = "LOPEZ"
	sess.Context["reg_birth_date"] = "1990-03-15"
	sess.Context["reg_gender"] = "M"
	sess.Context["reg_phone"] = "3001234567"
	sess.Context["reg_email"] = "juan@test.com"
	sess.Context["reg_address"] = "CRA 10"
	sess.Context["reg_municipality"] = "11001"
	sess.Context["reg_zone"] = "U"
	sess.Context["reg_entity"] = "EPS001"
	sess.Context["reg_affiliation_type"] = "C"
	sess.Context["reg_user_type"] = "1"
	sess.Context["reg_occupation"] = "ING"
	sess.Context["reg_marital_status"] = "1"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskMedicalOrder {
		t.Errorf("expected ASK_MEDICAL_ORDER, got %s", result.NextState)
	}
}

func TestCreatePatient_Error(t *testing.T) {
	patientRepo := &testutil.MockPatientRepo{
		CreateFn: func(ctx context.Context, input domain.CreatePatientInput) (string, error) {
			return "", context.DeadlineExceeded
		},
	}
	patientSvc := services.NewPatientService(patientRepo)

	m := sm.NewMachine()
	m.Register(sm.StateCreatePatient, createPatientHandler(patientSvc))

	sess := testSess(sm.StateCreatePatient)
	sess.Context["reg_birth_date"] = "1990-03-15"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED on error, got %s", result.NextState)
	}
}

// --- Mock types for municipality and entity repositories ---

type mockMunicipalityRepo struct {
	searchFn func(ctx context.Context, query string) ([]domain.Municipality, error)
}

func (m *mockMunicipalityRepo) Search(ctx context.Context, query string) ([]domain.Municipality, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, query)
	}
	return nil, nil
}

func (m *mockMunicipalityRepo) SearchBarrios(ctx context.Context, name, depCode, muniCode string) ([]domain.Barrio, error) {
	return nil, nil
}

// =============================================================================
// Tests for regOptionalFieldHandler (second surname / second name)
// =============================================================================

func TestRegOptionalField_NoTengo(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegSecondSurname, regOptionalFieldHandler("reg_second_surname", "Ingresa tu *segundo apellido* (o escribe \"no tengo\"):", sm.StateRegFirstName, ""))

	sess := testSess(sm.StateRegSecondSurname)
	result, err := m.Process(context.Background(), sess, textM("no tengo"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegFirstName {
		t.Errorf("expected REG_FIRST_NAME, got %s", result.NextState)
	}
	if v, ok := result.UpdateCtx["reg_second_surname"]; !ok || v != "" {
		t.Errorf("expected reg_second_surname to be empty, got %q", v)
	}
}

func TestRegOptionalField_ValidName(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegSecondSurname, regOptionalFieldHandler("reg_second_surname", "Ingresa tu *segundo apellido* (o escribe \"no tengo\"):", sm.StateRegFirstName, ""))

	sess := testSess(sm.StateRegSecondSurname)
	result, err := m.Process(context.Background(), sess, textM("Garcia"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegFirstName {
		t.Errorf("expected REG_FIRST_NAME, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_second_surname"]; v != "GARCIA" {
		t.Errorf("expected reg_second_surname=GARCIA, got %q", v)
	}
}

func TestRegOptionalField_Invalid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegSecondSurname, regOptionalFieldHandler("reg_second_surname", "Ingresa tu *segundo apellido* (o escribe \"no tengo\"):", sm.StateRegFirstName, ""))

	sess := testSess(sm.StateRegSecondSurname)
	result, err := m.Process(context.Background(), sess, textM("123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegSecondSurname {
		t.Errorf("expected REG_SECOND_SURNAME (retry), got %s", result.NextState)
	}
}

// =============================================================================
// Tests for regGenderHandler
// =============================================================================

func registerGenderConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateRegGender, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"M", "F"},
		Handler:   regGenderHandler(),
	})
}

func TestRegGender_Male(t *testing.T) {
	m := sm.NewMachine()
	registerGenderConfig(m)

	sess := testSess(sm.StateRegGender)
	result, err := m.Process(context.Background(), sess, postbackM("M"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegBloodType {
		t.Errorf("expected REG_BLOOD_TYPE, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_gender"]; v != "M" {
		t.Errorf("expected reg_gender=M, got %q", v)
	}
	if v := result.UpdateCtx["patient_gender"]; v != "M" {
		t.Errorf("expected patient_gender=M, got %q", v)
	}
}

func TestRegGender_Female(t *testing.T) {
	m := sm.NewMachine()
	registerGenderConfig(m)

	sess := testSess(sm.StateRegGender)
	result, err := m.Process(context.Background(), sess, postbackM("F"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegBloodType {
		t.Errorf("expected REG_BLOOD_TYPE, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_gender"]; v != "F" {
		t.Errorf("expected reg_gender=F, got %q", v)
	}
}

func TestRegGender_Invalid(t *testing.T) {
	m := sm.NewMachine()
	registerGenderConfig(m)

	sess := testSess(sm.StateRegGender)
	result, err := m.Process(context.Background(), sess, textM("X"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegGender {
		t.Errorf("expected REG_GENDER (retry), got %s", result.NextState)
	}
}

// =============================================================================
// Tests for regMaritalStatusHandler
// =============================================================================

func registerMaritalStatusConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateRegMaritalStatus, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"1", "2", "3", "4", "5", "6"},
		Handler:   regMaritalStatusHandler(),
	})
}

func TestRegMaritalStatus_Valid(t *testing.T) {
	m := sm.NewMachine()
	registerMaritalStatusConfig(m)

	sess := testSess(sm.StateRegMaritalStatus)
	result, err := m.Process(context.Background(), sess, postbackM("1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegPhone {
		t.Errorf("expected REG_PHONE, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_marital_status"]; v != "1" {
		t.Errorf("expected reg_marital_status=1, got %q", v)
	}
}

func TestRegMaritalStatus_Casado(t *testing.T) {
	m := sm.NewMachine()
	registerMaritalStatusConfig(m)

	sess := testSess(sm.StateRegMaritalStatus)
	result, err := m.Process(context.Background(), sess, postbackM("2"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegPhone {
		t.Errorf("expected REG_PHONE, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_marital_status"]; v != "2" {
		t.Errorf("expected reg_marital_status=2, got %q", v)
	}
}

func TestRegMaritalStatus_Invalid(t *testing.T) {
	m := sm.NewMachine()
	registerMaritalStatusConfig(m)

	sess := testSess(sm.StateRegMaritalStatus)
	result, err := m.Process(context.Background(), sess, textM("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegMaritalStatus {
		t.Errorf("expected REG_MARITAL_STATUS (retry), got %s", result.NextState)
	}
}

// =============================================================================
// Tests for regEmailHandler
// =============================================================================

func TestRegEmail_Valid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegEmail, regEmailHandler())

	sess := testSess(sm.StateRegEmail)
	result, err := m.Process(context.Background(), sess, textM("test@test.com"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegUserType {
		t.Errorf("expected REG_USER_TYPE (affiliation group after personal), got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_email"]; v != "test@test.com" {
		t.Errorf("expected reg_email=test@test.com, got %q", v)
	}
}

func TestRegEmail_NoTengo(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegEmail, regEmailHandler())

	sess := testSess(sm.StateRegEmail)
	result, err := m.Process(context.Background(), sess, textM("no tengo"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegUserType {
		t.Errorf("expected REG_USER_TYPE (affiliation group after personal), got %s", result.NextState)
	}
	if v, ok := result.UpdateCtx["reg_email"]; !ok || v != "" {
		t.Errorf("expected reg_email to be empty, got %q", v)
	}
}

func TestRegEmail_Invalid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegEmail, regEmailHandler())

	sess := testSess(sm.StateRegEmail)
	result, err := m.Process(context.Background(), sess, textM("notanemail"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegEmail {
		t.Errorf("expected REG_EMAIL (retry), got %s", result.NextState)
	}
}

// =============================================================================
// Tests for regOptionalPhoneHandler
// =============================================================================

func TestRegOptionalPhone_NoTengo(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegPhone2, regOptionalPhoneHandler())

	sess := testSess(sm.StateRegPhone2)
	result, err := m.Process(context.Background(), sess, textM("no tengo"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegEmail {
		t.Errorf("expected REG_EMAIL, got %s", result.NextState)
	}
	if v, ok := result.UpdateCtx["reg_phone2"]; !ok || v != "" {
		t.Errorf("expected reg_phone2 to be empty, got %q", v)
	}
}

func TestRegOptionalPhone_ValidPhone(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegPhone2, regOptionalPhoneHandler())

	sess := testSess(sm.StateRegPhone2)
	result, err := m.Process(context.Background(), sess, textM("3001234567"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegEmail {
		t.Errorf("expected REG_EMAIL, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_phone2"]; v == "" {
		t.Error("expected reg_phone2 to be non-empty for valid phone")
	}
}

func TestRegOptionalPhone_InvalidPhone(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegPhone2, regOptionalPhoneHandler())

	sess := testSess(sm.StateRegPhone2)
	result, err := m.Process(context.Background(), sess, textM("12345"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegEmail {
		t.Errorf("expected REG_EMAIL (optional, saves empty), got %s", result.NextState)
	}
	if v, ok := result.UpdateCtx["reg_phone2"]; !ok || v != "" {
		t.Errorf("expected reg_phone2 to be empty for invalid phone, got %q", v)
	}
}

// =============================================================================
// Tests for regMunicipalityHandler
// =============================================================================

func TestRegMunicipality_Postback(t *testing.T) {
	repo := &mockMunicipalityRepo{}
	m := sm.NewMachine()
	m.Register(sm.StateRegMunicipality, regMunicipalityHandler(repo))

	sess := testSess(sm.StateRegMunicipality)
	result, err := m.Process(context.Background(), sess, postbackM("11001"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegZone {
		t.Errorf("expected REG_ZONE, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_municipality"]; v != "11001" {
		t.Errorf("expected reg_municipality=11001, got %q", v)
	}
}

func TestRegMunicipality_SingleResult(t *testing.T) {
	repo := &mockMunicipalityRepo{
		searchFn: func(ctx context.Context, query string) ([]domain.Municipality, error) {
			return []domain.Municipality{
				{MunicipalityCode: "11001", MunicipalityName: "Bogotá D.C.", DepartmentName: "Bogotá"},
			}, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateRegMunicipality, regMunicipalityHandler(repo))

	sess := testSess(sm.StateRegMunicipality)
	result, err := m.Process(context.Background(), sess, textM("Bogotá"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegZone {
		t.Errorf("expected REG_ZONE, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_municipality"]; v != "11001" {
		t.Errorf("expected reg_municipality=11001, got %q", v)
	}
}

func TestRegMunicipality_MultipleResults(t *testing.T) {
	repo := &mockMunicipalityRepo{
		searchFn: func(ctx context.Context, query string) ([]domain.Municipality, error) {
			return []domain.Municipality{
				{MunicipalityCode: "50001", MunicipalityName: "Villavicencio", DepartmentName: "Meta"},
				{MunicipalityCode: "76001", MunicipalityName: "Villanueva", DepartmentName: "Casanare"},
				{MunicipalityCode: "68001", MunicipalityName: "Villahermosa", DepartmentName: "Tolima"},
			}, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateRegMunicipality, regMunicipalityHandler(repo))

	sess := testSess(sm.StateRegMunicipality)
	result, err := m.Process(context.Background(), sess, textM("Villa"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegMunicipality {
		t.Errorf("expected REG_MUNICIPALITY (stays, shows list), got %s", result.NextState)
	}
}

func TestRegMunicipality_TooMany(t *testing.T) {
	repo := &mockMunicipalityRepo{
		searchFn: func(ctx context.Context, query string) ([]domain.Municipality, error) {
			results := make([]domain.Municipality, 6)
			for i := range results {
				results[i] = domain.Municipality{
					MunicipalityCode: fmt.Sprintf("0%d", i),
					MunicipalityName: fmt.Sprintf("City%d", i),
					DepartmentName:   "Dept",
				}
			}
			return results, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateRegMunicipality, regMunicipalityHandler(repo))

	sess := testSess(sm.StateRegMunicipality)
	result, err := m.Process(context.Background(), sess, textM("a"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegMunicipality {
		t.Errorf("expected REG_MUNICIPALITY (too many results), got %s", result.NextState)
	}
}

func TestRegMunicipality_NoResults(t *testing.T) {
	repo := &mockMunicipalityRepo{
		searchFn: func(ctx context.Context, query string) ([]domain.Municipality, error) {
			return []domain.Municipality{}, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateRegMunicipality, regMunicipalityHandler(repo))

	sess := testSess(sm.StateRegMunicipality)
	result, err := m.Process(context.Background(), sess, textM("xyz"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegMunicipality {
		t.Errorf("expected REG_MUNICIPALITY (no results), got %s", result.NextState)
	}
}

// =============================================================================
// Tests for regZoneHandler
// =============================================================================

func registerZoneConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateRegZone, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"U", "R"},
		Handler:   regZoneHandler(),
	})
}

func TestRegZone_Urban(t *testing.T) {
	m := sm.NewMachine()
	registerZoneConfig(m)

	sess := testSess(sm.StateRegZone)
	result, err := m.Process(context.Background(), sess, postbackM("U"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegAddress {
		t.Errorf("expected REG_ADDRESS (address group), got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_zone"]; v != "U" {
		t.Errorf("expected reg_zone=U, got %q", v)
	}
}

// =============================================================================
// Tests for regUserTypeHandler
// =============================================================================

func registerUserTypeConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateRegUserType, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   userTypePayloads(),
		Handler:   regUserTypeHandler(),
	})
}

func TestRegUserType_Contributivo(t *testing.T) {
	m := sm.NewMachine()
	registerUserTypeConfig(m)

	sess := testSess(sm.StateRegUserType)
	result, err := m.Process(context.Background(), sess, postbackM("1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegAffiliationType {
		t.Errorf("expected REG_AFFILIATION_TYPE, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_user_type"]; v != "1" {
		t.Errorf("expected reg_user_type=1, got %q", v)
	}
}

// =============================================================================
// Tests for regAffiliationTypeHandler
// =============================================================================

func registerAffiliationTypeConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateRegAffiliationType, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"1", "2", "3", "4"},
		Handler:   regAffiliationTypeHandler(),
	})
}

func TestRegAffiliationType_Cotizante(t *testing.T) {
	m := sm.NewMachine()
	registerAffiliationTypeConfig(m)

	sess := testSess(sm.StateRegAffiliationType)
	result, err := m.Process(context.Background(), sess, postbackM("1"))
	if err != nil {
		t.Fatal(err)
	}
	// Affiliation now starts the address group → REG_MUNICIPALITY.
	if result.NextState != sm.StateRegMunicipality {
		t.Errorf("expected REG_MUNICIPALITY, got %s", result.NextState)
	}
	if v := result.UpdateCtx["reg_affiliation_type"]; v != "1" {
		t.Errorf("expected reg_affiliation_type=1, got %q", v)
	}
}

// =============================================================================
// Tests for formatGender and formatOptional helpers
// =============================================================================

func TestFormatGender(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"M", "Masculino"},
		{"F", "Femenino"},
		{"", ""},
	}
	for _, tc := range tests {
		got := formatGender(tc.input)
		if got != tc.expected {
			t.Errorf("formatGender(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestFormatOptional(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "No tiene"},
		{"value", "value"},
	}
	for _, tc := range tests {
		got := formatOptional(tc.input)
		if got != tc.expected {
			t.Errorf("formatOptional(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// =============================================================================
// Tests for regSelectCorrectionHandler
// =============================================================================

func registerSelectCorrectionConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateRegSelectCorrection, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   correctionPayloads(),
		Handler:   regSelectCorrectionHandler(),
	})
}

func TestRegSelectCorrection_FirstName(t *testing.T) {
	m := sm.NewMachine()
	registerSelectCorrectionConfig(m)

	sess := testSess(sm.StateRegSelectCorrection)
	result, err := m.Process(context.Background(), sess, postbackM("corr_first_name"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegFirstName {
		t.Errorf("expected REG_FIRST_NAME, got %s", result.NextState)
	}
	if result.UpdateCtx["reg_correction_mode"] != "true" {
		t.Error("expected reg_correction_mode=true")
	}
	if len(result.Messages) == 0 {
		t.Error("expected prompt message")
	}
}

func TestRegSelectCorrection_Restart(t *testing.T) {
	m := sm.NewMachine()
	registerSelectCorrectionConfig(m)

	sess := testSess(sm.StateRegSelectCorrection)
	result, err := m.Process(context.Background(), sess, postbackM("corr_restart"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegDocumentType {
		t.Errorf("expected REG_DOCUMENT_TYPE, got %s", result.NextState)
	}
	if v, ok := result.UpdateCtx["reg_correction_mode"]; ok && v == "true" {
		t.Error("restart should not set correction mode")
	}
}

func TestRegSelectCorrection_Invalid(t *testing.T) {
	m := sm.NewMachine()
	registerSelectCorrectionConfig(m)

	sess := testSess(sm.StateRegSelectCorrection)
	result, err := m.Process(context.Background(), sess, textM("invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegSelectCorrection {
		t.Errorf("expected REG_SELECT_CORRECTION (retry), got %s", result.NextState)
	}
}

// =============================================================================
// Tests for withCorrectionRedirect wrapper
// =============================================================================

func correctionTestSession() *session.Session {
	sess := testSess("")
	sess.Context["reg_correction_mode"] = "true"
	sess.Context["reg_document_type"] = "CC"
	sess.Context["patient_doc"] = "1234567890"
	sess.Context["reg_first_name"] = "JUAN"
	sess.Context["reg_second_name"] = ""
	sess.Context["reg_first_surname"] = "GARCIA"
	sess.Context["reg_second_surname"] = ""
	sess.Context["reg_birth_date"] = "1990-03-15"
	sess.Context["patient_age"] = "35"
	sess.Context["reg_gender"] = "M"
	sess.Context["reg_marital_status"] = "1"
	sess.Context["reg_address"] = "CRA 10"
	sess.Context["reg_phone"] = "3001234567"
	sess.Context["reg_email"] = ""
	sess.Context["reg_occupation"] = "ING"
	sess.Context["reg_municipality"] = "11001"
	sess.Context["reg_entity"] = "EPS001"
	sess.Context["reg_user_type"] = "1"
	sess.Context["reg_affiliation_type"] = "C"
	return sess
}

func TestCorrectionRedirect_FirstName(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegFirstName, withCorrectionRedirect(
		regFieldHandler("reg_first_name", "Primer nombre:", validateName, sm.StateRegSecondName, ""),
	))

	sess := correctionTestSession()
	sess.CurrentState = sm.StateRegFirstName

	result, err := m.Process(context.Background(), sess, textM("Pedro"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmRegistration {
		t.Errorf("expected CONFIRM_REGISTRATION, got %s", result.NextState)
	}
	if result.UpdateCtx["reg_first_name"] != "PEDRO" {
		t.Errorf("expected reg_first_name=PEDRO, got %q", result.UpdateCtx["reg_first_name"])
	}
}

func TestCorrectionRedirect_NoCorrectionMode(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegFirstName, withCorrectionRedirect(
		regFieldHandler("reg_first_name", "Primer nombre:", validateName, sm.StateRegSecondName, ""),
	))

	sess := testSess(sm.StateRegFirstName)
	result, err := m.Process(context.Background(), sess, textM("Pedro"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegSecondName {
		t.Errorf("expected REG_SECOND_NAME (normal flow), got %s", result.NextState)
	}
}

func TestCorrectionRedirect_ValidationFails(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegFirstName, withCorrectionRedirect(
		regFieldHandler("reg_first_name", "Primer nombre:", validateName, sm.StateRegSecondName, ""),
	))

	sess := correctionTestSession()
	sess.CurrentState = sm.StateRegFirstName

	result, err := m.Process(context.Background(), sess, textM("123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegFirstName {
		t.Errorf("expected REG_FIRST_NAME (retry), got %s", result.NextState)
	}
}

func TestCorrectionRedirect_DocumentType(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegDocumentType, withCorrectionRedirect(regDocumentTypeHandler()))

	sess := correctionTestSession()
	sess.CurrentState = sm.StateRegDocumentType

	result, err := m.Process(context.Background(), sess, textM("TI"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmRegistration {
		t.Errorf("expected CONFIRM_REGISTRATION, got %s", result.NextState)
	}
	if result.UpdateCtx["reg_document_type"] != "TI" {
		t.Errorf("expected reg_document_type=TI, got %q", result.UpdateCtx["reg_document_type"])
	}
}
