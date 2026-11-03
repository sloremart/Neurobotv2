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

// RunPass corre UNA pasada de un bucle de fondo con un plazo derivado de ctx (timeout<=0 =
// BackgroundPassTimeout). Si la pasada vence deja UNA línea de error con el nombre del bucle (que
// el AlertHandler sube a Telegram) y devuelve true; la siguiente pasada arranca normal. Un corte
// por el apagado (ctx cancelado) no es una pasada vencida.
func RunPass(ctx context.Context, loop string, timeout time.Duration, pass func(context.Context)) bool {
	if timeout <= 0 {
		timeout = BackgroundPassTimeout
	}
	passCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pass(passCtx)
	if !errors.Is(passCtx.Err(), context.DeadlineExceeded) || ctx.Err() != nil {
		return false
	}
	slog.Error("bucle de fondo: la pasada venció su plazo, la siguiente arranca normal",
		"loop", loop, "timeout", timeout)
	return true
}
