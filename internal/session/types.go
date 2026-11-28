package session

import (
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/utils"
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
	// ContactIdentifier es el contacto de la conversación de Néctar tal como lo da Néctar (E.164
	// sin «+», o el BSUID si el número es oculto). Vacío en Bird. Ver ContactPhoneOf.
	ContactIdentifier string
	EscalatedAt       *time.Time // When escalated to agent
	EscalatedTeam     string     // Bird team ID assigned
	AgentID           string     // Bird agent asignado al escalar (SLA por agente)
	AgentName         string     // Nombre del agente asignado (displayName de Bird)
	ResumedAt         *time.Time // When agent returned to bot
	LastActivity      time.Time
	ExpiresAt         time.Time
	CreatedAt         time.Time

	// NectarLine es la línea de Néctar de la conversación («main» o «legacy»); vacía = principal o Bird.
	NectarLine string

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
	// el próximo día hábil (la marca la pondrá el handler de escalación; aquí llega leída del
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

// CtxContactIdentifier es la clave de contexto donde viaja ContactIdentifier (statemachine la
// reexporta como CtxContactIdentifier). Se repite aquí porque este paquete no importa statemachine.
const CtxContactIdentifier = "contact_identifier"

// Claves de contexto del flujo «Resultados de estudios» (datos personales del paciente). Viven aquí
// y no en statemachine/handlers porque el cierre por inactividad también las borra y este paquete
// no puede importar a statemachine (ciclo).
const (
	CtxResultsDocType      = "results_doc_type"
	CtxResultsDoc          = "results_doc"
	CtxResultsDocHash      = "results_doc_hash"
	CtxResultsFirstName    = "results_first_name"
	CtxResultsFirstSurname = "results_first_surname"
	CtxResultsStudies      = "results_studies"
	CtxResultsChoice       = "results_choice"
)

// ResultsFlowKeys son todas las claves del flujo de resultados: se borran al salir de él, al
// escalar y al cerrar la sesión.
var ResultsFlowKeys = []string{
	CtxResultsDocType, CtxResultsDoc, CtxResultsDocHash,
	CtxResultsFirstName, CtxResultsFirstSurname, CtxResultsStudies, CtxResultsChoice,
}

// ContactPhoneOf devuelve el teléfono del paciente de una sesión de Néctar en la forma de
// utils.ParseColombianPhone («+57…»), o "" si el contacto es un BSUID, está oculto o no se conoce.
//
// Solo un identificador de puros dígitos es un teléfono: un BSUID puede llevar dígitos dentro
// («CO.3001112233») y ParseColombianPhone, que limpia todo lo que no es dígito, lo tomaría por uno.
func ContactPhoneOf(s *Session) string {
	if s == nil {
		return ""
	}
	id := s.ContactIdentifier
	if id == "" {
		id = s.GetContext(CtxContactIdentifier)
	}
	digits := strings.TrimPrefix(id, "+")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return ""
	}
	return utils.ParseColombianPhone(digits)
}

// IsNectarSession dice si la sesión viene de Néctar: su clave ocupa el sitio del teléfono y
// lleva el prefijo "nectar:" (un id de conversación numérico pelado no cuenta).
func IsNectarSession(phone string) bool { return strings.HasPrefix(phone, "nectar:") }
