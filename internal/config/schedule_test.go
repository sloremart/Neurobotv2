package config

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// EL PROVEEDOR DEL HORARIO Y LA CONVERSIÓN DE LO QUE PUBLICA NÉCTAR (port de los tests de
// hdg-bot: internal/config/schedule_test.go y schedule_remote_test.go).

var bogota = time.FixedZone("COT", -5*3600)

// spec arma una publicación con la zona del contrato.
func spec(w ...ScheduleWindowSpec) ScheduleSpec {
	return ScheduleSpec{Timezone: ScheduleTimezone, Windows: w}
}

// weekdays arma una ventana igual para los días ISO dados.
func weekdays(from, to string, days ...int) []ScheduleWindowSpec {
	out := make([]ScheduleWindowSpec, 0, len(days))
	for _, d := range days {
		out = append(out, ScheduleWindowSpec{Weekday: d, From: from, To: to})
	}
	return out
}

// shortHours es un horario distinto del de siempre, para ver a simple vista cuál rige.
func shortHours(t *testing.T) BusinessHours {
	t.Helper()
	bh, err := BusinessHoursFromSchedule(spec(weekdays("08:00", "14:00", 1, 2, 3, 4, 5)...))
	if err != nil {
		t.Fatal(err)
	}
	return bh
}

// El respaldo es el horario de siempre, también en el formato corto de los logs y en el texto.
func TestDefaultBusinessHours_EsElDeSiempre(t *testing.T) {
	def := DefaultBusinessHours()
	if got, want := def.String(), "L-V 07:00-18:00; S 07:00-12:00"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got, want := def.Describe(), "Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m."; got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
	if got, want := def.DescribeEmphasized(), "*Lunes a Viernes de 7:00 a.m. a 6:00 p.m.* y *Sábados de 7:00 a.m. a 12:00 m.*"; got != want {
		t.Errorf("DescribeEmphasized() = %q, want %q", got, want)
	}
	// El valor cero decide como el respaldo: nunca "abierto siempre".
	var zero BusinessHours
	sunday := time.Date(2026, 10, 25, 10, 0, 0, 0, bogota)
	if zero.Open(sunday) || !zero.Open(sunday.AddDate(0, 0, 1)) {
		t.Error("el valor cero tiene que decidir como el horario de siempre")
	}
}

// Néctar publicando el horario de siempre produce el MISMO horario y el mismo texto.
func TestBusinessHoursFromSchedule_IgualQueElRespaldo(t *testing.T) {
	w := weekdays("07:00", "18:00", 1, 2, 3, 4, 5)
	w = append(w, ScheduleWindowSpec{Weekday: 6, From: "07:00", To: "12:00"})
	bh, err := BusinessHoursFromSchedule(spec(w...))
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultBusinessHours()
	if bh.String() != def.String() || bh.Describe() != def.Describe() {
		t.Errorf("String/Describe = %q / %q, se esperaba %q / %q", bh.String(), bh.Describe(), def.String(), def.Describe())
	}
}

// El volcado del contrato: lunes entero, martes partido por el almuerzo, sábado corto y dos
// festivos.
func TestBusinessHoursFromSchedule_VolcadoDelContrato(t *testing.T) {
	bh, err := BusinessHoursFromSchedule(ScheduleSpec{
		Timezone: ScheduleTimezone,
		Windows: []ScheduleWindowSpec{
			{Weekday: 1, From: "07:00", To: "17:00"},
			{Weekday: 2, From: "07:00", To: "12:00"},
			{Weekday: 2, From: "14:00", To: "17:00"},
			{Weekday: 6, From: "07:00", To: "11:00"},
		},
		Holidays: []string{"2026-10-12", "2026-11-02"},
	})
	if err != nil {
		t.Fatalf("la lectura del contrato tiene que aplicarse: %v", err)
	}
	if want := "L 07:00-17:00; M 07:00-12:00; M 14:00-17:00; S 07:00-11:00"; bh.String() != want {
		t.Errorf("String() = %q, se esperaba %q", bh.String(), want)
	}
	if got := bh.Describe(); !strings.Contains(got, "Martes de 7:00 a.m. a 12:00 m.") ||
		!strings.Contains(got, "Sábados de 7:00 a.m. a 11:00 a.m.") {
		t.Errorf("Describe() = %q", got)
	}
	tuesday := func(h, m int) time.Time { return time.Date(2026, 9, 15, h, m, 0, 0, bogota) }
	if bh.Open(tuesday(12, 30)) || !bh.Open(tuesday(11, 59)) || !bh.Open(tuesday(14, 0)) {
		t.Error("el martes atiende 07-12 y 14-17, cerrado al almuerzo")
	}
	if got, want := bh.NextOpen(tuesday(12, 30)), tuesday(14, 0); !got.Equal(want) {
		t.Errorf("NextOpen al almuerzo = %s, want %s", got, want)
	}
	// Lunes 12-oct festivo: cerrado entero y la próxima apertura lo salta.
	holiday := time.Date(2026, 10, 12, 10, 0, 0, 0, bogota)
	if bh.Open(holiday) || !bh.IsHoliday(holiday) {
		t.Error("el festivo cierra el lunes entero")
	}
	if got, want := bh.NextOpen(time.Date(2026, 10, 10, 12, 0, 0, 0, bogota)), time.Date(2026, 10, 13, 7, 0, 0, 0, bogota); !got.Equal(want) {
		t.Errorf("del sábado tras cerrar, saltando el lunes festivo: %s, want %s", got, want)
	}
}

// Ventanas que se tocan o se pisan el mismo día (dos equipos) son UNA jornada para el cierre.
func TestBusinessHours_VentanasContiguasSonUnaJornada(t *testing.T) {
	bh, err := BusinessHoursFromSchedule(spec(
		ScheduleWindowSpec{Weekday: 1, From: "07:00", To: "12:00"},
		ScheduleWindowSpec{Weekday: 1, From: "12:00", To: "16:00"},
		ScheduleWindowSpec{Weekday: 1, From: "15:00", To: "18:00"},
	))
	if err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, 10, 26, 11, 0, 0, 0, bogota)
	if got := bh.MinutesToClose(monday); got != 7*60 {
		t.Errorf("MinutesToClose a las 11:00 = %d, se esperaban 420 (cierre 18:00)", got)
	}
}

// weekday 7 es el DOMINGO (ISO), y "24:00" vale como fin.
func TestBusinessHoursFromSchedule_DomingoYMedianoche(t *testing.T) {
	bh, err := BusinessHoursFromSchedule(spec(ScheduleWindowSpec{Weekday: 7, From: "22:00", To: "24:00"}))
	if err != nil {
		t.Fatalf("24:00 es válido como fin: %v", err)
	}
	if want := "D 22:00-24:00"; bh.String() != want {
		t.Errorf("String() = %q, se esperaba %q", bh.String(), want)
	}
	sunday := time.Date(2026, 9, 13, 23, 59, 0, 0, bogota)
	if !bh.Open(sunday) || bh.Open(sunday.AddDate(0, 0, 1)) {
		t.Error("abierto el domingo 23:59, cerrado el lunes a esa hora")
	}
	if got, want := bh.Describe(), "Domingos de 10:00 p.m. a 12:00 a.m."; got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}

// windows VACÍO = cerrado toda la semana, no «abierto siempre».
func TestBusinessHoursFromSchedule_SinVentanasEsCerrado(t *testing.T) {
	bh, err := BusinessHoursFromSchedule(ScheduleSpec{Timezone: ScheduleTimezone})
	if err != nil {
		t.Fatalf("windows vacío es una lectura VÁLIDA: %v", err)
	}
	for d := 0; d < 8; d++ {
		day := time.Date(2026, 9, 14, 0, 0, 0, 0, bogota).AddDate(0, 0, d)
		for _, h := range []int{0, 7, 12, 17, 23} {
			if bh.Open(day.Add(time.Duration(h) * time.Hour)) {
				t.Fatalf("sin ventanas está CERRADO: abrió el %s a las %02d:00", day.Weekday(), h)
			}
		}
	}
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, bogota)
	if got := bh.NextOpen(now); !got.Equal(now) {
		t.Errorf("sin ninguna apertura NextOpen devuelve t, no %s", got)
	}
	// Sin horario que nombrar el texto va vacío: cada frase que lo incrusta tiene la suya.
	if bh.Describe() != "" || bh.DescribeEmphasized() != "" || bh.SameAsDefault() {
		t.Errorf("cerrado toda la semana: Describe=%q DescribeEmphasized=%q", bh.Describe(), bh.DescribeEmphasized())
	}
}

// Los RECHAZOS: en todos, la lectura NO se aplica y no sale horario a medias.
func TestBusinessHoursFromSchedule_Rechazos(t *testing.T) {
	monday := []ScheduleWindowSpec{{Weekday: 1, From: "07:00", To: "17:00"}}
	cases := []struct {
		name string
		spec ScheduleSpec
		frag string
	}{
		{"otra zona horaria", ScheduleSpec{Timezone: "UTC", Windows: monday}, "zona horaria"},
		{"zona vacía", ScheduleSpec{Windows: monday}, "zona horaria"},
		{"cruza medianoche", spec(ScheduleWindowSpec{Weekday: 1, From: "22:00", To: "02:00"}), "22:00"},
		{"ventana vacía", spec(ScheduleWindowSpec{Weekday: 1, From: "10:00", To: "10:00"}), "10:00"},
		{"hora imposible", spec(ScheduleWindowSpec{Weekday: 1, From: "07:60", To: "17:00"}), "07:60"},
		{"hora sin minutos", spec(ScheduleWindowSpec{Weekday: 1, From: "7", To: "17:00"}), "7"},
		{"24:00 como inicio", spec(ScheduleWindowSpec{Weekday: 1, From: "24:00", To: "24:00"}), "24:00"},
		{"weekday 0", spec(ScheduleWindowSpec{Weekday: 0, From: "07:00", To: "17:00"}), "día 0"},
		{"weekday 8", spec(ScheduleWindowSpec{Weekday: 8, From: "07:00", To: "17:00"}), "día 8"},
		{"festivo ilegible", ScheduleSpec{Timezone: ScheduleTimezone, Windows: monday, Holidays: []string{"12/10/2026"}}, "12/10/2026"},
		{"festivo imposible", ScheduleSpec{Timezone: ScheduleTimezone, Windows: monday, Holidays: []string{"2026-02-31"}}, "2026-02-31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bh, err := BusinessHoursFromSchedule(tc.spec)
			if err == nil {
				t.Fatalf("tenía que rechazarse; salió %q", bh.String())
			}
			if !strings.Contains(err.Error(), tc.frag) {
				t.Errorf("el error tiene que decir qué venía mal (%q): %v", tc.frag, err)
			}
			if len(bh.windows) != 0 || bh.closed {
				t.Error("una lectura rechazada no devuelve horario a medias")
			}
		})
	}
}

// Días sueltos con el mismo tramo se juntan; solo son rango si son consecutivos, y un día
// repetido (dos equipos con la misma ventana) no se cuenta dos veces.
func TestBusinessHoursFromSchedule_AgrupaDias(t *testing.T) {
	bh, err := BusinessHoursFromSchedule(spec(weekdays("07:00", "17:00", 5, 1, 3)...))
	if err != nil {
		t.Fatal(err)
	}
	if want := "L,X,V 07:00-17:00"; bh.String() != want {
		t.Errorf("String() = %q, se esperaba %q", bh.String(), want)
	}
	if want := "Lunes, Miércoles y Viernes de 7:00 a.m. a 5:00 p.m."; bh.Describe() != want {
		t.Errorf("Describe() = %q, se esperaba %q", bh.Describe(), want)
	}
	bh2, err := BusinessHoursFromSchedule(spec(weekdays("07:00", "17:00", 1, 1)...))
	if err != nil {
		t.Fatal(err)
	}
	if want := "L 07:00-17:00"; bh2.String() != want {
		t.Errorf("día repetido: String() = %q, se esperaba %q", bh2.String(), want)
	}
}

func TestStaticSchedule_SiempreElMismo(t *testing.T) {
	s := NewStaticSchedule(shortHours(t))
	if got := s.Current().String(); got != "L-V 08:00-14:00" {
		t.Errorf("Current() = %q", got)
	}
}

// Mientras nadie cargue nada rige el RESPALDO.
func TestCachedSchedule_SinCargarRigeElRespaldo(t *testing.T) {
	c := NewCachedSchedule(DefaultBusinessHours())
	if got, want := c.Current().String(), DefaultBusinessHours().String(); got != want {
		t.Errorf("Current() sin cargar = %q, se esperaba el respaldo %q", got, want)
	}
	if c.Version() != "" || !c.LoadedAt().IsZero() {
		t.Errorf("sin cargar: version=%q loadedAt=%s", c.Version(), c.LoadedAt())
	}
}

// Una lectura válida reemplaza al respaldo, con su versión y su instante.
func TestCachedSchedule_SetPublicaLaLecturaVigente(t *testing.T) {
	c := NewCachedSchedule(DefaultBusinessHours())
	before := time.Now()
	c.Set(shortHours(t), "v1")
	if got := c.Current().String(); got != "L-V 08:00-14:00" || c.Version() != "v1" || c.LoadedAt().Before(before) {
		t.Errorf("tras Set: %q / %q / %s", got, c.Version(), c.LoadedAt())
	}
	// El festivo viaja DENTRO del horario, así que llega por el mismo Set.
	c.Set(DefaultBusinessHours().WithHolidays(map[string]bool{"2026-07-20": true}), "v2")
	if c.Current().Open(time.Date(2026, 7, 20, 10, 0, 0, 0, bogota)) {
		t.Error("el festivo publicado tiene que cerrar el día")
	}
}

// Lo leen todos los turnos a la vez y lo escribe el refrescador: sin carrera.
func TestCachedSchedule_Concurrente(t *testing.T) {
	c := NewCachedSchedule(DefaultBusinessHours())
	short := shortHours(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = c.Current().Open(time.Now())
				_ = c.Version()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			c.Set(short, "v")
		}
	}()
	wg.Wait()
}

// Touch (el 304) refresca CUÁNDO se supo sin tocar horario ni versión, y no inventa una lectura.
func TestCachedSchedule_TouchRefrescaLaMarcaSinCambiarNada(t *testing.T) {
	c := NewCachedSchedule(DefaultBusinessHours())
	c.Touch()
	if !c.LoadedAt().IsZero() {
		t.Error("sin ninguna lectura buena, un 304 no puede fingir que la hubo")
	}
	c.Set(shortHours(t), "v1")
	before := c.LoadedAt()
	c.Touch()
	if c.Version() != "v1" || c.Current().String() != "L-V 08:00-14:00" || c.LoadedAt().Before(before) {
		t.Errorf("Touch no cambia lo vigente: %q / %q", c.Version(), c.Current().String())
	}
	var nilCache *CachedSchedule
	nilCache.Touch()
	nilCache.Set(shortHours(t), "v1")
	if got, want := nilCache.Current().String(), DefaultBusinessHours().String(); got != want {
		t.Errorf("un proveedor nil rige el de siempre: %q", got)
	}
}

// cfg.Hours(): con proveedor manda el proveedor; sin él, y con Config nil, el de siempre.
func TestConfigHours_PrefiereElProveedor(t *testing.T) {
	def := DefaultBusinessHours().String()
	var nilCfg *Config
	if got := nilCfg.Hours().String(); got != def {
		t.Errorf("Hours() de un Config nil = %q", got)
	}
	cfg := &Config{}
	if got := cfg.Hours().String(); got != def {
		t.Errorf("sin proveedor Hours() = %q", got)
	}
	c := NewCachedSchedule(DefaultBusinessHours())
	cfg.Schedule = c
	if got := cfg.Hours().String(); got != def {
		t.Errorf("con proveedor sin cargar Hours() = %q", got)
	}
	c.Set(shortHours(t), "v1")
	if got := cfg.Hours().String(); got != "L-V 08:00-14:00" {
		t.Errorf("tras Set, Hours() = %q", got)
	}
}
