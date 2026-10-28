package nectar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// ServiceKeyHeader es la cabecera con la que el bot presenta su clave de servicio. Cabecera
// propia y no Authorization: así lo define Néctar (bot.go, botServiceKeyHeader).
const ServiceKeyHeader = "X-Bot-Service-Key"

// Los errores que el bot tiene que DISTINGUIR (§3, §8.4, §8.5). Todo lo demás es fallo de
// transporte y se reporta tal cual.
var (
	// ErrNotMine es el 404 de cualquiera de las siete rutas: «esta conversación ya no está en
	// tus manos» (con un agente, cerrada, inexistente, o un adjunto aún no listo —
	// indistinguibles a propósito). NO SE REINTENTA: se suelta el caso.
	ErrNotMine = fmt.Errorf("nectar: la conversación ya no es del bot (404): %w", bird.ErrPermanentSend)
	// ErrWindowClosed es el 409 reason=window_closed del envío: la ventana de 24 h está
	// cerrada; texto/botones/lista no pueden salir, solo una plantilla. Reintentar NO.
	ErrWindowClosed = fmt.Errorf("nectar: ventana de 24 h cerrada; use una plantilla (409): %w", bird.ErrPermanentSend)
	// ErrIdempotencyReused es el 409 reason=idempotency_key_reused: defecto de acuñado en el
	// cliente, no se reintenta ni con otra clave hasta mirar qué pasó.
	ErrIdempotencyReused = fmt.Errorf("nectar: idempotency_key reutilizada para otra clase de mensaje (409): %w", bird.ErrPermanentSend)
	// ErrUnauthorized (401) y ErrNotConfiguredAPI son de CONFIGURACIÓN: reintentar con las mismas
	// credenciales falla igual, así que también son permanentes para el reintento del envío.
	ErrUnauthorized     = fmt.Errorf("nectar: credenciales rechazadas (401): %w", bird.ErrPermanentSend)
	ErrNotConfiguredAPI = fmt.Errorf("nectar: cliente sin URL base o sin credenciales: %w", bird.ErrPermanentSend)
)

// Client habla con las siete rutas /api/bot/* de Néctar. Cada petición lleva las DOS
// credenciales: la clave de servicio (quién llama) y la firma X-Nectar-* con el secreto de
// ida (que la petición no se tocó ni se repite). Se firma AL ENVIAR (ventana de 5 min).
type Client struct {
	baseURL       string
	serviceKey    string
	requestSecret string
	http          *http.Client
	now           func() time.Time
}

// NewClient arma el cliente de las rutas /api/bot/* de Néctar. baseURL es la raíz pública (se le
// quita la barra final), serviceKey la clave de servicio y requestSecret el secreto de ida.
func NewClient(baseURL, serviceKey, requestSecret string) *Client {
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		serviceKey:    serviceKey,
		requestSecret: requestSecret,
		http:          &http.Client{Timeout: 15 * time.Second},
		now:           time.Now,
	}
}

// Configured dice si hay con qué llamar. Un cliente vacío falla cerrado en cada llamada.
func (c *Client) Configured() bool {
	return c != nil && c.baseURL != "" && c.serviceKey != "" && c.requestSecret != ""
}

// BaseURL es la raíz pública de Néctar (para el recorte por anfitrión del descargador).
func (c *Client) BaseURL() string { return c.baseURL }

// --- Tipos de cable (§8.5, §8.6 y bot_reads.go de Néctar) ---

// State es la respuesta de GET …/state: el estado crudo de la conversación y su ventana de 24 h.
type State struct {
	State           string     `json:"state"`
	WindowOpen      bool       `json:"window_open"`
	WindowExpiresAt *time.Time `json:"window_expires_at"`
}

// Availability es la respuesta de GET …/availability: si se puede escalar y, si no, por qué.
type Availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// Resolved es la respuesta de POST …/resolve: la conversación y el canal elegido por Néctar.
type Resolved struct {
	ConversationID int64 `json:"conversation_id"`
	ChannelID      int64 `json:"channel_id"`
}

// ReplyButton es un botón de respuesta rápida: el id es lo que vuelve en interactive_reply_id.
type ReplyButton struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ListRow es una fila de una lista interactiva.
type ListRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// ListSection es una sección de una lista interactiva.
type ListSection struct {
	Title string    `json:"title"`
	Rows  []ListRow `json:"rows"`
}

// ListBody es el cuerpo de un envío de tipo list: el rótulo del botón y sus secciones.
type ListBody struct {
	Button   string        `json:"button"`
	Sections []ListSection `json:"sections"`
}

// TemplateBody es el cuerpo de un envío de tipo template: nombre e idioma de Meta y parámetros
// por nombre de marcador.
type TemplateBody struct {
	Name       string            `json:"name"`
	Language   string            `json:"language"`
	Parameters map[string]string `json:"parameters"`
}

// sendRequest es el cuerpo de POST …/messages. idempotency_key es OBLIGATORIA y UUID, y no
// se reutiliza entre clases de mensaje: se acuña una por envío.
type sendRequest struct {
	IdempotencyKey string        `json:"idempotency_key"`
	Type           string        `json:"type"`
	Text           string        `json:"text,omitempty"`
	Buttons        []ReplyButton `json:"buttons,omitempty"`
	List           *ListBody     `json:"list,omitempty"`
	Template       *TemplateBody `json:"template,omitempty"`
}

// Escalation es el sobre del escalado, entero y en un solo valor. Es un ALIAS de
// bird.Escalation y no un tipo propio: el diseño lo nombra `nectar.Escalation` (§1.2) pero el
// tipo tiene que declararse en internal/bird, que es donde vive la interfaz que lo transporta
// —este paquete importa a aquél, nunca al revés—. El alias deja que los dos nombres sean la
// MISMA cosa, en vez de dos estructuras que alguien tendría que mantener sincronizadas.
type Escalation = bird.Escalation

// reasonDetailShape es la guarda de forma de `reason_detail` (D6): minúsculas, guiones bajos y
// hasta 64 runas. La promesa del protocolo —«ningún motivo lleva texto del paciente ni del
// agente»— se sostiene con este `if` y no con una revisión manual de cada sitio que escribe un
// escalation_reason, porque el que rompe la promesa es el sitio nuevo que alguien añade sin leerla.
var reasonDetailShape = regexp.MustCompile(`^[a-z_]{1,64}$`)

// safeReasonDetail devuelve el detalle si CASA la guarda, y "" con un ERROR si no. "" no se
// emite (omitempty): ausente y vacío son dos datos distintos y el bot se comprometió a mandarlo
// ausente. El slog.Error sube a Telegram a propósito: un motivo con la forma equivocada es un
// defecto del bot, y el sitio donde se ve es el despliegue que lo estrena.
func safeReasonDetail(detail string) string {
	if detail == "" || reasonDetailShape.MatchString(detail) {
		return detail
	}
	slog.Error("nectar: reason_detail con forma inválida, NO se emite",
		"len", len([]rune(detail)), "forma", "^[a-z_]{1,64}$")
	return ""
}

type escalateRequest struct {
	// Protocol es la versión del protocolo EN EL CUERPO (§2.1 del acta: la firma cubre sello +
	// URL + cuerpo, y ninguna cabecera — un número que decide cómo se parsea un cuerpo no puede
	// viajar fuera de lo que la firma protege). Con la bandera en 0 vale 0 y NO viaja: el
	// cuerpo es entonces byte a byte el de siempre.
	Protocol        int    `json:"protocol,omitempty"`
	Reason          string `json:"reason"`
	Summary         string `json:"summary"`
	SuggestedTeamID *int64 `json:"suggested_team_id,omitempty"`
	// ReasonDetail es el motivo INTERNO (escalation_reason), opaco y NO enum: el desahogo del
	// vocabulario cerrado de `reason`. Néctar lo guarda, lo muestra y lo traza; NUNCA ramifica
	// por él y NUNCA lo mapea a la nota. Sale con la guarda de forma de safeReasonDetail.
	ReasonDetail string `json:"reason_detail,omitempty"`
	// Facts es el MISMO resumen, estructurado: lista ORDENADA de grupos, no un diccionario
	// (§2 del diseño; el dorado saca `Documento:` y `EPS:` dos veces y un mapa por clave se
	// comería una de cada par). Acompaña a `summary`, no lo sustituye. nil = no viaja.
	Facts json.RawMessage `json:"facts,omitempty"`
	// Input es el DESCRIPTOR DE ENTRADA de v1 (§3): qué le pinta Néctar al agente para que
	// devuelva la conversación —los botones, los campos y, si el paso lo repite, las piezas
	// entre las que reparte—. SUSTITUYE a `bot_return_menu`, cuyas tres formas deducía Néctar
	// en silencio; aquí la forma la dicta el bot. Opaco en el cable, como los hechos.
	Input json.RawMessage `json:"input,omitempty"`
	// BotReturnMenu es el menú de retornos, DATO OPACO para Néctar: lo sella y se lo pinta
	// al agente sin interpretarlo (§6.2 punto 1, enmienda 2026-08-24). Va con omitempty
	// porque un escalate SIN menú se comporta byte a byte como el de siempre. Si se manda,
	// tiene que ser JSON válido: uno malformado tumba la decodificación con 400.
	BotReturnMenu json.RawMessage `json:"bot_return_menu,omitempty"`
	// CandadoResolver le pide a Néctar que su servidor NO deje resolver el hilo a secas: el
	// agente se lleva una tarea (devolver la cita escrita) y resolverlo sin devolverla deja al
	// paciente esperando algo que nadie le va a mandar. El valor es el MOTIVO acordado
	// ("cita_pendiente"); lo lee su servidor, no una persona. Campo top-level y no una línea
	// del resumen porque su árbol NO parsea el resumen (§8, acordado 2026-09-06).
	//
	// Va con omitempty y es 100 % aditivo: sin candado el cuerpo es byte a byte el de hoy, y
	// una versión de Néctar que aún no lo conozca lo ignora.
	CandadoResolver string `json:"candado_resolver,omitempty"`
	// ResolveLock es el MISMO motivo bajo el nombre que adopta el protocolo v1 (protocolo de
	// escalado v1 de Néctar). Se emiten LAS DOS claves a propósito: si el
	// bot renombrara de golpe, la pérdida que el candado vino a tapar volvería viva hasta que
	// los dos lados desplegaran a la vez. Néctar lee `resolve_lock` cuando vienen las dos y
	// traza el alias; `candado_resolver` se retira cuando el bot deje de emitirlo —no hay fecha,
	// y el retiro es de UNA línea aquí—.
	//
	// Mismo omitempty y misma regla: sin candado no viaja NINGUNA de las dos.
	ResolveLock string `json:"resolve_lock,omitempty"`
	// Rechazo es el RECHAZO TIPADO (§2.5): los cinco datos de una re-escalada por un dato que
	// el agente escribió mal, hoy legibles solo dentro de la prosa de `Nota:`. Es la quinta
	// clave de v1 —la discrepancia de recuento que el propio esquema declara— y viaja SOLO en
	// esa re-escalada y SOLO con la bandera en 1: quien arma el sobre no lo rellena en ningún
	// otro camino, y con la bandera en 0 el cuerpo es byte a byte el de siempre.
	Rechazo *rechazo `json:"rechazo,omitempty"`
}

// rechazo es la forma DE CABLE del rechazo tipado. Las claves van en español, como el resto de
// lo que Néctar lee, y el tipo es propio de este paquete —no el de internal/bird— porque aquí
// es donde se aplican los topes del esquema, igual que safeReasonDetail.
type rechazo struct {
	Motivo  string `json:"motivo"`
	Intento int    `json:"intento,omitempty"`
	Eco     string `json:"eco,omitempty"`
	Campo   string `json:"campo,omitempty"`
	Pieza   string `json:"pieza,omitempty"`
}

// rejectionMaxRunes es el tope que `escalate.schema.json` le pone a `motivo` y a `eco`. Se
// aplica AQUÍ, en el emisor, por lo mismo que la guarda de reason_detail: es el único punto por
// el que pasan todos los caminos, y el que escribe el motivo número diecinueve no va a leer el
// acta. El eco llega ya recortado a 120 runas desde el worker, así que este tope solo muerde si
// alguien cambia aquel; el motivo puede acercarse más, porque ecoa lo que el agente escribió.
const rejectionMaxRunes = 512

// safeRejection traduce el rechazo del sobre a su forma de cable, con los topes aplicados.
//
// Devuelve nil cuando NO HAY MOTIVO: el esquema lo exige, y emitir un `rechazo` sin él tumbaría
// la decodificación del sobre ENTERO —el caso se quedaría sin llegar a nadie por un dato
// accesorio—. Sin él, lo que el agente lee sigue siendo la `Nota:` del resumen, que no ha
// cambiado ni un byte.
//
// ⚠️ NO TRAZA NADA, a diferencia de safeReasonDetail: lo que aquí se recortaría es justo lo que
// no puede entrar en un registro (§2.5).
func safeRejection(r *bird.Rejection) *rechazo {
	if r == nil || r.Detail == "" {
		return nil
	}
	out := &rechazo{
		Motivo: fitRejectionRunes(r.Detail),
		Eco:    fitRejectionRunes(r.Echo),
		Campo:  r.Field,
		Pieza:  r.Piece,
	}
	// `intento` tiene `minimum: 1` en el esquema, y el emisor lo respeta igual que los dos topes
	// de largo: un 0 —o un negativo— no es «no lo sé», es un valor que su decodificador rechaza,
	// y con él se caería el sobre ENTERO. Fuera de rango va AUSENTE, que sí es un dato legal (la
	// clave es opcional). El contador real empieza en 1, así que esto solo muerde si alguien
	// escribe la clave persistida a mano o desde una prueba.
	if r.Attempt >= 1 {
		out.Intento = r.Attempt
	}
	return out
}

func fitRejectionRunes(s string) string {
	r := []rune(s)
	if len(r) <= rejectionMaxRunes {
		return s
	}
	return string(r[:rejectionMaxRunes])
}

// EscalateResult es la respuesta de POST …/escalate.
type EscalateResult struct {
	State        string `json:"state"`
	Transitioned bool   `json:"transitioned"`
}

// --- Las siete rutas ---

// GetState llama a GET /api/bot/conversations/{id}/state.
func (c *Client) GetState(ctx context.Context, conversationID int64) (State, error) {
	var out State
	err := c.do(ctx, http.MethodGet, c.convPath(conversationID, "state"), nil, &out)
	return out, err
}

// GetAvailability llama a GET /api/bot/conversations/{id}/availability. «No se puede escalar» es
// una respuesta NORMAL (Available=false con su Reason), no un error (§6.2.3).
func (c *Client) GetAvailability(ctx context.Context, conversationID int64) (Availability, error) {
	var out Availability
	err := c.do(ctx, http.MethodGet, c.convPath(conversationID, "availability"), nil, &out)
	return out, err
}

// Resolve llama a POST /api/bot/conversations/resolve — de un teléfono a una conversación. Busca,
// no crea (§8.5, P12-15). channelID 0 = que Néctar elija el hilo por el que el paciente
// habla ahora; el canal elegido vuelve siempre en la respuesta.
func (c *Client) Resolve(ctx context.Context, phone string, channelID int64) (Resolved, error) {
	body := map[string]any{"phone": phone}
	if channelID != 0 {
		body["channel_id"] = channelID
	}
	var out Resolved
	err := c.do(ctx, http.MethodPost, "/api/bot/conversations/resolve", body, &out)
	return out, err
}

// SendText / SendButtons / SendList / SendTemplate: POST /api/bot/conversations/{id}/messages.
// Devuelven el id del mensaje en Néctar.
func (c *Client) SendText(ctx context.Context, conversationID int64, text string) (int64, error) {
	return c.send(ctx, conversationID, sendRequest{Type: "text", Text: text})
}

// SendButtons envía botones de respuesta rápida.
func (c *Client) SendButtons(ctx context.Context, conversationID int64, text string, buttons []ReplyButton) (int64, error) {
	return c.send(ctx, conversationID, sendRequest{Type: "buttons", Text: text, Buttons: buttons})
}

// SendList envía una lista interactiva.
func (c *Client) SendList(ctx context.Context, conversationID int64, text, button string, sections []ListSection) (int64, error) {
	return c.send(ctx, conversationID, sendRequest{Type: "list", Text: text, List: &ListBody{Button: button, Sections: sections}})
}

// SendTemplate envía una plantilla de Meta (la única clase de mensaje con la ventana cerrada).
func (c *Client) SendTemplate(ctx context.Context, conversationID int64, tmpl TemplateBody) (int64, error) {
	return c.send(ctx, conversationID, sendRequest{Type: "template", Template: &tmpl})
}

func (c *Client) send(ctx context.Context, conversationID int64, req sendRequest) (int64, error) {
	req.IdempotencyKey = uuid.NewString()
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, c.convPath(conversationID, "messages"), req, &out)
	return out.ID, err
}

// Close llama a POST /api/bot/conversations/{id}/close — cierre por inactividad del bot.
func (c *Client) Close(ctx context.Context, conversationID int64) error {
	return c.do(ctx, http.MethodPost, c.convPath(conversationID, "close"), nil, nil)
}

// Escalate llama a POST /api/bot/conversations/{id}/escalate. reason es del vocabulario cerrado de
// Néctar (patient_asked_for_human, bot_cannot_answer, sensitive_topic; lo demás se colapsa
// a other); summary es lo que el agente ve al abrir el hilo — es el único sitio donde cabe,
// porque después del escalate la conversación ya no es del bot y todo da 404.
// esc.ResolveLock es el motivo del candado del botón «Resolver» ("" = sin candado, cuerpo
// idéntico al de siempre); viaja bajo las DOS claves del alias (ver escalateRequest).
//
// CON LA BANDERA EN 0 EL CUERPO ES BYTE A BYTE EL DE HOY: `protocol` vale 0 y no viaja, y
// quien arma el sobre (handlers/escalation.go) no rellena ni ReasonDetail ni Facts. La guarda
// de forma se aplica aquí igual, que es el único punto por el que pasan todos los emisores.
func (c *Client) Escalate(ctx context.Context, conversationID int64, esc Escalation,
	suggestedTeamID *int64,
) (EscalateResult, error) {
	var out EscalateResult
	err := c.do(ctx, http.MethodPost, c.convPath(conversationID, "escalate"),
		escalateBody(esc, suggestedTeamID), &out)
	return out, err
}

// EncodeEscalateBody serializa el cuerpo del escalado SIN mandarlo. Es exactamente lo que
// Escalate pone en el cable —la misma estructura, las mismas guardas, el mismo orden de
// claves—, y existe para los VOLCADOS de referencia que se le entregan a Néctar (§6.2 del
// diseño): un volcado escrito a mano sería un ejemplo, no una prueba de lo que viaja.
func EncodeEscalateBody(esc Escalation, suggestedTeamID *int64) ([]byte, error) {
	return json.Marshal(escalateBody(esc, suggestedTeamID))
}

// escalateBody arma el cuerpo. Lo comparten el envío real y el volcado a propósito: dos
// constructores separados se desincronizan, y el volcado dejaría de demostrar nada.
func escalateBody(esc Escalation, suggestedTeamID *int64) escalateRequest {
	return escalateRequest{
		Protocol:        esc.Protocol,
		Reason:          esc.Reason,
		Summary:         esc.Summary,
		SuggestedTeamID: suggestedTeamID,
		ReasonDetail:    safeReasonDetail(esc.ReasonDetail),
		Facts:           json.RawMessage(esc.Facts),
		Input:           json.RawMessage(esc.Input),
		BotReturnMenu:   json.RawMessage(esc.ReturnMenu),
		CandadoResolver: esc.ResolveLock,
		ResolveLock:     esc.ResolveLock,
		Rechazo:         safeRejection(esc.Rejection),
	}
}

// AttachmentURL es la URL absoluta de GET /api/bot/attachments/{id}.
func (c *Client) AttachmentURL(id string) string { return c.baseURL + "/api/bot/attachments/" + id }

// --- transporte ---

func (c *Client) convPath(id int64, leaf string) string {
	return "/api/bot/conversations/" + strconv.FormatInt(id, 10) + "/" + leaf
}

// do arma, firma y ejecuta una petición, y traduce los códigos del contrato a errores.
// No reintenta NADA: el 404 significa «suelta el caso»; los 409 son instrucciones; y los
// fallos de transporte los decide quien llama.
func (c *Client) do(ctx context.Context, method, path string, in any, out any) error {
	if !c.Configured() {
		return ErrNotConfiguredAPI
	}
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("nectar: serializar %s: %w", path, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(ServiceKeyHeader, c.serviceKey)
	// La firma NO cubre cabeceras (sello + URL completa + cuerpo).
	SignRequest(req, c.requestSecret, c.now(), body)

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nectar: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	slog.Debug("nectar: llamada", "method", method, "path", path, "status", resp.StatusCode,
		"ms", time.Since(started).Milliseconds())

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotMine
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusConflict:
		var e struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &e)
		switch e.Reason {
		case "window_closed":
			return ErrWindowClosed
		case "idempotency_key_reused":
			return ErrIdempotencyReused
		}
		return fmt.Errorf("nectar: %s %s: 409 %s", method, path, strings.TrimSpace(string(raw)))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("nectar: %s %s: status %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("nectar: leer respuesta de %s: %w", path, err)
		}
	}
	return nil
}

// ConversationIDFromSessionKey recupera el id de conversación de una clave de sesión
// "nectar:<id>". ok=false si la clave no es de Néctar.
func ConversationIDFromSessionKey(key string) (int64, bool) {
	rest, ok := strings.CutPrefix(key, "nectar:")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
