//go:build integration

package siesa

import (
	"context"
	"testing"
	"time"
)

// emgCodesForTest = CUPS del Grupo 1 (EMG), espejo de services.FisiatriaEmgCodes (inline para no
// acoplar el paquete siesa a services en el test).
var emgCodesForTest = []string{
	"29120", "930810", "892302", "892301", "930820", "930860",
	"893601", "930801", "29101", "000005", "000006", "000004",
}

// TestFindPendingEmgAppointment_AgainstDB valida, contra la BD real, que FindPendingEmgAppointment
// devuelve la cita futura PENDIENTE ('P') más próxima del paciente que incluye un CUP EMG (G1), y que
// respeta los filtros (estado 'P', fecha>=hoy, contiene EMG). Requiere SIESA_DSN (ver openTestDB).
func TestFindPendingEmgAppointment_AgainstDB(t *testing.T) {
	db := openTestDB(t)
	repo := NewAppointmentRepo(db, "", 0)
	ctx := context.Background()

	// Buscar un paciente con una cita 'P' futura que incluya un CUP EMG (misma condición del repo).
	var autoid string
	if err := db.QueryRowContext(ctx, `
		SELECT TOP 1 CAST(c.autoid AS VARCHAR(20))
		FROM citas c WITH (NOLOCK)
		WHERE c.estado='P' AND c.fecha >= CAST(GETDATE() AS DATE)
		  AND EXISTS (SELECT 1 FROM citas_procedimientos cp WITH (NOLOCK)
		              WHERE cp.id_cita=c.id AND cp.id_procedimiento IN
		              ('29120','930810','892302','892301','930820','930860','893601','930801','29101','000005','000006','000004'))
		ORDER BY c.fecha, c.hora`).Scan(&autoid); err != nil {
		t.Skipf("no hay paciente con cita EMG pendiente futura: %v", err)
	}

	appt, err := repo.FindPendingEmgAppointment(ctx, autoid, emgCodesForTest)
	if err != nil {
		t.Fatalf("FindPendingEmgAppointment: %v", err)
	}
	if appt == nil {
		t.Fatalf("esperaba una cita EMG pendiente para autoid=%s, got nil", autoid)
	}
	if appt.Canceled {
		t.Error("la cita no debe estar cancelada")
	}
	if appt.Date.Before(time.Now().Truncate(24 * time.Hour)) {
		t.Errorf("la cita debe ser futura, got %s", appt.Date.Format("2006-01-02"))
	}
	// Debe contener al menos un CUP EMG entre sus procedimientos (cargados por scanAppointments).
	hasEMG := false
	emgSet := map[string]bool{}
	for _, c := range emgCodesForTest {
		emgSet[c] = true
	}
	for _, p := range appt.Procedures {
		if emgSet[p.CupCode] {
			hasEMG = true
			break
		}
	}
	if !hasEMG {
		t.Errorf("la cita %s no incluye ningún CUP EMG en sus procedimientos: %+v", appt.ID, appt.Procedures)
	}

	// Un paciente inexistente devuelve nil sin error.
	none, err := repo.FindPendingEmgAppointment(ctx, "-1", emgCodesForTest)
	if err != nil {
		t.Fatalf("FindPendingEmgAppointment(inexistente): %v", err)
	}
	if none != nil {
		t.Errorf("esperaba nil para paciente inexistente, got %+v", none)
	}
}
