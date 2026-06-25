package domain

import "time"

// FlowEvent es una fila de la traza de negocio (tabla flow_events). Vive en domain (paquete hoja)
// para que tanto el productor (observability) como la persistencia (repository/local) y la API la
// usen sin ciclos de import. Ver docs/OBSERVABILIDAD.md.
type FlowEvent struct {
	ID        int64
	TraceID   string
	Flow      string
	Step      string
	Level     int // 1=error 2=outcome 3=milestone 4=full
	Outcome   string
	Reason    string
	Phone     string // enmascarado
	RefType   string
	RefID     string
	Attrs     map[string]interface{}
	CreatedAt time.Time
}

// FlowStatCount es un conteo agregado (key = step/outcome/reason).
type FlowStatCount struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// FlowStats es el agregado de un flujo en una ventana: funnel por step, distribución de
// terminales por outcome, y conteo por reason (tasas/errores). Ver §5 de docs/OBSERVABILIDAD.md.
type FlowStats struct {
	ByStep    []FlowStatCount `json:"by_step"`
	ByOutcome []FlowStatCount `json:"by_outcome"`
	ByReason  []FlowStatCount `json:"by_reason"`
}
