//go:build integration

// Los informes de asesores con Néctar (migración 047) contra MySQL real: upsert de tramos sin pisar
// escalation_id, tramo abierto más viejo, atención de Néctar sobre escalations y estado de la
// sincronización. Correr con: make test-integration (requiere LOCAL_TEST_DSN).
package local

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

func ptrI64(v int64) *int64          { return &v }
func ptrTime(v time.Time) *time.Time { return &v }
func ptrStr(v string) *string        { return &v }

// cycleFixture da un rango de 10 ids de tramo únicos por prueba y los borra al terminar.
func cycleFixture(t *testing.T, db *sql.DB) (base int64) {
	t.Helper()
	base = time.Now().UnixNano() % 1000000000 * 10
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM nectar_waiting_cycles WHERE id BETWEEN ? AND ?", base, base+9)
	})
	return base
}

func TestNectarReportsRepo_UpsertKeepsEscalationID(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewNectarReportsRepo(db)
	base := cycleFixture(t, db)
	conv := base + 1000
	opened := time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC)

	c := nectar.WaitingCycle{ID: base, ConversationID: conv, TeamID: ptrI64(7), OpenedAt: opened}
	if err := repo.UpsertCycles(ctx, []nectar.WaitingCycle{c}); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	if err := repo.SetCycleEscalation(ctx, []int64{base}, 42); err != nil {
		t.Fatalf("set escalation: %v", err)
	}

	resp := opened.Add(3 * time.Minute)
	c2 := c
	c2.AgentID = ptrI64(9)
	c2.FirstResponseAt = ptrTime(resp)
	c2.ClosedAt = ptrTime(resp.Add(time.Hour))
	c2.CloseReason = ptrStr("resolved")
	c2.Backfilled = true
	c2.ContactMessagesBeforeFirstResponse = 4
	if err := repo.UpsertCycles(ctx, []nectar.WaitingCycle{c2}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM nectar_waiting_cycles WHERE id = ?", base).Scan(&n)
	if n != 1 {
		t.Fatalf("una fila, got %d", n)
	}
	got, esc, err := repo.CyclesForConversations(ctx, []int64{conv})
	if err != nil || len(got) != 1 {
		t.Fatalf("lectura: %+v err=%v", got, err)
	}
	g := got[0]
	if g.AgentID == nil || *g.AgentID != 9 || g.FirstResponseAt == nil || !g.FirstResponseAt.Equal(resp) ||
		g.CloseReason == nil || *g.CloseReason != "resolved" || !g.Backfilled ||
		g.ContactMessagesBeforeFirstResponse != 4 || !g.OpenedAt.Equal(opened) || g.TeamID == nil || *g.TeamID != 7 {
		t.Fatalf("datos nuevos no quedaron: %+v", g)
	}
	if e := esc[base]; e == nil || *e != 42 {
		t.Fatalf("escalation_id intacto, got %v", esc[base])
	}
}

func TestNectarReportsRepo_OldestOpenCycle(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewNectarReportsRepo(db)
	base := cycleFixture(t, db)

	// Año 2000: más viejo que cualquier dato real de la base de prueba.
	old := time.Date(2000, 1, 1, 10, 0, 0, 0, time.UTC)
	cycles := []nectar.WaitingCycle{
		{ID: base, ConversationID: base, OpenedAt: old.Add(-time.Hour), ClosedAt: ptrTime(old)}, // cerrado: no cuenta
		{ID: base + 1, ConversationID: base, OpenedAt: old},
		{ID: base + 2, ConversationID: base, OpenedAt: old.Add(time.Hour)},
	}
	if err := repo.UpsertCycles(ctx, cycles); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.OldestOpenCycleOpenedAt(ctx)
	if err != nil || got == nil || !got.Equal(old) {
		t.Fatalf("el abierto más viejo es %v, got %v err=%v", old, got, err)
	}
	if err := repo.UpsertCycles(ctx, []nectar.WaitingCycle{
		{ID: base + 1, ConversationID: base, OpenedAt: old, ClosedAt: ptrTime(old.Add(time.Minute))},
	}); err != nil {
		t.Fatalf("cerrar: %v", err)
	}
	got, _ = repo.OldestOpenCycleOpenedAt(ctx)
	if got == nil || !got.Equal(old.Add(time.Hour)) {
		t.Fatalf("tras cerrar, el más viejo es +1h, got %v", got)
	}
}

func TestNectarReportsRepo_OldestOpenCycle_None(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	repo := NewNectarReportsRepo(db)
	// No se borra nada: en una base con tramos sincronizados la prueba no aplica.
	var open int
	if err := db.QueryRow("SELECT COUNT(*) FROM nectar_waiting_cycles WHERE closed_at IS NULL").Scan(&open); err != nil {
		t.Fatalf("contar abiertos: %v", err)
	}
	if open > 0 {
		t.Skipf("hay %d tramos abiertos ajenos al fixture", open)
	}
	got, err := repo.OldestOpenCycleOpenedAt(context.Background())
	if err != nil || got != nil {
		t.Fatalf("sin abiertos: nil, got %v err=%v", got, err)
	}
}

func TestNectarReportsRepo_AgentsAndState(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewNectarReportsRepo(db)
	id := time.Now().UnixNano() % 1000000000
	key := fmt.Sprintf("itest-%d", id)
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM nectar_agents WHERE id = ?", id)
		_, _ = db.Exec("DELETE FROM nectar_sync_state WHERE name = ?", key)
	})

	a := nectar.ReportAgent{
		ID: id, Name: "Ana", Active: true, Presence: "online",
		Teams: []nectar.ReportTeam{{ID: 1, Name: "Neuro"}}, OpenConversations: 3,
	}
	if err := repo.ReplaceAgents(ctx, []nectar.ReportAgent{a}); err != nil {
		t.Fatalf("agents 1: %v", err)
	}
	var teams sql.NullString
	_ = db.QueryRow("SELECT teams FROM nectar_agents WHERE id = ?", id).Scan(&teams)
	if !strings.Contains(teams.String, "Neuro") {
		t.Fatalf("equipos en JSON: %q", teams.String)
	}
	a.Name, a.Active, a.Presence, a.Teams, a.OpenConversations = "Ana B", false, "offline", nil, 0
	if err := repo.ReplaceAgents(ctx, []nectar.ReportAgent{a}); err != nil {
		t.Fatalf("agents 2: %v", err)
	}
	var gotName, presence string
	var active bool
	var open int
	if err := db.QueryRow("SELECT name, active, presence, open_conversations FROM nectar_agents WHERE id = ?", id).
		Scan(&gotName, &active, &presence, &open); err != nil {
		t.Fatalf("leer agente: %v", err)
	}
	if gotName != "Ana B" || active || presence != "offline" || open != 0 {
		t.Fatalf("agente no se actualizó: %q %v %q %d", gotName, active, presence, open)
	}

	if v, err := repo.GetState(ctx, key); err != nil || v != "" {
		t.Fatalf("estado ausente = vacío, got %q err=%v", v, err)
	}
	if err := repo.SetState(ctx, key, "a"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := repo.SetState(ctx, key, "b"); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	if v, _ := repo.GetState(ctx, key); v != "b" {
		t.Fatalf("estado = b, got %q", v)
	}
}

// escFixture inserta una escalación y devuelve su id; la borra al terminar.
func escFixture(t *testing.T, db *sql.DB, phone, outcome string, escalatedAt time.Time, firstAgent *time.Time, agentID string) uint64 {
	t.Helper()
	var out, fa, ag interface{}
	if outcome != "" {
		out = outcome
	}
	if firstAgent != nil {
		fa = *firstAgent
	}
	if agentID != "" {
		ag = agentID
	}
	res, err := db.Exec(`INSERT INTO escalations (session_id, phone_number, escalated_at, outcome, first_agent_msg_at, agent_id)
		VALUES (?, ?, ?, ?, ?, ?)`, "itest-nr-"+phone, phone, escalatedAt, out, fa, ag)
	if err != nil {
		t.Fatalf("insert escalation: %v", err)
	}
	id, _ := res.LastInsertId()
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM escalations WHERE id = ?", id) })
	return uint64(id)
}

func TestEscalationRepo_ApplyNectarAttention(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewEscalationRepo(db)
	phone := fmt.Sprintf("nectar:%d", time.Now().UnixNano()%1000000000)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	id := escFixture(t, db, phone, "", at, nil, "")
	r1 := at.Add(time.Minute)
	msgs := 3
	if err := repo.ApplyNectarAttention(ctx, id, NectarAttentionUpdate{
		FirstResponseAt: &r1, AgentID: "nectar:5", AgentName: "Ana", ContactMsgs: &msgs,
	}); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	read := func() (fa sql.NullTime, ag, nm sql.NullString, cm sql.NullInt64) {
		_ = db.QueryRow("SELECT first_agent_msg_at, agent_id, agent_name, contact_msgs_before_response FROM escalations WHERE id = ?", id).
			Scan(&fa, &ag, &nm, &cm)
		return
	}
	fa, ag, nm, cm := read()
	if !fa.Valid || !fa.Time.Equal(r1) || ag.String != "nectar:5" || nm.String != "Ana" || cm.Int64 != 3 {
		t.Fatalf("primer apply: %+v %v %v %v", fa, ag, nm, cm)
	}

	// Con la primera respuesta guardada, nada se pisa.
	r2 := at.Add(10 * time.Minute)
	msgs2 := 9
	if err := repo.ApplyNectarAttention(ctx, id, NectarAttentionUpdate{
		FirstResponseAt: &r2, AgentID: "nectar:6", AgentName: "Beto", ContactMsgs: &msgs2,
	}); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	fa, ag, nm, cm = read()
	if !fa.Time.Equal(r1) || ag.String != "nectar:5" || nm.String != "Ana" || cm.Int64 != 3 {
		t.Fatalf("segundo apply no debe pisar: %+v %v %v %v", fa, ag, nm, cm)
	}

	// last_agent_msg_at se llena con la primera respuesta y tampoco se pisa.
	var last sql.NullTime
	_ = db.QueryRow("SELECT last_agent_msg_at FROM escalations WHERE id = ?", id).Scan(&last)
	if !last.Valid || !last.Time.Equal(r1) {
		t.Fatalf("last_agent_msg_at = primera respuesta, got %+v", last)
	}

	// Un valor previo de last_agent_msg_at no se pisa; un update vacío no cambia nada.
	prev := at.Add(30 * time.Minute)
	id3 := escFixture(t, db, phone+"c", "", at, nil, "")
	if _, err := db.Exec("UPDATE escalations SET last_agent_msg_at = ? WHERE id = ?", prev, id3); err != nil {
		t.Fatalf("sembrar last: %v", err)
	}
	if err := repo.ApplyNectarAttention(ctx, id3, NectarAttentionUpdate{FirstResponseAt: &r2}); err != nil {
		t.Fatalf("apply 4: %v", err)
	}
	if err := repo.ApplyNectarAttention(ctx, id3, NectarAttentionUpdate{}); err != nil {
		t.Fatalf("apply vacío 2: %v", err)
	}
	_ = db.QueryRow("SELECT last_agent_msg_at FROM escalations WHERE id = ?", id3).Scan(&last)
	if !last.Valid || !last.Time.Equal(prev) {
		t.Fatalf("last_agent_msg_at previo intacto, got %+v", last)
	}
	id4 := escFixture(t, db, phone+"d", "", at, nil, "")
	if err := repo.ApplyNectarAttention(ctx, id4, NectarAttentionUpdate{}); err != nil {
		t.Fatalf("apply vacío 3: %v", err)
	}
	_ = db.QueryRow("SELECT last_agent_msg_at FROM escalations WHERE id = ?", id4).Scan(&last)
	if last.Valid {
		t.Fatalf("update vacío deja last_agent_msg_at NULL, got %+v", last)
	}

	// Sin primera respuesta el agente SÍ se puede sobrescribir (ruling del controlador).
	id2 := escFixture(t, db, phone+"b", "", at, nil, "nectar:1")
	if err := repo.ApplyNectarAttention(ctx, id2, NectarAttentionUpdate{AgentID: "nectar:2", AgentName: "Caro"}); err != nil {
		t.Fatalf("apply 3: %v", err)
	}
	var ag2, nm2 sql.NullString
	_ = db.QueryRow("SELECT agent_id, agent_name FROM escalations WHERE id = ?", id2).Scan(&ag2, &nm2)
	if ag2.String != "nectar:2" || nm2.String != "Caro" {
		t.Fatalf("agente sin primera respuesta se sobrescribe: %v %v", ag2, nm2)
	}
	// Un update vacío no borra nada.
	if err := repo.ApplyNectarAttention(ctx, id2, NectarAttentionUpdate{}); err != nil {
		t.Fatalf("apply vacío: %v", err)
	}
	_ = db.QueryRow("SELECT agent_id, agent_name FROM escalations WHERE id = ?", id2).Scan(&ag2, &nm2)
	if ag2.String != "nectar:2" || nm2.String != "Caro" {
		t.Fatalf("update vacío no borra: %v %v", ag2, nm2)
	}
}

// NoShow solo cambia expired; completed (un agente pulsó «Resolver»), returned y NULL quedan
// intactos.
func TestEscalationRepo_ApplyNectarAttention_NoShowOnlyExpired(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewEscalationRepo(db)
	phone := fmt.Sprintf("nectar:%d", time.Now().UnixNano()%1000000000)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	expired := escFixture(t, db, phone+"e", "expired", at, nil, "")
	completed := escFixture(t, db, phone+"c", "completed", at, nil, "")
	returned := escFixture(t, db, phone+"r", "returned", at, nil, "")
	open := escFixture(t, db, phone+"o", "", at, nil, "")
	for _, id := range []uint64{expired, completed, returned, open} {
		if err := repo.ApplyNectarAttention(ctx, id, NectarAttentionUpdate{NoShow: true}); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	outcome := func(id uint64) sql.NullString {
		var o sql.NullString
		_ = db.QueryRow("SELECT outcome FROM escalations WHERE id = ?", id).Scan(&o)
		return o
	}
	if o := outcome(expired); o.String != "agent_no_show" {
		t.Fatalf("expired -> agent_no_show, got %v", o)
	}
	if o := outcome(completed); o.String != "completed" {
		t.Fatalf("completed intacto, got %v", o)
	}
	if o := outcome(returned); o.String != "returned" {
		t.Fatalf("returned intacto, got %v", o)
	}
	if o := outcome(open); o.Valid {
		t.Fatalf("NULL intacto, got %v", o)
	}
}

func TestEscalationRepo_ListNectarEscalationsSince(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewEscalationRepo(db)
	n := time.Now().UnixNano() % 1000000000
	now := time.Now().UTC().Truncate(time.Second)

	fa := now.Add(-50 * time.Minute)
	nectarPhone := fmt.Sprintf("nectar:%d", n)
	nec := escFixture(t, db, nectarPhone, "expired", now.Add(-time.Hour), &fa, "nectar:3")
	_ = escFixture(t, db, fmt.Sprintf("+57300%07d", n%10000000), "", now.Add(-time.Hour), nil, "") // Bird
	_ = escFixture(t, db, fmt.Sprintf("nectar:%d", n+1), "", now.Add(-48*time.Hour), nil, "")      // vieja

	list, err := repo.ListNectarEscalationsSince(ctx, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *NectarEscalation
	for i := range list {
		if !strings.HasPrefix(list[i].Phone, "nectar:") {
			t.Fatalf("solo nectar:%%, got %q", list[i].Phone)
		}
		if list[i].EscalatedAt.Before(now.Add(-2 * time.Hour)) {
			t.Fatalf("fuera de ventana: %+v", list[i])
		}
		if list[i].ID == nec {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("falta la escalación de Néctar en %d filas", len(list))
	}
	if found.SessionID != "itest-nr-"+nectarPhone || found.Phone != nectarPhone ||
		!found.EscalatedAt.Equal(now.Add(-time.Hour)) || found.Outcome == nil || *found.Outcome != "expired" ||
		found.FirstAgentMsgAt == nil || !found.FirstAgentMsgAt.Equal(fa) || found.AgentID == nil || *found.AgentID != "nectar:3" {
		t.Fatalf("campos: %+v", found)
	}
}

func TestEscalationRepo_SetResolution(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewEscalationRepo(db)
	phone := fmt.Sprintf("nectar:%d", time.Now().UnixNano()%1000000000)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	closed := escFixture(t, db, phone, "completed", at.Add(-time.Hour), nil, "")
	open := escFixture(t, db, phone, "", at, nil, "")
	if err := repo.SetResolution(ctx, phone, "agent", "bot"); err != nil {
		t.Fatalf("set: %v", err)
	}
	get := func(id uint64) (r, e sql.NullString) {
		_ = db.QueryRow("SELECT resolved_by, escalated_by FROM escalations WHERE id = ?", id).Scan(&r, &e)
		return
	}
	if r, e := get(open); r.String != "agent" || e.String != "bot" {
		t.Fatalf("abierta: %v %v", r, e)
	}
	if r, e := get(closed); r.Valid || e.Valid {
		t.Fatalf("la cerrada no se toca: %v %v", r, e)
	}
	// Vacío se guarda NULL.
	if err := repo.SetResolution(ctx, phone, "", ""); err != nil {
		t.Fatalf("set vacío: %v", err)
	}
	if r, e := get(open); r.Valid || e.Valid {
		t.Fatalf("vacío = NULL: %v %v", r, e)
	}
}

// syncedAt lee synced_at de una fila de la tabla dada.
func syncedAt(t *testing.T, db *sql.DB, table string, id int64) time.Time {
	t.Helper()
	var at time.Time
	//nolint:gosec // G201: table es una constante de la prueba.
	if err := db.QueryRow("SELECT synced_at FROM "+table+" WHERE id = ?", id).Scan(&at); err != nil {
		t.Fatalf("leer synced_at de %s: %v", table, err)
	}
	return at
}

// I1: un upsert que deja la fila IGUAL también mueve synced_at. Sin eso MySQL no toca la fila, el
// TIMESTAMP ON UPDATE no avanza y el dashboard toma por vencido un roster que sí se sincroniza.
func TestNectarReportsRepo_IdenticalUpsertAdvancesSyncedAt(t *testing.T) {
	db := openLocalTestDB(t)
	t.Cleanup(func() { _ = db.Close() }) // antes que los cleanups de los fixtures: corre al final
	ctx := context.Background()
	repo := NewNectarReportsRepo(db)
	base := cycleFixture(t, db)
	agentID := base % 1000000000
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM nectar_agents WHERE id = ?", agentID) })

	c := nectar.WaitingCycle{
		ID: base, ConversationID: base + 1000, TeamID: ptrI64(7),
		OpenedAt: time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC),
	}
	a := nectar.ReportAgent{
		ID: agentID, Name: "Ana", Active: true, Presence: "online",
		Teams: []nectar.ReportTeam{{ID: 1, Name: "Neuro"}}, OpenConversations: 1,
	}
	if err := repo.UpsertCycles(ctx, []nectar.WaitingCycle{c}); err != nil {
		t.Fatalf("upsert tramo 1: %v", err)
	}
	if err := repo.ReplaceAgents(ctx, []nectar.ReportAgent{a}); err != nil {
		t.Fatalf("upsert agente 1: %v", err)
	}
	cycleBefore := syncedAt(t, db, "nectar_waiting_cycles", base)
	agentBefore := syncedAt(t, db, "nectar_agents", agentID)

	time.Sleep(1100 * time.Millisecond) // TIMESTAMP sin fracción: hace falta ≥1 s para verlo moverse
	if err := repo.UpsertCycles(ctx, []nectar.WaitingCycle{c}); err != nil {
		t.Fatalf("upsert tramo 2: %v", err)
	}
	if err := repo.ReplaceAgents(ctx, []nectar.ReportAgent{a}); err != nil {
		t.Fatalf("upsert agente 2: %v", err)
	}
	if got := syncedAt(t, db, "nectar_waiting_cycles", base); !got.After(cycleBefore) {
		t.Errorf("synced_at del tramo debe avanzar: antes %v, después %v", cycleBefore, got)
	}
	if got := syncedAt(t, db, "nectar_agents", agentID); !got.After(agentBefore) {
		t.Errorf("synced_at del agente debe avanzar: antes %v, después %v", agentBefore, got)
	}
}
