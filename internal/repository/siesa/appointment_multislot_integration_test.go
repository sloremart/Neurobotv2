//go:build integration

// Integration test for the multi-slot appointment creation against a real SIESA database.
// Run with: go test -tags integration -run TestCreateMultiSlot -v ./internal/repository/siesa/
// Requires SIESA_DSN, e.g.
//
//	sqlserver://sa:pass@localhost:1433?database=ZeusSalud_Neuro&encrypt=disable
package siesa

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// TestCreateMultiSlot verifica el modelo correcto: UNA cita asociada a N slots contiguos
// (programacion_medico_detalle.IdCita), reclamados atómicamente por repo.Create con Espacios>1.
// Usa una agenda real con slots libres, crea la cita, valida N slots, y limpia todo.
func TestCreateMultiSlot(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	repo := NewAppointmentRepo(db)
	ctx := context.Background()

	const agenda = 315 // TAC (médico 4, asunto 3) — validado
	const espacios = 3

	// Primer slot libre futuro de la agenda.
	var fecha time.Time
	err := db.QueryRowContext(ctx, `
		SELECT MIN(Fecha) FROM programacion_medico_detalle WITH (NOLOCK)
		WHERE IdProgramacionMedico=@p1 AND IdCita IS NULL AND Bloqueado=0 AND SinProgramacion=0 AND Fecha>=GETDATE()`,
		agenda).Scan(&fecha)
	if err != nil {
		t.Fatalf("buscar slot libre: %v", err)
	}
	timeSlot := fecha.Format("200601021504") // YYYYMMDDHHmm

	in := domain.CreateAppointmentInput{
		Date:         fecha,
		TimeSlot:     timeSlot,
		DoctorID:     "4",
		PatientID:    "5", // autoid de prueba (citas no tiene FK a sis_paci)
		Entity:       "EPS005",
		AgendaID:     agenda,
		AgendaSede:   3,
		SubjectType:  3, // TAC
		Espacios:     espacios,
		Observations: "TEST MULTISLOT — borrar",
	}

	appt, err := repo.Create(ctx, in)
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}
	citaID := appt.ID

	// Cleanup garantizado.
	defer func() {
		_, _ = db.ExecContext(ctx, `UPDATE programacion_medico_detalle SET IdCita=NULL WHERE IdCita=@p1`, citaID)
		_, _ = db.ExecContext(ctx, `DELETE FROM log_citas WHERE id_cita_modificada=@p1`, citaID)
		_, _ = db.ExecContext(ctx, `DELETE FROM citas WHERE id=@p1`, citaID)
	}()

	// 1) Debe existir UNA sola fila de cita.
	var nCitas int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM citas WHERE id=@p1`, citaID).Scan(&nCitas); err != nil {
		t.Fatalf("contar cita: %v", err)
	}
	if nCitas != 1 {
		t.Fatalf("esperaba 1 cita, got %d", nCitas)
	}

	// 2) Deben quedar EXACTAMENTE `espacios` slots asociados a esa cita, y contiguos.
	rows, err := db.QueryContext(ctx, `
		SELECT CONVERT(VARCHAR(5),Fecha,108) FROM programacion_medico_detalle
		WHERE IdCita=@p1 ORDER BY Fecha`, citaID)
	if err != nil {
		t.Fatalf("leer slots: %v", err)
	}
	defer rows.Close()
	var horas []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		horas = append(horas, h)
	}
	if len(horas) != espacios {
		t.Fatalf("esperaba %d slots asociados a la cita, got %d (%v)", espacios, len(horas), horas)
	}

	// 3) Los slots deben ser FÍSICAMENTE contiguos: gaps iguales y positivos (la grilla real).
	// Esto valida que repo.Create reclamó filas adyacentes en Fecha (no slots dispersos que
	// solaparían el procedimiento con citas intermedias de otros pacientes).
	mins := make([]int, len(horas))
	for i, h := range horas {
		var hh, mm int
		if _, err := fmt.Sscanf(h, "%d:%d", &hh, &mm); err != nil {
			t.Fatalf("parsear hora %q: %v", h, err)
		}
		mins[i] = hh*60 + mm
	}
	gap := mins[1] - mins[0]
	if gap <= 0 {
		t.Fatalf("gap inicial no positivo: %v", horas)
	}
	for i := 1; i < len(mins); i++ {
		if g := mins[i] - mins[i-1]; g != gap {
			t.Fatalf("slots no contiguos: gap esperado %d, got %d en %v", gap, g, horas)
		}
	}
	t.Logf("OK: 1 cita id=%s con %d slots contiguos (grilla %dmin): %v", citaID, len(horas), gap, horas)
}
