package bird

import (
	"errors"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

func TestSendTemplate_WhatsAppDisabled(t *testing.T) {
	c := &Client{notifWADisabled: true}
	id, err := c.SendTemplate("+573001234567", TemplateConfig{ProjectID: "p"})
	if !errors.Is(err, ErrWhatsAppNotificationsDisabled) {
		t.Fatalf("expected ErrWhatsAppNotificationsDisabled, got %v", err)
	}
	if id != "" {
		t.Errorf("expected empty msgID when disabled, got %q", id)
	}
}

func TestPlaceCall_IVRDisabled(t *testing.T) {
	// voiceChannelID set on purpose: the disable gate must win over the "channel not configured" error.
	c := &Client{notifIVRDisabled: true, voiceChannelID: "ch"}
	id, err := c.PlaceCall("+573001234567", nil)
	if !errors.Is(err, ErrIVRNotificationsDisabled) {
		t.Fatalf("expected ErrIVRNotificationsDisabled, got %v", err)
	}
	if id != "" {
		t.Errorf("expected empty callID when disabled, got %q", id)
	}
}

// El zero-value del cliente está HABILITADO (no rompe NewClientForTest ni construcciones directas).
func TestPlaceCall_EnabledByDefault(t *testing.T) {
	c := &Client{} // notifIVRDisabled=false → habilitado; sin canal de voz da OTRO error, no el de deshabilitado
	if _, err := c.PlaceCall("+573001234567", nil); errors.Is(err, ErrIVRNotificationsDisabled) {
		t.Error("a zero-value client must be ENABLED, not disabled")
	}
}

// NewClient cablea los flags desde la config (en negativo internamente).
func TestNewClient_NotificationFlagsWired(t *testing.T) {
	c := NewClient(&config.Config{WhatsAppNotificationsEnabled: false, IVRNotificationsEnabled: false})
	if _, err := c.SendTemplate("+573001234567", TemplateConfig{}); !errors.Is(err, ErrWhatsAppNotificationsDisabled) {
		t.Errorf("expected WA disabled via config, got %v", err)
	}
	if _, err := c.PlaceCall("+573001234567", nil); !errors.Is(err, ErrIVRNotificationsDisabled) {
		t.Errorf("expected IVR disabled via config, got %v", err)
	}

	// Y con ambos habilitados, NO devuelve los sentinels de deshabilitado.
	enabled := NewClient(&config.Config{WhatsAppNotificationsEnabled: true, IVRNotificationsEnabled: true})
	if _, err := enabled.PlaceCall("+573001234567", nil); errors.Is(err, ErrIVRNotificationsDisabled) {
		t.Error("expected IVR enabled via config")
	}
}
