package database

import (
	"database/sql"
	"fmt"
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

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("local db ping: %w", err)
	}

	return db, nil
}

// NewExternalDB opens a connection to the external clinical database.
// For "siesa" driver it uses SQL Server (go-mssqldb); otherwise MySQL.
func NewExternalDB(cfg *config.Config) (*sql.DB, error) {
	if cfg.ExternalDBDriver == "siesa" {
		return NewSIESADB(cfg)
	}
	return newMySQLExternalDB(cfg)
}

func newMySQLExternalDB(cfg *config.Config) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&loc=America%%2FBogota&charset=utf8mb4&collation=utf8mb4_unicode_ci",
		cfg.ExtDBUser, cfg.ExtDBPassword, cfg.ExtDBHost, cfg.ExtDBPort, cfg.ExtDBDatabase)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("external db open: %w", err)
	}

	db.SetMaxOpenConns(cfg.ExternalDBMaxOpen)
	db.SetMaxIdleConns(cfg.ExternalDBMaxIdle)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("external db ping: %w", err)
	}

	return db, nil
}

// NewAntaresDB abre la conexión a Antares (MySQL datosipsndx) para uso simultáneo
// con SIESA cuando EXTERNAL_DB_DRIVER=siesa. cups_procedimientos y cup_medico
// siguen leyéndose desde Antares.
func NewAntaresDB(cfg *config.Config) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&loc=America%%2FBogota&charset=utf8mb4&collation=utf8mb4_unicode_ci",
		cfg.AntaresDBUser, cfg.AntaresDBPassword, cfg.AntaresDBHost, cfg.AntaresDBPort, cfg.AntaresDBDatabase)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("antares db open: %w", err)
	}

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("antares db ping: %w", err)
	}

	return db, nil
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
	// Disable certificate validation for internal networks (use TrustServerCertificate=true for dev)
	query.Set("encrypt", "disable")

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
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("siesa db ping: %w", err)
	}

	return db, nil
}
