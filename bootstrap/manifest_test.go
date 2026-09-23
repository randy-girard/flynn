package bootstrap

import (
	"os"
	"strings"
	"testing"
)

func TestGenRandomDiscoverdAuthKeySetsEnv(t *testing.T) {
	t.Setenv("DISCOVERD_AUTH_KEY", "")
	s := &State{StepData: map[string]interface{}{}}
	a := &GenRandomAction{ID: "discoverd-key", DiscoverdAuthKey: true}
	if err := a.Run(s); err != nil {
		t.Fatal(err)
	}
	d, ok := s.StepData["discoverd-key"].(*RandomData)
	if !ok || d.Data == "" {
		t.Fatalf("step data: %#v", s.StepData["discoverd-key"])
	}
	if len(d.Data) != 32 {
		t.Fatalf("want 128-bit hex, got len=%d", len(d.Data))
	}
	if os.Getenv("DISCOVERD_AUTH_KEY") != d.Data {
		t.Fatalf("env=%q data=%q", os.Getenv("DISCOVERD_AUTH_KEY"), d.Data)
	}
	if s.DiscoverdAuthKey() != d.Data {
		t.Fatalf("state=%q", s.DiscoverdAuthKey())
	}
}

func TestManifestGeneratesAndInjectsDiscoverdAuthKey(t *testing.T) {
	data, err := os.ReadFile("manifest_template.json")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `"id": "discoverd-key"`) {
		t.Fatal("missing discoverd-key gen-random step")
	}
	if !strings.Contains(s, `"discoverd_auth_key": true`) {
		t.Fatal("discoverd-key must set discoverd_auth_key")
	}
	if strings.Count(s, `"id": "controller-key"`) != 1 {
		t.Fatalf("controller-key must be generated once, got %d", strings.Count(s, `"id": "controller-key"`))
	}
	discKeyAt := strings.Index(s, `"id": "discoverd-key"`)
	hostAuthAt := strings.Index(s, `"id": "configure-host-auth"`)
	discoverdAt := strings.Index(s, `"id": "discoverd"`)
	if discKeyAt < 0 || hostAuthAt < 0 || discoverdAt < 0 || discKeyAt > hostAuthAt || hostAuthAt > discoverdAt {
		t.Fatal("discoverd-key must be generated and persisted on hosts before discoverd starts")
	}
	n := strings.Count(s, `"DISCOVERD_AUTH_KEY"`)
	if n < 11 {
		t.Fatalf("DISCOVERD_AUTH_KEY must be passed into system apps, got %d", n)
	}
	for _, app := range []string{"discoverd", "flannel", "postgres", "controller", "blobstore", "router", "gitreceive", "tarreceive", "logaggregator", "taffy", "status"} {
		if !strings.Contains(s, app) {
			t.Fatalf("missing app %s", app)
		}
	}
	if strings.Count(s, `(index .StepData \"discoverd-key\").Data`) < 11 {
		t.Fatalf("discoverd-key not referenced enough times: %d", strings.Count(s, `(index .StepData \"discoverd-key\").Data`))
	}
}
