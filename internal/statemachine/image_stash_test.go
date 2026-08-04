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
			if !strings.Contains(result.UpdateCtx["stashed_order_urls"], "https://media.bird.com/orden.jpg") {
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
	if _, ok := result.UpdateCtx[StashedOrderURLsKey]; ok {
		t.Error("no debe guardarse como orden médica una imagen enviada después de leída la orden")
	}
}

// TestImageOutOfContext_DoesNotOverwriteExistingStash: si ya hay una orden guardada, una segunda foto no
// debe PISARLA. La primera hoja es la que el paciente mandó con intención y debe seguir siendo la primera
// (la segunda se agrega detrás — ver TestImageOutOfContext_StashesAdditionalPages).
func TestImageOutOfContext_DoesNotOverwriteExistingStash(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()

	sess := newSess(StateAskClientType)
	sess.Context["stashed_order_url"] = "https://media.bird.com/primera.jpg"

	result, _ := interceptor(context.Background(), sess, imageMsgWithURL("https://media.bird.com/segunda.jpg"))
	stash := result.UpdateCtx["stashed_order_urls"]
	if !strings.Contains(stash, "primera.jpg") {
		t.Errorf("no debe perderse la orden ya guardada; got %q", stash)
	}
	if i, j := strings.Index(stash, "primera.jpg"), strings.Index(stash, "segunda.jpg"); j >= 0 && i > j {
		t.Errorf("la primera hoja debe seguir siendo la primera; got %q", stash)
	}
}

// TestImageOutOfContext_StashesAdditionalPages cubre lo medido en producción el 2026-08-04
// (auditoría ciclo 133): un paciente mandó su orden completa en CINCO fotos en dos segundos, antes de
// que el bot alcanzara a contestar. La primera se guardó y las otras CUATRO se descartaron con
// stash_reason=already_stashed, respondiéndole "Ya tengo tu orden, no necesitas reenviarla" — una frase
// FALSA: el bot tenía la primera hoja, no la orden.
//
// Mandar una orden de varias hojas como varias fotos seguidas es el comportamiento normal del paciente,
// y el bot ya sabe fusionar páginas aguas abajo (PhotoAppendInterceptor/mergeCupsJSON). El stash debe
// acumularlas igual, o la cita se agenda con la mitad de los procedimientos y sin ninguna señal.
func TestImageOutOfContext_StashesAdditionalPages(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()

	sess := newSess(StateAskClientType)
	sess.Context["stashed_order_urls"] = `["https://media.bird.com/pag1.jpg"]`

	result, intercepted := interceptor(context.Background(), sess, imageMsgWithURL("https://media.bird.com/pag2.jpg"))
	if !intercepted {
		t.Fatal("esperaba interceptación")
	}
	stash := result.UpdateCtx["stashed_order_urls"]
	for _, must := range []string{"pag1.jpg", "pag2.jpg"} {
		if !strings.Contains(stash, must) {
			t.Errorf("la página %s debe quedar guardada; got %q", must, stash)
		}
	}
	if i, j := strings.Index(stash, "pag1.jpg"), strings.Index(stash, "pag2.jpg"); i > j {
		t.Errorf("las páginas deben conservar su orden de llegada; got %q", stash)
	}

	var texts string
	for _, m := range result.Messages {
		texts += OutboundText(m)
	}
	if strings.Contains(texts, "no necesitas reenviarla") {
		t.Errorf("no debe decirle que ya la tiene: acaba de recibir OTRA hoja; got %q", texts)
	}
}

// TestImageOutOfContext_StashPageCap: el stash acumula páginas pero con tope, para no disparar el costo
// de OCR de una sesión (cada hoja guardada es un análisis más al consumirla). Pasado el tope se acusa
// recibo sin guardar, nunca el dead-end.
func TestImageOutOfContext_StashPageCap(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()

	sess := newSess(StateAskClientType)
	sess.Context["stashed_order_urls"] = `["https://media.bird.com/p1.jpg","https://media.bird.com/p2.jpg","https://media.bird.com/p3.jpg","https://media.bird.com/p4.jpg"]`

	result, intercepted := interceptor(context.Background(), sess, imageMsgWithURL("https://media.bird.com/p5.jpg"))
	if !intercepted {
		t.Fatal("esperaba interceptación")
	}
	for k, v := range result.UpdateCtx {
		if strings.Contains(v, "p5.jpg") {
			t.Errorf("pasado el tope no debe guardarse otra página; got %s=%q", k, v)
		}
	}
	var texts string
	for _, m := range result.Messages {
		texts += OutboundText(m)
	}
	if strings.Contains(texts, "No esperaba una imagen") {
		t.Errorf("el tope no es un callejón: debe acusar recibo; got %q", texts)
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
