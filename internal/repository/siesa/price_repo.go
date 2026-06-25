package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/repository"
)

var _ repository.PriceRepository = (*PriceRepo)(nil)

// PriceRepo retrieves procedure prices from SIESA's sis_proc_precios table.
// tariffType must be the contratos.manual code as a string (e.g. "11" for Sanitas).
// In the SIESA EntityRepo, entity.PriceType is set to this manual code.
type PriceRepo struct {
	db *sql.DB
}

// NewPriceRepo crea el repositorio de precios/tarifas sobre sis_proc_precios.
func NewPriceRepo(db *sql.DB) *PriceRepo {
	return &PriceRepo{db: db}
}

// FindPrice looks up the price for a CUPS code in sis_proc_precios.
// tariffType = contratos.manual as string (e.g. "11").
// Returns nil when not found; *0.0 when price is zero.
func (r *PriceRepo) FindPrice(ctx context.Context, cupCode, tariffType string) (*float64, error) {
	if tariffType == "" {
		return nil, nil
	}

	// Validar que sea numérico y normalizar ceros a la izquierda ("08"→"8"). IMPORTANTE:
	// sis_proc_precios.Cod_manual es varchar; hay que pasar el manual como STRING (no int),
	// porque un int fuerza CONVERT_IMPLICIT(int, Cod_manual) como predicado RESIDUAL sobre la
	// tabla de ~1.48M filas, sacando Cod_manual del índice. Como string entra al seek
	// (IX_sis_proc_precios_manual_proc / UQ_sis_proc_precios).
	manualInt, err := strconv.Atoi(tariffType)
	if err != nil {
		return nil, fmt.Errorf("invalid manual code %q: %w", tariffType, err)
	}
	manualStr := strconv.Itoa(manualInt)

	// Handle CUPS with suffix (e.g., "891901-72") — try exact match first, then base code.
	cupsBase := cupCode
	if idx := strings.Index(cupCode, "-"); idx > 0 {
		cupsBase = cupCode[:idx]
	}

	query := `SELECT TOP 1 spp.Precio
	          FROM sis_proc_precios spp WITH (NOLOCK)
	          WHERE spp.Cod_manual = @p1
	            AND spp.Codigo_proc IN (@p2, @p3)
	            AND spp.Tipo_proc = '256'
	          ORDER BY CASE WHEN spp.Codigo_proc = @p4 THEN 0 ELSE 1 END`

	var price float64
	err = r.db.QueryRowContext(ctx, query, manualStr, cupCode, cupsBase, cupCode).Scan(&price)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("siesa find price: %w", err)
	}
	return &price, nil
}
