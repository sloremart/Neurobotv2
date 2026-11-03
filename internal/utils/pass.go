package utils

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// BackgroundPassTimeout es el plazo por defecto de UNA pasada de un bucle de fondo (D-07). Igual
// que la cota por mensaje del worker (processMsgTimeout): generoso para una pasada normal y corto
// frente a un MySQL colgado, que sin plazo dejaría el bucle bloqueado para siempre.
const BackgroundPassTimeout = 2 * time.Minute

// RowTimeout acota el trabajo de UNA fila de una pasada (RowContext). Igual que la cota por mensaje
// del worker: por encima de la llamada externa sin ctx más larga de una fila (un POST a Bird vence a
// los 30 s, CloseFeedItems hace varios), para que la fila no se corte antes de terminar el envío.
const RowTimeout = 2 * time.Minute

// MarkTimeout acota la escritura que registra un envío que ya salió (MarkContext).
const MarkTimeout = 10 * time.Second

// appCtxKey guarda en el ctx de la pasada el ctx del que deriva (el de la app), para RowContext.
type appCtxKey struct{}

// rowTimeoutKey sobreescribe RowTimeout en un ctx (WithRowTimeout).
type rowTimeoutKey struct{}

// WithRowTimeout fija en ctx el plazo de las filas que deriven de él (RowContext); para las pruebas.
func WithRowTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, rowTimeoutKey{}, d)
}

// RunPass corre UNA pasada de un bucle de fondo con un plazo derivado de ctx (timeout<=0 =
// BackgroundPassTimeout). El plazo decide solo si la pasada sigue arrancando filas: cada fila corre
// con RowContext, que no lo hereda. Si la pasada vence deja UNA línea de error con el nombre del
// bucle (que el AlertHandler sube a Telegram) y devuelve true; la siguiente pasada arranca normal.
// Un corte por el apagado (ctx cancelado) no es una pasada vencida.
func RunPass(ctx context.Context, loop string, timeout time.Duration, pass func(context.Context)) bool {
	if timeout <= 0 {
		timeout = BackgroundPassTimeout
	}
	passCtx, cancel := context.WithTimeout(context.WithValue(ctx, appCtxKey{}, ctx), timeout)
	defer cancel()
	pass(passCtx)
	if !errors.Is(passCtx.Err(), context.DeadlineExceeded) || ctx.Err() != nil {
		return false
	}
	slog.Error("bucle de fondo: la pasada venció su plazo, la siguiente arranca normal",
		"loop", loop, "timeout", timeout)
	return true
}

// RowContext da el ctx de UNA fila de la pasada passCtx: acotado a RowTimeout y derivado del ctx de
// la app, no del plazo de la pasada. Una fila que ya empezó termina sus escrituras (un envío queda
// con su marca) aunque la pasada venza a mitad; sí se corta con el apagado. Fuera de RunPass deriva
// de passCtx tal cual.
func RowContext(passCtx context.Context) (context.Context, context.CancelFunc) {
	parent := passCtx
	if app, ok := passCtx.Value(appCtxKey{}).(context.Context); ok {
		parent = app
	}
	timeout := RowTimeout
	if d, ok := passCtx.Value(rowTimeoutKey{}).(time.Duration); ok && d > 0 {
		timeout = d
	}
	return context.WithTimeout(parent, timeout) //nolint:gosec // G118: el cancel se devuelve; lo llama quien recibe la fila.
}

// MarkContext da un ctx NUEVO y corto (MarkTimeout) para registrar un envío que ya salió, o el
// cierre de la fila del WAL de un turno que ya terminó. Se crea DESPUÉS del trabajo y no hereda ni el
// plazo ni la cancelación de ctx: un envío o un turno lento queda marcado igual, y no se repite en
// la pasada siguiente ni en el replay.
func MarkContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), MarkTimeout) //nolint:gosec // G118: el cancel se devuelve; lo llama quien marca.
}
