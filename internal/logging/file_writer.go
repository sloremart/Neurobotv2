package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DailyFileWriter writes to date-stamped log files with automatic rotation.
// Implements io.Writer so it can be used with io.MultiWriter.
type DailyFileWriter struct {
	dir        string
	prefix     string
	retainDays int
	maxBytes   int64 // 0 = sin tope de tamaño (solo rotación diaria)

	mu      sync.Mutex
	current *os.File
	curDate string
	curSize int64
}

// NewDailyFileWriter creates a writer that produces files like <dir>/<prefix>-YYYY-MM-DD.log.
// retainDays controls how many days of log files to keep (0 = keep all).
//
// maxBytes acota el tamaño de UN archivo (0 = sin tope). Con solo rotación diaria, un bucle caliente
// escribe sin freno hasta llenar el disco: el 28-jul-2026 una recursión llevó el archivo del día a
// 7,4 GB en dos horas (~3 MB/s), contra ~30 MB de un día normal. Al superar el tope, el archivo
// actual se archiva como <prefix>-YYYY-MM-DD.NN.log y se empieza uno nuevo, de modo que el nivel
// `debug` —que es el que permite diagnosticar— no sea un riesgo de disco.
func NewDailyFileWriter(dir, prefix string, retainDays int, maxBytes int64) (*DailyFileWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	w := &DailyFileWriter{
		dir:        dir,
		prefix:     prefix,
		retainDays: retainDays,
		maxBytes:   maxBytes,
	}
	if err := w.rotate(); err != nil {
		return nil, err
	}
	// Cleanup old files on startup.
	go w.cleanup()
	return w, nil
}

// Write implements io.Writer. Thread-safe, rotates on date change or size limit.
func (w *DailyFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != w.curDate {
		if err := w.rotateLocked(today); err != nil {
			return 0, err
		}
		// #32 (auditoría): limpiar logs viejos en cada rotación diaria, no solo al arranque (en procesos
		// longevos, sin esto crecían sin límite). Async para no sostener el lock.
		go w.cleanup()
	} else if w.maxBytes > 0 && w.curSize+int64(len(p)) > w.maxBytes {
		if err := w.rollBySizeLocked(today); err != nil {
			return 0, err
		}
	}

	n, err := w.current.Write(p)
	w.curSize += int64(n)
	return n, err
}

// rollBySizeLocked archiva el archivo actual con sufijo numérico y abre uno nuevo con el nombre del
// día. El archivo "sin sufijo" es siempre el más reciente, y los sufijos ordenan cronológicamente
// antes que él (.01 < .02 < sin sufijo), que es lo que espera el lector al ordenar por nombre.
func (w *DailyFileWriter) rollBySizeLocked(date string) error {
	if w.current != nil {
		_ = w.current.Close()
		w.current = nil
	}
	live := filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", w.prefix, date))
	archived := filepath.Join(w.dir, fmt.Sprintf("%s-%s.%02d.log", w.prefix, date, w.nextArchiveSeq(date)))
	if err := os.Rename(live, archived); err != nil {
		// Si el rename falla (permisos, archivo en uso), seguir escribiendo en el archivo actual es
		// preferible a perder logs: se reabre y se sigue, aunque supere el tope.
		return w.rotateLocked(date)
	}
	return w.rotateLocked(date)
}

// nextArchiveSeq devuelve el siguiente número de secuencia libre para el día dado.
func (w *DailyFileWriter) nextArchiveSeq(date string) int {
	matches, err := filepath.Glob(filepath.Join(w.dir, fmt.Sprintf("%s-%s.*.log", w.prefix, date)))
	if err != nil {
		return 1
	}
	return len(matches) + 1
}

// Close closes the current log file.
func (w *DailyFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current != nil {
		return w.current.Close()
	}
	return nil
}

// Dir returns the log directory path.
func (w *DailyFileWriter) Dir() string { return w.dir }

// Prefix returns the filename prefix.
func (w *DailyFileWriter) Prefix() string { return w.prefix }

func (w *DailyFileWriter) rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rotateLocked(time.Now().Format("2006-01-02"))
}

func (w *DailyFileWriter) rotateLocked(date string) error {
	if w.current != nil {
		_ = w.current.Close()
	}
	path := filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", w.prefix, date))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", path, err)
	}
	w.current = f
	w.curDate = date
	// El archivo puede venir con contenido (reinicio del bot a mitad del día): el tope de tamaño se
	// mide sobre lo que YA tiene, no desde cero, o un proceso que reinicia mucho nunca rotaría.
	w.curSize = 0
	if st, err := f.Stat(); err == nil {
		w.curSize = st.Size()
	}
	return nil
}

func (w *DailyFileWriter) cleanup() {
	if w.retainDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -w.retainDays)
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(w.dir, e.Name()))
		}
	}
}
