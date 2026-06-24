//go:build integration

// Tests de PARIDAD con el histórico de SIESA, contra la BD real. Validan que la lógica del bot
// produce los mismos resultados que la UI de SIESA ha venido registrando.
// Correr con: go test -tags integration -run 'TestCupSuffix|TestWriteCreationAudit' -v ./internal/repository/siesa/
// Requiere SIESA_DSN (ver openTestDB en patient_create_integration_test.go).
package siesa

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

// TestCupSuffixMatchesHistory valida, contra la BD real, que la decisión de almacenamiento de
// CUPS (cupVariantExistsQuery + chooseCupStorage) reproduce exactamente la forma que usa la UI:
// sufijo {base}-{qty} con Cantidad=1 cuando la variante existe en sis_proc_precios; base con
// Cantidad=qty cuando no. Casos verificados el 2026-06-24 (incluye 1005927 qty=3, que el viejo
// heurístico qty>4 guardaba mal).
func TestCupSuffixMatchesHistory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	cases := []struct {
		base         string
		qty          int
		wantExists   bool
		wantCode     string
		wantQuantity int
	}{
		{"930860", 4, true, "930860-4", 1},
		{"930860", 2, false, "930860", 2},
		{"891515", 4, true, "891515-4", 1},
		{"891515", 2, false, "891515", 2},
		{"1005927", 3, true, "1005927-3", 1}, // qty=3 usa sufijo; qty>4 lo guardaba como base
		{"1005927", 9, false, "1005927", 9},
		{"891509", 8, true, "891509-8", 1},
		{"861401", 2, false, "861401", 2},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("%s_qty%d", c.base, c.qty), func(t *testing.T) {
			variant := fmt.Sprintf("%s-%d", c.base, c.qty)
			var found int
			if err := db.QueryRowContext(ctx, cupVariantExistsQuery, variant).Scan(&found); err != nil {
				t.Fatalf("EXISTS %s: %v", variant, err)
			}
			gotExists := found == 1
			if gotExists != c.wantExists {
				t.Fatalf("existe(%s)=%t, esperado %t (¿cambió sis_proc_precios?)", variant, gotExists, c.wantExists)
			}
			code, qty := chooseCupStorage(c.base, c.qty, gotExists)
			if code != c.wantCode || qty != c.wantQuantity {
				t.Errorf("chooseCupStorage(%s,%d) = (%q,%d), esperado (%q,%d)",
					c.base, c.qty, code, qty, c.wantCode, c.wantQuantity)
			}
		})
	}
}

// TestFindPriceMatchesHistory valida, contra la BD real, que el FindPrice REAL del bot (SoatRepo)
// reproduce exactamente el precio que la UI guardó en citas_procedimientos_asuntos.Valor para las
// consultas históricas. Para cada consulta toma su CUPS y el manual del contrato de la cita, corre
// SoatRepo.FindPrice(cup, manual) y asevera que devuelve el mismo valor unitario almacenado.
//
// ALCANCE: esto prueba el COMPONENTE de búsqueda de precio (origen sis_proc_precios + valor
// unitario). NO cubre la resolución de QUÉ manual usa el bot en vivo (patient_entity → EntityRepo →
// contrato principal), que es una capa aparte: aquí se usa el manual del contrato real de la cita.
func TestFindPriceMatchesHistory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	repo := NewSoatRepo(db)

	type histRow struct {
		cup    string
		manual string
		valor  float64
	}
	var hist []histRow
	rs, err := db.QueryContext(ctx, `
		SELECT TOP 25 cpa.CodProcedimiento, CAST(ct.manual AS VARCHAR(20)), cpa.Valor
		FROM citas_procedimientos_asuntos cpa WITH (NOLOCK)
		JOIN citas c WITH (NOLOCK) ON c.id = cpa.IdCita
		JOIN contratos ct WITH (NOLOCK) ON ct.codigo = TRY_CAST(c.contrato AS INT)
		WHERE c.fecha_solicitud >= DATEADD(DAY, -10, CAST(GETDATE() AS DATE))
		  AND ISNULL(cpa.Valor, 0) > 0
		ORDER BY cpa.IdCita DESC`)
	if err != nil {
		t.Fatalf("query histórico: %v", err)
	}
	for rs.Next() {
		var r histRow
		if err := rs.Scan(&r.cup, &r.manual, &r.valor); err != nil {
			rs.Close()
			t.Fatal(err)
		}
		hist = append(hist, r)
	}
	rs.Close()
	if len(hist) == 0 {
		t.Skip("no hay consultas con Valor en el rango")
	}

	mismatches := 0
	for _, r := range hist {
		price, err := repo.FindPrice(ctx, r.cup, r.manual)
		if err != nil {
			t.Errorf("FindPrice(%s, manual=%s): %v", r.cup, r.manual, err)
			mismatches++
			continue
		}
		if price == nil {
			t.Errorf("FindPrice(%s, manual=%s) = nil, histórico guardó %.2f", r.cup, r.manual, r.valor)
			mismatches++
			continue
		}
		if math.Abs(*price-r.valor) > 0.01 {
			t.Errorf("FindPrice(%s, manual=%s) = %.2f, histórico cpa.Valor = %.2f", r.cup, r.manual, *price, r.valor)
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d/%d consultas con precio distinto al histórico", mismatches, len(hist))
	}
	t.Logf("OK: %d consultas — FindPrice del bot == cpa.Valor histórico (unitario)", len(hist))
}

// TestWriteCreationAuditMatchesUI ejecuta el SQL EXACTO de WriteCreationAudit (creationAuditQuery)
// sobre una cita real con CUPS, dentro de una transacción que se REVIERTE (no persiste nada), y
// asevera que genera la misma estructura que la UI: 1 fila log_citas (APARTAR CITA) + N filas
// log_citas_procedimientos (una por CUPS de citas_procedimientos), enlazadas por id_log, con
// id_procedimiento/Servicio idénticos al detalle real de la cita.
func TestWriteCreationAuditMatchesUI(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Cita real más reciente que tenga CUPS en citas_procedimientos (referencia "histórico UI").
	var cita int
	if err := db.QueryRowContext(ctx,
		`SELECT TOP 1 id_cita FROM citas_procedimientos WITH (NOLOCK) ORDER BY id_cita DESC`).Scan(&cita); err != nil {
		t.Fatalf("buscar cita con CUPS: %v", err)
	}

	// CUPS reales de esa cita: id_procedimiento -> Servicio.
	wantCP := map[string]int{}
	rows, err := db.QueryContext(ctx,
		`SELECT id_procedimiento, ISNULL(Servicio,0) FROM citas_procedimientos WITH (NOLOCK) WHERE id_cita=@p1`, cita)
	if err != nil {
		t.Fatalf("leer CUPS de cita %d: %v", cita, err)
	}
	for rows.Next() {
		var code string
		var svc int
		if err := rows.Scan(&code, &svc); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		wantCP[code] = svc
	}
	rows.Close()
	if len(wantCP) == 0 {
		t.Fatalf("precondición: la cita %d debe tener citas_procedimientos", cita)
	}

	const marker = "TEST AUDIT PARITY - rollback"

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() // garantiza que NADA se persista

	// Ejecuta el SQL de producción (mismo que WriteCreationAudit). @p1=cita, @p2=obs, @p3=usuario.
	if _, err := tx.ExecContext(ctx, creationAuditQuery, fmt.Sprintf("%d", cita), marker, siesaBotUserID); err != nil {
		t.Fatalf("creationAuditQuery: %v", err)
	}

	// 1) Header: una fila log_citas APARTAR CITA con el marker.
	var idlog int64
	var evento string
	if err := tx.QueryRowContext(ctx,
		`SELECT id, tipo_evento FROM log_citas WHERE id_cita_modificada=@p1 AND observacion=@p2`,
		cita, marker).Scan(&idlog, &evento); err != nil {
		t.Fatalf("leer log_citas generado: %v", err)
	}
	if evento != "APARTAR CITA" {
		t.Errorf("tipo_evento=%q, esperado APARTAR CITA", evento)
	}

	// 2) Detalle: una fila por CUPS, enlazada por id_log, con id_procedimiento/Servicio idénticos.
	gotCP := map[string]int{}
	drows, err := tx.QueryContext(ctx,
		`SELECT id_procedimiento, ISNULL(Servicio,0), id_cita, estado
		 FROM log_citas_procedimientos WHERE id_log=@p1`, idlog)
	if err != nil {
		t.Fatalf("leer detalle generado: %v", err)
	}
	for drows.Next() {
		var code string
		var svc, idc, estado int
		if err := drows.Scan(&code, &svc, &idc, &estado); err != nil {
			drows.Close()
			t.Fatal(err)
		}
		if idc != cita {
			t.Errorf("detalle id_cita=%d, esperado %d", idc, cita)
		}
		if estado != 0 {
			t.Errorf("detalle estado=%d, esperado 0", estado)
		}
		gotCP[code] = svc
	}
	drows.Close()

	if len(gotCP) != len(wantCP) {
		t.Fatalf("detalle generó %d filas, esperado %d (= CUPS de la cita)", len(gotCP), len(wantCP))
	}
	for code, svc := range wantCP {
		got, ok := gotCP[code]
		if !ok {
			t.Errorf("falta detalle para CUPS %s", code)
			continue
		}
		if got != svc {
			t.Errorf("CUPS %s: Servicio detalle=%d, esperado %d", code, got, svc)
		}
	}
	t.Logf("OK: cita %d -> 1 log_citas (id=%d) + %d detalle(s) idénticos al histórico UI", cita, idlog, len(gotCP))
}

// TestPriceManualFromContract valida el fix GAP-3 (doc dudas §8): el manual del precio debe salir
// del CONTRATO del paciente, no de la entidad. Caso MRC: contrato 6 → manual 8 → $53.560 por 890374;
// la entidad EPS005 resuelve al contrato principal (menor código = 4, EVENTO, manual 11) → $57.649.
// Demuestra que entidad y contrato dan manuales/precios distintos y que el del contrato es el correcto.
func TestPriceManualFromContract(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	entRepo := NewEntityRepo(db)
	soat := NewSoatRepo(db)
	const cup = "890374"

	byContract, err := entRepo.FindByCode(ctx, "6")
	if err != nil || byContract == nil {
		t.Fatalf("FindByCode(contrato 6): %v", err)
	}
	if byContract.PriceType != "8" {
		t.Fatalf("contrato 6: manual = %q, esperado 8", byContract.PriceType)
	}

	byEntity, err := entRepo.FindByCode(ctx, "EPS005")
	if err != nil || byEntity == nil {
		t.Fatalf("FindByCode(EPS005): %v", err)
	}
	if byEntity.PriceType == byContract.PriceType {
		t.Skipf("entidad y contrato dan el mismo manual (%q): el caso del bug no se reproduce con los datos actuales", byContract.PriceType)
	}

	priceContract, err := soat.FindPrice(ctx, cup, byContract.PriceType) // manual 8
	if err != nil || priceContract == nil {
		t.Fatalf("FindPrice(%s, %s): %v", cup, byContract.PriceType, err)
	}
	priceEntity, err := soat.FindPrice(ctx, cup, byEntity.PriceType) // manual del contrato principal
	if err != nil || priceEntity == nil {
		t.Fatalf("FindPrice(%s, %s): %v", cup, byEntity.PriceType, err)
	}
	if *priceContract >= *priceEntity {
		t.Errorf("esperaba precio MRC (manual %s=%.0f) MENOR que por entidad (manual %s=%.0f)",
			byContract.PriceType, *priceContract, byEntity.PriceType, *priceEntity)
	}
	t.Logf("OK: por contrato (manual %s)=%.0f  vs  por entidad (manual %s)=%.0f — el fix usa el del contrato",
		byContract.PriceType, *priceContract, byEntity.PriceType, *priceEntity)
}

// TestProcServicioMatchesHistory valida, contra la BD real, que resolveProcServicio (la regla
// "servicios por (contrato, cod_proc) excluyendo el catch-all 27") reproduce el Servicio que la
// UI ha venido guardando en citas_procedimientos, sobre los últimos 4 días con datos. Se espera
// >=98% de coincidencia: los pocos mismatch son elecciones de operador que guardaron el catch-all.
func TestProcServicioMatchesHistory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	repo := NewAppointmentRepo(db)

	var cutoff string
	if err := db.QueryRowContext(ctx, `
		SELECT CONVERT(VARCHAR(10), MIN(d), 120) FROM (
		  SELECT DISTINCT TOP 4 CAST(fecha_solicitud AS DATE) AS d
		  FROM citas WITH (NOLOCK) WHERE fecha_solicitud IS NOT NULL ORDER BY d DESC) x`).Scan(&cutoff); err != nil {
		t.Fatalf("cutoff: %v", err)
	}

	type row struct {
		cup      string
		contrato int
		stored   int
	}
	var data []row
	rs, err := db.QueryContext(ctx, `
		SELECT cp.id_procedimiento, ISNULL(TRY_CONVERT(INT, c.contrato),0), cp.Servicio
		FROM citas_procedimientos cp WITH (NOLOCK)
		JOIN citas c WITH (NOLOCK) ON c.id = cp.id_cita
		WHERE c.fecha_solicitud >= @p1 AND ISNULL(cp.Servicio,0) > 0`, cutoff)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.cup, &r.contrato, &r.stored); err != nil {
			rs.Close()
			t.Fatal(err)
		}
		data = append(data, r)
	}
	rs.Close()
	if len(data) == 0 {
		t.Skip("sin procedimientos con Servicio en la ventana")
	}

	match := 0
	for _, r := range data {
		base := r.cup
		if i := strings.LastIndex(base, "-"); i > 0 {
			base = base[:i]
		}
		if repo.resolveProcServicio(ctx, r.contrato, base, r.cup) == r.stored {
			match++
		}
	}
	ratio := float64(match) / float64(len(data))
	if ratio < 0.98 {
		t.Errorf("solo %d/%d (%.1f%%) coinciden con la regla; esperado >=98%%", match, len(data), ratio*100)
	}
	t.Logf("OK: %d/%d (%.1f%%) coinciden con resolveProcServicio en ventana de 4 días (desde %s)",
		match, len(data), ratio*100, cutoff)
}
