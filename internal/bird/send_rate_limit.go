package bird

import (
	"errors"
	"log/slog"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Rate-limit de envíos SALIENTES por teléfono SIN actividad entrante del paciente.
//
// Defensa en profundidad contra la clase de bug del incidente 11/12-ago-2026 (bucle
// no-show↔re-escalación: 18 pacientes con 1 WhatsApp/min toda la noche, ~10.800 envíos
// cobrados). Los cortacircuitos específicos viven en sus flujos (guard __resume__, tope de
// escalaciones, claim-then-send); ESTE tope es el último recurso genérico: ningún bug futuro,
// venga de donde venga, puede convertirse en spam masivo a un paciente, porque el grifo se
// cierra en el único punto por el que todos los envíos salen del proceso.
//
// Semántica: cada mensaje ENTRANTE del paciente (RecordInbound, desde el webhook) resetea su
// cuota — una conversación real nunca choca con el tope porque el paciente sigue escribiendo.
// Solo un emisor DESBOCADO (que envía sin que el paciente responda) agota la cuota; el envío
// excedente se rechaza con ErrSendRateLimited y el primer bloqueo de la ventana se loguea en
// ERROR (→ alerta Telegram, visible para el auditor).
//
// Cubre los envíos al paciente: SendText, SendButtons, SendList, SendTemplate y PlaceCall.
// NO cubre SendInternalText (nota interna del Inbox, invisible al paciente y sin costo WA).

// ErrSendRateLimited es el rechazo de un envío por el tope saliente. NO es un fallo permanente
// del destinatario (IsPermanentSendError no lo incluye): el número es válido, el emisor está roto.
var ErrSendRateLimited = errors.New("send rate limit exceeded for phone (runaway sender guard)")

const sendRateWindow = 1 * time.Hour

type sendRateEntry struct {
	windowStart time.Time
	count       int
	alerted     bool // ya se alertó (ERROR) en esta ventana — los siguientes bloqueos van en Debug
}

// SetSendRateLimit fija el máximo de envíos salientes por teléfono por hora sin inbound del
// paciente. <=0 desactiva el tope (default en tests; en prod viene de SEND_RATE_LIMIT_PER_HOUR).
func (c *Client) SetSendRateLimit(perHour int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendRateLimit = perHour
	if c.sendRate == nil {
		c.sendRate = make(map[string]*sendRateEntry)
	}
}

// RecordInbound registra actividad ENTRANTE del paciente: resetea su cuota de salida.
// Lo llama el webhook al recibir cada mensaje entrante ya validado.
func (c *Client) RecordInbound(phone string) {
	if phone == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sendRate, phone)
}

// allowSend consume una unidad de cuota para el teléfono. Devuelve false si la ventana está
// agotada (el caller NO debe enviar). El primer bloqueo de cada ventana sube a ERROR.
func (c *Client) allowSend(phone string) bool {
	if phone == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sendRateLimit <= 0 {
		return true
	}
	if c.sendRate == nil {
		c.sendRate = make(map[string]*sendRateEntry)
	}
	now := time.Now()
	e := c.sendRate[phone]
	if e == nil || now.Sub(e.windowStart) >= sendRateWindow {
		c.sendRate[phone] = &sendRateEntry{windowStart: now, count: 1}
		return true
	}
	if e.count >= c.sendRateLimit {
		if !e.alerted {
			e.alerted = true
			slog.Error("send rate limit: envíos salientes bloqueados (emisor desbocado)",
				"phone", utils.MaskPhone(phone),
				"limit_per_hour", c.sendRateLimit,
				"window_start", e.windowStart.Format(time.RFC3339),
			)
		} else {
			slog.Debug("send rate limit: envío bloqueado (ya alertado en esta ventana)",
				"phone", utils.MaskPhone(phone))
		}
		return false
	}
	e.count++
	return true
}

// evictStaleSendRate purga entradas con ventana vencida (mantiene el mapa acotado en procesos
// de larga vida). Se llama desde el loop de limpieza periódica del cliente.
func (c *Client) evictStaleSendRate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-2 * sendRateWindow)
	for phone, e := range c.sendRate {
		if e.windowStart.Before(cutoff) {
			delete(c.sendRate, phone)
		}
	}
}

// sendRateSnapshot expone (para tests) el contador actual de un teléfono.
func (c *Client) sendRateSnapshot(phone string) (count int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.sendRate[phone]
	if !found {
		return 0, false
	}
	return e.count, true
}
