package scheduler

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakeurgent"
)

// Aviso urgente de volver a Bird desde las corridas de recordatorios (Tasks.Urgent).

// Corrida detenida por configuración: ReportProactiveConfigStop dispara el aviso, una
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

// No cuentan como falla de la línea: el tope local de envíos, el contacto no alcanzable, un rechazo
// permanente del destinatario (400 phone_invalid) ni, en el de día antes, el paciente con un agente.
// Con umbral 1, ninguno avisa en ninguna de las dos corridas.
func TestUrgent_NonLineFailuresDoNotCount(t *testing.T) {
	cases := []struct {
		name    string
		openErr error
	}{
		{"tope local", bird.ErrSendRateLimited},
		{"no contactable", bird.ErrNonContactable},
		{"rechazo permanente", fmt.Errorf("nectar: POST open: status 400 reason \"phone_invalid\": %w", bird.ErrPermanentSend)},
		{"con un agente", &bird.AlreadyOpenError{ConversationID: 55, Mine: false}},
	}
	for _, sameDay := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/sameDay=%v", tc.name, sameDay), func(t *testing.T) {
				rig := newNectarReminderRigN(t, &fakenectar.Bridge{OpenErr: tc.openErr}, sameDay, 2)
				stopRigTimers(t, rig.nm, 2)
				urgent := &fakeurgent.Recorder{}
				rig.tasks.Urgent = urgent
				rig.tasks.Cfg.UrgentUnsentThreshold = 1
				rig.run(t, sameDay)
				if fires := urgent.Fires(); len(fires) != 0 {
					t.Fatalf("no es falla de la línea: %+v", fires)
				}
			})
		}
	}
}

// Los suprimidos por entregas de WhatsApp fallando quedan para el IVR pero no avisan «volver a Bird».
func TestUrgent_NoWhatsAppDoesNotCount(t *testing.T) {
	rig := newNectarReminderRigN(t, &fakenectar.Bridge{OpenID: 77}, false, 1)
	stopRigTimers(t, rig.nm, 1)
	rig.nm.SetDeliverability(&failingDeliverability{failures: 2})
	urgent := &fakeurgent.Recorder{}
	rig.tasks.Urgent = urgent
	rig.tasks.Cfg.UrgentUnsentThreshold = 1
	rig.run(t, false)
	if fires := urgent.Fires(); len(fires) != 0 {
		t.Fatalf("la supresión no es falla de la línea: %+v", fires)
	}
	if rig.nm.PendingCount() != 1 {
		t.Fatalf("el pending para el IVR sigue: %d", rig.nm.PendingCount())
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

// reportUnsent: una corrida sin no enviados nunca avisa. Config.Load garantiza un umbral ≥ 1
// (parsePositiveInt), pero una Config armada a mano puede traer 0 y tampoco debe avisar por 0.
func TestReportUnsent_ZeroUnsentNeverFires(t *testing.T) {
	for _, threshold := range []int{0, 1} {
		urgent := &fakeurgent.Recorder{}
		tasks := &Tasks{Urgent: urgent, Cfg: &config.Config{UrgentUnsentThreshold: threshold}}
		tasks.reportUnsent(context.Background(), "confirmation", 0, false)
		if fires := urgent.Fires(); len(fires) != 0 {
			t.Fatalf("umbral %d, 0 sin enviar: %+v", threshold, fires)
		}
		tasks.reportUnsent(context.Background(), "confirmation", 1, false)
		if fires := urgent.Fires(); len(fires) != 1 || fires[0].Trigger != monitor.TriggerRemindersUnsent {
			t.Fatalf("umbral %d, 1 sin enviar avisa: %+v", threshold, fires)
		}
	}
}
