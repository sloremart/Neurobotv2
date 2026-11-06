package services

import (
	"slices"
	"testing"
)

// Revisión del lote C, ronda 5 (R5-M2): 879523-1 pone entre paréntesis lo que lo distingue («AXIALES DE ROTULA O
// LONGITUD DE MIEMBROS INFERIORES»), y como la anotación no obliga, «TAC MIEMBROS INFERIORES PEDIATRICA» encajaba
// como único pediátrico y se asignaba a otro procedimiento que el de 879520 a secas. Cuando el paréntesis del único
// candidato que encaja nombra un subtipo o subregión que el leído no dice y que otra fila de la misma familia
// escribe en su nombre (879523), es duda. Las anotaciones de facturación o de equipo siguen sin obligar: «RX
// TORAX» (871121), «TAC CUELLO» (879161), «TAC ORBITAS» (879121) y «EMG» (930860) se asignan como antes.
func TestResolveCupsByName_DistinguishingAnnotationIsADoubt(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	for _, read := range []string{"TAC MIEMBROS INFERIORES PEDIATRICA", "TAC MMII PEDIATRICO", "TAC DE MIEMBROS INFERIORES PEDIATRIA"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
	}
	for read, want := range map[string]string{
		"RX TORAX": "871121", "RX DE TORAX": "871121", "TAC CUELLO": "879161", "TAC ORBITAS": "879121", "EMG": "930860",
		"TAC ABDOMEN Y PELVIS": "879420", "TAC SILLA TURCA": "879116", "ECO TORAX": "881211", "RX CARA": "870101",
		"TAC MIEMBROS INFERIORES Y ARTICULACIONES": "879520",
	} {
		if got := acceptedByAdversary(read, catalog, false); !slices.Equal(got, []string{want}) {
			t.Errorf("%q → %v, quiero [%s]", read, got, want)
		}
	}
}
