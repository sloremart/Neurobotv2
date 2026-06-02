package domain

import "time"

type AppointmentStatus string

const (
	AppointmentPending   AppointmentStatus = "pending"
	AppointmentConfirmed AppointmentStatus = "confirmed"
	AppointmentCancelled AppointmentStatus = "cancelled"
)

type Appointment struct {
	ID                    string
	RequestDate           time.Time
	Date                  time.Time
	TimeSlot              string // YYYYMMDDHHmm
	DoctorID              string // cod_medi SIESA (interno)
	DoctorDocument        string // cédula real (sis_medi.cedula) para lookup en Antares
	DoctorName            string
	PatientID             string
	PatientName           string
	PatientPhone          string
	Entity                string
	AgendaID              int
	Canceled              bool
	CancelDate            *time.Time
	Confirmed             bool
	ConfirmationDate      *time.Time
	ConfirmationChannel   string
	ConfirmationChannelID string
	Fulfilled             bool
	Observations          string
	Remonte               int
	Procedures            []AppointmentProcedure
}

type AppointmentProcedure struct {
	ID            string
	AppointmentID string
	CupCode       string
	CupName       string // nombre from cups_procedimientos
	Quantity      int
	UnitValue     float64
	ServiceID     int
}

type CreateAppointmentInput struct {
	Date       time.Time
	TimeSlot   string // YYYYMMDDHHmm
	DoctorID   string
	PatientID  string
	Entity     string
	AgendaID   int
	CreatedBy  string
	Observations string
	Procedures []CreateProcedureInput
}

type CreateProcedureInput struct {
	CupCode   string
	Quantity  int
	UnitValue float64
	ServiceID int
}
