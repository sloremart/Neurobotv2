package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/microsoft/go-mssqldb"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

func NewLocalDB(cfg *config.Config) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true&loc=America%%2FBogota&charset=utf8mb4&collation=utf8mb4_unicode_ci",
		cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBDatabase)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("local db open: %w", err)
	}

	db.SetMaxOpenConns(cfg.LocalDBMaxOpen)
	db.SetMaxIdleConns(cfg.LocalDBMaxIdle)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)

	// N-48: Ping con timeout acotado para no colgar el startup si la BD no responde.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // #34 (auditoría): cerrar el pool si el Ping falla (no fugarlo)
		return nil, fmt.Errorf("local db ping: %w", err)
	}

	return db, nil
}

// NewExternalDB opens a connection to the external clinical database.
// Only the SIESA (SQL Server) driver is supported; the legacy MySQL/Antares
// external path was removed.
func NewExternalDB(cfg *config.Config) (*sql.DB, error) {
	return NewSIESADB(cfg)
}

// NewSIESADB opens a SQL Server connection to the SIESA database (ZeusSalud_Neuro).
// DSN: EXTERNAL_DB_HOST, EXTERNAL_DB_PORT (default 1433), EXTERNAL_DB_DATABASE, EXTERNAL_DB_USER, EXTERNAL_DB_PASSWORD
func NewSIESADB(cfg *config.Config) (*sql.DB, error) {
	port := cfg.ExtDBPort
	if port == "" || port == "3306" {
		port = "1433"
	}

	query := url.Values{}
	query.Set("database", cfg.ExtDBDatabase)
	// Cifrado configurable (N-11). Default "disable" preserva el comportamiento actual; en
	// producción usar EXTERNAL_DB_ENCRYPT=true para cifrar el canal TDS (PII de salud). Con
	// encrypt activo se confía en el certificado del server (LAN interna sin CA propia).
	encrypt := cfg.ExtDBEncrypt
	if encrypt == "" {
		encrypt = "disable"
	}
	query.Set("encrypt", encrypt)
	if encrypt != "disable" {
		query.Set("TrustServerCertificate", "true")
	} else {
		// M2 (auditoría): canal TDS sin cifrar hacia la BD clínica → PII/PHI (teléfono, documento,
		// nombre, diagnósticos) viaja en claro por la red. Se avisa fuerte para que en producción se
		// setee EXTERNAL_DB_ENCRYPT=true. (No se fuerza por defecto para no romper servers sin TLS.)
		slog.Warn("SIESA TDS ENCRYPTION DISABLED — PHI viaja en claro; setea EXTERNAL_DB_ENCRYPT=true en producción",
			"host", cfg.ExtDBHost)
	}

	u := &url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(cfg.ExtDBUser, cfg.ExtDBPassword),
		Host:     fmt.Sprintf("%s:%s", cfg.ExtDBHost, port),
		RawQuery: query.Encode(),
	}

	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, fmt.Errorf("siesa db open: %w", err)
	}

	db.SetMaxOpenConns(cfg.ExternalDBMaxOpen)
	db.SetMaxIdleConns(cfg.ExternalDBMaxIdle)
	db.SetConnMaxLifetime(2 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	// N-48: Ping con timeout acotado para no colgar el startup si la BD no responde.
	// M5 (auditoría): si el Ping falla al arrancar (blip transitorio de SQL Server), NO se descarta el
	// pool — database/sql reconecta solo cuando SIESA vuelva, así que las queries se recuperan sin
	// reiniciar el bot. Antes se devolvía nil y el bot quedaba PERMANENTEMENTE degradado (repos nil, sin
	// reintento). /health (que hace su propio Ping) reporta el estado real mientras tanto.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		slog.Warn("siesa db ping failed at startup — pool retained, will reconnect lazily when SIESA recovers", "error", err)
	}

	return db, nil
}
