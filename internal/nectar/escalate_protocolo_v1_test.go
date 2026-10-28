package nectar

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// El sobre del escalado en los DOS dialectos (protocolo de escalado v1 de
// Néctar). Lo que se fija aquí es la promesa de la bandera: **con 0 el cuerpo es BYTE A
// BYTE el de hoy**. No es una frase de diseño — es lo único que permite desplegar v1 apagado sin
// que Néctar note nada.

// cuerpoDeHoy es el sobre del dialecto, escrito a mano y en el orden en que lo serializa
// escalateRequest. Se escribe A MANO a propósito: si alguien añade una clave sin `omitempty`,
// esta constante es la que se pone roja, y no una comparación del cuerpo contra sí mismo.
const cuerpoDeHoy = `{"reason":"bot_cannot_answer","summary":"RESUMEN",` +
	`"bot_return_menu":{"paso_actual":"MAIN_MENU"},` +
	`"candado_resolver":"cita_pendiente","resolve_lock":"cita_pendiente"}`

func sobreDePrueba(protocol int, detail string, facts []byte) Escalation {
	return Escalation{
		Protocol:     protocol,
		Reason:       ReasonBotCannotAnswer,
		ReasonDetail: detail,
		Summary:      "RESUMEN",
		Facts:        facts,
		ReturnMenu:   []byte(`{"paso_actual":"MAIN_MENU"}`),
		ResolveLock:  "cita_pendiente",
	}
}

// sobreV1DePrueba es el sobre tal como lo arma el emisor con la bandera en 1: con descriptor y
// SIN menú del dialecto, que es justo lo que v1 sustituye (§1.2).
func sobreV1DePrueba(input []byte) Escalation {
	esc := sobreDePrueba(1, "agendamiento_asistido", []byte(`[{"clave":"pie","titulo":null,"campos":[]}]`))
	esc.ReturnMenu = nil
	esc.Input = input
	return esc
}

func TestEscalate_ConLaBanderaEnCeroElCuerpoEsElDeHoy(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)

	// Con la bandera en 0 el emisor NO rellena ni el detalle ni los hechos (escalationEnvelope,
	// handlers), pero aunque se colaran, `protocol` en 0 no viaja y el resto lleva omitempty.
	if _, err := c.Escalate(context.Background(), 1234, sobreDePrueba(0, "", nil), nil); err != nil {
		t.Fatal(err)
	}
	if got := string(f.bodies[0]); got != cuerpoDeHoy {
		t.Fatalf("el cuerpo del dialecto cambió (eso es un cambio de contrato):\n want %s\n got  %s",
			cuerpoDeHoy, got)
	}
}

func TestEscalate_ConLaBanderaEnUnoEntranLasClavesDeV1(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)

	facts := []byte(`[{"clave":"pie","titulo":null,"campos":[]}]`)
	if _, err := c.Escalate(context.Background(), 1234,
		sobreDePrueba(1, "agendamiento_asistido", facts), nil); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(f.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if string(got["protocol"]) != "1" {
		t.Errorf("`protocol` va en el CUERPO y vale 1: %s", got["protocol"])
	}
	if string(got["reason_detail"]) != `"agendamiento_asistido"` {
		t.Errorf("reason_detail: %s", got["reason_detail"])
	}
	if !bytes.Equal(got["facts"], facts) {
		t.Errorf("los hechos viajan tal cual: %s", got["facts"])
	}
	// Y lo de siempre sigue: el candado bajo sus DOS nombres y el resumen intacto.
	if string(got["candado_resolver"]) != `"cita_pendiente"` || string(got["resolve_lock"]) != `"cita_pendiente"` {
		t.Errorf("el alias del candado se sigue emitiendo: %v", got)
	}
	if string(got["summary"]) != `"RESUMEN"` {
		t.Errorf("summary: %s", got["summary"])
	}
}

// T4 — `input` viaja OPACO por el cable, igual que `facts` y que el menú del dialecto: el bot lo
// serializa y el cliente no lo vuelve a tocar.
func TestEscalate_ElDescriptorViajaTalCual(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)

	input := []byte(`{"paso":"MAIN_MENU","opciones":[{"rotulo":"x","valor":"y","salida":"cerrar"}]}`)
	if _, err := c.Escalate(context.Background(), 1234, sobreV1DePrueba(input), nil); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(f.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got["input"], input) {
		t.Errorf("el descriptor viaja tal cual: %s", got["input"])
	}
	// Y `bot_return_menu` YA NO: es la clave a la que `input` sustituye. Con el menú vacío el
	// omitempty del cable la deja fuera, que es lo que hace el cambio 100 % de emisión.
	if _, hay := got["bot_return_menu"]; hay {
		t.Errorf("v1 retira `bot_return_menu`: %s", f.bodies[0])
	}
}

// Un descriptor que no se pudo armar no puede inventarse una clave vacía: `input` ausente es un
// dato (Néctar pinta con lo que sepa), `"input": null` sería un cuerpo que su esquema rechaza.
func TestEscalate_SinDescriptorLaClaveNoViaja(t *testing.T) {
	f := newFakeNectar(t)
	c := newTestClient(f)

	if _, err := c.Escalate(context.Background(), 1234, sobreV1DePrueba(nil), nil); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(f.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if _, hay := got["input"]; hay {
		t.Errorf("sin descriptor la clave va AUSENTE: %s", f.bodies[0])
	}
}

// D6: la guarda de forma de `reason_detail` vive en el EMISOR, que es el único punto por el que
// pasan todos los motivos —también el próximo, que lo escribirá alguien que no leyó la regla—.
func TestEscalate_ReasonDetailConFormaInvalidaNoSeEmite(t *testing.T) {
	casos := map[string]string{
		"texto del paciente":   "Hola, necesito una cita para mi mamá",
		"con mayúsculas":       "AgendamientoAsistido",
		"con espacios":         "agendamiento asistido",
		"con dígitos":          "motivo_42",
		"más de 64 runas":      "a_" + string(bytes.Repeat([]byte("b"), 63)),
		"vacío no viaja igual": "",
	}
	for nombre, detalle := range casos {
		t.Run(nombre, func(t *testing.T) {
			f := newFakeNectar(t)
			c := newTestClient(f)
			if _, err := c.Escalate(context.Background(), 1234, sobreDePrueba(1, detalle, nil), nil); err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			_ = json.Unmarshal(f.bodies[0], &got)
			if _, hay := got["reason_detail"]; hay {
				t.Fatalf("un detalle que no casa ^[a-z_]{1,64}$ NO se emite, ni vacío: %s", got["reason_detail"])
			}
		})
	}
}

func TestEscalate_ReasonDetailConFormaValidaSiViaja(t *testing.T) {
	// Los dos extremos reales del vocabulario de hoy: el más corto y el más largo (26 runas).
	for _, detalle := range []string{"duda_cita", "notificacion_sin_respuesta", "notificacion_fallo_sistema"} {
		f := newFakeNectar(t)
		c := newTestClient(f)
		if _, err := c.Escalate(context.Background(), 1234, sobreDePrueba(1, detalle, nil), nil); err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		_ = json.Unmarshal(f.bodies[0], &got)
		if got["reason_detail"] != detalle {
			t.Errorf("%q no viajó: %q", detalle, got["reason_detail"])
		}
	}
}
