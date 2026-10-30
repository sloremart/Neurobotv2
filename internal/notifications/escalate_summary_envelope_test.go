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
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// escalationBridge es el puente de Néctar de prueba: solo recuerda los sobres que le llegan.
type escalationBridge struct {
	mu        sync.Mutex
	escalated []bird.Escalation
	// fail, si no es nil, hace fallar Escalate (el sobre igual se anota).
	fail error
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
	return b.fail
}
func (b *escalationBridge) Close(context.Context, int64) error { return nil }
func (b *escalationBridge) ConversationState(context.Context, int64) (string, error) {
	return "", nil
}
func (b *escalationBridge) IsNotMine(error) bool { return false }

func (b *escalationBridge) OpenWithTemplate(context.Context, string, bird.TemplateConfig) (int64, string, error) {
	return 0, "", nil
}

func (b *escalationBridge) Contact(context.Context, int64) (bird.NectarContact, error) {
	return bird.NectarContact{}, nil
}

func (b *escalationBridge) WindowOpen(context.Context, int64) (bool, error) { return false, nil }

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

// registerNotifPendingMenu declara el menú de NOTIF_PENDING con la misma regla que
// handlers.notifPendingReturnOptions (aquí no se importa: handlers depende de repository/local,
// que importa este paquete). Las filas por tipo y motivo se prueban contra la función real en
// handlers/return_menus_test.go; aquí solo importa que la escalación pase la vista de la sesión.
func registerNotifPendingMenu(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { sm.UnregisterReturnStepForTest(sm.StateNotifPending) })
	np := sm.StateNotifPending
	sm.RegisterReturnMenu(np, func(sess *session.Session) []sm.ReturnOption {
		switch {
		case sess != nil && sess.GetContext(sm.CtxNotifEscalationDetail) == sm.EscalationReasonNotifSystemFailure:
			return []sm.ReturnOption{
				{Label: "Reintentar la confirmación en SIESA", Value: "confirm", Target: np, Data: "confirm"},
				{Label: "Ya la confirmé a mano en SIESA", Value: "confirmada_manual", Close: true},
			}
		case sess != nil && sess.GetContext(sm.CtxNotifEscalationDetail) == sm.EscalationReasonRescheduleNoProcedure:
			return []sm.ReturnOption{
				{Label: "Ya la reprogramé a mano en SIESA", Value: "reprogramada_manual", Close: true},
				{Label: "Entendido, sin reprogramar", Value: "understood", Target: np, Data: "understood"},
			}
		case sess != nil && sess.GetContext("notif_type") == "waiting_list":
			return []sm.ReturnOption{
				{Label: "Agendar ahora", Value: "wl_schedule", Target: np, Data: "wl_schedule"},
				{Label: "No le interesa", Value: "wl_decline", Target: np, Data: "wl_decline"},
			}
		case sess != nil && sess.GetContext("notif_type") == "cancellation":
			return []sm.ReturnOption{
				{Label: "Entendido, sin reprogramar", Value: "understood", Target: np, Data: "understood"},
				{Label: "Reprogramar", Value: "reschedule", Target: np, Data: "reschedule"},
			}
		}
		return []sm.ReturnOption{
			{Label: "Confirmar la cita", Value: "confirm", Target: np, Data: "confirm"},
			{Label: "Reprogramar la cita", Value: "reschedule", Target: np, Data: "reschedule"},
			{Label: "Cancelar la cita", Value: "cancel", Target: np, Data: "cancel"},
		}
	})
}

// descriptorOptions lee las opciones (valor y salida) del descriptor v1 que viajó en el sobre.
func descriptorOptions(t *testing.T, raw []byte) (values, exits []string) {
	t.Helper()
	var d struct {
		Opciones []struct {
			Valor  string `json:"valor"`
			Salida string `json:"salida"`
		} `json:"opciones"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("descriptor ilegible %s: %v", raw, err)
	}
	for _, o := range d.Opciones {
		values = append(values, o.Valor)
		exits = append(exits, o.Salida)
	}
	return values, exits
}

func nectarManager(t *testing.T, cfg *config.Config) (*NotificationManager, *escalationBridge) {
	t.Helper()
	registerNotifPendingMenu(t)
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
	if len(esc.Input) == 0 || string(esc.Input) != string(sm.StaticInputDescriptor(nil, sm.StateNotifPending)) {
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

// S26: el paciente SÍ confirmó pero falló la persistencia en SIESA. Al agente de Néctar le sirve
// reintentar o dar la cita por confirmada a mano (que cierra el caso), no re-pedir confirmación.
func TestConfirmationEscalate_SystemFailureOffersRetryAndManual(t *testing.T) {
	mgr, bridge := nectarManager(t, &config.Config{
		BirdTeamFallback: "team-fallback", ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
	})
	mgr.SetWaitingListDeps(nil, &mockSessionCreatorWithLookup{}, &mockVirtualEnqueuer{})

	mgr.escalateToAgent(&PendingNotification{Phone: nectarPhone, AppointmentID: "APT001", Type: "confirmation", ConversationID: "777"}, escalationSystemFailure)

	values, exits := descriptorOptions(t, bridge.only(t).Input)
	if len(values) < 2 || values[0] != "confirm" || values[1] != "confirmada_manual" {
		t.Fatalf("opciones = %v, deben empezar por confirm, confirmada_manual", values)
	}
	if exits[1] != "cerrar" {
		t.Errorf("confirmada_manual cierra el caso: salida %q", exits[1])
	}
}

// S24: la notificación de lista de espera ofrece agendar o declinar, y la sesión guarda el id de
// la entrada (sin él, «agendar» no sabría qué oferta tomar).
func TestEscalateNotifToAgent_WaitingListOffersScheduleAndDecline(t *testing.T) {
	mgr, bridge := nectarManager(t, &config.Config{
		BirdTeamFallback: "team-fallback", ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
	})
	repo := &mockSessionCreatorWithLookup{}
	mgr.SetWaitingListDeps(nil, repo, &mockVirtualEnqueuer{})

	mgr.escalateNotifToAgent(&PendingNotification{
		Phone: nectarPhone, AppointmentID: "APT001", Type: "waiting_list", WaitingListID: "wl-1",
	}, "777")

	values, _ := descriptorOptions(t, bridge.only(t).Input)
	if len(values) < 2 || values[0] != "wl_schedule" || values[1] != "wl_decline" {
		t.Fatalf("opciones = %v, deben empezar por wl_schedule, wl_decline", values)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.batchCtx[sm.CtxNotifWaitingListID] != "wl-1" {
		t.Errorf("notif_wl_id = %q, quiero wl-1", repo.batchCtx[sm.CtxNotifWaitingListID])
	}
	if repo.batchCtx[sm.CtxNotifEscalationDetail] != sm.EscalationReasonNotifFreeText {
		t.Errorf("notif_escalation_detail = %q", repo.batchCtx[sm.CtxNotifEscalationDetail])
	}
}

// El agente agenda la oferta de lista de espera: el comando llega con el id de la entrada (la
// notificación ya no está en memoria) y toma la oferta, no la trata como confirmación.
func TestHandleNotifPendingCommand_WaitingListSchedules(t *testing.T) {
	srv := escalationBirdServer()
	defer srv.Close()
	var foundID string
	wl := &mockWaitingListFinder{findByIDFn: func(_ context.Context, id string) (*domain.WaitingListEntry, error) {
		foundID = id
		return nil, nil
	}}
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL),
		services.NewAppointmentService(&mockApptRepoNotif{}, nil), &config.Config{})
	mgr.SetWaitingListDeps(wl, &mockSessionCreator{}, &mockVirtualEnqueuer{})

	mgr.HandleNotifPendingCommand(nectarPhone, "wl_schedule", "777", "", "waiting_list", "wl-1")

	if foundID != "wl-1" {
		t.Fatalf("FindByID(%q), quiero wl-1", foundID)
	}
}

// El escalado de una notificación lleva el candado de NOTIF_PENDING en Néctar (cita por
// resolver) y en Bird no viaja nada.
func TestNotifEscalation_CarriesResolveLock(t *testing.T) {
	sm.RegisterResolveGuard(sm.StateNotifPending, sm.ResolveGuardPendingAppointment)
	t.Cleanup(func() { sm.RegisterResolveGuard(sm.StateNotifPending, "") })

	for _, cfg := range []*config.Config{
		{ProtocolEscaladoVersion: config.ProtocolEscaladoV1}, {},
	} {
		esc := notifEscalation(cfg, nectarPhone, &session.Session{}, "detalle", "resumen")
		if esc.ResolveLock != "cita_pendiente" {
			t.Errorf("Néctar (protocolo %d): ResolveLock = %q, quiero cita_pendiente", cfg.ProtocolEscaladoVersion, esc.ResolveLock)
		}
	}
	if esc := notifEscalation(&config.Config{}, "+573001234567", &session.Session{}, "detalle", "resumen"); esc.ResolveLock != "" {
		t.Errorf("Bird no lleva candado: %q", esc.ResolveLock)
	}
}

// notifFactsView es la sesión escalada de una notificación con los datos de la cita.
func notifFactsView() *session.Session {
	return &session.Session{Context: map[string]string{
		"patient_name":         "Ana Pérez",
		"notif_appointment_id": "555",
		"notif_type":           "reminder",
		"notif_appt_date":      "lunes 2 de noviembre",
		"notif_appt_time":      "08:30",
		"notif_cups_name":      "RADIOGRAFIA DE TORAX",
		sm.CtxNotifCupsCode:    "883101",
		"phone":                "+573001234567",
	}}
}

// En Néctar v1 el sobre lleva los hechos de la cita y el equipo sugerido por el CUPS notificado.
func TestNotifEscalation_NectarV1CarriesFactsAndTeam(t *testing.T) {
	cfg := &config.Config{
		ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
		NectarTeamByGroup:       map[string]int64{"imagenes": 7},
	}
	esc := notifEscalation(cfg, nectarPhone, notifFactsView(), "detalle", "resumen")

	if esc.SuggestedTeamID == nil || *esc.SuggestedTeamID != 7 {
		t.Fatalf("SuggestedTeamID = %v, quiero 7", esc.SuggestedTeamID)
	}
	var groups []nectar.FactGroup
	if err := json.Unmarshal(esc.Facts, &groups); err != nil {
		t.Fatalf("facts no es una lista de grupos: %v (%s)", err, esc.Facts)
	}
	byKey := map[string]nectar.FactGroup{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	cita, ok := byKey["cita"]
	if !ok {
		t.Fatalf("falta el grupo cita: %s", esc.Facts)
	}
	got := map[string]string{}
	for _, f := range cita.Fields {
		got[f.Key] = f.Value
	}
	want := map[string]string{
		"id": "555", "fecha": "lunes 2 de noviembre", "hora": "08:30",
		"procedimiento": "RADIOGRAFIA DE TORAX", "tipo_notificacion": "reminder",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("cita.%s = %q, quiero %q", k, got[k], v)
		}
	}
	if p := byKey["paciente"]; len(p.Fields) != 1 || p.Fields[0].Key != "nombre" || p.Fields[0].Value != "Ana Pérez" {
		t.Errorf("grupo paciente = %+v", p)
	}
	if strings.Contains(string(esc.Facts), "573001234567") {
		t.Errorf("el teléfono no viaja en facts: %s", esc.Facts)
	}
}

// Con v0 no hay facts, pero el equipo sugerido viaja igual (existe desde antes de v1).
func TestNotifEscalation_V0NoFactsButTeam(t *testing.T) {
	cfg := &config.Config{NectarTeamByGroup: map[string]int64{"imagenes": 7}}
	esc := notifEscalation(cfg, nectarPhone, notifFactsView(), "detalle", "resumen")
	if esc.Facts != nil {
		t.Errorf("v0 no lleva facts: %s", esc.Facts)
	}
	if esc.SuggestedTeamID == nil || *esc.SuggestedTeamID != 7 {
		t.Errorf("SuggestedTeamID = %v, quiero 7", esc.SuggestedTeamID)
	}
}

// Bird conserva solo motivo y resumen.
func TestNotifEscalation_BirdOnlySummary(t *testing.T) {
	cfg := &config.Config{
		ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
		NectarTeamByGroup:       map[string]int64{"imagenes": 7},
	}
	esc := notifEscalation(cfg, "+573001234567", notifFactsView(), "detalle", "resumen")
	if esc.Facts != nil || esc.Input != nil || esc.SuggestedTeamID != nil {
		t.Errorf("Bird no lleva extras: facts=%s input=%s team=%v", esc.Facts, esc.Input, esc.SuggestedTeamID)
	}
	if esc.Summary != "resumen" {
		t.Errorf("Summary = %q", esc.Summary)
	}
}
