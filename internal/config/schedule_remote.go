package config

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DE LO QUE PUBLICA NÉCTAR AL HORARIO CON QUE DECIDE EL BOT (port de hdg-bot,
// internal/config/schedule_remote.go; contrato GET /api/bot/schedule).
//
// La conversión vive AQUÍ y no en internal/nectar porque BusinessHours guarda sus ventanas en
// campos privados, y porque internal/nectar ya importa (vía bird) a config: config importando a
// nectar sería un ciclo. Por eso los tipos de entrada son NEUTROS y quien lee el endpoint copia
// los campos (cmd/server/schedule_nectar.go).
//
// La regla: ante cualquier cosa que no se entienda, ERROR y no se aplica NADA. El refrescador
// conserva la última lectura buena; un horario a medias cerraría un día entero sin aviso.

// DefaultScheduleRefreshMin es cada cuántos minutos se relee el horario sin
// NECTAR_SCHEDULE_REFRESH_MIN en el entorno.
const DefaultScheduleRefreshMin = 5

// ScheduleTimezone es la única zona en que el contrato publica horarios. Otra correría el horario
// entero en silencio.
const ScheduleTimezone = "America/Bogota"

// ScheduleWindowSpec es una ventana publicada. Weekday es ISO (1 = lunes … 7 = domingo);
// From/To en "HH:MM" 24 h con To EXCLUSIVO y "24:00" válido como «hasta el final del día».
type ScheduleWindowSpec struct {
	Weekday  int
	From, To string
}

// ScheduleSpec es una publicación completa: zona, ventanas y festivos ("YYYY-MM-DD").
type ScheduleSpec struct {
	Timezone string
	Windows  []ScheduleWindowSpec
	Holidays []string
}

// BusinessHoursFromSchedule valida una publicación y la convierte en horario. Devuelve error —y
// un horario CERO que nadie debe aplicar— ante una zona distinta de America/Bogota, un día fuera
// de 1..7, una hora que no sea HH:MM ("24:00" solo como fin), una ventana vacía o que cruce la
// medianoche, o un festivo que no sea una fecha real.
//
// Una lista de ventanas VACÍA es válida y significa CERRADO TODA LA SEMANA (el contrato lo dice
// así), al revés que el valor cero de BusinessHours: por eso lleva la marca `closed`.
func BusinessHoursFromSchedule(spec ScheduleSpec) (BusinessHours, error) {
	if err := checkScheduleTimezone(spec.Timezone); err != nil {
		return BusinessHours{}, err
	}
	// Agrupadas por TRAMO (inicio-fin): los días con el mismo horario son UNA ventana, que es lo
	// que permite decir "Lunes a Viernes de…" en vez de enumerar los cinco días. Néctar publica
	// una fila por día.
	type span struct{ start, end int }
	days := map[span]map[int]bool{}
	var order []span
	for _, w := range spec.Windows {
		if w.Weekday < 1 || w.Weekday > len(dayLetters) {
			return BusinessHours{}, fmt.Errorf("día %d fuera de 1..7 (1 = lunes, 7 = domingo)", w.Weekday)
		}
		start, err := parseClock(w.From)
		if err != nil {
			return BusinessHours{}, fmt.Errorf("ventana del día %d: inicio %w", w.Weekday, err)
		}
		end, err := parseClockEnd(w.To)
		if err != nil {
			return BusinessHours{}, fmt.Errorf("ventana del día %d: fin %w", w.Weekday, err)
		}
		if end <= start {
			return BusinessHours{}, fmt.Errorf(
				"ventana del día %d (%s-%s): vacía o cruzando la medianoche, que no se soporta",
				w.Weekday, w.From, w.To)
		}
		s := span{start, end}
		if days[s] == nil {
			days[s] = map[int]bool{}
			order = append(order, s)
		}
		days[s][w.Weekday-1] = true // a índice de dayLetters: L=0 … D=6
	}

	holidays, err := parseScheduleHolidays(spec.Holidays)
	if err != nil {
		return BusinessHours{}, err
	}

	if len(order) == 0 {
		bh := BusinessHours{closed: true, raw: closedSpec}
		return bh.WithHolidays(holidays), nil
	}

	// Orden estable y legible: por el primer día del tramo y, a igualdad, por hora de inicio.
	sort.Slice(order, func(i, j int) bool {
		fi, fj := firstDay(days[order[i]]), firstDay(days[order[j]])
		if fi != fj {
			return fi < fj
		}
		return order[i].start < order[j].start
	})

	bh := BusinessHours{}
	for _, s := range order {
		bh.windows = append(bh.windows, windowFromDays(days[s], s.start, s.end))
	}
	bh.raw = describeSpec(bh.windows)
	return bh.WithHolidays(holidays), nil
}

// DefaultHoursWithHolidays es el horario de siempre con los festivos de una publicación, para un bot
// con puente y NECTAR_SCHEDULE_ENABLED apagada: las ventanas publicadas ni se miran, así que Néctar sin
// horario de equipos no puede dejar al bot cerrado. El festivo decide el día (atención, escalación,
// próxima apertura), pero el texto de ayuda sigue siendo el de siempre (SameAsDefault). Misma regla
// que el resto: otra zona o una fecha que no se entienda es error y no se aplica nada.
func DefaultHoursWithHolidays(spec ScheduleSpec) (BusinessHours, error) {
	if err := checkScheduleTimezone(spec.Timezone); err != nil {
		return BusinessHours{}, err
	}
	holidays, err := parseScheduleHolidays(spec.Holidays)
	if err != nil {
		return BusinessHours{}, err
	}
	bh := DefaultBusinessHours().WithHolidays(holidays)
	bh.holidaysOnly = true
	return bh, nil
}

// checkScheduleTimezone rechaza una publicación en otra zona: correría el horario y las fechas.
func checkScheduleTimezone(timezone string) error {
	if strings.TrimSpace(timezone) != ScheduleTimezone {
		return fmt.Errorf("zona horaria %q: se esperaba %q", timezone, ScheduleTimezone)
	}
	return nil
}

// closedSpec es cómo se nombra en los logs el horario sin ninguna ventana.
const closedSpec = "(sin ventanas: cerrado toda la semana)"

// parseScheduleHolidays convierte las fechas publicadas. Una que no se entienda tumba la lectura
// ENTERA: un festivo perdido es un día en que el bot atiende y no hay nadie al otro lado.
func parseScheduleHolidays(dates []string) (map[string]bool, error) {
	if len(dates) == 0 {
		return nil, nil
	}
	days := make(map[string]bool, len(dates))
	for _, raw := range dates {
		d, err := time.Parse(holidayKey, strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("festivo %q: se esperaba YYYY-MM-DD", raw)
		}
		days[d.Format(holidayKey)] = true
	}
	return days, nil
}

// firstDay es el índice del primer día del conjunto (L=0 … D=6).
func firstDay(set map[int]bool) int {
	first := len(dayLetters)
	for d := range set {
		if d < first {
			first = d
		}
	}
	return first
}

// windowFromDays arma la ventana de un tramo. Marca asRange solo si los días son CONSECUTIVOS y
// hay más de uno: es lo que hace decir "Lunes a Viernes" en vez de enumerarlos.
func windowFromDays(set map[int]bool, start, end int) businessWindow {
	idx := make([]int, 0, len(set))
	for d := range set {
		idx = append(idx, d)
	}
	sort.Ints(idx)
	w := businessWindow{start: start, end: end}
	for _, d := range idx {
		w.days = append(w.days, dayLetters[d].weekday)
	}
	w.asRange = len(idx) > 1 && idx[len(idx)-1]-idx[0] == len(idx)-1
	return w
}

// describeSpec escribe las ventanas en el formato corto ("L-V 07:00-18:00; S 07:00-12:00"):
// es lo que sale en los logs, comparable de un vistazo con el respaldo.
func describeSpec(windows []businessWindow) string {
	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		parts = append(parts, fmt.Sprintf("%s %s-%s", specDays(w), specClock(w.start), specClock(w.end)))
	}
	return strings.Join(parts, "; ")
}

func specDays(w businessWindow) string {
	letters := make([]string, 0, len(w.days))
	for _, d := range w.days {
		for _, x := range dayLetters {
			if x.weekday == d {
				letters = append(letters, x.letter)
			}
		}
	}
	if w.asRange && len(letters) > 1 {
		return letters[0] + "-" + letters[len(letters)-1]
	}
	return strings.Join(letters, ",")
}

// specClock imprime los minutos como HH:MM, incluido el 24:00 del fin.
func specClock(minute int) string { return fmt.Sprintf("%02d:%02d", minute/60, minute%60) }
