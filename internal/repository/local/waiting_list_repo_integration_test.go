//go:build integration

// Tests de integración de WaitingListRepo contra MySQL real. Validan el SQL del claim-then-send
// (MarkNotified, N-33) y el round-trip de Create/FindByID/UpdateStatus, que el resto de la suite
// solo cubre con mocks.
//
// Correr con: make test-integration   (requiere LOCAL_TEST_DSN).
package local

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

func sampleWLEntry(id, phone string) *domain.WaitingListEntry {
	return &domain.WaitingListEntry{
		ID: id, PhoneNumber: phone, PatientID: "PAT-" + id, PatientDoc: "1234567890",
		PatientName: "Test Paciente", PatientAge: 40, PatientGender: "M",
		PatientEntity: "EPS-TEST", ContractCode: "4",
		CupsCode: "890271", CupsName: "EMG", Espacios: 1,
		ProceduresJSON: `[{"code":"890271","qty":1}]`, ProcedureType: "procedimiento",
		Status: "waiting", ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

// TestWaitingListRepo_ClaimThenSend valida la semántica de MarkNotified (N-33): solo la PRIMERA
// llamada reclama (true); la segunda, ya 'notified', devuelve false.
func TestWaitingListRepo_ClaimThenSend(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewWaitingListRepo(db)
	ctx := context.Background()

	id := fmt.Sprintf("itest-wl-%d", time.Now().UnixNano())
	phone := "+57398" + fmt.Sprintf("%07d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM waiting_list WHERE id = ?", id) })

	if err := repo.Create(ctx, sampleWLEntry(id, phone)); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Round-trip.
	got, err := repo.FindByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("find by id: %v (got %v)", err, got)
	}
	if got.CupsCode != "890271" || got.Status != "waiting" {
		t.Errorf("unexpected entry: cups=%s status=%s", got.CupsCode, got.Status)
	}

	// Aparece en la fila de espera del CUP.
	waiting, err := repo.GetWaitingByCups(ctx, "890271", 50)
	if err != nil {
		t.Fatalf("get waiting: %v", err)
	}
	if !containsWL(waiting, id) {
		t.Error("expected entry to appear in GetWaitingByCups")
	}

	// Primer claim → true.
	claimed, err := repo.MarkNotified(ctx, id)
	if err != nil {
		t.Fatalf("mark notified: %v", err)
	}
	if !claimed {
		t.Error("expected first MarkNotified to claim (true)")
	}

	// Segundo claim → false (ya está 'notified', no 'waiting').
	claimed2, err := repo.MarkNotified(ctx, id)
	if err != nil {
		t.Fatalf("mark notified 2: %v", err)
	}
	if claimed2 {
		t.Error("expected second MarkNotified to fail the claim (false) — N-33")
	}

	// UpdateStatus persiste.
	if err := repo.UpdateStatus(ctx, id, "declined"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = repo.FindByID(ctx, id)
	if got.Status != "declined" {
		t.Errorf("expected status declined, got %s", got.Status)
	}
}

func containsWL(list []domain.WaitingListEntry, id string) bool {
	for _, e := range list {
		if e.ID == id {
			return true
		}
	}
	return false
}
