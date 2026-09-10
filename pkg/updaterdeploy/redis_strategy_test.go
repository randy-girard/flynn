package updaterdeploy

import (
	"errors"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/inconshreveable/log15"
)

type fakeAppUpdater struct {
	updated *ct.App
	err     error
}

func (f *fakeAppUpdater) UpdateApp(app *ct.App) error {
	f.updated = app
	return f.err
}

func TestEnsureRedisApplianceStrategyUpdatesAllAtOnce(t *testing.T) {
	app := &ct.App{
		Name:     "redis-11111111-2222-3333-4444-555555555555",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "all-at-once",
	}
	fake := &fakeAppUpdater{}
	if err := EnsureRedisApplianceStrategy(fake, app, log15.New()); err != nil {
		t.Fatal(err)
	}
	if fake.updated == nil {
		t.Fatal("expected UpdateApp for all-at-once redis appliance")
	}
	if app.Strategy != ct.RedisApplianceStrategy {
		t.Fatalf("Strategy = %q, want %q", app.Strategy, ct.RedisApplianceStrategy)
	}
}

func TestEnsureRedisApplianceStrategyNoopWhenAlreadySet(t *testing.T) {
	app := ct.NewRedisApplianceApp("redis-11111111-2222-3333-4444-555555555555")
	fake := &fakeAppUpdater{}
	if err := EnsureRedisApplianceStrategy(fake, app, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated != nil {
		t.Fatal("UpdateApp must not be called when strategy is already correct")
	}
}

func TestEnsureRedisApplianceStrategyIgnoresUserApps(t *testing.T) {
	app := &ct.App{Name: "upgrade-smoke", Strategy: "all-at-once"}
	fake := &fakeAppUpdater{}
	if err := EnsureRedisApplianceStrategy(fake, app, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated != nil {
		t.Fatal("UpdateApp must not be called for a user app")
	}
	if app.Strategy != "all-at-once" {
		t.Fatalf("user Strategy = %q, want all-at-once", app.Strategy)
	}
}

func TestEnsureRedisApplianceStrategyNilApp(t *testing.T) {
	if err := EnsureRedisApplianceStrategy(&fakeAppUpdater{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRedisApplianceStrategyOneByOne(t *testing.T) {
	app := &ct.App{
		Name:     "redis-11111111-2222-3333-4444-555555555555",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "one-by-one",
	}
	fake := &fakeAppUpdater{}
	if err := EnsureRedisApplianceStrategy(fake, app, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated == nil {
		t.Fatal("one-by-one also starts the new job while the old holds /data; must switch")
	}
	if app.Strategy != ct.RedisApplianceStrategy {
		t.Fatalf("Strategy = %q, want %q", app.Strategy, ct.RedisApplianceStrategy)
	}
}

func TestEnsureRedisApplianceStrategyUpdateError(t *testing.T) {
	app := &ct.App{
		Name:     "redis-11111111-2222-3333-4444-555555555555",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "all-at-once",
	}
	fake := &fakeAppUpdater{err: errors.New("boom")}
	err := EnsureRedisApplianceStrategy(fake, app, nil)
	if err == nil {
		t.Fatal("expected UpdateApp error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q must wrap the UpdateApp cause", err)
	}
}
