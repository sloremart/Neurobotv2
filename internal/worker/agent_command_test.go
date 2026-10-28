package worker

import (
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

func TestParseAgentCommand_Orden(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		action string
		data   string
	}{
		{
			name:   "orden with description",
			input:  "/bot orden Resonancia cerebral simple codigo 883141 cantidad 1",
			action: "orden",
			data:   "Resonancia cerebral simple codigo 883141 cantidad 1",
		},
		{
			name:   "orden with multiple procedures",
			input:  "/bot orden EMG 4 ext codigo 930810, Resonancia columna lumbar codigo 883210",
			action: "orden",
			data:   "EMG 4 ext codigo 930810, Resonancia columna lumbar codigo 883210",
		},
		{
			name:   "orden without description",
			input:  "/bot orden",
			action: "orden",
			data:   "",
		},
		{
			name:   "orden case insensitive",
			input:  "/bot ORDEN resonancia cerebral",
			action: "orden",
			data:   "resonancia cerebral",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := ParseAgentCommand(tt.input)
			if cmd.Action != tt.action {
				t.Errorf("expected action %q, got %q", tt.action, cmd.Action)
			}
			if cmd.Data != tt.data {
				t.Errorf("expected data %q, got %q", tt.data, cmd.Data)
			}
		})
	}
}

func TestParseAgentCommand_ExistingActions(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		action string
		state  string
		data   string
	}{
		{"just /bot", "/bot", "reset", "", ""},
		{"resume without state", "/bot resume", "resume", "", ""},
		{"resume with state", "/bot resume MAIN_MENU", "resume", "MAIN_MENU", ""},
		{"resume with state and data", "/bot resume ASK_DOCUMENT 1234567890", "resume", "ASK_DOCUMENT", "1234567890"},
		{"resume with multi-word data", "/bot resume ASK_MANUAL_CUPS resonancia cerebral simple", "resume", "ASK_MANUAL_CUPS", "resonancia cerebral simple"},
		{"cerrar", "/bot cerrar", "close", "", ""},
		{"info", "/bot info", "info", "", ""},
		{"unknown action", "/bot xyz", "reset", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := ParseAgentCommand(tt.input)
			if cmd.Action != tt.action {
				t.Errorf("expected action %q, got %q", tt.action, cmd.Action)
			}
			if cmd.State != tt.state {
				t.Errorf("expected state %q, got %q", tt.state, cmd.State)
			}
			if cmd.Data != tt.data {
				t.Errorf("expected data %q, got %q", tt.data, cmd.Data)
			}
		})
	}
}

// ChosenOption: la `opcion` del retorno v1 manda sobre el `return_value` del dialecto, y sin
// ninguno de los dos no hay decisión.
func TestAgentCommand_ChosenOption(t *testing.T) {
	tests := []struct {
		name string
		cmd  AgentCommand
		want string
	}{
		{"sin decision", AgentCommand{Action: "resume"}, ""},
		{"dialecto", AgentCommand{ReturnValue: "cola_vencida"}, "cola_vencida"},
		{"v1", AgentCommand{ReturnV1: &nectar.V1Return{Option: "bot_reanudar"}}, "bot_reanudar"},
		{
			"v1 manda sobre el dialecto",
			AgentCommand{ReturnValue: "cola_vencida", ReturnV1: &nectar.V1Return{Option: "bot_reanudar"}},
			"bot_reanudar",
		},
		{
			"v1 sin opcion no pisa el dialecto",
			AgentCommand{ReturnValue: "cola_vencida", ReturnV1: &nectar.V1Return{}},
			"cola_vencida",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cmd.ChosenOption(); got != tt.want {
				t.Errorf("ChosenOption() = %q, want %q", got, tt.want)
			}
		})
	}
}
