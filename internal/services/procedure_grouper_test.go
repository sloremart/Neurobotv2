package services

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// ---------------------------------------------------------------------------
// Mock ProcedureRepository
// ---------------------------------------------------------------------------

type mockProcRepo struct {
	procs map[string]*domain.Procedure
}

func (m *mockProcRepo) FindByCode(ctx context.Context, code string) (*domain.Procedure, error) {
	if p, ok := m.procs[code]; ok {
		return p, nil
	}
	return nil, nil
}

func (m *mockProcRepo) FindByID(ctx context.Context, id int) (*domain.Procedure, error) {
	return nil, nil
}

func (m *mockProcRepo) SearchByName(ctx context.Context, name string) ([]domain.Procedure, error) {
	return nil, nil
}

func (m *mockProcRepo) FindAllActive(ctx context.Context) ([]domain.Procedure, error) {
	return nil, nil
}

func (m *mockProcRepo) FindSubjectTypeForCups(ctx context.Context, cupsCode string) (int, error) {
	return 0, nil
}

func (m *mockProcRepo) FindMedicosForCups(_ context.Context, _ string) ([]int, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newMock(entries ...struct {
	code    string
	name    string
	service string
	spaces  int
},
) *mockProcRepo {
	m := &mockProcRepo{procs: make(map[string]*domain.Procedure)}
	for _, e := range entries {
		m.procs[e.code] = &domain.Procedure{
			Code:           e.code,
			Name:           e.name,
			ServiceName:    e.service,
			RequiredSpaces: e.spaces,
			IsActive:       true, // catalog rows used in these tests are active (GroupByServiceFromDB skips inactive)
		}
	}
	return m
}

func cup(code, name string, qty int) CUPSEntry {
	return CUPSEntry{Code: code, Name: name, Quantity: qty}
}

func findGroup(groups []CUPSGroup, svc string) *CUPSGroup {
	for i := range groups {
		if groups[i].ServiceType == svc {
			return &groups[i]
		}
	}
	return nil
}

func findCup(cups []CUPSEntry, code string) *CUPSEntry {
	for i := range cups {
		if cups[i].Code == code {
			return &cups[i]
		}
	}
	return nil
}

// ===========================================================================
// GroupByServiceFromDB tests
// ===========================================================================

// 1. Empty input produces a single "General" group with Espacios=1.
func TestGroupByServiceFromDB_Empty(t *testing.T) {
	mock := newMock()
	groups, err := GroupByServiceFromDB(context.Background(), nil, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].ServiceType != "General" {
		t.Errorf("expected service 'General', got %q", groups[0].ServiceType)
	}
	if groups[0].Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", groups[0].Espacios)
	}
	if len(groups[0].Cups) != 0 {
		t.Errorf("expected 0 cups, got %d", len(groups[0].Cups))
	}
}

// 2. Single CUPS found in DB (non-Fisiatria service) gets correct group.
func TestGroupByServiceFromDB_SingleCup(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"890271", "RESONANCIA CEREBRAL", "Resonancia", 2})

	cups := []CUPSEntry{cup("890271", "RM Cerebral OCR", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.ServiceType != "Resonancia" {
		t.Errorf("expected service 'Resonancia', got %q", g.ServiceType)
	}
	// applyResonanciaRules overrides: 890271 not in resonanciaCodes → fallback qty=1
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1 (890271 not in resonanciaCodes, fallback to qty), got %d", g.Espacios)
	}
	if len(g.Cups) != 1 {
		t.Fatalf("expected 1 cup, got %d", len(g.Cups))
	}
	// Name should be enriched from DB
	if g.Cups[0].Name != "RESONANCIA CEREBRAL" {
		t.Errorf("expected enriched name 'RESONANCIA CEREBRAL', got %q", g.Cups[0].Name)
	}
}

// 3. CUPS code not found in DB is skipped (inactive/unknown).
func TestGroupByServiceFromDB_SingleCup_NotInDB(t *testing.T) {
	mock := newMock() // empty DB

	cups := []CUPSEntry{cup("999999", "Unknown Procedure", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Code not found in DB is skipped entirely
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups (code not in DB is skipped), got %d", len(groups))
	}
}

// 4. Multiple CUPS of the same service are merged into one group with summed spaces.
func TestGroupByServiceFromDB_MultipleSameService(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"883101", "RM CEREBRAL SIMPLE", "Resonancia", 2},
		struct {
			code, name, service string
			spaces              int
		}{"883201", "RM COLUMNA CERVICAL", "Resonancia", 2},
	)

	cups := []CUPSEntry{
		cup("883101", "RM cerebral", 1),
		cup("883201", "RM columna", 1),
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.ServiceType != "Resonancia" {
		t.Errorf("expected service 'Resonancia', got %q", g.ServiceType)
	}
	if g.Espacios != 2 {
		t.Errorf("expected Espacios=2 (resonanciaCodes simple=1+fallback qty=1), got %d", g.Espacios)
	}
	if len(g.Cups) != 2 {
		t.Errorf("expected 2 cups, got %d", len(g.Cups))
	}
}

// 5. CUPS from different services produce separate groups.
func TestGroupByServiceFromDB_MultipleDiffService(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"883101", "RM CEREBRAL", "Resonancia", 2},
		struct {
			code, name, service string
			spaces              int
		}{"890205", "CONSULTA NEUROLOGIA", "Neurologia", 1},
	)

	cups := []CUPSEntry{
		cup("883101", "RM cerebral", 1),
		cup("890205", "Consulta neuro", 1),
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	resGrp := findGroup(groups, "Resonancia")
	if resGrp == nil {
		t.Fatal("missing 'Resonancia' group")
	}
	neuroGrp := findGroup(groups, "Neurologia")
	if neuroGrp == nil {
		t.Fatal("missing 'Neurologia' group")
	}
}

// 6. DB name enrichment overwrites OCR-extracted name.
func TestGroupByServiceFromDB_EnrichesName(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"890271", "ELECTROMIOGRAFIA DE 2 EXTREMIDADES", "Fisiatria", 1})

	cups := []CUPSEntry{cup("890271", "Electromiografia OCR name", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) < 1 {
		t.Fatal("expected at least 1 group")
	}
	g := groups[0]
	if len(g.Cups) == 0 {
		t.Fatal("expected at least 1 cup in group")
	}
	if g.Cups[0].Name != "ELECTROMIOGRAFIA DE 2 EXTREMIDADES" {
		t.Errorf("expected enriched name from DB, got %q", g.Cups[0].Name)
	}
}

// 7. Quantity acts as multiplier for RequiredSpaces.
func TestGroupByServiceFromDB_QuantityMultiplier(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"883101", "RM CEREBRAL", "Resonancia", 2})

	cups := []CUPSEntry{cup("883101", "RM cerebral", 3)} // Quantity=3
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	// applyResonanciaRules: 883101 Simple=1, qty=3 → 1*3=3
	if groups[0].Espacios != 3 {
		t.Errorf("expected Espacios=3 (3 qty * 1 simple from resonanciaCodes), got %d", groups[0].Espacios)
	}
}

// ===========================================================================
// Fisiatria rules tests (applyFisiatriaRules)
// ===========================================================================

//  8. EMG code without NC: NC (891509) is auto-added with Qty = totalEMG * 4.
//     2 EMG (<=3) -> Espacios=1.
func TestFisiatria_EMGWithoutNC(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"29120", "ELECTROMIOGRAFIA", "Fisiatria", 1})

	cups := []CUPSEntry{cup("29120", "EMG", 2)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	// Should have 2 cups: original EMG + auto-added NC
	if len(g.Cups) != 2 {
		t.Fatalf("expected 2 cups (EMG + NC), got %d", len(g.Cups))
	}

	nc := findCup(g.Cups, "891509")
	if nc == nil {
		t.Fatal("NC code 891509 should have been auto-added")
	}
	if nc.Quantity != 8 {
		t.Errorf("expected NC quantity=8 (2 EMG * 4), got %d", nc.Quantity)
	}
	if nc.Name != "NEUROCONDUCCION (CADA NERVIO)" {
		t.Errorf("expected NC name 'NEUROCONDUCCION (CADA NERVIO)', got %q", nc.Name)
	}

	// Espacios: 2 EMG <= 3 -> 1
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1 (2 EMG <= 3), got %d", g.Espacios)
	}
}

// 9. EMG + NC already present: NC quantity adjusted to totalEMG * 4.
func TestFisiatria_EMGWithNC(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"29120", "ELECTROMIOGRAFIA", "Fisiatria", 1},
		struct {
			code, name, service string
			spaces              int
		}{"891509", "NEUROCONDUCCION", "Fisiatria", 1},
	)

	cups := []CUPSEntry{
		cup("29120", "EMG", 1),
		cup("891509", "NC", 2), // original Qty=2 should be adjusted to 1*4=4
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	nc := findCup(g.Cups, "891509")
	if nc == nil {
		t.Fatal("NC code 891509 should be present")
	}
	if nc.Quantity != 4 {
		t.Errorf("expected NC quantity=4 (1 EMG * 4), got %d", nc.Quantity)
	}

	// 1 EMG <= 3 -> Espacios=1
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

//  10. NC code without EMG: kept as a standalone Fisiatria procedure (commit 7ce9774).
//     Neuroconducción can be ordered on its own, so it is no longer discarded.
func TestFisiatria_NCWithoutEMG(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"891509", "NEUROCONDUCCION", "Fisiatria", 1})

	cups := []CUPSEntry{cup("891509", "NC", 4)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	// NC without EMG is kept as a standalone procedure
	if len(g.Cups) != 1 {
		t.Fatalf("expected 1 cup (NC kept as standalone), got %d", len(g.Cups))
	}
	if g.Cups[0].Code != "891509" {
		t.Errorf("expected remaining cup '891509', got %q", g.Cups[0].Code)
	}
	// Standalone procedure → 1 slot
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

// 11. EMG quantity >= 4 results in Espacios=2.
func TestFisiatria_EMGOver3(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"29120", "ELECTROMIOGRAFIA", "Fisiatria", 1})

	cups := []CUPSEntry{cup("29120", "EMG", 4)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	// 4 EMG >= 4 -> Espacios=2
	if g.Espacios != 2 {
		t.Errorf("expected Espacios=2 (4 EMG >= 4), got %d", g.Espacios)
	}

	// NC auto-added with quantity = 4 * 4 = 16
	nc := findCup(g.Cups, "891509")
	if nc == nil {
		t.Fatal("NC should have been auto-added")
	}
	if nc.Quantity != 16 {
		t.Errorf("expected NC quantity=16 (4 EMG * 4), got %d", nc.Quantity)
	}
}

// 12. EMG + dependent code (Onda F): both kept, NC auto-added.
func TestFisiatria_EMGWithDependent(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"29120", "ELECTROMIOGRAFIA", "Fisiatria", 1},
		struct {
			code, name, service string
			spaces              int
		}{"891514", "ONDA F", "Fisiatria", 1},
	)

	cups := []CUPSEntry{
		cup("29120", "EMG", 1),
		cup("891514", "Onda F", 1),
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	// EMG should be present
	emg := findCup(g.Cups, "29120")
	if emg == nil {
		t.Error("EMG code should be kept")
	}

	// Dependent code should be kept (EMG is present)
	dep := findCup(g.Cups, "891514")
	if dep == nil {
		t.Error("dependent code 891514 should be kept when EMG is present")
	}

	// NC should be auto-added
	nc := findCup(g.Cups, "891509")
	if nc == nil {
		t.Fatal("NC should have been auto-added")
	}
	if nc.Quantity != 4 {
		t.Errorf("expected NC quantity=4 (1 EMG * 4), got %d", nc.Quantity)
	}

	// 3 cups total: EMG + dependent + NC
	if len(g.Cups) != 3 {
		t.Errorf("expected 3 cups (EMG + dependent + NC), got %d", len(g.Cups))
	}

	// 1 EMG <= 3 -> Espacios=1
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

//  13. Dependent code without EMG: kept as a standalone Fisiatria procedure (commit 7ce9774).
//     Onda F can be ordered on its own, so it is no longer discarded.
func TestFisiatria_DependentWithoutEMG(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"891514", "ONDA F", "Fisiatria", 1})

	cups := []CUPSEntry{cup("891514", "Onda F", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	// Onda F without EMG is kept as a standalone procedure
	if len(g.Cups) != 1 {
		t.Fatalf("expected 1 cup (dependent kept as standalone), got %d", len(g.Cups))
	}
	if g.Cups[0].Code != "891514" {
		t.Errorf("expected remaining cup '891514', got %q", g.Cups[0].Code)
	}
	// Standalone procedure → 1 slot
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

// ===========================================================================
// Edge cases
// ===========================================================================

// 14. Mixed services: Fisiatria EMG rules only apply to the Fisiatria group.
func TestGroupByServiceFromDB_MixedServicesIsolation(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"29120", "ELECTROMIOGRAFIA", "Fisiatria", 1},
		struct {
			code, name, service string
			spaces              int
		}{"883101", "RM CEREBRAL", "Resonancia", 2},
	)

	cups := []CUPSEntry{
		cup("29120", "EMG", 2),
		cup("883101", "RM cerebral", 1),
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	// Fisiatria group should have EMG rules applied
	fis := findGroup(groups, "Fisiatria")
	if fis == nil {
		t.Fatal("missing 'Fisiatria' group")
	}
	nc := findCup(fis.Cups, "891509")
	if nc == nil {
		t.Fatal("NC should be auto-added in Fisiatria group")
	}
	if fis.Espacios != 1 {
		t.Errorf("Fisiatria Espacios: expected 1, got %d", fis.Espacios)
	}

	// Resonancia group should NOT be affected by Fisiatria rules
	res := findGroup(groups, "Resonancia")
	if res == nil {
		t.Fatal("missing 'Resonancia' group")
	}
	// applyResonanciaRules: 883101 Simple=1, qty=1 → 1
	if res.Espacios != 1 {
		t.Errorf("Resonancia Espacios: expected 1 (883101 simple=1 from resonanciaCodes), got %d", res.Espacios)
	}
}

// 15. CUPS with empty code defaults to General.
func TestGroupByServiceFromDB_EmptyCode(t *testing.T) {
	mock := newMock()
	cups := []CUPSEntry{cup("", "Some procedure", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].ServiceType != "General" {
		t.Errorf("expected 'General', got %q", groups[0].ServiceType)
	}
	if groups[0].Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", groups[0].Espacios)
	}
}

// 16. Quantity=0 should still result in minimum Espacios=1.
func TestGroupByServiceFromDB_ZeroQuantity(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"883101", "RM CEREBRAL", "Resonancia", 2})

	cups := []CUPSEntry{cup("883101", "RM cerebral", 0)} // Quantity=0
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	// 0 qty * 2 spaces = 0, but minimum is 1
	if groups[0].Espacios != 1 {
		t.Errorf("expected minimum Espacios=1, got %d", groups[0].Espacios)
	}
}

// 17. Multiple NC codes with EMG: first NC adjusted, duplicates removed.
func TestFisiatria_MultipleNCWithEMG(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"29120", "ELECTROMIOGRAFIA", "Fisiatria", 1},
		struct {
			code, name, service string
			spaces              int
		}{"891509", "NEUROCONDUCCION", "Fisiatria", 1},
		struct {
			code, name, service string
			spaces              int
		}{"29103", "NC VARIANTE", "Fisiatria", 1},
	)

	cups := []CUPSEntry{
		cup("29120", "EMG", 2),
		cup("891509", "NC 1", 1),
		cup("29103", "NC 2", 1),
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	// Duplicate NC codes should be removed; only first NC kept with adjusted qty
	ncCount := 0
	for _, c := range g.Cups {
		if ncCodes[c.Code] {
			ncCount++
			if c.Quantity != 8 {
				t.Errorf("NC quantity: expected 8 (2 EMG * 4), got %d", c.Quantity)
			}
		}
	}
	if ncCount != 1 {
		t.Errorf("expected exactly 1 NC code after dedup, got %d", ncCount)
	}

	// EMG + 1 NC = 2 cups
	if len(g.Cups) != 2 {
		t.Errorf("expected 2 cups (EMG + 1 NC), got %d", len(g.Cups))
	}
}

//  18. Fisiatria group with non-EMG/NC/dependent code plus NC only:
//     NC removed, non-EMG code stays.
func TestFisiatria_NonEMGCodePlusNC(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"890271", "CONSULTA FISIATRIA", "Fisiatria", 1},
		struct {
			code, name, service string
			spaces              int
		}{"891509", "NEUROCONDUCCION", "Fisiatria", 1},
	)

	cups := []CUPSEntry{
		cup("890271", "Consulta", 1),
		cup("891509", "NC", 4),
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}

	// No EMG -> NC removed, non-EMG code kept
	if len(g.Cups) != 1 {
		t.Fatalf("expected 1 cup (NC removed), got %d", len(g.Cups))
	}
	if g.Cups[0].Code != "890271" {
		t.Errorf("expected remaining cup '890271', got %q", g.Cups[0].Code)
	}
	// Espacios = sum of non-NC quantities = 1
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

// 19. Group order is preserved based on insertion order.
func TestGroupByServiceFromDB_GroupOrder(t *testing.T) {
	mock := newMock(
		struct {
			code, name, service string
			spaces              int
		}{"890205", "CONSULTA NEUROLOGIA", "Neurologia", 1},
		struct {
			code, name, service string
			spaces              int
		}{"883101", "RM CEREBRAL", "Resonancia", 2},
		struct {
			code, name, service string
			spaces              int
		}{"890301", "CONSULTA NEUROCIRUGÍA", "Neurocirugia", 1},
	)

	cups := []CUPSEntry{
		cup("890205", "Consulta neuro", 1),
		cup("883101", "RM cerebral", 1),
		cup("890301", "Consulta NC", 1),
	}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	expected := []string{"Neurologia", "Resonancia", "Neurocirugia"}
	for i, exp := range expected {
		if groups[i].ServiceType != exp {
			t.Errorf("group[%d]: expected %q, got %q", i, exp, groups[i].ServiceType)
		}
	}
}

// 20. EMG boundary: exactly 3 EMG -> Espacios=1.
func TestFisiatria_EMGExactly3(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"29120", "ELECTROMIOGRAFIA", "Fisiatria", 1})

	cups := []CUPSEntry{cup("29120", "EMG", 3)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Fisiatria")
	if g == nil {
		t.Fatal("missing 'Fisiatria' group")
	}
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1 (3 EMG <= 3), got %d", g.Espacios)
	}

	nc := findCup(g.Cups, "891509")
	if nc == nil {
		t.Fatal("NC should have been auto-added")
	}
	if nc.Quantity != 12 {
		t.Errorf("expected NC quantity=12 (3 EMG * 4), got %d", nc.Quantity)
	}
}

// ===========================================================================
// Radiografía rules tests (applyRadiografiaRules)
// ===========================================================================

// 21. Single standard Rx = 1 space.
func TestRadiografia_SingleDefault(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cup("873100", "Rx Mano", 1)},
	})
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

// 22. Exception code: 871060 = 3 spaces.
func TestRadiografia_ExceptionCode(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cup("871060", "Columna vertebral total", 1)},
	})
	if g.Espacios != 3 {
		t.Errorf("expected Espacios=3 (871060 exception), got %d", g.Espacios)
	}
}

// 23. Multiple Rx → sum of individual spaces.
func TestRadiografia_MultipleRx(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups: []CUPSEntry{
			cup("873100", "Rx Mano", 1),    // 1 space
			cup("871030", "Columna DL", 1), // 2 spaces (exception)
			cup("871060", "Col Total", 1),  // 3 spaces (exception)
		},
	})
	if g.Espacios != 6 { // 1 + 2 + 3
		t.Errorf("expected Espacios=6 (1+2+3), got %d", g.Espacios)
	}
}

// 24. Quantity multiplier on exception code.
func TestRadiografia_QuantityMultiplier(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cup("871040", "Columna LS", 2)}, // 2 spaces * qty 2 = 4
	})
	if g.Espacios != 4 {
		t.Errorf("expected Espacios=4 (2 spaces * qty 2), got %d", g.Espacios)
	}
}

// 25. Exception code 873302 = 3 spaces.
func TestRadiografia_ExceptionCode3Spaces(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cup("873302", "Medición MMII", 1)},
	})
	if g.Espacios != 3 {
		t.Errorf("expected Espacios=3 (873302 exception), got %d", g.Espacios)
	}
}

// 26. Zero-quantity Rx defaults to 1.
func TestRadiografia_ZeroQuantity(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cup("873100", "Rx Mano", 0)},
	})
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1 (zero qty defaults to 1), got %d", g.Espacios)
	}
}

// 27. Force-mapping: Rx code without DB service → "Radiografia".
func TestRadiografia_ForceMapping(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"873100", "RX MANO", "", 1}) // Empty service in DB

	cups := []CUPSEntry{cup("873100", "Rx Mano", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Radiografia")
	if g == nil {
		t.Fatal("missing 'Radiografia' group — forceServiceByCode should override empty DB service")
	}
}

// ===========================================================================
// Tomografía rules tests (applyTomografiaRules)
// ===========================================================================

// 28. Simple TAC = 1 space.
func TestTomografia_Simple(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups:        []CUPSEntry{cup("879101", "TAC Cerebral Simple", 1)},
	})
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

// 29. Contrasted TAC = 2 spaces.
func TestTomografia_Contrasted(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups:        []CUPSEntry{{Code: "879102", Name: "TAC Cerebral C", Quantity: 1, IsContrasted: true}},
	})
	if g.Espacios != 2 {
		t.Errorf("expected Espacios=2 (contrasted), got %d", g.Espacios)
	}
}

// 30. 3D code (879910) → always 3 regardless of other entries.
func TestTomografia_3DOverride(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups: []CUPSEntry{
			cup("879101", "TAC Simple", 1),
			cup("879910", "Reconstrucción 3D", 1),
		},
	})
	if g.Espacios != 3 {
		t.Errorf("expected Espacios=3 (3D override), got %d", g.Espacios)
	}
}

// 31. Fixed code 879112 = 2 spaces.
func TestTomografia_FixedCode(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups:        []CUPSEntry{cup("879112", "TAC Cráneo con Contraste", 1)},
	})
	if g.Espacios != 2 {
		t.Errorf("expected Espacios=2 (fixed code 879112), got %d", g.Espacios)
	}
}

// 32. Mixed simple + contrasted TAC.
func TestTomografia_Mixed(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups: []CUPSEntry{
			cup("879101", "TAC Simple", 1),                                           // 1
			{Code: "879102", Name: "TAC Contraste", Quantity: 1, IsContrasted: true}, // 2
		},
	})
	if g.Espacios != 3 { // 1 + 2
		t.Errorf("expected Espacios=3 (1+2), got %d", g.Espacios)
	}
}

// 33. Force-mapping: TAC code without DB service → "Tomografia".
func TestTomografia_ForceMapping(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"879101", "TAC CEREBRAL SIMPLE", "", 1})

	cups := []CUPSEntry{cup("879101", "TAC", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Tomografia")
	if g == nil {
		t.Fatal("missing 'Tomografia' group — forceServiceByCode should override empty DB service")
	}
}

// ===========================================================================
// Ecografía rules tests (applyEcografiaRules)
// ===========================================================================

// 34. Standard echo = 1 space.
func TestEcografia_Standard(t *testing.T) {
	g := applyEcografiaRules(CUPSGroup{
		ServiceType: "Ecografia",
		Cups:        []CUPSEntry{cup("881101", "Eco Abdomen", 1)},
	})
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", g.Espacios)
	}
}

// 35. Obstetric echo = 2 spaces.
func TestEcografia_Obstetric(t *testing.T) {
	g := applyEcografiaRules(CUPSGroup{
		ServiceType: "Ecografia",
		Cups:        []CUPSEntry{cup("881436", "Eco Obstétrica TN", 1)},
	})
	if g.Espacios != 2 {
		t.Errorf("expected Espacios=2 (obstetric), got %d", g.Espacios)
	}
}

// 36. Doppler = qty-based (1 per unit).
func TestEcografia_Doppler(t *testing.T) {
	g := applyEcografiaRules(CUPSGroup{
		ServiceType: "Ecografia",
		Cups:        []CUPSEntry{cup("882308", "Doppler Art MMII", 3)},
	})
	if g.Espacios != 3 {
		t.Errorf("expected Espacios=3 (Doppler qty=3), got %d", g.Espacios)
	}
}

// 37. Mixed obstetric + standard.
func TestEcografia_Mixed(t *testing.T) {
	g := applyEcografiaRules(CUPSGroup{
		ServiceType: "Ecografia",
		Cups: []CUPSEntry{
			cup("881436", "Eco Obstétrica TN", 1),   // 2 spaces
			cup("881101", "Eco Abdomen", 1),         // 1 space
			cup("882317", "Doppler Venoso MMII", 2), // 2 spaces (qty-based)
		},
	})
	if g.Espacios != 5 { // 2 + 1 + 2
		t.Errorf("expected Espacios=5 (2+1+2), got %d", g.Espacios)
	}
}

// 38. Both obstetric codes.
func TestEcografia_BothObstetric(t *testing.T) {
	g := applyEcografiaRules(CUPSGroup{
		ServiceType: "Ecografia",
		Cups: []CUPSEntry{
			cup("881436", "Eco Obstétrica TN", 1),
			cup("881437", "Eco Detalle Anat", 1),
		},
	})
	if g.Espacios != 4 { // 2 + 2
		t.Errorf("expected Espacios=4 (2+2), got %d", g.Espacios)
	}
}

// 39. Force-mapping: echo code without DB service → "Ecografia".
func TestEcografia_ForceMapping(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"881101", "ECOGRAFIA ABDOMEN", "", 1})

	cups := []CUPSEntry{cup("881101", "Eco", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Ecografia")
	if g == nil {
		t.Fatal("missing 'Ecografia' group — forceServiceByCode should override empty DB service")
	}
}

// ===========================================================================
// Neurología rules tests (applyNeurologiaRules)
// ===========================================================================

// 40. First visit only → 1 group.
func TestNeurologia_FirstVisitOnly(t *testing.T) {
	groups := applyNeurologiaRules(CUPSGroup{
		ServiceType: "Neurologia",
		Cups:        []CUPSEntry{cup("890274", "Consulta Neuro 1ra vez", 1)},
	})
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", groups[0].Espacios)
	}
}

// 41. Both consultations → 2 separate groups (never together).
func TestNeurologia_BothConsultations(t *testing.T) {
	groups := applyNeurologiaRules(CUPSGroup{
		ServiceType: "Neurologia",
		Cups: []CUPSEntry{
			cup("890274", "Consulta 1ra vez", 1),
			cup("890374", "Control", 1),
		},
	})
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (890274 + 890374 never together), got %d", len(groups))
	}
	// First group: first visit
	if findCup(groups[0].Cups, "890274") == nil {
		t.Error("first group should contain 890274")
	}
	// Second group: control
	if findCup(groups[1].Cups, "890374") == nil {
		t.Error("second group should contain 890374")
	}
}

// 42. Procedure (053105) always separate with 1 fixed space.
func TestNeurologia_ProcedureSeparate(t *testing.T) {
	groups := applyNeurologiaRules(CUPSGroup{
		ServiceType: "Neurologia",
		Cups: []CUPSEntry{
			cup("890274", "Consulta", 1),
			cup("053105", "Bloqueo", 1),
		},
	})
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (consultation + procedure), got %d", len(groups))
	}
	// Find procedure group
	var procGroup *CUPSGroup
	for i, g := range groups {
		if findCup(g.Cups, "053105") != nil {
			procGroup = &groups[i]
			break
		}
	}
	if procGroup == nil {
		t.Fatal("missing procedure group")
	}
	if procGroup.Espacios != 1 {
		t.Errorf("expected Espacios=1 (fixed), got %d", procGroup.Espacios)
	}
}

// 43. All three → 3 separate groups.
func TestNeurologia_AllThree(t *testing.T) {
	groups := applyNeurologiaRules(CUPSGroup{
		ServiceType: "Neurologia",
		Cups: []CUPSEntry{
			cup("890274", "Consulta 1ra", 1),
			cup("890374", "Control", 1),
			cup("053105", "Bloqueo", 1),
		},
	})
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
}

// 44. Procedure ignores quantity — always 1 space.
func TestNeurologia_ProcedureIgnoresQuantity(t *testing.T) {
	groups := applyNeurologiaRules(CUPSGroup{
		ServiceType: "Neurologia",
		Cups:        []CUPSEntry{cup("053105", "Bloqueo", 10)},
	})
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Espacios != 1 {
		t.Errorf("expected Espacios=1 (fixed, ignores qty=10), got %d", groups[0].Espacios)
	}
}

// 45. Control visit only → 1 group.
func TestNeurologia_ControlOnly(t *testing.T) {
	groups := applyNeurologiaRules(CUPSGroup{
		ServiceType: "Neurologia",
		Cups:        []CUPSEntry{cup("890374", "Control Neuro", 1)},
	})
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Espacios != 1 {
		t.Errorf("expected Espacios=1, got %d", groups[0].Espacios)
	}
}

// 46. Force-mapping: 890274 without DB service → "Neurologia".
func TestNeurologia_ForceMapping(t *testing.T) {
	mock := newMock(struct {
		code, name, service string
		spaces              int
	}{"890274", "CONSULTA NEUROLOGIA", "", 1})

	cups := []CUPSEntry{cup("890274", "Consulta", 1)}
	groups, err := GroupByServiceFromDB(context.Background(), cups, mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := findGroup(groups, "Neurologia")
	if g == nil {
		t.Fatal("missing 'Neurologia' group — forceServiceByCode should override empty DB service")
	}
}

// ===========================================================================
// forceServiceByCode tests
// ===========================================================================

// 47. Force mapping returns correct service for each code type.
func TestForceServiceByCode(t *testing.T) {
	tests := []struct {
		code     string
		expected string
	}{
		{"29120", "Fisiatria"},    // EMG
		{"891509", "Fisiatria"},   // NC
		{"891514", "Fisiatria"},   // Dependent
		{"883101", "Resonancia"},  // RM code
		{"998702", "Resonancia"},  // Sedación resonancia
		{"053105", "Neurologia"},  // Bloqueo
		{"890274", "Neurologia"},  // Consulta 1ra vez
		{"890374", "Neurologia"},  // Control
		{"879101", "Tomografia"},  // TAC
		{"879910", "Tomografia"},  // 3D
		{"881436", "Ecografia"},   // Obstetric
		{"882308", "Ecografia"},   // Doppler
		{"873100", "Radiografia"}, // Rx
		{"871060", "Radiografia"}, // Rx exception
		{"999999", ""},            // Unknown
	}

	for _, tt := range tests {
		got := forceServiceByCode(tt.code)
		if got != tt.expected {
			t.Errorf("forceServiceByCode(%q) = %q, want %q", tt.code, got, tt.expected)
		}
	}
}

// ===========================================================================
// Bilateral helper and rules tests
// ===========================================================================

func cupWithObs(code, name string, qty int, obs string) CUPSEntry {
	return CUPSEntry{Code: code, Name: name, Quantity: qty, Observations: obs}
}

// 48. isBilateral positive cases.
func TestIsBilateral_Positive(t *testing.T) {
	for _, obs := range []string{"bilateral", "BILATERAL", "Bilateral", "rodilla bilateral", "BILATERAL AMB"} {
		if !isBilateral(obs) {
			t.Errorf("isBilateral(%q) should be true", obs)
		}
	}
}

// 49. isBilateral negative cases.
func TestIsBilateral_Negative(t *testing.T) {
	for _, obs := range []string{"", "normal", "unilateral", "AMB SUPERIORES"} {
		if isBilateral(obs) {
			t.Errorf("isBilateral(%q) should be false", obs)
		}
	}
}

// ===========================================================================
// Radiografía bilateral tests
// ===========================================================================

// 50. Rx + bilateral → ×2.
func TestRadiografia_Bilateral(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cupWithObs("873100", "Rx Rodilla", 1, "bilateral")},
	})
	if g.Espacios != 2 { // 1 * 2
		t.Errorf("expected Espacios=2 (1 space ×2 bilateral), got %d", g.Espacios)
	}
}

// 51. Rx comparative + bilateral → NO duplica (already includes both sides).
func TestRadiografia_BilateralComparative(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cupWithObs("873422", "Rodillas comparativas", 1, "bilateral")},
	})
	if g.Espacios != 2 { // 2 spaces (exception), NOT doubled
		t.Errorf("expected Espacios=2 (comparative, no bilateral doubling), got %d", g.Espacios)
	}
}

// 52. Rx exception code + bilateral → ×2.
func TestRadiografia_BilateralException(t *testing.T) {
	g := applyRadiografiaRules(CUPSGroup{
		ServiceType: "Radiografia",
		Cups:        []CUPSEntry{cupWithObs("871040", "Columna LS", 1, "BILATERAL")},
	})
	if g.Espacios != 4 { // 2 spaces (exception) ×2
		t.Errorf("expected Espacios=4 (2 exception ×2 bilateral), got %d", g.Espacios)
	}
}

// ===========================================================================
// Tomografía bilateral tests
// ===========================================================================

// 53. TAC simple + bilateral → ×2.
func TestTomografia_BilateralSimple(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups:        []CUPSEntry{cupWithObs("879101", "TAC Rodilla Simple", 1, "bilateral")},
	})
	if g.Espacios != 2 { // 1 ×2
		t.Errorf("expected Espacios=2 (simple ×2 bilateral), got %d", g.Espacios)
	}
}

// 54. TAC contrasted + bilateral → ×2 (2→4).
func TestTomografia_BilateralContrasted(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups:        []CUPSEntry{{Code: "879102", Name: "TAC Hombro C", Quantity: 1, IsContrasted: true, Observations: "Bilateral"}},
	})
	if g.Espacios != 4 { // 2 ×2
		t.Errorf("expected Espacios=4 (contrasted 2 ×2 bilateral), got %d", g.Espacios)
	}
}

// 55. TAC 3D override ignores bilateral.
func TestTomografia_3DOverrideBilateral(t *testing.T) {
	g := applyTomografiaRules(CUPSGroup{
		ServiceType: "Tomografia",
		Cups:        []CUPSEntry{cupWithObs("879910", "3D Reconstrucción", 1, "bilateral")},
	})
	if g.Espacios != 3 { // 3D override = always 3
		t.Errorf("expected Espacios=3 (3D override), got %d", g.Espacios)
	}
}

// ===========================================================================
// Resonancia bilateral tests
// ===========================================================================

// 56. RM extremity + bilateral → ×2.
func TestResonancia_BilateralExtremity(t *testing.T) {
	g := applyResonanciaRules(CUPSGroup{
		ServiceType: "Resonancia",
		Cups:        []CUPSEntry{cupWithObs("883512", "RM Articulación MS", 1, "bilateral")},
	})
	// 883512 simple=1, bilateral ×2 = 2
	if g.Espacios != 2 {
		t.Errorf("expected Espacios=2 (1 ×2 bilateral), got %d", g.Espacios)
	}
}

// 57. RM non-eligible (cerebro) + bilateral → NO duplica.
func TestResonancia_BilateralNonEligible(t *testing.T) {
	g := applyResonanciaRules(CUPSGroup{
		ServiceType: "Resonancia",
		Cups:        []CUPSEntry{cupWithObs("883101", "RM Cerebral", 1, "bilateral")},
	})
	// 883101 simple=1, NOT in bilateral-eligible → stays 1
	if g.Espacios != 1 {
		t.Errorf("expected Espacios=1 (cerebro, no bilateral), got %d", g.Espacios)
	}
}

// 58. RM extremity contrasted + bilateral → ×2.
func TestResonancia_BilateralContrasted(t *testing.T) {
	g := applyResonanciaRules(CUPSGroup{
		ServiceType: "Resonancia",
		Cups:        []CUPSEntry{{Code: "883522", Name: "RM Art MI", Quantity: 1, IsContrasted: true, Observations: "BILATERAL"}},
	})
	// 883522 contrasted=2, bilateral ×2 = 4
	if g.Espacios != 4 {
		t.Errorf("expected Espacios=4 (contrasted 2 ×2 bilateral), got %d", g.Espacios)
	}
}

// 59. RM bilateral + sedation: bilateral applied before sedation.
func TestResonancia_BilateralPlusSedation(t *testing.T) {
	g := applyResonanciaRules(CUPSGroup{
		ServiceType: "Resonancia",
		Cups: []CUPSEntry{
			cupWithObs("883512", "RM Articulación MS", 1, "bilateral"),
			cup("998702", "Sedación", 1),
		},
	})
	// 883512 simple=1 ×2 bilateral = 2, sedation +1 (simple) = 3
	if g.Espacios != 3 {
		t.Errorf("expected Espacios=3 (1×2 bilateral + 1 sedation), got %d", g.Espacios)
	}
}
