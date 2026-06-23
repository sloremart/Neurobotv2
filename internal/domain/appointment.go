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
	DoctorID              string // internal code (citas.cod_medi)
	DoctorDocument        string // real document number (sis_medi.cedula)
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
	Date      time.Time
	TimeSlot  string // YYYYMMDDHHmm
	DoctorID  string
	PatientID string
	Entity    string
	AgendaID  int
	// AgendaSede is the selected slot's programacion_medico.id_sede (ground truth for
	// citas.id_sede). 0 = unset (repo falls back to the subject-based location map).
	AgendaSede   int
	CreatedBy    string
	Observations string
	// SubjectType is the SIESA asunto_id resolved from the local CUPS catalog
	// (cups_procedimientos.asunto_id; 17 when sedation is declared). It is the
	// deterministic source for citas.asunto. 0 = unresolved (repo falls back to history).
	SubjectType int
	// ContractCode is the patient's contract (sis_paci.contrato, e.g. "6"). When set, the
	// appointment is booked under that contract (correct billing manual for MRC vs Evento).
	ContractCode string
	// Espacios is the number of consecutive slots the procedure occupies (duration-based).
	// >1 → the single cita is linked to N contiguous slots (programacion_medico_detalle.IdCita).
	// 0/1 → single slot. The N slots are claimed atomically inside Create.
	Espacios   int
	Procedures []CreateProcedureInput
}

type CreateProcedureInput struct {
	CupCode   string
	Quantity  int
	UnitValue float64
	ServiceID int
}
