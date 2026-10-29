package nectar

// LOS HECHOS DEL CASO (`facts` del sobre de escalado).
//
// Néctar lleva additionalProperties:false: las etiquetas JSON de estos tipos son las del esquema
// y renombrarlas o añadir una rompe la validación. Los resúmenes de los handlers
// (summaryGroup) y los de las notificaciones arman sus grupos con ESTOS tipos y estas claves, de
// modo que el mismo dato se llama igual venga de donde venga.

// FactField es un par del resumen estructurado. El rótulo es opcional: un par sin él sigue siendo
// legible por su clave.
type FactField struct {
	Key   string `json:"clave"`
	Label string `json:"rotulo,omitempty"`
	Value string `json:"valor"`
}

// FactGroup es un bloque de hechos. `titulo` NULO es un valor legal («bloque sin título») y por
// eso es puntero y NO lleva omitempty.
type FactGroup struct {
	Key    string      `json:"clave"`
	Title  *string     `json:"titulo"`
	Fields []FactField `json:"campos,omitempty"`
}

// FactSpec nombra un campo: su clave de cable y su rótulo.
type FactSpec struct{ Key, Label string }

// Campos del grupo `cita` (la cita a la que se refiere el caso). Los comparten el resumen de los
// handlers y el de las notificaciones.
var (
	FactAppointmentID   = FactSpec{"id", "Cita"}
	FactAppointmentDate = FactSpec{"fecha", "Fecha"}
	FactAppointmentTime = FactSpec{"hora", "Hora"}
	FactProcedure       = FactSpec{"procedimiento", "Procedimiento de la cita"}
	FactNotifType       = FactSpec{"tipo_notificacion", "Notificación"}
)

// FactPatientName es el campo del grupo `paciente`.
var FactPatientName = FactSpec{"nombre", "Paciente"}

// Field arma el campo de una especificación.
func (s FactSpec) Field(value string) FactField {
	return FactField{Key: s.Key, Label: s.Label, Value: value}
}
