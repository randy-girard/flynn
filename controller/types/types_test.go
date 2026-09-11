package types

import "testing"

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
			name: "docker receive",
			rel:  &Release{Meta: map[string]string{"docker-receive": "true"}},
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
