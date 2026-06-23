package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// --- Mock PatientRepository ---

type mockPatientRepo struct {
	findByDocumentFn    func(ctx context.Context, docType, doc string) (*domain.Patient, error)
	findByIDFn          func(ctx context.Context, id string) (*domain.Patient, error)
	createFn            func(ctx context.Context, input domain.CreatePatientInput) (string, error)
	updateEntityFn      func(ctx context.Context, patientID, entityCode string) error
	updateContactInfoFn func(ctx context.Context, patientID, phone, email string) error
}

func (m *mockPatientRepo) FindByDocument(ctx context.Context, docType, doc string) (*domain.Patient, error) {
	if m.findByDocumentFn != nil {
		return m.findByDocumentFn(ctx, docType, doc)
	}
	return nil, nil
}

func (m *mockPatientRepo) FindByID(ctx context.Context, id string) (*domain.Patient, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *mockPatientRepo) Create(ctx context.Context, input domain.CreatePatientInput) (string, error) {
	if m.createFn != nil {
		return m.createFn(ctx, input)
	}
	return "new-id", nil
}

func (m *mockPatientRepo) UpdateEntity(ctx context.Context, patientID, entityCode string) error {
	if m.updateEntityFn != nil {
		return m.updateEntityFn(ctx, patientID, entityCode)
	}
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

// --- Tests ---

func TestLookupByDocument_Found(t *testing.T) {
	patient := &domain.Patient{ID: "P1", DocumentNumber: "1234567890", FirstName: "Juan"}
	repo := &mockPatientRepo{
		findByDocumentFn: func(ctx context.Context, docType, doc string) (*domain.Patient, error) {
			if doc == "1234567890" {
				return patient, nil
			}
			return nil, nil
		},
	}
	svc := NewPatientService(repo)

	p, err := svc.LookupByDocument(context.Background(), "CC", "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.ID != "P1" {
		t.Error("expected patient P1")
	}
}

func TestLookupByDocument_NotFound(t *testing.T) {
	repo := &mockPatientRepo{}
	svc := NewPatientService(repo)

	p, err := svc.LookupByDocument(context.Background(), "CC", "9999999999")
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Error("expected nil for unknown document")
	}
}

func TestLookupByDocument_Error(t *testing.T) {
	repo := &mockPatientRepo{
		findByDocumentFn: func(ctx context.Context, docType, doc string) (*domain.Patient, error) {
			return nil, fmt.Errorf("db error")
		},
	}
	svc := NewPatientService(repo)

	_, err := svc.LookupByDocument(context.Background(), "CC", "1234567890")
	if err == nil {
		t.Error("expected error")
	}
}

func TestCalculateAge(t *testing.T) {
	tests := []struct {
		name    string
		birth   time.Time
		wantAge int
	}{
		{"exact_birthday", time.Date(time.Now().Year()-30, time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC), 30},
		{"not_yet", time.Date(time.Now().Year()-30, time.Now().Month()+1, 1, 0, 0, 0, 0, time.UTC), 29},
		{"baby", time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC), 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CalculateAge(tc.birth)
			if got != tc.wantAge {
				t.Errorf("CalculateAge = %d, want %d", got, tc.wantAge)
			}
		})
	}
}

func TestFormatFullName(t *testing.T) {
	tests := []struct {
		name     string
		patient  *domain.Patient
		expected string
	}{
		{
			"full_name_set",
			&domain.Patient{FullName: "Juan Carlos Perez Lopez"},
			"Juan Carlos Perez Lopez",
		},
		{
			"all_parts",
			&domain.Patient{FirstName: "Juan", SecondName: "Carlos", FirstSurname: "Perez", SecondSurname: "Lopez"},
			"Juan Carlos Perez Lopez",
		},
		{
			"partial",
			&domain.Patient{FirstName: "Maria", FirstSurname: "Garcia"},
			"Maria Garcia",
		},
		{
			"empty",
			&domain.Patient{},
			"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatFullName(tc.patient)
			if got != tc.expected {
				t.Errorf("FormatFullName = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestFormatAge(t *testing.T) {
	birth := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	result := FormatAge(birth)
	if result == "" || result == "0" {
		t.Errorf("expected non-zero age string, got %q", result)
	}
}

// =============================================================================
// LookupByID tests
// =============================================================================

func TestLookupByID_Found(t *testing.T) {
	patient := &domain.Patient{ID: "P100", DocumentNumber: "5551234", FirstName: "Maria"}
	repo := &mockPatientRepo{
		findByIDFn: func(ctx context.Context, id string) (*domain.Patient, error) {
			if id == "P100" {
				return patient, nil
			}
			return nil, nil
		},
	}
	svc := NewPatientService(repo)

	p, err := svc.LookupByID(context.Background(), "P100")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.ID != "P100" {
		t.Error("expected patient P100")
	}
	if p.FirstName != "Maria" {
		t.Errorf("expected FirstName 'Maria', got %q", p.FirstName)
	}
}

func TestLookupByID_NotFound(t *testing.T) {
	repo := &mockPatientRepo{} // findByIDFn is nil → returns nil, nil
	svc := NewPatientService(repo)

	p, err := svc.LookupByID(context.Background(), "NONEXISTENT")
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Errorf("expected nil for unknown ID, got %+v", p)
	}
}

func TestLookupByID_Error(t *testing.T) {
	repo := &mockPatientRepo{
		findByIDFn: func(ctx context.Context, id string) (*domain.Patient, error) {
			return nil, fmt.Errorf("connection timeout")
		},
	}
	svc := NewPatientService(repo)

	_, err := svc.LookupByID(context.Background(), "P100")
	if err == nil {
		t.Error("expected error to be propagated")
	}
}

// =============================================================================
// Create tests
// =============================================================================

func TestCreate_Success(t *testing.T) {
	repo := &mockPatientRepo{
		createFn: func(ctx context.Context, input domain.CreatePatientInput) (string, error) {
			return "P999", nil
		},
	}
	svc := NewPatientService(repo)

	id, err := svc.Create(context.Background(), domain.CreatePatientInput{
		DocumentType:   "CC",
		DocumentNumber: "1234567890",
		FirstName:      "Juan",
		FirstSurname:   "Perez",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "P999" {
		t.Errorf("expected ID 'P999', got %q", id)
	}
}

func TestCreate_Error(t *testing.T) {
	repo := &mockPatientRepo{
		createFn: func(ctx context.Context, input domain.CreatePatientInput) (string, error) {
			return "", fmt.Errorf("duplicate key")
		},
	}
	svc := NewPatientService(repo)

	_, err := svc.Create(context.Background(), domain.CreatePatientInput{
		DocumentNumber: "1234567890",
	})
	if err == nil {
		t.Error("expected error to be propagated")
	}
}

// =============================================================================
// UpdateEntity tests
// =============================================================================

func TestUpdateEntity_Success(t *testing.T) {
	called := false
	repo := &mockPatientRepo{
		updateEntityFn: func(ctx context.Context, patientID, entityCode string) error {
			if patientID != "P100" || entityCode != "EPS001" {
				t.Errorf("unexpected args: patientID=%q, entityCode=%q", patientID, entityCode)
			}
			called = true
			return nil
		},
	}
	svc := NewPatientService(repo)

	err := svc.UpdateEntity(context.Background(), "P100", "EPS001")
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("expected repo.UpdateEntity to be called")
	}
}

func TestUpdateEntity_Error(t *testing.T) {
	repo := &mockPatientRepo{
		updateEntityFn: func(ctx context.Context, patientID, entityCode string) error {
			return fmt.Errorf("update failed")
		},
	}
	svc := NewPatientService(repo)

	err := svc.UpdateEntity(context.Background(), "P100", "EPS001")
	if err == nil {
		t.Error("expected error to be propagated")
	}
}
