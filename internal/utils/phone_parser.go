package utils

import (
	"regexp"
	"strings"
)

var (
	// mobileRe valida un móvil colombiano: 3 + 9 dígitos.
	mobileRe = regexp.MustCompile(`^3\d{9}$`)
	digitRe  = regexp.MustCompile(`\d`)
	// splitRe separa cadenas con varios números. Incluye '-' y '.' porque también se usan como
	// separador entre números (ej. "3107558761-3125920492"); los números individuales que llevan
	// '-'/'.' internos (ej. "300-123-4567") ya los resuelve el paso 1 sobre la cadena completa.
	splitRe = regexp.MustCompile(`[\s,.\-/;|]+`)
)

// ParseColombianPhone parsea y normaliza un número de teléfono colombiano a "+57XXXXXXXXXX".
// Retorna "" si no hay un móvil colombiano válido.
//
// Validación de longitud ESTRICTA: un número malformado (p.ej. 11 dígitos por un tecleo de más)
// devuelve "" en vez de "rescatarse" como un número equivocado — esto evita enviar la plantilla
// de WhatsApp o la llamada IVR (PHI) a una persona distinta.
func ParseColombianPhone(phoneString string) string {
	if phoneString == "" {
		return ""
	}

	lower := strings.ToLower(strings.TrimSpace(phoneString))
	if lower == "null" || lower == "no tiene" || lower == "n/a" || lower == "-" {
		return ""
	}

	// 1. La cadena completa como UN solo número (maneja "+57 300 123 4567", "300-123-4567",
	//    "300.123.4567", "573001234567").
	if p := normalizeStrict(onlyDigits(phoneString)); p != "" {
		return p
	}

	// 2. Varios números ("3107558761 / 3125920492"): partir por separadores y tomar el primero
	//    que sea un móvil válido por longitud exacta.
	for _, tok := range splitRe.Split(phoneString, -1) {
		if p := normalizeStrict(onlyDigits(tok)); p != "" {
			return p
		}
	}

	return ""
}

// onlyDigits devuelve solo los dígitos de s.
func onlyDigits(s string) string {
	return strings.Join(digitRe.FindAllString(s, -1), "")
}

// normalizeStrict acepta SOLO longitudes exactas de móvil colombiano: 10 dígitos (3XXXXXXXXX) o
// 12 dígitos con prefijo "57". Cualquier otra longitud → "" (no se adivina).
func normalizeStrict(digits string) string {
	switch {
	case len(digits) == 10 && mobileRe.MatchString(digits):
		return "+57" + digits
	case len(digits) == 12 && strings.HasPrefix(digits, "57") && mobileRe.MatchString(digits[2:]):
		return "+57" + digits[2:]
	}
	return ""
}

// FormatPhoneDisplay formatea un teléfono para mostrar al usuario.
// "+573103343616" → "+57 310 334 3616", vacío → "(no registrado)"
func FormatPhoneDisplay(phone string) string {
	parsed := ParseColombianPhone(phone)
	if parsed == "" {
		if phone == "" {
			return "(no registrado)"
		}
		return phone
	}
	d := parsed[3:] // quitar "+57"
	return "+57 " + d[:3] + " " + d[3:6] + " " + d[6:]
}
