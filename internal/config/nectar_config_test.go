package config

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Configuración de Néctar (migración Bird → Néctar, fase A): el grupo de CUPS que decide el
// equipo, el mapa grupo → id de equipo de Néctar, la lista clave=valor de plantillas y la
// bandera del protocolo de escalado.

func TestCupsGroup(t *testing.T) {
	cfg := &Config{}

	tests := []struct {
		cups  string
		want  string
		label string
	}{
		{"881100", "imagenes", "Ecografia 881"},
		{"882100", "imagenes", "Ecografia 882"},
		{"883100", "imagenes", "Resonancia"},
		{"871100", "imagenes", "TAC 871"},
		{"879100", "imagenes", "TAC 879"},
		{"870100", "imagenes", "RX 870"},
		{"873100", "imagenes", "RX limite inferior 873"},
		{"875100", "imagenes", "RX intermedio 875"},
		{"878100", "imagenes", "RX limite superior 878"},
		{"872100", "callcenter", "872 no es imagen"},
		{"880100", "callcenter", "880 no es imagen"},
		{"884100", "callcenter", "884 no es imagen"},
		{"291100", "neuro", "EMG 291"},
		{"930100", "neuro", "930"},
		{"892100", "neuro", "892"},
		{"890274", "neuro", "Neurologia primera vez"},
		{"890374", "neuro", "Neurologia control"},
		{"053105", "neuro", "053105"},
		{"890275", "callcenter", "Neuropediatria no esta en neuro"},
		{"999999", "callcenter", "Desconocido"},
		{"ab", "callcenter", "Muy corto"},
		{"", "callcenter", "Vacio"},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			if got := cfg.CupsGroup(tt.cups); got != tt.want {
				t.Errorf("CupsGroup(%q) = %q, want %q", tt.cups, got, tt.want)
			}
		})
	}
}

func TestParseNectarTeamByGroup(t *testing.T) {
	t.Run("validas con espacios", func(t *testing.T) {
		got, err := parseNectarTeamByGroup("imagenes:7, neuro:8,callcenter:9")
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		want := map[string]int64{"imagenes": 7, "neuro": 8, "callcenter": 9}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("mayusculas y coma colgando", func(t *testing.T) {
		got, err := parseNectarTeamByGroup(" Neuro : 8 ,")
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(got) != 1 || got["neuro"] != 8 {
			t.Fatalf("got %v, want map[neuro:8]", got)
		}
	})

	for _, empty := range []string{"", "   "} {
		got, err := parseNectarTeamByGroup(empty)
		if err != nil {
			t.Fatalf("%q: error inesperado: %v", empty, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("%q: want mapa vacio no nil, got %#v", empty, got)
		}
	}

	bad := []struct{ label, raw string }{
		{"grupo desconocido", "rx:1"},
		{"id cero", "neuro:0"},
		{"id negativo", "neuro:-3"},
		{"id no numerico", "neuro:ocho"},
		{"id vacio", "neuro:"},
		{"sin dos puntos", "neuro-8"},
		{"sin grupo", ":8"},
		{"grupo repetido", "neuro:8,Neuro:9"},
		{"uno malo entre buenos", "imagenes:7,rx:1"},
	}
	for _, tt := range bad {
		t.Run(tt.label, func(t *testing.T) {
			if got, err := parseNectarTeamByGroup(tt.raw); err == nil {
				t.Fatalf("%q debia fallar, devolvio %v", tt.raw, got)
			}
		})
	}
}

func TestSuggestedTeamForCups(t *testing.T) {
	cfg := &Config{NectarTeamByGroup: map[string]int64{"imagenes": 7}}

	if got := cfg.SuggestedTeamForCups("883100"); got == nil || *got != 7 {
		t.Errorf("883100 = %v, want 7", got)
	}
	// El grupo existe en el CUPS pero no tiene id en el mapa: nil.
	if got := cfg.SuggestedTeamForCups("890274"); got != nil {
		t.Errorf("890274 = %d, want nil (neuro sin id)", *got)
	}
	if got := cfg.SuggestedTeamForCups("999999"); got != nil {
		t.Errorf("999999 = %d, want nil (callcenter sin id)", *got)
	}
	// Mapa vacio (el estado de hoy) y config nula: nil.
	if got := (&Config{}).SuggestedTeamForCups("883100"); got != nil {
		t.Errorf("mapa vacio = %d, want nil", *got)
	}
	var nilCfg *Config
	if got := nilCfg.SuggestedTeamForCups("883100"); got != nil {
		t.Errorf("config nil = %d, want nil", *got)
	}
}

// El puntero devuelto es una copia: quien lo reciba no puede alterar el mapa de la config.
func TestSuggestedTeamForCups_ReturnsCopy(t *testing.T) {
	cfg := &Config{NectarTeamByGroup: map[string]int64{"imagenes": 7}}
	got := cfg.SuggestedTeamForCups("883100")
	*got = 99
	if again := cfg.SuggestedTeamForCups("883100"); again == nil || *again != 7 {
		t.Fatalf("el mapa de la config se modifico desde fuera: %v", again)
	}
}

func TestParseKeyValueList(t *testing.T) {
	got := parseKeyValueList("p1=recordatorio_cita, p2=lista_espera")
	if len(got) != 2 || got["p1"] != "recordatorio_cita" || got["p2"] != "lista_espera" {
		t.Fatalf("got %v", got)
	}

	// Entradas vacias o sin '=' se ignoran; el valor puede quedar vacio.
	got = parseKeyValueList(" a = b ,,sin_igual, =huerfano, c=")
	if len(got) != 2 || got["a"] != "b" || got["c"] != "" {
		t.Fatalf("got %v, want map[a:b c:]", got)
	}

	if got := parseKeyValueList(""); got == nil || len(got) != 0 {
		t.Fatalf("vacia: want mapa vacio no nil, got %#v", got)
	}
}

func TestParseProtocolEscaladoVersion(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		got, err := parseProtocolEscaladoVersion(raw)
		if err != nil || got != ProtocolEscaladoV0 {
			t.Fatalf("%q = (%d, %v), want (0, nil)", raw, got, err)
		}
	}
	valid := []struct {
		raw  string
		want int
	}{
		{"0", ProtocolEscaladoV0},
		{"1", ProtocolEscaladoV1},
		{" 1 ", ProtocolEscaladoV1}, // el .env deja espacios con facilidad
	}
	for _, tt := range valid {
		got, err := parseProtocolEscaladoVersion(tt.raw)
		if err != nil || got != tt.want {
			t.Fatalf("%q = (%d, %v), want (%d, nil)", tt.raw, got, err, tt.want)
		}
	}
	// Ni un texto ni un numero fuera de rango: cualquiera tumba el arranque.
	for _, raw := range []string{"v1", "2", "-1", "uno", "1.0", "true"} {
		if got, err := parseProtocolEscaladoVersion(raw); err == nil {
			t.Fatalf("%q debia fallar, devolvio %d", raw, got)
		}
	}
}

func TestProtocolVersion_EmitsV1(t *testing.T) {
	if v := (&Config{}).ProtocolVersion(); v != ProtocolEscaladoV0 {
		t.Fatalf("Config vacia emite %d, want %d", v, ProtocolEscaladoV0)
	}
	if (&Config{}).EmitsProtocolV1() {
		t.Fatal("Config vacia no debe emitir v1")
	}
	cfg := &Config{ProtocolEscaladoVersion: ProtocolEscaladoV1}
	if cfg.ProtocolVersion() != ProtocolEscaladoV1 || !cfg.EmitsProtocolV1() {
		t.Fatal("ProtocolEscaladoVersion=1 debe emitir v1")
	}
	// La recepcion es dual siempre: ninguna bandera la apaga.
	if !ReceptionIsDual {
		t.Fatal("la recepcion dual no se apaga con ninguna bandera")
	}
}

// Load() lee las variables de Néctar sin exigirlas: ninguna entra en validate().
func TestLoad_NectarVarsDefaultsAndValues(t *testing.T) {
	setRequiredEnv(t)
	for _, k := range []string{
		"NECTAR_BASE_URL", "NECTAR_SERVICE_KEY", "NECTAR_NOTICE_SECRET", "NECTAR_REQUEST_SECRET",
		"NECTAR_TEMPLATE_NAMES", "NECTAR_TEAM_BY_GROUP", "PROTOCOL_ESCALADO_VERSION", "NECTAR_CHANNEL_ID",
	} {
		t.Setenv(k, "")
	}

	cfg := Load()
	if cfg.NectarBaseURL != "" || cfg.NectarServiceKey != "" || cfg.NectarNoticeSecret != "" || cfg.NectarRequestSecret != "" {
		t.Errorf("sin variables los campos de Nectar deben quedar vacios: %+v", cfg)
	}
	if cfg.NectarTemplateNames == nil || len(cfg.NectarTemplateNames) != 0 {
		t.Errorf("NectarTemplateNames = %#v, want mapa vacio", cfg.NectarTemplateNames)
	}
	if cfg.NectarTeamByGroup == nil || len(cfg.NectarTeamByGroup) != 0 {
		t.Errorf("NectarTeamByGroup = %#v, want mapa vacio", cfg.NectarTeamByGroup)
	}
	if cfg.ProtocolEscaladoVersion != 0 {
		t.Errorf("ProtocolEscaladoVersion = %d, want 0", cfg.ProtocolEscaladoVersion)
	}
	if cfg.NectarChannelID != 0 {
		t.Errorf("NectarChannelID = %d, want 0 (apertura sin configurar)", cfg.NectarChannelID)
	}

	t.Setenv("NECTAR_BASE_URL", "https://nectar.example.com//")
	t.Setenv("NECTAR_SERVICE_KEY", "svc")
	t.Setenv("NECTAR_NOTICE_SECRET", "notice")
	t.Setenv("NECTAR_REQUEST_SECRET", "request")
	t.Setenv("NECTAR_TEMPLATE_NAMES", "p1=recordatorio_cita, p2=lista_espera")
	t.Setenv("NECTAR_TEAM_BY_GROUP", "imagenes:7,neuro:8,callcenter:9")
	t.Setenv("PROTOCOL_ESCALADO_VERSION", "1")
	t.Setenv("NECTAR_CHANNEL_ID", " 12 ")

	cfg = Load()
	if cfg.NectarBaseURL != "https://nectar.example.com" {
		t.Errorf("NectarBaseURL = %q, want sin barra final", cfg.NectarBaseURL)
	}
	if cfg.NectarServiceKey != "svc" || cfg.NectarNoticeSecret != "notice" || cfg.NectarRequestSecret != "request" {
		t.Errorf("secretos mal leidos: %q %q %q", cfg.NectarServiceKey, cfg.NectarNoticeSecret, cfg.NectarRequestSecret)
	}
	if cfg.NectarTemplateNames["p1"] != "recordatorio_cita" || cfg.NectarTemplateNames["p2"] != "lista_espera" {
		t.Errorf("NectarTemplateNames = %v", cfg.NectarTemplateNames)
	}
	if cfg.NectarTeamByGroup["imagenes"] != 7 || cfg.NectarTeamByGroup["neuro"] != 8 || cfg.NectarTeamByGroup["callcenter"] != 9 {
		t.Errorf("NectarTeamByGroup = %v", cfg.NectarTeamByGroup)
	}
	if !cfg.EmitsProtocolV1() {
		t.Errorf("PROTOCOL_ESCALADO_VERSION=1 debe emitir v1")
	}
	if cfg.NectarChannelID != 12 {
		t.Errorf("NectarChannelID = %d, want 12", cfg.NectarChannelID)
	}
}

// Un valor que no se entendio tumba el arranque (log.Fatalf): se verifica en un subproceso
// porque Fatalf termina el proceso.
func TestLoad_InvalidNectarVarsAbortStartup(t *testing.T) {
	if os.Getenv("CONFIG_TEST_FATAL_CHILD") == "1" {
		Load()
		return
	}

	cases := []struct{ envKey, envVal string }{
		{"NECTAR_TEAM_BY_GROUP", "rx:1"},
		{"NECTAR_TEAM_BY_GROUP", "neuro:0"},
		{"PROTOCOL_ESCALADO_VERSION", "v1"},
		{"PROTOCOL_ESCALADO_VERSION", "2"},
		{"NECTAR_CHANNEL_ID", "siete"},
		{"NECTAR_CHANNEL_ID", "-3"},
	}
	for _, tt := range cases {
		t.Run(tt.envKey+"="+tt.envVal, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoad_InvalidNectarVarsAbortStartup$")
			cmd.Env = append(os.Environ(),
				"CONFIG_TEST_FATAL_CHILD=1",
				"OPENAI_API_KEY=x", "EXTERNAL_DB_PASSWORD=x", "BIRD_API_URL=x", "BIRD_WEBHOOK_SECRET=x",
				"BIRD_WORKSPACE_ID=x", "INTERNAL_API_KEY=x", "DB_PASSWORD=x", "EXTERNAL_DB_USER=x",
				"BIRD_API_KEY_WA=x", "BIRD_CHANNEL_ID=x", "BIRD_TEAM_FALLBACK=x",
				tt.envKey+"="+tt.envVal,
			)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("Load() con %s=%s debia terminar el proceso; salida: %s", tt.envKey, tt.envVal, out)
			}
			if !strings.Contains(string(out), tt.envKey+" inválida") {
				t.Fatalf("la salida no nombra %s: %s", tt.envKey, out)
			}
		})
	}
}

// La cola del próximo día hábil se gobierna con dos variables: el margen antes del cierre
// (BUSINESS_HOURS_CLOSE_MARGIN_MIN) y la espera máxima hasta la apertura
// (BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN). Un valor que no se entiende tumba el arranque.

func TestParseCloseMarginMin(t *testing.T) {
	if got, err := parseCloseMarginMin(""); err != nil || got != DefaultCloseMarginMin {
		t.Errorf("sin variable = (%d, %v), want (%d, nil)", got, err, DefaultCloseMarginMin)
	}
	if got, err := parseCloseMarginMin("  30 "); err != nil || got != 30 {
		t.Errorf("«30» = (%d, %v), want (30, nil)", got, err)
	}
	if got, err := parseCloseMarginMin("0"); err != nil || got != 0 {
		t.Errorf("«0» = (%d, %v), want (0, nil): apagar el margen es legal", got, err)
	}
	for _, bad := range []string{"quince", "15m", "15.0", "-", "1e2", "-1", "999"} {
		if _, err := parseCloseMarginMin(bad); err == nil {
			t.Errorf("%q tiene que ser rechazado, no caer al default", bad)
		}
	}
}

func TestParseQueueMaxWaitMin(t *testing.T) {
	if got, err := parseQueueMaxWaitMin(""); err != nil || got != DefaultQueueMaxWaitMin {
		t.Errorf("sin variable = (%d, %v), want (%d, nil)", got, err, DefaultQueueMaxWaitMin)
	}
	if got, err := parseQueueMaxWaitMin(" 600 "); err != nil || got != 600 {
		t.Errorf("«600» = (%d, %v), want (600, nil)", got, err)
	}
	if got, err := parseQueueMaxWaitMin("0"); err != nil || got != 0 {
		t.Errorf("«0» = (%d, %v), want (0, nil): apagar la cola es legal", got, err)
	}
	for _, bad := range []string{"-1", "veinte horas", "20h", "1200.0"} {
		if _, err := parseQueueMaxWaitMin(bad); err == nil {
			t.Errorf("%q tiene que ser rechazado, no caer al default", bad)
		}
	}
}

func TestLoad_BusinessHoursQueueDefaultsAndValues(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("BUSINESS_HOURS_CLOSE_MARGIN_MIN", "")
	t.Setenv("BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN", "")

	cfg := Load()
	if cfg.BusinessHoursCloseMarginMin != 15 {
		t.Errorf("BusinessHoursCloseMarginMin = %d, want 15", cfg.BusinessHoursCloseMarginMin)
	}
	if cfg.BusinessHoursQueueMaxWaitMin != 1200 {
		t.Errorf("BusinessHoursQueueMaxWaitMin = %d, want 1200", cfg.BusinessHoursQueueMaxWaitMin)
	}

	t.Setenv("BUSINESS_HOURS_CLOSE_MARGIN_MIN", "10")
	t.Setenv("BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN", "0")
	cfg = Load()
	if cfg.BusinessHoursCloseMarginMin != 10 || cfg.BusinessHoursQueueMaxWaitMin != 0 {
		t.Errorf("valores leídos: margen=%d espera=%d", cfg.BusinessHoursCloseMarginMin, cfg.BusinessHoursQueueMaxWaitMin)
	}
}

// Un valor imposible termina el proceso (log.Fatalf): se verifica en un subproceso.
func TestLoad_InvalidBusinessHoursQueueVarsAbortStartup(t *testing.T) {
	if os.Getenv("CONFIG_TEST_FATAL_CHILD") == "1" {
		Load()
		return
	}

	cases := []struct{ envKey, envVal string }{
		{"BUSINESS_HOURS_CLOSE_MARGIN_MIN", "quince"},
		{"BUSINESS_HOURS_CLOSE_MARGIN_MIN", "999"},
		{"BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN", "-1"},
		{"BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN", "20h"},
	}
	for _, tt := range cases {
		t.Run(tt.envKey+"="+tt.envVal, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoad_InvalidBusinessHoursQueueVarsAbortStartup$")
			cmd.Env = append(os.Environ(),
				"CONFIG_TEST_FATAL_CHILD=1",
				"OPENAI_API_KEY=x", "EXTERNAL_DB_PASSWORD=x", "BIRD_API_URL=x", "BIRD_WEBHOOK_SECRET=x",
				"BIRD_WORKSPACE_ID=x", "INTERNAL_API_KEY=x", "DB_PASSWORD=x", "EXTERNAL_DB_USER=x",
				"BIRD_API_KEY_WA=x", "BIRD_CHANNEL_ID=x", "BIRD_TEAM_FALLBACK=x",
				tt.envKey+"="+tt.envVal,
			)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("Load() con %s=%s debía terminar el proceso; salida: %s", tt.envKey, tt.envVal, out)
			}
			if !strings.Contains(string(out), tt.envKey+" inválido") {
				t.Fatalf("la salida no nombra %s: %s", tt.envKey, out)
			}
		})
	}
}

func TestParseNectarCloseMin(t *testing.T) {
	if got, err := parseNectarCloseMin(""); err != nil || got != DefaultNectarCloseMin {
		t.Errorf("sin variable = (%d, %v), want (%d, nil)", got, err, DefaultNectarCloseMin)
	}
	if DefaultNectarCloseMin != 480 {
		t.Errorf("el default debe ser 480 min (8 h), got %d", DefaultNectarCloseMin)
	}
	if got, err := parseNectarCloseMin(" 600 "); err != nil || got != 600 {
		t.Errorf("«600» = (%d, %v), want (600, nil)", got, err)
	}
	for _, bad := range []string{"0", "-1", "ocho horas", "8h", "480.0"} {
		if _, err := parseNectarCloseMin(bad); err == nil {
			t.Errorf("%q tiene que ser rechazado, no caer al default", bad)
		}
	}
}
