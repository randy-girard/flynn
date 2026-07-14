package postgres

import (
	"testing"
	"time"
)

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
