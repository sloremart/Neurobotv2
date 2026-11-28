package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
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
// salir del flujo.
const (
	ctxResultsDocType      = "results_doc_type"
	ctxResultsDoc          = "results_doc"
	ctxResultsDocHash      = "results_doc_hash"
	ctxResultsAttempts     = "results_attempts"
	ctxResultsFirstName    = "results_first_name"
	ctxResultsFirstSurname = "results_first_surname"
	ctxResultsStudies      = "results_studies"
)

// resultsFlowKeys son todas las claves del flujo: se borran al salir de él.
var resultsFlowKeys = []string{
	ctxResultsDocType, ctxResultsDoc, ctxResultsDocHash, ctxResultsAttempts,
	ctxResultsFirstName, ctxResultsFirstSurname, ctxResultsStudies,
}

// resultsSearcher es la API de resultados (results.Client).
type resultsSearcher interface {
	Search(ctx context.Context, documentNumber string) ([]results.Study, error)
	DownloadPDF(ctx context.Context, file string) ([]byte, error)
}

// resultsAttempts es el tope persistente de intentos (local.ResultsAttemptsRepo).
type resultsAttempts interface {
	IsLocked(ctx context.Context, docHash, phone string, now time.Time) (bool, error)
	RecordFailure(ctx context.Context, docHash, phone string, now time.Time) (bool, error)
	Reset(ctx context.Context, docHash string) error
}

// resultsFeatureOn dice si «Resultados de estudios» entra a la verificación (y luego al PDF): hay
// API, Néctar ya tiene los adjuntos y la conversación es de Néctar. Si no, el enlace de siempre.
func resultsFeatureOn(cfg *config.Config, sess *session.Session) bool {
	return cfg != nil && cfg.ResultsAPIURL != "" && cfg.ResultsPDFEnabled && session.IsNectarSession(sess.PhoneNumber)
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
	return "Por seguridad, la consulta está bloqueada por hoy. Puedes consultar tus resultados en " +
		cfg.ResultsURL + " o escribir *menú* y elegir Atención al usuario (SIAU)."
}

func resultsUnavailableText(cfg *config.Config) string {
	return "No pude verificar tus datos en este momento. 🙏 Puedes consultar tus resultados en " + cfg.ResultsURL
}

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

// RegisterResultsFlowHandlers registra la verificación de «Resultados de estudios». patients nil
// se trata como SIESA caída (enlace al portal).
func RegisterResultsFlowHandlers(m *sm.Machine, cfg *config.Config, patients repository.PatientRepository,
	searcher resultsSearcher, attempts resultsAttempts,
) {
	m.Register(sm.StateResultsStart, resultsStartHandler(cfg, attempts))

	m.Register(sm.StateResultsAskDocType, resultsDocTypeHandler())
	m.RegisterWithConfig(sm.StateResultsAskDocNumber, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: validators.Document,
		ErrorMsg:     "Por favor ingresa un número de documento válido (solo números, entre 5 y 15 dígitos).",
		Handler:      resultsDocNumberHandler(cfg, attempts),
	})
	m.Register(sm.StateResultsAskBirthDate, resultsBirthDateHandler(cfg, patients, attempts))
	// Pasos escalables: el agente reanuda (el paso vuelve a preguntar) o vuelve al menú.
	for _, step := range []string{sm.StateResultsAskDocType, sm.StateResultsAskDocNumber, sm.StateResultsAskBirthDate} {
		sm.RegisterResumeLabel(step, "Reanudar")
		sm.RegisterStaticReturnMenu(step, resultsBackToMenu())
	}

	m.Register(sm.StateResultsLookup, resultsLookupHandler(searcher))
	m.Register(sm.StateResultsList, resultsListPlaceholderHandler(cfg))
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
		docHash := local.HashDocument(sess.GetContext(ctxResultsDocType), doc)
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
// Orden (Ruling 2): 1) SIESA, que si falla no gasta intento; 2) se RESERVA el intento
// (RecordFailure) y, si con él queda bloqueado, no se verifica; 3) se compara; 4) si coincide,
// Reset del documento. El paciente inexistente y la fecha distinta dan el mismo mensaje.
func resultsBirthDateHandler(cfg *config.Config, patients repository.PatientRepository, attempts resultsAttempts) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
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

		attempt := 1
		if n, convErr := strconv.Atoi(sess.GetContext(ctxResultsAttempts)); convErr == nil {
			attempt = n + 1
		}
		locked, err := attempts.RecordFailure(ctx, docHash, resultsPhone(sess), nowFunc())
		if err != nil {
			// Sin reserva no hay tope que haga cumplir: no se verifica.
			slog.Error("results_attempts_record_failed", "error", err)
			return resultsFallback(resultsUnavailableText(cfg), resultsFallbackAttemptsDB), nil
		}
		if locked {
			return resultsFallback(resultsLockedText(cfg), resultsFallbackLocked).
				WithEvent("results_verification_failed", map[string]interface{}{"attempt": attempt}), nil
		}

		if patient == nil || patient.BirthDate.Format("2006-01-02") != birthDate.Format("2006-01-02") {
			return sm.NewResult(sm.StateResultsAskDocType).
				WithClearCtx(ctxResultsDocType, ctxResultsDoc, ctxResultsDocHash).
				WithContext(ctxResultsAttempts, strconv.Itoa(attempt)).
				WithText(resultsMismatchText+docTypeMenuText()).
				WithEvent("results_verification_failed", map[string]interface{}{"attempt": attempt}), nil
		}

		if err := attempts.Reset(ctx, docHash); err != nil {
			// La verificación vale; el contador del documento queda hasta que venza su ventana.
			slog.Warn("results_attempts_reset_failed", "error", err)
		}
		return sm.NewResult(sm.StateResultsLookup).
			WithClearCtx(ctxResultsAttempts).
			WithContext(ctxResultsFirstName, patient.FirstName).
			WithContext(ctxResultsFirstSurname, patient.FirstSurname).
			WithEvent("results_verified", nil), nil
	}
}

// RESULTS_LOOKUP (automático) — consulta la API de resultados con el documento verificado. Un
// error cuenta como «sin resultados» (lista vacía). El número deja el contexto aquí.
func resultsLookupHandler(searcher resultsSearcher) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		studies, err := searcher.Search(ctx, sess.GetContext(ctxResultsDoc))
		if err != nil {
			slog.Warn("results_search_failed", "error", err)
			studies = nil
		}
		if studies == nil {
			studies = []results.Study{}
		}
		raw, err := json.Marshal(studies)
		if err != nil {
			return nil, err
		}
		return sm.NewResult(sm.StateResultsList).
			WithClearCtx(ctxResultsDoc).
			WithContext(ctxResultsStudies, string(raw)), nil
	}
}

// RESULTS_LIST — PROVISIONAL hasta la Tarea 7 (lista y envío del PDF): manda el enlace al portal
// y cierra, borrando el contexto del flujo.
func resultsListPlaceholderHandler(cfg *config.Config) sm.StateHandler {
	return func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		return buildAutoCloseResult("Puedes consultar tus resultados en " + cfg.ResultsURL).
			WithClearCtx(resultsFlowKeys...), nil
	}
}
