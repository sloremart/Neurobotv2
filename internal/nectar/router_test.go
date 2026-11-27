package nectar

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// routerRig arma un router con dos falsos Néctar (principal y vieja) y un registro en memoria.
type routerRig struct {
	main, legacy *fakeNectar
	reg          *LineRegistry
	router       *LineRouter
}

func newRouterRig(t *testing.T) *routerRig {
	t.Helper()
	r := &routerRig{main: newFakeNectar(t), legacy: newFakeNectar(t)}
	r.reg = NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}})
	names := map[string]string{"p": "plantilla_x"}
	r.router = NewLineRouter(r.reg, map[Line]*Bridge{
		LineMain:   NewBridge(newTestClient(r.main), names),
		LineLegacy: NewBridge(newTestClient(r.legacy), names),
	})
	return r
}

func (r *routerRig) callCounts() (mainCalls, legacyCalls int) {
	r.main.mu.Lock()
	defer r.main.mu.Unlock()
	r.legacy.mu.Lock()
	defer r.legacy.mu.Unlock()
	return len(r.main.calls), len(r.legacy.calls)
}

func TestLineRouter_SendUsesConversationLine(t *testing.T) {
	r := newRouterRig(t)
	if err := r.reg.Register(context.Background(), 1234, LineLegacy); err != nil {
		t.Fatal(err)
	}
	if _, err := r.router.SendText(context.Background(), 1234, "hola"); err != nil {
		t.Fatal(err)
	}
	if m, l := r.callCounts(); m != 0 || l != 1 {
		t.Fatalf("main=%d legacy=%d, quiero 0 y 1", m, l)
	}
	if !r.router.IsLegacy(context.Background(), 1234) {
		t.Fatal("IsLegacy debe ser true")
	}
}

func TestLineRouter_UnregisteredUsesMain(t *testing.T) {
	r := newRouterRig(t)
	if _, err := r.router.SendText(context.Background(), 1234, "hola"); err != nil {
		t.Fatal(err)
	}
	if m, l := r.callCounts(); m != 1 || l != 0 {
		t.Fatalf("main=%d legacy=%d, quiero 1 y 0", m, l)
	}
	if r.router.IsLegacy(context.Background(), 1234) {
		t.Fatal("sin registro no es legacy")
	}
}

func TestLineRouter_OpenWithTemplateAlwaysMainAndRegistersMain(t *testing.T) {
	main := newFakeOpen(t, scriptedReply{status: http.StatusCreated, body: opened201})
	legacy := newFakeOpen(t)
	st := &fakeLineStore{lines: map[int64]Line{}}
	reg := NewLineRegistry(st)
	names := map[string]string{"p": "plantilla_x"}
	mb := NewBridge(NewClient(main.srv.URL, svcKey, reqSecret), names)
	mb.ChannelID = 7
	lb := NewBridge(NewClient(legacy.srv.URL, svcKey, reqSecret), names)
	lb.ChannelID = 9
	router := NewLineRouter(reg, map[Line]*Bridge{LineMain: mb, LineLegacy: lb})

	id, msg, err := router.OpenWithTemplate(context.Background(), "+573001112233", bird.TemplateConfig{ProjectID: "p"})
	if err != nil || id != 77 || msg != "901" {
		t.Fatalf("id=%d msg=%q err=%v", id, msg, err)
	}
	if main.calls() != 1 || legacy.calls() != 0 {
		t.Fatalf("main=%d legacy=%d, quiero 1 y 0", main.calls(), legacy.calls())
	}
	if st.lines[77] != LineMain {
		t.Fatalf("la conversación abierta debe quedar registrada como main: %v", st.lines)
	}
}

func TestLineRouter_SendTemplateOnLegacyRefuses(t *testing.T) {
	r := newRouterRig(t)
	if err := r.reg.Register(context.Background(), 1234, LineLegacy); err != nil {
		t.Fatal(err)
	}
	_, err := r.router.SendTemplate(context.Background(), 1234, bird.TemplateConfig{ProjectID: "p"})
	if !errors.Is(err, ErrTemplateOnLegacy) {
		t.Fatalf("err = %v", err)
	}
	if m, l := r.callCounts(); m != 0 || l != 0 {
		t.Fatalf("no debe llamar a ningún stub: main=%d legacy=%d", m, l)
	}
}

func TestLineRouter_IsNotMineDelegates(t *testing.T) {
	r := newRouterRig(t)
	if !r.router.IsNotMine(ErrNotMine) || r.router.IsNotMine(errors.New("otro")) {
		t.Fatal("IsNotMine debe reconocer solo ErrNotMine")
	}
}

// Sin puente de línea vieja todo es main, aunque el registro guarde filas legacy de antes.
func TestLineRouter_NoLegacyBridgeAlwaysMain(t *testing.T) {
	f := newFakeNectar(t)
	st := &fakeLineStore{lines: map[int64]Line{1234: LineLegacy}}
	router := NewLineRouter(NewLineRegistry(st), map[Line]*Bridge{LineMain: NewBridge(newTestClient(f), nil)})
	if _, err := router.SendText(context.Background(), 1234, "x"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 1 || st.reads != 0 {
		t.Fatalf("calls=%v reads=%d", f.calls, st.reads)
	}
}

// Con solo la línea principal el router da lo mismo que el Bridge directo.
func TestLineRouter_OnlyMainBehavesAsBridge(t *testing.T) {
	ctx := context.Background()
	fd, fr := newFakeNectar(t), newFakeNectar(t)
	direct := NewBridge(newTestClient(fd), map[string]string{"p": "x"})
	router := NewLineRouter(NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}}),
		map[Line]*Bridge{LineMain: NewBridge(newTestClient(fr), map[string]string{"p": "x"})})

	type out struct {
		id  string
		err error
	}
	both := func(f func(bird.NectarBridge) any) {
		t.Helper()
		if a, b := f(direct), f(router); !reflect.DeepEqual(a, b) {
			t.Fatalf("difieren: bridge=%v router=%v", a, b)
		}
	}
	both(func(b bird.NectarBridge) any { id, err := b.SendText(ctx, 1234, "hola"); return out{id, err} })
	both(func(b bird.NectarBridge) any {
		id, err := b.SendButtons(ctx, 1234, "t", []bird.Button{{Text: "a", Payload: "b"}})
		return out{id, err}
	})
	both(func(b bird.NectarBridge) any {
		id, err := b.SendTemplate(ctx, 1234, bird.TemplateConfig{ProjectID: "p"})
		return out{id, err}
	})
	both(func(b bird.NectarBridge) any { ok, why, err := b.Available(ctx, 1234); return []any{ok, why, err} })
	both(func(b bird.NectarBridge) any { return b.Close(ctx, 1234) })
	both(func(b bird.NectarBridge) any { s, err := b.ConversationState(ctx, 1234); return out{s, err} })
	both(func(b bird.NectarBridge) any { ok, err := b.WindowOpen(ctx, 1234); return []any{ok, err} })
	// Mismas llamadas en el mismo orden contra cada falso.
	if !reflect.DeepEqual(fd.calls, fr.calls) {
		t.Fatalf("llamadas distintas: bridge=%v router=%v", fd.calls, fr.calls)
	}
	// Una conversación sin estado en la fake (404): IsNotMine coincide.
	fd.notMine, fr.notMine = true, true
	_, e1 := direct.SendText(ctx, 1234, "x")
	_, e2 := router.SendText(ctx, 1234, "x")
	if direct.IsNotMine(e1) != router.IsNotMine(e2) || !router.IsNotMine(e2) {
		t.Fatalf("IsNotMine difiere: %v %v", e1, e2)
	}
}

// Línea vieja armada y el registro sin responder: error y NINGÚN envío (ni con la clave principal).
func TestLineRouter_LegacyArmedStoreErrorSendsNothing(t *testing.T) {
	r := newRouterRig(t)
	r.reg = NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}, err: errors.New("mysql caído")})
	r.router = NewLineRouter(r.reg, map[Line]*Bridge{
		LineMain:   NewBridge(newTestClient(r.main), nil),
		LineLegacy: NewBridge(newTestClient(r.legacy), nil),
	})
	if _, err := r.router.SendText(context.Background(), 1234, "hola"); err == nil {
		t.Fatal("debía fallar")
	}
	if _, err := r.router.SendTemplate(context.Background(), 1234, bird.TemplateConfig{ProjectID: "p"}); err == nil || errors.Is(err, ErrTemplateOnLegacy) {
		t.Fatalf("err = %v", err)
	}
	if m, l := r.callCounts(); m != 0 || l != 0 {
		t.Fatalf("no debe llamar a ningún stub: main=%d legacy=%d", m, l)
	}
}

// Sin línea vieja el router no consulta el registro en ningún envío.
func TestLineRouter_LegacyOffNeverReadsStore(t *testing.T) {
	f := newFakeNectar(t)
	st := &fakeLineStore{lines: map[int64]Line{}}
	router := NewLineRouter(NewLineRegistry(st), map[Line]*Bridge{LineMain: NewBridge(newTestClient(f), map[string]string{"p": "x"})})
	ctx := context.Background()
	_, _ = router.SendText(ctx, 1234, "a")
	_, _ = router.SendTemplate(ctx, 1234, bird.TemplateConfig{ProjectID: "p"})
	_, _, _ = router.Available(ctx, 1234)
	_ = router.IsLegacy(ctx, 1234)
	if st.reads != 0 {
		t.Fatalf("lecturas del store = %d, quiero 0", st.reads)
	}
}
