package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
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
	defer statemachine.RegisterStaticReturnMenu(testReturnStep)

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
	pool, _, sender, sess, resumedTo, processed := returnSession(t, "ASK_DOCUMENT")
	asNectar(sess)
	const value, problem = "12ab", "El documento solo lleva números."
	pool.machine.(*mockMessageProcessor).returnInput = statemachine.ReturnInputResult{
		Written: true, Value: value, Problem: problem, Field: "documento",
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnInputPrefix + value,
	})

	if *resumedTo != statemachine.StateEscalateToAgent {
		t.Fatalf("el dato rechazado re-escala, got %q", *resumedTo)
	}
	if got := sess.GetContext(statemachine.CtxAgentInputRejected); got != "1" {
		t.Errorf("CtxAgentInputRejected = %q, esperaba \"1\"", got)
	}
	if got, want := sess.GetContext("escalation_note"), statemachine.RejectedInputNote(1, value, problem); got != want {
		t.Errorf("escalation_note = %q, esperaba %q", got, want)
	}
	if got := sess.GetContext(statemachine.CtxPreAutoState); got != "ASK_DOCUMENT" {
		t.Errorf("el paso bloqueado sigue siendo el de origen, got %q", got)
	}
	rej, ok := statemachine.DecodeRejection(sess.GetContext(statemachine.CtxAgentInputRejection))
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
	defer statemachine.RegisterStaticReturnMenu(testReturnStep)

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

// conversacion_resuelta: el agente ya cerró con el paciente en Néctar, así que el bot no se
// despide otra vez, y el cierre es síncrono (no hay entrega en vuelo que esperar).
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
			Action: "close", Phone: sess.PhoneNumber, Reason: "resuelta",
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
		if len(closed) != 1 || closed[0] != "conv1" {
			t.Fatalf("en Néctar el cierre es síncrono, got %v", closed)
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
