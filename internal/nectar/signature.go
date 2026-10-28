// Package nectar es la frontera del bot con Néctar, la plataforma de mensajería y
// bandeja de agentes de Colibrixa que reemplaza a Bird en WhatsApp (la voz se queda
// en Bird). Contrato: docs/PARA-EL-EQUIPO-DEL-BOT.md y, campo por campo,
// AUDITORIA-ACOPLAMIENTO-BOT.md §8 del repositorio de Néctar.
package nectar

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Cabeceras de la firma. Son las de Bird renombradas: Néctar firma con SU clave, no
// reenvía la de Bird, y llamarlas MessageBird-* describiría mal lo que viaja (§1).
const (
	SignatureHeader = "X-Nectar-Signature"
	TimestampHeader = "X-Nectar-Request-Timestamp"
)

// noticeWindow es la ventana del sello de tiempo para los avisos de Néctar: cinco
// minutos a cada lado. NO son las 24 h de Bird (bird.maxTimestampAge) y la diferencia
// es de premisa, no de gusto: Bird reintenta repitiendo el sello ORIGINAL durante ~24 h,
// así que allí una ventana corta rechazaba reintentos legítimos; Néctar REFIRMA en cada
// intento, así que la ventana no tiene que cubrir ninguna política de reintentos. Lo que
// compra acortarla: una petición capturada deja de ser reenviable a los cinco minutos.
const noticeWindow = 5 * time.Minute

// Los cinco motivos por los que VerifyNotice rechaza un aviso: ninguno se confunde con otro.
var (
	ErrNotConfigured = errors.New("nectar: la frontera no tiene secreto configurado")
	ErrMissingHeader = errors.New("nectar: falta la firma o el sello de tiempo")
	ErrTimestamp     = errors.New("nectar: el sello de tiempo no es un entero de segundos")
	ErrWindow        = errors.New("nectar: el sello de tiempo está fuera de la ventana")
	ErrSignature     = errors.New("nectar: la firma no cuadra")
)

// VerifyNotice verifica la firma de un aviso entrante de Néctar. Devuelve nil solo si
// el sello cae en la ventana y la firma cuadra.
//
// El HMAC lo calcula bird.VerifySignatureWithKey, tal cual: es el mismo esquema y
// recibe clave y cabeceras por parámetro, así que le da igual cómo se llamen. Lo que
// esta función añade es la ventana corta (arriba) y el fallo cerrado con secreto vacío.
func VerifyNotice(secret, signature, timestamp, requestURL string, body []byte) error {
	if secret == "" {
		return ErrNotConfigured
	}
	if signature == "" || timestamp == "" {
		return ErrMissingHeader
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	drift := time.Since(time.Unix(seconds, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > noticeWindow {
		return ErrWindow
	}
	if !bird.VerifySignatureWithKey(secret, signature, timestamp, requestURL, body) {
		return ErrSignature
	}
	return nil
}

// Sign produce la firma del esquema para un instante, una URL y un cuerpo dados. Es la
// contraparte de VerifyNotice: la usan las pruebas para fabricar avisos válidos y, en
// la dirección contraria, el cliente que llama a las siete rutas de Néctar (que exigen
// la misma firma con el secreto del otro sentido).
func Sign(secret, timestamp, requestURL string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("\n"))
	mac.Write([]byte(requestURL))
	mac.Write([]byte("\n"))
	mac.Write(bodyHash[:]) // bytes crudos del hash, no hex — igual que Bird
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Timestamp da el sello en el formato del esquema: segundos desde la época, decimal.
func Timestamp(at time.Time) string { return strconv.FormatInt(at.Unix(), 10) }

// SignRequest firma una petición SALIENTE del bot hacia Néctar y le pone las dos
// cabeceras. Se firma AL ENVIAR, no al construir: la ventana del otro lado son cinco
// minutos y un reintento tiene que refirmarse. La URL firmada es req.URL.String(), que
// en una petición de cliente es absoluta — exactamente lo que Néctar reconstruye.
func SignRequest(req *http.Request, secret string, at time.Time, body []byte) {
	ts := Timestamp(at)
	req.Header.Set(TimestampHeader, ts)
	req.Header.Set(SignatureHeader, Sign(secret, ts, req.URL.String(), body))
}
