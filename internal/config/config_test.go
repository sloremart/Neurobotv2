package config

import (
	"testing"
)

// --- parsePhoneList ---

func TestParsePhoneList_Empty(t *testing.T) {
	result := parsePhoneList("")
	if result != nil {
		t.Errorf("expected nil for empty string, got %v", result)
	}
}

func TestParsePhoneList_Single(t *testing.T) {
	result := parsePhoneList("+573001234567")
	if len(result) != 1 || result[0] != "+573001234567" {
		t.Errorf("expected [+573001234567], got %v", result)
	}
}

func TestParsePhoneList_MultipleWithSpaces(t *testing.T) {
	result := parsePhoneList("+573001234567, +573009876543 , +573005555555")
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
