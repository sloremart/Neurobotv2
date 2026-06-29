package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ChatEvent represents an event record in the chat_events table.
type ChatEvent struct {
	ID          int64
	SessionID   string
	PhoneNumber string
	EventType   string
	EventData   map[string]interface{}
	StateFrom   string
	StateTo     string
	CreatedAt   time.Time
}

// EventRepo handles persistence and querying of chat events.
type EventRepo struct {
	db *sql.DB
}

// NewEventRepo creates a new EventRepo.
func NewEventRepo(db *sql.DB) *EventRepo {
	return &EventRepo{db: db}
}

// Insert persists a single chat event.
func (r *EventRepo) Insert(ctx context.Context, event *ChatEvent) error {
	dataJSON, err := json.Marshal(event.EventData)
	if err != nil {
		dataJSON = []byte("{}")
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO chat_events (session_id, phone_number, event_type, event_data, state_from, state_to, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.SessionID, event.PhoneNumber, event.EventType, string(dataJSON),
		nullString(event.StateFrom), nullString(event.StateTo), event.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert chat event: %w", err)
	}
	return nil
}

// InsertBatch persists multiple events in a single transaction.
func (r *EventRepo) InsertBatch(ctx context.Context, events []ChatEvent) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO chat_events (session_id, phone_number, event_type, event_data, state_from, state_to, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, event := range events {
		dataJSON, _ := json.Marshal(event.EventData)
		if _, err := stmt.ExecContext(
			ctx,
			event.SessionID, event.PhoneNumber, event.EventType, string(dataJSON),
			nullString(event.StateFrom), nullString(event.StateTo), event.CreatedAt,
		); err != nil {
			return fmt.Errorf("insert event %s: %w", event.EventType, err)
		}
	}

	return tx.Commit()
}

// FindByPhone returns chat events filtered by phone number and optional date range / event type.
// Results are ordered chronologically (oldest first), limited to maxRows.
func (r *EventRepo) FindByPhone(ctx context.Context, phone string, from, to time.Time, eventType string, maxRows int) ([]ChatEvent, error) {
	if maxRows <= 0 || maxRows > 500 {
		maxRows = 200
	}

	where := "phone_number = ?"
	args := []interface{}{phone}

	if !from.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, from)
	}
	if !to.IsZero() {
		where += " AND created_at <= ?"
		args = append(args, to)
	}
	if eventType != "" {
		where += " AND event_type = ?"
		args = append(args, eventType)
	}

	query := fmt.Sprintf(`SELECT id, session_id, phone_number, event_type, event_data,
		COALESCE(state_from,''), COALESCE(state_to,''), created_at
		FROM chat_events WHERE %s ORDER BY created_at ASC LIMIT ?`, where)
	args = append(args, maxRows)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find events by phone: %w", err)
	}
	defer rows.Close()

	var events []ChatEvent
	for rows.Next() {
		var e ChatEvent
		var dataJSON string
		if err := rows.Scan(&e.ID, &e.SessionID, &e.PhoneNumber, &e.EventType,
			&dataJSON, &e.StateFrom, &e.StateTo, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		if dataJSON != "" {
			json.Unmarshal([]byte(dataJSON), &e.EventData)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// === KPI Queries ===

// FunnelData contains conversion funnel metrics.
type FunnelData struct {
	FromDate             string  `json:"from_date"`
	ToDate               string  `json:"to_date"`
	TotalSessions        int     `json:"total_sessions"`
	IdentifiedPatients   int     `json:"identified_patients"`
	MenuSelected         int     `json:"menu_selected"`
	DocumentEntered      int     `json:"document_entered"`
	PatientFound         int     `json:"patient_found"`
	MedicalOrderStarted  int     `json:"medical_order_started"`
	OCRCompleted         int     `json:"ocr_completed"`
	ValidationsComplete  int     `json:"validations_complete"`
	SlotsFound           int     `json:"slots_found"`
	BookingConfirmed     int     `json:"booking_confirmed"`
	AppointmentCreated   int     `json:"appointment_created"`
	DropAfterGreeting    float64 `json:"drop_after_greeting"`
	DropAfterDocument    float64 `json:"drop_after_document"`
	DropAfterOrder       float64 `json:"drop_after_order"`
	DropAfterValidations float64 `json:"drop_after_validations"`
	DropAfterSlots       float64 `json:"drop_after_slots"`
	ConversionRate       float64 `json:"conversion_rate"`
}

// GetFunnel returns conversion funnel data for a date range.
func (r *EventRepo) GetFunnel(ctx context.Context, from, to time.Time) (*FunnelData, error) {
	funnel := &FunnelData{
		FromDate: from.Format("2006-01-02"),
		ToDate:   to.Format("2006-01-02"),
	}

	// L2: rango half-open [from, to+1día) — consistente con el resto del repo (N-44). El BETWEEN
	// inclusivo con created_at TIMESTAMP excluía todos los eventos del último día (00:00:01–23:59:59).
	toExclusive := to.AddDate(0, 0, 1)
	rows, err := r.db.QueryContext(ctx,
		`SELECT event_type, COUNT(DISTINCT session_id) as sessions
		 FROM chat_events
		 WHERE created_at >= ? AND created_at < ?
		 AND event_type IN (
			 'session_started', 'patient_identified', 'menu_selected',
			 'document_entered', 'patient_found', 'order_method_selected',
			 'ocr_validated', 'validations_complete', 'slots_found',
			 'booking_confirmed', 'appointment_created'
		 )
		 GROUP BY event_type`, from, toExclusive)
	if err != nil {
		return nil, fmt.Errorf("get funnel: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var eventType string
		var count int
		if err := rows.Scan(&eventType, &count); err != nil {
			return nil, fmt.Errorf("scan funnel row: %w", err)
		}

		switch eventType {
		case "session_started":
			funnel.TotalSessions = count
		case "patient_identified":
			funnel.IdentifiedPatients = count
		case "menu_selected":
			funnel.MenuSelected = count
		case "document_entered":
			funnel.DocumentEntered = count
		case "patient_found":
			funnel.PatientFound = count
		case "order_method_selected":
			funnel.MedicalOrderStarted = count
		case "ocr_validated":
			funnel.OCRCompleted = count
		case "validations_complete":
			funnel.ValidationsComplete = count
		case "slots_found":
			funnel.SlotsFound = count
		case "booking_confirmed":
			funnel.BookingConfirmed = count
		case "appointment_created":
			funnel.AppointmentCreated = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate funnel rows: %w", err)
	}

	// Calculate drop-off rates
	if funnel.TotalSessions > 0 {
		funnel.ConversionRate = float64(funnel.AppointmentCreated) / float64(funnel.TotalSessions) * 100
		funnel.DropAfterGreeting = float64(funnel.TotalSessions-funnel.DocumentEntered) / float64(funnel.TotalSessions) * 100
	}
	if funnel.DocumentEntered > 0 {
		funnel.DropAfterDocument = float64(funnel.DocumentEntered-funnel.PatientFound) / float64(funnel.DocumentEntered) * 100
	}
	if funnel.MedicalOrderStarted > 0 {
		funnel.DropAfterOrder = float64(funnel.MedicalOrderStarted-funnel.OCRCompleted) / float64(funnel.MedicalOrderStarted) * 100
	}
	if funnel.ValidationsComplete > 0 {
		funnel.DropAfterValidations = float64(funnel.ValidationsComplete-funnel.SlotsFound) / float64(funnel.ValidationsComplete) * 100
	}
	if funnel.SlotsFound > 0 {
		funnel.DropAfterSlots = float64(funnel.SlotsFound-funnel.AppointmentCreated) / float64(funnel.SlotsFound) * 100
	}

	return funnel, nil
}

// CountAppointmentsCreated cuenta las FILAS del evento appointment_created (una por cita creada,
// slots.go lo emite una vez por cita) en la ventana [from, to+1día). A diferencia del embudo
// (COUNT(DISTINCT session_id)), esto cuenta citas, no sesiones — la unidad correcta para comparar
// contra las filas de la tabla citas de SIESA en la discrepancia de conversión real.
func (r *EventRepo) CountAppointmentsCreated(ctx context.Context, from, to time.Time) (int, error) {
	toExclusive := to.AddDate(0, 0, 1)
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chat_events
		 WHERE event_type = 'appointment_created' AND created_at >= ? AND created_at < ?`,
		from, toExclusive).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count appointments created: %w", err)
	}
	return count, nil
}
