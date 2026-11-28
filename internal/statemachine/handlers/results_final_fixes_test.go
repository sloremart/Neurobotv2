package handlers

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Fuera de horario, «Resultados» entra al mismo flujo que el menú principal si la función está
// encendida; si no, el enlace de siempre.
func TestOutOfHoursResults_EntersFlowOnlyWhenFeatureOn(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, resultsCfg(), nil)
	r, err := m.Process(context.Background(), nectarResultsSess(sm.StateOutOfHoursMenu), postbackM("ooh_resultados"))
	if err != nil || r.NextState != sm.StateResultsStart {
		t.Fatalf("encendida: NextState = %v (err %v), quiero RESULTS_START", r, err)
	}

	cases := map[string]func(*config.Config, *session.Session){
		"sin URL":        func(c *config.Config, _ *session.Session) { c.ResultsAPIURL = "" },
		"bandera false":  func(c *config.Config, _ *session.Session) { c.ResultsPDFEnabled = false },
		"sesión Bird":    func(_ *config.Config, s *session.Session) { s.PhoneNumber = "+573001234567" },
		"sin clave hash": func(c *config.Config, _ *session.Session) { c.ResultsHashSecret = "" },
	}
	for name, tweak := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := resultsCfg()
			sess := nectarResultsSess(sm.StateOutOfHoursMenu)
			tweak(cfg, sess)
			m := sm.NewMachine()
			RegisterGreetingHandlers(m, cfg, nil)
			r, err := m.Process(context.Background(), sess, postbackM("ooh_resultados"))
			if err != nil {
				t.Fatal(err)
			}
			if r.NextState != sm.StateTerminated || !strings.Contains(extractText(r), cfg.ResultsURL) {
				t.Fatalf("apagada: %s %q, quiero el enlace de siempre", r.NextState, extractText(r))
			}
		})
	}
}

// Una sesión de Bird que quedó en RESULTS_SEND no sube ni envía nada facturable.
func TestResultsSend_BirdSessionSendsNothingAndShowsLink(t *testing.T) {
	rig := newResultsRig()
	sess := nectarResultsSess(sm.StateResultsSend)
	sess.PhoneNumber = "+573001234567"
	sess.Context[ctxResultsStudies] = `[{"PatientName":"JUAN PEREZ","StudyType":"EMG","File":"a.pdf"}]`
	sess.Context[ctxResultsChoice] = "0"
	sess.Context[ctxResultsFirstName] = "Juan"
	sess.Context[ctxResultsFirstSurname] = "Perez"

	r, err := rig.m.Process(context.Background(), sess, bird.InboundMessage{})
	if err != nil {
		t.Fatal(err)
	}
	// El rig registra SHOW_RESULTS: el paso encadena al enlace de siempre y cierra.
	if _, shown := eventData(r, "results_shown"); !shown || !strings.Contains(extractText(r), rig.cfg.ResultsURL) {
		t.Fatalf("quiero el enlace de siempre (results_shown): %s %q", r.NextState, extractText(r))
	}
	if len(rig.sender.texts) != 0 || len(rig.sender.docs) != 0 || len(rig.searcher.files) != 0 {
		t.Fatalf("no debía enviar ni bajar nada: textos=%v docs=%d descargas=%v",
			rig.sender.texts, len(rig.sender.docs), rig.searcher.files)
	}
	for _, k := range resultsFlowKeys {
		if !slices.Contains(r.ClearCtx, k) {
			t.Errorf("no borra %s", k)
		}
	}
}

// Con la función apagada (sin cliente), una sesión huérfana en cualquier paso RESULTS_* se maneja:
// no hay «no handler registered» ni pánico, y cae en el enlace.
func TestResultsFlow_OrphanedSessionsHandledWhenFeatureOff(t *testing.T) {
	cfg := resultsCfg()
	cfg.ResultsAPIURL = ""
	m := sm.NewMachine()
	RegisterResultsFlowHandlers(m, cfg, nil, nil, nil, nil)
	for _, state := range []string{
		sm.StateResultsStart, sm.StateResultsAskDocType, sm.StateResultsAskDocNumber,
		sm.StateResultsAskBirthDate, sm.StateResultsLookup, sm.StateResultsList, sm.StateResultsSend,
	} {
		t.Run(state, func(t *testing.T) {
			msg := textM(resultsTestDoc)
			if state == sm.StateResultsAskBirthDate {
				msg = textM(resultsTestDate)
			}
			r, err := m.Process(context.Background(), nectarResultsSess(state), msg)
			if err != nil {
				t.Fatal(err)
			}
			if r.NextState != sm.StateShowResults {
				t.Fatalf("NextState = %s, quiero SHOW_RESULTS", r.NextState)
			}
		})
	}
}

// Sin RESULTS_URL los textos nuevos no terminan en «…en »: llevan el texto sin enlace de siempre.
func TestResultsTexts_EmptyPortalURLHasNoDanglingLink(t *testing.T) {
	cfg := resultsCfg()
	cfg.ResultsURL = ""
	for name, text := range map[string]string{
		"locked":      resultsLockedText(cfg),
		"unavailable": resultsUnavailableText(cfg),
		"none":        resultsNoneText(cfg),
		"send failed": resultsSendFailedText(cfg),
		"older":       resultsPortalText(cfg, "Para resultados anteriores ingresa a %s"),
	} {
		if strings.HasSuffix(strings.TrimSpace(text), " en") || strings.Contains(text, " en \n") ||
			strings.HasSuffix(text, " en ") || !strings.Contains(text, resultsNoPortalText) {
			t.Errorf("%s: %q", name, text)
		}
	}
	// El bloqueo sigue mencionando SIAU aunque no haya portal.
	if got := resultsLockedText(cfg); !strings.Contains(got, "Atención al usuario (SIAU)") {
		t.Errorf("locked sin URL no menciona SIAU: %q", got)
	}
	// El texto de SHOW_RESULTS sin URL es el mismo que usan los nuevos.
	m := sm.NewMachine()
	RegisterResultsAndLocationHandlers(m, cfg, nil)
	r, err := m.Process(context.Background(), testSess(sm.StateShowResults), bird.InboundMessage{})
	if err != nil || !strings.Contains(extractText(r), resultsNoPortalText) {
		t.Fatalf("SHOW_RESULTS sin URL: %q (err %v)", extractText(r), err)
	}
}

// Los datos personales del flujo no sobreviven a una escalación ni al fin de sesión.
func TestResultsKeys_ClearedOnEscalationExitAndSessionEnd(t *testing.T) {
	r := escalationExit(sm.NewResult(sm.StateEscalated))
	for _, k := range resultsFlowKeys {
		if !slices.Contains(r.ClearCtx, k) {
			t.Errorf("escalationExit no borra %s", k)
		}
	}
	d := escalationDiverted(sm.StateMainMenu, sm.NewResult(sm.StateMainMenu))
	for _, k := range resultsFlowKeys {
		if !slices.Contains(d.ClearCtx, k) {
			t.Errorf("escalationDiverted no borra %s", k)
		}
	}
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)
	res, err := m.Process(context.Background(), testSess(sm.StateTerminated), bird.InboundMessage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range resultsFlowKeys {
		if !slices.Contains(res.ClearCtx, k) {
			t.Errorf("TERMINATED no borra %s", k)
		}
	}
}

// La lista de claves de session (la usa el cierre por inactividad) es la del flujo.
func TestResultsFlowKeys_MatchSessionList(t *testing.T) {
	want := []string{
		ctxResultsDocType, ctxResultsDoc, ctxResultsDocHash, ctxResultsFirstName,
		ctxResultsFirstSurname, ctxResultsStudies, ctxResultsChoice,
	}
	for _, k := range want {
		if !slices.Contains(session.ResultsFlowKeys, k) {
			t.Errorf("falta %s", k)
		}
	}
	if len(session.ResultsFlowKeys) != len(want) {
		t.Errorf("claves: %v", session.ResultsFlowKeys)
	}
}

// Lo que no empieza por %PDF no se sube: cierra con el enlace, motivo not_pdf.
func TestResultsSend_NonPDFIsNotUploaded(t *testing.T) {
	rig := newResultsRig()
	rig.searcher.pdf = []byte("<html>no soy un pdf</html>")
	sess := nectarResultsSess(sm.StateResultsSend)
	sess.Context[ctxResultsStudies] = `[{"PatientName":"JUAN PEREZ","StudyType":"EMG","File":"a.pdf"}]`
	sess.Context[ctxResultsChoice] = "0"
	sess.Context[ctxResultsFirstName] = "Juan"
	sess.Context[ctxResultsFirstSurname] = "Perez"

	r, err := rig.m.Process(context.Background(), sess, bird.InboundMessage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rig.sender.docs) != 0 {
		t.Fatal("subió un archivo que no es PDF")
	}
	data, ok := eventData(r, "results_fallback_link")
	if !ok || data["reason"] != "not_pdf" {
		t.Fatalf("evento: %v %v", data, ok)
	}
}
