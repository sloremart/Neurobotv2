package bird

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// El "regalo" del hallazgo de hdg-bot: base64.StdEncoding NO es estricto. Una firma de 32 bytes
// son 44 caracteres donde el ultimo lleva 2 bits sin usar, asi que CUATRO cadenas distintas
// decodifican a los mismos bytes y las cuatro se aceptan como la misma firma.
func TestVerifySignature_RejectsNonCanonicalBase64(t *testing.T) {
	const secret = "test-secret"
	const url = "https://example.test/api/webhooks/whatsapp/outbound"
	body := []byte(`{"payload":{"id":"msg-1"}}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s\n%s\n%s", ts, url, sum)
	canonical := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !VerifySignatureWithKey(secret, canonical, ts, url, body) {
		t.Fatal("la firma canonica debe verificar")
	}

	// Variantes del ultimo caracter que decodifican a los MISMOS 32 bytes: los 2 bits sobrantes
	// del caracter 44 se ignoran en el modo no estricto.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := canonical[42] // 32 bytes = 43 caracteres de datos + 1 de relleno; el 43.o lleva 2 bits sin usar
	base := alphabet[:64]
	idx := -1
	for i := 0; i < len(base); i++ {
		if base[i] == last {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("caracter final inesperado %q", last)
	}
	variantes := 0
	for d := 1; d < 4; d++ {
		alt := (idx & 0x3C) | ((idx + d) & 0x03) // mismos 4 bits utiles, 2 bits de relleno distintos
		if alt == idx {
			continue
		}
		forged := canonical[:42] + string(base[alt]) + canonical[43:]
		if forged == canonical {
			continue
		}
		if VerifySignatureWithKey(secret, forged, ts, url, body) {
			variantes++
			t.Errorf("una firma NO canonica se acepto como valida: %q (canonica %q)", forged, canonical)
		}
	}
	if variantes == 0 {
		t.Log("base64 estricto: solo la cadena canonica verifica")
	}
}
