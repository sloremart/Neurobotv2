package nectar

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

// EL VOLCADO REAL de GET /api/bot/schedule (Néctar, 2026-09-12,
// docs/volcados/nectar-bot-schedule-volcado.json → cuerpo_servido, copiado tal cual a
// testdata/nectar_bot_schedule_volcado.json), parseado con el tipo Schedule real y convertido
// con la función real que decide el horario (config.BusinessHoursFromSchedule).
//
// La escena la sembró la propia prueba de Néctar sobre una base desechable: dos equipos
// fundidos el lunes, el martes partido por el almuerzo, un festivo futuro que cae justo en
// viernes, un sábado parcial, un viernes que cierra en "24:00" y sin domingo (el único equipo
// que lo abre está de baja).
func TestScheduleVolcadoReal_SeConvierteEnHorarioDeAtencion(t *testing.T) {
	raw, err := os.ReadFile("testdata/nectar_bot_schedule_volcado.json")
	if err != nil {
		t.Fatalf("leyendo el volcado real: %v", err)
	}

	var sch Schedule
	if err := json.Unmarshal(raw, &sch); err != nil {
		t.Fatalf("el tipo Schedule no decodificó el volcado real (¿cambió una clave respecto al contrato?): %v", err)
	}

	if sch.Version != "ea487ae3227eea94" {
		t.Fatalf("la version del volcado no se conservó al parsear: got %q", sch.Version)
	}

	// Copia de campos a los tipos neutros de config: config no puede importar nectar (crearía
	// el ciclo nectar → bird → config, ver internal/config/schedule_remote.go), así que esta
	// glue trivial vive por duplicado en cmd/server (scheduleSpec) y aquí. No decide nada — la
	// que decide es BusinessHoursFromSchedule, la función real.
	spec := config.ScheduleSpec{Timezone: sch.Timezone}
	for _, w := range sch.Windows {
		spec.Windows = append(spec.Windows, config.ScheduleWindowSpec{
			Weekday: w.Weekday, From: w.From, To: w.To,
		})
	}
	for _, h := range sch.Holidays {
		spec.Holidays = append(spec.Holidays, h.Date)
	}

	hours, err := config.BusinessHoursFromSchedule(spec)
	if err != nil {
		t.Fatalf("BusinessHoursFromSchedule rechazó el volcado real: %v", err)
	}

	bogota, err := time.LoadLocation("America/Bogota")
	if err != nil {
		t.Fatalf("cargando America/Bogota: %v", err)
	}
	at := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, bogota)
	}

	// Viernes 2026-09-25 (no festivo): el volcado lo cierra en "24:00" -- abierto hasta el
	// último minuto del día, cerrado ya entrado el sábado.
	if !hours.Open(at(2026, 9, 25, 23, 59)) {
		t.Error("viernes 23:59 debería estar abierto (ventana hasta \"24:00\")")
	}
	if hours.Open(at(2026, 9, 26, 0, 0)) {
		t.Error("sábado 00:00 debería estar cerrado (el viernes ya terminó)")
	}

	// Martes 2026-09-29: DOS ventanas -- el almuerzo entre ellas viaja intacto.
	if !hours.Open(at(2026, 9, 29, 7, 0)) {
		t.Error("martes 07:00 debería estar abierto (primera ventana)")
	}
	if hours.Open(at(2026, 9, 29, 12, 30)) {
		t.Error("martes 12:30 debería estar cerrado (almuerzo)")
	}
	if hours.Open(at(2026, 9, 29, 13, 59)) {
		t.Error("martes 13:59 debería seguir cerrado (almuerzo)")
	}
	if !hours.Open(at(2026, 9, 29, 14, 0)) {
		t.Error("martes 14:00 debería estar abierto (segunda ventana)")
	}
	if hours.Open(at(2026, 9, 29, 17, 0)) {
		t.Error("martes 17:00 ya debería estar cerrado")
	}

	// Lunes 2026-09-28: dos equipos solapados FUNDIDOS en una sola ventana continua
	// 07:00-17:00 -- sin el hueco que sí tiene el martes.
	for _, hm := range [][2]int{{7, 0}, {9, 0}, {12, 0}, {13, 0}, {16, 59}} {
		if !hours.Open(at(2026, 9, 28, hm[0], hm[1])) {
			t.Errorf("lunes %02d:%02d debería estar abierto (ventana fundida, sin hueco)", hm[0], hm[1])
		}
	}
	if hours.Open(at(2026, 9, 28, 17, 0)) {
		t.Error("lunes 17:00 debería estar cerrado")
	}
	if hours.Open(at(2026, 9, 28, 6, 59)) {
		t.Error("lunes 06:59 debería estar cerrado")
	}

	// Sábado 2026-09-26: parcial, 07:00-11:00 (las horas del volcado).
	if !hours.Open(at(2026, 9, 26, 7, 0)) {
		t.Error("sábado 07:00 debería estar abierto")
	}
	if !hours.Open(at(2026, 9, 26, 10, 59)) {
		t.Error("sábado 10:59 debería estar abierto")
	}
	if hours.Open(at(2026, 9, 26, 11, 0)) {
		t.Error("sábado 11:00 debería estar cerrado (fin exclusivo)")
	}

	// Domingo 2026-09-27: no aparece en el volcado (el único equipo que lo abre está de baja)
	// -> cerrado todo el día.
	if hours.Open(at(2026, 9, 27, 0, 0)) ||
		hours.Open(at(2026, 9, 27, 12, 0)) ||
		hours.Open(at(2026, 9, 27, 23, 59)) {
		t.Error("domingo debería estar cerrado todo el día")
	}

	// El festivo del volcado (2026-10-02) cae en VIERNES -- que normalmente abre 07:00-24:00
	// -- pero el festivo cierra el día entero.
	holiday := at(2026, 10, 2, 0, 0)
	if !hours.IsHoliday(holiday) {
		t.Fatal("2026-10-02 debería quedar marcado como festivo")
	}
	if hours.Open(at(2026, 10, 2, 7, 0)) {
		t.Error("festivo 07:00 debería estar cerrado pese a ser viernes")
	}
	if hours.Open(at(2026, 10, 2, 23, 59)) {
		t.Error("festivo 23:59 debería estar cerrado pese a ser viernes")
	}

	if desc := hours.Describe(); desc == "" {
		t.Error("Describe() no debería quedar vacío")
	} else if !strings.Contains(desc, "Sábado") {
		t.Errorf("Describe() debería mencionar el sábado, got %q", desc)
	}
}
