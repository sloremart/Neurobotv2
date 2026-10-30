package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// Un recordatorio que no salió deja el pending bajo el teléfono («+57…»). Con Néctar la respuesta
// del paciente llega bajo "nectar:<id>": AdoptPhonePending mueve ese pending a la clave de la
// conversación para que la respuesta cuente y el IVR de las 13:00 no lo llame.

const (
	adoptPhone = "+573001112233"
	adoptKey   = "nectar:55"
)

// adoptManager arma un manager con persister y repositorio de sesiones falsos y el pending del
// recordatorio fallido bajo el teléfono (como lo deja sendWhatsAppReminders).
func adoptManager(t *testing.T) (*NotificationManager, *mockPersister, *mockSessionCreator) {
	t.Helper()
	mgr := NewNotificationManager(nil, nil, &config.Config{})
	return seedAdopt(t, mgr)
}

func seedAdopt(t *testing.T, mgr *NotificationManager) (*NotificationManager, *mockPersister, *mockSessionCreator) {
	t.Helper()
	pers := &mockPersister{}
	mgr.SetPersister(pers)
	sessRepo := &mockSessionCreator{}
	mgr.SetWaitingListDeps(nil, sessRepo, &mockVirtualEnqueuer{})
	if !mgr.RegisterPending(PendingNotification{Type: "confirmation", Phone: adoptPhone, AppointmentID: "APT001"}) {
		t.Fatal("register")
	}
	pers.findAllRows = append(pers.findAllRows, pers.upserted...)
	t.Cleanup(func() {
		if p, ok := mgr.LoadPendingForTest(adoptKey); ok && p.Timer != nil {
			p.Timer.Stop()
		}
		if p, ok := mgr.LoadPendingForTest(adoptPhone); ok && p.Timer != nil {
			p.Timer.Stop()
		}
	})
	return mgr, pers, sessRepo
}

func TestAdoptPhonePending_MovesPendingToSessionKey(t *testing.T) {
	mgr, pers, _ := adoptManager(t)
	before, _ := mgr.LoadPendingForTest(adoptPhone)
	oldTimer, expiresAt := before.Timer, before.ExpiresAt

	if !mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("debía adoptar el pending del teléfono")
	}
	if mgr.HasPending(adoptPhone) {
		t.Fatal("no queda pending bajo el teléfono")
	}
	p, ok := mgr.LoadPendingForTest(adoptKey)
	if !ok {
		t.Fatal("el pending queda bajo la clave de la conversación")
	}
	if p.Phone != adoptKey || p.ConversationID != "55" || p.ContactPhone != adoptPhone || p.AppointmentID != "APT001" {
		t.Fatalf("pending adoptado = %+v", p)
	}
	// (f) el vencimiento es el que ya tenía, con un timer nuevo; el viejo quedó parado.
	if !p.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt cambió: %v → %v", expiresAt, p.ExpiresAt)
	}
	if p.Timer == nil || p.Timer == oldTimer {
		t.Fatal("el pending adoptado lleva su propio timer")
	}
	if oldTimer.Stop() {
		t.Fatal("el timer de la clave vieja debía estar parado")
	}
	// El índice de contacto apunta a la clave nueva (idempotencia por teléfono del scheduler).
	if !mgr.HasPendingForContact(adoptPhone) {
		t.Fatal("el teléfono sigue encontrando su pending por el índice de contacto")
	}
	// El IVR lo sigue viendo (si el paciente no contesta), marcando al teléfono.
	ivr := mgr.GetPendingForIVR()
	if len(ivr) != 1 || ivr[0].Phone != adoptKey || ivr[0].DialPhone() != adoptPhone {
		t.Fatalf("GetPendingForIVR = %+v", ivr)
	}
	// (g) persistencia: la fila cambió de clave.
	if len(pers.rekeyCalls) != 1 || pers.rekeyCalls[0] != [4]string{adoptPhone, adoptKey, "55", adoptPhone} {
		t.Fatalf("Rekey = %v", pers.rekeyCalls)
	}
	rows, _ := pers.FindAll(context.Background())
	if len(rows) != 1 || rows[0].Phone != adoptKey || rows[0].ConversationID != "55" || rows[0].ContactPhone != adoptPhone {
		t.Fatalf("FindAll tras Rekey = %+v", rows)
	}
}

// (f) un disparo tardío del timer viejo no encuentra nada bajo la clave vieja ni toca el adoptado.
func TestAdoptPhonePending_OldKeyTimeoutIsNoop(t *testing.T) {
	mgr, pers, _ := adoptManager(t)
	if !mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("adopt")
	}
	mgr.handleTimeout(adoptPhone)
	if !mgr.HasPending(adoptKey) {
		t.Fatal("el pending adoptado sigue vivo")
	}
	if pers.resolvedPhone != "" {
		t.Fatalf("nada se resolvió, got %q", pers.resolvedPhone)
	}
}

// (a) «Confirmar» tras adoptar consume el pending: el IVR ya no lo llama.
func TestAdoptPhonePending_ConfirmConsumesPending(t *testing.T) {
	base, _ := confirmManager(t)
	mgr, pers, _ := seedAdopt(t, base)

	if !mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("adopt")
	}
	mgr.HandleResponse(adoptKey, "confirm", "55")

	if mgr.HasPending(adoptPhone) || mgr.HasPending(adoptKey) || mgr.HasPendingForContact(adoptPhone) {
		t.Fatal("no queda pending ni bajo el teléfono ni bajo la conversación")
	}
	if ivr := mgr.GetPendingForIVR(); len(ivr) != 0 {
		t.Fatalf("el IVR no lo llama: %+v", ivr)
	}
	if pers.resolvedPhone != adoptKey || pers.resolvedStatus != "confirmed" {
		t.Fatalf("se resuelve bajo la clave nueva: %q %q", pers.resolvedPhone, pers.resolvedStatus)
	}
}

// (c) conversación con un agente (sesión escalada bajo la clave): no se adopta; el pending del
// teléfono sigue para el IVR.
func TestAdoptPhonePending_EscalatedSessionNotAdopted(t *testing.T) {
	mgr, pers, sessRepo := adoptManager(t)
	var asked string
	sessRepo.findCurrentFn = func(_ context.Context, phone string) (*session.Session, error) {
		asked = phone
		return &session.Session{ID: "s1", PhoneNumber: phone, Status: session.StatusEscalated}, nil
	}
	if mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("con la conversación en manos de un agente no se adopta")
	}
	if asked != adoptKey {
		t.Fatalf("la sesión se busca por la clave de la conversación, got %q", asked)
	}
	assertPhonePendingKept(t, mgr, pers)
}

// Sin poder comprobar la sesión (error de BD) no se adopta: el IVR llama como si no hubiera escrito.
func TestAdoptPhonePending_SessionLookupErrorNotAdopted(t *testing.T) {
	mgr, pers, sessRepo := adoptManager(t)
	sessRepo.findCurrentFn = func(context.Context, string) (*session.Session, error) {
		return nil, errors.New("db down")
	}
	if mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("ante la duda no se adopta")
	}
	assertPhonePendingKept(t, mgr, pers)
}

// Una sesión activa del bot (no escalada) no impide adoptar: es lo mismo que en Bird, donde el
// pending del teléfono intercepta aunque haya sesión.
func TestAdoptPhonePending_ActiveSessionAdopts(t *testing.T) {
	mgr, _, sessRepo := adoptManager(t)
	sessRepo.findCurrentFn = func(_ context.Context, phone string) (*session.Session, error) {
		return &session.Session{ID: "s1", PhoneNumber: phone, Status: session.StatusActive}, nil
	}
	if !mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("una sesión activa del bot no bloquea la adopción")
	}
}

// (e) ya hay pending bajo la conversación: no se toca ninguno (el camino de hoy).
func TestAdoptPhonePending_ExistingSessionPendingNotAdopted(t *testing.T) {
	mgr, pers, _ := adoptManager(t)
	mgr.RegisterPending(PendingNotification{Type: "waiting_list", Phone: adoptKey, ContactPhone: adoptPhone, WaitingListID: "wl-1"})
	if mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("con pending bajo la conversación no se adopta")
	}
	if !mgr.HasPending(adoptPhone) {
		t.Fatal("el pending del teléfono sigue")
	}
	if p, _ := mgr.LoadPendingForTest(adoptKey); p.Type != "waiting_list" {
		t.Fatalf("el de la conversación no cambia: %+v", p)
	}
	if len(pers.rekeyCalls) != 0 {
		t.Fatalf("no hubo Rekey: %v", pers.rekeyCalls)
	}
}

// Sin pending del teléfono, o de un tipo que el recordatorio no deja, no hay nada que adoptar.
func TestAdoptPhonePending_NothingToAdopt(t *testing.T) {
	mgr := NewNotificationManager(nil, nil, &config.Config{})
	mgr.SetWaitingListDeps(nil, &mockSessionCreator{}, &mockVirtualEnqueuer{})
	if mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("sin pending no se adopta")
	}
	mgr.RegisterPending(PendingNotification{Type: "waiting_list", Phone: adoptPhone, WaitingListID: "wl-1"})
	t.Cleanup(func() {
		if p, ok := mgr.LoadPendingForTest(adoptPhone); ok {
			p.Timer.Stop()
		}
	})
	if mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("solo se adoptan confirmation/reschedule")
	}
	if mgr.AdoptPhonePending("", adoptKey, "55") || mgr.AdoptPhonePending(adoptPhone, adoptPhone, "55") {
		t.Fatal("teléfono vacío o misma clave no adoptan")
	}
}

// Un pending ya vencido lo procesa su timeout; no se adopta.
func TestAdoptPhonePending_ExpiredNotAdopted(t *testing.T) {
	mgr, pers, _ := adoptManager(t)
	p, _ := mgr.LoadPendingForTest(adoptPhone)
	p.ExpiresAt = time.Now().Add(-time.Minute)
	if mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("vencido no se adopta")
	}
	assertPhonePendingKept(t, mgr, pers)
}

// Si la BD no deja cambiar la clave, no se adopta: un reinicio restauraría la fila bajo el teléfono
// y el IVR llamaría a quien ya contestó.
func TestAdoptPhonePending_RekeyErrorNotAdopted(t *testing.T) {
	mgr, pers, _ := adoptManager(t)
	pers.rekeyErr = errors.New("db down")
	if mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("sin persistir la clave no se adopta")
	}
	if !mgr.HasPending(adoptPhone) || mgr.HasPending(adoptKey) {
		t.Fatal("el pending sigue bajo el teléfono")
	}
}

// Una llamada IVR en curso sigue correlacionando su DTMF tras adoptar.
func TestAdoptPhonePending_MovesCallID(t *testing.T) {
	mgr, _, _ := adoptManager(t)
	mgr.RegisterCallID("call-1", adoptPhone)
	if !mgr.AdoptPhonePending(adoptPhone, adoptKey, "55") {
		t.Fatal("adopt")
	}
	if v, ok := mgr.callIDMap.Load("call-1"); !ok || v.(string) != adoptKey {
		t.Fatalf("callIDMap apunta a la clave nueva, got %v %v", v, ok)
	}
	if p, _ := mgr.LoadPendingForTest(adoptKey); p.CallID != "call-1" {
		t.Fatalf("el pending conserva su CallID: %+v", p)
	}
}

func assertPhonePendingKept(t *testing.T, mgr *NotificationManager, pers *mockPersister) {
	t.Helper()
	if !mgr.HasPending(adoptPhone) || mgr.HasPending(adoptKey) {
		t.Fatal("el pending sigue bajo el teléfono y nada bajo la conversación")
	}
	if ivr := mgr.GetPendingForIVR(); len(ivr) != 1 || ivr[0].Phone != adoptPhone {
		t.Fatalf("el IVR lo sigue viendo bajo el teléfono: %+v", ivr)
	}
	if len(pers.rekeyCalls) != 0 {
		t.Fatalf("no hubo Rekey: %v", pers.rekeyCalls)
	}
}
