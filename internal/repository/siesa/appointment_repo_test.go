package siesa

import "testing"

// TestSlotToDateTimeComponents_Meridiem verifica el fix N-4: el meridiano se deriva del valor
// 24h del slot (el slot SIEMPRE viene en 24h desde SlotService). La heurística previa "1-6 → pm"
// marcaba como PM los slots reales de 5–6 AM (polisomnografía/EEG matutino).
func TestSlotToDateTimeComponents_Meridiem(t *testing.T) {
	cases := []struct {
		slot         string
		wantDate     string
		wantTime     string
		wantMeridiem string
	}{
		{"202603160500", "2026-03-16", "05:00", "am"}, // 5 AM → am (antes: pm, BUG)
		{"202603160600", "2026-03-16", "06:00", "am"}, // 6 AM → am (antes: pm, BUG)
		{"202603160700", "2026-03-16", "07:00", "am"},
		{"202603161100", "2026-03-16", "11:00", "am"},
		{"202603161200", "2026-03-16", "12:00", "pm"}, // mediodía → pm
		{"202603161300", "2026-03-16", "13:00", "pm"},
		{"202603161700", "2026-03-16", "17:00", "pm"}, // 5 PM (24h) → pm
		{"202603160000", "2026-03-16", "00:00", "am"}, // medianoche → am
	}
	for _, c := range cases {
		t.Run(c.slot, func(t *testing.T) {
			date, timeStr, mer := slotToDateTimeComponents(c.slot)
			if date != c.wantDate || timeStr != c.wantTime || mer != c.wantMeridiem {
				t.Errorf("slotToDateTimeComponents(%q) = (%q,%q,%q), want (%q,%q,%q)",
					c.slot, date, timeStr, mer, c.wantDate, c.wantTime, c.wantMeridiem)
			}
		})
	}
}
