package local

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/notifications"
)

// Revisión del lote C, ronda 3 (R3-M5): que MarkSiesaPending guarde la respuesta en status solo lo cubría el
// test de integración, que no corre en CI. Este driver de mentira anota lo que le mandan: comprueba el SQL y
// sus argumentos sin una base de datos. Lo que MySQL hace con ellos sigue siendo de la integración.

type recordedStatement struct {
	query string
	args  []driver.Value
}

// recordingDriver responde al SELECT con una fila (la última del historial) o con ninguna, y anota todo.
type recordingDriver struct {
	mu         sync.Mutex
	statements []recordedStatement
	historyRow bool
}

func (d *recordingDriver) Open(string) (driver.Conn, error) { return &recordingConn{d}, nil }

func (d *recordingDriver) find(prefix string) (recordedStatement, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.statements {
		if strings.HasPrefix(strings.TrimSpace(s.query), prefix) {
			return s, true
		}
	}
	return recordedStatement{}, false
}

type recordingConn struct{ d *recordingDriver }

func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	return &recordingStmt{c.d, query}, nil
}
func (c *recordingConn) Close() error              { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) { return recordingTx{}, nil }

type recordingTx struct{}

func (recordingTx) Commit() error   { return nil }
func (recordingTx) Rollback() error { return nil }

type recordingStmt struct {
	d     *recordingDriver
	query string
}

func (s *recordingStmt) Close() error  { return nil }
func (s *recordingStmt) NumInput() int { return -1 }

func (s *recordingStmt) record(args []driver.Value) {
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	s.d.statements = append(s.d.statements, recordedStatement{s.query, args})
}

func (s *recordingStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.record(args)
	return driver.RowsAffected(1), nil
}

func (s *recordingStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.record(args)
	return &recordingRows{pending: s.d.historyRow}, nil
}

type recordingRows struct{ pending bool }

func (r *recordingRows) Columns() []string { return []string{"id", "already"} }
func (r *recordingRows) Close() error      { return nil }
func (r *recordingRows) Next(dest []driver.Value) error {
	if !r.pending {
		return io.EOF
	}
	r.pending = false
	dest[0], dest[1] = int64(7), false
	return nil
}

func markWithRecordingDriver(t *testing.T, name string, historyRow bool) *recordingDriver {
	t.Helper()
	d := &recordingDriver{historyRow: historyRow}
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	row := notifications.SiesaPendingRow{Phone: "+570000000000", AppointmentID: "1", Action: "cancel"}
	if _, err := NewNotificationRepo(db).MarkSiesaPending(context.Background(), row, "cancel_requested"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return d
}

// La marca sobre una fila existente deja la respuesta nueva en status: lo lee el SELECT para no repetir el acuse.
func TestMarkSiesaPending_UpdateWritesStatus(t *testing.T) {
	d := markWithRecordingDriver(t, "recording-update", true)
	update, ok := d.find("UPDATE notification_history")
	if !ok {
		t.Fatal("debía actualizar la fila")
	}
	if !strings.Contains(update.query, "SET status = ?,") {
		t.Fatalf("el UPDATE no guarda status: %s", update.query)
	}
	if len(update.args) == 0 || update.args[0] != "cancel_requested" {
		t.Fatalf("el primer argumento debe ser la respuesta: %v", update.args)
	}
	sel, ok := d.find("SELECT id")
	if !ok || !strings.Contains(sel.query, "status = ?") || sel.args[0] != "cancel_requested" {
		t.Fatalf("el SELECT compara con la respuesta: %+v", sel)
	}
}

// Sin fila, la respuesta entra con su status.
func TestMarkSiesaPending_InsertWritesStatus(t *testing.T) {
	d := markWithRecordingDriver(t, "recording-insert", false)
	insert, ok := d.find("INSERT INTO notification_history")
	if !ok {
		t.Fatal("debía insertar la fila")
	}
	found := false
	for _, a := range insert.args {
		found = found || a == "cancel_requested"
	}
	if !strings.Contains(insert.query, "status") || !found {
		t.Fatalf("el INSERT no guarda status: %s %v", insert.query, insert.args)
	}
}
