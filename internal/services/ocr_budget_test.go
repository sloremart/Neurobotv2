package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAnalyze_OwnBudgetDoesNotDrainParentContext cubre el bug de producción 2026-07-27: el OCR recibía
// el contexto COMPLETO del mensaje (2 min) sin cota propia, así que un OCR colgado agotaba TODO el
// presupuesto y las escrituras posteriores (SaveState, chat_events, inbox MarkDone) fallaban en cascada
// con "context deadline exceeded", perdiendo el estado del paciente.
//
// La garantía que se exige aquí: el OCR debe rendirse con SU PROPIO plazo y devolver el control con el
// contexto del mensaje TODAVÍA VIVO, para que quede margen de persistir y responderle al paciente.
func TestAnalyze_OwnBudgetDoesNotDrainParentContext(t *testing.T) {
	// Servidor que nunca responde a tiempo: simula el OCR colgado.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	s := NewOCRServiceForTest(srv.URL)
	s.SetAnalyzeTimeout(150 * time.Millisecond) // presupuesto propio del OCR, muy por debajo del padre

	// El "presupuesto del mensaje": generoso, como los 2 min de processMessage.
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := s.AnalyzeImage(parent, "data:image/jpeg;base64,AAAA")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("esperaba error de timeout del OCR")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("el error debe ser DeadlineExceeded para que el handler emita ocr_timeout y ofrezca reintento, got: %v", err)
	}
	// Lo esencial: el contexto del MENSAJE sigue vivo → hay margen para persistir estado y responder.
	if parent.Err() != nil {
		t.Fatalf("el contexto del mensaje quedó agotado por el OCR: %v", parent.Err())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("el OCR no respetó su presupuesto propio: tardó %v", elapsed)
	}
}

// TestAnalyze_BudgetNeverExtendsParent: si al mensaje le queda MENOS tiempo que el presupuesto del OCR,
// manda el del mensaje (el presupuesto propio es un techo, nunca una extensión).
func TestAnalyze_BudgetNeverExtendsParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	s := NewOCRServiceForTest(srv.URL)
	s.SetAnalyzeTimeout(10 * time.Second)

	parent, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := s.AnalyzeImage(parent, "data:image/jpeg;base64,AAAA"); err == nil {
		t.Fatal("esperaba error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("el presupuesto propio no debe extender el del padre: tardó %v", elapsed)
	}
}
