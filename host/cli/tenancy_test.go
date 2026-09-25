package cli

import (
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/networkpolicy"
)

func TestTenancyHostCommandsRegistered(t *testing.T) {
	for _, name := range []string{"tenancy:mode", "tenancy:network-policy", "user:bootstrap-admin"} {
		if commands[name] == nil {
			t.Fatalf("missing %s", name)
		}
		if strings.Contains(name, " ") {
			t.Fatalf("space in %s", name)
		}
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
