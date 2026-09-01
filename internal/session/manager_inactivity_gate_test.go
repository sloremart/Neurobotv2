package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// El "¿Sigues ahí?" se ELIMINÓ por completo (decisión del 2026-08-31). Antes se filtraba por estado
// —solo se enviaba si el paciente estaba a mitad de un trámite—; ahora no se envía en NINGÚN caso.
// Motivo medido: buena parte de esos envíos ni siquiera se podían entregar (la ventana de servicio
// de 24 h ya había cerrado) y Bird los rechazaba con "no active session… use an approved template",
// cobrando el intento. El cierre por inactividad NO cambia, y sigue siendo silencioso: no existe
// ningún mensaje posterior que le anuncie al paciente que su sesión se cerró.

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

// NINGÚN estado recibe ya el "¿Sigues ahí?": ni los de menú (que antes se saltaban) ni los que
// están a mitad de un trámite (que antes sí lo recibían). Cero envíos, en todos los escenarios.
func TestInactivityReminder_NeverSentInAnyState(t *testing.T) {
	bird, _ := runInactivityCheck(t, []InactiveSession{
		// Antes se SALTABAN.
		inactiveSess("s-menu", "MAIN_MENU", 15),
		inactiveSess("s-ooh", "OUT_OF_HOURS_MENU", 15),
		inactiveSess("s-fallback", "FALLBACK_MENU", 15),
		inactiveSess("s-greeting", "GREETING", 15),
		// Antes SÍ lo recibían: son los que este cambio elimina.
		inactiveSess("s-upload", "UPLOAD_MEDICAL_ORDER", 15),
		inactiveSess("s-slots", "SHOW_SLOTS", 15),
		inactiveSess("s-confirm", "CONFIRM_BOOKING", 15),
		inactiveSess("s-doc", "ASK_DOCUMENT", 15),
	})
	if got := bird.sentCount(); got != 0 {
		t.Errorf("el recordatorio de inactividad está eliminado: esperaba 0 envíos, hubo %d (%v)",
			got, bird.texts)
	}
}

// La marca se sigue poniendo aunque ya no se envíe nada: es lo que mantiene el cierre en CloseMin.
// Sin ella, el fallback anti-sesión-inmortal retrasaría cada cierre hasta CloseMin+ReminderMin.
func TestInactivityReminder_StillMarksSoCloseTimingIsUnchanged(t *testing.T) {
	marks := map[string]string{}
	repo := &mockRepo{}
	repo.findInactiveFn = func(_ context.Context, _ int) ([]InactiveSession, error) {
		return []InactiveSession{
			inactiveSess("s-upload", "UPLOAD_MEDICAL_ORDER", 15),
			inactiveSess("s-menu", "MAIN_MENU", 15),
		}, nil
	}
	repo.setContextFn = func(_ context.Context, sessionID, key, value string) error {
		if key == "inactivity_reminders" {
			marks[sessionID] = value
		}
		return nil
	}
	m := NewSessionManager(repo, 30)
	m.checkInactiveSessions(context.Background(), InactivityDeps{
		BirdClient:  &inactivityBirdMock{},
		ReminderMin: 10,
		CloseMin:    30,
	})
	if marks["s-upload"] != "1" || marks["s-menu"] != "1" {
		t.Errorf("ambas sesiones debían quedar marcadas para no retrasar el cierre, got %v", marks)
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
