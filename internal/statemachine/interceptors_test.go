package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

func newSess(state string) *session.Session {
	return &session.Session{
		ID:           "sess-1",
		PhoneNumber:  "+573001234567",
		CurrentState: state,
		Status:       session.StatusActive,
		Context:      make(map[string]string),
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
}

func textMsg(text string) bird.InboundMessage {
	return bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", Text: text, ReceivedAt: time.Now(),
	}
}

func postbackMsg(payload string) bird.InboundMessage {
	return bird.InboundMessage{
		ID: "msg-pb-1", Phone: "+573001234567",
		MessageType: "text", IsPostback: true, PostbackPayload: payload,
		ReceivedAt: time.Now(),
	}
}

func typedMsg(msgType string) bird.InboundMessage {
	return bird.InboundMessage{
		ID: "msg-t-1", Phone: "+573001234567",
		MessageType: msgType, ReceivedAt: time.Now(),
	}
}

// ==================== MenuResetInterceptor ====================

func TestMenuReset_Keywords(t *testing.T) {
	interceptor := MenuResetInterceptor()
	ctx := context.Background()

	keywords := []string{"menu", "menú", "inicio", "reiniciar", "0"}
	for _, kw := range keywords {
		t.Run(kw, func(t *testing.T) {
			sess := newSess(StateAskDocument)
			result, intercepted := interceptor(ctx, sess, textMsg(kw))
			if !intercepted {
				t.Fatal("expected interception")
			}
			if result.NextState != StateGreeting {
				t.Errorf("expected GREETING, got %s", result.NextState)
			}
			if len(result.ClearCtx) == 0 || result.ClearCtx[0] != "__all__" {
				t.Error("expected ClearCtx __all__")
			}
		})
	}
}

func TestMenuReset_UpperCase(t *testing.T) {
	interceptor := MenuResetInterceptor()
	sess := newSess(StateAskDocument)
	// Keywords are compared lowercase; "MENU" should NOT match because input is lowercased
	// Actually the interceptor does: strings.TrimSpace(strings.ToLower(msg.Text))
	result, intercepted := interceptor(context.Background(), sess, textMsg("MENU"))
	if !intercepted {
		t.Fatal("expected uppercase MENU to be intercepted (lowered to menu)")
	}
	if result.NextState != StateGreeting {
		t.Errorf("expected GREETING, got %s", result.NextState)
	}
}

func TestMenuReset_NotInTerminated(t *testing.T) {
	interceptor := MenuResetInterceptor()
	ctx := context.Background()

	for _, state := range []string{StateTerminated, StateEscalated} {
		t.Run(state, func(t *testing.T) {
			sess := newSess(state)
			_, intercepted := interceptor(ctx, sess, textMsg("menu"))
			if intercepted {
				t.Errorf("should NOT intercept in %s", state)
			}
		})
	}
}

func TestMenuReset_PostbackIgnored(t *testing.T) {
	interceptor := MenuResetInterceptor()
	sess := newSess(StateAskDocument)
	_, intercepted := interceptor(context.Background(), sess, postbackMsg("menu"))
	if intercepted {
		t.Error("should NOT intercept postback even if payload is 'menu'")
	}
}

func TestMenuReset_PartialMatch(t *testing.T) {
	interceptor := MenuResetInterceptor()
	sess := newSess(StateAskDocument)

	nonKeywords := []string{"menuitem", "menudo", "inicial", "00", "menu principal"}
	for _, input := range nonKeywords {
		t.Run(input, func(t *testing.T) {
			_, intercepted := interceptor(context.Background(), sess, textMsg(input))
			if intercepted {
				t.Errorf("should NOT intercept partial match %q", input)
			}
		})
	}
}

func TestMenuReset_EventData(t *testing.T) {
	interceptor := MenuResetInterceptor()
	sess := newSess(StateAskDocument)
	result, intercepted := interceptor(context.Background(), sess, textMsg("inicio"))
	if !intercepted {
		t.Fatal("expected interception")
	}
	if len(result.Events) == 0 {
		t.Fatal("expected event")
	}
	ev := result.Events[0]
	if ev.Type != "menu_reset" {
		t.Errorf("expected menu_reset event, got %s", ev.Type)
	}
	if ev.Data["from_state"] != StateAskDocument {
		t.Errorf("expected from_state=ASK_DOCUMENT, got %v", ev.Data["from_state"])
	}
}

// ==================== UnsupportedMessageInterceptor ====================

func TestUnsupported_UnsupportedTypes(t *testing.T) {
	interceptor := UnsupportedMessageInterceptor()
	ctx := context.Background()

	unsupported := []string{"audio", "video", "location", "contact", "sticker"}
	for _, typ := range unsupported {
		t.Run(typ, func(t *testing.T) {
			sess := newSess(StateMainMenu)
			result, intercepted := interceptor(ctx, sess, typedMsg(typ))
			if !intercepted {
				t.Fatalf("expected interception for %s", typ)
			}
			if result.NextState != StateMainMenu {
				t.Errorf("expected state to stay %s, got %s", StateMainMenu, result.NextState)
			}
			if len(result.Messages) == 0 {
				t.Error("expected error message")
			}
		})
	}
}

func TestUnsupported_TextPasses(t *testing.T) {
	interceptor := UnsupportedMessageInterceptor()
	_, intercepted := interceptor(context.Background(), newSess(StateMainMenu), textMsg("hola"))
	if intercepted {
		t.Error("text should NOT be intercepted")
	}
}

func TestUnsupported_ImagePasses(t *testing.T) {
	interceptor := UnsupportedMessageInterceptor()
	_, intercepted := interceptor(context.Background(), newSess(StateMainMenu), typedMsg("image"))
	if intercepted {
		t.Error("image should NOT be intercepted by UnsupportedMessage (handled by ImageOutOfContext)")
	}
}

// ==================== ImageOutOfContextInterceptor ====================

func TestImageOutOfContext_InUpload(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()
	sess := newSess(StateUploadMedicalOrder)
	_, intercepted := interceptor(context.Background(), sess, typedMsg("image"))
	if intercepted {
		t.Error("image in UPLOAD_MEDICAL_ORDER should NOT be intercepted")
	}
}

// TestImageOutOfContext_InEmgUpload: la 2ª orden EMG (consolidación Fisiatría) se sube como foto en
// UPLOAD_EMG_ORDER. El interceptor NO debe rechazarla (antes lo hacía → paciente atascado, evento
// image_out_of_context{state:UPLOAD_EMG_ORDER}).
func TestImageOutOfContext_InEmgUpload(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()
	for _, typ := range []string{"image", "document"} {
		t.Run(typ, func(t *testing.T) {
			sess := newSess(StateUploadEmgOrder)
			_, intercepted := interceptor(context.Background(), sess, typedMsg(typ))
			if intercepted {
				t.Errorf("%s en UPLOAD_EMG_ORDER NO debe interceptarse (es la 2ª orden EMG)", typ)
			}
		})
	}
}

func TestImageOutOfContext_OutsideUpload(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()
	// MAIN_MENU se excluye a propósito: una foto ahí = intención de agendar, la maneja
	// PhotoIntentInterceptor (ver TestImageOutOfContext_MainMenuPassesThrough).
	states := []string{StateAskDocument, StateConfirmIdentity}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			sess := newSess(state)
			result, intercepted := interceptor(context.Background(), sess, typedMsg("image"))
			if !intercepted {
				t.Fatalf("expected interception in %s", state)
			}
			if result.NextState != state {
				t.Errorf("expected state to stay %s, got %s", state, result.NextState)
			}
		})
	}
}

// MAIN_MENU debe DEJARSE PASAR por ImageOutOfContext (para que PhotoIntentInterceptor arranque el flujo
// de agendar en vez del dead-end "No esperaba una imagen"). Ver §3.6.
func TestImageOutOfContext_MainMenuPassesThrough(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()
	for _, typ := range []string{"image", "document"} {
		t.Run(typ, func(t *testing.T) {
			sess := newSess(StateMainMenu)
			if _, intercepted := interceptor(context.Background(), sess, typedMsg(typ)); intercepted {
				t.Errorf("%s en MAIN_MENU NO debe interceptarse por ImageOutOfContext (lo maneja PhotoIntent)", typ)
			}
		})
	}
}

func TestImageOutOfContext_TextIgnored(t *testing.T) {
	interceptor := ImageOutOfContextInterceptor()
	_, intercepted := interceptor(context.Background(), newSess(StateAskDocument), textMsg("hello"))
	if intercepted {
		t.Error("text should NOT be intercepted by ImageOutOfContext")
	}
}

// ==================== RegisterInterceptors ====================

func TestRegisterInterceptors_Count(t *testing.T) {
	m := NewMachine()
	RegisterInterceptors(m)
	if len(m.interceptors) != 5 {
		t.Errorf("expected 5 interceptors, got %d", len(m.interceptors))
	}
}
