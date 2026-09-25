package networkpolicy

import (
	"strings"
	"testing"
)

func TestSelfHostedEmpty(t *testing.T) {
	got, err := Rules("self_hosted", "user:a", SystemDeny(), nil)
	if err != nil || got != "" {
		t.Fatalf("self_hosted rules %q err %v", got, err)
	}
	got, err = Rules("", "user:a", SystemDeny(), nil)
	if err != nil || got != "" {
		t.Fatalf("default rules %q err %v", got, err)
	}
}

func TestHostedDeniesSystemAndForeignDB(t *testing.T) {
	_, err := Rules("hosted", "user:b", SystemDeny(), []Endpoint{{
		Owner: "user:a", CIDR: "10.1.0.5/32", Port: 5432, Name: "db-a",
	}})
	if err == nil || !strings.Contains(err.Error(), "user:a") {
		t.Fatalf("tenant B must not include tenant A database: %v", err)
	}
	got, err := Rules("hosted", "user:b", SystemDeny(), []Endpoint{{
		Owner: "user:b", CIDR: "10.1.0.9/32", Port: 5432, Name: "db-b",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"host-api", "dport 1113 drop", "postgres-admin", "discoverd-write", "blobstore-internal", "controller-internal", "db-b", "dport 5432 accept"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("missing %q in\n%s", needle, got)
		}
	}
	if strings.Contains(got, "user:a") {
		t.Fatalf("foreign owner leaked:\n%s", got)
	}
}
