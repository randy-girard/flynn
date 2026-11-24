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

func TestTaffyReleaseCanMintBuildTokens(t *testing.T) {
	data, err := os.ReadFile("manifest_template.json")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	i := strings.Index(s, `"id": "taffy"`)
	if i < 0 {
		t.Fatal("missing taffy step")
	}
	rest := s[i:]
	if j := strings.Index(rest[len(`"id": "taffy"`):], `"id":`); j >= 0 {
		rest = rest[:len(`"id": "taffy"`)+j]
	}
	for _, key := range []string{"ACCESS_TOKEN_KEY", "ACCESS_TOKEN_SIGNING_KEY"} {
		if !strings.Contains(rest, key) {
			t.Fatalf("taffy runs flynn-receiver and needs %s in its release env", key)
		}
	}
}

func TestRouterAndGitreceiveKeepControllerKey(t *testing.T) {
	data, err := os.ReadFile("manifest_template.json")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// logaggregator and router API need it since EDGE-04.
	for _, id := range []string{"router", "gitreceive", "logaggregator"} {
		i := strings.Index(s, `"id": "`+id+`"`)
		if i < 0 {
			t.Fatalf("missing %s", id)
		}
		rest := s[i:]
		if j := strings.Index(rest[len(`"id": "`+id+`"`):], `"id":`); j >= 0 {
			rest = rest[:len(`"id": "`+id+`"`)+j]
		}
		if !strings.Contains(rest, `"CONTROLLER_KEY"`) {
			t.Fatalf("%s release must keep CONTROLLER_KEY", id)
		}
	}
	if strings.Contains(s, "billing") {
		t.Fatal("bootstrap must not require the billing plugin")
	}
}

func TestFlannelReleaseReadsSubnetEnv(t *testing.T) {
	data, err := os.ReadFile("manifest_template.json")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	i := strings.Index(s, `"name": "flannel"`)
	if i < 0 {
		t.Fatal("missing flannel app")
	}
	rest := s[i:]
	if j := strings.Index(rest[len(`"name": "flannel"`):], `"name":`); j >= 0 {
		rest = rest[:len(`"name": "flannel"`)+j]
	}
	for _, key := range []string{
		`"NETWORK": "{{ getenv \"FLANNEL_NETWORK\" }}"`,
		`"SUBNET_LEN": "{{ getenv \"FLANNEL_SUBNET_LEN\" }}"`,
		`"SUBNET_MIN": "{{ getenv \"FLANNEL_SUBNET_MIN\" }}"`,
		`"SUBNET_MAX": "{{ getenv \"FLANNEL_SUBNET_MAX\" }}"`,
	} {
		if !strings.Contains(rest, key) {
			t.Fatalf("flannel release env missing %s", key)
		}
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
	if strings.Count(s, `"name":"platform-postgres"`) != 2 {
		t.Fatalf("controller and blobstore must use the platform-postgres provider, got %d", strings.Count(s, `"name":"platform-postgres"`))
	}
	if strings.Count(s, `"flynn-datastore": "true"`) < 2 {
		t.Fatal("platform postgres must be marked flynn-datastore so Flynn CLI cannot run a shell on it")
	}
	if strings.Contains(s, `"name":"postgres"`) {
		t.Fatal("bootstrap must not register the built-in appliance as the postgres resource provider")
	}
	if !strings.Contains(s, `"env": {"FLYNN_METRICS_PORT": "9180"},
          "args": ["/bin/start-flynn-postgres", "postgres"]`) {
		t.Fatal("platform postgres peers must set FLYNN_METRICS_PORT so flynn-host scrapes their samples")
	}
	if strings.Count(s, `(index .StepData \"discoverd-key\").Data`) < 11 {
		t.Fatalf("discoverd-key not referenced enough times: %d", strings.Count(s, `(index .StepData \"discoverd-key\").Data`))
	}
}

// Since EDGE-04 the router API answers 401 to everything but its status
// path without the cluster key, so a wait for the old 404 on / never ends
// and a fresh bootstrap times out at router-wait.
func TestRouterWaitUsesPublicStatusPath(t *testing.T) {
	data, err := os.ReadFile("manifest_template.json")
	if err != nil {
		t.Fatal(err)
	}
	// the template has unquoted placeholders, so read the step as text
	s := string(data)
	i := strings.Index(s, `"id": "router-wait"`)
	if i < 0 {
		t.Fatal("missing router-wait step")
	}
	step := s[i:]
	if j := strings.Index(step, "}"); j >= 0 {
		step = step[:j]
	}
	if !strings.Contains(step, `"url": "http://router-api.discoverd/.well-known/status"`) || !strings.Contains(step, `"status": 200`) {
		t.Fatalf("router-wait must wait for 200 on the public status path, got:\n%s", step)
	}
}
