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
