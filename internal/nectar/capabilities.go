package nectar

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

// --- El registro de capacidades (hdg-bot docs/DISENO-PROTOCOLO-ESCALADO-V1-BOT.md §1.3) ---
//
// Es lo que el bot contesta en GET /api/nectar/capabilities, la ruta que sustituye las TRES
// deducciones silenciosas con que Néctar adivinaba qué habla el bot. La forma la fija su
// esquema (`nectar-protocolo/internal/protocol/schema/capabilities.schema.json`,
// additionalProperties:false en los cuatro niveles), así que aquí no sobra ni falta una clave.
//
// D5 — SE CALCULA DEL BINARIO, NUNCA DE CONFIGURACIÓN: cada booleano sale de la misma
// constante o del mismo emisor que lo cumple. Una capacidad declarada que el código no cumple
// es peor que no declararla: Néctar pediría —o esperaría— algo que nunca va a llegar, y el
// desenlace de su regla de fallo es conservar lo último conocido sin alertar de nada.

// Los dos motivos del vocabulario cerrado de Néctar que este bot emite. Viven aquí, en la
// frontera del cable, y NO como literales sueltos en el handler de escalación: el registro los
// publica y tiene que publicar los que de verdad salen. La lista es informativa —el vocabulario
// que manda sigue siendo el del servidor de Néctar— pero un tercer motivo emitido sin añadirlo
// aquí dejaría al registro mintiendo.
const (
	ReasonBotCannotAnswer      = "bot_cannot_answer"
	ReasonPatientAskedForHuman = "patient_asked_for_human"
)

// ResolveLockAliasKey es el nombre HEREDADO del candado del «Resolver», el que escalateRequest
// sigue emitiendo junto a `resolve_lock` (fila A7 del acta). Se publica en el registro porque
// su AUSENCIA allí es la condición de retiro del alias: ausente y presente-con-valor son dos
// datos distintos y no se colapsan.
const ResolveLockAliasKey = "candado_resolver"

// Lo que este BINARIO cumple una vez entregada la fase A de la migración a Néctar. Son
// constantes y no configuración a propósito (D5): la enciende quien despliega el emisor, en el
// mismo commit, y no hay ningún `.env` capaz de declarar una capacidad que el código no tenga.
// Las de EMISIÓN además solo salen con la bandera PROTOCOL_ESCALADO_VERSION en 1 (que nace en
// 0), así que mientras un emisor no esté desplegado el registro por defecto no lo declara.
const (
	// emitsFacts — el resumen estructurado (`facts` en el sobre).
	emitsFacts = true
	// emitsReasonDetail — `reason_detail`, el motivo interno con su guarda de forma
	// (safeReasonDetail en client.go).
	emitsReasonDetail = true
	// emitsInputDescriptor — el descriptor `input` que sustituye a `bot_return_menu`: es cuando
	// `bot_return_menu` deja de viajar.
	emitsInputDescriptor = true
	// parsesReturnValues — el retorno v1 con los datos separados del token (V1Return en
	// notice.go). Es de RECEPCIÓN, así que la bandera NO la gobierna (ver la regla en
	// Capabilities): se declara igual en los dos estados.
	parsesReturnValues = true
	// emitsTypedRejection — `rechazo{motivo,intento,eco,campo?,pieza?}` (escalateRequest). La
	// gobierna la bandera como a las demás de emisión: la clave solo sale en el sobre de v1.
	emitsTypedRejection = true
)

// groupInTheReturn es la posición del bot sobre si el grupo tiene que seguir viajando en el
// retorno. "opcional": el bot sabe deducirlo, pero un retorno que lo traiga sigue siendo
// válido. Es obligatoria EN EL REGISTRO —un bot que no la declare deja a Néctar adivinando— y
// no la gobierna la bandera: el bot deriva el grupo en los dos dialectos.
const groupInTheReturn = "opcional"

// Los dos vocabularios del descriptor. Solo VIAJAN cuando emitsInputDescriptor está encendida
// (si no, van como listas vacías, que es lo que el binario cumple).
//
// Son DOS tipos y no los del vocabulario completo de Néctar: los pasos de Neuro solo le piden
// al agente texto, corto o largo. No hay fecha, hora ni enlace, y declararlos haría que Néctar
// esperara algo que nunca llega. Se cruzan contra lo que EMITEN los pasos, y en los dos sentidos,
// en TestElRegistroDelDescriptorNoMiente (internal/statemachine/handlers): declarar de más hace
// que Néctar espere algo que no llega; declarar de menos deja al adaptador pintando de menos.
var (
	inputKinds = []string{"texto", "texto_largo"}
	inputExits = []string{"reanudar", "reanudar_sin_dato", "cerrar"}
)

// CapabilityRegistry es el cuerpo de GET /api/nectar/capabilities.
type CapabilityRegistry struct {
	// Protocol es const 1 en el esquema: la versión del PROTOCOLO de esta ruta, que es de v1
	// aunque el bot todavía EMITA el dialecto. Lo que la bandera gobierna son las capacidades,
	// no la ruta — si no, un bot en 0 no tendría forma de decir que está en 0.
	Protocol int `json:"protocol"`
	// Version es el hash del CONTENIDO servido, 16 hex. Ver Encode.
	Version      string        `json:"version"`
	Capabilities CapabilitySet `json:"capabilities"`
}

// CapabilitySet — qué habla este bot. Las listas van SIEMPRE como listas, nunca nulas (fila
// E46): un nulo sería una segunda forma de decir lo mismo que el parser tendría que interpretar.
type CapabilitySet struct {
	Escalation EscalationCapabilities `json:"escalate"`
	Input      InputCapabilities      `json:"input"`
	Return     ReturnCapabilities     `json:"return"`
	Notices    []string               `json:"notices"`
	// Plantillas es INFORMATIVO (qué plantillas conoce el bot para que un agente abra
	// conversación). En Neuro va VACÍA —sigue siendo una lista, no un nulo—: no hay plantilla
	// que un agente pueda abrir hacia el bot, y el cuarto aviso siempre responde «agent».
	Plantillas []string `json:"plantillas"`
}

// EscalationCapabilities es lo que el bot declara sobre el sobre de `escalate`.
type EscalationCapabilities struct {
	Facts        bool `json:"facts"`
	ReasonDetail bool `json:"reason_detail"`
	ResolveLock  bool `json:"resolve_lock"`
	// ResolveLockAlias es opcional EN EL ESQUEMA y su ausencia es un dato (el alias se retiró).
	ResolveLockAlias string   `json:"resolve_lock_alias,omitempty"`
	Reasons          []string `json:"reasons"`
}

// InputCapabilities es lo que el bot declara sobre el descriptor `input`.
type InputCapabilities struct {
	Kinds []string `json:"tipos"`
	Exits []string `json:"salidas"`
	// Repeatable es false siempre en Neuro: no hay formulario repetible, así que el descriptor
	// nunca lleva piezas entre las que repartir.
	Repeatable bool `json:"repetible"`
	ResumesAt  bool `json:"retoma_en"`
}

// ReturnCapabilities es lo que el bot declara sobre el retorno de la conversación.
type ReturnCapabilities struct {
	Values           bool   `json:"valores"`
	TypedRejection   bool   `json:"rechazo_tipado"`
	GroupInTheReturn string `json:"grupo_en_el_retorno"`
}

// Capabilities arma el registro para la versión de protocolo que el bot EMITE (0 = el dialecto
// de hoy, 1 = v1). Dos entradas gobiernan cada booleano y hacen falta las dos:
//
//   - que el emisor —o el LECTOR— exista en este binario (las constantes de arriba), y
//   - que la bandera lo deje salir, donde la bandera manda.
//
// ⚠️ LA REGLA: **la bandera gobierna lo que el bot EMITE, nunca lo que LEE**. De ahí las TRES
// categorías de booleano (fijadas por TestCapabilities_EmissionFollowsFlagReceptionDoesNot):
//
//   - EMISIÓN solo en el sobre de v1 (`facts`, `reason_detail`, `rechazo_tipado` y el descriptor
//     `input` entero): con la bandera en 0 el cable no los lleva, así que el registro no los
//     declara — Néctar no puede pedir lo que no va a recibir.
//   - EMISIÓN en los DOS dialectos (`resolve_lock` y su alias, los dos `reasons`): la bandera no
//     los toca, porque escalateRequest los escribe igual en 0 y en 1.
//   - RECEPCIÓN (`return.valores`, `notices`, y `grupo_en_el_retorno`, que el bot DERIVA en los
//     dos dialectos): la recepción es dual y NO SE APAGA NUNCA (D2), así que la bandera tampoco
//     los toca.
//
// El precio de equivocarse es concreto: Néctar negocia POR CAPACIDAD con este registro, así que
// un `false` en una recepción viva le haría mandar el dialecto a un bot que lee el retorno v1.
// Un booleano del registro dice qué HACE este binario con la bandera donde está, no en qué
// versión nació la clave.
func Capabilities(protocolVersion int) CapabilityRegistry {
	v1 := protocolVersion == config.ProtocolEscaladoV1
	descriptor := emitsInputDescriptor && v1

	in := InputCapabilities{
		Kinds: []string{}, Exits: []string{},
		// Neuro no tiene formulario repetible: false con las dos banderas.
		Repeatable: false,
		ResumesAt:  descriptor,
	}
	if descriptor {
		in.Kinds = append(in.Kinds, inputKinds...)
		in.Exits = append(in.Exits, inputExits...)
	}

	return CapabilityRegistry{
		Protocol: config.ProtocolEscaladoV1,
		Capabilities: CapabilitySet{
			Escalation: EscalationCapabilities{
				Facts:        emitsFacts && v1,
				ReasonDetail: emitsReasonDetail && v1,
				// El candado NO lo gobierna la bandera: escalateRequest lo emite en los dos
				// dialectos, bajo `resolve_lock` y bajo su alias.
				ResolveLock:      true,
				ResolveLockAlias: ResolveLockAliasKey,
				Reasons:          []string{ReasonBotCannotAnswer, ReasonPatientAskedForHuman},
			},
			Input: in,
			Return: ReturnCapabilities{
				// RECEPCIÓN: el lector existe en este binario y no consulta ninguna bandera, así
				// que se declara igual en los dos estados. Ver la regla de arriba.
				Values:           parsesReturnValues,
				TypedRejection:   emitsTypedRejection && v1,
				GroupInTheReturn: groupInTheReturn,
			},
			// Los avisos que ParseNotice acepta, en el orden del diseño. No es una lista escrita
			// a mano: sale de KnownKinds() (notice.go), los mismos Kind que Known() acepta.
			Notices: noticeKinds(),
			// Lista vacía y no nil: un nulo serializaría `null` (ver CapabilitySet).
			Plantillas: []string{},
		},
	}
}

// noticeKinds son los Kind que KnownKinds() declara, como cadenas de cable y en el mismo orden.
func noticeKinds() []string {
	kinds := KnownKinds()
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}

// Encode serializa el registro y devuelve el cuerpo servido y su versión.
//
// La versión es el sha256 del CONTENIDO —el mismo cuerpo con `version` vacía—, 16 hex. No
// puede ser el hash de los bytes servidos en sentido literal (incluirían su propio hash), y lo
// que la regla protege es otra cosa: que NADA que cambie por petición entre en el cálculo. Por
// eso aquí no hay sello de generación ni de arranque — con uno dentro, Néctar registraría una
// actualización en cada lectura, para siempre, y el 304 no serviría de nada.
func (r CapabilityRegistry) Encode() ([]byte, string, error) {
	r.Version = ""
	content, err := json.Marshal(r)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(content)
	r.Version = hex.EncodeToString(sum[:])[:16]
	body, err := json.Marshal(r)
	if err != nil {
		return nil, "", err
	}
	return body, r.Version, nil
}
