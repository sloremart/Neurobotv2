package siesa

import (
	"context"
	"database/sql"
	"fmt"

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
//         MunicipalityCode = sis_muni.codigo (ej: "001") — formato que espera sis_paci.
func (r *MunicipalityRepo) Search(ctx context.Context, name string) ([]domain.Municipality, error) {
	query := `
	SELECT TOP 10
	    m.Id,
	    CAST(m.id_dep AS VARCHAR(10))   AS dep_code,
	    ISNULL(d.nombre, '')            AS dep_name,
	    m.codigo                        AS muni_code,
	    m.nombre                        AS muni_name
	FROM sis_muni m
	LEFT JOIN departamentos d ON d.codigo = CAST(m.id_dep AS VARCHAR(10))
	WHERE m.nombre LIKE @p1 COLLATE Latin1_General_CI_AI
	ORDER BY m.nombre`

	rows, err := r.db.QueryContext(ctx, query, "%"+name+"%")
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
