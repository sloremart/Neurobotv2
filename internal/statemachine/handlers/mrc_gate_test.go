package handlers

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// ---------------------------------------------------------------------------
// H145 (reporte de la entidad 13-ago-2026: se agendó por encima del tope MRC):
// los caminos de LISTA DE ESPERA entraban a SEARCH_SLOTS sin el flag
// mrc_limit_check → agendaban SIN tope. Regla ABSOLUTA: ningún camino (flujo
// principal, WL, reprogramación, consolidación EMG) agenda superado el límite.
// ---------------------------------------------------------------------------

// mrcTestSvc arma un AppointmentService cuyo conteo mensual del grupo devuelve `consumed`.
func mrcTestSvc(consumed int) *services.AppointmentService {
	repo := &testutil.MockAppointmentRepo{
		CountMonthlyByGroupFn: func(_ context.Context, _ []string, _, _ int) (int, error) {
			return consumed, nil
		},
	}
	return services.NewAppointmentService(repo, nil)
}

// wlLikeSession replica el contexto con el que los caminos de lista de espera entran a
// SEARCH_SLOTS: contrato MRC + procedures_json, SIN el flag mrc_limit_check.
func wlLikeSession(cupsJSON string) *session.Session {
	s := testSess(sm.StateSearchSlots)
	s.Context["patient_contract"] = "5"
	s.Context["procedures_json"] = cupsJSON
	s.Context["current_procedure_idx"] = "0"
	s.Context["cups_code"] = "891509"
	return s
}

// TestMrcMonthFilter_SinFlag_ActivaIgual: el filtro debe activarse para un paciente MRC
// aunque NADIE haya marcado mrc_limit_check (los caminos WL no lo marcan — el hueco).
func TestMrcMonthFilter_SinFlag_ActivaIgual(t *testing.T) {
	svc := mrcTestSvc(930) // consumido 930 de 932 (otros_procedimientos)
	sess := wlLikeSession(`[{"service":"general","cups":[{"cups_code":"891509","cups_name":"NC","quantity":8}],"espacios":1}]`)

	filter := mrcMonthFilter(context.Background(), svc, sess, "891509")
	if filter == nil {
		t.Fatal("el filtro DEBE activarse para paciente MRC sin flag (hueco de lista de espera)")
	}
	allowed, err := filter(2026, 8)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("930 consumido + 8 de la orden > 932: el mes debe quedar BLOQUEADO")
	}
}

// TestMrcMonthFilter_AgregaDemandaDelGrupo: dos CUPS del MISMO grupo suman su demanda
// (8+1=9); validar solo el CUP principal subcontaba.
func TestMrcMonthFilter_AgregaDemandaDelGrupo(t *testing.T) {
	svc := mrcTestSvc(924) // 924+9=933 > 932 bloquea; con subconteo (solo 8: 932) habría pasado
	sess := wlLikeSession(`[{"service":"general","cups":[{"cups_code":"891509","cups_name":"NC","quantity":8},{"cups_code":"891515","cups_name":"OTRO","quantity":1}],"espacios":1}]`)

	filter := mrcMonthFilter(context.Background(), svc, sess, "891509")
	if filter == nil {
		t.Fatal("filtro nil")
	}
	allowed, _ := filter(2026, 8)
	if allowed {
		t.Error("924 + (8+1) = 933 > 932: debe bloquear por demanda AGREGADA del grupo")
	}
}

// TestMrcMonthFilter_NoMRC_NoAplica: paciente no-MRC → nil (cero costo para el resto).
func TestMrcMonthFilter_NoMRC_NoAplica(t *testing.T) {
	svc := mrcTestSvc(9999)
	sess := wlLikeSession(`[{"service":"general","cups":[{"cups_code":"891509","quantity":8}],"espacios":1}]`)
	sess.Context["patient_contract"] = "13" // Salud Total

	if filter := mrcMonthFilter(context.Background(), svc, sess, "891509"); filter != nil {
		t.Error("paciente no-MRC no debe llevar filtro")
	}
}

// TestMrcFinalGate_BloqueaPorMesDelSlot: el candado final valida contra el MES DEL SLOT
// elegido y bloquea aunque ningún filtro previo haya corrido (cubre WL y TOCTOU).
func TestMrcFinalGate_BloqueaPorMesDelSlot(t *testing.T) {
	svc := mrcTestSvc(930)
	sess := wlLikeSession(`[{"service":"general","cups":[{"cups_code":"891509","cups_name":"NC","quantity":8}],"espacios":1}]`)

	if group := mrcFinalGateBlocked(context.Background(), svc, sess, "2026-08-25"); group == "" {
		t.Error("el candado final debe bloquear (930+8 > 932)")
	}
	svcOK := mrcTestSvc(100)
	if group := mrcFinalGateBlocked(context.Background(), svcOK, sess, "2026-08-25"); group != "" {
		t.Errorf("bajo el tope no debe bloquear, got %q", group)
	}
}

// ---------------------------------------------------------------------------
// H146: bloqueos SANITAS agendados por el bot (4 unidades medidas en prod bajo
// contratos 5/6). El gate vivía solo en VALIDATE_OCR; lista de espera,
// reprogramaciones y auto-add lo saltaban. La regla es del negocio, no de la
// ruta: gate en la puerta de SEARCH_SLOTS + candado en CREATE.
// ---------------------------------------------------------------------------

// TestSearchSlots_BloqueoSanitas_EscalaSinBuscar: una sesión tipo lista-de-espera (sin pasar
// por VALIDATE_OCR) con un CUP de bloqueo y contrato SANITAS escala a asesor en la puerta.
func TestSearchSlots_BloqueoSanitas_EscalaSinBuscar(t *testing.T) {
	h := searchSlotsHandler(nil, nil, nil, nil, nil) // el gate corre antes de tocar servicios
	sess := testSess(sm.StateSearchSlots)
	sess.Context["patient_contract"] = "5" // SANITAS MRC
	sess.Context["cups_code"] = "053105"   // bloqueo
	sess.Context["procedures_json"] = `[{"service":"general","cups":[{"cups_code":"053105","quantity":1}],"espacios":1}]`
	sess.Context["current_procedure_idx"] = "0"

	res, err := h(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateEscalateToAgent {
		t.Errorf("bloqueo SANITAS debe escalar a asesor, got %s", res.NextState)
	}
}

// TestSearchSlots_BloqueoNoSanitas_NoAplica: el mismo bloqueo con contrato NO Sanitas sigue
// el flujo normal (la regla es solo para SANITAS).
func TestSearchSlots_BloqueoNoSanitas_NoAplica(t *testing.T) {
	sess := testSess(sm.StateSearchSlots)
	sess.Context["patient_contract"] = "13" // Salud Total
	sess.Context["cups_code"] = "053105"
	sess.Context["procedures_json"] = `[{"service":"general","cups":[{"cups_code":"053105","quantity":1}],"espacios":1}]`
	sess.Context["current_procedure_idx"] = "0"

	if cup := sanitasBloqueoInCurrentGroup(sess); cup != "" {
		t.Errorf("contrato no-SANITAS no debe activar el gate, got %q", cup)
	}
}

// TestSanitasBloqueo_DetectaEnGrupoMultiCup: el bloqueo escondido entre otros CUPS del grupo
// también se detecta (no solo el primario).
func TestSanitasBloqueo_DetectaEnGrupoMultiCup(t *testing.T) {
	sess := testSess(sm.StateSearchSlots)
	sess.Context["patient_contract"] = "6"
	sess.Context["cups_code"] = "891509" // primario NO bloqueo
	sess.Context["procedures_json"] = `[{"service":"general","cups":[{"cups_code":"891509","quantity":8},{"cups_code":"053111","quantity":1}],"espacios":1}]`
	sess.Context["current_procedure_idx"] = "0"

	if cup := sanitasBloqueoInCurrentGroup(sess); cup != "053111" {
		t.Errorf("debe detectar el bloqueo del grupo, got %q", cup)
	}
}
