package notifications

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// Con Néctar la clave del pending es "nectar:<id>" y el teléfono del paciente viaja en ContactPhone.
// La nota al call center y las sesiones que crea el NotificationManager tienen que llevar ese
// teléfono, no la clave. Con Bird (sin ContactPhone) todo sigue exactamente igual.

const contactPhone = "+573001112233"

func TestNoResponseNote_NectarShowsContactPhone(t *testing.T) {
	mgr, bridge := nectarManager(t, &config.Config{BirdTeamFallback: "team-fallback"})
	mgr.escalateToAgent(&PendingNotification{
		Phone: nectarPhone, ContactPhone: contactPhone, AppointmentID: "APT001",
		Type: "confirmation", ConversationID: "777",
	}, escalationNoResponse)

	esc := bridge.only(t)
	if !strings.Contains(esc.Summary, ">> Tel: "+contactPhone+"\n") || strings.Contains(esc.Summary, "Tel: nectar:") {
		t.Fatalf("la nota lleva el teléfono a marcar, no la clave: %q", esc.Summary)
	}
}

func TestNoResponseNote_BirdUnchanged(t *testing.T) {
	notes := &birdNotes{}
	srv := notes.server(t)
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL),
		services.NewAppointmentService(&mockApptRepoNotif{}, nil), &config.Config{BirdTeamFallback: "team-fallback"})
	mgr.escalateToAgent(&PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT001", Type: "confirmation", ConversationID: "conv-1",
	}, escalationNoResponse)

	notes.mu.Lock()
	defer notes.mu.Unlock()
	if len(notes.texts) != 1 || !strings.Contains(notes.texts[0], ">> Tel: +573001234567\n") {
		t.Fatalf("la nota de Bird sigue con el teléfono de la clave: %q", notes.texts)
	}
}

// confirmManager arma el «Confirmar» de un recordatorio con un repositorio de sesiones que anota.
func confirmManager(t *testing.T) (*NotificationManager, *mockSessionCreator) {
	t.Helper()
	birdClient, srv := newTestBirdClient()
	t.Cleanup(srv.Close)
	apptRepo := &mockApptRepoNotif{
		findByIDFn: func(_ context.Context, id string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.ID = id
			return &appt, nil
		},
		confirmFn: func(context.Context, string, string, string) error { return nil },
	}
	mgr := NewNotificationManager(birdClient, services.NewAppointmentService(apptRepo, nil), &config.Config{})
	sessRepo := &mockSessionCreator{}
	mgr.SetWaitingListDeps(nil, sessRepo, &mockVirtualEnqueuer{})
	return mgr, sessRepo
}

func TestConfirmSession_NectarCarriesContact(t *testing.T) {
	mgr, sessRepo := confirmManager(t)
	mgr.RegisterPending(PendingNotification{
		Type: "confirmation", Phone: "nectar:77", ContactPhone: contactPhone, AppointmentID: "APT001",
	})
	mgr.HandleResponse("nectar:77", "confirm", "77")

	assertSessionContact(t, sessRepo, "573001112233")
}

func TestConfirmSession_BirdUnchanged(t *testing.T) {
	mgr, sessRepo := confirmManager(t)
	mgr.RegisterPending(PendingNotification{Type: "confirmation", Phone: "+573001234567", AppointmentID: "APT001"})
	mgr.HandleResponse("+573001234567", "confirm", "conv-1")

	assertSessionContact(t, sessRepo, "")
}

func wlScheduleManager(t *testing.T) (*NotificationManager, *mockSessionCreator) {
	t.Helper()
	birdClient, srv := newTestBirdClient()
	t.Cleanup(srv.Close)
	wlRepo := &mockWaitingListFinder{
		findByIDFn: func(context.Context, string) (*domain.WaitingListEntry, error) {
			return sampleWaitingListEntry(), nil
		},
	}
	mgr := NewNotificationManager(birdClient, nil, &config.Config{})
	sessRepo := &mockSessionCreator{}
	mgr.SetWaitingListDeps(wlRepo, sessRepo, &mockVirtualEnqueuer{})
	return mgr, sessRepo
}

func TestWaitingListScheduleSession_NectarCarriesContact(t *testing.T) {
	mgr, sessRepo := wlScheduleManager(t)
	mgr.RegisterPending(PendingNotification{
		Type: "waiting_list", Phone: "nectar:77", ContactPhone: contactPhone, WaitingListID: "wl-1",
	})
	mgr.HandleResponse("nectar:77", "wl_schedule", "77")

	assertSessionContact(t, sessRepo, "573001112233")
}

func TestWaitingListScheduleSession_BirdUnchanged(t *testing.T) {
	mgr, sessRepo := wlScheduleManager(t)
	mgr.RegisterPending(PendingNotification{Type: "waiting_list", Phone: "+573001234567", WaitingListID: "wl-1"})
	mgr.HandleResponse("+573001234567", "wl_schedule", "conv-1")

	assertSessionContact(t, sessRepo, "")
}

// assertSessionContact: la sesión creada lleva el contacto en la columna y en el contexto (E.164
// sin «+», como lo da Néctar); "" = ni columna ni clave de contexto.
func assertSessionContact(t *testing.T, sessRepo *mockSessionCreator, want string) {
	t.Helper()
	sessRepo.mu.Lock()
	defer sessRepo.mu.Unlock()
	if sessRepo.createdSession == nil {
		t.Fatal("se esperaba una sesión creada")
	}
	if got := sessRepo.createdSession.ContactIdentifier; got != want {
		t.Errorf("contact_identifier = %q, want %q", got, want)
	}
	got, ok := sessRepo.batchKVs[session.CtxContactIdentifier]
	if want == "" && ok {
		t.Errorf("con Bird el contexto no lleva contacto: %q", got)
	}
	if want != "" && got != want {
		t.Errorf("contexto %s = %q, want %q", session.CtxContactIdentifier, got, want)
	}
}
