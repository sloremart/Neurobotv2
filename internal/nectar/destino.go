package nectar

import (
	"net"
	"net/url"
	"strings"
)

// Con quién habla el bot: el host de NECTAR_BASE_URL, y si ese host es un stub de pruebas.
//
// Vive aquí —y no en cada comando— porque la respuesta tiene que ser LA MISMA en los tres
// sitios que la preguntan: /health (que la publica), las pruebas de humo locales (que se niegan
// a mandarle tráfico sintético a un Néctar real) y la sonda manual. Un bot de desarrollo con
// NECTAR_BASE_URL apuntando al Néctar REAL le mandaría cierres y mensajes con ids sintéticos.

// hostsDeStub son los destinos que cuentan como "de pruebas". nectar_stub es el nombre de
// servicio que se le da al stub de Néctar en un compose de pruebas.
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

// SafeBaseURL contesta si la clave de servicio y la firma pueden viajar a ese destino: por https, o
// en claro solo a un stub local de pruebas. Una URL ilegible o sin esquema no lo es.
func SafeBaseURL(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || IsStubHost(baseURL)
}

// IsStubHost contesta si ese destino es un stub local de pruebas. Un host vacío NO es stub:
// «no sé a dónde apunta» nunca puede leerse como «es seguro».
func IsStubHost(baseURL string) bool {
	h := HostOf(baseURL)
	return h != "" && hostsDeStub[h]
}
