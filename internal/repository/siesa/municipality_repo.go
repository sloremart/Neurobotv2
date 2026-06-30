package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

var _ repository.MunicipalityRepository = (*MunicipalityRepo)(nil)

type MunicipalityRepo struct {
	db *sql.DB
}

func NewMunicipalityRepo(db *sql.DB) *MunicipalityRepo {
	return &MunicipalityRepo{db: db}
}

// Search busca municipios en sis_muni por nombre.
// Retorna DepartmentCode = sis_muni.id_dep (ej: "50"),
//
//	MunicipalityCode = sis_muni.codigo (ej: "001") — formato que espera sis_paci.
func (r *MunicipalityRepo) Search(ctx context.Context, name string) ([]domain.Municipality, error) {
	// Acepta "Ciudad - Departamento" o "Ciudad, Departamento": busca por el nombre
	// de la ciudad y, si se indica departamento, acota por él. Así "Cubarral - Meta"
	// encuentra CUBARRAL aunque la columna solo guarde el nombre del municipio.
	city := strings.TrimSpace(name)
	dept := ""
	for _, sep := range []string{"-", ","} {
		if i := strings.Index(city, sep); i >= 0 {
			dept = strings.TrimSpace(city[i+1:])
			city = strings.TrimSpace(city[:i])
			break
		}
	}
	if city == "" {
		city = strings.TrimSpace(name)
	}

	// Primer intento: nombre de ciudad + (si se dio) filtro de departamento.
	municipalities, err := r.searchMunicipios(ctx, city, dept)
	if err != nil {
		return nil, err
	}
	// Fallback (N7): si el filtro de departamento no devolvió nada, reintentar SOLO por ciudad.
	// El split por '-'/',' puede tomar como "departamento" algo que no lo es (p.ej. "Bogotá, D.C."
	// o "Miriti - Parana"), dejando 0 filas aunque la ciudad exista. Sin filtro de dept recupera.
	if len(municipalities) == 0 && dept != "" {
		if municipalities, err = r.searchMunicipios(ctx, city, ""); err != nil {
			return nil, err
		}
	}
	// Fallback por TOKENS: la gente escribe "Villavicencio Meta" (sin guion/coma) y el LIKE por el
	// string completo falla porque "VILLAVICENCIO META" no es un nombre de municipio. Se tokeniza el
	// input y se exige que CADA palabra aparezca en el municipio O el departamento. Maneja cualquier
	// separador (espacio/guion/coma) y nombres multi-palabra ("San José del Guaviare", "Valle del
	// Cauca"): todas las palabras caen en muni+dept. Solo se usa si lo anterior no encontró nada.
	if len(municipalities) == 0 {
		if toks := muniSearchTokens(name); len(toks) > 0 {
			return r.searchMunicipiosByTokens(ctx, toks)
		}
	}
	return municipalities, nil
}

// muniSearchTokens normaliza el input a palabras de búsqueda: minúsculas, separadores ('-', ',', '.') →
// espacio, y descarta conectores ("y") y palabras de 1 carácter. Conserva "de/del/la" porque son parte
// de nombres reales (San José DEL Guaviare, LA Guajira, Valle DEL Cauca). Cap de 6 tokens.
func muniSearchTokens(s string) []string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("-", " ", ",", " ", ".", " ").Replace(s)
	var toks []string
	for _, w := range strings.Fields(s) {
		if w == "y" || len(w) < 2 {
			continue
		}
		toks = append(toks, w)
		if len(toks) == 6 {
			break
		}
	}
	return toks
}

// searchMunicipiosByTokens busca municipios donde CADA token aparezca en el nombre del municipio O del
// departamento (LIKE acento-insensible). El AND de todos los tokens acota al resultado correcto
// ("villavicencio"→muni, "meta"→dept). El conteo alto lo maneja el handler (ValidateSearchCount).
func (r *MunicipalityRepo) searchMunicipiosByTokens(ctx context.Context, tokens []string) ([]domain.Municipality, error) {
	conds := make([]string, 0, len(tokens))
	args := make([]interface{}, 0, len(tokens))
	for i, t := range tokens {
		p := fmt.Sprintf("@p%d", i+1)
		conds = append(conds, fmt.Sprintf("(m.nombre LIKE %s COLLATE Latin1_General_CI_AI OR d.nombre LIKE %s COLLATE Latin1_General_CI_AI)", p, p))
		args = append(args, "%"+t+"%")
	}
	// conds son fragmentos FIJOS con placeholders @pN (sin input del usuario); los valores van
	// parametrizados en args. La concatenación es solo de esos fragmentos controlados, no de datos.
	base := "SELECT TOP 10 m.Id, CAST(m.id_dep AS VARCHAR(10)) AS dep_code, ISNULL(d.nombre, '') AS dep_name, " +
		"m.codigo AS muni_code, m.nombre AS muni_name FROM sis_muni m WITH (NOLOCK) " +
		"LEFT JOIN departamentos d WITH (NOLOCK) ON d.codigo = CAST(m.id_dep AS VARCHAR(10)) WHERE "
	query := base + strings.Join(conds, " AND ") + " ORDER BY m.nombre" //nolint:gosec // G202: conds = placeholders fijos (@pN), valores parametrizados; sin input en el SQL

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("municipality token search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var municipalities []domain.Municipality
	for rows.Next() {
		var m domain.Municipality
		if err := rows.Scan(&m.ID, &m.DepartmentCode, &m.DepartmentName, &m.MunicipalityCode, &m.MunicipalityName); err != nil {
			return nil, err
		}
		municipalities = append(municipalities, m)
	}
	return municipalities, rows.Err()
}

// searchMunicipios ejecuta la consulta a sis_muni por nombre de ciudad y, opcionalmente,
// filtrando por nombre de departamento.
func (r *MunicipalityRepo) searchMunicipios(ctx context.Context, city, dept string) ([]domain.Municipality, error) {
	query := `
	SELECT TOP 10
	    m.Id,
	    CAST(m.id_dep AS VARCHAR(10))   AS dep_code,
	    ISNULL(d.nombre, '')            AS dep_name,
	    m.codigo                        AS muni_code,
	    m.nombre                        AS muni_name
	FROM sis_muni m WITH (NOLOCK)
	LEFT JOIN departamentos d WITH (NOLOCK) ON d.codigo = CAST(m.id_dep AS VARCHAR(10))
	WHERE m.nombre LIKE @p1 COLLATE Latin1_General_CI_AI`

	args := []interface{}{"%" + city + "%"}
	if dept != "" {
		query += ` AND d.nombre LIKE @p2 COLLATE Latin1_General_CI_AI`
		args = append(args, "%"+dept+"%")
	}
	query += `
	ORDER BY m.nombre`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("municipality search: %w", err)
	}
	defer rows.Close()

	var municipalities []domain.Municipality
	for rows.Next() {
		var m domain.Municipality
		if err := rows.Scan(&m.ID, &m.DepartmentCode, &m.DepartmentName, &m.MunicipalityCode, &m.MunicipalityName); err != nil {
			return nil, err
		}
		municipalities = append(municipalities, m)
	}
	return municipalities, rows.Err()
}

// SearchBarrios busca barrios en sis_barrios por nombre, acotados al municipio
// (dept, municipio). Retorna Code = sis_barrios.codigo (bigint, va a sis_paci.barrio).
func (r *MunicipalityRepo) SearchBarrios(ctx context.Context, name, depCode, muniCode string) ([]domain.Barrio, error) {
	query := `
	SELECT TOP 15
	    CAST(b.codigo AS VARCHAR(20)) AS codigo,
	    b.nombres                     AS nombre,
	    ISNULL(b.zona,'')             AS zona
	FROM sis_barrios b WITH (NOLOCK)
	WHERE b.nombres LIKE @p1 COLLATE Latin1_General_CI_AI
	  AND b.municipio = @p2 AND b.dept = @p3
	ORDER BY b.nombres`

	rows, err := r.db.QueryContext(ctx, query, "%"+strings.TrimSpace(name)+"%", muniCode, depCode)
	if err != nil {
		return nil, fmt.Errorf("barrio search: %w", err)
	}
	defer rows.Close()

	var barrios []domain.Barrio
	for rows.Next() {
		var b domain.Barrio
		if err := rows.Scan(&b.Code, &b.Name, &b.Zone); err != nil {
			return nil, err
		}
		barrios = append(barrios, b)
	}
	return barrios, rows.Err()
}
