package services

import "encoding/json"

// MergeEmgAndDependentCups fusiona los CUPS de la orden EMG (2ª orden) con los dependientes
// guardados de la 1ª orden (emg_dep_cups_json). Devuelve el JSON combinado para ocr_cups_json y
// los conteos (emg, dependientes). Un depCupsJSON vacío o inválido → solo los EMG.
func MergeEmgAndDependentCups(emgCups []CUPSEntry, depCupsJSON string) (string, int, int) {
	var depCups []CUPSEntry
	if depCupsJSON != "" {
		_ = json.Unmarshal([]byte(depCupsJSON), &depCups)
	}
	merged := make([]CUPSEntry, 0, len(emgCups)+len(depCups))
	merged = append(merged, emgCups...)
	merged = append(merged, depCups...)
	mergedJSON, _ := json.Marshal(merged)
	return string(mergedJSON), len(emgCups), len(depCups)
}
