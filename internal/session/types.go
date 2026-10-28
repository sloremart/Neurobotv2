package session

import (
	"strings"
	"time"
)

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
	CurrentState   string // estado actual — decide si el recordatorio de inactividad vale el envío
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
	EscalatedAt    time.Time  // when THIS escalation happened (anchors the no-show clock; zero = legacy row)
	// QueuedForNextDay dice si esta escalación quedó ENCOLADA para que una persona la atienda
	// el próximo día hábil (la marca la pone el handler de escalación; aquí llega leída del
	// session_context). Al paciente se le dijo que lo atienden mañana, así que su silencio de
	// esta noche no es abandono.
	QueuedForNextDay bool
}

// EscalationQueuedForNextDayKey es la clave de session_context que marca una escalación
// encolada para el próximo día hábil. El repo la lee por SQL para llenar QueuedForNextDay.
//
// El literal se escribe a mano porque este paquete no puede importar statemachine (crearía
// ciclo); el test TestEscalationQueuedForNextDayKey_MatchStateMachine lo mantiene sincronizado
// con statemachine.CtxEscalationNextDay.
const EscalationQueuedForNextDayKey = "escalacion_proximo_dia"

// escalationExempt dice si una escalación debe sobrevivir al checker (ni cierre por silencio ni
// no-show): quedó encolada para el próximo día hábil y esa espera aún no termina. La exención
// caduca en la próxima apertura del horario (nextOpen aplicado a EscalatedAt); sin horario
// cableado o sin escalated_at no hay con qué calcular el fin y no caduca, que es el lado seguro
// (cerrarle el chat a quien espera a una persona es peor que dejarlo esperando).
func escalationExempt(s EscalatedSession, now time.Time, nextOpen func(time.Time) time.Time) bool {
	return s.QueuedForNextDay && (nextOpen == nil || s.EscalatedAt.IsZero() || now.Before(nextOpen(s.EscalatedAt)))
}

// IsNectarSession dice si la sesión viene de Néctar: su clave ocupa el sitio del teléfono y
// lleva el prefijo "nectar:" (un id de conversación numérico pelado no cuenta).
func IsNectarSession(phone string) bool { return strings.HasPrefix(phone, "nectar:") }
