package cli

import (
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/host/networkpolicy"
)

func TestTenancyHostCommandsRegistered(t *testing.T) {
	for _, name := range []string{"tenancy:mode", "tenancy:network-policy", "postgres:availability", "mysql:availability", "redis:availability", "mongodb:availability", "kafka:availability", "clickhouse:availability", "user:bootstrap-admin", "user:create", "user:list", "user:admin"} {
		if commands[name] == nil {
			t.Fatalf("missing %s", name)
		}
		if strings.Contains(name, " ") {
			t.Fatalf("space in %s", name)
		}
	}
	if commands["tenancy:set"] != nil {
		t.Fatal("tenancy:set must not exist")
	}
}

func TestTenancyModeBodyIsModeOnly(t *testing.T) {
	body := tenancyModeBody("hosted")
	if len(body) != 1 || body["mode"] != "hosted" {
		t.Fatalf("%v", body)
	}
	if _, ok := body["signup_enabled"]; ok {
		t.Fatal("signup_enabled must not be in the body")
	}
	self := tenancyModeBody("self_hosted")
	if len(self) != 1 || self["mode"] != "self_hosted" {
		t.Fatalf("%v", self)
	}
}

func TestNetworkPolicyDryRunDeniesForeignOwner(t *testing.T) {
	_, err := networkpolicy.Rules("hosted", "user:b", networkpolicy.SystemDeny(), []networkpolicy.Endpoint{{
		Owner: "user:a", Name: "db", CIDR: "10.1.0.9/32", Port: 5432,
	}})
	if err == nil || !strings.Contains(err.Error(), "user:a") {
		t.Fatalf("foreign endpoint: %v", err)
	}
	self, err := networkpolicy.Rules("self_hosted", "user:b", networkpolicy.SystemDeny(), nil)
	if err != nil || self != "" {
		t.Fatalf("self_hosted rules %q err %v", self, err)
	}
}

func TestHostReadPasswordUsesFlag(t *testing.T) {
	got, generated, err := hostReadPassword("s3cret")
	if err != nil || generated || got != "s3cret" {
		t.Fatalf("%q generated=%v err=%v", got, generated, err)
	}
}

// TestTenancyModeForce: --force is only for self_hosted, prints what the
// override does before it is sent, and passes force=true. Without --force
// the PUT is the plain mode body and nothing is printed.
func TestTenancyModeForce(t *testing.T) {
	type call struct {
		mode  string
		force bool
	}
	var calls []call
	put := func(mode string, force bool) error {
		calls = append(calls, call{mode, force})
		return nil
	}
	noCheck := func() error { return nil }

	var out strings.Builder
	if err := applyTenancyMode(&out, put, noCheck, "hosted", true); err == nil || !strings.Contains(err.Error(), "self_hosted") {
		t.Fatalf("--force with hosted: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("hosted --force must not PUT: %v", calls)
	}

	out.Reset()
	if err := applyTenancyMode(&out, put, noCheck, "self_hosted", false); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || len(calls) != 1 || calls[0] != (call{"self_hosted", false}) {
		t.Fatalf("plain: out %q calls %v", out.String(), calls)
	}

	out.Reset()
	if err := applyTenancyMode(&out, put, noCheck, "self_hosted", true); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1] != (call{"self_hosted", true}) {
		t.Fatalf("force: calls %v", calls)
	}
	for _, want := range []string{"WARNING", "owner_account", "unlimited", "account_quotas", "reserve_resources", "tenancy.force_self_hosted"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("warning missing %q: %q", want, out.String())
		}
	}

	if _, err := docopt.Parse(commands["tenancy:mode"].usage, []string{"tenancy:mode", "--force", "self_hosted"}, true, "", false); err != nil {
		t.Fatalf("usage must accept --force: %v", err)
	}
}
