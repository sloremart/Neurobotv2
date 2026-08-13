package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Consolidación de órdenes EMG/NC de Fisiatría (docs/DISENO-CONSOLIDACION-EMG-CITAS.md).
// Se llega aquí cuando la orden trae SOLO CUPS dependientes/NC del bloque de Fisiatría (G2/G3) sin EMG
// (G1) — detectado por services.IsFisiatriaDependentOnly en confirmOCRResultHandler.

// askEmgOrderPrompt arma el prompt "¿tienes la orden de la EMG?".
func askEmgOrderPrompt() *sm.StateResult {
	return sm.NewResult(sm.StateAskEmgOrder).
		WithButtons(
			"Estos procedimientos (neuroconducción / onda F / reflejo H) se realizan *junto con una electromiografía (EMG)*, que no vino en esta orden.\n\n¿Tienes también la *orden de la electromiografía*?",
			sm.Button{Text: "Sí, la tengo", Payload: "emg_order_yes"},
			sm.Button{Text: "No la tengo", Payload: "emg_order_no"},
		)
}

// CHECK_EMG_CONSOLIDATION (auto): busca una cita EMG futura pendiente del paciente. Si existe, ofrece
// consolidar (agregar estos CUPS a esa cita); si no, pregunta si tiene la orden de la EMG.
func checkEmgConsolidationHandler(apptSvc *services.AppointmentService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		patientID := sess.GetContext("patient_id")
		if apptSvc == nil || patientID == "" {
			return askEmgOrderPrompt(), nil
		}
		appt, err := apptSvc.FindPendingEmgAppointment(ctx, patientID, services.FisiatriaEmgCodes())
		if err != nil {
			slog.Warn("emg_consolidation: find pending emg appointment", "error", err)
			return askEmgOrderPrompt(), nil
		}
		if appt == nil {
			return askEmgOrderPrompt(), nil
		}
		return sm.NewResult(sm.StateConfirmConsolidate).
			WithContext("consolidate_appt_id", appt.ID).
			WithButtons(
				fmt.Sprintf("Ya tienes una cita el *%s* que incluye tu *electromiografía*.\n\nEstos procedimientos se realizan en esa misma sesión, así que los agregaremos a *esa cita* (no necesitas una aparte). ¿Confirmas?", appt.Date.Format("2006-01-02")),
				sm.Button{Text: "Sí, agregar", Payload: "consolidate_yes"},
				sm.Button{Text: "No", Payload: "consolidate_no"},
			).
			WithEvent("emg_consolidation_offer", map[string]interface{}{"appt_id": appt.ID}), nil
	}
}

// CONFIRM_CONSOLIDATE (interactivo): confirma agregar los CUPS a la cita EMG existente.
func confirmConsolidateHandler(apptSvc *services.AppointmentService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "consolidate_yes", "consolidate_no")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Confirmas agregar estos procedimientos a tu cita existente?",
				Buttons: []sm.Button{
					{Text: "Sí, agregar", Payload: "consolidate_yes"},
					{Text: "No", Payload: "consolidate_no"},
				},
			}}
			return result, nil
		}

		switch selected {
		case "consolidate_no":
			// No quiere agregar a esa cita → ofrecer subir la orden de la EMG.
			return askEmgOrderPrompt(), nil

		case "consolidate_yes":
			if apptSvc == nil {
				return escalateEmg("No pudimos agregar los procedimientos automáticamente. Te comunico con un agente."), nil
			}
			apptID := sess.GetContext("consolidate_appt_id")
			appt, _, err := apptSvc.FindBlockByAppointmentID(ctx, apptID)
			if err != nil || appt == nil {
				//nolint:nilerr // se maneja escalando a agente, no se propaga el error a la FSM.
				return escalateEmg("No pude cargar tu cita. Te comunico con un agente."), nil
			}
			var newCups []services.CUPSEntry
			if uerr := json.Unmarshal([]byte(sess.GetContext("ocr_cups_json")), &newCups); uerr != nil || len(newCups) == 0 {
				//nolint:nilerr // se maneja escalando a agente, no se propaga el error a la FSM.
				return escalateEmg("No pudimos procesar los procedimientos. Te comunico con un agente."), nil
			}
			res, err := apptSvc.ConsolidateIntoAppointment(ctx, appt, newCups, sess.GetContext("patient_contract"))
			if err != nil {
				// H145: tope mensual MRC alcanzado — regla ABSOLUTA: no se agrega consumo. El
				// paciente conserva su cita EMG original; se informa el límite (no es error técnico).
				if errors.Is(err, services.ErrMRCLimitReached) {
					observability.Emit(observability.TraceSession(sess.ID), "agendar", "mrc_limit_blocked",
						observability.EmitOpts{Phone: sess.PhoneNumber, Reason: "emg_consolidation"})
					return buildAutoCloseResult("No fue posible agregar estos procedimientos: se alcanzó el límite mensual autorizado para este tipo de procedimiento con tu convenio. Tu cita original se mantiene. Por favor comunícate con la clínica para más información.").
						WithEvent("mrc_limit_blocked", map[string]interface{}{"appt_id": apptID, "source": "emg_consolidation"}), nil
				}
				slog.Warn("emg_consolidation: consolidate failed", "appt_id", apptID, "error", err)
				return escalateEmg("No pudimos agregar los procedimientos a tu cita. Te comunico con un agente."), nil
			}
			if res.NeedsReschedule {
				// Defensivo: con orden dependiente-sola el bloque no crece; si pasa, reprogramación manual.
				return escalateEmg("Tu cita necesita reprogramarse para incluir estos procedimientos. Te comunico con un agente."), nil
			}
			return buildAutoCloseResult(fmt.Sprintf("Listo ✅ Agregamos estos procedimientos a tu cita del *%s*. Se realizarán en la misma sesión — no necesitas otra cita.", appt.Date.Format("2006-01-02"))).
				WithEvent("emg_consolidated", map[string]interface{}{"appt_id": apptID, "added": res.AddedCups}), nil
		}
		return sm.NewResult(sess.CurrentState), nil
	}
}

// ASK_EMG_ORDER (interactivo): ¿tiene la orden de la EMG? Sí → subirla; No → avisar y cerrar.
func askEmgOrderHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "emg_order_yes", "emg_order_no")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Tienes también la *orden de la electromiografía (EMG)*?",
				Buttons: []sm.Button{
					{Text: "Sí, la tengo", Payload: "emg_order_yes"},
					{Text: "No la tengo", Payload: "emg_order_no"},
				},
			}}
			return result, nil
		}

		switch selected {
		case "emg_order_yes":
			// Guardar los dependientes de la 1ª orden para fusionarlos con la EMG de la 2ª.
			return sm.NewResult(sm.StateUploadEmgOrder).
				WithContext("emg_dep_cups_json", sess.GetContext("ocr_cups_json")).
				WithText("Perfecto. Envía una *foto clara* o *PDF* de la orden de la *electromiografía*."), nil
		case "emg_order_no":
			return buildAutoCloseResult("Para agendar estos procedimientos necesitas primero la *orden de la electromiografía (EMG)*. Cuando la tengas, escríbenos y agendamos todo junto.").
				WithEvent("emg_order_missing", nil), nil
		}
		return sm.NewResult(sess.CurrentState), nil
	}
}

// UPLOAD_EMG_ORDER (interactivo): recibe la 2ª foto (orden EMG), corre OCR y FUSIONA sus CUPS con los
// dependientes de la 1ª orden → vuelve a VALIDATE_OCR para agendar UNA sola cita (ya con EMG el bloque
// es válido y sigue el flujo normal).
func uploadEmgOrderHandler(ocrSvc *services.OCRService, birdClient *bird.Client) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		switch msg.MessageType {
		case "image", "document":
			mediaURL := msg.ImageURL
			if msg.MessageType == "document" {
				mediaURL = msg.DocumentURL
			}
			if mediaURL == "" {
				return sm.NewResult(sess.CurrentState).
					WithText("No pudimos obtener el archivo. Envía otra foto o PDF de la orden de la EMG."), nil
			}
			ocrResult, err := ocrSvc.AnalyzeDocument(ctx, mediaURL)
			if err != nil || !ocrResult.Success || len(ocrResult.Cups) == 0 {
				//nolint:nilerr // el fallo de OCR se maneja re-pidiendo la foto, no se propaga a la FSM.
				return sm.NewResult(sess.CurrentState).
					WithButtons("No pudimos leer la orden de la EMG. ¿Qué deseas hacer?", ocrRetryButtons(birdClient)...).
					WithEvent("emg_ocr_failed", nil), nil
			}

			// Fusionar EMG (2ª orden) + dependientes (1ª orden).
			mergedJSON, emgN, depN := mergeEmgAndDependentCups(ocrResult.Cups, sess.GetContext("emg_dep_cups_json"))

			return sm.NewResult(sm.StateValidateOCR).
				WithContext("ocr_cups_json", mergedJSON).
				WithClearCtx("emg_dep_cups_json").
				WithEvent("emg_order_merged", map[string]interface{}{"emg_cups": emgN, "dep_cups": depN}), nil

		default:
			if msg.IsPostback && msg.PostbackPayload == "escalate_agent" {
				return sm.NewResult(sm.StateEscalateToAgent).
					WithText("En un momento un asesor se comunicará contigo."), nil
			}
			return sm.NewResult(sess.CurrentState).
				WithText("Por favor envía una *foto* o *PDF* de la orden de la electromiografía."), nil
		}
	}
}

// escalateEmg construye una escalada a agente con mensaje.
func escalateEmg(text string) *sm.StateResult {
	return sm.NewResult(sm.StateEscalateToAgent).WithText(text)
}

// mergeEmgAndDependentCups fusiona los CUPS de la orden EMG (2ª foto, ya extraídos por OCR) con los
// dependientes guardados de la 1ª orden (emg_dep_cups_json). Devuelve el JSON combinado para
// ocr_cups_json y los conteos (emg, dependientes). Un depCupsJSON vacío o inválido → solo los EMG.
func mergeEmgAndDependentCups(emgCups []services.CUPSEntry, depCupsJSON string) (string, int, int) {
	var depCups []services.CUPSEntry
	if depCupsJSON != "" {
		_ = json.Unmarshal([]byte(depCupsJSON), &depCups)
	}
	merged := make([]services.CUPSEntry, 0, len(emgCups)+len(depCups))
	merged = append(merged, emgCups...)
	merged = append(merged, depCups...)
	mergedJSON, _ := json.Marshal(merged)
	return string(mergedJSON), len(emgCups), len(depCups)
}
