//go:build integration

package local

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

const lineTestID int64 = 990000001

func cleanLine(t *testing.T, r *ConversationLineRepo) {
	t.Helper()
	_, _ = r.db.Exec(`DELETE FROM nectar_conversation_line WHERE conversation_id = ?`, lineTestID)
}

func TestConversationLineRepo_RegisterAndRead(t *testing.T) {
	r := NewConversationLineRepo(openLocalTestDB(t))
	cleanLine(t, r)
	t.Cleanup(func() { cleanLine(t, r) })
	ctx := context.Background()

	if _, found, err := r.LineOf(ctx, lineTestID); err != nil || found {
		t.Fatalf("antes de registrar: found=%v err=%v", found, err)
	}
	if err := r.Register(ctx, lineTestID, nectar.LineLegacy); err != nil {
		t.Fatal(err)
	}
	l, found, err := r.LineOf(ctx, lineTestID)
	if err != nil || !found || l != nectar.LineLegacy {
		t.Fatalf("got %q found=%v err=%v", l, found, err)
	}
}

func TestConversationLineRepo_RegisterIsIdempotentAndKeepsFirst(t *testing.T) {
	r := NewConversationLineRepo(openLocalTestDB(t))
	cleanLine(t, r)
	t.Cleanup(func() { cleanLine(t, r) })
	ctx := context.Background()

	if err := r.Register(ctx, lineTestID, nectar.LineLegacy); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ctx, lineTestID, nectar.LineMain); err != nil {
		t.Fatalf("el segundo registro no debe fallar: %v", err)
	}
	l, _, _ := r.LineOf(ctx, lineTestID)
	if l != nectar.LineLegacy {
		t.Fatalf("got %q, want legacy", l)
	}
}
