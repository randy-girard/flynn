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
