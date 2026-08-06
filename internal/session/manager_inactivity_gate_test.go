package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// El "¿Sigues ahí?" solo vale la pena si el paciente estaba a mitad de un trámite. El 52% de las
// sesiones abandonadas mueren en MAIN_MENU (vieron el menú y se fueron): recordarles es un envío
// cobrado con valor ~cero (~1.500-1.900/sem). El cierre por inactividad NO cambia.

type inactivityBirdMock struct {
	mu    sync.Mutex
	texts []string
}

func (b *inactivityBirdMock) SendText(_, _, text string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.texts = append(b.texts, text)
	return "msg-1", nil
}

func (b *inactivityBirdMock) SendInternalText(_, _ string) (string, error) {
	return "msg-int", nil
}
func (b *inactivityBirdMock) UnassignFeedItem(_ string, _ bool) error { return nil }
func (b *inactivityBirdMock) CloseFeedItems(_ string) error           { return nil }

func (b *inactivityBirdMock) sentCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.texts)
}

func inactiveSess(id, state string, idleMin int) InactiveSession {
	return InactiveSession{
		ID:           id,
		PhoneNumber:  "+573001234567",
		CurrentState: state,
		LastActivity: time.Now().Add(-time.Duration(idleMin) * time.Minute),
	}
}

func runInactivityCheck(t *testing.T, sessions []InactiveSession) (*inactivityBirdMock, *mockRepo) {
	t.Helper()
	repo := &mockRepo{}
	repo.findInactiveFn = func(_ context.Context, _ int) ([]InactiveSession, error) {
		return sessions, nil
	}
	birdMock := &inactivityBirdMock{}
	m := NewSessionManager(repo, 30)
	m.checkInactiveSessions(context.Background(), InactivityDeps{
		BirdClient:  birdMock,
		ReminderMin: 10,
		CloseMin:    30,
	})
	return birdMock, repo
}

// Sesión estancada en el MENÚ (sin trámite iniciado): NO se envía el recordatorio.
func TestInactivityReminder_SkippedInMenuStates(t *testing.T) {
	bird, _ := runInactivityCheck(t, []InactiveSession{
		inactiveSess("s-menu", "MAIN_MENU", 15),
		inactiveSess("s-ooh", "OUT_OF_HOURS_MENU", 15),
		inactiveSess("s-fallback", "FALLBACK_MENU", 15),
	})
	if got := bird.sentCount(); got != 0 {
		t.Errorf("estados sin progreso no deben recibir recordatorio, hubo %d envíos", got)
	}
}

// Sesión a mitad de un trámite real: el recordatorio SÍ se envía (recuperarla vale el mensaje).
func TestInactivityReminder_SentForInFlowStates(t *testing.T) {
	bird, _ := runInactivityCheck(t, []InactiveSession{
		inactiveSess("s-upload", "UPLOAD_MEDICAL_ORDER", 15),
		inactiveSess("s-slots", "SHOW_SLOTS", 15),
	})
	if got := bird.sentCount(); got != 2 {
		t.Errorf("estados con trámite en curso deben recibir recordatorio, hubo %d envíos", got)
	}
}

// El CIERRE por inactividad no cambia: también aplica a los estados de menú.
func TestInactivityClose_UnchangedForMenuStates(t *testing.T) {
	var closed []string
	repo := &mockRepo{}
	repo.findInactiveFn = func(_ context.Context, _ int) ([]InactiveSession, error) {
		s := inactiveSess("s-menu-close", "MAIN_MENU", 45)
		s.Reminders = 1 // la marca la puso el tick anterior (enviando o saltando el recordatorio)
		return []InactiveSession{s}, nil
	}
	repo.updateStatusFn = func(_ context.Context, sessionID, status string) error {
		closed = append(closed, sessionID+":"+status)
		return nil
	}
	birdMock := &inactivityBirdMock{}
	m := NewSessionManager(repo, 30)
	m.checkInactiveSessions(context.Background(), InactivityDeps{
		BirdClient:  birdMock,
		ReminderMin: 10,
		CloseMin:    30,
	})
	if len(closed) != 1 || closed[0] != "s-menu-close:abandoned" {
		t.Errorf("la sesión de menú inactiva debe cerrarse como siempre, got %v", closed)
	}
	if birdMock.sentCount() != 0 {
		t.Errorf("el cierre es silencioso, hubo %d envíos", birdMock.sentCount())
	}
}
