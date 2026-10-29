package notifications

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakenectar"
)

// Los followups de WhatsApp fueron ELIMINADOS (decisión 06-ago-2026): eran texto libre que
// WhatsApp no entrega fuera de la ventana de servicio pero Bird cobraba igual. Los pasos 0/1 de
// la cadena ahora solo avanzan RetryCount y timers (sin envío), para que el IVR de las 15:00
// (RetryCount==2) y la escalación a agente por no-respuesta sigan funcionando con la misma
// temporización de siempre.

func followupChainManager(t *testing.T, lastMsg *time.Time) (*NotificationManager, *atomic.Int32, func()) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			hits.Add(1)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	cfg := &config.Config{
		ConfirmFollowupEnabled: true,
		ConfirmFollowup1Hours:  3,
		ConfirmFollowup2Hours:  3,
		ConfirmPostIVRMinutes:  30,
	}
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL), nil, cfg)
	mgr.SetWaitingListDeps(nil, &mockSessionCreator{lastPatientMsgAt: lastMsg}, nil)
	return mgr, &hits, srv.Close
}

// Paso 0 de la cadena: NINGÚN texto de WhatsApp, aunque la ventana esté abierta. Solo avanza.
func TestHandleTimeout_Step0_NoWhatsAppText_ChainAdvances(t *testing.T) {
	recent := time.Now().Add(-1 * time.Hour) // ventana abierta: aun así no debe enviarse nada
	mgr, hits, closeSrv := followupChainManager(t, &recent)
	defer closeSrv()

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type: "confirmation", Phone: "+573001234567", RetryCount: 0,
	})
	mgr.handleTimeout("+573001234567")

	if hits.Load() != 0 {
		t.Errorf("los followups de WhatsApp fueron eliminados: no debe haber envíos, hubo %d", hits.Load())
	}
	val, _ := mgr.pending.Load("+573001234567")
	if p := val.(*PendingNotification); p.RetryCount != 1 {
		t.Errorf("la cadena debe avanzar (RetryCount 1) para que IVR/escalación sigan, got %d", p.RetryCount)
	}
}

// Paso 1: igual — sin texto, avanza a RetryCount 2 (el objetivo del IVR de las 15:00).
func TestHandleTimeout_Step1_NoWhatsAppText_ChainAdvances(t *testing.T) {
	mgr, hits, closeSrv := followupChainManager(t, nil)
	defer closeSrv()

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type: "confirmation", Phone: "+573001234567", RetryCount: 1,
	})
	mgr.handleTimeout("+573001234567")

	if hits.Load() != 0 {
		t.Errorf("los followups de WhatsApp fueron eliminados: no debe haber envíos, hubo %d", hits.Load())
	}
	val, _ := mgr.pending.Load("+573001234567")
	if p := val.(*PendingNotification); p.RetryCount != 2 {
		t.Errorf("la cadena debe avanzar (RetryCount 2), got %d", p.RetryCount)
	}
}

// WindowOpen sigue existiendo para el texto de PREPARACIÓN de los recordatorios.
// Sin sessionRepo (o con error de BD) el gate NO debe silenciarla: fail-open.
func TestWindowOpen_FailOpenWithoutRepo(t *testing.T) {
	cfg := &config.Config{}
	mgr := NewNotificationManager(nil, nil, cfg)
	if !mgr.WindowOpen(t.Context(), "+573001234567") {
		t.Error("sin sessionRepo el gate debe ser fail-open (true)")
	}
}

// Con una clave de sesión de Néctar la ventana la dice Néctar (GET …/state), no el último mensaje
// del paciente en la BD del bot: la conversación que abre una plantilla aún no tiene sesión aquí.
func TestWindowOpen_NectarAsksTheBridge(t *testing.T) {
	client := bird.NewClientForTest("http://unused")
	fb := &fakenectar.Bridge{Window: true}
	client.SetNectar(fb)
	recent := time.Now().Add(-1 * time.Hour)
	sessRepo := &mockSessionCreator{lastPatientMsgAt: &recent}
	mgr := NewNotificationManager(client, nil, &config.Config{})
	mgr.SetWaitingListDeps(nil, sessRepo, nil)

	if !mgr.WindowOpen(t.Context(), "nectar:77") {
		t.Error("Néctar dice abierta")
	}
	fb.Window = false
	if mgr.WindowOpen(t.Context(), "nectar:77") {
		t.Error("Néctar dice cerrada, aunque la BD del bot tenga un mensaje reciente")
	}
	fb.Window, fb.WindowErr = true, errors.New("timeout")
	if mgr.WindowOpen(t.Context(), "nectar:77") {
		t.Error("un error de Néctar cuenta como ventana cerrada")
	}
	if got := fb.WindowQuestions(); len(got) != 3 || got[0] != 77 {
		t.Fatalf("las tres preguntas van a Néctar por la conversación 77: %v", got)
	}
	if sessRepo.lastPatientAsks != 0 {
		t.Errorf("con Néctar no se consulta LastPatientMessageAt (%d)", sessRepo.lastPatientAsks)
	}
	// Un teléfono de Bird sigue preguntando a la BD, aunque el puente esté puesto.
	if !mgr.WindowOpen(t.Context(), "+573001234567") || sessRepo.lastPatientAsks != 1 {
		t.Errorf("Bird: ventana por LastPatientMessageAt (%d consultas)", sessRepo.lastPatientAsks)
	}
}
