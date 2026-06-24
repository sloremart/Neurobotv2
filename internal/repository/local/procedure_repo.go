package local

import (
	"context"
	"database/sql"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

var _ repository.ProcedureRepository = (*ProcedureRepo)(nil)

// ProcedureRepo reads the CUPS catalog from the local MySQL `cups_procedimientos`
// table (migration 019). It replaces the Antares-backed datosipsndx implementation:
// the catalog (name, service, preparation, address) plus the SIESA subject mapping
// (asunto_id) now live in the bot's own database instead of the external clinic ERP.
type ProcedureRepo struct {
	db *sql.DB
}

func NewProcedureRepo(db *sql.DB) *ProcedureRepo {
	return &ProcedureRepo{db: db}
}

// procedureColumns is the shared projection for single-row lookups.
// Las columnas Antares `servicio_id`/`servicio` (y antes `tipo`, `especialidad_id`,
// `horario_especifico_id`) ya NO se leen: la clasificación del CUPS sale del `asunto_id` de
// SIESA (ver serviceNameForAsunto), no del catálogo Antares.
const procedureColumns = `id, codigo_cups, nombre, COALESCE(descripcion, ''),
	COALESCE(preparacion, ''), COALESCE(direccion, ''),
	COALESCE(video_url, ''), COALESCE(audio_url, ''),
	asunto_id, COALESCE(activo, 1)`

func (r *ProcedureRepo) scanOne(row *sql.Row) (*domain.Procedure, error) {
	var p domain.Procedure
	var asuntoID, active int
	err := row.Scan(
		&p.ID, &p.Code, &p.Name, &p.Description,
		&p.Preparation, &p.Address,
		&p.VideoURL, &p.AudioURL,
		&asuntoID, &active,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Asunto = asuntoID
	p.ServiceName = serviceNameForAsunto(asuntoID)
	p.IsActive = (active == 1)
	return &p, nil
}

// serviceNameForAsunto traduce el asunto de SIESA al nombre de servicio clínico que usan las
// reglas de agrupación (forceServiceByCode lo sobreescribe para códigos específicos). Reemplaza
// la antigua columna Antares `servicio`. Mapa derivado del catálogo real (cups_procedimientos):
//
//	Fisiatría: 1,7,15   Radiografía: 2   Tomografía: 3,12
//	Resonancia: 4,17    Ecografía: 6,19  Neurología: 8,9,10,11,16   (resto → General)
func serviceNameForAsunto(asunto int) string {
	switch asunto {
	case 1, 7, 15:
		return "Fisiatria"
	case 2:
		return "Radiografia"
	case 3, 12:
		return "Tomografia"
	case 4, 17:
		return "Resonancia"
	case 6, 19:
		return "Ecografia"
	case 8, 9, 10, 11, 16:
		return "Neurologia"
	default:
		return "General"
	}
}

func (r *ProcedureRepo) FindByCode(ctx context.Context, code string) (*domain.Procedure, error) {
	query := `SELECT ` + procedureColumns + ` FROM cups_procedimientos WHERE codigo_cups = ? LIMIT 1`
	return r.scanOne(r.db.QueryRowContext(ctx, query, code))
}

func (r *ProcedureRepo) FindByID(ctx context.Context, id int) (*domain.Procedure, error) {
	query := `SELECT ` + procedureColumns + ` FROM cups_procedimientos WHERE id = ? LIMIT 1`
	return r.scanOne(r.db.QueryRowContext(ctx, query, id))
}

func (r *ProcedureRepo) FindAllActive(ctx context.Context) ([]domain.Procedure, error) {
	query := `SELECT id, codigo_cups, nombre FROM cups_procedimientos WHERE activo = 1 ORDER BY codigo_cups`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var procs []domain.Procedure
	for rows.Next() {
		var p domain.Procedure
		if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			return nil, err
		}
		p.IsActive = true
		procs = append(procs, p)
	}
	return procs, rows.Err()
}

func (r *ProcedureRepo) SearchByName(ctx context.Context, name string) ([]domain.Procedure, error) {
	query := `SELECT id, codigo_cups, nombre
	          FROM cups_procedimientos
	          WHERE nombre LIKE ? AND activo = 1
	          ORDER BY nombre
	          LIMIT 10`

	rows, err := r.db.QueryContext(ctx, query, "%"+name+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var procs []domain.Procedure
	for rows.Next() {
		var p domain.Procedure
		if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			return nil, err
		}
		p.IsActive = true
		procs = append(procs, p)
	}
	return procs, rows.Err()
}

// FindSubjectTypeForCups returns the SIESA subject id (asunto_id) for a CUPS code.
// It is the deterministic replacement for the old history-based lookups against SIESA
// (ScheduleRepo.FindAsuntoForCups / appointment history). Returns 0 when the CUPS code
// is not in the catalog. Suffixed CUPS (e.g. "891901-72") fall back to their base code.
func (r *ProcedureRepo) FindSubjectTypeForCups(ctx context.Context, cupsCode string) (int, error) {
	var asuntoID int
	query := `SELECT asunto_id FROM cups_procedimientos WHERE codigo_cups = ? LIMIT 1`

	err := r.db.QueryRowContext(ctx, query, cupsCode).Scan(&asuntoID)
	if err == nil {
		return asuntoID, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	// Fallback: retry with the base code for suffixed CUPS.
	base := utils.BaseCupCode(cupsCode)
	if base == cupsCode {
		return 0, nil
	}
	err = r.db.QueryRowContext(ctx, query, base).Scan(&asuntoID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return asuntoID, nil
}
