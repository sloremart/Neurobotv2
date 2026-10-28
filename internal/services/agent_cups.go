package services

import (
	"strconv"
	"strings"
)

// AgentCupsNoCodesProblem es el motivo cuando lo que el asesor escribió no trae ningún código.
// Lo comparten el comando `/bot cups` y el campo de CUPS del menú de retornos de Néctar: es el
// mismo veredicto sobre la misma gramática.
const AgentCupsNoCodesProblem = "No se encontraron códigos CUPS válidos en lo escrito (formato: 883141 o 883141:2, separados por espacios)."

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
