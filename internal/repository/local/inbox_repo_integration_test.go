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

// TestInboxRepo_PoisonExhausted: solo las filas 'pending' que agotaron los intentos pasan a
// 'poisoned'; las que aún tienen presupuesto y las ya 'done' no se tocan.
func TestInboxRepo_PoisonExhausted(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewInboxRepo(db)
	ctx := context.Background()

	tag := fmt.Sprintf("itest-poison-%d", time.Now().UnixNano())
	fresh, exhausted, done := tag+"-fresh", tag+"-exhausted", tag+"-done"
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM message_inbox WHERE id IN (?,?,?)", fresh, exhausted, done)
	})

	now := time.Now()
	for _, id := range []string{fresh, exhausted, done} {
		if _, err := repo.InsertIfNotExists(ctx, id, "+57300", `{}`, "text", now); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, "UPDATE message_inbox SET attempts = 3 WHERE id IN (?,?)", exhausted, done); err != nil {
		t.Fatalf("set attempts: %v", err)
	}
	if err := repo.MarkDone(ctx, done); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	ids, err := repo.PoisonExhausted(ctx, 3)
	if err != nil {
		t.Fatalf("poison: %v", err)
	}
	if len(ids) != 1 || ids[0] != exhausted {
		t.Fatalf("ids = %v, se esperaba solo %s", ids, exhausted)
	}

	statuses := map[string]string{}
	for _, id := range []string{fresh, exhausted, done} {
		var st string
		if err := db.QueryRowContext(ctx, "SELECT status FROM message_inbox WHERE id = ?", id).Scan(&st); err != nil {
			t.Fatalf("select %s: %v", id, err)
		}
		statuses[id] = st
	}
	if statuses[exhausted] != "poisoned" {
		t.Errorf("la fila agotada quedó en %q, se esperaba 'poisoned'", statuses[exhausted])
	}
	if statuses[fresh] != "pending" {
		t.Errorf("la fila con presupuesto quedó en %q, no debía tocarse", statuses[fresh])
	}
	if statuses[done] != "done" {
		t.Errorf("la fila ya procesada quedó en %q, no debía tocarse", statuses[done])
	}
}

// TestInboxRepo_ReplayLoopStopsAfterMaxAttempts reproduce el bucle de reinicio del 28-jul-2026: un
// mensaje cuyo procesamiento mata al proceso se replaya en cada arranque. Simula 4 arranques con la
// misma secuencia que main.go (poison → find → contar intento) y verifica que al cuarto ya no se
// entrega, o sea que el bot deja de morir aunque el mensaje siga siendo veneno.
func TestInboxRepo_ReplayLoopStopsAfterMaxAttempts(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewInboxRepo(db)
	ctx := context.Background()

	id := fmt.Sprintf("itest-loop-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM message_inbox WHERE id = ?", id)
	})
	if _, err := repo.InsertIfNotExists(ctx, id, "+57300", `{}`, "text", time.Now()); err != nil {
		t.Fatalf("insert: %v", err)
	}

	const maxAttempts = 3
	delivered := 0
	for boot := 1; boot <= 4; boot++ {
		if _, err := repo.PoisonExhausted(ctx, maxAttempts); err != nil {
			t.Fatalf("boot %d poison: %v", boot, err)
		}
		rows, err := repo.FindPending(ctx, 500)
		if err != nil {
			t.Fatalf("boot %d find: %v", boot, err)
		}
		var ids []string
		for _, r := range rows {
			if r.ID == id {
				delivered++
				ids = append(ids, r.ID)
			}
		}
		if err := repo.MarkReplayAttempt(ctx, ids); err != nil {
			t.Fatalf("boot %d attempt: %v", boot, err)
		}
		// El "procesamiento" mata al proceso: la fila nunca se marca 'done'.
	}

	if delivered != maxAttempts {
		t.Errorf("el mensaje se entregó %d veces en 4 arranques, se esperaba %d: sin el tope el bot "+
			"queda en bucle de reinicio permanente", delivered, maxAttempts)
	}
}

// TestInboxRepo_FindPendingRespectsLimit: el tope acota cuánto se carga en memoria de una sola vez.
func TestInboxRepo_FindPendingRespectsLimit(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewInboxRepo(db)
	ctx := context.Background()

	tag := fmt.Sprintf("itest-limit-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM message_inbox WHERE id LIKE ?", tag+"%")
	})
	for i := 0; i < 3; i++ {
		if _, err := repo.InsertIfNotExists(ctx, fmt.Sprintf("%s-%d", tag, i), "+57300", `{}`, "text", time.Now()); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	rows, err := repo.FindPending(ctx, 2)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(rows) > 2 {
		t.Errorf("FindPending devolvió %d filas con limit=2", len(rows))
	}
}
