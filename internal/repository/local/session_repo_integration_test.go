//go:build integration

// Tests de integración de SessionRepo contra una BD MySQL real (con migraciones aplicadas,
// incluida la 023 de tracking de escalación). Validan que el SQL de las queries de escalación
// (TouchPatientActivity, TouchAgentActivity, FindEscalatedSessions, IncrementAgentReminders,
// MarkAbandoned) funciona de verdad — no solo la lógica del manager con mocks.
//
// Correr con: make test-integration   (requiere LOCAL_TEST_DSN, p.ej.
//
//	botuser:botpass@tcp(127.0.0.1:3306)/neuro_bot?parseTime=true )
package local

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

func openLocalTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("LOCAL_TEST_DSN")
	if dsn == "" {
		t.Skip("LOCAL_TEST_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

// TestSessionRepo_EscalationLifecycle ejercita el ciclo de vida de una sesión escalada a nivel SQL.
func TestSessionRepo_EscalationLifecycle(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewSessionRepo(db)
	ctx := context.Background()

	id := fmt.Sprintf("itest-esc-%d", time.Now().UnixNano())
	phone := "+57399" + fmt.Sprintf("%07d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id) })

	// 1. Crear sesión activa.
	s := &session.Session{
		ID: id, PhoneNumber: phone, CurrentState: "X",
		Status: session.StatusActive, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 2. Escalar y sellar actividad del paciente.
	if err := repo.MarkEscalated(ctx, id, "team-1"); err != nil {
		t.Fatalf("mark escalated: %v", err)
	}
	if err := repo.TouchPatientActivity(ctx, id, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("touch patient: %v", err)
	}

	// 3. FindEscalatedSessions debe traerla: paciente reciente, agente sin responder, 0 recordatorios.
	list, err := repo.FindEscalatedSessions(ctx)
	if err != nil {
		t.Fatalf("find escalated: %v", err)
	}
	got := findEsc(list, id)
	if got == nil {
		t.Fatal("expected escalated session in result")
	}
	if got.LastAgentMsg != nil {
		t.Errorf("expected nil LastAgentMsg before agent reply, got %v", got.LastAgentMsg)
	}
	if got.RemindersSent != 0 {
		t.Errorf("expected 0 reminders after patient activity, got %d", got.RemindersSent)
	}

	// 4. Recordatorio + respuesta del agente.
	if err := repo.IncrementAgentReminders(ctx, id); err != nil {
		t.Fatalf("increment reminders: %v", err)
	}
	if err := repo.TouchAgentActivity(ctx, phone); err != nil {
		t.Fatalf("touch agent: %v", err)
	}

	list, _ = repo.FindEscalatedSessions(ctx)
	got = findEsc(list, id)
	if got == nil {
		t.Fatal("expected escalated session after agent activity")
	}
	if got.LastAgentMsg == nil {
		t.Error("expected LastAgentMsg set after agent reply")
	}
	if got.RemindersSent != 1 {
		t.Errorf("expected 1 reminder, got %d", got.RemindersSent)
	}

	// 5. Otro mensaje del paciente reinicia el contador de recordatorios.
	if err := repo.TouchPatientActivity(ctx, id, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("touch patient 2: %v", err)
	}
	list, _ = repo.FindEscalatedSessions(ctx)
	if got = findEsc(list, id); got == nil || got.RemindersSent != 0 {
		t.Errorf("expected reminders reset to 0 after new patient message, got %+v", got)
	}

	// 6. Cerrar (abandonar) → ya no aparece como escalada.
	if err := repo.MarkAbandoned(ctx, id); err != nil {
		t.Fatalf("mark abandoned: %v", err)
	}
	list, _ = repo.FindEscalatedSessions(ctx)
	if findEsc(list, id) != nil {
		t.Error("expected abandoned session to be excluded from escalated list")
	}
}

func findEsc(list []session.EscalatedSession, id string) *session.EscalatedSession {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// TestSessionRepo_UpdateConversationIDByPhone (H2): el UPDATE dirigido cambia SOLO conversation_id
// y NO pisa current_state ni la PII del paciente (eso era el lost-update del webhook outbound).
func TestSessionRepo_UpdateConversationIDByPhone(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewSessionRepo(db)
	ctx := context.Background()

	id := fmt.Sprintf("itest-conv-%d", time.Now().UnixNano())
	phone := "+57397" + fmt.Sprintf("%07d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id) })

	s := &session.Session{
		ID: id, PhoneNumber: phone, CurrentState: "ASK_CLIENT_TYPE",
		Status: session.StatusActive, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simular avance del flujo + PII por el worker (fila con estado y datos).
	loaded, _ := repo.FindByID(ctx, id)
	loaded.CurrentState = "SHOW_SLOTS"
	loaded.PatientName = "Juan Perez"
	if err := repo.Save(ctx, loaded); err != nil {
		t.Fatalf("save: %v", err)
	}

	// El webhook outbound actualiza solo conversation_id.
	if err := repo.UpdateConversationIDByPhone(ctx, phone, "conv-xyz"); err != nil {
		t.Fatalf("update conv id: %v", err)
	}

	got, _ := repo.FindByID(ctx, id)
	if got.ConversationID != "conv-xyz" {
		t.Errorf("expected conversation_id updated, got %q", got.ConversationID)
	}
	if got.CurrentState != "SHOW_SLOTS" {
		t.Errorf("H2: current_state fue pisado, esperaba SHOW_SLOTS, got %q", got.CurrentState)
	}
	if got.PatientName != "Juan Perez" {
		t.Errorf("H2: PII fue pisada, esperaba Juan Perez, got %q", got.PatientName)
	}
}

// Auditoría ciclo 130 (H130-5): CompleteActiveByPhone es "cierra lo que OCUPA el cupo del teléfono", y
// el cupo lo define el índice único uq_active_phone, que NO mira expires_at. Mientras el SQL filtraba
// expires_at > NOW(), una sesión VENCIDA (típico de una escalación que nadie cerró) sobrevivía a la
// limpieza y seguía bloqueando el INSERT siguiente con ErrActiveSessionExists.
func TestSessionRepo_CompleteActiveByPhone_AlsoClearsExpiredOccupant(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	repo := NewSessionRepo(db)
	ctx := context.Background()

	id := fmt.Sprintf("itest-ph-%d", time.Now().UnixNano())
	phone := "+57398" + fmt.Sprintf("%07d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM sessions WHERE phone_number = ?", phone) })

	// Sesión fantasma: escalada y ya vencida, pero sigue ocupando el cupo único del teléfono.
	phantom := &session.Session{
		ID: id, PhoneNumber: phone, CurrentState: "ESCALATED",
		Status: session.StatusEscalated, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Create(ctx, phantom); err != nil {
		t.Fatalf("create phantom: %v", err)
	}
	// El vencimiento se fuerza en el SERVIDOR: escribirlo desde Go depende de la zona horaria del DSN
	// (sin loc= el driver manda UTC y la fila parece viva 5h más), y entonces el test no probaría nada.
	if _, err := db.ExecContext(ctx,
		"UPDATE sessions SET expires_at = NOW() - INTERVAL 2 HOUR WHERE id = ?", id); err != nil {
		t.Fatalf("forzar vencimiento: %v", err)
	}

	if err := repo.CompleteActiveByPhone(ctx, phone); err != nil {
		t.Fatalf("complete active by phone: %v", err)
	}

	got, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Status != session.StatusCompleted {
		t.Fatalf("la fantasma vencida sigue ocupando el cupo: status=%q, esperaba completed", got.Status)
	}

	// Consecuencia que importa: con el cupo libre, el siguiente INSERT ya no choca.
	fresh := &session.Session{
		ID: id + "-new", PhoneNumber: phone, CurrentState: "SEARCH_SLOTS",
		Status: session.StatusActive, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Create(ctx, fresh); err != nil {
		t.Fatalf("create tras limpiar el cupo: %v", err)
	}
}
