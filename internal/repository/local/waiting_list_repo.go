package local

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

type WaitingListRepo struct {
	db *sql.DB
}

func NewWaitingListRepo(db *sql.DB) *WaitingListRepo {
	return &WaitingListRepo{db: db}
}

// Create inserts a new waiting list entry.
func (r *WaitingListRepo) Create(ctx context.Context, entry *domain.WaitingListEntry) error {
	query := `INSERT INTO waiting_list (
		id, phone_number, patient_id, patient_doc, patient_name, patient_age, patient_gender, patient_entity, patient_contract,
		cups_code, cups_name, is_contrasted, is_sedated, espacios, procedures_json, procedure_type,
		gfr_creatinine, gfr_height_cm, gfr_weight_kg, gfr_disease_type, gfr_calculated,
		is_pregnant, baby_weight_cat, preferred_doctor_doc,
		status, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := r.db.ExecContext(ctx, query,
		entry.ID, entry.PhoneNumber, entry.PatientID, entry.PatientDoc,
		entry.PatientName, entry.PatientAge, entry.PatientGender, entry.PatientEntity, entry.ContractCode,
		entry.CupsCode, entry.CupsName, entry.IsContrasted, entry.IsSedated,
		entry.Espacios, entry.ProceduresJSON, entry.ProcedureType,
		nullFloat(entry.GfrCreatinine), nullInt(entry.GfrHeightCm),
		nullFloat(entry.GfrWeightKg), nullString(entry.GfrDiseaseType),
		nullFloat(entry.GfrCalculated),
		nullBool(entry.IsPregnant), nullString(entry.BabyWeightCat),
		nullString(entry.PreferredDoctorDoc),
		entry.Status, entry.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create waiting list entry: %w", err)
	}
	return nil
}

// FindByID retrieves a waiting list entry by ID.
func (r *WaitingListRepo) FindByID(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
	query := `SELECT id, phone_number, patient_id, patient_doc, patient_name, patient_age, patient_gender, patient_entity, patient_contract,
		cups_code, cups_name, is_contrasted, is_sedated, espacios, procedures_json, procedure_type,
		gfr_creatinine, gfr_height_cm, gfr_weight_kg, gfr_disease_type, gfr_calculated,
		is_pregnant, baby_weight_cat, preferred_doctor_doc,
		status, notified_at, resolved_at, created_at, expires_at
		FROM waiting_list WHERE id = ?`

	e := &domain.WaitingListEntry{}
	var gfrCreatinine, gfrWeightKg, gfrCalculated sql.NullFloat64
	var gfrHeightCm sql.NullInt32
	var gfrDiseaseType, babyWeightCat, preferredDoctorDoc sql.NullString
	var isPregnant sql.NullBool
	var notifiedAt, resolvedAt sql.NullTime

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&e.ID, &e.PhoneNumber, &e.PatientID, &e.PatientDoc,
		&e.PatientName, &e.PatientAge, &e.PatientGender, &e.PatientEntity, &e.ContractCode,
		&e.CupsCode, &e.CupsName, &e.IsContrasted, &e.IsSedated,
		&e.Espacios, &e.ProceduresJSON, &e.ProcedureType,
		&gfrCreatinine, &gfrHeightCm, &gfrWeightKg, &gfrDiseaseType, &gfrCalculated,
		&isPregnant, &babyWeightCat, &preferredDoctorDoc,
		&e.Status, &notifiedAt, &resolvedAt, &e.CreatedAt, &e.ExpiresAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find waiting list entry: %w", err)
	}

	if gfrCreatinine.Valid {
		e.GfrCreatinine = gfrCreatinine.Float64
	}
	if gfrHeightCm.Valid {
		e.GfrHeightCm = int(gfrHeightCm.Int32)
	}
	if gfrWeightKg.Valid {
		e.GfrWeightKg = gfrWeightKg.Float64
	}
	if gfrDiseaseType.Valid {
		e.GfrDiseaseType = gfrDiseaseType.String
	}
	if gfrCalculated.Valid {
		e.GfrCalculated = gfrCalculated.Float64
	}
	if isPregnant.Valid {
		e.IsPregnant = isPregnant.Bool
	}
	if babyWeightCat.Valid {
		e.BabyWeightCat = babyWeightCat.String
	}
	if preferredDoctorDoc.Valid {
		e.PreferredDoctorDoc = preferredDoctorDoc.String
	}
	if notifiedAt.Valid {
		e.NotifiedAt = &notifiedAt.Time
	}
	if resolvedAt.Valid {
		e.ResolvedAt = &resolvedAt.Time
	}

	return e, nil
}

// HasActiveForPatientAndCups checks if patient already has an active entry for this CUPS.
func (r *WaitingListRepo) HasActiveForPatientAndCups(ctx context.Context, patientID, cupsCode string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM waiting_list WHERE patient_id = ? AND cups_code = ? AND status IN ('waiting', 'notified')",
		patientID, cupsCode).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check active waiting list: %w", err)
	}
	return count > 0, nil
}

// GetDistinctWaitingCups returns distinct CUPS codes with waiting entries.
func (r *WaitingListRepo) GetDistinctWaitingCups(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT DISTINCT cups_code FROM waiting_list WHERE status = 'waiting' ORDER BY cups_code")
	if err != nil {
		return nil, fmt.Errorf("get distinct waiting cups: %w", err)
	}
	defer rows.Close()

	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("scan cups code: %w", err)
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

// GetWaitingByCups returns waiting entries for a CUPS code, ordered FIFO, limited to N.
func (r *WaitingListRepo) GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error) {
	query := `SELECT id, phone_number, patient_id, patient_doc, patient_name, patient_age, patient_gender, patient_entity, patient_contract,
		cups_code, cups_name, is_contrasted, is_sedated, espacios, procedures_json, procedure_type,
		gfr_creatinine, gfr_height_cm, gfr_weight_kg, gfr_disease_type, gfr_calculated,
		is_pregnant, baby_weight_cat, preferred_doctor_doc,
		status, created_at, expires_at
		FROM waiting_list
		WHERE cups_code = ? AND status = 'waiting'
		ORDER BY created_at ASC
		LIMIT ?`

	rows, err := r.db.QueryContext(ctx, query, cupsCode, limit)
	if err != nil {
		return nil, fmt.Errorf("get waiting by cups: %w", err)
	}
	defer rows.Close()

	var entries []domain.WaitingListEntry
	for rows.Next() {
		var e domain.WaitingListEntry
		var gfrCreatinine, gfrWeightKg, gfrCalculated sql.NullFloat64
		var gfrHeightCm sql.NullInt32
		var gfrDiseaseType, babyWeightCat, preferredDoctorDoc sql.NullString
		var isPregnant sql.NullBool

		if err := rows.Scan(
			&e.ID, &e.PhoneNumber, &e.PatientID, &e.PatientDoc,
			&e.PatientName, &e.PatientAge, &e.PatientGender, &e.PatientEntity, &e.ContractCode,
			&e.CupsCode, &e.CupsName, &e.IsContrasted, &e.IsSedated,
			&e.Espacios, &e.ProceduresJSON, &e.ProcedureType,
			&gfrCreatinine, &gfrHeightCm, &gfrWeightKg, &gfrDiseaseType, &gfrCalculated,
			&isPregnant, &babyWeightCat, &preferredDoctorDoc,
			&e.Status, &e.CreatedAt, &e.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan waiting list entry: %w", err)
		}

		if gfrCreatinine.Valid {
			e.GfrCreatinine = gfrCreatinine.Float64
		}
		if gfrHeightCm.Valid {
			e.GfrHeightCm = int(gfrHeightCm.Int32)
		}
		if gfrWeightKg.Valid {
			e.GfrWeightKg = gfrWeightKg.Float64
		}
		if gfrDiseaseType.Valid {
			e.GfrDiseaseType = gfrDiseaseType.String
		}
		if gfrCalculated.Valid {
			e.GfrCalculated = gfrCalculated.Float64
		}
		if isPregnant.Valid {
			e.IsPregnant = isPregnant.Bool
		}
		if babyWeightCat.Valid {
			e.BabyWeightCat = babyWeightCat.String
		}
		if preferredDoctorDoc.Valid {
			e.PreferredDoctorDoc = preferredDoctorDoc.String
		}

		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// UpdateStatus changes the status of a waiting list entry.
func (r *WaitingListRepo) UpdateStatus(ctx context.Context, id, status string) error {
	query := "UPDATE waiting_list SET status = ?, resolved_at = NOW(), updated_at = NOW() WHERE id = ?"
	_, err := r.db.ExecContext(ctx, query, status, id)
	if err != nil {
		return fmt.Errorf("update waiting list status: %w", err)
	}
	return nil
}

// MarkNotified marks an entry as notified with timestamp.
func (r *WaitingListRepo) MarkNotified(ctx context.Context, id string) error {
	query := "UPDATE waiting_list SET status = 'notified', notified_at = NOW(), updated_at = NOW() WHERE id = ?"
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("mark notified: %w", err)
	}
	return nil
}

// ExpireOld expires waiting entries older than N days.
func (r *WaitingListRepo) ExpireOld(ctx context.Context, days int) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		"UPDATE waiting_list SET status = 'expired', resolved_at = NOW() WHERE status = 'waiting' AND expires_at < NOW()")
	if err != nil {
		return 0, fmt.Errorf("expire waiting list: %w", err)
	}
	return result.RowsAffected()
}

// List returns paginated waiting list entries with optional filters.
func (r *WaitingListRepo) List(ctx context.Context, filters domain.WaitingListFilters, page, pageSize int) ([]domain.WaitingListEntry, int, error) {
	where := "1=1"
	var args []interface{}

	if filters.Status != "" {
		where += " AND status = ?"
		args = append(args, filters.Status)
	}
	if filters.CupsCode != "" {
		where += " AND cups_code = ?"
		args = append(args, filters.CupsCode)
	}
	if filters.Phone != "" {
		where += " AND phone_number = ?"
		args = append(args, filters.Phone)
	}
	if filters.DateFrom != "" {
		where += " AND created_at >= ?"
		args = append(args, filters.DateFrom)
	}
	if filters.DateTo != "" {
		where += " AND created_at <= ?"
		args = append(args, filters.DateTo+" 23:59:59")
	}

	// Count total
	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM waiting_list WHERE %s", where)
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count waiting list: %w", err)
	}

	// Fetch page
	offset := (page - 1) * pageSize
	dataQuery := fmt.Sprintf(`SELECT id, phone_number, patient_id, patient_doc, patient_name,
		cups_code, cups_name, status, created_at, expires_at
		FROM waiting_list WHERE %s ORDER BY created_at DESC LIMIT ? OFFSET ?`, where)
	dataArgs := append(args, pageSize, offset)

	rows, err := r.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list waiting list: %w", err)
	}
	defer rows.Close()

	var entries []domain.WaitingListEntry
	for rows.Next() {
		var e domain.WaitingListEntry
		if err := rows.Scan(
			&e.ID, &e.PhoneNumber, &e.PatientID, &e.PatientDoc, &e.PatientName,
			&e.CupsCode, &e.CupsName, &e.Status, &e.CreatedAt, &e.ExpiresAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan waiting list: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

// --- Helpers ---

func nullFloat(f float64) sql.NullFloat64 {
	if f == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

func nullBool(b bool) sql.NullBool {
	return sql.NullBool{Bool: b, Valid: true}
}
