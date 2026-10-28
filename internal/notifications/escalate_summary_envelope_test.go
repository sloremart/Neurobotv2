package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// escalationBridge es el puente de Néctar de prueba: solo recuerda los sobres que le llegan.
type escalationBridge struct {
	mu        sync.Mutex
	escalated []bird.Escalation
}

func (b *escalationBridge) SendText(context.Context, int64, string) (string, error) { return "m", nil }
func (b *escalationBridge) SendButtons(context.Context, int64, string, []bird.Button) (string, error) {
	return "m", nil
}

func (b *escalationBridge) SendList(context.Context, int64, string, string, []bird.ListSection) (string, error) {
	return "m", nil
}

func (b *escalationBridge) SendTemplate(context.Context, int64, bird.TemplateConfig) (string, error) {
	return "m", nil
}

func (b *escalationBridge) Available(context.Context, int64) (bool, string, error) {
	return true, "", nil
}

func (b *escalationBridge) Escalate(_ context.Context, _ int64, esc bird.Escalation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.escalated = append(b.escalated, esc)
	return nil
}
func (b *escalationBridge) Close(context.Context, int64) error { return nil }
func (b *escalationBridge) ConversationState(context.Context, int64) (string, error) {
	return "", nil
}
func (b *escalationBridge) IsNotMine(error) bool { return false }

func (b *escalationBridge) only(t *testing.T) bird.Escalation {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.escalated) != 1 {
		t.Fatalf("esperaba 1 escalación en el puente, hubo %d", len(b.escalated))
	}
	return b.escalated[0]
}

const nectarPhone = "nectar:777"

// registerNotifPendingMenu declara el menú estático de NOTIF_PENDING igual que lo hace
// handlers.RegisterAppointmentHandlers (aquí no se importa: handlers no es dependencia de este paquete).
func registerNotifPendingMenu() {
	sm.RegisterStaticReturnMenu(sm.StateNotifPending,
		sm.ReturnOption{Label: "Confirmar la cita", Value: "confirm", Target: sm.StateNotifPending, Data: "confirm"},
		sm.ReturnOption{Label: "Reprogramar la cita", Value: "reschedule", Target: sm.StateNotifPending, Data: "reschedule"},
		sm.ReturnOption{Label: "Cancelar la cita", Value: "cancel", Target: sm.StateNotifPending, Data: "cancel"},
	)
}

func nectarManager(t *testing.T, cfg *config.Config) (*NotificationManager, *escalationBridge) {
	t.Helper()
	registerNotifPendingMenu()
	srv := escalationBirdServer()
	t.Cleanup(srv.Close)
	client := bird.NewClientForTest(srv.URL)
	bridge := &escalationBridge{}
	client.SetNectar(bridge)
	return NewNotificationManager(client, services.NewAppointmentService(&mockApptRepoNotif{}, nil), cfg), bridge
}

// birdNotes registra los textos de las notas internas que llegan al Inbox de Bird.
type birdNotes struct {
	mu    sync.Mutex
	texts []string
}

func (n *birdNotes) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/") && strings.HasSuffix(r.URL.Path, "/messages"):
			raw, _ := io.ReadAll(r.Body)
			var payload struct {
				Draft bool `json:"draft"`
				Body  struct {
					Text struct {
						Text string `json:"text"`
					} `json:"text"`
				} `json:"body"`
			}
			_ = json.Unmarshal(raw, &payload)
			if payload.Draft { // las notas internas son borradores; los textos al paciente no
				n.mu.Lock()
				n.texts = append(n.texts, payload.Body.Text.Text)
				n.mu.Unlock()
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"note-1"}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/agents"):
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","teams":[{"id":"team-fallback"}],"availability":{"status":"active"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-1","feedId":"channel:ch-test","closed":false}]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEscalateNotifToAgent_UsesSummaryEnvelope(t *testing.T) {
	mgr, bridge := nectarManager(t, &config.Config{BirdTeamFallback: "team-fallback"})

	mgr.escalateNotifToAgent(&PendingNotification{
		Phone: nectarPhone, AppointmentID: "APT001", Type: "confirmation",
	}, "777")

	esc := bridge.only(t)
	if !strings.Contains(esc.Summary, "Cita ID: APT001") || !strings.Contains(esc.Summary, "Requiere gestion manual del agente.") {
		t.Errorf("el resumen del sobre debe ser la nota del caso, got %q", esc.Summary)
	}
	if esc.Reason != nectar.ReasonBotCannotAnswer {
		t.Errorf("Reason = %q", esc.Reason)
	}
}

// Con el dialecto de hoy (v0) el agente de Néctar recibe el menú de retornos y no el descriptor.
func TestEscalateNotifToAgent_NectarV0CarriesReturnMenu(t *testing.T) {
	mgr, bridge := nectarManager(t, &config.Config{BirdTeamFallback: "team-fallback"})
	mgr.escalateNotifToAgent(&PendingNotification{Phone: nectarPhone, AppointmentID: "APT001", Type: "confirmation"}, "777")

	esc := bridge.only(t)
	if esc.Input != nil || esc.Protocol != 0 || esc.ReasonDetail != "" {
		t.Errorf("v0 no lleva descriptor, protocolo ni detalle: %+v", esc)
	}
	want, _ := json.Marshal(sm.BuildReturnMenu(nil, sm.StateNotifPending))
	if string(esc.ReturnMenu) != string(want) {
		t.Errorf("ReturnMenu = %s, quiero %s", esc.ReturnMenu, want)
	}
	if !strings.Contains(string(esc.ReturnMenu), `"confirm"`) || !strings.Contains(string(esc.ReturnMenu), `"cancel"`) {
		t.Errorf("el menú debe ofrecer las acciones de NOTIF_PENDING: %s", esc.ReturnMenu)
	}
}

// Con v1 el descriptor `input` SUSTITUYE al menú y viaja el detalle del motivo.
func TestEscalateNotifToAgent_NectarV1CarriesInputDescriptor(t *testing.T) {
	mgr, bridge := nectarManager(t, &config.Config{
		BirdTeamFallback: "team-fallback", ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
	})
	mgr.escalateNotifToAgent(&PendingNotification{Phone: nectarPhone, AppointmentID: "APT001", Type: "confirmation"}, "777")

	esc := bridge.only(t)
	if esc.Protocol != config.ProtocolEscaladoV1 || esc.ReasonDetail != "confirmacion_texto_libre" {
		t.Errorf("sobre v1 incompleto: protocolo %d, detalle %q", esc.Protocol, esc.ReasonDetail)
	}
	if esc.ReturnMenu != nil {
		t.Errorf("con v1 el menú no viaja: %s", esc.ReturnMenu)
	}
	if len(esc.Input) == 0 || string(esc.Input) != string(sm.StaticInputDescriptor(sm.StateNotifPending)) {
		t.Errorf("Input = %s", esc.Input)
	}
}

// En Bird el sobre se degrada a lo de siempre: la nota del caso (ahora tras asignar al agente)
// más la nota de comandos, sin menú ni descriptor (Bird no los tiene).
func TestEscalateNotifToAgent_BirdKeepsBothNotes(t *testing.T) {
	notes := &birdNotes{}
	srv := notes.server(t)
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL), services.NewAppointmentService(&mockApptRepoNotif{}, nil), &config.Config{BirdTeamFallback: "team-fallback"})

	mgr.escalateNotifToAgent(&PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT001", Type: "confirmation",
	}, "conv-1")

	notes.mu.Lock()
	defer notes.mu.Unlock()
	var caseNote, commands int
	for _, txt := range notes.texts {
		switch {
		case strings.HasPrefix(txt, "Paciente envio texto libre repetidamente"):
			caseNote++
		case strings.HasPrefix(txt, "Comandos disponibles:") && strings.Contains(txt, "/bot resume NOTIF_PENDING confirm"):
			commands++
		}
	}
	if caseNote != 1 || commands != 1 {
		t.Errorf("Bird debe recibir 1 nota del caso y 1 de comandos; got caso=%d comandos=%d (%q)", caseNote, commands, notes.texts)
	}
}

func TestConfirmationEscalate_UsesSummaryEnvelope(t *testing.T) {
	mgr, bridge := nectarManager(t, &config.Config{BirdTeamFallback: "team-fallback"})
	pending := &PendingNotification{Phone: nectarPhone, AppointmentID: "APT001", Type: "confirmation", ConversationID: "777"}

	mgr.escalateToAgent(pending, escalationSystemFailure)

	esc := bridge.only(t)
	if !strings.Contains(esc.Summary, "la persistencia en SIESA FALLÓ") || !strings.Contains(esc.Summary, "APT001") {
		t.Errorf("el resumen debe ser la nota del fallo de sistema, got %q", esc.Summary)
	}
	if len(esc.ReturnMenu) == 0 {
		t.Error("v0 en Néctar debe llevar el menú de retornos")
	}
}

func TestConfirmationEscalate_V1DetailPerReason(t *testing.T) {
	for reason, want := range map[escalationReason]string{
		escalationNoResponse:    "notificacion_sin_respuesta",
		escalationSystemFailure: "notificacion_fallo_sistema",
	} {
		mgr, bridge := nectarManager(t, &config.Config{
			BirdTeamFallback: "team-fallback", ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
		})
		mgr.escalateToAgent(&PendingNotification{Phone: nectarPhone, AppointmentID: "APT001", Type: "confirmation", ConversationID: "777"}, reason)
		esc := bridge.only(t)
		if esc.ReasonDetail != want || len(esc.Input) == 0 || esc.ReturnMenu != nil {
			t.Errorf("reason %v: detalle %q, input %d bytes, menú %s", reason, esc.ReasonDetail, len(esc.Input), esc.ReturnMenu)
		}
	}
}

// En Bird la nota del caso sigue llegando como nota interna (ahora tras asignar al agente).
func TestConfirmationEscalate_BirdSendsSummaryAsNote(t *testing.T) {
	notes := &birdNotes{}
	srv := notes.server(t)
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL), services.NewAppointmentService(&mockApptRepoNotif{}, nil), &config.Config{BirdTeamFallback: "team-fallback"})

	mgr.escalateToAgent(&PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT001", Type: "confirmation", ConversationID: "conv-1",
	}, escalationNoResponse)

	notes.mu.Lock()
	defer notes.mu.Unlock()
	if len(notes.texts) != 1 || !strings.Contains(notes.texts[0], "APT001") {
		t.Errorf("Bird debe recibir exactamente la nota del caso, got %q", notes.texts)
	}
}
