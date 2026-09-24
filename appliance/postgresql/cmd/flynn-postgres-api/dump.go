package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func validDumpDatabase(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func pgDumpArgv(database string) []string {
	return []string{"pg_dump", "--format=custom", "--no-owner", "--no-acl", "--dbname=" + database}
}

func pgRestoreArgv(database string) []string {
	return []string{"pg_restore", "--clean", "--if-exists", "--no-owner", "--no-acl", "--dbname=" + database}
}

func pgDumpallArgv() []string {
	return []string{"pg_dumpall", "--clean", "--if-exists", "--exclude-database=template0", "--exclude-database=template1"}
}

func pgDumpallRestoreArgv() []string {
	return []string{"psql", "--dbname=postgres", "-v", "ON_ERROR_STOP=1"}
}

func pgClientEnv() []string {
	env := os.Environ()
	env = append(env, "PGHOST="+serviceHost, "PGUSER=flynn", "PGPORT=5432")
	if os.Getenv("PGPASSWORD") != "" {
		env = append(env, "PGPASSWORD="+os.Getenv("PGPASSWORD"))
	}
	return env
}

func (p *pgAPI) dumpDatabase(_ context.Context, w http.ResponseWriter, req *http.Request) {
	database := strings.TrimSpace(req.URL.Query().Get("database"))
	argv := pgDumpArgv(database)
	filename := "postgres.dump"
	if database == "" {
		argv = pgDumpallArgv()
		filename = "postgres-all.sql"
	} else if !validDumpDatabase(database) {
		httphelper.ValidationError(w, "database", "is invalid")
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = pgClientEnv()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cmd.Stdout = w
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		httphelper.Error(w, fmt.Errorf("%s: %w", argv[0], err))
	}
}

func (p *pgAPI) restoreDatabase(_ context.Context, w http.ResponseWriter, req *http.Request) {
	database := strings.TrimSpace(req.URL.Query().Get("database"))
	argv := pgRestoreArgv(database)
	if database == "" {
		argv = pgDumpallRestoreArgv()
	} else if !validDumpDatabase(database) {
		httphelper.ValidationError(w, "database", "is invalid")
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = pgClientEnv()
	cmd.Stdin = req.Body
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		httphelper.Error(w, fmt.Errorf("%s: %w", argv[0], err))
		return
	}
	w.WriteHeader(http.StatusOK)
}
