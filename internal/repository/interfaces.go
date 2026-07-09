package repository

import (
	"context"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// PatientRepository — operaciones sobre pacientes en BD externa.
// Implementación actual: siesa (tabla: sis_paci)
type PatientRepository interface {
	FindByDocument(ctx context.Context, docType, doc string) (*domain.Patient, error)
	FindByID(ctx context.Context, id string) (*domain.Patient, error)
	Create(ctx context.Context, input domain.CreatePatientInput) (string, error)
	UpdateEntity(ctx context.Context, patientID, entityCode string) error
	UpdateContract(ctx context.Context, patientID, contractCode string) error
	UpdateMunicipality(ctx context.Context, patientID, depCode, muniCode string) error
	UpdateContactInfo(ctx context.Context, patientID, phone, email string) error
}

// AppointmentRepository — operaciones sobre citas en BD externa.
// Implementación actual: siesa (tablas: citas, citas_procedimientos, citas_procedimientos_asuntos)
type AppointmentRepository interface {
	FindByID(ctx context.Context, id string) (*domain.Appointment, error)
	FindUpcomingByPatient(ctx context.Context, patientID string) ([]domain.Appointment, error)
	// FindPendingEmgAppointment returns the nearest future PENDING (estado 'P') appointment of the
	// patient whose procedures include an EMG (Group 1) CUP — the anchor for consolidating separate
	// EMG/NC orders into a single appointment. nil if none. Procedures come loaded.
	FindPendingEmgAppointment(ctx context.Context, patientID string, emgCodes []string) (*domain.Appointment, error)
	FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error)
	// FindAgendasByDoctor lista las agendas (programacion_medico) del médico con citas próximas no
	// atendidas (estado NOT IN ('C','A'), fecha >= from), para el selector del dashboard. Solo lectura
	// NOLOCK. `from` en YYYY-MM-DD ("" = hoy).
	FindAgendasByDoctor(ctx context.Context, doctorCode, from string) ([]domain.AgendaSummary, error)
	// FindAgendaAppointmentsPaged lista, paginado y filtrable, las citas próximas no atendidas de una
	// agenda (o médico). Ordena por fecha+hora del slot. Solo lectura NOLOCK; total vía COUNT(*) OVER().
	FindAgendaAppointmentsPaged(ctx context.Context, f domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error)
	Create(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error)
	CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error
	CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error
	Confirm(ctx context.Context, id string, channel, channelID string) error
	Cancel(ctx context.Context, id string, reason, channel, channelID string) error
	ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error
	CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error
	DeleteBatch(ctx context.Context, ids []string) error
	HasFutureForCup(ctx context.Context, patientID, cupCode string) (bool, error)
	FindLastDoctorForCups(ctx context.Context, patientID string, cups []string) (string, error)
	CountMonthlyByGroup(ctx context.Context, cupsCodes []string, year, month int) (int, error)
	FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error)
	RescheduleDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (int, error)
	// SlotCountForAppointment devuelve cuántos slots de programacion_medico_detalle
	// están asociados a la cita (IdCita = apptID). Es la fuente de verdad del número
	// de espacios que ocupa una cita multi-slot (1 cita / N slots).
	SlotCountForAppointment(ctx context.Context, apptID string) (int, error)
	// WriteCreationAudit registra el alta de la cita en SIESA: una fila en log_citas
	// (APARTAR CITA) + su detalle en log_citas_procedimientos (una por CUPS de
	// citas_procedimientos). Best-effort/asíncrono. Se llama tras insertar los CUPS.
	WriteCreationAudit(ctx context.Context, appointmentID, observations string)
}

// ScheduleRepository — agendas, horarios y excepciones.
// Implementación actual: siesa (tablas: programacion_medico, programacion_medico_detalle, programacion_medico_relacion*)
type ScheduleRepository interface {
	// FindAvailableSlots returns every free SIESA slot for agendas serving the given subject
	// (asunto_id), within the 3h..90-day window, optionally after the given date (YYYY-MM-DD,
	// for pagination). allowedDoctors restringe a los médicos (sis_medi.codigo) que realizan el
	// CUPS (cups_medico); vacío = sin restricción. Cada fila es un slot agendable. Replaces the old
	// FindFutureWorkingDays + FindScheduleConfig + FindBookedSlots + in-memory slot generation.
	FindAvailableSlots(ctx context.Context, asuntoID int, afterDate string, allowedDoctors []int) ([]domain.AvailableSlotRow, error)
	// GetAsuntosByAgenda returns the distinct SIESA subjects (IdAsunto) an ACTIVE agenda serves,
	// via its consultorio relation. agendaID = programacion_medico.id (= domain Appointment.AgendaID).
	// Used to know which CUPS a freed slot can be reused for (waiting-list slot matching).
	GetAsuntosByAgenda(ctx context.Context, agendaID int) ([]int, error)
	FindByScheduleID(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error)
	FindWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (*domain.WorkingDay, error)
	UpdateWorkingDayExceptionDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (bool, error)
	DeleteWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (bool, error)
}

// PriceRepository — búsqueda de precios por CUPS según tarifa.
// Implementación actual: siesa (tabla: sis_proc_precios)
// FindPrice returns nil when the CUPS code is not found in the SOAT table,
// and *0.0 when the tariff is legitimately zero.
type PriceRepository interface {
	FindPrice(ctx context.Context, cupCode, tariffType string) (*float64, error)
}

// ProcedureRepository — catálogo de procedimientos CUPS.
// Implementación actual: local MySQL (tabla: cups_procedimientos, migración 019)
type ProcedureRepository interface {
	FindByCode(ctx context.Context, code string) (*domain.Procedure, error)
	FindByID(ctx context.Context, id int) (*domain.Procedure, error)
	SearchByName(ctx context.Context, name string) ([]domain.Procedure, error)
	FindAllActive(ctx context.Context) ([]domain.Procedure, error)
	// FindSubjectTypeForCups returns the SIESA subject id (asunto_id) for a CUPS code,
	// or 0 if not found. Deterministic replacement for ScheduleRepo.FindAsuntoForCups.
	FindSubjectTypeForCups(ctx context.Context, cupsCode string) (int, error)
	// FindMedicosForCups returns the sis_medi codes (cod_medi) authorized to perform a CUPS,
	// from the cups_medico relation. Empty slice = no restriction configured (callers fail-open).
	FindMedicosForCups(ctx context.Context, cupsCode string) ([]int, error)
	// FindCupsForDoctorAndAsuntos returns the active CUPS codes a doctor (medicoID = cod_medi) is
	// authorized to perform (cups_medico) whose asunto is in the given set. Builds the eligible-CUPS
	// pool of a freed slot (its doctor + the asuntos of its agenda) for waiting-list slot matching.
	FindCupsForDoctorAndAsuntos(ctx context.Context, medicoID int, asuntos []int) ([]string, error)
}

// EntityRepository — entidades/EPS activas.
// Implementación actual: siesa (tabla: contratos)
type EntityRepository interface {
	FindActive(ctx context.Context) ([]domain.Entity, error)
	FindActiveByCategory(ctx context.Context, category string) ([]domain.Entity, error)
	FindByCode(ctx context.Context, code string) (*domain.Entity, error)
	GetCodeByIndexAndCategory(ctx context.Context, index int, category string) (string, error)
}

// MunicipalityRepository — búsqueda de municipios.
// Implementación actual: siesa (tabla: sis_muni)
type MunicipalityRepository interface {
	Search(ctx context.Context, name string) ([]domain.Municipality, error)
	// SearchBarrios busca barrios del catálogo sis_barrios por nombre, acotados al
	// municipio (depCode, muniCode) ya seleccionado.
	SearchBarrios(ctx context.Context, name, depCode, muniCode string) ([]domain.Barrio, error)
	// CreateNeighborhood crea un barrio en sis_barrios (nombres/municipio/zona/dept; codigo es
	// IDENTITY) y devuelve el código nuevo. Hace una verificación exacta previa para no duplicar.
	CreateNeighborhood(ctx context.Context, name, depCode, muniCode, zone string) (string, error)
}

// Repositories agrupa todas las interfaces para inyección de dependencias.
type Repositories struct {
	Patient      PatientRepository
	Appointment  AppointmentRepository
	Schedule     ScheduleRepository
	Procedure    ProcedureRepository
	Entity       EntityRepository
	Municipality MunicipalityRepository
	Price        PriceRepository
}
