//go:build integration

// Rekey contra MySQL real: la fila del pending de un recordatorio fallido pasa de la clave del
// teléfono a la de la conversación de Néctar, sin mover su vencimiento y con su marca unsent (045).
// Correr con: make test-integration (requiere LOCAL_TEST_DSN).
package local

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNotificationRepo_Rekey(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	n := time.Now().UnixNano()
	oldKey := fmt.Sprintf("+57300%07d", n%10000000)
	newKey := fmt.Sprintf("nectar:%d", n%1000000000)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM notification_pending WHERE phone IN (?, ?)", oldKey, newKey)
	})

	expiresAt := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	if err := repo.Upsert(ctx, oldKey, "confirmation", "APT-ITEST", "", "", "", "", true, 0, expiresAt); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.Rekey(ctx, oldKey, newKey, "55", oldKey); err != nil {
		t.Fatalf("rekey: %v", err)
	}

	rows, err := repo.FindAll(ctx)
	if err != nil {
		t.Fatalf("find all: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.Phone == oldKey {
			t.Fatal("no queda fila bajo el teléfono")
		}
		if r.Phone != newKey {
			continue
		}
		found = true
		if r.ConversationID != "55" || r.ContactPhone != oldKey || r.AppointmentID != "APT-ITEST" || !r.Unsent {
			t.Fatalf("fila re-clave = %+v", r)
		}
		if !r.ExpiresAt.Equal(expiresAt) {
			t.Fatalf("expires_at se movió: %v → %v", expiresAt, r.ExpiresAt)
		}
	}
	if !found {
		t.Fatal("la fila queda bajo la clave de la conversación")
	}
}
