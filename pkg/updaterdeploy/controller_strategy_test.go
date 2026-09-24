package updaterdeploy

import (
	"errors"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

func TestEnsureControllerStrategyUpdatesOneByOne(t *testing.T) {
	app := &ct.App{
		Name:     "controller",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "one-by-one",
	}
	fake := &fakeAppUpdater{}
	if err := EnsureControllerStrategy(fake, app, 1, log15.New()); err != nil {
		t.Fatal(err)
	}
	if fake.updated == nil {
		t.Fatal("expected UpdateApp for one-by-one controller on 1 host")
	}
	if app.Strategy != ct.ControllerStrategy {
		t.Fatalf("Strategy = %q, want %q", app.Strategy, ct.ControllerStrategy)
	}
}

func TestEnsureControllerStrategyHAUsesOneByOne(t *testing.T) {
	app := &ct.App{
		Name:     "controller",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "all-at-once",
	}
	fake := &fakeAppUpdater{}
	if err := EnsureControllerStrategy(fake, app, 3, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated == nil {
		t.Fatal("expected UpdateApp for all-at-once controller on 3 hosts")
	}
	if app.Strategy != ct.ControllerHAStrategy {
		t.Fatalf("Strategy = %q, want %q", app.Strategy, ct.ControllerHAStrategy)
	}
}

func TestEnsureControllerStrategyNoopWhenAlreadySet(t *testing.T) {
	app := &ct.App{
		Name:     "controller",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: ct.ControllerStrategy,
	}
	fake := &fakeAppUpdater{}
	if err := EnsureControllerStrategy(fake, app, 1, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated != nil {
		t.Fatal("UpdateApp must not be called when strategy is already correct")
	}
}

func TestEnsureControllerStrategyIgnoresOtherApps(t *testing.T) {
	app := &ct.App{Name: "router", Meta: map[string]string{"flynn-system-app": "true"}, Strategy: "one-by-one"}
	fake := &fakeAppUpdater{}
	if err := EnsureControllerStrategy(fake, app, 1, nil); err != nil {
		t.Fatal(err)
	}
	if fake.updated != nil {
		t.Fatal("UpdateApp must not be called for a non-controller system app")
	}
}

func TestEnsureControllerStrategyNilApp(t *testing.T) {
	if err := EnsureControllerStrategy(&fakeAppUpdater{}, nil, 1, nil); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureControllerStrategyUpdateError(t *testing.T) {
	app := &ct.App{
		Name:     "controller",
		Meta:     map[string]string{"flynn-system-app": "true"},
		Strategy: "one-by-one",
	}
	fake := &fakeAppUpdater{err: errors.New("boom")}
	err := EnsureControllerStrategy(fake, app, 1, nil)
	if err == nil {
		t.Fatal("expected UpdateApp error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q must wrap the UpdateApp cause", err)
	}
}
