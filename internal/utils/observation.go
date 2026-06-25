package utils

import "strings"

// accentReplacer normaliza vocales acentuadas y ñ para comparar texto libre de forma robusta.
var accentReplacer = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
	"Á", "a", "É", "e", "Í", "i", "Ó", "o", "Ú", "u", "Ü", "u", "Ñ", "n",
)

// normalizeObs pasa el texto a minúsculas y le quita acentos para matching insensible a caso/tilde.
func normalizeObs(s string) string {
	return accentReplacer.Replace(strings.ToLower(s))
}

// ObservationHasContrast detecta si la observación (texto libre de SIESA) indica un estudio
// CONTRASTADO. Maneja variantes de escritura (Contrastada/CONTRASTADO/contraste, con/sin acento)
// y negaciones explícitas (sin/no contraste, sin medio de contraste).
//
// Sesgo a DETECTAR contraste: un falso negativo (no detectar un contraste real) agenda el estudio
// en una ventana incorrecta → riesgo clínico; un falso positivo solo restringe disponibilidad. Por
// eso solo se descarta ante negaciones inequívocas.
func ObservationHasContrast(obs string) bool {
	n := normalizeObs(obs)
	if !strings.Contains(n, "contrast") {
		return false
	}
	if strings.Contains(n, "sin contrast") ||
		strings.Contains(n, "no contrast") ||
		strings.Contains(n, "sin medio de contrast") {
		return false
	}
	return true
}

// ObservationHasSedation detecta si la observación indica sedación/anestesia, con las mismas
// reglas de normalización y negación que ObservationHasContrast.
func ObservationHasSedation(obs string) bool {
	n := normalizeObs(obs)
	if strings.Contains(n, "sin sedaci") || strings.Contains(n, "no sedaci") {
		return false
	}
	return strings.Contains(n, "sedaci") || // sedación, sedacion
		strings.Contains(n, "sedad") || // sedado, sedada
		strings.Contains(n, "anestesi") // anestesia, anestesiado
}
