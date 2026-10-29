package config

// HORARIO DE ATENCIÓN COMO VALOR (port de hdg-bot, internal/config/business_hours.go, por función).
//
// Hasta la lectura de Néctar el horario vivía QUEMADO en handlers/greeting.go (L-V 7:00-17:59,
// Sáb 7:00-11:59). Ahora es un valor que se le pregunta al proveedor (cfg.Hours()) en cada
// decisión: el respaldo es ese mismo horario, exacto, y Néctar puede reemplazarlo sin reiniciar
// (GET /api/bot/schedule, cmd/server/schedule_nectar.go).
//
// De hdg se trae solo lo que los gates de Neuro necesitan —abierto, minutos al cierre, próxima
// apertura, festivos y el texto—. No se trae el parser de BUSINESS_HOURS/BUSINESS_HOLIDAYS (Neuro
// no tiene esas variables: el respaldo es fijo), ni la flota RPA, ni QueueDecision (la cola del
// próximo día de Neuro ya vive en handlers.nextBusinessDayQueue).

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BusinessHours es el horario de atención. El valor CERO (un Config armado a mano en pruebas)
// se comporta como el horario por defecto, nunca como "abierto siempre".
type BusinessHours struct {
	raw     string
	windows []businessWindow
	// holidays son las fechas CIVILES cerradas (clave "2006-01-02", en la zona del instante
	// que se pregunte). Nil = ninguno.
	holidays map[string]bool
	// closed marca el horario que CIERRA TODA LA SEMANA: Néctar publicó `windows: []`. Existe
	// porque "sin ventanas" ya significa el valor cero (horario por defecto). Lo pone solo
	// BusinessHoursFromSchedule.
	closed bool
}

// businessWindow es una ventana: qué días y entre qué minutos del día.
type businessWindow struct {
	days []time.Weekday
	// asRange recuerda que los días son consecutivos, lo único que Describe() necesita para
	// decir "Lunes a Viernes" en vez de enumerarlos.
	asRange    bool
	start, end int // minutos desde medianoche; end EXCLUSIVO
}

// dayLetters es el alfabeto de días, en orden de semana (la X del miércoles es la convención
// colombiana). Los nombres van con mayúscula inicial: es como el bot los ha escrito siempre
// ("Lunes a Viernes… y Sábados…").
var dayLetters = []struct {
	letter   string
	weekday  time.Weekday
	singular string
	plural   string
}{
	{"L", time.Monday, "Lunes", "Lunes"},
	{"M", time.Tuesday, "Martes", "Martes"},
	{"X", time.Wednesday, "Miércoles", "Miércoles"},
	{"J", time.Thursday, "Jueves", "Jueves"},
	{"V", time.Friday, "Viernes", "Viernes"},
	{"S", time.Saturday, "Sábado", "Sábados"},
	{"D", time.Sunday, "Domingo", "Domingos"},
}

// DefaultBusinessHours es el horario de SIEMPRE de Neuro: L-V 07:00-18:00 y S 07:00-12:00, con
// el fin exclusivo y sin festivos. Es el respaldo mientras Néctar no publique (o con
// NECTAR_SCHEDULE_ENABLED apagada), y tiene que decidir —y describirse— EXACTAMENTE como el
// horario quemado de antes: lo fija handlers.TestBusinessHours_FallbackEqualsLegacy.
func DefaultBusinessHours() BusinessHours {
	weekdays := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	return BusinessHours{
		raw: "L-V 07:00-18:00; S 07:00-12:00",
		windows: []businessWindow{
			{days: weekdays, asRange: true, start: 7 * 60, end: 18 * 60},
			{days: []time.Weekday{time.Saturday}, start: 7 * 60, end: 12 * 60},
		},
	}
}

// parseClockEnd es parseClock admitiendo además "24:00" (1440 minutos), el único caso en que
// las 24 tienen sentido: como fin exclusivo de una ventana («hasta el final del día»).
func parseClockEnd(s string) (int, error) {
	if strings.TrimSpace(s) == "24:00" {
		return 24 * 60, nil
	}
	return parseClock(s)
}

// parseClock lee "HH:MM" 24 h en minutos desde medianoche.
func parseClock(s string) (int, error) {
	s = strings.TrimSpace(s)
	hh, mm, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("hora %q sin minutos (use HH:MM)", s)
	}
	h, err := strconv.Atoi(hh)
	if err != nil {
		return 0, fmt.Errorf("hora %q no numérica", s)
	}
	m, err := strconv.Atoi(mm)
	if err != nil {
		return 0, fmt.Errorf("minutos de %q no numéricos", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("hora %q fuera de rango", s)
	}
	return h*60 + m, nil
}

// holidayKey es la fecha civil del instante, en la zona en que venga (la del reloj del bot,
// America/Bogota).
const holidayKey = "2006-01-02"

// WithHolidays devuelve una COPIA del horario con esos festivos: BusinessHours viaja por valor
// y mutarlo en el sitio lo cambiaría bajo los pies de quien ya lo tiene.
func (b BusinessHours) WithHolidays(days map[string]bool) BusinessHours {
	if len(days) == 0 {
		b.holidays = nil
		return b
	}
	copied := make(map[string]bool, len(days))
	for k, v := range days {
		if v {
			copied[k] = true
		}
	}
	b.holidays = copied
	return b
}

// IsHoliday dice si la fecha civil de t es festiva.
func (b BusinessHours) IsHoliday(t time.Time) bool {
	return b.holidays[t.Format(holidayKey)]
}

// effectiveWindows son las ventanas con que se decide: el valor cero cae en las del respaldo.
func (b BusinessHours) effectiveWindows() []businessWindow {
	if len(b.windows) == 0 {
		return DefaultBusinessHours().windows
	}
	return b.windows
}

// Open dice si el horario atiende en ese instante (fin EXCLUSIVO; un festivo cierra el día).
func (b BusinessHours) Open(t time.Time) bool {
	return b.MinutesToClose(t) > 0
}

// MinutesToClose son los minutos que le quedan a la jornada vigente en t, o 0 si está cerrado.
// Ventanas que se tocan o se pisan el mismo día (dos equipos, o 07:00-12:00 y 12:00-18:00)
// cuentan como UNA jornada: el cierre es el de la última que la continúa. Si no, el margen de
// cierre de la escalación saltaría a media jornada.
func (b BusinessHours) MinutesToClose(t time.Time) int {
	if b.closed || b.IsHoliday(t) {
		return 0
	}
	windows := b.effectiveWindows()
	minute := t.Hour()*60 + t.Minute()
	end := minute
	for extended := true; extended; {
		extended = false
		for _, w := range windows {
			if w.covers(t.Weekday()) && w.start <= end && end < w.end {
				end = w.end
				extended = true
			}
		}
	}
	return end - minute
}

// covers dice si la ventana atiende ese día de la semana.
func (w businessWindow) covers(d time.Weekday) bool {
	for _, day := range w.days {
		if day == d {
			return true
		}
	}
	return false
}

// nextOpenSweepDays acota la búsqueda de NextOpen: con festivos ya no basta una semana (un puente
// puede tapar varios días seguidos).
const nextOpenSweepDays = 21

// NextOpen es la próxima APERTURA estrictamente posterior a t, en la zona de t, saltando
// festivos. Si en tres semanas no hay ninguna (un horario publicado sin ventanas) devuelve t:
// quien la use tiene que tratar "no después de t" como «no abre».
func (b BusinessHours) NextOpen(t time.Time) time.Time {
	if b.closed {
		return t
	}
	windows := b.effectiveWindows()
	minute := t.Hour()*60 + t.Minute()
	for d := 0; d < nextOpenSweepDays; d++ {
		day := t.AddDate(0, 0, d)
		if b.IsHoliday(day) {
			continue
		}
		earliest := -1
		for _, w := range windows {
			if !w.covers(day.Weekday()) {
				continue
			}
			// Hoy solo valen las aperturas que todavía no han ocurrido.
			if d == 0 && w.start <= minute {
				continue
			}
			if earliest < 0 || w.start < earliest {
				earliest = w.start
			}
		}
		if earliest >= 0 {
			return time.Date(day.Year(), day.Month(), day.Day(),
				earliest/60, earliest%60, 0, 0, t.Location())
		}
	}
	return t
}

// String es el horario en el formato corto de hdg ("L-V 07:00-18:00; S 07:00-12:00"), para logs.
func (b BusinessHours) String() string {
	if b.raw == "" {
		return DefaultBusinessHours().raw
	}
	return b.raw
}

// Describe es el horario EN PALABRAS, para el paciente, con la forma que el bot ha usado siempre:
// "Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m.".
//
// Con el horario CERRADO TODA LA SEMANA (Néctar sin ventanas) devuelve "": no hay horario que
// nombrar, y cada texto que lo incrusta tiene su frase para ese caso (una frase fija metida en
// «Te atendemos %s» o «en horario de %s» quedaba rota).
func (b BusinessHours) Describe() string {
	if b.closed {
		return ""
	}
	return b.describe("%s")
}

// DescribeEmphasized es Describe con cada ventana en negrilla de WhatsApp, la forma del aviso de
// «no hay asesores» y del de la orden fuera de horario:
// "*Lunes a Viernes de 7:00 a.m. a 6:00 p.m.* y *Sábados de 7:00 a.m. a 12:00 m.*". "" si cerrado.
func (b BusinessHours) DescribeEmphasized() string {
	if b.closed {
		return ""
	}
	return b.describe("*%s*")
}

// SameAsDefault dice si este horario decide y se describe igual que el de siempre (sin festivos).
// Lo usa el texto de ayuda, que con el horario de siempre conserva su forma propia.
func (b BusinessHours) SameAsDefault() bool {
	return !b.closed && len(b.holidays) == 0 && b.String() == DefaultBusinessHours().String()
}

// OpenedEarlierToday dice si hoy (la fecha civil de t) hubo una apertura a la hora de t o antes:
// el día es hábil y ya empezó. Separa la pausa de una jornada partida (07-12 y 14-17, a las
// 12:30) de la madrugada antes de abrir.
func (b BusinessHours) OpenedEarlierToday(t time.Time) bool {
	if b.closed || b.IsHoliday(t) {
		return false
	}
	minute := t.Hour()*60 + t.Minute()
	for _, w := range b.effectiveWindows() {
		if w.covers(t.Weekday()) && w.start <= minute {
			return true
		}
	}
	return false
}

// ClockText es la hora de t como la escribe el bot: "2:00 p.m.", "7:30 a.m.", "12:00 m.".
func ClockText(t time.Time) string {
	return describeClock(t.Hour()*60 + t.Minute())
}

func (b BusinessHours) describe(wrap string) string {
	windows := b.effectiveWindows()
	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		parts = append(parts, fmt.Sprintf(wrap, fmt.Sprintf("%s de %s a %s",
			describeDays(w), describeClock(w.start), describeClock(w.end))))
	}
	return joinWithY(parts)
}

// describeDays: "Lunes a Viernes" para un rango, "Lunes y Sábados" para días sueltos.
func describeDays(w businessWindow) string {
	if w.asRange && len(w.days) > 1 {
		return nameOf(w.days[0], false) + " a " + nameOf(w.days[len(w.days)-1], false)
	}
	names := make([]string, 0, len(w.days))
	for _, d := range w.days {
		names = append(names, nameOf(d, true))
	}
	return joinWithY(names)
}

func nameOf(d time.Weekday, plural bool) string {
	for _, x := range dayLetters {
		if x.weekday == d {
			if plural {
				return x.plural
			}
			return x.singular
		}
	}
	return d.String()
}

// describeClock pasa los minutos a como los ha escrito el bot: 7:00 a.m., 6:00 p.m., 12:00 m.
// (mediodía) y 12:00 a.m. (medianoche, el "24:00" de fin).
func describeClock(minute int) string {
	h, m := (minute/60)%24, minute%60
	switch {
	case h == 12 && m == 0:
		return "12:00 m."
	case h < 12:
		return fmt.Sprintf("%d:%02d a.m.", hour12(h), m)
	default:
		return fmt.Sprintf("%d:%02d p.m.", hour12(h), m)
	}
}

func hour12(h int) int {
	h %= 12
	if h == 0 {
		return 12
	}
	return h
}

// joinWithY une con comas y una "y" antes del último, como se escribe en español.
func joinWithY(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " y " + parts[len(parts)-1]
	}
}
