package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/pgappliance"
)

func TestBuildProvisionPlanLimitsAndExtensions(t *testing.T) {
	plan := BuildProvisionPlan("abcdabcdabcdabcdabcdabcdabcdabcd", "p'w", "dbdbdbdbdbdbdbdbdbdbdbdbdbdbdbdb", 20)
	joined := strings.Join(plan.Maintenance, "\n")
	if !strings.Contains(joined, `CONNECTION LIMIT 20`) {
		t.Fatalf("missing connection limit:\n%s", joined)
	}
	if !strings.Contains(joined, `NOSUPERUSER`) {
		t.Fatalf("role must not be superuser:\n%s", joined)
	}
	if !strings.Contains(joined, `PASSWORD 'p''w'`) {
		t.Fatalf("password literal:\n%s", joined)
	}
	if !strings.Contains(joined, `REVOKE CONNECT ON DATABASE "dbdbdbdbdbdbdbdbdbdbdbdbdbdbdbdb" FROM PUBLIC`) {
		t.Fatalf("connect revoke:\n%s", joined)
	}
	tenant := strings.Join(plan.TenantDB, "\n")
	if !strings.Contains(tenant, "CREATE EXTENSION is not allowed for tenant roles") || !strings.Contains(tenant, "CREATE EVENT TRIGGER") {
		t.Fatalf("extension block:\n%s", tenant)
	}
	if strings.Contains(tenant, "p'w") {
		t.Fatal("tenant sql must not include the password")
	}
}

func TestTenantProvisionRejectedWithoutSQL(t *testing.T) {
	user := "abcdabcdabcdabcdabcdabcdabcdabcd"
	for _, body := range [][]byte{nil, []byte(`{}`), []byte(`{"platform":false}`)} {
		plan, err := planProvision(body, user, "secret", "dbdbdbdbdbdbdbdbdbdbdbdbdbdbdbdb")
		if !errors.Is(err, pgappliance.ErrTenantProvision) {
			t.Fatalf("body %q: %v", body, err)
		}
		if len(plan.Maintenance) != 0 || len(plan.TenantDB) != 0 {
			t.Fatalf("tenant provision built SQL: %#v", plan)
		}
		if strings.Contains(err.Error(), "CREATE USER") || strings.Contains(err.Error(), "CREATE DATABASE") {
			t.Fatalf("error must not be SQL: %v", err)
		}
	}
}

func TestPlatformProvisionStillPreparesSystemDatabase(t *testing.T) {
	plan, err := planProvision([]byte(pgappliance.PlatformProvisionBody), "abcdabcdabcdabcdabcdabcdabcdabcd", "pw", "dbdbdbdbdbdbdbdbdbdbdbdbdbdbdbdb")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Maintenance, "\n")
	if !strings.Contains(joined, "CREATE USER") || !strings.Contains(joined, "CREATE DATABASE") {
		t.Fatalf("system database SQL:\n%s", joined)
	}
}

func TestProvisionEnvRefusesSuperuserPassword(t *testing.T) {
	env, err := provisionEnv("postgres", "leader.postgres.discoverd", "role", "super-secret", "appdb", "super-secret")
	if err == nil || env != nil {
		t.Fatalf("env=%v err=%v", env, err)
	}
	if !strings.Contains(err.Error(), "superuser") {
		t.Fatal(err)
	}
	env, err = provisionEnv("postgres", "leader.postgres.discoverd", "role", "role-secret", "appdb", "super-secret")
	if err != nil {
		t.Fatal(err)
	}
	if env["PGPASSWORD"] != "role-secret" || strings.Contains(env["DATABASE_URL"], "super-secret") {
		t.Fatalf("env leaked superuser: %#v", env)
	}
}
