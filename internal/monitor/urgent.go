package monitor

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math"
	"sync"
	"time"
)

// Aviso URGENTE de la transición Bird → Néctar: cuando Néctar o la línea nueva fallan, una persona
// tiene que devolver el bot a Bird a mano (NECTAR_BASE_URL vacía + reinicio) siguiendo la guía.
// Es un mensaje PROPIO a Telegram, aparte de los errores comunes: NO pasa por slog.Error, porque el
// AlertHandler los agrupa con su enfriamiento y este aviso quedaría tapado entre ellos.

// UrgentTrigger es el motivo del aviso urgente; cada uno lleva su propio enfriamiento.
type UrgentTrigger string

const (
	// TriggerNectarDown son fallos de transporte o 5xx de Néctar seguidos en una ventana corta.
	TriggerNectarDown UrgentTrigger = "nectar_down"
	// TriggerProactiveStopped es una corrida de plantillas proactivas se detuvo por configuración.
	TriggerProactiveStopped UrgentTrigger = "proactive_stopped"
	// TriggerRemindersUnsent es una corrida de recordatorios dejó demasiados sin enviar o en duda.
	TriggerRemindersUnsent UrgentTrigger = "reminders_unsent"
	// TriggerEscalationFailing son las escalaciones a Néctar fallan seguidas.
	TriggerEscalationFailing UrgentTrigger = "escalation_failing"
)

// urgentTriggerLabels es lo que lee la persona de turno para cada disparador.
var urgentTriggerLabels = map[UrgentTrigger]string{
	TriggerNectarDown:        "Néctar no responde",
	TriggerProactiveStopped:  "Plantillas proactivas detenidas por configuración",
	TriggerRemindersUnsent:   "Recordatorios sin enviar por encima del umbral",
	TriggerEscalationFailing: "Escalaciones a Néctar fallando",
}

// urgentQueueSize acota los avisos pendientes de envío; urgentSendTimeout, cuánto espera cada uno a
// Telegram. Con el enfriamiento por disparador nunca hay más de unos pocos en vuelo.
const (
	urgentQueueSize   = 16
	urgentSendTimeout = 10 * time.Second
)

// UrgentAlerter manda el aviso urgente. Por disparador, a lo sumo un aviso por cooldown: mientras
// el problema siga, se repite cada cooldown. Un receptor nil no hace nada (Telegram sin configurar o
// puente de Néctar apagado).
//
// Fire y Count NUNCA bloquean a quien los llama (un envío a Néctar, una corrida de recordatorios):
// la decisión (enfriamiento, ventana) es síncrona bajo el candado y el envío lo hace Start en su
// propia goroutine, como el AlertHandler de internal/telegram.
type UrgentAlerter struct {
	send      func(ctx context.Context, text string) error
	guidePath string
	cooldown  time.Duration
	now       func() time.Time
	queue     chan urgentMessage

	mu        sync.Mutex
	lastFired map[UrgentTrigger]time.Time
	hits      map[UrgentTrigger][]time.Time // últimas ocurrencias de Count dentro de la ventana (≤ umbral)
}

type urgentMessage struct {
	trigger UrgentTrigger
	text    string
}

// NewUrgentAlerter arma el aviso. send es el envío directo a Telegram (tg.Client.SendMessage),
// guidePath la ruta de la guía de vuelta a Bird y now el reloj (inyectable para las pruebas).
// Los avisos salen solo con Start corriendo.
func NewUrgentAlerter(send func(ctx context.Context, text string) error, guidePath string,
	cooldown time.Duration, now func() time.Time,
) *UrgentAlerter {
	return &UrgentAlerter{
		send:      send,
		guidePath: guidePath,
		cooldown:  cooldown,
		now:       now,
		queue:     make(chan urgentMessage, urgentQueueSize),
		lastFired: map[UrgentTrigger]time.Time{},
		hits:      map[UrgentTrigger][]time.Time{},
	}
}

// Start envía los avisos encolados, cada uno con su propio timeout. Bloquea hasta que ctx se
// cancela; al apagar, intenta los que quedaban en la cola.
func (a *UrgentAlerter) Start(ctx context.Context) {
	if a == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case m := <-a.queue:
					a.deliver(context.WithoutCancel(ctx), m)
				default:
					return
				}
			}
		case m := <-a.queue:
			a.deliver(ctx, m)
		}
	}
}

func (a *UrgentAlerter) deliver(ctx context.Context, m urgentMessage) {
	ctx, cancel := context.WithTimeout(ctx, urgentSendTimeout)
	defer cancel()
	if err := a.send(ctx, m.text); err != nil {
		// Warn y no Error: un Error subiría a Telegram por el AlertHandler, que es lo que acaba de fallar.
		slog.Warn("aviso urgente: falló el envío a Telegram", "trigger", string(m.trigger), "error", err)
	}
}

// Fire encola el aviso del disparador salvo que ya haya salido uno dentro del enfriamiento. El
// enfriamiento se marca al decidir, no al entregar: con Telegram caído no se martillea. Si la cola
// está llena el aviso no sale y el enfriamiento se devuelve: el siguiente Fire del mismo disparador
// lo intenta de nuevo, en vez de quedarse sin aviso todo el enfriamiento.
func (a *UrgentAlerter) Fire(_ context.Context, trigger UrgentTrigger, detail string) {
	if a == nil {
		return
	}
	now := a.now()
	a.mu.Lock()
	previous, hadPrevious := a.lastFired[trigger]
	if hadPrevious && now.Sub(previous) < a.cooldown {
		a.mu.Unlock()
		return
	}
	a.lastFired[trigger] = now
	a.mu.Unlock()

	slog.Warn("aviso urgente: volver a Bird", "trigger", string(trigger), "detail", detail)
	select {
	case a.queue <- urgentMessage{trigger: trigger, text: a.message(trigger, detail, now)}:
	default:
		slog.Warn("aviso urgente: cola llena, se descarta", "trigger", string(trigger))
		a.mu.Lock()
		// Solo si la marca sigue siendo la de este Fire: otro pudo marcar después.
		if a.lastFired[trigger].Equal(now) {
			if hadPrevious {
				a.lastFired[trigger] = previous
			} else {
				delete(a.lastFired, trigger)
			}
		}
		a.mu.Unlock()
	}
}

// Count suma una ocurrencia del disparador y, si en la ventana deslizante hay threshold o más, llama
// a Fire (que aplica el enfriamiento) con cuántas fueron y desde cuándo. Solo guarda las últimas
// threshold ocurrencias: una caída sostenida no hace crecer la memoria.
func (a *UrgentAlerter) Count(ctx context.Context, trigger UrgentTrigger, threshold int, window time.Duration, detail string) {
	if a == nil {
		return
	}
	threshold = max(threshold, 1)
	now := a.now()
	a.mu.Lock()
	kept := make([]time.Time, 0, threshold)
	for _, at := range a.hits[trigger] {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	if len(kept) > threshold {
		kept = kept[len(kept)-threshold:]
	}
	a.hits[trigger] = kept
	count, since := len(kept), kept[0]
	a.mu.Unlock()

	if count < threshold {
		return
	}
	// «al menos»: solo se guardan las últimas threshold ocurrencias, así que count no es el total.
	a.Fire(ctx, trigger, fmt.Sprintf("al menos %d en menos de %s (desde las %s); %s",
		count, wholeMinutes(window), since.Format("15:04:05"), detail))
}

// wholeMinutes dice la ventana en minutos enteros («5 minutos», «1 minuto») y no «5m0s»: la lee la
// persona de turno. Las ventanas se configuran en minutos; una fracción se redondea hacia arriba.
func wholeMinutes(d time.Duration) string {
	m := max(int(math.Ceil(d.Minutes())), 1)
	if m == 1 {
		return "1 minuto"
	}
	return fmt.Sprintf("%d minutos", m)
}

// message arma el texto (HTML de Telegram; el detalle se escapa). Sin PII: los llamadores no ponen
// teléfonos en el detalle.
func (a *UrgentAlerter) message(trigger UrgentTrigger, detail string, at time.Time) string {
	label := urgentTriggerLabels[trigger]
	if label == "" {
		label = string(trigger)
	}
	return fmt.Sprintf("🚨 <b>URGENTE — volver a Bird</b>\n\n"+
		"Disparador: <b>%s</b> (<code>%s</code>)\n"+
		"Detalle: %s\n"+
		"Hora: %s\n\n"+
		"Guía: <code>%s</code>",
		html.EscapeString(label), trigger, html.EscapeString(detail),
		at.Format("2006-01-02 15:04:05"), html.EscapeString(a.guidePath))
}
