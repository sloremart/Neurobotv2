package main

import (
	"context"
	"encoding/json"
	"errors"
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
	// GoHandleResponse corre la respuesta en segundo plano, registrada para el apagado (D-10).
	GoHandleResponse(phone, payload, conversationID string)
	HandleInvalidInput(phone, conversationID string) bool
	AcceptsPostback(phone, payload string) bool
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
// Un mensaje_entrante pasa por la misma clasificación que el camino HTTP (dispatchNectar →
// routeNectarInbound): la respuesta a una notificación pendiente va al NotificationManager y su fila
// se cierra allí, como en el replay de Bird de abajo; el resto entra al pool por Enqueue (que
// deduplica por ID). Un aviso ilegible se cierra para no repetirlo en cada corrida. Con el puente
// apagado (bird.ErrNectarBridgeOff) la fila también se cierra, sin Error: no se despachó nada, el
// despachador ya dejó su Warn bridge_off, y al volver el puente no debe re-despacharse algo viejo.
// Un aviso que el pool todavía tiene en memoria (api.ErrNoticeInFlight) no se toca: su fila la
// cierra la copia en curso.
func replayNectarRow(ctx context.Context, logPrefix string, row localrepo.InboxRow, r nectarReplayer, c inboxCloser) {
	slog.Info(logPrefix+": re-despachando aviso de Néctar", "id", row.ID)
	err := r.DispatchNectarNotice(ctx, row.RawBody)
	switch {
	case err == nil:
		return
	case errors.Is(err, api.ErrNoticeInFlight):
		slog.Info(logPrefix+": aviso de Néctar todavía en vuelo, se deja", "id", row.ID)
		return
	case !errors.Is(err, bird.ErrNectarBridgeOff):
		slog.Error(logPrefix+": aviso de Néctar ilegible", "id", row.ID, "error", err)
	}
	_ = c.MarkDone(ctx, row.ID)
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
			// Misma regla que el camino HTTP: con un recordatorio que no salió solo sus botones cuentan.
			if msg.IsPostback && api.IsNotificationPostback(msg.PostbackPayload) &&
				deps.notify.AcceptsPostback(msg.Phone, msg.PostbackPayload) {
				slog.Info("WAL replay: notification postback routed to handler",
					"id", row.ID, "phone", msg.Phone, "payload", msg.PostbackPayload)
				deps.notify.GoHandleResponse(msg.Phone, msg.PostbackPayload, msg.ConversationID)
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
