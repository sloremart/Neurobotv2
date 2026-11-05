package config

import (
	"sync/atomic"
	"time"
)

// EL HORARIO SE PREGUNTA EN EL MOMENTO DE DECIDIR (port de hdg-bot, internal/config/schedule.go).
//
// Quien decide algo con el horario —el gate del saludo, el de la escalación, la cola del próximo
// día hábil, el vencimiento de la cola, el texto al paciente— lo PREGUNTA por cfg.Hours() en cada
// decisión. Así lo que publique Néctar rige sin reiniciar el bot, y todos los gates leen la misma
// fuente.

// ScheduleSource entrega el horario VIGENTE. Se consulta en cada decisión: es barato (una
// lectura atómica).
type ScheduleSource interface {
	Current() BusinessHours
}

// StaticSchedule es el horario que no cambia. Sirve de proveedor en pruebas, para fijar un
// horario sin montar caché.
type StaticSchedule struct {
	hours BusinessHours
}

// NewStaticSchedule envuelve un horario fijo.
func NewStaticSchedule(hours BusinessHours) StaticSchedule {
	return StaticSchedule{hours: hours}
}

// Current devuelve siempre el mismo horario.
func (s StaticSchedule) Current() BusinessHours { return s.hours }

// scheduleSnapshot es una lectura válida ya aplicada. Viaja entera y se reemplaza de una vez
// (puntero atómico) para que nadie lea un horario nuevo con la versión vieja.
type scheduleSnapshot struct {
	hours    BusinessHours
	version  string
	loadedAt time.Time
}

// CachedSchedule guarda la ÚLTIMA lectura válida de Néctar y responde con ella; mientras no haya
// ninguna —al arrancar, sin puente o si Néctar nunca contestó— responde con el respaldo
// (DefaultBusinessHours, el horario de siempre).
//
// No caduca: un refresco que falle no llama a Set y el bot sigue con lo último bueno. Quedarse
// sin horario significaría abrir siempre o cerrar siempre. Seguro para uso concurrente: lo leen
// los turnos de todos los teléfonos y lo escribe una sola goroutine (el refrescador).
type CachedSchedule struct {
	fallback BusinessHours
	snap     atomic.Pointer[scheduleSnapshot]
}

// NewCachedSchedule arma la caché con el respaldo que rige hasta la primera lectura buena.
func NewCachedSchedule(fallback BusinessHours) *CachedSchedule {
	return &CachedSchedule{fallback: fallback}
}

// Set publica una lectura YA VALIDADA (BusinessHoursFromSchedule o DefaultHoursWithHolidays). version identifica la
// publicación del lado de Néctar; se guarda para la lectura condicional y los logs.
func (c *CachedSchedule) Set(hours BusinessHours, version string) {
	if c == nil {
		return
	}
	c.snap.Store(&scheduleSnapshot{hours: hours, version: version, loadedAt: time.Now()})
}

// Touch marca que Néctar CONTESTÓ sin traer nada nuevo (304): el horario y la versión siguen y
// solo se refresca LoadedAt. Sin lectura buena previa no hace nada: un 304 no convierte el
// respaldo en una publicación que nadie leyó.
func (c *CachedSchedule) Touch() {
	if c == nil {
		return
	}
	if s := c.snap.Load(); s != nil {
		c.snap.Store(&scheduleSnapshot{hours: s.hours, version: s.version, loadedAt: time.Now()})
	}
}

// Current es el horario vigente: la última lectura válida, o el respaldo si no hubo ninguna.
func (c *CachedSchedule) Current() BusinessHours {
	if c == nil {
		return DefaultBusinessHours()
	}
	if s := c.snap.Load(); s != nil {
		return s.hours
	}
	return c.fallback
}

// Version identifica la lectura vigente; "" mientras rija el respaldo.
func (c *CachedSchedule) Version() string {
	if c == nil {
		return ""
	}
	if s := c.snap.Load(); s != nil {
		return s.version
	}
	return ""
}

// LoadedAt es cuándo se aplicó (o se confirmó con un 304) la lectura vigente; el cero mientras
// rija el respaldo.
func (c *CachedSchedule) LoadedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	if s := c.snap.Load(); s != nil {
		return s.loadedAt
	}
	return time.Time{}
}

// Hours es el horario con el que hay que DECIDIR: el del proveedor si hay uno cableado (Schedule)
// y si no el de siempre. Config nil (pruebas de handlers) también da el de siempre: nunca
// "abierto siempre".
func (c *Config) Hours() BusinessHours {
	if c == nil || c.Schedule == nil {
		return DefaultBusinessHours()
	}
	return c.Schedule.Current()
}
