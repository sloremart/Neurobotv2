package local

import (
	"context"
	"database/sql"
	"strings"

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
// SIESA (ver serviceNameForSubjectType), no del catálogo Antares.
// La dirección y la URL de Google Maps se resuelven desde center_location_id (FK a center_locations)
// → dirección + mapa canónicos para el mensaje de WhatsApp. La columna `direccion` (texto legado) se
// eliminó en la migración 027: la ubicación vive solo en center_locations, no duplicada por CUPS.
// COLLATE para conciliar las collations distintas de las dos tablas.
const procedureColumns = `id, codigo_cups, nombre, COALESCE(descripcion, ''),
	COALESCE(preparacion, ''),
	COALESCE((SELECT cl.address FROM center_locations cl WHERE cl.id = cups_procedimientos.center_location_id) COLLATE utf8mb4_unicode_ci, ''),
	COALESCE((SELECT cl.google_maps_url FROM center_locations cl WHERE cl.id = cups_procedimientos.center_location_id) COLLATE utf8mb4_unicode_ci, ''),
	COALESCE(video_url, ''), COALESCE(audio_url, ''),
	asunto_id, COALESCE(activo, 1)`

func (r *ProcedureRepo) scanOne(row *sql.Row) (*domain.Procedure, error) {
	var p domain.Procedure
	var subjectTypeID, active int
	err := row.Scan(
		&p.ID, &p.Code, &p.Name, &p.Description,
		&p.Preparation, &p.Address, &p.MapsURL,
		&p.VideoURL, &p.AudioURL,
		&subjectTypeID, &active,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.ServiceName = serviceNameForSubjectType(subjectTypeID)
	p.IsActive = (active == 1)
	return &p, nil
}

// serviceNameForSubjectType traduce el asunto (subject type) de SIESA al nombre de servicio
// clínico que usan las reglas de agrupación (forceServiceByCode lo sobreescribe para códigos
// específicos). Reemplaza la antigua columna Antares `servicio`. Mapa derivado del catálogo real:
//
//	Fisiatría: 1,7,15   Radiografía: 2   Tomografía: 3   PET/CT: 12
//	Resonancia: 4,17    Ecografía: 6,19  Neurología: 8,9,10,11,16   (resto → General)
//
// PET (asunto 12) es servicio PROPIO —no "Tomografia"— para que agrupe como su propia cita (no se
// mezcle con un TAC) y caiga a la regla por defecto: 1 espacio por procedimiento × cantidad.
func serviceNameForSubjectType(subjectType int) string {
	switch subjectType {
	case 1, 7, 15:
		return "Fisiatria"
	case 2:
		return "Radiografia"
	case 3:
		return "Tomografia"
	case 12:
		return "PET"
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

// FindMedicosForCups devuelve los códigos sis_medi (cod_medi) habilitados para realizar un CUPS,
// según la relación cups_medico. medico_id ya es sis_medi.codigo (= programacion_medico_detalle.Medico),
// así que el resultado se puede usar directo para filtrar la agenda en SIESA. Slice vacío = el CUPS no
// tiene médicos configurados → el llamador debe operar fail-open (no restringir). Los CUPS con sufijo
// (ej "891901-72") caen al código base.
func (r *ProcedureRepo) FindMedicosForCups(ctx context.Context, cupsCode string) ([]int, error) {
	const q = `SELECT cm.medico_id
	           FROM cups_medico cm
	           JOIN cups_procedimientos cp ON cp.id = cm.cup_id
	           WHERE cp.codigo_cups = ? AND cm.activo = 1`
	ids, err := r.queryMedicoIDs(ctx, q, cupsCode)
	if err != nil || len(ids) > 0 {
		return ids, err
	}
	base := utils.BaseCupCode(cupsCode)
	if base == cupsCode {
		return ids, nil
	}
	return r.queryMedicoIDs(ctx, q, base)
}

// FindCupsForDoctorAndAsuntos returns the active CUPS codes that doctor `medicoID` (cod_medi) is
// authorized to perform (cups_medico) whose asunto_id is in `asuntos`. Es el conjunto elegible de un
// slot liberado (su médico + los asuntos de su agenda) para el matching de lista de espera por slot.
func (r *ProcedureRepo) FindCupsForDoctorAndAsuntos(ctx context.Context, medicoID int, asuntos []int) ([]string, error) {
	if len(asuntos) == 0 {
		return nil, nil
	}
	ph := make([]string, len(asuntos))
	args := make([]interface{}, 0, len(asuntos)+1)
	args = append(args, medicoID)
	for i, a := range asuntos {
		ph[i] = "?"
		args = append(args, a)
	}
	//nolint:gosec // G202: ph = placeholders `?` fijos, valores parametrizados; sin input de usuario en el SQL.
	q := `SELECT DISTINCT cp.codigo_cups
	      FROM cups_procedimientos cp
	      JOIN cups_medico cm ON cm.cup_id = cp.id AND cm.activo = 1
	      WHERE cp.activo = 1 AND cm.medico_id = ? AND cp.asunto_id IN (` + strings.Join(ph, ",") + `)`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	codes := make([]string, 0, 16)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

func (r *ProcedureRepo) queryMedicoIDs(ctx context.Context, query, code string) ([]int, error) {
	rows, err := r.db.QueryContext(ctx, query, code)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int, 0, 8)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
