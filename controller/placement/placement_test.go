package placement

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestValidateMode(t *testing.T) {
	if err := ValidateMode(ModeMixed); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMode(ModeSplit); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMode("region"); err == nil {
		t.Fatal("region is not a placement mode")
	}
	if err := ValidateMode(""); err == nil {
		t.Fatal("empty mode")
	}
}

func TestMixedIgnoresRole(t *testing.T) {
	job := Classify(&ct.App{Name: "web"}, false)
	if !HostAllows(ModeMixed, job, map[string]string{}) {
		t.Fatal("mixed must place a user job on an untagged host")
	}
	if !HostAllows("", job, map[string]string{TagRole: RoleCore}) {
		t.Fatal("before the first read, placement behaves as mixed")
	}
}

func TestSplitUserJobNeedsAppHost(t *testing.T) {
	job := Classify(&ct.App{Name: "web"}, false)
	if HostAllows(ModeSplit, job, map[string]string{TagRole: RoleCore}) {
		t.Fatal("split must not place a user job on a core host")
	}
	if HostAllows(ModeSplit, job, nil) {
		t.Fatal("split must not place a user job on an untagged host")
	}
	if !HostAllows(ModeSplit, job, map[string]string{TagRole: RoleApp}) {
		t.Fatal("split places a user job on an app host")
	}
}

func TestSplitControllerWebNeedsCore(t *testing.T) {
	job := Classify(&ct.App{Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}, false)
	if !HostAllows(ModeSplit, job, map[string]string{TagRole: RoleCore}) {
		t.Fatal("controller web places on a core host")
	}
	if HostAllows(ModeSplit, job, map[string]string{TagRole: RoleApp}) {
		t.Fatal("controller web must not place on an app host")
	}
}

func TestSplitRouterIsOmni(t *testing.T) {
	job := Classify(&ct.App{Name: "router", Meta: map[string]string{"flynn-system-app": "true"}}, true)
	if !job.Omni {
		t.Fatal("router process is omni")
	}
	if !HostAllows(ModeSplit, job, map[string]string{TagRole: RoleApp}) {
		t.Fatal("the router still places on an app host")
	}
	if !HostAllows(ModeSplit, job, nil) {
		t.Fatal("omni still places on an untagged host")
	}
}

func TestSplitKeepsPluginsAndPlatformPostgresOnCore(t *testing.T) {
	plugin := Classify(&ct.App{
		Name: "postgres-plugin",
		Meta: map[string]string{"flynn-system-app": "true", "flynn-plugin": "true", "flynn-datastore": "true"},
	}, false)
	if plugin.Role != RoleCore {
		t.Fatalf("postgres-plugin role %q", plugin.Role)
	}
	platform := Classify(&ct.App{
		Name: "postgres",
		Meta: map[string]string{"flynn-system-app": "true", "flynn-datastore": "true"},
	}, false)
	if platform.Role != RoleCore {
		t.Fatalf("platform postgres role %q", platform.Role)
	}
	tenant := Classify(&ct.App{
		Name: "postgresql-concave-48291",
		Meta: map[string]string{"flynn-system-app": "true", "flynn-datastore": "true"},
	}, false)
	if tenant.Role != RoleApp {
		t.Fatalf("tenant postgres role %q", tenant.Role)
	}
	redis := Classify(&ct.App{
		Name: "redis-11111111-1111-1111-1111-111111111111",
		Meta: map[string]string{"flynn-system-app": "true", "flynn-datastore": "true"},
	}, false)
	if redis.Role != RoleApp {
		t.Fatalf("redis appliance role %q", redis.Role)
	}
}

func TestSplitRequiresALiveCoreHost(t *testing.T) {
	if HasCore([]Host{{Tags: map[string]string{TagRole: RoleApp}}}) {
		t.Fatal("an app host is not a core host")
	}
	if HasCore([]Host{{Tags: map[string]string{TagRole: RoleCore}, Shutdown: true}}) {
		t.Fatal("a shutting-down core host does not count")
	}
	if !HasCore([]Host{{Tags: map[string]string{TagRole: RoleCore}}}) {
		t.Fatal("a live core host allows split")
	}
	if HasApp(nil) {
		t.Fatal("zero app hosts is allowed and must report false")
	}
}
