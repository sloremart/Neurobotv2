package services

import (
	"math"
	"testing"
)

func almostEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}

func TestGFR_Schwartz(t *testing.T) {
	svc := NewGFRService()

	// Child 10 years, creatinine 0.8, height 140cm
	// Schwartz: k=0.55 (age 1-12), GFR = 0.55 * 140 / 0.8 = 96.25
	result := svc.Calculate(10, "M", "", "", 0.8, 140, 0)

	if result.Formula != "Schwartz" {
		t.Errorf("expected Schwartz formula, got %s", result.Formula)
	}
	if !almostEqual(result.Value, 96.25, 0.1) {
		t.Errorf("expected GFR ~96.25, got %.1f", result.Value)
	}
	if !result.Eligible {
		t.Error("expected eligible for GFR >= 60")
	}
}

func TestGFR_Schwartz_Baby(t *testing.T) {
	svc := NewGFRService()

	// Baby <1 year, low birth weight: k=0.33
	result := svc.Calculate(0, "M", "", "bajo", 0.5, 50, 0)
	if result.Formula != "Schwartz" {
		t.Errorf("expected Schwartz formula, got %s", result.Formula)
	}
	// GFR = 0.33 * 50 / 0.5 = 33.0
	if !almostEqual(result.Value, 33.0, 0.1) {
		t.Errorf("expected GFR ~33.0, got %.1f", result.Value)
	}

	// Baby <1 year, normal weight: k=0.45
	result2 := svc.Calculate(0, "F", "", "normal", 0.5, 50, 0)
	// GFR = 0.45 * 50 / 0.5 = 45.0
	if !almostEqual(result2.Value, 45.0, 0.1) {
		t.Errorf("expected GFR ~45.0, got %.1f", result2.Value)
	}
}

func TestGFR_CKDEPI(t *testing.T) {
	svc := NewGFRService()

	// Male, 25 years, creatinine 1.0, no disease → CKD-EPI
	result := svc.Calculate(25, "M", "", "", 1.0, 0, 0)
	if result.Formula != "CKD-EPI" {
		t.Errorf("expected CKD-EPI formula, got %s", result.Formula)
	}
	// Should produce a reasonable GFR > 90 for healthy young adult
	if result.Value <= 0 {
		t.Errorf("expected positive GFR, got %.1f", result.Value)
	}

	// Female, 25 years, creatinine 0.7 → CKD-EPI with sexFactor=1.018
	resultF := svc.Calculate(25, "F", "", "", 0.7, 0, 0)
	if resultF.Formula != "CKD-EPI" {
		t.Errorf("expected CKD-EPI formula, got %s", resultF.Formula)
	}
	if resultF.Value <= 0 {
		t.Errorf("expected positive GFR, got %.1f", resultF.Value)
	}
}

func TestGFR_CKDEPI_WithDisease(t *testing.T) {
	svc := NewGFRService()

	// 25 years with renal disease → should use Cockcroft-Gault (not CKD-EPI)
	result := svc.Calculate(25, "M", "disease_renal", "", 1.0, 0, 70)
	if result.Formula != "Cockcroft-Gault" {
		t.Errorf("expected Cockcroft-Gault for renal disease, got %s", result.Formula)
	}

	// 25 years with diabetic disease → should use Cockcroft-Gault
	result2 := svc.Calculate(25, "M", "disease_diabetica", "", 1.0, 0, 70)
	if result2.Formula != "Cockcroft-Gault" {
		t.Errorf("expected Cockcroft-Gault for diabetic disease, got %s", result2.Formula)
	}
}

func TestGFR_CockcroftGault(t *testing.T) {
	svc := NewGFRService()

	// Male, 50 years, 70kg, creatinine 1.2
	// CG = ((140 - 50) * 70) / (72 * 1.2) = 6300 / 86.4 = 72.916...
	result := svc.Calculate(50, "M", "", "", 1.2, 0, 70)
	if result.Formula != "Cockcroft-Gault" {
		t.Errorf("expected Cockcroft-Gault formula, got %s", result.Formula)
	}
	if !almostEqual(result.Value, 72.9, 0.2) {
		t.Errorf("expected GFR ~72.9, got %.1f", result.Value)
	}

	// Female, 50 years, 60kg, creatinine 1.2 → multiply by 0.85
	// CG = ((140-50) * 60) / (72 * 1.2) * 0.85 = 5400/86.4 * 0.85 = 53.125
	resultF := svc.Calculate(50, "F", "", "", 1.2, 0, 60)
	if !almostEqual(resultF.Value, 53.1, 0.2) {
		t.Errorf("expected GFR ~53.1, got %.1f", resultF.Value)
	}
}

func TestGFR_ZeroCreatinine(t *testing.T) {
	svc := NewGFRService()

	// All formulas should return 0 for zero creatinine
	tests := []struct {
		name string
		age  int
	}{
		{"Schwartz", 10},
		{"CKD-EPI", 25},
		{"Cockcroft-Gault", 50},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := svc.Calculate(tc.age, "M", "", "", 0, 140, 70)
			if result.Value != 0 {
				t.Errorf("expected GFR 0 for zero creatinine, got %.1f", result.Value)
			}
			if result.Eligible {
				t.Error("expected not eligible for GFR 0")
			}
		})
	}
}

func TestGFR_Eligibility(t *testing.T) {
	svc := NewGFRService()

	tests := []struct {
		name         string
		age          int
		creatinine   float64
		weight       float64
		wantMin      float64
		wantMax      float64
		wantEligible bool
	}{
		// Male 40y, 80kg, low creatinine → high GFR (eligible, no hydration)
		{"high_gfr", 40, 0.5, 80, 60, 300, true},
		// Male 70y, 80kg, high creatinine → low GFR (not eligible)
		{"low_gfr", 70, 5.0, 60, 0, 30, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := svc.Calculate(tc.age, "M", "", "", tc.creatinine, 0, tc.weight)
			if result.Value < tc.wantMin || result.Value > tc.wantMax {
				t.Errorf("GFR %.1f outside expected range [%.1f, %.1f]", result.Value, tc.wantMin, tc.wantMax)
			}
			if result.Eligible != tc.wantEligible {
				t.Errorf("eligible=%v, want %v (GFR=%.1f)", result.Eligible, tc.wantEligible, result.Value)
			}
		})
	}
}

func TestGFR_HydrationWarning(t *testing.T) {
	svc := NewGFRService()

	// Male 60y, 70kg, creatinine that gives GFR in 30-59 range (hydration needed)
	// CG: (140-60)*70 / (72 * 2.0) = 5600/144 = 38.89
	result := svc.Calculate(60, "M", "", "", 2.0, 0, 70)
	if !result.Eligible {
		t.Error("GFR 30-59 should be eligible (with hydration)")
	}
	if result.Value < 30 || result.Value >= 60 {
		t.Errorf("expected GFR in 30-59, got %.1f", result.Value)
	}
	if result.Message == "" {
		t.Error("expected hydration message")
	}
}

func TestGFR_HighGFR_NoHydration(t *testing.T) {
	svc := NewGFRService()

	// Male 25y, creatinine 0.5 → high GFR, no hydration needed
	result := svc.Calculate(25, "M", "", "", 0.5, 0, 0)
	if result.Value < 60 {
		t.Errorf("expected GFR >= 60, got %.1f", result.Value)
	}
	if result.Eligible != true {
		t.Error("expected eligible")
	}
}

func TestGFR_Schwartz_BabyLowWeight(t *testing.T) {
	svc := NewGFRService()

	// Baby < 1 year, low weight category: k=0.33
	// Schwartz: GFR = 0.33 * 50 / 0.4 = 41.25 (in 30-59 range = eligible with hydration)
	result := svc.Calculate(0, "M", "", "bajo", 0.4, 50, 0)
	if result.Formula != "Schwartz" {
		t.Errorf("expected Schwartz formula, got %s", result.Formula)
	}
	if !almostEqual(result.Value, 41.25, 0.1) {
		t.Errorf("expected GFR ~41.25, got %.2f", result.Value)
	}
	// GFR 41.25 is in the 30-59 range: eligible with hydration warning
	if !result.Eligible {
		t.Error("expected eligible (with hydration) for GFR in 30-59 range")
	}
	if result.Message == "" {
		t.Error("expected hydration warning message for GFR in 30-59 range")
	}
}

func TestGFR_Schwartz_TeenFemale(t *testing.T) {
	svc := NewGFRService()

	// Female 14 years: k=0.55 (female 13-14)
	// Schwartz: GFR = 0.55 * 160 / 0.8 = 110.0
	result := svc.Calculate(14, "F", "", "", 0.8, 160, 0)
	if result.Formula != "Schwartz" {
		t.Errorf("expected Schwartz formula, got %s", result.Formula)
	}
	if !almostEqual(result.Value, 110.0, 0.1) {
		t.Errorf("expected GFR ~110.0, got %.2f", result.Value)
	}
	if !result.Eligible {
		t.Error("expected eligible for GFR >= 60")
	}
}

func TestGFR_Schwartz_TeenMale(t *testing.T) {
	svc := NewGFRService()

	// Male 13 years: k=0.70 (male 13+)
	// Schwartz: GFR = 0.70 * 155 / 1.0 = 108.5
	result := svc.Calculate(13, "M", "", "", 1.0, 155, 0)
	if result.Formula != "Schwartz" {
		t.Errorf("expected Schwartz formula, got %s", result.Formula)
	}
	if !almostEqual(result.Value, 108.5, 0.1) {
		t.Errorf("expected GFR ~108.5, got %.2f", result.Value)
	}
	if !result.Eligible {
		t.Error("expected eligible for GFR >= 60")
	}
}

func TestGFR_Schwartz_ZeroCreatinine(t *testing.T) {
	svc := NewGFRService()

	// Zero creatinine should short-circuit in schwartz, returning GFR=0
	result := svc.Calculate(5, "M", "", "", 0, 120, 0)
	if result.Formula != "Schwartz" {
		t.Errorf("expected Schwartz formula, got %s", result.Formula)
	}
	if result.Value != 0 {
		t.Errorf("expected GFR 0 for zero creatinine, got %.2f", result.Value)
	}
	if result.Eligible {
		t.Error("expected NOT eligible for GFR 0")
	}
}
