package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

// nectarInboxType es el msg_type con el que un aviso de Néctar se guarda en message_inbox.
// Existe porque los DOS replays del WAL (arranque y atascados, cmd/server) parsean raw_body como
// bird.WebhookEvent, y un cuerpo de Néctar ahí dentro deserializa EN CERO Y SIN ERROR — un mensaje
// sin teléfono encolado en silencio. Con esta marca el replay lo enruta por nectar.ParseNotice.
// Cabe en VARCHAR(20).
const nectarInboxType = "nectar_notice"

// nectarReplyToAgent es la respuesta del cuarto aviso: la conversación se queda con el agente.
// En Neuro es la ÚNICA respuesta posible: el bot nunca preabre una sesión a partir de una
// plantilla que abrió un agente, así que no hay decisión que tomar ni opener que consultar.
const nectarReplyToAgent = "agent"

// nectarDispatcher es lo que el manejador necesita del pool. Interfaz estrecha para poder
// contar en pruebas qué llegó a la cola sin arrancar workers.
type nectarDispatcher interface {
	Enqueue(msg bird.InboundMessage) bool
	EnqueueAgentCommand(cmd worker.AgentCommand)
}

// nectarNotifier es la parte del NotificationManager que clasifica un mensaje entrante como
// respuesta a una notificación proactiva (la misma que usa el camino de Bird). Interfaz estrecha
// para poder probar el enrutamiento con un fake; solo la usa el camino de Néctar.
type nectarNotifier interface {
	HasPending(phone string) bool
	HandleResponse(phone, payload, conversationID string)
	HandleInvalidInput(phone, conversationID string) bool
}

// nectarSeen es la memoria local de event_id vistos: segunda línea detrás del WAL (y la
// única cuando no hay BD). Un aviso reintentado por Néctar es byte a byte idéntico, con
// el mismo event_id; sin esto, el bot le contestaría dos veces al paciente.
type nectarSeen struct {
	mu   sync.Mutex
	ids  map[string]time.Time
	last time.Time
}

const nectarSeenTTL = 24 * time.Hour // la ventana de servicio: después, Néctar ya no reintenta

// claim registra el event_id y dice si es la primera vez que se ve.
func (s *nectarSeen) claim(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = map[string]time.Time{}
	}
	if now.Sub(s.last) > time.Hour {
		for k, at := range s.ids {
			if now.Sub(at) > nectarSeenTTL {
				delete(s.ids, k)
			}
		}
		s.last = now
	}
	if _, dup := s.ids[id]; dup {
		return false
	}
	s.ids[id] = now
	return true
}

// nectarContactTTL es cuánto se recuerda el contacto de una conversación: la ventana de servicio.
// Pasado ese tiempo se vuelve a preguntar (el contacto de una conversación no cambia, pero la
// caché no debe crecer sin fin).
const nectarContactTTL = 24 * time.Hour

// nectarContactTimeout acota la pregunta por el contacto: corre antes de encolar el mensaje, y
// sin contacto el mensaje sigue igual, así que no vale la pena esperar más.
const nectarContactTimeout = 5 * time.Second

// nectarContactCache recuerda a quién le habla el bot en cada conversación de Néctar
// (conversación → contacto), para preguntarlo UNA vez y no en cada mensaje. Solo en memoria: no
// va al WAL, y el replay tras una caída lo vuelve a pedir. Un fallo no se guarda: el siguiente
// mensaje vuelve a preguntar.
type nectarContactCache struct {
	entries   sync.Map // int64 → nectarContactEntry
	mu        sync.Mutex
	lastSweep time.Time
}

type nectarContactEntry struct {
	contact bird.NectarContact
	at      time.Time
}

func (c *nectarContactCache) get(conversationID int64, now time.Time) (bird.NectarContact, bool) {
	v, ok := c.entries.Load(conversationID)
	if !ok {
		return bird.NectarContact{}, false
	}
	e := v.(nectarContactEntry)
	if now.Sub(e.at) > nectarContactTTL {
		c.entries.Delete(conversationID)
		return bird.NectarContact{}, false
	}
	return e.contact, true
}

func (c *nectarContactCache) put(conversationID int64, contact bird.NectarContact, now time.Time) {
	c.entries.Store(conversationID, nectarContactEntry{contact: contact, at: now})
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.lastSweep) <= time.Hour {
		return
	}
	c.lastSweep = now
	c.entries.Range(func(k, v any) bool {
		if now.Sub(v.(nectarContactEntry).at) > nectarContactTTL {
			c.entries.Delete(k)
		}
		return true
	})
}

// nectarContact devuelve el contacto de la conversación: de la caché, o preguntándole a Néctar
// (GET …/state). Un fallo deja el contacto vacío —el mensaje sigue igual— y queda en la traza.
func (h *WebhookHandler) nectarContact(ctx context.Context, conversationID int64) bird.NectarContact {
	now := time.Now()
	if c, ok := h.nectarContacts.get(conversationID, now); ok {
		return c
	}
	key := nectar.SessionKey(conversationID)
	ctx, cancel := context.WithTimeout(ctx, nectarContactTimeout)
	defer cancel()
	c, ok, err := h.birdClient.NectarContact(ctx, key)
	if !ok {
		return bird.NectarContact{} // puente apagado: no hay a quién preguntar
	}
	if err != nil {
		slog.Warn("nectar: contacto no disponible; el mensaje sigue sin él",
			"conversation_id", conversationID, "error", err)
		observability.Emit(observability.TraceSession(key), "nectar", "contacto_no_disponible", observability.EmitOpts{})
		return bird.NectarContact{}
	}
	h.nectarContacts.put(conversationID, c, now)
	return c
}

// nectarContactWhitelisted aplica TESTING_WHITELIST_PHONES al contacto de Néctar. La lista se
// escribe como se quiera («+57…» o «57…»): el contacto (E.164 sin «+») se compara tal cual, con «+»
// y en la forma de utils.ParseColombianPhone. Un contacto sin número (oculto, solo BSUID, o que no
// se pudo leer) PASA: lo que aísla al bot de pruebas es su propio workspace de Néctar (spec §7.2.6).
func (h *WebhookHandler) nectarContactWhitelisted(c bird.NectarContact) bool {
	if c.Identifier == "" {
		return true
	}
	return h.cfg.IsPhoneWhitelisted(c.Identifier) ||
		h.cfg.IsPhoneWhitelisted("+"+c.Identifier) ||
		h.cfg.IsPhoneWhitelisted(utils.ParseColombianPhone(c.Identifier))
}

// nectarContactIdentifier es lo que viaja en InboundMessage.ContactIdentifier: el número si
// Néctar lo publica, si no el BSUID.
func nectarContactIdentifier(c bird.NectarContact) string {
	if c.Identifier != "" {
		return c.Identifier
	}
	return c.BSUID
}

// SetNectarSink sustituye el destino de los avisos (por defecto, el pool de workers). Existe
// para que las pruebas cuenten lo que llegó a la cola sin arrancar workers.
func (h *WebhookHandler) SetNectarSink(sink nectarDispatcher) {
	h.nectarSink = sink
}

// SetNectarNotifier sustituye el NotificationManager del camino de Néctar (por defecto,
// notifyManager). Existe para las pruebas.
func (h *WebhookHandler) SetNectarNotifier(n nectarNotifier) {
	h.nectarNotify = n
}

// nectarNotifierOrNil devuelve el clasificador de notificaciones del camino de Néctar, o nil si el
// bot arrancó sin NotificationManager. Sin esto, un *NotificationManager nil metido en la interfaz
// no sería nil.
func (h *WebhookHandler) nectarNotifierOrNil() nectarNotifier {
	if h.nectarNotify != nil {
		return h.nectarNotify
	}
	if h.notifyManager != nil {
		return h.notifyManager
	}
	return nil
}

// HandleNectarNotice atiende POST /api/webhooks/nectar: los cuatro avisos de Néctar
// (PARA-EL-EQUIPO-DEL-BOT.md §1, contrato §8.6).
//
// Orden, y por qué: (1) verificar la firma ANTES de leer nada del cuerpo; (2) persistir con
// dedupe por event_id ANTES de contestar 2xx — si el bot cae después, el replay del WAL lo
// recupera; si Néctar reintenta, el duplicado se reconoce y no se despacha; (3) 200;
// (4) despachar. Cualquier no-2xx lo reintenta Néctar, así que solo se contesta 5xx cuando
// reintentar tiene sentido (BD caída) y 4xx cuando el aviso es inutilizable de forma
// permanente.
func (h *WebhookHandler) HandleNectarNotice(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	requestURL := reconstructFullURL(r)
	if verr := nectar.VerifyNotice(h.cfg.NectarNoticeSecret,
		r.Header.Get(nectar.SignatureHeader), r.Header.Get(nectar.TimestampHeader),
		requestURL, body); verr != nil {
		sum := sha256.Sum256(body)
		//nolint:gosec // G706: la URL sale del Host/X-Forwarded-* de una conexión HTTP que Go ya valida y slog cita los valores; es justo el dato que hay que cotejar con Néctar.
		slog.Warn("nectar: aviso rechazado", "reason", verr.Error(), "url", requestURL,
			"body_len", len(body), "body_sha256", hex.EncodeToString(sum[:])[:16])
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	n, err := nectar.ParseNotice(body)
	if err != nil {
		// Firmado por Néctar pero inutilizable (sin event_id, sin conversación, JSON roto):
		// es un defecto de contrato, no de transporte. 400 en vez de 200 a propósito: el
		// reintento de Néctar acaba en alerta a su supervisor, que es quien puede arreglarlo.
		slog.Error("nectar: aviso inutilizable", "error", err, "body_len", len(body))
		http.Error(w, "bad notice", http.StatusBadRequest)
		return
	}

	// DESACUERDO DE VERSIÓN, dirección Néctar → bot: ALERTA Y 200, NUNCA 400. El aviso se procesa
	// igual —el lector es tolerante y lo que no conoce lo ignora—; lo que no se puede hacer es
	// rechazarlo, porque el buzón de Néctar reintenta con espera exponencial SIN CORTE y un 400
	// permanente reintentaría para siempre. El 400 sigue reservado a lo inutilizable de forma
	// permanente (arriba).
	if n.Protocol > nectar.ProtocolMax {
		slog.Error("nectar: aviso con una versión de protocolo desconocida; se procesa igual",
			"protocol", n.Protocol, "soportada", nectar.ProtocolMax,
			"kind", n.Kind, "event_id", n.EventID)
		observability.Emit(observability.TraceSession(nectar.SessionKey(n.ConversationID)),
			"protocolo", "version_desconocida", observability.EmitOpts{
				Attrs: map[string]interface{}{"protocol": n.Protocol, "soportada": nectar.ProtocolMax},
			})
	}

	// EL CUARTO AVISO se resuelve de forma SÍNCRONA, a diferencia de los otros tres —que se
	// persisten, se acusan con un 200 VACÍO y se despachan aparte (dispatchNectar), sea por el
	// worker o por el replay del WAL—. Néctar necesita la decisión EN LA RESPUESTA de este mismo
	// request para saber con quién queda la conversación; por eso NUNCA pasa por dispatchNectar
	// ni por el sink.
	if n.Kind == nectar.KindConversationOpenedByAgent {
		h.handleOpenedByAgent(context.WithoutCancel(r.Context()), w, n, time.Now())
		return
	}

	key := nectar.SessionKey(n.ConversationID)
	now := time.Now()
	if h.inboxRepo != nil {
		inserted, ierr := h.inboxRepo.InsertIfNotExists(r.Context(), n.EventID, key, string(body), nectarInboxType, now)
		if ierr != nil {
			slog.Error("nectar: inbox persist failed", "event_id", n.EventID, "error", ierr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !inserted {
			slog.Info("nectar: aviso duplicado (reintento), ya procesado", "event_id", n.EventID, "kind", n.Kind)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	if !h.nectarSeenIDs.claim(n.EventID, now) {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusOK)
	// El acuse sale YA: el despacho de un mensaje puede preguntar el contacto a Néctar (GET …/state)
	// y Néctar no tiene por qué esperar esa vuelta para dar el aviso por entregado.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// La whitelist de teléfonos no va aquí sino en dispatchNectar: el aviso no trae el número
	// (ámbito mínimo de Néctar), hay que preguntarlo, y así el replay del WAL la aplica también.
	if n.Kind == nectar.KindInboundMessage && h.birdClient != nil {
		h.birdClient.RecordInbound(key) // actividad real del paciente: resetea su cuota de salida
	}
	h.dispatchNectar(context.WithoutCancel(r.Context()), n)
}

// handleOpenedByAgent resuelve EL CUARTO AVISO (kind=conversacion_abierta_por_agente): dedup
// idempotente, WAL con el cuerpo ENMASCARADO (es el único de los cuatro avisos que trae PII del
// paciente en el sobre) y la respuesta con cuerpo que Néctar necesita para enrutar.
//
// En Neuro la respuesta es SIEMPRE "agent": la conversación se queda con quien la abrió. No se
// preabre ninguna sesión, así que el aviso solo deja rastro: el WAL (cerrado en el acto) y la
// traza de que Néctar preguntó.
func (h *WebhookHandler) handleOpenedByAgent(ctx context.Context, w http.ResponseWriter, n nectar.Notice, now time.Time) {
	// Un reintento (respuesta perdida, timeout de Néctar) recibe la misma respuesta sin repetir
	// el trabajo: es idempotente porque la respuesta no depende de nada.
	if !h.nectarSeenIDs.claim(n.EventID, now) {
		writeNectarReplyToAgent(w)
		return
	}

	// El cuerpo persistido en el WAL va ENMASCARADO: este es el ÚNICO aviso que trae PII del
	// paciente en el sobre (identificador de contacto, variables de la plantilla), y
	// message_inbox.raw_body no es sitio para guardarla en claro (Ley 1581,
	// Notice.MaskedForInbox). Un fallo enmascarando o persistiendo no bloquea la respuesta: el
	// WAL es para recuperación ante caída, no para decidir.
	if h.inboxRepo != nil {
		if masked, merr := n.MaskedForInbox(); merr != nil {
			slog.Error("nectar: no se pudo enmascarar el cuarto aviso para el WAL",
				"event_id", n.EventID, "error", merr)
		} else if _, ierr := h.inboxRepo.InsertIfNotExists(ctx, n.EventID, nectar.SessionKey(n.ConversationID),
			string(masked), nectarInboxType, now); ierr != nil {
			slog.Error("nectar: inbox persist failed (cuarto aviso)", "event_id", n.EventID, "error", ierr)
		}
	}

	// La traza cuelga de la conversación: no hay sesión que la ancle, porque no se preabre ninguna.
	traceKey := observability.TraceSession(nectar.SessionKey(n.ConversationID))
	if !n.ValidOpening() {
		// `opened` ausente o sin template_name: no es una apertura legible. Se responde igual
		// (agent) y se traza como dato de contrato.
		observability.Emit(traceKey, "apertura", "aviso_invalido", observability.EmitOpts{})
	} else {
		// Solo la plantilla: el nombre del agente es dato de una persona y no entra al vocabulario
		// de attrs de la traza (§10.5); quien quiera saber quién abrió tiene la conversación.
		observability.Emit(traceKey, "apertura", "solo_traza", observability.EmitOpts{
			Attrs: map[string]interface{}{"plantilla": n.Opened.TemplateName},
		})
	}

	if h.inboxRepo != nil {
		if err := h.inboxRepo.MarkDone(ctx, n.EventID); err != nil {
			slog.Warn("nectar: mark done failed (cuarto aviso)", "event_id", n.EventID, "error", err)
		}
	}
	writeNectarReplyToAgent(w)
}

// writeNectarReplyToAgent responde el cuerpo que EL CUARTO AVISO necesita para que Néctar enrute:
// {"respuesta_va_a": "agent"}.
func writeNectarReplyToAgent(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"respuesta_va_a": nectarReplyToAgent})
}

// dispatchNectar enruta un aviso ya verificado y deduplicado. Lo comparten el manejador
// HTTP y el replay del WAL (DispatchNectarNotice).
func (h *WebhookHandler) dispatchNectar(ctx context.Context, n nectar.Notice) {
	var sink nectarDispatcher = h.workerPool
	if h.nectarSink != nil {
		sink = h.nectarSink
	}
	key := nectar.SessionKey(n.ConversationID)
	slog.Info("nectar: aviso", "kind", n.Kind, "event_id", n.EventID, "conversation_id", n.ConversationID)

	switch n.Kind {
	case nectar.KindInboundMessage:
		// La raíz de Néctar viaja para componer la URL del adjunto cuando el aviso trae solo
		// su `id` (§5.3): sin ella, ese mensaje llegaba al paso de la orden como una imagen
		// sin imagen.
		msg := n.ToInboundMessage(h.cfg.NectarBaseURL)
		// El contacto (D2) y la whitelist van ANTES de enrutar: lo descartado no llega ni al
		// NotificationManager ni al worker, como en Bird (webhook_handler.go, HandleWhatsApp).
		contact := h.nectarContact(ctx, n.ConversationID)
		if !h.nectarContactWhitelisted(contact) {
			slog.Debug("phone not whitelisted, ignoring", "phone", utils.MaskPhone(contact.Identifier))
			break // la fila del WAL se cierra abajo: el replay no debe volver a despacharlo
		}
		msg.ContactIdentifier = nectarContactIdentifier(contact)
		if h.routeNectarInbound(msg) {
			// Lo consumió el NotificationManager: el worker no va a cerrar la fila, se cierra
			// aquí (como el replay de Bird) o el replay de arranque la volvería a despachar.
			break
		}
		// El worker marca la fila del WAL como done al terminar (msg.ID = event_id).
		sink.Enqueue(msg)
		return
	case nectar.KindConversationResolved:
		// El caso se cerró fuera del bot: soltar el estado de sesión (Complete). El motivo
		// (p. ej. cola_vencida) viaja para que el worker lo deje en la traza; sin motivo viaja el
		// centinela CloseReasonResolved, para que el worker no se despida ni cierre una
		// conversación que ya no es suya (el «cerrar» del menú de retornos va por otro camino).
		reason := n.Motivo
		if reason == "" {
			reason = worker.CloseReasonResolved
		} else {
			slog.Info("nectar: resuelta con motivo", "conversation_id", n.ConversationID, "motivo", n.Motivo)
		}
		sink.EnqueueAgentCommand(worker.AgentCommand{Action: "close", Phone: key, Reason: reason})
	case nectar.KindConversationReturned:
		// Vuelve a ser del bot. Si el agente eligió una salida de NUESTRO menú, viaja aquí y
		// el worker ramifica por ella; ausente = devolución sin decisión, se retoma en
		// pre_escalation_state como siempre.
		//
		// Y desde v1 la decisión puede venir en la clave `retorno` —el token APARTE de los datos
		// escritos—, que viaja igual de entera al worker. La recepción de las dos formas está
		// SIEMPRE encendida: no depende de PROTOCOL_ESCALADO_VERSION, que gobierna solo lo que
		// el bot EMITE.
		value, chosen := n.ChosenReturnValue()
		cmd := worker.AgentCommand{Action: "resume", Phone: key, ReturnValue: value}
		if ret, v1 := n.V1Return(); v1 {
			cmd.ReturnV1 = &ret
			// `valores` NO se traza: puede llevar el documento del paciente.
			slog.Info("nectar: devuelta con retorno v1", "conversation_id", n.ConversationID,
				"opcion", ret.Option, "con_valores", len(ret.Values) > 0)
		} else if chosen {
			// El token se traza por su CLASE, no por su contenido: en el dialecto puede ser
			// `bot_dato:<lo que tecleó el agente>` —en ASK_DOCUMENT, el documento del paciente—.
			// Misma regla y misma función que el worker.
			slog.Info("nectar: devuelta con decisión del agente", "conversation_id", n.ConversationID,
				"return_value", statemachine.ReturnValueForLog(value))
		}
		sink.EnqueueAgentCommand(cmd)
	case nectar.KindConversationOpenedByAgent:
		// EL CUARTO AVISO llegando por el REPLAY (arranque tras una caída, o el barredor de
		// filas atascadas). Aquí ya no hay nada que hacer: su valor entero era la RESPUESTA
		// SÍNCRONA —`{"respuesta_va_a": …}` en el mismo request—, y ese request murió con el
		// proceso. Néctar ya decidió por su cuenta (agent), así que se acusa (MarkDone, abajo)
		// y se ignora, con traza para poder contarlo.
		//
		// Case EXPLÍCITO y no el `default` de kind desconocido: este kind se conoce de sobra, y
		// dejarlo caer ahí lo confundiría con un aviso del futuro que Néctar aún no despliega.
		slog.Warn("nectar: apertura por plantilla en replay — la respuesta síncrona ya se perdió; "+
			"se acusa y se ignora", "event_id", n.EventID, "conversation_id", n.ConversationID)
		observability.Emit(observability.TraceSession(key), "apertura", "replay_perdida", observability.EmitOpts{})
	default:
		slog.Warn("nectar: kind desconocido, acusado e ignorado", "kind", n.Kind, "event_id", n.EventID)
	}
	// Los avisos sin mensaje no pasan por processMessage, que es quien cierra filas del WAL:
	// se cierran aquí o el replay de arranque los volvería a despachar en cada reinicio.
	if h.inboxRepo != nil {
		if err := h.inboxRepo.MarkDone(ctx, n.EventID); err != nil {
			slog.Warn("nectar: mark done failed", "event_id", n.EventID, "error", err)
		}
	}
}

// routeNectarInbound es la clasificación de webhook_handler.go (HandleWhatsAppInbound) para un
// mensaje de Néctar: con una notificación pendiente, la pulsación de uno de sus botones va a
// HandleResponse y cualquier otra cosa a HandleInvalidInput (reenvía los botones). true = lo
// consumió el NotificationManager; false = sigue al worker.
//
// Diferencia con Bird, solo de transporte: Néctar manda como interactive_reply_id el TÍTULO del
// botón de la plantilla («Confirmar»), no su payload. Se traduce SOLO con pending: sin él, el
// postback sigue tal cual a la máquina de estados y los menús propios del bot no cambian. Una
// pulsación de plantilla que llega vacía (kind button sin interactive_reply_id, lo de hoy antes
// del arreglo de Néctar) no es postback y cae en HandleInvalidInput, como un texto libre.
func (h *WebhookHandler) routeNectarInbound(msg bird.InboundMessage) bool {
	notify := h.nectarNotifierOrNil()
	if notify == nil || !notify.HasPending(msg.Phone) {
		return false
	}
	if msg.IsPostback {
		payload := msg.PostbackPayload
		if p, ok := notifications.TemplateButtonPayload(payload); ok {
			payload = p
		}
		if IsNotificationPostback(payload) {
			slog.Info("nectar: notification postback received",
				"phone", utils.MaskPhone(msg.Phone), "payload", payload)
			go func() {
				defer recoverLog("notification-response")
				notify.HandleResponse(msg.Phone, payload, msg.ConversationID)
			}()
			return true
		}
	}
	return notify.HandleInvalidInput(msg.Phone, msg.ConversationID)
}

// DispatchNectarNotice es la entrada del replay del WAL para filas con msg_type =
// nectar_notice: vuelve a parsear el aviso y lo despacha como si acabara de llegar.
func (h *WebhookHandler) DispatchNectarNotice(ctx context.Context, rawBody string) error {
	n, err := nectar.ParseNotice([]byte(rawBody))
	if err != nil {
		return err
	}
	h.dispatchNectar(ctx, n)
	return nil
}

// IsNectarInboxRow dice si una fila del WAL es un aviso de Néctar (y no un webhook de Bird).
func IsNectarInboxRow(msgType string) bool { return msgType == nectarInboxType }
