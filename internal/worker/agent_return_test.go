package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/handlers"
)

// Retorno del agente desde Néctar: el token que eligió (o el dato que escribió) se traduce
// AQUÍ, contra el paso desde el que se ofreció el menú. Lo que se fija es que la rama elegida
// es la que se ejecuta, y que un token que no se puede traducir NO inventa una.

const (
	testReturnStep   = "PASO_DE_VUELTA_7C"
	testNectarPhone  = "nectar:12"
	interstitialText = "Hemos retomado tu atención"
)

// returnSession deja una sesión escalada en `step` y devuelve a dónde se reanudó y qué mensaje
// virtual entró al flujo: esas dos cosas SON la rama elegida (el estado final no sirve, porque
// después de reanudar el flujo sigue avanzando).
func returnSession(t *testing.T, step string) (*MessageWorkerPool, *mockSessionMgmt, *mockMessageSender, *session.Session, *string, *bird.InboundMessage) {
	t.Helper()
	pool, sm, sender, sess := escalatedPool(t)
	sess.Context["pre_escalation_state"] = step
	resumedTo := new(string)
	sm.resumeFn = func(_ context.Context, s *session.Session, target string) error {
		*resumedTo = target
		s.Status = session.StatusActive
		s.CurrentState = target
		return nil
	}
	processed := new(bird.InboundMessage)
	pool.machine = &mockMessageProcessor{
		processFn: func(_ context.Context, _ *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			*processed = msg
			return &statemachine.StateResult{NextState: step}, nil
		},
	}
	return pool, sm, sender, sess, resumedTo, processed
}

// asNectar convierte la sesión del pool en una de Néctar (su clave ocupa el sitio del teléfono).
func asNectar(sess *session.Session) {
	sess.PhoneNumber = testNectarPhone
}

func sentTexts(s *mockMessageSender) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		out = append(out, m.text)
	}
	return out
}

func TestHandleAgentResume_OptionTargetAndData(t *testing.T) {
	statemachine.RegisterStaticReturnMenu(testReturnStep,
		statemachine.ReturnOption{
			Label: "Cita agendada, continúa", Value: "cita_ok",
			Target: "OTRO_PASO", Data: "dato_de_la_opcion",
		})
	t.Cleanup(func() { statemachine.UnregisterReturnStepForTest(testReturnStep) })

	pool, _, _, sess, resumedTo, processed := returnSession(t, testReturnStep)
	asNectar(sess)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: "cita_ok",
	})

	if *resumedTo != "OTRO_PASO" {
		t.Fatalf("se retoma en el estado de la opción elegida, got %q", *resumedTo)
	}
	if processed.Text != "dato_de_la_opcion" {
		t.Fatalf("la decisión entra al flujo como el dato de la opción, got %q", processed.Text)
	}
}

// Un token que no pertenece al paso (menú viejo, o el paso cambió) reanuda SIN decisión: en el
// paso de siempre, con __resume__, y el token no llega a ninguna parte.
func TestHandleAgentResume_UnknownTokenResumesWithoutDecision(t *testing.T) {
	pool, _, sender, sess, resumedTo, processed := returnSession(t, testReturnStep)
	asNectar(sess)
	const token = "token_que_ya_no_existe"

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: token,
	})

	if *resumedTo != testReturnStep {
		t.Fatalf("se retoma en el paso de la escalación, got %q", *resumedTo)
	}
	if processed.Text != "__resume__" {
		t.Fatalf("sin decisión traducible se re-promptea el paso, sin inyectar dato: %q", processed.Text)
	}
	for _, txt := range sentTexts(sender) {
		if strings.Contains(txt, token) {
			t.Fatalf("el token desconocido no puede llegar al paciente: %q", txt)
		}
	}
}

func TestHandleAgentResume_WrittenValueInjected(t *testing.T) {
	pool, _, _, sess, resumedTo, processed := returnSession(t, "ASK_DOCUMENT")
	asNectar(sess)
	pool.machine.(*mockMessageProcessor).returnInput = statemachine.ReturnInputResult{
		Written: true, Value: "1234567890", Target: "OTRO_PASO",
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnInputPrefix + "1234567890",
	})

	if *resumedTo != "OTRO_PASO" {
		t.Fatalf("el dato escrito se inyecta en el Target de la entrada, got %q", *resumedTo)
	}
	if processed.Text != "1234567890" {
		t.Fatalf("el dato escrito entra al flujo tal cual, got %q", processed.Text)
	}
}

// Un paso cuya entrada declara Command entrega el dato al comando del asesor (aquí, /bot cups),
// no lo inyecta como mensaje virtual.
func TestHandleAgentResume_WrittenCupsGoesToHandleAgentCups(t *testing.T) {
	pool, _, sender, sess, resumedTo, processed := returnSession(t, statemachine.StateUploadMedicalOrder)
	asNectar(sess)
	pool.machine.(*mockMessageProcessor).returnInput = statemachine.ReturnInputResult{
		Written: true, Value: "883141:2 930810", Command: statemachine.ReturnCommandCups,
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnInputPrefix + "883141:2 930810",
	})

	if *resumedTo != statemachine.StateValidateOCR {
		t.Fatalf("el comando cups reanuda en VALIDATE_OCR, got %q", *resumedTo)
	}
	var cups []services.CUPSEntry
	if err := json.Unmarshal([]byte(sess.GetContext("ocr_cups_json")), &cups); err != nil {
		t.Fatalf("ocr_cups_json ilegible: %v (%q)", err, sess.GetContext("ocr_cups_json"))
	}
	if len(cups) != 2 || cups[0].Code != "883141" || cups[0].Quantity != 2 || cups[1].Code != "930810" {
		t.Fatalf("los CUPS escritos no llegaron a handleAgentCups: %+v", cups)
	}
	if processed.Text == "883141:2 930810" {
		t.Fatal("el dato de un comando no se inyecta como mensaje virtual")
	}
	for _, txt := range sentTexts(sender) {
		if strings.Contains(txt, interstitialText) {
			t.Fatalf("en Néctar no se acusa el retorno: %q", txt)
		}
	}
}

// Un dato escrito que no sirve vuelve a una PERSONA con el motivo; al paciente no le llega nada.
func TestHandleAgentResume_RejectedReescalates(t *testing.T) {
	pool, sm, sender, sess, resumedTo, _ := returnSession(t, "ASK_DOCUMENT")
	asNectar(sess)
	db := persistedContext(sm)
	const value, problem = "12ab", "El documento solo lleva números."
	persistedAtProcess := ""
	var processed bird.InboundMessage
	pool.machine = &mockMessageProcessor{
		returnInput: statemachine.ReturnInputResult{
			Written: true, Value: value, Problem: problem, Field: "documento",
		},
		processFn: func(_ context.Context, _ *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			processed = msg
			persistedAtProcess = db[statemachine.CtxAgentInputRejected]
			return &statemachine.StateResult{NextState: statemachine.StateEscalated}, nil
		},
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnInputPrefix + value,
	})

	if *resumedTo != statemachine.StateEscalateToAgent {
		t.Fatalf("el dato rechazado re-escala, got %q", *resumedTo)
	}
	// Todo se lee de lo PERSISTIDO: el siguiente comando recarga la sesión de la BD.
	if got := db[statemachine.CtxAgentInputRejected]; got != "1" {
		t.Errorf("CtxAgentInputRejected persistido = %q, esperaba \"1\"", got)
	}
	if persistedAtProcess != "1" {
		t.Errorf("el contexto se persiste ANTES del turno que re-escala, got %q", persistedAtProcess)
	}
	if got, want := db["escalation_note"], statemachine.RejectedInputNote(1, value, problem); got != want {
		t.Errorf("escalation_note = %q, esperaba %q", got, want)
	}
	if got := db[statemachine.CtxPreAutoState]; got != "ASK_DOCUMENT" {
		t.Errorf("el paso bloqueado sigue siendo el de origen, got %q", got)
	}
	if got := sess.GetContext(statemachine.CtxPreAutoState); got != "ASK_DOCUMENT" {
		t.Errorf("el turno de re-escalada ve el paso de origen en memoria, got %q", got)
	}
	rej, ok := statemachine.DecodeRejection(db[statemachine.CtxAgentInputRejection])
	if !ok || rej.Reason != problem || rej.Attempt != 1 || rej.Echo != value || rej.Field != "documento" {
		t.Errorf("rechazo tipado = %+v (ok=%v)", rej, ok)
	}
	if processed.Text == "__resume__" || processed.Text == value {
		t.Errorf("el turno de re-escalada no puede ser __resume__ (anti-bucle) ni el dato: %q", processed.Text)
	}
	if n := len(sentTexts(sender)); n != 0 {
		t.Errorf("el paciente no recibe nada, got %d mensajes: %v", n, sentTexts(sender))
	}
}

// En Néctar el retorno no se acusa con «Hemos retomado…»: el mensaje de verdad sale en el mismo
// turno. En Bird sigue saliendo igual que hoy.
func TestHandleAgentResume_NectarNoInterstitialText(t *testing.T) {
	statemachine.RegisterStaticReturnMenu(testReturnStep,
		statemachine.ReturnOption{Label: "Con dato", Value: "con_dato", Data: "CC"})
	t.Cleanup(func() { statemachine.UnregisterReturnStepForTest(testReturnStep) })

	for name, cmd := range map[string]AgentCommand{
		"sin dato": {Action: "resume", ReturnValue: statemachine.ReturnResume},
		"con dato": {Action: "resume", ReturnValue: "con_dato"},
	} {
		t.Run("nectar "+name, func(t *testing.T) {
			pool, _, sender, sess, _, _ := returnSession(t, testReturnStep)
			asNectar(sess)
			cmd.Phone = sess.PhoneNumber
			pool.processAgentCommand(context.Background(), cmd)
			for _, txt := range sentTexts(sender) {
				if strings.Contains(txt, interstitialText) {
					t.Fatalf("en Néctar no se acusa el retorno: %q", txt)
				}
			}
		})
	}

	t.Run("bird sin cambios", func(t *testing.T) {
		pool, _, sender, sess, _, _ := returnSession(t, testReturnStep)
		pool.processAgentCommand(context.Background(), AgentCommand{Action: "resume", Phone: sess.PhoneNumber})
		got := sentTexts(sender)
		if len(got) == 0 || got[0] != "Hemos retomado tu atención. Continuamos con tu proceso." {
			t.Fatalf("Bird conserva el acuse de retorno, got %v", got)
		}
	})
	t.Run("bird con dato sin cambios", func(t *testing.T) {
		pool, _, sender, sess, _, _ := returnSession(t, testReturnStep)
		pool.processAgentCommand(context.Background(), AgentCommand{
			Action: "resume", Phone: sess.PhoneNumber, State: testReturnStep, Data: "1234",
		})
		got := sentTexts(sender)
		if len(got) == 0 || got[0] != "Hemos retomado tu atención. Procesando tu información..." {
			t.Fatalf("Bird conserva el acuse de retorno con dato, got %v", got)
		}
	})
}

// conversacion_resuelta: el agente ya cerró con el paciente en Néctar, así que el bot ni se
// despide ni cierra: la conversación ya no es suya y los dos envíos morirían en 404.
func TestHandleAgentClose_NectarResolvedSkipsFarewell(t *testing.T) {
	const farewell = "Tu consulta ha sido resuelta. Gracias por comunicarte con nosotros!"

	t.Run("nectar resuelta", func(t *testing.T) {
		pool, sm, sender, sess := escalatedPool(t)
		asNectar(sess)
		completed := false
		sm.completeFn = func(_ context.Context, s *session.Session) error {
			completed = true
			s.Status = session.StatusCompleted
			return nil
		}

		pool.processAgentCommand(context.Background(), AgentCommand{
			Action: "close", Phone: sess.PhoneNumber, Reason: CloseReasonResolved,
		})

		if !completed {
			t.Fatal("la sesión se completa")
		}
		if n := len(sentTexts(sender)); n != 0 {
			t.Fatalf("resuelta en Néctar: el bot no se despide, got %v", sentTexts(sender))
		}
		sender.mu.Lock()
		closed := append([]string(nil), sender.closedConvs...)
		sender.mu.Unlock()
		if len(closed) != 0 {
			t.Fatalf("resuelta en Néctar: el bot no cierra una conversación que ya no es suya, got %v", closed)
		}
	})

	t.Run("nectar cerrar desde el menú se despide", func(t *testing.T) {
		pool, _, sender, sess := escalatedPool(t)
		asNectar(sess)
		pool.processAgentCommand(context.Background(), AgentCommand{
			Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnClose,
		})
		got := sentTexts(sender)
		if len(got) != 1 || got[0] != farewell {
			t.Fatalf("cerrar desde el menú de retornos sí se despide, got %v", got)
		}
		sender.mu.Lock()
		closed := len(sender.closedConvs)
		sender.mu.Unlock()
		if closed != 1 {
			t.Fatalf("en Néctar el cierre es síncrono, got %d cierres", closed)
		}
	})

	t.Run("bird sin cambios", func(t *testing.T) {
		pool, _, sender, sess := escalatedPool(t)
		pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: sess.PhoneNumber})
		got := sentTexts(sender)
		if len(got) != 1 || got[0] != farewell {
			t.Fatalf("Bird conserva la despedida, got %v", got)
		}
		sender.mu.Lock()
		closed := len(sender.closedConvs)
		sender.mu.Unlock()
		if closed != 0 {
			t.Fatalf("en Bird el cierre sigue aplazado 3 s, got %d cierres inmediatos", closed)
		}
	})
}

// El no-show no existe para Néctar: la cola la administra Néctar, no el checker del bot.
func TestHandleAgentNoShow_IgnoresNectar(t *testing.T) {
	pool, sm, sender, sess := escalatedPool(t)
	asNectar(sess)
	resumed := false
	sm.resumeNoShowFn = func(context.Context, *session.Session, string) error {
		resumed = true
		return nil
	}

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "no_show", Phone: sess.PhoneNumber})

	if resumed {
		t.Fatal("una sesión de Néctar no se reanuda por no-show")
	}
	if n := len(sentTexts(sender)); n != 0 {
		t.Fatalf("no se le escribe al paciente, got %v", sentTexts(sender))
	}
	if sess.Status != session.StatusEscalated {
		t.Fatalf("la sesión sigue escalada, got %v", sess.Status)
	}
}

// persistedContext simula la tabla de contexto: SaveState aplica updateCtx/clearCtx en la «BD» y
// en memoria, como el SessionManager real. Lo que no pasa por aquí no sobrevive a la recarga de
// la sesión que hace cada comando de agente.
func persistedContext(sm *mockSessionMgmt) map[string]string {
	db := map[string]string{}
	sm.saveFn = func(_ context.Context, s *session.Session, state string, upd map[string]string, clr []string) error {
		s.CurrentState = state
		for _, k := range clr {
			delete(db, k)
			delete(s.Context, k)
		}
		for k, v := range upd {
			db[k] = v
			s.SetContext(k, v)
		}
		return nil
	}
	return db
}

// captureLogs desvía slog a un búfer mientras dura el test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Cada comando de agente recarga la sesión de la BD: el número de intento solo avanza si la
// re-escalada lo persistió.
func TestReescalate_AttemptSurvivesSessionReload(t *testing.T) {
	pool, sm, _, _ := escalatedPool(t)
	db := persistedContext(sm)
	db["pre_escalation_state"] = "ASK_DOCUMENT"
	sm.findOrCreateFn = func(context.Context, string) (*session.Session, bool, error) {
		ctx := make(map[string]string, len(db))
		for k, v := range db {
			ctx[k] = v
		}
		return &session.Session{
			ID: "s1", PhoneNumber: testNectarPhone, Status: session.StatusEscalated,
			ConversationID: "conv1", CurrentState: statemachine.StateEscalated, Context: ctx,
		}, false, nil
	}
	pool.SetDependencies(sm, pool.birdClient, &mockMessageProcessor{
		returnInput: statemachine.ReturnInputResult{Written: true, Value: "12ab", Problem: "Solo números."},
	})

	for range 2 {
		pool.processAgentCommand(context.Background(), AgentCommand{
			Action: "resume", Phone: testNectarPhone, ReturnValue: statemachine.ReturnInputPrefix + "12ab",
		})
	}

	if got := db[statemachine.CtxAgentInputRejected]; got != "2" {
		t.Fatalf("el segundo rechazo sobre la sesión recargada es el intento 2, got %q", got)
	}
	rej, ok := statemachine.DecodeRejection(db[statemachine.CtxAgentInputRejection])
	if !ok || rej.Attempt != 2 {
		t.Fatalf("el rechazo tipado persistido lleva el intento 2, got %+v (ok=%v)", rej, ok)
	}
	if want := statemachine.RejectedInputNote(2, "12ab", "Solo números."); db["escalation_note"] != want {
		t.Fatalf("escalation_note = %q, esperaba %q", db["escalation_note"], want)
	}
}

// Un /bot cups que no puede con lo escrito avisa con una nota interna, que en Néctar no tiene
// destino: el fallo vuelve al agente como dato rechazado, con el motivo.
func TestHandleAgentResume_NectarCupsCommandFailureReescalates(t *testing.T) {
	pool, sm, sender, sess, resumedTo, _ := returnSession(t, statemachine.StateUploadMedicalOrder)
	asNectar(sess)
	db := persistedContext(sm)
	pool.machine.(*mockMessageProcessor).returnInput = statemachine.ReturnInputResult{
		Written: true, Value: ":2", Command: statemachine.ReturnCommandCups,
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnInputPrefix + ":2",
	})

	if *resumedTo != statemachine.StateEscalateToAgent {
		t.Fatalf("el comando fallido re-escala, got %q", *resumedTo)
	}
	if !strings.Contains(db["escalation_note"], "No se encontraron códigos CUPS válidos") {
		t.Fatalf("la nota lleva el motivo del comando, got %q", db["escalation_note"])
	}
	if db[statemachine.CtxAgentInputRejected] != "1" {
		t.Fatalf("cuenta como un rechazo, got %q", db[statemachine.CtxAgentInputRejected])
	}
	if n := countSent(sender, "text"); n != 0 {
		t.Fatalf("el paciente no recibe nada, got %d", n)
	}
}

// En Bird el /bot cups fallido sigue igual que hoy: nota interna y la sesión queda escalada.
func TestAgentCommand_CupsFailureUnchangedForBird(t *testing.T) {
	pool, sm, sender, sess := escalatedPool(t)
	resumed := false
	sm.resumeFn = func(context.Context, *session.Session, string) error { resumed = true; return nil }

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "cups", Phone: sess.PhoneNumber, Data: ":2"})

	sender.mu.Lock()
	sent := append([]sentMsg(nil), sender.sent...)
	sender.mu.Unlock()
	if len(sent) != 1 || sent[0].msgType != "internal" || sent[0].text != "No se encontraron codigos CUPS validos en el comando." {
		t.Fatalf("Bird conserva la nota interna, got %+v", sent)
	}
	if resumed || sess.Status != session.StatusEscalated {
		t.Fatalf("la sesión de Bird sigue escalada para que el agente corrija, resumed=%v status=%v", resumed, sess.Status)
	}
}

// Si la despedida de Néctar no sale, queda en el registro.
func TestCloseNectarConversation_LogsFarewellError(t *testing.T) {
	logs := captureLogs(t)
	pool, _, sender, sess := escalatedPool(t)
	asNectar(sess)
	sender.sendErr = errors.New("envío caído")

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnClose,
	})

	if !strings.Contains(logs.String(), "farewell on agent close failed") || !strings.Contains(logs.String(), "envío caído") {
		t.Fatalf("el fallo de la despedida se registra:\n%s", logs.String())
	}
}

// La `opcion` de un retorno v1 se traza por ReturnValueForLog: si trae un dato escrito, solo su
// prefijo.
func TestResolveAgentReturn_V1OptionLoggedByPrefix(t *testing.T) {
	logs := captureLogs(t)
	pool, _, _, sess, _, _ := returnSession(t, "ASK_DOCUMENT")
	asNectar(sess)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber,
		ReturnV1: &nectar.V1Return{Option: statemachine.ReturnInputPrefix + "1000000001"},
	})

	if strings.Contains(logs.String(), "1000000001") {
		t.Fatalf("el dato escrito no puede aparecer en el registro:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), statemachine.ReturnInputPrefix) {
		t.Fatalf("el prefijo sí se traza:\n%s", logs.String())
	}
}

// notifResponderSpy anota las acciones de NOTIF_PENDING que el worker le entrega.
type notifResponderSpy struct {
	actions   []string
	convID    string
	apptID    string
	notifType string
	wlID      string
}

func (s *notifResponderSpy) HandleResponse(string, string, string) {}

func (s *notifResponderSpy) HandleNotifPendingCommand(_, action, convID, appointmentID, notifType, waitingListID string) {
	s.actions = append(s.actions, action)
	s.convID, s.apptID = convID, appointmentID
	s.notifType, s.wlID = notifType, waitingListID
}

// notifPendingReturnSession deja una sesión escalada desde NOTIF_PENDING con el menú estático
// que declara el paso (el real vive en handlers; el worker no lo importa).
func notifPendingReturnSession(t *testing.T) (*MessageWorkerPool, *session.Session, *notifResponderSpy, *int, *string) {
	t.Helper()
	statemachine.RegisterStaticReturnMenu(statemachine.StateNotifPending,
		statemachine.ReturnOption{
			Label: "Confirmar la cita", Value: "confirm",
			Target: statemachine.StateNotifPending, Data: "confirm",
		})
	t.Cleanup(func() { statemachine.UnregisterReturnStepForTest(statemachine.StateNotifPending) })

	pool, sm, _, sess, resumedTo, _ := returnSession(t, statemachine.StateNotifPending)
	sess.Context["notif_appointment_id"] = "A-77"
	completed := 0
	sm.completeFn = func(_ context.Context, s *session.Session) error {
		completed++
		s.Status = session.StatusCompleted
		return nil
	}
	spy := &notifResponderSpy{}
	pool.SetNotifyResponder(spy)
	return pool, sess, spy, &completed, resumedTo
}

// Ruling 17: el retorno de Néctar con la opción `confirm` debe hacer LO MISMO que
// `/bot resume NOTIF_PENDING confirm`: completar la sesión y entregar la acción, no reanudar el
// paso con un mensaje virtual (la notificación pendiente ya no está en memoria).
func TestHandleAgentResume_NectarNotifPendingOptionRunsTheShortcut(t *testing.T) {
	pool, sess, spy, completed, resumedTo := notifPendingReturnSession(t)
	asNectar(sess)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: "confirm",
	})

	if len(spy.actions) != 1 || spy.actions[0] != "confirm" || spy.apptID != "A-77" {
		t.Fatalf("la acción debe llegar a HandleNotifPendingCommand, got %v (cita %q)", spy.actions, spy.apptID)
	}
	if *completed != 1 {
		t.Fatalf("la sesión se completa, completadas=%d", *completed)
	}
	if *resumedTo != "" {
		t.Fatalf("no se reanuda el flujo (retomó en %q)", *resumedTo)
	}
}

// El comando de Bird no cambia: mismo resultado que antes de mover la traducción.
func TestHandleAgentResume_BirdNotifPendingCommandUnchanged(t *testing.T) {
	pool, sess, spy, completed, resumedTo := notifPendingReturnSession(t)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, State: statemachine.StateNotifPending, Data: "confirm",
	})

	if len(spy.actions) != 1 || spy.actions[0] != "confirm" || *completed != 1 || *resumedTo != "" {
		t.Fatalf("acciones=%v completadas=%d retomó=%q", spy.actions, *completed, *resumedTo)
	}
}

// S24: en una notificación de lista de espera el agente de Néctar elige «Agendar ahora»: el atajo
// la entrega con el tipo y el id de la entrada guardados en la sesión, y la sesión se completa.
func TestHandleAgentResume_NotifPendingWaitingListSchedule(t *testing.T) {
	pool, sess, spy, completed, resumedTo := notifPendingReturnSession(t)
	statemachine.RegisterStaticReturnMenu(statemachine.StateNotifPending,
		statemachine.ReturnOption{
			Label: "Agendar ahora", Value: "wl_schedule",
			Target: statemachine.StateNotifPending, Data: "wl_schedule",
		})
	asNectar(sess)
	sess.Context["notif_type"] = "waiting_list"
	sess.Context[statemachine.CtxNotifWaitingListID] = "wl-1"

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnV1: &nectar.V1Return{Option: "wl_schedule"},
	})

	if len(spy.actions) != 1 || spy.actions[0] != "wl_schedule" || spy.apptID != "A-77" ||
		spy.notifType != "waiting_list" || spy.wlID != "wl-1" {
		t.Fatalf("acciones=%v cita=%q tipo=%q lista=%q", spy.actions, spy.apptID, spy.notifType, spy.wlID)
	}
	if *completed != 1 || *resumedTo != "" {
		t.Fatalf("la sesión se completa sin reanudar: completadas=%d retomó=%q", *completed, *resumedTo)
	}
}

// Una acción que no es del tipo de la notificación (confirm sobre una lista de espera) no se
// ejecuta: en Bird la sesión sigue escalada y el agente recibe una nota con las acciones válidas.
func TestHandleAgentResume_BirdNotifPendingActionOfAnotherTypeIsRefused(t *testing.T) {
	pool, sess, spy, completed, resumedTo := notifPendingReturnSession(t)
	sess.Context["notif_type"] = "waiting_list"
	sender := pool.birdClient.(*mockMessageSender)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, State: statemachine.StateNotifPending, Data: "confirm",
	})

	if len(spy.actions) != 0 || *completed != 0 || *resumedTo != "" {
		t.Fatalf("no se ejecuta ni se completa: acciones=%v completadas=%d retomó=%q", spy.actions, *completed, *resumedTo)
	}
	var note string
	for _, txt := range sentTexts(sender) {
		if strings.Contains(txt, "/bot resume NOTIF_PENDING wl_schedule") {
			note = txt
		}
	}
	if note == "" || !strings.Contains(note, "/bot resume NOTIF_PENDING wl_decline") {
		t.Fatalf("el agente recibe las acciones válidas del tipo: %q", sentTexts(sender))
	}
}

// En Néctar la acción que no es del tipo vuelve a una persona como dato rechazado, sin completar
// la sesión ni entregar la acción.
func TestHandleAgentResume_NectarNotifPendingActionOfAnotherTypeReescalates(t *testing.T) {
	pool, sess, spy, completed, resumedTo := notifPendingReturnSession(t)
	asNectar(sess)
	sess.Context["notif_type"] = "cancellation"

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: "confirm",
	})

	if len(spy.actions) != 0 || *completed != 0 {
		t.Fatalf("no se ejecuta ni se completa: acciones=%v completadas=%d", spy.actions, *completed)
	}
	if *resumedTo != statemachine.StateEscalateToAgent {
		t.Fatalf("re-escala como dato rechazado (retoma en ESCALATE_TO_AGENT): retomó=%q", *resumedTo)
	}
}

// M2: el motivo de conversacion_resuelta queda en la traza de agent_closed (p. ej. cola_vencida).
func TestHandleAgentClose_NectarTracesReason(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	pool, _, _, sess := escalatedPool(t)
	asNectar(sess)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "close", Phone: sess.PhoneNumber, Reason: "cola_vencida",
	})
	stop()

	ev := sink.FindStep("escalacion", "agent_closed")
	if ev == nil || ev.Attrs["motivo"] != "cola_vencida" {
		t.Fatalf("agent_closed lleva el motivo: %+v", ev)
	}
}

// El kill switch deja la sesión en el paso BOT_DISABLED, que no tiene handler: el agente de
// Néctar lo devuelve con la opción que publica el menú y el bot retoma en el saludo.
func TestHandleAgentResume_NectarBotDisabledResumesAtGreeting(t *testing.T) {
	// El registro del menú es global e idempotente (siempre el mismo): no hay que limpiarlo.
	handlers.RegisterEscalationHandlers(statemachine.NewMachine(), nil, nil, nil)

	pool, _, _, sess, resumedTo, processed := returnSession(t, statemachine.BotDisabledStep)
	sess.PhoneNumber = "nectar:7"
	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber,
		ReturnV1: &nectar.V1Return{Option: "bot_iniciar"},
	})

	if *resumedTo != statemachine.StateGreeting {
		t.Fatalf("se retoma en GREETING, got %q", *resumedTo)
	}
	if processed.Phone == "" {
		t.Error("el turno del saludo se procesa")
	}
}

// Ningún retorno cae en un estado sin handler: el de un comando de Bird sin State y con el
// paso fantasma guardado se redirige al saludo y queda trazado.
func TestHandleAgentResume_UnregisteredTargetFallsBackToGreeting(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)

	pool, _, _, sess, resumedTo, _ := returnSession(t, statemachine.BotDisabledStep)
	pool.machine.(*mockMessageProcessor).unregistered = true
	pool.processAgentCommand(context.Background(), AgentCommand{Action: "resume", Phone: sess.PhoneNumber})

	if *resumedTo != statemachine.StateGreeting {
		t.Fatalf("un destino sin handler cae a GREETING, got %q", *resumedTo)
	}
	stop()
	if sink.FindStep("escalacion", "retorno_estado_inexistente") == nil {
		t.Error("falta el evento escalacion/retorno_estado_inexistente")
	}
}

// El dato del comando era para un paso que no existe: al caer al saludo no se inyecta como texto.
func TestHandleAgentResume_UnregisteredTargetDropsData(t *testing.T) {
	pool, _, _, sess, resumedTo, processed := returnSession(t, statemachine.BotDisabledStep)
	pool.machine.(*mockMessageProcessor).unregistered = true
	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, State: "NO_EXISTE", Data: "1234",
	})

	if *resumedTo != statemachine.StateGreeting {
		t.Fatalf("cae a GREETING, got %q", *resumedTo)
	}
	if strings.Contains(processed.Text, "1234") {
		t.Errorf("el dato no se inyecta en el saludo: %q", processed.Text)
	}
}

// La re-escalada por un dato que no sirvió dice POR QUÉ: el agente ve el detalle, no el `reason`.
func TestReescalateRejected_WritesReason(t *testing.T) {
	pool, sm, _, sess, _, _ := returnSession(t, "ASK_DOCUMENT")
	asNectar(sess)
	db := persistedContext(sm)
	pool.machine = &mockMessageProcessor{
		returnInput: statemachine.ReturnInputResult{Written: true, Value: "12ab", Problem: "Solo números."},
		processFn: func(context.Context, *session.Session, bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{NextState: statemachine.StateEscalated}, nil
		},
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnInputPrefix + "12ab",
	})

	if got := db[statemachine.CtxEscalationReason]; got != statemachine.EscalationReasonAgentInputRejected {
		t.Fatalf("motivo persistido = %q, quería dato_agente_rechazado", got)
	}
}

// El agente llenó el campo y además pulsó una opción que no lleva dato: manda la opción. Lo
// escrito se ignora y no entra al flujo.
func TestApplyAgentReturn_NoInputOptionIgnoresWrittenValues(t *testing.T) {
	statemachine.RegisterStaticReturnMenu(testReturnStep,
		statemachine.ReturnOption{Label: "Sin dato", Value: "opt_sin_dato", NoInput: true, Data: "dato_de_la_opcion"})
	t.Cleanup(func() { statemachine.UnregisterReturnStepForTest(testReturnStep) })

	pool, _, _, sess, resumedTo, processed := returnSession(t, testReturnStep)
	asNectar(sess)
	pool.machine.(*mockMessageProcessor).returnInput = statemachine.ReturnInputResult{Written: true, Value: "x"}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber,
		ReturnV1: &nectar.V1Return{Option: "opt_sin_dato", Values: json.RawMessage(`{"dato":"x"}`)},
	})

	if *resumedTo != testReturnStep {
		t.Fatalf("se retoma por la opción, got %q", *resumedTo)
	}
	if processed.Text != "dato_de_la_opcion" {
		t.Fatalf("entra el dato de la opción y no lo escrito, got %q", processed.Text)
	}
}

// `reanudar` sin `valores` en un paso de campo requerido (Néctar solo lo valida en la consola):
// vuelve a una persona con «Se devolvió el campo vacío.», nunca un __resume__.
func TestApplyAgentReturn_ResumeWithoutValuesOnRequiredFieldReescalates(t *testing.T) {
	const step = "PASO_REQUERIDO_T5"
	statemachine.RegisterReturnInput(step, statemachine.ReturnInput{Label: "Dato", Required: true})
	t.Cleanup(func() { statemachine.UnregisterReturnStepForTest(step) })

	pool, sm, _, sess, resumedTo, processed := returnSession(t, step)
	asNectar(sess)
	db := persistedContext(sm)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber,
		ReturnV1: &nectar.V1Return{Option: statemachine.ReturnResume},
	})

	if *resumedTo != statemachine.StateEscalateToAgent {
		t.Fatalf("re-escala, got %q", *resumedTo)
	}
	if processed.Text != "__agent_input_rejected__" {
		t.Fatalf("el turno es el de la re-escalada, got %q", processed.Text)
	}
	rej, ok := statemachine.DecodeRejection(db[statemachine.CtxAgentInputRejection])
	if !ok || rej.Reason != "Se devolvió el campo vacío." {
		t.Fatalf("rechazo = %+v (ok=%v)", rej, ok)
	}
}

// Una opción con Clear borra esas claves del contexto al retomar.
func TestHandleAgentResume_OptionClearsContext(t *testing.T) {
	statemachine.RegisterStaticReturnMenu(testReturnStep,
		statemachine.ReturnOption{Label: "Corregir", Value: "corregir", Target: "OTRO_PASO", Clear: []string{"patient_contract"}})
	t.Cleanup(func() { statemachine.UnregisterReturnStepForTest(testReturnStep) })

	pool, sm, _, sess, resumedTo, processed := returnSession(t, testReturnStep)
	asNectar(sess)
	sess.Context["patient_contract"] = "7"
	var cleared []string
	var clearedBeforeProcess bool
	sm.saveFn = func(_ context.Context, s *session.Session, state string, _ map[string]string, clr []string) error {
		s.CurrentState = state
		cleared = append(cleared, clr...)
		for _, k := range clr {
			delete(s.Context, k)
		}
		return nil
	}
	pool.machine.(*mockMessageProcessor).processFn = func(_ context.Context, s *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
		*processed = msg
		clearedBeforeProcess = s.GetContext("patient_contract") == ""
		return &statemachine.StateResult{NextState: "OTRO_PASO"}, nil
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnV1: &nectar.V1Return{Option: "corregir"},
	})

	if *resumedTo != "OTRO_PASO" {
		t.Fatalf("se retoma en el Target de la opción, got %q", *resumedTo)
	}
	if strings.Join(cleared, ",") != "patient_contract" {
		t.Fatalf("SaveState borra las claves de la opción, got %v", cleared)
	}
	if !clearedBeforeProcess {
		t.Fatal("el turno ya no ve la clave borrada")
	}
}

// El Data de una OPCIÓN viaja como postback y como texto a la vez: los pasos de botones solo
// aceptan postback.
func TestHandleAgentResume_OptionDataArrivesAsPostback(t *testing.T) {
	statemachine.RegisterStaticReturnMenu(testReturnStep,
		statemachine.ReturnOption{Label: "Elegir", Value: "elegir", Data: "proc_2"})
	t.Cleanup(func() { statemachine.UnregisterReturnStepForTest(testReturnStep) })

	pool, _, _, sess, _, processed := returnSession(t, testReturnStep)
	asNectar(sess)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnV1: &nectar.V1Return{Option: "elegir"},
	})

	if !processed.IsPostback || processed.PostbackPayload != "proc_2" || processed.Text != "proc_2" {
		t.Fatalf("la opción llega como postback y como texto: %+v", *processed)
	}
}

// Un comando de Bird (`/bot resume X y`) sigue llegando como texto, igual que hoy.
func TestHandleAgentResume_BirdCommandStaysText(t *testing.T) {
	pool, _, _, sess, _, processed := returnSession(t, testReturnStep)

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", State: "X", Data: "y", Phone: sess.PhoneNumber,
	})

	if processed.IsPostback || processed.PostbackPayload != "" || processed.Text != "y" || processed.MessageType != "text" {
		t.Fatalf("un comando de Bird llega como texto: %+v", *processed)
	}
}
