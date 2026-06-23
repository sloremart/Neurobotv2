package handlers

import (
	"context"
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
