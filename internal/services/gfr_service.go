package services

import (
	"fmt"
	"math"
)

type GFRService struct{}

type GFRResult struct {
	Value    float64
	Formula  string // "Schwartz", "CKD-EPI", "Cockcroft-Gault"
	Eligible bool   // true si GFR >= 30
	Message  string
}

func NewGFRService() *GFRService {
	return &GFRService{}
}

// Calculate selecciona la fórmula correcta según edad/enfermedad y calcula GFR.
// babyWeightCat: "bajo" o "normal" (solo relevante si age < 1).
func (s *GFRService) Calculate(age int, gender, diseaseType, babyWeightCat string, creatinine, heightCm, weightKg float64) GFRResult {
	var value float64
	var formula string

	switch {
	case age <= 14:
		value = schwartz(age, gender, babyWeightCat, heightCm, creatinine)
		formula = "Schwartz"

	case age >= 40:
		value = ckdEPI(age, gender, creatinine)
		formula = "CKD-EPI"

	default: // 15-39
		if diseaseType == "disease_renal" || diseaseType == "disease_diabetica" {
			value = cockcroftGault(age, gender, weightKg, creatinine)
			formula = "Cockcroft-Gault"
		} else {
			value = ckdEPI(age, gender, creatinine)
			formula = "CKD-EPI"
		}
	}

	result := GFRResult{
		Value:   math.Round(value*100) / 100, // 2 decimales per R-PROC-02
		Formula: formula,
	}

	switch {
	case result.Value >= 60:
		result.Eligible = true
		result.Message = fmt.Sprintf(
			"Tu tasa de filtración glomerular es *%.2f ml/min*.\n\nPuedes proceder con el examen contrastado.",
			result.Value)
	case result.Value >= 30:
		result.Eligible = true
		result.Message = fmt.Sprintf(
			"Tu tasa de filtración glomerular es *%.2f ml/min*.\n\n"+
				"Puedes proceder, pero se requiere *hidratación previa*. "+
				"Bebe 1 litro de agua repartido entre hoy y la mañana de la cita. "+
				"Evita café y alcohol 24h antes.",
			result.Value)
	default:
		result.Eligible = false
		result.Message = fmt.Sprintf(
			"Tu tasa de filtración glomerular es *%.2f ml/min*.\n\n"+
				"Por seguridad, *no es posible realizar el examen con contraste*. "+
				"Consulta con tu médico tratante para alternativas.",
			result.Value)
	}

	return result
}

// Schwartz (niños <= 14 años): k × altura_cm / creatinina.
// k varía según edad, género y peso al nacer para < 1 año.
// Per R-PROC-02:
//   - < 1 año peso bajo: k = 0.33
//   - < 1 año peso normal: k = 0.45
//   - 1-12 años: k = 0.55
//   - >= 13 años masculino: k = 0.70
//   - >= 13 años femenino: k = 0.55
func schwartz(age int, gender, babyWeightCat string, heightCm, creatinine float64) float64 {
	if creatinine <= 0 {
		return 0
	}

	var k float64
	switch {
	case age < 1:
		if babyWeightCat == "bajo" {
			k = 0.33
		} else {
			k = 0.45
		}
	case age <= 12:
		k = 0.55
	default: // 13-14
		if gender == "M" {
			k = 0.70
		} else {
			k = 0.55
		}
	}

	return (k * heightCm) / creatinine
}

// CKD-EPI (adultos 15-39 sanos)
// Per R-PROC-02:
//   - Female: GFR = 144 × (Cr/0.7)^alpha × 0.993^age
//   - Male:   GFR = 141 × (Cr/0.9)^alpha × 0.993^age
func ckdEPI(age int, gender string, creatinine float64) float64 {
	if creatinine <= 0 {
		return 0
	}

	var kappa, alpha, base float64

	if gender == "F" {
		kappa = 0.7
		alpha = -0.329
		base = 144 // Per documentation
	} else {
		kappa = 0.9
		alpha = -0.411
		base = 141
	}

	scrKappa := creatinine / kappa

	var term1 float64
	if scrKappa < 1 {
		term1 = math.Pow(scrKappa, alpha)
	} else {
		term1 = math.Pow(scrKappa, -1.209)
	}

	return base * term1 * math.Pow(0.993, float64(age))
}

// Cockcroft-Gault (15-39 CON enfermedad renal/diabética — es la única vía que la selecciona).
// Los >= 40 pasaron a CKD-EPI: decía ">= 40" y contradecía al selector de Calculate.
// ((140 - edad) × peso_kg) / (72 × creatinina) [× 0.85 si mujer]
func cockcroftGault(age int, gender string, weightKg, creatinine float64) float64 {
	if creatinine <= 0 {
		return 0
	}

	result := (float64(140-age) * weightKg) / (72.0 * creatinine)

	if gender == "F" {
		result *= 0.85
	}

	return result
}
