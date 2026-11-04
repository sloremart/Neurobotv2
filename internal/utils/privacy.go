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

// sensitiveDocKeys / sensitivePhoneKeys / sensitiveTextKeys son las claves de log que portan PII
// de salud (Ley 1581). "to", "from" y "payload" no están: también nombran estados y botones.
var (
	sensitiveDocKeys = map[string]bool{
		"doc": true, "cedula": true, "cédula": true, "document": true, "documento": true,
		"num_id": true, "numid": true, "numero_documento": true, "patient_doc": true,
		"name": true, "nombre": true, "nombres": true, "patient_name": true, "paciente": true,
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
