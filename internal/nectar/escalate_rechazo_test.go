package nectar

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// `rechazo` es PURAMENTE ADITIVO: sin él el cuerpo es el de siempre, y con él entra una sola
// clave más con la forma que declara el esquema ({motivo, intento, eco, campo?, pieza?}).
func TestElRechazoViajaConSuFormaYSoloCuandoLoHay(t *testing.T) {
	sin, err := EncodeEscalateBody(bird.Escalation{
		Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sin, []byte("rechazo")) {
		t.Fatalf("sin rechazo la clave NO viaja: %s", sin)
	}

	con, err := EncodeEscalateBody(bird.Escalation{
		Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
		Rejection: &bird.Rejection{
			Detail:  "un código no está en los programables: 999999",
			Attempt: 2,
			Echo:    `bot_citas:{"citas":[]}`,
			Field:   "cups",
			Piece:   "999999",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cuerpo struct {
		Rechazo map[string]json.RawMessage `json:"rechazo"`
	}
	if err := json.Unmarshal(con, &cuerpo); err != nil {
		t.Fatal(err)
	}
	quiere := map[string]string{
		"motivo":  `"un código no está en los programables: 999999"`,
		"intento": `2`,
		"eco":     `"bot_citas:{\"citas\":[]}"`,
		"campo":   `"cups"`,
		"pieza":   `"999999"`,
	}
	for k, v := range quiere {
		if string(cuerpo.Rechazo[k]) != v {
			t.Errorf("rechazo.%s = %s, quiere %s", k, cuerpo.Rechazo[k], v)
		}
	}
	if len(cuerpo.Rechazo) != len(quiere) {
		t.Errorf("el rechazo no lleva más claves que las cinco del esquema: %v", cuerpo.Rechazo)
	}
}

// Las cuatro OPCIONALES salen ausentes cuando el bot no las separa: ausente y vacío son datos
// distintos, y el esquema solo exige `motivo`.
func TestLasClavesOpcionalesDelRechazoVanAusentes(t *testing.T) {
	body, err := EncodeEscalateBody(bird.Escalation{
		Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
		Rejection: &bird.Rejection{Detail: "la plantilla llegó sin ningún campo con contenido"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"campo", "pieza", "eco", "intento"} {
		if bytes.Contains(body, []byte(`"`+k+`"`)) {
			t.Errorf("`%s` vacío tiene que ir AUSENTE: %s", k, body)
		}
	}
	if !bytes.Contains(body, []byte(`"motivo"`)) {
		t.Errorf("`motivo` es obligatorio: %s", body)
	}
}

// UN RECHAZO SIN MOTIVO NO ES UN RECHAZO: el esquema lo exige, así que el emisor no manda una
// clave que su decodificador va a rechazar entera — se queda con el resumen, que ya lo cuenta.
func TestUnRechazoSinMotivoNoSeEmite(t *testing.T) {
	body, err := EncodeEscalateBody(bird.Escalation{
		Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
		Rejection: &bird.Rejection{Attempt: 1, Echo: "abc", Field: "documento"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("rechazo")) {
		t.Fatalf("sin motivo no hay rechazo que emitir: %s", body)
	}
}

// Los topes del esquema (512 runas en `motivo` y en `eco`) los aplica el EMISOR, que es el
// único punto por el que pasan todos los caminos — igual que la guarda de reason_detail. Y el
// corte es por RUNAS: por bytes partiría un carácter multibyte y dejaría basura en la pantalla
// del agente.
func TestElMotivoYElEcoSeCortanAlTopeDelEsquemaEnRunas(t *testing.T) {
	largo := strings.Repeat("ñ", 600)
	body, err := EncodeEscalateBody(bird.Escalation{
		Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
		Rejection: &bird.Rejection{Detail: largo, Echo: largo},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cuerpo struct {
		Rechazo struct {
			Motivo string `json:"motivo"`
			Eco    string `json:"eco"`
		} `json:"rechazo"`
	}
	if err := json.Unmarshal(body, &cuerpo); err != nil {
		t.Fatal(err)
	}
	for nombre, v := range map[string]string{"motivo": cuerpo.Rechazo.Motivo, "eco": cuerpo.Rechazo.Eco} {
		if n := len([]rune(v)); n != 512 {
			t.Errorf("%s mide %d runas y el tope del esquema es 512", nombre, n)
		}
		if strings.ContainsRune(v, '\uFFFD') {
			t.Errorf("%s se cortó a mitad de un carácter", nombre)
		}
	}
}

// `intento` tiene `minimum: 1` en el esquema, y el emisor lo respeta como respeta los topes de
// largo: fuera de rango va AUSENTE (la clave es opcional), nunca con un valor que el
// decodificador de Néctar rechazaría — con él se caería el sobre ENTERO.
func TestUnIntentoFueraDeRangoVaAusenteYNoRompeElSobre(t *testing.T) {
	for _, intento := range []int{0, -3} {
		body, err := EncodeEscalateBody(bird.Escalation{
			Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
			Rejection: &bird.Rejection{Detail: "un motivo cualquiera", Attempt: intento},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(`"intento"`)) {
			t.Errorf("intento %d: tiene que ir AUSENTE, no con un valor que el esquema rechaza: %s",
				intento, body)
		}
		// Y el resto del rechazo viaja igual: el intento es accesorio, el motivo no.
		if !bytes.Contains(body, []byte(`"motivo"`)) {
			t.Errorf("intento %d: el rechazo se emite igual: %s", intento, body)
		}
	}
	// El primero válido sí viaja: el tope es un mínimo, no una prohibición.
	body, err := EncodeEscalateBody(bird.Escalation{
		Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
		Rejection: &bird.Rejection{Detail: "un motivo cualquiera", Attempt: 1},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"intento":1`)) {
		t.Errorf("el intento 1 sí viaja: %s", body)
	}
}

// ⚠️ NI EL MOTIVO NI EL ECO SE TRAZAN (§2.5): el eco es lo que una persona escribió sobre un
// paciente —en ASK_DOCUMENT, su documento—, y el motivo lo ecoa truncado. Van al sobre, que
// Néctar sella, y a ningún registro. Esta es la mitad del EMISOR de la prueba #12.
func TestElEmisorNoTrazaNiElMotivoNiElEco(t *testing.T) {
	var registro bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&registro, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(anterior)

	const motivo = "MOTIVO-CANARIO-NO-TRAZABLE"
	const eco = "ECO-CANARIO-1000000001"
	if _, err := EncodeEscalateBody(bird.Escalation{
		Protocol: 1, Reason: testReasonBotCannotAnswer, Summary: "x",
		// Con un reason_detail INVÁLIDO a propósito: es el único slog de este camino, y hay que
		// comprobar que ni siquiera esa línea arrastra el rechazo.
		ReasonDetail: "NO CASA LA GUARDA",
		Rejection:    &bird.Rejection{Detail: motivo, Echo: eco, Attempt: 3},
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, prohibido := range []string{motivo, eco} {
		if strings.Contains(registro.String(), prohibido) {
			t.Errorf("el registro no puede llevar %q:\n%s", prohibido, registro.String())
		}
	}
}
