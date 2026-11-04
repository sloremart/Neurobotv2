package handlers

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// LA LISTA DE CONFIRM_OCR_RESULT: corta, va en el cuerpo de los botones como siempre; si no cabe en
// 1.024 caracteres, la lista completa sale antes en texto (en varios si pasa de 4.096) y los botones
// llevan solo la pregunta. Nunca se recorta con «…».

func longOrder(n int) []services.CUPSEntry {
	long := strings.Repeat("ULTRASONOGRAFÍA DE ABDOMEN TOTAL HIGADO PANCREAS VESICULA ", 3)
	cups := make([]services.CUPSEntry, 0, n)
	for i := 0; i < n; i++ {
		cups = append(cups, services.CUPSEntry{Code: "x", Name: fmt.Sprintf("%s %d", long, i+1), OrderName: long, Quantity: 2})
	}
	return cups
}

// splitSummary separa los textos de la lista y el mensaje final con botones.
func splitSummary(t *testing.T, msgs []sm.OutboundMessage) ([]string, *sm.ButtonMessage) {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("sin mensajes")
	}
	bm, ok := msgs[len(msgs)-1].(*sm.ButtonMessage)
	if !ok {
		t.Fatalf("el último mensaje lleva los botones, got %T", msgs[len(msgs)-1])
	}
	var texts []string
	for _, m := range msgs[:len(msgs)-1] {
		tm, ok := m.(*sm.TextMessage)
		if !ok {
			t.Fatalf("antes de los botones solo va texto, got %T", m)
		}
		texts = append(texts, tm.Text)
	}
	return texts, bm
}

func TestOCRSummary_ShortListIsOneMessageAsToday(t *testing.T) {
	cups := []services.CUPSEntry{
		{Code: "930810", Name: "ELECTROMIOGRAFIA", Quantity: 4},
		{Code: "891509", Name: "NEUROCONDUCCION", Quantity: 1},
	}
	msgs := ocrSummaryMessages(cups, 1)
	if len(msgs) != 1 {
		t.Fatalf("una lista corta va en un solo mensaje, got %d", len(msgs))
	}
	bm, ok := msgs[0].(*sm.ButtonMessage)
	if !ok {
		t.Fatalf("got %T", msgs[0])
	}
	const want = "Detectamos los siguientes procedimientos en tu orden:\n\n" +
		"1. *ELECTROMIOGRAFIA* — Cantidad: *4*\n2. *NEUROCONDUCCION*\n" +
		"\n⚠️ 1 procedimiento(s) de tu orden no están disponibles y fueron omitidos.\n\n¿Es correcto?"
	if bm.Text != want {
		t.Errorf("cuerpo = %q, quería %q", bm.Text, want)
	}
	if !slices.Equal(bm.Buttons, ocrConfirmButtons()) {
		t.Errorf("botones = %+v", bm.Buttons)
	}
}

func TestOCRSummary_LongListGoesCompleteBeforeTheButtons(t *testing.T) {
	for _, n := range []int{15, 120} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			cups := longOrder(n)
			texts, bm := splitSummary(t, ocrSummaryMessages(cups, 3))
			if len(texts) == 0 {
				t.Fatal("la lista larga va en un texto aparte")
			}
			for i, txt := range texts {
				if c := utf8.RuneCountInString(txt); c > maxTextRunes {
					t.Errorf("texto %d de %d caracteres, máximo %d", i+1, c, maxTextRunes)
				}
			}
			if n > 100 && len(texts) < 2 {
				t.Errorf("una lista de más de 4.096 caracteres se divide, got %d textos", len(texts))
			}
			list := strings.Join(texts, "\n")
			if !strings.HasPrefix(list, "Detectamos los siguientes procedimientos en tu orden:\n\n") {
				t.Errorf("la lista abre con el encabezado de siempre: %q", list[:80])
			}
			if strings.Contains(list, "\n…\n") || strings.HasSuffix(list, "…") {
				t.Error("la lista no se recorta con «…»")
			}
			last := -1
			for i, cup := range cups {
				line := fmt.Sprintf("%d. *%s*", i+1, cup.Name)
				pos := strings.Index(list, line)
				if pos < 0 || pos < last {
					t.Fatalf("falta o está fuera de orden el procedimiento %d", i+1)
				}
				last = pos
			}
			const question = "⚠️ 3 procedimiento(s) de tu orden no están disponibles y fueron omitidos.\n\n¿Es correcto?"
			if bm.Text != question {
				t.Errorf("los botones llevan solo la pregunta, got %q", bm.Text)
			}
			if !slices.Equal(bm.Buttons, ocrConfirmButtons()) {
				t.Errorf("botones = %+v", bm.Buttons)
			}
		})
	}
}

// El retorno sin dato del agente repite lo leído con la misma forma: lista aparte si es larga.
func TestConfirmOCRResult_ResumeWithLongListKeepsItWhole(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(nil, nil))
	cups := longOrder(15)
	cupsJSON, _ := json.Marshal(cups)
	sess := resumeTurnSess(sm.StateConfirmOCRResult, true)
	sess.Context["ocr_cups_json"] = string(cupsJSON)

	res := resumeTurn(t, m, sess, sm.StateConfirmOCRResult)
	texts, bm := splitSummary(t, res.Messages)
	if len(texts) == 0 || bm.Text != "¿Es correcto?" {
		t.Errorf("quería la lista aparte y la pregunta con botones, got %d textos y %q", len(texts), bm.Text)
	}
}
