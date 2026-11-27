//go:build integration

// El contacto de Néctar (migración 045) contra MySQL real: la sesión lo guarda, chat_events y
// escalations lo copian de su sesión al insertar, y las búsquedas "por teléfono" lo casan con y
// sin «+». Correr con: make test-integration (requiere LOCAL_TEST_DSN).
package local

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// contactFixture crea una sesión de Néctar con contacto y dos chat_events + una escalación colgando
// de ella. Devuelve la sesión, su clave y el contacto (E.164 sin «+», como lo da Néctar).
func contactFixture(t *testing.T, db *sql.DB) (id, key, contact string) {
	t.Helper()
	ctx := context.Background()
	sessions := NewSessionRepo(db)
	events := NewEventRepo(db)
	escalations := NewEscalationRepo(db)

	n := time.Now().UnixNano()
	id = fmt.Sprintf("itest-contact-%d", n)
	key = fmt.Sprintf("nectar:%d", n%1000000000)
	contact = fmt.Sprintf("57300%07d", n%10000000)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM chat_events WHERE session_id = ?", id)
		_, _ = db.ExecContext(ctx, "DELETE FROM escalations WHERE session_id = ?", id)
		_, _ = db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	})

	if err := sessions.Create(ctx, &session.Session{
		ID: id, PhoneNumber: key, CurrentState: "X",
		Status: session.StatusActive, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := sessions.SetContactIdentifier(ctx, id, contact); err != nil {
		t.Fatalf("set contact: %v", err)
	}
	if err := events.Insert(ctx, &ChatEvent{SessionID: id, PhoneNumber: key, EventType: "itest_contact", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if err := events.InsertBatch(ctx, []ChatEvent{{SessionID: id, PhoneNumber: key, EventType: "itest_contact_batch", CreatedAt: time.Now()}}); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if err := escalations.Create(ctx, id, key, "X", "", "", ""); err != nil {
		t.Fatalf("create escalation: %v", err)
	}
	return id, key, contact
}

func TestSessionCreate_StoresContactIdentifier_SQL(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	ctx := context.Background()
	sessions := NewSessionRepo(db)
	events := NewEventRepo(db)
	id, key, contact := contactFixture(t, db)

	got, err := sessions.FindByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("find: %v %v", got, err)
	}
	if got.ContactIdentifier != contact {
		t.Fatalf("la columna se lee: got %q", got.ContactIdentifier)
	}
	if active, _ := sessions.FindActiveByPhone(ctx, key); active == nil || active.ContactIdentifier != contact {
		t.Fatalf("FindActiveByPhone también la lee: %+v", active)
	}

	// chat_events y escalations copian el contacto de su sesión.
	var evCopied, escCopied int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_events WHERE session_id = ? AND contact_identifier = ?", id, contact).Scan(&evCopied)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM escalations WHERE session_id = ? AND contact_identifier = ?", id, contact).Scan(&escCopied)
	if evCopied != 2 || escCopied != 1 {
		t.Fatalf("copias: chat_events=%d (quería 2) escalations=%d (quería 1)", evCopied, escCopied)
	}

	// Sin sesión que lo tenga, la columna queda NULL (Bird).
	birdID := id + "-b"
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM chat_events WHERE session_id = ?", birdID) })
	if err := events.Insert(ctx, &ChatEvent{SessionID: birdID, PhoneNumber: "+573001234567", EventType: "itest_contact", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("insert bird event: %v", err)
	}
	var nulls int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_events WHERE session_id = ? AND contact_identifier IS NULL", birdID).Scan(&nulls)
	if nulls != 1 {
		t.Fatalf("en Bird la columna queda NULL, got %d", nulls)
	}
}

func TestFindRecentByPhone_MatchesContact(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	ctx := context.Background()
	sessions := NewSessionRepo(db)
	id, key, contact := contactFixture(t, db)

	for _, phone := range []string{contact, "+" + contact, key} {
		recent, err := sessions.FindRecentByPhone(ctx, phone, 5)
		if err != nil || len(recent) != 1 || recent[0].ID != id || recent[0].ContactIdentifier != contact {
			t.Fatalf("FindRecentByPhone(%q): %+v, err=%v", phone, recent, err)
		}
	}
}

func TestEventsFindByPhone_MatchesContact(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	ctx := context.Background()
	events := NewEventRepo(db)
	_, key, contact := contactFixture(t, db)

	for _, phone := range []string{contact, "+" + contact, key} {
		found, err := events.FindByPhone(ctx, phone, time.Time{}, time.Time{}, "", 50)
		if err != nil || len(found) != 2 {
			t.Fatalf("FindByPhone(%q): %d eventos, err=%v", phone, len(found), err)
		}
		typed, err := events.FindByPhone(ctx, phone, time.Time{}, time.Time{}, "itest_contact_batch", 50)
		if err != nil || len(typed) != 1 {
			t.Fatalf("FindByPhone(%q) con tipo: %d eventos, err=%v", phone, len(typed), err)
		}
	}
}
