package nectar

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// La LECTURA del horario (GET /api/bot/schedule, contrato del 2026-09-12) contra un falso
// Néctar que exige lo mismo que el real: la firma X-Nectar-* sobre la URL COMPLETA (con la
// query, que es donde viaja channel_id) y la clave de servicio.

type fakeSchedule struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []string // "GET /api/bot/schedule?channel_id=7"
	conds  []string // el If-None-Match de cada llamada ("" = sin cabecera)
	status int
	body   string
}

func newFakeSchedule(t *testing.T) *fakeSchedule {
	f := &fakeSchedule{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		full := "http://" + r.Host + r.URL.String()
		if err := VerifyNotice(reqSecret, r.Header.Get(SignatureHeader), r.Header.Get(TimestampHeader), full, body); err != nil {
			t.Errorf("firma rechazada: %v url=%s", err, full)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get(ServiceKeyHeader) != svcKey {
			t.Errorf("clave de servicio ausente o distinta: %q", r.Header.Get(ServiceKeyHeader))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
		f.conds = append(f.conds, r.Header.Get("If-None-Match"))
		status, payload := f.status, f.body
		f.mu.Unlock()
		if status == http.StatusNotModified {
			// Un 304 va SIN cuerpo (RFC 9110 §15.4.5); escribir uno aquí haría que el servidor
			// de pruebas se quejara y taparía lo que este test mide.
			w.WriteHeader(status)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"x"}`))
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSchedule) client() *Client { return NewClient(f.srv.URL, svcKey, reqSecret) }

func (f *fakeSchedule) lastCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeSchedule) lastCond() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.conds) == 0 {
		return ""
	}
	return f.conds[len(f.conds)-1]
}

func (f *fakeSchedule) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

// El volcado del contrato (§«Respuesta 200»): dos ventanas el martes (almuerzo), sábado y
// dos festivos.
const scheduleSample = `{
  "timezone": "America/Bogota",
  "version": "b7c1e2f0",
  "generated_at": "2026-09-12T10:15:00-05:00",
  "windows": [
    {"weekday": 1, "from": "07:00", "to": "17:00"},
    {"weekday": 2, "from": "07:00", "to": "12:00"},
    {"weekday": 2, "from": "14:00", "to": "17:00"},
    {"weekday": 6, "from": "07:00", "to": "11:00"}
  ],
  "holidays": [
    {"date": "2026-10-12", "name": "Día de la Raza"},
    {"date": "2026-11-02", "name": "Todos los Santos"}
  ]
}`

func TestGetSchedule_LeeElVolcadoDelContrato(t *testing.T) {
	f := newFakeSchedule(t)
	f.body = scheduleSample

	got, err := f.client().GetSchedule(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	// Sin channel_id la ruta va PELADA: el ámbito son todos los canales de la entidad.
	if want := "GET /api/bot/schedule"; f.lastCall() != want {
		t.Errorf("ruta = %q, se esperaba %q", f.lastCall(), want)
	}
	if got.Timezone != "America/Bogota" || got.Version != "b7c1e2f0" {
		t.Errorf("timezone/version = %q/%q", got.Timezone, got.Version)
	}
	if got.GeneratedAt != "2026-09-12T10:15:00-05:00" {
		t.Errorf("generated_at = %q", got.GeneratedAt)
	}
	if len(got.Windows) != 4 {
		t.Fatalf("ventanas = %d, se esperaban 4", len(got.Windows))
	}
	if w := got.Windows[2]; w.Weekday != 2 || w.From != "14:00" || w.To != "17:00" {
		t.Errorf("tercera ventana = %+v", w)
	}
	if len(got.Holidays) != 2 || got.Holidays[0].Date != "2026-10-12" ||
		got.Holidays[1].Name != "Todos los Santos" {
		t.Errorf("festivos = %+v", got.Holidays)
	}
}

func TestGetSchedule_FiltraPorCanal(t *testing.T) {
	f := newFakeSchedule(t)
	f.body = `{"timezone":"America/Bogota","version":"v1","windows":[]}`

	if _, err := f.client().GetSchedule(context.Background(), 7, ""); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if want := "GET /api/bot/schedule?channel_id=7"; f.lastCall() != want {
		t.Errorf("ruta = %q, se esperaba %q", f.lastCall(), want)
	}
}

func TestGetSchedule_Errores(t *testing.T) {
	f := newFakeSchedule(t)
	f.body = scheduleSample

	cases := []struct {
		name   string
		status int
		want   error
	}{
		// El 404 de esta ruta NO es «la conversación ya no es del bot»: es un channel_id que
		// no pertenece a la entidad. Se distingue para que el log diga qué arreglar.
		{"canal ajeno", http.StatusNotFound, ErrScheduleUnknownChannel},
		{"clave inválida", http.StatusUnauthorized, ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.mu.Lock()
			f.status = tc.status
			f.mu.Unlock()
			_, err := f.client().GetSchedule(context.Background(), 0, "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, se esperaba %v", err, tc.want)
			}
		})
	}

	t.Run("caída del servidor", func(t *testing.T) {
		f.mu.Lock()
		f.status = http.StatusInternalServerError
		f.mu.Unlock()
		_, err := f.client().GetSchedule(context.Background(), 0, "")
		if err == nil {
			t.Fatal("un 500 tiene que ser error: el bot conserva la última lectura")
		}
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrScheduleUnknownChannel) {
			t.Errorf("un 500 no es ninguno de los errores de negocio: %v", err)
		}
	})
}

// --- LECTURA CONDICIONAL (fila E45 del ACTA-PROTOCOLO-DE-ESCALADO-v1, 2026-09-14) ---
//
// La PRIMERA lectura no puede llevar condicional (no hay versión que citar) y las siguientes
// sí: es lo que convierte un refresco cada pocos minutos en un 304 de tres líneas cuando el
// horario no cambió.
func TestGetSchedule_LaPrimeraLecturaVaSinCondicional(t *testing.T) {
	f := newFakeSchedule(t)
	f.body = scheduleSample

	if _, err := f.client().GetSchedule(context.Background(), 0, ""); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got := f.lastCond(); got != "" {
		t.Fatalf("If-None-Match = %q; sin versión no se cita ninguna", got)
	}
}

// Con versión: la cabecera viaja ENTRECOMILLADA (es un ETag, no una cadena cualquiera) y un
// 304 se distingue como «sin cambios» — no es un fallo ni una lectura vacía que borre nada.
func TestGetSchedule_CitaLaVersionYDistingueEl304(t *testing.T) {
	f := newFakeSchedule(t)
	f.body = scheduleSample
	f.setStatus(http.StatusNotModified)

	got, err := f.client().GetSchedule(context.Background(), 0, "b7c1e2f0")
	if !errors.Is(err, ErrScheduleNotModified) {
		t.Fatalf("err = %v, se esperaba ErrScheduleNotModified", err)
	}
	if want := `"b7c1e2f0"`; f.lastCond() != want {
		t.Fatalf("If-None-Match = %q, se esperaba %s", f.lastCond(), want)
	}
	if len(got.Windows) != 0 || got.Version != "" {
		t.Fatalf("un 304 no trae horario: %+v", got)
	}

	// Y una versión que YA venía entrecomillada no se entrecomilla dos veces.
	f.setStatus(http.StatusOK)
	if _, err := f.client().GetSchedule(context.Background(), 0, `"b7c1e2f0"`); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if want := `"b7c1e2f0"`; f.lastCond() != want {
		t.Fatalf("If-None-Match = %q, se esperaba %s", f.lastCond(), want)
	}
}

// Un 304 que llega SIN haberlo pedido (nadie citó una versión) no es «sin cambios»: es una
// respuesta que el bot no pidió, y tratarla como éxito dejaría un horario vacío pasando por
// bueno. Sigue el camino de cualquier código raro: error.
func TestGetSchedule_Un304SinCondicionalEsError(t *testing.T) {
	f := newFakeSchedule(t)
	f.body = scheduleSample
	f.setStatus(http.StatusNotModified)

	_, err := f.client().GetSchedule(context.Background(), 0, "")
	if err == nil || errors.Is(err, ErrScheduleNotModified) {
		t.Fatalf("err = %v; un 304 no pedido no es «sin cambios»", err)
	}
}

// Sin credenciales no se llama a nadie: el cliente falla cerrado, como en las otras rutas.
func TestGetSchedule_SinConfigurar(t *testing.T) {
	_, err := NewClient("", "", "").GetSchedule(context.Background(), 0, "")
	if !errors.Is(err, ErrNotConfiguredAPI) {
		t.Fatalf("err = %v, se esperaba ErrNotConfiguredAPI", err)
	}
}
