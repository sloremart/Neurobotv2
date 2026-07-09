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

// AgendaSummary describe una agenda (programacion_medico) con citas próximas no atendidas, para el
// selector del dashboard. Un médico puede tener varias agendas (incluso el mismo día) y una agenda
// puede abarcar varias fechas — por eso la agenda (no la fecha) es la entidad de selección.
type AgendaSummary struct {
	AgendaID    int    `json:"agenda_id"`
	Consultorio string `json:"consultorio"`
	FirstDate   string `json:"primera_fecha"` // YYYY-MM-DD
	LastDate    string `json:"ultima_fecha"`  // YYYY-MM-DD
	Dates       int    `json:"fechas"`        // fechas distintas con citas
	Citas       int    `json:"citas"`
}

// AgendaAppointmentRow es una fila del listado paginado de citas por agenda (vista de gestión del
// dashboard). Solo trae citas próximas no atendidas (estado NOT IN ('C','A'), fecha >= hoy) y solo
// las columnas visibles: hora del slot + nombre + cédula del paciente. El `ID` no se muestra pero es
// necesario para las acciones (cancelar/confirmar). El teléfono NO se expone (se resuelve en el bot
// al notificar) → menos PII.
type AgendaAppointmentRow struct {
	ID          string `json:"id"`
	Date        string `json:"fecha"` // YYYY-MM-DD (orden asc por fecha, luego hora)
	Hora        string `json:"hora"`  // HH:MM del slot
	PatientName string `json:"patient_name"`
	PatientDoc  string `json:"patient_doc"` // sis_paci.num_id
}

// AgendaAppointmentsPage es la respuesta paginada del listado.
type AgendaAppointmentsPage struct {
	Items []AgendaAppointmentRow `json:"items"`
	Total int                    `json:"total"`
	Page  int                    `json:"page"`
	Pages int                    `json:"pages"`
}

// AgendaAppointmentsFilter parametriza el listado paginado. AgendaID/DoctorCode: al menos uno debe
// venir (AgendaID es el principal). Date/Name/Doc son opcionales ("" = sin filtro).
type AgendaAppointmentsFilter struct {
	AgendaID   *int   // nil = no filtrar por agenda (0 es una agenda válida, por eso puntero)
	DoctorCode string // alterno (cod_medi); "" = no filtrar
	Date       string // sub-filtro dentro de una agenda multi-día; "" = todas
	Name       string // LIKE prefijo sobre el nombre del paciente
	Doc        string // prefijo LIKE sobre sis_paci.num_id (filtra al teclear)
	Page       int    // 1-based
	PageSize   int    // acotado por el repo (máx 100)
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

// RescheduleDayInput describes moving ALL the appointments of ONE day of an agenda to another date.
// The source agenda may be single- or multi-day: only the citas on OldDate move.
//   - DestAgendaID == 0  → create the destination agenda by duplicating the source day's slot grid on
//     NewDate (only possible if the doctor has no slots at those times on NewDate).
//   - DestAgendaID  > 0  → move into that existing agenda (must be the same doctor and have free slots at
//     the same HH:MM on NewDate). May equal the source agenda (multi-day: move a day within it).
type RescheduleDayInput struct {
	AgendaID     int    // source programacion_medico.id
	OldDate      string // YYYY-MM-DD (day to vacate)
	NewDate      string // YYYY-MM-DD (destination day)
	DestAgendaID int    // 0 = create by duplication; >0 = existing destination agenda
	// DryRun ejecuta toda la validación y calcula el resumen (cuántas citas, agenda destino, si crea)
	// pero hace ROLLBACK en vez de commit: no muta nada. Para la vista previa del dashboard.
	DryRun bool
}

// DoctorAgendaOnDate describe una agenda del médico con slots en una fecha (para elegir destino de
// reprogramación, incluidas las agendas-reserva vacías que no aparecen en la lista basada en citas).
type DoctorAgendaOnDate struct {
	AgendaID    int    `json:"agenda_id"`
	Consultorio string `json:"consultorio"`
	Slots       int    `json:"slots"`      // total de slots ese día
	Free        int    `json:"free"`       // slots libres (IdCita NULL)
	HoraDesde   string `json:"hora_desde"` // HH:MM del primer slot
	HoraHasta   string `json:"hora_hasta"` // HH:MM del último slot
}

// RescheduleDayResult reports the outcome of RescheduleDayOfAgenda.
type RescheduleDayResult struct {
	DestAgendaID int      // agenda the citas now belong to (new one if Created)
	Created      bool     // true when a new agenda was created by duplication
	Moved        int      // number of citas moved
	MovedIDs     []string // ids of the moved citas (for targeted notifications)
}

// RescheduleInvalidError marca un fallo de REGLA DE NEGOCIO de RescheduleDayOfAgenda (agenda inexistente,
// sin citas, destino incompatible, conflicto de horario, otro médico). El API lo mapea a 409. Los errores
// de infraestructura (tx/consulta) NO usan este tipo → el API los mapea a 500.
type RescheduleInvalidError struct{ Msg string }

func (e RescheduleInvalidError) Error() string { return e.Msg }
