package updaterdeploy

import (
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/updater/accesstoken"
)

type fakeSecretController struct {
	controller.Client
	releases map[string]*ct.Release
}

func (f *fakeSecretController) GetAppRelease(appID string) (*ct.Release, error) {
	if r, ok := f.releases[appID]; ok {
		return r, nil
	}
	return nil, controller.ErrNotFound
}

func TestEnsureReleaseAuthEnvBlobstoreGetsClusterKeys(t *testing.T) {
	env := map[string]string{}
	s := ClusterSecrets{ControllerKey: "ck", DiscoverdAuthKey: "dk", AccessTokenKey: "tok"}
	if !EnsureReleaseAuthEnv("blobstore", env, s) {
		t.Fatal("expected change")
	}
	if env["AUTH_KEY"] != "ck" || env["CONTROLLER_KEY"] != "ck" || env["DISCOVERD_AUTH_KEY"] != "dk" || env["ACCESS_TOKEN_KEY"] != "tok" {
		t.Fatalf("%v", env)
	}
	if EnsureReleaseAuthEnv("blobstore", env, ClusterSecrets{ControllerKey: "other"}) {
		t.Fatal("must not overwrite")
	}
}

func TestEnsureReleaseAuthEnvStatusDoesNotStealAuthKey(t *testing.T) {
	env := map[string]string{}
	s := ClusterSecrets{ControllerKey: "ck", DiscoverdAuthKey: "dk"}
	if !EnsureReleaseAuthEnv("status", env, s) {
		t.Fatal("expected change")
	}
	if env["CONTROLLER_KEY"] != "ck" || env["DISCOVERD_AUTH_KEY"] != "dk" {
		t.Fatalf("%v", env)
	}
	if _, ok := env["AUTH_KEY"]; ok {
		t.Fatal("status AUTH_KEY must stay the status-API secret")
	}
}

func TestEnsureReleaseAuthEnvPostgresAndRedis(t *testing.T) {
	s := ClusterSecrets{ControllerKey: "ck", DiscoverdAuthKey: "dk"}
	for _, name := range []string{"postgres", "redis-abc", "taffy"} {
		env := map[string]string{}
		if !EnsureReleaseAuthEnv(name, env, s) {
			t.Fatalf("%s: expected change", name)
		}
		if env["CONTROLLER_KEY"] != "ck" || env["DISCOVERD_AUTH_KEY"] != "dk" {
			t.Fatalf("%s: %v", name, env)
		}
		if _, ok := env["AUTH_KEY"]; ok {
			t.Fatalf("%s must not get AUTH_KEY alias: %v", name, env)
		}
	}
}

func TestEnsureReleaseAuthEnvNilSafe(t *testing.T) {
	if EnsureReleaseAuthEnv("blobstore", nil, ClusterSecrets{ControllerKey: "ck"}) {
		t.Fatal("nil env")
	}
}

func TestLoadClusterSecretsFromControllerAndFallback(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "")
	t.Setenv("AUTH_KEY", "")
	t.Setenv("DISCOVERD_AUTH_KEY", "from-env")
	t.Setenv("FLYNN_HOST_AUTH_KEY", "")
	client := &fakeSecretController{releases: map[string]*ct.Release{
		"controller": {Env: map[string]string{"AUTH_KEY": "cluster", "ACCESS_TOKEN_KEY": "pub"}},
		"discoverd":  {Env: map[string]string{"DISCOVERD_AUTH_KEY": "from-discoverd"}},
	}}
	s := LoadClusterSecrets(client)
	if s.ControllerKey != "cluster" {
		t.Fatalf("ControllerKey=%q", s.ControllerKey)
	}
	if s.DiscoverdAuthKey != "from-discoverd" {
		t.Fatalf("DiscoverdAuthKey=%q", s.DiscoverdAuthKey)
	}
	if s.AccessTokenKey != "pub" {
		t.Fatalf("AccessTokenKey=%q", s.AccessTokenKey)
	}

	s = LoadClusterSecrets(&fakeSecretController{})
	if s.DiscoverdAuthKey != "from-env" {
		t.Fatalf("env fallback DiscoverdAuthKey=%q", s.DiscoverdAuthKey)
	}
}

func TestBackfillAppAuthSkipsUserApps(t *testing.T) {
	env := map[string]string{}
	changed, err := BackfillAppAuth(nil, &ct.App{Name: "myapp"}, env)
	if err != nil || changed {
		t.Fatalf("user app: %v %v", changed, err)
	}
	if len(env) != 0 {
		t.Fatalf("must not inject cluster keys into user apps: %v", env)
	}
}

func TestBackfillAppAuthFillsBlobstore(t *testing.T) {
	accesstoken.ResetPairForTest()
	client := &fakeSecretController{releases: map[string]*ct.Release{
		"controller": {Env: map[string]string{"AUTH_KEY": "cluster", "ACCESS_TOKEN_KEY": "pub"}},
		"gitreceive": {Env: map[string]string{
			"ACCESS_TOKEN_KEY":         "pub",
			"ACCESS_TOKEN_SIGNING_KEY": "priv",
		}},
	}}
	env := map[string]string{}
	app := &ct.App{Name: "blobstore", Meta: map[string]string{"flynn-system-app": "true"}}
	changed, err := BackfillAppAuth(client, app, env)
	if err != nil || !changed {
		t.Fatalf("blobstore: changed=%v err=%v", changed, err)
	}
	if env["AUTH_KEY"] != "cluster" || env["CONTROLLER_KEY"] != "cluster" {
		t.Fatalf("%v", env)
	}
}
