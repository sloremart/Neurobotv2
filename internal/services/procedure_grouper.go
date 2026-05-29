package services

import (
	"context"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/repository"
)

// ── Resonancia Magnética (883xxx) rules ──────────────────────────────────────
// Slots required per CUPS code depending on whether the exam is contrasted.
// Source: config/ai.php from legacy Laravel system.
type resonanciaSlots struct{ Simple, Contrasted int }

var resonanciaCodes = map[string]resonanciaSlots{
	"883101": {1, 2}, // cerebro
	"883102": {1, 2}, // base de cráneo / silla turca
	"883103": {1, 2}, // órbitas
	"883104": {1, 2}, // cerebro funcional
	"883105": {1, 2}, // articulación temporomandibular
	"883106": {1, 2}, // tractografía (cerebro)
	"883107": {1, 2}, // dinámica de LCR
	"883108": {1, 2}, // pares craneanos
	"883109": {1, 2}, // oídos
	"883110": {1, 2}, // senos paranasales / cara
	"883111": {1, 2}, // cuello
	"883112": {1, 2}, // hipocampo volumétrico
	"883141": {1, 2}, // cerebro simple (variant)
	"883210": {1, 2}, // columna cervical
	"883211": {2, 2}, // columna cervical con contraste
	"883220": {1, 2}, // columna torácica
	"883221": {2, 2}, // columna torácica con contraste
	"883230": {1, 2}, // columna lumbosacra
	"883231": {2, 2}, // columna lumbar con contraste
	"883232": {1, 2}, // sacroilíaca
	"883233": {2, 2}, // sacroilíaca con contraste
	"883234": {1, 2}, // sacrococcígea
	"883235": {2, 2}, // sacrococcígea con contraste
	"883301": {1, 2}, // tórax
	"883321": {1, 2}, // corazón (morfología)
	"883341": {1, 2}, // angiorresonancia de tórax
	"883351": {2, 3}, // mama
	"883401": {1, 2}, // abdomen
	"883430": {1, 2}, // vías biliares
	"883434": {2, 3}, // colangioresonancia
	"883435": {1, 2}, // urorresonancia
	"883436": {1, 2}, // enterorresonancia
	"883440": {2, 2}, // pelvis
	"883441": {2, 3}, // dinámica de piso pélvico
	"883442": {1, 2}, // obstétrica
	"883443": {1, 2}, // placenta
	"883511": {1, 2}, // miembro superior (sin articulaciones)
	"883512": {1, 2}, // articulaciones miembro superior
	"883521": {1, 2}, // miembro inferior (sin articulaciones)
	"883522": {1, 2}, // articulaciones miembro inferior
	"883560": {1, 2}, // plexo braquial
	"883590": {1, 2}, // sistema músculo-esquelético
	"883902": {1, 2}, // RM con perfusión
	"883904": {1, 2}, // RM de sitio no especificado
	"883909": {1, 2}, // RM con angiografía
	"883913": {1, 2}, // difusión
}

// Combination rules: when all listed codes appear together, use combined slot count
// instead of summing individual slots. Source: legacy Laravel ai.php config.
type resonanciaCombination struct {
	codes      []string
	simple     int
	contrasted int
}

var resonanciaCombinations = []resonanciaCombination{
	{codes: []string{"883401", "883440"}, simple: 2, contrasted: 3}, // abdomen + pelvis
	{codes: []string{"883902", "883904"}, simple: 1, contrasted: 2}, // perfusión combination
}

// sedacionResonanciaCode adds extra slots when combined with resonancia.
const sedacionResonanciaCode = "998702"

// SpacesForCUPS returns the number of consecutive slots required for a resonancia CUPS code.
// Returns (spaces, true) if the code is in the resonancia map; (1, false) otherwise.
func SpacesForCUPS(code string, isContrasted bool) (int, bool) {
	if slots, ok := resonanciaCodes[code]; ok {
		if isContrasted {
			return slots.Contrasted, true
		}
		return slots.Simple, true
	}
	return 1, false
}

// ── Fisiatría EMG code groups (from institutional rules) ─────────────────────
var emgCodes = map[string]bool{
	"29120": true, "930810": true, "892302": true, "892301": true,
	"930820": true, "930860": true, "893601": true, "930801": true,
	"29101": true, "000005": true, "000006": true, "000004": true,
}

var ncCodes = map[string]bool{
	"29103":  true, "891509": true, "29102": true,
	"891098": true, // NEUROCONDUCCION POR CADA EXTREMIDAD
}

var emgDependentCodes = map[string]bool{
	"891514": true, "891515": true,
	"891503": true, // REFLEJO NEUROLOGICO TRIGEMINO FACIAL
}

// ── Radiografía (870-873xxx) rules ──────────────────────────────────────────
// All Rx grouped into 1 appointment. Spaces = sum of individual (1 default, exceptions 2-3).
// Source: config/ai.php from legacy Laravel system.
var radiografiaExceptions = map[string]int{
	"871060": 3, // Columna vertebral total
	"873302": 3, // Medición miembros inferiores / pie plano
	"871030": 2, // Columna dorsolumbar
	"871040": 2, // Columna lumbosacra
	"871050": 2, // Sacro-cóccix
	"870005": 2, // Mastoides comparativas
	"873123": 2, // Extremidades superiores comparativas
	"873202": 2, // Articulaciones acromioclaviculares comparativas
	"873303": 2, // Pies con apoyo comparativos
	"873412": 2, // Pelvis (cadera) comparativa
	"873422": 2, // Rodillas de pie comparativas
	"873443": 2, // Extremidades inferiores comparativas
	"873444": 2, // Proyecciones adicionales extremidades
}

// ── Tomografía (879xxx) rules ───────────────────────────────────────────────
// Simple=1, Contrasted=2. Fixed codes override. 879910 (3D) always = 3 total.
var tomografiaFixedCodes = map[string]int{
	"879112": 2, // Cráneo con contraste
	"879113": 2, // Cráneo simple y contrastado
}

const tomografia3DCode = "879910" // Reconstrucción 3D → siempre 3 espacios

// ── Ecografía (881-882xxx) rules ────────────────────────────────────────────
// Obstetric = 2 spaces, Doppler = qty-based, rest = 1. All in 1 appointment.
var ecografiaObstetric = map[string]bool{
	"881436": true, // Obstétrica translucencia nucal → 2 espacios
	"881437": true, // Obstétrica detalle anatómico → 2 espacios
}
var ecografiaDoppler = map[string]bool{
	"882308": true, // Doppler arterial MMII
	"882309": true, // Doppler venoso MMSS
	"882316": true, // Doppler venoso MS
	"882317": true, // Doppler venoso MMII
	"882318": true, // Doppler venoso MI
}

// ── Neurología rules ────────────────────────────────────────────────────────
// 890274/890374 never together. 053105 always separate with 1 fixed space.
const neurologiaProcedureCode = "053105" // Bloqueo - siempre 1 espacio fijo

// ── Bilateral rules ─────────────────────────────────────────────────────────
// If "bilateral" appears in procedure observations, multiply spaces ×2.
// Only applies to specific services/codes. Source: config/ai.php from legacy Laravel.

// resonanciaBilateralCodes are RM codes for extremities/joints where bilateral ×2 applies.
var resonanciaBilateralCodes = map[string]bool{
	"883511": true, // miembro superior (sin articulaciones)
	"883512": true, // articulaciones miembro superior
	"883521": true, // miembro inferior (sin articulaciones)
	"883522": true, // articulaciones miembro inferior
	"883560": true, // plexo braquial
	"883590": true, // sistema músculo-esquelético
}

// radiografiaComparativeCodes already include both sides; bilateral does NOT apply.
var radiografiaComparativeCodes = map[string]bool{
	"873123": true, // Extremidades superiores comparativas
	"873202": true, // Articulaciones acromioclaviculares comparativas
	"873303": true, // Pies con apoyo comparativos
	"873412": true, // Pelvis (cadera) comparativa
	"873422": true, // Rodillas de pie comparativas
	"873443": true, // Extremidades inferiores comparativas
}

// isBilateral checks if observations contain "bilateral" (case-insensitive).
func isBilateral(obs string) bool {
	return strings.Contains(strings.ToLower(obs), "bilateral")
}

type enrichedCup struct {
	CUPSEntry
	ServiceName    string
	RequiredSpaces int
}

// forceServiceByCode returns a service name for CUPS codes that belong to a known
// service, regardless of what the DB says. Returns "" to use DB value.
func forceServiceByCode(code string) string {
	// Fisiatria EMG/NC
	if emgCodes[code] || ncCodes[code] || emgDependentCodes[code] {
		return "Fisiatria"
	}
	// Resonancia
	if _, ok := resonanciaCodes[code]; ok {
		return "Resonancia"
	}
	if code == sedacionResonanciaCode {
		return "Resonancia"
	}
	// Neurología (exact codes)
	if code == neurologiaProcedureCode || code == "890274" || code == "890374" {
		return "Neurologia"
	}
	// Tomografía (879xxx)
	if strings.HasPrefix(code, "879") {
		return "Tomografia"
	}
	// Ecografía (881xxx, 882xxx)
	if strings.HasPrefix(code, "881") || strings.HasPrefix(code, "882") {
		return "Ecografia"
	}
	// Radiografía (870-873xxx)
	if strings.HasPrefix(code, "870") || strings.HasPrefix(code, "871") || strings.HasPrefix(code, "872") || strings.HasPrefix(code, "873") {
		return "Radiografia"
	}
	return ""
}

// GroupByServiceFromDB groups CUPS entries by service using DB data (deterministic, no AI).
// Each CUPS is looked up in the DB to get ServiceName and RequiredSpaces.
// Special rules apply for Fisiatría (EMG/NC grouping).
func GroupByServiceFromDB(ctx context.Context, cups []CUPSEntry, procRepo repository.ProcedureRepository) ([]CUPSGroup, error) {
	if len(cups) == 0 {
		return []CUPSGroup{{ServiceType: "General", Cups: cups, Espacios: 1}}, nil
	}

	// Enrich each CUPS with DB data; skip inactive/unknown codes
	enriched := make([]enrichedCup, 0, len(cups))
	for _, c := range cups {
		ec := enrichedCup{CUPSEntry: c, ServiceName: "General", RequiredSpaces: 1}
		if c.Code != "" {
			proc, err := procRepo.FindByCode(ctx, c.Code)
			if err != nil || proc == nil || !proc.IsActive {
				// Code not found or inactive (activo=0) — skip it
				continue
			}
			ec.Name = proc.Name
			// Use DB service name first
			if proc.ServiceName != "" {
				ec.ServiceName = proc.ServiceName
			}
			// Force-mapping overrides DB when code belongs to a known service
			if forced := forceServiceByCode(c.Code); forced != "" {
				ec.ServiceName = forced
			}
			if proc.RequiredSpaces >= 1 {
				ec.RequiredSpaces = proc.RequiredSpaces
			}
		}
		enriched = append(enriched, ec)
	}

	// Group by service name (maintain insertion order)
	groupOrder := []string{}
	groupMap := map[string][]enrichedCup{}
	for _, ec := range enriched {
		if _, exists := groupMap[ec.ServiceName]; !exists {
			groupOrder = append(groupOrder, ec.ServiceName)
		}
		groupMap[ec.ServiceName] = append(groupMap[ec.ServiceName], ec)
	}

	// Build result groups
	groups := make([]CUPSGroup, 0, len(groupOrder))
	for _, svc := range groupOrder {
		ecs := groupMap[svc]
		cupEntries := make([]CUPSEntry, len(ecs))
		totalEspacios := 0
		for i, ec := range ecs {
			cupEntries[i] = ec.CUPSEntry
			// Only EMG codes multiply spaces by quantity (EMG count drives slot calculation).
			// All other codes (NC, dependents, Potenciales Evocados, etc.) always use
			// RequiredSpaces as-is — quantity is clinical repetitions, not extra slots.
			if emgCodes[ec.Code] {
				totalEspacios += ec.RequiredSpaces * ec.Quantity
			} else {
				totalEspacios += ec.RequiredSpaces
			}
		}
		if totalEspacios < 1 {
			totalEspacios = 1
		}
		groups = append(groups, CUPSGroup{
			ServiceType: svc,
			Cups:        cupEntries,
			Espacios:    totalEspacios,
		})
	}

	// Apply service-specific rules
	var finalGroups []CUPSGroup
	for _, g := range groups {
		svc := strings.ToLower(g.ServiceType)
		switch {
		case svc == "fisiatria" || svc == "fisiatría":
			finalGroups = append(finalGroups, applyFisiatriaRules(g))
		case svc == "resonancia":
			finalGroups = append(finalGroups, applyResonanciaRules(g))
		case svc == "radiografia" || svc == "radiografía":
			finalGroups = append(finalGroups, applyRadiografiaRules(g))
		case svc == "tomografia" || svc == "tomografía":
			finalGroups = append(finalGroups, applyTomografiaRules(g))
		case svc == "ecografia" || svc == "ecografía":
			finalGroups = append(finalGroups, applyEcografiaRules(g))
		case svc == "neurologia" || svc == "neurología":
			finalGroups = append(finalGroups, applyNeurologiaRules(g)...)
		default:
			finalGroups = append(finalGroups, g)
		}
	}

	return finalGroups, nil
}

// applyResonanciaRules calculates the correct number of consecutive slots for an
// MRI group based on:
//  1. Whether each exam is contrasted (IsContrasted on the CUPSEntry)
//  2. Combination rules (e.g. abdomen+pelvis together = 3 slots contrasted, not 4)
//  3. Sedation code 998702 (adds +1 simple / +2 contrasted, never alone)
func applyResonanciaRules(g CUPSGroup) CUPSGroup {
	// Determine if any RM in the group is contrasted
	isContrasted := false
	for _, c := range g.Cups {
		if c.IsContrasted {
			isContrasted = true
			break
		}
	}

	// Collect present RM codes (excluding sedation)
	codeSet := make(map[string]bool)
	for _, c := range g.Cups {
		if c.Code != sedacionResonanciaCode {
			codeSet[c.Code] = true
		}
	}

	// Check if any combination rule applies (all codes in rule must be present)
	combinationSpaces := -1
	for _, combo := range resonanciaCombinations {
		allPresent := true
		for _, code := range combo.codes {
			if !codeSet[code] {
				allPresent = false
				break
			}
		}
		if allPresent {
			if isContrasted {
				combinationSpaces = combo.contrasted
			} else {
				combinationSpaces = combo.simple
			}
			break // use first matching combination
		}
	}

	totalSpaces := 0
	if combinationSpaces >= 0 {
		// Use combined slot count; add individual slots for any codes NOT in the combination
		// (find which combination matched and which codes are outside it)
		totalSpaces = combinationSpaces
		// Add slots for RM codes not covered by the combination
		for _, c := range g.Cups {
			if c.Code == sedacionResonanciaCode {
				continue
			}
			inCombo := false
			for _, combo := range resonanciaCombinations {
				if combinationSpaces >= 0 {
					for _, cc := range combo.codes {
						if cc == c.Code {
							inCombo = true
							break
						}
					}
				}
			}
			if !inCombo {
				spaces := 0
				if slots, ok := resonanciaCodes[c.Code]; ok {
					if isContrasted {
						spaces = slots.Contrasted
					} else {
						spaces = slots.Simple
					}
				} else {
					spaces = 1
				}
				// Bilateral ×2 for eligible extremity/joint codes
				if isBilateral(c.Observations) && resonanciaBilateralCodes[c.Code] {
					spaces *= 2
				}
				totalSpaces += spaces * c.Quantity
			}
		}
	} else {
		// No combination: sum individual slots per code
		for _, c := range g.Cups {
			if c.Code == sedacionResonanciaCode {
				continue
			}
			spaces := 0
			if slots, ok := resonanciaCodes[c.Code]; ok {
				if isContrasted {
					spaces = slots.Contrasted
				} else {
					spaces = slots.Simple
				}
			} else {
				spaces = 1
			}
			// Bilateral ×2 for eligible extremity/joint codes
			if isBilateral(c.Observations) && resonanciaBilateralCodes[c.Code] {
				spaces *= 2
			}
			totalSpaces += spaces * c.Quantity
		}
	}

	// Add sedation slots: +1 simple, +2 contrasted (only if there are other RM procedures)
	hasSedacion := false
	for _, c := range g.Cups {
		if c.Code == sedacionResonanciaCode {
			hasSedacion = true
			break
		}
	}
	if hasSedacion && totalSpaces > 0 {
		if isContrasted {
			totalSpaces += 2
		} else {
			totalSpaces += 1
		}
	}

	if totalSpaces < 1 {
		totalSpaces = 1
	}
	g.Espacios = totalSpaces
	return g
}

// applyFisiatriaRules implements institutional EMG/NC grouping rules:
//   - Max 4 EMG (one per extremity). If order has EMG > 4 and NC is present,
//     NC qty is the ground truth: correctedEMG = NCqty / 4.
//     If no NC, cap at 4.
//   - NC quantity = correctedEMG × 4
//   - If no NC in order, add 891509
//   - If NC/dependent codes exist without EMG, remove them
//   - Espacios: correctedEMG ≤ 3 → 1, ≥ 4 → 2
func applyFisiatriaRules(g CUPSGroup) CUPSGroup {
	totalEMG := 0
	hasNC := false
	orderNCQty := 0

	for _, c := range g.Cups {
		if emgCodes[c.Code] {
			totalEMG += c.Quantity
		}
		if ncCodes[c.Code] && !hasNC {
			hasNC = true
			orderNCQty = c.Quantity
		}
	}

	// No EMG procedures → remove NC/dependent codes only if hay otros procedimientos.
	// Si el grupo es SOLO NC/dependientes (neuroconducción u onda F sin EMG),
	// mantenerlos como procedimiento independiente en agenda de Fisiatría.
	if totalEMG == 0 {
		var valid, ncAndDeps []CUPSEntry
		for _, c := range g.Cups {
			if ncCodes[c.Code] || emgDependentCodes[c.Code] {
				ncAndDeps = append(ncAndDeps, c)
			} else {
				valid = append(valid, c)
			}
		}
		if len(valid) > 0 {
			// Hay otros procedimientos — descartar NC/dependientes sin EMG
			g.Cups = valid
			g.Espacios = len(valid)
			if g.Espacios < 1 {
				g.Espacios = 1
			}
			return g
		}
		// Solo NC/dependientes sin EMG → procedimiento independiente (1 slot)
		g.Cups = ncAndDeps
		g.Espacios = 1
		return g
	}

	// Correct EMG if physiologically impossible (max 4 extremities).
	// When order has EMG > 4, NC quantity is the authoritative source:
	// correctedEMG = orderNCQty / 4. Without NC, cap at 4.
	correctedEMG := totalEMG
	if totalEMG > 4 {
		if hasNC && orderNCQty >= 4 {
			correctedEMG = orderNCQty / 4
			if correctedEMG < 1 {
				correctedEMG = 1
			}
			if correctedEMG > 4 {
				correctedEMG = 4
			}
		} else {
			correctedEMG = 4
		}
	}

	ncQuantity := correctedEMG * 4

	// Rebuild g.Cups: one EMG entry (corrected qty), one NC entry (ncQuantity),
	// keep all other non-EMG/NC codes (dependents, etc). Skip duplicates.
	rebuilt := make([]CUPSEntry, 0, len(g.Cups))
	emgAdded := false
	ncAdded := false
	for _, c := range g.Cups {
		switch {
		case emgCodes[c.Code]:
			if !emgAdded {
				c.Quantity = correctedEMG
				rebuilt = append(rebuilt, c)
				emgAdded = true
			}
		case ncCodes[c.Code]:
			if !ncAdded {
				c.Quantity = ncQuantity
				rebuilt = append(rebuilt, c)
				ncAdded = true
			}
		default:
			rebuilt = append(rebuilt, c)
		}
	}
	if !ncAdded {
		rebuilt = append(rebuilt, CUPSEntry{
			Code:     "891509",
			Name:     "NEUROCONDUCCION (CADA NERVIO)",
			Quantity: ncQuantity,
		})
	}
	g.Cups = rebuilt

	if correctedEMG <= 3 {
		g.Espacios = 1
	} else {
		g.Espacios = 2
	}

	return g
}

// applyRadiografiaRules: all Rx in 1 appointment. Spaces = sum of individual
// (1 default, exceptions with 2-3 spaces). Source: legacy Laravel ai.php.
func applyRadiografiaRules(g CUPSGroup) CUPSGroup {
	totalSpaces := 0
	for _, c := range g.Cups {
		qty := c.Quantity
		if qty < 1 {
			qty = 1
		}
		spaces := 1 // default Rx = 1 space
		if exc, ok := radiografiaExceptions[c.Code]; ok {
			spaces = exc
		}
		// Bilateral ×2: only if NOT a comparative code (those already include both sides)
		if isBilateral(c.Observations) && !radiografiaComparativeCodes[c.Code] {
			spaces *= 2
		}
		totalSpaces += spaces * qty
	}
	if totalSpaces < 1 {
		totalSpaces = 1
	}
	g.Espacios = totalSpaces
	return g
}

// applyTomografiaRules: 879910 (3D) → always 3. Otherwise simple=1, contrasted=2.
// Fixed codes override. Source: legacy Laravel ai.php.
func applyTomografiaRules(g CUPSGroup) CUPSGroup {
	// Override: 879910 (3D) → entire appointment = 3 spaces
	for _, c := range g.Cups {
		if c.Code == tomografia3DCode {
			g.Espacios = 3
			return g
		}
	}
	totalSpaces := 0
	for _, c := range g.Cups {
		qty := c.Quantity
		if qty < 1 {
			qty = 1
		}
		spaces := 1
		if fixed, ok := tomografiaFixedCodes[c.Code]; ok {
			spaces = fixed
		} else if c.IsContrasted {
			spaces = 2
		}
		// Bilateral ×2 for extremities/joints
		if isBilateral(c.Observations) {
			spaces *= 2
		}
		totalSpaces += spaces * qty
	}
	if totalSpaces < 1 {
		totalSpaces = 1
	}
	g.Espacios = totalSpaces
	return g
}

// applyEcografiaRules: obstetric=2, doppler=qty-based, rest=1. All in 1 appointment.
// Source: legacy Laravel ai.php.
func applyEcografiaRules(g CUPSGroup) CUPSGroup {
	totalSpaces := 0
	for _, c := range g.Cups {
		qty := c.Quantity
		if qty < 1 {
			qty = 1
		}
		if ecografiaObstetric[c.Code] {
			totalSpaces += 2 * qty
		} else if ecografiaDoppler[c.Code] {
			totalSpaces += qty // 1 space per unit
		} else {
			totalSpaces += 1 * qty
		}
	}
	if totalSpaces < 1 {
		totalSpaces = 1
	}
	g.Espacios = totalSpaces
	return g
}

// applyNeurologiaRules: 890274 and 890374 NEVER together. 053105 ALWAYS separate
// with 1 fixed space. Returns multiple groups. Source: legacy Laravel ai.php.
func applyNeurologiaRules(g CUPSGroup) []CUPSGroup {
	var firstVisit, control, procedure, other []CUPSEntry

	for _, c := range g.Cups {
		switch c.Code {
		case "890274":
			firstVisit = append(firstVisit, c)
		case "890374":
			control = append(control, c)
		case neurologiaProcedureCode:
			procedure = append(procedure, c)
		default:
			other = append(other, c)
		}
	}

	var groups []CUPSGroup

	if len(firstVisit) > 0 {
		spaces := 0
		for _, c := range firstVisit {
			q := c.Quantity
			if q < 1 {
				q = 1
			}
			spaces += q
		}
		groups = append(groups, CUPSGroup{ServiceType: "Neurologia", Cups: firstVisit, Espacios: spaces})
	}
	if len(control) > 0 {
		spaces := 0
		for _, c := range control {
			q := c.Quantity
			if q < 1 {
				q = 1
			}
			spaces += q
		}
		groups = append(groups, CUPSGroup{ServiceType: "Neurologia", Cups: control, Espacios: spaces})
	}
	if len(procedure) > 0 {
		// 053105 ALWAYS 1 fixed space, regardless of quantity
		groups = append(groups, CUPSGroup{ServiceType: "Neurologia", Cups: procedure, Espacios: 1})
	}
	if len(other) > 0 {
		for _, c := range other {
			q := c.Quantity
			if q < 1 {
				q = 1
			}
			groups = append(groups, CUPSGroup{ServiceType: "Neurologia", Cups: []CUPSEntry{c}, Espacios: q})
		}
	}

	if len(groups) == 0 {
		return []CUPSGroup{{ServiceType: "Neurologia", Cups: g.Cups, Espacios: 1}}
	}
	return groups
}
