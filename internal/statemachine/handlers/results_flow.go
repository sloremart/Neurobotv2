package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/results"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/validators"
)

// RESULTADOS DE ESTUDIOS: nadie recibe un resultado sin verificar que el documento y la fecha de
// nacimiento coinciden con el paciente de SIESA. El tope de intentos es persistente (migración
// 050) y se RESERVA antes de comparar: dos sesiones de teléfonos distintos no pueden probar a la
// vez más fechas de las que el tope permite. Nunca se escriben en logs ni en eventos el número,
// la fecha ni el nombre.

// Claves de contexto del flujo. results_doc (el número en claro) y results_studies se borran al
// salir del flujo. results_studies guarda solo los estudios listados (ya filtrados por nombre) y
// results_choice el índice del elegido.
const (
	ctxResultsDocType      = session.CtxResultsDocType
	ctxResultsDoc          = session.CtxResultsDoc
	ctxResultsDocHash      = session.CtxResultsDocHash
	ctxResultsFirstName    = session.CtxResultsFirstName
	ctxResultsFirstSurname = session.CtxResultsFirstSurname
	ctxResultsStudies      = session.CtxResultsStudies
	ctxResultsChoice       = session.CtxResultsChoice
)

// resultsFlowKeys son todas las claves del flujo: se borran al salir de él, al escalar y al cerrar
// la sesión (la lista vive en session porque el cierre por inactividad también la usa).
var resultsFlowKeys = session.ResultsFlowKeys

// resultsSearcher es la API de resultados (results.Client).
type resultsSearcher interface {
	Search(ctx context.Context, documentNumber string) ([]results.Study, error)
	DownloadPDF(ctx context.Context, file string) ([]byte, error)
}

// resultsAttempts es el tope persistente de intentos (local.ResultsAttemptsRepo).
type resultsAttempts interface {
	IsLocked(ctx context.Context, docHash, phone string, now time.Time) (bool, error)
	RecordFailure(ctx context.Context, docHash, phone string, now time.Time) (attempt int, locked bool, err error)
	Reset(ctx context.Context, docHash string) error
	Release(ctx context.Context, phone string, now time.Time) error
}

// resultsFeatureOn dice si «Resultados de estudios» entra a la verificación (y luego al PDF): hay
// API, clave del hash del documento (RESULTS_HASH_SECRET), Néctar ya tiene los adjuntos y la
// conversación es de Néctar. Si no, el enlace de siempre.
func resultsFeatureOn(cfg *config.Config, sess *session.Session) bool {
	return cfg != nil && cfg.ResultsAPIURL != "" && cfg.ResultsHashSecret != "" && cfg.ResultsPDFEnabled &&
		session.IsNectarSession(sess.PhoneNumber)
}

// resultsPhone es el teléfono con que se cuenta el tope: el del contacto de Néctar si se conoce;
// si no, la clave de la sesión.
func resultsPhone(sess *session.Session) string {
	if p := session.ContactPhoneOf(sess); p != "" {
		return p
	}
	return sess.PhoneNumber
}

// Mensajes al paciente.
const resultsMismatchText = "Los datos no coinciden. Volvamos a intentarlo.\n\n"

func resultsLockedText(cfg *config.Config) string {
	// Sin RESULTS_URL solo cae la parte del portal: SIAU se menciona siempre.
	return "Por seguridad, la consulta está bloqueada por hoy. " +
		resultsPortalText(cfg, "Puedes consultar tus resultados en %s.") +
		" También puedes escribir *menú* y elegir Atención al usuario (SIAU)."
}

func resultsUnavailableText(cfg *config.Config) string {
	return "No pude verificar tus datos en este momento. 🙏 " +
		resultsPortalText(cfg, "Puedes consultar tus resultados en %s")
}

// resultsMaxAttempts es el tope de fallos de verificación (el mismo del repo del tope).
const resultsMaxAttempts = 3

// Motivos de results_fallback_link.
const (
	resultsFallbackLocked     = "locked"
	resultsFallbackSIESADown  = "siesa_down"
	resultsFallbackAttemptsDB = "attempts_down"
)

// resultsFallback cierra el flujo con el enlace al portal y borra su contexto.
func resultsFallback(text, reason string) *sm.StateResult {
	return sm.NewResult(sm.StateTerminated).
		WithText(text).
		WithClearCtx(resultsFlowKeys...).
		WithEvent("results_fallback_link", map[string]interface{}{"reason": reason})
}

// resultsBackToMenu es la salida del agente hacia el menú principal, olvidando el flujo.
func resultsBackToMenu() sm.ReturnOption {
	return sm.ReturnOption{Label: "Volver al menú", Value: "results_menu", Target: sm.StateMainMenu, Clear: resultsFlowKeys}
}

// resultsGated envuelve el handler de un paso del flujo: si la función ya no está encendida (sin
// URL, bandera apagada, sesión de Bird) o falta el cliente o el tope, no ejecuta el paso, que no
// debe tocar nada ni enviar nada facturable, y manda el enlace de siempre. Cubre las sesiones que
// quedaron en un paso RESULTS_* antes de apagar la función o que cambiaron de canal.
func resultsGated(cfg *config.Config, enabled bool, h sm.StateHandler) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		if !enabled || !resultsFeatureOn(cfg, sess) {
			return sm.NewResult(sm.StateShowResults).WithClearCtx(resultsFlowKeys...), nil
		}
		return h(ctx, sess, msg)
	}
}

// RegisterResultsFlowHandlers registra la verificación de «Resultados de estudios», la lista y el
// envío del PDF. Se registra SIEMPRE (una sesión huérfana en un paso RESULTS_* no debe quedar sin
// handler): con la función apagada cada paso manda el enlace de siempre. patients nil se trata
// como SIESA caída (enlace al portal); searcher o attempts nil = función apagada.
func RegisterResultsFlowHandlers(m *sm.Machine, cfg *config.Config, patients repository.PatientRepository,
	searcher resultsSearcher, sender resultsSender, attempts resultsAttempts,
) {
	on := searcher != nil && attempts != nil
	gate := func(h sm.StateHandler) sm.StateHandler { return resultsGated(cfg, on, h) }
	m.Register(sm.StateResultsStart, gate(resultsStartHandler(cfg, attempts)))

	m.Register(sm.StateResultsAskDocType, gate(resultsDocTypeHandler()))
	m.RegisterWithConfig(sm.StateResultsAskDocNumber, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: validators.Document,
		ErrorMsg:     "Por favor ingresa un número de documento válido (solo números, entre 5 y 15 dígitos).",
		Handler:      gate(resultsDocNumberHandler(cfg, attempts)),
	})
	m.Register(sm.StateResultsAskBirthDate, gate(resultsBirthDateHandler(cfg, patients, attempts)))
	// Pasos escalables: el agente reanuda (el paso vuelve a preguntar) o vuelve al menú.
	for _, step := range []string{sm.StateResultsAskDocType, sm.StateResultsAskDocNumber, sm.StateResultsAskBirthDate} {
		sm.RegisterResumeLabel(step, "Reanudar")
		sm.RegisterStaticReturnMenu(step, resultsBackToMenu())
	}

	m.Register(sm.StateResultsLookup, gate(resultsLookupHandler(cfg, searcher)))
	m.Register(sm.StateResultsList, gate(resultsListHandler(cfg)))
	// La lista se resuelve eligiendo: el agente reanuda (se repite la lista) o vuelve al menú.
	sm.RegisterStaticReturnMenu(sm.StateResultsList, resultsBackToMenu())
	m.Register(sm.StateResultsSend, gate(resultsSendHandler(cfg, searcher, sender, resultsSendBudget)))
}

// RESULTS_START (automático) — revisa el tope del teléfono antes de pedir nada.
func resultsStartHandler(cfg *config.Config, attempts resultsAttempts) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		// Sin documento todavía: solo el teléfono (HashDocument nunca da "", así que no hay fila "doc:").
		locked, err := attempts.IsLocked(ctx, "", resultsPhone(sess), nowFunc())
		if err != nil {
			slog.Error("results_attempts_check_failed", "error", err)
			return resultsFallback(resultsUnavailableText(cfg), resultsFallbackAttemptsDB), nil
		}
		if locked {
			return resultsFallback(resultsLockedText(cfg), resultsFallbackLocked), nil
		}
		return sm.NewResult(sm.StateResultsAskDocType).
			WithClearCtx(resultsFlowKeys...).
			WithText(docTypeMenuText()).
			WithEvent("results_menu", nil), nil
	}
}

// RESULTS_ASK_DOC_TYPE (interactivo) — el mismo menú y la misma validación del registro.
func resultsDocTypeHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		if retry := sm.ValidateWithRetry(sess, input,
			func(s string) bool { return parseDocType(s) != "" }, docTypeMenuText()); retry != nil {
			return retry, nil
		}
		return sm.NewResult(sm.StateResultsAskDocNumber).
			WithContext(ctxResultsDocType, parseDocType(input)).
			WithText(documentNumberQuestion), nil
	}
}

// RESULTS_ASK_DOC_NUMBER (interactivo; validación declarativa) — guarda el número y su hash, y no
// pide la fecha si el documento ya está bloqueado.
func resultsDocNumberHandler(cfg *config.Config, attempts resultsAttempts) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		doc := strings.TrimSpace(msg.Text)
		docHash := local.HashDocument(cfg.ResultsHashSecret, sess.GetContext(ctxResultsDocType), doc)
		locked, err := attempts.IsLocked(ctx, docHash, resultsPhone(sess), nowFunc())
		if err != nil {
			slog.Error("results_attempts_check_failed", "error", err)
			return resultsFallback(resultsUnavailableText(cfg), resultsFallbackAttemptsDB), nil
		}
		if locked {
			return resultsFallback(resultsLockedText(cfg), resultsFallbackLocked), nil
		}
		return sm.NewResult(sm.StateResultsAskBirthDate).
			WithContext(ctxResultsDoc, doc).
			WithContext(ctxResultsDocHash, docHash).
			WithText(birthDateHelp), nil
	}
}

// RESULTS_ASK_BIRTH_DATE (interactivo) — verifica contra SIESA.
//
// Orden (Ruling 4): 1) SIESA, que si falla no gasta intento; 2) se RESERVA el intento
// (RecordFailure, que devuelve su número): pasado el tope (4.º o más, también el de una sesión
// concurrente) se rechaza sin comparar; 3) se compara; 4) si coincide, Reset del documento y
// Release del teléfono (el acierto no deja intento gastado); si no, el 3.º cierra con el portal y
// SIAU. El tope es así de exactamente 3 fallos. El paciente inexistente y la fecha distinta dan
// el mismo mensaje.
func resultsBirthDateHandler(cfg *config.Config, patients repository.PatientRepository, attempts resultsAttempts) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		if resultsMissingDoc(sess) {
			return resultsRestart(), nil
		}
		birthDate, verdict := judgeBirthDate(msg.Text)
		switch verdict {
		case birthDateUnreadable:
			return sm.ValidateWithRetry(sess, "", func(string) bool { return false },
				sm.ResumeText(sess, "No entendí la fecha. "+birthDateHelp, birthDateHelp)), nil
		case birthDateInFuture:
			return sm.NewResult(sess.CurrentState).WithText("La fecha no puede ser en el futuro. " + birthDateHelp), nil
		case birthDateBefore1900:
			return sm.NewResult(sess.CurrentState).WithText("Fecha no válida. " + birthDateHelp), nil
		}
		sess.RetryCount = 0

		docType, doc, docHash := sess.GetContext(ctxResultsDocType), sess.GetContext(ctxResultsDoc), sess.GetContext(ctxResultsDocHash)
		if patients == nil {
			return resultsFallback(resultsUnavailableText(cfg), resultsFallbackSIESADown), nil
		}
		patient, err := patients.FindByDocument(ctx, docType, doc)
		if err != nil {
			slog.Error("results_siesa_lookup_failed", "error", err)
			return resultsFallback(resultsUnavailableText(cfg), resultsFallbackSIESADown), nil
		}

		phone := resultsPhone(sess)
		attempt, locked, err := attempts.RecordFailure(ctx, docHash, phone, nowFunc())
		if err != nil {
			// Sin reserva no hay tope que haga cumplir: no se verifica.
			slog.Error("results_attempts_record_failed", "error", err)
			return resultsFallback(resultsUnavailableText(cfg), resultsFallbackAttemptsDB), nil
		}
		if attempt > resultsMaxAttempts {
			return resultsFallback(resultsLockedText(cfg), resultsFallbackLocked), nil
		}

		if patient == nil || patient.BirthDate.Format("2006-01-02") != birthDate.Format("2006-01-02") {
			failed := map[string]interface{}{"attempt": attempt}
			if locked {
				return resultsFallback(resultsLockedText(cfg), resultsFallbackLocked).
					WithEvent("results_verification_failed", failed), nil
			}
			return sm.NewResult(sm.StateResultsAskDocType).
				WithClearCtx(ctxResultsDocType, ctxResultsDoc, ctxResultsDocHash).
				WithText(resultsMismatchText+docTypeMenuText()).
				WithEvent("results_verification_failed", failed), nil
		}

		// La verificación vale aunque falle la limpieza: el contador queda hasta que venza su ventana.
		if err := attempts.Reset(ctx, docHash); err != nil {
			slog.Warn("results_attempts_reset_failed", "error", err)
		}
		if err := attempts.Release(ctx, phone, nowFunc()); err != nil {
			slog.Warn("results_attempts_release_failed", "error", err)
		}
		return sm.NewResult(sm.StateResultsLookup).
			WithContext(ctxResultsFirstName, patient.FirstName).
			WithContext(ctxResultsFirstSurname, patient.FirstSurname).
			WithEvent("results_verified", nil), nil
	}
}

// resultsMissingDoc dice si a la sesión le falta el documento ya pedido (tipo, número o hash):
// p. ej. un retorno del agente que la dejó en un paso posterior sin esos datos. Sin ellos no se
// verifica ni se consulta nada.
func resultsMissingDoc(sess *session.Session) bool {
	return sess.GetContext(ctxResultsDocType) == "" || sess.GetContext(ctxResultsDoc) == "" ||
		sess.GetContext(ctxResultsDocHash) == ""
}

// resultsRestart vuelve a pedir el documento desde el tipo, sin contar intento.
func resultsRestart() *sm.StateResult {
	return sm.NewResult(sm.StateResultsAskDocType).
		WithClearCtx(ctxResultsDocType, ctxResultsDoc, ctxResultsDocHash).
		WithText(docTypeMenuText())
}

// RESULTS_LOOKUP (automático) — consulta la API de resultados con el documento verificado y deja
// solo los estudios cuyo nombre coincide con el paciente de SIESA (el número puede ser de otra
// persona con otro tipo de documento). Un error de la API cuenta como «sin resultados». Ninguno →
// cierra; uno → se envía directo; varios → lista. El número deja el contexto aquí.
func resultsLookupHandler(cfg *config.Config, searcher resultsSearcher) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		if resultsMissingDoc(sess) {
			return resultsRestart(), nil
		}
		found, err := searcher.Search(ctx, sess.GetContext(ctxResultsDoc))
		if err != nil {
			slog.Warn("results_search_failed", "error", err)
			found = nil
		}
		if len(found) == 0 {
			return resultsNoneResult(cfg, "results_none"), nil
		}
		firstName, firstSurname := sess.GetContext(ctxResultsFirstName), sess.GetContext(ctxResultsFirstSurname)
		var matching []results.Study
		for _, st := range results.SortAndDedupe(found) {
			if results.NameMatches(firstName, firstSurname, st.PatientName) {
				matching = append(matching, st)
			}
		}
		if len(matching) == 0 {
			return resultsNoneResult(cfg, "results_name_mismatch"), nil
		}

		listed := matching[:min(len(matching), resultsListMax)]
		raw, err := json.Marshal(listed)
		if err != nil {
			return nil, err
		}
		if len(listed) == 1 {
			return sm.NewResult(sm.StateResultsSend).
				WithClearCtx(ctxResultsDoc).
				WithContext(ctxResultsStudies, string(raw)).
				WithContext(ctxResultsChoice, "0"), nil
		}
		r := resultsListResult(listed).
			WithClearCtx(ctxResultsDoc).
			WithContext(ctxResultsStudies, string(raw))
		if len(matching) > resultsListMax {
			r.WithText(resultsPortalText(cfg, "Para resultados anteriores ingresa a %s"))
		}
		return r, nil
	}
}

// resultsListMax es el tope de filas de una lista de WhatsApp.
const resultsListMax = 10

// resultsNoneText es el mensaje de n8n cuando no hay resultados (o ninguno es del paciente).
func resultsNoneText(cfg *config.Config) string {
	return "No encontramos resultados para ese documento. 🔍\nPuede que aún no estén disponibles; intenta mañana.\n\n" +
		resultsPortalText(cfg, "También puedes consultarlos en %s")
}

// resultsNoneResult cierra sin resultados. El sin-coincidencia de nombre da el mismo texto que el
// sin-resultados: solo el evento los distingue.
func resultsNoneResult(cfg *config.Config, event string) *sm.StateResult {
	return buildAutoCloseResult(resultsNoneText(cfg)).
		WithClearCtx(resultsFlowKeys...).
		WithEvent(event, nil)
}

// resultsRowID es el id de la fila i (desde 0) de la lista: result_1, result_2…
func resultsRowID(i int) string { return "result_" + strconv.Itoa(i+1) }

// resultsListResult muestra la lista de estudios (ya ordenada, máx. 10) y espera la elección.
func resultsListResult(listed []results.Study) *sm.StateResult {
	rows := make([]sm.ListRow, len(listed))
	for i, st := range listed {
		date := "sin fecha"
		if !st.Date.IsZero() {
			date = st.Date.Format("02/01/2006")
		}
		title := st.StudyType
		if strings.TrimSpace(title) == "" {
			title = "Estudio"
		}
		rows[i] = sm.ListRow{
			ID:          resultsRowID(i),
			Title:       truncate(title, 24),
			Description: truncate("Fecha: "+date+" | Adm: "+st.Admission, 72),
		}
	}
	return sm.NewResult(sm.StateResultsList).
		WithList("Tienes estos resultados disponibles. Elige el que quieres recibir 👇", "Ver resultados",
			sm.ListSection{Title: "Resultados", Rows: rows})
}

// resultsStoredStudies lee los estudios listados del contexto; ilegibles o ausentes, ninguno.
func resultsStoredStudies(sess *session.Session) []results.Study {
	var studies []results.Study
	if json.Unmarshal([]byte(sess.GetContext(ctxResultsStudies)), &studies) != nil {
		return nil
	}
	return studies
}

// RESULTS_LIST (interactivo) — el paciente elige un estudio de la lista.
func resultsListHandler(cfg *config.Config) sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		studies := resultsStoredStudies(sess)
		if len(studies) == 0 {
			// La lista ya no está (no debería pasar): el portal, sin más.
			return resultsSendFallback(cfg, "list_lost"), nil
		}
		// Retorno sin dato del agente: se repite la lista, no el aviso de error.
		if sm.IsAgentResumeTurn(sess) {
			return resultsListResult(studies), nil
		}
		ids := make([]string, len(studies))
		for i := range studies {
			ids[i] = resultsRowID(i)
		}
		retry, selected := sm.ValidateButtonResponse(sess, msg, ids...)
		if retry != nil {
			return retry, nil
		}
		return sm.NewResult(sm.StateResultsSend).
			WithContext(ctxResultsChoice, strconv.Itoa(slices.Index(ids, selected))), nil
	}
}

// resultsSendBudget es el presupuesto del turno para bajar, subir y enviar el PDF: como el OCR
// (defaultAnalyzeTimeout), holgadamente por debajo de processMsgTimeout (2 min), no el plazo
// corto de los envíos.
const resultsSendBudget = 45 * time.Second

// resultsSender envía al paciente por la conversación (bird.Client, que desvía a Néctar).
type resultsSender interface {
	SendTextCtx(ctx context.Context, to, conversationID, text string) (string, error)
	SendDocumentCtx(ctx context.Context, phone, filename string, data []byte, caption string) (string, error)
}

// Textos del envío.
const (
	resultsWaitText = "Un momento, estoy preparando tu resultado… ⏳"
	resultsSentText = "¡Listo! Te envié tu resultado. ✅"
)

func resultsSendFailedText(cfg *config.Config) string {
	return "No pude enviarte el archivo en este momento. 🙏 " +
		resultsPortalText(cfg, "Puedes descargarlo en %s")
}

// resultsSendFallback cierra el envío fallido con el enlace al portal y borra el contexto.
func resultsSendFallback(cfg *config.Config, reason string) *sm.StateResult {
	slog.Warn("results_send_fallback", "reason", reason)
	return buildAutoCloseResult(resultsSendFailedText(cfg)).
		WithClearCtx(resultsFlowKeys...).
		WithEvent("results_fallback_link", map[string]interface{}{"reason": reason})
}

// resultsDownloadReason es el motivo de results_fallback_link de una descarga fallida.
func resultsDownloadReason(err error) string {
	switch {
	case errors.Is(err, results.ErrInvalidFile):
		return "invalid_file"
	case errors.Is(err, results.ErrTooLarge):
		return "pdf_too_large"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "api_down"
	}
}

// resultsUploadReason es el motivo de results_fallback_link de una subida o envío fallido.
func resultsUploadReason(err error) string {
	switch {
	case errors.Is(err, nectar.ErrUnsupportedMedia):
		return "unsupported_media"
	case errors.Is(err, nectar.ErrTooLarge):
		return "attachment_too_large"
	case errors.Is(err, nectar.ErrAttachmentUnavailable):
		return "attachment_unavailable"
	case errors.Is(err, nectar.ErrWindowClosed):
		return "window_closed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "send_failed"
	}
}

// RESULTS_SEND (automático) — avisa que se prepara, baja el PDF a memoria y lo envía como adjunto
// dentro del presupuesto. Cualquier falla cierra con el enlace al portal. En duda (5xx, sin
// respuesta) no se reintenta en el turno. Nunca se registran el nombre, el archivo ni el PDF.
func resultsSendHandler(cfg *config.Config, searcher resultsSearcher, sender resultsSender, budget time.Duration) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		studies := resultsStoredStudies(sess)
		choice, convErr := strconv.Atoi(sess.GetContext(ctxResultsChoice))
		if convErr != nil || choice < 0 || choice >= len(studies) {
			return resultsSendFallback(cfg, "list_lost"), nil
		}
		study := studies[choice]
		firstName := sess.GetContext(ctxResultsFirstName)
		// Defensa: nunca sale un estudio cuyo nombre no es el del paciente verificado.
		if !results.NameMatches(firstName, sess.GetContext(ctxResultsFirstSurname), study.PatientName) {
			return resultsSendFallback(cfg, "name_mismatch"), nil
		}
		if sender == nil {
			return resultsSendFallback(cfg, "send_failed"), nil
		}

		// Directo y antes de la descarga: llega mientras se baja el PDF, no al final del turno.
		if _, err := sender.SendTextCtx(ctx, sess.PhoneNumber, "", resultsWaitText); err != nil {
			slog.Warn("results_wait_text_failed", "error", err)
		}

		sendCtx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		data, err := searcher.DownloadPDF(sendCtx, study.File)
		if err != nil {
			slog.Warn("results_download_failed", "error", err)
			return resultsSendFallback(cfg, resultsDownloadReason(err)), nil
		}
		// Guarda barata: no se sube lo que no es un PDF (Néctar lo rechazaría con 415 tras subirlo).
		if !bytes.HasPrefix(data, []byte("%PDF")) {
			return resultsSendFallback(cfg, "not_pdf"), nil
		}
		if _, err := sender.SendDocumentCtx(sendCtx, sess.PhoneNumber, resultsFilename(study),
			data, resultsCaption(firstName, study.StudyType)); err != nil {
			slog.Warn("results_document_send_failed", "error", err)
			return resultsSendFallback(cfg, resultsUploadReason(err)), nil
		}
		return buildAutoCloseResult(resultsSentText).
			WithClearCtx(resultsFlowKeys...).
			WithEvent("results_sent", map[string]interface{}{"study_type": study.StudyType}), nil
	}
}

// resultsFilenameMax y resultsCaptionMax son los topes del adjunto (contrato del lote 11).
const (
	resultsFilenameMax = 200
	resultsCaptionMax  = 1024
)

// resultsAccents quita las tildes antes de sanear el nombre del archivo.
var resultsAccents = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
	"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N",
)

// resultsFilename es «Resultado-<tipo saneado>-<aaaa-mm-dd>.pdf», máx. 200: el tipo queda en
// letras, números y guiones (cada tramo de otros caracteres es un guion), sin tildes.
func resultsFilename(st results.Study) string {
	var b strings.Builder
	dash := false
	for _, r := range resultsAccents.Replace(st.StudyType) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	kind := strings.TrimRight(b.String(), "-")
	if kind == "" {
		kind = "Estudio"
	}
	date := "sin-fecha"
	if !st.Date.IsZero() {
		date = st.Date.Format("2006-01-02")
	}
	const prefix, ext = "Resultado-", ".pdf"
	// Todo es ASCII: bytes = caracteres.
	if room := resultsFilenameMax - len(prefix) - len("-"+date) - len(ext); len(kind) > room {
		kind = strings.TrimRight(kind[:room], "-")
	}
	return prefix + kind + "-" + date + ext
}

// resultsCaption es «📄 Hola <nombre>, tu resultado de *<tipo>*.», máx. 1024 caracteres: si no
// cabe se recorta el tipo.
func resultsCaption(firstName, studyType string) string {
	head, tail := "📄 Hola "+firstName+", tu resultado de *", "*."
	if room := resultsCaptionMax - utf8.RuneCountInString(head+tail); utf8.RuneCountInString(studyType) > room {
		studyType = truncate(studyType, max(room, 1))
	}
	return head + studyType + tail
}
