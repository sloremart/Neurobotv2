package api

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// La ruta del registro de capacidades del protocolo de escalado v1 de Néctar (ver
// internal/nectar/capabilities.go y el spec docs/superpowers/specs/2026-10-27-migracion-nectar-design.md §6).
//
// Es la SEGUNDA ruta del bot hacia Néctar —la otra es POST /api/webhooks/nectar— y vive en el
// mux PÚBLICO, junto a ella. El patrón se declara aquí, con su método, y cmd/server y las
// pruebas lo comparten: si el registro se hiciera con el camino pelado, un POST entraría al
// handler en vez de caer en el 405 del mux, y 405 es justo lo que la regla de fallo de Néctar
// trata como error de configuración CON alerta.
//
// Y mientras la ruta no exista, el mux da 404, que del otro lado es «el adaptador v1 no está
// desplegado» y NO dispara alerta: por eso esta ruta se despliega antes que nada de v1.
const (
	NectarCapabilitiesPath    = "/api/nectar/capabilities"
	NectarCapabilitiesPattern = "GET " + NectarCapabilitiesPath
)

// HandleNectarCapabilities atiende GET (y HEAD, que net/http empareja con el mismo patrón)
// /api/nectar/capabilities: qué habla este bot, calculado del binario.
//
// AUTENTICACIÓN: la MISMA firma de los avisos, con el mismo secreto, verificada con
// nectar.VerifyNotice tal cual y CUERPO VACÍO. La firma cubre sello + URL + sha256(cuerpo) y el
// resumen de un cuerpo vacío es un valor fijo, así que no se estrena esquema de firma. La URL
// que se verifica es la que reconstruye reconstructFullURL —la del túnel por el que entra
// Néctar, con X-Forwarded-Proto/Host si el proxy los pone—, la misma que ya usan los webhooks
// de Bird: si Néctar firma otra cadena el desenlace es un 401 permanente que su regla de fallo
// se traga en silencio, por eso el rechazo deja traza.
func (h *WebhookHandler) HandleNectarCapabilities(w http.ResponseWriter, r *http.Request) {
	requestURL := reconstructFullURL(r)
	if verr := nectar.VerifyNotice(h.cfg.NectarNoticeSecret,
		r.Header.Get(nectar.SignatureHeader), r.Header.Get(nectar.TimestampHeader),
		requestURL, []byte{}); verr != nil {
		// Un desajuste de cadena firmada no puede ser mudo. Va el sha256 del cuerpo vacío para
		// que los dos lados comparen el valor fijo.
		sum := sha256.Sum256([]byte{})
		//nolint:gosec // G706: la URL sale del Host/X-Forwarded-* de una conexión HTTP que Go ya valida y slog cita los valores; es justo el dato que hay que cotejar con Néctar.
		slog.Warn("nectar: capacidades rechazadas", "reason", verr.Error(), "url", requestURL,
			"empty_body_sha256", hex.EncodeToString(sum[:])[:16])
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	body, version, err := nectar.Capabilities(h.cfg.ProtocolVersion()).Encode()
	if err != nil {
		// Serializar el registro no depende de nada externo: si falla, es un defecto nuestro.
		slog.Error("nectar: el registro de capacidades no se pudo serializar", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	etag := `"` + version + `"`
	w.Header().Set("ETag", etag)
	// Revalidar SIEMPRE, pero dejar conservar: la regla de fallo de Néctar dice que sin lectura
	// se conserven las últimas capacidades conocidas, así que `no-store` —que le prohibiría
	// guardarlas— contradiría el contrato. `private` porque el registro va a un solo lector.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Type", "application/json")

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(body); werr != nil {
		slog.Warn("nectar: capacidades no entregadas", "error", werr)
	}
}

// etagMatches implementa la comparación DÉBIL de If-None-Match (RFC 9110 §13.1.2), que es la
// que corresponde a un GET condicional: la cabecera puede traer varias versiones separadas por
// comas, cada una con el prefijo `W/`, o el comodín `*` —que casa con cualquier representación
// existente—. Se escribe aquí, corta, en vez de tirar de una dependencia: la alternativa era
// comparar la cadena entera y devolver 200 cada vez que Néctar mandara una lista o un `W/`.
func etagMatches(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if weakETag(candidate) == weakETag(etag) {
			return true
		}
	}
	return false
}

// weakETag normaliza una versión a su forma comparable: sin espacios y sin el prefijo `W/`.
func weakETag(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "W/")
}
