package nectar

import (
	"net"
	"net/url"
	"strings"
)

// Con quién habla el bot: el host de NECTAR_BASE_URL, y si ese host es un stub de pruebas.
//
// Vive aquí —y no en cada comando— porque la respuesta tiene que ser LA MISMA en los tres
// sitios que la preguntan: /health (que la publica), el arnés de estrés (que se niega a
// martillear un Néctar real) y la sonda manual. El incidente del 2026-09-08 pasó justamente
// porque nadie preguntaba: el bot local de desarrollo tenía NECTAR_BASE_URL apuntando al
// Néctar REAL y el humo del arnés le mandó 3.301 cierres y 761 mensajes con ids sintéticos.

// hostsDeStub son los destinos que cuentan como "de pruebas". nectar_stub es el nombre del
// servicio en deploy/estres/docker-compose.stress.yml.
var hostsDeStub = map[string]bool{
	"localhost":   true,
	"127.0.0.1":   true,
	"::1":         true,
	"nectar_stub": true,
	"nectar-stub": true,
}

// HostOf devuelve el host (sin puerto, sin credenciales, sin ruta) de una base URL de Néctar.
// "" si está vacía o no se puede leer — que es lo mismo que decir «no hay destino».
func HostOf(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(u.Host); err == nil {
		return h
	}
	return u.Host
}

// IsStubHost contesta si ese destino es un stub local de pruebas. Un host vacío NO es stub:
// «no sé a dónde apunta» nunca puede leerse como «es seguro».
func IsStubHost(baseURL string) bool {
	h := HostOf(baseURL)
	return h != "" && hostsDeStub[h]
}
