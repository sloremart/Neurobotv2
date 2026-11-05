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

// M-1: solo https, o http hacia un stub local, con esquema explícito y host.
func TestSafeBaseURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://nectar.colibrixa.com": true,
		"HTTPS://nectar.colibrixa.com": true,
		"http://localhost:9999":        true,
		"http://127.0.0.1:9999":        true,
		"http://[::1]:9999":            true,
		"http://nectar-stub:9999":      true,
		"http://nectar.colibrixa.com":  false,
		"ftp://localhost:1":            false,
		"gopher://nectar-stub:1":       false,
		"//localhost:1":                false,
		"//nectar.colibrixa.com":       false,
		"https://:443":                 false,
		"http://:9999":                 false,
		"https://":                     false,
		"localhost:9999":               false,
		"":                             false,
	} {
		if got := SafeBaseURL(url); got != want {
			t.Errorf("SafeBaseURL(%q) = %v, quiero %v", url, got, want)
		}
	}
}
