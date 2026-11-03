package database

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/neuro-bot/neuro-bot/internal/config"
)

// D-07: el DSN de la BD local lleva timeout de conexión y conserva los parámetros de siempre;
// no lleva readTimeout ni writeTimeout (cortarían consultas largas legítimas).
func TestLocalDSN_ConnectTimeoutOnly(t *testing.T) {
	dsn := localDSN(&config.Config{DBUser: "u", DBPassword: "p", DBHost: "db", DBPort: "3306", DBDatabase: "neuro_bot"})
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("DSN inválido: %v", err)
	}
	if parsed.Timeout != localDialTimeout {
		t.Errorf("timeout = %v, want %v", parsed.Timeout, localDialTimeout)
	}
	if parsed.ReadTimeout != 0 || parsed.WriteTimeout != 0 {
		t.Errorf("readTimeout/writeTimeout = %v/%v, want 0/0", parsed.ReadTimeout, parsed.WriteTimeout)
	}
	if !parsed.ParseTime || !parsed.MultiStatements || parsed.Loc.String() != "America/Bogota" ||
		parsed.Collation != "utf8mb4_unicode_ci" || parsed.DBName != "neuro_bot" || parsed.Addr != "db:3306" {
		t.Errorf("se perdió un parámetro de siempre: %+v", parsed)
	}
	if !strings.Contains(dsn, "charset=utf8mb4") {
		t.Errorf("se perdió charset: %q", dsn)
	}
}
