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

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("siesa db ping: %w", err)
	}

	return db, nil
}
