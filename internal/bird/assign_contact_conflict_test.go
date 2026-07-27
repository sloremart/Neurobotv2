package bird

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cuerpo REAL devuelto por Bird en producción (2026-07-27, paciente +573***5855, feed item 96fc2776):
// el reopen de una conversación cerrada se rechaza porque el CONTACTO ya tiene otra conversación activa.
const birdAnotherActiveConvBody = `{"code":"ResourceAlreadyExists","message":"There is another active conversation already: another active conversation exists for this contact"}`

// TestAssignFeedItem_ContactConflictIsNotRetriedBlindly: este 409 NO es concurrencia sobre el feed item
// (el caso que el retry existente cubre), sino un conflicto de CONTACTO: reabrir ESTA conversación es
// imposible mientras el contacto tenga otra activa. Reintentar la misma conversación no puede funcionar
// nunca — debe distinguirse y reportarse como tal.
func TestAssignFeedItem_ContactConflictIsNotRetriedBlindly(t *testing.T) {
	reopenCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-closed","feedId":"channel:ch-test","closed":true}]}`))
		case r.Method == "PATCH":
			body, _ := io.ReadAll(r.Body)
			var p map[string]interface{}
			_ = json.Unmarshal(body, &p)
			if _, isAssign := p["teamId"]; isAssign {
				w.WriteHeader(200)
				return
			}
			reopenCount++
			w.WriteHeader(409)
			_, _ = w.Write([]byte(birdAnotherActiveConvBody))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	// Sin teléfono no hay forma de resolver la otra conversación: debe fallar RÁPIDO y con un error
	// tipado, en vez de gastar 3 reintentos condenados.
	err := c.AssignFeedItem(context.Background(), "conv-vieja", "", "team-a", "agent-1")
	if err == nil {
		t.Fatal("esperaba error")
	}
	if !errors.Is(err, ErrContactHasAnotherConversation) {
		t.Fatalf("esperaba ErrContactHasAnotherConversation para distinguirlo del 409 de concurrencia, got %v", err)
	}
	if reopenCount != 1 {
		t.Errorf("este 409 no debe reintentarse a ciegas (la misma conversación nunca se podrá reabrir); reopens=%d", reopenCount)
	}
}

// TestAssignFeedItem_ContactConflictSwitchesToActiveConversation: el caso que rescata el handoff. Con el
// teléfono, el cliente resuelve cuál es la conversación ACTIVA del contacto y asigna ESA, en vez de
// dejar al paciente en PICKUP MANUAL.
func TestAssignFeedItem_ContactConflictSwitchesToActiveConversation(t *testing.T) {
	var assignedFeedItems []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isFeedItemSearch(r):
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "conv-activa") {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"results":[{"id":"fi-activa","feedId":"channel:ch-test","closed":false}]}`))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-closed","feedId":"channel:ch-test","closed":true}]}`))
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/conversations"):
			// Lookup por teléfono: la conversación ACTIVA del contacto.
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"conv-activa","featuredParticipants":[{"identifierValue":"+573001234567"}]}]}`))
		case r.Method == "PATCH":
			body, _ := io.ReadAll(r.Body)
			var p map[string]interface{}
			_ = json.Unmarshal(body, &p)
			if _, isAssign := p["teamId"]; isAssign {
				assignedFeedItems = append(assignedFeedItems, r.URL.Path)
				w.WriteHeader(200)
				return
			}
			// Solo la conversación vieja está cerrada y da el conflicto de contacto.
			w.WriteHeader(409)
			_, _ = w.Write([]byte(birdAnotherActiveConvBody))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	if err := c.AssignFeedItem(context.Background(), "conv-vieja", "+573001234567", "team-a", "agent-1"); err != nil {
		t.Fatalf("el handoff debía completarse sobre la conversación activa del contacto, got %v", err)
	}
	if len(assignedFeedItems) != 1 || !strings.Contains(assignedFeedItems[0], "fi-activa") {
		t.Fatalf("esperaba la asignación sobre fi-activa, got %v", assignedFeedItems)
	}
	// La conversación buena queda cacheada para los envíos siguientes del paciente.
	if cached := c.GetCachedConversationID("+573001234567"); cached != "conv-activa" {
		t.Errorf("esperaba conv-activa en caché, got %q", cached)
	}
}
