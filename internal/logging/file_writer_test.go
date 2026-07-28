package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDailyFileWriter_RollsBySize: al superar el tope, el archivo actual se archiva con sufijo y se
// sigue escribiendo en uno nuevo. Sin esto, un bucle caliente llena el disco (28-jul-2026: 7,4 GB en
// dos horas).
func TestDailyFileWriter_RollsBySize(t *testing.T) {
	dir := t.TempDir()
	w, err := NewDailyFileWriter(dir, "neuro-bot", 0, 200) // tope diminuto
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer func() { _ = w.Close() }()

	line := []byte(strings.Repeat("x", 80) + "\n")
	for i := 0; i < 10; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	date := time.Now().Format("2006-01-02")
	archived, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.*.log", date)))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(archived) == 0 {
		t.Fatal("no se archivó ningún archivo: el tope de tamaño no se aplicó")
	}

	// Ningún archivo debe exceder el tope de forma significativa (se rota ANTES de escribir).
	for _, p := range append(archived, filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.log", date))) {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.Size() > 200 {
			t.Errorf("%s pesa %d bytes, supera el tope de 200", filepath.Base(p), st.Size())
		}
	}
}

// TestDailyFileWriter_NoRollWithoutLimit: con maxBytes=0 se conserva el comportamiento anterior
// (solo rotación diaria), para no cambiar nada donde no se configure el tope.
func TestDailyFileWriter_NoRollWithoutLimit(t *testing.T) {
	dir := t.TempDir()
	w, err := NewDailyFileWriter(dir, "neuro-bot", 0, 0)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer func() { _ = w.Close() }()

	for i := 0; i < 50; i++ {
		if _, err := w.Write([]byte(strings.Repeat("y", 100) + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	date := time.Now().Format("2006-01-02")
	archived, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.*.log", date)))
	if len(archived) != 0 {
		t.Errorf("se archivaron %d archivos sin tope configurado", len(archived))
	}
}

// TestDailyFileWriter_SizeCountsExistingContent: al reabrir un archivo del día que ya tiene
// contenido, el tope se mide sobre lo que YA pesa. Si se contara desde cero, un proceso que
// reinicia seguido nunca rotaría — que es exactamente el escenario de un bucle de reinicio.
func TestDailyFileWriter_SizeCountsExistingContent(t *testing.T) {
	dir := t.TempDir()
	date := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.log", date))
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 500)), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := NewDailyFileWriter(dir, "neuro-bot", 0, 200)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer func() { _ = w.Close() }()

	if _, err := w.Write([]byte("nueva linea\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	archived, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.*.log", date)))
	if len(archived) == 0 {
		t.Error("no rotó: el tamaño preexistente del archivo no se tuvo en cuenta al reabrirlo")
	}
}

// TestExtractDate_HandlesSizeSuffix: el lector debe reconocer la fecha de un archivo rotado por
// tamaño. Si no, cae en "no se pudo leer la fecha → incluir" y esas partes se escanean siempre,
// aunque queden fuera de la ventana pedida.
func TestExtractDate_HandlesSizeSuffix(t *testing.T) {
	plain := extractDate("/logs/neuro-bot-2026-07-28.log", "neuro-bot")
	rolled := extractDate("/logs/neuro-bot-2026-07-28.02.log", "neuro-bot")

	if plain.IsZero() {
		t.Fatal("no se pudo leer la fecha del archivo sin sufijo")
	}
	if rolled.IsZero() {
		t.Fatal("no se pudo leer la fecha del archivo rotado por tamaño")
	}
	if !plain.Equal(rolled) {
		t.Errorf("fechas distintas para el mismo día: %v vs %v", plain, rolled)
	}
}

// TestReadLogs_IncludesRolledFilesInOrder: las partes rotadas por tamaño deben leerse ANTES del
// archivo vivo, para que "últimas N líneas" siga devolviendo lo más reciente.
func TestReadLogs_IncludesRolledFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	date := time.Now().Format("2006-01-02")

	rolled := filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.01.log", date))
	live := filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.log", date))
	entry := func(msg string) string {
		return fmt.Sprintf(`{"time":"%sT10:00:00.000000000-05:00","level":"INFO","msg":"%s"}`+"\n", date, msg)
	}
	if err := os.WriteFile(rolled, []byte(entry("vieja")), 0o600); err != nil {
		t.Fatalf("write rolled: %v", err)
	}
	if err := os.WriteFile(live, []byte(entry("nueva")), 0o600); err != nil {
		t.Fatalf("write live: %v", err)
	}

	got, err := ReadLogs(dir, "neuro-bot", LogFilter{Lines: 10})
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("devolvió %d líneas, se esperaban 2 (la rotada y la viva)", len(got))
	}
	if !strings.Contains(got[len(got)-1], "nueva") {
		t.Errorf("la última línea es %q, se esperaba la del archivo vivo", got[len(got)-1])
	}
}
