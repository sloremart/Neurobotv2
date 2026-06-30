package statemachine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// StateHandler procesa un mensaje en un estado específico
type StateHandler func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error)

// Interceptor evalúa un mensaje antes de que llegue al handler.
// Retorna (result, true) si interceptó; (nil, false) si no.
type Interceptor func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool)

// RecoveryCoordinator es la capa de recuperación asistida por IA (opcional, inyectable).
// La FSM la invoca en dos puntos (ver docs/RECUPERACION-IA.md §2.1):
//   - TryStart: antes de escalar a humano (primer disparo); retorna (result, true) si toma el
//     control en lugar de escalar, (nil, false) para escalar de verdad.
//   - Active/Handle: mientras el modo recuperación esté activo, procesa cada mensaje del paciente
//     (interceptor); Handle retorna (result, true) si lo atiende.
//
// nil = capa desactivada → comportamiento idéntico al actual.
type RecoveryCoordinator interface {
	Active(sess *session.Session) bool
	Handle(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool)
	TryStart(ctx context.Context, sess *session.Session, msg bird.InboundMessage, blockedState string) (*StateResult, bool)
}

// Machine es el motor de la máquina de estados
type Machine struct {
	handlers     map[string]StateHandler
	interceptors []Interceptor
	// configs retiene la HandlerConfig declarativa por estado (validador puro, opciones,
	// metadatos de recuperación IA). La capa de recuperación la consulta para validar el
	// input sin re-correr Process. Solo se llena para estados registrados con RegisterWithConfig.
	configs map[string]HandlerConfig
	// recovery es la capa de recuperación asistida por IA (opcional). nil = desactivada.
	recovery RecoveryCoordinator
}

// SetRecoveryCoordinator inyecta la capa de recuperación IA (opcional). Se llama desde main
// después de construir la máquina. nil mantiene el comportamiento actual.
func (m *Machine) SetRecoveryCoordinator(rc RecoveryCoordinator) {
	m.recovery = rc
}

// NewMachine crea una nueva máquina de estados
func NewMachine() *Machine {
	return &Machine{
		handlers: make(map[string]StateHandler),
		configs:  make(map[string]HandlerConfig),
	}
}

// Register registra un handler para un estado
func (m *Machine) Register(state string, handler StateHandler) {
	m.handlers[state] = handler
}

// Config devuelve la HandlerConfig declarativa de un estado, si fue registrado con
// RegisterWithConfig. ok=false si el estado no tiene config declarativa.
func (m *Machine) Config(state string) (HandlerConfig, bool) {
	cfg, ok := m.configs[state]
	return cfg, ok
}

// AddInterceptor agrega un interceptor global
func (m *Machine) AddInterceptor(interceptor Interceptor) {
	m.interceptors = append(m.interceptors, interceptor)
}

// Process ejecuta la máquina de estados para un mensaje entrante
func (m *Machine) Process(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
	var result *StateResult

	// 1. Ejecutar interceptores globales
	intercepted := false
	for _, interceptor := range m.interceptors {
		if r, ok := interceptor(ctx, sess, msg); ok {
			result = r
			intercepted = true
			break
		}
	}

	if !intercepted {
		// 2. Buscar handler para el estado actual
		handler, ok := m.handlers[sess.CurrentState]
		if !ok {
			return nil, fmt.Errorf("no handler registered for state: %s", sess.CurrentState)
		}

		// 3. Ejecutar handler
		prevState := sess.CurrentState
		var err error
		result, err = handler(ctx, sess, msg)
		if err != nil {
			return nil, fmt.Errorf("handler %s: %w", sess.CurrentState, err)
		}

		// Reset RetryCount when transitioning to a different state
		if result.NextState != prevState {
			sess.RetryCount = 0
		}
	}

	// 4. Si el resultado lleva a un estado automático, ejecutarlo encadenado
	const maxAutoChain = 20 // Cycle guard: prevent infinite loops
	visited := make(map[string]bool)
	for i := 0; IsAutomatic(result.NextState) && result.NextState != sess.CurrentState; i++ {
		if i >= maxAutoChain {
			slog.Error("auto-chain cycle guard triggered",
				"iterations", i,
				"current_state", sess.CurrentState,
				"next_state", result.NextState,
			)
			break
		}
		if visited[result.NextState] {
			slog.Error("auto-chain cycle detected",
				"state", result.NextState,
				"visited", visited,
			)
			break
		}
		visited[result.NextState] = true

		slog.Debug("auto-chaining state",
			"from", sess.CurrentState,
			"to", result.NextState,
		)

		// Save previous state for auto-chained handlers (e.g., escalation needs origin state)
		sess.SetContext("_pre_auto_state", sess.CurrentState)

		// Actualizar estado en session (para que el handler automático lo vea)
		sess.CurrentState = result.NextState

		autoHandler, ok := m.handlers[result.NextState]
		if !ok {
			break // No hay handler para ese estado, parar
		}

		// Preservar mensajes y eventos acumulados
		prevMessages := result.Messages
		prevEvents := result.Events
		prevCtx := result.UpdateCtx
		prevClear := result.ClearCtx

		// Aplicar contexto pendiente a la sesión para que el auto handler pueda leerlo
		for k, v := range prevCtx {
			sess.SetContext(k, v)
		}
		// Aplicar borrado pendiente en memoria (evita que contexto del grupo anterior
		// se filtre al siguiente grupo durante el auto-chain)
		for _, k := range prevClear {
			if k != "__all__" {
				delete(sess.Context, k)
			}
		}

		autoResult, err := autoHandler(ctx, sess, msg)
		if err != nil {
			return nil, fmt.Errorf("auto handler %s: %w", result.NextState, err)
		}

		// Combinar resultados: mensajes previos + nuevos
		autoResult.Messages = append(prevMessages, autoResult.Messages...)
		autoResult.Events = append(prevEvents, autoResult.Events...)

		// Merge contexto (prev no sobreescribe auto)
		if autoResult.UpdateCtx == nil {
			autoResult.UpdateCtx = prevCtx
		} else if prevCtx != nil {
			for k, v := range prevCtx {
				if _, exists := autoResult.UpdateCtx[k]; !exists {
					autoResult.UpdateCtx[k] = v
				}
			}
		}
		autoResult.ClearCtx = append(prevClear, autoResult.ClearCtx...)

		result = autoResult
	}

	return result, nil
}
