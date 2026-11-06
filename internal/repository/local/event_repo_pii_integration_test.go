//go:build integration

// Retención de PII de chat_events (C-04) contra MySQL real: las filas que pasaron la retención
// quedan con el teléfono enmascarado (salvo las claves nectar:, que no son un teléfono y que el
// dashboard usa para separar líneas), sin las claves de PII en event_data y con el resto intacto;
// las recientes no se tocan. Correr con: make test-integration (requiere LOCAL_TEST_DSN).
package local

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestMaskPIIBatch_SQL(t *testing.T) {
	db := openLocalTestDB(t)
	defer db.Close()
	ctx := context.Background()
	events := NewEventRepo(db)

	n := time.Now().UnixNano()
	sessID := fmt.Sprintf("itest-pii-%d", n)
	phone := fmt.Sprintf("+57300%07d", n%10000000)
	nectarKey := fmt.Sprintf("nectar:%d", n%1000000000)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM chat_events WHERE session_id = ?", sessID)
		_, _ = db.ExecContext(ctx, "DELETE FROM scheduler_runs WHERE task_name = ?", chatEventsPIIMark)
	})

	oldAt := time.Now().AddDate(0, 0, -40)
	data := map[string]interface{}{"name": "Juan Perez", "email": "juan@correo.com", "text": "hola", "type": "confirmation", "patient_id": "PAT1", "error": "boom"}
	for _, ev := range []ChatEvent{
		{SessionID: sessID, PhoneNumber: phone, EventType: "itest_pii_old", EventData: data, CreatedAt: oldAt},
		{SessionID: sessID, PhoneNumber: nectarKey, EventType: "itest_pii_old_nectar", EventData: data, CreatedAt: oldAt},
		{SessionID: sessID, PhoneNumber: phone, EventType: "registration_failed", EventData: data, CreatedAt: oldAt},
		{SessionID: sessID, PhoneNumber: phone, EventType: "itest_pii_recent", EventData: data, CreatedAt: time.Now()},
	} {
		if err := events.Insert(ctx, &ev); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// El contacto de Néctar (copiado de la sesión al insertar) también se enmascara.
	if _, err := db.ExecContext(ctx, "UPDATE chat_events SET contact_identifier = ? WHERE session_id = ?", phone, sessID); err != nil {
		t.Fatal(err)
	}
	maskedPhone := "***" + phone[len(phone)-4:]
	var firstID int64
	if err := db.QueryRowContext(ctx, "SELECT MIN(id) FROM chat_events WHERE session_id = ?", sessID).Scan(&firstID); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().AddDate(0, 0, -30)
	lastID, masked, err := events.MaskPIIBatch(ctx, firstID-1, cutoff, 500)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	if masked < 3 || lastID < firstID+2 {
		t.Fatalf("debía enmascarar las tres filas viejas: lastID=%d masked=%d", lastID, masked)
	}

	type row struct{ phone, contact, name, text, typ, pid, errMsg string }
	read := func(eventType string) row {
		var r row
		if err := db.QueryRowContext(ctx, `SELECT phone_number, COALESCE(contact_identifier,''),
			COALESCE(JSON_UNQUOTE(JSON_EXTRACT(event_data,'$.name')),''),
			COALESCE(JSON_UNQUOTE(JSON_EXTRACT(event_data,'$.text')),''),
			COALESCE(JSON_UNQUOTE(JSON_EXTRACT(event_data,'$.type')),''),
			COALESCE(JSON_UNQUOTE(JSON_EXTRACT(event_data,'$.patient_id')),''),
			COALESCE(JSON_UNQUOTE(JSON_EXTRACT(event_data,'$.error')),'')
			FROM chat_events WHERE session_id = ? AND event_type = ?`, sessID, eventType).Scan(
			&r.phone, &r.contact, &r.name, &r.text, &r.typ, &r.pid, &r.errMsg); err != nil {
			t.Fatalf("read %s: %v", eventType, err)
		}
		return r
	}
	if got := read("itest_pii_old"); got.phone != maskedPhone || got.contact != maskedPhone || got.name != "" ||
		got.text != "" || got.typ != "confirmation" || got.pid != "PAT1" || got.errMsg != "boom" {
		t.Fatalf("fila vieja: %+v", got)
	}
	if got := read("itest_pii_old_nectar"); got.phone != nectarKey || got.name != "" {
		t.Fatalf("fila vieja de Néctar: %+v", got)
	}
	if got := read("registration_failed"); got.errMsg != "" {
		t.Fatalf("registration_failed vieja pierde su error: %+v", got)
	}
	if got := read("itest_pii_recent"); got.phone != phone || got.name != "Juan Perez" || got.text != "hola" {
		t.Fatalf("la fila reciente no se toca: %+v", got)
	}

	// La marca queda guardada para que la pasada siguiente arranque cerca de ella.
	var marks int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_runs WHERE task_name = ?", chatEventsPIIMark).Scan(&marks)
	if marks != 1 {
		t.Fatalf("la marca de hasta dónde se enmascaró no quedó en scheduler_runs")
	}
	if _, err := events.PIIMaskStart(ctx); err != nil {
		t.Fatalf("PIIMaskStart: %v", err)
	}
	// Repetir es idempotente.
	if _, _, err := events.MaskPIIBatch(ctx, firstID-1, cutoff, 500); err != nil {
		t.Fatal(err)
	}
	if got := read("itest_pii_old"); got.phone != maskedPhone || got.contact != maskedPhone {
		t.Fatalf("segunda pasada: %+v", got)
	}
}
