package handlers

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/observability"
)

// El tope MRC se aplica en varias capas, pero la principal —el filtro de MES de la búsqueda de
// cupos— descartaba los meses llenos EN SILENCIO. Al paciente se le sigue diciendo solo "no hay
// citas" (regla de negocio, no cambia), pero no quedaba NINGÚN rastro, así que era imposible
// responder "¿cuánto está protegiendo el tope?" ni "¿el bot agendó con el cupo ya cruzado?".
// Auditoría 01-sep-2026: agosto cerró +487 en otros_procedimientos y el único registro de todo el
// mes fueron 2 bloqueos de la última capa (21-ago), que no es donde se hace el trabajo.

// El descarte deja rastro auditable: grupo, mes, y consumido contra el tope.
func TestMRCMonthFilter_EmitsWhenMonthIsFull(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)

	// 891509 x8 sobre un consumido de 930: 938 > 932 (otros_procedimientos) → mes lleno.
	svc := mrcTestSvc(930)
	sess := wlLikeSession(`[{"service":"general","cups":[{"cups_code":"891509","cups_name":"NC","quantity":8}],"espacios":1}]`)

	filter := mrcMonthFilter(context.Background(), svc, sess, "891509")
	ok, err := filter(2026, 9)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("con el tope superado el mes debe descartarse")
	}
	stop()

	ev := sink.FindStep("agendar", "mrc_month_filtered")
	if ev == nil {
		t.Fatal("el descarte silencioso debe dejar un flow_event mrc_month_filtered")
	}
	if ev.Reason != "otros_procedimientos" {
		t.Errorf("reason debe ser el grupo MRC, got %q", ev.Reason)
	}
	if got, want := ev.Attrs["month"], "2026-09"; got != want {
		t.Errorf("attr month=%v, esperaba %v", got, want)
	}
	// Sin consumido/tope el evento diría "algo se bloqueó" pero no cuánto nos pasamos, que es
	// justo la pregunta que no se pudo responder en la auditoría.
	if ev.Attrs["consumed"] == nil || ev.Attrs["limit"] == nil {
		t.Errorf("el evento debe traer consumido y tope, got %v", ev.Attrs)
	}
}

// Y no genera ruido cuando el mes sí cabe.
func TestMRCMonthFilter_SilentWhenMonthFits(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)

	svc := mrcTestSvc(10)
	sess := wlLikeSession(`[{"service":"general","cups":[{"cups_code":"891509","cups_name":"NC","quantity":8}],"espacios":1}]`)
	filter := mrcMonthFilter(context.Background(), svc, sess, "891509")
	if ok, err := filter(2026, 9); err != nil || !ok {
		t.Fatalf("el mes cabe: ok=%v err=%v", ok, err)
	}
	stop()

	if n := sink.CountStep("agendar", "mrc_month_filtered"); n != 0 {
		t.Errorf("no debe emitirse nada cuando el mes cabe, hubo %d", n)
	}
}

// booking_success debe decir A QUÉ MES fue la cita y de qué grupo con tope consume. Antes solo
// llevaba el id de la cita, así que para saber si un agendamiento cayó en un grupo/mes pasado de
// tope había que cruzar a mano contra SIESA — y por eso la auditoría no pudo responderlo.
func TestBookingSuccess_CarriesMonthAndMRCGroup(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)

	sess := wlLikeSession(`[{"service":"general","cups":[{"cups_code":"891509","cups_name":"NC","quantity":8}],"espacios":1}]`)
	groups := mrcGroupNames(currentMRCDemands(sess, sess.GetContext("cups_code")))
	if len(groups) != 1 || groups[0] != "otros_procedimientos" {
		t.Fatalf("esperaba el grupo otros_procedimientos, got %v", groups)
	}

	// Se emite igual que en createAppointmentHandler.
	observability.Emit(observability.TraceSession(sess.ID), "agendar", "booking_success",
		observability.EmitOpts{
			Phone: sess.PhoneNumber, RefID: "12345",
			Attrs: map[string]interface{}{"date": "2026-09-15", "mrc_groups": groups[0]},
		})
	stop()

	ev := sink.FindStep("agendar", "booking_success")
	if ev == nil {
		t.Fatal("no se capturó booking_success")
	}
	if ev.Attrs["date"] != "2026-09-15" {
		t.Errorf("falta el mes destino: %v", ev.Attrs)
	}
	if ev.Attrs["mrc_groups"] != "otros_procedimientos" {
		t.Errorf("falta el grupo MRC: %v", ev.Attrs)
	}
}
