package nectar

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// EL RETORNO v1 Y SUS DOS FUENTES (§4.1 del diseño, T5). La recepción es DUAL y no depende de
// ninguna bandera: las dos formas conviven, y un `return_value` que no sea ninguna de ellas
// sigue siendo el dialecto de hoy.

func TestV1Return_DeLaClaveRetorno(t *testing.T) {
	body := `{"protocol":1,"event_id":"e","kind":"conversacion_devuelta","conversation_id":4,
	          "retorno":{"opcion":"bot_reanudar","valores":{"documento":"1000000001"}}}`
	n, err := ParseNotice([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ret, ok := n.V1Return()
	if !ok {
		t.Fatal("la clave `retorno` es la fuente de verdad de un retorno v1")
	}
	if ret.Option != "bot_reanudar" {
		t.Errorf("opcion = %q", ret.Option)
	}
	var valores map[string]string
	if err := json.Unmarshal(ret.Values, &valores); err != nil {
		t.Fatalf("valores llega crudo y decodificable: %v", err)
	}
	if valores["documento"] != "1000000001" {
		t.Errorf("valores = %v", valores)
	}
}

func TestV1Return_DelObjetoSerializadoEnReturnValue(t *testing.T) {
	// La fuente (2) del §4.1: cabe SIN cambiar ningún esquema, porque `return_value` es opaco y
	// viaja byte a byte. Es con la que T5 se construyó y se probó entera.
	body := `{"protocol":1,"event_id":"e","kind":"conversacion_devuelta","conversation_id":4,
	          "return_value":"{\"protocol\":1,\"opcion\":\"bot_cerrar\"}"}`
	n, err := ParseNotice([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ret, ok := n.V1Return()
	if !ok {
		t.Fatal("el objeto serializado en return_value también es un retorno v1")
	}
	if ret.Option != "bot_cerrar" || len(ret.Values) != 0 {
		t.Errorf("ret = %+v", ret)
	}
}

func TestV1Return_LaClaveRetornoMandaSobreReturnValue(t *testing.T) {
	// La misma regla, y por la misma razón, que el candado y su alias heredado (fila A7).
	body := `{"protocol":1,"event_id":"e","kind":"conversacion_devuelta","conversation_id":4,
	          "return_value":"{\"protocol\":1,\"opcion\":\"del_serializado\"}",
	          "retorno":{"opcion":"del_retorno"}}`
	n, _ := ParseNotice([]byte(body))
	ret, ok := n.V1Return()
	if !ok || ret.Option != "del_retorno" {
		t.Fatalf("manda `retorno`: %+v (ok=%v)", ret, ok)
	}
	// Y el dialecto sigue viendo su propia clave: quien la lee es ChosenReturnValue, que no
	// cambia. El worker mira el retorno v1 primero, y por eso esto no es ambiguo.
	if v, chosen := n.ChosenReturnValue(); !chosen || v == "" {
		t.Errorf("ChosenReturnValue no cambia: %q / %v", v, chosen)
	}
}

func TestV1Return_LoQueNoEsV1NoLoEs(t *testing.T) {
	casos := map[string]string{
		"token del dialecto":       `"agendar_cita"`,
		"dato escrito":             `"bot_dato:1000000001"`,
		"citas del dialecto":       `"bot_citas:{\"citas\":[]}"`,
		"objeto sin protocol":      `"{\"opcion\":\"x\"}"`,
		"objeto roto":              `"{\"protocol\":1,\"opcion\":"`,
		"alias de la cola vencida": `"cola_vencida"`,
	}
	for nombre, valor := range casos {
		t.Run(nombre, func(t *testing.T) {
			body := `{"protocol":1,"event_id":"e","kind":"conversacion_devuelta",` +
				`"conversation_id":4,"return_value":` + valor + `}`
			n, err := ParseNotice([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := n.V1Return(); ok {
				t.Fatal("no es un retorno v1: el camino de hoy queda INTACTO")
			}
		})
	}
}

func TestV1Return_SoloEnConversacionDevuelta(t *testing.T) {
	// Igual que ChosenReturnValue: en los otros dos avisos la clave no viaja, y si viajara no
	// significaría nada.
	body := `{"protocol":1,"event_id":"e","kind":"mensaje_entrante","conversation_id":4,
	          "retorno":{"opcion":"bot_reanudar"}}`
	n, _ := ParseNotice([]byte(body))
	if _, ok := n.V1Return(); ok {
		t.Fatal("el retorno solo tiene sentido en conversacion_devuelta")
	}
}

func TestV1Return_RetornoSinOpcionNoSeTragaElDialecto(t *testing.T) {
	// `opcion` es obligatoria (minLength 1). Un `retorno` sin ella no es un retorno, así que no
	// puede ganarle la precedencia al `return_value` que sí trae algo utilizable.
	body := `{"protocol":1,"event_id":"e","kind":"conversacion_devuelta","conversation_id":4,
	          "return_value":"agendar_cita","retorno":{"opcion":""}}`
	n, _ := ParseNotice([]byte(body))
	if _, ok := n.V1Return(); ok {
		t.Fatal("un retorno sin opcion no es un retorno v1")
	}
	if v, _ := n.ChosenReturnValue(); v != "agendar_cita" {
		t.Fatalf("y el token del dialecto sigue llegando: %q", v)
	}
}

// EL AVISO DE «PARECE v1 Y NO SE PUDO LEER» NO TRAZA EL ERROR DE `encoding/json`, y esa omisión
// es deliberada: ese error incluye EL CARÁCTER OFENSOR del cuerpo, y el cuerpo es un retorno con
// `valores` —que puede llevar el documento del paciente (§2.5)—. Misma regla y misma forma que
// la rama hermana de `return_menu.go` («`valores` no es un objeto»): se deja `len`, que es lo
// que hace falta para saber si llegó un cuerpo enorme o uno vacío, y el `conversation_id`, que
// es lo que hace falta para encontrar el caso.
func TestV1Return_ElAvisoDeCuerpoIlegibleNoTrazaElError(t *testing.T) {
	var registro bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&registro, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(anterior)

	// Un objeto v1 ROTO cuyo primer carácter inesperado es un dígito del documento: con `err` en
	// la traza, el mensaje de `encoding/json` lo escribiría («invalid character '1' …»).
	roto := `{"protocol":1,"opcion":"bot_reanudar","valores":{"documento":}1000000001`
	crudo, err := json.Marshal(roto)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"protocol":1,"event_id":"e","kind":"conversacion_devuelta","conversation_id":4,` +
		`"return_value":` + string(crudo) + `}`
	n, err := ParseNotice([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.V1Return(); ok {
		t.Fatal("un cuerpo ilegible no es un retorno v1: sigue el camino del dialecto")
	}
	linea := registro.String()
	if !strings.Contains(linea, "no se pudo leer") {
		t.Fatalf("el aviso tiene que quedar: %s", linea)
	}
	if strings.Contains(linea, "invalid character") || strings.Contains(linea, "error=") {
		t.Fatalf("el error de encoding/json ecoa el cuerpo y no entra en el registro: %s", linea)
	}
	if !strings.Contains(linea, "len=") {
		t.Fatalf("queda el tamaño, que es lo que distingue un cuerpo enorme de uno vacío: %s", linea)
	}
}
