package siesa

import (
	"errors"
	"fmt"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// TestIsUniqueViolation verifica el mapeo de errores de SQL Server a "horario tomado":
// 2627 (PRIMARY KEY) y 2601 (índice único) → true; cualquier otro error → false.
// En `citas`, una violación de PK_citas significa que el médico ya tiene una cita 'P' a esa
// fecha/hora → Create devuelve domain.ErrSlotTaken y el handler re-busca horarios.
func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"pk violation 2627", mssql.Error{Number: 2627}, true},
		{"unique index 2601", mssql.Error{Number: 2601}, true},
		{"wrapped pk 2627", fmt.Errorf("insert citas: %w", mssql.Error{Number: 2627}), true},
		{"other mssql error", mssql.Error{Number: 547}, false}, // FK violation
		{"non-mssql error", errors.New("connection reset"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUniqueViolation(c.err); got != c.want {
				t.Errorf("isUniqueViolation(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestErrSlotTaken_Identity garantiza que el sentinel conserva el texto histórico "slot_taken"
// (compatibilidad con logs/búsquedas) y que errors.Is lo reconoce a través de envoltura con %w.
func TestErrSlotTaken_Identity(t *testing.T) {
	if domain.ErrSlotTaken.Error() != "slot_taken" {
		t.Errorf("ErrSlotTaken.Error() = %q, want \"slot_taken\"", domain.ErrSlotTaken.Error())
	}
	wrapped := fmt.Errorf("create: %w", domain.ErrSlotTaken)
	if !errors.Is(wrapped, domain.ErrSlotTaken) {
		t.Error("errors.Is no reconoce ErrSlotTaken envuelto con %w")
	}
}

// TestNewAppointmentRepo_BotIdentity verifica la identidad del bot en SIESA: el usuario PRINCIPAL
// se toma de los argumentos (cédula → cod_user_asigna_cita, id → usuario_evento/id_usuario_cancela),
// y si llegan vacío/cero caen al usuario de automatización "Procesos Automáticos" como FALLBACK
// (cédula '000000', id 10006). Es la lógica que pidió el cambio: SHERNANDEZ principal, automatización
// fallback.
func TestNewAppointmentRepo_BotIdentity(t *testing.T) {
	cases := []struct {
		name       string
		inCedula   string
		inID       int
		wantCedula string
		wantID     int
	}{
		{"principal SHERNANDEZ", "1029987853", 10093, "1029987853", 10093},
		{"fallback cedula vacia", "", 10093, defaultSiesaBotCedula, 10093},
		{"fallback id cero", "1029987853", 0, "1029987853", defaultSiesaBotUserID},
		{"fallback ambos (automatizacion)", "", 0, defaultSiesaBotCedula, defaultSiesaBotUserID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewAppointmentRepo(nil, c.inCedula, c.inID)
			if r.assignCedula != c.wantCedula {
				t.Errorf("assignCedula = %q, want %q", r.assignCedula, c.wantCedula)
			}
			if r.botUserID != c.wantID {
				t.Errorf("botUserID = %d, want %d", r.botUserID, c.wantID)
			}
		})
	}
	// El fallback debe ser exactamente el usuario de automatización documentado.
	if defaultSiesaBotCedula != "000000" || defaultSiesaBotUserID != 10006 {
		t.Errorf("fallback inesperado: cedula=%q id=%d (esperado 000000/10006)",
			defaultSiesaBotCedula, defaultSiesaBotUserID)
	}
}

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

// TestChooseCupStorage verifica la regla de almacenamiento de CUPS+cantidad (existencia de la
// variante con sufijo en sis_proc_precios), reemplazo del viejo heurístico qty>4. Los flags
// variantExists reflejan lo verificado contra la BD real (2026-06-24).
func TestChooseCupStorage(t *testing.T) {
	cases := []struct {
		name          string
		base          string
		qty           int
		variantExists bool
		wantCode      string
		wantCantidad  int
	}{
		{"qty1 siempre base", "930860", 1, true, "930860", 1},
		{"qty1 sin variante", "879131", 1, false, "879131", 1},
		{"930860 qty4 variante existe -> sufijo", "930860", 4, true, "930860-4", 1},
		{"930860 qty2 sin variante -> base", "930860", 2, false, "930860", 2},
		{"891515 qty4 existe -> sufijo", "891515", 4, true, "891515-4", 1},
		{"891515 qty2 no existe -> base", "891515", 2, false, "891515", 2},
		{"1005927 qty3 existe -> sufijo (qty>4 fallaba)", "1005927", 3, true, "1005927-3", 1},
		{"891509 qty8 existe -> sufijo", "891509", 8, true, "891509-8", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, cant := chooseCupStorage(c.base, c.qty, c.variantExists)
			if code != c.wantCode || cant != c.wantCantidad {
				t.Errorf("chooseCupStorage(%q,%d,%t) = (%q,%d), want (%q,%d)",
					c.base, c.qty, c.variantExists, code, cant, c.wantCode, c.wantCantidad)
			}
		})
	}
}
