package domain

import "time"

type Schedule struct {
	ID             int
	DoctorDocument string
	Name           string
}

// AvailableSlotRow is a single free SIESA slot returned by the unified slot query
// (ScheduleRepository.FindAvailableSlots). Each row from programacion_medico_detalle
// IS a slot — SIESA pre-generates them, so no in-memory slot calculation is needed.
type AvailableSlotRow struct {
	SlotTime        time.Time // programacion_medico_detalle.Fecha (date + time of the slot)
	DoctorDocument  string    // sis_medi.cedula — used for age/preferred-doctor filtering
	DoctorName      string    // RTRIM(sis_medi.nombre)
	DoctorSiesaCode string    // sis_medi.codigo — used as cod_medi when creating the appointment
	AgendaID        int       // programacion_medico.id (= IdProgramacionMedico)
	DurationMin     int       // programacion_medico.intervalo (slot length in minutes)
	AgendaSede      int       // programacion_medico.id_sede
}

// Deprecated: ScheduleConfig was the Antares-era per-day schedule template. SIESA stores
// pre-generated slots, so it is no longer used by the slot service. Retained only until the
// legacy datosipsndx package and the internal exception endpoints are removed.
type ScheduleConfig struct {
	ID                     int
	DoctorDocument         string
	AppointmentDuration    int // minutos
	IsActive               bool
	AgendaID               int
	SessionsPerAppointment int
	WorkDays               [7]bool   // 0=domingo..6=sábado
	MorningStart           [7]string // HH:mm por día
	MorningEnd             [7]string
	AfternoonStart         [7]string
	AfternoonEnd           [7]string
}

type WorkingDay struct {
	DoctorDocument   string
	DoctorSiesaCode  string // sis_medi.codigo (código interno SIESA), usado como cod_medi en citas
	Date             string // YYYY-MM-DD
	MorningEnabled   bool
	AfternoonEnabled bool
	AgendaID         int
}
