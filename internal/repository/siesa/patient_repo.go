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

// affiliationTypeInt maps the TipoAfiliado catalog ID (string "1".."4") to tipo_afilia int.
// 1=Cotizante, 2=Beneficiario, 3=Sustituto de pensión, 4=Pensionado.
func affiliationTypeInt(t string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil && n >= 1 && n <= 4 {
		return n
	}
	return 1
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
	ISNULL(p.cod_muni,''),
	ISNULL(CAST(p.contrato AS VARCHAR(20)),''),
	ISNULL(p.cod_dep,''),
	ISNULL(mu.nombre,'')`

// patientFromClause joins sis_muni to resolve the residence municipality name
// (mu.nombre) used for display in the Sanitas municipality-confirm step.
const patientFromClause = `
	FROM sis_paci p WITH (NOLOCK)
	LEFT JOIN sis_muni mu WITH (NOLOCK)
		ON mu.codigo = p.cod_muni AND CAST(mu.id_dep AS VARCHAR(10)) = p.cod_dep`

func (r *PatientRepo) scanPatient(row *sql.Row) (*domain.Patient, error) {
	p := &domain.Patient{}
	var birthStr string
	err := row.Scan(
		&p.ID, &p.DocumentType, &p.DocumentNumber,
		&p.FirstName, &p.SecondName, &p.FirstSurname, &p.SecondSurname, &p.FullName,
		&birthStr, &p.Gender, &p.Phone, &p.Email, &p.EntityCode,
		&p.Address, &p.CityCode, &p.ContractCode,
		&p.DepartmentCode, &p.CityName,
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

// FindByDocument looks up a patient by (tipo_id, num_id). sis_paci uniqueness is
// (nro_historia, tipo_id), and num_id alone is NOT unique — real data has the same number
// under different document types (e.g. CC vs CE) — so both are required to avoid mis-identifying.
func (r *PatientRepo) FindByDocument(ctx context.Context, docType, doc string) (*domain.Patient, error) {
	query := `SELECT ` + patientSelectCols + patientFromClause + `
	          WHERE p.tipo_id = @p1 AND p.num_id = @p2`
	return r.scanPatient(r.db.QueryRowContext(ctx, query, docType, doc))
}

func (r *PatientRepo) FindByID(ctx context.Context, id string) (*domain.Patient, error) {
	query := `SELECT ` + patientSelectCols + patientFromClause + `
	          WHERE p.autoid = @p1`
	return r.scanPatient(r.db.QueryRowContext(ctx, query, id))
}

func (r *PatientRepo) Create(ctx context.Context, input domain.CreatePatientInput) (string, error) {
	userTypeInt, _ := strconv.Atoi(input.UserType)
	if userTypeInt == 0 {
		userTypeInt = 1
	}
	maritalStatusInt, _ := strconv.Atoi(input.MaritalStatus)

	// barrio es bigint (FK a sis_barrios). Vacío → NULL.
	var barrioArg interface{}
	if n, err := strconv.ParseInt(strings.TrimSpace(input.Barrio), 10, 64); err == nil && n > 0 {
		barrioArg = n
	}

	// OUTPUT INTO @table_variable funciona con triggers (a diferencia de OUTPUT directo).
	// nro_historia = tipo_id + num_id es requerido por el constraint sis_paci_uq(nro_historia, tipo_id).
	// NombreCompleto NO se setea aquí: lo llena automáticamente el trigger updatenro_historia
	// (UPPER(tipo_id - num_id : nombres apellidos)) en cada INSERT/UPDATE.
	// telefono = principal (100% poblado en histórico), telefono_alternativo = secundario;
	// celular se deja vacío como en el histórico (solo 0.5% lo usa). id_sede = 2 (SEDE 01, 99.3% real).
	query := `
	DECLARE @ids TABLE (autoid INT);
	INSERT INTO sis_paci (
		tipo_id, num_id, nro_historia,
		primer_ape, segundo_ape, primer_nom, segundo_nom,
		fecha_naci, sexo,
		direccion, cod_dep, cod_muni, zona, barrio,
		telefono, celular, telefono_alternativo, email,
		entidad, fecha_crea, id_sede,
		tipo_usuario, tipo_afilia, estadoCivil, tipo_sangre,
		IdPais, codPaisResidencia
	) OUTPUT INSERTED.autoid INTO @ids
	VALUES (
		@p1, @p2, @p1 + @p2,
		@p3, @p4, @p5, @p6,
		@p7, @p8,
		@p9, @p10, @p11, @p12, @p21,
		@p13, @p13, @p14, @p15,
		@p16, GETDATE(), 2,
		@p17, @p18, @p19, @p20,
		50, '50'
	);
	SELECT autoid FROM @ids;`

	var newID string
	err := r.db.QueryRowContext(ctx, query,
		input.DocumentType, input.DocumentNumber,
		truncateSIESA(input.FirstSurname, 250), truncateSIESA(input.SecondSurname, 250),
		truncateSIESA(input.FirstName, 250), truncateSIESA(input.SecondName, 250),
		input.BirthDate, input.Gender,
		truncateSIESA(input.Address, 100), input.DepartmentCode, input.CityCode, input.Zone,
		input.Phone, truncateSIESA(input.Phone2, 10), input.Email,
		input.EntityCode,
		userTypeInt, affiliationTypeInt(input.AffiliationType), maritalStatusInt, truncateSIESA(input.BloodType, 10),
		barrioArg,
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
	// Stores entityCode directly in sis_paci.entidad.
	// Can be company code (e.g. "EPS005") or numeric contract code (e.g. "6" for MRC Contributivo).
	// lookupContract in appointment_repo distinguishes both cases when scheduling.
	_, err := r.db.ExecContext(ctx,
		`UPDATE sis_paci SET entidad = @p1 WHERE autoid = @p2`,
		entityCode, patientID)
	if err != nil {
		return fmt.Errorf("siesa update entity: %w", err)
	}
	return nil
}

// UpdateContract persists the resolved contract on the patient (sis_paci.contrato),
// so the contract stays tied to the patient for future calculations. Used by the
// EPS contract resolution (régimen + Sanitas municipality).
func (r *PatientRepo) UpdateContract(ctx context.Context, patientID, contractCode string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sis_paci SET contrato = @p1 WHERE autoid = @p2`,
		contractCode, patientID)
	if err != nil {
		return fmt.Errorf("siesa update contract: %w", err)
	}
	return nil
}

// UpdateMunicipality persiste el departamento y municipio de residencia del
// paciente (sis_paci.cod_dep, sis_paci.cod_muni) cuando los corrige/edita.
func (r *PatientRepo) UpdateMunicipality(ctx context.Context, patientID, depCode, muniCode string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sis_paci SET cod_dep = @p1, cod_muni = @p2 WHERE autoid = @p3`,
		depCode, muniCode, patientID)
	if err != nil {
		return fmt.Errorf("siesa update municipality: %w", err)
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
