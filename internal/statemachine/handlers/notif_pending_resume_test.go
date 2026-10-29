package handlers

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// Las sesiones escaladas de una notificación (escalateToAgent por falta de respuesta y
// escalateNotifToAgent por texto libre) guardan la cita como notif_appointment_id, sin
// notif_appt_id ni notif_appt_ids. «Devolver al bot en el mismo paso» las deja en NOTIF_PENDING y
// lo que el paciente elija después debe actuar sobre ESA cita.

// escalatedNotifContexts son los contextos que dejan las dos escalaciones de notificación
// (internal/notifications: confirmation.go escalateToAgent y manager.go escalateNotifToAgent).
func escalatedNotifContexts() map[string]map[string]string {
	common := map[string]string{
		"notif_appointment_id": "APT-NOTIF",
		"notif_type":           "confirmation",
		"notif_appt_date":      "lunes 3 de noviembre",
		"notif_appt_time":      "7:30 AM",
		"notif_cups_name":      "Electromiografia",
		sm.CtxNotifCupsCode:    "890271",
		"notif_conv_id":        "conv-1",
		"patient_name":         "ANA PEREZ",
		"pre_escalation_state": sm.StateNotifPending,
	}
	with := func(kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range common {
			out[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	return map[string]map[string]string{
		"escalateToAgent":      with(sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifNoResponse),
		"escalateNotifToAgent": with(sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifFreeText, "notif_bird_msg_id", "m-1"),
	}
}

// notifApptRepo sirve el bloque del día de la cita notificada (dos citas, como cuando se
// notificaron juntas) y anota qué ids se confirman o cancelan.
type notifApptRepo struct {
	testutil.MockAppointmentRepo
	confirmed, cancelled []string
}

func newNotifApptRepo() *notifApptRepo {
	day := time.Now().AddDate(0, 0, 5)
	first := testutil.SampleAppointment(day)
	first.ID = "APT-NOTIF"
	second := testutil.SampleAppointment(day)
	second.ID = "APT-SAME-DAY"
	r := &notifApptRepo{}
	r.FindByIDFn = func(_ context.Context, id string) (*domain.Appointment, error) {
		if id != first.ID {
			return nil, nil
		}
		a := first
		return &a, nil
	}
	r.FindUpcomingByPatientFn = func(context.Context, string) ([]domain.Appointment, error) {
		return []domain.Appointment{first, second}, nil
	}
	r.ConfirmBatchFn = func(_ context.Context, ids []string, _, _ string) error {
		r.confirmed = append(r.confirmed, ids...)
		return nil
	}
	r.CancelBatchFn = func(_ context.Context, ids []string, _, _, _ string) error {
		r.cancelled = append(r.cancelled, ids...)
		return nil
	}
	return r
}

func notifResumeSess(ctx map[string]string) *session.Session {
	sess := resumeTurnSess(sm.StateNotifPending, true)
	for k, v := range ctx {
		sess.Context[k] = v
	}
	return sess
}

// El retorno sin dato re-muestra los 3 botones con la cita, sin gastar reintento.
func TestNotifPending_NectarPlainResumeShowsTheButtons(t *testing.T) {
	for name, ctx := range escalatedNotifContexts() {
		t.Run(name, func(t *testing.T) {
			m := sm.NewMachine()
			RegisterAppointmentHandlers(m, services.NewAppointmentService(newNotifApptRepo(), nil), nil, nil, nil)
			sess := notifResumeSess(ctx)
			sess.RetryCount = 1

			result, err := m.Process(context.Background(), sess, resumeMsg())
			if err != nil {
				t.Fatal(err)
			}
			if result.NextState != sm.StateNotifPending || sess.RetryCount != 1 {
				t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
			}
			if len(result.Messages) != 1 {
				t.Fatalf("quería solo los botones, hay %d mensajes", len(result.Messages))
			}
			bm, ok := result.Messages[0].(*sm.ButtonMessage)
			if !ok {
				t.Fatalf("quería los botones de la notificación, obtuve %T", result.Messages[0])
			}
			if !strings.Contains(bm.Text, "lunes 3 de noviembre") || !strings.Contains(bm.Text, "Electromiografia") {
				t.Errorf("los botones no muestran la cita: %q", bm.Text)
			}
			var payloads []string
			for _, b := range bm.Buttons {
				payloads = append(payloads, b.Payload)
			}
			if !slices.Equal(payloads, []string{"confirm", "reschedule", "cancel"}) {
				t.Errorf("botones = %v", payloads)
			}
		})
	}
}

// Tras el retorno, cada botón actúa sobre la cita de la notificación (notif_appointment_id).
func TestNotifPending_EscalatedSessionActsOnTheNotifiedAppointment(t *testing.T) {
	for name, ctx := range escalatedNotifContexts() {
		t.Run(name+"/confirm", func(t *testing.T) {
			repo := newNotifApptRepo()
			m := sm.NewMachine()
			RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
			sess := notifResumeSess(ctx)
			delete(sess.Context, sm.CtxAgentResumeTurn)

			result, err := m.Process(context.Background(), sess, postbackM("confirm"))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(repo.confirmed, []string{"APT-NOTIF", "APT-SAME-DAY"}) {
				t.Errorf("confirmadas = %v (resultado %s)", repo.confirmed, result.NextState)
			}
		})
		t.Run(name+"/reschedule", func(t *testing.T) {
			// Con una sola cita ese día se reprograma directo esa cita.
			repo := newNotifApptRepo()
			repo.FindUpcomingByPatientFn = nil
			m := sm.NewMachine()
			RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
			sess := notifResumeSess(ctx)
			delete(sess.Context, sm.CtxAgentResumeTurn)

			result, err := m.Process(context.Background(), sess, postbackM("reschedule"))
			if err != nil {
				t.Fatal(err)
			}
			if result.NextState != sm.StateSearchSlots || result.UpdateCtx["reschedule_appt_id"] != "APT-NOTIF" {
				t.Errorf("next=%s reschedule_appt_id=%q", result.NextState, result.UpdateCtx["reschedule_appt_id"])
			}
		})
		t.Run(name+"/cancel", func(t *testing.T) {
			repo := newNotifApptRepo()
			m := sm.NewMachine()
			RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
			sess := notifResumeSess(ctx)
			delete(sess.Context, sm.CtxAgentResumeTurn)

			result, err := m.Process(context.Background(), sess, postbackM("cancel"))
			if err != nil {
				t.Fatal(err)
			}
			if result.NextState != sm.StateConfirmCancelNotif {
				t.Fatalf("next = %s", result.NextState)
			}
			sess.CurrentState = sm.StateConfirmCancelNotif
			if _, err := m.Process(context.Background(), sess, postbackM("cancel_yes")); err != nil {
				t.Fatal(err)
			}
			// Sin notif_appt_ids en la sesión escalada, se cancelan las citas del bloque del día.
			if !slices.Equal(repo.cancelled, []string{"APT-NOTIF", "APT-SAME-DAY"}) {
				t.Errorf("canceladas = %v", repo.cancelled)
			}
		})
	}
}

// Si la escalación reusó una sesión que traía la cita de OTRA notificación (notif_appt_id y
// notif_appt_ids viejos), manda la que la escalación escribió: es la que el agente y el paciente
// ven en los botones.
func TestNotifPending_EscalatedCtxWinsOverAStaleConfirmationSession(t *testing.T) {
	repo := newNotifApptRepo()
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
	sess := notifResumeSess(escalatedNotifContexts()["escalateNotifToAgent"])
	delete(sess.Context, sm.CtxAgentResumeTurn)
	sess.Context["notif_appt_id"] = "APT-OLD"
	sess.Context["notif_appt_ids"] = `["APT-OLD"]`

	if _, err := m.Process(context.Background(), sess, postbackM("cancel")); err != nil {
		t.Fatal(err)
	}
	sess.CurrentState = sm.StateConfirmCancelNotif
	if _, err := m.Process(context.Background(), sess, postbackM("cancel_yes")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repo.cancelled, []string{"APT-NOTIF", "APT-SAME-DAY"}) {
		t.Errorf("canceladas = %v, no la cita vieja", repo.cancelled)
	}
}

// La sesión de confirmación normal (startConfirmCancelSession) sigue igual: notif_appt_id y
// notif_appt_ids mandan.
func TestNotifPending_ConfirmationSessionUnchanged(t *testing.T) {
	repo := newNotifApptRepo()
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
	sess := testSess(sm.StateConfirmCancelNotif)
	sess.Context["notif_appt_id"] = "APT-NOTIF"
	sess.Context["notif_appt_ids"] = `["APT-NOTIF"]`

	if _, err := m.Process(context.Background(), sess, postbackM("cancel_yes")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repo.cancelled, []string{"APT-NOTIF"}) {
		t.Errorf("canceladas = %v", repo.cancelled)
	}
}

// El menú de NOTIF_PENDING vuelve a ofrecer «Devolver al bot en el mismo paso» donde los 3 botones
// tienen sentido (confirmación o reprogramación). En la lista de espera, la cancelación de la
// clínica y el fallo de SIESA los botones no corresponden: ahí sigue oculto.
func TestNotifPendingReturnMenu_PlainResumeOnlyWhereTheButtonsApply(t *testing.T) {
	returnMenuMachine()
	view := func(kv ...string) *session.Session {
		s := testSess(sm.StateNotifPending)
		for i := 0; i+1 < len(kv); i += 2 {
			s.Context[kv[i]] = kv[i+1]
		}
		return s
	}
	cases := []struct {
		name string
		sess *session.Session
		want bool
	}{
		{"sin sesión", nil, true},
		{"escalateToAgent", view("notif_type", "confirmation", sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifNoResponse), true},
		{"escalateNotifToAgent", view("notif_type", "confirmation", sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifFreeText), true},
		{"reprogramación", view("notif_type", "reschedule"), true},
		{"sesión de la máquina", view("notif_appt_id", "1"), true},
		{"fallo de SIESA", view("notif_type", "confirmation", sm.CtxNotifEscalationDetail, sm.EscalationReasonNotifSystemFailure), false},
		{"lista de espera", view("notif_type", "waiting_list"), false},
		{"cancelación", view("notif_type", "cancellation"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var values []string
			for _, o := range sm.ReturnOptionsFor(c.sess, sm.StateNotifPending) {
				values = append(values, o.Value)
			}
			if got := slices.Contains(values, sm.ReturnResume); got != c.want {
				t.Errorf("ofrece %s = %v, quería %v: %v", sm.ReturnResume, got, c.want, values)
			}
			for _, v := range []string{sm.ReturnReset, sm.ReturnClose} {
				if !slices.Contains(values, v) {
					t.Errorf("falta la universal %s: %v", v, values)
				}
			}
		})
	}
}

// «Reprogramar» con una sola cita ese día arma la búsqueda con los datos de LA CITA notificada:
// la sesión escalada no trae CUPS, procedimientos ni paciente, y si la escalación reusó una
// sesión viva, lo que esta traía es de otro trámite (otro procedimiento, incluso otro paciente
// del mismo teléfono).
func TestNotifPending_RescheduleBuildsTheSearchFromTheNotifiedAppointment(t *testing.T) {
	stale := map[string]string{
		"patient_id": "OTRO-PACIENTE", "patient_entity": "EPS999", "patient_contract": "8",
		"cups_code": "883101", "cups_name": "RESONANCIA", "espacios": "3",
		"procedures_json":      `[{"service_type":"general","cups":[{"code":"883101"}]}]`,
		"preferred_doctor_doc": "999", "selected_slot_id": "1|3|202611030730",
		"available_slots_json": "[{}]", "gestational_max_date": "2026-12-01", "from_waiting_list": "1",
	}
	cases := map[string]map[string]string{"sesión escalada nueva": nil, "sesión reusada con contexto viejo": stale}
	for name, extra := range cases {
		for ctxName, ctx := range escalatedNotifContexts() {
			t.Run(name+"/"+ctxName, func(t *testing.T) {
				repo := newNotifApptRepo()
				repo.FindUpcomingByPatientFn = nil // una sola cita ese día
				base := repo.FindByIDFn
				repo.FindByIDFn = func(c context.Context, id string) (*domain.Appointment, error) {
					a, err := base(c, id)
					if a != nil {
						a.Entity = "EPS005"
						a.DoctorDocument = "1234"
					}
					return a, err
				}
				m := sm.NewMachine()
				RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
				sess := notifResumeSess(ctx)
				delete(sess.Context, sm.CtxAgentResumeTurn)
				for k, v := range extra {
					sess.Context[k] = v
				}

				result, err := m.Process(context.Background(), sess, postbackM("reschedule"))
				if err != nil {
					t.Fatal(err)
				}
				if result.NextState != sm.StateSearchSlots {
					t.Fatalf("next = %s", result.NextState)
				}
				want := map[string]string{
					"reschedule_appt_id": "APT-NOTIF", "cups_code": "890271", "cups_name": "Electromiografia",
					"patient_id": "PAT001", "patient_entity": "EPS005", "patient_contract": "EPS005",
					"preferred_doctor_doc": "1234", "total_procedures": "1", "current_procedure_idx": "0",
				}
				for k, v := range want {
					if got := result.UpdateCtx[k]; got != v {
						t.Errorf("%s = %q, quería %q", k, got, v)
					}
				}
				var groups []services.CUPSGroup
				if err := json.Unmarshal([]byte(result.UpdateCtx["procedures_json"]), &groups); err != nil ||
					len(groups) != 1 || len(groups[0].Cups) != 1 || groups[0].Cups[0].Code != "890271" {
					t.Errorf("procedures_json = %q", result.UpdateCtx["procedures_json"])
				}
				// Lo que el trámite viejo dejó y la búsqueda leería se borra; lo recién armado no.
				for _, k := range []string{"selected_slot_id", "available_slots_json", "gestational_max_date", "from_waiting_list"} {
					if !slices.Contains(result.ClearCtx, k) {
						t.Errorf("no se borra %s; ClearCtx=%v", k, result.ClearCtx)
					}
				}
				for k := range result.UpdateCtx {
					if slices.Contains(result.ClearCtx, k) {
						t.Errorf("%s se escribe y se borra a la vez", k)
					}
				}
			})
		}
	}
}

// Con varias citas ese día el paciente elige cuál en la lista de «Mis citas»: la sesión ya queda
// con el paciente de la cita notificada y sin el trámite viejo.
func TestNotifPending_RescheduleWithSeveralAppointmentsFixesThePatient(t *testing.T) {
	repo := newNotifApptRepo()
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
	sess := notifResumeSess(escalatedNotifContexts()["escalateNotifToAgent"])
	delete(sess.Context, sm.CtxAgentResumeTurn)
	sess.Context["patient_id"] = "OTRO-PACIENTE"
	sess.Context["selected_slot_id"] = "1|3|202611030730"

	result, err := m.Process(context.Background(), sess, postbackM("reschedule"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateListAppointments || result.UpdateCtx["patient_id"] != "PAT001" {
		t.Fatalf("next=%s patient_id=%q", result.NextState, result.UpdateCtx["patient_id"])
	}
	if !slices.Contains(result.ClearCtx, "selected_slot_id") {
		t.Errorf("no se borra el horario del trámite viejo; ClearCtx=%v", result.ClearCtx)
	}
}
