package statemachine

import (
	"maps"
	"regexp"
)

// Los motivos del candado del botón «Resolver» de Néctar. Son PROVISIONALES: viven juntos en
// este archivo para poder renombrarlos cuando Néctar fije su vocabulario. Hoy Néctar guarda el
// motivo como cadena opaca (≤64 runas) y todavía no actúa sobre él; el bot solo lo declara.
const (
	// ResolveGuardPendingReturn: el flujo sigue cuando el agente devuelve el dato que falta.
	ResolveGuardPendingReturn = "retorno_pendiente"
	// ResolveGuardPendingAppointment: queda una cita, o una acción de cita, por hacer.
	ResolveGuardPendingAppointment = "cita_pendiente"
)

// resolveGuardShape es la forma que Néctar acepta para el motivo (la misma de reason_detail).
var resolveGuardShape = regexp.MustCompile(`^[a-z_]{1,64}$`)

// DeclaredResolveGuards devuelve una COPIA de los candados declarados por paso. Sirve para que
// el registro de capacidades solo anuncie el candado si algún paso lo declara.
func DeclaredResolveGuards() map[string]string {
	returnMenusMu.RLock()
	defer returnMenusMu.RUnlock()
	out := make(map[string]string, len(resolveGuards))
	maps.Copy(out, resolveGuards)
	return out
}
