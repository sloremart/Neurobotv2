package nectar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Las siete rutas (§3) contra un falso Néctar que exige lo mismo que el real: primero la
// firma X-Nectar-* (con el secreto de ida del bot), después la clave de servicio; 404 vacío
// para todo lo que no es del bot; 409 con reason para la ventana cerrada.

type fakeNectar struct {
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []string // "METHOD path"
	bodies   [][]byte
	notMine  bool
	conflict string // reason del 409 a devolver en /messages
}

func newFakeNectar(t *testing.T) *fakeNectar {
	f := &fakeNectar{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		full := "http://" + r.Host + r.URL.String()
		if err := VerifyNotice(reqSecret, r.Header.Get(SignatureHeader), r.Header.Get(TimestampHeader), full, body); err != nil {
			t.Logf("firma rechazada: %v url=%s", err, full)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get(ServiceKeyHeader) != svcKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, body)
		notMine, conflict := f.notMine, f.conflict
		f.mu.Unlock()
		if notMine {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/api/bot/conversations/resolve":
			_, _ = w.Write([]byte(`{"conversation_id":1234,"channel_id":7}`))
		case "/api/bot/conversations/1234/state":
			_, _ = w.Write([]byte(`{"state":"bot","window_open":true,"window_expires_at":null}`))
		case "/api/bot/conversations/1234/availability":
			_, _ = w.Write([]byte(`{"available":false,"reason":"fuera_de_horario"}`))
		case "/api/bot/conversations/1234/messages":
			if conflict != "" {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"x","reason":"` + conflict + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":55}`))
		case "/api/bot/conversations/1234/close":
			w.WriteHeader(http.StatusNoContent)
		case "/api/bot/conversations/1234/escalate":
			_, _ = w.Write([]byte(`{"state":"waiting","transitioned":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

const (
	svcKey    = "clave-servicio"
	reqSecret = "secreto-ida-bot"
)

func newTestClient(f *fakeNectar) *Client { return NewClient(f.srv.URL+"/", svcKey, reqSecret) }

func TestResolveGoesFromPhoneToConversation(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	r, err := c.Resolve(context.Background(), "573001112233", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.ConversationID != 1234 || r.ChannelID != 7 {
		t.Fatalf("resuelto mal: %+v", r)
	}
	var sent map[string]any
	_ = json.Unmarshal(f.bodies[0], &sent)
	if sent["phone"] != "573001112233" {
		t.Fatalf("cuerpo: %s", f.bodies[0])
	}
	if _, has := sent["channel_id"]; has {
		t.Fatal("sin canal elegido no se manda channel_id: Néctar elige el hilo actual")
	}
}

func TestSendTextMintsAFreshUUIDIdempotencyKeyPerSend(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	for i := 0; i < 2; i++ {
		if id, err := c.SendText(context.Background(), 1234, "hola"); err != nil || id != 55 {
			t.Fatalf("id=%d err=%v", id, err)
		}
	}
	var k1, k2 struct {
		Key  string `json:"idempotency_key"`
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(f.bodies[0], &k1)
	_ = json.Unmarshal(f.bodies[1], &k2)
	if _, err := uuid.Parse(k1.Key); err != nil {
		t.Fatalf("idempotency_key tiene que ser UUID: %q", k1.Key)
	}
	if k1.Key == k2.Key {
		t.Fatal("dos envíos no pueden compartir clave de idempotencia")
	}
	if k1.Type != "text" || k1.Text != "hola" {
		t.Fatalf("cuerpo: %+v", k1)
	}
}

func TestButtonsMapIDAndTitle(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	_, err := c.SendButtons(context.Background(), 1234, "elige", []ReplyButton{{ID: "p1", Title: "Agendar"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(f.bodies[0]) == "" || !json.Valid(f.bodies[0]) {
		t.Fatal("cuerpo inválido")
	}
	var got struct {
		Type    string        `json:"type"`
		Buttons []ReplyButton `json:"buttons"`
	}
	_ = json.Unmarshal(f.bodies[0], &got)
	if got.Type != "buttons" || len(got.Buttons) != 1 || got.Buttons[0].ID != "p1" || got.Buttons[0].Title != "Agendar" {
		t.Fatalf("%+v", got)
	}
}

func TestA404IsErrNotMineOnEveryRoute(t *testing.T) {
	// «Ya no es mía»: las siete contestan el mismo 404 vacío; el cliente lo traduce a UN
	// error que quien llama suelta sin reintentar.
	f := newFakeNectar(t)
	f.notMine = true
	c := newTestClient(f)
	ctx := context.Background()
	checks := []error{
		func() error { _, e := c.GetState(ctx, 1234); return e }(),
		func() error { _, e := c.GetAvailability(ctx, 1234); return e }(),
		func() error { _, e := c.Resolve(ctx, "57300", 0); return e }(),
		func() error { _, e := c.SendText(ctx, 1234, "x"); return e }(),
		c.Close(ctx, 1234),
		func() error {
			_, e := c.Escalate(ctx, 1234, Escalation{Reason: "bot_cannot_answer", Summary: "resumen"}, nil)
			return e
		}(),
	}
	for i, err := range checks {
		if !errors.Is(err, ErrNotMine) {
			t.Fatalf("ruta %d: esperaba ErrNotMine, got %v", i, err)
		}
	}
}

func TestWindowClosedIsAnInstructionNotATransportError(t *testing.T) {
	f := newFakeNectar(t)
	f.conflict = "window_closed"
	c := newTestClient(f)
	_, err := c.SendText(context.Background(), 1234, "x")
	if !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("409 window_closed → ErrWindowClosed, got %v", err)
	}
	f.conflict = "idempotency_key_reused"
	_, err = c.SendText(context.Background(), 1234, "x")
	if !errors.Is(err, ErrIdempotencyReused) {
		t.Fatalf("409 idempotency_key_reused → ErrIdempotencyReused, got %v", err)
	}
}

func TestEscalateCarriesReasonAndSummary(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	res, err := c.Escalate(context.Background(), 1234,
		Escalation{Reason: "patient_asked_for_human", Summary: "Paciente: X\nCUPS: 890374"}, nil)
	if err != nil || !res.Transitioned || res.State != "waiting" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	var got map[string]any
	_ = json.Unmarshal(f.bodies[0], &got)
	if got["reason"] != "patient_asked_for_human" || got["summary"] != "Paciente: X\nCUPS: 890374" {
		t.Fatalf("%v", got)
	}
	if _, has := got["suggested_team_id"]; has {
		t.Fatal("sin equipo sugerido no se manda el campo")
	}
}

func TestAvailabilityFalseIsANormalAnswer(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	a, err := c.GetAvailability(context.Background(), 1234)
	if err != nil {
		t.Fatalf("«no se puede escalar» es respuesta normal, no error: %v", err)
	}
	if a.Available || a.Reason != "fuera_de_horario" {
		t.Fatalf("%+v", a)
	}
}

func TestWrongCredentialsAre401(t *testing.T) {
	f := newFakeNectar(t)
	c := NewClient(f.srv.URL, "otra", reqSecret)
	if _, err := c.GetState(context.Background(), 1234); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("clave mala → ErrUnauthorized, got %v", err)
	}
	c = NewClient(f.srv.URL, svcKey, "otro-secreto")
	if _, err := c.GetState(context.Background(), 1234); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("firma mala → ErrUnauthorized, got %v", err)
	}
}

func TestAnUnconfiguredClientFailsClosed(t *testing.T) {
	var c *Client
	if _, err := c.GetState(context.Background(), 1); !errors.Is(err, ErrNotConfiguredAPI) {
		t.Fatal(err)
	}
	if _, err := NewClient("", "", "").SendText(context.Background(), 1, "x"); !errors.Is(err, ErrNotConfiguredAPI) {
		t.Fatal(err)
	}
}

func TestSessionKeyRoundTrip(t *testing.T) {
	id, ok := ConversationIDFromSessionKey(SessionKey(1234))
	if !ok || id != 1234 {
		t.Fatal("ida y vuelta de la clave de sesión")
	}
	if _, ok := ConversationIDFromSessionKey("+573001234567"); ok {
		t.Fatal("un teléfono no es una clave de Néctar")
	}
	if _, ok := ConversationIDFromSessionKey("nectar:abc"); ok {
		t.Fatal("una clave corrupta no es de Néctar")
	}
}

func TestNectarInstructionsArePermanentForTheSendRetry(t *testing.T) {
	// 401 y cliente sin configurar también: reintentar con las mismas credenciales falla igual.
	for _, err := range []error{ErrNotMine, ErrWindowClosed, ErrIdempotencyReused, ErrUnauthorized, ErrNotConfiguredAPI} {
		if !bird.IsPermanentSendError(err) {
			t.Fatalf("%v tiene que ser permanente: el pool no lo reintenta", err)
		}
	}
}

// §6.2 punto 1: el menú de retornos viaja OPACO en bot_return_menu, y su ausencia deja el
// escalate byte a byte como el de siempre (inocuidad).
func TestEscalateCarriesTheReturnMenuOpaque(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	menu := `{"paso_actual":"CONFIRM_ENTITY","menu":[{"etiqueta":"Los datos están bien, continúa","valor":"entity_ok"}]}`

	if _, err := c.Escalate(context.Background(), 1234, Escalation{Reason: "bot_cannot_answer", Summary: "resumen", ReturnMenu: []byte(menu)}, nil); err != nil {
		t.Fatal(err)
	}
	var got struct {
		BotReturnMenu json.RawMessage `json:"bot_return_menu"`
	}
	if err := json.Unmarshal(f.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	// Bytes tal cual: Néctar no lo interpreta, lo sella y se lo devuelve al agente.
	var sent, want any
	_ = json.Unmarshal(got.BotReturnMenu, &sent)
	_ = json.Unmarshal([]byte(menu), &want)
	if sent == nil || !jsonEqual(sent, want) {
		t.Fatalf("el menú no viajó íntegro: %s", got.BotReturnMenu)
	}
}

func TestEscalateWithoutMenuOmitsTheKeyEntirely(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	if _, err := c.Escalate(context.Background(), 1234, Escalation{Reason: "bot_cannot_answer", Summary: "resumen"}, nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(f.bodies[0], &body)
	if _, has := body["bot_return_menu"]; has {
		t.Fatalf("sin menú, la clave no se manda (ni vacía): %s", f.bodies[0])
	}
}

// --- El candado del botón «Resolver» (§8, acordado con Néctar el 2026-09-06) ---

// INOCUIDAD, medida en BYTES. El campo es aditivo, pero «aditivo» hay que demostrarlo: se
// serializa el cuerpo con los campos de SIEMPRE y se compara contra el JSON literal de antes
// del candado. Si alguien le quita el omitempty, le cambia el nombre a una clave, o mueve un
// campo de sitio, esta comparación lo delata — y esos son justo los cambios que del lado de
// Néctar se ven como un escalate distinto sin que aquí falle nada más.
func TestTheEscalateBodyWithoutTheGuardIsByteIdenticalToTheOldOne(t *testing.T) {
	team := int64(7)
	got, err := json.Marshal(escalateRequest{
		Reason:          "bot_cannot_answer",
		Summary:         "resumen",
		SuggestedTeamID: &team,
		BotReturnMenu:   json.RawMessage(`{"paso_actual":"MAIN_MENU","menu":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	const antes = `{"reason":"bot_cannot_answer","summary":"resumen","suggested_team_id":7,` +
		`"bot_return_menu":{"paso_actual":"MAIN_MENU","menu":[]}}`
	if string(got) != antes {
		t.Fatalf("el cuerpo dejó de ser el de siempre:\n got: %s\nantes: %s", got, antes)
	}

	// Y por el camino real, con el cliente entero: sin candado la clave NO viaja, ni vacía.
	f := newFakeNectar(t)
	c := newTestClient(f)
	if _, err := c.Escalate(context.Background(), 1234, Escalation{Reason: "bot_cannot_answer", Summary: "resumen"}, nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(f.bodies[0], &body)
	if _, has := body["candado_resolver"]; has {
		t.Fatalf("sin candado, la clave no se manda: %s", f.bodies[0])
	}
	// Y el alias de v1 tampoco: sin candado no viaja NINGUNA de las dos (fila A7 del acta).
	if _, has := body["resolve_lock"]; has {
		t.Fatalf("sin candado, el alias tampoco se manda: %s", f.bodies[0])
	}
}

// Con candado: el MOTIVO viaja como campo top-level, hermano de summary. Va fuera del texto
// porque el árbol de Néctar NO parsea el resumen (decisión suya explícita), y es su servidor
// —no una persona— quien lo lee para no dejar resolver el hilo a secas.
func TestTheEscalateBodyCarriesTheResolveGuardAtTopLevel(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)
	if _, err := c.Escalate(context.Background(), 1234, Escalation{Reason: "bot_cannot_answer", Summary: "resumen", ResolveLock: "cita_pendiente"}, nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(f.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body["candado_resolver"] != "cita_pendiente" {
		t.Fatalf("el candado tiene que viajar top-level con su motivo: %s", f.bodies[0])
	}
	// Y no se lleva por delante nada de lo de antes.
	if body["reason"] != "bot_cannot_answer" || body["summary"] != "resumen" {
		t.Fatalf("el resto del cuerpo cambió: %s", f.bodies[0])
	}
}

// DOBLE EMISIÓN DEL CANDADO (protocolo de escalado v1 de Néctar): el mismo
// motivo viaja bajo las DOS claves —`candado_resolver`, la de hoy, y `resolve_lock`, la de
// v1— para que los dos lados no tengan que desplegar a la vez. Néctar lee el nuevo cuando
// vienen los dos; el viejo se retira cuando el bot deje de emitirlo.
//
// El JSON se compara EXACTO —no clave a clave— porque lo que hay que fijar es que las dos
// claves llevan el MISMO valor y que ninguna otra se movió de sitio: un alias con el motivo de
// otra escalación sería peor que no mandarlo.
func TestTheResolveGuardTravelsUnderBothKeys(t *testing.T) {
	got, err := json.Marshal(escalateRequest{
		Reason:          "bot_cannot_answer",
		Summary:         "resumen",
		CandadoResolver: "cita_pendiente",
		ResolveLock:     "cita_pendiente",
	})
	if err != nil {
		t.Fatal(err)
	}
	const quiero = `{"reason":"bot_cannot_answer","summary":"resumen",` +
		`"candado_resolver":"cita_pendiente","resolve_lock":"cita_pendiente"}`
	if string(got) != quiero {
		t.Fatalf("el cuerpo del escalate con candado cambió:\n got: %s\nquiero: %s", got, quiero)
	}

	// Y por el camino real: quien llama pasa UN motivo y el cliente lo pone en las dos claves.
	f := newFakeNectar(t)
	c := newTestClient(f)
	if _, err := c.Escalate(context.Background(), 1234, Escalation{Reason: "bot_cannot_answer", Summary: "resumen", ResolveLock: "cita_pendiente"}, nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(f.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body["candado_resolver"] != "cita_pendiente" || body["resolve_lock"] != "cita_pendiente" {
		t.Fatalf("el motivo tiene que viajar IGUAL bajo las dos claves: %s", f.bodies[0])
	}
}

// El puente entrega el estado CRUDO de GET …/state, sin interpretarlo: quién decide qué significa
// `bot` o `waiting` es el bot (lo usa el tope de la escalación encolada sin atender).
func TestTheBridgeHandsTheRawConversationState(t *testing.T) {
	f := newFakeNectar(t)
	b := NewBridge(newTestClient(f), nil)
	got, err := b.ConversationState(context.Background(), 1234)
	if err != nil || got != "bot" {
		t.Fatalf("estado=%q err=%v", got, err)
	}
	if len(f.calls) != 1 || f.calls[0] != "GET /api/bot/conversations/1234/state" {
		t.Fatalf("ruta consultada: %v", f.calls)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// EL EQUIPO SUGERIDO VIAJA POR EL PUENTE (docs/DISENO-PLANTILLA-HANDOFF-Y-AGENTE-PREFERIDO.md
// §3.4). Hasta el 2026-09-18 el puente pasaba `nil` SIEMPRE, así que el campo existía en el
// esquema y nadie lo rellenaba nunca: lo que este test fija es el cable entero —el sobre que
// arma el handler llega al cuerpo—, porque el `nil` de antes no daba ningún error visible.
func TestBridgeEscalate_LlevaElEquipoSugeridoDelSobre(t *testing.T) {
	f := newFakeNectar(t)
	b := NewBridge(newTestClient(f), nil)
	team := int64(12)
	if err := b.Escalate(context.Background(), 1234,
		Escalation{Reason: "bot_cannot_answer", Summary: "resumen", SuggestedTeamID: &team}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(f.bodies[0], &got)
	if got["suggested_team_id"] != float64(12) {
		t.Fatalf("suggested_team_id = %v (%T), quiero 12", got["suggested_team_id"], got["suggested_team_id"])
	}
}

// Y sin él, el cuerpo es el de siempre: el campo no aparece (que es el estado de hoy, con
// NECTAR_TEAM_BY_GROUP vacía).
func TestBridgeEscalate_SinEquipoElCuerpoEsElDeSiempre(t *testing.T) {
	f := newFakeNectar(t)
	b := NewBridge(newTestClient(f), nil)
	if err := b.Escalate(context.Background(), 1234,
		Escalation{Reason: "bot_cannot_answer", Summary: "resumen"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(f.bodies[0], &got)
	if _, has := got["suggested_team_id"]; has {
		t.Fatal("sin equipo sugerido no se manda el campo")
	}
}

// P2-7: el 403 reason=escalation_disabled del escalado es un error tipado y permanente; otro 403
// sigue siendo un fallo genérico.
func TestClientEscalate_403DisabledIsTyped(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		typed      bool
	}{
		{"apagado", `{"error":"x","reason":"escalation_disabled"}`, true},
		{"otro motivo", `{"error":"x","reason":"other"}`, false},
		{"sin cuerpo", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := NewClient(srv.URL, svcKey, reqSecret).Escalate(context.Background(), 1234,
				Escalation{Reason: "bot_cannot_answer", Summary: "s"}, nil)
			if err == nil {
				t.Fatal("un 403 es error")
			}
			if got := errors.Is(err, ErrEscalationDisabled); got != tc.typed {
				t.Fatalf("typed=%v err=%v", got, err)
			}
			if tc.typed && !errors.Is(err, bird.ErrPermanentSend) {
				t.Fatal("tiene que ser permanente")
			}
		})
	}
}

// --- El contacto en GET …/state (Plan 44 §1.2) ---

func TestGetState_Contact(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, svcKey, reqSecret)

	body = `{"state":"bot","window_open":true,"window_expires_at":"2026-10-29T15:00:00Z",` +
		`"contact":{"identifier":"573118349733","bsuid":"","profile_name":"Ana Pérez"}}`
	st, err := c.GetState(context.Background(), 1234)
	if err != nil {
		t.Fatal(err)
	}
	if st.Contact == nil || *st.Contact != (Contact{Identifier: "573118349733", ProfileName: "Ana Pérez"}) {
		t.Fatalf("contact = %+v", st.Contact)
	}
	if st.State != "bot" || !st.WindowOpen || st.WindowExpiresAt == nil {
		t.Fatalf("los tres campos de siempre no cambian: %+v", st)
	}

	// Ausente ≠ vacío: sin el bloque, Contact es nil.
	body = `{"state":"bot","window_open":false,"window_expires_at":null}`
	st, err = c.GetState(context.Background(), 1234)
	if err != nil || st.Contact != nil {
		t.Fatalf("sin bloque contact: %+v err=%v", st.Contact, err)
	}
}

// B-16: el cuerpo de un código fuera de contrato entra recortado al texto del error, que sale en
// el log de ERROR y en Telegram. El principio (el motivo) se conserva y el corte no parte una runa.
func TestDo_ErrorMessageTruncatesBody(t *testing.T) {
	huge := `{"reason":"motivo_x","detalle":"` + strings.Repeat("ñ", 100_000) + `"}`
	for _, status := range []int{http.StatusForbidden, http.StatusConflict, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(huge))
		}))
		c := NewClient(srv.URL, svcKey, reqSecret)
		_, err := c.GetState(context.Background(), 1234)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: quería error", status)
		}
		msg := err.Error()
		if len(msg) >= 1024 {
			t.Errorf("status %d: el error mide %d bytes", status, len(msg))
		}
		if !strings.Contains(msg, "motivo_x") || !strings.Contains(msg, strconv.Itoa(status)) {
			t.Errorf("status %d: se perdió lo que sirve para diagnosticar: %q", status, msg)
		}
		if !utf8.ValidString(msg) {
			t.Errorf("status %d: el corte partió una runa", status)
		}
	}
}

func TestBodySnippet_ShortBodyUnchanged(t *testing.T) {
	if got := bodySnippet([]byte("  corto \n")); got != "corto" {
		t.Errorf("bodySnippet = %q", got)
	}
}
