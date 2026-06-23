package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

type PatientService struct {
	repo repository.PatientRepository
}

func NewPatientService(repo repository.PatientRepository) *PatientService {
	return &PatientService{repo: repo}
}

// LookupByDocument busca un paciente por tipo + número de documento.
func (s *PatientService) LookupByDocument(ctx context.Context, docType, document string) (*domain.Patient, error) {
	return s.repo.FindByDocument(ctx, docType, document)
}

// LookupByID busca un paciente por NumeroPaciente
func (s *PatientService) LookupByID(ctx context.Context, id string) (*domain.Patient, error) {
	return s.repo.FindByID(ctx, id)
}

// CalculateAge calcula la edad a partir de la fecha de nacimiento.
// Uses month/day comparison instead of YearDay() to handle leap years correctly.
func CalculateAge(birthDate time.Time) int {
	now := time.Now()
	age := now.Year() - birthDate.Year()
	if now.Month() < birthDate.Month() ||
		(now.Month() == birthDate.Month() && now.Day() < birthDate.Day()) {
		age--
	}
	return age
}

// FormatFullName construye el nombre completo desde partes
func FormatFullName(p *domain.Patient) string {
	if p.FullName != "" {
		return strings.TrimSpace(p.FullName)
	}
	parts := []string{}
	if p.FirstName != "" {
		parts = append(parts, p.FirstName)
	}
	if p.SecondName != "" {
		parts = append(parts, p.SecondName)
	}
	if p.FirstSurname != "" {
		parts = append(parts, p.FirstSurname)
	}
	if p.SecondSurname != "" {
		parts = append(parts, p.SecondSurname)
	}
	return strings.Join(parts, " ")
}

// FormatAge devuelve la edad como string
func FormatAge(birthDate time.Time) string {
	return fmt.Sprintf("%d", CalculateAge(birthDate))
}

// Create crea un paciente nuevo en la BD externa
func (s *PatientService) Create(ctx context.Context, input domain.CreatePatientInput) (string, error) {
	return s.repo.Create(ctx, input)
}

// UpdateEntity actualiza la entidad/EPS de un paciente
func (s *PatientService) UpdateEntity(ctx context.Context, patientID, entityCode string) error {
	return s.repo.UpdateEntity(ctx, patientID, entityCode)
}

// UpdateContract persiste el contrato resuelto en el paciente (sis_paci.contrato).
func (s *PatientService) UpdateContract(ctx context.Context, patientID, contractCode string) error {
	return s.repo.UpdateContract(ctx, patientID, contractCode)
}

// UpdateMunicipality persiste departamento+municipio del paciente (sis_paci).
func (s *PatientService) UpdateMunicipality(ctx context.Context, patientID, depCode, muniCode string) error {
	return s.repo.UpdateMunicipality(ctx, patientID, depCode, muniCode)
}

// UpdateContactInfo actualiza teléfono y email de un paciente en la BD externa
func (s *PatientService) UpdateContactInfo(ctx context.Context, patientID, phone, email string) error {
	return s.repo.UpdateContactInfo(ctx, patientID, phone, email)
}
