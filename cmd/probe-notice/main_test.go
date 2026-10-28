package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbe_RefusesNonStubURL(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"-url", "https://bot.produccion.example.com/api/webhooks/nectar"}, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "me niego") || out.Len() != 0 {
		t.Fatalf("debía negarse sin enviar nada: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestProbe_SendsSignedNoticeToLocalhost(t *testing.T) {
	var sig, ts, proto string
	var got bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig, ts = r.Header.Get("X-Nectar-Signature"), r.Header.Get("X-Nectar-Request-Timestamp")
		proto = r.Header.Get("X-Forwarded-Proto")
		_, _ = got.ReadFrom(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	var out, errOut bytes.Buffer
	code := run([]string{"-url", srv.URL, "-text", "hola", "-event-id", "e1", "-conversation", "9"}, &out, &errOut)
	if code != 0 || !strings.HasPrefix(out.String(), "200") || sig == "" || ts == "" {
		t.Fatalf("code=%d out=%q err=%q sig=%q", code, out.String(), errOut.String(), sig)
	}
	if proto != "http" {
		t.Fatalf("X-Forwarded-Proto: %q", proto)
	}
	if !strings.Contains(got.String(), `"event_id":"e1"`) || !strings.Contains(got.String(), `"text":"hola"`) {
		t.Fatalf("cuerpo: %s", got.String())
	}
}

func TestProbe_RefusesDisguisedOrEmptyURLs(t *testing.T) {
	for _, u := range []string{
		"http://localhost@evil.com/api/webhooks/nectar",
		"http://localhost.evil.com/api/webhooks/nectar",
		"",
	} {
		var out, errOut bytes.Buffer
		if code := run([]string{"-url", u}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "me niego") {
			t.Errorf("%q debía rechazarse: code=%d err=%q", u, code, errOut.String())
		}
	}
}
