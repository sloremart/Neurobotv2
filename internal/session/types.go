package session

import "time"

const (
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusAbandoned = "abandoned"
	StatusEscalated = "escalated"
)

type Session struct {
	ID             string
	PhoneNumber    string
	CurrentState   string
	Status         string // active, completed, abandoned, escalated
	MenuOption     string
	PatientID      string
	PatientDoc     string
	PatientName    string
	PatientAge     int
	PatientGender  string
	PatientEntity  string
	RetryCount     int
	ConversationID string
	EscalatedAt    *time.Time // When escalated to agent
	EscalatedTeam  string     // Bird team ID assigned
	AgentID        string     // Bird agent asignado al escalar (SLA por agente)
	AgentName      string     // Nombre del agente asignado (displayName de Bird)
	ResumedAt      *time.Time // When agent returned to bot
	LastActivity   time.Time
	ExpiresAt      time.Time
	CreatedAt      time.Time

	// Context se carga lazy desde session_context
	Context map[string]string
}

// GetContext obtiene un valor del contexto en memoria
func (s *Session) GetContext(key string) string {
	if s.Context == nil {
		return ""
	}
	return s.Context[key]
}

// SetContext establece un valor en el contexto en memoria
func (s *Session) SetContext(key, value string) {
	if s.Context == nil {
		s.Context = make(map[string]string)
	}
	s.Context[key] = value
}

// InactiveSession is a lightweight struct for inactivity checking.
type InactiveSession struct {
	ID             string
	PhoneNumber    string
	ConversationID string
	LastActivity   time.Time
	Reminders      int // number of inactivity reminders already sent
}

// ExpiredEscalatedSession holds minimal data for closing expired escalated sessions.
type ExpiredEscalatedSession struct {
	ID             string
	PhoneNumber    string
	ConversationID string
}

// EscalatedSession holds the activity timestamps needed to decide, per escalated chat,
// whether to close it (patient went silent) or remind the agent (agent hasn't replied).
type EscalatedSession struct {
	ID             string
	PhoneNumber    string
	ConversationID string
	LastPatientMsg time.Time  // last inbound from the patient
	LastAgentMsg   *time.Time // last reply from the agent (nil = agent never replied)
	RemindersSent  int        // agent reminders already sent in this waiting window
}
