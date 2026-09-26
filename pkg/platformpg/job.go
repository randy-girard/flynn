// Package platformpg builds one-off jobs for the platform Postgres appliance.
// Tenant databases are the postgres plugin (flynn pg). These jobs are what
// flynn-host runs against the controller database.
package platformpg

import (
	"fmt"
	"strconv"
)

// Job is a one-off on the platform Postgres app.
type Job struct {
	App       string
	ReleaseID string
	Env       map[string]string
	Args      []string
	// Data asks for a /data volume so a parallel restore can spool the dump.
	Data bool
}

// FromController builds a psql, dump, or restore job from the controller
// release env. pgReleaseID is the current release of the FLYNN_POSTGRES app.
func FromController(controllerEnv map[string]string, pgReleaseID, kind string, psqlArgs []string, jobs int) (Job, error) {
	if controllerEnv == nil {
		return Job{}, fmt.Errorf("platform controller release has no env")
	}
	app := controllerEnv["FLYNN_POSTGRES"]
	if app == "" {
		return Job{}, fmt.Errorf("platform controller release has no FLYNN_POSTGRES; tenant Postgres is flynn pg from the postgres plugin")
	}
	env := make(map[string]string, 4)
	for _, k := range []string{"PGHOST", "PGUSER", "PGPASSWORD", "PGDATABASE"} {
		v := controllerEnv[k]
		if v == "" {
			return Job{}, fmt.Errorf("platform controller release is missing %s", k)
		}
		env[k] = v
	}
	if pgReleaseID == "" {
		return Job{}, fmt.Errorf("platform postgres release is missing")
	}
	job := Job{App: app, ReleaseID: pgReleaseID, Env: env}
	switch kind {
	case "psql":
		env["PAGER"] = "less"
		env["LESS"] = "--ignore-case --LONG-PROMPT --SILENT --tabs=4 --quit-if-one-screen --no-init --quit-at-eof"
		job.Args = append([]string{"psql"}, psqlArgs...)
	case "dump":
		job.Args = []string{"pg_dump", "--format=custom", "--no-owner", "--no-acl"}
	case "restore":
		if jobs < 1 {
			jobs = 1
		}
		if jobs == 1 {
			job.Args = []string{"pg_restore", "-d", env["PGDATABASE"], "-n", "public", "--clean", "--if-exists", "--no-owner", "--no-acl"}
			break
		}
		job.Data = true
		job.Args = []string{
			"bash", "-c",
			fmt.Sprintf("set -o pipefail; cat > /data/temp.dump && pg_restore -d %s -n public --clean --if-exists --no-owner --no-acl --jobs %s /data/temp.dump", env["PGDATABASE"], strconv.Itoa(jobs)),
		}
	default:
		return Job{}, fmt.Errorf("unknown platform postgres job %q", kind)
	}
	return job, nil
}
