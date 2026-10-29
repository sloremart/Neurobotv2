package services

import (
	"encoding/json"
	"testing"
)

// UPLOAD_EMG_ORDER: fusionar los CUPS de la 2ª orden (EMG) con los dependientes de la 1ª.
func TestMergeEmgAndDependentCups(t *testing.T) {
	emg := []CUPSEntry{{Code: "930860", Quantity: 2}}

	// Caso normal: EMG (2ª orden) + Onda F guardada (1ª orden).
	mergedJSON, emgN, depN := MergeEmgAndDependentCups(emg, `[{"cups_code":"891514","quantity":1}]`)
	if emgN != 1 || depN != 1 {
		t.Errorf("conteos: emg=%d dep=%d, esperaba 1 y 1", emgN, depN)
	}
	var got []CUPSEntry
	if err := json.Unmarshal([]byte(mergedJSON), &got); err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, c := range got {
		codes[c.Code] = true
	}
	if len(got) != 2 || !codes["930860"] || !codes["891514"] {
		t.Errorf("fusión esperaba {930860, 891514}, got %+v", got)
	}

	// dep vacío → solo EMG.
	_, _, depN = MergeEmgAndDependentCups(emg, "")
	if depN != 0 {
		t.Errorf("dep vacío: esperaba depN=0, got %d", depN)
	}
	// dep inválido → depN=0 (no rompe).
	mj, emgN, depN := MergeEmgAndDependentCups(emg, "no-json")
	if depN != 0 || emgN != 1 || mj == "" {
		t.Errorf("dep inválido: esperaba emg=1 dep=0 y json no vacío, got emg=%d dep=%d json=%q", emgN, depN, mj)
	}
}
