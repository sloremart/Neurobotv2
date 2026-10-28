package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

func sampleConfig() *config.Config {
	return &config.Config{
		BotName:    "NeuroBot",
		CenterName: "Neuro Electrodiagnóstico",
	}
}

func withTime(t time.Time, fn func()) {
	old := nowFunc
	nowFunc = func() time.Time { return t }
	defer func() { nowFunc = old }()
	fn()
}

// ==================== CheckBusinessHours ====================

func TestCheckBusinessHours_MondayMorning(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Monday 10 AM
	mon := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC) // Monday
	withTime(mon, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		// Should chain: CHECK_BUSINESS_HOURS → GREETING → MAIN_MENU
		if result.NextState != sm.StateMainMenu {
			t.Errorf("expected MAIN_MENU, got %s", result.NextState)
		}
	})
}

func TestCheckBusinessHours_FridayEvening(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Friday 5:30 PM (17:30) — still within 7-18
	fri := time.Date(2026, 3, 20, 17, 30, 0, 0, time.UTC)
	withTime(fri, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextState != sm.StateMainMenu {
			t.Errorf("expected MAIN_MENU (in hours), got %s", result.NextState)
		}
	})
}

func TestCheckBusinessHours_Friday6PM(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Friday 6:00 PM (18:00) — hour >= 18, out of hours
	fri := time.Date(2026, 3, 20, 18, 0, 0, 0, time.UTC)
	withTime(fri, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		// Should chain: CHECK_BUSINESS_HOURS → OUT_OF_HOURS → OUT_OF_HOURS_MENU (stops: interactive)
		if result.NextState != sm.StateOutOfHoursMenu {
			t.Errorf("expected OUT_OF_HOURS_MENU (out of hours), got %s", result.NextState)
		}
	})
}

func TestCheckBusinessHours_Saturday11AM(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Saturday 11:00 AM — in hours (7-12)
	sat := time.Date(2026, 3, 21, 11, 0, 0, 0, time.UTC) // Saturday
	withTime(sat, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextState != sm.StateMainMenu {
			t.Errorf("expected MAIN_MENU (Sat in hours), got %s", result.NextState)
		}
	})
}

func TestCheckBusinessHours_Saturday1PM(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Saturday 1:00 PM (13:00) — out of hours (>= 12)
	sat := time.Date(2026, 3, 21, 13, 0, 0, 0, time.UTC)
	withTime(sat, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextState != sm.StateOutOfHoursMenu {
			t.Errorf("expected OUT_OF_HOURS_MENU (Sat afternoon), got %s", result.NextState)
		}
	})
}

func TestCheckBusinessHours_Sunday(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Sunday 10 AM — out of hours
	sun := time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC) // Sunday
	withTime(sun, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextState != sm.StateOutOfHoursMenu {
			t.Errorf("expected OUT_OF_HOURS_MENU (Sunday), got %s", result.NextState)
		}
	})
}

func TestCheckBusinessHours_EarlyMorning(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Monday 5:00 AM — before 7, out of hours
	mon := time.Date(2026, 3, 16, 5, 0, 0, 0, time.UTC)
	withTime(mon, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextState != sm.StateOutOfHoursMenu {
			t.Errorf("expected OUT_OF_HOURS_MENU (before 7AM), got %s", result.NextState)
		}
	})
}

func TestCheckBusinessHours_Exactly7AM(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	// Monday 7:00 AM — in hours (>= 7)
	mon := time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC)
	withTime(mon, func() {
		sess := testSess(sm.StateCheckBusinessHours)
		result, err := m.Process(context.Background(), sess, textM("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextState != sm.StateMainMenu {
			t.Errorf("expected MAIN_MENU (7AM boundary), got %s", result.NextState)
		}
	})
}

// ==================== OutOfHours ====================

func TestOutOfHours_ShowsInteractiveMenu(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateOutOfHours)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	// OUT_OF_HOURS now shows interactive menu → OUT_OF_HOURS_MENU
	if result.NextState != sm.StateOutOfHoursMenu {
		t.Errorf("expected OUT_OF_HOURS_MENU, got %s", result.NextState)
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected messages")
	}
}

// ==================== Greeting ====================

func TestGreeting_WelcomeMessage(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateGreeting)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}
	// Should have 1 consolidated message (list with welcome text in body)
	if len(result.Messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(result.Messages))
	}
}

// ==================== MainMenu ====================

func TestMainMenu_Consultar(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateMainMenu)
	result, err := m.Process(context.Background(), sess, postbackM("consultar"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskDocumentType {
		t.Errorf("expected ASK_DOCUMENT_TYPE, got %s", result.NextState)
	}
	if result.UpdateCtx["menu_option"] != "consultar" {
		t.Errorf("expected menu_option=consultar, got %s", result.UpdateCtx["menu_option"])
	}
}

func TestMainMenu_Agendar(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateMainMenu)
	result, err := m.Process(context.Background(), sess, postbackM("agendar"))
	if err != nil {
		t.Fatal(err)
	}
	// Bird V2: agendar → ASK_CLIENT_TYPE (entity type selection before document)
	if result.NextState != sm.StateAskClientType {
		t.Errorf("expected ASK_CLIENT_TYPE, got %s", result.NextState)
	}
	if result.UpdateCtx["menu_option"] != "agendar" {
		t.Error("expected menu_option=agendar")
	}
}

func TestMainMenu_Ayuda(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateMainMenu)
	result, err := m.Process(context.Background(), sess, postbackM("ayuda"))
	if err != nil {
		t.Fatal(err)
	}
	// SHOW_HELP is automatic → auto-closes session
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED (auto-close from SHOW_HELP), got %s", result.NextState)
	}
	// Should have help message (combined text)
	if len(result.Messages) < 1 {
		t.Errorf("expected at least 1 message (help text), got %d", len(result.Messages))
	}
}

func TestMainMenu_InvalidInput(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateMainMenu)
	result, err := m.Process(context.Background(), sess, textM("invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("expected MAIN_MENU (retry), got %s", result.NextState)
	}
}

// TestMainMenu_Medicamentos: seleccionar "medicamentos" en el menú lleva al gate de SANITAS
// (estado interactivo), NO directo a escalación (a diferencia de PET-CT).
// TestMainMenu_Medicamentos: el menú entra al flujo de agendar (identificar/crear paciente) con la
// bandera medication_flow, para validar el contrato SANITAS real (no autodeclarado).
func TestMainMenu_Medicamentos(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateMainMenu)
	result, err := m.Process(context.Background(), sess, postbackM("medicamentos"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskClientType {
		t.Errorf("expected ASK_CLIENT_TYPE (flujo de agendar), got %s", result.NextState)
	}
	if result.UpdateCtx["medication_flow"] != "1" {
		t.Errorf("expected medication_flow=1, got %q", result.UpdateCtx["medication_flow"])
	}
}

// TestMedicationCheck_SanitasNoChannel_Escalates: SANITAS sin canal externo configurado → escala a
// agente (como antes), tras validar el contrato real.
func TestMedicationCheck_SanitasNoChannel_Escalates(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil) // sin MedicationExternalWANumber

	sess := testSess(sm.StateMedicationCheckSanitas)
	sess.Context["patient_contract"] = "6" // SANITAS MRC contributivo
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
	if result.UpdateCtx["escalation_reason"] != "medicamentos" {
		t.Errorf("expected escalation_reason=medicamentos, got %s", result.UpdateCtx["escalation_reason"])
	}
}

// TestMedicationCheck_SanitasWithChannel_SendsLink: SANITAS con canal externo → NO escala; cierra con
// el link wa.me al canal (no pasa a ESCALATE_TO_AGENT).
func TestMedicationCheck_SanitasWithChannel_SendsLink(t *testing.T) {
	cfg := sampleConfig()
	cfg.MedicationExternalWANumber = "573001234567"
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, cfg, nil)

	sess := testSess(sm.StateMedicationCheckSanitas)
	sess.Context["patient_contract"] = "4" // SANITAS Evento contributivo
	sess.Context["patient_name"] = "Juan Perez"
	sess.Context["patient_doc"] = "123456"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState == sm.StateEscalateToAgent {
		t.Error("con canal externo NO debía escalar a agente")
	}
	if len(result.Messages) < 1 {
		t.Fatal("esperaba el mensaje con el link")
	}
	txt := result.Messages[0].(*sm.TextMessage).Text
	if !strings.Contains(txt, "wa.me/573001234567") {
		t.Errorf("esperaba el link wa.me al canal externo, got: %s", txt)
	}
}

// TestMedicationCheck_NotSanitas_BackToMenu: contrato NO-SANITAS → informa y vuelve al menú (no escala).
func TestMedicationCheck_NotSanitas_BackToMenu(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)

	sess := testSess(sm.StateMedicationCheckSanitas)
	sess.Context["patient_contract"] = "8" // PARTICULAR
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}
}

// ==================== Horario: próxima apertura y cola del próximo día ====================

// at arma un instante de prueba en UTC, la misma zona que usan las pruebas del horario de arriba
// (businessHoursOpen lee la hora tal como la entrega nowFunc).
func at(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

// nextBusinessOpen: la apertura en que una persona puede atender lo que llegue en ese instante.
// Con el centro abierto es el instante mismo.
func TestNextBusinessOpen(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		// El brief decía «viernes 18:30 → lunes 07:00»; con el horario de Neuro (sábados de
		// 7:00 a 12:00) la apertura siguiente es el sábado.
		{"viernes 18:30 → sábado 07:00", at(2026, 10, 23, 18, 30), at(2026, 10, 24, 7, 0)},
		{"sábado 12:30 → lunes 07:00", at(2026, 10, 24, 12, 30), at(2026, 10, 26, 7, 0)},
		{"martes 10:00 abierto → martes 10:00", at(2026, 10, 27, 10, 0), at(2026, 10, 27, 10, 0)},
		{"martes 05:00 → martes 07:00", at(2026, 10, 27, 5, 0), at(2026, 10, 27, 7, 0)},
		{"domingo 03:00 → lunes 07:00", at(2026, 10, 25, 3, 0), at(2026, 10, 26, 7, 0)},
		{"sábado 11:59 abierto → sábado 11:59", at(2026, 10, 24, 11, 59), at(2026, 10, 24, 11, 59)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextBusinessOpen(c.now); !got.Equal(c.want) {
				t.Errorf("nextBusinessOpen(%s) = %s, want %s", c.now, got, c.want)
			}
		})
	}
}

// QueuedWaitEnd: hasta cuándo espera lo que se encola en ese instante. Con el centro abierto
// (el margen antes del cierre) no es el instante mismo sino la apertura SIGUIENTE al cierre: si
// fuera el instante, la exención de la cola caducaría en el mismo momento de encolarse.
func TestQueuedWaitEnd(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"viernes 17:50 (margen) → sábado 07:00", at(2026, 10, 23, 17, 50), at(2026, 10, 24, 7, 0)},
		{"sábado 11:50 (margen) → lunes 07:00", at(2026, 10, 24, 11, 50), at(2026, 10, 26, 7, 0)},
		{"martes 10:00 → miércoles 07:00", at(2026, 10, 27, 10, 0), at(2026, 10, 28, 7, 0)},
		{"viernes 18:30 → sábado 07:00", at(2026, 10, 23, 18, 30), at(2026, 10, 24, 7, 0)},
		{"martes 05:00 → martes 07:00", at(2026, 10, 27, 5, 0), at(2026, 10, 27, 7, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QueuedWaitEnd(c.now); !got.Equal(c.want) {
				t.Errorf("QueuedWaitEnd(%s) = %s, want %s", c.now, got, c.want)
			}
		})
	}
}

func TestBusinessHoursDescription(t *testing.T) {
	want := "Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m."
	if got := BusinessHoursDescription(); got != want {
		t.Errorf("BusinessHoursDescription() = %q, want %q", got, want)
	}
}

func queueConfig() *config.Config {
	return &config.Config{BusinessHoursCloseMarginMin: 15, BusinessHoursQueueMaxWaitMin: 1200}
}

func TestNextBusinessDayQueue(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want escalationQueue
	}{
		{"martes 10:00, en jornada", at(2026, 10, 27, 10, 0), escalationQueue{}},
		{"martes 17:44, aún alcanza", at(2026, 10, 27, 17, 44), escalationQueue{}},
		{"martes 17:45, margen", at(2026, 10, 27, 17, 45), escalationQueue{window: true, queue: true, reason: "margen"}},
		{"viernes 17:50, margen (abre el sábado)", at(2026, 10, 23, 17, 50), escalationQueue{window: true, queue: true, reason: "margen"}},
		{"martes 18:30, de noche", at(2026, 10, 27, 18, 30), escalationQueue{window: true, queue: true, reason: "fuera_de_horario"}},
		{"sábado 11:50, margen pero abre el lunes", at(2026, 10, 24, 11, 50), escalationQueue{window: true, queue: false, reason: "margen"}},
		{"sábado 13:00, abre el lunes", at(2026, 10, 24, 13, 0), escalationQueue{window: true, queue: false, reason: "fuera_de_horario"}},
		{"domingo 15:00, abre en 16 h", at(2026, 10, 25, 15, 0), escalationQueue{window: true, queue: true, reason: "fuera_de_horario"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got escalationQueue
			withTime(c.now, func() { got = nextBusinessDayQueue(queueConfig()) })
			if got != c.want {
				t.Errorf("nextBusinessDayQueue = %+v, want %+v", got, c.want)
			}
			if got.farOpen() != (c.want.window && !c.want.queue) {
				t.Errorf("farOpen = %v", got.farOpen())
			}
		})
	}
}

// TESTING_ALWAYS_OPEN apaga la cola igual que apaga el horario; margen o espera en cero también.
func TestNextBusinessDayQueue_Disabled(t *testing.T) {
	night := at(2026, 10, 27, 18, 30)
	alwaysOpen := queueConfig()
	alwaysOpen.TestingAlwaysOpen = true
	noWait := queueConfig()
	noWait.BusinessHoursQueueMaxWaitMin = 0
	noMargin := queueConfig()
	noMargin.BusinessHoursCloseMarginMin = 0

	withTime(night, func() {
		if q := nextBusinessDayQueue(nil); q != (escalationQueue{}) {
			t.Errorf("cfg nil: %+v", q)
		}
		if q := nextBusinessDayQueue(alwaysOpen); q != (escalationQueue{}) {
			t.Errorf("TESTING_ALWAYS_OPEN: %+v", q)
		}
		if q := nextBusinessDayQueue(noWait); q.queue || !q.window {
			t.Errorf("espera máxima 0 apaga la cola, no la ventana: %+v", q)
		}
	})
	withTime(at(2026, 10, 27, 17, 50), func() {
		if q := nextBusinessDayQueue(noMargin); q.window {
			t.Errorf("con margen 0 las 17:50 todavía son de hoy: %+v", q)
		}
	})
}
