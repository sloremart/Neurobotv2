package statemachine

import "time"

// StateResult es el resultado de ejecutar un state handler
type StateResult struct {
	NextState string            // Siguiente estado
	Messages  []OutboundMessage // Mensajes a enviar al usuario
	UpdateCtx map[string]string // Contexto a guardar/actualizar
	ClearCtx  []string          // Claves de contexto a borrar
	Events    []Event           // Eventos para tracking

	// ExtendExpiryUntil, si no es cero, es hasta cuándo tiene que seguir VIVA la sesión
	// (expires_at). Lo pide el paso que sabe que el caso va a quedar esperando más que el TTL
	// normal —la escalación encolada para el próximo día hábil— y lo aplica quien persiste
	// (worker.sendAndSave). Es un instante ABSOLUTO porque quien lo calcula es el único que
	// conoce el horario de atención; nunca acorta el vencimiento que la sesión ya tenga.
	ExtendExpiryUntil time.Time
}

// Event representa un evento para tracking/KPI
type Event struct {
	Type string
	Data map[string]interface{}
}

// NewResult crea un StateResult con el estado de destino
func NewResult(nextState string) *StateResult {
	return &StateResult{NextState: nextState}
}

// WithText agrega un mensaje de texto
func (r *StateResult) WithText(text string) *StateResult {
	r.Messages = append(r.Messages, &TextMessage{Text: text})
	return r
}

// WithButtons agrega un mensaje con botones
func (r *StateResult) WithButtons(text string, buttons ...Button) *StateResult {
	r.Messages = append(r.Messages, &ButtonMessage{Text: text, Buttons: buttons})
	return r
}

// WithList agrega un mensaje con lista interactiva
func (r *StateResult) WithList(body, title string, sections ...ListSection) *StateResult {
	r.Messages = append(r.Messages, &ListMessage{Body: body, Title: title, Sections: sections})
	return r
}

// WithContext agrega un par clave-valor al contexto a guardar
func (r *StateResult) WithContext(key, value string) *StateResult {
	if r.UpdateCtx == nil {
		r.UpdateCtx = make(map[string]string)
	}
	r.UpdateCtx[key] = value
	return r
}

// WithContextMap agrega múltiples pares clave-valor al contexto
func (r *StateResult) WithContextMap(kvs map[string]string) *StateResult {
	if r.UpdateCtx == nil {
		r.UpdateCtx = make(map[string]string)
	}
	for k, v := range kvs {
		r.UpdateCtx[k] = v
	}
	return r
}

// WithClearCtx agrega claves de contexto a borrar
func (r *StateResult) WithClearCtx(keys ...string) *StateResult {
	r.ClearCtx = append(r.ClearCtx, keys...)
	return r
}

// WithExpiryUntil pide que la sesión siga viva hasta ese instante (ver ExtendExpiryUntil).
func (r *StateResult) WithExpiryUntil(until time.Time) *StateResult {
	r.ExtendExpiryUntil = until
	return r
}

// WithEvent agrega un evento de tracking
func (r *StateResult) WithEvent(eventType string, data map[string]interface{}) *StateResult {
	r.Events = append(r.Events, Event{Type: eventType, Data: data})
	return r
}
