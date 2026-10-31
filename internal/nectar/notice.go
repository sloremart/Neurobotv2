package nectar

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Kind es el tipo de aviso. Los literales van EN ESPAÑOL porque así los nombra el
// contrato que leen los dos lados: son literales de cable, no se traducen (§1).
type Kind string

// Los tres avisos de siempre. El cuarto, KindConversationOpenedByAgent, se declara aparte.
const (
	KindInboundMessage       Kind = "mensaje_entrante"
	KindConversationResolved Kind = "conversacion_resuelta"
	KindConversationReturned Kind = "conversacion_devuelta"
)

// KindConversationOpenedByAgent es EL SITIO DEL CUARTO AVISO (protocolo de escalado v1 de
// Néctar): el agente abre la conversación con una plantilla y
// el bot tiene que dejar la sesión PREABIERTA en el paso de esa plantilla, para que la primera
// respuesta del paciente no caiga en el saludo.
//
// Entra en el registro de capacidades y se resuelve de forma SÍNCRONA: el cuerpo es `opened{contact{identifier,
// profile_name}, channel_id, template_name, variables, agent{id, name}}` (Notice.Opened); la
// política de a quién responde el paciente —bot o agente— la decide el manejador del aviso
// (en Neuro siempre «agent»), no este paquete.
const KindConversationOpenedByAgent Kind = "conversacion_abierta_por_agente"

// KnownKinds son los CUATRO Kind de hoy, en el orden del diseño (entrante, devuelta, resuelta,
// abierta_por_agente) — la ÚNICA lista: tanto `Known()` como el registro de capacidades
// (capabilities.go, `notices`) la leen de aquí, así que no hay dos copias que puedan
// desincronizarse (un Kind nuevo que se olvide de este slice no queda aceptado por `Known` ni
// publicado en el registro; uno que solo se añada al slice sin querer aceptarlo lo pondría rojo
// el test de abajo, porque `Known` lo aceptaría también).
func KnownKinds() []Kind {
	return []Kind{
		KindInboundMessage,
		KindConversationReturned,
		KindConversationResolved,
		KindConversationOpenedByAgent,
	}
}

// Known dice si el tipo es uno de los de KnownKinds. Uno desconocido no es un error de parseo:
// es futuro declarado (§6.2) llegando antes de tiempo, y se acusa recibo para que Néctar no lo
// reintente indefinidamente. DERIVA de KnownKinds y no repite el literal (ver el comentario de
// arriba): así un Kind nuevo entra a los dos —o a ninguno— con un solo cambio.
func (k Kind) Known() bool {
	for _, known := range KnownKinds() {
		if k == known {
			return true
		}
	}
	return false
}

// NoticeTime es el instante del aviso, TOLERANTE a lo que venga escrito (§5.2 del diseño del
// protocolo de escalado v1).
//
// Néctar declara `occurred_at` como `format: date-time` (RFC 3339) y así lo emite; la tolerancia
// es una MEJORA del lector, no una condición del acuerdo. La razón es la desproporción: sin ella,
// un valor mal escrito tumbaría el `Unmarshal` del sobre ENTERO, el manejador contestaría 400 y
// el aviso se perdería — o sea que el PACIENTE se quedaría sin respuesta por una fecha que el bot
// no usa para decidir nada. Con ella, el instante queda a cero, se traza, y el aviso se procesa.
//
// Embebe time.Time a propósito: todo lo que hoy LEE el campo (IsZero, Equal, Format…) sigue
// compilando sin tocarse; para construirlo desde un time.Time está NoticeTimeOf.
type NoticeTime struct {
	time.Time
}

// NoticeTimeOf envuelve un instante para escribirlo en el sobre.
func NoticeTimeOf(t time.Time) NoticeTime { return NoticeTime{Time: t} }

// UnmarshalJSON NUNCA devuelve error: ése es todo el punto del tipo. Lo que no sea un RFC 3339
// legible deja el cero y una traza.
func (nt *NoticeTime) UnmarshalJSON(raw []byte) error {
	nt.Time = time.Time{}
	if string(raw) == "null" {
		// Ausencia explícita, no un defecto: no hay nada que avisar.
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s != "" {
			if t, perr := time.Parse(time.RFC3339, s); perr == nil {
				nt.Time = t
				return nil
			}
		}
	}
	// El valor es un instante, no texto del paciente ni del agente: se traza tal cual, acotado —y
	// con su LARGO REAL al lado, que es lo que distingue una fecha rara de un blob de 300 KB:
	// recortado, un blob se ve igual que 32 letras—.
	slog.Warn("nectar: occurred_at ilegible; el aviso se procesa igual con el instante a cero",
		"occurred_at", truncateForLog(string(raw), occurredAtLogMaxRunes), "len", len(raw))
	return nil
}

// occurredAtLogMaxRunes es cuánto del valor ilegible entra en la traza. Corto a propósito: lo que
// hace falta para reconocer QUÉ llegó, no para reproducirlo — el largo va aparte.
const occurredAtLogMaxRunes = 32

// truncateForLog acota un valor de traza a lo sumo n RUNAS (más el «…» que marca el corte, así que
// la cadena devuelta puede medir más de n BYTES: no es un tope de tamaño, es un tope de lectura).
//
// ⚠️ EL CORTE ES DE UNA PASADA, y eso no es cosmético: la versión anterior quitaba una runa por
// vuelta convirtiendo el slice ENTERO a cadena en cada una —cuadrático: 10 KB tardaba 3 s y
// 200 KB, 2 min 20 s—. Como el cuerpo del aviso llega acotado a 1 MB y no a 64 bytes, un aviso
// firmado con un blob donde debía ir la fecha bloqueaba la goroutine del webhook ANTES del 200 y
// del WAL, cada reintento de Néctar abría otra igual y el replay del WAL lo repetía en cada
// arranque: la tolerancia del §5.2 se volvía una denegación de servicio. Mismo patrón que
// `fitRejectionRunes` (client.go).
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s // n runas nunca ocupan menos de n bytes
	}
	// Recorte BARATO antes de convertir: una runa mide 4 bytes como mucho, así que estos bytes ya
	// contienen de sobra las n runas que van a salir (y una más, para que el corte sea siempre
	// visible). Sin esto, quedarse con 32 runas de un valor de 1 MB convertiría el megabyte
	// entero: O(n) igual, pero 46 ms medidos que no hacen falta.
	if len(s) > 4*(n+1) {
		s = s[:4*(n+1)]
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Notice es el sobre de un aviso (§8.6). La deserialización es TOLERANTE a propósito:
// encoding/json ignora los campos que no estén aquí, y es lo que el §6.2 exige — el
// sobre va a crecer (valor de desbloqueo en conversacion_devuelta) y un campo nuevo no
// puede tumbar el manejador. NO añadir DisallowUnknownFields.
type Notice struct {
	EventID string `json:"event_id"`
	// Protocol es la versión del protocolo con que Néctar escribió ESTE aviso. AUSENTE = 0 =
	// el dialecto de hoy, que es lo que llega hasta que ellos desplieguen v1. Una versión
	// MAYOR que ProtocolMax no rechaza el aviso: alerta y 200 (§1.2 del diseño) — el buzón de
	// Néctar tiene espera exponencial SIN CORTE, así que un 400 permanente reintentaría para
	// siempre, y el precio lo pagaría el paciente que espera una respuesta.
	Protocol       int            `json:"protocol,omitempty"`
	Kind           Kind           `json:"kind"`
	ConversationID int64          `json:"conversation_id"`
	OccurredAt     NoticeTime     `json:"occurred_at"`
	Message        *NoticeMessage `json:"message,omitempty"`
	// ReturnValue es el valor del MENÚ DE RETORNOS que el agente eligió al devolver la
	// conversación: NUESTRO token, de vuelta tal cual (§6.2 punto 2, enmienda 2026-08-24).
	// Va SÓLO en conversacion_devuelta y con omitempty. Puntero porque AUSENTE ≠ VACÍO:
	// ausente es «me la devolvieron sin decisión» y se reanuda como siempre.
	ReturnValue *string `json:"return_value,omitempty"`
	// Return es EL RETORNO v1 (§4.1 del diseño del protocolo de escalado v1): `{opcion, valores?}`.
	// Va SÓLO en conversacion_devuelta y cuando el aviso habla el protocolo. Es LA FUENTE DE
	// VERDAD cuando viene: si llegaran las dos, manda ésta sobre ReturnValue —la misma regla, y
	// por la misma razón, que el candado y su alias heredado (fila A7)—. Ausente = el dialecto.
	Return *V1Return `json:"retorno,omitempty"`
	// Motivo es por qué Néctar resolvió la conversación (solo conversacion_resuelta, opcional,
	// texto plano). Hoy el único valor acordado es "cola_vencida": el caso encolado fuera de
	// horario venció (8 horas hábiles en `waiting`) y Néctar avisó al paciente y resolvió
	// (opción B, 2026-09-12). Vacío = resolución sin motivo.
	Motivo string `json:"motivo,omitempty"`
	// Actor es quién cerró la conversación (solo conversacion_resuelta, Plan 43). nil = no consta.
	Actor *NoticeActor `json:"actor,omitempty"`
	// EscalatedBy es quién escaló ESTA atención (solo conversacion_resuelta, Plan 43). nil = no
	// consta: la clave falta cuando la atención no tiene asiento de escalado, entró por el ingreso
	// sin bot, se reabrió o el aviso estaba encolado antes del despliegue de Néctar. Ausente nunca
	// significa «nadie».
	EscalatedBy *NoticeActor `json:"escalada_por,omitempty"`
	// Opened es el cuerpo del CUARTO AVISO (kind=conversacion_abierta_por_agente): el agente
	// abrió la conversación con una plantilla de Meta. Puntero porque solo viaja en ese kind;
	// ver ValidOpening.
	Opened *NoticeOpened `json:"opened,omitempty"`
}

// NoticeActor es quien hizo algo en una conversación de Néctar (`actor` y `escalada_por` de
// conversacion_resuelta): una clase y, solo con la clase `agente`, su id. Sin nombre ni correo.
type NoticeActor struct {
	Class   string `json:"clase"` // agente | bot | sistema
	AgentID int64  `json:"agent_id,omitempty"`
}

// UnmarshalJSON nunca falla (mismo criterio que NoticeID): `agent_id` acepta número o cadena
// numérica, y cualquier otra forma —un `actor` que no es objeto, una clase que no es texto, un id
// ilegible— queda «no consta» (Label ""). La autoría es un dato de informe: un error aquí tumbaría
// el aviso de resuelta ENTERO (400, Néctar reintenta sin fin y la sesión queda escalada).
func (a *NoticeActor) UnmarshalJSON(b []byte) error {
	*a = NoticeActor{}
	var raw struct {
		Class   json.RawMessage `json:"clase"`
		AgentID NoticeID        `json:"agent_id"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		slog.Warn("nectar: actor ilegible; no consta y el aviso se procesa igual",
			"valor", truncateForLog(string(b), occurredAtLogMaxRunes), "len", len(b))
		return nil //nolint:nilerr // tolerancia deliberada: el actor ilegible NO tumba el aviso (ver arriba)
	}
	if err := json.Unmarshal(raw.Class, &a.Class); err != nil {
		a.Class = "" // clase ausente o que no es texto: no consta
	}
	if id, err := strconv.ParseInt(raw.AgentID.String(), 10, 64); err == nil {
		a.AgentID = id
	}
	return nil
}

// Label es la etiqueta con que el bot guarda al actor en `escalations`: "agente:8", "bot",
// "sistema". nil, clase vacía o un agente sin id legible dan "" (no consta): mejor no atribuir que
// atribuir mal.
func (a *NoticeActor) Label() string {
	if a == nil {
		return ""
	}
	switch a.Class {
	case "agente":
		if a.AgentID < 1 {
			return ""
		}
		return "agente:" + strconv.FormatInt(a.AgentID, 10)
	case "bot", "sistema":
		return a.Class
	}
	return ""
}

// NoticeOpened es el cuerpo de conversacion_abierta_por_agente (esquema del aviso de Néctar).
type NoticeOpened struct {
	Contact NoticeContact `json:"contact"`
	// ChannelID y Agent.ID llegan como NÚMEROS JSON (`"channel_id": 3`, `"agent": {"id": 17}`,
	// aviso de Néctar del 2026-09-18: es la misma forma de sus otros tres avisos). Se leen con
	// NoticeID, que acepta número o cadena numérica: un tipo estricto habría respondido 400 a
	// cada aviso y el buzón de Néctar lo habría reintentado sin fin.
	ChannelID    NoticeID          `json:"channel_id"`
	TemplateName string            `json:"template_name"`
	Variables    map[string]string `json:"variables"`
	Agent        NoticeAgent       `json:"agent"`
}

// NoticeContact es el paciente al que el agente le abrió la conversación con la plantilla.
// Identifier viene como en `mensaje_entrante`: E.164 SIN el signo `+` (solo dígitos, p. ej.
// "573001234567"); se acepta también con `+` por tolerancia.
type NoticeContact struct {
	Identifier  string `json:"identifier"`
	ProfileName string `json:"profile_name"`
}

// NoticeAgent es el agente humano que abrió la conversación (empleado identificado, no
// paciente: por eso MaskedForInbox no lo enmascara).
type NoticeAgent struct {
	ID   NoticeID `json:"id"`
	Name string   `json:"name"`
}

// NoticeID es un identificador de Néctar que en el cable viaja como NÚMERO JSON (así manda
// `channel_id` y `agent.id`) pero que el bot solo usa como texto (trazas, resumen). Acepta
// número, cadena numérica o cadena cualquiera; `null` o ausente queda vacío. Serializa como
// cadena, que es lo que el WAL enmascarado y las trazas necesitan.
type NoticeID string

// UnmarshalJSON acepta número, cadena o cualquier otro JSON, y nunca falla (ver NoticeID).
func (id *NoticeID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*id = ""
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*id = NoticeID(str)
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(b, &num); err != nil {
		// NI NÚMERO NI CADENA (un booleano, un objeto, un array): queda VACÍO y NO es un error
		// (M14 de la revisión final). El id es INFORMATIVO —va a una traza y al rótulo «Abierta
		// por:» del resumen, y ahí un identificador ausente solo se lee peor—, mientras que
		// devolver error aquí tumba el parseo del aviso ENTERO: el manejador responde 400 y el
		// buzón de Néctar reintenta con espera exponencial SIN CORTE. La misma razón por la que
		// `occurred_at` ilegible tampoco pierde el aviso (§5.2).
		slog.Warn("nectar: id ni número ni cadena; se deja vacío y el aviso se procesa igual",
			"valor", truncateForLog(s, occurredAtLogMaxRunes), "len", len(b))
		*id = ""
		return nil //nolint:nilerr // tolerancia deliberada: el error de decodificación NO tumba el aviso (ver arriba)
	}
	*id = NoticeID(num.String())
	return nil
}

// String devuelve el identificador como texto ("" si no vino).
func (id NoticeID) String() string { return string(id) }

// ValidOpening dice si este aviso es el CUARTO AVISO y trae lo mínimo para preabrir la sesión:
// el kind correcto y una plantilla nombrada (sin TemplateName no hay qué registrar ni con qué
// identificar la apertura en la traza).
func (n Notice) ValidOpening() bool {
	return n.Kind == KindConversationOpenedByAgent && n.Opened != nil && n.Opened.TemplateName != ""
}

// MaskedForInbox devuelve el sobre re-serializado para el WAL (message_inbox), con la PII del
// paciente enmascarada: el identificador del contacto y cada variable de la plantilla, que
// pueden llevar nombre, documento o fecha de cita (Ley 1581). SOLO este kind trae PII en el
// sobre —los otros tres no traen ni el teléfono ni el nombre del paciente (§8.6)—, así que es
// el único que necesita esta copia. El agente NO se enmascara: es un empleado identificado, no
// un paciente, y su nombre es justamente lo que el resumen necesita mostrar.
//
// La copia es SUPERFICIAL en Notice y profunda en Opened: el original nunca se muta.
func (n Notice) MaskedForInbox() ([]byte, error) {
	if n.Opened == nil {
		return json.Marshal(n)
	}
	masked := *n.Opened
	masked.Contact.Identifier = utils.MaskPhone(masked.Contact.Identifier)
	masked.Contact.ProfileName = maskFirstLetter(masked.Contact.ProfileName)
	if len(masked.Variables) > 0 {
		vars := make(map[string]string, len(masked.Variables))
		for k, v := range masked.Variables {
			vars[k] = maskShort(v)
		}
		masked.Variables = vars
	}
	out := n
	out.Opened = &masked
	return json.Marshal(out)
}

// maskFirstLetter enmascara el nombre del paciente (ProfileName) para el WAL: su primera letra
// y "…". Vacío se queda vacío: no hay nada que enmascarar.
func maskFirstLetter(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return string(r[:1]) + "…"
}

// maskShort enmascara una variable de plantilla (nombre, fecha…) para el WAL: los primeros dos
// caracteres y "…", o solo "…" si mide 2 o menos.
func maskShort(s string) string {
	r := []rune(s)
	if len(r) <= 2 {
		return "…"
	}
	return string(r[:2]) + "…"
}

// ChosenReturnValue es el token elegido por el agente, y si venía. Sólo tiene sentido en
// conversacion_devuelta: en los otros dos avisos la clave no viaja, ni vacía.
func (n Notice) ChosenReturnValue() (string, bool) {
	if n.Kind != KindConversationReturned || n.ReturnValue == nil || *n.ReturnValue == "" {
		return "", false
	}
	return *n.ReturnValue, true
}

// V1Return es el retorno del protocolo v1: el token que el agente eligió y —solo si el paso
// tenía campos— los datos que escribió.
//
// `valores` se guarda CRUDO (json.RawMessage) a propósito: sus claves salen del DESCRIPTOR que
// el bot emitió (la de la lista es `repetible_por.clave`, las de cada repetición son las
// `campos[].clave`), así que enumerarlas aquí sería declarar en el transporte lo que el paso
// declara en su registro — y las dos listas se separarían el día que un paso añada un campo.
// Quien conoce la forma es el parser del paso; este tipo solo la transporta.
type V1Return struct {
	Option string `json:"opcion"`
	// Values es opcional: ausente en un paso de 0 campos. Vacío y ausente son lo mismo.
	Values json.RawMessage `json:"valores,omitempty"`
}

// v1ReturnEnvelope es el OBJETO SERIALIZADO dentro de `return_value` — la fuente (2). Lleva su
// propia `protocol` porque es el cuerpo del retorno tal cual lo define return.schema.json; lo
// que gobierna cómo se lee el aviso sigue siendo la versión DEL AVISO.
type v1ReturnEnvelope struct {
	Protocol int             `json:"protocol"`
	Option   string          `json:"opcion"`
	Values   json.RawMessage `json:"valores,omitempty"`
}

// V1Return extrae el retorno v1 de un aviso `conversacion_devuelta`, si lo trae. Prueba DOS
// fuentes, en este orden (§4.1 del diseño):
//
//  1. la clave de cuerpo `retorno` — la forma limpia, acordada con Néctar el 2026-09-14;
//  2. `return_value` con el OBJETO SERIALIZADO (`{"protocol":1,"opcion":…,"valores":…}`), que
//     cabe sin cambiar ni un esquema porque ese valor es opaco y viaja byte a byte.
//
// Si vienen las dos, manda (1). Lo que NO se hace es adivinar: un `return_value` que no sea
// ninguna de las dos formas ni ninguno de los prefijos de hoy no es v1, y el camino del
// dialecto queda intacto —reanuda sin decidir, como siempre—.
//
// Un `retorno` SIN `opcion` no es un retorno (el esquema la exige, minLength 1): no se usa, y no
// se le come la precedencia al `return_value` que sí pueda traer algo utilizable.
func (n Notice) V1Return() (V1Return, bool) {
	if n.Kind != KindConversationReturned {
		return V1Return{}, false
	}
	if n.Return != nil {
		if n.Return.Option != "" {
			return *n.Return, true
		}
		slog.Warn("nectar: `retorno` sin opcion, se ignora", "conversation_id", n.ConversationID)
	}
	if n.ReturnValue == nil {
		return V1Return{}, false
	}
	raw := strings.TrimSpace(*n.ReturnValue)
	// Las dos condiciones son baratas y descartan de una todo el dialecto (tokens y prefijos):
	// solo un objeto JSON con `protocol` puede ser el retorno serializado.
	if !strings.HasPrefix(raw, "{") || !strings.Contains(raw, "protocol") {
		return V1Return{}, false
	}
	var env v1ReturnEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil || env.Option == "" {
		// El `err` NO se traza, a diferencia de lo normal: el de `encoding/json` incluye EL
		// CARÁCTER OFENSOR del cuerpo, y el cuerpo es un retorno con `valores` —que puede
		// llevar el documento del paciente (§2.5)—. Misma omisión y misma forma que la rama
		// hermana de `return_menu.go` («`valores` no es un objeto»): queda el `len`, que
		// distingue un cuerpo enorme de uno vacío, y el `conversation_id`, que es con lo que
		// se encuentra el caso. (Además `err` es nil cuando lo que falta es la `opcion`, así
		// que ahí ni siquiera decía nada.)
		slog.Warn("nectar: return_value parece un retorno v1 y no se pudo leer; sigue el camino del dialecto",
			"conversation_id", n.ConversationID, "len", len(raw))
		return V1Return{}, false
	}
	return V1Return{Option: env.Option, Values: env.Values}, true
}

// NoticeMessage es lo que trae mensaje_entrante además del sobre.
type NoticeMessage struct {
	ID         int64             `json:"id"`
	ExternalID string            `json:"external_id"` // wamid de Meta
	Sequence   int64             `json:"sequence"`
	Kind       string            `json:"kind"` // tipo de WhatsApp: text, image, document, audio…
	Text       string            `json:"text"` // texto o pie de foto
	Attachment *NoticeAttachment `json:"attachment,omitempty"`
	// InteractiveReplyID es el IDENTIFICADOR del botón/fila que el paciente pulsó: el que
	// este bot eligió al armar el menú (Button.Payload / ListRow.ID). Enmienda 2026-08-21 a
	// §8.6. Puntero porque AUSENTE ≠ VACÍA: viaja con omitempty y solo en pulsaciones
	// legibles; sin la clave, esto no fue una pulsación (texto, medio o pulsación ilegible).
	// Se ramifica SOLO por el id, nunca por el título (Text) en español.
	InteractiveReplyID *string `json:"interactive_reply_id,omitempty"`
}

// NoticeAttachment es la referencia LOCAL del adjunto: la URL apunta a
// GET /api/bot/attachments/{id} de Néctar, nunca a Meta. Puede llegar antes de que el
// archivo esté listo (404 mientras tanto → reintentar la descarga).
type NoticeAttachment struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

// ProtocolMax es la versión de protocolo MÁS ALTA que este binario sabe leer. Un aviso con una
// mayor se procesa igual —el lector es tolerante a propósito— y deja una alerta.
const ProtocolMax = 1

// UnusableAttachmentType es el MessageType de un adjunto que llegó sin nada con qué
// descargarlo (§5.3 del diseño, agujero de HOY): ni `url` ni `id`, o el adjunto falló del lado
// de Néctar. NO se despacha como `image`/`document` porque el paso de la orden médica vería un
// mensaje de imagen SIN imagen y se quedaría mirando una URL vacía; con un tipo que la máquina
// no maneja, le pide otra foto al paciente, que es lo correcto.
const UnusableAttachmentType = "adjunto_no_disponible"

// Los dos defectos que vuelven inutilizable un aviso (ver ParseNotice).
var (
	ErrNoEventID      = errors.New("nectar: aviso sin event_id")
	ErrNoConversation = errors.New("nectar: aviso sin conversation_id")
)

// ParseNotice lee el sobre. Rechaza solo lo que hace inutilizable el aviso (JSON roto,
// sin event_id, sin conversación); todo lo demás —campos de más, kind desconocido— pasa.
func ParseNotice(body []byte) (Notice, error) {
	var n Notice
	if err := json.Unmarshal(body, &n); err != nil {
		return Notice{}, fmt.Errorf("nectar: aviso ilegible: %w", err)
	}
	if n.EventID == "" {
		return Notice{}, ErrNoEventID
	}
	if n.ConversationID == 0 {
		return Notice{}, ErrNoConversation
	}
	return n, nil
}

// SessionKey es el identificador con el que el bot indexa la sesión de una conversación
// de Néctar. Ocupa el sitio del teléfono en toda la tubería local (sessions, message_inbox,
// chat_events… VARCHAR(63)) porque EL AVISO NO TRAE EL TELÉFONO del paciente — por diseño
// del ámbito mínimo de Néctar (§8.6). Con Néctar el destino de un envío es la conversación,
// no el número, así que el teléfono deja de ser necesario para contestar. El prefijo evita
// que se confunda con un número real y deja ver en los registros de dónde vino la sesión.
func SessionKey(conversationID int64) string {
	return "nectar:" + strconv.FormatInt(conversationID, 10)
}

// ToInboundMessage traduce un mensaje_entrante al tipo que consume la máquina de estados.
//
// msg.ID = event_id: es la clave con la que deduplican tanto el WAL (message_inbox) como el
// pool (recentMessages). El adjunto se pone en ImageURL/DocumentURL según el tipo, que es
// lo que AnalyzeDocument lee hoy; la descarga con credencial de Néctar la resuelve
// OCRService.downloadMedia (§2.2).
//
// baseURL es la raíz de Néctar (cfg.NectarBaseURL) y sirve para UNA cosa: componer la URL del
// adjunto a partir de su `id` cuando el aviso no trae `url` (§5.3, cinturón y tirantes —
// Attachment.ID se parseaba y no se usaba en ninguna parte). Vacía = no se compone nada, y un
// adjunto que se quede sin URL utilizable NO se despacha como imagen.
func (n Notice) ToInboundMessage(baseURL string) bird.InboundMessage {
	msg := bird.InboundMessage{
		ID:             n.EventID,
		Phone:          SessionKey(n.ConversationID),
		ConversationID: strconv.FormatInt(n.ConversationID, 10),
		ReceivedAt:     time.Now(),
		MessageType:    "text",
	}
	m := n.Message
	if m == nil {
		return msg
	}
	msg.Text = m.Text
	if m.InteractiveReplyID != nil && *m.InteractiveReplyID != "" {
		// Pulsación legible: mismo contrato que bird.ParseInboundMessage con un postback —
		// la máquina de estados valida por PostbackPayload y Text lleva el mismo id.
		msg.MessageType = "postback"
		msg.IsPostback = true
		msg.PostbackPayload = *m.InteractiveReplyID
		msg.Text = *m.InteractiveReplyID
		return msg
	}
	attachmentURL := ""
	if m.Attachment != nil {
		attachmentURL = m.Attachment.URL
		if attachmentURL == "" && m.Attachment.ID != "" && baseURL != "" {
			attachmentURL = strings.TrimRight(baseURL, "/") + "/api/bot/attachments/" + m.Attachment.ID
		}
	}
	switch m.Kind {
	case "", "text":
		msg.MessageType = "text"
	case "sticker":
		// Mismo tipo que produce bird.ParseInboundMessage: lo rechaza el interceptor de mensajes
		// no soportados. NO es una imagen —iría al OCR como si fuera la foto de una orden— y no
		// necesita descarga, así que se decide antes de mirar el adjunto.
		msg.MessageType = "sticker"
	case "contacts":
		// Néctar nombra la tarjeta de contacto en plural, como el payload crudo de WhatsApp;
		// Neuro la maneja con el tipo singular que ya produce Bird.
		msg.MessageType = "contact"
	case "image", "document":
		// Sin NADA con que descargarlo, el adjunto no se despacha como adjunto: ver
		// UnusableAttachmentType. El aviso se procesa igual y el paso pide otra foto.
		if attachmentURL == "" {
			slog.Warn("nectar: adjunto sin url ni id utilizables; no se despacha como adjunto",
				"event_id", n.EventID, "conversation_id", n.ConversationID, "kind", m.Kind)
			msg.MessageType = UnusableAttachmentType
			return msg
		}
		if m.Kind == "document" {
			msg.MessageType = "document"
			msg.DocumentURL = attachmentURL
			return msg
		}
		msg.MessageType = "image"
		msg.ImageURL = attachmentURL
	default:
		// audio, video, location, interactive… — la máquina de estados ya sabe qué decirle al
		// paciente con un tipo que no maneja.
		msg.MessageType = m.Kind
	}
	return msg
}
