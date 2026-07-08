package handlers

// EPS contract resolution.
//
// Bookings under an EPS entity must be tied to a specific contract whose tariff
// depends on the affiliation régimen (contributivo vs subsidiado) and, for
// SANITAS, on the patient's residence municipality (MRC vs Evento model).
//
// Verified against the SIESA DB (contratos table, regimen IN (1,2)):
//
//	EPS          empresa   contributivo            subsidiado
//	Salud Total  EPS002    13                      12
//	Sanitas      EPS005    MRC 6 / Evento 4        MRC 5 / Evento 7
//	Compensar    EPS008    16                      17
//	Capital      EPSC34    14                      15

// SIESA empresa codes handled by the régimen-based contract matrix.
const (
	entitySanitas    = "EPS005"
	entitySaludTotal = "EPS002"
	entityCompensar  = "EPS008"
	entityCapital    = "EPSC34"
)

// particularEntityCode is the standard out-of-pocket entity (PART02, "PARTICULAR
// 10% DESCUENTO"). PARTICULAR is a single option, so the bot skips the entity list
// and assigns it directly; lookupContract resolves PART02 → its active contract.
const particularEntityCode = "PART02"

// sanitasMRCMunis are the Meta-department (cod_dep = "50") municipality codes
// whose SANITAS affiliates fall under the MRC (Modelo de Riesgo Compartido)
// model. Any other municipality maps to the Evento contracts. Verified against
// sis_muni.
var sanitasMRCMunis = map[string]bool{
	"001": true, // Villavicencio (capital del Meta) — incorporado al MRC 2026-07 (antes era Evento)
	"006": true, // Acacías
	"110": true, // Barranca de Upía
	"124": true, // Cabuyaro
	"150": true, // Castilla la Nueva
	"223": true, // Cubarral
	"226": true, // Cumaral
	"313": true, // Granada
	"568": true, // Puerto Gaitán
	"573": true, // Puerto López
	"590": true, // Puerto Rico
	"680": true, // San Carlos de Guaroa
	"689": true, // San Martín
}

// isEPSWithMatrix reports whether the entity participates in the régimen-based
// contract matrix.
func isEPSWithMatrix(entityCode string) bool {
	switch entityCode {
	case entitySanitas, entitySaludTotal, entityCompensar, entityCapital:
		return true
	}
	return false
}

// isSanitasMRC reports whether a SANITAS affiliate residing in (depCode, muniCode)
// belongs to the MRC model. Only Meta-department (dep "50") municipalities in the
// explicit list qualify; everything else is Evento.
func isSanitasMRC(depCode, muniCode string) bool {
	return depCode == "50" && sanitasMRCMunis[muniCode]
}

// resolveEPSContract returns the contract code for an EPS affiliate given the
// régimen ("1" = contributivo, "2" = subsidiado) and, for SANITAS, the residence
// municipality. Returns "" when the entity is not handled by the matrix.
func resolveEPSContract(entityCode, regimen, depCode, muniCode string) string {
	subsidized := regimen == "2"
	switch entityCode {
	case entitySanitas:
		if isSanitasMRC(depCode, muniCode) {
			if subsidized {
				return "5"
			}
			return "6"
		}
		if subsidized {
			return "7"
		}
		return "4"
	case entitySaludTotal:
		if subsidized {
			return "12"
		}
		return "13"
	case entityCompensar:
		if subsidized {
			return "17"
		}
		return "16"
	case entityCapital:
		if subsidized {
			return "15"
		}
		return "14"
	}
	return ""
}
