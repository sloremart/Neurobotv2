package statemachine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// El MENÚ DE RETORNOS: lo que el bot le ofrece al agente humano al escalar, para que le devuelva
// la conversación con una decisión.
//
// # Por qué producir y resolver viven en la MISMA tabla
//
// Néctar recibe el menú como DATO OPACO —no lo interpreta—, lo sella y devuelve el `valor`
// elegido tal cual. El vocabulario es nuestro y nadie más lo valida: si el token se produjera en
// un sitio y se resolviera en otro, el día que uno de los dos cambie el agente elegiría una
// opción que el bot ya no sabe ejecutar y el paciente se quedaría esperando, sin error visible.
// Por eso cada ReturnOption lleva a la vez lo que el agente LEE (Label), el token que viaja
// (Value) y lo que el bot HACE al recibirlo (Target/Data/Close).

// ReturnOption es una salida válida desde un paso.
type ReturnOption struct {
	// Label es lo que el agente humano lee en su pantalla de Néctar.
	Label string
	// Value es NUESTRO token interno. Viaja opaco y vuelve idéntico.
	Value string
	// Target es el estado en el que se retoma. "" = el paso en el que se escaló.
	Target string
	// Data se inyecta como si el paciente lo hubiera escrito (mensaje virtual), que es como el
	// bot ya sabe reanudar con una decisión tomada por el agente. "" = ninguno.
	Data string
	// Close cierra el caso sin reanudar el flujo.
	Close bool
	// NoInput dice que ESTA opción reanuda SIN exigir el campo escrito del paso: la interfaz de
	// Néctar solo libera el botón sin dato para "cerrar", así que una salida que no lleva ningún
	// dato quedaría deshabilitada en un paso con campo. Un `tipo` que su interfaz aún no conozca
	// se trata como "reanudar" (lectura conservadora, exige el dato).
	NoInput bool
}

// Tokens de control, válidos desde CUALQUIER paso. Van en todos los menús porque son las dos
// salidas que siempre existen: devolver tal cual, o dar el caso por terminado.
const (
	ReturnResume = "bot_reanudar"
	ReturnClose  = "bot_cerrar"
)

// ReturnValueQueueExpired es el token con que Néctar DEVUELVE al bot una conversación que pasó 8
// horas hábiles en cola sin que ningún asesor la tomara. NO es una opción de ningún menú: no lo
// eligió un agente, lo declaró el barredor. Por eso el worker lo reconoce SIEMPRE, antes de
// traducir el `return_value` contra el menú del paso.
const ReturnValueQueueExpired = "cola_vencida"

// ReturnInputPrefix marca lo que el AGENTE ESCRIBIÓ, frente a los tokens del menú. El prefijo
// hace IMPOSIBLE confundir un dato escrito con un token de menú: un token que el bot no
// reconoce significa otra cosa (menú viejo, o el paso cambió) y se reanuda a propósito sin
// decidir; inyectarlo como dato sería justo el error que esa regla evita.
const ReturnInputPrefix = "bot_dato:"

// ReturnMenu es la forma EXACTA que viaja en `bot_return_menu`. Las claves van en español porque
// son literales de cable.
//
// ⚠️ LA CLAVE DEL ARREGLO SE LLAMA `menu`: Néctar guarda esto OPACO y el único que lo lee es la
// interfaz del agente; si el arreglo no se llama `menu` o una opción no trae `etiqueta` y
// `valor`, el agente NO VE NINGÚN BOTÓN, sin error en ninguna parte.
type ReturnMenu struct {
	PasoActual string             `json:"paso_actual"`
	Menu       []ReturnMenuOption `json:"menu"`
	// Entrada va SOLO en los pasos que esperan un dato escrito. Ausente = ese paso se resuelve
	// eligiendo, y la interfaz no debe pintar ningún campo de texto.
	Entrada *ReturnMenuInput `json:"entrada,omitempty"`
}

// ReturnMenuOption es una opción tal como la interfaz del agente la pinta. `etiqueta` y `valor`
// son obligatorias y ninguna puede ir vacía. `tipo` viaja SIEMPRE, con un vocabulario CERRADO de
// tres literales de cable:
//
//   - "reanudar"          → devuelve la conversación al bot en un paso (el mismo, o el de Target).
//     En un paso con campo de texto la interfaz EXIGE el dato escrito.
//   - "cerrar"            → da el caso por terminado; no hay paso al que volver.
//   - "reanudar_sin_dato" → reanuda como "reanudar", pero sin exigir el campo escrito.
//
// Un `tipo` desconocido se debe tratar como "reanudar": asumir "cerrar" por error deja un caso
// abandonado sin que nadie lo note.
type ReturnMenuOption struct {
	Etiqueta string `json:"etiqueta"`
	Valor    string `json:"valor"`
	Tipo     string `json:"tipo"`
}

// Los tres literales de `tipo` (y de `salida` del descriptor v1), de cable.
const (
	returnMenuTipoResume        = "reanudar"
	returnMenuTipoClose         = "cerrar"
	returnMenuTipoResumeNoInput = "reanudar_sin_dato"
)

// ReturnMenuInput es el descriptor tal como viaja en `bot_return_menu.entrada`.
type ReturnMenuInput struct {
	Etiqueta     string `json:"etiqueta"`
	Ayuda        string `json:"ayuda,omitempty"`
	PrefijoValor string `json:"prefijo_valor"`
}

// ReturnOptionsBuilder produce las opciones de un paso. Recibe la sesión porque algunos pasos
// dependen de ella; los estáticos la ignoran y por eso pueden construirse con sesión nil.
type ReturnOptionsBuilder func(sess *session.Session) []ReturnOption

var (
	returnMenusMu sync.RWMutex
	returnMenus   = map[string]ReturnOptionsBuilder{}
	returnInputs  = map[string]ReturnInput{}
	resolveGuards = map[string]string{}
)

// RegisterReturnMenu declara las salidas de un paso. Se llama donde se registra el estado,
// pegado a los payloads que refleja.
func RegisterReturnMenu(state string, build ReturnOptionsBuilder) {
	returnMenusMu.Lock()
	defer returnMenusMu.Unlock()
	returnMenus[state] = build
}

// RegisterStaticReturnMenu es RegisterReturnMenu para pasos cuyas salidas no dependen de la
// sesión.
func RegisterStaticReturnMenu(state string, options ...ReturnOption) {
	RegisterReturnMenu(state, func(*session.Session) []ReturnOption { return options })
}

// ReturnOptionsFor son las salidas de un paso MÁS las dos universales, en ese orden: lo
// específico primero, porque es lo que el agente busca. Con un menú estático, sess puede ser nil.
func ReturnOptionsFor(sess *session.Session, step string) []ReturnOption {
	returnMenusMu.RLock()
	build := returnMenus[step]
	returnMenusMu.RUnlock()

	var opts []ReturnOption
	if build != nil {
		opts = append(opts, build(sess)...)
	}
	return append(opts,
		ReturnOption{Label: "Devolver al bot en el mismo paso", Value: ReturnResume},
		ReturnOption{Label: "Caso cerrado, no reanudar", Value: ReturnClose, Close: true},
	)
}

// BuildReturnMenu arma el menú que viaja en el escalate.
func BuildReturnMenu(sess *session.Session, step string) ReturnMenu {
	opts := ReturnOptionsFor(sess, step)
	menu := ReturnMenu{PasoActual: step, Menu: make([]ReturnMenuOption, 0, len(opts))}
	for _, o := range opts {
		// Una opción sin etiqueta o sin valor es ilegible para la interfaz del agente. Se
		// descarta ESA, no el menú entero: dejar al agente sin ninguna salida es peor.
		if o.Label == "" || o.Value == "" {
			slog.Warn("menú de retornos: opción descartada por venir incompleta",
				"step", step, "etiqueta", o.Label, "valor", o.Value)
			continue
		}
		menu.Menu = append(menu.Menu, ReturnMenuOption{Etiqueta: o.Label, Valor: o.Value, Tipo: returnOptionExit(o)})
	}
	return menu
}

// returnOptionExit deriva el tipo de salida de una opción. Lo comparten el `tipo` del dialecto y
// el `salida` del descriptor v1: son el MISMO vocabulario, y dos derivaciones separadas acabarían
// discrepando justo en el caso raro.
//
// CERRAR MANDA sobre NoInput: decir "reanudar_sin_dato" de algo que el bot CIERRA pintaría un
// botón que promete volver al flujo mientras el caso se cierra a espaldas del agente. Al revés no
// se pierde nada: "cerrar" ya está exento del campo escrito.
func returnOptionExit(o ReturnOption) string {
	switch {
	case o.Close:
		return returnMenuTipoClose
	case o.NoInput:
		return returnMenuTipoResumeNoInput
	default:
		return returnMenuTipoResume
	}
}

// ResolveReturnValue traduce el token que el agente eligió a lo que el bot debe hacer. ok=false
// cuando el token no pertenece a ese paso: quien llama NO debe adivinar, reanudar sin decisión es
// lo correcto porque una rama equivocada es peor que ninguna.
func ResolveReturnValue(sess *session.Session, step, value string) (ReturnOption, bool) {
	if value == "" {
		return ReturnOption{}, false
	}
	for _, o := range ReturnOptionsFor(sess, step) {
		if o.Value == value {
			return o, true
		}
	}
	return ReturnOption{}, false
}

// RegisterResolveGuard declara que las escalaciones DESDE ese paso viajan con candado en el
// botón «Resolver» de Néctar, y con qué motivo. El motivo es vocabulario acordado con Néctar, no
// texto libre. Un motivo vacío equivale a no tener candado.
func RegisterResolveGuard(state, motivo string) {
	returnMenusMu.Lock()
	defer returnMenusMu.Unlock()
	resolveGuards[state] = motivo
}

// ResolveGuardFor es el motivo del candado de un paso. ok=false —o motivo vacío— significa que
// la escalación sale SIN candado.
func ResolveGuardFor(state string) (string, bool) {
	returnMenusMu.RLock()
	defer returnMenusMu.RUnlock()
	motivo, ok := resolveGuards[state]
	return motivo, ok && motivo != ""
}

// ReturnValueForLog rinde un token del dialecto para el REGISTRO: si lleva prefijo de dato, solo
// el prefijo; si no, el token entero. Un token SIN prefijo lo eligió el agente de un menú que
// armó el propio bot (vocabulario nuestro, se puede trazar); con prefijo, lo que sigue lo TECLEÓ
// una persona sobre un paciente y no entra en ningún registro. Vive junto al prefijo porque lo
// traza más de un paquete.
func ReturnValueForLog(token string) string {
	if strings.HasPrefix(token, ReturnInputPrefix) {
		return ReturnInputPrefix
	}
	return token
}

// ReturnInput describe el campo de texto que un paso ofrece al agente. Registrarlo es lo que
// hace que el paso ofrezca campo: sin registro no se pinta ninguno ni se acepta dato escrito.
// Kind, Label, Help, Required, MaxLen y Pattern describen el campo único del descriptor v1;
// MaxLen y Pattern se PUBLICAN para que la pantalla del agente pre-valide, pero la autoridad es
// Validate (el bot es el único validador).
type ReturnInput struct {
	// Kind es el `tipo` del campo (InputKindText o InputKindLongText). "" = InputKindText.
	Kind string
	// Label es el rótulo del campo en la pantalla del agente.
	Label string
	// Help explica el formato esperado. Por omisión sale del ErrorMsg del paso cuando este espera
	// texto: es el texto que el bot ya le da al paciente cuando se equivoca.
	Help string
	// Target es el estado en el que se retoma con el dato escrito. "" = el paso en el que se
	// escaló. Existe porque hay pasos que NO saben recibir lo que trae el agente (el de la orden
	// médica lee una foto con OCR): inyectarles un texto los haría escalar otra vez.
	Target string
	// Required hace que el campo vacío se rechace («Se devolvió el campo vacío.») y publica
	// `requerido` y `minimo_campos` de uno. Sin Required, un valor vacío no se rechaza por estar
	// vacío: el bot lo acepta como «sin dato».
	Required bool
	// MaxLen publica `largo` (en caracteres). 0 = ninguno declarado.
	MaxLen int
	// Pattern publica `patron`. "" = ninguno declarado.
	Pattern string
	// Parse juzga el `valores` ENTERO de un retorno v1 cuando el paso espera algo distinto de una
	// sola clave de texto. nil = solo se acepta una clave de texto. Devuelve el dato que se
	// inyecta —el mismo que produciría el dialecto— y el rechazo (el valor cero = válido).
	Parse ReturnValuesParser
	// Validate rechaza lo que el agente escribió ANTES de inyectarlo, y DICE POR QUÉ: el rechazo
	// vuelve a él, y "no se pudo usar" a secas lo deja adivinando. Recibe la sesión porque la
	// validación fina suele depender de ella. nil = basta con el TextValidate del paso.
	Validate func(sess *session.Session, value string) InputRejection
	// Command, si no es vacío (ReturnCommandCups o ReturnCommandOrder), hace que el worker
	// entregue el dato escrito al comando del asesor correspondiente en vez de inyectarlo como
	// mensaje virtual. RegisterReturnInput hace panic con cualquier otro valor.
	Command string
}

// ReturnValuesParser lee el `valores` de un retorno v1 y devuelve el dato que se inyecta y el
// rechazo (el valor cero = válido), cuyo motivo es el literal que el agente lee para corregirse.
type ReturnValuesParser func(sess *session.Session, values json.RawMessage) (value string, problem InputRejection)

// Los dos comandos del asesor a los que un ReturnInput puede entregar el dato escrito.
const (
	ReturnCommandCups  = "cups"
	ReturnCommandOrder = "orden"
)

// RegisterReturnInput declara que un paso ofrece campo de texto al agente. Se llama donde se
// registra el estado, por la misma razón que el menú. Un Command que no sea ninguno de los dos
// conocidos es un error de programación y se detecta al arrancar: hace panic.
func RegisterReturnInput(state string, in ReturnInput) {
	if in.Command != "" && in.Command != ReturnCommandCups && in.Command != ReturnCommandOrder {
		panic(fmt.Sprintf("RegisterReturnInput(%s): Command %q desconocido", state, in.Command))
	}
	returnMenusMu.Lock()
	defer returnMenusMu.Unlock()
	returnInputs[state] = in
}

// returnInputFor devuelve la entrada registrada de un paso; ok=false si el paso no ofrece campo.
func returnInputFor(step string) (ReturnInput, bool) {
	returnMenusMu.RLock()
	defer returnMenusMu.RUnlock()
	in, ok := returnInputs[step]
	return in, ok
}

// InputRejection es el veredicto NEGATIVO sobre un dato que escribió un agente, con sus piezas
// SEPARADAS. Los motivos son literales y NOMBRAN la clave o el código dentro de la frase; la
// frase no se reescribe (es lo que el agente lee), así que el dato se saca al lado. El valor
// CERO es «no hay rechazo».
type InputRejection struct {
	// Reason es el motivo en prosa, LITERAL y opaco. Es lo único obligatorio.
	Reason string
	// Field es la clave del campo que causó el rechazo, cuando el bot la separa. Vacío = el
	// motivo no habla de ningún campo; no se inventa uno.
	Field string
	// Piece es el valor de la pieza que lo causó, cuando el bot la separa.
	Piece string
}

// Rejected dice si hay rechazo. El motivo es lo que lo decide: `campo`/`pieza` solos no son un
// veredicto.
func (r InputRejection) Rejected() bool { return r.Reason != "" }

// Reject arma un rechazo de solo motivo, el atajo de los sitios que no separan clave ni pieza.
func Reject(reason string) InputRejection { return InputRejection{Reason: reason} }

// ReturnValuesUnsupportedProblem es el motivo de un `valores` cuya FORMA el paso no sabe leer.
// No se le echa la culpa a nadie ni se ecoa el valor: puede llevar el documento del paciente.
const ReturnValuesUnsupportedProblem = "El retorno trajo los datos en una forma que este paso no sabe leer."

// defaultReturnInputLabel es el rótulo cuando el paso no registró uno.
const defaultReturnInputLabel = "Escribir la respuesta y devolverla al bot"

// textConfig devuelve la config declarativa del paso solo si DECLARÓ que espera texto del
// paciente. InputText es el iota 0: una config vacía (la de un paso que solo colgó recuperación
// IA) diría "texto" sin haberlo declarado, pero esa no trae ErrorMsg ni, salvo recuperación,
// validador, así que no cambia el veredicto.
func (m *Machine) textConfig(step string) (HandlerConfig, bool) {
	cfg, ok := m.Config(step)
	return cfg, ok && cfg.InputType == InputText
}

// ReturnMenu arma el menú de retornos de un paso (dialecto `bot_return_menu`), con su campo de
// texto si el paso registró una entrada. Es método de la máquina porque la ayuda por omisión
// sale de la config del paso.
func (m *Machine) ReturnMenu(sess *session.Session, step string) ReturnMenu {
	menu := BuildReturnMenu(sess, step)
	in, offers := returnInputFor(step)
	if !offers {
		return menu
	}
	label, help := in.Label, in.Help
	if label == "" {
		label = defaultReturnInputLabel
	}
	if help == "" {
		help = m.defaultHelp(step)
	}
	menu.Entrada = &ReturnMenuInput{Etiqueta: label, Ayuda: help, PrefijoValor: ReturnInputPrefix}
	return menu
}

// defaultHelp es la ayuda de un campo que no registró la suya: el ErrorMsg del paso, que ES la
// explicación del formato que el bot ya le da al paciente. Un paso que no espera texto (una
// subida de foto) tiene un ErrorMsg que describe otra cosa, y no se usa.
func (m *Machine) defaultHelp(step string) string {
	if cfg, ok := m.textConfig(step); ok {
		return cfg.ErrorMsg
	}
	return ""
}

// ReturnInputResult es el veredicto sobre lo que el agente escribió. Los tres casos son
// distintos y cada uno tiene su salida:
//
//   - Written=false             → no venía escrito: se resuelve contra el menú, como siempre.
//   - Written=true, Problem==""  → sirve: se inyecta como si lo hubiera escrito el paciente.
//   - Written=true, Problem!=""  → no sirve: vuelve a un agente CON el motivo.
type ReturnInputResult struct {
	Written bool
	Value   string
	// Target es el estado en el que hay que retomar con el dato. "" = el paso de origen.
	Target  string
	Problem string
	// Field y Piece son las piezas que el motivo nombra dentro de su frase, separadas. Vacías es
	// un dato válido.
	Field string
	Piece string
	// Command es el comando del asesor ("cups" u "orden") al que se entrega el dato; "" = se
	// inyecta como mensaje virtual.
	Command string
}

// reject vuelca un InputRejection en el veredicto, para que ningún sitio olvide una pieza.
func (r *ReturnInputResult) reject(rej InputRejection) {
	r.Problem = rej.Reason
	r.Field = rej.Field
	r.Piece = rej.Piece
}

// OK es el caso útil: llegó escrito y sirve.
func (r ReturnInputResult) OK() bool { return r.Written && r.Problem == "" }

// Rejected es el caso que hay que devolverle a una persona.
func (r ReturnInputResult) Rejected() bool { return r.Written && r.Problem != "" }

// ResolveReturnInput juzga un `return_value` ESCRITO por el agente (dialecto: `bot_dato:` +
// dato). Se valida ANTES de inyectar porque el error del agente no puede acabar en la cara del
// paciente: inyectar un valor malo le manda a él el mensaje de formato y le gasta un reintento.
func (m *Machine) ResolveReturnInput(sess *session.Session, step, value string) ReturnInputResult {
	if !strings.HasPrefix(value, ReturnInputPrefix) {
		return ReturnInputResult{}
	}
	return m.judge(sess, step, strings.TrimSpace(strings.TrimPrefix(value, ReturnInputPrefix)), true)
}

// judge aplica las reglas comunes a un dato ya AISLADO de su envoltorio (el prefijo del dialecto,
// o la clave de `valores` en v1): paso que no ofrece campo, campo vacío, validador del paso,
// Validate de la entrada y destino. revalidate=false es para el dato que el Parse del paso ya
// validó y rindió: se le aplican las reglas del paso pero no se vuelve a juzgar la rendición.
func (m *Machine) judge(sess *session.Session, step, data string, revalidate bool) ReturnInputResult {
	in, offers := returnInputFor(step)
	if !offers {
		// El agente escribió en un paso que no ofrecía campo: es un menú de otro paso, o la
		// interfaz lo pintó donde no debía. No es culpa suya y no se le reprocha.
		slog.Warn("retorno escrito en un paso que no ofrece campo", "step", step)
		return ReturnInputResult{}
	}
	res := ReturnInputResult{Written: true, Value: data, Target: in.Target, Command: in.Command}
	if data == "" {
		// Solo un campo requerido rechaza el vacío; uno opcional lo acepta como «sin dato» y no
		// pasa por los validadores, que juzgan datos y no su ausencia.
		if in.Required {
			res.Problem = "Se devolvió el campo vacío."
		}
		return res
	}
	if !revalidate {
		return res
	}
	// El mismo validador que Process le aplica al paciente en un paso de texto. Sin config
	// declarativa no hay validador de texto: Validate de la entrada es la única regla.
	if cfg, ok := m.textConfig(step); ok && cfg.TextValidate != nil && !cfg.TextValidate(data) {
		res.Problem = cfg.ErrorMsg
		if res.Problem == "" {
			res.Problem = "El formato no es el que espera este paso."
		}
		return res
	}
	if in.Validate != nil {
		res.reject(in.Validate(sess, data))
	}
	return res
}

// ResolveReturnV1 juzga el `valores` de un retorno del protocolo v1. Es la hermana de
// ResolveReturnInput y devuelve LO MISMO: se inyecta, vuelve a una persona, o no había dato y
// decide el token. La bifurcación es por la PRESENCIA y la FORMA de `valores`:
//
//   - ausente o vacío              → no hay dato escrito: decide `opcion` (ResolveReturnValue).
//   - el paso registró Parse       → ese parser lee el `valores` ENTERO.
//   - una clave de TEXTO           → dato escrito: el mismo camino que el dialecto, recortado.
//   - cualquier otra forma         → a una persona, con ReturnValuesUnsupportedProblem.
//
// La garantía que daba el prefijo («un token rancio nunca se inyecta como dato») la da la
// separación `opcion`/`valores`: el dato ya no comparte campo con el token.
func (m *Machine) ResolveReturnV1(sess *session.Session, step string, values json.RawMessage) ReturnInputResult {
	raw := strings.TrimSpace(string(values))
	if raw == "" || raw == "null" {
		return ReturnInputResult{}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		// El error NO se traza: el de `encoding/json` incluye EL CARÁCTER OFENSOR del cuerpo, y
		// el cuerpo puede llevar el documento del paciente. Con el paso basta para encontrarlo.
		slog.Warn("retorno v1: `valores` no es un objeto", "step", step)
		return ReturnInputResult{Written: true, Problem: ReturnValuesUnsupportedProblem}
	}
	// Vacío y AUSENTE son lo mismo: un paso de 0 campos puede mandar `{}`.
	if len(fields) == 0 {
		return ReturnInputResult{}
	}
	in, offers := returnInputFor(step)
	if !offers {
		slog.Warn("retorno v1 escrito en un paso que no ofrece campo", "step", step)
		return ReturnInputResult{}
	}

	if in.Parse != nil {
		data, problem := in.Parse(sess, values)
		if problem.Rejected() {
			res := ReturnInputResult{Written: true}
			res.reject(problem)
			return res
		}
		// Sigue por el mismo camino que el dialecto, pero SIN volver a validarse: el parser del
		// paso acaba de juzgar lo que el agente escribió de verdad.
		return m.judge(sess, step, data, false)
	}

	// Sin parser propio, la única forma posible es la de UN campo de texto.
	if len(fields) > 1 {
		slog.Warn("retorno v1: varias claves en un paso de un solo campo", "step", step)
		return ReturnInputResult{Written: true, Problem: ReturnValuesUnsupportedProblem}
	}
	var value json.RawMessage
	for _, v := range fields {
		value = v
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		slog.Warn("retorno v1: el campo no vino como texto", "step", step)
		return ReturnInputResult{Written: true, Problem: ReturnValuesUnsupportedProblem}
	}
	// Néctar recorta los bordes y el bot NO se fía: un espacio de más no lo escribió nadie a
	// propósito.
	return m.judge(sess, step, strings.TrimSpace(text), true)
}
