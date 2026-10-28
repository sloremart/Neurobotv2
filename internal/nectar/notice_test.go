package nectar

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// El sobre de hoy (§8.6 del contrato de Néctar): event_id, kind, conversation_id,
// occurred_at y, solo en mensaje_entrante, message. Lo que va a CRECER (§6.2) llega
// como campos nuevos: el manejador tiene que ignorarlos, no romperse.

const inboundNotice = `{
  "event_id": "2f0f8c1e-0000-4000-8000-000000000001",
  "kind": "mensaje_entrante",
  "conversation_id": 1234,
  "occurred_at": "2026-08-11T20:15:04Z",
  "message": {
    "id": 55, "external_id": "wamid.HBgL", "sequence": 7,
    "kind": "image", "text": "pie de foto",
    "attachment": { "id": "abc123", "url": "https://aom.colibrixa.com/api/bot/attachments/abc123" }
  }
}`

func TestParsesTheInboundNotice(t *testing.T) {
	n, err := ParseNotice([]byte(inboundNotice))
	if err != nil {
		t.Fatal(err)
	}
	if n.EventID != "2f0f8c1e-0000-4000-8000-000000000001" || n.Kind != KindInboundMessage ||
		n.ConversationID != 1234 || n.Message == nil {
		t.Fatalf("sobre mal leído: %+v", n)
	}
	if n.Message.Kind != "image" || n.Message.Text != "pie de foto" ||
		n.Message.Attachment == nil || n.Message.Attachment.URL == "" {
		t.Fatalf("mensaje mal leído: %+v", n.Message)
	}
}

func TestUnknownFieldsDoNotBreakTheEnvelope(t *testing.T) {
	// Deserialización TOLERANTE (§6.2): el aviso conversacion_devuelta va a traer el valor
	// de desbloqueo elegido por el agente; un campo que hoy no existe no puede tumbar nada.
	body := `{"event_id":"e-1","kind":"conversacion_devuelta","conversation_id":9,
	          "occurred_at":"2026-08-11T20:15:04Z","unlock_value":"ASK_DOCUMENT","extra":{"a":1}}`
	n, err := ParseNotice([]byte(body))
	if err != nil {
		t.Fatalf("un campo desconocido no puede romper el manejador: %v", err)
	}
	if n.Kind != KindConversationReturned || n.ConversationID != 9 {
		t.Fatalf("sobre mal leído: %+v", n)
	}
}

func TestAMissingEventIDIsRejected(t *testing.T) {
	// Sin event_id no hay con qué deduplicar, y procesar sin deduplicar es contestarle dos
	// veces al paciente. Se rechaza de forma permanente (no es un fallo de transporte).
	_, err := ParseNotice([]byte(`{"kind":"mensaje_entrante","conversation_id":1}`))
	if err == nil {
		t.Fatal("un aviso sin event_id no es válido")
	}
}

func TestAMissingConversationIsRejected(t *testing.T) {
	_, err := ParseNotice([]byte(`{"event_id":"e","kind":"conversacion_resuelta"}`))
	if err == nil {
		t.Fatal("un aviso sin conversation_id no es válido")
	}
}

func TestAnUnknownKindIsNotAnError(t *testing.T) {
	// Un tipo de aviso que no conocemos es futuro declarado llegando antes de tiempo: se
	// acusa recibo (si no, Néctar lo reintentaría para siempre) y el despacho lo ignora.
	n, err := ParseNotice([]byte(`{"event_id":"e","kind":"algo_nuevo","conversation_id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind.Known() {
		t.Fatal("algo_nuevo no es un kind conocido")
	}
}

func TestMalformedJSONIsRejected(t *testing.T) {
	if _, err := ParseNotice([]byte(`{"event_id":`)); err == nil {
		t.Fatal("JSON roto no es un aviso")
	}
}

func TestSessionKeyIsStablePerConversation(t *testing.T) {
	// El aviso NO trae el teléfono del paciente (§8.6: "ni el teléfono, ni su nombre").
	// La sesión del bot se indexa por esta clave derivada de la conversación; tiene que
	// caber en la tubería (VARCHAR(63)) y no confundirse con un número.
	k := SessionKey(1234)
	if k != "nectar:1234" || len(k) > 63 || !strings.HasPrefix(k, "nectar:") {
		t.Fatalf("clave inesperada: %q", k)
	}
}

func TestInboundMessageMapping(t *testing.T) {
	n, _ := ParseNotice([]byte(inboundNotice))
	msg := n.ToInboundMessage("")
	if msg.ID != n.EventID {
		t.Fatalf("el ID del mensaje es el event_id (clave de dedupe del WAL y del pool): %q", msg.ID)
	}
	if msg.Phone != "nectar:1234" || msg.ConversationID != "1234" {
		t.Fatalf("sesión/conversación mal mapeadas: %+v", msg)
	}
	if msg.MessageType != "image" || msg.ImageURL != "https://aom.colibrixa.com/api/bot/attachments/abc123" {
		t.Fatalf("imagen mal mapeada: %+v", msg)
	}
	if msg.Text != "pie de foto" {
		t.Fatalf("texto mal mapeado: %q", msg.Text)
	}
}

func TestDocumentMappingUsesDocumentURL(t *testing.T) {
	body := strings.Replace(inboundNotice, `"kind": "image"`, `"kind": "document"`, 1)
	n, _ := ParseNotice([]byte(body))
	msg := n.ToInboundMessage("")
	if msg.MessageType != "document" || msg.DocumentURL == "" || msg.ImageURL != "" {
		t.Fatalf("documento mal mapeado: %+v", msg)
	}
}

// Ruling 7: el interceptor de no soportados de Neuro (statemachine.UnsupportedMessageInterceptor)
// rechaza "sticker" y "contact", los mismos tipos que produce bird.ParseInboundMessage. Un sticker
// NO puede llegar al paso de la orden como "image": iría al OCR como si fuera una foto de orden.
func TestStickerMappingIsUnsupportedNotImage(t *testing.T) {
	for name, attachment := range map[string]string{
		"con url":     `"attachment":{"id":"s1","url":"https://aom.colibrixa.com/api/bot/attachments/s1"}`,
		"solo id":     `"attachment":{"id":"s1"}`,
		"sin adjunto": `"text":""`,
	} {
		body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
		          "message":{"id":1,"kind":"sticker",` + attachment + `}}`
		n, _ := ParseNotice([]byte(body))
		msg := n.ToInboundMessage("https://aom.colibrixa.com")
		if msg.MessageType != "sticker" {
			t.Errorf("%s: tipo=%q, el sticker se traduce a %q", name, msg.MessageType, "sticker")
		}
		if msg.ImageURL != "" || msg.DocumentURL != "" {
			t.Errorf("%s: un sticker no lleva URL de descarga: %+v", name, msg)
		}
	}
}

func TestContactsMappingMatchesBirdContact(t *testing.T) {
	body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
	          "message":{"id":1,"kind":"contacts","text":""}}`
	n, _ := ParseNotice([]byte(body))
	if msg := n.ToInboundMessage(""); msg.MessageType != "contact" {
		t.Fatalf("contacts → %q, el interceptor de Neuro espera %q", msg.MessageType, "contact")
	}
}

func TestTextMapping(t *testing.T) {
	body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
	          "message":{"id":1,"kind":"text","text":"hola"}}`
	n, _ := ParseNotice([]byte(body))
	msg := n.ToInboundMessage("")
	if msg.MessageType != "text" || msg.Text != "hola" || msg.IsPostback {
		t.Fatalf("texto mal mapeado: %+v", msg)
	}
}

// Enmienda 2026-08-21 a §8.6: message.interactive_reply_id. Es el JSON exacto del contrato.
const interactiveNotice = `{
  "event_id": "9c1d4b70-3f2a-4c88-9a51-6b0e2d7f4a11",
  "kind": "mensaje_entrante",
  "conversation_id": 1234,
  "occurred_at": "2026-08-21T15:42:07Z",
  "message": {
    "id": 77,
    "external_id": "wamid.HBgMNTczMDAxMTEyMjMzFQIAEhgUM0E0QjVDNkQ3RThGOTAxMjM0NTYA",
    "sequence": 12,
    "kind": "interactive",
    "text": "Agendar cita de control",
    "interactive_reply_id": "AGENDAR_CONTROL"
  }
}`

func TestAReadableButtonPressBranchesByIDNotByTitle(t *testing.T) {
	n, err := ParseNotice([]byte(interactiveNotice))
	if err != nil {
		t.Fatal(err)
	}
	msg := n.ToInboundMessage("")
	if !msg.IsPostback || msg.PostbackPayload != "AGENDAR_CONTROL" || msg.MessageType != "postback" {
		t.Fatalf("la pulsación se enruta por el id: %+v", msg)
	}
	if msg.Text != "AGENDAR_CONTROL" {
		t.Fatalf("Text lleva el id (como con un postback de Bird), nunca el título: %q", msg.Text)
	}
}

func TestAnAbsentReplyIDIsNotAPress(t *testing.T) {
	// AUSENTE ≠ VACÍA: sin la clave es texto/medio/pulsación ilegible — como antes del arreglo.
	body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":1,
	          "message":{"id":1,"kind":"interactive","text":""}}`
	n, _ := ParseNotice([]byte(body))
	msg := n.ToInboundMessage("")
	if msg.IsPostback || msg.PostbackPayload != "" || msg.MessageType != "interactive" {
		t.Fatalf("un interactive degradado no es una pulsación: %+v", msg)
	}
	// Y un texto normal tampoco la lleva.
	body = `{"event_id":"e","kind":"mensaje_entrante","conversation_id":1,
	         "message":{"id":1,"kind":"text","text":"1"}}`
	n, _ = ParseNotice([]byte(body))
	if msg := n.ToInboundMessage(""); msg.IsPostback || msg.Text != "1" {
		t.Fatalf("%+v", msg)
	}
}

func TestAnEmptyReplyIDIsTreatedAsAbsent(t *testing.T) {
	// Defensa: si alguna vez llegara "" (no debería, va con omitempty), no se ramifica por
	// un payload vacío — equivale a pulsación ilegible.
	body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":1,
	          "message":{"id":1,"kind":"interactive","text":"x","interactive_reply_id":""}}`
	n, _ := ParseNotice([]byte(body))
	if msg := n.ToInboundMessage(""); msg.IsPostback {
		t.Fatalf("%+v", msg)
	}
}

// §6.2 punto 2: return_value viaja SÓLO en conversacion_devuelta, con omitempty.
func TestTheReturnedNoticeCarriesTheChosenValue(t *testing.T) {
	body := `{"event_id":"e","kind":"conversacion_devuelta","conversation_id":9,
	          "occurred_at":"2026-08-24T10:00:00Z","return_value":"cita_ok"}`
	n, err := ParseNotice([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := n.ChosenReturnValue()
	if !ok || v != "cita_ok" {
		t.Fatalf("ok=%v v=%q", ok, v)
	}
}

func TestAReturnWithoutDecisionHasNoValue(t *testing.T) {
	// AUSENTE ≠ VACÍO: sin la clave, «me la devolvieron sin decidir» → se reanuda como siempre.
	n, _ := ParseNotice([]byte(`{"event_id":"e","kind":"conversacion_devuelta","conversation_id":9}`))
	if _, ok := n.ChosenReturnValue(); ok {
		t.Fatal("sin la clave no hay decisión")
	}
	if n.ReturnValue != nil {
		t.Fatal("ausente se distingue de vacío")
	}
	// Vacía (no debería viajar así) se trata como ausente.
	n, _ = ParseNotice([]byte(`{"event_id":"e","kind":"conversacion_devuelta","conversation_id":9,"return_value":""}`))
	if _, ok := n.ChosenReturnValue(); ok {
		t.Fatal("un valor vacío no es una decisión")
	}
}

func TestOtherNoticesNeverCarryAReturnValue(t *testing.T) {
	// Los otros dos avisos no llevan la clave; si por lo que sea llegara, no se interpreta.
	for _, kind := range []string{"mensaje_entrante", "conversacion_resuelta"} {
		body := `{"event_id":"e","kind":"` + kind + `","conversation_id":9,"return_value":"cita_ok"}`
		n, _ := ParseNotice([]byte(body))
		if _, ok := n.ChosenReturnValue(); ok {
			t.Fatalf("%s no decide retornos", kind)
		}
	}
}

// ---- `occurred_at` TOLERANTE (§5.2 del diseño del protocolo v1, T7) ----

func TestOccurredAtRFC3339SeConservaExacto(t *testing.T) {
	// Néctar lo declara `format: date-time` y lo emite con fracción de segundo y Z. La
	// tolerancia de abajo es una MEJORA del lector, no una excusa para perder precisión.
	n, err := ParseNotice([]byte(`{"event_id":"e","kind":"mensaje_entrante","conversation_id":9,
	          "occurred_at":"2026-09-14T15:02:11.123456Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 14, 15, 2, 11, 123456000, time.UTC)
	if !n.OccurredAt.Equal(want) {
		t.Fatalf("occurred_at = %v, quiero %v", n.OccurredAt.Time, want)
	}
	// Con desfase horario, el instante es el mismo: lo que se conserva es el punto en el tiempo.
	n, err = ParseNotice([]byte(`{"event_id":"e","kind":"mensaje_entrante","conversation_id":9,
	          "occurred_at":"2026-09-14T10:02:11-05:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !n.OccurredAt.Equal(time.Date(2026, 9, 14, 15, 2, 11, 0, time.UTC)) {
		t.Fatalf("occurred_at con desfase = %v", n.OccurredAt.Time)
	}
}

func TestOccurredAtMalFormadoNoTumbaElAviso(t *testing.T) {
	// Un 400 por una fecha rara le costaría el aviso al PACIENTE (el bot no le contesta) y
	// una alerta al supervisor de Néctar; la desproporción no la arregla un acuerdo. El aviso
	// se procesa igual y el instante queda a cero, que es lo que nadie lee para decidir nada.
	for _, valor := range []string{
		`"ayer"`,                 // texto libre
		`""`,                     // vacío
		`"2026-09-14 15:02:11"`,  // sin la T ni la zona: casi RFC 3339
		`"2026-13-45T99:99:99Z"`, // con la forma, sin el instante
		`1757862131`,             // epoch: otro tipo
		`{"fecha":"2026-09-14"}`, // un objeto
		`null`,                   // ausencia explícita
	} {
		body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":9,"occurred_at":` + valor + `}`
		n, err := ParseNotice([]byte(body))
		if err != nil {
			t.Fatalf("occurred_at = %s no puede tumbar el aviso: %v", valor, err)
		}
		if !n.OccurredAt.IsZero() {
			t.Errorf("occurred_at = %s: se esperaba el cero, got %v", valor, n.OccurredAt.Time)
		}
		// Y el resto del sobre se lee entero: es lo que hace que el aviso siga sirviendo.
		if n.EventID != "e" || n.Kind != KindInboundMessage || n.ConversationID != 9 {
			t.Errorf("occurred_at = %s dejó el sobre a medias: %+v", valor, n)
		}
	}
}

func TestOccurredAtAusenteEsElCero(t *testing.T) {
	n, err := ParseNotice([]byte(`{"event_id":"e","kind":"conversacion_resuelta","conversation_id":9}`))
	if err != nil {
		t.Fatal(err)
	}
	if !n.OccurredAt.IsZero() {
		t.Fatalf("sin la clave, el cero: %v", n.OccurredAt.Time)
	}
}

func TestOccurredAtSeSerializaComoRFC3339(t *testing.T) {
	// El tipo propio no puede cambiar lo que el bot ESCRIBE: los volcados de referencia se
	// generan serializando este sobre, y el arnés de estrés arma avisos con él.
	raw, err := json.Marshal(Notice{
		EventID: "e", Kind: KindInboundMessage, ConversationID: 9,
		OccurredAt: NoticeTimeOf(time.Date(2026, 9, 14, 15, 2, 11, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"occurred_at":"2026-09-14T15:02:11Z"`) {
		t.Fatalf("el sobre serializado = %s", raw)
	}
}

func TestElCuartoAvisoYaEsConocido(t *testing.T) {
	// hdg-bot docs/DISENO-CUARTO-AVISO-PLANTILLAS.md: el cuarto aviso entra en el registro de
	// capacidades y se resuelve de forma SÍNCRONA — deja de ser "futuro declarado" que cae
	// en el `default` del despacho.
	if !KindConversationOpenedByAgent.Known() {
		t.Fatal("conversacion_abierta_por_agente ya entra en el registro de capacidades")
	}
	if KindConversationOpenedByAgent != "conversacion_abierta_por_agente" {
		t.Fatalf("el literal de cable es el del contrato, got %q", KindConversationOpenedByAgent)
	}
	n, err := ParseNotice([]byte(`{"event_id":"e","kind":"conversacion_abierta_por_agente","conversation_id":9,
	          "occurred_at":"2026-09-14T15:02:11Z"}`))
	if err != nil {
		t.Fatalf("el lector lo acepta: %v", err)
	}
	if n.Kind != KindConversationOpenedByAgent {
		t.Fatalf("kind = %q", n.Kind)
	}
}

// TestKnownDerivaDeKnownKinds fija la garantía que el comentario de KnownKinds promete: hay UNA
// sola lista, y Known() la recorre en vez de repetir el literal. Sin esto, un Kind nuevo podía
// añadirse al switch de Known() sin tocar KnownKinds() (el registro de capacidades se quedaría
// sin publicarlo) o viceversa, y ningún test lo habría notado.
func TestKnownDerivaDeKnownKinds(t *testing.T) {
	for _, k := range KnownKinds() {
		if !k.Known() {
			t.Errorf("KnownKinds() incluye %q pero Known() lo rechaza", k)
		}
	}
	if Kind("otro").Known() {
		t.Fatal(`Kind("otro").Known() tiene que ser false`)
	}
}

// ---- El cuarto aviso: `opened{…}` (hdg-bot docs/DISENO-CUARTO-AVISO-PLANTILLAS.md §2 y §3.1) ----

const openedBody = `{"event_id":"e-ap-1","kind":"conversacion_abierta_por_agente","conversation_id":9001,
 "occurred_at":"2026-09-18T14:00:00Z",
 "opened":{"contact":{"identifier":"+570000000000","profile_name":"Paciente Prueba"},
  "channel_id":"ch-1","template_name":"cita_pendiente_contacto",
  "variables":{"1":"Paciente Prueba","2":"18 de septiembre"},
  "agent":{"id":"ag-7","name":"Asesor Prueba"}}}`

// inboundBodyFor arma un mensaje_entrante mínimo con el event_id dado; es el que usan estos
// tests para probar que ESE aviso nunca es una apertura (no hay un helper equivalente entre
// los vecinos de este archivo, que arman el cuerpo inline por caso).
func inboundBodyFor(eventID string) string {
	return `{"event_id":"` + eventID + `","kind":"mensaje_entrante","conversation_id":1,
	          "occurred_at":"2026-09-18T14:00:00Z","message":{"id":1,"kind":"text","text":"hola"}}`
}

func TestParseNotice_CuartoAviso(t *testing.T) {
	n, err := ParseNotice([]byte(openedBody))
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != KindConversationOpenedByAgent || !n.Kind.Known() {
		t.Fatalf("kind %q known=%v", n.Kind, n.Kind.Known())
	}
	if !n.ValidOpening() {
		t.Fatal("apertura válida")
	}
	if n.Opened.TemplateName != "cita_pendiente_contacto" || n.Opened.Agent.Name != "Asesor Prueba" ||
		n.Opened.Variables["2"] != "18 de septiembre" || n.Opened.Contact.Identifier != "+570000000000" {
		t.Errorf("%+v", *n.Opened)
	}
}

func TestParseNotice_CuartoAvisoSinOpenedNoEsApertura(t *testing.T) {
	for _, body := range []string{
		`{"event_id":"e","kind":"conversacion_abierta_por_agente","conversation_id":1,"occurred_at":"2026-09-18T14:00:00Z"}`,
		`{"event_id":"e","kind":"conversacion_abierta_por_agente","conversation_id":1,"occurred_at":"2026-09-18T14:00:00Z","opened":{"contact":{"identifier":"+570000000000"}}}`,
	} {
		n, err := ParseNotice([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if n.ValidOpening() {
			t.Errorf("no debe ser apertura válida: %s", body)
		}
	}
	n, _ := ParseNotice([]byte(inboundBodyFor("e-2"))) // el helper que usen los tests vecinos para un mensaje_entrante
	if n.ValidOpening() {
		t.Error("un mensaje_entrante nunca es apertura")
	}
}

func TestMaskedForInbox_EnmascaraIdentificadorYVariables(t *testing.T) {
	n, _ := ParseNotice([]byte(openedBody))
	out, err := n.MaskedForInbox()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, pii := range []string{"+570000000000", "Paciente Prueba", "18 de septiembre"} {
		if strings.Contains(s, pii) {
			t.Errorf("PII sin máscara en el WAL: %q en %s", pii, s)
		}
	}
	for _, keep := range []string{`"e-ap-1"`, `"cita_pendiente_contacto"`, `"Asesor Prueba"`, `"ch-1"`, `9001`} {
		if !strings.Contains(s, keep) {
			t.Errorf("se perdió %s: %s", keep, s)
		}
	}
	// El original no se toca.
	if n.Opened.Contact.Identifier != "+570000000000" {
		t.Error("MaskedForInbox mutó el aviso")
	}
}

// ⚠️ EL RECORTE DE LA TRAZA ES LINEAL, Y ESO NO ES COSMÉTICO (ronda 1 de T7). El recorte
// original convertía el slice de runas a cadena EN CADA VUELTA para quitar UNA runa: cuadrático.
// Medido antes del arreglo: 10 KB → 3 s, 50 KB → 38 s, 200 KB → 2 min 20 s. Y el cuerpo del aviso
// llega acotado a 1 MB (`MaxBytesReader` del manejador del webhook), así que un aviso FIRMADO con un blob
// donde debía ir la fecha bloqueaba la goroutine del webhook ANTES del 200 y del WAL; cada
// reintento de Néctar abría otra igual, y el replay del WAL lo repetía en cada arranque. O sea:
// la tolerancia del §5.2 se convertía en una denegación de servicio.
func TestUnOccurredAtEnormeNoCuelgaElLector(t *testing.T) {
	blob := strings.Repeat("ó", 32*1024) // 64 KB en bytes, y runas multibyte a propósito
	inicio := time.Now()
	var nt NoticeTime
	crudo, err := json.Marshal(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := nt.UnmarshalJSON(crudo); err != nil {
		t.Fatal(err)
	}
	if tardo := time.Since(inicio); tardo > 100*time.Millisecond {
		t.Fatalf("el recorte de la traza volvió a ser cuadrático: %v con 64 KB", tardo)
	}
	if !nt.IsZero() {
		t.Fatal("y sigue dejando el cero")
	}
}

func TestUnAvisoConUnBlobPorFechaSeProcesaEnSeguida(t *testing.T) {
	// El caso real: el cuerpo entero, por el lector entero. 500 KB caben de sobra en el KB del
	// tope del manejador.
	blob, err := json.Marshal(strings.Repeat("a", 500*1024))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":9,"occurred_at":` +
		string(blob) + `,"message":{"id":1,"kind":"text","text":"hola"}}`
	inicio := time.Now()
	n, err := ParseNotice([]byte(body))
	if err != nil {
		t.Fatalf("un blob por fecha no puede tumbar el aviso: %v", err)
	}
	if tardo := time.Since(inicio); tardo > 250*time.Millisecond {
		t.Fatalf("el aviso tardó %v: la tolerancia no puede costar la goroutine del webhook", tardo)
	}
	if !n.OccurredAt.IsZero() || n.Message == nil || n.Message.Text != "hola" {
		t.Fatalf("y el resto del sobre se lee entero: %+v", n)
	}
}

// `null` es AUSENCIA EXPLÍCITA, no un defecto de nadie: deja el cero y NO traza. Sin esta prueba,
// borrar el `if == "null"` dejaba la suite verde y llenaba el registro de avisos de una clave que
// Néctar tiene todo el derecho a mandar vacía.
func TestUnOccurredAtNuloNoTraza(t *testing.T) {
	var registro bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&registro, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(anterior)

	n, err := ParseNotice([]byte(`{"event_id":"e","kind":"mensaje_entrante","conversation_id":9,"occurred_at":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if !n.OccurredAt.IsZero() {
		t.Fatalf("null es el cero: %v", n.OccurredAt.Time)
	}
	if registro.Len() != 0 {
		t.Fatalf("y no traza nada: %s", registro.String())
	}
	// Y el de al lado SÍ traza, con el largo del valor: un blob tiene que verse como blob.
	registro.Reset()
	_, _ = ParseNotice([]byte(`{"event_id":"e","kind":"mensaje_entrante","conversation_id":9,"occurred_at":"ayer"}`))
	linea := registro.String()
	if !strings.Contains(linea, "occurred_at") || !strings.Contains(linea, "len=") {
		t.Fatalf("la traza lleva el valor acotado y su largo REAL: %s", linea)
	}
}

// El recorte barato de bytes que hay ANTES de convertir a runas no puede cambiar lo que sale: se
// cruza contra la versión ingenua (convertir entero y cortar) sobre valores de una, dos, tres y
// cuatro bytes por runa, y sobre los largos de alrededor del corte.
func TestElRecorteDeTrazaCortaPorRunasYNoCambiaConElAtajo(t *testing.T) {
	ingenuo := func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n]) + "…"
	}
	for _, runa := range []string{"a", "ó", "€", "𝔘"} {
		for _, veces := range []int{0, 1, 5, 31, 32, 33, 40, 200, 5000} {
			for _, n := range []int{1, 32, 64} {
				s := strings.Repeat(runa, veces)
				if got, want := truncateForLog(s, n), ingenuo(s, n); got != want {
					t.Fatalf("runa %q × %d, n=%d: %q ≠ %q", runa, veces, n, got, want)
				}
			}
		}
	}
	// Y nunca parte una runa por la mitad.
	if got := truncateForLog(strings.Repeat("𝔘", 100), 3); got != "𝔘𝔘𝔘…" {
		t.Fatalf("corte a mitad de runa: %q", got)
	}
}

// Los tipos REALES del cable (aviso de Néctar, 2026-09-18): channel_id y agent.id son NÚMEROS
// JSON y el identificador va sin `+`. Un parser estricto habría dado 400 y reintentos sin fin.
func TestParseNotice_CuartoAvisoConIdsNumericosYSinMas(t *testing.T) {
	body := `{"event_id":"e-ap-9","kind":"conversacion_abierta_por_agente","conversation_id":9002,
	 "occurred_at":"2026-09-18T14:00:00Z",
	 "opened":{"contact":{"identifier":"570000000000","profile_name":"Paciente Prueba"},
	  "channel_id":3,"template_name":"recordatorio_cita","variables":{"1":"18 de septiembre"},
	  "agent":{"id":17,"name":"Asesor Prueba"}}}`
	n, err := ParseNotice([]byte(body))
	if err != nil {
		t.Fatalf("los ids numéricos tienen que leerse: %v", err)
	}
	if !n.ValidOpening() || n.Opened.ChannelID.String() != "3" || n.Opened.Agent.ID.String() != "17" ||
		n.Opened.Contact.Identifier != "570000000000" {
		t.Errorf("%+v", *n.Opened)
	}
	// Y la forma con cadenas (tolerancia, p. ej. un volcado de prueba) sigue valiendo.
	n2, err := ParseNotice([]byte(openedBody))
	if err != nil || n2.Opened.ChannelID.String() != "ch-1" || n2.Opened.Agent.ID.String() != "ag-7" {
		t.Errorf("err=%v opened=%+v", err, n2.Opened)
	}
	// El enmascarado sigue funcionando con el identificador sin `+`.
	out, err := n.MaskedForInbox()
	if err != nil || strings.Contains(string(out), "570000000000") {
		t.Errorf("err=%v masked=%s", err, out)
	}
}

// NoticeID TOLERA CUALQUIER JSON (M14 de la revisión final). El id es INFORMATIVO —va a una
// traza y al rótulo «Abierta por:» del resumen—, y `channel_id`/`agent.id` los escribe el otro
// lado: un booleano, un objeto o un array donde debía ir el número no puede costar el aviso
// entero. Devolver error ahí hace que ParseNotice falle, que el manejador responda 400 y que el
// buzón de Néctar reintente con espera exponencial SIN CORTE — la misma trampa que evita la
// tolerancia de `occurred_at` (§5.2).
func TestNoticeID_ToleraCualquierJSON(t *testing.T) {
	casos := map[string]string{
		"booleano": `true`,
		"objeto":   `{"id":7}`,
		"array":    `[1,2]`,
		"nulo":     `null`,
		"flotante": `3.5`,
	}
	for nombre, crudo := range casos {
		t.Run(nombre, func(t *testing.T) {
			body := `{"event_id":"e-id-` + nombre + `","kind":"conversacion_abierta_por_agente",` +
				`"conversation_id":1,"occurred_at":"2026-09-18T14:00:00Z",` +
				`"opened":{"contact":{"identifier":"570000000000"},"channel_id":` + crudo + `,` +
				`"template_name":"recordatorio_cita","variables":{"1":"18 de septiembre"},` +
				`"agent":{"id":` + crudo + `,"name":"Asesor Prueba"}}}`
			n, err := ParseNotice([]byte(body))
			if err != nil {
				t.Fatalf("un id ilegible no puede costar el aviso: %v", err)
			}
			if !n.ValidOpening() {
				t.Fatalf("y la apertura sigue siendo válida: %+v", n.Opened)
			}
			// Los que NO son número quedan vacíos; un número, aunque sea flotante, se conserva
			// tal cual lo escribió Néctar.
			quiero := ""
			if nombre == "flotante" {
				quiero = "3.5"
			}
			if n.Opened.ChannelID.String() != quiero || n.Opened.Agent.ID.String() != quiero {
				t.Errorf("channel_id=%q agent.id=%q, esperaba %q",
					n.Opened.ChannelID, n.Opened.Agent.ID, quiero)
			}
			// Y lo que de verdad hace falta sigue entero.
			if n.Opened.TemplateName != "recordatorio_cita" || n.Opened.Agent.Name != "Asesor Prueba" {
				t.Errorf("%+v", *n.Opened)
			}
		})
	}
}
