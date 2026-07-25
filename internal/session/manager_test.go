package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	birdpkg "github.com/neuro-bot/neuro-bot/internal/bird"
)

// mockRepo is a test-local mock implementing SessionRepo.
type mockRepo struct {
	findActiveByPhoneFn  func(ctx context.Context, phone string) (*Session, error)
	findCurrentByPhoneFn func(ctx context.Context, phone string) (*Session, error)
	createFn             func(ctx context.Context, s *Session) error
	saveFn               func(ctx context.Context, s *Session) error
	updateStatusFn       func(ctx context.Context, sessionID, status string) error
	renewExpiryFn        func(ctx context.Context, sessionID string, expiresAt time.Time) error
	setContextFn         func(ctx context.Context, sessionID, key, value string) error
	setContextBatchFn    func(ctx context.Context, sessionID string, kvs map[string]string) error
	getContextFn         func(ctx context.Context, sessionID, key string) (string, error)
	getAllContextFn      func(ctx context.Context, sessionID string) (map[string]string, error)
	clearContextFn       func(ctx context.Context, sessionID string, keys ...string) error
	clearAllContextFn    func(ctx context.Context, sessionID string) error
	markEscalatedFn      func(ctx context.Context, sessionID, teamID string) error
	resumeSessionFn      func(ctx context.Context, sessionID, newState string, timeoutMinutes int) error
	findEscalatedFn      func(ctx context.Context) ([]EscalatedSession, error)
	touchPatientFn       func(ctx context.Context, sessionID string, expiresAt time.Time) error
	touchAgentFn         func(ctx context.Context, phone string) error
	incrementRemFn       func(ctx context.Context, sessionID string) error
	markAbandonedFn      func(ctx context.Context, sessionID string) error
	updateConvIDFn       func(ctx context.Context, phone, conversationID string) error
}

func (r *mockRepo) FindActiveByPhone(ctx context.Context, phone string) (*Session, error) {
	if r.findActiveByPhoneFn != nil {
		return r.findActiveByPhoneFn(ctx, phone)
	}
	return nil, nil
}

func (r *mockRepo) FindCurrentByPhone(ctx context.Context, phone string) (*Session, error) {
	if r.findCurrentByPhoneFn != nil {
		return r.findCurrentByPhoneFn(ctx, phone)
	}
	return r.FindActiveByPhone(ctx, phone)
}

func (r *mockRepo) Create(ctx context.Context, s *Session) error {
	if r.createFn != nil {
		return r.createFn(ctx, s)
	}
	return nil
}

func (r *mockRepo) Save(ctx context.Context, s *Session) error {
	if r.saveFn != nil {
		return r.saveFn(ctx, s)
	}
	return nil
}

func (r *mockRepo) UpdateStatus(ctx context.Context, sessionID, status string) error {
	if r.updateStatusFn != nil {
		return r.updateStatusFn(ctx, sessionID, status)
	}
	return nil
}

func (r *mockRepo) RenewExpiry(ctx context.Context, sessionID string, expiresAt time.Time) error {
	if r.renewExpiryFn != nil {
		return r.renewExpiryFn(ctx, sessionID, expiresAt)
	}
	return nil
}

func (r *mockRepo) UpdateConversationIDByPhone(ctx context.Context, phone, conversationID string) error {
	if r.updateConvIDFn != nil {
		return r.updateConvIDFn(ctx, phone, conversationID)
	}
	return nil
}

func (r *mockRepo) FindInactiveSessions(ctx context.Context, idleMinutes int) ([]InactiveSession, error) {
	return nil, nil
}

func (r *mockRepo) FindExpiredEscalatedSessions(ctx context.Context) ([]ExpiredEscalatedSession, error) {
	return nil, nil
}

func (r *mockRepo) MarkAbandoned(ctx context.Context, sessionID string) error {
	if r.markAbandonedFn != nil {
		return r.markAbandonedFn(ctx, sessionID)
	}
	return nil
}

func (r *mockRepo) FindEscalatedSessions(ctx context.Context) ([]EscalatedSession, error) {
	if r.findEscalatedFn != nil {
		return r.findEscalatedFn(ctx)
	}
	return nil, nil
}

func (r *mockRepo) TouchPatientActivity(ctx context.Context, sessionID string, expiresAt time.Time) error {
	if r.touchPatientFn != nil {
		return r.touchPatientFn(ctx, sessionID, expiresAt)
	}
	return nil
}

func (r *mockRepo) TouchAgentActivity(ctx context.Context, phone string) error {
	if r.touchAgentFn != nil {
		return r.touchAgentFn(ctx, phone)
	}
	return nil
}

func (r *mockRepo) IncrementAgentReminders(ctx context.Context, sessionID string) error {
	if r.incrementRemFn != nil {
		return r.incrementRemFn(ctx, sessionID)
	}
	return nil
}

func (r *mockRepo) SetContext(ctx context.Context, sessionID, key, value string) error {
	if r.setContextFn != nil {
		return r.setContextFn(ctx, sessionID, key, value)
	}
	return nil
}

func (r *mockRepo) SetContextBatch(ctx context.Context, sessionID string, kvs map[string]string) error {
	if r.setContextBatchFn != nil {
		return r.setContextBatchFn(ctx, sessionID, kvs)
	}
	return nil
}

func (r *mockRepo) GetContext(ctx context.Context, sessionID, key string) (string, error) {
	if r.getContextFn != nil {
		return r.getContextFn(ctx, sessionID, key)
	}
	return "", nil
}

func (r *mockRepo) GetAllContext(ctx context.Context, sessionID string) (map[string]string, error) {
	if r.getAllContextFn != nil {
		return r.getAllContextFn(ctx, sessionID)
	}
	return make(map[string]string), nil
}

func (r *mockRepo) ClearContext(ctx context.Context, sessionID string, keys ...string) error {
	if r.clearContextFn != nil {
		return r.clearContextFn(ctx, sessionID, keys...)
	}
	return nil
}

func (r *mockRepo) ClearAllContext(ctx context.Context, sessionID string) error {
	if r.clearAllContextFn != nil {
		return r.clearAllContextFn(ctx, sessionID)
	}
	return nil
}

func (r *mockRepo) MarkEscalated(ctx context.Context, sessionID, teamID string) error {
	if r.markEscalatedFn != nil {
		return r.markEscalatedFn(ctx, sessionID, teamID)
	}
	return nil
}

func (r *mockRepo) ResumeSession(ctx context.Context, sessionID, newState string, timeoutMinutes int) error {
	if r.resumeSessionFn != nil {
		return r.resumeSessionFn(ctx, sessionID, newState, timeoutMinutes)
	}
	return nil
}

func (r *mockRepo) CompleteActiveByPhone(ctx context.Context, phone string) error {
	return nil
}

func newTestSession() *Session {
	return &Session{
		ID:           "sess-1",
		PhoneNumber:  "+573001234567",
		CurrentState: "CHECK_BUSINESS_HOURS",
		Status:       StatusActive,
		Context:      make(map[string]string),
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
}

func TestFindOrCreate_NewSession(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewSessionManager(repo, 120)

	sess, isNew, err := mgr.FindOrCreate(context.Background(), "+573001234567")
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Error("expected isNew=true")
	}
	if sess.PhoneNumber != "+573001234567" {
		t.Errorf("expected phone, got %s", sess.PhoneNumber)
	}
	if sess.CurrentState != "CHECK_BUSINESS_HOURS" {
		t.Errorf("expected CHECK_BUSINESS_HOURS, got %s", sess.CurrentState)
	}
}

func TestFindOrCreate_ExistingSession(t *testing.T) {
	existing := newTestSession()
	existing.ID = "existing-id"
	repo := &mockRepo{
		findActiveByPhoneFn: func(ctx context.Context, phone string) (*Session, error) {
			return existing, nil
		},
	}
	mgr := NewSessionManager(repo, 120)

	sess, isNew, err := mgr.FindOrCreate(context.Background(), "+573001234567")
	if err != nil {
		t.Fatal(err)
	}
	if isNew {
		t.Error("expected isNew=false")
	}
	if sess.ID != "existing-id" {
		t.Errorf("expected existing-id, got %s", sess.ID)
	}
}

func TestFindOrCreate_Error(t *testing.T) {
	repo := &mockRepo{
		findActiveByPhoneFn: func(ctx context.Context, phone string) (*Session, error) {
			return nil, fmt.Errorf("db error")
		},
	}
	mgr := NewSessionManager(repo, 120)

	_, _, err := mgr.FindOrCreate(context.Background(), "+573001234567")
	if err == nil {
		t.Error("expected error")
	}
}

func TestRenewTimeout(t *testing.T) {
	renewed := false
	repo := &mockRepo{
		renewExpiryFn: func(ctx context.Context, sessionID string, expiresAt time.Time) error {
			renewed = true
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()
	// Expiry corto a propósito: newTestSession usa Now+2h (== timeout) y en Windows dos time.Now()
	// consecutivos caen en el mismo tick del reloj → After() sobre iguales fallaba (flake).
	sess.ExpiresAt = time.Now().Add(30 * time.Minute)
	oldExpiry := sess.ExpiresAt

	err := mgr.RenewTimeout(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed {
		t.Error("expected repo.RenewExpiry to be called")
	}
	if !sess.ExpiresAt.After(oldExpiry) {
		t.Error("expected ExpiresAt to be extended")
	}
}

func TestSaveState_WithContext(t *testing.T) {
	savedState := ""
	batchCalled := false
	repo := &mockRepo{
		saveFn: func(ctx context.Context, s *Session) error {
			savedState = s.CurrentState
			return nil
		},
		setContextBatchFn: func(ctx context.Context, sessionID string, kvs map[string]string) error {
			batchCalled = true
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.SaveState(context.Background(), sess, "MAIN_MENU",
		map[string]string{"key1": "val1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if savedState != "MAIN_MENU" {
		t.Errorf("expected MAIN_MENU, got %s", savedState)
	}
	if !batchCalled {
		t.Error("expected SetContextBatch to be called")
	}
	if sess.Context["key1"] != "val1" {
		t.Error("expected in-memory context update")
	}
}

func TestSaveState_WithClearCtx(t *testing.T) {
	clearCalled := false
	repo := &mockRepo{
		clearContextFn: func(ctx context.Context, sessionID string, keys ...string) error {
			clearCalled = true
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()
	sess.Context["old_key"] = "old_val"

	err := mgr.SaveState(context.Background(), sess, "NEXT", nil, []string{"old_key"})
	if err != nil {
		t.Fatal(err)
	}
	if !clearCalled {
		t.Error("expected ClearContext to be called")
	}
	if _, exists := sess.Context["old_key"]; exists {
		t.Error("expected old_key to be deleted from memory")
	}
}

func TestSetContext(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.SetContext(context.Background(), sess, "foo", "bar")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Context["foo"] != "bar" {
		t.Error("expected in-memory update")
	}
}

func TestSetContextBatch(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.SetContextBatch(context.Background(), sess, map[string]string{"a": "1", "b": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Context["a"] != "1" || sess.Context["b"] != "2" {
		t.Error("expected batch update in memory")
	}
}

func TestClearAllContext(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()
	sess.Context["x"] = "y"

	err := mgr.ClearAllContext(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Context) != 0 {
		t.Error("expected empty context after ClearAll")
	}
}

func TestComplete(t *testing.T) {
	statusSaved := ""
	repo := &mockRepo{
		updateStatusFn: func(ctx context.Context, sessionID, status string) error {
			statusSaved = status
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.Complete(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != StatusCompleted {
		t.Errorf("expected completed, got %s", sess.Status)
	}
	if statusSaved != StatusCompleted {
		t.Errorf("expected repo to receive completed, got %s", statusSaved)
	}
}

func TestEscalate(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.Escalate(context.Background(), sess, "team-test")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != StatusEscalated {
		t.Errorf("expected escalated, got %s", sess.Status)
	}
	if sess.EscalatedTeam != "team-test" {
		t.Errorf("expected team-test, got %s", sess.EscalatedTeam)
	}
	if sess.EscalatedAt == nil {
		t.Error("expected EscalatedAt to be set")
	}
}

func TestFindOrCreate_GetAllContextError(t *testing.T) {
	existing := newTestSession()
	existing.ID = "existing-id"
	repo := &mockRepo{
		findActiveByPhoneFn: func(ctx context.Context, phone string) (*Session, error) {
			return existing, nil
		},
		getAllContextFn: func(ctx context.Context, sessionID string) (map[string]string, error) {
			return nil, fmt.Errorf("ctx load error")
		},
	}
	mgr := NewSessionManager(repo, 120)

	_, _, err := mgr.FindOrCreate(context.Background(), "+573001234567")
	if err == nil {
		t.Fatal("expected error when GetAllContext fails")
	}
	if err.Error() != "ctx load error" {
		t.Errorf("expected 'ctx load error', got %q", err.Error())
	}
}

func TestFindOrCreate_CreateError(t *testing.T) {
	repo := &mockRepo{
		findActiveByPhoneFn: func(ctx context.Context, phone string) (*Session, error) {
			return nil, nil // no existing session
		},
		createFn: func(ctx context.Context, s *Session) error {
			return fmt.Errorf("create failed")
		},
	}
	mgr := NewSessionManager(repo, 120)

	_, _, err := mgr.FindOrCreate(context.Background(), "+573001234567")
	if err == nil {
		t.Fatal("expected error when Create fails")
	}
	if err.Error() != "create failed" {
		t.Errorf("expected 'create failed', got %q", err.Error())
	}
}

func TestSaveState_SaveError(t *testing.T) {
	repo := &mockRepo{
		saveFn: func(ctx context.Context, s *Session) error {
			return fmt.Errorf("save failed")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.SaveState(context.Background(), sess, "NEXT", nil, nil)
	if err == nil {
		t.Fatal("expected error when Save fails")
	}
	if err.Error() != "save failed" {
		t.Errorf("expected 'save failed', got %q", err.Error())
	}
}

func TestSaveState_SetContextBatchError(t *testing.T) {
	repo := &mockRepo{
		saveFn: func(ctx context.Context, s *Session) error {
			return nil // Save succeeds
		},
		setContextBatchFn: func(ctx context.Context, sessionID string, kvs map[string]string) error {
			return fmt.Errorf("batch failed")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.SaveState(context.Background(), sess, "NEXT",
		map[string]string{"key": "val"}, nil)
	if err == nil {
		t.Fatal("expected error when SetContextBatch fails")
	}
	if err.Error() != "batch failed" {
		t.Errorf("expected 'batch failed', got %q", err.Error())
	}
}

func TestSaveState_ClearContextError(t *testing.T) {
	repo := &mockRepo{
		saveFn: func(ctx context.Context, s *Session) error {
			return nil
		},
		clearContextFn: func(ctx context.Context, sessionID string, keys ...string) error {
			return fmt.Errorf("clear failed")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()
	sess.Context["old"] = "val"

	err := mgr.SaveState(context.Background(), sess, "NEXT", nil, []string{"old"})
	if err == nil {
		t.Fatal("expected error when ClearContext fails")
	}
	if err.Error() != "clear failed" {
		t.Errorf("expected 'clear failed', got %q", err.Error())
	}
}

func TestSaveState_BothUpdateAndClear(t *testing.T) {
	batchCalled := false
	clearCalled := false
	repo := &mockRepo{
		saveFn: func(ctx context.Context, s *Session) error {
			return nil
		},
		setContextBatchFn: func(ctx context.Context, sessionID string, kvs map[string]string) error {
			batchCalled = true
			if kvs["new_key"] != "new_val" {
				return fmt.Errorf("unexpected batch kvs: %v", kvs)
			}
			return nil
		},
		clearContextFn: func(ctx context.Context, sessionID string, keys ...string) error {
			clearCalled = true
			if len(keys) != 1 || keys[0] != "old_key" {
				return fmt.Errorf("unexpected clear keys: %v", keys)
			}
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()
	sess.Context["old_key"] = "old_val"

	err := mgr.SaveState(context.Background(), sess, "NEXT",
		map[string]string{"new_key": "new_val"}, []string{"old_key"})
	if err != nil {
		t.Fatal(err)
	}
	if !batchCalled {
		t.Error("expected SetContextBatch to be called")
	}
	if !clearCalled {
		t.Error("expected ClearContext to be called")
	}
	if sess.Context["new_key"] != "new_val" {
		t.Error("expected new_key in memory")
	}
	if _, exists := sess.Context["old_key"]; exists {
		t.Error("expected old_key to be deleted from memory")
	}
}

func TestSaveState_EmptyMaps(t *testing.T) {
	batchCalled := false
	clearCalled := false
	repo := &mockRepo{
		saveFn: func(ctx context.Context, s *Session) error {
			return nil
		},
		setContextBatchFn: func(ctx context.Context, sessionID string, kvs map[string]string) error {
			batchCalled = true
			return nil
		},
		clearContextFn: func(ctx context.Context, sessionID string, keys ...string) error {
			clearCalled = true
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	// Pass empty maps — neither batch nor clear should be called
	err := mgr.SaveState(context.Background(), sess, "NEXT",
		map[string]string{}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if batchCalled {
		t.Error("SetContextBatch should NOT be called for empty updateCtx")
	}
	if clearCalled {
		t.Error("ClearContext should NOT be called for empty clearCtx")
	}
}

func TestSetContext_RepoError(t *testing.T) {
	repo := &mockRepo{
		setContextFn: func(ctx context.Context, sessionID, key, value string) error {
			return fmt.Errorf("set ctx error")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.SetContext(context.Background(), sess, "foo", "bar")
	if err == nil {
		t.Fatal("expected error when SetContext repo fails")
	}
	// Memory should NOT be updated when repo fails
	if sess.Context["foo"] == "bar" {
		t.Error("expected in-memory context NOT to be updated on repo error")
	}
}

func TestSetContextBatch_RepoError(t *testing.T) {
	repo := &mockRepo{
		setContextBatchFn: func(ctx context.Context, sessionID string, kvs map[string]string) error {
			return fmt.Errorf("batch error")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.SetContextBatch(context.Background(), sess,
		map[string]string{"a": "1", "b": "2"})
	if err == nil {
		t.Fatal("expected error when SetContextBatch repo fails")
	}
	// Memory should NOT be updated
	if sess.Context["a"] == "1" {
		t.Error("expected in-memory context NOT to be updated on repo error")
	}
}

func TestClearAllContext_RepoError(t *testing.T) {
	repo := &mockRepo{
		clearAllContextFn: func(ctx context.Context, sessionID string) error {
			return fmt.Errorf("clear all error")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()
	sess.Context["x"] = "y"

	err := mgr.ClearAllContext(context.Background(), sess)
	if err == nil {
		t.Fatal("expected error when ClearAllContext repo fails")
	}
	// Memory should NOT be cleared on error
	if len(sess.Context) == 0 {
		t.Error("expected context NOT to be cleared on repo error")
	}
}

func TestComplete_RepoError(t *testing.T) {
	repo := &mockRepo{
		updateStatusFn: func(ctx context.Context, sessionID, status string) error {
			return fmt.Errorf("update status error")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.Complete(context.Background(), sess)
	if err == nil {
		t.Fatal("expected error when UpdateStatus fails")
	}
	// Note: status is already set in memory before repo call
	if sess.Status != StatusCompleted {
		t.Errorf("expected status set in memory even on error, got %s", sess.Status)
	}
}

func TestEscalate_RepoError(t *testing.T) {
	repo := &mockRepo{
		markEscalatedFn: func(ctx context.Context, sessionID, teamID string) error {
			return fmt.Errorf("mark escalated error")
		},
	}
	mgr := NewSessionManager(repo, 120)
	sess := newTestSession()

	err := mgr.Escalate(context.Background(), sess, "team-test")
	if err == nil {
		t.Fatal("expected error when MarkEscalated fails")
	}
	// Status is already set in memory before repo call
	if sess.Status != StatusEscalated {
		t.Errorf("expected status set in memory even on error, got %s", sess.Status)
	}
}

// TestUpdateConversationID_TargetedUpdate (H2): UpdateConversationID debe hacer un UPDATE dirigido
// de la columna y NUNCA FindActiveByPhone + Save (que reescribe la fila completa → lost update).
func TestUpdateConversationID_TargetedUpdate(t *testing.T) {
	got := ""
	repo := &mockRepo{
		updateConvIDFn: func(_ context.Context, phone, convID string) error {
			got = phone + "|" + convID
			return nil
		},
		findActiveByPhoneFn: func(_ context.Context, _ string) (*Session, error) {
			t.Error("UpdateConversationID must NOT call FindActiveByPhone (lost-update path)")
			return nil, nil
		},
		saveFn: func(_ context.Context, _ *Session) error {
			t.Error("UpdateConversationID must NOT call Save (full-row write = lost update)")
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	if err := mgr.UpdateConversationID(context.Background(), "+573001234567", "conv-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "+573001234567|conv-1" {
		t.Errorf("expected targeted update with phone+conv, got %q", got)
	}
}

func TestUpdateConversationID_NoopOnEmpty(t *testing.T) {
	repo := &mockRepo{
		updateConvIDFn: func(_ context.Context, _, _ string) error {
			t.Error("must not update on empty phone/conversationID")
			return nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	_ = mgr.UpdateConversationID(context.Background(), "", "conv")
	_ = mgr.UpdateConversationID(context.Background(), "+573001234567", "")
}

func TestPhoneMutex_Returns(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewSessionManager(repo, 120)

	pm := mgr.PhoneMutex()
	if pm == nil {
		t.Fatal("expected non-nil PhoneMutex")
	}
}

// mockInactivityBird records calls for the escalated-session checker tests.
type mockInactivityBird struct {
	internalTexts   []string
	closedFeeds     int
	internalSendErr error // si != nil, SendInternalText lo devuelve (no agrega a internalTexts)
}

func (m *mockInactivityBird) SendText(_, _, _ string) (string, error) {
	return "", nil
}

func (m *mockInactivityBird) SendInternalText(_, text string) (string, error) {
	if m.internalSendErr != nil {
		return "", m.internalSendErr
	}
	m.internalTexts = append(m.internalTexts, text)
	return "msg-id", nil
}

func (m *mockInactivityBird) UnassignFeedItem(_ string, _ bool) error { return nil }

func (m *mockInactivityBird) CloseFeedItems(_ string) error {
	m.closedFeeds++
	return nil
}

func escalationDeps(bird *mockInactivityBird) InactivityDeps {
	return InactivityDeps{
		BirdClient:         bird,
		EscalationCloseMin: 120,
		AgentReminderMin:   15,
		AgentReminderMax:   3,
	}
}

// TestCheckEscalatedSessions_CloseOnPatientSilence: el paciente lleva > CloseMin en silencio → cerrar.
func TestCheckEscalatedSessions_CloseOnPatientSilence(t *testing.T) {
	abandoned := ""
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: time.Now().Add(-130 * time.Minute), // > 120
			}}, nil
		},
		markAbandonedFn: func(_ context.Context, sessionID string) error { abandoned = sessionID; return nil },
	}
	mgr := NewSessionManager(repo, 120)
	bird := &mockInactivityBird{}

	mgr.checkEscalatedSessions(context.Background(), escalationDeps(bird))

	if abandoned != "s1" {
		t.Errorf("expected session s1 abandoned, got %q", abandoned)
	}
	if bird.closedFeeds != 1 {
		t.Errorf("expected 1 feed closed, got %d", bird.closedFeeds)
	}
	if len(bird.internalTexts) != 0 {
		t.Errorf("expected no agent reminder when closing, got %d", len(bird.internalTexts))
	}
}

// TestCheckEscalatedSessions_RemindWhenAgentSilent: paciente espera, agente no respondió → recordatorio.
func TestCheckEscalatedSessions_RemindWhenAgentSilent(t *testing.T) {
	incremented := ""
	abandoned := false
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: time.Now().Add(-16 * time.Minute), // > 15, < 120
				LastAgentMsg:   nil,
				RemindersSent:  0,
			}}, nil
		},
		incrementRemFn:  func(_ context.Context, sessionID string) error { incremented = sessionID; return nil },
		markAbandonedFn: func(_ context.Context, _ string) error { abandoned = true; return nil },
	}
	mgr := NewSessionManager(repo, 120)
	bird := &mockInactivityBird{}

	mgr.checkEscalatedSessions(context.Background(), escalationDeps(bird))

	if abandoned {
		t.Error("session must not be closed while patient is recent")
	}
	if len(bird.internalTexts) != 1 {
		t.Fatalf("expected 1 agent reminder, got %d", len(bird.internalTexts))
	}
	if incremented != "s1" {
		t.Errorf("expected reminder counter incremented for s1, got %q", incremented)
	}
}

// TestCheckEscalatedSessions_NoRemindWhenAgentReplied: el agente respondió tras el último msg del paciente.
func TestCheckEscalatedSessions_NoRemindWhenAgentReplied(t *testing.T) {
	now := time.Now()
	agentReply := now.Add(-1 * time.Minute)
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: now.Add(-16 * time.Minute),
				LastAgentMsg:   &agentReply, // posterior al paciente → atendido
			}}, nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	bird := &mockInactivityBird{}

	mgr.checkEscalatedSessions(context.Background(), escalationDeps(bird))

	if len(bird.internalTexts) != 0 {
		t.Errorf("expected no reminder when agent already replied, got %d", len(bird.internalTexts))
	}
}

// TestCheckEscalatedSessions_StopsAtMax: no recordar más allá de AgentReminderMax.
func TestCheckEscalatedSessions_StopsAtMax(t *testing.T) {
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: time.Now().Add(-90 * time.Minute),
				RemindersSent:  3, // == max
			}}, nil
		},
	}
	mgr := NewSessionManager(repo, 120)
	bird := &mockInactivityBird{}

	mgr.checkEscalatedSessions(context.Background(), escalationDeps(bird))

	if len(bird.internalTexts) != 0 {
		t.Errorf("expected no reminder past max, got %d", len(bird.internalTexts))
	}
}

// TestCheckEscalatedSessions_ClosesOnInactiveConversation (BUG-005): si el recordatorio al agente
// falla porque la conversación de Bird ya no está activa (el agente la cerró), la escalación se cierra
// en vez de reintentar cada minuto indefinidamente.
func TestCheckEscalatedSessions_ClosesOnInactiveConversation(t *testing.T) {
	var abandoned string
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: time.Now().Add(-90 * time.Minute), // ya vencido el recordatorio
				RemindersSent:  0,
			}}, nil
		},
		markAbandonedFn: func(_ context.Context, sessionID string) error { abandoned = sessionID; return nil },
	}
	mgr := NewSessionManager(repo, 120)
	rec := &mockEscalationRecorder{}
	mgr.SetEscalationRecorder(rec)
	bird := &mockInactivityBird{internalSendErr: fmt.Errorf("send: %w", birdpkg.ErrConversationNotActive)}

	deps := escalationDeps(bird)
	deps.EscalationCloseMin = 0 // aislar: que NO cierre por silencio, solo por conversación inactiva
	mgr.checkEscalatedSessions(context.Background(), deps)

	if abandoned != "s1" {
		t.Errorf("esperaba que la sesión s1 se cerrara (MarkAbandoned), got %q", abandoned)
	}
	if rec.expire != 1 {
		t.Errorf("esperaba 1 Expire de la escalación, got %d", rec.expire)
	}
	if len(bird.internalTexts) != 0 {
		t.Errorf("no debió registrarse recordatorio enviado, got %d", len(bird.internalTexts))
	}
}

func TestStartInactivityChecker_ContextCancellation(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewSessionManager(repo, 120)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.StartInactivityChecker(ctx, InactivityDeps{
			ReminderMin: 5,
			CloseMin:    15,
		})
		close(done)
	}()

	// Cancel immediately
	cancel()

	// Wait for goroutine to exit
	select {
	case <-done:
		// goroutine exited — success
	case <-time.After(2 * time.Second):
		t.Fatal("StartInactivityChecker goroutine did not exit after context cancellation")
	}
}

// --- Escalation recorder wiring (tabla escalations) ---

type mockEscalationRecorder struct {
	touch, resume, closeN, expire, noShow int
}

func (m *mockEscalationRecorder) TouchAgent(_ context.Context, _ string) error { m.touch++; return nil }

func (m *mockEscalationRecorder) Resume(_ context.Context, _ string) error { m.resume++; return nil }

func (m *mockEscalationRecorder) Close(_ context.Context, _ string) error { m.closeN++; return nil }

func (m *mockEscalationRecorder) Expire(_ context.Context, _ string) error { m.expire++; return nil }

func (m *mockEscalationRecorder) NoShow(_ context.Context, _ string) error { m.noShow++; return nil }

type mockResumer struct{ phones []string }

func (m *mockResumer) ResumeEscalationNoShow(phone string) { m.phones = append(m.phones, phone) }

// TestCheckEscalatedSessions_AgentNoShow_ReturnsToBot: el agente NUNCA respondió y se agotó la ventana
// de recordatorios → se devuelve al bot (resumer) y NO se cierra como abandono del paciente.
func TestCheckEscalatedSessions_AgentNoShow_ReturnsToBot(t *testing.T) {
	var abandoned bool
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: time.Now().Add(-70 * time.Minute), // > 60 = AgentReminderMin*(Max+1)=15*4
				LastAgentMsg:   nil,                               // agente nunca respondió
				RemindersSent:  3,
			}}, nil
		},
		markAbandonedFn: func(_ context.Context, _ string) error { abandoned = true; return nil },
	}
	mgr := NewSessionManager(repo, 120)
	rec := &mockEscalationRecorder{}
	mgr.SetEscalationRecorder(rec)
	resumer := &mockResumer{}
	deps := escalationDeps(&mockInactivityBird{})
	deps.Resumer = resumer

	mgr.checkEscalatedSessions(context.Background(), deps)

	if len(resumer.phones) != 1 || resumer.phones[0] != "+57300" {
		t.Fatalf("esperaba ResumeEscalationNoShow(+57300), got %v", resumer.phones)
	}
	if abandoned {
		t.Error("NO debe marcar abandono: el agente nunca atendió (es no-show)")
	}
}

// TestCheckEscalatedSessions_ClosesWhenAgentEngaged: si el agente YA atendió y el paciente lleva >2h
// en silencio, SÍ se cierra como abandono (escalation_expired), no como no-show.
func TestCheckEscalatedSessions_ClosesWhenAgentEngaged(t *testing.T) {
	agentReplied := time.Now().Add(-100 * time.Minute)
	var abandoned bool
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: time.Now().Add(-130 * time.Minute), // > EscalationCloseMin (120)
				LastAgentMsg:   &agentReplied,                      // el agente SÍ atendió
				RemindersSent:  1,
			}}, nil
		},
		markAbandonedFn: func(_ context.Context, _ string) error { abandoned = true; return nil },
	}
	mgr := NewSessionManager(repo, 120)
	rec := &mockEscalationRecorder{}
	mgr.SetEscalationRecorder(rec)
	resumer := &mockResumer{}
	deps := escalationDeps(&mockInactivityBird{})
	deps.Resumer = resumer

	mgr.checkEscalatedSessions(context.Background(), deps)

	if !abandoned {
		t.Error("debe cerrar como abandono cuando el agente atendió y el paciente lleva >2h en silencio")
	}
	if rec.expire != 1 {
		t.Errorf("esperaba 1 Expire, got %d", rec.expire)
	}
	if len(resumer.phones) != 0 {
		t.Errorf("no debe disparar no-show cuando el agente atendió, got %v", resumer.phones)
	}
}

// TestCheckEscalatedSessions_NoShowFallbackClosesWithoutResumer: sin resumer, el no-show no puede actuar;
// para no fugar la sesión, a las 2h se cierra igual aunque el agente nunca atendió (fallback defensivo).
func TestCheckEscalatedSessions_NoShowFallbackClosesWithoutResumer(t *testing.T) {
	var abandoned bool
	repo := &mockRepo{
		findEscalatedFn: func(_ context.Context) ([]EscalatedSession, error) {
			return []EscalatedSession{{
				ID: "s1", PhoneNumber: "+57300", ConversationID: "conv1",
				LastPatientMsg: time.Now().Add(-130 * time.Minute),
				LastAgentMsg:   nil,
				RemindersSent:  3,
			}}, nil
		},
		markAbandonedFn: func(_ context.Context, _ string) error { abandoned = true; return nil },
	}
	mgr := NewSessionManager(repo, 120)
	mgr.SetEscalationRecorder(&mockEscalationRecorder{})
	deps := escalationDeps(&mockInactivityBird{}) // sin Resumer (nil)

	mgr.checkEscalatedSessions(context.Background(), deps)

	if !abandoned {
		t.Error("sin resumer, a las 2h debe cerrar igual para no fugar la sesión")
	}
}

func TestTouchAgentActivity_RecordsEscalation(t *testing.T) {
	mgr := NewSessionManager(&mockRepo{}, 120)
	rec := &mockEscalationRecorder{}
	mgr.SetEscalationRecorder(rec)
	if err := mgr.TouchAgentActivity(context.Background(), "+573001234567"); err != nil {
		t.Fatal(err)
	}
	if rec.touch != 1 {
		t.Errorf("expected TouchAgent called once, got %d", rec.touch)
	}
}

func TestResumeFromEscalation_RecordsResume(t *testing.T) {
	mgr := NewSessionManager(&mockRepo{}, 120)
	rec := &mockEscalationRecorder{}
	mgr.SetEscalationRecorder(rec)
	if err := mgr.ResumeFromEscalation(context.Background(), newTestSession(), "GREETING"); err != nil {
		t.Fatal(err)
	}
	if rec.resume != 1 {
		t.Errorf("expected Resume called once, got %d", rec.resume)
	}
}

func TestComplete_EscalatedRecordsClose(t *testing.T) {
	mgr := NewSessionManager(&mockRepo{}, 120)
	rec := &mockEscalationRecorder{}
	mgr.SetEscalationRecorder(rec)
	sess := newTestSession()
	sess.Status = StatusEscalated
	if err := mgr.Complete(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if rec.closeN != 1 {
		t.Errorf("expected Close called once for escalated session, got %d", rec.closeN)
	}
}

func TestComplete_NonEscalatedNoClose(t *testing.T) {
	mgr := NewSessionManager(&mockRepo{}, 120)
	rec := &mockEscalationRecorder{}
	mgr.SetEscalationRecorder(rec)
	if err := mgr.Complete(context.Background(), newTestSession()); err != nil {
		t.Fatal(err)
	}
	if rec.closeN != 0 {
		t.Errorf("expected no Close for non-escalated session, got %d", rec.closeN)
	}
}

// M3: si Create choca con el índice único (otra ruta creó la activa primero), FindOrCreate debe
// re-leer la sesión ganadora en vez de duplicar o fallar.
func TestFindOrCreate_DuplicateActive_RereadsWinner(t *testing.T) {
	winner := &Session{ID: "winner", PhoneNumber: "+573001112233", Status: StatusActive}
	findCalls := 0
	repo := &mockRepo{
		findActiveByPhoneFn: func(_ context.Context, _ string) (*Session, error) {
			findCalls++
			if findCalls == 1 {
				return nil, nil // 1ª: no hay activa → se intenta crear
			}
			return winner, nil // tras el 1062: ya existe la ganadora
		},
		createFn: func(_ context.Context, _ *Session) error {
			return ErrActiveSessionExists // otra ruta ganó el race
		},
		getAllContextFn: func(_ context.Context, _ string) (map[string]string, error) {
			return map[string]string{"k": "v"}, nil
		},
	}
	mgr := NewSessionManager(repo, 120)

	s, isNew, err := mgr.FindOrCreate(context.Background(), "+573001112233")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if isNew {
		t.Error("expected isNew=false (re-leyó la ganadora, no creó)")
	}
	if s == nil || s.ID != "winner" {
		t.Errorf("expected winner session, got %+v", s)
	}
	if s != nil && s.Context["k"] != "v" {
		t.Error("expected context cargado de la ganadora")
	}
}
