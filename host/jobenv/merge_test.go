package jobenv

import (
	"testing"

	"github.com/randy-girard/flynn/pkg/netpolicy"
)

func TestMergeKeepsHostDiscoverdWhenReleaseBlank(t *testing.T) {
	got := Merge(
		map[string]string{"DISCOVERD": "http://100.100.31.1:1111", "PATH": "/bin"},
		map[string]string{"DISCOVERD": "", "DISCOVERD_AUTH_KEY": ""},
		map[string]string{"DISCOVERD_AUTH_KEY": "host-key"},
	)
	if got["DISCOVERD"] != "http://100.100.31.1:1111" {
		t.Fatalf("blank release DISCOVERD cleared the host address: %q", got["DISCOVERD"])
	}
	if got["DISCOVERD_AUTH_KEY"] != "host-key" {
		t.Fatalf("blank release auth key cleared the host key: %q", got["DISCOVERD_AUTH_KEY"])
	}
	if got["PATH"] != "/bin" {
		t.Fatalf("PATH %q", got["PATH"])
	}
}

func TestContainerEnvOmitsDiscoverdAuthForUserAndBuild(t *testing.T) {
	key := "disc-secret"
	layers := []map[string]string{
		{"DISCOVERD": "http://100.64.1.1:1111", "PATH": "/bin"},
		{"DISCOVERD_AUTH_KEY": key, "FOO": "bar"},
		{"DISCOVERD_AUTH_KEY": key},
	}
	for _, class := range []netpolicy.Class{netpolicy.ClassUser, netpolicy.ClassBuild} {
		got := ContainerEnv(class, layers...)
		if _, ok := got["DISCOVERD_AUTH_KEY"]; ok {
			t.Fatalf("%s container env still contains DISCOVERD_AUTH_KEY", class)
		}
		if got["DISCOVERD"] != "http://100.64.1.1:1111" || got["FOO"] != "bar" || got["PATH"] != "/bin" {
			t.Fatalf("%s dropped other env: %v", class, got)
		}
	}
	for _, class := range []netpolicy.Class{netpolicy.ClassSystem, netpolicy.ClassDatastore} {
		got := ContainerEnv(class, layers...)
		if got["DISCOVERD_AUTH_KEY"] != key {
			t.Fatalf("%s must keep DISCOVERD_AUTH_KEY for discoverd registration: %v", class, got)
		}
		if got["DISCOVERD"] != "http://100.64.1.1:1111" {
			t.Fatalf("%s discoverd url: %v", class, got)
		}
	}
}

func TestMergeReleaseDiscoverdOverridesHost(t *testing.T) {
	got := Merge(
		map[string]string{"DISCOVERD": "http://100.100.31.1:1111"},
		map[string]string{"DISCOVERD": "http://100.100.70.1:1111"},
	)
	if got["DISCOVERD"] != "http://100.100.70.1:1111" {
		t.Fatalf("release DISCOVERD %q", got["DISCOVERD"])
	}
}
