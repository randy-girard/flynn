package main

import (
	"fmt"
	"io"
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
	router.GET("/dump", httphelper.WrapHandler(api.dumpDatabase))
	router.POST("/dump", httphelper.WrapHandler(api.dumpDatabase))
	router.POST("/restore", httphelper.WrapHandler(api.restoreDatabase))

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
	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	username, password, database := random.Hex(16), random.Hex(16), random.Hex(16)
	if err := rejectSuperuserPassword(password, os.Getenv("PGPASSWORD")); err != nil {
		httphelper.Error(w, err)
		return
	}
	// Tenant requests fail here, before CREATE USER / CREATE DATABASE is built.
	plan, err := planProvision(body, username, password, database)
	if err != nil {
		httphelper.ValidationError(w, "provider", err.Error())
		return
	}
	env, err := provisionEnv(serviceName, serviceHost, username, password, database, os.Getenv("PGPASSWORD"))
	if err != nil {
		httphelper.Error(w, err)
		return
	}

	var applied []string
	fail := func(err error) {
		for i := len(applied) - 1; i >= 0; i-- {
			switch {
			case strings.HasPrefix(applied[i], "CREATE DATABASE"):
				p.db.Exec("DROP DATABASE " + quoteIdent(database))
			case strings.HasPrefix(applied[i], "CREATE USER"):
				p.db.Exec("DROP USER " + quoteIdent(username))
			}
		}
		httphelper.Error(w, err)
	}
	for _, stmt := range plan.Maintenance {
		if err := p.db.Exec(stmt); err != nil {
			fail(err)
			return
		}
		applied = append(applied, stmt)
	}
	if err := p.applyTenantDB(database, plan.TenantDB); err != nil {
		fail(err)
		return
	}

	httphelper.JSON(w, 200, resource.Resource{
		ID:  fmt.Sprintf("/databases/%s:%s", username, database),
		Env: env,
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

// applyTenantDB installs the CREATE EXTENSION block inside the new database.
func (p *pgAPI) applyTenantDB(database string, stmts []string) error {
	tenant, err := postgres.Open(&postgres.Conf{
		Service:  serviceName,
		User:     "flynn",
		Password: os.Getenv("PGPASSWORD"),
		Database: database,
	}, nil)
	if err != nil {
		return err
	}
	defer tenant.Close()
	for _, stmt := range stmts {
		if err := tenant.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Flynn postgres speaks TLS (ssl=on) but still accepts non-TLS so in-cluster
// discoverd clients using sslmode=disable keep working. New DATABASE_URL values
// require TLS (encrypt, no CA verify) so app clients prefer TLS by default.
func databaseURL(username, password, host, database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=require", username, password, host, database)
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
