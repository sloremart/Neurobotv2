package handlers

import (
	"context"
	"testing"

	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Una foto o documento en el MENÚ PRINCIPAL debe arrancar el flujo de agendar (no el dead-end
// "No esperaba una imagen"). Ver §3.6: recupera ~20 sesiones/día de fotos-en-menú perdidas.
func TestPhotoIntent_ImageAtMainMenu_StartsScheduling(t *testing.T) {
	intercept := PhotoIntentInterceptor()

	// Imagen en MAIN_MENU → intercepta y arranca agendar (ASK_CLIENT_TYPE).
	sess := testSess(sm.StateMainMenu)
	sess.RetryCount = 2
	res, ok := intercept(context.Background(), sess, imageMsg("http://x/y.jpg"))
	if !ok {
		t.Fatal("esperaba que interceptara una imagen en MAIN_MENU")
	}
	if res.NextState != sm.StateAskClientType {
		t.Errorf("NextState: esperaba ASK_CLIENT_TYPE, got %s", res.NextState)
	}
	if res.UpdateCtx["menu_option"] != "agendar" {
		t.Errorf("esperaba menu_option=agendar en el contexto")
	}
	if sess.RetryCount != 0 {
		t.Errorf("esperaba RetryCount reseteado a 0, got %d", sess.RetryCount)
	}
	if len(res.Messages) == 0 {
		t.Errorf("esperaba mensajes (texto de aviso + lista de entidades)")
	}
}

// Un documento en MAIN_MENU también se reconduce a agendar.
func TestPhotoIntent_DocumentAtMainMenu_StartsScheduling(t *testing.T) {
	intercept := PhotoIntentInterceptor()
	sess := testSess(sm.StateMainMenu)
	res, ok := intercept(context.Background(), sess, documentMsg("http://x/y.pdf"))
	if !ok || res.NextState != sm.StateAskClientType {
		t.Fatalf("documento en MAIN_MENU debía arrancar agendar; ok=%v next=%v", ok, res)
	}
}

// Texto en MAIN_MENU NO se intercepta (lo maneja el menú normal).
func TestPhotoIntent_TextAtMainMenu_PassesThrough(t *testing.T) {
	intercept := PhotoIntentInterceptor()
	sess := testSess(sm.StateMainMenu)
	if res, ok := intercept(context.Background(), sess, textM("1")); ok {
		t.Errorf("no debía interceptar texto en MAIN_MENU; got %v", res)
	}
}

// Una imagen en OTRO estado (no menú) NO la maneja este interceptor (la trata ImageOutOfContext / el
// estado de subida correspondiente).
func TestPhotoIntent_ImageOutsideMainMenu_PassesThrough(t *testing.T) {
	intercept := PhotoIntentInterceptor()
	sess := testSess(sm.StateAskDocument)
	if _, ok := intercept(context.Background(), sess, imageMsg("http://x/y.jpg")); ok {
		t.Errorf("no debía interceptar una imagen fuera de MAIN_MENU")
	}
}
