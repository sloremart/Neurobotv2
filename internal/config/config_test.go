package config

import (
	"strings"
	"testing"
)

// --- parseCSVList ---

func TestParsePhoneList_Empty(t *testing.T) {
	result := parseCSVList("")
	if result != nil {
		t.Errorf("expected nil for empty string, got %v", result)
	}
}

func TestParsePhoneList_Single(t *testing.T) {
	result := parseCSVList("+573001234567")
	if len(result) != 1 || result[0] != "+573001234567" {
		t.Errorf("expected [+573001234567], got %v", result)
	}
}

func TestParsePhoneList_MultipleWithSpaces(t *testing.T) {
	result := parseCSVList("+573001234567, +573009876543 , +573005555555")
	if len(result) != 3 {
		t.Fatalf("expected 3 phones, got %d: %v", len(result), result)
	}
	expected := []string{"+573001234567", "+573009876543", "+573005555555"}
	for i, e := range expected {
		if result[i] != e {
			t.Errorf("phone[%d]: expected %s, got %s", i, e, result[i])
		}
	}
}

// --- IsPhoneWhitelisted ---

func TestIsPhoneWhitelisted_EmptyList_AllAllowed(t *testing.T) {
	cfg := &Config{TestingWhitelistPhones: nil}
	if !cfg.IsPhoneWhitelisted("+573001234567") {
		t.Error("expected true when whitelist is empty")
	}
}

func TestIsPhoneWhitelisted_InList(t *testing.T) {
	cfg := &Config{TestingWhitelistPhones: []string{"+573001234567", "+573009876543"}}
	if !cfg.IsPhoneWhitelisted("+573009876543") {
		t.Error("expected true for phone in whitelist")
	}
}

func TestIsPhoneWhitelisted_NotInList(t *testing.T) {
	cfg := &Config{TestingWhitelistPhones: []string{"+573001234567"}}
	if cfg.IsPhoneWhitelisted("+573005555555") {
		t.Error("expected false for phone not in whitelist")
	}
}

// --- ResolveTeamForCups ---

func TestResolveTeamForCups_Routing(t *testing.T) {
	cfg := &Config{
		TeamRoutingEnabled: true,
		BirdTeamGrupoA:     "team-a",
		BirdTeamGrupoB:     "team-b",
		BirdTeamFallback:   "team-fallback",
	}

	tests := []struct {
		cups     string
		expected string
		label    string
	}{
		{"881100", "team-a", "Ecografia"},
		{"883100", "team-a", "Resonancia"},
		{"871100", "team-a", "TAC"},
		{"870100", "team-a", "RX"},
		{"291100", "team-b", "EMG"},
		{"890274", "team-b", "Neurologia"},
		{"999999", "team-fallback", "Unknown"},
		{"ab", "team-fallback", "Too short"},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got := cfg.ResolveTeamForCups(tt.cups)
			if got != tt.expected {
				t.Errorf("ResolveTeamForCups(%q) = %s, want %s", tt.cups, got, tt.expected)
			}
		})
	}
}

func TestResolveTeamForCups_RoutingDisabled(t *testing.T) {
	cfg := &Config{
		TeamRoutingEnabled: false,
		BirdTeamGrupoA:     "team-a",
		BirdTeamGrupoB:     "team-b",
		BirdTeamFallback:   "team-fallback",
	}
	// Even a Grupo A code should return fallback when routing is disabled
	got := cfg.ResolveTeamForCups("883100")
	if got != "team-fallback" {
		t.Errorf("expected fallback when routing disabled, got %s", got)
	}
}

// --- ResolveOutboundWebhookSecret ---

func TestResolveOutboundWebhookSecret_Custom(t *testing.T) {
	cfg := &Config{
		BirdWebhookSecret:         "main-secret",
		BirdWebhookSecretOutbound: "outbound-secret",
	}
	got := cfg.ResolveOutboundWebhookSecret()
	if got != "outbound-secret" {
		t.Errorf("expected outbound-secret, got %s", got)
	}
}

func TestResolveOutboundWebhookSecret_Fallback(t *testing.T) {
	cfg := &Config{
		BirdWebhookSecret:         "main-secret",
		BirdWebhookSecretOutbound: "",
	}
	got := cfg.ResolveOutboundWebhookSecret()
	if got != "main-secret" {
		t.Errorf("expected main-secret (fallback), got %s", got)
	}
}

// --- NEW_LINE_ANNOUNCEMENT ---

// El anuncio de la línea nueva se toma tal cual, sin los espacios de alrededor (vacía = apagado).
func TestNewLineAnnouncement_Trimmed(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("NEW_LINE_ANNOUNCEMENT", "  Escríbenos a la línea nueva.\n ")
	if got := Load().NewLineAnnouncement; got != "Escríbenos a la línea nueva." {
		t.Errorf("NewLineAnnouncement = %q", got)
	}
	t.Setenv("NEW_LINE_ANNOUNCEMENT", "   ")
	if got := Load().NewLineAnnouncement; got != "" {
		t.Errorf("solo espacios debe quedar vacía, got %q", got)
	}
}

// --- Línea vieja de Néctar (NECTAR_LEGACY_*) ---

// Sin secretos propios, la línea vieja usa los de la principal; con ellos, los suyos.
func TestNectarLegacy_DefaultsSecretsToMain(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("NECTAR_REQUEST_SECRET", "req-main")
	t.Setenv("NECTAR_NOTICE_SECRET", "notice-main")
	cfg := Load()
	if cfg.NectarLegacyRequestSecret != "req-main" || cfg.NectarLegacyNoticeSecret != "notice-main" {
		t.Fatalf("defaults = %q / %q", cfg.NectarLegacyRequestSecret, cfg.NectarLegacyNoticeSecret)
	}
	t.Setenv("NECTAR_LEGACY_REQUEST_SECRET", "req-legacy")
	t.Setenv("NECTAR_LEGACY_NOTICE_SECRET", "notice-legacy")
	cfg = Load()
	if cfg.NectarLegacyRequestSecret != "req-legacy" || cfg.NectarLegacyNoticeSecret != "notice-legacy" {
		t.Fatalf("propios = %q / %q", cfg.NectarLegacyRequestSecret, cfg.NectarLegacyNoticeSecret)
	}
}

func TestNectarLegacy_KeyWithoutChannelIsError(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("NECTAR_LEGACY_SERVICE_KEY", "k")
	ok, err := Load().NectarLegacyConfigured()
	if ok || err == nil || !strings.Contains(err.Error(), "NECTAR_LEGACY_CHANNEL_ID") {
		t.Fatalf("= (%v, %v), want error que nombre NECTAR_LEGACY_CHANNEL_ID", ok, err)
	}
}

func TestNectarLegacy_ChannelWithoutKeyIsError(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("NECTAR_LEGACY_CHANNEL_ID", "7")
	ok, err := Load().NectarLegacyConfigured()
	if ok || err == nil || !strings.Contains(err.Error(), "NECTAR_LEGACY_SERVICE_KEY") {
		t.Fatalf("= (%v, %v), want error que nombre NECTAR_LEGACY_SERVICE_KEY", ok, err)
	}
}

func TestNectarLegacy_EmptyIsOffNoError(t *testing.T) {
	setRequiredEnv(t)
	if ok, err := Load().NectarLegacyConfigured(); ok || err != nil {
		t.Fatalf("vacía = (%v, %v), want (false, nil)", ok, err)
	}
	t.Setenv("NECTAR_LEGACY_SERVICE_KEY", "k")
	t.Setenv("NECTAR_LEGACY_CHANNEL_ID", "7")
	if ok, err := Load().NectarLegacyConfigured(); !ok || err != nil {
		t.Fatalf("completa = (%v, %v), want (true, nil)", ok, err)
	}
}

// --- Aviso urgente de volver a Bird (URGENT_*) ---

// Sin variables, los umbrales del aviso urgente son los del diseño de la transición.
func TestLoad_UrgentAlertDefaults(t *testing.T) {
	setRequiredEnv(t)
	cfg := Load()
	got := []int{
		cfg.UrgentAlertCooldownMin, cfg.UrgentUnsentThreshold, cfg.UrgentNectarDownCount,
		cfg.UrgentNectarDownWindowMin, cfg.UrgentEscalationFailCount, cfg.UrgentEscalationFailWindowMin,
	}
	want := []int{10, 10, 5, 5, 3, 10}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("defaults = %v, want %v", got, want)
		}
	}
	t.Setenv("URGENT_UNSENT_THRESHOLD", " 25 ")
	if got := Load().UrgentUnsentThreshold; got != 25 {
		t.Fatalf("URGENT_UNSENT_THRESHOLD = %d, want 25", got)
	}
}

// Los umbrales tienen que ser enteros positivos: un 0 o un texto se rechaza, no cae al default.
func TestParsePositiveInt(t *testing.T) {
	if got, err := parsePositiveInt("", 7); err != nil || got != 7 {
		t.Errorf("vacío = (%d, %v), want (7, nil)", got, err)
	}
	if got, err := parsePositiveInt(" 12 ", 7); err != nil || got != 12 {
		t.Errorf("«12» = (%d, %v), want (12, nil)", got, err)
	}
	for _, bad := range []string{"0", "-3", "diez", "5m", "1.5"} {
		if _, err := parsePositiveInt(bad, 7); err == nil {
			t.Errorf("%q tiene que ser rechazado", bad)
		}
	}
}
