package nectarreports

import (
	"reflect"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

var t0 = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func ptrT(t time.Time) *time.Time { return &t }
func ptrI(i int64) *int64         { return &i }

func esc(id uint64, conv int64, at time.Time) Escalation {
	phone := "nectar:" + itoa(conv)
	return Escalation{ID: id, Phone: phone, EscalatedAt: at}
}

func itoa(i int64) string {
	s := ""
	if i == 0 {
		return "0"
	}
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func cyc(id, conv int64, opened time.Time) nectar.WaitingCycle {
	return nectar.WaitingCycle{ID: id, ConversationID: conv, OpenedAt: opened, ClosedAt: ptrT(opened.Add(time.Hour))}
}

func TestConversationID(t *testing.T) {
	if id, ok := ConversationID("nectar:4410"); !ok || id != 4410 {
		t.Fatalf("got %d %v", id, ok)
	}
	for _, p := range []string{"+573001234567", "nectar:", "nectar:abc", ""} {
		if _, ok := ConversationID(p); ok {
			t.Fatalf("%q no debe ser de Néctar", p)
		}
	}
}

func TestPair_Matching(t *testing.T) {
	tests := []struct {
		name    string
		offset  time.Duration // opened_at - escalated_at
		matched bool
	}{
		{"exacto", 0, true},
		{"90 s antes entra", -90 * time.Second, true},
		{"3 min antes no entra", -3 * time.Minute, false},
		{"encolada 12 h despues", 12 * time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Pair([]Escalation{esc(1, 10, t0)}, []nectar.WaitingCycle{cyc(100, 10, t0.Add(tc.offset))})
			if tc.matched != (len(got) == 1) {
				t.Fatalf("matched=%v got=%+v", tc.matched, got)
			}
			if tc.matched && (got[0].EscalationID != 1 || !reflect.DeepEqual(got[0].CycleIDs, []int64{100})) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestPair_TwoEscalationsSameConversation(t *testing.T) {
	e := []Escalation{esc(2, 10, t0.Add(5*time.Hour)), esc(1, 10, t0)}
	c := []nectar.WaitingCycle{cyc(200, 10, t0.Add(5*time.Hour)), cyc(100, 10, t0)}
	got := Pair(e, c)
	if len(got) != 2 || got[0].EscalationID != 1 || got[1].EscalationID != 2 {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got[0].CycleIDs, []int64{100}) || !reflect.DeepEqual(got[1].CycleIDs, []int64{200}) {
		t.Fatalf("got %+v", got)
	}
}

func TestPair_ReescalationChainedDoesNotInherit(t *testing.T) {
	e := []Escalation{esc(1, 10, t0), esc(2, 10, t0.Add(5*time.Hour))}
	c1 := cyc(100, 10, t0)
	c1.FirstResponseAt = ptrT(t0.Add(time.Minute))
	c1.AgentID = ptrI(7)
	c2 := cyc(200, 10, t0.Add(5*time.Hour))
	c2.PreviousCycleID = ptrI(100)
	got := Pair(e, []nectar.WaitingCycle{c1, c2})
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].FirstResponseAt == nil || !reflect.DeepEqual(got[0].CycleIDs, []int64{100}) {
		t.Fatalf("primera: %+v", got[0])
	}
	if got[1].FirstResponseAt != nil || !reflect.DeepEqual(got[1].CycleIDs, []int64{200}) {
		t.Fatalf("la segunda no debe heredar: %+v", got[1])
	}
}

func TestPair_HandoverSameAttention(t *testing.T) {
	c1 := cyc(100, 10, t0)
	c1.AgentID = ptrI(7)
	c1.ContactMessagesBeforeFirstResponse = 2
	c2 := cyc(200, 10, t0.Add(time.Hour))
	c2.PreviousCycleID = ptrI(100)
	c2.AgentID = ptrI(8)
	c2.FirstResponseAt = ptrT(t0.Add(70 * time.Minute))
	c2.ContactMessagesBeforeFirstResponse = 3
	c3 := cyc(300, 10, t0.Add(2*time.Hour))
	c3.PreviousCycleID = ptrI(200)
	c3.ContactMessagesBeforeFirstResponse = 9
	c3.ClosedAt = nil
	got := Pair([]Escalation{esc(1, 10, t0)}, []nectar.WaitingCycle{c3, c2, c1})
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	a := got[0]
	if !reflect.DeepEqual(a.CycleIDs, []int64{100, 200, 300}) {
		t.Fatalf("cycles %v", a.CycleIDs)
	}
	if a.FirstResponseAt == nil || !a.FirstResponseAt.Equal(*c2.FirstResponseAt) {
		t.Fatalf("first %v", a.FirstResponseAt)
	}
	if a.AgentID == nil || *a.AgentID != 8 {
		t.Fatalf("agent %v", a.AgentID)
	}
	if a.ContactMsgs != 5 {
		t.Fatalf("msgs %d", a.ContactMsgs)
	}
	if a.Closed {
		t.Fatal("el tramo 300 sigue abierto")
	}
}

func TestPair_NobodyAnswered(t *testing.T) {
	c1 := cyc(100, 10, t0)
	c1.AgentID = ptrI(7)
	c2 := cyc(200, 10, t0.Add(time.Hour))
	c2.PreviousCycleID = ptrI(100)
	c2.AgentID = ptrI(8)
	c3 := cyc(300, 10, t0.Add(2*time.Hour))
	c3.PreviousCycleID = ptrI(200)
	got := Pair([]Escalation{esc(1, 10, t0)}, []nectar.WaitingCycle{c1, c2, c3})
	a := got[0]
	if a.FirstResponseAt != nil {
		t.Fatalf("first %v", a.FirstResponseAt)
	}
	if a.AgentID == nil || *a.AgentID != 8 {
		t.Fatalf("agent %v", a.AgentID)
	}
	if !a.Closed {
		t.Fatal("todos cerrados")
	}
}

func TestPair_NoCycleAbsent(t *testing.T) {
	got := Pair([]Escalation{esc(1, 10, t0), {ID: 2, Phone: "+573001112233", EscalatedAt: t0}},
		[]nectar.WaitingCycle{cyc(100, 11, t0)})
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func closedAs(c nectar.WaitingCycle, reason string) nectar.WaitingCycle {
	c.CloseReason = &reason
	return c
}

// Plan 49: una reapertura (resuelta → en espera) trae previous_cycle_id al tramo resuelto, pero es otra
// atención (manual, sin escalación del bot): no se pega a la anterior ni le pasa respuesta, agente ni
// mensajes.
func TestPair_ReopenedAfterResolvedIsNotChained(t *testing.T) {
	c1 := closedAs(cyc(100, 10, t0), "resolved")
	c1.AgentID = ptrI(7)
	reopened := cyc(200, 10, t0.Add(3*time.Hour))
	reopened.PreviousCycleID = ptrI(100)
	reopened.ClosedAt = nil
	reopened.AgentID = ptrI(8)
	reopened.FirstResponseAt = ptrT(t0.Add(3*time.Hour + time.Minute))
	reopened.ContactMessagesBeforeFirstResponse = 4

	got := Pair([]Escalation{esc(1, 10, t0)}, []nectar.WaitingCycle{reopened, c1})
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	a := got[0]
	if !reflect.DeepEqual(a.CycleIDs, []int64{100}) || !a.Closed {
		t.Fatalf("la atención es solo el tramo resuelto y cerrado: %+v", a)
	}
	if a.FirstResponseAt != nil || a.AgentID == nil || *a.AgentID != 7 || a.ContactMsgs != 0 {
		t.Fatalf("nada de la reapertura pasa a la atención: %+v", a)
	}
}

// Néctar no impone un solo hijo por tramo: de un resuelto no se sigue ninguno.
func TestPair_ResolvedWithTwoChildrenChainsNone(t *testing.T) {
	c1 := closedAs(cyc(100, 10, t0), "resolved")
	a := cyc(200, 10, t0.Add(time.Hour))
	a.PreviousCycleID = ptrI(100)
	b := cyc(300, 10, t0.Add(2*time.Hour))
	b.PreviousCycleID = ptrI(100)
	b.FirstResponseAt = ptrT(t0.Add(2*time.Hour + time.Minute))

	got := Pair([]Escalation{esc(1, 10, t0)}, []nectar.WaitingCycle{c1, a, b})
	if len(got) != 1 || !reflect.DeepEqual(got[0].CycleIDs, []int64{100}) || got[0].FirstResponseAt != nil {
		t.Fatalf("got %+v", got)
	}
}

// Traspaso y liberación siguen siendo la misma atención.
func TestPair_TransferredAndReleasedStillChain(t *testing.T) {
	for _, reason := range []string{"transferred", "released"} {
		t.Run(reason, func(t *testing.T) {
			c1 := closedAs(cyc(100, 10, t0), reason)
			c2 := cyc(200, 10, t0.Add(time.Hour))
			c2.PreviousCycleID = ptrI(100)
			c2.FirstResponseAt = ptrT(t0.Add(70 * time.Minute))
			c2.AgentID = ptrI(8)
			got := Pair([]Escalation{esc(1, 10, t0)}, []nectar.WaitingCycle{c1, c2})
			if len(got) != 1 || !reflect.DeepEqual(got[0].CycleIDs, []int64{100, 200}) || got[0].FirstResponseAt == nil {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
