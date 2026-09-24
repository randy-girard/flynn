package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedisApplianceStrategy(t *testing.T) {
	app := NewRedisApplianceApp("redis-11111111-2222-3333-4444-555555555555")
	if !app.RedisAppliance() {
		t.Fatal("NewRedisApplianceApp must be classified as a redis appliance")
	}
	if app.Strategy != RedisApplianceStrategy {
		t.Fatalf("Strategy = %q, want %q", app.Strategy, RedisApplianceStrategy)
	}
	if app.EnsureRedisApplianceStrategy() {
		t.Fatal("already-correct strategy must be a no-op")
	}

	stale := &App{
		Name:     "redis-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "all-at-once",
	}
	if !stale.EnsureRedisApplianceStrategy() {
		t.Fatal("all-at-once redis appliance must switch to one-down-one-up")
	}
	if stale.Strategy != RedisApplianceStrategy {
		t.Fatalf("Strategy = %q, want %q", stale.Strategy, RedisApplianceStrategy)
	}

	user := &App{Name: "upgrade-smoke", Strategy: "all-at-once"}
	if user.EnsureRedisApplianceStrategy() {
		t.Fatal("non-appliance apps must not have their strategy rewritten")
	}
	if user.Strategy != "all-at-once" {
		t.Fatalf("user Strategy = %q, want all-at-once", user.Strategy)
	}
}

func TestRedisApplianceClassification(t *testing.T) {
	system := map[string]string{"flynn-system-app": "true"}
	cases := []struct {
		name string
		meta map[string]string
		want bool
	}{
		{"redis-11111111-2222-3333-4444-555555555555", system, true},
		{"redis", system, false}, // redis-api system app is not an appliance
		{"redis-11111111-2222-3333-4444-555555555555", nil, false},
		{"redis-11111111-2222-3333-4444-555555555555", map[string]string{"flynn-system-app": "false"}, false},
		{"redis-11111111-2222-3333-4444-555555555555", map[string]string{"flynn-system-app": "true"}, true},
		{"upgrade-smoke", system, false},
		{"mariadb", system, false},
	}
	for _, c := range cases {
		app := &App{Name: c.name, Meta: c.meta}
		if got := app.RedisAppliance(); got != c.want {
			t.Errorf("RedisAppliance name=%q system=%v = %v, want %v", c.name, c.meta != nil, got, c.want)
		}
	}
}

func TestPluginMeta(t *testing.T) {
	app := &App{Name: "widget", Meta: map[string]string{"flynn-plugin": "true", "flynn-system-app": "true"}}
	if !app.Plugin() || !app.System() {
		t.Fatal("plugin apps are system apps with flynn-plugin=true")
	}
	if (&App{Name: "widget"}).Plugin() {
		t.Fatal("missing meta is not a plugin")
	}
}

func TestNewRedisApplianceAppMetaAndStrategy(t *testing.T) {
	app := NewRedisApplianceApp("redis-deadbeef-0000-0000-0000-000000000000")
	if app.Meta["flynn-system-app"] != "true" {
		t.Fatalf("Meta = %v, want flynn-system-app=true", app.Meta)
	}
	if RedisApplianceStrategy != "one-down-one-up" {
		t.Fatalf("RedisApplianceStrategy = %q, want one-down-one-up", RedisApplianceStrategy)
	}
}

func TestEnsureRedisApplianceStrategyEmptyAndOneByOne(t *testing.T) {
	for _, stale := range []string{"", "all-at-once", "one-by-one"} {
		app := &App{
			Name:     "redis-11111111-2222-3333-4444-555555555555",
			Meta:     map[string]string{"flynn-system-app": "true"},
			Strategy: stale,
		}
		if !app.EnsureRedisApplianceStrategy() {
			t.Fatalf("strategy %q must switch to one-down-one-up", stale)
		}
		if app.Strategy != RedisApplianceStrategy {
			t.Fatalf("Strategy = %q after %q", app.Strategy, stale)
		}
	}
	if (&App{}).EnsureRedisApplianceStrategy() {
		t.Fatal("nil-name app must not be treated as a redis appliance")
	}
	already := NewRedisApplianceApp("redis-11111111-2222-3333-4444-555555555555")
	if already.EnsureRedisApplianceStrategy() {
		t.Fatal("one-down-one-up must be a no-op when already set")
	}
}

func TestEnsureRouterStrategy(t *testing.T) {
	system := map[string]string{"flynn-system-app": "true"}
	router := &App{Name: "router", Meta: system, Strategy: "all-at-once"}
	if !router.Router() {
		t.Fatal("system app named router must be classified as the cluster router")
	}
	if !router.EnsureRouterStrategy() {
		t.Fatal("all-at-once router must switch to one-down-one-up")
	}
	if router.Strategy != RouterStrategy {
		t.Fatalf("Strategy = %q, want %q", router.Strategy, RouterStrategy)
	}
	if router.EnsureRouterStrategy() {
		t.Fatal("already-correct router strategy must be a no-op")
	}

	for _, stale := range []string{"", "all-at-once", "one-by-one"} {
		app := &App{Name: "router", Meta: system, Strategy: stale}
		if !app.EnsureRouterStrategy() {
			t.Fatalf("strategy %q must switch to one-down-one-up", stale)
		}
	}

	user := &App{Name: "router", Strategy: "all-at-once"}
	if user.Router() || user.EnsureRouterStrategy() {
		t.Fatal("non-system app named router must not have its strategy rewritten")
	}
	other := &App{Name: "blobstore", Meta: system, Strategy: "all-at-once"}
	if other.EnsureRouterStrategy() {
		t.Fatal("other system apps must not have their strategy rewritten")
	}
	if (&App{}).EnsureRouterStrategy() {
		t.Fatal("empty app must not be treated as the router")
	}
	if RouterStrategy != "one-down-one-up" {
		t.Fatalf("RouterStrategy = %q, want one-down-one-up", RouterStrategy)
	}
}

func TestEnsureControllerStrategy(t *testing.T) {
	system := map[string]string{"flynn-system-app": "true"}
	controller := &App{Name: "controller", Meta: system, Strategy: "one-by-one"}
	if !controller.Controller() {
		t.Fatal("system app named controller must be classified as the cluster controller")
	}
	if !controller.EnsureControllerStrategy(1) {
		t.Fatal("one-by-one controller must switch to all-at-once on 1 host")
	}
	if controller.Strategy != ControllerStrategy {
		t.Fatalf("Strategy = %q, want %q", controller.Strategy, ControllerStrategy)
	}
	if controller.EnsureControllerStrategy(1) {
		t.Fatal("already-correct 1-host controller strategy must be a no-op")
	}

	ha := &App{Name: "controller", Meta: system, Strategy: "all-at-once"}
	if !ha.EnsureControllerStrategy(3) {
		t.Fatal("all-at-once controller must switch to one-by-one on 3 hosts")
	}
	if ha.Strategy != ControllerHAStrategy {
		t.Fatalf("HA Strategy = %q, want %q", ha.Strategy, ControllerHAStrategy)
	}
	if ha.EnsureControllerStrategy(3) {
		t.Fatal("already-correct HA controller strategy must be a no-op")
	}

	for _, stale := range []string{"", "one-by-one", "one-down-one-up"} {
		app := &App{Name: "controller", Meta: system, Strategy: stale}
		if !app.EnsureControllerStrategy(1) {
			t.Fatalf("strategy %q must switch to all-at-once on 1 host", stale)
		}
	}

	user := &App{Name: "controller", Strategy: "one-by-one"}
	if user.Controller() || user.EnsureControllerStrategy(1) {
		t.Fatal("non-system app named controller must not have its strategy rewritten")
	}
	other := &App{Name: "blobstore", Meta: system, Strategy: "one-by-one"}
	if other.EnsureControllerStrategy(1) {
		t.Fatal("other system apps must not have their strategy rewritten")
	}
	if (&App{}).EnsureControllerStrategy(1) {
		t.Fatal("empty app must not be treated as the controller")
	}
	if ControllerStrategy != "all-at-once" {
		t.Fatalf("ControllerStrategy = %q, want all-at-once", ControllerStrategy)
	}
	if ControllerHAStrategy != "one-by-one" {
		t.Fatalf("ControllerHAStrategy = %q, want one-by-one", ControllerHAStrategy)
	}
}

func TestReleaseDeployKind(t *testing.T) {
	cases := []struct {
		name   string
		rel    *Release
		git    bool
		slug   bool
		docker bool
	}{
		{name: "nil", rel: nil},
		{name: "empty", rel: &Release{}},
		{
			name: "slug heroku-24",
			rel:  &Release{Meta: map[string]string{"git": "true", "slugrunner.stack": "heroku-24"}},
			git:  true, slug: true,
		},
		{
			name: "slug default stack",
			rel:  &Release{Meta: map[string]string{"git": "true"}},
			git:  true, slug: true,
		},
		{
			name: "container stack",
			rel:  &Release{Meta: map[string]string{"git": "true", "slugrunner.stack": "container"}},
			git:  true, slug: false,
		},
		{
			name:   "docker receive",
			rel:    &Release{Meta: map[string]string{"docker-receive": "true"}},
			docker: true,
		},
	}
	for _, tc := range cases {
		if got := tc.rel.IsGitDeploy(); got != tc.git {
			t.Errorf("%s IsGitDeploy=%v want %v", tc.name, got, tc.git)
		}
		if got := tc.rel.IsSlugDeploy(); got != tc.slug {
			t.Errorf("%s IsSlugDeploy=%v want %v", tc.name, got, tc.slug)
		}
		if got := tc.rel.IsDockerReceiveDeploy(); got != tc.docker {
			t.Errorf("%s IsDockerReceiveDeploy=%v want %v", tc.name, got, tc.docker)
		}
	}
}

func TestIsSireniaSingleton(t *testing.T) {
	if (*Release)(nil).IsSireniaSingleton() {
		t.Fatal("nil release must not be a sirenia singleton")
	}
	ha := &Release{Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "false"}}
	if ha.IsSireniaSingleton() {
		t.Fatal("HA postgres (SINGLETON=false) must not defer as singleton")
	}
	unset := &Release{Env: map[string]string{"SIRENIA_PROCESS": "postgres"}}
	if unset.IsSireniaSingleton() {
		t.Fatal("sirenia without SINGLETON=true must not be treated as singleton")
	}
	one := &Release{Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "true"}}
	if !one.IsSirenia() || !one.IsSireniaSingleton() {
		t.Fatal("SINGLETON=true postgres must be a sirenia singleton")
	}
}

func TestArtifactIsSlugrunner(t *testing.T) {
	if (*Artifact)(nil).IsSlugrunner() {
		t.Fatal("nil artifact must not be slugrunner")
	}
	if (&Artifact{Meta: map[string]string{"flynn.component": "slugbuilder-24"}}).IsSlugrunner() {
		t.Fatal("slugbuilder must not count as slugrunner")
	}
	if !(&Artifact{Meta: map[string]string{"flynn.component": "slugrunner"}}).IsSlugrunner() {
		t.Fatal("legacy slugrunner alias must match")
	}
	if !(&Artifact{Meta: map[string]string{"flynn.component": "slugrunner-24"}}).IsSlugrunner() {
		t.Fatal("heroku-24 slugrunner-24 must be updated on flynn-host update")
	}
}

func TestIsInternalProcessType(t *testing.T) {
	for _, name := range []string{"slugbuilder", "SlugBuilder", "slugbuilder-24", "dockerbuilder", "slugrunner", "slugrunner-24"} {
		if !IsInternalProcessType(name) {
			t.Fatalf("%q must be an internal process type", name)
		}
	}
	for _, name := range []string{"web", "worker", "run", "runner", "console", "postgres", ""} {
		if IsInternalProcessType(name) {
			t.Fatalf("%q must not be an internal process type", name)
		}
	}
}

func TestNewJobProcessType(t *testing.T) {
	if got := NewJobProcessType(NewJob{}, false); got != ProcessTypeRunner {
		t.Fatalf("detached = %q, want runner", got)
	}
	if got := NewJobProcessType(NewJob{TTY: true}, false); got != ProcessTypeConsole {
		t.Fatalf("tty = %q, want console", got)
	}
	if got := NewJobProcessType(NewJob{}, true); got != ProcessTypeConsole {
		t.Fatalf("attach = %q, want console", got)
	}
	if got := NewJobProcessType(NewJob{Type: "custom"}, true); got != "custom" {
		t.Fatalf("explicit = %q, want custom", got)
	}
}

func TestManagedCertificateAddErrorSetsLastError(t *testing.T) {
	cert := &ManagedCertificate{Domain: "ex.com"}
	cert.AddError("order_error", "rate limited")
	if len(cert.Errors) != 1 || cert.Errors[0].Type != "order_error" {
		t.Fatalf("Errors = %+v", cert.Errors)
	}
	if cert.LastError == nil || *cert.LastError != "order_error: rate limited" {
		t.Fatalf("LastError = %v", cert.LastError)
	}
	if cert.LastErrorAt == nil {
		t.Fatal("LastErrorAt not set")
	}
}

func TestProcessTypeUnmarshalRuntime(t *testing.T) {
	var next ProcessType
	if err := json.Unmarshal([]byte(`{"runtime":"large"}`), &next); err != nil {
		t.Fatal(err)
	}
	if next.RuntimeProfile != "large" {
		t.Fatalf("runtime = %q", next.RuntimeProfile)
	}
	var legacy ProcessType
	if err := json.Unmarshal([]byte(`{"runtime_profile":"small"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.RuntimeProfile != "small" {
		t.Fatalf("legacy runtime_profile = %q", legacy.RuntimeProfile)
	}
	raw, err := json.Marshal(ProcessType{RuntimeProfile: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"runtime":"medium"`) {
		t.Fatalf("marshal = %s", raw)
	}
	if strings.Contains(string(raw), "runtime_profile") {
		t.Fatalf("marshal still used runtime_profile: %s", raw)
	}
}
