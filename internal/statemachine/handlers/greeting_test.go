package handlers

import (
	"context"
	"fmt"
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

// TestMedicationCheck_AllowedContract_GoesToExcel: un contrato permitido (Sanitas, Capital Salud, FOMAG)
// pide los documentos y sigue a MEDICATION_CHECK_EXCEL, sin escalar. Desde main (2026-11) la búsqueda en
// el listado reemplazó la escalación y el link al canal externo.
func TestMedicationCheck_AllowedContract_GoesToExcel(t *testing.T) {
	for _, contract := range []string{"4", "6", "14", "21"} {
		t.Run(contract, func(t *testing.T) {
			m := sm.NewMachine()
			RegisterGreetingHandlers(m, sampleConfig(), nil)

			sess := testSess(sm.StateMedicationCheckSanitas)
			sess.Context["patient_contract"] = contract
			result, err := m.Process(context.Background(), sess, textM(""))
			if err != nil {
				t.Fatal(err)
			}
			if result.NextState != sm.StateMedicationCheckExcel {
				t.Fatalf("expected MEDICATION_CHECK_EXCEL, got %s", result.NextState)
			}
			if len(result.Messages) < 1 {
				t.Fatal("esperaba el mensaje con los documentos")
			}
			if txt := result.Messages[0].(*sm.TextMessage).Text; !strings.Contains(txt, "Autorización vigente") {
				t.Errorf("esperaba la lista de documentos, got: %s", txt)
			}
		})
	}
}

// TestMedicationCheck_ExternalChannelNoLongerDiverts: con MEDICATION_EXTERNAL_WA_NUMBER configurado el flujo
// es el mismo (listado de medicamentos); el link wa.me ya no se envía.
func TestMedicationCheck_ExternalChannelNoLongerDiverts(t *testing.T) {
	cfg := sampleConfig()
	cfg.MedicationExternalWANumber = "573001234567"
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, cfg, nil)

	sess := testSess(sm.StateMedicationCheckSanitas)
	sess.Context["patient_contract"] = "4"
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMedicationCheckExcel {
		t.Fatalf("expected MEDICATION_CHECK_EXCEL, got %s", result.NextState)
	}
	for _, msg := range result.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok && strings.Contains(tm.Text, "wa.me/") {
			t.Errorf("no debía enviar el link al canal externo: %s", tm.Text)
		}
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
			if got := nextBusinessOpen(config.DefaultBusinessHours(), c.now); !got.Equal(c.want) {
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
			if got := QueuedWaitEnd(nil, c.now); !got.Equal(c.want) {
				t.Errorf("QueuedWaitEnd(%s) = %s, want %s", c.now, got, c.want)
			}
		})
	}
}

func TestBusinessHoursDescription(t *testing.T) {
	want := "Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m."
	if got := BusinessHoursDescription(nil); got != want {
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

// ==================== Horario: el respaldo es el de siempre, minuto a minuto ====================

// legacyHours es una COPIA LITERAL de la implementación del horario anterior a la lectura de
// Néctar (greeting.go hasta 69f2087). Es el oráculo: con NECTAR_SCHEDULE_ENABLED apagada el bot
// tiene que decidir exactamente lo mismo que decidía con el horario quemado.
type legacyHours struct{}

func (legacyHours) open(now time.Time) bool {
	switch now.Weekday() {
	case time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday:
		return now.Hour() >= 7 && now.Hour() < 18
	case time.Saturday:
		return now.Hour() >= 7 && now.Hour() < 12
	default:
		return false
	}
}

func (legacyHours) description() string {
	return "Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m."
}

// queuedNotice es el aviso de la escalación encolada de antes (escalation_nectar.go hasta 69f2087).
func (l legacyHours) queuedNotice() string {
	return fmt.Sprintf("Tu solicitud quedó registrada ✅. "+
		"Un asesor te atenderá el próximo día hábil y te escribirá por aquí. "+
		"Te atendemos %s", l.description())
}

// legacyHelpTail es el cierre de la guía de antes (greeting.go hasta 69f2087).
const legacyHelpTail = "Si en algún momento necesitas ayuda humana, el bot te conectará con un agente.\n\n" +
	"*Horario de atención:*\n" +
	"Lunes a viernes: 7:00 AM - 6:00 PM\n" +
	"Sábados: 7:00 AM - 12:00 PM"

// helpText corre SHOW_HELP y devuelve el texto que recibe el paciente.
func helpText(t *testing.T, cfg *config.Config) string {
	t.Helper()
	res, err := showHelpHandler(cfg)(context.Background(), testSess(sm.StateShowHelp), textM(""))
	if err != nil || len(res.Messages) == 0 {
		t.Fatalf("SHOW_HELP: %v", err)
	}
	return sm.OutboundText(res.Messages[0])
}

func (legacyHours) window(d time.Weekday) (openMin, closeMin int, ok bool) {
	switch d {
	case time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday:
		return 7 * 60, 18 * 60, true
	case time.Saturday:
		return 7 * 60, 12 * 60, true
	default:
		return 0, 0, false
	}
}

func (l legacyHours) minutesToClose(t time.Time) int {
	openMin, closeMin, ok := l.window(t.Weekday())
	minute := t.Hour()*60 + t.Minute()
	if !ok || minute < openMin || minute >= closeMin {
		return 0
	}
	return closeMin - minute
}

func (l legacyHours) nextOpen(now time.Time) time.Time {
	if l.minutesToClose(now) > 0 {
		return now
	}
	minute := now.Hour()*60 + now.Minute()
	for d := 0; d < 8; d++ {
		day := now.AddDate(0, 0, d)
		openMin, _, ok := l.window(day.Weekday())
		if !ok || (d == 0 && minute >= openMin) {
			continue
		}
		return time.Date(day.Year(), day.Month(), day.Day(), openMin/60, openMin%60, 0, 0, now.Location())
	}
	return now
}

func (l legacyHours) queuedWaitEnd(t time.Time) time.Time {
	if left := l.minutesToClose(t); left > 0 {
		closing := t.Truncate(time.Minute).Add(time.Duration(left) * time.Minute)
		return l.nextOpen(closing)
	}
	return l.nextOpen(t)
}

func (l legacyHours) queue(cfg *config.Config, now time.Time) escalationQueue {
	if cfg == nil || cfg.TestingAlwaysOpen {
		return escalationQueue{}
	}
	left := l.minutesToClose(now)
	if left > cfg.BusinessHoursCloseMarginMin {
		return escalationQueue{}
	}
	q := escalationQueue{window: true, reason: queueReasonClosed}
	if left > 0 {
		q.reason = queueReasonMargin
	}
	maxWait := time.Duration(cfg.BusinessHoursQueueMaxWaitMin) * time.Minute
	q.queue = cfg.BusinessHoursQueueMaxWaitMin > 0 && l.queuedWaitEnd(now).Sub(now) <= maxWait
	return q
}

// TestBusinessHours_FallbackEqualsLegacy: con NECTAR_SCHEDULE_ENABLED apagada (sin proveedor, o
// con la caché que nunca recibió una lectura), cada 15 minutos de una semana entera —más el
// minuto 14 de cada cuarto, para pisar los bordes 11:59 / 17:59—, el horario decide lo mismo que
// la implementación de hoy: abierto, próxima apertura, minutos al cierre, fin de la espera
// encolada, la cola del próximo día y el texto.
func TestBusinessHours_FallbackEqualsLegacy(t *testing.T) {
	var legacy legacyHours
	bogota := time.FixedZone("COT", -5*3600)
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, bogota) // lunes

	sources := map[string]func() *config.Config{
		"sin proveedor": queueConfig,
		"caché sin lectura": func() *config.Config {
			cfg := queueConfig()
			cfg.Schedule = config.NewCachedSchedule(config.DefaultBusinessHours())
			return cfg
		},
	}
	for name, mk := range sources {
		t.Run(name, func(t *testing.T) {
			cfg := mk()
			if got, want := BusinessHoursDescription(cfg), legacy.description(); got != want {
				t.Errorf("BusinessHoursDescription = %q, want %q", got, want)
			}
			legacyNoAgents := "En este momento no hay asesores disponibles. 🕐\n\nTe atendemos en horario de *Lunes a Viernes de 7:00 a.m. a 6:00 p.m.* y *Sábados de 7:00 a.m. a 12:00 m.* Por favor escríbenos en ese horario y con gusto te ayudamos. 😊"
			if got := noAgentsText(cfg); got != legacyNoAgents {
				t.Errorf("noAgentsText = %q, want %q", got, legacyNoAgents)
			}
			if got := helpText(t, cfg); !strings.HasSuffix(got, legacyHelpTail+closingNote) {
				t.Errorf("la guía con el horario de siempre conserva su cierre: %q", got)
			}
			for step := 0; step < 7*24*4; step++ {
				for _, extra := range []time.Duration{0, 14 * time.Minute} {
					now := start.Add(time.Duration(step)*15*time.Minute + extra)
					h := cfg.Hours()
					var open bool
					var q escalationQueue
					var notice string
					withTime(now, func() {
						open = businessHoursOpen(cfg)
						q = nextBusinessDayQueue(cfg)
						notice = escalationNoticeText(cfg, true)
					})
					if notice != legacy.queuedNotice() {
						t.Fatalf("%s: escalationNoticeText = %q, want %q", now, notice, legacy.queuedNotice())
					}
					if open != legacy.open(now) {
						t.Fatalf("%s: businessHoursOpen = %v, want %v", now, open, legacy.open(now))
					}
					if got, want := minutesToClose(h, now), legacy.minutesToClose(now); got != want {
						t.Fatalf("%s: minutesToClose = %d, want %d", now, got, want)
					}
					if got, want := nextBusinessOpen(h, now), legacy.nextOpen(now); !got.Equal(want) {
						t.Fatalf("%s: nextBusinessOpen = %s, want %s", now, got, want)
					}
					if got, want := QueuedWaitEnd(cfg, now), legacy.queuedWaitEnd(now); !got.Equal(want) {
						t.Fatalf("%s: QueuedWaitEnd = %s, want %s", now, got, want)
					}
					if want := legacy.queue(cfg, now); q != want {
						t.Fatalf("%s: nextBusinessDayQueue = %+v, want %+v", now, q, want)
					}
				}
			}
		})
	}

	// TESTING_ALWAYS_OPEN sigue mandando sobre el horario (de madrugada, abierto).
	cfg := queueConfig()
	cfg.TestingAlwaysOpen = true
	withTime(start.Add(3*time.Hour), func() {
		if !businessHoursOpen(cfg) {
			t.Error("TESTING_ALWAYS_OPEN tiene que abrir a las 03:00")
		}
	})
}

// TestBusinessHours_FromNectar: una publicación de Néctar con L-V 08:00-17:00 y un festivo el
// lunes cierra ese lunes entero, y el texto que lee el paciente pasa a ser el publicado.
func TestBusinessHours_FromNectar(t *testing.T) {
	bogota := time.FixedZone("COT", -5*3600)
	spec := config.ScheduleSpec{Timezone: config.ScheduleTimezone, Holidays: []string{"2026-11-02"}}
	for d := 1; d <= 5; d++ {
		spec.Windows = append(spec.Windows, config.ScheduleWindowSpec{Weekday: d, From: "08:00", To: "17:00"})
	}
	hours, err := config.BusinessHoursFromSchedule(spec)
	if err != nil {
		t.Fatal(err)
	}
	cache := config.NewCachedSchedule(config.DefaultBusinessHours())
	cache.Set(hours, "v1")
	cfg := queueConfig()
	cfg.Schedule = cache

	holiday := time.Date(2026, 11, 2, 10, 0, 0, 0, bogota) // lunes festivo
	withTime(holiday, func() {
		if businessHoursOpen(cfg) {
			t.Error("el lunes festivo está cerrado")
		}
		if q := nextBusinessDayQueue(cfg); !q.window || q.reason != queueReasonClosed {
			t.Errorf("la escalación del festivo queda para el próximo día hábil: %+v", q)
		}
	})
	if got, want := nextBusinessOpen(cfg.Hours(), holiday), time.Date(2026, 11, 3, 8, 0, 0, 0, bogota); !got.Equal(want) {
		t.Errorf("después del festivo abre el martes a las 8: %s", got)
	}
	tuesday := time.Date(2026, 11, 3, 7, 30, 0, 0, bogota)
	withTime(tuesday, func() {
		if businessHoursOpen(cfg) {
			t.Error("con el horario publicado el martes abre a las 8, no a las 7")
		}
	})
	withTime(tuesday.Add(time.Hour), func() {
		if !businessHoursOpen(cfg) {
			t.Error("el martes a las 8:30 está abierto")
		}
	})
	// Sábado: el respaldo abría, la publicación no.
	withTime(time.Date(2026, 11, 7, 9, 0, 0, 0, bogota), func() {
		if businessHoursOpen(cfg) {
			t.Error("la publicación no trae sábado")
		}
	})
	if got, want := BusinessHoursDescription(cfg), "Lunes a Viernes de 8:00 a.m. a 5:00 p.m."; got != want {
		t.Errorf("BusinessHoursDescription = %q, want %q", got, want)
	}
	if got := noAgentsText(cfg); !strings.Contains(got, "*Lunes a Viernes de 8:00 a.m. a 5:00 p.m.* Por favor") {
		t.Errorf("el desvío sin asesores nombra el horario publicado: %q", got)
	}
}

// Un horario publicado que no abre nunca (Néctar sin ventanas) no puede encolar: la espera
// «hasta la apertura» saldría en cero y el caso quedaría encolado sin fin. Se desvía.
func TestBusinessHours_ClosedAllWeekNeverQueues(t *testing.T) {
	hours, err := config.BusinessHoursFromSchedule(config.ScheduleSpec{Timezone: config.ScheduleTimezone})
	if err != nil {
		t.Fatal(err)
	}
	cache := config.NewCachedSchedule(config.DefaultBusinessHours())
	cache.Set(hours, "v0")
	cfg := queueConfig()
	cfg.Schedule = cache
	withTime(at(2026, 10, 27, 10, 0), func() {
		if businessHoursOpen(cfg) {
			t.Error("sin ventanas está cerrado")
		}
		if q := nextBusinessDayQueue(cfg); !q.window || q.queue {
			t.Errorf("sin ninguna apertura la ventana está activa pero no se encola: %+v", q)
		}
	})

	// Y cada frase que incrusta el horario se lee bien sin él (frases completas).
	if got, want := noAgentsText(cfg), "En este momento no hay asesores disponibles. 🕐\n\n"+
		"Por favor escríbenos más tarde y con gusto te ayudamos. 😊"; got != want {
		t.Errorf("noAgentsText = %q", got)
	}
	withTime(at(2026, 10, 27, 10, 0), func() {
		if got, want := escalationNoticeText(cfg, true), "Tu solicitud quedó registrada ✅. "+
			"Un asesor te atenderá el próximo día hábil y te escribirá por aquí."; got != want {
			t.Errorf("escalationNoticeText = %q", got)
		}
	})
	if got := helpText(t, cfg); !strings.HasSuffix(got, "te conectará con un agente."+closingNote) ||
		strings.Contains(got, "Horario de atención") {
		t.Errorf("la guía sin horario termina en el aviso del agente: %q", got)
	}
}

// publishedConfig arma una configuración que decide con la publicación dada.
func publishedConfig(t *testing.T, windows ...config.ScheduleWindowSpec) *config.Config {
	t.Helper()
	hours, err := config.BusinessHoursFromSchedule(config.ScheduleSpec{Timezone: config.ScheduleTimezone, Windows: windows})
	if err != nil {
		t.Fatal(err)
	}
	cache := config.NewCachedSchedule(config.DefaultBusinessHours())
	cache.Set(hours, "v1")
	cfg := queueConfig()
	cfg.Schedule = cache
	return cfg
}

// Jornada PARTIDA publicada en Néctar (martes 07-12 y 14-17, el volcado real): lo que se encola en
// el margen de las 11:50 o en la pausa del almuerzo se atiende HOY a las 14:00, no «el próximo día
// hábil». El margen de la tarde y la madrugada siguen diciendo «el próximo día hábil».
func TestEscalationNotice_SplitDaySameDay(t *testing.T) {
	cfg := publishedConfig(t,
		config.ScheduleWindowSpec{Weekday: 2, From: "07:00", To: "12:00"},
		config.ScheduleWindowSpec{Weekday: 2, From: "14:00", To: "17:00"},
		config.ScheduleWindowSpec{Weekday: 3, From: "07:00", To: "12:00"},
		config.ScheduleWindowSpec{Weekday: 3, From: "13:00", To: "17:00"},
	)
	tuesday := func(h, m int) time.Time { return at(2026, 10, 27, h, m) }
	cases := []struct {
		name string
		now  time.Time
		when string
	}{
		{"margen antes del almuerzo", tuesday(11, 50), "hoy a partir de las 2:00 p.m."},
		{"en la pausa", tuesday(12, 30), "hoy a partir de las 2:00 p.m."},
		{"reabre a la 1", at(2026, 10, 28, 12, 30), "hoy a partir de la 1:00 p.m."},
		{"margen de la tarde", tuesday(16, 50), "el próximo día hábil"},
		{"madrugada", tuesday(5, 0), "el próximo día hábil"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var q escalationQueue
			var notice string
			withTime(c.now, func() {
				q = nextBusinessDayQueue(cfg)
				notice = escalationNoticeText(cfg, true)
			})
			if !q.queue {
				t.Fatalf("se encola: %+v", q)
			}
			want := "Tu solicitud quedó registrada ✅. Un asesor te atenderá " + c.when +
				" y te escribirá por aquí. Te atendemos " + BusinessHoursDescription(cfg)
			if notice != want {
				t.Errorf("aviso = %q, want %q", notice, want)
			}
		})
	}
}

// La guía nombra el horario publicado en Néctar.
func TestHelpText_PublishedHours(t *testing.T) {
	var w []config.ScheduleWindowSpec
	for d := 1; d <= 5; d++ {
		w = append(w, config.ScheduleWindowSpec{Weekday: d, From: "08:00", To: "17:00"})
	}
	got := helpText(t, publishedConfig(t, w...))
	if !strings.HasSuffix(got, "*Horario de atención:*\nLunes a Viernes de 8:00 a.m. a 5:00 p.m."+closingNote) ||
		strings.Contains(got, "7:00 AM") {
		t.Errorf("la guía nombra el horario publicado: %q", got)
	}
}

// ==================== Anuncio de la línea nueva ====================

// Mientras conviven las dos líneas, los saludos de la línea de Bird anuncian la nueva
// (NEW_LINE_ANNOUNCEMENT) con un texto ANTES del menú; el coalescer lo fusiona al body si cabe.
// Solo con el puente de Néctar armado: la vuelta a Bird apaga el anuncio sin tocar la variable.

const testAnnouncement = "Pronto te atenderemos por nuestra nueva línea de WhatsApp."

// announcementConfig es la configuración con el anuncio y el puente armado.
func announcementConfig(text string) *config.Config {
	cfg := sampleConfig()
	cfg.NewLineAnnouncement = text
	cfg.NectarBridgeArmed = true
	return cfg
}

// Vuelta a Bird (NECTAR_BASE_URL vacía, puente sin armar) con NEW_LINE_ANNOUNCEMENT todavía puesta:
// no hay línea nueva a la que mandar a nadie, el saludo es el de siempre.
func TestNewLineAnnouncement_BridgeOffNoAnnouncement(t *testing.T) {
	cfg := announcementConfig(testAnnouncement)
	cfg.NectarBridgeArmed = false
	assertOnlyList(t, runGreetingState(t, cfg, sm.StateGreeting, "+573001112233"))
	assertOnlyList(t, runGreetingState(t, cfg, sm.StateOutOfHours, "+573001112233"))
}

// runGreetingState corre GREETING u OUT_OF_HOURS con la sesión dada y devuelve sus mensajes.
func runGreetingState(t *testing.T, cfg *config.Config, state, phone string, ctxKV ...string) []sm.OutboundMessage {
	t.Helper()
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, cfg, nil)
	sess := testSess(state)
	sess.PhoneNumber = phone
	for i := 0; i+1 < len(ctxKV); i += 2 {
		sess.Context[ctxKV[i]] = ctxKV[i+1]
	}
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	return result.Messages
}

func assertAnnouncementThenList(t *testing.T, msgs []sm.OutboundMessage) {
	t.Helper()
	if len(msgs) != 2 {
		t.Fatalf("esperaba [TextMessage, ListMessage], got %d mensajes", len(msgs))
	}
	txt, ok := msgs[0].(*sm.TextMessage)
	if !ok || txt.Text != testAnnouncement {
		t.Fatalf("el primer mensaje debe ser el anuncio, got %#v", msgs[0])
	}
	if _, ok := msgs[1].(*sm.ListMessage); !ok {
		t.Fatalf("el segundo mensaje debe ser el menú, got %T", msgs[1])
	}
}

func assertOnlyList(t *testing.T, msgs []sm.OutboundMessage) {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("esperaba solo el menú, got %d mensajes", len(msgs))
	}
	if _, ok := msgs[0].(*sm.ListMessage); !ok {
		t.Fatalf("esperaba ListMessage, got %T", msgs[0])
	}
}

func TestNewLineAnnouncement_BirdSession(t *testing.T) {
	cfg := announcementConfig(testAnnouncement)
	assertAnnouncementThenList(t, runGreetingState(t, cfg, sm.StateGreeting, "+573001112233"))
	assertAnnouncementThenList(t, runGreetingState(t, cfg, sm.StateOutOfHours, "+573001112233"))
}

func TestNewLineAnnouncement_NectarSessionUnchanged(t *testing.T) {
	cfg := announcementConfig(testAnnouncement)
	assertOnlyList(t, runGreetingState(t, cfg, sm.StateGreeting, "nectar:55"))
	assertOnlyList(t, runGreetingState(t, cfg, sm.StateOutOfHours, "nectar:55"))
}

func TestNewLineAnnouncement_EmptyUnchanged(t *testing.T) {
	cfg := announcementConfig("")
	assertOnlyList(t, runGreetingState(t, cfg, sm.StateGreeting, "+573001112233"))
	assertOnlyList(t, runGreetingState(t, cfg, sm.StateOutOfHours, "+573001112233"))
}

func TestNewLineAnnouncement_PostConfirmedSkips(t *testing.T) {
	cfg := announcementConfig(testAnnouncement)
	assertOnlyList(t, runGreetingState(t, cfg, sm.StateGreeting, "+573001112233", "post_confirmed", "1"))
}

// Con la bienvenida real: un anuncio de 300 runas cabe en el body del menú (1 envío); uno de 600
// pasa el tope interactivo y quedan dos mensajes.
func TestNewLineAnnouncement_CoalescedWithRealWelcome(t *testing.T) {
	for _, tc := range []struct {
		runes int
		want  int
	}{{300, 1}, {600, 2}} {
		announcement := strings.Repeat("a", tc.runes)
		msgs := runGreetingState(t, announcementConfig(announcement), sm.StateGreeting, "+573001112233")
		out := sm.CoalesceMessages(msgs)
		if len(out) != tc.want {
			t.Fatalf("anuncio de %d runas: esperaba %d mensajes, got %d", tc.runes, tc.want, len(out))
		}
		if tc.want == 1 {
			lst, ok := out[0].(*sm.ListMessage)
			if !ok || !strings.HasPrefix(lst.Body, announcement+"\n\n") ||
				!strings.HasSuffix(lst.Body, "¿En qué puedo ayudarte hoy?") {
				t.Fatalf("el body debe empezar con el anuncio y conservar la bienvenida: %#v", out[0])
			}
		}
	}
}

// D-08: los festivos de Néctar sobre la semana de siempre cierran el día y saltan en la próxima apertura,
// pero la guía sigue mostrando el horario de siempre, letra por letra.
func TestHolidaysOnly_DecidesButKeepsHelpText(t *testing.T) {
	cot := time.FixedZone("COT", -5*3600)
	hours, err := config.DefaultHoursWithHolidays(config.ScheduleSpec{
		Timezone: config.ScheduleTimezone, Holidays: []string{"2026-11-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Schedule: config.NewStaticSchedule(hours)}

	if got, want := helpHoursBlock(cfg), helpHoursBlock(&config.Config{}); got != want {
		t.Errorf("guía = %q, want la de siempre %q", got, want)
	}
	holiday := time.Date(2026, 11, 16, 10, 0, 0, 0, cot)
	if cfg.Hours().Open(holiday) {
		t.Error("el festivo cierra el día")
	}
	if got, want := cfg.Hours().NextOpen(holiday), time.Date(2026, 11, 17, 7, 0, 0, 0, cot); !got.Equal(want) {
		t.Errorf("próxima apertura = %v, want %v", got, want)
	}
}
