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

// NoShowRow es el KPI de inasistencia (no-show) REAL por día, calculado solo sobre las citas
// PASADAS (fecha < hoy). De las citas que debían atenderse (esperadas = no canceladas), cuántas se
// atendieron (estado 'A') y cuántas quedaron sin finalizar (no-show: ni canceladas ni atendidas).
// Las citas pendientes/confirmadas FUTURAS no cuentan: aún no han ocurrido, no son inasistencia.
type NoShowRow struct {
	Fecha     string `json:"fecha"`      // YYYY-MM-DD
	Esperadas int    `json:"esperadas"`  // citas pasadas no canceladas (= atendidas + sin_cerrar + no_show)
	Atendidas int    `json:"atendidas"`  // estado 'A'
	SinCerrar int    `json:"sin_cerrar"` // confirmada (AsistenciaConfirmada=1 o estado='CC') pero no cerrada — pudo asistir
	NoShow    int    `json:"no_show"`    // no-show PURO: pasada, no cancelada, no atendida y NO confirmada
}

// NoShowLeadRow agrupa el no-show por ANTELACION (dias entre solicitud y cita). Hallazgo 8.1 #2:
// el bucket 0-1d concentraba 25,6% de no-show (56% del total) por quedar fuera del recordatorio
// de las 07:00 - este KPI vigila que el recordatorio de corta antelacion lo haga caer.
type NoShowLeadRow struct {
	Bucket    string `json:"bucket"` // 0-1d | 2-3d | 4-7d | 8-15d | >15d
	Esperadas int    `json:"esperadas"`
	NoShow    int    `json:"no_show"`
}

// BotCreatedRow es el conteo de citas REALES creadas por el bot en SIESA por día (por
// cod_user_asigna_cita = cédula del bot, sobre fecha_solicitud). Es la verdad de negocio que se cruza
// con las sesiones del bot para medir la conversión real (no el evento appointment_created, que es
// solo lo que el bot creyó hacer).
type BotCreatedRow struct {
	Fecha string `json:"fecha"` // YYYY-MM-DD (fecha_solicitud)
	Total int    `json:"total"`
}

// BotAppointmentCup es una cita creada por el bot con su CUPS y médico. Se cruza con cups_medico
// (catálogo local) para detectar médico mal asignado (conciliación bot↔SIESA).
type BotAppointmentCup struct {
	CitaID  int    `json:"cita_id"`
	CodMedi int    `json:"cod_medi"`
	Cups    string `json:"cups"`
	Fecha   string `json:"fecha"` // YYYY-MM-DD (fecha de la cita)
}

// SlotRecoveryDay es el agregado por día (fecha de la CITA) del KPI "recuperación de cupos
// cancelados": cuántos slots con cita quedaron cancelados ese día y cuántos de esos se volvieron
// a ocupar con una cita nueva (mismo médico+fecha+hora, creada después).
type SlotRecoveryDay struct {
	Dia        string `json:"dia"` // YYYY-MM-DD (fecha de la cita/slot)
	Canceladas int    `json:"canceladas"`
	Rellenadas int    `json:"rellenadas"`
}

// SlotRecoveryData es el resultado SIESA del KPI: la serie por día y los IDs de las citas que
// RE-OCUPARON un slot cancelado — el caller los cruza con la BD local para saber cuáles nacieron
// de la lista de espera (waiting_list.appointment_id / flow_events lista_espera/booked).
type SlotRecoveryData struct {
	PorDia        []SlotRecoveryDay `json:"por_dia"`
	RefillCitaIDs []string          `json:"-"`
}
