package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// Un retorno sin dato del agente de Néctar (`__resume__`) re-muestra el paso sin gastar un
// reintento del paciente; Bird sigue exactamente como antes.

func resumeTurnSess(state string, nectar bool) *session.Session {
	sess := testSess(state)
	if nectar {
		sess.PhoneNumber = "nectar:12"
		sess.SetContext(sm.CtxAgentResumeTurn, "1")
	}
	return sess
}

func resumeMsg() bird.InboundMessage { return textM("__resume__") }

func TestCoverageMenuReturn_MainMenuRePromptsWithoutRetry(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)
	sess := resumeTurnSess(sm.StateMainMenu, true)
	sess.RetryCount = 1

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMainMenu {
		t.Fatalf("NextState = %s", result.NextState)
	}
	if sess.RetryCount != 1 {
		t.Errorf("RetryCount = %d, no debe subir", sess.RetryCount)
	}
	if len(result.Messages) == 0 {
		t.Error("debe re-dibujar el menú")
	}
}

func TestAskClientType_NectarResumeRePromptsWithoutRetry(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskClientType, askClientTypeHandler(&testutil.MockEntityRepo{}))
	sess := resumeTurnSess(sm.StateAskClientType, true)
	sess.RetryCount = 1

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskClientType || sess.RetryCount != 1 {
		t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("debe quedar solo la lista de tipos, hay %d mensajes", len(result.Messages))
	}
	if _, ok := result.Messages[0].(*sm.ListMessage); !ok {
		t.Errorf("quería la lista de tipos de entidad, obtuve %T", result.Messages[0])
	}
}

func TestAskClientType_BirdResumeStillCountsRetry(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskClientType, askClientTypeHandler(&testutil.MockEntityRepo{}))
	sess := resumeTurnSess(sm.StateAskClientType, false)

	if _, err := m.Process(context.Background(), sess, resumeMsg()); err != nil {
		t.Fatal(err)
	}
	if sess.RetryCount != 1 {
		t.Errorf("Bird no cambia: RetryCount = %d, quería 1", sess.RetryCount)
	}
}

func TestAskManualCups_NectarResumeDoesNotSearchTheKeyword(t *testing.T) {
	searched := false
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(_ context.Context, _ string) ([]domain.Procedure, error) {
			searched = true
			return nil, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))
	sess := resumeTurnSess(sm.StateAskManualCups, true)

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if searched {
		t.Fatal("__resume__ se buscó como nombre de procedimiento")
	}
	if result.NextState != sm.StateAskManualCups || sess.RetryCount != 0 {
		t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
	}
	for _, msg := range result.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok && strings.Contains(tm.Text, "No encontramos") {
			t.Errorf("texto de «no encontrado» tras un retorno: %q", tm.Text)
		}
	}
}

func TestAskManualCups_BirdResumeUnchanged(t *testing.T) {
	searched := false
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(_ context.Context, _ string) ([]domain.Procedure, error) {
			searched = true
			return nil, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))

	if _, err := m.Process(context.Background(), resumeTurnSess(sm.StateAskManualCups, false), resumeMsg()); err != nil {
		t.Fatal(err)
	}
	if !searched {
		t.Error("Bird conserva su comportamiento: busca el texto tal cual")
	}
}

func TestRegBirthDate_NectarResumeRePromptsWithoutRetryOrComplaint(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegBirthDate, regBirthDateHandler())
	sess := resumeTurnSess(sm.StateRegBirthDate, true)
	sess.RetryCount = 1

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegBirthDate || sess.RetryCount != 1 {
		t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
	}
	for _, msg := range result.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok && strings.Contains(tm.Text, "No entendí") {
			t.Errorf("queja al paciente que no escribió nada: %q", tm.Text)
		}
	}
}

func firstText(t *testing.T, result *sm.StateResult) string {
	t.Helper()
	for _, msg := range result.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok {
			return tm.Text
		}
	}
	t.Fatalf("el resultado no trae texto: %+v", result.Messages)
	return ""
}

// Los pasos de campo con validación re-preguntan con SU pregunta, sin el «Respuesta no válida».
func TestRegFields_NectarResumeSendsOnlyThePrompt(t *testing.T) {
	const prompt = "Escribe tu *primer apellido*:"
	cases := map[string]sm.StateHandler{
		"obligatorio": regFieldHandler("k", prompt, validateName, sm.StateRegSecondSurname, ""),
		"opcional":    regOptionalFieldHandler("k", prompt, sm.StateRegSecondSurname, ""),
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			m := sm.NewMachine()
			m.Register(sm.StateRegFirstSurname, handler)

			nectar := resumeTurnSess(sm.StateRegFirstSurname, true)
			nectar.RetryCount = 1
			result, err := m.Process(context.Background(), nectar, resumeMsg())
			if err != nil {
				t.Fatal(err)
			}
			if result.NextState != sm.StateRegFirstSurname || nectar.RetryCount != 1 {
				t.Fatalf("next=%s retry=%d", result.NextState, nectar.RetryCount)
			}
			if got := firstText(t, result); got != prompt {
				t.Errorf("texto = %q, quería solo la pregunta", got)
			}

			bird := resumeTurnSess(sm.StateRegFirstSurname, false)
			result, err = m.Process(context.Background(), bird, resumeMsg())
			if err != nil {
				t.Fatal(err)
			}
			if got := firstText(t, result); !strings.HasPrefix(got, "Respuesta no válida.") || bird.RetryCount != 1 {
				t.Errorf("Bird cambió: texto=%q retry=%d", got, bird.RetryCount)
			}
		})
	}
}

// Los pasos que buscan el texto en un catálogo no buscan `__resume__`: re-preguntan sin error
// ni reintento. En Bird buscan como siempre.
func TestSearchSteps_NectarResumeDoesNotSearch(t *testing.T) {
	searched := 0
	muniRepo := &mockMunicipalityRepo{
		searchFn: func(context.Context, string) ([]domain.Municipality, error) {
			searched++
			return nil, nil
		},
	}
	entityRepo := &testutil.MockEntityRepo{
		FindActiveFn: func(context.Context) ([]domain.Entity, error) {
			searched++
			return nil, nil
		},
	}
	steps := []struct {
		name    string
		state   string
		handler sm.StateHandler
	}{
		{"municipio de registro", sm.StateRegMunicipality, regMunicipalityHandler(muniRepo)},
		{"municipio SANITAS", sm.StateConfirmMunicipality, confirmMunicipalityHandler(muniRepo, nil)},
		{"entidad", sm.StateChangeEntity, changeEntityHandler(entityRepo, &testutil.MockPatientRepo{})},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			m := sm.NewMachine()
			m.Register(step.state, step.handler)

			searched = 0
			nectar := resumeTurnSess(step.state, true)
			nectar.RetryCount = 1
			result, err := m.Process(context.Background(), nectar, resumeMsg())
			if err != nil {
				t.Fatal(err)
			}
			if searched != 0 {
				t.Fatal("__resume__ se buscó en el catálogo")
			}
			if result.NextState != step.state || nectar.RetryCount != 1 {
				t.Fatalf("next=%s retry=%d", result.NextState, nectar.RetryCount)
			}
			if txt := firstText(t, result); strings.Contains(txt, "No encontré") {
				t.Errorf("error de búsqueda tras un retorno: %q", txt)
			}

			searched = 0
			if _, err := m.Process(context.Background(), resumeTurnSess(step.state, false), resumeMsg()); err != nil {
				t.Fatal(err)
			}
			if searched != 1 {
				t.Errorf("Bird conserva su comportamiento: búsquedas = %d, quería 1", searched)
			}
		})
	}
}

// «Volver a preguntarle» en un paso de texto: en Néctar el paciente recibe solo la pregunta normal
// del paso (no el aviso de error de algo que no escribió) y no se gasta reintento; en Bird el
// mismo texto inválido sigue dando el aviso de error y cuenta el reintento.
func TestTextSteps_NectarResumeAsksTheStepQuestion(t *testing.T) {
	cases := []struct {
		step, question, birdError string
	}{
		{sm.StateAskDocument, documentNumberQuestion, "Por favor ingresa un número de documento válido"},
		{sm.StateGfrCreatinine, creatinineQuestion, "Valor no válido."},
		{sm.StateGfrExamDate, examDateQuestion, "No entendí la fecha."},
		{sm.StateGfrHeight, heightQuestion, "Estatura no válida."},
		{sm.StateRegPhone, mainPhoneQuestion, "Número no válido."},
		{sm.StateRegEmail, regEmailQuestion, "Email no válido."},
		{sm.StateAskUpdateEmail, contactEmailQuestion, "Email no válido."},
	}
	m := returnMenuMachine()
	for _, c := range cases {
		t.Run(c.step, func(t *testing.T) {
			sess := resumeTurnSess(c.step, true)
			sess.RetryCount = 1
			result, err := m.Process(context.Background(), sess, resumeMsg())
			if err != nil {
				t.Fatal(err)
			}
			if result.NextState != c.step || sess.RetryCount != 1 {
				t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
			}
			if len(result.Messages) != 1 {
				t.Fatalf("quería solo la pregunta, hay %d mensajes", len(result.Messages))
			}
			if tm, ok := result.Messages[0].(*sm.TextMessage); !ok || tm.Text != c.question {
				t.Errorf("Néctar: quería la pregunta del paso %q, obtuve %+v", c.question, result.Messages[0])
			}

			bird := resumeTurnSess(c.step, false)
			result, err = m.Process(context.Background(), bird, textM("xx"))
			if err != nil {
				t.Fatal(err)
			}
			if bird.RetryCount != 1 {
				t.Errorf("Bird: RetryCount = %d, quería 1", bird.RetryCount)
			}
			if tm, ok := result.Messages[0].(*sm.TextMessage); !ok || !strings.HasPrefix(tm.Text, c.birdError) {
				t.Errorf("Bird: quería el aviso %q, obtuve %+v", c.birdError, result.Messages[0])
			}
		})
	}
}
