package platformpg

import "testing"

func controllerEnv() map[string]string {
	return map[string]string{
		"FLYNN_POSTGRES": "postgres",
		"PGHOST":         "leader.postgres.discoverd",
		"PGUSER":         "controller-role",
		"PGPASSWORD":     "controller-secret",
		"PGDATABASE":     "controller",
	}
}

func postgresEnv() map[string]string {
	return map[string]string{
		"PGHOST":     "leader.postgres.discoverd",
		"PGUSER":     "flynn",
		"PGPASSWORD": "superuser-secret",
		"PGDATABASE": "postgres",
	}
}

func TestFromControllerPsqlTargetsPlatformApp(t *testing.T) {
	job, err := FromController(controllerEnv(), "rel-1", postgresEnv(), "psql", []string{"-c", "SELECT 1"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.App != "postgres" || job.ReleaseID != "rel-1" || job.Env["PGDATABASE"] != "controller" {
		t.Fatalf("%+v", job)
	}
	if job.Env["PGUSER"] != "flynn" || job.Env["PGPASSWORD"] != "superuser-secret" {
		t.Fatal("psql must log in as the platform superuser so \\c reaches every database")
	}
	if len(job.Args) != 3 || job.Args[0] != "psql" || job.Args[2] != "SELECT 1" {
		t.Fatalf("args %v", job.Args)
	}
	if job.Env["PAGER"] != "less" || job.Env["LESS"] == "" {
		t.Fatal("interactive psql must set PAGER=less")
	}
}

func TestFromControllerRejectsTenantShape(t *testing.T) {
	env := controllerEnv()
	delete(env, "FLYNN_POSTGRES")
	if _, err := FromController(env, "rel-1", postgresEnv(), "psql", nil, 1); err == nil {
		t.Fatal("expected missing platform postgres")
	}
}

func TestFromControllerPsqlRequiresSuperuser(t *testing.T) {
	pg := postgresEnv()
	delete(pg, "PGPASSWORD")
	if _, err := FromController(controllerEnv(), "rel-1", pg, "psql", nil, 1); err == nil {
		t.Fatal("psql must not fall back to the controller role")
	}
}

func TestDumpRestoreKeepControllerRole(t *testing.T) {
	for _, kind := range []string{"dump", "restore"} {
		job, err := FromController(controllerEnv(), "rel-1", postgresEnv(), kind, nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		if job.Env["PGUSER"] != "controller-role" || job.Env["PGPASSWORD"] != "controller-secret" {
			t.Fatalf("%s must keep the controller role", kind)
		}
	}
	if _, err := FromController(controllerEnv(), "rel-1", nil, "dump", nil, 1); err != nil {
		t.Fatalf("dump must not need the postgres release env: %s", err)
	}
}

func TestRestoreParallelUsesDataVolume(t *testing.T) {
	job, err := FromController(controllerEnv(), "rel-1", postgresEnv(), "restore", nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !job.Data || job.Args[0] != "bash" {
		t.Fatalf("%+v", job)
	}
}

func TestDumpArgs(t *testing.T) {
	job, err := FromController(controllerEnv(), "rel-1", postgresEnv(), "dump", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Args[0] != "pg_dump" || job.Data {
		t.Fatalf("%+v", job)
	}
}
