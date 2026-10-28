package worker

import (
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// AgentCommand represents a parsed /bot command from a human agent.
type AgentCommand struct {
	// ReturnValue es el token del menú de retornos que el agente eligió en Néctar
	// (conversacion_devuelta.return_value). Vacío = devolución sin decisión.
	ReturnValue string

	// ReturnV1 es el retorno del protocolo v1 (`{opcion, valores?}`), cuando el aviso lo trajo.
	// nil = el dialecto, que es lo que llega hasta que Néctar despliegue v1 — y lo que seguirá
	// llegando después, porque la aceptación dual no se apaga.
	//
	// Va JUNTO a ReturnValue y no en su lugar: un aviso puede traer las dos (manda ésta).
	ReturnV1 *nectar.V1Return

	// Reason es el motivo con que Néctar resolvió la conversación (conversacion_resuelta.motivo,
	// p. ej. "cola_vencida"; CloseReasonResolved si llegó sin motivo). Solo viaja con Action "close";
	// vacío = el cierre lo decidió el agente desde el menú de retornos (o un comando de Bird).
	Reason string

	Action string // "resume", "close", "info", "reset", "orden", "cups"
	State  string // Target state (optional, for resume)
	Data   string // Corrected data (optional, for resume with data / orden description)
	Phone  string // Patient phone number
}

// ChosenOption es el TOKEN que el agente eligió, venga por donde venga: la `opcion` del retorno
// v1 si lo hay, y si no el `return_value` del dialecto. Existe para los sitios que preguntan por
// el token y nada más (hoy, el reconocimiento de la cola vencida en handleAgentResume), que si no
// tendrían que repetir la precedencia y acabarían discrepando.
//
// OJO: en el dialecto el mismo campo transporta también el DATO escrito (`bot_dato:`), así que
// esto no es «el token y solo el token».
func (c AgentCommand) ChosenOption() string {
	if c.ReturnV1 != nil && c.ReturnV1.Option != "" {
		return c.ReturnV1.Option
	}
	return c.ReturnValue
}

// ParseAgentCommand parses agent text into a structured command.
//
//	/bot                               → reset (restart from GREETING)
//	/bot resume                        → resume at pre_escalation_state
//	/bot resume ASK_DOCUMENT           → resume at ASK_DOCUMENT
//	/bot resume ASK_DOCUMENT 1234      → resume at ASK_DOCUMENT with data "1234"
//	/bot orden Resonancia ...          → extract CUPS from text description via AI
//	/bot cups 883141 930810            → inject CUPS codes directly (qty 1 each)
//	/bot cups 883141:2 930810:1        → inject CUPS codes with quantities
//	/bot cerrar                        → close session
//	/bot info                          → show session summary
func ParseAgentCommand(text string) AgentCommand {
	parts := strings.Fields(text)
	if len(parts) < 2 {
		// Just "/bot" → reset
		return AgentCommand{Action: "reset"}
	}

	switch strings.ToLower(parts[1]) {
	case "resume":
		cmd := AgentCommand{Action: "resume"}
		if len(parts) >= 3 {
			cmd.State = strings.ToUpper(parts[2])
		}
		if len(parts) >= 4 {
			// Everything after the state is the data
			cmd.Data = strings.Join(parts[3:], " ")
		}
		return cmd
	case "orden":
		cmd := AgentCommand{Action: "orden"}
		if len(parts) >= 3 {
			cmd.Data = strings.Join(parts[2:], " ")
		}
		return cmd
	case "cups":
		cmd := AgentCommand{Action: "cups"}
		if len(parts) >= 3 {
			cmd.Data = strings.Join(parts[2:], " ")
		}
		return cmd
	case "cerrar":
		return AgentCommand{Action: "close"}
	case "info":
		return AgentCommand{Action: "info"}
	default:
		return AgentCommand{Action: "reset"}
	}
}

// CloseReasonResolved es el motivo CENTINELA con que el manejador de avisos despacha un
// conversacion_resuelta que llegó SIN motivo. Toda resolución de Néctar —con motivo o sin él—
// viaja así con Reason no vacío, y el worker sabe que la conversación ya no es del bot: no se
// despide ni cierra (los dos envíos morirían en 404). Reason vacío queda solo para el «cerrar»
// del menú de retornos, donde la conversación volvió al bot y la despedida sí le toca a él.
const CloseReasonResolved = "resuelta"
