package handlers

import (
	"flag"
	"os"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// El texto que el agente lee en el inbox lo fijan estos archivos dorados byte a byte: el
// refactor a grupos (summary_groups.go) tiene que reproducirlos sin tocarlos. Regenerar un
// dorado es un cambio de contrato del resumen, no un trámite: no basta con correr -update.
var update = flag.Bool("update", false, "regenera los archivos dorados de internal/statemachine/handlers/testdata")

type summaryCase struct {
	name     string
	golden   string
	sess     func() *session.Session
	cupsCode string
	team     string
}

// summaryCases son las tres formas reales del resumen: paciente identificado con CUPS de
// imagen, paciente sin identificar escalado desde el menú y sesión con nota de escalación.
func summaryCases() []summaryCase {
	return []summaryCase{
		{
			name:   "identificado_con_cups_de_imagen",
			golden: "testdata/summary_identificado_imagen.golden",
			sess: func() *session.Session {
				s := &session.Session{
					CurrentState: sm.StateEscalated,
					PatientName:  "MARÍA PÉREZ NÚÑEZ",
					PatientDoc:   "1020304050",
					MenuOption:   "agendar",
				}
				s.SetContext("pre_escalation_state", sm.StateMainMenu)
				s.SetContext("service_name", "RESONANCIA MAGNÉTICA")
				s.SetContext("cups_name", "RESONANCIA MAGNETICA DE CEREBRO")
				s.SetContext("escalation_reason", "sin_agenda")
				return s
			},
			cupsCode: "883101",
			team:     "Grupo A (Imagenes)",
		},
		{
			name:   "sin_identificar_desde_menu",
			golden: "testdata/summary_sin_identificar_menu.golden",
			sess: func() *session.Session {
				s := &session.Session{CurrentState: sm.StateMainMenu}
				s.SetContext("pre_escalation_state", sm.StateMainMenu)
				s.SetContext("escalation_reason", "menu_agente")
				return s
			},
			team: "Call Center",
		},
		{
			name:   "con_nota_de_escalacion",
			golden: "testdata/summary_con_nota.golden",
			sess: func() *session.Session {
				s := &session.Session{
					CurrentState: sm.StateEscalated,
					PatientName:  "JUAN GÓMEZ",
					PatientDoc:   "79123456",
					MenuOption:   "agendar",
				}
				s.SetContext("pre_escalation_state", sm.StateMainMenu)
				s.SetContext("escalation_note", "El paciente escribió: «quiero hablar con alguien» 🙏")
				return s
			},
			team: "Grupo B (Neuro/Fisiatria)",
		},
		{
			name:   "caso_completo",
			golden: "testdata/summary_caso_completo.golden",
			sess: func() *session.Session {
				s := &session.Session{
					CurrentState: sm.StateEscalated,
					PatientName:  "ANA RUIZ",
					PatientDoc:   "52123456",
					MenuOption:   "agendar",
				}
				s.SetContext("pre_escalation_state", sm.StateAskDocument)
				s.SetContext("escalation_reason", "max_reintentos")
				s.SetContext("client_type", "EPS")
				s.SetContext("selected_entity_name", "SANITAS EPS")
				s.SetContext("selected_entity_code", "EPS005")
				s.SetContext("patient_contract", "7")
				s.SetContext("eps_regimen", "2")
				s.SetContext("ocr_cups_json", `[{"cups_code":"883141","cups_name":"RM DE CEREBRO","quantity":1},{"cups_code":"930810","cups_name":"POTENCIALES","quantity":2}]`)
				s.SetContext("total_procedures", "3")
				s.SetContext("current_procedure_idx", "0")
				s.SetContext("procedures_json", `[{"service":"RESONANCIA","cups":[{"cups_code":"883141","cups_name":"RM DE CEREBRO","quantity":1}]},`+
					`{"service":"NEUROFISIOLOGIA","cups":[{"cups_code":"930810","cups_name":"POTENCIALES","quantity":2},{"cups_code":"930820","cups_name":"CONDUCCION","quantity":1}]},`+
					`{"service":"ECOGRAFIA","cups":[{"cups_code":"881201","cups_name":"ECO DE ABDOMEN","quantity":1}]}]`)
				s.SetContext("selected_slot_id", "7||0800")
				s.SetContext("available_slots_json", `[{"date":"2026-11-03","time_slot":"0800","time_display":"08:00 AM","doctor_name":"ROBERTO ORTEGA","agenda_id":7}]`)
				s.SetContext("notif_appointment_id", "4521")
				s.SetContext("notif_appt_date", "2026-11-03")
				s.SetContext("notif_appt_time", "08:00")
				s.SetContext("notif_cups_name", "RM DE CEREBRO")
				s.SetContext("notif_type", "confirmation")
				s.SetContext("search_procedures_json", `[{"ID":1,"Code":"883141","Name":"RM DE CEREBRO"},{"ID":2,"Code":"883101","Name":"RM DE COLUMNA"}]`)
				s.SetContext(sm.CtxEscalationLastInput, "no entiendo")
				return s
			},
			team: "Call Center",
		},
	}
}

func TestSummaryGolden(t *testing.T) {
	for _, c := range summaryCases() {
		t.Run(c.name, func(t *testing.T) {
			got := buildAgentSummary(c.sess(), c.cupsCode, c.team)
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(c.golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(c.golden)
			if err != nil {
				t.Fatalf("falta el dorado (corre con -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("el resumen cambió respecto del dorado %s:\n--- got ---\n%s\n--- want ---\n%s", c.golden, got, want)
			}
		})
	}
}
