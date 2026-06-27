// Package domain define los tipos de dominio (entidades del negocio) compartidos por el bot.
package domain

// Tipos de KPI de SIESA expuestos (solo lectura, agregados) al dashboard. Las consultas que los
// producen usan WITH (NOLOCK), agregan en el servidor (GROUP BY) y se acotan por fecha, de modo
// que devuelven decenas de filas (no miles) y no bloquean la UI de SIESA. Ver siesa.AnalyticsRepo.

// OccupancyRow es la ocupación de la agenda de un médico en un día: slots ocupados vs libres.
type OccupancyRow struct {
	Medico   int    `json:"medico"` // sis_medi.codigo
	Nombre   string `json:"nombre"`
	Fecha    string `json:"fecha"` // YYYY-MM-DD
	Ocupados int    `json:"ocupados"`
	Libres   int    `json:"libres"`
}

// AppointmentStateRow es el conteo de citas por día y SITUACIÓN, usando la definición de SIESA que
// dedujimos (ver siesa.AppointmentRepo): cancelada = estado 'C'; confirmada = AsistenciaConfirmada=1
// o estado 'CC'; atendida = estado 'A'; pendiente = el resto. NO es el estado crudo (CC no es
// cancelada; la confirmación vive en AsistenciaConfirmada, no solo en el estado).
type AppointmentStateRow struct {
	Fecha     string `json:"fecha"`     // YYYY-MM-DD
	Situacion string `json:"situacion"` // pendiente | confirmada | atendida | cancelada
	Total     int    `json:"total"`
}

// BotAppointmentCup es una cita creada por el bot con su CUPS y médico. Se cruza con cups_medico
// (catálogo local) para detectar médico mal asignado (conciliación bot↔SIESA).
type BotAppointmentCup struct {
	CitaID  int    `json:"cita_id"`
	CodMedi int    `json:"cod_medi"`
	Cups    string `json:"cups"`
	Fecha   string `json:"fecha"` // YYYY-MM-DD (fecha de la cita)
}
