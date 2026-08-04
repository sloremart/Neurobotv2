package config

import (
	"os"
	"testing"
)

// setRequiredEnv puebla las variables obligatorias de Load() (que hace log.Fatal si faltan).
func setRequiredEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENAI_API_KEY", "EXTERNAL_DB_PASSWORD", "BIRD_API_URL", "BIRD_WEBHOOK_SECRET",
		"BIRD_WORKSPACE_ID", "INTERNAL_API_KEY", "DB_PASSWORD", "EXTERNAL_DB_USER",
		"BIRD_API_KEY_WA", "BIRD_CHANNEL_ID", "BIRD_TEAM_FALLBACK",
	} {
		t.Setenv(k, "test-value")
	}
}

// Auditoría queries P5: los KPIs del dashboard comparten el pool SIESA (10) con el flujo de
// pacientes; el pool dedicado de KPIs es chico por defecto y configurable.
func TestExternalDBKPIMaxOpenDefaultAndOverride(t *testing.T) {
	setRequiredEnv(t)
	_ = os.Unsetenv("EXTERNAL_DB_KPI_MAX_OPEN")
	cfg := Load()
	if cfg.ExternalDBKPIMaxOpen != 3 {
		t.Errorf("default ExternalDBKPIMaxOpen = %d, want 3", cfg.ExternalDBKPIMaxOpen)
	}
	t.Setenv("EXTERNAL_DB_KPI_MAX_OPEN", "5")
	cfg = Load()
	if cfg.ExternalDBKPIMaxOpen != 5 {
		t.Errorf("override ExternalDBKPIMaxOpen = %d, want 5", cfg.ExternalDBKPIMaxOpen)
	}
}
