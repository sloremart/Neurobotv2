package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// failingSessionReader falla como lo haría el driver: con un texto que trae comillas y nombres de la BD.
type failingSessionReader struct{}

var errSessionDriver = errors.New(`error 1054: unknown column "x" in 'sessions'`)

func (failingSessionReader) FindByID(context.Context, string) (*session.Session, error) {
	return nil, errSessionDriver
}

func (failingSessionReader) FindRecentByPhone(context.Context, string, int) ([]session.Session, error) {
	return nil, errSessionDriver
}

func (failingSessionReader) GetAllContext(context.Context, string) (map[string]string, error) {
	return nil, errSessionDriver
}

// TestSessionEndpoints_ErrorIsValidJSONWithoutDriverText: un fallo de la BD responde 500 con un JSON
// válido y genérico; el texto del driver va al log, no al cliente (A-10).
func TestSessionEndpoints_ErrorIsValidJSONWithoutDriverText(t *testing.T) {
	h := &InternalHandler{sessionReader: failingSessionReader{}}
	cases := map[string]struct {
		handler http.HandlerFunc
		url     string
	}{
		"sessions por id":       {h.HandleSessions, "/api/internal/sessions?id=s-1"},
		"sessions por teléfono": {h.HandleSessions, "/api/internal/sessions?phone=%2B573001234567"},
		"sessions/context":      {h.HandleSessionContext, "/api/internal/sessions/context?id=s-1"},
	}
	for name, tc := range cases {
		rec := httptest.NewRecorder()
		tc.handler(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		body := rec.Body.Bytes()
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: código %d, quiero 500", name, rec.Code)
		}
		if !json.Valid(body) {
			t.Errorf("%s: el cuerpo no es JSON válido: %s", name, body)
		}
		if strings.Contains(string(body), "unknown column") {
			t.Errorf("%s: el cuerpo filtra el texto del driver: %s", name, body)
		}
	}
}
