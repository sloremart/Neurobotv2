package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/repository"
)

var _ repository.SoatRepository = (*SoatRepo)(nil)

// SoatRepo retrieves procedure prices from SIESA's sis_proc_precios table.
// tariffType must be the contratos.manual code as a string (e.g. "11" for Sanitas).
// In the SIESA EntityRepo, entity.PriceType is set to this manual code.
type SoatRepo struct {
	db *sql.DB
}

func NewSoatRepo(db *sql.DB) *SoatRepo {
	return &SoatRepo{db: db}
}

// FindPrice looks up the price for a CUPS code in sis_proc_precios.
// tariffType = contratos.manual as string (e.g. "11").
// Returns nil when not found; *0.0 when price is zero.
func (r *SoatRepo) FindPrice(ctx context.Context, cupCode, tariffType string) (*float64, error) {
	if tariffType == "" {
		return nil, nil
	}

	manualInt, err := strconv.Atoi(tariffType)
	if err != nil {
		return nil, fmt.Errorf("invalid manual code %q: %w", tariffType, err)
	}

	// Handle CUPS with suffix (e.g., "891901-72") — try exact match first, then base code.
	cupsBase := cupCode
	if idx := strings.Index(cupCode, "-"); idx > 0 {
		cupsBase = cupCode[:idx]
	}

	query := `SELECT TOP 1 spp.Precio
	          FROM sis_proc_precios spp
	          WHERE spp.Cod_manual = @p1
	            AND spp.Codigo_proc IN (@p2, @p3)
	            AND spp.Tipo_proc = '256'
	          ORDER BY CASE WHEN spp.Codigo_proc = @p4 THEN 0 ELSE 1 END`

	var price float64
	err = r.db.QueryRowContext(ctx, query, manualInt, cupCode, cupsBase, cupCode).Scan(&price)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("siesa find price: %w", err)
	}
	return &price, nil
}
