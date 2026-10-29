package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// RescheduleContext arma el contexto de sesión para reprogramar appt desde una notificación: los
// datos del paciente, del procedimiento y las claves de reprogramación que leen SEARCH_SLOTS y
// createAppointmentHandler. Es el armado ÚNICO de las sesiones de reprogramación de notificaciones
// (startConfirmRescheduleSession, startSelfReschedule) y de «Reprogramar» en NOTIF_PENDING, para
// que el atajo del agente y el paso de la máquina reprogramen igual. blockLen es el número de citas
// del bloque (piso de los espacios). Quien llama añade lo propio de su canal (conversación, id del
// mensaje, textos) y cambia reschedule_skip_cancel cuando la cita ya fue cancelada.
//
// patient_contract es el contrato de la cita (appt.Entity = citas.contrato): effectiveContract
// re-valida con él la regla MRC/Evento por CUPS, igual que al crear. patient_age = "0" omite las
// restricciones de edad (ya se validaron al agendar la cita original).
func (s *AppointmentService) RescheduleContext(ctx context.Context, appt *domain.Appointment, blockLen int) map[string]string {
	// Modelo multi-slot: una cita ocupa N slots (programacion_medico_detalle.IdCita). El número de
	// espacios a re-reservar es el conteo real de slots de la cita, NO blockLen — que con este
	// modelo siempre vale 1 sin importar el tamaño real.
	espacios := blockLen
	if n, err := s.SlotCountForAppointment(ctx, appt.ID); err == nil && n > espacios {
		espacios = n
	}
	if espacios < 1 {
		espacios = 1
	}

	cupsCode, cupsName := "", ""
	if len(appt.Procedures) > 0 {
		cupsCode = appt.Procedures[0].CupCode
		cupsName = appt.Procedures[0].CupName
	}

	// Contraste: observación de SIESA + nombre del/los CUPS (una cita creada por un agente puede no
	// traer "Contrastada" en la observación pero sí "CON CONTRASTE" en el nombre → sin esto se
	// reprogramaría como simple y podría ofrecer un slot fuera de la ventana de contraste).
	procNames := []string{cupsName}
	for _, p := range appt.Procedures {
		procNames = append(procNames, p.CupName)
	}
	isContrasted := "0"
	if utils.AppointmentIsContrasted(appt.Observations, procNames...) {
		isContrasted = "1"
	}
	// Sedación: observación + asunto de la cita (17 = agenda de sedación, señal fiable cuando el
	// agente no escribió la observación).
	asunto, _ := s.AppointmentAsunto(ctx, appt.ID)
	isSedated := "0"
	if utils.AppointmentIsSedated(appt.Observations, asunto) {
		isSedated = "1"
	}

	// procedures_json, que necesita createAppointmentHandler.
	cups := make([]CUPSEntry, 0, len(appt.Procedures))
	for _, p := range appt.Procedures {
		cups = append(cups, CUPSEntry{Code: p.CupCode, Name: p.CupName, Quantity: 1})
	}
	proceduresJSON, _ := json.Marshal([]CUPSGroup{{ServiceType: "general", Cups: cups, Espacios: espacios}})

	return map[string]string{
		"patient_id":       appt.PatientID,
		"patient_name":     appt.PatientName,
		"patient_entity":   appt.Entity,
		"patient_contract": appt.Entity,
		"patient_age":      "0",

		"cups_code":       cupsCode,
		"cups_name":       cupsName,
		"is_contrasted":   isContrasted,
		"is_sedated":      isSedated,
		"espacios":        fmt.Sprintf("%d", espacios),
		"procedures_json": string(proceduresJSON),

		"total_procedures":      "1",
		"current_procedure_idx": "0",
		"menu_option":           "agendar",

		"reschedule_appt_id":     appt.ID,
		"reschedule_skip_cancel": "0",
		// N14: la cédula (sis_medi.cedula), no cod_medi: slot_service compara contra DoctorDocument.
		"preferred_doctor_doc": appt.DoctorDocument,
	}
}
