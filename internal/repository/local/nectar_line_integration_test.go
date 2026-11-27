//go:build integration

// La línea de Néctar (migración 049) contra MySQL real: la sesión la toma del registro de
// conversaciones y chat_events / escalations la copian de su sesión. Correr con: make test-integration.
package local

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

const lineSessConvID int64 = 990000042

// lineSession crea una sesión con la clave dada (con o sin fila en el registro) y limpia al final.
func lineSession(t *testing.T, db *sql.DB, key string, register bool) string {
	t.Helper()
	id, _ := lineSessionObj(t, db, key, register)
	return id
}

// lineSessionObj es lineSession y además devuelve el objeto tal como lo dejó Create (sin releer por ID).
func lineSessionObj(t *testing.T, db *sql.DB, key string, register bool) (string, *session.Session) {
	t.Helper()
	ctx := context.Background()
	id := fmt.Sprintf("itest-line-%d", time.Now().UnixNano())
	_, _ = db.ExecContext(ctx, "DELETE FROM nectar_conversation_line WHERE conversation_id = ?", lineSessConvID)
	if register {
		if err := NewConversationLineRepo(db).Register(ctx, lineSessConvID, nectar.LineLegacy); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM chat_events WHERE session_id = ?", id)
		_, _ = db.ExecContext(ctx, "DELETE FROM escalations WHERE session_id = ?", id)
		_, _ = db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
		_, _ = db.ExecContext(ctx, "DELETE FROM nectar_conversation_line WHERE conversation_id = ?", lineSessConvID)
	})
	s := &session.Session{
		ID: id, PhoneNumber: key, CurrentState: "X",
		Status: session.StatusActive, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := NewSessionRepo(db).Create(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}
	return id, s
}

func nullLine(t *testing.T, db *sql.DB, query string, args ...interface{}) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

func TestSessionCreate_CopiesLineFromRegistry(t *testing.T) {
	db := openLocalTestDB(t)
	id := lineSession(t, db, fmt.Sprintf("nectar:%d", lineSessConvID), true)

	if v := nullLine(t, db, "SELECT nectar_line FROM sessions WHERE id = ?", id); !v.Valid || v.String != "legacy" {
		t.Fatalf("sessions.nectar_line = %+v, quería legacy", v)
	}
	got, err := NewSessionRepo(db).FindByID(context.Background(), id)
	if err != nil || got == nil || got.NectarLine != "legacy" {
		t.Fatalf("lectura: %+v err=%v", got, err)
	}
}

// El primer turno trabaja con la sesión que devuelve Create, sin releerla: debe traer la línea.
func TestSessionCreate_SetsLineOnReturnedStruct(t *testing.T) {
	db := openLocalTestDB(t)
	nectarKey := fmt.Sprintf("nectar:%d", lineSessConvID)
	cases := []struct {
		name, key string
		register  bool
		want      string
	}{
		{"registrada legacy", nectarKey, true, "legacy"},
		{"sin registro", nectarKey, false, ""},
		{"teléfono de Bird", "+573001234567", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { // cada caso limpia su sesión al terminar
			if _, s := lineSessionObj(t, db, c.key, c.register); s.NectarLine != c.want {
				t.Fatalf("NectarLine = %q, quería %q", s.NectarLine, c.want)
			}
		})
	}
}

func TestSessionCreate_NoRegistryRowLineNull(t *testing.T) {
	db := openLocalTestDB(t)
	id := lineSession(t, db, fmt.Sprintf("nectar:%d", lineSessConvID), false)
	if v := nullLine(t, db, "SELECT nectar_line FROM sessions WHERE id = ?", id); v.Valid {
		t.Fatalf("sin fila de registro debe ser NULL, got %q", v.String)
	}
}

func TestSessionCreate_BirdPhoneLineNull(t *testing.T) {
	db := openLocalTestDB(t)
	// Aunque exista una fila en el registro, un teléfono de Bird no la toma.
	id := lineSession(t, db, "+573001234567", true)
	if v := nullLine(t, db, "SELECT nectar_line FROM sessions WHERE id = ?", id); v.Valid {
		t.Fatalf("Bird debe quedar NULL, got %q", v.String)
	}
	got, _ := NewSessionRepo(db).FindByID(context.Background(), id)
	if got == nil || got.NectarLine != "" {
		t.Fatalf("lectura: %+v", got)
	}
}

func TestChatEvent_CopiesSessionLine(t *testing.T) {
	db := openLocalTestDB(t)
	ctx := context.Background()
	id := lineSession(t, db, fmt.Sprintf("nectar:%d", lineSessConvID), true)
	events := NewEventRepo(db)

	if err := events.Insert(ctx, &ChatEvent{SessionID: id, PhoneNumber: "x", EventType: "itest_line", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := events.InsertBatch(ctx, []ChatEvent{{SessionID: id, PhoneNumber: "x", EventType: "itest_line_batch", CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_events WHERE session_id = ? AND nectar_line = 'legacy'", id).Scan(&n)
	if n != 2 {
		t.Fatalf("eventos con línea legacy = %d, quería 2", n)
	}
}

func TestEscalation_CopiesSessionLine(t *testing.T) {
	db := openLocalTestDB(t)
	id := lineSession(t, db, fmt.Sprintf("nectar:%d", lineSessConvID), true)
	if err := NewEscalationRepo(db).Create(context.Background(), id, "x", "X", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if v := nullLine(t, db, "SELECT nectar_line FROM escalations WHERE session_id = ?", id); !v.Valid || v.String != "legacy" {
		t.Fatalf("escalations.nectar_line = %+v", v)
	}
}

func TestChatEvent_ProactiveTypeTaggedMainWhenOn(t *testing.T) {
	db := openLocalTestDB(t)
	ctx := context.Background()
	phone := fmt.Sprintf("+57311%07d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM chat_events WHERE phone_number = ?", phone) })
	events := NewEventRepo(db)

	for _, on := range []bool{true, false} {
		events.SetProactiveLineTagging(on)
		if err := events.Insert(ctx, &ChatEvent{PhoneNumber: phone, EventType: "reminder_prep_sent", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT nectar_line FROM chat_events WHERE phone_number = ? ORDER BY id", phone)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []sql.NullString
	for rows.Next() {
		var v sql.NullString
		_ = rows.Scan(&v)
		got = append(got, v)
	}
	if len(got) != 2 || !got[0].Valid || got[0].String != "main" || got[1].Valid {
		t.Fatalf("encendido=main, apagado=NULL: got %+v", got)
	}
}

func TestChatEvent_NonProactiveRawPhoneStaysNull(t *testing.T) {
	db := openLocalTestDB(t)
	ctx := context.Background()
	phone := fmt.Sprintf("+57312%07d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM chat_events WHERE phone_number = ?", phone) })
	events := NewEventRepo(db)
	events.SetProactiveLineTagging(true)

	if err := events.Insert(ctx, &ChatEvent{PhoneNumber: phone, EventType: "itest_bird_conversation", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if v := nullLine(t, db, "SELECT nectar_line FROM chat_events WHERE phone_number = ?", phone); v.Valid {
		t.Fatalf("evento no proactivo debe quedar NULL, got %q", v.String)
	}
}
