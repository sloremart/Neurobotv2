package utils

import "regexp"

// e164Re: formato E.164 estricto ("+" y 8-15 dígitos, sin espacios ni separadores).
var e164Re = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// IsE164 reporta si el identificador es un teléfono E.164 válido. Los webhooks de Bird pueden
// traer identificadores que no son teléfonos (nombres de contacto del workspace); la Channels API
// los rechaza SIEMPRE con 422, así que enviarles es costo garantizado sin entrega.
func IsE164(s string) bool {
	return e164Re.MatchString(s)
}
