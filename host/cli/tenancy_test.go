package cli

import (
	"errors"
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
	hosted := func() (string, error) { return "hosted", nil }

	var out strings.Builder
	if err := applyTenancyMode(&out, put, noCheck, hosted, "self_hosted", false); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || len(calls) != 1 || calls[0] != (call{"self_hosted", false}) {
		t.Fatalf("plain: out %q calls %v", out.String(), calls)
	}

	out.Reset()
	if err := applyTenancyMode(&out, put, noCheck, hosted, "self_hosted", true); err != nil {
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

// TestTenancyModeForceMessages: NEW-47 / NEW-45. --force self_hosted prints
// the "while the cluster is hosted" warning only when the cluster is hosted.
// On a self_hosted cluster it says there is nothing to override. --force
// hosted is the cluster key's override of a failing production:check: the
// CLI prints the failing rows and what forcing does, then sends force, and
// the controller records tenancy.force_hosted. Without --force a failing
// check still refuses before the PUT.
func TestTenancyModeForceMessages(t *testing.T) {
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
	failing := func() error {
		return errors.New("refusing hosted: production:check would fail (blobstore); run flynn-host production:check")
	}
	mode := func(m string) func() (string, error) { return func() (string, error) { return m, nil } }

	var out strings.Builder
	if err := applyTenancyMode(&out, put, noCheck, mode("self_hosted"), "self_hosted", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "while the cluster is hosted") || strings.Contains(out.String(), "WARNING") {
		t.Fatalf("already self_hosted must not claim the cluster is hosted: %q", out.String())
	}
	if !strings.Contains(out.String(), "already self_hosted") {
		t.Fatalf("already self_hosted must say so: %q", out.String())
	}

	out.Reset()
	calls = nil
	if err := applyTenancyMode(&out, put, failing, mode("self_hosted"), "hosted", false); err == nil || !strings.Contains(err.Error(), "blobstore") {
		t.Fatalf("a failing check must refuse hosted without --force: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("refused hosted must not PUT: %v", calls)
	}

	out.Reset()
	if err := applyTenancyMode(&out, put, failing, mode("self_hosted"), "hosted", true); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != (call{"hosted", true}) {
		t.Fatalf("--force hosted must send force: %v", calls)
	}
	for _, want := range []string{"WARNING", "blobstore", "tenancy.force_hosted", "quotas"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("force hosted warning missing %q: %q", want, out.String())
		}
	}

	if _, err := docopt.Parse(commands["tenancy:mode"].usage, []string{"tenancy:mode", "--force", "hosted"}, true, "", false); err != nil {
		t.Fatalf("usage must accept --force hosted: %v", err)
	}
	usage := commands["tenancy:mode"].usage
	if strings.Contains(usage, "does not run production:check") {
		t.Fatalf("help must not say hosted skips production:check: %s", usage)
	}
	for _, want := range []string{"controller", "tenancy.force_hosted"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("help missing %q: %s", want, usage)
		}
	}
}
