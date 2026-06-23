package testutil

import (
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// NewTestSession creates a session in the given state with sensible defaults.
func NewTestSession(state string) *session.Session {
	return &session.Session{
		ID:           "test-session-1",
		PhoneNumber:  "+573001234567",
		CurrentState: state,
		Status:       session.StatusActive,
		Context:      make(map[string]string),
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
}

// NewTestMessage creates a text InboundMessage.
func NewTestMessage(text string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:          "msg-test-1",
		Phone:       "+573001234567",
		MessageType: "text",
		Text:        text,
		ReceivedAt:  time.Now(),
	}
}

// NewPostbackMessage creates a postback InboundMessage.
func NewPostbackMessage(payload string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:              "msg-postback-1",
		Phone:           "+573001234567",
		MessageType:     "text",
		IsPostback:      true,
		PostbackPayload: payload,
		ReceivedAt:      time.Now(),
	}
}

// NewImageMessage creates an image InboundMessage.
func NewImageMessage(url string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:          "msg-image-1",
		Phone:       "+573001234567",
		MessageType: "image",
		ImageURL:    url,
		ReceivedAt:  time.Now(),
	}
}

// NewAudioMessage creates an audio InboundMessage.
func NewAudioMessage() bird.InboundMessage {
	return bird.InboundMessage{
		ID:          "msg-audio-1",
		Phone:       "+573001234567",
		MessageType: "audio",
		ReceivedAt:  time.Now(),
	}
}

// NewTypedMessage creates a message of a specific type.
func NewTypedMessage(msgType string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:          "msg-typed-1",
		Phone:       "+573001234567",
		MessageType: msgType,
		ReceivedAt:  time.Now(),
	}
}

// SamplePatient returns a realistic test patient.
func SamplePatient() *domain.Patient {
	return &domain.Patient{
		ID:              "PAT001",
		DocumentType:    "CC",
		DocumentNumber:  "1234567890",
		FirstName:       "Juan",
		SecondName:      "Carlos",
		FirstSurname:    "Perez",
		SecondSurname:   "Lopez",
		FullName:        "Juan Carlos Perez Lopez",
		BirthDate:       time.Date(1990, 2, 15, 0, 0, 0, 0, time.UTC),
		Gender:          "M",
		Phone:           "+573001234567",
		Email:           "juan@test.com",
		Address:         "Calle 1 #2-3",
		CityCode:        "50001",
		Zone:            "U",
		EntityCode:      "EPS001",
		AffiliationType: "C",
		UserType:        "1",
	}
}

// SampleAppointment returns a realistic test appointment.
func SampleAppointment(date time.Time) domain.Appointment {
	return domain.Appointment{
		ID:         "APT001",
		PatientID:  "PAT001",
		DoctorID:   "DOC001",
		DoctorName: "Dr. Maria Garcia",
		Date:       date,
		TimeSlot:   date.Format("200601021504"),
		AgendaID:   1,
		Procedures: []domain.AppointmentProcedure{
			{CupCode: "890271", CupName: "Electromiografia", Quantity: 1},
		},
	}
}

// SampleConfig returns a Config with test values.
func SampleConfig() *config.Config {
	return &config.Config{
		Port:                  "8080",
		Timezone:              "America/Bogota",
		LogLevel:              "debug",
		BotName:               "NeuroBot",
		CenterName:            "Neuro Electrodiagnóstico",
		CenterKey:             "NEURO",
		SessionTimeoutMinutes: 120,
		BirdAPIURL:            "http://localhost:9999",
		BirdWorkspaceID:       "ws-test",
		BirdChannelID:         "ch-test",
		TeamRoutingEnabled:    true,
		BirdTeamGrupoA:        "team-grupo-a",
		BirdTeamGrupoB:        "team-grupo-b",
		BirdTeamFallback:      "team-fallback",
		BirdAgentFallback:     "agent-fallback",
		BirdWebhookSecret:     "test-secret",
		InternalAPIKey:        "test-api-key",
	}
}

// MustParseDate parses "2006-01-02" or panics.
func MustParseDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic("MustParseDate: " + err.Error())
	}
	return t
}
