package local

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

func TestIsProactiveEventType(t *testing.T) {
	for _, in := range []string{"reminder_prep_sent", "notification_in_doubt", "notification_timeout", "notification_cancelled_ivr"} {
		if !isProactiveEventType(in) {
			t.Errorf("%s debe ser proactivo", in)
		}
	}
	// Se registran con la clave del pending o la sesión: nunca se etiquetan 'main' a ciegas.
	for _, out := range []string{
		"notification_confirmed", "notification_cancel_acknowledged", "waiting_list_schedule_declined",
		"notification_sent", "notification_ivr_sent", "session_started", "",
	} {
		if isProactiveEventType(out) {
			t.Errorf("%s no debe ser proactivo", out)
		}
	}
}

// El script de corrección lista los mismos tipos que el código: si divergen, uno etiquetaría de más o de menos.
func TestBackfillScriptListsSameTypes(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/backfill-nectar-line-notifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)event_type IN \((.*?)\)`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("el script no tiene event_type IN (...)")
	}
	var inScript []string
	for _, s := range regexp.MustCompile(`'([a-z_]+)'`).FindAllSubmatch(m[1], -1) {
		inScript = append(inScript, string(s[1]))
	}
	var inCode []string
	for k := range proactiveEventTypes {
		inCode = append(inCode, k)
	}
	sort.Strings(inScript)
	sort.Strings(inCode)
	if len(inScript) != len(inCode) {
		t.Fatalf("script %v vs código %v", inScript, inCode)
	}
	for i := range inCode {
		if inScript[i] != inCode[i] {
			t.Fatalf("script %v vs código %v", inScript, inCode)
		}
	}
}
