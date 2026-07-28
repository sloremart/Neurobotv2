package logging

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// DefaultMaxScanBytes acota cuánto se lee del final de CADA archivo. Sin tope, una consulta sobre el
// log de un día con un bucle caliente escanea GB enteros: el 28-jul-2026 el archivo del día llegó a
// 7,4 GB y este endpoint —la herramienta pensada justamente para diagnosticar eso— se volvió
// inutilizable, porque el escaneo tardaba más de lo que el proceso sobrevivía entre reinicios.
// 64 MB son ~200k líneas: de sobra para cualquier consulta de "últimas N líneas".
const DefaultMaxScanBytes int64 = 64 << 20

// LogFilter defines filtering criteria for log queries.
type LogFilter struct {
	Lines  int       // Max lines to return (0 = all).
	Level  string    // Filter by level: debug, info, warn, error (empty = all).
	From   time.Time // Start time (zero = no lower bound).
	To     time.Time // End time (zero = no upper bound).
	Search string    // Substring search in "msg" field (empty = no filter).
	Phone  string    // Filter by phone number anywhere in the log line (empty = no filter).
	// MaxScanBytes: cuántos bytes leer del FINAL de cada archivo. 0 = DefaultMaxScanBytes.
	// Negativo = sin tope (archivo entero; solo para exportar deliberadamente).
	MaxScanBytes int64
}

// scanLimit resuelve el tope efectivo de bytes por archivo.
func (f LogFilter) scanLimit() int64 {
	if f.MaxScanBytes == 0 {
		return DefaultMaxScanBytes
	}
	return f.MaxScanBytes
}

// logEntry is the minimal JSON structure we need to parse for filtering.
type logEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// ReadLogs reads log files from dir matching prefix, applies filters, returns matching lines.
func ReadLogs(dir, prefix string, f LogFilter) ([]string, error) {
	files, err := findLogFiles(dir, prefix, f.From, f.To)
	if err != nil {
		return nil, err
	}

	// Acumulador acotado: se conservan solo las últimas f.Lines coincidencias. Antes se acumulaban
	// TODAS y se recortaba al final, así que `lines=15` no acotaba nada de la memoria — un filtro poco
	// selectivo sobre un log grande podía costar cientos de MB DENTRO del proceso del bot, justo
	// cuando el bot ya está en problemas.
	results := newTailBuffer(f.Lines)
	levelUpper := strings.ToUpper(f.Level)

	for _, path := range files {
		if err := readAndFilter(path, levelUpper, f, results); err != nil {
			continue // skip unreadable files
		}
	}

	return results.slice(), nil
}

// tailBuffer conserva las últimas N líneas añadidas sin dejar crecer la memoria. Con max <= 0 no
// acota (caso "todas las líneas", que el handler solo permite explícitamente).
type tailBuffer struct {
	max   int
	lines []string
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{max: limit}
}

func (b *tailBuffer) add(line string) {
	b.lines = append(b.lines, line)
	// Recorte amortizado: se deja crecer hasta 2*max y se descarta la mitad vieja de una sola vez,
	// para no copiar el slice en cada línea.
	if b.max > 0 && len(b.lines) > 2*b.max {
		b.lines = append(b.lines[:0], b.lines[len(b.lines)-b.max:]...)
	}
}

func (b *tailBuffer) slice() []string {
	if b.max > 0 && len(b.lines) > b.max {
		return b.lines[len(b.lines)-b.max:]
	}
	return b.lines
}

// findLogFiles returns log file paths sorted chronologically that may contain entries in the time range.
func findLogFiles(dir, prefix string, from, to time.Time) ([]string, error) {
	pattern := filepath.Join(dir, fmt.Sprintf("%s-*.log", prefix))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob log files: %w", err)
	}
	if len(matches) == 0 {
		return nil, nil
	}

	sort.Strings(matches) // alphabetical = chronological with YYYY-MM-DD format

	// If we have a date range, filter files by filename date.
	if !from.IsZero() || !to.IsZero() {
		var filtered []string
		for _, path := range matches {
			fileDate := extractDate(path, prefix)
			if fileDate.IsZero() {
				filtered = append(filtered, path) // can't parse → include
				continue
			}
			// File date is the day; include if it overlaps with [from, to].
			fileEnd := fileDate.Add(24 * time.Hour)
			if !from.IsZero() && fileEnd.Before(from) {
				continue
			}
			if !to.IsZero() && fileDate.After(to) {
				continue
			}
			filtered = append(filtered, path)
		}
		matches = filtered
	}

	return matches, nil
}

// extractDate parses the date from a filename like "neuro-bot-2026-03-24.log", o de un archivo
// rotado por tamaño como "neuro-bot-2026-03-24.02.log" (ver DailyFileWriter.rollBySizeLocked).
func extractDate(path, prefix string) time.Time {
	base := filepath.Base(path)
	base = strings.TrimPrefix(base, prefix+"-")
	base = strings.TrimSuffix(base, ".log")
	// Descartar el sufijo de secuencia: sin esto la fecha no parsea, el archivo cae en el caso
	// "no se pudo leer la fecha → incluir" y las partes rotadas se escanean SIEMPRE, aunque queden
	// fuera de la ventana pedida.
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	// L19: parsear la fecha del nombre en la MISMA zona que from/to (que vienen en time.Local).
	// Con time.Parse (UTC) y offset negativo (Colombia UTC-5), el archivo del propio día se excluía.
	t, err := time.ParseInLocation("2006-01-02", base, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// readAndFilter reads a single log file and appends matching lines to out.
// Solo lee los últimos filter.scanLimit() bytes del archivo: las consultas de log son siempre "lo
// reciente", y sin ese tope el costo crece con el tamaño del archivo en vez de con lo pedido.
func readAndFilter(path, levelUpper string, filter LogFilter, out *tailBuffer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	from, to, search, phone := filter.From, filter.To, filter.Search, filter.Phone

	if limit := filter.scanLimit(); limit > 0 {
		if st, err := f.Stat(); err == nil && st.Size() > limit {
			if _, err := f.Seek(st.Size()-limit, io.SeekStart); err != nil {
				return err
			}
			// Tras el salto caemos a mitad de una línea: se descarta hasta el próximo salto de línea
			// para no entregar un fragmento (que además no parsearía como JSON).
			br := bufio.NewReader(f)
			if _, err := br.ReadString('\n'); err != nil {
				return nil // el resto es una sola línea parcial: nada útil que devolver
			}
			return scanFiltered(br, levelUpper, from, to, search, phone, out)
		}
	}

	return scanFiltered(f, levelUpper, from, to, search, phone, out)
}

// scanFiltered aplica los filtros línea a línea sobre r y va volcando en out.
func scanFiltered(r io.Reader, levelUpper string, from, to time.Time, search, phone string, out *tailBuffer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024) // handle long lines

	searchLower := strings.ToLower(search)
	// L18: los teléfonos se escriben ENMASCARADOS en disco (+573***3616) por la política PII, así que
	// el número completo nunca aparece. Matchear ambas formas (completa por si LOG_MASK_PHONES=false).
	phoneMasked := ""
	if phone != "" {
		phoneMasked = utils.MaskPhone(phone)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Quick phone filter: check if the phone number appears anywhere in the line.
		if phone != "" && !strings.Contains(line, phone) && !strings.Contains(line, phoneMasked) {
			continue
		}

		// Quick level check without full JSON parse when possible.
		if levelUpper != "" && !strings.Contains(line, `"level":"`+levelUpper) {
			continue
		}

		// Parse JSON for time and search filtering.
		if !from.IsZero() || !to.IsZero() || search != "" {
			var entry logEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				continue
			}

			if !from.IsZero() || !to.IsZero() {
				t, err := time.Parse(time.RFC3339Nano, entry.Time)
				if err != nil {
					continue
				}
				if !from.IsZero() && t.Before(from) {
					continue
				}
				if !to.IsZero() && t.After(to) {
					continue
				}
			}

			if search != "" && !strings.Contains(strings.ToLower(entry.Msg), searchLower) {
				continue
			}
		}

		out.add(line)
	}

	return scanner.Err()
}
