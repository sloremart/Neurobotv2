package local

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// MedicationPendingRecord representa un medicamento pendiente de aplicación para un paciente.
type MedicationPendingRecord struct {
	ID                 int
	Cedula             string
	Paciente           string
	EPS                string
	Medicamento        string
	NombreComercial    string
	Concentracion      string
	FormaFarmaceutica  string
	SaldoActual        int
	FechaVencimiento   string
	NumeroAutorizacion string // "SIN AUTORIZACION" si no tiene autorización vigente
}

// HasAutorizacion retorna true cuando el registro tiene un número de autorización válido.
func (r *MedicationPendingRecord) HasAutorizacion() bool {
	v := strings.TrimSpace(strings.ToUpper(r.NumeroAutorizacion))
	return v != "" && v != "SIN AUTORIZACION"
}

// MedicationPendingRepo consulta la tabla medication_pending en MySQL.
type MedicationPendingRepo struct {
	db *sql.DB
}

func NewMedicationPendingRepo(db *sql.DB) *MedicationPendingRepo {
	return &MedicationPendingRepo{db: db}
}

// FindByDocument devuelve el primer registro activo con saldo > 0 para el documento dado.
// Normaliza la cédula (solo dígitos) antes de buscar.
func (r *MedicationPendingRepo) FindByDocument(ctx context.Context, cedula string) (*MedicationPendingRecord, error) {
	normalized := digitsOnly(cedula)
	if normalized == "" {
		return nil, fmt.Errorf("medication_pending: cédula vacía")
	}

	row := r.db.QueryRowContext(ctx, `
		SELECT id, cedula, paciente, eps, medicamento, nombre_comercial,
		       concentracion, forma_farmaceutica, saldo_actual, fecha_vencimiento,
		       COALESCE(numero_autorizacion, '')
		FROM medication_pending
		WHERE activo = 1 AND saldo_actual > 0 AND cedula = ?
		ORDER BY id ASC
		LIMIT 1`, normalized)

	var rec MedicationPendingRecord
	err := row.Scan(
		&rec.ID, &rec.Cedula, &rec.Paciente, &rec.EPS,
		&rec.Medicamento, &rec.NombreComercial,
		&rec.Concentracion, &rec.FormaFarmaceutica,
		&rec.SaldoActual, &rec.FechaVencimiento,
		&rec.NumeroAutorizacion,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("medication_pending find: %w", err)
	}
	return &rec, nil
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
