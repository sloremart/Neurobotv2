package main

import (
	"testing"
	"time"
)

// D-10: lo que una respuesta en vuelo encola antes de terminar lo toma el drenado de Stop, porque Stop
// corre después de esperarla. Al revés, el mensaje virtual se queda en una cola sin consumidor.
func TestStopWorkers_DrainSeesWhatResponsesEnqueued(t *testing.T) {
	queue := make(chan string, 1)
	drained := ""
	waitResponses := func(time.Duration) { queue <- "virtual" }
	stopPool := func() {
		select {
		case drained = <-queue:
		default:
		}
	}

	if !stopWorkers(waitResponses, stopPool, time.Second, 5*time.Second) {
		t.Fatal("no debía vencer el plazo")
	}
	if drained != "virtual" {
		t.Fatal("el drenado de Stop debe ver el mensaje que encoló la respuesta")
	}
}

// Las respuestas y el Stop van bajo un mismo plazo: uno que se cuelga no alarga el apagado.
func TestStopWorkers_SharedDeadline(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	if stopWorkers(func(time.Duration) {}, func() { <-block }, time.Second, 50*time.Millisecond) {
		t.Fatal("con Stop colgado debe vencer el plazo")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("el plazo no se respetó: %v", elapsed)
	}
}

// Los plazos caben en el stop_grace_period del compose (75 s), con margen para salir.
func TestShutdownBudgetFitsGracePeriod(t *testing.T) {
	const (
		httpShutdown = 10 * time.Second
		audits       = 10 * time.Second
		gracePeriod  = 75 * time.Second
	)
	if total := httpShutdown + workersStopTimeout + audits; total > gracePeriod-5*time.Second {
		t.Fatalf("el apagado suma %v; no cabe en %v", total, gracePeriod)
	}
	if responsesWaitTimeout >= workersStopTimeout {
		t.Fatal("el drenado necesita tiempo después de esperar las respuestas")
	}
}
