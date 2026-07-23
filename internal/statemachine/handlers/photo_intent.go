package handlers

import (
	"context"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// PhotoIntentInterceptor interpreta una foto/documento enviada en el MENÚ PRINCIPAL como intención de
// agendar. En el menú el paciente YA está identificado y es común que mande la orden médica directamente;
// antes el bot respondía con un dead-end ("No esperaba una imagen") y el paciente abandonaba (~20/día,
// §3.6 del spec de mejoras). En su lugar, arranca el flujo de agendar (selección de entidad) y le pide
// reenviar la orden en el paso correcto.
//
// Requisitos de orden: debe registrarse DESPUÉS de los interceptores base (ver cmd/server), y
// ImageOutOfContextInterceptor debe dejar pasar MAIN_MENU (whitelist) para que la imagen llegue aquí.
// Vive en el paquete handlers (no en statemachine) porque necesita construir la lista de entidades
// (startAgendarFlow/buildEntityTypeRows), que no es accesible desde el paquete statemachine.
func PhotoIntentInterceptor() sm.Interceptor {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, bool) {
		if msg.MessageType != "image" && msg.MessageType != "document" {
			return nil, false
		}
		if sess.CurrentState != sm.StateMainMenu {
			return nil, false
		}

		// Visible en el funnel (flow_event) además del chat_event, para medir cuántas fotos-en-menú se
		// reconducen a agendar en vez de perderse.
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "photo_intent_scheduling",
			observability.EmitOpts{Phone: sess.PhoneNumber})

		sess.RetryCount = 0
		r := sm.NewResult(sm.StateAskClientType).
			WithText("Veo que enviaste tu orden 📷. Vamos a agendar tu cita: te pido un par de datos y la uso enseguida — no necesitas reenviarla.")
		// Stash: guardar la orden para usarla al llegar al paso de la orden (askMedicalOrderHandler).
		if u := mediaURLOf(msg); u != "" {
			r.WithContext("stashed_order_url", u)
		}
		return startAgendarFlow(r).
			WithEvent("photo_intent_scheduling", map[string]interface{}{"from_state": sess.CurrentState}), true
	}
}
