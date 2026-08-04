package siesa

import (
	"strings"
	"testing"
)

// Auditoría queries P1: la query de slots es la más ejecutada del bot y no tenía cota de filas —
// devolvía TODOS los slots libres de 90 días para que Go mostrara 5. El SELECT lleva TOP con un
// cap generoso (no cambia el comportamiento hoy) y conserva ORDER BY pmd.Fecha, así la paginación
// por afterDate sigue alcanzando cualquier cola que el cap deje fuera.
func TestBuildAvailableSlotsQuery_CapsRows(t *testing.T) {
	q, args := buildAvailableSlotsQuery(8, "", nil)
	if !strings.Contains(q, "SELECT TOP (1500)") {
		t.Errorf("la query debe llevar TOP (1500); query:\n%s", q)
	}
	if !strings.Contains(q, "ORDER BY pmd.Fecha") {
		t.Errorf("la query debe conservar ORDER BY pmd.Fecha (el TOP recorta la cola, no el frente)")
	}
	if len(args) != 1 {
		t.Errorf("args = %d, want 1 (solo asunto)", len(args))
	}
}

func TestBuildAvailableSlotsQuery_ParamsPreserved(t *testing.T) {
	q, args := buildAvailableSlotsQuery(8, "2026-08-10", []int{20, 23})
	if len(args) != 4 { // asunto + 2 médicos + afterDate
		t.Fatalf("args = %d, want 4", len(args))
	}
	if !strings.Contains(q, "pmd.Medico IN (@p2, @p3)") {
		t.Errorf("filtro de médicos mal armado:\n%s", q)
	}
	if !strings.Contains(q, "DATEADD(DAY, 1, CAST(@p4 AS DATE))") {
		t.Errorf("paginación por afterDate mal armada:\n%s", q)
	}
}
