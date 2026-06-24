//go:build integration

// Tests de PARIDAD con el histórico de SIESA, contra la BD real. Validan que la lógica del bot
// produce los mismos resultados que la UI de SIESA ha venido registrando.
// Correr con: go test -tags integration -run 'TestCupSuffix|TestWriteCreationAudit' -v ./internal/repository/siesa/
// Requiere SIESA_DSN (ver openTestDB en patient_create_integration_test.go).
package siesa

import (
	"context"
	"fmt"
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
