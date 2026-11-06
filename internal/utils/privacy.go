package utils

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maskPhones controls whether phone numbers are masked in logs.
// Default: true (production). Set to false via LOG_MASK_PHONES=false for debugging.
// Solo afecta a los teléfonos: documento, nombre y texto libre se redactan siempre (C-22).
var maskPhones = true

// SetMaskPhones configures whether MaskPhone masks or passes through.
func SetMaskPhones(mask bool) {
	maskPhones = mask
}

// MaskPhone masks a phone number for logging: "+573103343616" → "+573***3616".
// When masking is disabled (LOG_MASK_PHONES=false), returns the phone unchanged.
func MaskPhone(phone string) string {
	if !maskPhones {
		return phone
	}
	if len(phone) < 7 {
		return "***"
	}
	return phone[:4] + "***" + phone[len(phone)-4:]
}

// MaskPhoneAlways enmascara un teléfono para lo que se persiste (chat_events, C-04), sin depender de
// LOG_MASK_PHONES: deja ver como mucho los últimos 4 caracteres, sea móvil o fijo ("+573103343616"
// → "***3616"), 2 si es corto (5 a 9) y ninguno con 4 o menos. Idempotente: lo que ya empieza por
// "***" queda igual (la retención vuelve a pasar por filas ya enmascaradas). La misma regla está en
// SQL en local.piiMaskedPhoneSQL.
func MaskPhoneAlways(phone string) string {
	if strings.HasPrefix(phone, "***") {
		return phone
	}
	r := []rune(phone)
	switch {
	case len(r) <= 4:
		return "***"
	case len(r) < 10:
		return "***" + string(r[len(r)-2:])
	default:
		return "***" + string(r[len(r)-4:])
	}
}

// MaskEmail enmascara un correo para lo que se persiste: "juan.perez@gmail.com" → "ju***@gmail.com".
// Conserva el dominio (sirve para ver errores de tipeo) y a lo sumo dos runas del usuario; sin «@»
// no hay dominio que conservar y queda "***". Vacío queda vacío.
func MaskEmail(email string) string {
	if email == "" {
		return email
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return "***"
	}
	user := []rune(email[:at])
	if len(user) <= 3 {
		return "***" + email[at:]
	}
	return string(user[:2]) + "***" + email[at:]
}

// MaskDocument masks a patient document number for logs/events (Ley 1581 PII):
// "1000000689" → "10***89". Runa-safe. Siempre enmascara: LOG_MASK_PHONES solo destapa teléfonos.
func MaskDocument(doc string) string {
	r := []rune(doc)
	if len(r) <= 4 {
		return "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-2:])
}

// sensitiveDocKeys / sensitivePhoneKeys / sensitiveTextKeys son las claves de log que portan PII
// de salud (Ley 1581). "to", "from" y "payload" no están: también nombran estados y botones.
var (
	sensitiveDocKeys = map[string]bool{
		"doc": true, "cedula": true, "cédula": true, "document": true, "documento": true,
		"num_id": true, "numid": true, "numero_documento": true, "patient_doc": true,
		"name": true, "nombre": true, "nombres": true, "patient_name": true, "paciente": true, "patient": true,
	}
	sensitivePhoneKeys = map[string]bool{
		"phone": true, "telefono": true, "teléfono": true, "celular": true, "patient_phone": true,
	}
	sensitiveTextKeys = map[string]bool{
		"text": true, "input": true, "last_input": true, "email": true, "correo": true,
		"direccion": true, "dirección": true, "address": true,
		"observacion": true, "observaciones": true, "observations": true,
	}
)

// digitRunRe reconoce una secuencia de dígitos con espacios, puntos o guiones sueltos entre ellos
// («1.000.000.689», «300 123 4567», «601-456-7890»). La barra no cuenta: una fecha no es un número.
var digitRunRe = regexp.MustCompile(`\d(?:[ .\-]?\d)+`)

// minMaskedDigits es desde cuántos dígitos una secuencia puede ser un documento o un teléfono.
const minMaskedDigits = 5

// MaskDigitRuns tapa en un texto libre las secuencias de 5 o más dígitos (un documento o un teléfono
// escrito donde no se pedía), dejando ver sus 2 últimos dígitos: «mi cédula es 1.000.000.689» → «mi
// cédula es ***89». El resto del texto queda igual (C-04).
func MaskDigitRuns(text string) string {
	return digitRunRe.ReplaceAllStringFunc(text, func(run string) string {
		var digits []byte
		for i := 0; i < len(run); i++ {
			if run[i] >= '0' && run[i] <= '9' {
				digits = append(digits, run[i])
			}
		}
		if len(digits) < minMaskedDigits {
			return run
		}
		return "***" + string(digits[len(digits)-2:])
	})
}

// MaskText reemplaza el texto libre del paciente (puede traer cédula, nombres, dirección o
// síntomas) por su largo en runas. Vacío queda vacío. No depende de LOG_MASK_PHONES.
func MaskText(text string) string {
	if text == "" {
		return text
	}
	return fmt.Sprintf("[redactado: %d caracteres]", utf8.RuneCountInString(text))
}

// IsSensitiveLogKey dice si la clave de log porta PII según la política común. El handler de
// Telegram la consulta para redactar las mismas claves con su propio formato (y sin depender de
// LOG_MASK_PHONES: un chat externo nunca recibe el teléfono en claro).
func IsSensitiveLogKey(key string) bool {
	k := strings.ToLower(key)
	return sensitiveDocKeys[k] || sensitivePhoneKeys[k] || sensitiveTextKeys[k]
}

// RedactLogAttr devuelve el valor enmascarado si la clave es PII (documento/nombre → MaskDocument,
// teléfono → MaskPhone, texto libre → MaskText); si no, lo devuelve igual. La usa el ReplaceAttr
// global de stdout/archivo; Telegram usa IsSensitiveLogKey con su propio formato.
func RedactLogAttr(key, value string) string {
	k := strings.ToLower(key)
	switch {
	case sensitiveDocKeys[k]:
		return MaskDocument(value)
	case sensitivePhoneKeys[k]:
		return MaskPhone(value)
	case sensitiveTextKeys[k]:
		return MaskText(value)
	}
	return value
}

// RedactLogMap devuelve una copia del mapa con cada valor redactado según su clave (también en
// mapas anidados), para loguear datos de eventos: el ReplaceAttr global solo ve attrs string de
// primer nivel, no las claves de un mapa. El original no se toca (es el que se persiste).
func RedactLogMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case string:
			out[k] = RedactLogAttr(k, val)
		case map[string]interface{}:
			out[k] = RedactLogMap(val)
		default:
			if IsSensitiveLogKey(k) {
				out[k] = RedactLogAttr(k, fmt.Sprint(val))
			} else {
				out[k] = v
			}
		}
	}
	return out
}
