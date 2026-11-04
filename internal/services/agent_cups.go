package services

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxCupsQuantity es la cantidad máxima de un CUPS que el bot agenda solo. La cantidad la extrae
// el LLM de la orden (el «(#N)» tal cual) y cada unidad suma espacios en la agenda: sin tope, una
// orden manipulada o mal leída bloquearía media jornada de un técnico. Ninguna orden real se acerca
// a 100; por encima la revisa un agente. EMG/NC conservan además su propio tope (applyFisiatriaRules).
const MaxCupsQuantity = 100

// AgentCupsNoCodesProblem es el motivo cuando lo que el asesor escribió no trae ningún código.
// Lo comparten el comando `/bot cups` y el campo de CUPS del menú de retornos de Néctar: es el
// mismo veredicto sobre la misma gramática.
const AgentCupsNoCodesProblem = "No se encontraron códigos CUPS válidos en lo escrito (formato: 883141 o 883141:2, separados por espacios)."

// AgentCupsQuantityProblem es el motivo cuando un CUPS que escribió el asesor pasa de
// MaxCupsQuantity ("" si ninguno). Lo comparten el comando `/bot cups` y el campo de CUPS del menú
// de retornos de Néctar: rechazado ahí, el asesor lo corrige en vez de que VALIDATE_OCR se lo
// vuelva a escalar.
func AgentCupsQuantityProblem(cups []CUPSEntry) string {
	for _, c := range cups {
		if c.Quantity > MaxCupsQuantity {
			return fmt.Sprintf("La cantidad máxima por CUPS es %d (%s tiene %d).", MaxCupsQuantity, c.Code, c.Quantity)
		}
	}
	return ""
}

// ParseAgentCups lee la gramática de los CUPS que escribe un asesor: tokens separados por
// espacios, cada uno `codigo` o `codigo:cantidad`. Una cantidad ausente, no numérica o menor que
// uno vale 1. El nombre queda vacío a propósito: VALIDATE_OCR lo completa desde el catálogo de
// procedimientos. Es función pura y vive aquí para que el worker (comando del asesor) y los
// pasos de la máquina (validación del campo) lean EXACTAMENTE lo mismo.
func ParseAgentCups(text string) []CUPSEntry {
	var cups []CUPSEntry
	for _, tok := range strings.Fields(text) {
		parts := strings.SplitN(tok, ":", 2)
		code := strings.TrimSpace(parts[0])
		if code == "" {
			continue
		}
		qty := 1
		if len(parts) == 2 {
			if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && n > 0 {
				qty = n
			}
		}
		cups = append(cups, CUPSEntry{Code: code, Quantity: qty})
	}
	return cups
}
