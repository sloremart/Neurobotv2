//go:build integration

// Test de integración de FindPendingOlderThan (M7) contra MySQL real.
// Correr con: make test-integration   (requiere LOCAL_TEST_DSN).
package local

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestInboxRepo_FindPendingOlderThan: solo devuelve filas 'pending' recibidas hace > N minutos.
func TestInboxRepo_FindPendingOlderThan(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewInboxRepo(db)
	ctx := context.Background()

	tag := fmt.Sprintf("itest-inbox-%d", time.Now().UnixNano())
	oldID := tag + "-old"
	freshID := tag + "-fresh"
	doneID := tag + "-done"
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM message_inbox WHERE id IN (?,?,?)", oldID, freshID, doneID)
	})

	old := time.Now().Add(-20 * time.Minute)
	now := time.Now()
	if _, err := repo.InsertIfNotExists(ctx, oldID, "+57300", `{}`, "text", old); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	if _, err := repo.InsertIfNotExists(ctx, freshID, "+57300", `{}`, "text", now); err != nil {
		t.Fatalf("insert fresh: %v", err)
	}
	if _, err := repo.InsertIfNotExists(ctx, doneID, "+57300", `{}`, "text", old); err != nil {
		t.Fatalf("insert done: %v", err)
	}
	if err := repo.MarkDone(ctx, doneID); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	rows, err := repo.FindPendingOlderThan(ctx, 10)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.ID] = true
	}
	if !got[oldID] {
		t.Error("la fila 'pending' vieja (20 min) debe aparecer")
	}
	if got[freshID] {
		t.Error("la fila 'pending' reciente NO debe aparecer (en vuelo)")
	}
	if got[doneID] {
		t.Error("la fila 'done' NO debe aparecer")
	}
}
