// Package siesa implementa los repositorios sobre la BD clínica SIESA (SQL Server, solo lectura
// para KPIs/referencia; lectura-escritura para citas/agenda).
package siesa

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// AnalyticsRepo expone KPIs agregados de SIESA (ocupación de agenda, citas por estado, citas del
// bot para conciliación) para el dashboard.
//
// Seguridad sobre la BD clínica de producción (no afectar la UI de SIESA):
//   - WITH (NOLOCK) en toda lectura: no toma locks compartidos, no bloquea las escrituras de la UI.
//   - Agrega en el servidor (GROUP BY / SUM): devuelve decenas de filas, nunca miles.
//   - Acota por fecha con predicado de rango sobre la columna indexada (sin CAST en la columna).
//   - OPTION (MAXDOP 1): no acapara CPU.
//   - Cache en memoria con TTL por clave: N cargas del dashboard = 1 query a SIESA por ventana.
type AnalyticsRepo struct {
	db  *sql.DB
	ttl time.Duration

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at   time.Time
	data interface{}
}

// NewAnalyticsRepo crea el repo de KPIs de SIESA con cache de TTL dado.
func NewAnalyticsRepo(db *sql.DB, ttl time.Duration) *AnalyticsRepo {
	return &AnalyticsRepo{db: db, ttl: ttl, cache: make(map[string]cacheEntry)}
}

func (r *AnalyticsRepo) cached(key string) (interface{}, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.cache[key]
	if ok && time.Since(e.at) <= r.ttl {
		return e.data, true
	}
	return nil, false
}

func (r *AnalyticsRepo) store(key string, data interface{}) {
	r.mu.Lock()
	r.cache[key] = cacheEntry{at: time.Now(), data: data}
	r.mu.Unlock()
}

// Occupancy devuelve la ocupación (slots ocupados vs libres) por médico y día para los próximos
// windowDays días. Agregado sobre programacion_medico_detalle (la única tabla grande): devuelve
// ~médicos×días filas (decenas/cientos), nunca los slots crudos.
func (r *AnalyticsRepo) Occupancy(ctx context.Context, windowDays int) ([]domain.OccupancyRow, error) {
	if windowDays <= 0 || windowDays > 60 {
		windowDays = 14
	}
	key := fmt.Sprintf("occ:%d", windowDays)
	if v, ok := r.cached(key); ok {
		return v.([]domain.OccupancyRow), nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT pmd.Medico, RTRIM(ISNULL(m.nombre,'')) AS nombre,
		       CONVERT(VARCHAR(10), pmd.Fecha, 23) AS dia,
		       SUM(CASE WHEN pmd.IdCita IS NOT NULL THEN 1 ELSE 0 END) AS ocupados,
		       SUM(CASE WHEN pmd.IdCita IS NULL AND pmd.Bloqueado=0
		                 AND pmd.SinProgramacion=0 THEN 1 ELSE 0 END) AS libres
		FROM programacion_medico_detalle pmd WITH (NOLOCK)
		JOIN programacion_medico pm WITH (NOLOCK)
		     ON pm.id = pmd.IdProgramacionMedico AND pm.activo = 1
		JOIN sis_medi m WITH (NOLOCK) ON m.codigo = pmd.Medico
		WHERE pmd.Fecha >= CAST(GETDATE() AS DATE)
		  AND pmd.Fecha <  DATEADD(DAY, @p1, CAST(GETDATE() AS DATE))
		GROUP BY pmd.Medico, RTRIM(ISNULL(m.nombre,'')), CONVERT(VARCHAR(10), pmd.Fecha, 23)
		ORDER BY pmd.Medico, dia
		OPTION (MAXDOP 1)`, windowDays)
	if err != nil {
		return nil, fmt.Errorf("siesa occupancy: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.OccupancyRow, 0, 256)
	for rows.Next() {
		var o domain.OccupancyRow
		if err := rows.Scan(&o.Medico, &o.Nombre, &o.Fecha, &o.Ocupados, &o.Libres); err != nil {
			return nil, fmt.Errorf("scan occupancy: %w", err)
		}
		o.Nombre = strings.TrimSpace(o.Nombre)
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	r.store(key, out)
	return out, nil
}

// AppointmentsByState devuelve el conteo de citas por día y SITUACIÓN en [from, to) (YYYY-MM-DD).
// La situación usa la definición de SIESA que dedujimos (no el estado crudo): cancelada = estado 'C';
// atendida = estado 'A'; confirmada = AsistenciaConfirmada=1 o estado 'CC'; pendiente = el resto.
// Si from/to vienen vacías, usa los últimos 30 días. Agregado: devuelve ~días×4 filas.
func (r *AnalyticsRepo) AppointmentsByState(ctx context.Context, from, to string) ([]domain.AppointmentStateRow, error) {
	if from == "" || to == "" {
		now := time.Now()
		from = now.AddDate(0, 0, -30).Format("2006-01-02")
		to = now.AddDate(0, 0, 1).Format("2006-01-02")
	}
	key := "state:" + from + "|" + to
	if v, ok := r.cached(key); ok {
		return v.([]domain.AppointmentStateRow), nil
	}

	// El CASE se repite en SELECT y GROUP BY (SQL Server no permite agrupar por alias).
	const situacionExpr = `CASE
		WHEN estado = 'C' THEN 'cancelada'
		WHEN estado = 'A' THEN 'atendida'
		WHEN ISNULL(AsistenciaConfirmada,0) = 1 OR estado = 'CC' THEN 'confirmada'
		ELSE 'pendiente' END`
	rows, err := r.db.QueryContext(ctx, `
		SELECT CONVERT(VARCHAR(10), fecha, 23) AS dia, `+situacionExpr+` AS situacion, COUNT(*) AS total
		FROM citas WITH (NOLOCK)
		WHERE fecha >= @p1 AND fecha < @p2
		GROUP BY CONVERT(VARCHAR(10), fecha, 23), `+situacionExpr+`
		ORDER BY dia
		OPTION (MAXDOP 1)`, from, to)
	if err != nil {
		return nil, fmt.Errorf("siesa citas-estado: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.AppointmentStateRow, 0, 128)
	for rows.Next() {
		var s domain.AppointmentStateRow
		if err := rows.Scan(&s.Fecha, &s.Situacion, &s.Total); err != nil {
			return nil, fmt.Errorf("scan citas-estado: %w", err)
		}
		s.Situacion = strings.TrimSpace(s.Situacion)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	r.store(key, out)
	return out, nil
}

// BotAppointmentsWithCups devuelve las citas-procedimiento creadas por el bot (cod_user_asigna_cita
// = botCedula) en los últimos `days` días, con su CUPS y médico. El cruce con cups_medico (catálogo
// local) lo hace el llamador para detectar médico mal asignado. Filtrado por usuario del bot +
// ventana corta → pocas filas; no escanea la historia completa.
func (r *AnalyticsRepo) BotAppointmentsWithCups(ctx context.Context, botCedula string, days int) ([]domain.BotAppointmentCup, error) {
	if days <= 0 || days > 30 {
		days = 4
	}
	if strings.TrimSpace(botCedula) == "" {
		return nil, nil // sin usuario del bot configurado no hay nada que conciliar
	}
	key := fmt.Sprintf("bot:%s:%d", botCedula, days)
	if v, ok := r.cached(key); ok {
		return v.([]domain.BotAppointmentCup), nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.cod_medi, RTRIM(ISNULL(cp.id_procedimiento,'')) AS cups,
		       CONVERT(VARCHAR(10), c.fecha, 23) AS dia
		FROM citas c WITH (NOLOCK)
		JOIN citas_procedimientos cp WITH (NOLOCK) ON cp.id_cita = c.id
		WHERE c.cod_user_asigna_cita = @p1
		  AND c.fecha_solicitud >= DATEADD(DAY, @p2, GETDATE())
		OPTION (MAXDOP 1)`, botCedula, -days)
	if err != nil {
		return nil, fmt.Errorf("siesa bot-citas: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.BotAppointmentCup, 0, 64)
	for rows.Next() {
		var b domain.BotAppointmentCup
		if err := rows.Scan(&b.CitaID, &b.CodMedi, &b.Cups, &b.Fecha); err != nil {
			return nil, fmt.Errorf("scan bot-cita: %w", err)
		}
		b.Cups = strings.TrimSpace(b.Cups)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	r.store(key, out)
	return out, nil
}
