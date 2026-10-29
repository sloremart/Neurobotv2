package notifications

import (
	"encoding/json"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// notifEscalation arma el sobre del escalado de una notificación. En Bird solo lleva el resumen
// (que EscalateToAgentWithSummary manda como nota interna) y el motivo, que Bird ignora.
//
// Si la sesión es de Néctar viaja además lo que le pinta al agente las opciones de retorno de
// NOTIF_PENDING, con la forma que manda la bandera del protocolo (igual que
// handlers.escalationEnvelope, pero sin máquina: el paso no pide campo): con v1 el descriptor
// `input` y el detalle del motivo; con v0 el menú de retornos. `view` es la vista del contexto de
// la sesión escalada: las opciones de NOTIF_PENDING dependen del tipo de notificación y del motivo.
func notifEscalation(cfg *config.Config, phone string, view *session.Session, detail, summary string) bird.Escalation {
	esc := bird.Escalation{Reason: nectar.ReasonBotCannotAnswer, Summary: summary}
	if !session.IsNectarSession(phone) {
		return esc
	}
	// Candado del botón «Resolver»: queda una cita o una acción de cita por hacer. Solo viaja en
	// el sobre de Néctar; en Bird no existe.
	esc.ResolveLock, _ = sm.ResolveGuardFor(sm.StateNotifPending)
	// El equipo sugerido viaja en v0 y v1 (el campo existe desde antes de v1), igual que en los
	// handlers; sale del grupo del CUPS de la cita notificada.
	esc.SuggestedTeamID = cfg.SuggestedTeamForCups(view.GetContext(sm.CtxNotifCupsCode))
	if cfg != nil && cfg.EmitsProtocolV1() {
		esc.Protocol = config.ProtocolEscaladoV1
		esc.ReasonDetail = detail
		esc.Input = sm.StaticInputDescriptor(view, sm.StateNotifPending)
		// Los hechos acompañan al resumen, no lo sustituyen. Sin serializar se escala sin ellos.
		if raw, err := json.Marshal(notifFacts(view)); err != nil {
			slog.Error("notificación: los hechos no se pudieron serializar, se escala sin ellos", "error", err)
		} else {
			esc.Facts = raw
		}
		return esc
	}
	// Sin poder serializar se escala sin menú: degrada la vuelta, no abandona al paciente.
	if raw, err := json.Marshal(sm.BuildReturnMenu(view, sm.StateNotifPending)); err != nil {
		slog.Error("notificación: menú de retornos no serializable, se escala sin él", "error", err)
	} else {
		esc.ReturnMenu = raw
	}
	return esc
}

// notifFacts son los hechos del caso de una notificación: el paciente y la cita notificada, con
// las mismas claves y rótulos que el resumen de los handlers (nectar.Fact*). Los campos sin valor
// se omiten; el teléfono nunca viaja (el agente ya lo ve en la conversación).
func notifFacts(view *session.Session) []nectar.FactGroup {
	var groups []nectar.FactGroup
	add := func(key string, specs []nectar.FactSpec, values []string) {
		g := nectar.FactGroup{Key: key}
		for i, spec := range specs {
			if values[i] != "" {
				g.Fields = append(g.Fields, spec.Field(values[i]))
			}
		}
		if len(g.Fields) > 0 {
			groups = append(groups, g)
		}
	}
	add("paciente", []nectar.FactSpec{nectar.FactPatientName}, []string{view.GetContext("patient_name")})
	add("cita",
		[]nectar.FactSpec{
			nectar.FactAppointmentID, nectar.FactAppointmentDate, nectar.FactAppointmentTime,
			nectar.FactProcedure, nectar.FactNotifType,
		},
		[]string{
			view.GetContext("notif_appointment_id"), view.GetContext("notif_appt_date"),
			view.GetContext("notif_appt_time"), view.GetContext("notif_cups_name"),
			view.GetContext("notif_type"),
		})
	return groups
}
