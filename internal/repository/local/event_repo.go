package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
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
		if _, err := stmt.ExecContext(ctx,
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

// DailyKPIs contains aggregated metrics for a single day.
type DailyKPIs struct {
	Date                  string  `json:"date"`
	TotalSessions         int     `json:"total_sessions"`
	CompletedSessions     int     `json:"completed_sessions"`
	AbandonedSessions     int     `json:"abandoned_sessions"`
	EscalatedSessions     int     `json:"escalated_sessions"`
	AppointmentsCreated   int     `json:"appointments_created"`
	AppointmentsConfirmed int     `json:"appointments_confirmed"`
	AppointmentsCancelled int     `json:"appointments_cancelled"`
	PatientsRegistered    int     `json:"patients_registered"`
	OCRAttempts           int     `json:"ocr_attempts"`
	OCRSuccesses          int     `json:"ocr_successes"`
	GFRCalculations       int     `json:"gfr_calculations"`
	GFRBlocked            int     `json:"gfr_blocked"`
	AvgSessionDuration    float64 `json:"avg_session_duration_min"`
	OutOfHoursAttempts    int     `json:"out_of_hours_attempts"`
	MaxRetriesReached     int     `json:"max_retries_reached"`
	// Notification KPIs
	ProactivesSent        int `json:"proactives_sent"`
	ProactivesConfirmed   int `json:"proactives_confirmed"`
	ProactivesCancelled   int `json:"proactives_cancelled"`
	ProactivesNoResponse  int `json:"proactives_no_response"`
	NotificationEscalated int `json:"notification_escalated"`
	// IVR KPIs
	IVRCallsSent int `json:"ivr_calls_sent"`
	IVRConfirmed int `json:"ivr_confirmed"`
	IVRCancelled int `json:"ivr_cancelled"`
	// Waiting list KPIs
	WaitingListJoined    int `json:"waiting_list_joined"`
	WaitingListScheduled int `json:"waiting_list_scheduled"`
	WaitingListAccepted  int `json:"waiting_list_accepted"`
	WaitingListDeclined  int `json:"waiting_list_declined"`
	// Admin flow KPIs
	AdminAgendasCancelled   int `json:"admin_agendas_cancelled"`
	AdminAgendasRescheduled int `json:"admin_agendas_rescheduled"`
	RescheduleConfirmed     int `json:"reschedule_confirmed"`
	CancelAcknowledged      int `json:"cancel_acknowledged"`
	RescheduleSelfService   int `json:"reschedule_self_service"`
	// Operational KPIs
	TotalMessagesSent int `json:"total_messages_sent"`
	NoSlotsFound      int `json:"no_slots_found"`
}

// GetDailyKPIs returns aggregated KPI metrics for a given date.
func (r *EventRepo) GetDailyKPIs(ctx context.Context, date time.Time) (*DailyKPIs, error) {
	dateStr := date.Format("2006-01-02")
	kpis := &DailyKPIs{Date: dateStr}

	rows, err := r.db.QueryContext(ctx,
		`SELECT event_type, COUNT(*) as cnt
		 FROM chat_events
		 WHERE DATE(created_at) = ?
		 GROUP BY event_type`, dateStr)
	if err != nil {
		return nil, fmt.Errorf("get daily kpis: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var eventType string
		var count int
		if err := rows.Scan(&eventType, &count); err != nil {
			return nil, fmt.Errorf("scan kpi row: %w", err)
		}

		switch eventType {
		case "session_started":
			kpis.TotalSessions = count
		case "session_completed":
			kpis.CompletedSessions = count
		case "session_closed_inactivity": // Bug 2 fix
			kpis.AbandonedSessions = count
		case "escalated_to_agent":
			kpis.EscalatedSessions = count
		case "appointment_created":
			kpis.AppointmentsCreated = count
		case "appointment_confirmed":
			kpis.AppointmentsConfirmed = count
		case "appointment_cancelled":
			kpis.AppointmentsCancelled = count
		case "registration_success":
			kpis.PatientsRegistered = count
		case "ocr_success":
			kpis.OCRAttempts += count
			kpis.OCRSuccesses = count
		case "ocr_failed":
			kpis.OCRAttempts += count
		case "gfr_calculated":
			kpis.GFRCalculations = count
		case "pregnant_blocked":
			kpis.GFRBlocked += count
		case "out_of_hours":
			kpis.OutOfHoursAttempts = count
		case "max_retries_reached":
			kpis.MaxRetriesReached = count
		case "notification_sent":
			kpis.ProactivesSent = count
		case "notification_confirmed":
			kpis.ProactivesConfirmed = count
		case "notification_cancel_confirmed": // Bug 3 fix
			kpis.ProactivesCancelled = count
		case "notification_timeout":
			kpis.ProactivesNoResponse = count
		case "notification_escalated_agent":
			kpis.NotificationEscalated = count
		case "notification_ivr_sent":
			kpis.IVRCallsSent = count
		case "notification_confirmed_ivr":
			kpis.IVRConfirmed = count
		case "notification_cancelled_ivr":
			kpis.IVRCancelled = count
		case "admin_cancel_agenda":
			kpis.AdminAgendasCancelled = count
		case "admin_reschedule_agenda":
			kpis.AdminAgendasRescheduled = count
		case "notification_reschedule_confirmed":
			kpis.RescheduleConfirmed = count
		case "notification_cancel_acknowledged":
			kpis.CancelAcknowledged = count
		case "notification_reschedule_self_service":
			kpis.RescheduleSelfService = count
		case "waiting_list_joined":
			kpis.WaitingListJoined = count
		case "waiting_list_booking_success":
			kpis.WaitingListScheduled = count
		case "waiting_list_schedule_accepted":
			kpis.WaitingListAccepted = count
		case "waiting_list_schedule_declined":
			kpis.WaitingListDeclined = count
		case "message_sent":
			kpis.TotalMessagesSent = count
		case "no_slots_found":
			kpis.NoSlotsFound = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kpi rows: %w", err)
	}

	// Average session duration
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(AVG(TIMESTAMPDIFF(MINUTE,
			(SELECT MIN(ce2.created_at) FROM chat_events ce2 WHERE ce2.session_id = ce.session_id),
			ce.created_at
		 )), 0)
		 FROM chat_events ce
		 WHERE ce.event_type IN ('session_completed', 'session_closed_inactivity')
		 AND DATE(ce.created_at) = ?`, dateStr).Scan(&kpis.AvgSessionDuration); err != nil {
		slog.Warn("kpi avg session duration query failed", "date", dateStr, "error", err)
	}

	return kpis, nil
}

// NotificationBreakdown contains notification_sent counts by notification type.
type NotificationBreakdown struct {
	Confirmation int `json:"confirmation"`
	Reschedule   int `json:"reschedule"`
	Cancellation int `json:"cancellation"`
	WaitingList  int `json:"waiting_list"`
}

// GetNotificationBreakdown returns notification_sent counts broken down by type for a date.
func (r *EventRepo) GetNotificationBreakdown(ctx context.Context, date time.Time) (*NotificationBreakdown, error) {
	dateStr := date.Format("2006-01-02")
	bd := &NotificationBreakdown{}

	rows, err := r.db.QueryContext(ctx,
		`SELECT JSON_UNQUOTE(JSON_EXTRACT(event_data, '$.type')) AS ntype, COUNT(*) AS cnt
		 FROM chat_events
		 WHERE DATE(created_at) = ? AND event_type = 'notification_sent'
		 GROUP BY ntype`, dateStr)
	if err != nil {
		return nil, fmt.Errorf("get notification breakdown: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ntype sql.NullString
		var count int
		if err := rows.Scan(&ntype, &count); err != nil {
			return nil, fmt.Errorf("scan notification breakdown: %w", err)
		}
		switch ntype.String {
		case "confirmation":
			bd.Confirmation = count
		case "reschedule":
			bd.Reschedule = count
		case "cancellation":
			bd.Cancellation = count
		case "waiting_list":
			bd.WaitingList = count
		}
	}
	return bd, rows.Err()
}

// AppointmentBreakdown contains appointment_created counts by service type and CUPS code.
type AppointmentBreakdown struct {
	ByService map[string]int `json:"by_service"`
	ByCups    map[string]int `json:"by_cups"`
}

// GetAppointmentBreakdown returns appointment_created counts broken down by service and CUPS for a date.
func (r *EventRepo) GetAppointmentBreakdown(ctx context.Context, date time.Time) (*AppointmentBreakdown, error) {
	dateStr := date.Format("2006-01-02")
	bd := &AppointmentBreakdown{
		ByService: make(map[string]int),
		ByCups:    make(map[string]int),
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT
			JSON_UNQUOTE(JSON_EXTRACT(event_data, '$.service_type')) AS service_type,
			JSON_UNQUOTE(JSON_EXTRACT(event_data, '$.cups_code')) AS cups_code,
			JSON_UNQUOTE(JSON_EXTRACT(event_data, '$.cups_name')) AS cups_name,
			COUNT(*) AS cnt
		 FROM chat_events
		 WHERE DATE(created_at) = ? AND event_type = 'appointment_created'
		 GROUP BY service_type, cups_code, cups_name`, dateStr)
	if err != nil {
		return nil, fmt.Errorf("get appointment breakdown: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var serviceType, cupsCode, cupsName sql.NullString
		var count int
		if err := rows.Scan(&serviceType, &cupsCode, &cupsName, &count); err != nil {
			return nil, fmt.Errorf("scan appointment breakdown: %w", err)
		}
		if serviceType.Valid && serviceType.String != "" {
			bd.ByService[serviceType.String] += count
		}
		if cupsCode.Valid && cupsCode.String != "" {
			key := cupsCode.String
			if cupsName.Valid && cupsName.String != "" {
				key += " - " + cupsName.String
			}
			bd.ByCups[key] += count
		}
	}
	return bd, rows.Err()
}

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

	rows, err := r.db.QueryContext(ctx,
		`SELECT event_type, COUNT(DISTINCT session_id) as sessions
		 FROM chat_events
		 WHERE created_at BETWEEN ? AND ?
		 AND event_type IN (
			 'session_started', 'patient_identified', 'menu_selected',
			 'document_entered', 'patient_found', 'order_method_selected',
			 'ocr_validated', 'validations_complete', 'slots_found',
			 'booking_confirmed', 'appointment_created'
		 )
		 GROUP BY event_type`, from, to)
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

// HealthMetrics contains system health and runtime metrics.
type HealthMetrics struct {
	ActiveSessions       int     `json:"active_sessions"`
	PendingNotifications int     `json:"pending_notifications"`
	WorkerQueueSize      int     `json:"worker_queue_size"`
	WorkerQueueCap       int     `json:"worker_queue_cap"`
	DBLocalLatencyMs     float64 `json:"db_local_latency_ms"`
	UptimeSeconds        int64   `json:"uptime_seconds"`
	MemoryMB             float64 `json:"memory_mb"`
	Goroutines           int     `json:"goroutines"`
}

// GetHealthMetrics returns system health metrics.
func (r *EventRepo) GetHealthMetrics(ctx context.Context) (*HealthMetrics, error) {
	metrics := &HealthMetrics{}

	// Active sessions count
	// N-29: capturar el error; antes se descartaba y un fallo de BD reportaba 0 sesiones en silencio.
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sessions WHERE status = 'active'").Scan(&metrics.ActiveSessions); err != nil {
		slog.Warn("health metrics: active sessions query failed", "error", err)
	}

	// DB local latency
	// N-29: capturar el error del Ping; un fallo no debe reportarse como BD sana en silencio.
	start := time.Now()
	if err := r.db.PingContext(ctx); err != nil {
		slog.Warn("health metrics: db ping failed", "error", err)
	}
	metrics.DBLocalLatencyMs = float64(time.Since(start).Microseconds()) / 1000.0

	// Runtime metrics
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	metrics.MemoryMB = float64(memStats.Alloc) / 1024 / 1024
	metrics.Goroutines = runtime.NumGoroutine()

	return metrics, nil
}
