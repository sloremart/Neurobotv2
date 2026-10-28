package statemachine

import (
	"encoding/json"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// EL DESCRIPTOR `input` DEL PROTOCOLO DE ESCALADO v1.
//
// `bot_return_menu` tiene dos formas —`menu` y `entrada`— que salen del MISMO registro por paso,
// y Néctar deduce en silencio cuál pintar. v1 las unifica en una sola clave que el BOT dicta:
// con 0 campos se pintan solo los botones y con 1 un dato. La forma la fija el esquema de
// Néctar, que es `additionalProperties:false`: aquí no sobra ni falta una clave, y todas van en
// español porque son literales de cable.
//
// LA FUENTE NO CAMBIA: se leen los MISMOS registros que `ReturnMenu` (las opciones y la entrada
// del paso), así que esto es un cambio de forma de emisión y no una segunda declaración.

// InputKindText es el `tipo` de campo que este bot emite: los pasos de Neuro solo le piden al agente texto. Un
// tipo nuevo se añade aquí Y al registro de capacidades en el mismo cambio
// (TestElRegistroDelDescriptorNoMiente los cruza).
const InputKindText = "texto"

// InputFieldKey es la `clave` del campo único de una entrada registrada: la que Néctar devuelve
// dentro de `valores`. Es vocabulario nuestro y es una sola porque un paso ofrece UN campo.
const InputFieldKey = "dato"

// InputDescriptor es la clave `input` del escalado v1.
type InputDescriptor struct {
	// Step es el paso del bot, OPACO y sin enum: el interceptor transversal escala desde
	// cualquier paso, así que cerrar la lista aquí dejaría fuera al caso que importa.
	Step string `json:"paso"`
	// Options nunca va vacía: las dos universales (devolver / cerrar) van en todos los menús.
	Options []InputOption `json:"opciones"`
	// Fields son los campos del paso. Ausente = el paso se resuelve eligiendo.
	Fields []InputField `json:"campos,omitempty"`
	// MinFields es `minimo_campos`: cuántos campos hay que llenar. AUSENTE = el paso no exige
	// ninguno; el `omitempty` es contrato y no ahorro de bytes, porque Néctar no inventa un
	// mínimo que el bot no declare.
	MinFields int `json:"minimo_campos,omitempty"`
	// ResumesAt es el paso en el que se retoma con lo escrito. "" = el mismo en el que se
	// escaló. Sin él, un paso que no sabe recibir lo que trae el agente volvería a escalar en
	// bucle.
	ResumesAt string `json:"retoma_en,omitempty"`
}

// InputOption es un botón de retorno. Las tres claves son obligatorias en el esquema.
type InputOption struct {
	Label string `json:"rotulo"`
	Value string `json:"valor"`
	// Exit es el vocabulario cerrado de tres literales (reanudar, reanudar_sin_dato, cerrar).
	// NUNCA va ausente: se deriva de Close/NoInput igual que el `tipo` del dialecto.
	Exit string `json:"salida"`
}

// InputField es UN campo del paso. `clave`, `rotulo` y `tipo` son obligatorias; el resto se emite
// solo donde el bot lo declara: `largo` ausente no es un tope de cero, y `patron` ausente no es
// una regla vacía. `requerido` solo viaja como true (el bot no declara «opcional a propósito»).
type InputField struct {
	Key      string `json:"clave"`
	Label    string `json:"rotulo"`
	Kind     string `json:"tipo"`
	Required bool   `json:"requerido,omitempty"`
	Pattern  string `json:"patron,omitempty"`
	Format   string `json:"formato,omitempty"`
	Help     string `json:"ayuda,omitempty"`
	MaxRunes int    `json:"largo,omitempty"`
}

// InputDescriptor arma el descriptor de un paso. Es hermana de ReturnMenu y método de la máquina
// por la misma razón: la ayuda por omisión sale de la config del paso. Recibe la sesión y no
// tiene caché: un descriptor cacheado por paso congelaría lo que dependa de la sesión.
func (m *Machine) InputDescriptor(sess *session.Session, step string) InputDescriptor {
	d := InputDescriptor{Step: step, Options: descriptorOptions(sess, step)}

	in, offers := returnInputFor(step)
	if !offers {
		return d
	}
	field := InputField{
		Key: InputFieldKey, Label: in.Label, Kind: in.Kind, Help: in.Help,
		Required: in.Required, Pattern: in.Pattern, MaxRunes: in.MaxLen,
	}
	// Rótulo y ayuda salen de donde ya salían en el dialecto: el registro, o el ErrorMsg del
	// paso. Rellenarlo aquí evita la segunda copia que se queda vieja.
	if field.Kind == "" {
		field.Kind = InputKindText
	}
	if field.Label == "" {
		field.Label = defaultReturnInputLabel
	}
	if field.Help == "" {
		field.Help = m.defaultHelp(step)
	}
	d.Fields = []InputField{field}
	d.ResumesAt = in.Target
	// Un campo único requerido ES un mínimo de uno: el bot rechaza el vacío.
	if in.Required {
		d.MinFields = 1
	}
	return d
}

// descriptorOptions son los botones del paso en la forma del descriptor: las salidas del paso y
// las dos universales al final, las MISMAS que BuildReturnMenu. La comparten la máquina y
// StaticInputDescriptor para que las dos formas de armar el descriptor no puedan separarse.
func descriptorOptions(sess *session.Session, step string) []InputOption {
	opts := ReturnOptionsFor(sess, step)
	out := make([]InputOption, 0, len(opts))
	for _, o := range opts {
		// Misma regla que el dialecto: una opción ilegible descarta LA OPCIÓN, no el menú.
		if o.Label == "" || o.Value == "" {
			slog.Warn("descriptor de entrada: opción descartada por venir incompleta",
				"step", step, "rotulo", o.Label, "valor", o.Value)
			continue
		}
		out = append(out, InputOption{Label: o.Label, Value: o.Value, Exit: returnOptionExit(o)})
	}
	return out
}

// StaticInputDescriptor serializa el descriptor `input` de un paso de menú ESTÁTICO (sin sesión
// ni máquina): quien escala sin tener la máquina, como el NotificationManager, lo arma con él.
// Solo lleva `paso` y `opciones` y SOLO sirve para pasos de menú estático: un paso que pide un
// campo al agente necesita la máquina (la ayuda por omisión sale de su config) y debe usar
// Machine.InputDescriptor. Llamarla con un paso que tiene ReturnInput registrado es un error de
// programación: se registra como ERROR y devuelve nil, en vez de emitir un descriptor sin
// `campos`. No hace panic porque quien llama es el NotificationManager en producción, y un panic
// ahí tumbaría el envío; el test que compara con la máquina (return_menus_test.go) lo atrapa en
// CI. También devuelve nil si no se puede serializar; nil significa «no viaja».
func StaticInputDescriptor(step string) []byte {
	if _, offers := returnInputFor(step); offers {
		slog.Error("StaticInputDescriptor: el paso ofrece un ReturnInput; usar Machine.InputDescriptor",
			"step", step)
		return nil
	}
	raw, err := json.Marshal(InputDescriptor{Step: step, Options: descriptorOptions(nil, step)})
	if err != nil {
		slog.Error("descriptor de entrada estático no serializable", "error", err, "step", step)
		return nil
	}
	return raw
}
