// Package pgtestutils configures PostgreSQL for Flynn unit tests only (not used in production images).
package pgtestutils

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/jackc/pgx"
)

// ensureSuperuserForPeerRoot creates a PostgreSQL role named "root" when unit tests
// run as Unix root and connect via the local socket (peer maps the OS user to the
// same DB role name). Debian/Ubuntu clusters ship without that role.
func ensureSuperuserForPeerRoot() error {
	if os.Geteuid() != 0 {
		return nil
	}
	sql := `DO $$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'root') THEN CREATE ROLE root WITH LOGIN SUPERUSER; END IF; END$$;`
	cmd := exec.Command("sudo", "-n", "-u", "postgres", "psql", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgtestutils: sudo -u postgres psql for root peer auth: %w\n%s", err, bytes.TrimSpace(out))
	}
	return nil
}

// defaultTestPGHost selects a sensible default when PGHOST is unset:
// Debian/Ubuntu socket directory if present, otherwise TCP localhost (typical Homebrew / manual installs).
func defaultTestPGHost() string {
	fi, err := os.Stat("/var/run/postgresql")
	if err == nil && fi.IsDir() {
		return "/var/run/postgresql"
	}
	return "127.0.0.1"
}

// baseConnConfig reads libpq-style environment variables via pgx.ParseEnvLibpq.
// If PGHOST is unset, uses /var/run/postgresql when that socket directory exists, else 127.0.0.1.
// If PGSSLMODE is unset, sets it to disable so local TCP/socket test servers work without TLS negotiation issues.
func baseConnConfig() (pgx.ConnConfig, error) {
	if os.Getenv("PGSSLMODE") == "" {
		os.Setenv("PGSSLMODE", "disable")
	}
	cc, err := pgx.ParseEnvLibpq()
	if err != nil {
		return cc, err
	}
	if cc.Host == "" {
		cc.Host = defaultTestPGHost()
	}
	return cc, nil
}

// ConnConfigForDatabase returns pgx settings for connecting to an existing database dbname,
// using the same host/port/user/password as SetupPostgres (PGHOST, PGPORT, PGUSER, PGPASSWORD, PGSSLMODE).
func ConnConfigForDatabase(dbname string) (pgx.ConnConfig, error) {
	cc, err := baseConnConfig()
	if err != nil {
		return cc, err
	}
	cc.Database = dbname
	return cc, nil
}

// adminDatabase returns the database used to run CREATE/DROP DATABASE (default: postgres).
// Override with PGTEST_ADMIN_DATABASE if your server uses another maintenance DB (e.g. template1).
func adminDatabase() string {
	if d := os.Getenv("PGTEST_ADMIN_DATABASE"); d != "" {
		return d
	}
	return "postgres"
}

// SetupPostgres drops (if present) and creates a database named dbname.
// The connecting role must be allowed to create databases (typically a superuser).
func SetupPostgres(dbname string) error {
	if err := ensureSuperuserForPeerRoot(); err != nil {
		return err
	}
	cc, err := baseConnConfig()
	if err != nil {
		return err
	}
	cc.Database = adminDatabase()

	db, err := pgx.Connect(cc)
	if err != nil {
		return err
	}

	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbname)); err != nil {
		return err
	}
	if _, err := db.Exec(fmt.Sprintf("CREATE DATABASE %s", dbname)); err != nil {
		return err
	}
	return nil
}
