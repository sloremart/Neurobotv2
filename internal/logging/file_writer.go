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

	mu      sync.Mutex
	current *os.File
	curDate string
}

// NewDailyFileWriter creates a writer that produces files like <dir>/<prefix>-YYYY-MM-DD.log.
// retainDays controls how many days of log files to keep (0 = keep all).
func NewDailyFileWriter(dir, prefix string, retainDays int) (*DailyFileWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	w := &DailyFileWriter{
		dir:        dir,
		prefix:     prefix,
		retainDays: retainDays,
	}
	if err := w.rotate(); err != nil {
		return nil, err
	}
	// Cleanup old files on startup.
	go w.cleanup()
	return w, nil
}

// Write implements io.Writer. Thread-safe, rotates on date change.
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
	}
	return w.current.Write(p)
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
