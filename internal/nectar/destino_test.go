package nectar

import "testing"

func TestHostOfNoPublicaNadaMasQueElHost(t *testing.T) {
	casos := map[string]string{
		"https://nectar.colibrixa.com":                "nectar.colibrixa.com",
		"https://nectar.colibrixa.com/api/bot":        "nectar.colibrixa.com",
		"https://user:clave@nectar.colibrixa.com:443": "nectar.colibrixa.com",
		"http://127.0.0.1:9099/":                      "127.0.0.1",
		"  http://localhost:8080  ":                   "localhost",
		"":                                            "",
		"no-es-una-url":                               "",
	}
	for in, want := range casos {
		if got := HostOf(in); got != want {
			t.Errorf("HostOf(%q) = %q, quería %q", in, got, want)
		}
	}
}

func TestIsStubHost(t *testing.T) {
	for _, u := range []string{"http://localhost:9099", "http://127.0.0.1:8080/x", "http://nectar_stub", "https://nectar-stub:443"} {
		if !IsStubHost(u) {
			t.Errorf("%q es un stub", u)
		}
	}
	for _, u := range []string{"https://nectar.colibrixa.com", "http://10.66.0.1:8080", "", "basura"} {
		if IsStubHost(u) {
			t.Errorf("%q NO es un stub: «no sé» nunca es «es seguro»", u)
		}
	}
}
