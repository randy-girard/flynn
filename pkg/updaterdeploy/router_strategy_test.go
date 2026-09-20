package updaterdeploy

import (
	"errors"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

func TestEnsureRouterStrategyUpdatesAllAtOnce(t *testing.T) {
	app := &ct.App{
		Name:     "router",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "all-at-once",
	}
	fake := &fakeAppUpdater{}
	if err := EnsureRouterStrategy(fake, app, log15.New()); err != nil {
		t.Fatal(err)
	}
	if fake.updated == nil {
		t.Fatal("expected UpdateApp for all-at-once router")
	}
	if app.Strategy != ct.RouterStrategy {
		t.Fatalf("Strategy = %q, want %q", app.Strategy, ct.RouterStrategy)
	}
}

func TestEnsureRouterStrategyNoopWhenAlreadySet(t *testing.T) {
	app := &ct.App{
		Name:     "router",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: ct.RouterStrategy,
	}
	fake := &fakeAppUpdater{}
	if err := EnsureRouterStrategy(fake, app, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated != nil {
		t.Fatal("UpdateApp must not be called when strategy is already correct")
	}
}

func TestEnsureRouterStrategyIgnoresOtherApps(t *testing.T) {
	app := &ct.App{Name: "blobstore", Meta: map[string]string{"flynn-system-app": "true"}, Strategy: "all-at-once"}
	fake := &fakeAppUpdater{}
	if err := EnsureRouterStrategy(fake, app, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated != nil {
		t.Fatal("UpdateApp must not be called for a non-router system app")
	}
}

func TestEnsureRouterStrategyNilApp(t *testing.T) {
	if err := EnsureRouterStrategy(&fakeAppUpdater{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRouterStrategyUpdateError(t *testing.T) {
	app := &ct.App{
		Name:     "router",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "all-at-once",
	}
	fake := &fakeAppUpdater{err: errors.New("boom")}
	err := EnsureRouterStrategy(fake, app, nil)
	if err == nil {
		t.Fatal("expected UpdateApp error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q must wrap the UpdateApp cause", err)
	}
}
