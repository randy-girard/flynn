package main

import (
	"strings"
	"testing"
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
