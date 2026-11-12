package tenancy

import "testing"

func TestValidatePostgresAvailability(t *testing.T) {
	for _, mode := range []string{"system", "manual"} {
		if err := ValidatePostgresAvailability(mode); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	for _, mode := range []string{"", "hosted", "self_hosted", "SYSTEM", "on"} {
		if err := ValidatePostgresAvailability(mode); err == nil {
			t.Fatalf("%q must be rejected", mode)
		}
	}
}

func TestPostgresAvailabilityDefaultIsTenancyOnce(t *testing.T) {
	if got := PostgresAvailabilityDefault("hosted"); got != AvailabilitySystem {
		t.Fatalf("hosted default %q", got)
	}
	if got := PostgresAvailabilityDefault("self_hosted"); got != AvailabilityManual {
		t.Fatalf("self-hosted default %q", got)
	}
	if got := PostgresAvailabilityDefault(""); got != AvailabilityManual {
		t.Fatalf("unknown tenancy must not become system, got %q", got)
	}
	// Installing the hosted group onto a cluster that already has tenant
	// databases must not flip the setting. A fresh hosted install does.
	if next, set := HostedGroupAvailability(AvailabilityManual, 0); !set || next != AvailabilitySystem {
		t.Fatalf("fresh hosted install: set=%v next=%q", set, next)
	}
	if _, set := HostedGroupAvailability(AvailabilityManual, 2); set {
		t.Fatal("existing databases must keep the operator's manual setting")
	}
	if _, set := HostedGroupAvailability(AvailabilitySystem, 0); set {
		t.Fatal("system is already set")
	}
	if _, set := HostedGroupAvailability(AvailabilityManual, -1); set {
		t.Fatal("a failed resource count must not change the setting")
	}
}
