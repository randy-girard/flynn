package postgres

import (
	"testing"
	"time"
)

func TestMaxPoolConnectionsIsPerProcess(t *testing.T) {
	if MaxPoolConnections < 2 || MaxPoolConnections > 8 {
		t.Fatalf("MaxPoolConnections=%d: HA multiplies this by controller processes; 20 exhausted a 20-conn role", MaxPoolConnections)
	}
}

func TestPostgresReadWriteBudget(t *testing.T) {
	if postgresReadWriteBudget < 2*time.Minute {
		t.Fatalf("postgresReadWriteBudget=%s is too short for sirenia restart recovery", postgresReadWriteBudget)
	}
}

func TestReadWritePollInterval(t *testing.T) {
	if readWritePollInterval <= 0 || readWritePollInterval > time.Second {
		t.Fatalf("readWritePollInterval=%s out of reasonable range", readWritePollInterval)
	}
}

func TestConfFromEnvUsesDatabaseURLWhenPGUserMissing(t *testing.T) {
	t.Setenv("FLYNN_POSTGRES", "")
	t.Setenv("PGUSER", "")
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGDATABASE", "")
	t.Setenv("DATABASE_URL", "postgres://role:s3cret@leader.postgres.discoverd:5432/appdb?sslmode=require")
	conf := confFromEnv()
	if conf.User != "role" || conf.Password != "s3cret" || conf.Database != "appdb" || conf.Service != "postgres" {
		t.Fatalf("%+v", conf)
	}
}

func TestConfFromEnvPrefersPGUserOverDatabaseURL(t *testing.T) {
	t.Setenv("FLYNN_POSTGRES", "postgres")
	t.Setenv("PGUSER", "flynn")
	t.Setenv("PGPASSWORD", "pw")
	t.Setenv("PGDATABASE", "controller")
	t.Setenv("DATABASE_URL", "postgres://other:x@leader.postgres.discoverd:5432/other")
	conf := confFromEnv()
	if conf.User != "flynn" || conf.Password != "pw" || conf.Database != "controller" || conf.Service != "postgres" {
		t.Fatalf("%+v", conf)
	}
}

func TestFillConfFromDatabaseURLDecodesPassword(t *testing.T) {
	conf := &Conf{}
	fillConfFromDatabaseURL(conf, "postgres://u:p%40ss@leader.postgres.discoverd:5432/db")
	if conf.User != "u" || conf.Password != "p@ss" || conf.Database != "db" || conf.Service != "postgres" {
		t.Fatalf("%+v", conf)
	}
}

func TestServiceFromPostgresHost(t *testing.T) {
	if got := serviceFromPostgresHost("leader.postgres.discoverd"); got != "postgres" {
		t.Fatalf("got %q", got)
	}
}

func TestSireniaMetaReady(t *testing.T) {
	if _, ok := sireniaMetaReady(nil); ok {
		t.Fatal("nil meta should not be ready")
	}
	if _, ok := sireniaMetaReady([]byte(`{"generation":1}`)); ok {
		t.Fatal("primary-only meta without sync should not be ready")
	}
	if singleton, ok := sireniaMetaReady([]byte(`{"generation":1,"sync":{"id":"a"}}`)); !ok || singleton {
		t.Fatalf("sync meta: ok=%v singleton=%v", ok, singleton)
	}
	if singleton, ok := sireniaMetaReady([]byte(`{"singleton":true,"primary":{"id":"a"}}`)); !ok || !singleton {
		t.Fatalf("singleton meta: ok=%v singleton=%v", ok, singleton)
	}
}
