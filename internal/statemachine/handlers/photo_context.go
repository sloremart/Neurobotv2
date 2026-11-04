package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// mediaURLOf devuelve la URL del adjunto (imagen o documento) o "" si no es un adjunto.
func mediaURLOf(msg bird.InboundMessage) string {
	switch msg.MessageType {
	case "image":
		return msg.ImageURL
	case "document":
		return msg.DocumentURL
	}
	return ""
}

// PhotoStartInterceptor maneja la orden médica enviada como PRIMER mensaje de la sesión (§8.1 #1,
// 930/mes): en vez del dead-end "No esperaba una imagen", GUARDA la URL en el contexto
// (stashed_order_url) y deja correr el arranque normal (horario → saludo → menú). Cuando el paciente
// llegue al paso de la orden, askMedicalOrderHandler la usa sin re-pedirla. Fuera de horario el flujo
// queda idéntico al de un mensaje de texto (sin stash: no puede agendar).
// Reusa checkBusinessHoursHandler para NO duplicar la lógica de horario.
func PhotoStartInterceptor(cfg *config.Config) sm.Interceptor {
	check := checkBusinessHoursHandler(cfg)
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, bool) {
		if mediaURLOf(msg) == "" || sess.CurrentState != sm.StateCheckBusinessHours {
			return nil, false
		}
		base, err := check(ctx, sess, msg)
		if err != nil || base == nil {
			return nil, false
		}
		if base.NextState == sm.StateGreeting {
			base.WithContext(sm.StashedOrderURLsKey, sm.EncodeStashedOrderURLs([]string{mediaURLOf(msg)})).
				WithEvent("photo_first_message", map[string]interface{}{"type": msg.MessageType})
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "photo_first_message",
				observability.EmitOpts{Phone: sess.PhoneNumber})
		}
		return base, true
	}
}

// mergeCupsJSON fusiona los CUPS de una página adicional con los ya leídos (dedupe por código o, sin
// código, por nombre).
// Devuelve el JSON combinado y cuántos se agregaron. Función pura (testeable sin OCR).
func mergeCupsJSON(existingJSON string, newCups []services.CUPSEntry) (string, int) {
	var existing []services.CUPSEntry
	_ = json.Unmarshal([]byte(existingJSON), &existing)
	// Clave de deduplicación: el código o, sin código, el nombre (lo resuelve VALIDATE_OCR por nombre).
	key := func(c services.CUPSEntry) string {
		if code := strings.TrimSpace(c.Code); code != "" {
			return code
		}
		if name := services.CupsNameKey(c.Name); name != "" {
			return "nombre:" + name
		}
		return ""
	}
	seen := map[string]bool{}
	for _, c := range existing {
		seen[key(c)] = true
	}
	added := 0
	for _, c := range newCups {
		k := key(c)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		existing = append(existing, c)
		added++
	}
	out, _ := json.Marshal(existing)
	return string(out), added
}

const maxOCRAppends = 3

// ocrConfirmButtons son los botones del paso "¿Es correcto?" (mismos de validateOCRHandler).
func ocrConfirmButtons() []sm.Button {
	return []sm.Button{
		{Text: "Sí, correcto", Payload: "ocr_correct"},
		{Text: "No, corregir", Payload: "ocr_incorrect"},
	}
}

// PhotoAppendInterceptor trata una imagen enviada durante "¿Es correcto?" (CONFIRM_OCR_RESULT) como
// PÁGINA ADICIONAL de la orden (§8.1 #3, 861/mes): la lee con OCR, fusiona sus CUPS con los ya
// detectados (dedupe) y re-entra a VALIDATE_OCR, que re-valida contra el catálogo y re-muestra el
// resumen completo — reuso total del flujo existente. Tope de 3 páginas adicionales por orden.
func PhotoAppendInterceptor(ocrSvc *services.OCRService) sm.Interceptor {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, bool) {
		url := mediaURLOf(msg)
		if url == "" || sess.CurrentState != sm.StateConfirmOCRResult {
			return nil, false
		}
		appends, _ := strconv.Atoi(sess.GetContext("ocr_append_count"))
		// Las páginas que no se pudieron leer cuentan para el mismo tope de lecturas fallidas de la
		// sesión que el paso de subida; aquí no se escala (lo ya leído es una orden válida): se deja
		// de llamar al OCR y se sigue con lo leído.
		failed := failedOrderReads(sess)
		if appends >= maxOCRAppends || failed >= maxFailedOrderReads || ocrSvc == nil {
			return sm.NewResult(sess.CurrentState).
				WithButtons("Ya tengo varias imágenes de tu orden; sigamos con lo leído.\n\n¿Es correcto?",
					ocrConfirmButtons()...), true
		}

		res, err := ocrSvc.AnalyzeDocument(ctx, url)
		if err != nil || res == nil || !res.Success || len(res.Cups) == 0 {
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_append_failed",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return sm.NewResult(sess.CurrentState).
				WithButtons("No pude leer esta imagen adicional. Si es otra página de tu orden, envíala más nítida.\n\n¿Es correcto lo detectado hasta ahora?",
					ocrConfirmButtons()...).
				WithContext(sm.CtxOCRFailedReads, strconv.Itoa(failed+1)).
				WithEvent("ocr_append_failed", nil), true
		}

		merged, added := mergeCupsJSON(sess.GetContext("ocr_cups_json"), res.Cups)
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_page_appended",
			observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"added": added}})
		// VALIDATE_OCR (automático) re-valida el conjunto completo y re-muestra "¿Es correcto?".
		return sm.NewResult(sm.StateValidateOCR).
			WithContext("ocr_cups_json", merged).
			WithContext("ocr_append_count", strconv.Itoa(appends+1)).
			// Lo ya leído llega validado: lo que no se pueda agendar sale de ESTA hoja.
			WithContext(ctxOCROrderURL, url).
			WithClearCtx(sm.CtxOCRFailedReads).
			WithEvent("ocr_page_appended", map[string]interface{}{"added": added}), true
	}
}
