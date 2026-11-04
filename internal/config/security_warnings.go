package config

import "strings"

// Valores de ejemplo de .env.example para la BD local. Un despliegue que copió el ejemplo sin cambiarlos
// deja la BD con una clave conocida (C-05). TestExamplePasswords_MatchEnvExample los mantiene al día.
const (
	exampleDBPassword     = "botpass"
	exampleDBRootPassword = "secret"
)

// minInternalAPIKeyLen: por debajo de 32 caracteres la clave de /api/internal/* deja de ser inviable de
// adivinar, y esa API se publica por el túnel (C-06).
const minInternalAPIKeyLen = 32

// SecurityWarnings devuelve los avisos de configuración insegura que no impiden arrancar. Son avisos y
// no fatales a propósito: tumbar el bot por esto cortaría producción. Ningún mensaje lleva el valor de
// un secreto, solo el nombre de la variable.
func (c *Config) SecurityWarnings() []string {
	var warnings []string

	if c.DBPassword == exampleDBPassword {
		warnings = append(warnings, "DB_PASSWORD es la de .env.example: cámbiala (BD local con PII)")
	}
	// Vacía, el compose cae a su valor por defecto, que es el mismo del ejemplo.
	if c.DBRootPassword == "" || c.DBRootPassword == exampleDBRootPassword {
		warnings = append(warnings, "DB_ROOT_PASSWORD vacía o la de .env.example: cámbiala (root de la BD local)")
	}

	if len(c.InternalAPIKey) < minInternalAPIKeyLen {
		warnings = append(warnings, "INTERNAL_API_KEY tiene menos de 32 caracteres: genera una con `openssl rand -hex 32`")
	}

	if !sqlServerEncryptsChannel(c.ExtDBEncrypt) {
		warnings = append(warnings, "EXTERNAL_DB_ENCRYPT no cifra el canal a SIESA: los datos clínicos viajan en claro (ver .env.example)")
	}

	return warnings
}

// sqlServerEncryptsChannel dice si el valor de "encrypt" cifra todo el canal TDS, con los mismos
// sinónimos que acepta go-mssqldb (msdsn.parseTLS). "false" cifra solo el login: los datos van en claro.
func sqlServerEncryptsChannel(encrypt string) bool {
	switch strings.ToLower(encrypt) {
	case "true", "strict", "mandatory", "yes", "1", "t":
		return true
	}
	return false
}
