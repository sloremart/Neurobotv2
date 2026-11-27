package nectar

import (
	"context"
	"errors"
	"testing"
)

type fakeLineStore struct {
	lines map[int64]Line
	reads int
	err   error
}

func (f *fakeLineStore) LineOf(_ context.Context, id int64) (Line, bool, error) {
	f.reads++
	if f.err != nil {
		return "", false, f.err
	}
	l, ok := f.lines[id]
	return l, ok, nil
}

func (f *fakeLineStore) Register(_ context.Context, id int64, l Line) error {
	if f.err != nil {
		return f.err
	}
	f.lines[id] = l
	return nil
}

func TestLineRegistry_UnknownIsMain(t *testing.T) {
	r := NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}})
	if got := r.Line(context.Background(), 1); got != LineMain {
		t.Fatalf("got %q, want main", got)
	}
}

func TestLineRegistry_RegisterThenLineFromCache(t *testing.T) {
	st := &fakeLineStore{lines: map[int64]Line{}}
	r := NewLineRegistry(st)
	if err := r.Register(context.Background(), 7, LineLegacy); err != nil {
		t.Fatal(err)
	}
	if got := r.Line(context.Background(), 7); got != LineLegacy {
		t.Fatalf("got %q, want legacy", got)
	}
	if st.reads != 0 {
		t.Fatalf("reads = %d, want 0", st.reads)
	}
}

func TestLineRegistry_StoreErrorIsMainNotPanic(t *testing.T) {
	r := NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}, err: errors.New("boom")})
	if got := r.Line(context.Background(), 1); got != LineMain {
		t.Fatalf("got %q, want main", got)
	}
	if r.Known(1) {
		t.Fatal("un error no debe quedar en caché")
	}
}

func TestLineRegistry_KnownOnlyAfterRegisterOrLookup(t *testing.T) {
	st := &fakeLineStore{lines: map[int64]Line{5: LineLegacy}}
	r := NewLineRegistry(st)
	if r.Known(5) {
		t.Fatal("no debe ser conocida antes de consultar")
	}
	if got := r.Line(context.Background(), 5); got != LineLegacy {
		t.Fatalf("got %q", got)
	}
	if !r.Known(5) {
		t.Fatal("debe ser conocida tras la consulta")
	}
	_ = r.Register(context.Background(), 6, LineMain)
	if !r.Known(6) {
		t.Fatal("debe ser conocida tras Register")
	}
	if r.Known(99) {
		t.Fatal("99 no es conocida")
	}
}

func TestLineRegistry_LookupDistinguishesMissingFromError(t *testing.T) {
	r := NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}})
	if l, err := r.Lookup(context.Background(), 1); err != nil || l != LineMain {
		t.Fatalf("sin registro: %q %v", l, err)
	}
	r = NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}, err: errors.New("boom")})
	if _, err := r.Lookup(context.Background(), 1); err == nil {
		t.Fatal("el error de lectura debe salir")
	}
}
