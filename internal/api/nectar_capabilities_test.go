package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// GET /api/nectar/capabilities — la SEGUNDA ruta del bot hacia Néctar (protocolo
// de escalado v1). Lo que se prueba es lo que su regla de fallo
// castiga: 401 y 405 son ERROR DE CONFIGURACIÓN del otro lado (alerta), 404 es «el adaptador no
// está desplegado» (sin alerta), así que un método mal registrado o una cadena firmada distinta
// no pueden pasar de aquí.

const (
	capabilitiesTestSecret = "secreto-de-capacidades-de-prueba"
	// capabilitiesTunnelHost es el anfitrión del túnel por el que Néctar llega al bot (ngrok,
	// como los webhooks de Bird). La cadena firmada es la URL TAL COMO SE CONECTA, y reconstructFullURL
	// la rearma con el esquema https por defecto de Neuro: el túnel entra siempre por https.
	capabilitiesTunnelHost = "neuro-tunnel.example"
	capabilitiesSignedURL  = "https://" + capabilitiesTunnelHost + NectarCapabilitiesPath
)

// capabilitiesRequest arma la petición como llega de verdad: el destino es el CAMINO y el
// anfitrión va en r.Host, que es lo que reconstructFullURL usa para rearmar la cadena firmada.
// (Pasarle la URL absoluta a httptest.NewRequest la deja también en r.URL y la reconstrucción
// sale DOBLADA — un artefacto del arnés que no ocurre contra un servidor real.)
func capabilitiesRequest(method string) *http.Request {
	req := httptest.NewRequest(method, NectarCapabilitiesPath, nil)
	req.Host = capabilitiesTunnelHost
	return req
}

// capabilitiesMux registra la ruta con el patrón COMPARTIDO (el mismo que cmd/server): así el
// 405 de un POST lo da el mux real y no una rama escrita para el test.
func capabilitiesMux(cfg *config.Config) *http.ServeMux {
	h := NewWebhookHandler(nil, nil, nil, cfg)
	mux := http.NewServeMux()
	mux.HandleFunc(NectarCapabilitiesPattern, h.HandleNectarCapabilities)
	return mux
}

// signCapabilitiesRequest firma con el esquema de los AVISOS (el mismo secreto) sobre CUERPO
// VACÍO: la firma cubre sello + URL + sha256(cuerpo), y el resumen de un cuerpo vacío es un
// valor fijo, así que no se estrena esquema de firma.
func signCapabilitiesRequest(req *http.Request, secret, signedURL string, at time.Time) {
	ts := nectar.Timestamp(at)
	req.Header.Set(nectar.TimestampHeader, ts)
	req.Header.Set(nectar.SignatureHeader, nectar.Sign(secret, ts, signedURL, []byte{}))
}

func signedCapabilitiesRequest(method, secret string) *http.Request {
	req := capabilitiesRequest(method)
	signCapabilitiesRequest(req, secret, capabilitiesSignedURL, time.Now())
	return req
}

func TestHandleNectarCapabilities_200WithETag(t *testing.T) {
	// Con un candado declarado (el real, el de la máquina de estados) el registro lo anuncia.
	statemachine.RegisterResolveGuard("CAPS_TEST_STEP", statemachine.ResolveGuardPendingReturn)
	t.Cleanup(func() { statemachine.RegisterResolveGuard("CAPS_TEST_STEP", "") })
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret, ProtocolEscaladoVersion: config.ProtocolEscaladoV1}
	rec := httptest.NewRecorder()
	capabilitiesMux(cfg).ServeHTTP(rec, signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret))

	if rec.Code != http.StatusOK {
		t.Fatalf("código %d, quiero 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	var reg nectar.CapabilityRegistry
	if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
		t.Fatalf("cuerpo no decodificable: %v", err)
	}
	// El cuerpo es el registro que el bot declara con la bandera en 1.
	body, _, err := nectar.Capabilities(config.ProtocolEscaladoV1, true).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != string(body) {
		t.Fatalf("el cuerpo servido no es el registro de v1:\n got %s\nwant %s", rec.Body.String(), body)
	}
	// El ETag es la versión ENTRECOMILLADA, y la versión es la del cuerpo: si no coincidieran,
	// el 304 de la siguiente lectura sería sobre otra cosa.
	if want := `"` + reg.Version + `"`; rec.Header().Get("ETag") != want {
		t.Fatalf("ETag = %q, quiero %q", rec.Header().Get("ETag"), want)
	}
	// Néctar CONSERVA las últimas capacidades conocidas cuando la lectura falla (su regla de
	// fallo), así que no se le puede decir `no-store`: lo que se pide es revalidar.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q", cc)
	}
}

func TestHandleNectarCapabilities_304OnIfNoneMatch(t *testing.T) {
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret, ProtocolEscaladoVersion: config.ProtocolEscaladoV1}
	mux := capabilitiesMux(cfg)

	first := httptest.NewRecorder()
	mux.ServeHTTP(first, signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("la primera lectura no trajo ETag")
	}

	// La comparación es la DÉBIL de RFC 9110 §13.1.2: el valor tal cual, con `W/`, dentro de una
	// lista y el comodín.
	for _, inm := range []string{etag, "W/" + etag, `"otra-cosa", ` + etag, "*"} {
		req := signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret)
		req.Header.Set("If-None-Match", inm)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Fatalf("If-None-Match %q: código %d, quiero 304", inm, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("If-None-Match %q: el 304 no lleva cuerpo, llevó %d bytes", inm, rec.Body.Len())
		}
		if rec.Header().Get("ETag") != etag {
			t.Fatalf("If-None-Match %q: el 304 tiene que repetir el ETag", inm)
		}
	}

	// Una versión VIEJA no es un 304: es la lectura nueva entera.
	req := signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret)
	req.Header.Set("If-None-Match", `"0000000000000000"`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("una versión vieja tiene que dar 200 con cuerpo, dio %d/%d bytes", rec.Code, rec.Body.Len())
	}
}

func TestHandleNectarCapabilities_401WithoutSignature(t *testing.T) {
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret}
	mux := capabilitiesMux(cfg)

	// El rechazo deja traza (un 401 mudo es justo lo que la regla de fallo de Néctar se traga):
	// se captura el log, que de paso mantiene limpia la salida de la prueba. Sin t.Parallel.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	cases := map[string]func() *http.Request{
		"sin cabeceras": func() *http.Request {
			return capabilitiesRequest(http.MethodGet)
		},
		"firmada con otro secreto": func() *http.Request {
			return signedCapabilitiesRequest(http.MethodGet, "otro-secreto")
		},
		"sello fuera de la ventana": func() *http.Request {
			req := capabilitiesRequest(http.MethodGet)
			signCapabilitiesRequest(req, capabilitiesTestSecret, capabilitiesSignedURL, time.Now().Add(-10*time.Minute))
			return req
		},
		// La firma va sobre la URL TAL COMO SE CONECTA: quien firme otro esquema, o el
		// anfitrión equivocado, firma otra cadena y el desenlace es un 401 permanente.
		"firmada sobre http en vez de https": func() *http.Request {
			req := capabilitiesRequest(http.MethodGet)
			signCapabilitiesRequest(req, capabilitiesTestSecret,
				strings.Replace(capabilitiesSignedURL, "https://", "http://", 1), time.Now())
			return req
		},
		"firmada sobre otro anfitrión": func() *http.Request {
			req := capabilitiesRequest(http.MethodGet)
			signCapabilitiesRequest(req, capabilitiesTestSecret,
				"https://otro-tunel.example"+NectarCapabilitiesPath, time.Now())
			return req
		},
	}
	for name, build := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, build())
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: código %d, quiero 401", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "protocol") {
			t.Fatalf("%s: un 401 no puede filtrar el registro", name)
		}
	}
	if got := strings.Count(logs.String(), "capacidades rechazadas"); got != len(cases) {
		t.Fatalf("cada 401 deja una traza: %d trazas para %d rechazos\n%s", got, len(cases), logs.String())
	}
	if !strings.Contains(logs.String(), capabilitiesSignedURL) {
		t.Fatalf("la traza lleva la URL que se verificó, para cotejarla con la que firma Néctar:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), capabilitiesTestSecret) {
		t.Fatal("la traza no puede llevar el secreto")
	}

	// Sin secreto configurado la ruta FALLA CERRADA (nectar.ErrNotConfigured): mejor un 401
	// —que del otro lado es alerta— que publicar el registro a quien pregunte.
	rec := httptest.NewRecorder()
	capabilitiesMux(&config.Config{}).ServeHTTP(rec, signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin NECTAR_NOTICE_SECRET: código %d, quiero 401", rec.Code)
	}
}

// La cadena firmada es la que reconstruye reconstructFullURL, que respeta las cabeceras del
// proxy: detrás de un proxy que habla http hacia el bot, Néctar firma lo que ve el proxy.
func TestHandleNectarCapabilities_SignedURLFollowsForwardedHeaders(t *testing.T) {
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret}
	req := capabilitiesRequest(http.MethodGet)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("X-Forwarded-Host", "10.66.0.1:8080")
	signCapabilitiesRequest(req, capabilitiesTestSecret, "http://10.66.0.1:8080"+NectarCapabilitiesPath, time.Now())

	rec := httptest.NewRecorder()
	capabilitiesMux(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d, quiero 200: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleNectarCapabilities_OnlyGETAndHEAD(t *testing.T) {
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret}
	mux := capabilitiesMux(cfg)

	// HEAD entra por el mismo patrón (net/http lo empareja con GET) y sirve las cabeceras sin
	// cuerpo: es lo que hace útil el ETag para una comprobación barata.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, signedCapabilitiesRequest(http.MethodHead, capabilitiesTestSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD: código %d, quiero 200", rec.Code)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("HEAD tiene que traer el ETag")
	}

	// Y cualquier otro método cae en el 405 del mux, que para Néctar es error de configuración
	// CON alerta. No se registra ningún otro método sobre este camino a propósito.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, signedCapabilitiesRequest(method, capabilitiesTestSecret))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: código %d, quiero 405", method, rec.Code)
		}
	}
}

// Con la bandera en 0 la ruta CONTESTA IGUAL —200 con el registro—, no 404: un 404 significa
// del otro lado «el adaptador v1 no está desplegado», y aquí sí lo está; lo que dice el
// registro es que todavía no emite v1.
func TestHandleNectarCapabilities_ZeroFlagStillAnswers200(t *testing.T) {
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret, ProtocolEscaladoVersion: config.ProtocolEscaladoV0}
	rec := httptest.NewRecorder()
	capabilitiesMux(cfg).ServeHTTP(rec, signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d, quiero 200 (nunca 404: el adaptador SÍ está desplegado)", rec.Code)
	}
	var reg nectar.CapabilityRegistry
	if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Protocol != config.ProtocolEscaladoV1 {
		t.Fatalf("protocol = %d: la ruta es de v1 aunque el bot emita el dialecto", reg.Protocol)
	}
	if reg.Capabilities.Escalation.Facts {
		t.Fatal("con la bandera en 0 el registro no puede declarar lo que el cable no lleva")
	}
	// Y al revés: lo que el bot LEE se declara igual con la bandera en 0. `return.valores` es
	// recepción, y la recepción no se apaga (D2).
	if !reg.Capabilities.Return.Values {
		t.Fatal("con la bandera en 0 la RECEPCIÓN sigue declarada: el lector está vivo")
	}
}

// El handler sirve lo que dice la BANDERA del Config, no una versión fija: con 0 y con 1 el
// ETag es otro, que es la señal con que Néctar relee.
func TestHandleNectarCapabilities_ETagFollowsConfigFlag(t *testing.T) {
	etag := func(version int) string {
		cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret, ProtocolEscaladoVersion: version}
		rec := httptest.NewRecorder()
		capabilitiesMux(cfg).ServeHTTP(rec, signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret))
		if rec.Code != http.StatusOK {
			t.Fatalf("versión %d: código %d", version, rec.Code)
		}
		return rec.Header().Get("ETag")
	}
	if v0, v1 := etag(config.ProtocolEscaladoV0), etag(config.ProtocolEscaladoV1); v0 == "" || v0 == v1 {
		t.Fatalf("el ETag tiene que cambiar con la bandera: v0=%q v1=%q", v0, v1)
	}
}

// El cableado: lo que se sirve sale de los candados REALMENTE declarados en cada petición, y
// declararlos mueve cuerpo y ETag (Néctar renegocia por versión).
func TestHandleNectarCapabilities_ResolveLockFollowsDeclaredGuards(t *testing.T) {
	cfg := &config.Config{NectarNoticeSecret: capabilitiesTestSecret, ProtocolEscaladoVersion: config.ProtocolEscaladoV1}
	mux := capabilitiesMux(cfg)
	read := func() (bool, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, signedCapabilitiesRequest(http.MethodGet, capabilitiesTestSecret))
		var reg nectar.CapabilityRegistry
		if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
			t.Fatalf("cuerpo no decodificable: %v", err)
		}
		return reg.Capabilities.Escalation.ResolveLock, rec.Header().Get("ETag")
	}

	lockOff, etagOff := read()
	if lockOff {
		t.Fatal("sin candados declarados resolve_lock debe ser false")
	}
	statemachine.RegisterResolveGuard("CAPS_TEST_STEP", statemachine.ResolveGuardPendingReturn)
	t.Cleanup(func() { statemachine.RegisterResolveGuard("CAPS_TEST_STEP", "") })
	lockOn, etagOn := read()
	if !lockOn {
		t.Fatal("con un candado declarado resolve_lock debe ser true")
	}
	if etagOn == etagOff {
		t.Errorf("el ETag no cambió con el flag: %s", etagOn)
	}
}
