package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/utils"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

// recoverLog logs panics from background goroutines without crashing the process.
func recoverLog(name string) {
	if r := recover(); r != nil {
		slog.Error("PANIC in background goroutine",
			"goroutine", name,
			"error", fmt.Sprintf("%v", r),
			"stack", string(debug.Stack()),
		)
	}
}

// sleepWithContext espera la duración indicada o retorna antes si el contexto se cancela.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// InboxPersister abstracts message inbox operations for crash recovery (WAL pattern).
type InboxPersister interface {
	InsertIfNotExists(ctx context.Context, id, phone, rawBody, msgType string, receivedAt time.Time) (bool, error)
}

type WebhookHandler struct {
	birdClient      *bird.Client
	workerPool      *worker.MessageWorkerPool
	notifyManager   *notifications.NotificationManager
	cfg             *config.Config
	inboxRepo       InboxPersister  // WAL for crash recovery (optional)
	deliveryTracker DeliveryTracker // fallos de entrega WA por teléfono (optional)
	// voiceGatherCmds maps callId → gatherEntry so we can query the DTMF result
	// from GET /calls/{callId}/commands/{commandId} when the call completes.
	voiceGatherCmds sync.Map
	// outboundSeen dedupea por Payload.ID: la suscripción con eventos de ESTADO entrega 4-5
	// eventos por mensaje (accepted→processing→sent→delivered/read); el trabajo caro (fetch del
	// texto para /bot, touch de actividad) corre UNA vez por mensaje. Valor: time.Time del store.
	outboundSeen      sync.Map
	outboundSeenCount atomic.Int64
	// startedAt: gracia de arranque para la pausa por intervención de agente (ver takeoverStartupGrace).
	startedAt time.Time
}

// DeliveryTracker registra el resultado de ENTREGA de los envíos salientes (webhook outbound).
// Es la base de la supresión de templates a números sin WhatsApp: cada envío se cobra aunque
// WhatsApp nunca lo entregue.
type DeliveryTracker interface {
	RecordFailure(ctx context.Context, phone, status string) error
	RecordSuccess(ctx context.Context, phone string) error
}

// SetDeliveryTracker injects the delivery-failure tracker (optional).
func (h *WebhookHandler) SetDeliveryTracker(t DeliveryTracker) {
	h.deliveryTracker = t
}

type gatherEntry struct {
	commandID string
	storedAt  time.Time
}

// StartGatherCleanup periodically evicts stale entries from voiceGatherCmds.
func (h *WebhookHandler) StartGatherCleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-10 * time.Minute)
			h.voiceGatherCmds.Range(func(key, val any) bool {
				if e, ok := val.(gatherEntry); ok && e.storedAt.Before(cutoff) {
					h.voiceGatherCmds.Delete(key)
				}
				return true
			})
		}
	}
}

func NewWebhookHandler(birdClient *bird.Client, workerPool *worker.MessageWorkerPool, notifyManager *notifications.NotificationManager, cfg *config.Config) *WebhookHandler {
	return &WebhookHandler{
		birdClient:    birdClient,
		workerPool:    workerPool,
		notifyManager: notifyManager,
		cfg:           cfg,
		startedAt:     time.Now(),
	}
}

// SetInboxRepo injects the message inbox for crash-recovery persistence (WAL pattern).
func (h *WebhookHandler) SetInboxRepo(repo InboxPersister) {
	h.inboxRepo = repo
}

// HandleWhatsApp procesa webhooks de mensajes inbound de Bird
func (h *WebhookHandler) HandleWhatsApp(w http.ResponseWriter, r *http.Request) {
	body, event, ok := h.verifyAndParse(w, r, false)
	if !ok {
		return
	}

	// Ignorar outbound que lleguen a este endpoint (por si acaso)
	// Bird usa "incoming"/"outgoing" como valores de direction
	if event.Payload.Direction == "outbound" || event.Payload.Direction == "outgoing" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Parsear mensaje inbound
	msg := bird.ParseInboundMessage(event)

	// Testing whitelist: ignorar teléfonos no autorizados
	if !h.cfg.IsPhoneWhitelisted(msg.Phone) {
		slog.Debug("phone not whitelisted, ignoring", "phone", utils.MaskPhone(msg.Phone))
		w.WriteHeader(http.StatusOK)
		return
	}

	// WAL: persistir mensaje a DB ANTES de responder 200 a Bird.
	// Si el bot crashea después de esto, el mensaje se replayea al reiniciar.
	if h.inboxRepo != nil && msg.ID != "" {
		inserted, err := h.inboxRepo.InsertIfNotExists(r.Context(), msg.ID, msg.Phone, string(body), msg.MessageType, msg.ReceivedAt)
		if err != nil {
			slog.Error("inbox persist failed", "id", msg.ID, "error", err)
			// DB falla → responder 500 → Bird reintenta (fail-safe)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !inserted {
			// Duplicado: ya persistido (quizás Bird reintentó). Acknowledge sin encolar.
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Mensaje seguro en DB → responder 200 a Bird
	w.WriteHeader(http.StatusOK)

	// Clasificar: postback de notificación o mensaje de chatbot?
	if h.notifyManager != nil && h.notifyManager.HasPending(msg.Phone) {
		if msg.IsPostback && IsNotificationPostback(msg.PostbackPayload) {
			slog.Info("notification postback received",
				"phone", utils.MaskPhone(msg.Phone),
				"payload", msg.PostbackPayload,
			)
			go func() {
				defer recoverLog("notification-response")
				h.notifyManager.HandleResponse(msg.Phone, msg.PostbackPayload, msg.ConversationID)
			}()
			return
		}
		// Patient sent free text instead of pressing a button — retry the prompt
		if h.notifyManager.HandleInvalidInput(msg.Phone, msg.ConversationID) {
			return
		}
	}

	// Mensaje normal -> Worker pool (state machine)
	h.workerPool.Enqueue(msg)
}

// HandleWhatsAppOutbound procesa webhooks de mensajes outbound de Bird.
// Endpoint separado porque Bird solo permite un tipo de evento por webhook.
// Detecta comandos /bot escritos por agentes humanos en Bird Inbox.
func (h *WebhookHandler) HandleWhatsAppOutbound(w http.ResponseWriter, r *http.Request) {
	_, event, ok := h.verifyAndParse(w, r, true)
	if !ok {
		return
	}

	// Responder 200 inmediatamente (outbound no necesita WAL)
	w.WriteHeader(http.StatusOK)

	// Solo procesar outbound (Bird usa "outgoing")
	if event.Payload.Direction != "outbound" && event.Payload.Direction != "outgoing" {
		return
	}

	h.handleOutbound(event)
}

// verifyAndParse lee el body, verifica HMAC y parsea el evento.
// NO escribe la respuesta HTTP — cada caller decide cuándo responder 200.
// Esto permite a HandleWhatsApp persistir el mensaje a DB antes de responder (WAL pattern).
func (h *WebhookHandler) verifyAndParse(w http.ResponseWriter, r *http.Request, outbound bool) ([]byte, bird.WebhookEvent, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return nil, bird.WebhookEvent{}, false
	}

	// Bird usa MessageBird-Signature + MessageBird-Request-Timestamp
	// Firma = HMAC-SHA256(signingKey, timestamp + "\n" + url + "\n" + SHA256(body)), base64
	signature := r.Header.Get("MessageBird-Signature")
	timestamp := r.Header.Get("MessageBird-Request-Timestamp")

	// Bird firma con la URL completa que tiene configurada.
	// Reconstruir desde X-Forwarded-Host/Proto (ngrok) o Host header.
	requestURL := reconstructFullURL(r)

	// Each Bird webhook subscription has its own signing key
	var valid bool
	if outbound {
		valid = h.birdClient.VerifyOutboundWebhookSignature(signature, timestamp, requestURL, body)
	} else {
		valid = h.birdClient.VerifyWebhookSignature(signature, timestamp, requestURL, body)
	}

	if !valid {
		// For outbound: try inbound key as fallback (Bird may sign agent messages with a different subscription key)
		if outbound {
			valid = h.birdClient.VerifyWebhookSignature(signature, timestamp, requestURL, body)
		}
	}

	if !valid {
		// M1 (auditoría): NO loguear el body crudo — contiene PHI (teléfono/nombre/texto del paciente).
		// Para correlacionar eventos idénticos basta un hash corto del cuerpo (sin exponer contenido).
		sum := sha256.Sum256(body)
		slog.Warn("invalid webhook signature",
			"has_signature", signature != "",
			"has_timestamp", timestamp != "",
			"url", requestURL,
			"body_len", len(body),
			"outbound", outbound,
			"body_sha256", hex.EncodeToString(sum[:])[:16],
		)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return nil, bird.WebhookEvent{}, false
	}

	var event bird.WebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("parse webhook event", "error", err)
		return body, bird.WebhookEvent{}, false
	}

	return body, event, true
}

// handleOutbound processes outbound webhook events.
// 1. Caches conversationId for future escalation (from ALL outbound messages).
// 2. Detects /bot commands from agents in Bird Inbox.
func (h *WebhookHandler) handleOutbound(event bird.WebhookEvent) {
	// Extract phone from receiver (Bird uses "connector" singular, legacy uses "contacts" array)
	phone := ""
	if event.Payload.Receiver.Connector.IdentifierValue != "" {
		phone = event.Payload.Receiver.Connector.IdentifierValue
	} else if len(event.Payload.Receiver.Contacts) > 0 {
		phone = event.Payload.Receiver.Contacts[0].IdentifierValue
	}

	slog.Debug("outbound_event_received",
		"phone", utils.MaskPhone(phone),
		"conversation_id", event.Payload.ConversationID,
		"direction", event.Payload.Direction,
		"status", event.Payload.Status,
		"msg_id", event.Payload.ID,
		"body_type", event.Payload.Body.Type,
	)

	// Registrar el estado de ENTREGA (no los intermedios): delivery_failed acumula el contador
	// del teléfono; delivered/read lo resetea. Con >=2 fallos consecutivos, los templates
	// programados a ese número se suprimen (se cobraban sin llegar). Corre POR EVENTO (los
	// estados difieren); todo lo demás de este handler corre una vez por mensaje (dedupe abajo).
	h.recordDeliveryStatus(phone, event.Payload.Status)

	// Cache conversationId from ALL outbound messages and persist to DB
	if event.Payload.ConversationID != "" && phone != "" {
		h.birdClient.CacheConversationID(phone, event.Payload.ConversationID)
		h.workerPool.UpdateConversationID(phone, event.Payload.ConversationID)
		slog.Debug("outbound_conversation_cached",
			"phone", utils.MaskPhone(phone),
			"conversation_id", event.Payload.ConversationID,
		)
	}

	// Dedupe por mensaje: los eventos de estado posteriores del mismo Payload.ID no repiten el
	// trabajo caro (FetchMessageText = GET a Bird por evento, TouchAgentActivity = UPDATE por
	// evento). Sin esto, la suscripción de estados multiplica ~4× esas llamadas.
	if id := event.Payload.ID; id != "" {
		if _, seen := h.outboundSeen.LoadOrStore(id, time.Now()); seen {
			return
		}
		if h.outboundSeenCount.Add(1)%512 == 0 {
			h.sweepOutboundSeen()
		}
	}

	// Check for agent /bot command
	// Bird Inbox agent messages arrive WITHOUT body in the webhook — fetch via API
	text := ""
	if event.Payload.Body.Text.Text != "" {
		text = event.Payload.Body.Text.Text
	} else if event.Payload.ID != "" {
		text = h.birdClient.FetchMessageText(event.Payload.ID)
	}
	text = strings.TrimSpace(text)

	// ¿Este saliente lo escribió el propio bot? El cliente recuerda los IDs de todo lo que envió.
	ownMessage := h.birdClient.IsOwnMessage(event.Payload.ID)

	// Respuesta del agente: cualquier saliente humano hacia el paciente (no propio, no /bot, no
	// mensaje-puente del bot) frena el recordatorio de la sesión escalada de este teléfono.
	if phone != "" && !ownMessage && !strings.HasPrefix(text, "/bot") && !worker.IsBotInterstitialMessage(text) {
		h.workerPool.TouchAgentActivity(phone)
	}

	if !strings.HasPrefix(text, "/bot") {
		// Pausa automática por intervención manual: un agente escribió en una conversación cuya
		// sesión está ACTIVA → el bot se aparta (la sesión queda escalada; /bot resume la devuelve).
		// Gracia de arranque: tras un reinicio la memoria de IDs propios está vacía y los eventos
		// tardíos de mensajes pre-reinicio parecerían de agente.
		if phone != "" && time.Since(h.startedAt) > takeoverStartupGrace &&
			isForeignOutboundMessage(ownMessage, event.Payload.Status, text, event.Payload.CreatedAt) {
			h.workerPool.HandleAgentTakeover(phone)
		}
		return // Not an agent command — ignore
	}

	if phone == "" {
		slog.Warn("outbound /bot command without phone", "text", text)
		return
	}

	cmd := worker.ParseAgentCommand(text)
	cmd.Phone = phone

	slog.Info("agent command received",
		"phone", utils.MaskPhone(phone),
		"action", cmd.Action,
		"state", cmd.State,
		"data", cmd.Data,
	)

	h.workerPool.EnqueueAgentCommand(cmd)
}

// takeoverStartupGrace: ventana tras el arranque en la que NO se dispara la pausa por intervención
// de agente — la memoria de IDs propios arranca vacía y los eventos tardíos de mensajes enviados
// antes del reinicio parecerían de un humano.
const takeoverStartupGrace = 3 * time.Minute

// takeoverMaxMessageAge: frescura máxima del mensaje para considerarlo intervención de agente.
// Caso real (06-ago 13:41): Bird RE-ENTREGÓ con backoff, 29 min después, el evento de un mensaje
// del BOT enviado antes del reinicio (su ID no estaba en la memoria del proceso nuevo) y pausó
// una sesión activa. Un agente de verdad genera el webhook en segundos.
const takeoverMaxMessageAge = 2 * time.Minute

// isForeignOutboundMessage decide si un evento outbound lo escribió un AGENTE humano (no el bot).
// Conservadora a propósito: un falso positivo pausaría el bot con sus propios mensajes.
//   - own: el ID está registrado como enviado por el cliente → bot.
//   - delivered/read: transiciones tardías (pueden ser de mensajes pre-reinicio) → nunca disparan.
//   - createdAt viejo, ausente o ilegible → sin prueba de frescura, no se pausa (reintentos de Bird).
//   - /bot y mensajes-puente del bot → no son intervención.
func isForeignOutboundMessage(ownMessage bool, status, text, createdAt string) bool {
	if ownMessage {
		return false
	}
	switch strings.ToLower(status) {
	case "delivered", "read":
		return false
	}
	ts, err := time.Parse(time.RFC3339, createdAt)
	if err != nil || time.Since(ts) > takeoverMaxMessageAge {
		return false
	}
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "/bot") {
		return false
	}
	if worker.IsBotInterstitialMessage(t) {
		return false
	}
	return true
}

// sweepOutboundSeen purga del dedupe los mensajes con más de 1h (los eventos de estado de un
// mensaje llegan en segundos/minutos; 1h cubre reintentos de webhook con margen holgado).
func (h *WebhookHandler) sweepOutboundSeen() {
	cutoff := time.Now().Add(-1 * time.Hour)
	h.outboundSeen.Range(func(k, v interface{}) bool {
		if ts, ok := v.(time.Time); ok && ts.Before(cutoff) {
			h.outboundSeen.Delete(k)
		}
		return true
	})
}

// recordDeliveryStatus registra fallos/éxitos de ENTREGA del webhook outbound. Solo estados
// finales; los intermedios (accepted/sent) no dicen nada de si el número tiene WhatsApp.
func (h *WebhookHandler) recordDeliveryStatus(phone, status string) {
	if h.deliveryTracker == nil || phone == "" || status == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch strings.ToLower(status) {
	case "delivery_failed", "failed", "rejected", "undeliverable", "sending_failed":
		if err := h.deliveryTracker.RecordFailure(ctx, phone, status); err != nil {
			slog.Warn("record delivery failure", "phone", utils.MaskPhone(phone), "error", err)
		} else {
			slog.Info("delivery failure recorded", "phone", utils.MaskPhone(phone), "status", status)
		}
	case "delivered", "read":
		if err := h.deliveryTracker.RecordSuccess(ctx, phone); err != nil {
			slog.Warn("record delivery success", "phone", utils.MaskPhone(phone), "error", err)
		}
	}
}

// extractConversationPhone extracts the phone from conversation participants.
func extractConversationPhone(participants []bird.Participant) string {
	for _, p := range participants {
		if p.IdentifierValue != "" {
			return p.IdentifierValue
		}
		if p.Contact.IdentifierValue != "" {
			return p.Contact.IdentifierValue
		}
	}
	return ""
}

// HandleConversation procesa webhooks del servicio Conversations de Bird.
// Handles conversation.created (cache), conversation.updated (invalidate if closed),
// and conversation.deleted (invalidate).
func (h *WebhookHandler) HandleConversation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// Firma OBLIGATORIA cuando hay un secreto disponible (el de conversations o, como fallback,
	// el principal). Cierra el envenenamiento de caché por teléfono cuando solo el conversations
	// secret estaba vacío pero el principal sí configurado (N-13). Si NINGÚN secreto está
	// configurado se omite (entorno sin configurar; este webhook solo cachea conversation IDs).
	secret := h.cfg.BirdWebhookSecretConversations
	if secret == "" {
		secret = h.cfg.BirdWebhookSecret
	}
	if secret != "" {
		signature := r.Header.Get("MessageBird-Signature")
		timestamp := r.Header.Get("MessageBird-Request-Timestamp")
		requestURL := reconstructFullURL(r)
		if !bird.VerifySignatureWithKey(secret, signature, timestamp, requestURL, body) {
			slog.Warn("invalid conversation webhook signature",
				"has_signature", signature != "",
				"has_timestamp", timestamp != "",
				"url", requestURL,
			)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	w.WriteHeader(http.StatusOK)

	var event bird.ConversationEvent
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("parse conversation webhook", "error", err)
		return
	}

	switch event.Event {
	case "conversation.created":
		// Fall through to cache the new conversation ID
	case "conversation.updated":
		if event.Payload.Status != "" && event.Payload.Status != "active" {
			phone := extractConversationPhone(event.Payload.FeaturedParticipants)
			if phone != "" {
				h.birdClient.InvalidateCachedConversationID(phone)
				slog.Info("conversation_closed_cache_invalidated",
					"phone", utils.MaskPhone(phone),
					"conversation_id", event.Payload.ID,
					"status", event.Payload.Status,
				)
			}
			return
		}
		// Status == "active" → fall through to update cache
	case "conversation.deleted":
		phone := extractConversationPhone(event.Payload.FeaturedParticipants)
		if phone != "" {
			h.birdClient.InvalidateCachedConversationID(phone)
			slog.Info("conversation_deleted_cache_invalidated",
				"phone", utils.MaskPhone(phone),
				"conversation_id", event.Payload.ID,
			)
		}
		return
	default:
		return
	}

	convID := event.Payload.ID
	if convID == "" {
		return
	}

	phone := extractConversationPhone(event.Payload.FeaturedParticipants)
	if phone != "" {
		h.birdClient.CacheConversationID(phone, convID)
		h.workerPool.UpdateConversationID(phone, convID)
		slog.Info("conversation_cached",
			"phone", utils.MaskPhone(phone),
			"conversation_id", convID,
			"event", event.Event,
			"channel_id", event.Payload.ChannelID,
		)
	} else {
		slog.Debug("conversation_event_no_phone",
			"conversation_id", convID,
			"event", event.Event,
			"body_len", len(body), // no loguear el body crudo (PHI) ni en debug
		)
	}
}

// HandleVoiceWebhook receives Bird voice call events (call_command_gather_finished, etc.)
// and processes the DTMF result to confirm or ignore the appointment confirmation.
func (h *WebhookHandler) HandleVoiceWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Firma OBLIGATORIA cuando hay secreto configurado: cierra el bypass fail-open (antes se
	// podía saltar la validación simplemente NO enviando el header MessageBird-Signature).
	// Si no hay secreto configurado, se omite (entornos sin configurar). Igual que HandleConversation.
	signature := r.Header.Get("MessageBird-Signature")
	timestamp := r.Header.Get("MessageBird-Request-Timestamp")
	secret := h.cfg.BirdWebhookSecretVoice
	if secret == "" {
		secret = h.cfg.BirdWebhookSecret
	}
	if secret != "" {
		requestURL := reconstructFullURL(r)
		if !bird.VerifySignatureWithKey(secret, signature, timestamp, requestURL, body) {
			slog.Warn("invalid voice webhook signature", "has_signature", signature != "")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Bird sends two possible shapes for voice webhooks:
	// 1. New format: {"service":"channels","event":"voice.outbound","payload":{"id":"callId","status":"..."}}
	// 2. Legacy format: {"type":"call_command_gather_finished","callId":"...","callCommand":{...}}
	var event struct {
		// New format
		Service string `json:"service"`
		Event   string `json:"event"`
		Payload struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			// gather result may arrive here for mid-call gather API results
			CallCommand struct {
				Gather struct {
					Keys string `json:"keys"`
				} `json:"gather"`
			} `json:"callCommand"`
		} `json:"payload"`
		// Legacy format
		Type              string `json:"type"`
		LegacyCallID      string `json:"callId"`
		LegacyCallCommand struct {
			Gather struct {
				Keys string `json:"keys"`
			} `json:"gather"`
		} `json:"callCommand"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Warn("voice webhook: invalid JSON", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	slog.Info("voice webhook event received",
		"event", event.Event,
		"type", event.Type,
		"callId_new", event.Payload.ID,
		"callId_legacy", event.LegacyCallID,
		"status", event.Payload.Status,
	)

	if h.notifyManager != nil {
		// New format: voice.outbound lifecycle events
		if event.Event == "voice.outbound" {
			callID := event.Payload.ID
			switch event.Payload.Status {
			case "ongoing":
				// Call is active — send mid-call gather command (two-phase IVR)
				slog.Info("voice call ongoing, sending gather", "callId", callID)
				go func() {
					defer recoverLog("voice-gather-send")
					commandID, err := h.birdClient.SendGather(callID)
					if err != nil {
						slog.Error("send gather failed", "callId", callID, "error", err)
						return
					}
					if commandID != "" {
						h.voiceGatherCmds.Store(callID, gatherEntry{commandID: commandID, storedAt: time.Now()})
					}
				}()
			case "completed":
				// Call finished — query gather command result via GET /calls/{id}/commands/{cmdId}
				go func() {
					defer recoverLog("voice-gather-result")
					val, hasCmd := h.voiceGatherCmds.LoadAndDelete(callID)
					if !hasCmd {
						// Gather command never ran (call not answered, or gather failed)
						h.notifyManager.HandleVoiceCallCompleted(callID)
						return
					}
					commandID := val.(gatherEntry).commandID
					keys, err := h.birdClient.GetGatherResult(callID, commandID)
					if err != nil {
						slog.Error("get gather result failed", "callId", callID, "commandId", commandID, "error", err)
						h.notifyManager.HandleVoiceCallCompleted(callID)
						return
					}
					slog.Info("IVR gather result retrieved", "callId", callID, "keys", keys)
					h.notifyManager.HandleVoiceGatherResult(callID, keys)
				}()
			}
			// gather result may also come inline if Bird ever includes it in the webhook
			if keys := event.Payload.CallCommand.Gather.Keys; keys != "" || event.Payload.Status == "gather_finished" {
				h.notifyManager.HandleVoiceGatherResult(callID, keys)
			}
		}

		// Legacy format (kept in case gather result uses old event type)
		switch event.Type {
		case "call_command_gather_finished":
			h.notifyManager.HandleVoiceGatherResult(event.LegacyCallID, event.LegacyCallCommand.Gather.Keys)
		case "outgoing_call_completed":
			h.notifyManager.HandleVoiceCallCompleted(event.LegacyCallID)
		}
	}

	w.WriteHeader(http.StatusOK)
}

// HandleVoiceDTMF is called by Bird's fetchCallFlow mechanism after a gather completes.
// Bird POSTs the call context (including gathered "keys") to this endpoint and executes
// the callFlow JSON we return. We also process the DTMF result asynchronously.
func (h *WebhookHandler) HandleVoiceDTMF(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Firma OBLIGATORIA cuando hay secreto configurado (cierra el bypass fail-open). Si no hay
	// secreto, se omite. ⚠️ Este endpoint procesa el DTMF que confirma/cancela la cita: si Bird
	// NO firma estos webhooks, esto los rechazaría — validar con una llamada IVR real.
	signature := r.Header.Get("MessageBird-Signature")
	timestamp := r.Header.Get("MessageBird-Request-Timestamp")
	secret := h.cfg.BirdWebhookSecretVoice
	if secret == "" {
		secret = h.cfg.BirdWebhookSecret
	}
	if secret != "" {
		requestURL := reconstructFullURL(r)
		if !bird.VerifySignatureWithKey(secret, signature, timestamp, requestURL, body) {
			slog.Warn("invalid voice dtmf webhook signature", "has_signature", signature != "")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Bird sends call context as JSON: {callID, keys, ...}
	var ctx struct {
		CallID string `json:"callId"`
		Keys   string `json:"keys"`
	}
	json.Unmarshal(body, &ctx) // best-effort; log raw for discovery

	slog.Info("voice dtmf fetchCallFlow received",
		"callId", ctx.CallID,
		"keys", ctx.Keys,
	)

	// Determine response TTS based on key pressed
	var responseText string
	switch ctx.Keys {
	case "1":
		responseText = "Gracias, su cita ha sido confirmada con exito. " +
			"Para consultar las preparaciones para su cita comuniquese con nosotros a traves de WhatsApp. " +
			"Hasta pronto."
	case "":
		responseText = "No hemos recibido su respuesta. Su cita queda pendiente de confirmacion. " +
			"Puede confirmarla comunicandose con nosotros a traves de WhatsApp. Hasta pronto."
	default:
		responseText = "Entendido. Si desea reagendar su cita puede comunicarse con nosotros a traves de WhatsApp. " +
			"Hasta pronto."
	}

	// Return callFlow to Bird: say response + hangup
	responseFlow := []map[string]interface{}{
		{
			"command": "say",
			"options": map[string]interface{}{
				"locale": "es-MX",
				"voice":  "female",
				"text":   responseText,
			},
		},
		{"command": "hangup"},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responseFlow)

	// Process DTMF result asynchronously (confirm/cancel in DB)
	if h.notifyManager != nil && ctx.CallID != "" {
		go func() {
			defer recoverLog("voice-gather-process")
			h.notifyManager.HandleVoiceGatherResult(ctx.CallID, ctx.Keys)
		}()
	}
}

// reconstructFullURL reconstruye la URL completa que Bird usó para firmar.
// Usa X-Forwarded-Proto/Host (ngrok) o Host header como fallback.
func reconstructFullURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host + r.URL.String()
}

// IsNotificationPostback determina si un postback viene de un template proactivo
func IsNotificationPostback(payload string) bool {
	switch payload {
	case "confirm", "cancelar", "cancel", "understood", "reschedule", "reprogramar",
		"wl_schedule", "wl_decline":
		return true
	default:
		return false
	}
}
