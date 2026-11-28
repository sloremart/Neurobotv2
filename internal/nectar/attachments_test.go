package nectar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// attStub es un falso Néctar de adjuntos: verifica la firma sobre los bytes crudos, anota cada
// petición y contesta con lo que dicte el guion de la ruta.
type attStub struct {
	srv *httptest.Server
	mu  sync.Mutex
	// reqs son las peticiones recibidas, en orden.
	reqs []attReq
	// script contesta por ruta; recibe el número de intento de esa ruta (desde 1).
	script func(path string, attempt int, w http.ResponseWriter)
	counts map[string]int
}

type attReq struct {
	path, contentType string
	body              []byte
}

func newAttStub(t *testing.T, script func(path string, attempt int, w http.ResponseWriter)) *attStub {
	t.Helper()
	s := &attStub{script: script, counts: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		full := "http://" + r.Host + r.URL.String()
		if err := VerifyNotice(reqSecret, r.Header.Get(SignatureHeader), r.Header.Get(TimestampHeader), full, body); err != nil {
			t.Errorf("firma rechazada: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get(ServiceKeyHeader) != svcKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, attReq{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: body})
		s.counts[r.URL.Path]++
		n := s.counts[r.URL.Path]
		s.mu.Unlock()
		s.script(r.URL.Path, n, w)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *attStub) client() *Client { return NewClient(s.srv.URL, svcKey, reqSecret) }

func (s *attStub) requests() []attReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]attReq(nil), s.reqs...)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

const uploaded201 = `{"attachment_id":"att-1","mime":"application/pdf","size":3,"sha256":"abc","filename":"r.pdf","expires_at":"2026-12-01T10:00:00Z"}`

func uploadOK(path string, _ int, w http.ResponseWriter) {
	if path == "/api/bot/uploads" {
		writeJSON(w, http.StatusCreated, uploaded201)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// parseMultipart devuelve los campos de texto y los bytes del archivo del cuerpo recibido.
func parseMultipart(t *testing.T, r attReq) (fields map[string]string, file []byte, fileName string) {
	t.Helper()
	mt, params, err := mime.ParseMediaType(r.contentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content-type %q: %v", r.contentType, err)
	}
	mr := multipart.NewReader(bytes.NewReader(r.body), params["boundary"])
	fields = map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		if p.FormName() == "file" {
			file, fileName = data, p.FileName()
			continue
		}
		fields[p.FormName()] = string(data)
	}
	return fields, file, fileName
}

func TestUpload_MultipartSignedOverRawBody(t *testing.T) {
	s := newAttStub(t, uploadOK) // la firma se verifica en el stub sobre los bytes crudos
	pdf := []byte("%PDF-1.4 contenido")
	if _, err := s.client().Upload(context.Background(), "key-1", "r.pdf", pdf); err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	if len(reqs) != 1 || reqs[0].path != "/api/bot/uploads" {
		t.Fatalf("peticiones: %+v", reqs)
	}
	fields, file, fileName := parseMultipart(t, reqs[0])
	if fields["idempotency_key"] != "key-1" || fields["filename"] != "r.pdf" {
		t.Fatalf("campos: %v", fields)
	}
	if string(file) != string(pdf) || fileName != "r.pdf" {
		t.Fatalf("archivo: %q %q", file, fileName)
	}
}

func TestUpload_ParsesAttachment(t *testing.T) {
	s := newAttStub(t, uploadOK)
	att, err := s.client().Upload(context.Background(), "k", "r.pdf", []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := Attachment{
		ID: "att-1", MIME: "application/pdf", Size: 3, SHA256: "abc", Filename: "r.pdf",
		ExpiresAt: time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC),
	}
	if !att.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("expires_at: %v", att.ExpiresAt)
	}
	att.ExpiresAt = want.ExpiresAt
	if att != want {
		t.Fatalf("got %+v", att)
	}
}

func TestUpload_TypedErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"415", 415, `{"error":"unsupported_media_type"}`, ErrUnsupportedMedia},
		{"413", 413, `{"error":"too_large","max_bytes":16777216}`, ErrTooLarge},
		{"409", 409, `{"error":"idempotency_key_reused"}`, ErrIdempotencyReused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttStub(t, func(_ string, _ int, w http.ResponseWriter) { writeJSON(w, tc.status, tc.body) })
			_, err := s.client().Upload(context.Background(), "k", "r.pdf", []byte("x"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, quiero %v", err, tc.want)
			}
		})
	}
}

func TestUpload_413PerType(t *testing.T) {
	for _, body := range []string{
		`{"error":"too_large","max_bytes":16777216}`, // PDF
		`{"error":"too_large","max_bytes":5242880}`,  // imagen
	} {
		s := newAttStub(t, func(_ string, _ int, w http.ResponseWriter) { writeJSON(w, 413, body) })
		if _, err := s.client().Upload(context.Background(), "k", "r.bin", []byte("x")); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("%s: got %v", body, err)
		}
	}
}

func TestUpload_503RetryAfter(t *testing.T) {
	s := newAttStub(t, func(_ string, attempt int, w http.ResponseWriter) {
		if attempt == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, uploaded201)
	})
	c := s.client()
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if _, err := c.Upload(context.Background(), "key-503", "r.pdf", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 5*time.Second {
		t.Fatalf("esperas: %v", slept)
	}
	reqs := s.requests()
	if len(reqs) != 2 {
		t.Fatalf("peticiones: %d", len(reqs))
	}
	for _, r := range reqs {
		if f, _, _ := parseMultipart(t, r); f["idempotency_key"] != "key-503" {
			t.Fatalf("la clave cambió: %v", f)
		}
	}
}

func TestUpload_503TwiceStops(t *testing.T) {
	s := newAttStub(t, func(_ string, _ int, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := s.client()
	c.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.Upload(context.Background(), "k", "r.pdf", []byte("x")); err == nil {
		t.Fatal("el segundo 503 es un error")
	}
	if n := len(s.requests()); n != 2 {
		t.Fatalf("un solo reintento, hubo %d peticiones", n)
	}
}

func TestSendAttachment_BodyAndErrors(t *testing.T) {
	t.Run("cuerpo con caption y reintento idempotente", func(t *testing.T) {
		s := newAttStub(t, func(_ string, attempt int, w http.ResponseWriter) {
			if attempt == 1 {
				writeJSON(w, http.StatusCreated, `{"id":900}`)
				return
			}
			writeJSON(w, http.StatusOK, `{"id":900}`)
		})
		id, err := s.client().SendAttachment(context.Background(), 77, "att-1", "Resultado")
		if err != nil || id != 900 {
			t.Fatalf("id=%d err=%v", id, err)
		}
		var got struct {
			Key        string `json:"idempotency_key"`
			Type       string `json:"type"`
			Attachment struct {
				ID      string `json:"id"`
				Caption string `json:"caption"`
			} `json:"attachment"`
		}
		if err := json.Unmarshal(s.requests()[0].body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Key == "" || got.Type != "attachment" || got.Attachment.ID != "att-1" || got.Attachment.Caption != "Resultado" {
			t.Fatalf("cuerpo: %+v", got)
		}
		if id, err := s.client().SendAttachment(context.Background(), 77, "att-1", "Resultado"); err != nil || id != 900 {
			t.Fatalf("reintento: id=%d err=%v", id, err)
		}
	})
	t.Run("caption vacío no viaja", func(t *testing.T) {
		s := newAttStub(t, func(_ string, _ int, w http.ResponseWriter) { writeJSON(w, http.StatusCreated, `{"id":1}`) })
		if _, err := s.client().SendAttachment(context.Background(), 77, "att-1", ""); err != nil {
			t.Fatal(err)
		}
		var raw map[string]map[string]any
		_ = json.Unmarshal(s.requests()[0].body, &raw)
		if _, has := raw["attachment"]["caption"]; has {
			t.Fatalf("el caption vacío no debe viajar: %s", s.requests()[0].body)
		}
	})
	errCases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"ventana cerrada por reason", 409, `{"error":"La ventana de 24 horas está cerrada","reason":"window_closed"}`, ErrWindowClosed},
		{"clave reutilizada", 409, `{"error":"x","reason":"idempotency_key_reused"}`, ErrIdempotencyReused},
		{"422 expired", 422, `{"error":"attachment_unavailable","reason":"expired"}`, ErrAttachmentUnavailable},
		{"422 not_found", 422, `{"error":"attachment_unavailable","reason":"not_found"}`, ErrAttachmentUnavailable},
		{"404 no es mia", 404, ``, ErrNotMine},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttStub(t, func(_ string, _ int, w http.ResponseWriter) { writeJSON(w, tc.status, tc.body) })
			c := s.client()
			_, err := c.SendAttachment(context.Background(), 77, "att-1", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, quiero %v", err, tc.want)
			}
			if errors.Is(tc.want, ErrNotMine) && !NewBridge(c, nil).IsNotMine(err) {
				t.Fatal("IsNotMine debe reconocer el 404")
			}
		})
	}
}

func TestBridge_SendDocumentUploadsThenSends(t *testing.T) {
	s := newAttStub(t, func(path string, _ int, w http.ResponseWriter) {
		if path == "/api/bot/uploads" {
			writeJSON(w, http.StatusCreated, uploaded201)
			return
		}
		writeJSON(w, http.StatusCreated, `{"id":321}`)
	})
	id, err := NewBridge(s.client(), nil).SendDocument(context.Background(), 77, "r.pdf", []byte("x"), "pie")
	if err != nil || id != "321" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	reqs := s.requests()
	if len(reqs) != 2 || reqs[0].path != "/api/bot/uploads" || reqs[1].path != "/api/bot/conversations/77/messages" {
		t.Fatalf("peticiones: %+v", reqs)
	}
	var sent struct {
		Attachment struct {
			ID string `json:"id"`
		} `json:"attachment"`
	}
	_ = json.Unmarshal(reqs[1].body, &sent)
	if sent.Attachment.ID != "att-1" {
		t.Fatalf("debe enviar el id subido: %s", reqs[1].body)
	}
}

func TestBridge_SendDocumentUploadErrorDoesNotSend(t *testing.T) {
	s := newAttStub(t, func(_ string, _ int, w http.ResponseWriter) { writeJSON(w, 415, `{"error":"unsupported_media_type"}`) })
	_, err := NewBridge(s.client(), nil).SendDocument(context.Background(), 77, "r.exe", []byte("x"), "")
	if !errors.Is(err, ErrUnsupportedMedia) || !errors.Is(err, bird.ErrPermanentSend) {
		t.Fatalf("got %v", err)
	}
	if n := len(s.requests()); n != 1 {
		t.Fatalf("no debe enviar tras fallar la subida: %d peticiones", n)
	}
}

func TestLineRouter_SendDocumentUsesConversationLine(t *testing.T) {
	handler := func(path string, _ int, w http.ResponseWriter) {
		if path == "/api/bot/uploads" {
			writeJSON(w, http.StatusCreated, uploaded201)
			return
		}
		writeJSON(w, http.StatusCreated, `{"id":5}`)
	}
	main, legacy := newAttStub(t, handler), newAttStub(t, handler)
	reg := NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}})
	router := NewLineRouter(reg, map[Line]*Bridge{
		LineMain:   NewBridge(main.client(), nil),
		LineLegacy: NewBridge(legacy.client(), nil),
	})
	if err := reg.Register(context.Background(), 1234, LineLegacy); err != nil {
		t.Fatal(err)
	}
	if _, err := router.SendDocument(context.Background(), 1234, "r.pdf", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	if m, l := len(main.requests()), len(legacy.requests()); m != 0 || l != 2 {
		t.Fatalf("main=%d legacy=%d, quiero 0 y 2 (subida y envío)", m, l)
	}
}

func TestLineRouter_SendDocumentRegistryErrorFailsWithoutSending(t *testing.T) {
	handler := func(string, int, http.ResponseWriter) { t.Error("no debe llamar a nadie") }
	main, legacy := newAttStub(t, handler), newAttStub(t, handler)
	reg := NewLineRegistry(&fakeLineStore{lines: map[int64]Line{}, err: errors.New("bd caida")})
	router := NewLineRouter(reg, map[Line]*Bridge{
		LineMain:   NewBridge(main.client(), nil),
		LineLegacy: NewBridge(legacy.client(), nil),
	})
	if _, err := router.SendDocument(context.Background(), 1234, "r.pdf", []byte("x"), ""); err == nil {
		t.Fatal("un error del registro no puede enviar")
	}
}

func TestBridge_SendDocumentUploadKeyIsDeterministic(t *testing.T) {
	s := newAttStub(t, func(path string, _ int, w http.ResponseWriter) {
		if path == "/api/bot/uploads" {
			writeJSON(w, http.StatusCreated, uploaded201)
			return
		}
		writeJSON(w, http.StatusCreated, `{"id":1}`)
	})
	br := NewBridge(s.client(), nil)
	for _, data := range []string{"uno", "uno", "dos"} {
		if _, err := br.SendDocument(context.Background(), 77, "r.pdf", []byte(data), ""); err != nil {
			t.Fatal(err)
		}
	}
	var keys []string
	for _, r := range s.requests() {
		if r.path == "/api/bot/uploads" {
			f, _, _ := parseMultipart(t, r)
			keys = append(keys, f["idempotency_key"])
		}
	}
	if len(keys) != 3 || keys[0] != keys[1] || keys[0] == keys[2] {
		t.Fatalf("claves de subida: %v", keys)
	}
}

func TestUpload_404IsNotNotMine(t *testing.T) {
	s := newAttStub(t, func(_ string, _ int, w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) })
	c := s.client()
	_, err := c.Upload(context.Background(), "k", "r.pdf", []byte("x"))
	if err == nil || errors.Is(err, ErrNotMine) || NewBridge(c, nil).IsNotMine(err) {
		t.Fatalf("un 404 de la subida no es «ya no es mía»: %v", err)
	}
}

// La subida lleva su propio plazo (más largo); el del resto de las llamadas no cambia.
func TestUpload_HasItsOwnLongerTimeout(t *testing.T) {
	c := NewClient("http://nectar.test", svcKey, reqSecret)
	if c.http.Timeout != requestTimeout {
		t.Errorf("el plazo normal cambió: %v", c.http.Timeout)
	}
	if c.uploadHTTP == nil || c.uploadHTTP.Timeout != uploadTimeout || uploadTimeout <= requestTimeout {
		t.Errorf("plazo de subida: %+v (uploadTimeout=%v)", c.uploadHTTP, uploadTimeout)
	}
	if c.uploadHTTP.CheckRedirect == nil {
		t.Error("la subida debe seguir sin seguir redirecciones")
	}

	// Comportamiento: una subida lenta pasa con su plazo aunque el normal ya habría vencido.
	s := newAttStub(t, func(path string, attempt int, w http.ResponseWriter) {
		time.Sleep(300 * time.Millisecond)
		uploadOK(path, attempt, w)
	})
	cl := s.client()
	cl.http.Timeout = 50 * time.Millisecond
	cl.uploadHTTP.Timeout = 5 * time.Second
	if _, err := cl.Upload(context.Background(), "k", "r.pdf", []byte("%PDF")); err != nil {
		t.Fatalf("la subida debía usar su plazo: %v", err)
	}
	if _, err := cl.GetState(context.Background(), 1); err == nil {
		t.Error("el resto de las llamadas sigue con el plazo corto")
	}
}
