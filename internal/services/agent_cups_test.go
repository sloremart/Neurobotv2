package services

import "testing"

func TestParseAgentCups(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []CUPSEntry
	}{
		{"vacío", "", nil},
		{"solo espacios", "   ", nil},
		{"un código", "883141", []CUPSEntry{{Code: "883141", Quantity: 1}}},
		{"con cantidad", "930810:2", []CUPSEntry{{Code: "930810", Quantity: 2}}},
		{"cantidad inválida vale 1", "883141:x 930810:0", []CUPSEntry{{Code: "883141", Quantity: 1}, {Code: "930810", Quantity: 1}}},
		{"varios", " 883141:1  930810:2 ", []CUPSEntry{{Code: "883141", Quantity: 1}, {Code: "930810", Quantity: 2}}},
		{"código vacío se salta", ":3 883141", []CUPSEntry{{Code: "883141", Quantity: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAgentCups(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}
