package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/buildlock"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

var errForbiddenForTest = httphelper.JSONError{Code: httphelper.ForbiddenErrorCode, Message: "forbidden"}

// lockingStore is the controller for beginBuild: app meta plus the lease.
type lockingStore struct {
	fakeBuildingStore
	held       *ct.BuildLock
	acquireErr error
	acquires   int
	metaWrites int
	events     []string
}

func (s *lockingStore) UpdateAppMeta(app *ct.App) error {
	s.metaWrites++
	if app.Meta[ct.MetaAppBuilding] != "" {
		s.events = append(s.events, "mark")
	} else {
		s.events = append(s.events, "clear")
	}
	return s.fakeBuildingStore.UpdateAppMeta(app)
}

func (s *lockingStore) AcquireBuildLock(appID string, req *ct.BuildLock) (*ct.BuildLock, error) {
	s.acquires++
	if s.acquireErr != nil {
		return nil, s.acquireErr
	}
	if s.held != nil && s.held.Holder != req.Holder {
		return nil, &ct.BuildLockHeldError{Lock: s.held}
	}
	if s.held == nil {
		s.events = append(s.events, "acquire")
	}
	s.held = &ct.BuildLock{AppID: appID, Holder: req.Holder, Description: req.Description}
	return s.held, nil
}

func (s *lockingStore) ReleaseBuildLock(appID, holder string) error {
	if s.held != nil && s.held.Holder == holder {
		s.events = append(s.events, "release")
		s.held = nil
	}
	return nil
}

// TestBeginBuildRefusesWhileAnotherBuildRuns (EDGE-09): a GitHub deploy or a
// preview push (flynn-receiver with no lease from gitreceive) is refused
// while another build of the app holds the lease, and leaves that build's
// "building" marker alone.
func TestBeginBuildRefusesWhileAnotherBuildRuns(t *testing.T) {
	t.Setenv(buildlock.HolderEnv, "")
	started := time.Date(2026, 11, 22, 12, 0, 1, 0, time.UTC)
	app := &ct.App{ID: "app-1", Name: "myapp", Meta: ct.MarkAppBuildingMeta(nil)}
	store := &lockingStore{
		fakeBuildingStore: fakeBuildingStore{app: app},
		held:              &ct.BuildLock{Holder: "gitreceive-push", Description: "a git push", AcquiredAt: &started},
	}
	_, err := beginBuild(store, app, map[string]string{"github": "true"})
	if err == nil {
		t.Fatal("beginBuild succeeded while another build holds the lease")
	}
	if !strings.Contains(err.Error(), "a git push to this app is in progress (started 12:00:01 UTC)") {
		t.Fatalf("error does not name the running build: %v", err)
	}
	if store.metaWrites != 0 {
		t.Fatalf("refused build wrote app meta %d times", store.metaWrites)
	}
	if store.fakeBuildingStore.app.Meta[ct.MetaAppBuilding] == "" {
		t.Fatal("refused build cleared the running build's marker")
	}
}

// TestBeginBuildHoldsLeaseUntilFinished: the lease is taken before the app is
// marked building and released after the marker is cleared.
func TestBeginBuildHoldsLeaseUntilFinished(t *testing.T) {
	t.Setenv(buildlock.HolderEnv, "")
	app := &ct.App{ID: "app-1", Name: "myapp"}
	store := &lockingStore{fakeBuildingStore: fakeBuildingStore{app: app}}
	finish, err := beginBuild(store, app, map[string]string{"github": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if store.held == nil || store.held.Description != "a GitHub deploy" {
		t.Fatalf("lease = %#v, want one described as a GitHub deploy", store.held)
	}
	finish(false)
	finish(false) // BeforeExit and the defer both call it
	if got := strings.Join(store.events, ","); got != "acquire,mark,clear,release" {
		t.Fatalf("events = %s", got)
	}
}

// TestBeginBuildUsesGitreceiveLease: under a git push gitreceive already
// holds the lease and says so in the environment.
func TestBeginBuildUsesGitreceiveLease(t *testing.T) {
	t.Setenv(buildlock.HolderEnv, "gitreceive-push")
	app := &ct.App{ID: "app-1", Name: "myapp"}
	store := &lockingStore{fakeBuildingStore: fakeBuildingStore{app: app}}
	finish, err := beginBuild(store, app, nil)
	if err != nil {
		t.Fatal(err)
	}
	finish(true)
	if store.acquires != 0 {
		t.Fatalf("flynn-receiver took the lease %d times under gitreceive's", store.acquires)
	}
}

// TestBeginBuildWithoutLeaseAPI: an older controller (404) or a credential
// that may not take the lease does not stop the deploy; any other error does.
func TestBeginBuildWithoutLeaseAPI(t *testing.T) {
	t.Setenv(buildlock.HolderEnv, "")
	app := &ct.App{ID: "app-1", Name: "myapp"}
	for _, err := range []error{ct.ErrNotFound, errForbiddenForTest} {
		store := &lockingStore{fakeBuildingStore: fakeBuildingStore{app: app}, acquireErr: err}
		finish, got := beginBuild(store, app, nil)
		if got != nil {
			t.Fatalf("beginBuild with %v: %v", err, got)
		}
		finish(false)
	}
	store := &lockingStore{fakeBuildingStore: fakeBuildingStore{app: app}, acquireErr: errors.New("connection refused")}
	if _, err := beginBuild(store, app, nil); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("beginBuild with a transport error = %v", err)
	}
	if store.metaWrites != 0 {
		t.Fatal("refused build marked the app")
	}
}
