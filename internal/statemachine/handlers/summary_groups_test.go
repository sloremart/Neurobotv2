package handlers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/services"
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
			// `Documento: ` se escribe igual). Los rótulos son únicos en todo el resumen: si uno
			// se repitiera, el cotejo inverso (texto -> campo) los confundiría.
			fields := map[string]summaryField{}
			for _, g := range groups {
				for _, f := range g.Fields {
					if f.Key == "" {
						t.Errorf("grupo %q: un campo sin clave", g.Key)
					}
					if _, dup := fields[f.Label]; dup {
						t.Fatalf("rótulo repetido %q: el cotejo no distinguiría los campos", f.Label)
					}
					fields[f.Label] = f
					if f.Key == "cups_nombre" || f.Key == "cups_codigo" {
						continue // línea fusionada: se asevera entera abajo
					}
					if want := f.Label + ": " + f.Value; !hasLine(g.text.String(), want) {
						t.Errorf("campo %q: falta la línea %q en %q", f.Key, want, g.text.String())
					}
				}
				if g.Key == "procedimiento" {
					var name, code string
					for _, f := range g.Fields {
						switch f.Key {
						case "cups_nombre":
							name = f.Value
						case "cups_codigo":
							code = f.Value
						}
					}
					if want := "Procedimiento: " + name + " (" + code + ")"; !hasLine(g.text.String(), want) {
						t.Errorf("falta la línea combinada %q en %q", want, g.text.String())
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

// hasLine dice si el texto trae esa línea ENTERA (no como subcadena de otra).
func hasLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// El teléfono del paciente NO viaja en facts: el agente ya lo ve en la conversación. La Tarea 9b
// enviará estos grupos como `facts` en el sobre v1.
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

func groupKeys(groups []*summaryGroup) []string {
	keys := make([]string, 0, len(groups))
	for _, g := range groups {
		keys = append(keys, g.Key)
	}
	return keys
}

func groupField(groups []*summaryGroup, group, key string) (string, bool) {
	for _, g := range groups {
		if g.Key != group {
			continue
		}
		for _, f := range g.Fields {
			if f.Key == key {
				return f.Value, true
			}
		}
	}
	return "", false
}

// Una sesión con solo el paciente no emite ningún grupo del caso: los dorados viejos no cambian.
func TestSummaryForAgent_CaseGroupsOmittedWhenEmpty(t *testing.T) {
	sess := summaryCases()[1].sess()
	sess.PatientName, sess.PatientDoc = "JUAN", "123"
	_, groups := summaryForAgent(sess, "", "Call Center")
	if want := []string{"encabezado", "paciente", "estado"}; !reflect.DeepEqual(groupKeys(groups), want) {
		t.Errorf("grupos = %v, quería %v", groupKeys(groups), want)
	}
}

// El grupo orden describe lo leído ANTES de agrupar: con cups_code ya hay procedimiento y no se emite.
func TestSummaryForAgent_OrderGroupOnlyBeforeGrouping(t *testing.T) {
	sess := summaryCases()[3].sess()
	_, groups := summaryForAgent(sess, "", "Call Center")
	if _, ok := groupField(groups, "orden", "cups_leidos"); !ok {
		t.Fatalf("sin cups_code debía haber grupo orden: %v", groupKeys(groups))
	}
	_, groups = summaryForAgent(sess, "883141", "Call Center")
	for _, g := range groups {
		if g.Key == "orden" {
			t.Errorf("con cups_code no debe haber grupo orden")
		}
	}
}

// En la consolidación EMG (consolidate_appt_id) ya hay cups_code, pero lo leído de la orden son los
// CUPS que se agregarían a la cita: el agente los necesita igual.
func TestSummaryForAgent_OrderGroupDuringConsolidation(t *testing.T) {
	sess := summaryCases()[3].sess()
	sess.Context["consolidate_appt_id"] = "90011"
	_, groups := summaryForAgent(sess, "883141", "Call Center")
	if value, ok := groupField(groups, "orden", "cups_leidos"); !ok || value != "883141:1 930810:2" {
		t.Fatalf("cups_leidos = %q (%v), grupos %v", value, ok, groupKeys(groups))
	}
}

// Lo que el agente lee en «CUPS leídos» es lo mismo que el campo de CUPS de la consola acepta.
func TestSummaryForAgent_CupsReadInAgentGrammar(t *testing.T) {
	_, groups := summaryForAgent(summaryCases()[3].sess(), "", "Call Center")
	value, ok := groupField(groups, "orden", "cups_leidos")
	if !ok || value != "883141:1 930810:2" {
		t.Fatalf("cups_leidos = %q (%v)", value, ok)
	}
	if n := len(services.ParseAgentCups(value)); n != 2 {
		t.Errorf("ParseAgentCups leyó %d CUPS, quería 2", n)
	}
}

// El orden total de los grupos es el del contrato, sin importar en qué sitio se escriba cada uno.
func TestSummaryForAgent_GroupOrder(t *testing.T) {
	_, groups := summaryForAgent(summaryCases()[3].sess(), "", "Call Center")
	want := []string{"encabezado", "paciente", "entidad", "orden", "grupo", "agenda", "cita", "busqueda", "intento", "estado"}
	if !reflect.DeepEqual(groupKeys(groups), want) {
		t.Errorf("grupos = %v, quería %v", groupKeys(groups), want)
	}
}

func TestSummaryForAgent_PendingGroupsAndChosenSlot(t *testing.T) {
	_, groups := summaryForAgent(summaryCases()[3].sess(), "", "Call Center")
	if v, _ := groupField(groups, "grupo", "actual"); v != "1 de 3" {
		t.Errorf("actual = %q", v)
	}
	if v, _ := groupField(groups, "grupo", "pendientes"); v != "NEUROFISIOLOGIA (POTENCIALES, CONDUCCION); ECOGRAFIA (ECO DE ABDOMEN)" {
		t.Errorf("pendientes = %q", v)
	}
	if v, _ := groupField(groups, "agenda", "horario_elegido"); v != "2026-11-03 08:00 AM con Dr. ROBERTO ORTEGA" {
		t.Errorf("horario_elegido = %q", v)
	}
	if _, ok := groupField(groups, "agenda", "horarios_ofrecidos"); ok {
		t.Error("con horario elegido no se listan los ofrecidos")
	}
}

// Con un CUPS elegido (línea fusionada «Procedimiento:») y una cita de notificación a la vez, el
// agente ve dos líneas con rótulos distintos y facts no las confunde.
func TestSummaryForAgent_ProcedureAndAppointmentProcedureDoNotCollide(t *testing.T) {
	sess := summaryCases()[3].sess()
	sess.SetContext("service_name", "RESONANCIA")
	sess.SetContext("cups_name", "RM DE CEREBRO SIMPLE")
	text, groups := summaryForAgent(sess, "883141", "Call Center")

	for _, want := range []string{
		"Procedimiento: RM DE CEREBRO SIMPLE (883141)",
		"Procedimiento de la cita: RM DE CEREBRO",
	} {
		if !hasLine(text, want) {
			t.Errorf("falta la línea %q en:\n%s", want, text)
		}
	}
	labels := map[string]bool{}
	for _, g := range groups {
		for _, f := range g.Fields {
			if labels[f.Label] {
				t.Errorf("rótulo repetido %q", f.Label)
			}
			labels[f.Label] = true
		}
	}
	if v, _ := groupField(groups, "cita", "procedimiento"); v != "RM DE CEREBRO" {
		t.Errorf("cita.procedimiento = %q", v)
	}
	if v, _ := groupField(groups, "procedimiento", "cups_codigo"); v != "883141" {
		t.Errorf("procedimiento.cups_codigo = %q", v)
	}
}

// El grupo del resumen y el de nectar.FactGroup (que usan las notificaciones) son el MISMO cable.
func TestSummaryGroupWireMatchesNectarFactGroup(t *testing.T) {
	title := "Título"
	for _, tc := range []struct {
		name  string
		group *summaryGroup
		facts nectar.FactGroup
	}{
		{
			"sin título", &summaryGroup{Key: "cita", Fields: []summaryField{{Key: "id", Label: "Cita", Value: "5"}, {Key: "x", Value: ""}}},
			nectar.FactGroup{Key: "cita", Fields: []nectar.FactField{{Key: "id", Label: "Cita", Value: "5"}, {Key: "x"}}},
		},
		{
			"con título y sin campos", &summaryGroup{Key: "g", Title: &title},
			nectar.FactGroup{Key: "g", Title: &title},
		},
	} {
		a, err1 := json.Marshal(tc.group)
		b, err2 := json.Marshal(tc.facts)
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if string(a) != string(b) {
			t.Errorf("%s: %s != %s", tc.name, a, b)
		}
	}
}
