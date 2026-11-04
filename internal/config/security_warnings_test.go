package config

import (
	"os"
	"strings"
	"testing"
)

// safeConfig devuelve una configuración que no dispara ningún aviso de seguridad.
func safeConfig() *Config {
	return &Config{
		DBPassword:     "a-strong-local-db-password",
		DBRootPassword: "a-strong-local-root-password",
		InternalAPIKey: strings.Repeat("k", minInternalAPIKeyLen),
		ExtDBEncrypt:   "true",
	}
}

func hasWarning(warnings []string, envVar string) bool {
	for _, w := range warnings {
		if strings.Contains(w, envVar) {
			return true
		}
	}
	return false
}

func TestSecurityWarnings_SafeConfig_None(t *testing.T) {
	if w := safeConfig().SecurityWarnings(); len(w) != 0 {
		t.Fatalf("expected no warnings, got %v", w)
	}
}

// C-05: la contraseña de la BD local (bot y root) igual a la de .env.example, o root vacía (el compose
// cae entonces a su valor por defecto débil), deben avisar.
func TestSecurityWarnings_LocalDBPasswords(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		envVar string
	}{
		{"db password is example", func(c *Config) { c.DBPassword = exampleDBPassword }, "DB_PASSWORD"},
		{"root password is example", func(c *Config) { c.DBRootPassword = exampleDBRootPassword }, "DB_ROOT_PASSWORD"},
		{"root password empty", func(c *Config) { c.DBRootPassword = "" }, "DB_ROOT_PASSWORD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := safeConfig()
			tc.mutate(cfg)
			w := cfg.SecurityWarnings()
			if !hasWarning(w, tc.envVar) {
				t.Fatalf("expected a warning naming %s, got %v", tc.envVar, w)
			}
		})
	}
}

// C-06: clave interna de menos de 32 caracteres avisa; 32 exactos no.
func TestSecurityWarnings_ShortInternalAPIKey(t *testing.T) {
	cfg := safeConfig()
	cfg.InternalAPIKey = strings.Repeat("k", minInternalAPIKeyLen-1)
	if !hasWarning(cfg.SecurityWarnings(), "INTERNAL_API_KEY") {
		t.Fatal("expected a warning for a short INTERNAL_API_KEY")
	}
}

// C-10: solo los valores con los que el driver cifra todo el canal callan el aviso. "false" cifra solo
// el login: los datos siguen en claro.
func TestSecurityWarnings_SIESAEncryption(t *testing.T) {
	for _, v := range []string{"", "disable", "false", "no", "optional", "0"} {
		cfg := safeConfig()
		cfg.ExtDBEncrypt = v
		if !hasWarning(cfg.SecurityWarnings(), "EXTERNAL_DB_ENCRYPT") {
			t.Errorf("encrypt=%q: expected a warning", v)
		}
	}
	for _, v := range []string{"true", "TRUE", "strict", "mandatory", "yes", "1", "t"} {
		cfg := safeConfig()
		cfg.ExtDBEncrypt = v
		if hasWarning(cfg.SecurityWarnings(), "EXTERNAL_DB_ENCRYPT") {
			t.Errorf("encrypt=%q: unexpected warning", v)
		}
	}
}

// Los avisos nunca llevan el valor de un secreto.
func TestSecurityWarnings_NeverPrintSecrets(t *testing.T) {
	cfg := &Config{
		DBPassword:     exampleDBPassword,
		DBRootPassword: exampleDBRootPassword,
		InternalAPIKey: "short-secret-value",
		ExtDBEncrypt:   "disable",
	}
	for _, w := range cfg.SecurityWarnings() {
		for _, secret := range []string{exampleDBPassword, exampleDBRootPassword, cfg.InternalAPIKey} {
			if strings.Contains(w, secret) {
				t.Fatalf("warning leaks a secret value: %q", w)
			}
		}
	}
}

// Las constantes del ejemplo deben seguir a .env.example: si alguien cambia el ejemplo y no el código,
// el aviso de C-05 dejaría de detectar el valor copiado.
func TestExamplePasswords_MatchEnvExample(t *testing.T) {
	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}

	want := map[string]string{
		"DB_PASSWORD":      exampleDBPassword,
		"DB_ROOT_PASSWORD": exampleDBRootPassword,
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if _, tracked := want[key]; ok && tracked {
			got[key] = value
		}
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf(".env.example %s no coincide con la constante del código", key)
		}
	}
}
