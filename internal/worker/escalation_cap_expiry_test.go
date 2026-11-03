package worker

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// cappedSessionProbe arma una sesión ACTIVA en FALLBACK_MENU (con o sin la marca del tope) y cuenta lo
// que el worker hace con ella: renovar la actividad, guardar, procesar y enviar.
type cappedSessionProbe struct {
	pool      *MessageWorkerPool
	sess      *session.Session
	sender    *mockMessageSender
	touches   int
	saves     int
	processed int
}

func newCappedSessionProbe(t *testing.T, capped bool) *cappedSessionProbe {
	t.Helper()
	p := &cappedSessionProbe{sender: &mockMessageSender{}}
	p.sess = &session.Session{
		ID: "s-cap", PhoneNumber: "+573001234567", Status: session.StatusActive,
		ConversationID: "conv1", CurrentState: statemachine.StateFallbackMenu,
		Context: map[string]string{}, ExpiresAt: time.Now().Add(30 * time.Minute),
	}
	if capped {
		p.sess.Context[statemachine.CtxEscalationCapped] = "1"
		p.sess.Context[statemachine.CtxEscalationCount] = "3"
	}
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(context.Context, string) (*session.Session, bool, error) { return p.sess, false, nil }
	sm.touchPatientFn = func(context.Context, *session.Session) error { p.touches++; return nil }
	sm.renewFn = func(context.Context, *session.Session) error { p.touches++; return nil }
	sm.saveFn = func(context.Context, *session.Session, string, map[string]string, []string) error {
		p.saves++
		return nil
	}
	processor := &mockMessageProcessor{
		processFn: func(context.Context, *session.Session, bird.InboundMessage) (*statemachine.StateResult, error) {
			p.processed++
			return &statemachine.StateResult{
				NextState: statemachine.StateFallbackMenu,
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Por favor selecciona una opción:"}},
			}, nil
		},
	}
	p.pool = NewMessageWorkerPool(1, 10)
	p.pool.SetDependencies(sm, p.sender, processor)
	p.pool.SetBotEnabled(true) // con el kill switch la sesión iría directo a ESCALATE_TO_AGENT
	return p
}

func textFrom(id, text string) bird.InboundMessage {
	return bird.InboundMessage{ID: id, Phone: "+573001234567", MessageType: "text", Text: text, ReceivedAt: time.Now()}
}

// Una entrada ignorada por el tope no renueva la expiración ni la última actividad, no guarda nada
// y no envía nada: la sesión vence contando desde lo último que el bot sí atendió.
func TestCappedSession_IgnoredInputKeepsExpiryAndActivity(t *testing.T) {
	p := newCappedSessionProbe(t, true)
	expires := p.sess.ExpiresAt
	for i, text := range []string{"Gracias por escribir", "hola", "asesor"} {
		p.pool.processMessage(context.Background(), textFrom("cap-"+string(rune('a'+i)), text))
	}
	if p.touches != 0 || p.saves != 0 || p.processed != 0 || len(p.sender.sent) != 0 {
		t.Errorf("entrada ignorada: renovaciones=%d guardados=%d procesados=%d envíos=%d, querían 0",
			p.touches, p.saves, p.processed, len(p.sender.sent))
	}
	if !p.sess.ExpiresAt.Equal(expires) {
		t.Errorf("la expiración no cambia: %v → %v", expires, p.sess.ExpiresAt)
	}

	// Un botón del corte sí es una interacción: se atiende y renueva como siempre.
	p.pool.processMessage(context.Background(), bird.InboundMessage{
		ID: "cap-btn", Phone: "+573001234567", MessageType: "postback", IsPostback: true,
		PostbackPayload: "action:end", Text: "action:end", ReceivedAt: time.Now(),
	})
	if p.touches != 1 || p.processed != 1 {
		t.Errorf("el botón del corte se atiende: renovaciones=%d procesados=%d", p.touches, p.processed)
	}
}

// Sin la marca, FALLBACK_MENU renueva y responde como hoy.
func TestFallbackMenu_WithoutCapMark_RenewsActivity(t *testing.T) {
	p := newCappedSessionProbe(t, false)
	p.pool.processMessage(context.Background(), textFrom("nocap-1", "hola"))
	if p.touches != 1 || p.processed != 1 || len(p.sender.sent) != 1 {
		t.Errorf("sesión normal: renovaciones=%d procesados=%d envíos=%d, querían 1/1/1",
			p.touches, p.processed, len(p.sender.sent))
	}
}

// Vencida la sesión cortada, el siguiente mensaje abre una sesión NUEVA: sin marca ni contador, se
// atiende con el saludo como cualquier otra.
func TestCappedSession_AfterExpiry_NewSessionGetsGreeting(t *testing.T) {
	sm := newMockSessionMgmt()
	fresh := &session.Session{
		ID: "s-new", PhoneNumber: "+573001234567", Status: session.StatusActive,
		CurrentState: "CHECK_BUSINESS_HOURS", Context: map[string]string{}, ExpiresAt: time.Now().Add(2 * time.Hour),
	}
	// La sesión cortada ya venció: FindOrCreate (que filtra expires_at) crea otra.
	sm.findOrCreateFn = func(context.Context, string) (*session.Session, bool, error) { return fresh, true, nil }
	sender := &mockMessageSender{}
	var seen *session.Session
	processor := &mockMessageProcessor{
		processFn: func(_ context.Context, s *session.Session, _ bird.InboundMessage) (*statemachine.StateResult, error) {
			seen = s
			return &statemachine.StateResult{
				NextState: statemachine.StateMainMenu,
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "¡Hola! Bienvenido"}},
			}, nil
		},
	}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)
	pool.SetBotEnabled(true)

	pool.processMessage(context.Background(), textFrom("after-ttl", "hola"))

	if seen == nil || seen.ID != "s-new" || seen.GetContext(statemachine.CtxEscalationCapped) != "" ||
		seen.GetContext(statemachine.CtxEscalationCount) != "" {
		t.Fatalf("el mensaje lo atiende la sesión nueva, sin tope: %+v", seen)
	}
	if len(sender.sent) != 1 || sender.sent[0].text != "¡Hola! Bienvenido" {
		t.Errorf("la sesión nueva recibe el saludo, got %+v", sender.sent)
	}
}
