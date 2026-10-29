package statemachine

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

// El acuse de la orden fuera de horario nombra el horario VIGENTE (Machine.SetBusinessHoursText).

// legacyOutOfHoursOrderText es el texto de siempre, copiado tal cual de antes del cambio.
const legacyOutOfHoursOrderText = "Recibimos tu orden 📷. Nuestro horario para *agendar citas* es *Lunes a Viernes de 7:00 a.m. a 6:00 p.m.* y *Sábados de 7:00 a.m. a 12:00 m.*\n\nEscríbenos en ese horario y con gusto te ayudamos a programar tu cita. 😊"

func outOfHoursOrderReply(t *testing.T, m *Machine) string {
	t.Helper()
	res, ok := imageOutOfContextInterceptor(m)(context.Background(), newSess(StateOutOfHoursMenu), typedMsg("image"))
	if !ok || res == nil || len(res.Messages) == 0 {
		t.Fatal("la orden fuera de horario se intercepta con un acuse")
	}
	return OutboundText(res.Messages[0])
}

func TestOutOfHoursOrderText_FallbackByteIdentical(t *testing.T) {
	if defaultBusinessHoursEmphasized != config.DefaultBusinessHours().DescribeEmphasized() {
		t.Fatalf("el literal del respaldo se apartó del horario de siempre: %q", defaultBusinessHoursEmphasized)
	}
	// Sin cablear, con la máquina sin proveedor y con el proveedor del respaldo (bandera apagada).
	cfg := &config.Config{Schedule: config.NewCachedSchedule(config.DefaultBusinessHours())}
	wired := NewMachine()
	wired.SetBusinessHoursText(func() string { return cfg.Hours().DescribeEmphasized() })
	for name, m := range map[string]*Machine{"sin máquina": nil, "sin proveedor": NewMachine(), "respaldo": wired} {
		if got := outOfHoursOrderReply(t, m); got != legacyOutOfHoursOrderText {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestOutOfHoursOrderText_PublishedAndClosed(t *testing.T) {
	spec := config.ScheduleSpec{Timezone: config.ScheduleTimezone}
	for d := 1; d <= 5; d++ {
		spec.Windows = append(spec.Windows, config.ScheduleWindowSpec{Weekday: d, From: "08:00", To: "17:00"})
	}
	published, err := config.BusinessHoursFromSchedule(spec)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := config.BusinessHoursFromSchedule(config.ScheduleSpec{Timezone: config.ScheduleTimezone})
	if err != nil {
		t.Fatal(err)
	}
	cache := config.NewCachedSchedule(config.DefaultBusinessHours())
	cfg := &config.Config{Schedule: cache}
	m := NewMachine()
	m.SetBusinessHoursText(func() string { return cfg.Hours().DescribeEmphasized() })

	cache.Set(published, "v1")
	if got := outOfHoursOrderReply(t, m); !strings.Contains(got, "es *Lunes a Viernes de 8:00 a.m. a 5:00 p.m.*\n\nEscríbenos en ese horario") {
		t.Errorf("con horario publicado: %q", got)
	}
	cache.Set(closed, "v0")
	want := "Recibimos tu orden 📷. Escríbenos más tarde y con gusto te ayudamos a programar tu cita. 😊"
	if got := outOfHoursOrderReply(t, m); got != want {
		t.Errorf("cerrado toda la semana: %q", got)
	}
}
