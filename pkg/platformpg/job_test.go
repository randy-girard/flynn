package platformpg

import "testing"

func controllerEnv() map[string]string {
	return map[string]string{
		"FLYNN_POSTGRES": "postgres",
		"PGHOST":         "leader.postgres.discoverd",
		"PGUSER":         "flynn",
		"PGPASSWORD":     "secret",
		"PGDATABASE":     "controller",
	}
}

func TestFromControllerPsqlTargetsPlatformApp(t *testing.T) {
	job, err := FromController(controllerEnv(), "rel-1", "psql", []string{"-c", "SELECT 1"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.App != "postgres" || job.ReleaseID != "rel-1" || job.Env["PGDATABASE"] != "controller" {
		t.Fatalf("%+v", job)
	}
	if len(job.Args) != 3 || job.Args[0] != "psql" || job.Args[2] != "SELECT 1" {
		t.Fatalf("args %v", job.Args)
	}
}

func TestFromControllerRejectsTenantShape(t *testing.T) {
	env := controllerEnv()
	delete(env, "FLYNN_POSTGRES")
	if _, err := FromController(env, "rel-1", "psql", nil, 1); err == nil {
		t.Fatal("expected missing platform postgres")
	}
}

func TestRestoreParallelUsesDataVolume(t *testing.T) {
	job, err := FromController(controllerEnv(), "rel-1", "restore", nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !job.Data || job.Args[0] != "bash" {
		t.Fatalf("%+v", job)
	}
}

func TestDumpArgs(t *testing.T) {
	job, err := FromController(controllerEnv(), "rel-1", "dump", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Args[0] != "pg_dump" || job.Data {
		t.Fatalf("%+v", job)
	}
}
