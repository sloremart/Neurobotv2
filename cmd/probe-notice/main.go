// Comando probe-notice: sonda de avisos de Néctar hacia el bot LOCAL, para la prueba de humo
// (docs/PRUEBA-LOCAL-NECTAR.md). Firma un aviso con el esquema X-Nectar-* y lo POSTea (o hace
// un GET firmado/sin firmar al registro de capacidades).
//
// SE NIEGA a hablar con un host que no sea stub/local (nectar.IsStubHost): firmar y enviar un
// aviso con ids sintéticos a un bot real —o con el secreto de un Néctar real— es exactamente
// el incidente del 2026-09-08 que ese predicado existe para impedir.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("probe-notice", flag.ContinueOnError)
	fs.SetOutput(errOut)
	target := fs.String("url", "http://localhost:8091/api/webhooks/nectar", "URL del bot (solo local/stub)")
	secret := fs.String("secret", "stub", "secreto de firma de los avisos")
	kind := fs.String("kind", string(nectar.KindInboundMessage), "tipo de aviso")
	conv := fs.Int64("conversation", 1, "conversation_id")
	text := fs.String("text", "", "texto del mensaje (mensaje_entrante)")
	eventID := fs.String("event-id", "", "event_id (vacío = uno nuevo; repetirlo prueba el dedupe)")
	protocol := fs.Int("protocol", 1, "protocolo del aviso (0 = dialecto antiguo)")
	retorno := fs.String("retorno", "", "JSON del retorno v1 (conversacion_devuelta)")
	method := fs.String("method", "POST", "POST (aviso) o GET (capacidades)")
	unsigned := fs.Bool("unsigned", false, "no firmar (para comprobar el 401)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if !nectar.IsStubHost(*target) {
		_, _ = fmt.Fprintf(errOut, "probe-notice: me niego, %q no es un host de pruebas (localhost/127.0.0.1/nectar-stub)\n", *target)
		return 2
	}

	var body []byte
	if *method == http.MethodPost {
		id := *eventID
		if id == "" {
			id = "probe-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
		n := map[string]any{
			"event_id": id, "kind": *kind, "conversation_id": *conv,
			"occurred_at": time.Now().UTC().Format(time.RFC3339),
		}
		if *protocol > 0 {
			n["protocol"] = *protocol
		}
		if *kind == string(nectar.KindInboundMessage) {
			n["message"] = map[string]any{"id": time.Now().UnixNano() % 1_000_000, "kind": "text", "text": *text, "sequence": 1}
		}
		if *retorno != "" {
			if !json.Valid([]byte(*retorno)) {
				_, _ = fmt.Fprintln(errOut, "probe-notice: -retorno no es JSON válido")
				return 2
			}
			n["retorno"] = json.RawMessage(*retorno)
		}
		body, _ = json.Marshal(n)
	}

	req, err := http.NewRequestWithContext(context.Background(), *method, *target, bytes.NewReader(body))
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "probe-notice:", err)
		return 2
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	// El bot reconstruye la URL firmada con X-Forwarded-Proto (por defecto https, como detrás
	// del túnel de producción): contra un bot local en http hay que decirlo, y firmar esa URL.
	if req.URL.Scheme == "http" {
		req.Header.Set("X-Forwarded-Proto", "http")
	}
	if !*unsigned {
		nectar.SignRequest(req, *secret, time.Now(), body)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "probe-notice:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(out, "%d %s\n", resp.StatusCode, bytes.TrimSpace(b))
	return 0
}
