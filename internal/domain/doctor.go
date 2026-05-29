package domain

type Doctor struct {
	Document      string
	FullName      string
	CupID         int
	IsActive      bool
	ConsultorioID int // SIESA: id_consultorio from programacion_medico_relacion (0 = not applicable)
}

type Schedule struct {
	ID             int
	DoctorDocument string
	Name           string
}

type ScheduleConfig struct {
	ID                     int
	DoctorDocument         string
	AppointmentDuration    int // minutos
	IsActive               bool
	AgendaID               int
	SessionsPerAppointment int
	WorkDays               [7]bool          // 0=domingo..6=sábado
	MorningStart           [7]string        // HH:mm por día
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
