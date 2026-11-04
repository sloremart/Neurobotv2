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

	"github.com/neuro-bot/neuro-bot/internal/notifications"
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
	if err := repo.Upsert(ctx, oldKey, "confirmation", "APT-ITEST", "", "", "", "", "", true, 0, expiresAt); err != nil {
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

// D-04 (migración 047): la respuesta queda pendiente de SIESA en la fila que dejó Resolve, con la
// conversación del webhook; el proceso de fondo la lee, suma intentos y la saca UNA sola vez.
func TestNotificationRepo_SiesaPendingLifecycle(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	n := time.Now().UnixNano()
	phone := fmt.Sprintf("+57301%07d", n%10000000)
	apptID := fmt.Sprintf("APT-SP-%d", n%1000000)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM notification_history WHERE phone = ?", phone)
		_, _ = db.ExecContext(ctx, "DELETE FROM notification_pending WHERE phone = ?", phone)
	})

	if err := repo.Upsert(ctx, phone, "confirmation", apptID, "202611041030", "", "msg-1", "", "", false, 0, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.Resolve(ctx, phone, "confirmed"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := findSiesaPendingFor(t, repo, phone); len(got) != 0 {
		t.Fatalf("una fila resuelta no está pendiente de SIESA: %+v", got)
	}

	row := notifications.SiesaPendingRow{Phone: phone, Type: "confirmation", AppointmentID: apptID, ConversationID: "conv-webhook", Action: "confirm"}
	if again, err := repo.MarkSiesaPending(ctx, row, "confirmed"); err != nil || again {
		t.Fatalf("mark = %v, %v; want primera vez", again, err)
	}
	var historyRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_history WHERE phone = ?", phone).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if historyRows != 1 {
		t.Fatalf("la marca va en la fila de Resolve, no en una nueva: %d filas", historyRows)
	}
	got := findSiesaPendingFor(t, repo, phone)
	if len(got) != 1 {
		t.Fatalf("pendientes = %+v, want 1", got)
	}
	p := got[0]
	if p.Action != "confirm" || p.ConversationID != "conv-webhook" || p.BirdMessageID != "msg-1" || p.Attempts != 0 ||
		p.AppointmentID != apptID || p.AppointmentSlot != "202611041030" {
		t.Fatalf("fila pendiente = %+v", p)
	}
	if p.PendingFor < 0 || p.PendingFor > time.Minute {
		t.Fatalf("PendingFor = %v, want recién marcada", p.PendingFor)
	}

	if err := repo.RecordSiesaAttempt(ctx, p.ID); err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if got := findSiesaPendingFor(t, repo, phone); len(got) != 1 || got[0].Attempts != 1 {
		t.Fatalf("tras un intento = %+v", got)
	}

	ok, err := repo.FinishSiesaPending(ctx, p.ID, "applied")
	if err != nil || !ok {
		t.Fatalf("finish = %v, %v; want true", ok, err)
	}
	if ok, err := repo.FinishSiesaPending(ctx, p.ID, "gave_up"); err != nil || ok {
		t.Fatalf("segundo finish = %v, %v; want false (ya no estaba pendiente)", ok, err)
	}
	var outcome, status string
	if err := db.QueryRowContext(ctx, "SELECT siesa_outcome, status FROM notification_history WHERE id = ?", p.ID).Scan(&outcome, &status); err != nil {
		t.Fatal(err)
	}
	if outcome != "applied" || status != "confirmed" {
		t.Fatalf("outcome=%q status=%q", outcome, status)
	}
	if got := findSiesaPendingFor(t, repo, phone); len(got) != 0 {
		t.Fatalf("la fila aplicada sigue pendiente: %+v", got)
	}

	// Volver a marcar la misma fila avisa que el paciente ya recibió el acuse (f5).
	if again, err := repo.MarkSiesaPending(ctx, row, "confirmed"); err != nil || !again {
		t.Fatalf("segunda marca = %v, %v; want ya pendiente antes", again, err)
	}
}

// Sin la fila de Resolve (la BD falló justo ahí) la marca inserta una, para no perder la respuesta.
func TestNotificationRepo_MarkSiesaPending_InsertsWithoutHistoryRow(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	n := time.Now().UnixNano()
	phone := fmt.Sprintf("nectar:%d", n%1000000000)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM notification_history WHERE phone = ?", phone) })

	row := notifications.SiesaPendingRow{Phone: phone, Type: "confirmation", AppointmentID: "APT-SP-INS", AppointmentSlot: "202611050800", ContactPhone: "+573001112233", Action: "cancel"}
	if _, err := repo.MarkSiesaPending(ctx, row, "cancel_requested"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got := findSiesaPendingFor(t, repo, phone)
	if len(got) != 1 || got[0].Action != "cancel" || got[0].ContactPhone != "+573001112233" || got[0].ConversationID != "" ||
		got[0].AppointmentSlot != "202611050800" {
		t.Fatalf("fila insertada = %+v", got)
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM notification_history WHERE id = ?", got[0].ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancel_requested" {
		t.Fatalf("status = %q", status)
	}
}

func findSiesaPendingFor(t *testing.T, repo *NotificationRepo, phone string) []notifications.SiesaPendingRow {
	t.Helper()
	all, err := repo.FindSiesaPending(context.Background(), 1000)
	if err != nil {
		t.Fatalf("find siesa pending: %v", err)
	}
	var out []notifications.SiesaPendingRow
	for _, r := range all {
		if r.Phone == phone {
			out = append(out, r)
		}
	}
	return out
}
