package statemachine

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

func imageMsgWithURL(url string) bird.InboundMessage {
	m := typedMsg("image")
	m.ImageURL = url
	return m
}

// TestImageOutOfContext_StashesOrderInQuestionStates cubre el callejón detectado en producción el
// 2026-07-27: 11 de 15 rechazos de imagen ocurrieron en ASK_CLIENT_TYPE, con pacientes reenviando la
// misma foto hasta 6 veces en 3 minutos antes de rendirse.
//
// La ironía es que PhotoIntentInterceptor manda al paciente a ASK_CLIENT_TYPE diciéndole "no necesitas
// reenviarla" — y si la reenvía igual (o si la manda antes de tiempo), se topaba con "No esperaba una
// imagen". Una orden enviada mientras el bot pregunta algo previo debe GUARDARSE, no descartarse.
func TestImageOutOfContext_StashesOrderInQuestionStates(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()

	for _, state := range []string{StateAskClientType, StateAskDocumentType, StateAskDocument} {
		t.Run(state, func(t *testing.T) {
			sess := newSess(state)
			result, intercepted := interceptor(context.Background(), sess, imageMsgWithURL("https://media.bird.com/orden.jpg"))
			if !intercepted {
				t.Fatal("esperaba interceptación")
			}
			if result.UpdateCtx["stashed_order_url"] != "https://media.bird.com/orden.jpg" {
				t.Errorf("la orden debe quedar guardada para usarla en el paso de la orden, got ctx=%v", result.UpdateCtx)
			}
			// El mensaje ya no puede ser el dead-end: debe acusar recibo.
			var texts string
			for _, m := range result.Messages {
				texts += OutboundText(m)
			}
			if strings.Contains(texts, "No esperaba una imagen") {
				t.Errorf("el paciente NO debe recibir el dead-end tras mandar su orden; got %q", texts)
			}
			if !strings.Contains(strings.ToLower(texts), "orden") {
				t.Errorf("el mensaje debe acusar recibo de la orden; got %q", texts)
			}
			if result.NextState != state {
				t.Errorf("el flujo debe continuar en %s, got %s", state, result.NextState)
			}
		})
	}
}

// TestImageOutOfContext_DoesNotStashAfterOrderRead: una foto enviada DESPUÉS de que la orden ya se leyó
// (p.ej. en GFR_CREATININE, donde el paciente manda su examen de laboratorio, o en SHOW_SLOTS) NO es la
// orden médica. Guardarla como tal la haría pasar por orden más adelante — peor que el dead-end.
func TestImageOutOfContext_DoesNotStashAfterOrderRead(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()

	sess := newSess(StateShowSlots)
	sess.Context["ocr_cups_json"] = `[{"cups_code":"890274"}]` // la orden YA fue leída

	result, intercepted := interceptor(context.Background(), sess, imageMsgWithURL("https://media.bird.com/laboratorio.jpg"))
	if !intercepted {
		t.Fatal("esperaba interceptación")
	}
	if _, ok := result.UpdateCtx["stashed_order_url"]; ok {
		t.Error("no debe guardarse como orden médica una imagen enviada después de leída la orden")
	}
}

// TestImageOutOfContext_DoesNotOverwriteExistingStash: si ya hay una orden guardada, una segunda foto no
// debe pisarla (la primera es la que el paciente mandó con intención).
func TestImageOutOfContext_DoesNotOverwriteExistingStash(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()

	sess := newSess(StateAskClientType)
	sess.Context["stashed_order_url"] = "https://media.bird.com/primera.jpg"

	result, _ := interceptor(context.Background(), sess, imageMsgWithURL("https://media.bird.com/segunda.jpg"))
	if got := result.UpdateCtx["stashed_order_url"]; got != "" && got != "https://media.bird.com/primera.jpg" {
		t.Errorf("no debe pisarse la orden ya guardada; got %q", got)
	}
}

// TestImageOutOfContext_SecondPhotoWithStash_AcknowledgesInsteadOfDeadEnd cubre lo medido en producción
// DESPUÉS del deploy del stash (auditoría ciclo 130, H130-2): en ASK_CLIENT_TYPE los 11 rechazos del día
// traían stashed=false, en clusters de hasta 4 fotos por sesión. La causa es que la orden se guarda al
// arrancar (photo_first_message / photo_intent_scheduling), así que en ASK_CLIENT_TYPE canStash es
// SIEMPRE false y la 2ª foto en adelante recibía el dead-end "No esperaba una imagen en este momento…
// primero selecciona la opción de agendar cita" — falso, porque el paciente YA está agendando y su orden
// YA está guardada. En sess:3d412228 eso terminó en escalación y expiración sin atender.
//
// No pisar el stash (correcto) y responder el dead-end (incorrecto) son DOS decisiones distintas.
func TestImageOutOfContext_SecondPhotoWithStash_AcknowledgesInsteadOfDeadEnd(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()

	sess := newSess(StateAskClientType)
	sess.Context["stashed_order_url"] = "https://media.bird.com/primera.jpg"

	result, intercepted := interceptor(context.Background(), sess, imageMsgWithURL("https://media.bird.com/segunda.jpg"))
	if !intercepted {
		t.Fatal("esperaba interceptación")
	}

	var texts string
	for _, m := range result.Messages {
		texts += OutboundText(m)
	}
	if strings.Contains(texts, "No esperaba una imagen") {
		t.Errorf("reenviar la orden ya guardada NO es un callejón: el paciente sigue en el flujo de agendar; got %q", texts)
	}
	if !strings.Contains(strings.ToLower(texts), "orden") {
		t.Errorf("el mensaje debe reconocer que su orden ya está guardada; got %q", texts)
	}
	if got := result.UpdateCtx["stashed_order_url"]; got != "" && got != "https://media.bird.com/primera.jpg" {
		t.Errorf("la orden guardada no debe pisarse; got %q", got)
	}
	if result.NextState != StateAskClientType {
		t.Errorf("el flujo debe continuar en ASK_CLIENT_TYPE, got %s", result.NextState)
	}
}
