package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

func signedRequest(t *testing.T, base, method, path, secret, key, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(nectar.ServiceKeyHeader, key)
	nectar.SignRequest(req, secret, time.Now(), []byte(body))
	return req
}

func TestStub_RecordsSignedCallsAndRejectsBadOnes(t *testing.T) {
	s := &stub{serviceKey: "k", requestSecret: "s", scheme: "http"}
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	const path = "/api/bot/conversations/7/messages"
	ok, _ := http.DefaultClient.Do(signedRequest(t, ts.URL, "POST", path, "s", "k", `{"type":"list"}`))
	if ok.StatusCode != 200 {
		t.Fatalf("firmada: %d", ok.StatusCode)
	}
	bad, _ := http.DefaultClient.Do(signedRequest(t, ts.URL, "POST", path, "otro", "k", `{}`))
	if bad.StatusCode != 401 {
		t.Fatalf("firma ajena debía dar 401, dio %d", bad.StatusCode)
	}
	noKey, _ := http.DefaultClient.Do(signedRequest(t, ts.URL, "POST", path, "s", "x", `{}`))
	if noKey.StatusCode != 401 {
		t.Fatalf("clave ajena debía dar 401, dio %d", noKey.StatusCode)
	}

	resp, _ := http.Get(ts.URL + "/records?route=messages")
	var got struct {
		Total   int      `json:"total"`
		Records []record `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || !strings.Contains(string(got.Records[0].Body), `"list"`) {
		t.Fatalf("solo la llamada válida debía registrarse: %+v", got)
	}
}

func TestStub_ResolveIDIsStableAndPositive(t *testing.T) {
	a, b := stableID("573001"), stableID("573001")
	if a != b || a <= 0 {
		t.Fatal("el id debe ser estable y positivo")
	}
}
