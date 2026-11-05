package main

import (
	"log/slog"
	"time"
)

// Plazos del apagado. El peor caso suma 67 s: HTTP (10) + workers (workersStopTimeout, a la par del
// scheduler, que son 20) + auditorías SIESA (10). Cabe en el stop_grace_period de 75 s del compose: si
// cambia, cambiar también el comentario de allá.
const (
	// responsesWaitTimeout cubre una respuesta a plantilla con SIESA colgada: los 30 s del ctx de
	// handleConfirmation más la marca y el envío del acuse. No cubre el peor caso teórico: con otros
	// 30 s esperando el candado del teléfono (notifLockTimeout). Una respuesta cortada en esa espera no
	// se pierde del todo, porque el pending sigue en la BD y se restaura al arrancar (el paciente puede
	// volver a pulsar y el IVR lo sigue viendo). Cortada después de sacar el pending, la pulsación se
	// pierde; queda el error en el log solo si la respuesta alcanza a fallar antes de que salga el proceso,
	// y si no, no queda ni eso.
	responsesWaitTimeout = 32 * time.Second
	// workersStopTimeout acota las respuestas más el Stop del pool, que drena hasta 15 s: 32 + 15. Residual
	// conocido: antes del drenado, Stop espera a los workers en vuelo sin plazo (wg.Wait), y un mensaje puede
	// durar hasta processMsgTimeout (2 min). Si pasa de 32 s le quita tiempo al drenado; si pasa de 47 s, el
	// drenado no llega a correr y se pierde el mensaje virtual encolado, que no tiene fila en el WAL. El mismo
	// plazo de 47 s tampoco espera a un mensaje drenado que se alargue: los 15 s solo se miran entre mensajes.
	workersStopTimeout = 47 * time.Second
)

// stopWorkers para el pool DESPUÉS de esperar las respuestas a plantillas en vuelo, en ese orden y bajo
// un mismo plazo (D-10): una respuesta puede encolar un mensaje virtual (los horarios de «Agendar», la
// pregunta de «Cancelar») que no tiene fila en el WAL, y solo lo procesa el drenado de Stop. Con los
// workers ya fuera, lo que entrara a la cola después del drenado no lo tomaría nadie. Los workers en
// vuelo siguen terminando mientras tanto. Devuelve false si venció el plazo.
func stopWorkers(waitResponses func(time.Duration), stopPool func(), responsesWait, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitResponses(responsesWait)
		stopPool()
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		slog.Error("shutdown timeout: worker pool did not stop in time", "timeout", timeout)
		return false
	}
}
