package domain

// CreateAppointmentProcedureInput is the input for registering one procedure of an
// appointment. In SIESA this becomes a row in citas_procedimientos (procedures/imaging)
// or citas_procedimientos_asuntos (consultations). Replaces the Antares-era "PxCita" naming.
type CreateAppointmentProcedureInput struct {
	AppointmentID int     `json:"appointment_id"`
	CupCode       string  `json:"cup_code"`
	Quantity      int     `json:"quantity"`
	UnitValue     float64 `json:"unit_value"`
	ServiceID     int     `json:"service_id"`
}
