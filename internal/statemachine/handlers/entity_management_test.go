package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// --- ASK_CLIENT_TYPE ---

func TestAskClientType_ValidSelection_EPS(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindActiveByCategoryFn: func(ctx context.Context, category string) ([]domain.Entity, error) {
			return []domain.Entity{
				{Code: "EPS001", Name: "NUEVA EPS", Category: "EPS", IsActive: true},
			}, nil
		},
	}
	m := sm.NewMachine()
	RegisterEntityManagementHandlers(m, entityRepo, &testutil.MockPatientRepo{})

	sess := testSess(sm.StateAskClientType)
	result, err := m.Process(context.Background(), sess, postbackM("ct_2"))
	if err != nil {
		t.Fatal(err)
	}
	// EPS now asks the régimen (contributivo/subsidiado) before the entity list.
	if result.NextState != sm.StateAskEpsRegimen {
		t.Errorf("expected ASK_EPS_REGIMEN, got %s", result.NextState)
	}
	// ASK_EPS_REGIMEN is interactive (no auto-chain), so context stays in the
	// result's UpdateCtx (the worker persists it afterward).
	if result.UpdateCtx["entity_category"] != "EPS" {
		t.Errorf("expected entity_category=EPS, got %s", result.UpdateCtx["entity_category"])
	}
	if result.UpdateCtx["client_type"] != "EPS" {
		t.Errorf("expected client_type=EPS, got %s", result.UpdateCtx["client_type"])
	}
}

func TestAskClientType_Particular_SkipsListToDocument(t *testing.T) {
	m := sm.NewMachine()
	RegisterEntityManagementHandlers(m, &testutil.MockEntityRepo{}, &testutil.MockPatientRepo{})

	sess := testSess(sm.StateAskClientType)
	result, err := m.Process(context.Background(), sess, postbackM("ct_1")) // ct_1 = PARTICULAR
	if err != nil {
		t.Fatal(err)
	}
	// PARTICULAR skips the entity list and goes straight to document type.
	if result.NextState != sm.StateAskDocumentType {
		t.Errorf("expected ASK_DOCUMENT_TYPE, got %s", result.NextState)
	}
	if result.UpdateCtx["selected_entity_code"] != "PART02" {
		t.Errorf("expected selected_entity_code=PART02, got %s", result.UpdateCtx["selected_entity_code"])
	}
}

func TestAskClientType_InvalidText(t *testing.T) {
	m := sm.NewMachine()
	RegisterEntityManagementHandlers(m, &testutil.MockEntityRepo{}, &testutil.MockPatientRepo{})

	sess := testSess(sm.StateAskClientType)
	result, err := m.Process(context.Background(), sess, textM("hello"))
	if err != nil {
		t.Fatal(err)
	}
	// Should stay on same state with retry
	if result.NextState != sm.StateAskClientType {
		t.Errorf("expected ASK_CLIENT_TYPE (retry), got %s", result.NextState)
	}
}

func TestAskClientType_MaxRetries_Escalates(t *testing.T) {
	m := sm.NewMachine()
	RegisterEntityManagementHandlers(m, &testutil.MockEntityRepo{}, &testutil.MockPatientRepo{})

	sess := testSess(sm.StateAskClientType)
	sess.RetryCount = 2 // Already at limit

	result, err := m.Process(context.Background(), sess, textM("invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT on max retries, got %s", result.NextState)
	}
}

// --- SHOW_ENTITY_LIST ---

func TestShowEntityList_WithEntities(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindActiveByCategoryFn: func(ctx context.Context, category string) ([]domain.Entity, error) {
			return []domain.Entity{
				{Code: "EPS001", Name: "NUEVA EPS", Category: "EPS", IsActive: true},
				{Code: "EPS002", Name: "FAMISANAR", Category: "EPS", IsActive: true},
			}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowEntityList, showEntityListHandler(entityRepo))

	sess := testSess(sm.StateShowEntityList)
	sess.Context["entity_category"] = "EPS"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskEntityNumber {
		t.Errorf("expected ASK_ENTITY_NUMBER, got %s", result.NextState)
	}
	// Context is in result.UpdateCtx (not yet applied to session for final interactive state)
	if result.UpdateCtx["entity_list_count"] != "2" {
		t.Errorf("expected entity_list_count=2, got %s", result.UpdateCtx["entity_list_count"])
	}
	codes := result.UpdateCtx["entity_list_codes"]
	if codes != "EPS001,EPS002" {
		t.Errorf("expected entity_list_codes=EPS001,EPS002, got %s", codes)
	}
}

func TestShowEntityList_Empty_Escalates(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindActiveByCategoryFn: func(ctx context.Context, category string) ([]domain.Entity, error) {
			return []domain.Entity{}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowEntityList, showEntityListHandler(entityRepo))

	sess := testSess(sm.StateShowEntityList)
	sess.Context["entity_category"] = "SOAT"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT for empty entities, got %s", result.NextState)
	}
}

func TestShowEntityList_RepoError_Fallback(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindActiveByCategoryFn: func(ctx context.Context, category string) ([]domain.Entity, error) {
			return nil, fmt.Errorf("db error")
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateShowEntityList, showEntityListHandler(entityRepo))

	sess := testSess(sm.StateShowEntityList)
	sess.Context["entity_category"] = "EPS"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskEntityNumber {
		t.Errorf("expected ASK_ENTITY_NUMBER (fallback), got %s", result.NextState)
	}
}

// --- ASK_ENTITY_NUMBER ---

func TestAskEntityNumber_ValidNumber_NonSanitas(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindByCodeFn: func(ctx context.Context, code string) (*domain.Entity, error) {
			return &domain.Entity{Code: code, Name: "NUEVA EPS", IsActive: true}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateAskEntityNumber, askEntityNumberHandler(entityRepo))

	sess := testSess(sm.StateAskEntityNumber)
	sess.Context["entity_list_count"] = "3"
	sess.Context["entity_list_codes"] = "EPS001,EPS002,EPS003"
	sess.Context["entity_category"] = "EPS"

	result, err := m.Process(context.Background(), sess, textM("1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskDocumentType {
		t.Errorf("expected ASK_DOCUMENT_TYPE, got %s", result.NextState)
	}
	if result.UpdateCtx["selected_entity_code"] != "EPS001" {
		t.Errorf("expected selected_entity_code=EPS001, got %s", result.UpdateCtx["selected_entity_code"])
	}
}

func TestAskEntityNumber_InvalidNumber_TooHigh(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskEntityNumber, askEntityNumberHandler(nil))

	sess := testSess(sm.StateAskEntityNumber)
	sess.Context["entity_list_count"] = "3"

	result, err := m.Process(context.Background(), sess, textM("99"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskEntityNumber {
		t.Errorf("expected ASK_ENTITY_NUMBER (retry), got %s", result.NextState)
	}
}

func TestAskEntityNumber_NonNumeric(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskEntityNumber, askEntityNumberHandler(nil))

	sess := testSess(sm.StateAskEntityNumber)
	sess.Context["entity_list_count"] = "3"

	result, err := m.Process(context.Background(), sess, textM("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskEntityNumber {
		t.Errorf("expected ASK_ENTITY_NUMBER (retry), got %s", result.NextState)
	}
}

// --- CHECK_ENTITY (legacy) ---

func TestCheckEntity_NoEntityCode(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateCheckEntity, checkEntityHandler(nil))

	sess := testSess(sm.StateCheckEntity)
	// patient_entity is empty

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateChangeEntity {
		t.Errorf("expected CHANGE_ENTITY, got %s", result.NextState)
	}
}

func TestCheckEntity_ActiveEntity(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindByCodeFn: func(ctx context.Context, code string) (*domain.Entity, error) {
			return &domain.Entity{Code: "EPS001", Name: "NUEVA EPS", IsActive: true}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateCheckEntity, checkEntityHandler(entityRepo))

	sess := testSess(sm.StateCheckEntity)
	sess.Context["patient_entity"] = "EPS001"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmEntity {
		t.Errorf("expected CONFIRM_ENTITY, got %s", result.NextState)
	}
}

func TestCheckEntity_InactiveEntity(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindByCodeFn: func(ctx context.Context, code string) (*domain.Entity, error) {
			return &domain.Entity{Code: "OLD01", Name: "ENTIDAD ANTIGUA", IsActive: false}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateCheckEntity, checkEntityHandler(entityRepo))

	sess := testSess(sm.StateCheckEntity)
	sess.Context["patient_entity"] = "OLD01"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateConfirmEntity {
		t.Errorf("expected CONFIRM_ENTITY, got %s", result.NextState)
	}
}

// --- CONFIRM_ENTITY (legacy) ---

func TestConfirmEntity_OK(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmEntity, confirmEntityHandler())

	sess := testSess(sm.StateConfirmEntity)
	sess.Context["entity_name"] = "NUEVA EPS"

	result, err := m.Process(context.Background(), sess, postbackM("entity_ok"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskMedicalOrder {
		t.Errorf("expected ASK_MEDICAL_ORDER, got %s", result.NextState)
	}
}

func TestConfirmEntity_Change(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmEntity, confirmEntityHandler())

	sess := testSess(sm.StateConfirmEntity)
	sess.Context["entity_name"] = "NUEVA EPS"

	result, err := m.Process(context.Background(), sess, postbackM("entity_change"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateChangeEntity {
		t.Errorf("expected CHANGE_ENTITY, got %s", result.NextState)
	}
}

// --- CHANGE_ENTITY (legacy) ---

func TestChangeEntity_PostbackSelection(t *testing.T) {
	var updatedEntity string
	patientRepo := &testutil.MockPatientRepo{
		UpdateEntityFn: func(ctx context.Context, patientID, entityCode string) error {
			updatedEntity = entityCode
			return nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateChangeEntity, changeEntityHandler(nil, patientRepo))

	sess := testSess(sm.StateChangeEntity)
	sess.Context["patient_id"] = "PAT-123"

	result, err := m.Process(context.Background(), sess, postbackM("EPS001"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskMedicalOrder {
		t.Errorf("expected ASK_MEDICAL_ORDER, got %s", result.NextState)
	}
	if updatedEntity != "EPS001" {
		t.Errorf("expected UpdateEntity called with EPS001, got %s", updatedEntity)
	}
}

func TestChangeEntity_TextSearch_ExactMatch(t *testing.T) {
	entityRepo := &testutil.MockEntityRepo{
		FindActiveFn: func(ctx context.Context) ([]domain.Entity, error) {
			return []domain.Entity{
				{Code: "SAN02", Name: "SANITAS MRC", IsActive: true},
				{Code: "EPS001", Name: "NUEVA EPS", IsActive: true},
			}, nil
		},
	}

	m := sm.NewMachine()
	m.Register(sm.StateChangeEntity, changeEntityHandler(entityRepo, &testutil.MockPatientRepo{}))

	sess := testSess(sm.StateChangeEntity)
	sess.Context["patient_id"] = "PAT-123"

	result, err := m.Process(context.Background(), sess, textM("NUEVA"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskMedicalOrder {
		t.Errorf("expected ASK_MEDICAL_ORDER (exact match), got %s", result.NextState)
	}
}
