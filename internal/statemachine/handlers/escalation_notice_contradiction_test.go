package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Estos tests NO corrigen nada: DOCUMENTAN el texto que hoy recibe el paciente cuando una ruta
// escala llevando EscalationNoticeText y el auto-chain la lleva a un handler que NO escala.
// El aviso viaja como mensaje del resultado previo y machine.go lo antepone
// (`autoResult.Messages = append(prevMessages, autoResult.Messages...)`), así que el coalescer
// lo funde con el texto del destino: el paciente lee una promesa y su desmentido en un solo globo.
//
// Se ejecutan sobre el mismo camino que producción: handler real → auto-chain real → CoalesceMessages.

// stuckState simula un estado interactivo que agota reintentos (el patrón de helpers.go:57/120/146).
const stuckState = "TEST_STUCK_STATE"

// patientReads devuelve lo que el paciente ve de verdad: los mensajes del turno ya coalescidos.
func patientReads(msgs []sm.OutboundMessage) []string {
	out := []string{}
	for _, m := range sm.CoalesceMessages(msgs) {
		switch v := m.(type) {
		case *sm.TextMessage:
			out = append(out, v.Text)
		case *sm.ButtonMessage:
			out = append(out, v.Text)
		case *sm.ListMessage:
			out = append(out, v.Body)
		}
	}
	return out
}

// noAgentsServer: Bird sin agentes activos → el gate de escalateHandler bloquea.
func noAgentsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// El caso que el equipo de hdg-bot pidió provocar: reintentos agotados + gate sin asesores.
func TestNoticeContradiction_RetriesExhausted_GateBlocks(t *testing.T) {
	srv := noAgentsServer(t)

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)
	// Estado que agota reintentos exactamente como los helpers de producción.
	m.Register(stuckState, func(_ context.Context, s *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		return sm.ValidateWithRetry(s, msg.Text, func(string) bool { return false }, "Dato inválido, intenta de nuevo."), nil
	})

	sess := testSess(stuckState)
	sess.ConversationID = "conv-test"
	sess.RetryCount = 2 // el siguiente fallo es el tercero → escala

	result, err := m.Process(context.Background(), sess, textM("dato que no valida"))
	if err != nil {
		t.Fatal(err)
	}

	reads := patientReads(result.Messages)
	t.Logf("MENSAJES COBRADOS: %d", len(reads))
	for i, r := range reads {
		t.Logf("--- mensaje %d ---\n%s", i+1, r)
	}

	joined := strings.Join(reads, "\n@@@\n")
	promete := strings.Contains(joined, "Te voy a conectar con un agente")
	desmiente := strings.Contains(joined, "no hay asesores disponibles")
	if !promete || !desmiente {
		t.Fatalf("la contradicción documentada ya no se reproduce — si fue la decisión de producto del §1, invertir este test; si no, alguien cambió el auto-chain sin querer: %s", joined)
	}
	t.Log("CONTRADICCIÓN VIGENTE: el paciente lee la promesa de agente y su desmentido en el mismo globo.")
}

// ¿El aviso CUESTA un mensaje? El documento de hdg-bot supone que sí ("es un mensaje entero"),
// porque en los cuatro sitios es el único texto del resultado. Este test mide el coste REAL en el
// camino en que la escalación SÍ ocurre: si el coalescer lo funde con lo que produce el handler de
// escalación, quitarlo no ahorra ningún envío.
func TestNoticeCost_SuccessfulEscalation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","displayName":"A","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-test","feedId":"channel:ch-test","closed":false}]}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	defer srv.Close()

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)
	m.Register(stuckState, func(_ context.Context, s *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		return sm.ValidateWithRetry(s, msg.Text, func(string) bool { return false }, "Dato inválido."), nil
	})

	sess := testSess(stuckState)
	sess.ConversationID = "conv-test"
	sess.RetryCount = 2

	result, err := m.Process(context.Background(), sess, textM("dato que no valida"))
	if err != nil {
		t.Fatal(err)
	}

	conAviso := patientReads(result.Messages)
	// Mismo turno, pero SIN el aviso: es lo que se enviaría si se aplicara la propuesta del §1.
	sinAviso := []sm.OutboundMessage{}
	for _, msg := range result.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok && tm.Text == sm.EscalationNoticeText {
			continue
		}
		sinAviso = append(sinAviso, msg)
	}
	sinAvisoReads := patientReads(sinAviso)

	t.Logf("con aviso: %d envío(s) cobrado(s)", len(conAviso))
	for i, r := range conAviso {
		t.Logf("  [%d] %s", i+1, r)
	}
	t.Logf("sin aviso: %d envío(s) cobrado(s)", len(sinAvisoReads))

	// Medido: en la escalación que SÍ ocurre el aviso es el único texto del turno → quitarlo ahorra
	// un envío entero. (En el camino del gate, en cambio, el coalescer ya lo funde: ahorro cero.)
	if len(conAviso) != 1 || len(sinAvisoReads) != 0 {
		t.Fatalf("coste del aviso cambiado: con=%d sin=%d (esperado 1 y 0)", len(conAviso), len(sinAvisoReads))
	}
}

// fakeRecovery simula la capa de recuperación IA arrancando y devolviendo su mensaje aclaratorio,
// que es lo que hace en producción 68 veces / 7d (flow_events recuperacion/ai_recovery_started).
type fakeRecovery struct{ clarify string }

func (f *fakeRecovery) Active(*session.Session) bool { return false }
func (f *fakeRecovery) Handle(context.Context, *session.Session, bird.InboundMessage) (*sm.StateResult, bool) {
	return nil, false
}

func (f *fakeRecovery) TryStart(_ context.Context, _ *session.Session, _ bird.InboundMessage, blockedState string) (*sm.StateResult, bool) {
	return sm.NewResult(blockedState).WithText(f.clarify), true
}

// La contradicción NO se limita al gate: cuando la recuperación IA arranca, el paciente ya leyó la
// promesa de agente y a continuación el bot sigue preguntándole. Es el camino MÁS frecuente
// (ai_recovery_started 68/7d frente a los ai_failed 16/7d que sí acaban en agente).
func TestNoticeContradiction_AIRecoveryStarts(t *testing.T) {
	srv := noAgentsServer(t)

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)
	m.SetRecoveryCoordinator(&fakeRecovery{clarify: "Para continuar, ¿me confirmas tu número de documento?"})
	m.Register(stuckState, func(_ context.Context, s *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		return sm.ValidateWithRetry(s, msg.Text, func(string) bool { return false }, "Dato inválido."), nil
	})

	sess := testSess(stuckState)
	sess.ConversationID = "conv-test"
	sess.RetryCount = 2

	result, err := m.Process(context.Background(), sess, textM("dato que no valida"))
	if err != nil {
		t.Fatal(err)
	}

	reads := patientReads(result.Messages)
	for i, r := range reads {
		t.Logf("--- mensaje %d ---\n%s", i+1, r)
	}
	joined := strings.Join(reads, "\n")
	if !strings.Contains(joined, "Te voy a conectar con un agente") || !strings.Contains(joined, "confirmas tu número de documento") {
		t.Fatalf("la contradicción de la recuperación IA ya no se reproduce — invertir el test si fue la decisión de producto: %s", joined)
	}
	t.Log("CONTRADICCIÓN VIGENTE (recuperación IA): se promete agente y acto seguido el bot sigue preguntando.")
}
