package buildlock

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// memStore is the controller's lease in memory: one live holder per app,
// renewable by that holder, free after expiry or release.
type memStore struct {
	mu       sync.Mutex
	locks    map[string]*ct.BuildLock
	renewals int
	releases int
	err      error
}

func newMemStore() *memStore { return &memStore{locks: map[string]*ct.BuildLock{}} }

func (m *memStore) AcquireBuildLock(appID string, req *ct.BuildLock) (*ct.BuildLock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	now := time.Now()
	exp := now.Add(time.Duration(req.TTLSeconds) * time.Second)
	if cur, ok := m.locks[appID]; ok && cur.ExpiresAt.After(now) {
		if cur.Holder != req.Holder {
			cp := *cur
			return nil, &ct.BuildLockHeldError{Lock: &cp}
		}
		m.renewals++
		cur.ExpiresAt = &exp
		cp := *cur
		return &cp, nil
	}
	l := &ct.BuildLock{AppID: appID, Holder: req.Holder, Description: req.Description, AcquiredAt: &now, ExpiresAt: &exp}
	m.locks[appID] = l
	cp := *l
	return &cp, nil
}

func (m *memStore) ReleaseBuildLock(appID, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases++
	if cur, ok := m.locks[appID]; ok && cur.Holder == holder {
		delete(m.locks, appID)
	}
	return nil
}

func TestAcquireRefusesSecondHolder(t *testing.T) {
	store := newMemStore()
	first, err := Acquire(store, "app-1", "a git push", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if first.Holder() == "" {
		t.Fatal("empty holder")
	}
	_, err = Acquire(store, "app-1", "a GitHub deploy", time.Minute)
	var held *ct.BuildLockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("second Acquire err = %v, want BuildLockHeldError", err)
	}
	if held.Lock.Description != "a git push" || !strings.Contains(held.Error(), "a git push") {
		t.Fatalf("held error does not name the first holder: %v", held)
	}
	other, err := Acquire(store, "app-2", "a git push", time.Minute)
	if err != nil {
		t.Fatalf("another app was blocked: %v", err)
	}
	other.Release()
}

func TestReleaseFreesTheApp(t *testing.T) {
	store := newMemStore()
	first, err := Acquire(store, "app-1", "a git push", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	first.Release() // twice is harmless
	second, err := Acquire(store, "app-1", "a git push", time.Minute)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	second.Release()
}

// TestLeaseRenewsWhileHeld: a build longer than the TTL keeps its lease, so
// a second push is still refused after the first TTL would have run out.
func TestLeaseRenewsWhileHeld(t *testing.T) {
	store := newMemStore()
	lease, err := Acquire(store, "app-1", "a git push", 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := Acquire(store, "app-1", "a git push", time.Second); err == nil {
		t.Fatal("lease expired while its holder was alive")
	}
	lease.Release()
	store.mu.Lock()
	renewals := store.renewals
	store.mu.Unlock()
	if renewals == 0 {
		t.Fatal("lease was never renewed")
	}
	// no renewals after release
	time.Sleep(700 * time.Millisecond)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.renewals != renewals {
		t.Fatalf("renewed %d times after Release", store.renewals-renewals)
	}
}

// TestAcquireUnsupported: a controller from before the lease API answers 404.
// That is ErrUnsupported so a rolling update does not refuse every push.
func TestAcquireUnsupported(t *testing.T) {
	for _, err := range []error{
		ct.ErrNotFound,
		httphelper.JSONError{Code: httphelper.NotFoundErrorCode, Message: "not found"},
	} {
		store := newMemStore()
		store.err = err
		if _, got := Acquire(store, "app-1", "a git push", time.Minute); !errors.Is(got, ErrUnsupported) {
			t.Fatalf("Acquire with %v = %v, want ErrUnsupported", err, got)
		}
	}
	store := newMemStore()
	store.err = errors.New("connection refused")
	if _, got := Acquire(store, "app-1", "a git push", time.Minute); got == nil || errors.Is(got, ErrUnsupported) {
		t.Fatalf("transport error = %v, want a plain error", got)
	}
}

func TestNilLeaseIsSafe(t *testing.T) {
	var l *Lease
	l.Release()
	if l.Holder() != "" {
		t.Fatal("nil lease has a holder")
	}
}

// keyStore is the controller's keyed leases in memory (EDGE-40).
type keyStore struct {
	m        *memStore
	bareHeld bool
}

func (k *keyStore) AcquireKeyedBuildLock(key string, req *ct.BuildLock) (*ct.BuildLock, error) {
	got, err := k.m.AcquireBuildLock(key, req)
	var held *ct.BuildLockHeldError
	if errors.As(err, &held) && k.bareHeld {
		return nil, &ct.BuildLockHeldError{}
	}
	if got != nil {
		got.Key, got.AppID = key, ""
	}
	return got, err
}

func (k *keyStore) ReleaseKeyedBuildLock(key, holder string) error {
	return k.m.ReleaseBuildLock(key, holder)
}

// TestKeyedLeaseNamesThePipeline: a pipeline's lease refuses a second holder
// with a message about the pipeline, even when the controller's 409 had no
// lease detail; an old controller's 404 is ErrUnsupported.
func TestKeyedLeaseNamesThePipeline(t *testing.T) {
	if Keyed(nil) != nil {
		t.Fatal("Keyed(nil) must be a nil Store")
	}
	for _, bare := range []bool{false, true} {
		ks := &keyStore{m: newMemStore(), bareHeld: bare}
		key := ct.PipelineBuildLockKey("shop")
		first, err := Acquire(Keyed(ks), key, "a git push", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Acquire(Keyed(ks), key, "a git push", time.Minute)
		var held *ct.BuildLockHeldError
		if !errors.As(err, &held) {
			t.Fatalf("second Acquire err = %v, want BuildLockHeldError", err)
		}
		if !strings.Contains(held.Error(), "to this pipeline is in progress") {
			t.Fatalf("bare=%v: held error does not name the pipeline: %v", bare, held)
		}
		first.Release()
		if _, ok := ks.m.locks[key]; ok {
			t.Fatal("Release left the keyed lease")
		}
	}
	ks := &keyStore{m: newMemStore()}
	ks.m.err = ct.ErrNotFound
	if _, err := Acquire(Keyed(ks), "pipeline:shop", "a git push", time.Minute); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("old controller = %v, want ErrUnsupported", err)
	}
}
