//go:build integration

// Integration test for the patient-creation flow against a real SIESA database.
// Run with: go test -tags integration -run TestCreatePatient -v ./internal/repository/siesa/
// Requires SIESA_DSN, e.g.
//
//	sqlserver://sa:pass@host.docker.internal:1433?database=ZeusSalud_Neuro&encrypt=disable
package siesa

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SIESA_DSN")
	if dsn == "" {
		t.Skip("SIESA_DSN not set")
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

// TestCreatePatientFlow exercises the real Create + read-back + Option-B updates,
// then cleans up the test row. Validates the INSERT (types, OUTPUT INTO), the
// NombreCompleto trigger, the sis_muni join, and the contract/municipality updates.
func TestCreatePatientFlow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	repo := NewPatientRepo(db)
	ctx := context.Background()

	const docType, doc = "CC", "999000777"
	// Pre-clean any leftover from a previous failed run.
	_, _ = db.ExecContext(ctx, `DELETE FROM sis_paci WHERE tipo_id=@p1 AND num_id=@p2`, docType, doc)

	birth, _ := time.Parse("2006-01-02", "1990-05-15")
	in := domain.CreatePatientInput{
		DocumentType: docType, DocumentNumber: doc, DocumentIssuePlace: "Acacías",
		FirstName: "JUAN", SecondName: "TEST", FirstSurname: "PRUEBA", SecondSurname: "BOT",
		BirthDate: birth, Gender: "M",
		Phone: "3001234567", Phone2: "3019998877", Email: "test.bot@example.com",
		Address: "CALLE 1 #2-3", DepartmentCode: "50", CityCode: "006", Zone: "U",
		EntityCode: "EPS005", AffiliationType: "1", UserType: "1",
		MaritalStatus: "1", BloodType: "O+", Barrio: "6457", CountryCode: "170",
	}

	id, err := repo.Create(ctx, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Logf("created autoid=%s", id)
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM sis_paci WHERE autoid=@p1`, id); err != nil {
			t.Logf("cleanup failed for autoid=%s: %v", id, err)
		}
	}()

	p, err := repo.FindByID(ctx, id)
	if err != nil || p == nil {
		t.Fatalf("FindByID: %v (p=%v)", err, p)
	}
	t.Logf("read back: name=%q dep=%s muni=%s (%s) entity=%s contrato=%q",
		p.FullName, p.DepartmentCode, p.CityCode, p.CityName, p.EntityCode, p.ContractCode)

	if p.DepartmentCode != "50" || p.CityCode != "006" {
		t.Errorf("municipality mismatch: dep=%s muni=%s want 50/006", p.DepartmentCode, p.CityCode)
	}
	if p.CityName == "" {
		t.Errorf("CityName empty — sis_muni join failed")
	}
	if p.EntityCode != "EPS005" {
		t.Errorf("entity=%s want EPS005", p.EntityCode)
	}
	if p.FullName == "" {
		t.Errorf("FullName empty — NombreCompleto trigger did not fire")
	}

	// Verify celular == telefono and tipo_sangre persisted (not exposed via FindByID).
	var celular, telefono, tipoSangre, codPais string
	var barrio, tipoAfilia, idPais int64
	if err := db.QueryRowContext(ctx,
		`SELECT ISNULL(celular,''), ISNULL(telefono,''), ISNULL(tipo_sangre,''), ISNULL(barrio,0), ISNULL(tipo_afilia,0), ISNULL(IdPais,0), ISNULL(codPaisResidencia,'') FROM sis_paci WHERE autoid=@p1`, id).
		Scan(&celular, &telefono, &tipoSangre, &barrio, &tipoAfilia, &idPais, &codPais); err != nil {
		t.Fatalf("read fields: %v", err)
	}
	t.Logf("celular=%q tel=%q rh=%q barrio=%d afilia=%d IdPais=%d codPais=%q", celular, telefono, tipoSangre, barrio, tipoAfilia, idPais, codPais)
	if idPais != 50 || codPais != "50" {
		t.Errorf("country codes: IdPais=%d codPais=%q want 50/\"50\"", idPais, codPais)
	}
	if celular != telefono || celular == "" {
		t.Errorf("celular should equal telefono: celular=%q telefono=%q", celular, telefono)
	}
	if tipoSangre != "O+" {
		t.Errorf("tipo_sangre=%q want O+", tipoSangre)
	}
	if barrio != 6457 {
		t.Errorf("barrio=%d want 6457", barrio)
	}
	if tipoAfilia != 1 {
		t.Errorf("tipo_afilia=%d want 1 (Cotizante)", tipoAfilia)
	}

	// Option B: persist resolved contract (Sanitas MRC Contributivo for Acacías).
	if err := repo.UpdateContract(ctx, id, "6"); err != nil {
		t.Fatalf("UpdateContract: %v", err)
	}
	// Patient edits municipality to Villavicencio (50-001).
	if err := repo.UpdateMunicipality(ctx, id, "50", "001"); err != nil {
		t.Fatalf("UpdateMunicipality: %v", err)
	}

	p2, err := repo.FindByID(ctx, id)
	if err != nil || p2 == nil {
		t.Fatalf("FindByID after updates: %v", err)
	}
	t.Logf("after updates: contrato=%q muni=%s (%s)", p2.ContractCode, p2.CityCode, p2.CityName)
	if p2.ContractCode != "6" {
		t.Errorf("contract not persisted: got %q want 6", p2.ContractCode)
	}
	if p2.CityCode != "001" {
		t.Errorf("municipality not updated: got %s want 001", p2.CityCode)
	}
}
