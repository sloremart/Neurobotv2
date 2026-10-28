package nectar

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// EL VOLCADO REAL DE NÉCTAR (2026-09-14): los tres avisos v1 de retorno, generados por SU
// emisor —leídos del cuerpo entregado y firmado que recibió el doble del bot, no de su columna
// del buzón—. Vive aquí porque es un fixture de AVISOS: UNA sola copia, con su ruta relativa,
// porque varias copias se separarían en la primera corrección de Néctar. Es copia literal del
// volcado que entregó Néctar (`docs/volcados/nectar-bot-return-notice-v1.json`).
const volcadoRetornosNectar = "testdata/nectar-bot-return-notice-v1.json"

// volcadoNectar es la forma del archivo: tres casos, cada uno con el AVISO que llega por el
// cable y la PETICIÓN que el agente mandó a la ruta de Néctar. Otro paquete que lo necesite
// declara su propio lector de cuatro líneas contra el MISMO archivo (un fichero de prueba no
// exporta nada fuera de su paquete): lo que no se duplica es el archivo, que es lo que podría
// separarse.
type casoDelVolcado struct {
	Aviso    json.RawMessage `json:"aviso"`
	Peticion json.RawMessage `json:"peticion"`
}

type volcadoNectar map[string]casoDelVolcado

// leerVolcadoRetornos descarta las claves de METADATO (`__procedencia__`, `__que_es__`): el
// archivo se entrega documentado, y lo que se ejercita son los casos.
func leerVolcadoRetornos(t *testing.T, ruta string) volcadoNectar {
	t.Helper()
	raw, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se pudo leer el volcado de Néctar: %v", err)
	}
	var crudo map[string]json.RawMessage
	if err := json.Unmarshal(raw, &crudo); err != nil {
		t.Fatalf("el volcado de Néctar no parsea: %v", err)
	}
	v := volcadoNectar{}
	for nombre, cuerpo := range crudo {
		if strings.HasPrefix(nombre, "__") {
			continue
		}
		var caso casoDelVolcado
		if err := json.Unmarshal(cuerpo, &caso); err != nil {
			t.Fatalf("el caso %q del volcado no parsea: %v", nombre, err)
		}
		v[nombre] = caso
	}
	if len(v) != 3 {
		t.Fatalf("el volcado trae %d casos, se esperaban los tres de Néctar", len(v))
	}
	return v
}

// Los TRES avisos, por el lector REAL. Lo que se fija es la FORMA del sobre tal como Néctar la
// emite —que es lo único que un volcado suyo puede demostrar— y a qué camino entra cada uno.
func TestVolcadoDeNectar_LosTresAvisosV1(t *testing.T) {
	volcado := leerVolcadoRetornos(t, volcadoRetornosNectar)

	casos := []struct {
		caso      string
		opcion    string
		conValues bool
	}{
		// ⚠️ EL ALIAS HEREDADO LLEGA COMO `opcion`, y `return_value` VA AUSENTE. El mensaje que
		// acompañaba al acta decía que seguiría viajando por `return_value`; EL VOLCADO MANDA, y
		// el bot acepta las dos formas —lo reconoce por el token, antes de mirar ningún menú—.
		// (El token es statemachine.ReturnValueQueueExpired; este paquete no importa la máquina,
		// así que quien cruza el literal contra la constante es la prueba de internal/worker.)
		{"retorno_del_alias_heredado", "cola_vencida", false},
		{"retorno_repetible_dos_citas", "bot_reanudar", true},
		{"retorno_simple", "bot_reanudar", true},
	}

	for _, c := range casos {
		t.Run(c.caso, func(t *testing.T) {
			crudo, ok := volcado[c.caso]
			if !ok {
				t.Fatalf("el volcado no trae el caso %q", c.caso)
			}
			n, err := ParseNotice(crudo.Aviso)
			if err != nil {
				t.Fatalf("el lector real tiene que aceptar el aviso de Néctar: %v", err)
			}
			if n.Kind != KindConversationReturned || n.Protocol != 1 {
				t.Fatalf("sobre = %+v", n)
			}
			ret, esV1 := n.V1Return()
			if !esV1 {
				t.Fatal("los tres traen `retorno`, que es la fuente de verdad")
			}
			if ret.Option != c.opcion {
				t.Fatalf("opcion = %q, esperaba %q", ret.Option, c.opcion)
			}
			if (len(ret.Values) > 0) != c.conValues {
				t.Fatalf("valores presentes = %v, esperaba %v", len(ret.Values) > 0, c.conValues)
			}
			// `return_value` VA AUSENTE en los tres: `retorno` la sustituye.
			if v, chosen := n.ChosenReturnValue(); chosen {
				t.Fatalf("`return_value` no viaja en estos cuerpos, y llegó %q", v)
			}
		})
	}
}

// LA FORMA DEL SOBRE, clave por clave, sobre el JSON CRUDO: son las tres cosas que un volcado
// suyo demuestra y una prueba nuestra no podría inventarse.
func TestVolcadoDeNectar_LaFormaDelSobre(t *testing.T) {
	volcado := leerVolcadoRetornos(t, volcadoRetornosNectar)

	for caso, crudo := range volcado {
		t.Run(caso, func(t *testing.T) {
			var sobre map[string]json.RawMessage
			if err := json.Unmarshal(crudo.Aviso, &sobre); err != nil {
				t.Fatal(err)
			}
			// (1) `retorno` NO lleva `protocol` dentro: la versión que gobierna cómo se lee esto
			// es la DEL AVISO, que es el cuerpo firmado. Dos versiones dentro del mismo cuerpo
			// podrían discrepar.
			var retorno map[string]json.RawMessage
			if err := json.Unmarshal(sobre["retorno"], &retorno); err != nil {
				t.Fatal(err)
			}
			if _, hay := retorno["protocol"]; hay {
				t.Error("`retorno` no lleva `protocol` propia: va en la raíz del aviso")
			}
			for k := range retorno {
				if k != "opcion" && k != "valores" {
					t.Errorf("clave %q fuera del retorno v1", k)
				}
			}
			if _, hay := sobre["protocol"]; !hay {
				t.Error("y la raíz sí la lleva")
			}
			// (2) `conversation_id` es un NÚMERO, no una cadena: el bot lo deserializa en int64.
			cid := strings.TrimSpace(string(sobre["conversation_id"]))
			if strings.HasPrefix(cid, `"`) {
				t.Errorf("conversation_id viene como número, y llegó %s", cid)
			}
			// (3) `occurred_at` con FRACCIÓN DE SEGUNDO y `Z` (RFC 3339). El lector lo
			// deserializa en un tipo de tiempo, así que esto no es cosmético: un instante escrito
			// de otra forma le haría devolver 400 y el aviso se perdería.
			var occurred string
			if err := json.Unmarshal(sobre["occurred_at"], &occurred); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(occurred, "Z") || !strings.Contains(occurred, ".") {
				t.Errorf("occurred_at = %q: se espera RFC 3339 con fracción y Z", occurred)
			}
			n, err := ParseNotice(crudo.Aviso)
			if err != nil {
				t.Fatal(err)
			}
			if n.OccurredAt.IsZero() || n.ConversationID != 1 {
				t.Fatalf("el sobre decodificado = %+v", n)
			}
		})
	}
}

// Y la PETICIÓN que el agente mandó a la ruta de Néctar es el MISMO retorno, más su `protocol`:
// es lo que sostiene que las dos formas del contrato (la ruta y el aviso) no se separen — del
// lado de Néctar lo sujeta una prueba suya, y aquí se coteja contra lo que el bot recibe.
func TestVolcadoDeNectar_LaPeticionYElAvisoLlevanElMismoRetorno(t *testing.T) {
	volcado := leerVolcadoRetornos(t, volcadoRetornosNectar)

	for caso, crudo := range volcado {
		t.Run(caso, func(t *testing.T) {
			n, err := ParseNotice(crudo.Aviso)
			if err != nil {
				t.Fatal(err)
			}
			ret, _ := n.V1Return()

			var peticion v1ReturnEnvelope
			if err := json.Unmarshal(crudo.Peticion, &peticion); err != nil {
				t.Fatal(err)
			}
			if peticion.Protocol != 1 {
				t.Errorf("la petición sí lleva `protocol`: %d", peticion.Protocol)
			}
			if peticion.Option != ret.Option {
				t.Errorf("opcion: petición %q / aviso %q", peticion.Option, ret.Option)
			}
			if compacto(t, peticion.Values) != compacto(t, ret.Values) {
				t.Errorf("valores: petición %s / aviso %s", peticion.Values, ret.Values)
			}
		})
	}
}

func compacto(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
