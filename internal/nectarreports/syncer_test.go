package nectarreports

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
)

// ─── fakes ────────────────────────────────────────────────────────────────────

type readCall struct {
	since, until time.Time
	cursor       string
}

type fakeReader struct {
	mu        sync.Mutex
	calls     []readCall
	pages     func(call readCall, n int) (nectar.WaitingCyclesPage, error)
	agents    []nectar.ReportAgent
	agentsErr error
}

func (f *fakeReader) GetWaitingCycles(_ context.Context, since, until time.Time, cursor string) (nectar.WaitingCyclesPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := readCall{since: since, until: until, cursor: cursor}
	f.calls = append(f.calls, c)
	if f.pages == nil {
		return nectar.WaitingCyclesPage{}, nil
	}
	return f.pages(c, len(f.calls))
}

func (f *fakeReader) GetAgents(context.Context) ([]nectar.ReportAgent, error) {
	return f.agents, f.agentsErr
}

func (f *fakeReader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeCycles struct {
	cycles map[int64]nectar.WaitingCycle
	marks  map[int64]uint64
	state  map[string]string
	agents []nectar.ReportAgent
}

func newFakeCycles() *fakeCycles {
	return &fakeCycles{cycles: map[int64]nectar.WaitingCycle{}, marks: map[int64]uint64{}, state: map[string]string{}}
}

func (f *fakeCycles) UpsertCycles(_ context.Context, cs []nectar.WaitingCycle) error {
	for _, c := range cs {
		f.cycles[c.ID] = c
	}
	return nil
}

func (f *fakeCycles) OldestOpenCycleOpenedAt(context.Context) (*time.Time, error) {
	var oldest *time.Time
	for _, c := range f.cycles {
		if c.ClosedAt == nil && (oldest == nil || c.OpenedAt.Before(*oldest)) {
			t := c.OpenedAt
			oldest = &t
		}
	}
	return oldest, nil
}

func (f *fakeCycles) CyclesForConversations(_ context.Context, convIDs []int64) ([]nectar.WaitingCycle, map[int64]*int64, error) {
	want := map[int64]bool{}
	for _, id := range convIDs {
		want[id] = true
	}
	var out []nectar.WaitingCycle
	marks := map[int64]*int64{}
	for _, c := range f.cycles {
		if want[c.ConversationID] {
			out = append(out, c)
			if m, ok := f.marks[c.ID]; ok {
				v := int64(m)
				marks[c.ID] = &v
			} else {
				marks[c.ID] = nil
			}
		}
	}
	return out, marks, nil
}

func (f *fakeCycles) SetCycleEscalation(_ context.Context, ids []int64, escalationID uint64) error {
	for _, id := range ids {
		f.marks[id] = escalationID
	}
	return nil
}

func (f *fakeCycles) ReplaceAgents(_ context.Context, agents []nectar.ReportAgent) error {
	f.agents = agents
	return nil
}

func (f *fakeCycles) GetState(_ context.Context, name string) (string, error) {
	return f.state[name], nil
}

func (f *fakeCycles) SetState(_ context.Context, name, value string) error {
	f.state[name] = value
	return nil
}

type fakeEscalations struct {
	list    []local.NectarEscalation
	since   []time.Time
	applied map[uint64]local.NectarAttentionUpdate
}

func (f *fakeEscalations) ListNectarEscalationsSince(_ context.Context, since time.Time) ([]local.NectarEscalation, error) {
	f.since = append(f.since, since)
	return f.list, nil
}

func (f *fakeEscalations) ApplyNectarAttention(_ context.Context, id uint64, a local.NectarAttentionUpdate) error {
	if f.applied == nil {
		f.applied = map[uint64]local.NectarAttentionUpdate{}
	}
	f.applied[id] = a
	return nil
}

func ptrS(s string) *string { return &s }

func newTestSyncer(r *fakeReader, c *fakeCycles, e *fakeEscalations, now time.Time) *Syncer {
	s := NewSyncer(r, c, e, time.Minute, DataFloor)
	s.now = func() time.Time { return now }
	return s
}

// ─── since y ventanas ─────────────────────────────────────────────────────────

// Sin estado previo la primera pasada lee desde DataFloor hasta ahora y guarda el estado en UTC.
func TestRunOnce_FirstPassUsesDataFloor(t *testing.T) {
	now := DataFloor.Add(time.Hour)
	hs := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	opened := DataFloor.Add(20 * time.Minute)
	r := &fakeReader{pages: func(readCall, int) (nectar.WaitingCyclesPage, error) {
		return nectar.WaitingCyclesPage{Cycles: []nectar.WaitingCycle{cyc(1, 10, opened)}, HistorySince: &hs}, nil
	}}
	c := newFakeCycles()
	s := newTestSyncer(r, c, &fakeEscalations{}, now)

	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || !r.calls[0].since.Equal(DataFloor) || !r.calls[0].until.Equal(now) {
		t.Fatalf("lecturas = %+v, want una desde DataFloor hasta ahora", r.calls)
	}
	if got := c.state[stateLastSeenOpenedAt]; got != "2026-10-30T22:38:00Z" {
		t.Errorf("last_seen_opened_at = %q", got)
	}
	if got := c.state[stateHistorySince]; got != "2026-08-15T00:00:00Z" {
		t.Errorf("history_since = %q", got)
	}
	if got := c.state[stateLastSuccessAt]; got != "2026-10-30T23:18:00Z" {
		t.Errorf("last_success_at = %q", got)
	}
	if _, ok := c.cycles[1]; !ok {
		t.Error("el tramo leído no se guardó")
	}
}

// La segunda pasada parte del tramo abierto más viejo cuando es anterior a last_seen − 10 min,
// y de last_seen − 10 min cuando no lo es. Nunca antes de DataFloor.
func TestRunOnce_SinceIsOldestOfOpenCycleAndLastSeen(t *testing.T) {
	now := DataFloor.Add(10 * time.Hour)
	lastSeen := DataFloor.Add(5 * time.Hour)
	cases := []struct {
		name     string
		openedAt *time.Time
		want     time.Time
	}{
		{"tramo abierto más viejo", ptrT(DataFloor.Add(time.Hour)), DataFloor.Add(time.Hour)},
		{"sin tramos abiertos", nil, lastSeen.Add(-10 * time.Minute)},
		{"tramo abierto reciente", ptrT(lastSeen.Add(-5 * time.Minute)), lastSeen.Add(-10 * time.Minute)},
		{"tramo abierto anterior al piso", ptrT(DataFloor.Add(-48 * time.Hour)), DataFloor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeReader{}
			c := newFakeCycles()
			c.state[stateLastSeenOpenedAt] = lastSeen.Format(time.RFC3339)
			if tc.openedAt != nil {
				c.cycles[5] = nectar.WaitingCycle{ID: 5, ConversationID: 50, OpenedAt: *tc.openedAt}
			}
			if err := newTestSyncer(r, c, &fakeEscalations{}, now).RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != 1 || !r.calls[0].since.Equal(tc.want) {
				t.Fatalf("since = %+v, want %v", r.calls, tc.want)
			}
			// Sin tramos leídos last_seen se conserva.
			if got := c.state[stateLastSeenOpenedAt]; got != lastSeen.Format(time.RFC3339) {
				t.Errorf("last_seen cambió sin tramos nuevos: %q", got)
			}
		})
	}
}

// Un rango de 70 días se parte en dos ventanas de a lo sumo 62 días.
func TestRunOnce_SplitsLongRangeIntoWindows(t *testing.T) {
	now := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	floor := now.Add(-70 * 24 * time.Hour)
	r := &fakeReader{}
	s := NewSyncer(r, newFakeCycles(), &fakeEscalations{}, time.Minute, floor)
	s.now = func() time.Time { return now }
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mid := floor.Add(62 * 24 * time.Hour)
	if len(r.calls) != 2 ||
		!r.calls[0].since.Equal(floor) || !r.calls[0].until.Equal(mid) ||
		!r.calls[1].since.Equal(mid) || !r.calls[1].until.Equal(now) {
		t.Fatalf("ventanas = %+v", r.calls)
	}
}

// Se pagina con next_cursor hasta que llega nulo, y se guarda todo lo leído.
func TestRunOnce_PaginatesUntilCursorIsNil(t *testing.T) {
	now := DataFloor.Add(3 * time.Hour)
	r := &fakeReader{pages: func(_ readCall, n int) (nectar.WaitingCyclesPage, error) {
		page := nectar.WaitingCyclesPage{Cycles: []nectar.WaitingCycle{cyc(int64(n), 10, DataFloor.Add(time.Duration(n)*time.Minute))}}
		if n < 3 {
			page.NextCursor = ptrS("c" + itoa(int64(n)))
		}
		return page, nil
	}}
	c := newFakeCycles()
	if err := newTestSyncer(r, c, &fakeEscalations{}, now).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 || r.calls[0].cursor != "" || r.calls[1].cursor != "c1" || r.calls[2].cursor != "c2" {
		t.Fatalf("lecturas = %+v", r.calls)
	}
	if len(c.cycles) != 3 {
		t.Fatalf("tramos guardados = %d, want 3", len(c.cycles))
	}
	if got := c.state[stateLastSeenOpenedAt]; got != DataFloor.Add(3*time.Minute).Format(time.RFC3339) {
		t.Errorf("last_seen = %q, want la máxima opened_at", got)
	}
}

// Antes de DataFloor no hay nada que leer en waiting-cycles (la ruta rechaza until ≤ since); el
// roster sí se lee.
func TestRunOnce_BeforeFloorSkipsCycles(t *testing.T) {
	r := &fakeReader{agents: []nectar.ReportAgent{{ID: 31, Name: "Laura M."}}}
	c := newFakeCycles()
	if err := newTestSyncer(r, c, &fakeEscalations{}, DataFloor.Add(-time.Hour)).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("no debe leer tramos antes del piso: %+v", r.calls)
	}
	if len(c.agents) != 1 {
		t.Fatal("el roster se lee igual")
	}
}

// ─── emparejamiento y escalaciones ────────────────────────────────────────────

// Review Focus 1: un tramo guardado abierto se vuelve a leer en la pasada siguiente (aunque
// last_seen ya avanzó) y, cuando trae respuesta, completa su escalación.
func TestRunOnce_RereadOpenCycleCompletesEscalation(t *testing.T) {
	escAt := DataFloor.Add(time.Hour)
	opened := escAt.Add(30 * time.Second)
	later := escAt.Add(3 * time.Hour) // otro tramo, de otra conversación: hace avanzar last_seen
	open := nectar.WaitingCycle{ID: 812, ConversationID: 4410, AgentID: ptrI(31), OpenedAt: opened, ContactMessagesBeforeFirstResponse: 1}
	other := cyc(900, 5000, later)

	answered := open
	answered.FirstResponseAt = ptrT(opened.Add(4 * time.Minute))
	answered.ContactMessagesBeforeFirstResponse = 2

	pass := 1
	r := &fakeReader{
		agents: []nectar.ReportAgent{{ID: 31, Name: "Laura M."}},
		pages: func(call readCall, _ int) (nectar.WaitingCyclesPage, error) {
			if pass == 1 {
				return nectar.WaitingCyclesPage{Cycles: []nectar.WaitingCycle{open, other}}, nil
			}
			if call.since.After(opened) {
				return nectar.WaitingCyclesPage{}, nil // la receta no releería el tramo abierto
			}
			return nectar.WaitingCyclesPage{Cycles: []nectar.WaitingCycle{answered, other}}, nil
		},
	}
	c := newFakeCycles()
	e := &fakeEscalations{list: []local.NectarEscalation{{ID: 7, Phone: "nectar:4410", EscalatedAt: escAt}}}

	s := newTestSyncer(r, c, e, escAt.Add(4*time.Hour))
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := e.applied[7]
	if first.FirstResponseAt != nil || first.ContactMsgs != nil || first.NoShow {
		t.Fatalf("pasada 1 (sin respuesta): %+v — los mensajes aún crecen, no se fijan", first)
	}
	if first.AgentID != "nectar:31" || first.AgentName != "Laura M." {
		t.Fatalf("pasada 1: agente asignado = %q %q", first.AgentID, first.AgentName)
	}
	if c.marks[812] != 7 {
		t.Fatalf("el tramo no quedó marcado con la escalación: %v", c.marks)
	}

	pass = 2
	s.now = func() time.Time { return escAt.Add(5 * time.Hour) }
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.calls[len(r.calls)-1].since.Equal(opened) {
		t.Fatalf("pasada 2 since = %v, want la apertura del tramo abierto %v", r.calls[len(r.calls)-1].since, opened)
	}
	if !e.since[1].Equal(opened.Add(-24 * time.Hour)) {
		t.Errorf("escalaciones desde %v, want since − 1 día", e.since[1])
	}
	got := e.applied[7]
	if got.FirstResponseAt == nil || !got.FirstResponseAt.Equal(*answered.FirstResponseAt) {
		t.Fatalf("pasada 2: first response = %v", got.FirstResponseAt)
	}
	if got.ContactMsgs == nil || *got.ContactMsgs != 2 {
		t.Fatalf("pasada 2: contact msgs = %v, want 2", got.ContactMsgs)
	}
	if got.AgentID != "nectar:31" || got.AgentName != "Laura M." || got.NoShow {
		t.Fatalf("pasada 2: %+v", got)
	}
}

// Un agente que no está en el roster deja el nombre vacío (el repo no pisa nada con vacío).
func TestRunOnce_UnknownAgentLeavesNameEmpty(t *testing.T) {
	escAt := DataFloor.Add(time.Hour)
	cy := nectar.WaitingCycle{ID: 1, ConversationID: 44, AgentID: ptrI(99), OpenedAt: escAt, FirstResponseAt: ptrT(escAt.Add(time.Minute))}
	r := &fakeReader{
		agents: []nectar.ReportAgent{{ID: 31, Name: "Laura M."}},
		pages: func(readCall, int) (nectar.WaitingCyclesPage, error) {
			return nectar.WaitingCyclesPage{Cycles: []nectar.WaitingCycle{cy}}, nil
		},
	}
	e := &fakeEscalations{list: []local.NectarEscalation{{ID: 3, Phone: "nectar:44", EscalatedAt: escAt}}}
	if err := newTestSyncer(r, newFakeCycles(), e, escAt.Add(time.Hour)).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.applied[3]; got.AgentID != "nectar:99" || got.AgentName != "" {
		t.Fatalf("got %q %q", got.AgentID, got.AgentName)
	}
}

// NoShow solo con atención cerrada, sin respuesta y la escalación en expired.
func TestRunOnce_NoShowOnlyWhenExpiredAndUnanswered(t *testing.T) {
	escAt := DataFloor.Add(time.Hour)
	closed := escAt.Add(8 * time.Hour)
	cases := []struct {
		name     string
		outcome  *string
		response *time.Time
		closedAt *time.Time
		want     bool
	}{
		{"expired, cerrada sin respuesta → no-show", ptrS("expired"), nil, &closed, true},
		{"expired, con respuesta → no", ptrS("expired"), ptrT(escAt.Add(time.Minute)), &closed, false},
		{"expired, atención abierta → no", ptrS("expired"), nil, nil, false},
		{"completed, cerrada sin respuesta → no", ptrS("completed"), nil, &closed, false},
		{"abierta (NULL), cerrada sin respuesta → no", nil, nil, &closed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cy := nectar.WaitingCycle{
				ID: 1, ConversationID: 44, OpenedAt: escAt,
				FirstResponseAt: tc.response, ClosedAt: tc.closedAt, CloseReason: ptrS("expired"),
			}
			r := &fakeReader{pages: func(readCall, int) (nectar.WaitingCyclesPage, error) {
				return nectar.WaitingCyclesPage{Cycles: []nectar.WaitingCycle{cy}}, nil
			}}
			e := &fakeEscalations{list: []local.NectarEscalation{{ID: 3, Phone: "nectar:44", EscalatedAt: escAt, Outcome: tc.outcome}}}
			if err := newTestSyncer(r, newFakeCycles(), e, escAt.Add(9*time.Hour)).RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, ok := e.applied[3]
			if !ok {
				t.Fatal("la atención no se aplicó")
			}
			if got.NoShow != tc.want {
				t.Fatalf("NoShow = %v, want %v", got.NoShow, tc.want)
			}
			// Cerrada sin respuesta: los mensajes ya no cambian y se fijan.
			if tc.closedAt != nil && (got.ContactMsgs == nil) {
				t.Fatal("atención cerrada: los mensajes antes de la respuesta deben guardarse")
			}
		})
	}
}

// ─── Run ──────────────────────────────────────────────────────────────────────

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// Review Focus 3: con la clave global (403 account_key_required) Run termina tras UNA lectura y
// UN solo log, sin reintentar en cada vuelta.
func TestRun_AccountKeyRequiredStopsAfterOneReadAndOneLog(t *testing.T) {
	logs := captureLogs(t)
	r := &fakeReader{pages: func(readCall, int) (nectar.WaitingCyclesPage, error) {
		return nectar.WaitingCyclesPage{}, nectar.ErrAccountKeyRequired
	}}
	s := NewSyncer(r, newFakeCycles(), &fakeEscalations{}, time.Millisecond, DataFloor)
	s.now = func() time.Time { return DataFloor.Add(time.Hour) }

	done := make(chan struct{})
	go func() { s.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no terminó con account_key_required")
	}
	if n := r.callCount(); n != 1 {
		t.Fatalf("lecturas = %d, want 1", n)
	}
	out := strings.TrimSpace(logs.String())
	if lines := strings.Split(out, "\n"); len(lines) != 1 || !strings.Contains(out, "level=ERROR") ||
		!strings.Contains(out, "clave de cuenta") {
		t.Fatalf("logs = %q, want un solo ERROR que pida la clave de cuenta", out)
	}
}

// Cualquier otro error es un aviso y se reintenta en la vuelta siguiente.
func TestRun_OtherErrorsWarnAndRetry(t *testing.T) {
	logs := captureLogs(t)
	r := &fakeReader{pages: func(readCall, int) (nectar.WaitingCyclesPage, error) {
		return nectar.WaitingCyclesPage{}, errors.New("nectar: GET: status 502")
	}}
	s := NewSyncer(r, newFakeCycles(), &fakeEscalations{}, time.Millisecond, DataFloor)
	s.now = func() time.Time { return DataFloor.Add(time.Hour) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	for r.callCount() < 3 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("no reintentó")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || strings.Contains(out, "level=ERROR") {
		t.Fatalf("logs = %q, want WARN y ningún ERROR", out)
	}
}
