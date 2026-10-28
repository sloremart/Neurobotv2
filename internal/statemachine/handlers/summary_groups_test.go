package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// La invariante del refactor: el texto es el render de los grupos y los campos son sus líneas,
// en las dos direcciones. Sin esto `facts` podría irse quedando atrás sin que ningún dorado se
// pusiera rojo.
func TestSummaryForAgent_FactsMatchText(t *testing.T) {
	for _, c := range summaryCases() {
		t.Run(c.name, func(t *testing.T) {
			text, groups := summaryForAgent(c.sess(), c.cupsCode, c.team)

			var concat strings.Builder
			for _, g := range groups {
				if g.Key == "" {
					t.Error("todo grupo lleva clave")
				}
				concat.WriteString(g.text.String())
			}
			if concat.String() != text {
				t.Fatalf("los grupos no reconstruyen el resumen:\n%s\n---\n%s", concat.String(), text)
			}

			// Campo -> texto: cada campo está escrito como línea de SU grupo (el vacío también:
			// `Documento: ` se escribe igual).
			fields := map[string]summaryField{}
			for _, g := range groups {
				for _, f := range g.Fields {
					if f.Key == "" {
						t.Errorf("grupo %q: un campo sin clave", g.Key)
					}
					fields[f.Label] = f
					if f.Key == "cups_codigo" || f.Key == "cups_nombre" {
						if !strings.Contains(g.text.String(), f.Value) {
							t.Errorf("campo %q=%q no está en el texto", f.Key, f.Value)
						}
						continue
					}
					if want := f.Label + ": " + f.Value; !strings.Contains(g.text.String(), want) {
						t.Errorf("campo %q: falta la línea %q en %q", f.Key, want, g.text.String())
					}
				}
			}

			// Texto -> campo: cada línea «Rotulo: valor» tiene su campo, salvo la del encabezado.
			for _, line := range strings.Split(text, "\n") {
				label, value, ok := strings.Cut(line, ": ")
				if !ok {
					// «Documento: » sin valor termina en ": " y sí lleva corte; solo caen aquí
					// el encabezado y las líneas en blanco.
					if line != "" && line != "Transferencia de chatbot" {
						t.Errorf("línea sin forma «Rotulo: valor»: %q", line)
					}
					continue
				}
				if label == "Procedimiento" {
					if fields["Procedimiento"].Key != "" {
						t.Error("Procedimiento es una línea fusionada: va partida en cups_nombre y cups_codigo")
					}
					if !strings.Contains(value, "(") {
						t.Errorf("línea Procedimiento sin código: %q", value)
					}
					continue
				}
				f, found := fields[label]
				if !found {
					t.Errorf("la línea %q no tiene campo", line)
				} else if f.Value != value {
					t.Errorf("línea %q / campo %+v", line, f)
				}
			}
		})
	}
}

// El teléfono del paciente NO viaja en facts: el agente lo ve en la conversación, y facts se
// guarda aparte.
func TestSummaryForAgent_NoPhoneInFacts(t *testing.T) {
	const phone = "+573001234567"
	for _, c := range summaryCases() {
		t.Run(c.name, func(t *testing.T) {
			sess := c.sess()
			sess.PhoneNumber = phone
			sess.SetContext("phone", phone)
			text, groups := summaryForAgent(sess, c.cupsCode, c.team)
			raw, err := json.Marshal(groups)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "573001234567") || strings.Contains(text, "573001234567") {
				t.Errorf("el teléfono se coló: %s", raw)
			}
		})
	}
}

// Un grupo que no escribe nada no se emite: sin nota ni servicio el resumen no los trae.
func TestSummaryForAgent_EmptyGroupsAreOmitted(t *testing.T) {
	_, groups := summaryForAgent(summaryCases()[1].sess(), "", "Call Center")
	for _, g := range groups {
		if g.Key == "nota" || g.Key == "procedimiento" {
			t.Errorf("grupo %q no debería emitirse", g.Key)
		}
	}
}
