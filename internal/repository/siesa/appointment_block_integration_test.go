//go:build integration

package siesa

import (
	"context"
	"testing"
	"time"
)

// TestPatientDayGrouping_AgainstDB valida, contra la BD real, que FindUpcomingByPatient devuelve
// TODAS las citas activas del paciente para un día (incluso en agendas distintas). Esa es la base
// de la agrupación paciente+día con la que el bot confirma/cancela desde notificación, en reemplazo
// del bloque consecutivo Antares (que se eliminó). Requiere SIESA_DSN (ver openTestDB).
func TestPatientDayGrouping_AgainstDB(t *testing.T) {
	db := openTestDB(t)
	repo := NewAppointmentRepo(db)
	ctx := context.Background()

	// Buscar un paciente con >=2 citas activas el MISMO día futuro (cualquier agenda).
	var autoid string
	var fecha time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT TOP 1 CAST(autoid AS VARCHAR(20)), CAST(fecha AS DATE)
		FROM citas
		WHERE estado <> 'C' AND fecha >= CAST(GETDATE() AS DATE)
		GROUP BY autoid, CAST(fecha AS DATE)
		HAVING COUNT(*) >= 2
		ORDER BY CAST(fecha AS DATE)`).Scan(&autoid, &fecha); err != nil {
		t.Skipf("no hay paciente con >=2 citas futuras el mismo día: %v", err)
	}

	dateStr := fecha.Format("2006-01-02")

	// Verdad de la BD: cuántas citas activas tiene ese paciente ese día (mismo filtro que el repo).
	var expected int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM citas
		WHERE autoid = @p1 AND fecha = @p2 AND estado <> 'C'`,
		autoid, dateStr).Scan(&expected); err != nil {
		t.Fatalf("contar citas del paciente: %v", err)
	}

	// FindUpcomingByPatient + filtro por día = lo que hace GetPatientAppointmentsForDate.
	all, err := repo.FindUpcomingByPatient(ctx, autoid)
	if err != nil {
		t.Fatalf("FindUpcomingByPatient: %v", err)
	}
	got := 0
	agendas := map[int]bool{}
	for _, a := range all {
		if a.Date.Format("2006-01-02") == dateStr {
			got++
			agendas[a.AgendaID] = true
		}
	}

	if got != expected {
		t.Errorf("paciente %s, día %s: agrupación devolvió %d citas, la BD tiene %d (mismo filtro)",
			autoid, dateStr, got, expected)
	}
	if got < 2 {
		t.Errorf("esperaba >=2 citas agrupadas por paciente+día, got %d", got)
	}
	t.Logf("OK: paciente %s tiene %d citas el %s (%d agenda(s)) agrupadas por paciente+día",
		autoid, got, dateStr, len(agendas))
}
