package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

// affiliationTypeInt maps C/B/O → tipo_afilia int (1=Cotizante, 2=Beneficiario, 3=Otro).
func affiliationTypeInt(t string) int {
	switch strings.ToUpper(t) {
	case "C":
		return 1
	case "B":
		return 2
	case "O":
		return 3
	default:
		return 1
	}
}

var _ repository.PatientRepository = (*PatientRepo)(nil)

// PatientRepo lee y escribe pacientes en ZeusSalud_Neuro.sis_paci.
//
// Mapeo de campos (sis_paci → domain.Patient):
//   autoid       → Patient.ID
//   tipo_id      → Patient.DocumentType
//   num_id       → Patient.DocumentNumber
//   primer_nom   → Patient.FirstName
//   segundo_nom  → Patient.SecondName
//   primer_ape   → Patient.FirstSurname
//   segundo_ape  → Patient.SecondSurname
//   fecha_naci   → Patient.BirthDate
//   sexo         → Patient.Gender
//   telefono     → Patient.Phone
//   celular      → Patient.Phone (fallback si telefono vacío)
//   email        → Patient.Email
//   entidad      → Patient.EntityCode
//   direccion    → Patient.Address
//   cod_muni     → Patient.CityCode
type PatientRepo struct {
	db *sql.DB
}

func NewPatientRepo(db *sql.DB) *PatientRepo {
	return &PatientRepo{db: db}
}

const patientSelectCols = `
	p.autoid,
	ISNULL(p.tipo_id,''),
	ISNULL(p.num_id,''),
	ISNULL(p.primer_nom,''),
	ISNULL(p.segundo_nom,''),
	ISNULL(p.primer_ape,''),
	ISNULL(p.segundo_ape,''),
	ISNULL(LTRIM(RTRIM(CONCAT(
		p.primer_nom,' ',ISNULL(p.segundo_nom,''),' ',
		p.primer_ape,' ',ISNULL(p.segundo_ape,'')
	))), ''),
	ISNULL(CONVERT(VARCHAR(10), p.fecha_naci, 120), '1900-01-01'),
	ISNULL(p.sexo,''),
	ISNULL(NULLIF(LTRIM(RTRIM(p.telefono)),''), ISNULL(p.celular,'')),
	ISNULL(p.email,''),
	ISNULL(p.entidad,''),
	ISNULL(p.direccion,''),
	ISNULL(p.cod_muni,'')`

func (r *PatientRepo) scanPatient(row *sql.Row) (*domain.Patient, error) {
	p := &domain.Patient{}
	var birthStr string
	err := row.Scan(
		&p.ID, &p.DocumentType, &p.DocumentNumber,
		&p.FirstName, &p.SecondName, &p.FirstSurname, &p.SecondSurname, &p.FullName,
		&birthStr, &p.Gender, &p.Phone, &p.Email, &p.EntityCode,
		&p.Address, &p.CityCode,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t, err2 := time.Parse("2006-01-02", birthStr); err2 == nil {
		p.BirthDate = t
	}
	return p, nil
}

func (r *PatientRepo) FindByDocument(ctx context.Context, doc string) (*domain.Patient, error) {
	query := `SELECT ` + patientSelectCols + `
	          FROM sis_paci p
	          WHERE p.num_id = @p1`
	return r.scanPatient(r.db.QueryRowContext(ctx, query, doc))
}

func (r *PatientRepo) FindByID(ctx context.Context, id string) (*domain.Patient, error) {
	query := `SELECT ` + patientSelectCols + `
	          FROM sis_paci p
	          WHERE p.autoid = @p1`
	return r.scanPatient(r.db.QueryRowContext(ctx, query, id))
}

func (r *PatientRepo) Create(ctx context.Context, input domain.CreatePatientInput) (string, error) {
	userTypeInt, _ := strconv.Atoi(input.UserType)
	if userTypeInt == 0 {
		userTypeInt = 1
	}
	maritalStatusInt, _ := strconv.Atoi(input.MaritalStatus)

	// OUTPUT INTO @table_variable funciona con triggers (a diferencia de OUTPUT directo).
	// nro_historia = tipo_id + num_id es requerido por el constraint sis_paci_uq(nro_historia, tipo_id).
	query := `
	DECLARE @ids TABLE (autoid INT);
	INSERT INTO sis_paci (
		tipo_id, num_id, nro_historia,
		primer_ape, segundo_ape, primer_nom, segundo_nom,
		fecha_naci, sexo,
		direccion, cod_dep, cod_muni, zona,
		telefono, celular, email,
		entidad, fecha_crea,
		tipo_usuario, tipo_afilia, estadoCivil
	) OUTPUT INSERTED.autoid INTO @ids
	VALUES (
		@p1, @p2, @p1 + @p2,
		@p3, @p4, @p5, @p6,
		@p7, @p8,
		@p9, @p10, @p11, @p12,
		@p13, @p13, @p14,
		@p15, GETDATE(),
		@p16, @p17, @p18
	);
	SELECT autoid FROM @ids;`

	var newID string
	err := r.db.QueryRowContext(ctx, query,
		input.DocumentType, input.DocumentNumber,
		truncateSIESA(input.FirstSurname, 250), truncateSIESA(input.SecondSurname, 250),
		truncateSIESA(input.FirstName, 250), truncateSIESA(input.SecondName, 250),
		input.BirthDate, input.Gender,
		truncateSIESA(input.Address, 100), input.DepartmentCode, input.CityCode, input.Zone,
		input.Phone, input.Email,
		input.EntityCode,
		userTypeInt, affiliationTypeInt(input.AffiliationType), maritalStatusInt,
	).Scan(&newID)
	if err != nil {
		slog.Error("siesa_create_patient_failed",
			"doc_type", input.DocumentType,
			"doc", input.DocumentNumber,
			"entity", input.EntityCode,
			"dep", input.DepartmentCode,
			"muni", input.CityCode,
			"error", err.Error(),
		)
		return "", fmt.Errorf("siesa create patient: %w", err)
	}
	return newID, nil
}

func (r *PatientRepo) UpdateEntity(ctx context.Context, patientID, entityCode string) error {
	// Guarda entityCode directamente en sis_paci.entidad.
	// Puede ser empresa (e.g. "EPS005") o código numérico de contrato (e.g. "6" para MRC Contributivo).
	// lookupContrato distingue ambos casos al agendar.
	_, err := r.db.ExecContext(ctx,
		`UPDATE sis_paci SET entidad = @p1 WHERE autoid = @p2`,
		entityCode, patientID)
	if err != nil {
		return fmt.Errorf("siesa update entity: %w", err)
	}
	return nil
}

func (r *PatientRepo) UpdateContactInfo(ctx context.Context, patientID, phone, email string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sis_paci SET telefono = @p1, email = @p2 WHERE autoid = @p3`,
		phone, email, patientID)
	if err != nil {
		return fmt.Errorf("siesa update contact: %w", err)
	}
	return nil
}

func truncateSIESA(s string, maxLen int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > maxLen {
		return string(r[:maxLen])
	}
	return strings.TrimSpace(s)
}
