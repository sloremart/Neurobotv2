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

// ReferenceRepo expone catálogos de referencia de SIESA (médicos, asuntos) para el dashboard.
//
// Eficiencia / seguridad sobre la BD clínica de producción:
//   - Solo lectura, WITH (NOLOCK): no toma locks ni bloquea la UI de SIESA.
//   - Catálogos pequeños (médicos ~60, asuntos ~20) y casi estáticos → se cachean en memoria
//     con TTL: SIESA se consulta a lo sumo una vez por TTL, no en cada carga del dashboard.
//   - Solo las columnas necesarias; sin JOINs pesados ni escaneos grandes.
type ReferenceRepo struct {
	db  *sql.DB
	ttl time.Duration

	mu        sync.RWMutex
	medicos   []domain.MedicoRef
	medicosAt time.Time
	asuntos   []domain.AsuntoRef
	asuntosAt time.Time
}

// NewReferenceRepo crea el repo de catálogos de referencia de SIESA con cache de TTL dado.
func NewReferenceRepo(db *sql.DB, ttl time.Duration) *ReferenceRepo {
	return &ReferenceRepo{db: db, ttl: ttl}
}

// Medicos devuelve la lista de médicos de SIESA (cacheada). ~60 filas.
func (r *ReferenceRepo) Medicos(ctx context.Context) ([]domain.MedicoRef, error) {
	r.mu.RLock()
	if r.medicos != nil && time.Since(r.medicosAt) <= r.ttl {
		out := r.medicos
		r.mu.RUnlock()
		return out, nil
	}
	r.mu.RUnlock()

	rows, err := r.db.QueryContext(ctx, `
		SELECT codigo, RTRIM(ISNULL(cedula,'')), RTRIM(ISNULL(nombre,''))
		FROM sis_medi WITH (NOLOCK)
		WHERE cedula IS NOT NULL AND cedula <> ''
		ORDER BY nombre`)
	if err != nil {
		return nil, fmt.Errorf("siesa medicos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.MedicoRef, 0, 64)
	for rows.Next() {
		var m domain.MedicoRef
		if err := rows.Scan(&m.Codigo, &m.Cedula, &m.Nombre); err != nil {
			return nil, fmt.Errorf("scan medico: %w", err)
		}
		m.Cedula = strings.TrimSpace(m.Cedula)
		m.Nombre = strings.TrimSpace(m.Nombre)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.medicos, r.medicosAt = out, time.Now()
	r.mu.Unlock()
	return out, nil
}

// Asuntos devuelve la lista de asuntos de SIESA (cacheada). ~20 filas.
func (r *ReferenceRepo) Asuntos(ctx context.Context) ([]domain.AsuntoRef, error) {
	r.mu.RLock()
	if r.asuntos != nil && time.Since(r.asuntosAt) <= r.ttl {
		out := r.asuntos
		r.mu.RUnlock()
		return out, nil
	}
	r.mu.RUnlock()

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, RTRIM(ISNULL(nombre,''))
		FROM sis_asunto WITH (NOLOCK)
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("siesa asuntos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.AsuntoRef, 0, 24)
	for rows.Next() {
		var a domain.AsuntoRef
		if err := rows.Scan(&a.ID, &a.Nombre); err != nil {
			return nil, fmt.Errorf("scan asunto: %w", err)
		}
		a.Nombre = strings.TrimSpace(a.Nombre)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.asuntos, r.asuntosAt = out, time.Now()
	r.mu.Unlock()
	return out, nil
}
