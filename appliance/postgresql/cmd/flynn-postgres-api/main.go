package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/julienschmidt/httprouter"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/randy-girard/flynn/pkg/resource"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"golang.org/x/net/context"
)

const (
	disallowConns   = `UPDATE pg_database SET datallowconn = FALSE WHERE datname = $1`
	disconnectConns = `
SELECT pg_terminate_backend(pg_stat_activity.pid)
FROM pg_stat_activity
WHERE pg_stat_activity.datname = $1
  AND pid <> pg_backend_pid();`
)

var serviceName = os.Getenv("FLYNN_POSTGRES")
var serviceHost string

func init() {
	if serviceName == "" {
		serviceName = "postgres"
	}
	serviceHost = fmt.Sprintf("leader.%s.discoverd", serviceName)
}

func main() {
	defer shutdown.Exit()

	db := postgres.Wait(&postgres.Conf{
		Service:  serviceName,
		User:     "flynn",
		Password: os.Getenv("PGPASSWORD"),
		Database: "postgres",
	}, nil)
	api := &pgAPI{db}

	// Provisioned roles can CONNECT only to their own database. PUBLIC
	// CONNECT is revoked on every database, including controller/blobstore
	// DBs created at bootstrap, so another app's PGUSER cannot open them.
	revokePublicConnect(db)

	router := httprouter.New()
	router.POST("/databases", httphelper.WrapHandler(api.createDatabase))
	router.DELETE("/databases", httphelper.WrapHandler(api.dropDatabase))
	router.GET("/ping", httphelper.WrapHandler(api.ping))

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	addr := ":" + port

	hb, err := discoverd.AddServiceAndRegister(serviceName+"-api", addr)
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { hb.Close() })

	handler := httphelper.ContextInjector(serviceName+"-api", httphelper.NewRequestLogger(router))
	shutdown.Fatal(http.ListenAndServe(addr, handler))
}

type pgAPI struct {
	db *postgres.DB
}

func (p *pgAPI) createDatabase(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	username, password, database := random.Hex(16), random.Hex(16), random.Hex(16)

	if err := p.db.Exec(fmt.Sprintf(`CREATE USER "%s" WITH PASSWORD '%s'`, username, password)); err != nil {
		httphelper.Error(w, err)
		return
	}
	// Create database with the user as owner. This gives them full privileges
	// including CREATE on the public schema (required for PostgreSQL 15+).
	if err := p.db.Exec(fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, database, username)); err != nil {
		p.db.Exec(fmt.Sprintf(`DROP USER "%s"`, username))
		httphelper.Error(w, err)
		return
	}

	// Isolate the new database: revoke the default PUBLIC connect privilege
	// so that only the owning user (and the "flynn" superuser) can connect.
	if err := p.db.Exec(revokeConnectSQL(database)); err != nil {
		// best-effort cleanup
		p.db.Exec(fmt.Sprintf(`DROP DATABASE "%s"`, database))
		p.db.Exec(fmt.Sprintf(`DROP USER "%s"`, username))
		httphelper.Error(w, err)
		return
	}
	// Explicitly grant connect to the owner (redundant for owner, but
	// makes the intent clear and survives ownership changes).
	p.db.Exec(fmt.Sprintf(`GRANT CONNECT ON DATABASE "%s" TO "%s"`, database, username))

	// Revoke the user's ability to connect to the shared postgres database
	// (they should only need their own database).
	p.db.Exec(fmt.Sprintf(`REVOKE CONNECT ON DATABASE "postgres" FROM "%s"`, username))

	url := databaseURL(username, password, serviceHost, database)
	httphelper.JSON(w, 200, resource.Resource{
		ID: fmt.Sprintf("/databases/%s:%s", username, database),
		Env: map[string]string{
			"FLYNN_POSTGRES": serviceName,
			"PGHOST":         serviceHost,
			"PGUSER":         username,
			"PGPASSWORD":     password,
			"PGDATABASE":     database,
			"DATABASE_URL":   url,
		},
	})
}

func (p *pgAPI) dropDatabase(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	user, database, ok := parseDatabaseResourceID(req.FormValue("id"))
	if !ok {
		httphelper.ValidationError(w, "id", "is invalid")
		return
	}

	// disable new connections to the target database
	if err := p.db.Exec(disallowConns, database); err != nil {
		httphelper.Error(w, err)
		return
	}

	// terminate current connections
	if err := p.db.Exec(disconnectConns, database); err != nil {
		httphelper.Error(w, err)
		return
	}

	if err := p.db.Exec("DROP DATABASE " + quoteIdent(database)); err != nil {
		httphelper.Error(w, err)
		return
	}

	if err := p.db.Exec("DROP USER " + quoteIdent(user)); err != nil {
		httphelper.Error(w, err)
		return
	}

	w.WriteHeader(200)
}

func (p *pgAPI) ping(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if err := p.db.Exec("SELECT 1"); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Flynn postgres does not speak TLS. pgx v5 defaults to sslmode=prefer, so
// DATABASE_URL must disable SSL or clients log a refused handshake before
// falling back to plaintext.
func databaseURL(username, password, host, database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable", username, password, host, database)
}

func parseDatabaseResourceID(id string) (user, database string, ok bool) {
	id = strings.TrimSpace(strings.TrimPrefix(id, "/databases/"))
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 || !isHexID(parts[0]) || !isHexID(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func isHexID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func revokeConnectSQL(name string) string {
	return "REVOKE CONNECT ON DATABASE " + quoteIdent(name) + " FROM PUBLIC"
}

// revokePublicConnect drops the default PUBLIC CONNECT privilege on every
// database the flynn superuser can see. The superuser still connects; each
// provisioned role keeps an explicit GRANT CONNECT on its own database.
func revokePublicConnect(db *postgres.DB) {
	for _, sysDB := range []string{"postgres", "template1"} {
		_ = db.Exec(revokeConnectSQL(sysDB))
	}
	rows, err := db.Query(`SELECT datname FROM pg_database WHERE datallowconn`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		_ = db.Exec(revokeConnectSQL(name))
	}
}
