//go:build integration

package local

import (
	"context"
	"testing"
	"time"
)

const attemptsCleanup = `DELETE FROM results_verification_attempts WHERE subject LIKE 'doc:t4-%' OR subject LIKE 'phone:+t4-%'`

func newAttemptsRepo(t *testing.T) *ResultsAttemptsRepo {
	t.Helper()
	db := openLocalTestDB(t)
	t.Cleanup(func() {
		_, _ = db.Exec(attemptsCleanup)
		db.Close()
	})
	_, _ = db.Exec(attemptsCleanup)
	return NewResultsAttemptsRepo(db)
}

func TestAttempts_ThreeFailuresLock24h(t *testing.T) {
	r := newAttemptsRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

	// Mismo documento, teléfonos distintos.
	for i, p := range []string{"+t4-a1", "+t4-a2", "+t4-a3"} {
		_, locked, err := r.RecordFailure(ctx, "t4-docA", p, now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if locked != (i == 2) {
			t.Fatalf("fallo %d: locked=%v", i+1, locked)
		}
	}
	if ok, _ := r.IsLocked(ctx, "t4-docA", "+t4-nuevo", now.Add(time.Hour)); !ok {
		t.Fatal("el documento debe quedar bloqueado con otro teléfono")
	}

	// Mismo teléfono, documentos distintos.
	for _, d := range []string{"t4-d1", "t4-d2", "t4-d3"} {
		if _, _, err := r.RecordFailure(ctx, d, "+t4-b", now); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := r.IsLocked(ctx, "t4-otro", "+t4-b", now.Add(time.Hour)); !ok {
		t.Fatal("el teléfono debe quedar bloqueado con otro documento")
	}
	if ok, _ := r.IsLocked(ctx, "t4-otro", "+t4-libre", now.Add(time.Hour)); ok {
		t.Fatal("un documento y teléfono sin fallos no deben estar bloqueados")
	}
}

func TestAttempts_WindowResetsAfter24h(t *testing.T) {
	r := newAttemptsRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if _, _, err := r.RecordFailure(ctx, "t4-docW", "+t4-w", now); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(25 * time.Hour)
	attempt, locked, err := r.RecordFailure(ctx, "t4-docW", "+t4-w", later)
	if err != nil || locked || attempt != 1 {
		t.Fatalf("pasada la ventana el conteo reinicia: attempt=%d locked=%v err=%v", attempt, locked, err)
	}
	if _, locked, _ = r.RecordFailure(ctx, "t4-docW", "+t4-w", later); locked {
		t.Fatal("2.º fallo de la nueva ventana no bloquea")
	}
	if _, locked, _ = r.RecordFailure(ctx, "t4-docW", "+t4-w", later); !locked {
		t.Fatal("3.er fallo de la nueva ventana bloquea")
	}
}

func TestAttempts_LockExpires(t *testing.T) {
	r := newAttemptsRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, _, err := r.RecordFailure(ctx, "t4-docE", "+t4-e", now); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := r.IsLocked(ctx, "t4-docE", "+t4-e", now.Add(23*time.Hour)); !ok {
		t.Fatal("a las 23 h sigue bloqueado")
	}
	if ok, _ := r.IsLocked(ctx, "t4-docE", "+t4-e", now.Add(24*time.Hour+time.Second)); ok {
		t.Fatal("pasadas las 24 h el bloqueo vence")
	}
}

func TestAttempts_ResetClearsDoc(t *testing.T) {
	r := newAttemptsRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, _, err := r.RecordFailure(ctx, "t4-docR", "", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Reset(ctx, "t4-docR"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.IsLocked(ctx, "t4-docR", "", now); ok {
		t.Fatal("Reset debe limpiar el documento")
	}
}

func TestAttempts_AttemptIsMaxOfDocAndPhone(t *testing.T) {
	r := newAttemptsRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	// El teléfono ya lleva un fallo con otro documento: el primer intento de docM es el 2.º.
	if a, _, err := r.RecordFailure(ctx, "t4-docOther", "+t4-m", now); err != nil || a != 1 {
		t.Fatalf("attempt=%d err=%v", a, err)
	}
	for i, want := range []int{2, 3, 4} {
		a, locked, err := r.RecordFailure(ctx, "t4-docM", "+t4-m", now.Add(time.Duration(i)*time.Minute))
		if err != nil || a != want || locked != (want >= 3) {
			t.Fatalf("intento %d: attempt=%d locked=%v err=%v", want, a, locked, err)
		}
	}
	// Sin teléfono solo cuenta el documento.
	if a, _, err := r.RecordFailure(ctx, "t4-docSolo", "", now); err != nil || a != 1 {
		t.Fatalf("sin teléfono: attempt=%d err=%v", a, err)
	}
}

func TestAttempts_LockDoesNotSlide(t *testing.T) {
	r := newAttemptsRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, _, err := r.RecordFailure(ctx, "t4-docS", "+t4-s", now); err != nil {
			t.Fatal(err)
		}
	}
	// Un intento más 20 h después, con el bloqueo vigente, no lo alarga.
	if a, locked, err := r.RecordFailure(ctx, "t4-docS", "+t4-s", now.Add(20*time.Hour)); err != nil || a != 4 || !locked {
		t.Fatalf("attempt=%d locked=%v err=%v", a, locked, err)
	}
	if ok, _ := r.IsLocked(ctx, "t4-docS", "+t4-s", now.Add(24*time.Hour+time.Second)); ok {
		t.Fatal("el bloqueo se deslizó con el intento hecho estando bloqueado")
	}
}

func TestAttempts_ReleaseDecrementsAndUnlocksPhone(t *testing.T) {
	r := newAttemptsRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, _, err := r.RecordFailure(ctx, "t4-docL"+string(rune('a'+i)), "+t4-l", now); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := r.IsLocked(ctx, "t4-docNuevo", "+t4-l", now); !ok {
		t.Fatal("tres fallos bloquean el teléfono")
	}
	if err := r.Release(ctx, "+t4-l", now); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.IsLocked(ctx, "t4-docNuevo", "+t4-l", now); ok {
		t.Fatal("Release por debajo del tope debe quitar el bloqueo")
	}
	// Quedan 2: el siguiente intento es el 3.º.
	if a, _, err := r.RecordFailure(ctx, "t4-docNuevo", "+t4-l", now); err != nil || a != 3 {
		t.Fatalf("tras Release: attempt=%d err=%v", a, err)
	}
	// Nunca por debajo de 0.
	for i := 0; i < 5; i++ {
		if err := r.Release(ctx, "+t4-l", now); err != nil {
			t.Fatal(err)
		}
	}
	if a, _, err := r.RecordFailure(ctx, "t4-docOtro2", "+t4-l", now); err != nil || a != 1 {
		t.Fatalf("Release no baja de 0: attempt=%d err=%v", a, err)
	}
	if err := r.Release(ctx, "", now); err != nil {
		t.Fatal("teléfono vacío: no-op sin error")
	}
}

func TestHashDocument_NoClearNumber(t *testing.T) {
	h := HashDocument("CC", "123456")
	if len(h) != 64 || h == "CC:123456" {
		t.Fatalf("hash inesperado: %s", h)
	}
}
