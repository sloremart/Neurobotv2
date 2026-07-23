package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// RegisterEntityManagementHandlers registers entity selection flow:
// ASK_CLIENT_TYPE → SHOW_ENTITY_LIST → ASK_ENTITY_NUMBER
// (Bird V2 order: entity type → entity list → entity number → document)
func RegisterEntityManagementHandlers(
	m *sm.Machine,
	entityRepo repository.EntityRepository,
	patientRepo repository.PatientRepository,
) {
	m.Register(sm.StateAskClientType, askClientTypeHandler())
	m.Register(sm.StateAskEpsRegimen, askEpsRegimenHandler())
	m.Register(sm.StateShowEntityList, showEntityListHandler(entityRepo))
	m.Register(sm.StateAskEntityNumber, askEntityNumberHandler(entityRepo))
	// Legacy handlers kept for backwards compatibility
	m.Register(sm.StateCheckEntity, checkEntityHandler(entityRepo))
	m.Register(sm.StateConfirmEntity, confirmEntityHandler())
	m.Register(sm.StateChangeEntity, changeEntityHandler(entityRepo, patientRepo))
}

// ASK_CLIENT_TYPE (interactivo) — selecciona tipo de entidad (6 categorías).
// Bird V2: list with PARTICULAR, EPS, PREPAGADA, REGIMEN ESPECIAL, ARL, POLIZA
func askClientTypeHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		validPayloads := make([]string, 6)
		for i := 1; i <= 6; i++ {
			validPayloads[i-1] = fmt.Sprintf("ct_%d", i)
		}

		result, selected := sm.ValidateButtonResponse(sess, msg, validPayloads...)
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = []sm.OutboundMessage{buildEntityTypeList()}
			return result, nil
		}

		// Parse selected index
		indexStr := strings.TrimPrefix(selected, "ct_")
		index, _ := strconv.Atoi(indexStr)

		category, ok := domain.EntityCategories[index]
		if !ok {
			return sm.NewResult(sess.CurrentState).
				WithText("Selección no válida. Intenta de nuevo."), nil
		}

		label := domain.EntityCategoryLabels[index]

		// PARTICULAR is a single option (PART02): skip the entity list and go
		// straight to the document step. The contract is resolved from the entity.
		if strings.Contains(strings.ToUpper(category), "PARTICULAR") {
			return sm.NewResult(sm.StateAskDocumentType).
				WithContext("client_type", label).
				WithContext("entity_category", category).
				WithContext("menu_option", "agendar").
				WithContext("selected_entity_code", particularEntityCode).
				WithContext("selected_entity_name", "Particular").
				WithText("Has seleccionado atención *particular*.\n\n"+docTypeMenuText()).
				WithEvent("client_type_selected", map[string]interface{}{"type": label, "category": category}), nil
		}

		// EPS entities require the régimen (contributivo/subsidiado) before the
		// entity list, since it selects the contract tariff. Ask it first.
		if category == "EPS" {
			return sm.NewResult(sm.StateAskEpsRegimen).
				WithContext("client_type", label).
				WithContext("entity_category", category).
				WithContext("entity_type_index", indexStr).
				WithButtons(
					"Para tu EPS, indícanos tu régimen de afiliación:",
					sm.Button{Text: "Contributivo", Payload: "regimen_1"},
					sm.Button{Text: "Subsidiado", Payload: "regimen_2"},
				).
				WithEvent("client_type_selected", map[string]interface{}{"type": label, "category": category}), nil
		}

		return sm.NewResult(sm.StateShowEntityList).
			WithContext("client_type", label).
			WithContext("entity_category", category).
			WithContext("entity_type_index", indexStr).
			WithEvent("client_type_selected", map[string]interface{}{"type": label, "category": category}), nil
	}
}

// ASK_EPS_REGIMEN (interactivo) — para EPS, pregunta contributivo vs subsidiado.
// El régimen determina el contrato (tarifa) al agendar.
func askEpsRegimenHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "regimen_1", "regimen_2")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "Indícanos tu régimen de afiliación:",
				Buttons: []sm.Button{
					{Text: "Contributivo", Payload: "regimen_1"},
					{Text: "Subsidiado", Payload: "regimen_2"},
				},
			}}
			return result, nil
		}

		regimen := strings.TrimPrefix(selected, "regimen_")
		return sm.NewResult(sm.StateShowEntityList).
			WithContext("eps_regimen", regimen).
			WithEvent("eps_regimen_selected", map[string]interface{}{"regimen": regimen}), nil
	}
}

// buildEntityTypeList creates the 6-option list for entity type selection.
func buildEntityTypeList() *sm.ListMessage {
	rows := make([]sm.ListRow, 6)
	for i := 1; i <= 6; i++ {
		rows[i-1] = sm.ListRow{
			ID:          fmt.Sprintf("ct_%d", i),
			Title:       domain.EntityCategoryLabels[i],
			Description: "",
		}
	}
	return &sm.ListMessage{
		Body:  "Selecciona el tipo de entidad a la que perteneces",
		Title: "Tipo de entidad",
		Sections: []sm.ListSection{{
			Title: "Tipos de entidad",
			Rows:  rows,
		}},
	}
}

// SHOW_ENTITY_LIST (automático) — muestra lista numerada de entidades filtradas por categoría.
func showEntityListHandler(entityRepo repository.EntityRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		category := sess.GetContext("entity_category")

		if entityRepo == nil {
			return sm.NewResult(sm.StateAskEntityNumber).
				WithText("Escribe el número de tu entidad:"), nil
		}

		var entities []domain.Entity
		var err error

		if category != "" {
			entities, err = entityRepo.FindActiveByCategory(ctx, category)
		} else {
			entities, err = entityRepo.FindActive(ctx)
		}
		if err != nil {
			slog.Error("entity_query_error", "category", category, "err", err)
			return sm.NewResult(sm.StateAskEntityNumber).
				WithText("Error al obtener entidades. Escribe el número de tu entidad:"), nil
		}
		slog.Debug("entity_query_result", "category", category, "count", len(entities))

		if len(entities) == 0 {
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("Lo siento, en este momento no pude completar tu solicitud automaticamente. Te voy a comunicar con uno de nuestros agentes para que pueda ayudarte.").
				WithEvent("no_entities_found", map[string]interface{}{"category": category}), nil
		}

		// Build numbered list and store ordered codes so ASK_ENTITY_NUMBER can resolve
		// by display index (independent of DB ordering).
		codes := make([]string, len(entities))
		names := make([]string, len(entities))
		var sb strings.Builder
		for i, e := range entities {
			codes[i] = e.Code
			names[i] = e.DisplayName()
			sb.WriteString(fmt.Sprintf("%d - %s\n", i+1, e.DisplayName()))
		}

		return sm.NewResult(sm.StateAskEntityNumber).
			WithContext("entity_list_count", fmt.Sprintf("%d", len(entities))).
			WithContext("entity_list_codes", strings.Join(codes, ",")).
			// Nombres EXACTAMENTE como se mostraron, para poder matchear por nombre (§8.1 #4).
			WithContext("entity_list_names", strings.Join(names, "|")).
			WithText(fmt.Sprintf("Escribe el *número* de tu entidad de la siguiente lista:\n\n%s", sb.String())).
			WithEvent("entity_list_shown", map[string]interface{}{"count": len(entities), "category": category}), nil
	}
}

// ASK_ENTITY_NUMBER (interactivo) — usuario escribe número de entidad de la lista.
func askEntityNumberHandler(entityRepo repository.EntityRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		maxCount, _ := strconv.Atoi(sess.GetContext("entity_list_count"))
		if maxCount == 0 {
			maxCount = 999
		}

		// §8.1 #5: botón de un mensaje ANTERIOR (regimen_2, ct_4… ~190/mes) o disparador interno
		// (__resume__ al volver del agente, 73/mes) → NO es culpa del paciente: re-mostrar la lista
		// SIN gastar reintento. Solo si tenemos los nombres guardados; si no, flujo actual.
		if namesCtx := sess.GetContext("entity_list_names"); namesCtx != "" {
			_, numErr := strconv.Atoi(input)
			stalePayload := stalePayloadRe.MatchString(input) || (msg.IsPostback && numErr != nil)
			if sm.IsReservedKeyword(input) {
				return sm.NewResult(sess.CurrentState).
					WithText("Continuemos 👍. Escribe el *número* o el *nombre* de tu entidad:\n\n" +
						entityListText(strings.Split(namesCtx, "|"))), nil
			}
			if stalePayload {
				return sm.NewResult(sess.CurrentState).
					WithText("Ese botón es de un paso anterior 👆. Elige tu entidad de esta lista:\n\n"+
						entityListText(strings.Split(namesCtx, "|"))).
					WithEvent("stale_payload_redirected", map[string]interface{}{"input": input}), nil
			}
		}

		matchedByName := false
		index, err := strconv.Atoi(input)
		if err != nil || index < 1 || index > maxCount {
			// §8.1 #4: el paciente suele escribir el NOMBRE de su EPS ("fomag", "capital salud",
			// "4 sanitas" — 100+/mes medidos). Antes de gastar un reintento, matchear contra los
			// nombres que se le mostraron. Solo con candidato ÚNICO; en ambigüedad, reintento normal.
			if namesCtx := sess.GetContext("entity_list_names"); namesCtx != "" {
				if m := matchEntityByName(input, strings.Split(namesCtx, "|")); m >= 1 && m <= maxCount {
					index = m
					matchedByName = true
				}
			}
			if !matchedByName {
				retryResult := sm.ValidateWithRetry(sess, input, func(s string) bool {
					n, e := strconv.Atoi(s)
					return e == nil && n >= 1 && n <= maxCount
				}, fmt.Sprintf("Escribe el *número* (1 a %d) o el *nombre* de tu entidad.", maxCount))
				return retryResult, nil
			}
		}

		category := sess.GetContext("entity_category")

		// Resolve entity code by display index.
		// Prefer the ordered list stored in session (set by showEntityListHandler after filtering),
		// which ensures indices match exactly what the patient saw on screen.
		// Fall back to DB lookup if session list is absent.
		var entityCode string
		var entityName string
		if stored := sess.GetContext("entity_list_codes"); stored != "" {
			parts := strings.Split(stored, ",")
			if index >= 1 && index <= len(parts) {
				entityCode = parts[index-1]
				if entityRepo != nil {
					entity, _ := entityRepo.FindByCode(ctx, entityCode)
					if entity != nil {
						entityName = entity.DisplayName()
					}
				}
			}
		} else if entityRepo != nil && category != "" {
			code, codeErr := entityRepo.GetCodeByIndexAndCategory(ctx, index, category)
			if codeErr == nil {
				entityCode = code
				entity, _ := entityRepo.FindByCode(ctx, code)
				if entity != nil {
					entityName = entity.DisplayName()
				}
			}
		}

		// N-25: si no se pudo resolver la entidad (error de query o índice fuera de la lista),
		// NO continuar a registro con entidad vacía (crearía paciente/cita sin entidad/contrato):
		// escalar a un agente, como en la rama "sin entidades".
		if entityCode == "" {
			slog.Warn("entity_number: empty entity code, escalating", "index", index, "category", category)
			observability.Emit(observability.TraceSession(sess.ID), "entidad", "no_entities",
				observability.EmitOpts{Phone: sess.PhoneNumber, Reason: "unresolved"})
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("Lo siento, no pude identificar tu entidad. Te comunico con un agente para ayudarte.").
				WithEvent("entity_resolution_failed", map[string]interface{}{"index": index, "category": category}), nil
		}

		observability.Emit(observability.TraceSession(sess.ID), "entidad", "entity_selected",
			observability.EmitOpts{Phone: sess.PhoneNumber})
		r := sm.NewResult(sm.StateAskDocumentType).
			WithContext("entity_number", fmt.Sprintf("%d", index)).
			WithContext("menu_option", "agendar").
			WithText(docTypeMenuText()).
			WithEvent("entity_number_selected", map[string]interface{}{"index": index, "code": entityCode}).
			WithContext("selected_entity_code", entityCode).
			WithContext("selected_entity_name", entityName)
		if matchedByName {
			// Medible en dashboard: cuántas selecciones se rescatan por nombre (antes eran invalid_input).
			r.WithEvent("entity_matched_by_name", map[string]interface{}{"input": input, "index": index})
		}

		return r, nil
	}
}

// stalePayloadRe reconoce payloads de botones de OTROS pasos que llegan como texto (el paciente tocó
// un botón de un mensaje anterior): ct_N (tipo de entidad), regimen_N (régimen). Un paciente no
// escribe eso a mano.
var stalePayloadRe = regexp.MustCompile(`^(ct|regimen)_\d+$`)

// entityListText re-arma la lista numerada mostrada (desde entity_list_names), para re-preguntarla
// sin queries cuando llega un botón viejo o un __resume__.
func entityListText(names []string) string {
	var sb strings.Builder
	for i, n := range names {
		fmt.Fprintf(&sb, "%d - %s\n", i+1, n)
	}
	return sb.String()
}

// normalizeEntityInput prepara un texto para matching de entidad: minúsculas, sin tildes/ñ, sin
// dígitos ni puntuación ("4 Sanitas." → "sanitas"), espacios colapsados.
func normalizeEntityInput(s string) string {
	repl := strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n")
	s = repl.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// matchEntityByName busca la entidad que el paciente escribió por NOMBRE contra los nombres mostrados
// (§8.1 #4). Devuelve el índice 1-based, o 0 si no hay candidato ÚNICO (0 matches o ambigüedad — en
// ese caso NUNCA adivina: elegir la EPS equivocada afecta contrato y tarifa). Niveles: igualdad exacta
// → prefijo (en cualquier dirección) → contención; gana el primer nivel con exactamente 1 candidato.
func matchEntityByName(input string, names []string) int {
	in := normalizeEntityInput(input)
	if len(in) < 3 {
		return 0
	}
	norm := make([]string, len(names))
	for i, n := range names {
		norm[i] = normalizeEntityInput(n)
	}
	// Dos niveles: igualdad exacta → contención. NO hay nivel de prefijo: "salud" haría prefijo único
	// con SALUD TOTAL aunque CAPITAL SALUD también la contiene — eso sería adivinar en ambigüedad.
	tiers := []func(name string) bool{
		func(name string) bool { return name == in },
		func(name string) bool { return strings.Contains(name, in) },
	}
	for _, match := range tiers {
		found := 0
		idx := 0
		for i, name := range norm {
			if name != "" && match(name) {
				found++
				idx = i + 1
			}
		}
		if found == 1 {
			return idx
		}
		if found > 1 {
			return 0 // ambiguo en este nivel → no adivinar
		}
	}
	return 0
}

// --- Legacy handlers (kept for existing patient entity check/change flow) ---

// CHECK_ENTITY (automático) — verifica entidad del paciente existente.
func checkEntityHandler(entityRepo repository.EntityRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		entityCode := sess.GetContext("patient_entity")

		if entityCode == "" {
			return sm.NewResult(sm.StateChangeEntity).
				WithText("No tenemos registrada tu entidad o EPS. Por favor, escribe el nombre de tu entidad:").
				WithEvent("entity_not_found", nil), nil
		}

		if entityRepo != nil {
			entity, err := entityRepo.FindByCode(ctx, entityCode)
			if err == nil && entity != nil {
				if !entity.IsActive {
					return sm.NewResult(sm.StateConfirmEntity).
						WithContext("entity_name", entity.DisplayName()).
						WithButtons(
							fmt.Sprintf("Tu entidad registrada es *%s*, pero actualmente *no tiene convenio activo* con nosotros.\n\n¿Deseas cambiar de entidad o continuar como particular?", entity.DisplayName()),
							sm.Button{Text: "Cambiar entidad", Payload: "entity_change"},
							sm.Button{Text: "Continuar", Payload: "entity_ok"},
						).
						WithEvent("entity_inactive", map[string]interface{}{"entity": entityCode}), nil
				}

				return sm.NewResult(sm.StateConfirmEntity).
					WithContext("entity_name", entity.DisplayName()).
					WithButtons(
						fmt.Sprintf("Tu entidad registrada es *%s*.\n\n¿Es correcta?", entity.DisplayName()),
						sm.Button{Text: "Sí, correcta", Payload: "entity_ok"},
						sm.Button{Text: "Cambiar entidad", Payload: "entity_change"},
					).
					WithEvent("entity_checked", map[string]interface{}{"entity": entityCode, "active": true}), nil
			}
		}

		return sm.NewResult(sm.StateConfirmEntity).
			WithButtons(
				fmt.Sprintf("Tu entidad registrada es *%s*.\n\n¿Es correcta?", entityCode),
				sm.Button{Text: "Sí, correcta", Payload: "entity_ok"},
				sm.Button{Text: "Cambiar entidad", Payload: "entity_change"},
			), nil
	}
}

// CONFIRM_ENTITY (interactivo)
func confirmEntityHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "entity_ok", "entity_change")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			entityName := sess.GetContext("entity_name")
			if entityName == "" {
				entityName = sess.GetContext("patient_entity")
			}
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: fmt.Sprintf("Tu entidad registrada es *%s*.\n\n¿Es correcta?", entityName),
				Buttons: []sm.Button{
					{Text: "Sí, correcta", Payload: "entity_ok"},
					{Text: "Cambiar entidad", Payload: "entity_change"},
				},
			}}
			return result, nil
		}

		switch selected {
		case "entity_ok":
			return sm.NewResult(sm.StateAskMedicalOrder).
				WithEvent("entity_confirmed", nil), nil

		case "entity_change":
			return sm.NewResult(sm.StateChangeEntity).
				WithText("Escribe el nombre de tu nueva *entidad o EPS* (ejemplo: Nueva EPS, Sanitas, etc.):").
				WithEvent("entity_change_requested", nil), nil
		}

		return nil, fmt.Errorf("unreachable")
	}
}

// CHANGE_ENTITY (interactivo) — busca y selecciona nueva entidad.
func changeEntityHandler(entityRepo repository.EntityRepository, patientRepo repository.PatientRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		if msg.IsPostback {
			entityCode := msg.PostbackPayload
			sess.RetryCount = 0

			patientID := sess.GetContext("patient_id")
			if patientRepo != nil && patientID != "" {
				if err := patientRepo.UpdateEntity(ctx, patientID, entityCode); err != nil {
					slog.Warn("update_entity_failed", "patient_id", patientID, "entity", entityCode, "error", err)
				}
			}

			sess.PatientEntity = entityCode

			return sm.NewResult(sm.StateAskMedicalOrder).
				WithContext("patient_entity", entityCode).
				WithEvent("entity_changed", map[string]interface{}{"entity": entityCode}), nil
		}

		input := strings.TrimSpace(msg.Text)
		if input == "" {
			return sm.NewResult(sess.CurrentState).
				WithText("Escribe el nombre de tu *entidad o EPS*:"), nil
		}

		if entityRepo == nil {
			return sm.NewResult(sm.StateAskMedicalOrder).
				WithContext("patient_entity", input), nil
		}

		entities, err := entityRepo.FindActive(ctx)
		if err != nil {
			return sm.NewResult(sess.CurrentState).
				WithText("Error al buscar entidades. Intenta de nuevo:"), nil
		}

		inputLower := strings.ToLower(input)
		var matches []domain.Entity
		for _, e := range entities {
			if strings.Contains(strings.ToLower(e.Name), inputLower) ||
				strings.EqualFold(e.Code, input) {
				matches = append(matches, e)
			}
		}

		outcome, errResult := sm.ValidateSearchCount(sess, len(matches), 10,
			"No encontré entidades con ese nombre. Intenta con otro nombre:",
			"Encontré demasiados resultados. Sé más específico con el nombre de tu entidad:")
		if errResult != nil {
			return errResult, nil
		}

		switch outcome {
		case sm.SearchExact:
			sess.RetryCount = 0
			patientID := sess.GetContext("patient_id")
			if patientRepo != nil && patientID != "" {
				if err := patientRepo.UpdateEntity(ctx, patientID, matches[0].Code); err != nil {
					slog.Warn("update_entity_failed", "patient_id", patientID, "entity", matches[0].Code, "error", err)
				}
			}
			sess.PatientEntity = matches[0].Code

			return sm.NewResult(sm.StateAskMedicalOrder).
				WithContext("patient_entity", matches[0].Code).
				WithContext("entity_name", matches[0].DisplayName()).
				WithText(fmt.Sprintf("Entidad actualizada: *%s*", matches[0].DisplayName())).
				WithEvent("entity_changed", map[string]interface{}{"entity": matches[0].Code}), nil
		case sm.SearchMultiple:
			rows := make([]sm.ListRow, len(matches))
			for i, e := range matches {
				rows[i] = sm.ListRow{
					ID:    e.Code,
					Title: e.DisplayName(),
				}
			}
			return sm.NewResult(sess.CurrentState).
				WithList("Selecciona tu entidad:", "Entidades",
					sm.ListSection{Title: "Entidades activas", Rows: rows}), nil
		}

		return nil, fmt.Errorf("unreachable: outcome=%d", outcome)
	}
}
