package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/tracking"
)

// --- Mock SessionManagement ---

type mockSessionMgmt struct {
	mutex             *session.PhoneMutex
	findOrCreateFn    func(ctx context.Context, phone string) (*session.Session, bool, error)
	findForAgentCmdFn func(ctx context.Context, phone string) (*session.Session, error)
	renewFn           func(ctx context.Context, sess *session.Session) error
	touchPatientFn    func(ctx context.Context, sess *session.Session) error
	touchAgentFn      func(ctx context.Context, phone string) error
	saveFn            func(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error
	clearAllFn        func(ctx context.Context, sess *session.Session) error
	escalateFn        func(ctx context.Context, sess *session.Session, teamID string) error
	resumeFn          func(ctx context.Context, sess *session.Session, targetState string) error
	resumeNoShowFn    func(ctx context.Context, sess *session.Session, targetState string) error
	completeFn        func(ctx context.Context, sess *session.Session) error
}

func newMockSessionMgmt() *mockSessionMgmt {
	return &mockSessionMgmt{mutex: session.NewPhoneMutex()}
}

func (m *mockSessionMgmt) PhoneMutex() *session.PhoneMutex { return m.mutex }
func (m *mockSessionMgmt) FindOrCreate(ctx context.Context, phone string) (*session.Session, bool, error) {
	if m.findOrCreateFn != nil {
		return m.findOrCreateFn(ctx, phone)
	}
	return &session.Session{ID: "sess-test", PhoneNumber: phone, CurrentState: "GREETING", Context: map[string]string{}}, true, nil
}

func (m *mockSessionMgmt) FindForAgentCommand(ctx context.Context, phone string) (*session.Session, error) {
	if m.findForAgentCmdFn != nil {
		return m.findForAgentCmdFn(ctx, phone)
	}
	// Fallback: los tests existentes de comandos de agente stubean findOrCreateFn.
	s, _, err := m.FindOrCreate(ctx, phone)
	return s, err
}

func (m *mockSessionMgmt) RenewTimeout(ctx context.Context, sess *session.Session) error {
	if m.renewFn != nil {
		return m.renewFn(ctx, sess)
	}
	return nil
}

func (m *mockSessionMgmt) TouchPatientActivity(ctx context.Context, sess *session.Session) error {
	if m.touchPatientFn != nil {
		return m.touchPatientFn(ctx, sess)
	}
	return nil
}

func (m *mockSessionMgmt) TouchAgentActivity(ctx context.Context, phone string) error {
	if m.touchAgentFn != nil {
		return m.touchAgentFn(ctx, phone)
	}
	return nil
}

func (m *mockSessionMgmt) SaveState(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error {
	if m.saveFn != nil {
		return m.saveFn(ctx, sess, state, updateCtx, clearCtx)
	}
	sess.CurrentState = state
	return nil
}

func (m *mockSessionMgmt) ClearAllContext(ctx context.Context, sess *session.Session) error {
	if m.clearAllFn != nil {
		return m.clearAllFn(ctx, sess)
	}
	sess.Context = make(map[string]string)
	return nil
}

func (m *mockSessionMgmt) Escalate(ctx context.Context, sess *session.Session, teamID string) error {
	if m.escalateFn != nil {
		return m.escalateFn(ctx, sess, teamID)
	}
	sess.Status = session.StatusEscalated
	return nil
}

func (m *mockSessionMgmt) ResumeFromEscalation(ctx context.Context, sess *session.Session, targetState string) error {
	if m.resumeFn != nil {
		return m.resumeFn(ctx, sess, targetState)
	}
	sess.Status = session.StatusActive
	sess.CurrentState = targetState
	return nil
}

func (m *mockSessionMgmt) ResumeFromEscalationNoShow(ctx context.Context, sess *session.Session, targetState string) error {
	if m.resumeNoShowFn != nil {
		return m.resumeNoShowFn(ctx, sess, targetState)
	}
	sess.Status = session.StatusActive
	sess.CurrentState = targetState
	return nil
}

func (m *mockSessionMgmt) Complete(ctx context.Context, sess *session.Session) error {
	if m.completeFn != nil {
		return m.completeFn(ctx, sess)
	}
	sess.Status = session.StatusCompleted
	return nil
}

func (m *mockSessionMgmt) UpdateConversationID(ctx context.Context, phone, conversationID string) error {
	return nil
}

func (m *mockSessionMgmt) SetContext(ctx context.Context, sess *session.Session, key, value string) error {
	sess.SetContext(key, value)
	return nil
}

// --- Mock MessageSender ---

type mockMessageSender struct {
	mu               sync.Mutex
	sent             []sentMsg
	sendErr          error
	cachedConvID     string // returned by GetCachedConversationID
	lookupConvID     string // returned by LookupConversationByPhone
	lookupConvErr    error
	lookupConvCalled bool
}

type sentMsg struct {
	phone   string
	msgType string
	text    string
}

func (m *mockMessageSender) SendText(phone, conversationID, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMsg{phone: phone, msgType: "text", text: text})
	if m.sendErr != nil {
		return "", m.sendErr
	}
	return "msg-sent-1", nil
}

func (m *mockMessageSender) SendButtons(phone, conversationID, text string, buttons []bird.Button) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMsg{phone: phone, msgType: "buttons", text: text})
	if m.sendErr != nil {
		return "", m.sendErr
	}
	return "msg-sent-btn", nil
}

func (m *mockMessageSender) SendList(phone, conversationID, body, title string, sections []bird.ListSection) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMsg{phone: phone, msgType: "list", text: body})
	if m.sendErr != nil {
		return "", m.sendErr
	}
	return "msg-sent-list", nil
}

func (m *mockMessageSender) SendInternalText(conversationID, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMsg{phone: "", msgType: "internal", text: text})
	if m.sendErr != nil {
		return "", m.sendErr
	}
	return "msg-sent-internal", nil
}

func (m *mockMessageSender) UnassignFeedItem(conversationID string, closed bool) error {
	return nil
}

func (m *mockMessageSender) CloseFeedItems(conversationID string) error {
	return nil
}

func (m *mockMessageSender) GetCachedConversationID(phone string) string {
	return m.cachedConvID
}

func (m *mockMessageSender) LookupConversationByPhone(phone string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookupConvCalled = true
	return m.lookupConvID, m.lookupConvErr
}

// --- Mock MessageProcessor ---

type mockMessageProcessor struct {
	processFn func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error)
}

func (m *mockMessageProcessor) Process(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
	if m.processFn != nil {
		return m.processFn(ctx, sess, msg)
	}
	return &statemachine.StateResult{NextState: "GREETING"}, nil
}

// --- Existing Tests ---

func TestWorkerPool_Dedup(t *testing.T) {
	pool := NewMessageWorkerPool(2, 50)

	msg := bird.InboundMessage{
		ID:          "msg-dedup-1",
		Phone:       "+573001234567",
		MessageType: "text",
		Text:        "hello",
		ReceivedAt:  time.Now(),
	}

	ok1 := pool.Enqueue(msg)
	if !ok1 {
		t.Error("first enqueue should succeed")
	}

	ok2 := pool.Enqueue(msg)
	if ok2 {
		t.Error("second enqueue with same ID should be rejected")
	}
}

func TestWorkerPool_QueueStats(t *testing.T) {
	pool := NewMessageWorkerPool(10, 50)

	size, capacity := pool.QueueStats()
	if size != 0 {
		t.Errorf("expected queue size 0, got %d", size)
	}
	if capacity != 50 {
		t.Errorf("expected queue capacity 50, got %d", capacity)
	}
}

func TestWorkerPool_EnqueueSuccess(t *testing.T) {
	pool := NewMessageWorkerPool(2, 10)

	msg := bird.InboundMessage{
		ID:          "msg-enq-1",
		Phone:       "+573001234567",
		MessageType: "text",
		Text:        "test",
		ReceivedAt:  time.Now(),
	}

	ok := pool.Enqueue(msg)
	if !ok {
		t.Error("enqueue should succeed")
	}

	size, _ := pool.QueueStats()
	if size != 1 {
		t.Errorf("expected queue size 1, got %d", size)
	}
}

func TestWorkerPool_OverflowLimit(t *testing.T) {
	pool := NewMessageWorkerPool(1, 1)

	pool.Enqueue(bird.InboundMessage{
		ID:          "msg-fill-1",
		Phone:       "+573001111111",
		MessageType: "text",
		Text:        "fill",
		ReceivedAt:  time.Now(),
	})

	size, capacity := pool.QueueStats()
	if size != 1 {
		t.Errorf("expected queue size 1, got %d", size)
	}
	if capacity != 1 {
		t.Errorf("expected capacity 1, got %d", capacity)
	}
}

func TestWorkerPool_DefaultValues(t *testing.T) {
	pool := NewMessageWorkerPool(0, 0)

	if pool.workers != defaultWorkers {
		t.Errorf("expected default workers %d, got %d", defaultWorkers, pool.workers)
	}

	_, cap := pool.QueueStats()
	if cap != defaultQueueSize {
		t.Errorf("expected default queue size %d, got %d", defaultQueueSize, cap)
	}
}

func TestWorkerPool_GracefulShutdown(t *testing.T) {
	pool := NewMessageWorkerPool(2, 10)

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	time.Sleep(10 * time.Millisecond)

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestWorkerPool_EnqueueVirtual(t *testing.T) {
	pool := NewMessageWorkerPool(2, 10)

	pool.EnqueueVirtual("+573001234567")

	size, _ := pool.QueueStats()
	if size != 1 {
		t.Errorf("expected queue size 1 after EnqueueVirtual, got %d", size)
	}
}

func TestWorkerPool_DedupDifferentIDs(t *testing.T) {
	pool := NewMessageWorkerPool(2, 50)

	for i := 0; i < 5; i++ {
		msg := bird.InboundMessage{
			ID:          fmt.Sprintf("msg-unique-%d", i),
			Phone:       "+573001234567",
			MessageType: "text",
			Text:        "test",
			ReceivedAt:  time.Now(),
		}
		if !pool.Enqueue(msg) {
			t.Errorf("enqueue should succeed for unique msg %d", i)
		}
	}

	size, _ := pool.QueueStats()
	if size != 5 {
		t.Errorf("expected 5 messages in queue, got %d", size)
	}
}

func TestWorkerPool_DedupCleanup(t *testing.T) {
	pool := NewMessageWorkerPool(2, 50)

	msg := bird.InboundMessage{
		ID:          "msg-cleanup-1",
		Phone:       "+573001234567",
		MessageType: "text",
		Text:        "test",
		ReceivedAt:  time.Now(),
	}
	pool.Enqueue(msg)

	pool.recentMessages.Store(msg.ID, time.Now().Add(-10*time.Minute))

	now := time.Now()
	pool.recentMessages.Range(func(key, value interface{}) bool {
		if now.Sub(value.(time.Time)) > dedupTTL {
			pool.recentMessages.Delete(key)
		}
		return true
	})

	_, exists := pool.recentMessages.Load(msg.ID)
	if exists {
		t.Error("expected old dedup entry to be cleaned up")
	}
}

// --- Tests: processMessage with mocks ---

func TestProcessMessage_Success_TextResponse(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Bienvenido!"}},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-pm-1", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.sent))
	}
	if sender.sent[0].text != "Bienvenido!" {
		t.Errorf("expected 'Bienvenido!', got %q", sender.sent[0].text)
	}
}

// TestProcessMessage_DetachesFromShutdownContext (BUG-004): aunque el ctx padre esté CANCELADO
// (apagado/redeploy en pleno vuelo), el procesamiento debe completar — el ctx se desacopla con
// WithoutCancel, así que SaveState/MarkDone/respuesta no se abortan con "context canceled".
func TestProcessMessage_DetachesFromShutdownContext(t *testing.T) {
	var procCtxErr error
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, _ *session.Session, _ bird.InboundMessage) (*statemachine.StateResult, error) {
			procCtxErr = ctx.Err() // debe ser nil pese a que el padre está cancelado
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "ok"}},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simular shutdown: el ctx YA está cancelado al entrar

	pool.processMessage(ctx, bird.InboundMessage{
		ID: "msg-bug004", Phone: "+573001234567", MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	})

	if procCtxErr != nil {
		t.Fatalf("el ctx de procesamiento NO debe estar cancelado pese al padre cancelado, got %v", procCtxErr)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("el mensaje debió procesarse pese al ctx padre cancelado, got %d enviados", len(sender.sent))
	}
}

func TestProcessMessage_StateMachineError_SendsErrorMsg(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return nil, errors.New("handler panic")
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-err-1", Phone: "+573001234567",
		MessageType: "text", Text: "test", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	// Should have sent error message to user
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 error message sent, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "text" {
		t.Errorf("expected text message, got %s", sender.sent[0].msgType)
	}
}

func TestProcessMessage_SessionError_Returns(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return nil, false, errors.New("db error")
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-sess-err", Phone: "+573001234567",
		MessageType: "text", Text: "test", ReceivedAt: time.Now(),
	}

	// Should not panic
	pool.processMessage(context.Background(), msg)

	// Error message should be sent to patient (E1 fix: no silent failures)
	if len(sender.sent) != 1 {
		t.Errorf("expected 1 error message on session error, got %d", len(sender.sent))
	}
	if len(sender.sent) > 0 && !strings.Contains(sender.sent[0].text, "dificultades técnicas") {
		t.Errorf("expected technical error message, got: %s", sender.sent[0].text)
	}
}

func TestProcessMessage_ClearAll(t *testing.T) {
	sm := newMockSessionMgmt()
	clearCalled := false
	sm.clearAllFn = func(ctx context.Context, sess *session.Session) error {
		clearCalled = true
		sess.Context = make(map[string]string)
		return nil
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "GREETING",
				ClearCtx:  []string{"__all__"},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-clear-all", Phone: "+573001234567",
		MessageType: "text", Text: "menu", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if !clearCalled {
		t.Error("expected ClearAllContext to be called")
	}
}

func TestProcessMessage_SaveStateWithContext(t *testing.T) {
	sm := newMockSessionMgmt()
	var savedState string
	var savedCtx map[string]string
	sm.saveFn = func(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error {
		savedState = state
		savedCtx = updateCtx
		return nil
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "ASK_DOCUMENT",
				UpdateCtx: map[string]string{"intent": "agendar"},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-save-ctx", Phone: "+573001234567",
		MessageType: "text", Text: "agendar", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if savedState != "ASK_DOCUMENT" {
		t.Errorf("expected ASK_DOCUMENT, got %s", savedState)
	}
	if savedCtx["intent"] != "agendar" {
		t.Errorf("expected intent=agendar, got %v", savedCtx)
	}
}

func TestProcessMessage_MultipleMessages(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages: []statemachine.OutboundMessage{
					&statemachine.TextMessage{Text: "Hola!"},
					&statemachine.TextMessage{Text: "Cómo te puedo ayudar?"},
				},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-multi", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if len(sender.sent) != 2 {
		t.Fatalf("expected 2 messages sent, got %d", len(sender.sent))
	}
}

func TestProcessMessage_ButtonsMessage(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages: []statemachine.OutboundMessage{
					&statemachine.ButtonMessage{
						Text: "Elige:",
						Buttons: []statemachine.Button{
							{Text: "Agendar", Payload: "agendar"},
							{Text: "Consultar", Payload: "consultar"},
						},
					},
				},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-btn", Phone: "+573001234567",
		MessageType: "text", Text: "test", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "buttons" {
		t.Errorf("expected buttons message, got %s", sender.sent[0].msgType)
	}
}

func TestSendMessage_UnknownType(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10)
	pool.birdClient = &mockMessageSender{}

	// Unknown message type
	_, err := pool.sendMessage("+573001234567", "", &unknownMsg{})
	if err == nil {
		t.Error("expected error for unknown message type")
	}
}

// unknownMsg implements OutboundMessage but isn't Text/Button/List
type unknownMsg struct{}

func (u *unknownMsg) Type() string { return "unknown" }

// --- Mock EventStore for tracker tests ---

type mockEventStoreWorker struct {
	mu             sync.Mutex
	insertedEvents []localrepo.ChatEvent
	batchEvents    []localrepo.ChatEvent
}

func (m *mockEventStoreWorker) Insert(_ context.Context, event *localrepo.ChatEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertedEvents = append(m.insertedEvents, *event)
	return nil
}

func (m *mockEventStoreWorker) InsertBatch(_ context.Context, events []localrepo.ChatEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchEvents = append(m.batchEvents, events...)
	return nil
}

// === New coverage tests ===

func TestProcessMessage_ListMessage(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "SELECT_SLOT",
				Messages: []statemachine.OutboundMessage{
					&statemachine.ListMessage{
						Body:  "Selecciona un horario:",
						Title: "Horarios",
						Sections: []statemachine.ListSection{
							{
								Title: "Manana",
								Rows: []statemachine.ListRow{
									{ID: "slot-1", Title: "8:00 AM", Description: "Dr. Garcia"},
									{ID: "slot-2", Title: "9:00 AM", Description: "Dr. Lopez"},
								},
							},
						},
					},
				},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-list-1", Phone: "+573001234567",
		MessageType: "text", Text: "ver horarios", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "list" {
		t.Errorf("expected list message type, got %s", sender.sent[0].msgType)
	}
	if sender.sent[0].text != "Selecciona un horario:" {
		t.Errorf("expected list body text, got %q", sender.sent[0].text)
	}
}

func TestProcessMessage_TrackerCalls(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages: []statemachine.OutboundMessage{
					&statemachine.TextMessage{Text: "Hola!"},
					&statemachine.TextMessage{Text: "Que deseas hacer?"},
				},
				Events: []statemachine.Event{
					{Type: "greeting_sent", Data: map[string]interface{}{"source": "bot"}},
					{Type: "menu_shown", Data: map[string]interface{}{"options": 3}},
				},
			}, nil
		},
	}

	eventStore := &mockEventStoreWorker{}
	tracker := tracking.NewEventTracker(eventStore)

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)
	pool.SetTracker(tracker)

	msg := bird.InboundMessage{
		ID: "msg-track-1", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	// Verify events: 1 session_started + 2 message_sent = 3 total
	eventStore.mu.Lock()
	defer eventStore.mu.Unlock()
	if len(eventStore.insertedEvents) != 3 {
		t.Errorf("expected 3 events (1 session_started + 2 message_sent), got %d", len(eventStore.insertedEvents))
	}
	if eventStore.insertedEvents[0].EventType != "session_started" {
		t.Errorf("expected session_started as first event, got %s", eventStore.insertedEvents[0].EventType)
	}
	for _, ev := range eventStore.insertedEvents[1:] {
		if ev.EventType != "message_sent" {
			t.Errorf("expected message_sent event type, got %s", ev.EventType)
		}
	}

	// Verify LogBatch called with events (2 events)
	if len(eventStore.batchEvents) != 2 {
		t.Errorf("expected 2 batch events, got %d", len(eventStore.batchEvents))
	}
	if eventStore.batchEvents[0].EventType != "greeting_sent" {
		t.Errorf("expected greeting_sent, got %s", eventStore.batchEvents[0].EventType)
	}
	if eventStore.batchEvents[1].EventType != "menu_shown" {
		t.Errorf("expected menu_shown, got %s", eventStore.batchEvents[1].EventType)
	}
}

func TestProcessMessage_TrackerNoEvents(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "GREETING",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "OK"}},
				// No events
			}, nil
		},
	}

	eventStore := &mockEventStoreWorker{}
	tracker := tracking.NewEventTracker(eventStore)

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)
	pool.SetTracker(tracker)

	msg := bird.InboundMessage{
		ID: "msg-no-events", Phone: "+573001234567",
		MessageType: "text", Text: "test", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	eventStore.mu.Lock()
	defer eventStore.mu.Unlock()
	// Should have 1 session_started + 1 message_sent but 0 batch events
	if len(eventStore.insertedEvents) != 2 {
		t.Errorf("expected 2 events (1 session_started + 1 message_sent), got %d", len(eventStore.insertedEvents))
	}
	if len(eventStore.batchEvents) != 0 {
		t.Errorf("expected 0 batch events, got %d", len(eventStore.batchEvents))
	}
}

func TestProcessMessage_RenewTimeoutError(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.touchPatientFn = func(_ context.Context, _ *session.Session) error {
		return errors.New("redis timeout")
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Bienvenido!"}},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-renew-err", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	// Should not crash — processing continues despite renew error
	pool.processMessage(context.Background(), msg)

	// Message should still be sent
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 message sent despite renew error, got %d", len(sender.sent))
	}
	if sender.sent[0].text != "Bienvenido!" {
		t.Errorf("expected 'Bienvenido!', got %q", sender.sent[0].text)
	}
}

func TestProcessMessage_SaveStateError(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.saveFn = func(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error {
		return errors.New("db write error")
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "ASK_DOCUMENT",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Por favor ingresa tu documento"}},
				UpdateCtx: map[string]string{"intent": "agendar"},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-save-err", Phone: "+573001234567",
		MessageType: "text", Text: "agendar", ReceivedAt: time.Now(),
	}

	// Should not crash — SaveState error is logged but does not prevent processing
	pool.processMessage(context.Background(), msg)

	// Message should still be sent
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 message sent despite save error, got %d", len(sender.sent))
	}
}

func TestEnqueue_OverflowGoroutines(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "GREETING",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "OK"}},
			}, nil
		},
	}

	// Queue size 1, no workers started (so queue stays full)
	pool := NewMessageWorkerPool(1, 1)
	pool.SetDependencies(sm, sender, processor)

	// Fill the queue with one message
	pool.Enqueue(bird.InboundMessage{
		ID: "msg-fill-queue", Phone: "+573009999999",
		MessageType: "text", Text: "fill", ReceivedAt: time.Now(),
	})

	// Queue is now full (size=1, capacity=1)
	size, _ := pool.QueueStats()
	if size != 1 {
		t.Fatalf("expected queue size 1, got %d", size)
	}

	// Enqueue another — should go to overflow goroutine
	ok := pool.Enqueue(bird.InboundMessage{
		ID: "msg-overflow-1", Phone: "+573001234567",
		MessageType: "text", Text: "overflow", ReceivedAt: time.Now(),
	})
	if !ok {
		t.Error("expected overflow enqueue to succeed")
	}

	// Wait for overflow goroutine to complete
	time.Sleep(200 * time.Millisecond)

	// Verify the overflow message was processed (sent a response)
	sender.mu.Lock()
	found := false
	for _, s := range sender.sent {
		if s.phone == "+573001234567" {
			found = true
			break
		}
	}
	sender.mu.Unlock()

	if !found {
		t.Error("expected overflow message to be processed")
	}
}

func TestEnqueue_OverflowLimit(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		// Slow down processing to keep overflow goroutines alive
		time.Sleep(500 * time.Millisecond)
		return &session.Session{ID: "sess-slow", PhoneNumber: phone, CurrentState: "GREETING", Context: map[string]string{}}, true, nil
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{}

	// Queue size 1, no workers started
	pool := NewMessageWorkerPool(1, 1)
	pool.SetDependencies(sm, sender, processor)

	// Fill the queue
	pool.Enqueue(bird.InboundMessage{
		ID: "msg-fill-limit", Phone: "+573009999999",
		MessageType: "text", Text: "fill", ReceivedAt: time.Now(),
	})

	// Enqueue maxOverflowGoroutines messages to overflow
	for i := 0; i < maxOverflowGoroutines; i++ {
		ok := pool.Enqueue(bird.InboundMessage{
			ID:          fmt.Sprintf("msg-overflow-limit-%d", i),
			Phone:       fmt.Sprintf("+5730012345%02d", i),
			MessageType: "text",
			Text:        "overflow",
			ReceivedAt:  time.Now(),
		})
		if !ok {
			t.Errorf("overflow enqueue %d should succeed", i)
		}
	}

	// Now the next one should be dropped (overflow limit reached)
	ok := pool.Enqueue(bird.InboundMessage{
		ID: "msg-over-limit", Phone: "+573008888888",
		MessageType: "text", Text: "dropped", ReceivedAt: time.Now(),
	})
	if ok {
		t.Error("expected enqueue to fail when overflow limit reached")
	}
}

func TestSendMessage_ListMessage(t *testing.T) {
	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.birdClient = sender

	listMsg := &statemachine.ListMessage{
		Body:  "Elige una opcion:",
		Title: "Opciones",
		Sections: []statemachine.ListSection{
			{
				Title: "Procedimientos",
				Rows: []statemachine.ListRow{
					{ID: "proc-1", Title: "EMG", Description: "Electromiografia"},
					{ID: "proc-2", Title: "EEG", Description: "Electroencefalografia"},
				},
			},
			{
				Title: "Otras",
				Rows: []statemachine.ListRow{
					{ID: "other-1", Title: "Cancelar", Description: "Cancelar operacion"},
				},
			},
		},
	}

	msgID, err := pool.sendMessage("+573001234567", "", listMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgID != "msg-sent-list" {
		t.Errorf("expected msg-sent-list, got %s", msgID)
	}

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "list" {
		t.Errorf("expected list type, got %s", sender.sent[0].msgType)
	}
	if sender.sent[0].text != "Elige una opcion:" {
		t.Errorf("expected body text, got %q", sender.sent[0].text)
	}
}

func TestSendMessage_ButtonMessage(t *testing.T) {
	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.birdClient = sender

	btnMsg := &statemachine.ButtonMessage{
		Text: "Que deseas hacer?",
		Buttons: []statemachine.Button{
			{Text: "Agendar", Payload: "agendar"},
			{Text: "Consultar", Payload: "consultar"},
			{Text: "Cancelar", Payload: "cancelar"},
		},
	}

	msgID, err := pool.sendMessage("+573001234567", "", btnMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgID != "msg-sent-btn" {
		t.Errorf("expected msg-sent-btn, got %s", msgID)
	}

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "buttons" {
		t.Errorf("expected buttons, got %s", sender.sent[0].msgType)
	}
}

func TestSendMessage_TextMessage(t *testing.T) {
	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.birdClient = sender

	textMsg := &statemachine.TextMessage{Text: "Hola mundo"}

	msgID, err := pool.sendMessage("+573001234567", "", textMsg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgID != "msg-sent-1" {
		t.Errorf("expected msg-sent-1, got %s", msgID)
	}

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "text" {
		t.Errorf("expected text, got %s", sender.sent[0].msgType)
	}
	if sender.sent[0].text != "Hola mundo" {
		t.Errorf("expected 'Hola mundo', got %q", sender.sent[0].text)
	}
}

func TestSendMessage_SendError(t *testing.T) {
	sender := &mockMessageSender{sendErr: errors.New("network error")}
	pool := NewMessageWorkerPool(1, 10)
	pool.birdClient = sender

	textMsg := &statemachine.TextMessage{Text: "test"}
	_, err := pool.sendMessage("+573001234567", "", textMsg)
	if err == nil {
		t.Error("expected error when sender fails")
	}
}

func TestProcessMessage_SendMessageError_Continues(t *testing.T) {
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{sendErr: errors.New("bird api down")}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages: []statemachine.OutboundMessage{
					&statemachine.TextMessage{Text: "Msg 1"},
					&statemachine.TextMessage{Text: "Msg 2"},
				},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-send-err", Phone: "+573001234567",
		MessageType: "text", Text: "test", ReceivedAt: time.Now(),
	}

	// Should not crash even when send fails — continues to next message
	pool.processMessage(context.Background(), msg)

	// Both messages attempted (both fail, but processing continues)
	if len(sender.sent) != 2 {
		t.Errorf("expected 2 send attempts, got %d", len(sender.sent))
	}
}

func TestProcessMessage_ClearAllContextError(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.clearAllFn = func(ctx context.Context, sess *session.Session) error {
		return errors.New("clear all failed")
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "GREETING",
				ClearCtx:  []string{"__all__"},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-clear-err", Phone: "+573001234567",
		MessageType: "text", Text: "menu", ReceivedAt: time.Now(),
	}

	// Should not crash — error is logged
	pool.processMessage(context.Background(), msg)
}

func TestProcessMessage_NewSession(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-new", PhoneNumber: phone,
			CurrentState: "GREETING",
			Context:      map[string]string{},
		}, true, nil // isNew=true
	}
	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Bienvenido!"}},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-new-sess", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.sent))
	}
}

func TestProcessMessage_LookupConversationByPhone(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-no-conv", PhoneNumber: phone,
			CurrentState: "GREETING",
			Context:      map[string]string{},
			// ConversationID is empty — triggers API lookup
		}, false, nil
	}
	var savedConvID string
	sm.saveFn = func(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error {
		savedConvID = sess.ConversationID
		return nil
	}

	sender := &mockMessageSender{lookupConvID: "conv-from-api-123"}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			// Verify conversationID was populated before state machine runs
			if sess.ConversationID != "conv-from-api-123" {
				t.Errorf("expected conversationID set before Process, got %q", sess.ConversationID)
			}
			return &statemachine.StateResult{
				NextState: "MAIN_MENU",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "OK"}},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-lookup-conv", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if !sender.lookupConvCalled {
		t.Error("expected LookupConversationByPhone to be called")
	}
	if savedConvID != "conv-from-api-123" {
		t.Errorf("expected conversationID persisted via SaveState, got %q", savedConvID)
	}
}

func TestProcessMessage_LookupSkippedWhenCached(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-with-conv", PhoneNumber: phone,
			CurrentState:   "GREETING",
			ConversationID: "conv-already-set",
			Context:        map[string]string{},
		}, false, nil
	}

	// Cache confirms the session's ID — no API lookup needed
	sender := &mockMessageSender{cachedConvID: "conv-already-set"}
	processor := &mockMessageProcessor{}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-skip-lookup", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if sender.lookupConvCalled {
		t.Error("LookupConversationByPhone should NOT be called when cache confirms conversationID")
	}
}

func TestProcessMessage_StaleConvID_RefreshedByLookup(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-stale", PhoneNumber: phone,
			CurrentState:   "GREETING",
			ConversationID: "conv-old-stale",
			Context:        map[string]string{},
		}, false, nil
	}
	var savedConvID string
	sm.saveFn = func(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error {
		savedConvID = sess.ConversationID
		return nil
	}

	// Cache empty (simulates bot restart) — triggers API lookup
	sender := &mockMessageSender{lookupConvID: "conv-fresh-new"}
	processor := &mockMessageProcessor{}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-stale-fix", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if !sender.lookupConvCalled {
		t.Error("expected LookupConversationByPhone when cache is empty (stale ID scenario)")
	}
	if savedConvID != "conv-fresh-new" {
		t.Errorf("expected stale ID refreshed to conv-fresh-new, got %q", savedConvID)
	}
}

func TestProcessMessage_StaleConvID_ClearedWhenNoActiveConv(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-stale-clear", PhoneNumber: phone,
			CurrentState:   "GREETING",
			ConversationID: "conv-very-old",
			Context:        map[string]string{},
		}, false, nil
	}
	var savedConvID string
	sm.saveFn = func(ctx context.Context, sess *session.Session, state string, updateCtx map[string]string, clearCtx []string) error {
		savedConvID = sess.ConversationID
		return nil
	}

	// Cache empty, lookup returns empty (no active conversation for this phone)
	sender := &mockMessageSender{lookupConvID: ""}
	processor := &mockMessageProcessor{}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	msg := bird.InboundMessage{
		ID: "msg-stale-clear", Phone: "+573001234567",
		MessageType: "text", Text: "hola", ReceivedAt: time.Now(),
	}

	pool.processMessage(context.Background(), msg)

	if !sender.lookupConvCalled {
		t.Error("expected LookupConversationByPhone when cache is empty")
	}
	if savedConvID != "conv-very-old" {
		t.Errorf("expected stale conversationID kept for downstream self-heal, got %q", savedConvID)
	}
}

// --- Mock OCRAnalyzer ---

type mockOCRAnalyzer struct {
	result *services.OCRResult
	err    error
}

func (m *mockOCRAnalyzer) AnalyzeText(ctx context.Context, desc string) (*services.OCRResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.result != nil {
		return m.result, nil
	}
	return &services.OCRResult{
		Success: true,
		Cups:    []services.CUPSEntry{{Code: "883141", Name: "Resonancia cerebral simple", Quantity: 1}},
	}, nil
}

// --- Tests: handleAgentOrder ---

func TestHandleAgentOrder_Success(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-orden", PhoneNumber: phone,
			CurrentState:   "ESCALATED",
			ConversationID: "conv-orden",
			Status:         session.StatusEscalated,
			Context:        map[string]string{"pre_escalation_state": "UPLOAD_MEDICAL_ORDER"},
		}, false, nil
	}
	var resumedState string
	sm.resumeFn = func(ctx context.Context, sess *session.Session, targetState string) error {
		resumedState = targetState
		sess.Status = session.StatusActive
		sess.CurrentState = targetState
		return nil
	}

	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "CONFIRM_OCR_RESULT",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Procedimientos detectados"}},
			}, nil
		},
	}
	ocr := &mockOCRAnalyzer{}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)
	pool.SetOCRService(ocr)

	cmd := AgentCommand{
		Action: "orden",
		Data:   "Resonancia cerebral simple codigo 883141 cantidad 1",
		Phone:  "+573001234567",
	}

	pool.processAgentCommand(context.Background(), cmd)

	// Verify resumed at VALIDATE_OCR
	if resumedState != statemachine.StateValidateOCR {
		t.Errorf("expected resume at VALIDATE_OCR, got %q", resumedState)
	}

	// Verify messages sent: internal summary + patient notification + state result
	sender.mu.Lock()
	defer sender.mu.Unlock()

	// Should have at least: 1 internal (summary) + 1 text (patient) + 1 text (state result)
	if len(sender.sent) < 3 {
		t.Fatalf("expected at least 3 messages sent, got %d", len(sender.sent))
	}

	// Check internal summary was sent
	foundInternal := false
	for _, s := range sender.sent {
		if s.msgType == "internal" && s.text != "" {
			foundInternal = true
			break
		}
	}
	if !foundInternal {
		t.Error("expected internal summary message sent to agent")
	}
}

func TestHandleAgentOrder_EmptyData(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-orden-empty", PhoneNumber: phone,
			CurrentState:   "ESCALATED",
			ConversationID: "conv-orden",
			Status:         session.StatusEscalated,
			Context:        map[string]string{},
		}, false, nil
	}

	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{}
	ocr := &mockOCRAnalyzer{}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)
	pool.SetOCRService(ocr)

	cmd := AgentCommand{
		Action: "orden",
		Data:   "", // empty
		Phone:  "+573001234567",
	}

	pool.processAgentCommand(context.Background(), cmd)

	// Should send usage hint as internal message
	sender.mu.Lock()
	defer sender.mu.Unlock()

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 internal usage message, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "internal" {
		t.Errorf("expected internal message, got %s", sender.sent[0].msgType)
	}
}

func TestHandleAgentOrder_OCRError(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-orden-err", PhoneNumber: phone,
			CurrentState:   "ESCALATED",
			ConversationID: "conv-orden",
			Status:         session.StatusEscalated,
			Context:        map[string]string{},
		}, false, nil
	}

	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{}
	ocr := &mockOCRAnalyzer{err: errors.New("openai api timeout")}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)
	pool.SetOCRService(ocr)

	cmd := AgentCommand{
		Action: "orden",
		Data:   "Resonancia cerebral",
		Phone:  "+573001234567",
	}

	pool.processAgentCommand(context.Background(), cmd)

	// Should send error as internal message
	sender.mu.Lock()
	defer sender.mu.Unlock()

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(sender.sent))
	}
	if sender.sent[0].msgType != "internal" {
		t.Errorf("expected internal message, got %s", sender.sent[0].msgType)
	}
}

func TestHandleAgentOrder_OCRNotSuccess(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(ctx context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-orden-fail", PhoneNumber: phone,
			CurrentState:   "ESCALATED",
			ConversationID: "conv-orden",
			Status:         session.StatusEscalated,
			Context:        map[string]string{},
		}, false, nil
	}

	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{}
	ocr := &mockOCRAnalyzer{
		result: &services.OCRResult{Success: false, Error: "no_table_detected"},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)
	pool.SetOCRService(ocr)

	cmd := AgentCommand{
		Action: "orden",
		Data:   "algo ilegible",
		Phone:  "+573001234567",
	}

	pool.processAgentCommand(context.Background(), cmd)

	// Should send error as internal message
	sender.mu.Lock()
	defer sender.mu.Unlock()

	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(sender.sent))
	}
}

// TestTryAddOverflow_RespectsClosing verifica el guard del #5: con el pool abierto registra la
// goroutine de overflow; tras marcar el cierre (como hace Stop antes de wg.Wait) se niega a hacer
// wg.Add, evitando el panic "WaitGroup is reused".
func TestTryAddOverflow_RespectsClosing(t *testing.T) {
	pool := NewMessageWorkerPool(1, 1)

	if !pool.tryAddOverflow() {
		t.Fatal("pool abierto: tryAddOverflow debería registrar (true)")
	}
	pool.wg.Done()              // deshacer el Add del caso abierto
	pool.activeOverflow.Add(-1) // y su contador

	pool.closeMu.Lock()
	pool.closing = true
	pool.closeMu.Unlock()

	if pool.tryAddOverflow() {
		t.Fatal("pool cerrando: tryAddOverflow debería rechazar (false)")
	}
	if got := pool.activeOverflow.Load(); got != 0 {
		t.Fatalf("overflow no debió incrementarse al cerrar, got %d", got)
	}
}

// TestEnqueue_DropsOverflowWhenClosing: con la cola llena y el pool cerrando, Enqueue descarta el
// mensaje (no lanza goroutine) y LIBERA el claim de dedup para que el replay por WAL pueda re-encolarlo.
func TestEnqueue_DropsOverflowWhenClosing(t *testing.T) {
	pool := NewMessageWorkerPool(1, 1)
	pool.queue <- bird.InboundMessage{ID: "fill"} // llenar el único slot (sin Start, nada consume)

	pool.closeMu.Lock()
	pool.closing = true
	pool.closeMu.Unlock()

	ok := pool.Enqueue(bird.InboundMessage{
		ID: "drop-me", Phone: "+573001234567", MessageType: "text", ReceivedAt: time.Now(),
	})
	if ok {
		t.Fatal("esperaba Enqueue=false (cola llena + cerrando)")
	}
	if got := pool.activeOverflow.Load(); got != 0 {
		t.Fatalf("no debió lanzarse goroutine de overflow, activeOverflow=%d", got)
	}
	if _, seen := pool.recentMessages.Load("drop-me"); seen {
		t.Fatal("el claim de dedup debió liberarse al descartar (para re-encolar por WAL)")
	}
}

// ─── Acuse durante escalación (§8.1 #6) ───────────────────────────────────────

var errTest = errors.New("boom")

type fakeAgentChecker struct {
	responded bool
	err       error
}

func (f *fakeAgentChecker) HasAgentResponded(_ context.Context, _ string) (bool, error) {
	return f.responded, f.err
}

// El acuse se envía SOLO en el primer mensaje, con checker sano y agente sin responder;
// en cualquier otra condición (flag puesto, agente ya respondió, error, sin checker) = silencio.
func TestShouldSendEscalationAck(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		checker AgentActivityChecker
		ackSent bool
		want    bool
	}{
		{"primer mensaje, agente sin responder → envía", &fakeAgentChecker{responded: false}, false, true},
		{"flag ya puesto → silencio", &fakeAgentChecker{responded: false}, true, false},
		{"agente YA respondió → silencio absoluto", &fakeAgentChecker{responded: true}, false, false},
		{"error del chequeo → fail-quiet", &fakeAgentChecker{err: errTest}, false, false},
		{"sin checker cableado → fail-quiet", nil, false, false},
	}
	for _, c := range cases {
		if got := shouldSendEscalationAck(ctx, c.checker, c.ackSent, "+573001112233"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
