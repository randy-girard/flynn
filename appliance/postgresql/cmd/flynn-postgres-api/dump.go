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
	if !validDumpDatabase(database) {
		httphelper.ValidationError(w, "database", "is invalid")
		return
	}
	argv := pgDumpArgv(database)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = pgClientEnv()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="postgres.dump"`)
	cmd.Stdout = w
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		httphelper.Error(w, fmt.Errorf("pg_dump: %w", err))
	}
}

func (p *pgAPI) restoreDatabase(_ context.Context, w http.ResponseWriter, req *http.Request) {
	database := strings.TrimSpace(req.URL.Query().Get("database"))
	if !validDumpDatabase(database) {
		httphelper.ValidationError(w, "database", "is invalid")
		return
	}
	argv := pgRestoreArgv(database)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = pgClientEnv()
	cmd.Stdin = req.Body
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		httphelper.Error(w, fmt.Errorf("pg_restore: %w", err))
		return
	}
	w.WriteHeader(http.StatusOK)
}
