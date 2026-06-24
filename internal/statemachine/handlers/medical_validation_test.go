package handlers

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/validators"
)

// mockApptRepo for medical validation tests
type mockApptRepo struct {
	hasFutureForCupFn       func(ctx context.Context, pid, cup string) (bool, error)
	findLastDoctorForCupsFn func(ctx context.Context, pid string, cups []string) (string, error)
	countMonthlyByGroupFn   func(ctx context.Context, cups []string, year, month int) (int, error)
}

func (m *mockApptRepo) FindByID(ctx context.Context, id string) (*domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepo) FindUpcomingByPatient(ctx context.Context, pid string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepo) FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepo) Create(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
	return &domain.Appointment{ID: "new"}, nil
}
func (m *mockApptRepo) Confirm(ctx context.Context, id, channel, channelID string) error { return nil }
func (m *mockApptRepo) Cancel(ctx context.Context, id, reason, ch, chID string) error    { return nil }
func (m *mockApptRepo) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	return nil
}

func (m *mockApptRepo) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	return nil
}

func (m *mockApptRepo) DeleteBatch(ctx context.Context, ids []string) error {
	return nil
}

func (m *mockApptRepo) HasFutureForCup(ctx context.Context, pid, cup string) (bool, error) {
	if m.hasFutureForCupFn != nil {
		return m.hasFutureForCupFn(ctx, pid, cup)
	}
	return false, nil
}

func (m *mockApptRepo) FindLastDoctorForCups(ctx context.Context, pid string, cups []string) (string, error) {
	if m.findLastDoctorForCupsFn != nil {
		return m.findLastDoctorForCupsFn(ctx, pid, cups)
	}
	return "", nil
}

func (m *mockApptRepo) CountMonthlyByGroup(ctx context.Context, cups []string, year, month int) (int, error) {
	if m.countMonthlyByGroupFn != nil {
		return m.countMonthlyByGroupFn(ctx, cups, year, month)
	}
	return 0, nil
}

func (m *mockApptRepo) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	return nil, nil
}

func (m *mockApptRepo) RescheduleDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (int, error) {
	return 0, nil
}

func (m *mockApptRepo) SlotCountForAppointment(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *mockApptRepo) WriteCreationAudit(_ context.Context, _, _ string) {}

func (m *mockApptRepo) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	return nil
}

func (m *mockApptRepo) CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error {
	return nil
}

// ==================== AskContrasted ====================

func TestAskContrasted_NotContrastable_Skip(t *testing.T) {
	gfrSvc := services.NewGFRService()
	apptSvc := services.NewAppointmentService(&mockApptRepo{}, nil)

	m := sm.NewMachine()
	RegisterMedicalValidationHandlers(m, gfrSvc, apptSvc)

	sess := testSess(sm.StateAskContrasted)
	sess.Context["cups_code"] = "890271" // Not a contrastable CUPS

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	// Non-contrastable → auto-chains through sedation skip → CHECK_EXISTING → ...
	// Since all are automatic, it chains until interactive or terminal
	if result.NextState == sm.StateAskContrasted {
		t.Error("expected to move past ASK_CONTRASTED for non-contrastable CUPS")
	}
}

func TestAskContrasted_Yes(t *testing.T) {
	// Only register the single handler to avoid auto-chain
	m := sm.NewMachine()
	m.Register(sm.StateAskContrasted, askContrastedHandler())

	sess := testSess(sm.StateAskContrasted)
	sess.Context["cups_code"] = "883533" // Contrastable CUPS (resonancia)
	sess.Context["patient_gender"] = "M"
	sess.Context["patient_age"] = "30"

	// Step 1: First invocation — no _prompted_contrast → sends buttons, stays in ASK_CONTRASTED
	result1, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result1.NextState != sm.StateAskContrasted {
		t.Errorf("step1: expected ASK_CONTRASTED (prompt), got %s", result1.NextState)
	}
	if result1.UpdateCtx == nil || result1.UpdateCtx["_prompted_contrast"] != "1" {
		t.Errorf("step1: expected _prompted_contrast=1, got %v", result1.UpdateCtx)
	}

	// Apply context updates from step 1 to session
	for k, v := range result1.UpdateCtx {
		sess.Context[k] = v
	}

	// Step 2: Second invocation — _prompted_contrast set, user selects contrast_yes
	result2, err := m.Process(context.Background(), sess, postbackM("contrast_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result2.UpdateCtx == nil || result2.UpdateCtx["is_contrasted"] != "1" {
		t.Errorf("step2: expected is_contrasted=1, got %v", result2.UpdateCtx)
	}
	if result2.NextState != sm.StateGfrCreatinine {
		t.Errorf("step2: expected GFR_CREATININE for male, got %s", result2.NextState)
	}
}

// ==================== AskPregnancy ====================

func TestAskPregnancy_MaleSkip(t *testing.T) {
	gfrSvc := services.NewGFRService()
	apptSvc := services.NewAppointmentService(&mockApptRepo{}, nil)

	m := sm.NewMachine()
	RegisterMedicalValidationHandlers(m, gfrSvc, apptSvc)

	sess := testSess(sm.StateAskPregnancy)
	sess.Context["patient_gender"] = "M"
	sess.Context["patient_age"] = "30"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	// Male should skip pregnancy check - but ASK_PREGNANCY is interactive,
	// so ValidateButtonResponse with text "any" returns retry
	if result.NextState == sm.StatePregnancyBlock {
		t.Error("male should not be blocked by pregnancy")
	}
}

// ==================== GfrCreatinine ====================

func TestGfrCreatinine_ValidInput(t *testing.T) {
	// Only register the single handler to avoid auto-chain
	m := sm.NewMachine()
	m.Register(sm.StateGfrCreatinine, gfrCreatinineHandler())

	sess := testSess(sm.StateGfrCreatinine)
	sess.Context["patient_age"] = "50" // >= 40 → goes to GFR_WEIGHT

	result, err := m.Process(context.Background(), sess, textM("1.2"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_creatinine"] != "1.20" {
		t.Errorf("expected gfr_creatinine=1.20, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrWeight {
		t.Errorf("expected GFR_WEIGHT for age 50, got %s", result.NextState)
	}
}

func TestGfrCreatinine_InvalidInput(t *testing.T) {
	gfrSvc := services.NewGFRService()
	apptSvc := services.NewAppointmentService(&mockApptRepo{}, nil)

	m := sm.NewMachine()
	RegisterMedicalValidationHandlers(m, gfrSvc, apptSvc)

	sess := testSess(sm.StateGfrCreatinine)
	sess.Context["patient_age"] = "50"

	result, err := m.Process(context.Background(), sess, textM("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateGfrCreatinine {
		t.Errorf("expected retry at GFR_CREATININE, got %s", result.NextState)
	}
}

// ==================== GfrResult ====================

func TestGfrResult_Above60(t *testing.T) {
	gfrSvc := services.NewGFRService()

	// Only register the single handler to avoid auto-chain
	m := sm.NewMachine()
	m.Register(sm.StateGfrResult, gfrResultHandler(gfrSvc))

	sess := testSess(sm.StateGfrResult)
	sess.Context["patient_age"] = "50"
	sess.Context["patient_gender"] = "M"
	sess.Context["gfr_creatinine"] = "0.5"
	sess.Context["gfr_weight_kg"] = "80"
	sess.Context["gfr_disease_type"] = "disease_none"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	// High GFR → eligible, should go to ASK_SEDATION
	if result.NextState == sm.StateGfrNotEligible {
		t.Error("expected eligible for low creatinine")
	}
	if result.NextState != sm.StateAskSedation {
		t.Errorf("expected ASK_SEDATION, got %s", result.NextState)
	}
}

func TestGfrResult_Below30(t *testing.T) {
	gfrSvc := services.NewGFRService()

	// Only register the single handler
	m := sm.NewMachine()
	m.Register(sm.StateGfrResult, gfrResultHandler(gfrSvc))

	sess := testSess(sm.StateGfrResult)
	sess.Context["patient_age"] = "70"
	sess.Context["patient_gender"] = "M"
	sess.Context["gfr_creatinine"] = "5.0"
	sess.Context["gfr_weight_kg"] = "60"
	sess.Context["gfr_disease_type"] = "disease_none"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateGfrNotEligible {
		t.Errorf("expected GFR_NOT_ELIGIBLE, got %s", result.NextState)
	}
}

// ==================== AskSedation ====================

func TestAskSedation_Yes(t *testing.T) {
	// Only register the single handler to avoid auto-chain
	m := sm.NewMachine()
	m.Register(sm.StateAskSedation, askSedationHandler())

	sess := testSess(sm.StateAskSedation)
	sess.Context["cups_code"] = "883533" // Sedatable

	// Step 1: First invocation — no _prompted_sedation → sends buttons, stays in ASK_SEDATION
	result1, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result1.NextState != sm.StateAskSedation {
		t.Errorf("step1: expected ASK_SEDATION (prompt), got %s", result1.NextState)
	}
	if result1.UpdateCtx == nil || result1.UpdateCtx["_prompted_sedation"] != "1" {
		t.Errorf("step1: expected _prompted_sedation=1, got %v", result1.UpdateCtx)
	}

	// Apply context updates from step 1 to session
	for k, v := range result1.UpdateCtx {
		sess.Context[k] = v
	}

	// Step 2: Second invocation — _prompted_sedation set, user selects sedated_yes
	result2, err := m.Process(context.Background(), sess, postbackM("sedated_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result2.UpdateCtx == nil || result2.UpdateCtx["is_sedated"] != "1" {
		t.Errorf("step2: expected is_sedated=1, got %v", result2.UpdateCtx)
	}
	if result2.NextState != sm.StateCheckExisting {
		t.Errorf("step2: expected CHECK_EXISTING, got %s", result2.NextState)
	}
}

// ==================== CheckExisting ====================

func TestCheckExisting_NoExisting(t *testing.T) {
	repo := &mockApptRepo{
		hasFutureForCupFn: func(ctx context.Context, pid, cup string) (bool, error) {
			return false, nil
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	// Only register the single handler
	m := sm.NewMachine()
	m.Register(sm.StateCheckExisting, checkExistingHandler(apptSvc))

	sess := testSess(sm.StateCheckExisting)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["cups_code"] = "890271"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	// No existing → proceed to CHECK_PRIOR_CONSULT
	if result.NextState != sm.StateCheckPriorConsult {
		t.Errorf("expected CHECK_PRIOR_CONSULTATION, got %s", result.NextState)
	}
}

func TestCheckExisting_HasExisting(t *testing.T) {
	repo := &mockApptRepo{
		hasFutureForCupFn: func(ctx context.Context, pid, cup string) (bool, error) {
			return true, nil
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	// Only register the single handler
	m := sm.NewMachine()
	m.Register(sm.StateCheckExisting, checkExistingHandler(apptSvc))

	sess := testSess(sm.StateCheckExisting)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["cups_code"] = "890271"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAppointmentExists {
		t.Errorf("expected APPOINTMENT_EXISTS, got %s", result.NextState)
	}
}

// ==================== AskContrasted (additional paths) ====================

func TestAskContrasted_No(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskContrasted, askContrastedHandler())

	sess := testSess(sm.StateAskContrasted)
	sess.Context["cups_code"] = "883533" // Contrastable
	sess.Context["patient_gender"] = "M"
	sess.Context["patient_age"] = "30"

	// Step 1: prompt
	result1, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result1.NextState != sm.StateAskContrasted {
		t.Errorf("step1: expected ASK_CONTRASTED, got %s", result1.NextState)
	}
	for k, v := range result1.UpdateCtx {
		sess.Context[k] = v
	}

	// Step 2: user selects contrast_no
	result2, err := m.Process(context.Background(), sess, postbackM("contrast_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result2.UpdateCtx == nil || result2.UpdateCtx["is_contrasted"] != "0" {
		t.Errorf("expected is_contrasted=0, got %v", result2.UpdateCtx)
	}
	if result2.NextState != sm.StateAskSedation {
		t.Errorf("expected ASK_SEDATION, got %s", result2.NextState)
	}
}

func TestAskContrasted_FemaleContrast(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskContrasted, askContrastedHandler())

	sess := testSess(sm.StateAskContrasted)
	sess.Context["cups_code"] = "883533" // Contrastable
	sess.Context["patient_gender"] = "F"
	sess.Context["patient_age"] = "30"

	// Step 1: prompt
	result1, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range result1.UpdateCtx {
		sess.Context[k] = v
	}

	// Step 2: female + contrast_yes → ASK_PREGNANCY
	result2, err := m.Process(context.Background(), sess, postbackM("contrast_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result2.UpdateCtx == nil || result2.UpdateCtx["is_contrasted"] != "1" {
		t.Errorf("expected is_contrasted=1, got %v", result2.UpdateCtx)
	}
	if result2.NextState != sm.StateAskPregnancy {
		t.Errorf("expected ASK_PREGNANCY for female, got %s", result2.NextState)
	}
}

func TestAskContrasted_BabyContrast(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskContrasted, askContrastedHandler())

	sess := testSess(sm.StateAskContrasted)
	sess.Context["cups_code"] = "883533" // Contrastable
	sess.Context["patient_gender"] = "M"
	sess.Context["patient_age"] = "0" // Baby

	// Step 1: prompt
	result1, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range result1.UpdateCtx {
		sess.Context[k] = v
	}

	// Step 2: baby + contrast_yes → ASK_BABY_WEIGHT
	result2, err := m.Process(context.Background(), sess, postbackM("contrast_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result2.UpdateCtx == nil || result2.UpdateCtx["is_contrasted"] != "1" {
		t.Errorf("expected is_contrasted=1, got %v", result2.UpdateCtx)
	}
	if result2.NextState != sm.StateAskBabyWeight {
		t.Errorf("expected ASK_BABY_WEIGHT for baby, got %s", result2.NextState)
	}
}

// ==================== AskPregnancy (additional paths) ====================

func TestAskPregnancy_FemalePrompt(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskPregnancy, askPregnancyHandler())

	sess := testSess(sm.StateAskPregnancy)
	sess.Context["patient_gender"] = "F"
	sess.Context["patient_age"] = "30"

	// First invocation: no _prompted_pregnancy → prompt, stays at ASK_PREGNANCY
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskPregnancy {
		t.Errorf("expected ASK_PREGNANCY (prompt), got %s", result.NextState)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["_prompted_pregnancy"] != "1" {
		t.Errorf("expected _prompted_pregnancy=1, got %v", result.UpdateCtx)
	}
}

func TestAskPregnancy_FemaleYes(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskPregnancy, askPregnancyHandler())

	sess := testSess(sm.StateAskPregnancy)
	sess.Context["patient_gender"] = "F"
	sess.Context["patient_age"] = "30"
	sess.Context["_prompted_pregnancy"] = "1" // Already prompted

	result, err := m.Process(context.Background(), sess, postbackM("pregnant_yes"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["is_pregnant"] != "1" {
		t.Errorf("expected is_pregnant=1, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StatePregnancyBlock {
		t.Errorf("expected PREGNANCY_BLOCK, got %s", result.NextState)
	}
}

func TestAskPregnancy_FemaleNo(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskPregnancy, askPregnancyHandler())

	sess := testSess(sm.StateAskPregnancy)
	sess.Context["patient_gender"] = "F"
	sess.Context["patient_age"] = "30"
	sess.Context["_prompted_pregnancy"] = "1" // Already prompted

	result, err := m.Process(context.Background(), sess, postbackM("pregnant_no"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["is_pregnant"] != "0" {
		t.Errorf("expected is_pregnant=0, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrCreatinine {
		t.Errorf("expected GFR_CREATININE, got %s", result.NextState)
	}
}

// ==================== PregnancyBlock ====================

func TestPregnancyBlock(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StatePregnancyBlock, pregnancyBlockHandler())

	sess := testSess(sm.StatePregnancyBlock)

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
	if len(result.Messages) == 0 {
		t.Error("expected a message explaining pregnancy block")
	}
}

// ==================== AskBabyWeight ====================

func TestAskBabyWeight_Low(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskBabyWeight, askBabyWeightHandler())

	sess := testSess(sm.StateAskBabyWeight)

	result, err := m.Process(context.Background(), sess, postbackM("baby_low"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["baby_weight_cat"] != "bajo" {
		t.Errorf("expected baby_weight_cat=bajo, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrCreatinine {
		t.Errorf("expected GFR_CREATININE, got %s", result.NextState)
	}
}

func TestAskBabyWeight_Normal(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskBabyWeight, askBabyWeightHandler())

	sess := testSess(sm.StateAskBabyWeight)

	result, err := m.Process(context.Background(), sess, postbackM("baby_normal"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["baby_weight_cat"] != "normal" {
		t.Errorf("expected baby_weight_cat=normal, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrCreatinine {
		t.Errorf("expected GFR_CREATININE, got %s", result.NextState)
	}
}

// ==================== GfrCreatinine (additional age paths) ====================

func TestGfrCreatinine_Child(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrCreatinine, gfrCreatinineHandler())

	sess := testSess(sm.StateGfrCreatinine)
	sess.Context["patient_age"] = "10" // <= 14 → GFR_HEIGHT

	result, err := m.Process(context.Background(), sess, textM("0.8"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_creatinine"] != "0.80" {
		t.Errorf("expected gfr_creatinine=0.80, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrHeight {
		t.Errorf("expected GFR_HEIGHT for age 10, got %s", result.NextState)
	}
}

func TestGfrCreatinine_YoungAdult(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrCreatinine, gfrCreatinineHandler())

	sess := testSess(sm.StateGfrCreatinine)
	sess.Context["patient_age"] = "25" // 15-39 → GFR_DISEASE

	result, err := m.Process(context.Background(), sess, textM("1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_creatinine"] != "1.00" {
		t.Errorf("expected gfr_creatinine=1.00, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrDisease {
		t.Errorf("expected GFR_DISEASE for age 25, got %s", result.NextState)
	}
}

// ==================== GfrDisease ====================

func TestGfrDisease_None(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrDisease, gfrDiseaseHandler())

	sess := testSess(sm.StateGfrDisease)

	result, err := m.Process(context.Background(), sess, postbackM("disease_none"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_disease_type"] != "disease_none" {
		t.Errorf("expected gfr_disease_type=disease_none, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrResult {
		t.Errorf("expected GFR_RESULT, got %s", result.NextState)
	}
}

func TestGfrDisease_Renal(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrDisease, gfrDiseaseHandler())

	sess := testSess(sm.StateGfrDisease)

	result, err := m.Process(context.Background(), sess, postbackM("disease_renal"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_disease_type"] != "disease_renal" {
		t.Errorf("expected gfr_disease_type=disease_renal, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrWeight {
		t.Errorf("expected GFR_WEIGHT, got %s", result.NextState)
	}
}

func TestGfrDisease_Diabetica(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrDisease, gfrDiseaseHandler())

	sess := testSess(sm.StateGfrDisease)

	result, err := m.Process(context.Background(), sess, postbackM("disease_diabetica"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_disease_type"] != "disease_diabetica" {
		t.Errorf("expected gfr_disease_type=disease_diabetica, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrWeight {
		t.Errorf("expected GFR_WEIGHT, got %s", result.NextState)
	}
}

// ==================== GfrHeight ====================

func TestGfrHeight_ValidChild(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrHeight, gfrHeightHandler())

	sess := testSess(sm.StateGfrHeight)
	sess.Context["patient_age"] = "10" // <= 14 → GFR_RESULT

	result, err := m.Process(context.Background(), sess, textM("120"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_height_cm"] != "120" {
		t.Errorf("expected gfr_height_cm=120, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrResult {
		t.Errorf("expected GFR_RESULT for child, got %s", result.NextState)
	}
}

func TestGfrHeight_ValidAdult(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrHeight, gfrHeightHandler())

	sess := testSess(sm.StateGfrHeight)
	sess.Context["patient_age"] = "20" // > 14 → GFR_WEIGHT

	result, err := m.Process(context.Background(), sess, textM("170"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_height_cm"] != "170" {
		t.Errorf("expected gfr_height_cm=170, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrWeight {
		t.Errorf("expected GFR_WEIGHT for adult, got %s", result.NextState)
	}
}

func TestGfrHeight_Invalid(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrHeight, gfrHeightHandler())

	sess := testSess(sm.StateGfrHeight)
	sess.Context["patient_age"] = "10"

	result, err := m.Process(context.Background(), sess, textM("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateGfrHeight {
		t.Errorf("expected retry at GFR_HEIGHT, got %s", result.NextState)
	}
}

// ==================== GfrWeight ====================

func registerGfrWeightConfig(m *sm.Machine) {
	m.RegisterWithConfig(sm.StateGfrWeight, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: validators.FloatRange(10, 300),
		ErrorMsg:     "Peso no válido. Ingresa tu peso en kilogramos (ejemplo: 70).",
		Handler:      gfrWeightHandler(),
	})
}

func TestGfrWeight_Valid(t *testing.T) {
	m := sm.NewMachine()
	registerGfrWeightConfig(m)

	sess := testSess(sm.StateGfrWeight)

	result, err := m.Process(context.Background(), sess, textM("70"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["gfr_weight_kg"] != "70.0" {
		t.Errorf("expected gfr_weight_kg=70.0, got %v", result.UpdateCtx)
	}
	if result.NextState != sm.StateGfrResult {
		t.Errorf("expected GFR_RESULT, got %s", result.NextState)
	}
}

func TestGfrWeight_Invalid(t *testing.T) {
	m := sm.NewMachine()
	registerGfrWeightConfig(m)

	sess := testSess(sm.StateGfrWeight)

	result, err := m.Process(context.Background(), sess, textM("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateGfrWeight {
		t.Errorf("expected retry at GFR_WEIGHT, got %s", result.NextState)
	}
}

// ==================== GfrNotEligible ====================

func TestGfrNotEligible(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateGfrNotEligible, gfrNotEligibleHandler())

	sess := testSess(sm.StateGfrNotEligible)
	sess.Context["gfr_calculated"] = "25.0"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}

// ==================== AppointmentExists ====================

func TestAppointmentExists(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAppointmentExists, appointmentExistsHandler())

	sess := testSess(sm.StateAppointmentExists)
	sess.Context["cups_name"] = "Resonancia Cerebral"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
	if len(result.Messages) == 0 {
		t.Error("expected a message about existing appointment")
	}
}

// ==================== CheckPriorConsult ====================

func TestCheckPriorConsult_NotBlocked(t *testing.T) {
	apptSvc := services.NewAppointmentService(&mockApptRepo{}, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCheckPriorConsult, checkPriorConsultHandler(apptSvc))

	sess := testSess(sm.StateCheckPriorConsult)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["cups_code"] = "890271" // Not in cupsRequiresPreviousDoctor → pass through

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckMRCLimit {
		t.Errorf("expected CHECK_MRC_LIMIT, got %s", result.NextState)
	}
}

func TestCheckPriorConsult_SetsPreferredDoctor(t *testing.T) {
	repo := &mockApptRepo{
		findLastDoctorForCupsFn: func(ctx context.Context, pid string, cups []string) (string, error) {
			return "87654321", nil // Found last neurologist
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCheckPriorConsult, checkPriorConsultHandler(apptSvc))

	sess := testSess(sm.StateCheckPriorConsult)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["cups_code"] = "053105" // Requires prior neurology consultation

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckMRCLimit {
		t.Errorf("expected CHECK_MRC_LIMIT, got %s", result.NextState)
	}
	if result.UpdateCtx == nil || result.UpdateCtx["preferred_doctor_doc"] != "87654321" {
		t.Error("expected preferred_doctor_doc=87654321 in UpdateCtx")
	}
}

func TestCheckPriorConsult_NoPriorDoctor_StillContinues(t *testing.T) {
	repo := &mockApptRepo{
		findLastDoctorForCupsFn: func(ctx context.Context, pid string, cups []string) (string, error) {
			return "", nil // No prior consultation found — but NOT blocked
		},
	}
	apptSvc := services.NewAppointmentService(repo, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCheckPriorConsult, checkPriorConsultHandler(apptSvc))

	sess := testSess(sm.StateCheckPriorConsult)
	sess.Context["patient_id"] = "PAT001"
	sess.Context["cups_code"] = "053105"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckMRCLimit {
		t.Errorf("expected CHECK_MRC_LIMIT, got %s", result.NextState)
	}
}

// ==================== CheckSoatLimit ====================

func TestCheckMRCLimit_NotBlocked(t *testing.T) {
	apptSvc := services.NewAppointmentService(&mockApptRepo{}, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCheckMRCLimit, checkMRCLimitHandler(apptSvc))

	sess := testSess(sm.StateCheckMRCLimit)
	sess.Context["cups_code"] = "890271"
	sess.Context["patient_contract"] = "4" // Evento (non-MRC) → not flagged

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckAgeRestriction {
		t.Errorf("expected CHECK_AGE_RESTRICTION, got %s", result.NextState)
	}
}

func TestCheckMRCLimit_MRCContract_SetsFlag(t *testing.T) {
	apptSvc := services.NewAppointmentService(&mockApptRepo{}, nil)

	m := sm.NewMachine()
	m.Register(sm.StateCheckMRCLimit, checkMRCLimitHandler(apptSvc))

	sess := testSess(sm.StateCheckMRCLimit)
	sess.Context["cups_code"] = "861411"   // aplicacion_sustancia → in mrcGroup
	sess.Context["patient_contract"] = "6" // Sanitas MRC Contributivo

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateCheckAgeRestriction {
		t.Errorf("expected CHECK_AGE_RESTRICTION, got %s", result.NextState)
	}
	// Should set MRC flag
	if result.UpdateCtx == nil || result.UpdateCtx["mrc_limit_check"] != "1" {
		t.Error("expected mrc_limit_check=1 in UpdateCtx for MRC contract + mrcGroup CUPS")
	}
}

// ==================== CheckAgeRestriction ====================

func TestCheckAgeRestriction(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateCheckAgeRestriction, checkAgeRestrictionHandler())

	sess := testSess(sm.StateCheckAgeRestriction)

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateSearchSlots {
		t.Errorf("expected SEARCH_SLOTS, got %s", result.NextState)
	}
}

// ==================== Helper functions ====================

func TestIsContrastable(t *testing.T) {
	tests := []struct {
		cups string
		want bool
	}{
		{"883533", true},  // Resonancia → contrastable
		{"871100", true},  // Tomografía → contrastable
		{"890271", false}, // Consulta → not contrastable
	}
	for _, tt := range tests {
		got := isContrastable(tt.cups)
		if got != tt.want {
			t.Errorf("isContrastable(%q) = %v, want %v", tt.cups, got, tt.want)
		}
	}
}

func TestIsSedatable(t *testing.T) {
	tests := []struct {
		cups string
		want bool
	}{
		{"883533", true},  // Resonancia → sedatable
		{"871100", false}, // Tomografía → not sedatable
	}
	for _, tt := range tests {
		got := isSedatable(tt.cups)
		if got != tt.want {
			t.Errorf("isSedatable(%q) = %v, want %v", tt.cups, got, tt.want)
		}
	}
}
