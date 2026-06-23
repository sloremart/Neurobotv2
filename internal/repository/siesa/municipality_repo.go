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
		return r.searchMunicipios(ctx, city, "")
	}
	return municipalities, nil
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
