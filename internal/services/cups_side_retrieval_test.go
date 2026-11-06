package services

import "testing"

// Revisión del lote C, ronda 4 (R4-I1): el lado no busca candidatos. El catálogo tiene un solo código para
// los dos lados, así que la palabra del lado nunca coincide y bajaba el parecido hasta dejar a la EMG o la
// NC con lado sin ningún candidato: el procedimiento se descartaba en silencio.
func TestNameCandidates_SideDoesNotLoseCandidates(t *testing.T) {
	catalog := []CupsCandidate{
		{"930860", "ELECTROMIOGRAFÍIA EN CADA EXTREMIDAD (UNO O MAS MUSCULOS)"},
		{"891509", "NEUROCONDUCCION (CADA NERVIO)"},
		{"882317", "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS INFERIORES"},
		{"891530", "POTENCIALES EVOCADOS SOMATOSENSORIALES (275)"},
	}
	for read, want := range map[string]string{
		"EMG MMII D": "930860", "EMG MMII DERECHA": "930860", "EMG MIEMBRO SUPERIOR DERECHO": "930860",
		"EMG MIEMBROS INFERIORES BILATERAL": "930860", "ELECTROMIOGRAFIA MMSS IZQ": "930860",
		"NC MMSS I": "891509", "NEUROCONDUCCION MIEMBRO INFERIOR IZQUIERDO": "891509",
		"DOPPLER VENOSO MMII DERECHO":                                  "882317",
		"POTENCIALES EVOCADOS SOMATOSENSORIALES MIEMBROS INFERIORES D": "891530",
	} {
		res := ResolveCupsByName(read, catalog, false, nil)
		if len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos (se descartaría en silencio)", read)
			continue
		}
		if got := acceptedByAdversary(read, catalog, false); len(got) != 1 || got[0] != want {
			t.Errorf("%q: el modelo confirma %v, quiero solo %s", read, got, want)
		}
	}
}
