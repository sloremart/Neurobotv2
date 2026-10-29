package nectar

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

// El REGISTRO DE CAPACIDADES (protocolo de escalado v1 de Néctar, D5): se
// CALCULA del binario, nunca de configuración. Una capacidad declarada que el código no cumple
// es peor que no declararla, así que lo que estas pruebas persiguen es UNA SOLA cosa: que el
// registro NO PUEDA MENTIR.

// Sin ningún candado declarado el registro NO anuncia `resolve_lock` (C8): no promete lo que
// ningún paso emite.
func TestCapabilities_ResolveLockFalseWithoutDeclaredGuards(t *testing.T) {
	for _, ver := range []int{config.ProtocolEscaladoV0, config.ProtocolEscaladoV1} {
		if Capabilities(ver, false).Capabilities.Escalation.ResolveLock {
			t.Errorf("versión %d: resolve_lock en true sin candados declarados", ver)
		}
	}
}

func TestCapabilities_SchemaShape(t *testing.T) {
	// La forma es contrato con Néctar (capabilities.schema.json, additionalProperties:false):
	// las claves obligatorias tienen que estar TODAS y con su tipo, y no puede sobrar ninguna.
	// Sin librería de JSON Schema en go.mod, se comprueban las claves y los tipos a mano.
	for _, ver := range []int{config.ProtocolEscaladoV0, config.ProtocolEscaladoV1} {
		body, _, err := Capabilities(ver, true).Encode()
		if err != nil {
			t.Fatalf("versión %d: %v", ver, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("versión %d: el cuerpo no es JSON: %v", ver, err)
		}
		requireKeys(t, raw, []string{"protocol", "version", "capabilities"}, nil)
		// `protocol` es const 1 en el esquema: es la versión del PROTOCOLO de esta ruta, no la
		// de la bandera — la ruta es de v1 aunque el bot aún emita el dialecto.
		if raw["protocol"] != float64(config.ProtocolEscaladoV1) {
			t.Fatalf("versión %d: protocol = %v, quiero 1", ver, raw["protocol"])
		}
		if s, ok := raw["version"].(string); !ok || s == "" {
			t.Fatalf("versión %d: `version` tiene que ser una cadena no vacía, es %v", ver, raw["version"])
		}
		caps, _ := raw["capabilities"].(map[string]any)
		requireKeys(t, caps, []string{"escalate", "input", "return", "notices", "plantillas"}, nil)
		esc, _ := caps["escalate"].(map[string]any)
		// resolve_lock_alias es OPCIONAL en el esquema: su ausencia es el dato «el alias se retiró».
		requireKeys(t, esc, []string{"facts", "reason_detail", "resolve_lock", "reasons"}, []string{"resolve_lock_alias"})
		requireBools(t, esc, "facts", "reason_detail", "resolve_lock")
		in, _ := caps["input"].(map[string]any)
		requireKeys(t, in, []string{"tipos", "salidas", "repetible", "retoma_en"}, nil)
		requireBools(t, in, "repetible", "retoma_en")
		ret, _ := caps["return"].(map[string]any)
		requireKeys(t, ret, []string{"valores", "rechazo_tipado", "grupo_en_el_retorno"}, nil)
		requireBools(t, ret, "valores", "rechazo_tipado")
		if g := ret["grupo_en_el_retorno"]; g != "obligatorio" && g != "opcional" {
			t.Fatalf("versión %d: grupo_en_el_retorno = %v, fuera del enum del esquema", ver, g)
		}
		// LAS LISTAS VAN SIEMPRE COMO LISTAS, NUNCA NULAS (fila E46 del acta): un nulo sería
		// una segunda forma de decir lo mismo que el parser tendría que interpretar. Incluye
		// `plantillas`, que en Neuro va VACÍA y por eso es el candidato natural a salir `null`.
		for _, list := range []struct {
			where map[string]any
			key   string
		}{{esc, "reasons"}, {in, "tipos"}, {in, "salidas"}, {caps, "notices"}, {caps, "plantillas"}} {
			if _, ok := list.where[list.key].([]any); !ok {
				t.Fatalf("versión %d: %q no es una lista (¿nula?): %#v", ver, list.key, list.where[list.key])
			}
		}
	}
}

func TestCapabilities_V0HidesEmissionOnlyFields(t *testing.T) {
	// §1.3: «Con PROTOCOL_ESCALADO_VERSION=0, escalate.facts y escalate.reason_detail salen
	// false, porque el emisor está apagado. Así el registro y el cable dicen lo mismo en los dos
	// estados de la bandera, y Néctar no pide lo que no va a recibir.»
	//
	// `return.valores` NO está en esa lista: es RECEPCIÓN, no emisión, y declararla false mientras
	// el lector está vivo sería el registro mintiendo. Tiene su propia prueba, abajo.
	c := Capabilities(config.ProtocolEscaladoV0, true).Capabilities
	if c.Escalation.Facts || c.Escalation.ReasonDetail {
		t.Fatalf("con la bandera en 0 no se declara lo que no se emite: %+v", c)
	}
	// Sin descriptor que declarar: las dos listas vacías (NO nulas) y los dos booleanos en false.
	if c.Input.Repeatable || c.Input.ResumesAt {
		t.Fatalf("con la bandera en 0 no hay descriptor `input` que declarar: %+v", c.Input)
	}
	if c.Input.Kinds == nil || len(c.Input.Kinds) != 0 || c.Input.Exits == nil || len(c.Input.Exits) != 0 {
		t.Fatalf("con la bandera en 0 `tipos` y `salidas` son listas VACÍAS, no nulas: %+v", c.Input)
	}
	if c.Return.TypedRejection {
		t.Fatalf("el rechazo tipado es de v1: %+v", c.Return)
	}
	// Lo que SÍ se declara igual con la bandera en 0: la recepción del retorno v1, el candado
	// bajo sus dos nombres y los dos motivos, que son los mismos en los dos dialectos.
	if !c.Return.Values {
		t.Fatal("return.valores es RECEPCIÓN: el lector está vivo con la bandera donde esté")
	}
	if !c.Escalation.ResolveLock {
		t.Fatal("resolve_lock se emite con la bandera en 0 (client.go), la bandera no lo gobierna")
	}
	if c.Escalation.ResolveLockAlias != ResolveLockAliasKey {
		t.Fatalf("resolve_lock_alias = %q, quiero %q", c.Escalation.ResolveLockAlias, ResolveLockAliasKey)
	}
	if !reflect.DeepEqual(c.Escalation.Reasons, []string{ReasonBotCannotAnswer, ReasonPatientAskedForHuman}) {
		t.Fatalf("reasons = %v", c.Escalation.Reasons)
	}
}

func TestCapabilities_V1(t *testing.T) {
	// Con la bandera en 1 TODO está encendido salvo `repetible`: Neuro no tiene formulario
	// repetible (el descriptor nunca lleva piezas), así que declararlo sería prometerle a Néctar
	// una pantalla que el bot no sabe interpretar. Se compara el conjunto ENTERO y con los
	// literales de cable a mano —no con las constantes del paquete— para que cambiar una
	// capacidad exija cambiar también esta prueba. DeepEqual distingue nulo de vacío, así que
	// `plantillas` tiene que ser una lista vacía.
	want := CapabilitySet{
		Escalation: EscalationCapabilities{
			Facts:            true,
			ReasonDetail:     true,
			ResolveLock:      true,
			ResolveLockAlias: "candado_resolver",
			Reasons:          []string{"bot_cannot_answer", "patient_asked_for_human"},
		},
		Input: InputCapabilities{
			// Los tipos que los pasos de Neuro declaran: no hay texto largo, hora ni enlace.
			Kinds:      []string{"texto", "fecha", "numero", "lista"},
			Exits:      []string{"reanudar", "reanudar_sin_dato", "cerrar"},
			Repeatable: false,
			ResumesAt:  true,
		},
		Return: ReturnCapabilities{
			Values:           true,
			TypedRejection:   true,
			GroupInTheReturn: "opcional",
		},
		Notices: []string{
			"mensaje_entrante", "conversacion_devuelta", "conversacion_resuelta",
			"conversacion_abierta_por_agente",
		},
		// Neuro no tiene plantillas que un agente pueda abrir hacia el bot.
		Plantillas: []string{},
	}
	reg := Capabilities(config.ProtocolEscaladoV1, true)
	if reg.Protocol != 1 {
		t.Fatalf("protocol = %d, quiero 1", reg.Protocol)
	}
	if !reflect.DeepEqual(reg.Capabilities, want) {
		t.Fatalf("capacidades con la bandera en 1:\n got %+v\nwant %+v", reg.Capabilities, want)
	}
}

// LA REGLA (§1.3): la bandera gobierna lo que el bot EMITE y NO lo que LEE. Es la tabla de los
// booleanos del registro que dependen de ella, uno por uno, con su categoría: lo que la pone
// roja es declarar `false` una recepción que está viva, o `true` una emisión que el cable no
// lleva.
//
// `input.repetible` no está en la tabla: en Neuro es false con las dos banderas (se comprueba
// aparte), así que no tiene un estado «cumplido con la bandera en 1» que contrastar.
func TestCapabilities_EmissionFollowsFlagReceptionDoesNot(t *testing.T) {
	c0 := Capabilities(config.ProtocolEscaladoV0, true).Capabilities
	c1 := Capabilities(config.ProtocolEscaladoV1, true).Capabilities

	// Las TRES categorías. No son dos porque `resolve_lock` es EMISIÓN y aun así no la gobierna
	// la bandera: se emite en los dos dialectos, y un booleano declara lo que el binario hace en
	// el estado en que está la bandera, no en qué versión nació la clave.
	const (
		emissionV1Only   = "emisión, solo en el sobre de v1: la bandera la gobierna"
		emissionBothFlag = "emisión, en los DOS dialectos: la bandera no la toca"
		reception        = "RECEPCIÓN: dual y no se apaga nunca (D2), así que la bandera no la toca"
	)
	cases := []struct {
		key      string
		category string
		in0, in1 bool
	}{
		{"escalate.facts", emissionV1Only, c0.Escalation.Facts, c1.Escalation.Facts},
		{"escalate.reason_detail", emissionV1Only, c0.Escalation.ReasonDetail, c1.Escalation.ReasonDetail},
		{"return.rechazo_tipado", emissionV1Only, c0.Return.TypedRejection, c1.Return.TypedRejection},
		{"input.retoma_en", emissionV1Only, c0.Input.ResumesAt, c1.Input.ResumesAt},
		{"escalate.resolve_lock", emissionBothFlag, c0.Escalation.ResolveLock, c1.Escalation.ResolveLock},
		{"return.valores", reception, c0.Return.Values, c1.Return.Values},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if !c.in1 {
				t.Fatalf("%s: con la bandera en 1 el bot lo cumple, así que lo declara", c.key)
			}
			switch c.category {
			case emissionV1Only:
				if c.in0 {
					t.Fatalf("%s — %s: con la bandera en 0 el cable no lo lleva y el registro no puede declararlo",
						c.key, c.category)
				}
			default:
				if !c.in0 {
					t.Fatalf("%s — %s: la bandera no lo gobierna, así que en 0 se declara igual",
						c.key, c.category)
				}
			}
		})
	}

	// `repetible` es false con las dos banderas: no es que la bandera lo apague, es que el bot
	// no tiene formulario repetible.
	if c0.Input.Repeatable || c1.Input.Repeatable {
		t.Errorf("input.repetible es false siempre en Neuro: v0=%v v1=%v", c0.Input.Repeatable, c1.Input.Repeatable)
	}
	// `notices` es la otra RECEPCIÓN: son los Kind que ParseNotice acepta, y ParseNotice no
	// consulta ninguna bandera.
	if !reflect.DeepEqual(c0.Notices, c1.Notices) {
		t.Errorf("`notices` es recepción: las dos banderas declaran los mismos (%v vs %v)", c0.Notices, c1.Notices)
	}
	// Y `grupo_en_el_retorno` tampoco: el bot deriva el grupo en los dos dialectos.
	if c0.Return.GroupInTheReturn != c1.Return.GroupInTheReturn {
		t.Errorf("`grupo_en_el_retorno` no lo gobierna la bandera: %q vs %q",
			c0.Return.GroupInTheReturn, c1.Return.GroupInTheReturn)
	}
}

func TestCapabilities_RegistryCannotLieAboutTheLock(t *testing.T) {
	// Cruce contra el EMISOR y no contra una copia del literal: el alias que el registro
	// publica tiene que ser la clave de cable que escribe escalateRequest. Si alguien retira
	// el alias (fila A7: su AUSENCIA en el registro es la condición de retiro) y se olvida del
	// otro sitio, esto se pone rojo.
	typ := reflect.TypeOf(escalateRequest{})
	alias, ok := typ.FieldByName("CandadoResolver")
	if !ok {
		t.Fatal("escalateRequest ya no tiene CandadoResolver: ¿se retiró el alias? Retirarlo también del registro")
	}
	if got := jsonKey(alias.Tag.Get("json")); got != ResolveLockAliasKey {
		t.Fatalf("el alias del cable es %q y el registro declara %q", got, ResolveLockAliasKey)
	}
	lock, ok := typ.FieldByName("ResolveLock")
	if !ok {
		t.Fatal("escalateRequest ya no tiene ResolveLock, pero el registro lo declara en true")
	}
	if got := jsonKey(lock.Tag.Get("json")); got != "resolve_lock" {
		t.Fatalf("la clave de v1 del candado es %q", got)
	}
}

func TestCapabilities_NoticesMatchKnownKinds(t *testing.T) {
	// `notices` no es una lista escrita a mano: son los CUATRO Kind que ParseNotice admite
	// (KnownKinds, notice.go), en el orden del diseño. Un aviso declarado que el parser
	// rechazara sería una invitación a que Néctar lo mandara.
	got := Capabilities(config.ProtocolEscaladoV1, true).Capabilities.Notices
	want := make([]string, 0, len(KnownKinds()))
	for _, k := range KnownKinds() {
		want = append(want, string(k))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notices = %v, quiero los de KnownKinds %v", got, want)
	}
	// Y los literales de cable, a mano: si KnownKinds cambiara, el contrato con Néctar también.
	literal := []string{
		string(KindInboundMessage), string(KindConversationReturned),
		string(KindConversationResolved), string(KindConversationOpenedByAgent),
	}
	if !reflect.DeepEqual(got, literal) {
		t.Fatalf("notices = %v, quiero %v", got, literal)
	}
	for _, k := range got {
		if !Kind(k).Known() {
			t.Fatalf("el registro declara el aviso %q y ParseNotice no lo acepta", k)
		}
	}
}

func TestCapabilities_EncodeVersionIsStable(t *testing.T) {
	body1, v1, err := Capabilities(config.ProtocolEscaladoV1, true).Encode()
	if err != nil {
		t.Fatal(err)
	}
	body2, v2, err := Capabilities(config.ProtocolEscaladoV1, true).Encode()
	if err != nil {
		t.Fatal(err)
	}
	// ESTABLE entre lecturas: si llevara un sello de generación dentro del hash, Néctar
	// registraría una actualización en cada lectura, para siempre (nota del esquema).
	if v1 != v2 || string(body1) != string(body2) {
		t.Fatalf("dos lecturas seguidas dieron versiones distintas: %q vs %q", v1, v2)
	}
	if len(v1) != 16 {
		t.Fatalf("la versión mide %d, quiero 16 hex", len(v1))
	}
	// Y CAMBIA cuando cambia el contenido: es lo único que hace útil el 304.
	other := Capabilities(config.ProtocolEscaladoV1, true)
	other.Capabilities.Escalation.Facts = !other.Capabilities.Escalation.Facts
	if _, otherVersion, _ := other.Encode(); otherVersion == v1 {
		t.Fatal("la versión no cambió al cambiar las capacidades declaradas")
	}
	// La versión servida es la que viaja DENTRO del cuerpo.
	var reg CapabilityRegistry
	if err := json.Unmarshal(body1, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Version != v1 {
		t.Fatalf("el cuerpo dice %q y la cabecera diría %q", reg.Version, v1)
	}
}

func TestCapabilities_FlagMovesBodyAndVersion(t *testing.T) {
	// Con `facts` y `reason_detail` encendidos las dos banderas dan cuerpos DISTINTOS y
	// `version` cambia con ellos: ese movimiento del ETag es, literalmente, la señal con que
	// Néctar se entera de que el bot ya emite el resumen estructurado.
	b0, v0, err := Capabilities(config.ProtocolEscaladoV0, true).Encode()
	if err != nil {
		t.Fatal(err)
	}
	b1, v1, err := Capabilities(config.ProtocolEscaladoV1, true).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(b0) == string(b1) {
		t.Fatalf("las banderas tienen que diferir en el cuerpo:\n%s", b0)
	}
	if v0 == v1 {
		t.Fatal("y la versión tiene que moverse con ellas: es lo que dispara la relectura de Néctar")
	}
}

// requireKeys comprueba que estén TODAS las obligatorias y que no sobre ninguna fuera de las
// opcionales: el esquema es additionalProperties:false en los cuatro niveles.
func requireKeys(t *testing.T, m map[string]any, required, optional []string) {
	t.Helper()
	if m == nil {
		t.Fatalf("objeto ausente; esperaba las claves %v", required)
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Fatalf("falta la clave obligatoria %q en %v", k, slices.Sorted(maps.Keys(m)))
		}
	}
	for k := range m {
		if !slices.Contains(required, k) && !slices.Contains(optional, k) {
			t.Fatalf("clave %q de más (el esquema es additionalProperties:false)", k)
		}
	}
}

func requireBools(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k].(bool); !ok {
			t.Fatalf("%q tiene que ser booleano, es %#v", k, m[k])
		}
	}
}

// jsonKey saca el nombre de cable de una etiqueta `json:"..."` (descarta ,omitempty).
func jsonKey(tag string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	return tag
}
