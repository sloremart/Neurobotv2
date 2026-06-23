package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

var _ repository.EntityRepository = (*EntityRepo)(nil)

// EntityRepo agrupa contratos de SIESA por empresa para exponer una sola entidad
// por EPS/prepagada, igual al comportamiento de Antares.
//
// entity.Code     = contratos.empresa  (e.g. "EPS005") — coincide con sis_paci.entidad
// entity.Name     = alias limpio, sin sufijos " CONTRIBUTIVO"/" SUBSIDIADO"/" EVENTO"/" MRC"
// entity.PriceType = manual del contrato principal (menor codigo activo)
// entity.Category  = regimen del contrato principal
//
// When scheduling, lookupContract (appointment_repo) resolves empresa → contrato.codigo.
type EntityRepo struct {
	db *sql.DB
}

func NewEntityRepo(db *sql.DB) *EntityRepo {
	return &EntityRepo{db: db}
}

// groupSelectSQL devuelve filas (empresa, nombre_corto, manual, regimen) agrupadas por empresa.
// Limpia sufijos técnicos del alias para mostrarlo al paciente.
const groupSelectSQL = `
SELECT
    ISNULL(empresa, '')                                                                 AS empresa,
    ISNULL(RTRIM(REPLACE(REPLACE(REPLACE(REPLACE(
        MIN(alias),
        ' CONTRIBUTIVO', ''),
        ' SUBSIDIADO',   ''),
        ' EVENTO',       ''),
        ' MRC',          '')), '')                                                      AS nombre_corto,
    ISNULL(MIN(manual), 0)                                                              AS manual,
    ISNULL(MIN(regimen), 1)                                                             AS regimen
FROM contratos WITH (NOLOCK)`

func scanEntityGroup(rows *sql.Rows) (domain.Entity, error) {
	var e domain.Entity
	var company, cleanName string
	var priceType, regime int64
	if err := rows.Scan(&company, &cleanName, &priceType, &regime); err != nil {
		return e, err
	}
	e.Code = company
	e.Name = cleanName
	e.PriceType = strconv.FormatInt(priceType, 10)
	e.Category = categoryNameFromRegime(regime)
	e.IsActive = true
	return e, nil
}

// categoryNameFromRegime maps a SIESA regimen number to the domain category name
// used by EntityCategories and CachedEntityRepo.byCategory.
func categoryNameFromRegime(regimen int64) string {
	switch regimen {
	case 1, 2:
		return "EPS"
	case 4:
		return "PARTICULAR"
	case 5:
		return "EMPRESA DE MEDICINA PREPAGADA"
	case 7:
		return "REGIMEN ESPECIAL"
	default:
		return strconv.FormatInt(regimen, 10)
	}
}

// scanEntity para consultas individuales (FindByCode) que retornan una fila de contratos.
func scanEntity(rows *sql.Rows) (domain.Entity, error) {
	var e domain.Entity
	var codigo int
	var alias, company sql.NullString
	var priceType, regime sql.NullInt64
	var activo int
	if err := rows.Scan(&codigo, &alias, &company, &priceType, &regime, &activo); err != nil {
		return e, err
	}
	e.ID = codigo
	e.Code = company.String // company (empresa) as Code, consistent with sis_paci.entidad
	e.Name = alias.String
	e.PriceType = strconv.FormatInt(priceType.Int64, 10)
	e.Category = strconv.FormatInt(regime.Int64, 10)
	e.IsActive = (activo == 1)
	return e, nil
}

const entitySelectCols = `c.codigo, ISNULL(c.alias,''), ISNULL(c.empresa,''), c.manual, c.regimen, c.activo`

func (r *EntityRepo) FindActive(ctx context.Context) ([]domain.Entity, error) {
	query := groupSelectSQL + `
	WHERE activo = 1
	GROUP BY empresa
	ORDER BY MIN(alias)`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("entity FindActive: %w", err)
	}
	defer rows.Close()

	var entities []domain.Entity
	for rows.Next() {
		e, err := scanEntityGroup(rows)
		if err != nil {
			return nil, err
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

func (r *EntityRepo) FindActiveByCategory(ctx context.Context, category string) ([]domain.Entity, error) {
	upper := strings.ToUpper(strings.TrimSpace(category))

	var whereExtra string
	var args []interface{}

	switch {
	case upper == "EPS":
		whereExtra = `AND regimen IN (1, 2)`
	case upper == "ARL":
		whereExtra = `AND TipoAseguramiento = 8`
	case upper == "POLIZA" || upper == "PÓLIZA":
		// regimen=5 + no es prepagada + no es ARL (TipoAseguramiento=8) → cubre SOAT y pólizas
		whereExtra = `AND regimen = 5 AND es_prepagado = 0 AND TipoAseguramiento <> 8`
	case strings.Contains(upper, "PREPAGADA"):
		whereExtra = `AND es_prepagado = 1`
	case strings.Contains(upper, "PARTICULAR"):
		// Contrato estándar de paciente particular (PART02, 10% descuento). El bot
		// no muestra lista para PARTICULAR (es opción única); se asigna directo.
		whereExtra = `AND empresa = 'PART02'`
	default:
		regimenInt, err := strconv.Atoi(category)
		if err != nil {
			regimenInt = regimeFromCategoryName(category)
		}
		whereExtra = `AND regimen = @p1`
		args = append(args, regimenInt)
	}

	query := groupSelectSQL + `
	WHERE activo = 1 ` + whereExtra + `
	GROUP BY empresa
	ORDER BY MIN(alias)`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("entity FindActiveByCategory %s: %w", category, err)
	}
	defer rows.Close()

	var entities []domain.Entity
	for rows.Next() {
		e, err := scanEntityGroup(rows)
		if err != nil {
			return nil, err
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

func (r *EntityRepo) FindByCode(ctx context.Context, code string) (*domain.Entity, error) {
	var query string
	var param interface{}

	if codigoInt, err := strconv.Atoi(code); err == nil {
		// código numérico de contrato: buscar por codigo
		query = `SELECT ` + entitySelectCols + ` FROM contratos c WITH (NOLOCK) WHERE c.codigo = @p1`
		param = codigoInt
	} else {
		// código empresa (e.g. "EPS005" de sis_paci.entidad): primer contrato activo
		query = `SELECT TOP 1 ` + entitySelectCols + ` FROM contratos c WITH (NOLOCK) WHERE c.empresa = @p1 AND c.activo = 1 ORDER BY c.codigo`
		param = code
	}

	rows, err := r.db.QueryContext(ctx, query, param)
	if err != nil {
		return nil, fmt.Errorf("entity FindByCode %s: %w", code, err)
	}
	defer rows.Close()

	if rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		return &e, rows.Err()
	}
	return nil, rows.Err()
}

func (r *EntityRepo) GetCodeByIndexAndCategory(ctx context.Context, index int, category string) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(category))

	var whereExtra string
	var args []interface{}

	switch {
	case upper == "EPS":
		whereExtra = `AND regimen IN (1, 2)`
		args = []interface{}{index - 1}
	case upper == "ARL":
		whereExtra = `AND TipoAseguramiento = 8`
		args = []interface{}{index - 1}
	case upper == "POLIZA" || upper == "PÓLIZA":
		whereExtra = `AND regimen = 5 AND es_prepagado = 0 AND TipoAseguramiento <> 8`
		args = []interface{}{index - 1}
	case strings.Contains(upper, "PREPAGADA"):
		whereExtra = `AND es_prepagado = 1`
		args = []interface{}{index - 1}
	case strings.Contains(upper, "PARTICULAR"):
		whereExtra = `AND empresa = 'PART02'`
		args = []interface{}{index - 1}
	default:
		regimenInt, err := strconv.Atoi(category)
		if err != nil {
			regimenInt = regimeFromCategoryName(category)
		}
		whereExtra = `AND regimen = @p1`
		args = []interface{}{regimenInt, index - 1}
	}

	// Devuelve empresa como código (coincide con lo que muestra FindActiveByCategory)
	query := groupSelectSQL + `
	WHERE activo = 1 ` + whereExtra + `
	GROUP BY empresa
	ORDER BY MIN(alias)
	OFFSET @p` + strconv.Itoa(len(args)) + ` ROWS FETCH NEXT 1 ROWS ONLY`

	var company string
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&company, new(string), new(int64), new(int64)); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("entity index %d out of range for category %s", index, category)
		}
		return "", fmt.Errorf("entity GetCodeByIndex: %w", err)
	}
	return company, nil
}

// regimeFromCategoryName mapea categoría a regimen SIESA.
// "EPS" se maneja por separado (IN 1,2) en FindActiveByCategory.
func regimeFromCategoryName(name string) int {
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch {
	case strings.Contains(upper, "SUBSIDIADO"):
		return 2
	case strings.Contains(upper, "PARTICULAR"):
		return 4
	case strings.Contains(upper, "PREPAGADA") || strings.Contains(upper, "VOLUNTARIO") || strings.Contains(upper, "MODULAR"):
		return 5
	case strings.Contains(upper, "MAGISTERIO") || strings.Contains(upper, "FOMAG") || strings.Contains(upper, "ESPECIAL"):
		return 7
	default:
		return 1
	}
}
