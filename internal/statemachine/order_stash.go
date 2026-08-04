package statemachine

import (
	"encoding/json"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// Stash de la orden médica: las páginas que el paciente envía ANTES de llegar al paso de la orden
// (primer mensaje de la sesión, foto en el menú, o foto mientras el bot pregunta otra cosa) se guardan
// para consumirlas después sin re-pedirlas.
//
// Es una LISTA, no una URL. Auditoría ciclo 133 (2026-08-04): un paciente mandó su orden completa en
// cinco fotos en dos segundos; con una sola clave se guardaba la primera hoja y las otras cuatro se
// descartaban respondiéndole "ya tengo tu orden" — falso, y encima le pedía explícitamente no reenviar
// lo que faltaba. Mandar una orden de varias hojas como varias fotos seguidas es normal, y el flujo ya
// sabe fusionar páginas aguas abajo (PhotoAppendInterceptor/mergeCupsJSON).
const (
	// StashedOrderURLsKey guarda las páginas como un array JSON, en orden de llegada.
	StashedOrderURLsKey = "stashed_order_urls"
	// legacyStashedOrderURLKey es la clave de UNA sola URL previa a la lista. Se sigue LEYENDO para no
	// romper las sesiones vivas al desplegar (duran ~2h); ya no se escribe.
	legacyStashedOrderURLKey = "stashed_order_url"
	// MaxStashedOrderPages acota cuántas hojas se guardan: cada una es un análisis OCR más al
	// consumirlas, y el presupuesto del mensaje es finito.
	MaxStashedOrderPages = 4
)

// StashedOrderURLs devuelve las páginas guardadas de la orden, en orden de llegada. Incluye la clave
// legada al final si aún no está en la lista, para que una sesión creada antes del despliegue no pierda
// su orden.
func StashedOrderURLs(sess *session.Session) []string {
	var urls []string
	if raw := sess.GetContext(StashedOrderURLsKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &urls)
	}
	out := make([]string, 0, len(urls)+1)
	for _, u := range append(urls, sess.GetContext(legacyStashedOrderURLKey)) {
		out = AppendStashedOrderURL(out, u)
	}
	return out
}

// AppendStashedOrderURL agrega una página al final si trae URL y no estaba ya (el paciente puede reenviar
// la misma imagen: Bird le da otra URL, pero una URL repetida nunca es una hoja nueva). Función pura.
func AppendStashedOrderURL(urls []string, url string) []string {
	if url == "" {
		return urls
	}
	for _, u := range urls {
		if u == url {
			return urls
		}
	}
	return append(urls, url)
}

// EncodeStashedOrderURLs serializa la lista para guardarla en el contexto de la sesión.
func EncodeStashedOrderURLs(urls []string) string {
	b, err := json.Marshal(urls)
	if err != nil {
		return ""
	}
	return string(b)
}

// StashKeysToClear son TODAS las claves del stash. Se borran juntas al consumirlo o al fallar: dejar la
// legada viva haría que una sesión vieja reintentara para siempre la misma orden.
func StashKeysToClear() []string {
	return []string{StashedOrderURLsKey, legacyStashedOrderURLKey}
}
