package utils

import (
	"fmt"
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

// MaskDocument masks a patient document number for logs/events (Ley 1581 PII):
// "1000000689" → "10***89". Runa-safe. Siempre enmascara: LOG_MASK_PHONES solo destapa teléfonos.
func MaskDocument(doc string) string {
	r := []rune(doc)
	if len(r) <= 4 {
		return "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-2:])
}

// sensitiveDocKeys / sensitivePhoneKeys son las claves de log que portan PII de salud (Ley 1581).
var (
	sensitiveDocKeys = map[string]bool{
		"doc": true, "cedula": true, "cédula": true, "document": true, "documento": true,
		"num_id": true, "numid": true, "patient_doc": true,
		"name": true, "nombre": true, "nombres": true, "patient_name": true, "paciente": true,
	}
	sensitivePhoneKeys = map[string]bool{
		"phone": true, "telefono": true, "teléfono": true, "celular": true,
	}
)

// MaskText reemplaza el texto libre del paciente (puede traer cédula, nombres, dirección o
// síntomas) por su largo en runas. Vacío queda vacío. No depende de LOG_MASK_PHONES.
func MaskText(text string) string {
	if text == "" {
		return text
	}
	return fmt.Sprintf("[redactado: %d caracteres]", utf8.RuneCountInString(text))
}

// RedactLogAttr devuelve el valor enmascarado si la clave es PII (documento/nombre → MaskDocument,
// teléfono → MaskPhone, texto del mensaje → MaskText); si no, lo devuelve igual. Centraliza la
// política de redacción para todos los sinks de logs (stdout/archivo vía ReplaceAttr global, y el
// handler de Telegram).
func RedactLogAttr(key, value string) string {
	k := strings.ToLower(key)
	switch {
	case sensitiveDocKeys[k]:
		return MaskDocument(value)
	case sensitivePhoneKeys[k]:
		return MaskPhone(value)
	case k == "text":
		return MaskText(value)
	}
	return value
}
