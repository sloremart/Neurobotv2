package scheduler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakeurgent"
)

// Aviso urgente de volver a Bird desde las corridas de recordatorios (Tasks.Urgent).

// Corrida detenida por configuración: el llamador de LogProactiveConfigStop dispara el aviso, una
// vez, con la corrida y cuántos quedaron. No se suma el aviso de «sin enviar»: los pacientes que
// la detención dejó atrás ya van contados en este (config_stop no cuenta para el umbral).
func TestUrgent_ConfigStopFiresProactiveStopped(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		name := map[bool]string{false: "día antes", true: "mismo día"}[sameDay]
		t.Run(name, func(t *testing.T) {
			rig := newNectarReminderRigN(t, &fakenectar.Bridge{
				OpenErr: fmt.Errorf("%w (409 template_other_account)", nectar.ErrTemplateRejected),
			}, sameDay, 3)
			stopRigTimers(t, rig.nm, 3)
			urgent := &fakeurgent.Recorder{}
			rig.tasks.Urgent = urgent
			rig.tasks.Cfg.UrgentUnsentThreshold = 1
			rig.run(t, sameDay)

			fires := urgent.Fires()
			if len(fires) != 1 || fires[0].Trigger != monitor.TriggerProactiveStopped {
				t.Fatalf("un aviso de corrida detenida: %+v", fires)
			}
			run := map[bool]string{false: "confirmation", true: "confirmation_same_day"}[sameDay]
			if !strings.Contains(fires[0].Detail, run) || !strings.Contains(fires[0].Detail, "3") {
				t.Fatalf("el detalle nombra la corrida y cuántos quedaron: %q", fires[0].Detail)
			}
		})
	}
}

// Corrida con no enviados (aquí en duda) por encima del umbral: un aviso al final con la corrida y
// el conteo, sin teléfonos. Por debajo del umbral, nada.
func TestUrgent_UnsentOverThresholdFires(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		name := map[bool]string{false: "día antes", true: "mismo día"}[sameDay]
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				threshold, wantFires int
			}{{2, 1}, {3, 0}} {
				rig := newNectarReminderRigN(t, &fakenectar.Bridge{OpenErr: nectar.ErrOpenInDoubt}, sameDay, 2)
				stopRigTimers(t, rig.nm, 2)
				urgent := &fakeurgent.Recorder{}
				rig.tasks.Urgent = urgent
				rig.tasks.Cfg.UrgentUnsentThreshold = tc.threshold
				rig.run(t, sameDay)

				fires := urgent.Fires()
				if len(fires) != tc.wantFires {
					t.Fatalf("umbral %d: %d avisos, se esperaban %d: %+v", tc.threshold, len(fires), tc.wantFires, fires)
				}
				if tc.wantFires == 0 {
					continue
				}
				run := map[bool]string{false: "confirmation", true: "confirmation_same_day"}[sameDay]
				d := fires[0].Detail
				if fires[0].Trigger != monitor.TriggerRemindersUnsent || !strings.Contains(d, run) || !strings.Contains(d, "2") {
					t.Fatalf("aviso de no enviados con corrida y conteo: %+v", fires[0])
				}
				if strings.Contains(d, "300123") || strings.Contains(d, "+57") {
					t.Fatalf("sin teléfonos en el aviso: %q", d)
				}
			}
		})
	}
}

// Los envíos que fallan (no en duda) también cuentan; en el de día-antes, también los suprimidos y
// los de un agente (cada registerUnsentReminder).
func TestUrgent_SendErrorsCount(t *testing.T) {
	for _, sameDay := range []bool{false, true} {
		rig := newNectarReminderRigN(t, &fakenectar.Bridge{OpenErr: fmt.Errorf("nectar: 500 boom")}, sameDay, 2)
		stopRigTimers(t, rig.nm, 2)
		urgent := &fakeurgent.Recorder{}
		rig.tasks.Urgent = urgent
		rig.tasks.Cfg.UrgentUnsentThreshold = 2
		rig.run(t, sameDay)
		if fires := urgent.Fires(); len(fires) != 1 || fires[0].Trigger != monitor.TriggerRemindersUnsent {
			t.Fatalf("sameDay=%v: un aviso de no enviados: %+v", sameDay, fires)
		}
	}
}

// Sin aviso cableado (Telegram sin configurar o puente apagado) la corrida es la de siempre.
func TestUrgent_NilReporterNoChange(t *testing.T) {
	rig := newNectarReminderRigN(t, &fakenectar.Bridge{OpenErr: nectar.ErrOpenInDoubt}, false, 2)
	stopRigTimers(t, rig.nm, 2)
	rig.tasks.Cfg.UrgentUnsentThreshold = 1
	rig.run(t, false)
	if rig.nm.PendingCount() != 2 {
		t.Fatalf("los 2 pending para el IVR siguen: %d", rig.nm.PendingCount())
	}
}
