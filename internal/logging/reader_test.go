package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLogFile crea <dir>/neuro-bot-<date>.log con n entradas JSON al estilo slog.
func writeLogFile(t *testing.T, dir, date string, n int) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.log", date))
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, `{"time":"%sT10:00:00.000000000-05:00","level":"INFO","msg":"linea %d"}`+"\n", date, i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestReadLogs_ReturnsLastNLines: el límite devuelve las ÚLTIMAS N líneas, no las primeras.
func TestReadLogs_ReturnsLastNLines(t *testing.T) {
	dir := t.TempDir()
	date := time.Now().Format("2006-01-02")
	writeLogFile(t, dir, date, 1000)

	got, err := ReadLogs(dir, "neuro-bot", LogFilter{Lines: 5})
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("devolvió %d líneas, se esperaban 5", len(got))
	}
	if !strings.Contains(got[4], `"msg":"linea 999"`) {
		t.Errorf("la última línea es %q, se esperaba la 999 (deben ser las últimas, no las primeras)", got[4])
	}
	if !strings.Contains(got[0], `"msg":"linea 995"`) {
		t.Errorf("la primera línea devuelta es %q, se esperaba la 995", got[0])
	}
}

// TestReadLogs_ScanLimitReadsOnlyTail reproduce la condición que dejó el endpoint inutilizable el
// 28-jul-2026: el archivo del día crecido por un bucle caliente (7,4 GB reales). Con el tope de
// escaneo solo se lee la cola, así que el costo depende de lo pedido y no del tamaño del archivo.
func TestReadLogs_ScanLimitReadsOnlyTail(t *testing.T) {
	dir := t.TempDir()
	date := time.Now().Format("2006-01-02")
	writeLogFile(t, dir, date, 5000)

	// Tope diminuto: solo debe alcanzar para las últimas líneas del archivo.
	got, err := ReadLogs(dir, "neuro-bot", LogFilter{Lines: 10, MaxScanBytes: 2048})
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no devolvió nada: el salto al final del archivo dejó fuera todas las líneas")
	}
	if !strings.Contains(got[len(got)-1], `"msg":"linea 4999"`) {
		t.Errorf("la última línea es %q, se esperaba la 4999", got[len(got)-1])
	}
	// Ninguna línea del principio del archivo debe aparecer: no se escaneó esa parte.
	for _, line := range got {
		if strings.Contains(line, `"msg":"linea 0"`) {
			t.Error("apareció una línea del principio del archivo: el tope de escaneo no se aplicó")
		}
	}
}

// TestReadLogs_NoPartialFirstLine: tras saltar al final se cae a mitad de una línea; esa línea
// parcial debe descartarse, porque no es JSON válido y ensuciaría la salida.
func TestReadLogs_NoPartialFirstLine(t *testing.T) {
	dir := t.TempDir()
	date := time.Now().Format("2006-01-02")
	writeLogFile(t, dir, date, 500)

	got, err := ReadLogs(dir, "neuro-bot", LogFilter{Lines: 50, MaxScanBytes: 777}) // corte a mitad de línea
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	for _, line := range got {
		if !strings.HasPrefix(line, `{"time":`) {
			t.Errorf("línea truncada en la salida: %q", line)
		}
	}
}

// TestTailBuffer_BoundsMemory: el acumulador no crece sin límite aunque coincidan muchas más líneas
// de las pedidas. Antes se acumulaban TODAS y se recortaba al final, así que `lines` no acotaba nada.
func TestTailBuffer_BoundsMemory(t *testing.T) {
	b := newTailBuffer(10)
	for i := 0; i < 10000; i++ {
		b.add(fmt.Sprintf("linea %d", i))
	}
	if len(b.lines) > 20 {
		t.Errorf("el buffer retuvo %d líneas con max=10: debía mantenerse acotado", len(b.lines))
	}
	out := b.slice()
	if len(out) != 10 {
		t.Fatalf("slice() devolvió %d líneas, se esperaban 10", len(out))
	}
	if out[9] != "linea 9999" {
		t.Errorf("la última es %q, se esperaba 'linea 9999'", out[9])
	}
}

// TestReadLogs_FiltersStillApply: el tope de escaneo no debe romper el filtrado por nivel.
func TestReadLogs_FiltersStillApply(t *testing.T) {
	dir := t.TempDir()
	date := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, fmt.Sprintf("neuro-bot-%s.log", date))
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		level := "INFO"
		if i%10 == 0 {
			level = "ERROR"
		}
		fmt.Fprintf(&sb, `{"time":"%sT10:00:00.000000000-05:00","level":"%s","msg":"linea %d"}`+"\n", date, level, i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ReadLogs(dir, "neuro-bot", LogFilter{Lines: 100, Level: "error"})
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("devolvió %d líneas ERROR, se esperaban 10", len(got))
	}
	for _, line := range got {
		if !strings.Contains(line, `"level":"ERROR"`) {
			t.Errorf("línea que no es ERROR en la salida: %q", line)
		}
	}
}
