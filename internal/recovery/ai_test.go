package recovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// llmReplying es un OpenAI falso que contesta siempre con esa decisión.
func llmReplying(t *testing.T, dec Decision) *LLMClient {
	t.Helper()
	content, _ := json.Marshal(dec)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": string(content)}}},
		})
	}))
	t.Cleanup(srv.Close)
	c := NewLLMClient("clave-de-prueba", DefaultModel, DefaultMaxOutputTokens)
	c.apiURL = srv.URL
	return c
}

// Un mensaje aclaratorio normal del modelo llega al paciente tal cual.
func TestAIRecovery_PlainClarifyMessagePasses(t *testing.T) {
	const msg = "Entiendo. Para seguir necesito tu *primer nombre*, por ejemplo: Juan. ¿Me lo escribes?"
	res := NewAIRecovery(llmReplying(t, Decision{Msg: msg, Reason: "ambiguous"})).
		Try(context.Background(), Request{BlockedState: "REG_FIRST_NAME", Input: "no se"})
	if !res.Handled || res.Message != msg {
		t.Fatalf("el texto normal pasa: %+v", res)
	}
}

// B-13: el texto libre del modelo no llega al paciente con un enlace, un teléfono o un número largo,
// un correo, ni si es demasiado largo. Sin mensaje, el coordinador usa el texto fijo de la escalación.
func TestAIRecovery_ClarifyMessageIsSanitised(t *testing.T) {
	cases := map[string]string{
		"enlace":         "Puedes pagar en https://pagos.example/neuro y luego me escribes.",
		"www":            "Revisa www.example.com para ver los requisitos.",
		"dominio":        "Entra a pagos-neuro.com y paga ahí.",
		"telefono":       "Llámanos al 300 123 4567 para ayudarte.",
		"numero_largo":   "Tu radicado es 98765432, guárdalo.",
		"fijo_con_guion": "Comunícate al 601-555-1234.",
		"correo":         "Escríbenos a citas@example.com con tus datos.",
		"largo":          strings.Repeat("Necesito que me digas tu nombre. ", 20),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			ai := NewAIRecovery(llmReplying(t, Decision{Msg: msg, Reason: "ambiguous"}))
			if res := ai.Try(context.Background(), Request{BlockedState: "REG_FIRST_NAME", Input: "x"}); res.Message != "" {
				t.Fatalf("el texto no pasa el filtro: %q", res.Message)
			}

			m := sm.NewMachine()
			m.RegisterWithConfig("REG_FIRST_NAME", sm.HandlerConfig{
				InputType:    sm.InputText,
				TextValidate: func(string) bool { return false },
				AIRecovery:   true,
				Handler: func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
					return sm.NewResult("REG_SECOND_NAME"), nil
				},
			})
			coord := NewCoordinator(m, ai, Config{Enabled: true, MaxPatientAttempts: 3})
			sess := newRecoverySession("REG_FIRST_NAME")
			sess.SetContext(CtxRecoveryActive, "REG_FIRST_NAME")
			out, handled := coord.Handle(context.Background(), sess, bird.InboundMessage{
				ID: "msg-1", Phone: sess.PhoneNumber, MessageType: "text", Text: "x", ReceivedAt: time.Now(),
			})
			if !handled || out == nil || out.NextState != sm.StateEscalateToAgent {
				t.Fatalf("sin texto seguro se escala: %+v", out)
			}
			if len(out.Messages) == 0 {
				t.Fatal("sin mensaje al paciente")
			}
			if txt, ok := out.Messages[0].(*sm.TextMessage); !ok || txt.Text != sm.EscalationNoticeText {
				t.Errorf("el paciente recibe el texto fijo de la escalación: %+v", out.Messages[0])
			}
		})
	}
}

// Las fechas, cantidades y opciones cortas que el modelo usa para orientar no son números largos.
func TestSafeClarifyText_AllowsDatesAndShortNumbers(t *testing.T) {
	for _, msg := range []string{
		"Escribe tu fecha de nacimiento así: 1990-05-12.",
		"Por ejemplo 15/03/1990.",
		"Tu estatura en centímetros, por ejemplo 170 (1.70 m).",
		"Responde con el número de la opción (ej.: *1* = Cédula de Ciudadanía).",
		"El valor de creatinina, por ejemplo 0.9 mg/dL.",
	} {
		if !safeClarifyText(msg) {
			t.Errorf("debería pasar: %q", msg)
		}
	}
}
