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
	if cfg != nil && cfg.EmitsProtocolV1() {
		esc.Protocol = config.ProtocolEscaladoV1
		esc.ReasonDetail = detail
		esc.Input = sm.StaticInputDescriptor(view, sm.StateNotifPending)
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
