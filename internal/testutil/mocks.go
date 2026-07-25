package testutil

import (
	"context"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// === Repository Mocks ===

// MockPatientRepo implements repository.PatientRepository.
type MockPatientRepo struct {
	FindByDocumentFn     func(ctx context.Context, docType, doc string) (*domain.Patient, error)
	FindByIDFn           func(ctx context.Context, id string) (*domain.Patient, error)
	CreateFn             func(ctx context.Context, input domain.CreatePatientInput) (string, error)
	UpdateEntityFn       func(ctx context.Context, patientID, entityCode string) error
	UpdateContractFn     func(ctx context.Context, patientID, contractCode string) error
	UpdateMunicipalityFn func(ctx context.Context, patientID, depCode, muniCode string) error
	UpdateContactInfoFn  func(ctx context.Context, patientID, phone, email string) error
}

func (m *MockPatientRepo) FindByDocument(ctx context.Context, docType, doc string) (*domain.Patient, error) {
	if m.FindByDocumentFn != nil {
		return m.FindByDocumentFn(ctx, docType, doc)
	}
	return nil, nil
}

func (m *MockPatientRepo) FindByID(ctx context.Context, id string) (*domain.Patient, error) {
	if m.FindByIDFn != nil {
		return m.FindByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *MockPatientRepo) Create(ctx context.Context, input domain.CreatePatientInput) (string, error) {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, input)
	}
	return "new-id", nil
}

func (m *MockPatientRepo) UpdateEntity(ctx context.Context, patientID, entityCode string) error {
	if m.UpdateEntityFn != nil {
		return m.UpdateEntityFn(ctx, patientID, entityCode)
	}
	return nil
}

func (m *MockPatientRepo) UpdateContract(ctx context.Context, patientID, contractCode string) error {
	if m.UpdateContractFn != nil {
		return m.UpdateContractFn(ctx, patientID, contractCode)
	}
	return nil
}

func (m *MockPatientRepo) UpdateMunicipality(ctx context.Context, patientID, depCode, muniCode string) error {
	if m.UpdateMunicipalityFn != nil {
		return m.UpdateMunicipalityFn(ctx, patientID, depCode, muniCode)
	}
	return nil
}

func (m *MockPatientRepo) UpdateContactInfo(ctx context.Context, patientID, phone, email string) error {
	if m.UpdateContactInfoFn != nil {
		return m.UpdateContactInfoFn(ctx, patientID, phone, email)
	}
	return nil
}

// MockAppointmentRepo implements repository.AppointmentRepository.
type MockAppointmentRepo struct {
	FindByIDFn                   func(ctx context.Context, id string) (*domain.Appointment, error)
	FindUpcomingByPatientFn      func(ctx context.Context, patientID string) ([]domain.Appointment, error)
	FindPendingEmgAppointmentFn  func(ctx context.Context, patientID string, emgCodes []string) (*domain.Appointment, error)
	FindByAgendaAndDateFn        func(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error)
	CreateFn                     func(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error)
	CreateAppointmentProcedureFn func(ctx context.Context, input domain.CreateAppointmentProcedureInput) error
	ConfirmFn                    func(ctx context.Context, id string, channel, channelID string) error
	CancelFn                     func(ctx context.Context, id string, reason, channel, channelID string) error
	ConfirmBatchFn               func(ctx context.Context, ids []string, channel, channelID string) error
	CancelBatchFn                func(ctx context.Context, ids []string, reason, channel, channelID string) error
	DeleteBatchFn                func(ctx context.Context, ids []string) error
	HasFutureForCupFn            func(ctx context.Context, patientID, cupCode string) (bool, error)
	FindLastDoctorForCupsFn      func(ctx context.Context, patientID string, cups []string) (string, error)
	CountMonthlyByGroupFn        func(ctx context.Context, cupsCodes []string, year, month int) (int, error)
	FindPendingByDateFn          func(ctx context.Context, date string) ([]domain.Appointment, error)
	RescheduleDayOfAgendaFn      func(ctx context.Context, in domain.RescheduleDayInput) (domain.RescheduleDayResult, error)
	SlotCountForAppointmentFn    func(ctx context.Context, apptID string) (int, error)
	WriteCreationAuditFn         func(ctx context.Context, appointmentID, observations string)
}

func (m *MockAppointmentRepo) FindByID(ctx context.Context, id string) (*domain.Appointment, error) {
	if m.FindByIDFn != nil {
		return m.FindByIDFn(ctx, id)
	}
	return nil, nil
}

// FindPendingEmgAppointment mock: devuelve FindPendingEmgAppointmentFn si está configurado, si no nil.
func (m *MockAppointmentRepo) FindPendingEmgAppointment(ctx context.Context, patientID string, emgCodes []string) (*domain.Appointment, error) {
	if m.FindPendingEmgAppointmentFn != nil {
		return m.FindPendingEmgAppointmentFn(ctx, patientID, emgCodes)
	}
	return nil, nil
}

func (m *MockAppointmentRepo) FindUpcomingByPatient(ctx context.Context, patientID string) ([]domain.Appointment, error) {
	if m.FindUpcomingByPatientFn != nil {
		return m.FindUpcomingByPatientFn(ctx, patientID)
	}
	return nil, nil
}

func (m *MockAppointmentRepo) FindByAgendaAndDate(ctx context.Context, agendaID int, date string) ([]domain.Appointment, error) {
	if m.FindByAgendaAndDateFn != nil {
		return m.FindByAgendaAndDateFn(ctx, agendaID, date)
	}
	return nil, nil
}

// FindAgendasByDoctor stub del mock (módulo Agenda).
func (m *MockAppointmentRepo) FindAgendasByDoctor(_ context.Context, _, _ string) ([]domain.AgendaSummary, error) {
	return nil, nil
}

// FindAgendaAppointmentsPaged stub del mock (módulo Agenda).
func (m *MockAppointmentRepo) FindAgendaAppointmentsPaged(_ context.Context, _ domain.AgendaAppointmentsFilter) (*domain.AgendaAppointmentsPage, error) {
	return &domain.AgendaAppointmentsPage{}, nil
}

func (m *MockAppointmentRepo) Create(ctx context.Context, input domain.CreateAppointmentInput) (*domain.Appointment, error) {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, input)
	}
	return &domain.Appointment{ID: "apt-new"}, nil
}

func (m *MockAppointmentRepo) CreateAppointmentProcedure(ctx context.Context, input domain.CreateAppointmentProcedureInput) error {
	if m.CreateAppointmentProcedureFn != nil {
		return m.CreateAppointmentProcedureFn(ctx, input)
	}
	return nil
}

func (m *MockAppointmentRepo) CreateAppointmentProcedureBatch(ctx context.Context, inputs []domain.CreateAppointmentProcedureInput) error {
	return nil
}

func (m *MockAppointmentRepo) Confirm(ctx context.Context, id string, channel, channelID string) error {
	if m.ConfirmFn != nil {
		return m.ConfirmFn(ctx, id, channel, channelID)
	}
	return nil
}

func (m *MockAppointmentRepo) Cancel(ctx context.Context, id string, reason, channel, channelID string) error {
	if m.CancelFn != nil {
		return m.CancelFn(ctx, id, reason, channel, channelID)
	}
	return nil
}

func (m *MockAppointmentRepo) ConfirmBatch(ctx context.Context, ids []string, channel, channelID string) error {
	if m.ConfirmBatchFn != nil {
		return m.ConfirmBatchFn(ctx, ids, channel, channelID)
	}
	for _, id := range ids {
		if err := m.Confirm(ctx, id, channel, channelID); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockAppointmentRepo) CancelBatch(ctx context.Context, ids []string, reason, channel, channelID string) error {
	if m.CancelBatchFn != nil {
		return m.CancelBatchFn(ctx, ids, reason, channel, channelID)
	}
	for _, id := range ids {
		if err := m.Cancel(ctx, id, reason, channel, channelID); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockAppointmentRepo) DeleteBatch(ctx context.Context, ids []string) error {
	if m.DeleteBatchFn != nil {
		return m.DeleteBatchFn(ctx, ids)
	}
	return nil
}

func (m *MockAppointmentRepo) HasFutureForCup(ctx context.Context, patientID, cupCode string) (bool, error) {
	if m.HasFutureForCupFn != nil {
		return m.HasFutureForCupFn(ctx, patientID, cupCode)
	}
	return false, nil
}

func (m *MockAppointmentRepo) FindLastDoctorForCups(ctx context.Context, patientID string, cups []string) (string, error) {
	if m.FindLastDoctorForCupsFn != nil {
		return m.FindLastDoctorForCupsFn(ctx, patientID, cups)
	}
	return "", nil
}

func (m *MockAppointmentRepo) CountMonthlyByGroup(ctx context.Context, cupsCodes []string, year, month int, _ string) (int, error) {
	if m.CountMonthlyByGroupFn != nil {
		return m.CountMonthlyByGroupFn(ctx, cupsCodes, year, month)
	}
	return 0, nil
}

func (m *MockAppointmentRepo) FindPendingByDate(ctx context.Context, date string) ([]domain.Appointment, error) {
	if m.FindPendingByDateFn != nil {
		return m.FindPendingByDateFn(ctx, date)
	}
	return nil, nil
}

// RescheduleDayOfAgenda mueve un día de agenda (mock).
func (m *MockAppointmentRepo) RescheduleDayOfAgenda(ctx context.Context, in domain.RescheduleDayInput) (domain.RescheduleDayResult, error) {
	if m.RescheduleDayOfAgendaFn != nil {
		return m.RescheduleDayOfAgendaFn(ctx, in)
	}
	return domain.RescheduleDayResult{}, nil
}

// FindDoctorAgendasOnDate lista agendas del médico en una fecha (mock).
func (m *MockAppointmentRepo) FindDoctorAgendasOnDate(_ context.Context, _, _ string) ([]domain.DoctorAgendaOnDate, error) {
	return nil, nil
}

// AppointmentAsunto returns 0 by default (mock; real impl reads citas.asunto).
func (m *MockAppointmentRepo) AppointmentAsunto(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// SlotCountForAppointment returns the configured slot count (or 0 by default).
func (m *MockAppointmentRepo) SlotCountForAppointment(ctx context.Context, apptID string) (int, error) {
	if m.SlotCountForAppointmentFn != nil {
		return m.SlotCountForAppointmentFn(ctx, apptID)
	}
	return 0, nil
}

// WriteCreationAudit invokes the configured function (no-op by default).
func (m *MockAppointmentRepo) WriteCreationAudit(ctx context.Context, appointmentID, observations string) {
	if m.WriteCreationAuditFn != nil {
		m.WriteCreationAuditFn(ctx, appointmentID, observations)
	}
}

// MockScheduleRepo implements repository.ScheduleRepository.
type MockScheduleRepo struct {
	FindAvailableSlotsFn            func(ctx context.Context, asuntoID int, afterDate string, allowedDoctors []int) ([]domain.AvailableSlotRow, error)
	GetAsuntosByAgendaFn            func(ctx context.Context, agendaID int) ([]int, error)
	FindByScheduleIDFn              func(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error)
	FindWorkingDayExceptionFn       func(ctx context.Context, agendaID int, doctorDoc, date string) (*domain.WorkingDay, error)
	UpdateWorkingDayExceptionDateFn func(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (bool, error)
	DeleteWorkingDayExceptionFn     func(ctx context.Context, agendaID int, doctorDoc, date string) (bool, error)
}

// FindAvailableSlots implements repository.ScheduleRepository.
func (m *MockScheduleRepo) FindAvailableSlots(ctx context.Context, asuntoID int, afterDate string, allowedDoctors []int) ([]domain.AvailableSlotRow, error) {
	if m.FindAvailableSlotsFn != nil {
		return m.FindAvailableSlotsFn(ctx, asuntoID, afterDate, allowedDoctors)
	}
	return nil, nil
}

// GetAsuntosByAgenda mock: devuelve GetAsuntosByAgendaFn si está configurado, si no nil.
func (m *MockScheduleRepo) GetAsuntosByAgenda(ctx context.Context, agendaID int) ([]int, error) {
	if m.GetAsuntosByAgendaFn != nil {
		return m.GetAsuntosByAgendaFn(ctx, agendaID)
	}
	return nil, nil
}

func (m *MockScheduleRepo) FindByScheduleID(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error) {
	if m.FindByScheduleIDFn != nil {
		return m.FindByScheduleIDFn(ctx, scheduleID, scheduleType)
	}
	return nil, nil
}

func (m *MockScheduleRepo) FindWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (*domain.WorkingDay, error) {
	if m.FindWorkingDayExceptionFn != nil {
		return m.FindWorkingDayExceptionFn(ctx, agendaID, doctorDoc, date)
	}
	return nil, nil
}

func (m *MockScheduleRepo) UpdateWorkingDayExceptionDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (bool, error) {
	if m.UpdateWorkingDayExceptionDateFn != nil {
		return m.UpdateWorkingDayExceptionDateFn(ctx, agendaID, doctorDoc, oldDate, newDate)
	}
	return false, nil
}

func (m *MockScheduleRepo) DeleteWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (bool, error) {
	if m.DeleteWorkingDayExceptionFn != nil {
		return m.DeleteWorkingDayExceptionFn(ctx, agendaID, doctorDoc, date)
	}
	return false, nil
}

// MockProcedureRepo implements repository.ProcedureRepository.
type MockProcedureRepo struct {
	FindByCodeFn                  func(ctx context.Context, code string) (*domain.Procedure, error)
	FindByIDFn                    func(ctx context.Context, id int) (*domain.Procedure, error)
	SearchByNameFn                func(ctx context.Context, name string) ([]domain.Procedure, error)
	FindAllActiveFn               func(ctx context.Context) ([]domain.Procedure, error)
	FindSubjectTypeForCupsFn      func(ctx context.Context, cupsCode string) (int, error)
	FindMedicosForCupsFn          func(ctx context.Context, cupsCode string) ([]int, error)
	FindCupsForDoctorAndAsuntosFn func(ctx context.Context, medicoID int, asuntos []int) ([]string, error)
}

func (m *MockProcedureRepo) FindSubjectTypeForCups(ctx context.Context, cupsCode string) (int, error) {
	if m.FindSubjectTypeForCupsFn != nil {
		return m.FindSubjectTypeForCupsFn(ctx, cupsCode)
	}
	return 0, nil
}

// FindMedicosForCups implements repository.ProcedureRepository.
func (m *MockProcedureRepo) FindMedicosForCups(ctx context.Context, cupsCode string) ([]int, error) {
	if m.FindMedicosForCupsFn != nil {
		return m.FindMedicosForCupsFn(ctx, cupsCode)
	}
	// Default: un médico mapeado, para que el gate estricto de cups_medico (slot_service) no corte los
	// slots en los tests que no prueban ese gate. Los que sí lo prueban fijan FindMedicosForCupsFn.
	return []int{1}, nil
}

// FindCupsForDoctorAndAsuntos mock: devuelve FindCupsForDoctorAndAsuntosFn si está configurado, si no nil.
func (m *MockProcedureRepo) FindCupsForDoctorAndAsuntos(ctx context.Context, medicoID int, asuntos []int) ([]string, error) {
	if m.FindCupsForDoctorAndAsuntosFn != nil {
		return m.FindCupsForDoctorAndAsuntosFn(ctx, medicoID, asuntos)
	}
	return nil, nil
}

func (m *MockProcedureRepo) FindByCode(ctx context.Context, code string) (*domain.Procedure, error) {
	if m.FindByCodeFn != nil {
		return m.FindByCodeFn(ctx, code)
	}
	return nil, nil
}

func (m *MockProcedureRepo) FindByID(ctx context.Context, id int) (*domain.Procedure, error) {
	if m.FindByIDFn != nil {
		return m.FindByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *MockProcedureRepo) SearchByName(ctx context.Context, name string) ([]domain.Procedure, error) {
	if m.SearchByNameFn != nil {
		return m.SearchByNameFn(ctx, name)
	}
	return nil, nil
}

func (m *MockProcedureRepo) FindAllActive(ctx context.Context) ([]domain.Procedure, error) {
	if m.FindAllActiveFn != nil {
		return m.FindAllActiveFn(ctx)
	}
	return nil, nil
}

// MockEntityRepo implements repository.EntityRepository.
type MockEntityRepo struct {
	FindActiveFn                func(ctx context.Context) ([]domain.Entity, error)
	FindActiveByCategoryFn      func(ctx context.Context, category string) ([]domain.Entity, error)
	FindByCodeFn                func(ctx context.Context, code string) (*domain.Entity, error)
	GetCodeByIndexAndCategoryFn func(ctx context.Context, index int, category string) (string, error)
}

func (m *MockEntityRepo) FindActive(ctx context.Context) ([]domain.Entity, error) {
	if m.FindActiveFn != nil {
		return m.FindActiveFn(ctx)
	}
	return nil, nil
}

func (m *MockEntityRepo) FindActiveByCategory(ctx context.Context, category string) ([]domain.Entity, error) {
	if m.FindActiveByCategoryFn != nil {
		return m.FindActiveByCategoryFn(ctx, category)
	}
	return nil, nil
}

func (m *MockEntityRepo) FindByCode(ctx context.Context, code string) (*domain.Entity, error) {
	if m.FindByCodeFn != nil {
		return m.FindByCodeFn(ctx, code)
	}
	return nil, nil
}

func (m *MockEntityRepo) GetCodeByIndexAndCategory(ctx context.Context, index int, category string) (string, error) {
	if m.GetCodeByIndexAndCategoryFn != nil {
		return m.GetCodeByIndexAndCategoryFn(ctx, index, category)
	}
	return "", nil
}

// MockMunicipalityRepo implements repository.MunicipalityRepository.
type MockMunicipalityRepo struct {
	SearchFn        func(ctx context.Context, name string) ([]domain.Municipality, error)
	SearchBarriosFn func(ctx context.Context, name, depCode, muniCode string) ([]domain.Barrio, error)
}

func (m *MockMunicipalityRepo) Search(ctx context.Context, name string) ([]domain.Municipality, error) {
	if m.SearchFn != nil {
		return m.SearchFn(ctx, name)
	}
	return nil, nil
}

func (m *MockMunicipalityRepo) SearchBarrios(ctx context.Context, name, depCode, muniCode string) ([]domain.Barrio, error) {
	if m.SearchBarriosFn != nil {
		return m.SearchBarriosFn(ctx, name, depCode, muniCode)
	}
	return nil, nil
}

// MockPriceRepo implements repository.PriceRepository.
type MockPriceRepo struct {
	FindPriceFn func(ctx context.Context, cupCode, entityCode string) (float64, error)
}

// FindPrice devuelve el precio configurado (o 0 por defecto).
func (m *MockPriceRepo) FindPrice(ctx context.Context, cupCode, entityCode string) (float64, error) {
	if m.FindPriceFn != nil {
		return m.FindPriceFn(ctx, cupCode, entityCode)
	}
	return 0, nil
}

// === Session Mock ===

// MockSessionRepo implements session.SessionRepo with in-memory storage.
type MockSessionRepo struct {
	FindActiveByPhoneFn  func(ctx context.Context, phone string) (*session.Session, error)
	FindCurrentByPhoneFn func(ctx context.Context, phone string) (*session.Session, error)
	CreateFn             func(ctx context.Context, s *session.Session) error
	SaveFn               func(ctx context.Context, s *session.Session) error
	UpdateStatusFn       func(ctx context.Context, sessionID, status string) error
	RenewExpiryFn        func(ctx context.Context, sessionID string, expiresAt time.Time) error
	ExpireSessionsFn     func(ctx context.Context) (int64, error)
	SetContextFn         func(ctx context.Context, sessionID, key, value string) error
	SetContextBatchFn    func(ctx context.Context, sessionID string, kvs map[string]string) error
	GetContextFn         func(ctx context.Context, sessionID, key string) (string, error)
	GetAllContextFn      func(ctx context.Context, sessionID string) (map[string]string, error)
	ClearContextFn       func(ctx context.Context, sessionID string, keys ...string) error
	ClearAllContextFn    func(ctx context.Context, sessionID string) error
}

func (m *MockSessionRepo) FindActiveByPhone(ctx context.Context, phone string) (*session.Session, error) {
	if m.FindActiveByPhoneFn != nil {
		return m.FindActiveByPhoneFn(ctx, phone)
	}
	return nil, nil
}

// FindCurrentByPhone devuelve la sesión activa/escalada sin filtro de expiry (delegable por test).
func (m *MockSessionRepo) FindCurrentByPhone(ctx context.Context, phone string) (*session.Session, error) {
	if m.FindCurrentByPhoneFn != nil {
		return m.FindCurrentByPhoneFn(ctx, phone)
	}
	// Fallback: mismo comportamiento que FindActiveByPhone si el test no lo stubea.
	return m.FindActiveByPhone(ctx, phone)
}

func (m *MockSessionRepo) Create(ctx context.Context, s *session.Session) error {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, s)
	}
	return nil
}

func (m *MockSessionRepo) Save(ctx context.Context, s *session.Session) error {
	if m.SaveFn != nil {
		return m.SaveFn(ctx, s)
	}
	return nil
}

func (m *MockSessionRepo) UpdateStatus(ctx context.Context, sessionID, status string) error {
	if m.UpdateStatusFn != nil {
		return m.UpdateStatusFn(ctx, sessionID, status)
	}
	return nil
}

func (m *MockSessionRepo) RenewExpiry(ctx context.Context, sessionID string, expiresAt time.Time) error {
	if m.RenewExpiryFn != nil {
		return m.RenewExpiryFn(ctx, sessionID, expiresAt)
	}
	return nil
}

func (m *MockSessionRepo) ExpireSessions(ctx context.Context) (int64, error) {
	if m.ExpireSessionsFn != nil {
		return m.ExpireSessionsFn(ctx)
	}
	return 0, nil
}

func (m *MockSessionRepo) SetContext(ctx context.Context, sessionID, key, value string) error {
	if m.SetContextFn != nil {
		return m.SetContextFn(ctx, sessionID, key, value)
	}
	return nil
}

func (m *MockSessionRepo) SetContextBatch(ctx context.Context, sessionID string, kvs map[string]string) error {
	if m.SetContextBatchFn != nil {
		return m.SetContextBatchFn(ctx, sessionID, kvs)
	}
	return nil
}

func (m *MockSessionRepo) GetContext(ctx context.Context, sessionID, key string) (string, error) {
	if m.GetContextFn != nil {
		return m.GetContextFn(ctx, sessionID, key)
	}
	return "", nil
}

func (m *MockSessionRepo) GetAllContext(ctx context.Context, sessionID string) (map[string]string, error) {
	if m.GetAllContextFn != nil {
		return m.GetAllContextFn(ctx, sessionID)
	}
	return make(map[string]string), nil
}

func (m *MockSessionRepo) ClearContext(ctx context.Context, sessionID string, keys ...string) error {
	if m.ClearContextFn != nil {
		return m.ClearContextFn(ctx, sessionID, keys...)
	}
	return nil
}

func (m *MockSessionRepo) ClearAllContext(ctx context.Context, sessionID string) error {
	if m.ClearAllContextFn != nil {
		return m.ClearAllContextFn(ctx, sessionID)
	}
	return nil
}

func (m *MockSessionRepo) MarkEscalated(ctx context.Context, sessionID, teamID string) error {
	return nil
}

func (m *MockSessionRepo) ResumeSession(ctx context.Context, sessionID, newState string, timeoutMinutes int) error {
	return nil
}

func (m *MockSessionRepo) FindInactiveSessions(ctx context.Context, idleMinutes int) ([]session.InactiveSession, error) {
	return nil, nil
}

func (m *MockSessionRepo) FindExpiredEscalatedSessions(ctx context.Context) ([]session.ExpiredEscalatedSession, error) {
	return nil, nil
}

// FindEscalatedSessions is a no-op mock.
func (m *MockSessionRepo) FindEscalatedSessions(_ context.Context) ([]session.EscalatedSession, error) {
	return nil, nil
}

// UpdateConversationIDByPhone is a no-op mock.
func (m *MockSessionRepo) UpdateConversationIDByPhone(_ context.Context, _, _ string) error {
	return nil
}

// TouchPatientActivity is a no-op mock.
func (m *MockSessionRepo) TouchPatientActivity(_ context.Context, _ string, _ time.Time) error {
	return nil
}

// TouchAgentActivity is a no-op mock.
func (m *MockSessionRepo) TouchAgentActivity(_ context.Context, _ string) error {
	return nil
}

// IncrementAgentReminders is a no-op mock.
func (m *MockSessionRepo) IncrementAgentReminders(_ context.Context, _ string) error {
	return nil
}

func (m *MockSessionRepo) MarkAbandoned(ctx context.Context, sessionID string) error {
	return nil
}

func (m *MockSessionRepo) CompleteActiveByPhone(ctx context.Context, phone string) error {
	return nil
}

// === WaitingList Mock ===

// MockWaitingListCreator implements handlers.WaitingListCreator.
type MockWaitingListCreator struct {
	CreateFn                     func(ctx context.Context, entry *domain.WaitingListEntry) error
	HasActiveForPatientAndCupsFn func(ctx context.Context, patientID, cupsCode string) (bool, error)
	UpdateStatusFn               func(ctx context.Context, id, status string) error
	GetActiveByPatientFn         func(ctx context.Context, patientID string) ([]domain.WaitingListEntry, error)
	FindByIDFn                   func(ctx context.Context, id string) (*domain.WaitingListEntry, error)
}

// GetActiveByPatient simula las entradas activas de un paciente en lista de espera.
func (m *MockWaitingListCreator) GetActiveByPatient(ctx context.Context, patientID string) ([]domain.WaitingListEntry, error) {
	if m.GetActiveByPatientFn != nil {
		return m.GetActiveByPatientFn(ctx, patientID)
	}
	return nil, nil
}

// FindByID simula la búsqueda de una entrada de lista de espera por ID.
func (m *MockWaitingListCreator) FindByID(ctx context.Context, id string) (*domain.WaitingListEntry, error) {
	if m.FindByIDFn != nil {
		return m.FindByIDFn(ctx, id)
	}
	return nil, nil
}

// UpdateStatus registra/simula el cambio de estado de una entrada de lista de espera.
func (m *MockWaitingListCreator) UpdateStatus(ctx context.Context, id, status string) error {
	if m.UpdateStatusFn != nil {
		return m.UpdateStatusFn(ctx, id, status)
	}
	return nil
}

func (m *MockWaitingListCreator) Create(ctx context.Context, entry *domain.WaitingListEntry) error {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, entry)
	}
	return nil
}

func (m *MockWaitingListCreator) HasActiveForPatientAndCups(ctx context.Context, patientID, cupsCode string) (bool, error) {
	if m.HasActiveForPatientAndCupsFn != nil {
		return m.HasActiveForPatientAndCupsFn(ctx, patientID, cupsCode)
	}
	return false, nil
}

// === Event Tracking Mock ===

// MockEventRepo records calls for assertion.
type MockEventRepo struct {
	InsertedEvents []local.ChatEvent
	InsertFn       func(ctx context.Context, event *local.ChatEvent) error
	InsertBatchFn  func(ctx context.Context, events []local.ChatEvent) error
}

func (m *MockEventRepo) Insert(ctx context.Context, event *local.ChatEvent) error {
	m.InsertedEvents = append(m.InsertedEvents, *event)
	if m.InsertFn != nil {
		return m.InsertFn(ctx, event)
	}
	return nil
}

func (m *MockEventRepo) InsertBatch(ctx context.Context, events []local.ChatEvent) error {
	m.InsertedEvents = append(m.InsertedEvents, events...)
	if m.InsertBatchFn != nil {
		return m.InsertBatchFn(ctx, events)
	}
	return nil
}

// === Statemachine Event Helper ===

// MakeEvent creates a statemachine.Event for test assertions.
func MakeEvent(eventType string) statemachine.Event {
	return statemachine.Event{Type: eventType}
}

// CancelBatchAndBlockSlots stub del mock (módulo Agenda).
func (m *MockAppointmentRepo) CancelBatchAndBlockSlots(_ context.Context, _ []string, _, _, _ string) error {
	return nil
}
