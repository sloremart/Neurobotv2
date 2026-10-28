package nectar

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// La firma de Néctar es el esquema de Bird con las cabeceras renombradas
// (PARA-EL-EQUIPO-DEL-BOT.md §1): base64(HMAC-SHA256(clave, ts + "\n" + url + "\n" + SHA256(cuerpo))).
// Estas pruebas fijan que el verificador acepta exactamente lo que Néctar produce y nada más.

const (
	testSecret = "clave-de-prueba-de-nectar"
	testURL    = "https://bot.example.test/api/webhooks/nectar"
)

var testBody = []byte(`{"event_id":"2f0f8c1e-0000-4000-8000-000000000001","kind":"mensaje_entrante","conversation_id":1234}`)

func nowStamp() string { return strconv.FormatInt(time.Now().Unix(), 10) }

func TestAValidSignatureIsAccepted(t *testing.T) {
	ts := nowStamp()
	sig := Sign(testSecret, ts, testURL, testBody)
	if err := VerifyNotice(testSecret, sig, ts, testURL, testBody); err != nil {
		t.Fatalf("una firma correcta debe pasar: %v", err)
	}
}

func TestASignatureWithAnotherSecretIsRejected(t *testing.T) {
	ts := nowStamp()
	sig := Sign("otra-clave", ts, testURL, testBody)
	if err := VerifyNotice(testSecret, sig, ts, testURL, testBody); err == nil {
		t.Fatal("una firma con otra clave no puede pasar")
	}
}

func TestTheURLIsPartOfWhatIsSigned(t *testing.T) {
	ts := nowStamp()
	sig := Sign(testSecret, ts, testURL, testBody)
	if err := VerifyNotice(testSecret, sig, ts, testURL+"?x=1", testBody); err == nil {
		t.Fatal("cambiar la URL (incluida la consulta) tiene que invalidar la firma")
	}
}

func TestTheBodyIsPartOfWhatIsSigned(t *testing.T) {
	ts := nowStamp()
	sig := Sign(testSecret, ts, testURL, testBody)
	if err := VerifyNotice(testSecret, sig, ts, testURL, append([]byte{}, append(testBody, ' ')...)); err == nil {
		t.Fatal("un byte más en el cuerpo tiene que invalidar la firma")
	}
}

func TestMissingHeadersAreRejected(t *testing.T) {
	if err := VerifyNotice(testSecret, "", nowStamp(), testURL, testBody); err == nil {
		t.Fatal("sin firma no pasa")
	}
	if err := VerifyNotice(testSecret, Sign(testSecret, "1", testURL, testBody), "", testURL, testBody); err == nil {
		t.Fatal("sin sello no pasa")
	}
}

func TestAnEmptySecretAcceptsNothing(t *testing.T) {
	// Falla cerrado: un bot sin NECTAR_NOTICE_SECRET no puede aceptar avisos, ni siquiera
	// los firmados con la cadena vacía.
	ts := nowStamp()
	if err := VerifyNotice("", Sign("", ts, testURL, testBody), ts, testURL, testBody); err == nil {
		t.Fatal("con secreto vacío la frontera está cerrada")
	}
}

func TestAStampOutsideTheWindowIsRejected(t *testing.T) {
	// Néctar REFIRMA en cada intento, así que la ventana no tiene que cubrir reintentos
	// viejos: un sello de hace más de noticeWindow se rechaza aunque la firma cuadre.
	old := strconv.FormatInt(time.Now().Add(-noticeWindow-time.Minute).Unix(), 10)
	if err := VerifyNotice(testSecret, Sign(testSecret, old, testURL, testBody), old, testURL, testBody); err == nil {
		t.Fatal("un sello fuera de la ventana no pasa")
	}
	// Y es simétrica: uno en el futuro tampoco.
	future := strconv.FormatInt(time.Now().Add(noticeWindow+time.Minute).Unix(), 10)
	if err := VerifyNotice(testSecret, Sign(testSecret, future, testURL, testBody), future, testURL, testBody); err == nil {
		t.Fatal("un sello en el futuro no pasa")
	}
}

func TestSignMatchesBirdScheme(t *testing.T) {
	// Vector fijo: si alguien "mejora" Sign (hex en vez de bytes crudos del hash, otro
	// separador…), deja de cuadrar con lo que Néctar manda. Calculado con el esquema de
	// Bird que bird.VerifySignatureWithKey ya verifica en producción.
	got := Sign("k", "1754870400", "https://h/p", []byte("{}"))
	const want = "7ZclCn4AygqskXFdqniEpRlhTigXgu+GS7C7uvUfkmY="
	if got != want {
		t.Fatalf("Sign cambió de esquema: got %s want %s", got, want)
	}
}

func TestSignRequestPutsBothHeadersAndSignsTheAbsoluteURL(t *testing.T) {
	// La ida bot→Néctar: Néctar reconstruye scheme://host/ruta?consulta y exige que cuadre.
	body := []byte(`{"phone":"573001112233"}`)
	req, _ := http.NewRequest(http.MethodPost, "https://aom.colibrixa.com/api/bot/conversations/resolve?x=1", bytes.NewReader(body))
	at := time.Now()
	SignRequest(req, testSecret, at, body)
	if req.Header.Get(TimestampHeader) != Timestamp(at) {
		t.Fatalf("sello: %q", req.Header.Get(TimestampHeader))
	}
	if err := VerifyNotice(testSecret, req.Header.Get(SignatureHeader), req.Header.Get(TimestampHeader),
		"https://aom.colibrixa.com/api/bot/conversations/resolve?x=1", body); err != nil {
		t.Fatalf("lo firmado tiene que verificarse con la misma URL absoluta: %v", err)
	}
}

func TestSignRequestWithNoBodySignsTheEmptyHash(t *testing.T) {
	// GET /api/bot/attachments/{id}: sin cuerpo, se firma SHA256("").
	req, _ := http.NewRequest(http.MethodGet, "https://aom.colibrixa.com/api/bot/attachments/abc", nil)
	SignRequest(req, testSecret, time.Now(), nil)
	if err := VerifyNotice(testSecret, req.Header.Get(SignatureHeader), req.Header.Get(TimestampHeader),
		"https://aom.colibrixa.com/api/bot/attachments/abc", nil); err != nil {
		t.Fatal(err)
	}
}
