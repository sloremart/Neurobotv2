package handlers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// LOS DATOS DEL CASO EN EL RESUMEN.
//
// Además del paciente, el agente necesita ver dónde se atascó el caso: la entidad con la que el
// paciente se identificó, lo que el OCR leyó de la orden, qué grupo de procedimientos iba, qué
// horario eligió, a qué cita se refiere la notificación y qué escribió por última vez. Cada dato
// sale de una clave de la sesión; todas las líneas son `optional`, así que una sesión que no las
// tiene produce EXACTAMENTE el resumen de antes (los dorados viejos no cambian).
//
// El orden de los grupos en el resumen lo fija summaryGroupOrder, no el orden en que se escriben.

// oneLine deja un valor libre en una sola línea: el texto del resumen es «Rotulo: valor» por línea
// y un salto dentro del valor partiría la línea en dos.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// writeCaseGroups añade los grupos con los datos del caso (entidad, orden, grupo, agenda, cita,
// búsqueda e intento). cupsCode es el CUPS ya elegido: antes de elegirlo la orden aún no está
// agrupada y el agente necesita lo que se leyó de ella.
func writeCaseGroups(doc *summaryDoc, sess *session.Session, cupsCode string) {
	writeEntityGroup(doc, sess)
	if cupsCode == "" {
		writeOrderGroup(doc, sess)
	}
	writeProcedureGroupsGroup(doc, sess)
	writeScheduleGroup(doc, sess)
	writeAppointmentGroup(doc, sess)
	writeSearchGroup(doc, sess)
	doc.untitled("intento").optional("ultimo_mensaje", "Último mensaje del paciente",
		oneLine(sess.GetContext(sm.CtxEscalationLastInput)))
}

func writeEntityGroup(doc *summaryDoc, sess *session.Session) {
	g := doc.untitled("entidad")
	g.optional("tipo_cliente", "Tipo de cliente", sess.GetContext("client_type"))
	entity := sess.GetContext("selected_entity_name")
	if entity == "" {
		entity = sess.GetContext("patient_entity")
	}
	g.optional("entidad", "Entidad", entity)
	g.optional("codigo_entidad", "Código de entidad", sess.GetContext("selected_entity_code"))
	g.optional("contrato", "Contrato", sess.GetContext("patient_contract"))
	g.optional("regimen", "Régimen", sess.GetContext("eps_regimen"))
}

// writeOrderGroup describe la lectura de la orden. `cups_leidos` va en la gramática del campo de
// CUPS del agente (`883141:1 930810:2`): se puede copiar tal cual en lo que devuelve.
func writeOrderGroup(doc *summaryDoc, sess *session.Session) {
	g := doc.untitled("orden")
	var cups []services.CUPSEntry
	if err := json.Unmarshal([]byte(sess.GetContext("ocr_cups_json")), &cups); err == nil {
		tokens := make([]string, 0, len(cups))
		for _, c := range cups {
			code := strings.TrimSpace(c.Code)
			if code == "" {
				continue
			}
			qty := c.Quantity
			if qty < 1 {
				qty = 1
			}
			tokens = append(tokens, code+":"+strconv.Itoa(qty))
		}
		g.optional("cups_leidos", "CUPS leídos", strings.Join(tokens, " "))
	}
	g.optional("sin_codigo", "Procedimientos sin código", oneLine(sess.GetContext(sm.CtxOCRUnreadNames)))
	g.optional("causa_ocr", "Causa del fallo de lectura", oneLine(sess.GetContext(sm.CtxOCRFailureCause)))
	g.optional("lista_espera", "Lista de espera", sess.GetContext(sm.CtxEscalationWaitingListID))
}

// writeProcedureGroupsGroup dice en qué grupo de procedimientos va el paciente y cuáles faltan.
func writeProcedureGroupsGroup(doc *summaryDoc, sess *session.Session) {
	total, _ := strconv.Atoi(sess.GetContext("total_procedures"))
	if total < 2 {
		return
	}
	idx, _ := strconv.Atoi(sess.GetContext("current_procedure_idx"))
	if idx < 0 {
		idx = 0
	}
	g := doc.untitled("grupo")
	g.optional("actual", "Grupo", fmt.Sprintf("%d de %d", idx+1, total))

	var groups []services.CUPSGroup
	if err := json.Unmarshal([]byte(sess.GetContext("procedures_json")), &groups); err != nil {
		return
	}
	var pending []string
	for i := idx + 1; i < len(groups); i++ {
		names := make([]string, 0, len(groups[i].Cups))
		for _, c := range groups[i].Cups {
			name := strings.TrimSpace(c.Name)
			if name == "" {
				name = strings.TrimSpace(c.Code)
			}
			if name != "" {
				names = append(names, name)
			}
		}
		pending = append(pending, oneLine(fmt.Sprintf("%s (%s)", groups[i].ServiceType, strings.Join(names, ", "))))
	}
	g.optional("pendientes", "Pendientes", strings.Join(pending, "; "))
}

// writeScheduleGroup muestra el horario que el paciente eligió o, si aún no eligió, los que se le
// ofrecieron.
func writeScheduleGroup(doc *summaryDoc, sess *session.Session) {
	g := doc.untitled("agenda")
	if slot := findSelectedSlot(sess); slot != nil {
		g.optional("horario_elegido", "Horario elegido", slotLabel(slot))
		return
	}
	var slots []services.AvailableSlot
	if err := json.Unmarshal([]byte(sess.GetContext("available_slots_json")), &slots); err != nil {
		return
	}
	offered := make([]string, 0, len(slots))
	for i := range slots {
		offered = append(offered, fmt.Sprintf("%d) %s", i+1, slotLabel(&slots[i])))
	}
	g.optional("horarios_ofrecidos", "Horarios ofrecidos", strings.Join(offered, "; "))
}

// slotLabel es «<fecha> <hora> con Dr. <médico>».
func slotLabel(s *services.AvailableSlot) string {
	hour := s.TimeDisplay
	if hour == "" {
		hour = s.TimeSlot
	}
	return oneLine(fmt.Sprintf("%s %s con Dr. %s", s.Date, hour, s.DoctorName))
}

// writeAppointmentGroup describe la cita a la que se refiere el caso (notificación, cancelación o
// consolidación).
func writeAppointmentGroup(doc *summaryDoc, sess *session.Session) {
	g := doc.untitled("cita")
	id := sess.GetContext("notif_appointment_id")
	if id == "" {
		id = sess.GetContext("selected_appointment_id")
	}
	if id == "" {
		id = sess.GetContext("consolidate_appt_id")
	}
	g.optional("id", "Cita", id)
	g.optional("fecha", "Fecha", sess.GetContext("notif_appt_date"))
	g.optional("hora", "Hora", sess.GetContext("notif_appt_time"))
	g.optional("procedimiento", "Procedimiento", oneLine(sess.GetContext("notif_cups_name")))
	g.optional("tipo_notificacion", "Notificación", sess.GetContext("notif_type"))
}

// writeSearchGroup muestra lo que devolvió la búsqueda manual de procedimientos.
func writeSearchGroup(doc *summaryDoc, sess *session.Session) {
	var procs []struct {
		Code string
		Name string
	}
	if err := json.Unmarshal([]byte(sess.GetContext("search_procedures_json")), &procs); err != nil {
		return
	}
	found := make([]string, 0, len(procs))
	for _, p := range procs {
		found = append(found, oneLine(p.Code+" "+p.Name))
	}
	doc.untitled("busqueda").optional("resultados", "Resultados de la búsqueda", strings.Join(found, "; "))
}
