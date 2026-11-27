package bird

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// NectarBridge es lo que el cliente de Bird necesita de Néctar para DESVIAR las
// conversaciones que ya no son de Bird.
//
// # Por qué el desvío vive aquí y no en una interfaz de mensajería nueva
//
// Decenas de archivos reciben *bird.Client concreto: los handlers del flujo, el pool, el
// session manager, las notificaciones. Sustituir eso por una interfaz es una cirugía de todo
// el árbol que el camino de Bird necesita intacto (Bird sigue vivo mientras dura la migración,
// para la voz y para las conversaciones que aún no pasaron a Néctar). Poner el desvío DENTRO del
// cliente de Bird es el punto por el que ya pasan todas las llamadas, y deja ese camino sin
// tocar: con nectar == nil el cliente hace exactamente lo de antes. La interfaz se declara aquí
// para no importar el paquete nectar (que importa este).
//
// # Qué conversación es de Néctar
//
// La clave de sesión "nectar:<id>" (nectar.SessionKey) ocupa el sitio del teléfono, y SOLO ella
// desvía. El ConversationID numérico NO basta: suponer que "Bird usa UUIDs, así que un id de puros
// dígitos es de Néctar" manda al Néctar REAL cierres y mensajes con ids que vienen de cualquier
// otra parte (pruebas, datos viejos). La clave de sesión solo la construye el webhook de Néctar.
type NectarBridge interface {
	SendText(ctx context.Context, conversationID int64, text string) (string, error)
	SendButtons(ctx context.Context, conversationID int64, text string, buttons []Button) (string, error)
	SendList(ctx context.Context, conversationID int64, body, buttonLabel string, sections []ListSection) (string, error)
	SendTemplate(ctx context.Context, conversationID int64, tmpl TemplateConfig) (string, error)
	// Available contesta si se puede escalar esta conversación. «No» es respuesta normal.
	Available(ctx context.Context, conversationID int64) (bool, string, error)
	// Escalate pasa la conversación a un agente con el resumen DENTRO: después de esto la
	// conversación ya no es del bot y cualquier otra ruta contesta 404. Todo lo que viaja en
	// el sobre va en Escalation, que es la única firma que el protocolo v1 cambia.
	Escalate(ctx context.Context, conversationID int64, esc Escalation) error
	Close(ctx context.Context, conversationID int64) error
	// ConversationState devuelve el estado de la conversación tal como Néctar lo publica
	// (`waiting`, `assigned`…), SIN interpretarlo: quien decide qué significa es el bot.
	ConversationState(ctx context.Context, conversationID int64) (string, error)
	// IsNotMine reconoce el 404 «ya no es mía»: se suelta el caso, sin reintentar.
	IsNotMine(err error) bool
	// OpenWithTemplate ABRE una conversación nueva con una plantilla al teléfono dado (el bot
	// inicia: recordatorios, lista de espera). Devuelve el id de la conversación en Néctar y el
	// del mensaje.
	OpenWithTemplate(ctx context.Context, phone string, tmpl TemplateConfig) (conversationID int64, messageID string, err error)
	// Contact dice a quién le habla el bot en la conversación; vacío si Néctar no lo publica.
	Contact(ctx context.Context, conversationID int64) (NectarContact, error)
	// WindowOpen dice si la ventana de 24 h de la conversación está abierta.
	WindowOpen(ctx context.Context, conversationID int64) (bool, error)
	// SendDocument sube el archivo y lo envía como adjunto de la conversación; devuelve el id del
	// mensaje. caption vacío = sin pie.
	SendDocument(ctx context.Context, conversationID int64, filename string, data []byte, caption string) (string, error)
}

// NectarLines es la interfaz OPCIONAL de un puente que reparte por línea (nectar.LineRouter): quien
// necesita saber si una conversación es de la línea vieja la pide con una aserción de tipo; un
// puente sin ella (un Bridge suelto) solo tiene la línea principal.
type NectarLines interface {
	IsLegacy(ctx context.Context, conversationID int64) bool
}

// NectarContact es el contacto de una conversación de Néctar (GET …/state, bloque `contact`).
// Se declara aquí y no en internal/nectar por la misma razón que Escalation: la interfaz que lo
// transporta vive en este paquete. Identifier es el E.164 sin «+» ("" si el número es oculto).
// Es PII: no se traza ni se persiste en crudo.
type NectarContact struct {
	Identifier string
	BSUID      string
}

// Escalation es TODO lo que viaja en el sobre del escalado, en un solo valor
// (protocolo de escalado v1 de Néctar: el cable lleva más campos que el dialecto de siempre y
// pasarlos sueltos por la firma de Escalate ya no escalaba).
//
// # Por qué vive en este paquete y no en internal/nectar
//
// El diseño la nombra `nectar.Escalation` y ahí está su ALIAS (`nectar.Escalation`, una línea),
// pero el tipo TIENE que declararse aquí: NectarBridge es una interfaz de este paquete y
// `internal/nectar` importa a `internal/bird`, nunca al revés (ver el comentario de cabecera).
// Un tipo declarado allá no se podría nombrar en esta firma.
//
// # Por qué una estructura y no cuatro parámetros más
//
// EscalateToAgentWithSummary ya lleva ocho parámetros posicionales y v1 le añadía cuatro más.
// Con la estructura, cada clave nueva del sobre (el descriptor `input`, el `rechazo` tipado) es
// un campo más y NINGUNA firma vuelve a cambiar.
//
// Los campos opacos (Facts, ReturnMenu) viajan como JSON ya serializado: este paquete no sabe
// —ni tiene por qué— qué forma tienen dentro.
type Escalation struct {
	// Protocol es la versión que el bot EMITE (config.ProtocolVersion). 0 = el dialecto
	// de hoy: la clave no viaja y el cuerpo sale byte a byte como siempre.
	Protocol int
	// Reason es el vocabulario CERRADO de Néctar (bot_cannot_answer, patient_asked_for_human).
	Reason string
	// ReasonDetail es el motivo INTERNO del bot (escalation_reason), opcional y con guarda de
	// forma en el emisor (§2.4): vacío no viaja, ni como cadena vacía.
	ReasonDetail string
	// Summary es el texto del resumen que lee el agente. No se retira nunca.
	Summary string
	// Facts es el MISMO resumen estructurado, JSON ya serializado y OPACO aquí. nil = no viaja.
	Facts []byte
	// ReturnMenu es el menú de retornos de hoy (`bot_return_menu`), opaco. Con la bandera en 1
	// va VACÍO: lo sustituye Input, y emitir las dos dejaría a Néctar con dos fuentes para la
	// misma pregunta.
	ReturnMenu []byte
	// Input es el descriptor de entrada de v1 (`input`), JSON ya serializado y OPACO aquí igual
	// que Facts: la forma la fija internal/statemachine (InputDescriptor). nil = no viaja, y
	// ausente es un dato — nunca una clave con nulo, que el esquema de Néctar rechaza.
	Input []byte
	// ResolveLock es el motivo del candado del botón «Resolver» ("" = sin candado). Viaja bajo
	// sus DOS nombres de cable mientras el alias siga vivo.
	ResolveLock string
	// SuggestedTeamID es el EQUIPO de Néctar que el bot sugiere para este caso, según el grupo
	// de CUPS del procedimiento (NECTAR_TEAM_BY_GROUP: imagenes / neuro / callcenter). Néctar
	// reparte por canal y no sabe qué equipo atiende qué estudio, así que el bot se lo sugiere.
	//
	// nil = no viaja, y el cuerpo es byte a byte el de siempre. Es el estado con la variable
	// vacía: sin equipos por grupo no hay nada que sugerir. NO lo gobierna la bandera del
	// protocolo — el campo existe en el esquema de Néctar desde antes de v1.
	SuggestedTeamID *int64
	// Rejection es el RECHAZO TIPADO de un dato que devolvió un agente (§2.5): la re-escalada
	// que hoy solo cuenta la prosa de `Nota:`. nil = esta escalación no nace de un dato
	// rechazado, y la clave no viaja — es el caso de nueve de los doce dorados.
	Rejection *Rejection
}

// Rejection es `rechazo` tal como lo declara el esquema de Néctar: los cinco datos que hoy
// viven SOLO dentro de la frase de la nota del resumen, separados.
//
// ⚠️ NI `Detail` NI `Echo` SE TRAZAN. El eco es lo que una persona escribió en la pantalla
// sobre un paciente —en ASK_DOCUMENT es su documento— y el motivo lo ecoa truncado; los dos van
// al sobre, que Néctar sella, y a ningún registro (§2.5, acta §2.7). Que el eco ya viajara
// dentro de `Nota:` no es excusa para trazarlo ahora que tiene clave propia.
type Rejection struct {
	// Detail es el motivo en prosa, OPACO y LITERAL: lo que el agente lee para corregirse.
	// Es el único obligatorio; sin él no se emite nada (el emisor lo comprueba).
	Detail string
	// Attempt es el número de intento, el MISMO que la prosa escribe como «(intento N)».
	Attempt int
	// Echo es el dato recibido tal cual, ya recortado a las 120 runas de truncateRejectedEcho:
	// el sobre no es sitio para un texto de 1000 caracteres.
	Echo string
	// Field y Piece son la clave y el código que el motivo nombra dentro de la frase, cuando el
	// bot los separa. Vacíos = no los separa, y eso es un dato.
	Field string
	Piece string
}

// ErrNoConversationState es «esta conversación no tiene estado que consultar»: Bird no publica
// ninguno, así que solo las de Néctar contestan. No es un fallo de transporte — es que no hay a
// quién preguntar—, pero quien pregunta se queda igual de a ciegas, y por eso también es error.
var ErrNoConversationState = errors.New("bird: el estado de la conversación solo lo publica Néctar")

// ErrNectarBridgeOff es «esta conversación es de Néctar y el puente está apagado» (vuelta a Bird:
// NECTAR_BASE_URL vacía). No hay por dónde contestarle y Bird no es su canal. Envuelve
// ErrPermanentSend: reintentar no cambia nada mientras el puente siga apagado.
var ErrNectarBridgeOff = fmt.Errorf("bird: conversación de Néctar con el puente apagado: %w", ErrPermanentSend)

// ErrDocumentNotSupported es «esta conversación no admite adjuntos del bot»: solo las de Néctar los
// envían; con un teléfono de Bird no hay por dónde. Envuelve ErrPermanentSend: reintentar no cambia nada.
var ErrDocumentNotSupported = fmt.Errorf("bird: el envío de documentos solo existe en Néctar: %w", ErrPermanentSend)

// SetNectar activa el desvío. nil lo desactiva (comportamiento previo al puente).
func (c *Client) SetNectar(b NectarBridge) { c.nectar = b }

// nectarPhonePrefix es el prefijo de nectar.SessionKey. Se repite aquí (y no se importa)
// porque el paquete nectar importa a este.
const nectarPhonePrefix = "nectar:"

// nectarConversation decide si una llamada va a Néctar y con qué id. La ÚNICA prueba es la
// clave de sesión "nectar:<id>": el conversationID solo sirve para avisar de un descuadre.
// Sin la clave, la llamada se queda en el camino de Bird, que es donde estaba antes del puente.
func (c *Client) nectarConversation(phone, conversationID string) (int64, bool) {
	if c == nil || c.nectar == nil {
		return 0, false
	}
	rest, ok := strings.CutPrefix(phone, nectarPhonePrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	// Descuadre: manda la clave de sesión (es la que identifica la conversación en Néctar),
	// pero que quede dicho — es cableado mal hecho, no un caso normal.
	if conversationID != "" && conversationID != rest {
		slog.Warn("nectar: la clave de sesión y el conversation_id no coinciden",
			"session_key", phone, "conversation_id", conversationID)
	}
	return id, true
}

// nectarKeyWithoutBridge dice si la clave es de una conversación de Néctar con el puente APAGADO
// (vuelta a Bird: NECTAR_BASE_URL vacía). Es el par de nectarConversation y, como ella, decide solo
// por la clave. Esa conversación no tiene canal: Bird no la conoce (su «teléfono» es nectar:<id>, y
// el camino de Bird lo trataría como username: upsert de contacto, crear y asignar conversación) y
// Néctar no está. Cada método con rama Néctar la consulta ANTES de cualquier petición y devuelve
// ErrNectarBridgeOff (o su cero), sin tocar Bird.
func (c *Client) nectarKeyWithoutBridge(phone string) bool {
	return c != nil && c.nectar == nil && strings.HasPrefix(phone, nectarPhonePrefix)
}

// HasAvailableAgentsFor es HasAvailableAgents con contexto de conversación: en Néctar la
// disponibilidad es por conversación (GET …/availability), no global. Un fallo de
// transporte o un 404 cuentan como «no hay» — la misma política que Bird (ListActiveAgents
// con error → false): nunca se crea una escalación destinada a no-show.
func (c *Client) HasAvailableAgentsFor(phone, conversationID string) bool {
	if c.nectarKeyWithoutBridge(phone) {
		return false
	}
	id, ok := c.nectarConversation(phone, conversationID)
	if !ok {
		return c.HasAvailableAgents()
	}
	available, reason, err := c.nectar.Available(context.Background(), id)
	if err != nil {
		slog.Warn("nectar: availability falló; se asume sin asesores", "conversation_id", id, "error", err)
		return false
	}
	if !available {
		slog.Info("nectar: no se puede escalar", "conversation_id", id, "reason", reason)
	}
	return available
}

// ConversationState pregunta en qué estado está la conversación. Solo Néctar lo publica
// (GET …/state); una conversación de Bird devuelve ErrNoConversationState, y quien pregunta trata
// ese error como cualquier otro: no sabe, así que no decide.
func (c *Client) ConversationState(ctx context.Context, phone, conversationID string) (string, error) {
	if c.nectarKeyWithoutBridge(phone) {
		return "", ErrNectarBridgeOff
	}
	id, ok := c.nectarConversation(phone, conversationID)
	if !ok {
		return "", ErrNoConversationState
	}
	return c.nectar.ConversationState(ctx, id)
}

// NectarWindowOpen dice si la ventana de 24 h de una conversación de Néctar está abierta, preguntándole
// a Néctar (GET …/state). ok=false: la clave no es de Néctar y quien pregunta decide como siempre. Un
// error cuenta como CERRADA: un texto libre fuera de ventana no llega, así que ante la duda no se manda.
func (c *Client) NectarWindowOpen(ctx context.Context, phone string) (open, ok bool) {
	id, ok := c.nectarConversation(phone, "")
	if !ok {
		return false, false
	}
	open, err := c.nectar.WindowOpen(ctx, id)
	if err != nil {
		slog.Warn("nectar: no se pudo leer la ventana; se asume cerrada", "conversation_id", id, "error", err)
		return false, true
	}
	return open, true
}

// NectarContact pregunta a Néctar a quién le habla el bot en la conversación (GET …/state, bloque
// `contact`). ok=false: la clave no es de Néctar (o el puente está apagado) y no se pregunta nada.
func (c *Client) NectarContact(ctx context.Context, phone string) (contact NectarContact, ok bool, err error) {
	id, ok := c.nectarConversation(phone, "")
	if !ok {
		return NectarContact{}, false, nil
	}
	contact, err = c.nectar.Contact(ctx, id)
	return contact, true, err
}

// EscalateToAgentWithSummary escala y entrega el resumen del caso al agente. En Bird son
// dos pasos (EscalateToAgent + nota interna, en ese orden, como siempre); en Néctar es UNO:
// el resumen viaja en el cuerpo del escalate porque después de escalar el bot ya no alcanza
// la conversación. esc.Reason es del vocabulario de Néctar; Bird lo ignora.
//
// esc.ResolveLock tampoco existe en Bird y por la vía pura se DESCARTA a propósito: el
// candado es una guarda del servidor de Néctar sobre su botón «Resolver», y el Inbox de Bird
// no tiene dónde aplicarlo. Descartarlo no pierde nada que Bird supiera hacer; lo que se
// pierde es la guarda, y por eso el paso que la necesite solo se enciende sobre el puente.
func (c *Client) EscalateToAgentWithSummary(ctx context.Context, conversationID, phone, teamID, teamName,
	patientName, fallbackTeamID string, esc Escalation,
) (agentID, agentName string, err error) {
	if c.nectarKeyWithoutBridge(phone) {
		return "", "", ErrNectarBridgeOff
	}
	if id, ok := c.nectarConversation(phone, conversationID); ok {
		return "", "", c.nectar.Escalate(ctx, id, esc)
	}
	agentID, agentName, err = c.EscalateToAgent(ctx, conversationID, phone, teamID, teamName, patientName, fallbackTeamID)
	if err != nil {
		return "", "", err
	}
	// La nota interna es de cortesía para el agente: si falla, la asignación ya quedó hecha.
	// Con el id vacío, EscalateToAgent resolvió la conversación por su cuenta; la nota va a la
	// que dejó la caché (el mismo rescate que hacía el handler antes de mandarla).
	noteConversationID := conversationID
	if noteConversationID == "" {
		noteConversationID = c.GetCachedConversationID(phone)
	}
	_, _ = c.SendInternalText(phone, noteConversationID, esc.Summary)
	return agentID, agentName, nil
}
