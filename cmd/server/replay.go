package main

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/api"
	"github.com/neuro-bot/neuro-bot/internal/bird"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
)

// Interfaces estrechas de lo que el replay del WAL necesita de cada colaborador. Existen para
// poder probar el enrutamiento de las filas sin base de datos ni worker pool; en producción las
// satisfacen *api.WebhookHandler, *notifications.NotificationManager, *worker.MessageWorkerPool
// y *localrepo.InboxRepo.

// nectarReplayer despacha las filas de Néctar (msg_type nectar_notice), que no son eventos de Bird.
type nectarReplayer interface {
	DispatchNectarNotice(ctx context.Context, rawBody string) error
}

// replayNotifier es la parte del NotificationManager que clasifica un mensaje de Bird como
// respuesta a una notificación proactiva.
type replayNotifier interface {
	HasPending(phone string) bool
	HandleResponse(phone, payload, conversationID string)
	HandleInvalidInput(phone, conversationID string) bool
}

// replayPool es la cola de mensajes del worker pool.
type replayPool interface {
	Enqueue(msg bird.InboundMessage) bool
}

// inboxCloser cierra filas del WAL.
type inboxCloser interface {
	MarkDone(ctx context.Context, id string) error
}

// replayDeps agrupa los colaboradores del replay de arranque del WAL.
type replayDeps struct {
	nectar nectarReplayer
	// notify es nil cuando el bot arranca sin NotificationManager (sin BD de SIESA).
	notify replayNotifier
	pool   replayPool
	inbox  inboxCloser
}

// replayNectarRow re-despacha UNA fila de Néctar del WAL; lo comparten el replay de arranque y el
// de atascados (inboxReplaySource), así que las dos rutas no pueden discrepar. logPrefix
// ("inbox replay" / "stale replay") dice de cuál viene cada línea del registro.
//
// Un mensaje_entrante entra al pool por Enqueue (que deduplica por ID); un aviso ilegible se cierra
// para no repetirlo en cada corrida.
func replayNectarRow(ctx context.Context, logPrefix string, row localrepo.InboxRow, r nectarReplayer, c inboxCloser) {
	slog.Info(logPrefix+": re-despachando aviso de Néctar", "id", row.ID)
	if err := r.DispatchNectarNotice(ctx, row.RawBody); err != nil {
		slog.Error(logPrefix+": aviso de Néctar ilegible", "id", row.ID, "error", err)
		_ = c.MarkDone(ctx, row.ID)
	}
}

// replayPendingRows reprocesa las filas 'pending' del WAL que quedaron sin terminar antes del
// último apagado o caída. El conteo de intentos (migración 035) y el descarte de mensajes veneno
// se hacen ANTES, en main.
func replayPendingRows(ctx context.Context, rows []localrepo.InboxRow, deps replayDeps) {
	for _, row := range rows {
		// Aviso de Néctar: NO es un bird.WebhookEvent (deserializaría en cero y sin error, y
		// acabaría encolado como un mensaje sin teléfono). Se vuelve a parsear como aviso y se
		// despacha como si acabara de llegar; uno ilegible se cierra para no repetirlo.
		if api.IsNectarInboxRow(row.MsgType) {
			replayNectarRow(ctx, "inbox replay", row, deps.nectar, deps.inbox)
			continue
		}

		var event bird.WebhookEvent
		if err := json.Unmarshal([]byte(row.RawBody), &event); err != nil {
			slog.Error("inbox replay parse failed", "id", row.ID, "error", err)
			_ = deps.inbox.MarkDone(ctx, row.ID)
			continue
		}
		msg := bird.ParseInboundMessage(event)

		// Classify: notification postback vs regular message
		if deps.notify != nil && deps.notify.HasPending(msg.Phone) {
			if msg.IsPostback && api.IsNotificationPostback(msg.PostbackPayload) {
				slog.Info("WAL replay: notification postback routed to handler",
					"id", row.ID, "phone", msg.Phone, "payload", msg.PostbackPayload)
				go deps.notify.HandleResponse(msg.Phone, msg.PostbackPayload, msg.ConversationID)
				_ = deps.inbox.MarkDone(ctx, row.ID)
				continue
			}
			if deps.notify.HandleInvalidInput(msg.Phone, msg.ConversationID) {
				slog.Info("WAL replay: invalid input during notification handled",
					"id", row.ID, "phone", msg.Phone)
				_ = deps.inbox.MarkDone(ctx, row.ID)
				continue
			}
		}

		deps.pool.Enqueue(msg)
	}
}
