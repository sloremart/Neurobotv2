package handlers

import (
	"testing"

	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// COLA DEL PRÓXIMO DÍA HÁBIL (portado de hdg-bot, lo aplicable a Neuro). El bot atiende hasta el
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
	if txt := nb.texts(); len(txt) != 1 || txt[0] != escalationNoticeText(true) {
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
	if _, set := res.UpdateCtx[sm.CtxEscalationGate]; set || !hasClear(res, sm.CtxEscalationGate) {
		t.Fatalf("el desvío por no_agents borra la marca del gate: %v / %v", res.UpdateCtx, res.ClearCtx)
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
