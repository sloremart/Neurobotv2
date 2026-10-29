package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// COLA DEL PRÓXIMO DÍA HÁBIL (solo sesiones de Néctar). El bot atiende hasta el
// cierre exacto, pero lo que dependa de UNA PERSONA deja de caber en el día unos minutos antes
// (BUSINESS_HOURS_CLOSE_MARGIN_MIN) y, por supuesto, con el horario cerrado. En Néctar el caso se
// escala igual y lo único que cambia es lo que se le dice al paciente; con la apertura lejos
// (BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN) vuelve el desvío de siempre. Los casos principales viven en
// escalation_nectar_test.go; aquí, los controles y las exenciones.

// En la ventana el aviso sale aunque el paciente no haya pedido a nadie y aunque el caller ya
// hubiera avisado: lo único que necesita saber es que la espera es hasta el próximo día hábil.
func TestEscalate_QueuedAlwaysNotifiesEvenIfFlushedOrNotAsked(t *testing.T) {
	m, nb := nectarMachine(t, nextDayConfig(), nil)
	sess := nectarSession(false)
	sess.SetContext(sm.CtxEscalationNoticeFlushed, "1")

	withTime(at(2026, 10, 27, 18, 30), func() { // martes de noche
		escalateOnce(t, m, sess)
	})
	if txt := nb.texts(); len(txt) != 1 || txt[0] != escalationNoticeText(nil, true) {
		t.Fatalf("el aviso del próximo día sale igual: %v", nb.events)
	}
}

// Sin asesores en plena jornada no es la ventana del próximo día: el desvío al menú se mantiene.
func TestEscalate_NoAgentsInWorkingHoursStillDiverts(t *testing.T) {
	m, nb := nectarMachine(t, nextDayConfig(), nil)
	nb.unavailable = true
	var res *sm.StateResult
	withTime(at(2026, 10, 27, 10, 0), func() {
		res = escalateOnce(t, m, nectarSession(true))
	})
	if len(nb.events) != 0 || res.NextState != sm.StateMainMenu {
		t.Fatalf("sin asesores y en jornada NO se escala: %s %v", res.NextState, nb.events)
	}
}

// En la ventana, el «no hay asesores» de Néctar no desvía: el caso se encola.
func TestEscalate_QueuedIgnoresAvailabilityGate(t *testing.T) {
	m, nb := nectarMachine(t, nextDayConfig(), nil)
	nb.unavailable = true
	var res *sm.StateResult
	withTime(at(2026, 10, 27, 18, 30), func() {
		res = escalateOnce(t, m, nectarSession(true))
	})
	if res.NextState != sm.StateEscalated || len(nb.escalations) != 1 {
		t.Fatalf("en la ventana se encola igual: %s %v", res.NextState, nb.events)
	}
}

// La marca del próximo día describe la escalación en curso: la que no se encola la borra.
func TestEscalate_NextDayMarkClearedWhenNotQueued(t *testing.T) {
	m, _ := nectarMachine(t, nextDayConfig(), nil)
	sess := nectarSession(true)
	sess.SetContext(sm.CtxEscalationNextDay, "1")
	var res *sm.StateResult
	withTime(at(2026, 10, 27, 10, 0), func() {
		res = escalateOnce(t, m, sess)
	})
	if !hasClear(res, sm.CtxEscalationNextDay) {
		t.Fatalf("la marca vieja tiene que borrarse: %v", res.ClearCtx)
	}
	if _, still := res.UpdateCtx[sm.CtxEscalationNextDay]; still {
		t.Fatal("y no volver a escribirse")
	}
	if !res.ExtendExpiryUntil.IsZero() {
		t.Fatalf("una escalación en jornada no estira el vencimiento: %s", res.ExtendExpiryUntil)
	}
}

// El tope por sesión no desvía una escalación ya encolada mientras dure su espera…
func TestEscalate_SessionCapExemptWhileQueuedWaitOpen(t *testing.T) {
	escRepo := &mockEscalationCreator{}
	m, _ := nectarMachine(t, nextDayConfig(), escRepo)
	sess := nectarSession(true)
	sess.SetContext("escalation_count", "3")
	sess.SetContext(sm.CtxEscalationNextDay, "1")
	queuedAt := at(2026, 10, 27, 18, 30)
	sess.EscalatedAt = &queuedAt

	var res *sm.StateResult
	withTime(at(2026, 10, 27, 18, 40), func() {
		res = escalateOnce(t, m, sess)
	})
	if res.NextState != sm.StateEscalated || escRepo.calls != 1 {
		t.Fatalf("el tope no puede desviar un caso encolado: %s / %d", res.NextState, escRepo.calls)
	}
}

// …pero la exención caduca con la apertura.
func TestEscalate_SessionCapAppliesAgainAfterOpen(t *testing.T) {
	escRepo := &mockEscalationCreator{}
	m, _ := nectarMachine(t, nextDayConfig(), escRepo)
	sess := nectarSession(true)
	sess.SetContext("escalation_count", "3")
	sess.SetContext(sm.CtxEscalationNextDay, "1")
	queuedAt := at(2026, 10, 27, 18, 30)
	sess.EscalatedAt = &queuedAt

	var res *sm.StateResult
	withTime(at(2026, 10, 28, 9, 0), func() {
		res = escalateOnce(t, m, sess)
	})
	if res.NextState != sm.StateFallbackMenu || escRepo.calls != 0 {
		t.Fatalf("pasada la apertura el tope vuelve a cortar: %s / %d", res.NextState, escRepo.calls)
	}
}

// NextDayQueueApplies es el predicado que el pool consulta en el pre-envío: solo Néctar y solo
// en la ventana de la cola.
func TestNextDayQueueApplies(t *testing.T) {
	applies := NextDayQueueApplies(nextDayConfig())
	birdSess := testSess(sm.StateEscalateToAgent)
	birdSess.PhoneNumber = "+573001234567"

	withTime(at(2026, 10, 23, 17, 50), func() { // viernes, dentro del margen de cierre
		if !applies(nectarSession(true)) {
			t.Error("viernes 17:50 en Néctar: la cola aplica")
		}
		if applies(birdSess) {
			t.Error("Bird nunca encola")
		}
	})
	withTime(at(2026, 10, 23, 10, 0), func() {
		if applies(nectarSession(true)) {
			t.Error("viernes 10:00: la cola no aplica")
		}
	})
}

// wireQueuedPreFlush cablea en la máquina el pre-envío con el MISMO contrato que
// worker.PreEscalationFlush: con la cola aplicando quita solo el aviso genérico de conexión
// (sm.WithoutConnectionNotice) y manda lo demás coalescido; sin cola lo manda todo.
func wireQueuedPreFlush(m *sm.Machine, cfg *config.Config, nb *fakeNectarBridge) {
	applies := NextDayQueueApplies(cfg)
	client := bird.NewClientForTest("http://nectar.invalid")
	client.SetNectar(nb)
	m.SetPreEscalationFlush(func(_ context.Context, sess *session.Session, msgs []sm.OutboundMessage) bool {
		if applies(sess) {
			msgs = sm.WithoutConnectionNotice(msgs)
		}
		for _, out := range sm.CoalesceMessages(msgs) {
			//nolint:contextcheck // SendText no toma ctx (patrón existente del pre-envío)
			_, _ = client.SendText(sess.PhoneNumber, sess.ConversationID, sm.OutboundText(out))
		}
		return true
	})
}

// Viernes 17:50, la palabra clave «asesor» (el caller escala con el aviso genérico «te voy a
// conectar… un momento»): el paciente lee UN solo aviso, el del próximo día hábil.
func TestEscalate_QueuedCallerNoticeYieldsSingleNextDayNotice(t *testing.T) {
	cfg := nextDayConfig()
	m, nb := nectarMachine(t, cfg, nil)
	wireQueuedPreFlush(m, cfg, nb)
	const callerState = "TEST_CALLER"
	m.Register(callerState, func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		return sm.NewResult(sm.StateEscalateToAgent).WithText(sm.EscalationNoticeText), nil
	})
	sess := nectarSession(true)
	sess.CurrentState = callerState

	withTime(at(2026, 10, 23, 17, 50), func() {
		escalateOnce(t, m, sess)
	})
	if txt := nb.texts(); len(txt) != 1 || txt[0] != escalationNoticeText(nil, true) {
		t.Fatalf("un solo aviso, el del próximo día: %q", txt)
	}
}

// Viernes 17:50, BOOKING_SUCCESS con otro procedimiento pendiente que es un estudio del sueño:
// la cadena BOOKING_SUCCESS → CHECK_SPECIAL_CUPS → ESCALATE_TO_AGENT cae en la cola. La cita ya
// quedó en SIESA, así que el paciente TIENE que leer su confirmación (fecha, hora, preparación) y
// el motivo de la escalación, y después el aviso del próximo día — nunca «un momento por favor».
func TestEscalate_QueuedAfterBookingKeepsConfirmation(t *testing.T) {
	cfg := nextDayConfig()
	m, nb := nectarMachine(t, cfg, nil)
	wireQueuedPreFlush(m, cfg, nb)
	m.Register(sm.StateBookingSuccess, bookingSuccessHandler(nil))
	m.Register(sm.StateCheckSpecialCups, checkSpecialCupsHandler())

	slots := sampleSlots()
	groups := []services.CUPSGroup{
		{Cups: []services.CUPSEntry{{Code: "890271", Name: "Electromiografia", Quantity: 1}}, Espacios: 1},
		{Cups: []services.CUPSEntry{{Code: "891901", Name: "Polisomnografia", Quantity: 1}}, Espacios: 1},
	}
	groupsJSON, _ := json.Marshal(groups)
	sess := nectarSession(false)
	sess.CurrentState = sm.StateBookingSuccess
	sess.Context["available_slots_json"] = slotsJSON(slots)
	sess.Context["selected_slot_id"] = slotKey(&slots[0])
	sess.Context["cups_name"] = "Electromiografia"
	sess.Context["cups_code"] = "890271"
	sess.Context["cups_preparation"] = "Asistir con el cabello limpio"
	sess.Context["created_appointment_id"] = "apt-100"
	sess.Context["total_procedures"] = "2"
	sess.Context["current_procedure_idx"] = "0"
	sess.Context["procedures_json"] = string(groupsJSON)

	var res *sm.StateResult
	withTime(at(2026, 10, 23, 17, 50), func() {
		res = escalateOnce(t, m, sess)
	})
	if res.NextState != sm.StateEscalated {
		t.Fatalf("la cadena termina escalada y encolada: %s", res.NextState)
	}
	txt := nb.texts()
	all := strings.Join(txt, "\n")
	for _, want := range []string{"Recuerda presentarte", "Asistir con el cabello limpio", "estudios del sueño"} {
		if !strings.Contains(all, want) {
			t.Errorf("el paciente tiene que leer %q: %q", want, txt)
		}
	}
	if strings.Contains(all, sm.EscalationNoticeText) || strings.Contains(all, "Un momento por favor") {
		t.Errorf("con la cola no sale «un momento por favor»: %q", txt)
	}
	if n := strings.Count(all, escalationNoticeText(nil, true)); n != 1 || txt[len(txt)-1] != escalationNoticeText(nil, true) {
		t.Errorf("el aviso del próximo día sale una vez y al final: %d veces en %q", n, txt)
	}
}
