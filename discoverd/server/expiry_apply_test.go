package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/keepalive"
)

// Expiry apply must delete the instance and the leader stamp. A late stamp
// that landed after the scan still loses, and the key must not stay in the
// clock for the life of the process.
func TestExpireApplyDropsStamp(t *testing.T) {
	s := openLeaderStore(t)

	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}
	inst := &discoverd.Instance{ID: "inst0", Addr: "10.0.0.1:80", Proto: "tcp"}
	if err := s.AddInstance("service0", inst); err != nil {
		t.Fatal(err)
	}
	key := instanceKey{service: "service0", instanceID: "inst0"}
	s.heartbeats.stamp(key, time.Now())

	cmd, err := json.Marshal(&expireInstancesCommand{Instances: []expireInstance{{
		Service:    "service0",
		InstanceID: "inst0",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err = s.applyExpireInstancesCommand(cmd)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if s.heartbeats.has(key) {
		t.Fatal("expiry apply left the heartbeat stamp in place")
	}
	got, err := s.Instances("service0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("instance still registered: %+v", got)
	}
}

func TestUnchangedAddInstanceDoesNotRaft(t *testing.T) {
	s := openLeaderStore(t)
	if err := s.AddService("web", nil); err != nil {
		t.Fatal(err)
	}
	inst := &discoverd.Instance{ID: "live", Addr: "10.0.0.2:80", Proto: "tcp", Index: 99}
	if err := s.AddInstance("web", inst); err != nil {
		t.Fatal(err)
	}
	before := s.LastIndex()
	inst.Index = 1
	if err := s.AddInstance("web", inst); err != nil {
		t.Fatal(err)
	}
	if s.LastIndex() != before {
		t.Fatalf("unchanged heartbeat wrote raft: %d -> %d", before, s.LastIndex())
	}
	inst.Meta = map[string]string{"k": "v"}
	if err := s.AddInstance("web", inst); err != nil {
		t.Fatal(err)
	}
	if s.LastIndex() == before {
		t.Fatal("meta change must raft")
	}
	got, err := s.Instances("web")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Meta["k"] != "v" || got[0].Index == 0 {
		t.Fatalf("instance %+v", got)
	}
}

func TestHeartbeatBatchDoesNotRaftOrCreate(t *testing.T) {
	s := openLeaderStore(t)
	if err := s.AddService("web", nil); err != nil {
		t.Fatal(err)
	}
	live := &discoverd.Instance{ID: "live", Addr: "10.0.0.2:80", Proto: "tcp", Meta: map[string]string{"FLYNN_APP_NAME": "web"}}
	if err := s.AddInstance("web", live); err != nil {
		t.Fatal(err)
	}
	before := s.LastIndex()
	res, err := s.HeartbeatBatch([]discoverd.HeartbeatStamp{
		{Service: "web", ID: "live"},
		{Service: "web", ID: "live"},
		{Service: "web", ID: "missing"},
		{Service: "other", ID: "live"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.LastIndex() != before {
		t.Fatalf("batch wrote raft: before %d after %d", before, s.LastIndex())
	}
	if len(res.Missing) != 2 {
		t.Fatalf("missing %+v", res.Missing)
	}
	got, err := s.Instances("web")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Meta["FLYNN_APP_NAME"] != "web" {
		t.Fatalf("instances %+v", got)
	}
	if _, err := s.Instances("other"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiryWaitsTwoTTLThenExpiresUnstamped(t *testing.T) {
	clock := newFrozenClock(time.Unix(1_700_000_000, 0).UTC())
	s := openLeaderStoreAt(t, clock.Now)
	s.InstanceTTL = 10 * time.Second
	if err := s.AddService("web", nil); err != nil {
		t.Fatal(err)
	}
	for _, inst := range []*discoverd.Instance{
		{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"},
		{ID: "b", Addr: "10.0.0.1:2", Proto: "tcp"},
		{ID: "c", Addr: "10.0.0.1:3", Proto: "tcp"},
	} {
		if err := s.AddInstance("web", inst); err != nil {
			t.Fatal(err)
		}
	}

	clock.Advance(s.InstanceTTL + time.Second)
	if err := s.EnforceExpiry(); err != ErrLeaderWait {
		t.Fatalf("empty-clock wait: %v", err)
	}
	if ids := instanceIDs(t, s, "web"); len(ids) != 3 {
		t.Fatalf("expired during leader wait: %v", ids)
	}

	clock.Advance(s.InstanceTTL)
	// A single-node raft can step down and back up while this runs. That
	// clears the clock and restarts the leader wait. Pin the wait and the
	// stamps immediately before the scan, and retry if leadership moved.
	expire := func(stale, fresh []string) {
		t.Helper()
		var last error
		for attempt := 0; attempt < 6; attempt++ {
			now := clock.Now()
			s.mu.Lock()
			s.leaderTime = now.Add(-3 * s.InstanceTTL)
			s.mu.Unlock()
			for _, id := range fresh {
				s.heartbeats.stamp(instanceKey{service: "web", instanceID: id}, now)
			}
			for _, id := range stale {
				s.heartbeats.stamp(instanceKey{service: "web", instanceID: id}, now.Add(-s.InstanceTTL-time.Second))
			}
			before := s.LastIndex()
			err := s.EnforceExpiry()
			if err == ErrLeaderWait || err == raft.ErrNotLeader {
				last = err
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			ids := instanceIDs(t, s, "web")
			if s.LastIndex() == before {
				kept := false
				for _, id := range stale {
					if ids[id] {
						kept = true
					}
				}
				if kept {
					last = fmt.Errorf("stale stamps were refreshed before the scan")
					continue
				}
				t.Fatalf("instances %v disappeared without a raft write (index %d)", stale, before)
			}
			if s.LastIndex() != before+1 {
				t.Fatalf("expiry raft index %d -> %d", before, s.LastIndex())
			}
			for _, id := range stale {
				if ids[id] {
					t.Fatalf("instance %s still registered after expiry: %v", id, ids)
				}
			}
			return
		}
		t.Fatal(last)
	}
	s.mu.Lock()
	s.needFullSweep = true
	s.mu.Unlock()
	expire([]string{"c"}, []string{"a", "b"})
	if ids := instanceIDs(t, s, "web"); len(ids) != 2 || !ids["a"] || !ids["b"] {
		t.Fatalf("after first sweep: %v", ids)
	}

	clock.Advance(s.InstanceTTL + time.Second)
	s.mu.Lock()
	s.needFullSweep = false
	s.mu.Unlock()
	expire([]string{"a", "b"}, nil)
	if ids := instanceIDs(t, s, "web"); len(ids) != 0 {
		t.Fatalf("heap path kept instances: %v", ids)
	}
}

func TestRestoreOnLeaderRestartsExpiryWait(t *testing.T) {
	clock := newFrozenClock(time.Unix(1_700_000_000, 0).UTC())
	s := openLeaderStoreAt(t, clock.Now)
	s.InstanceTTL = 10 * time.Second
	if err := s.AddService("web", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddInstance("web", &discoverd.Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * s.InstanceTTL)

	s.mu.Lock()
	buf, err := json.Marshal(s.data)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(io.NopCloser(bytes.NewReader(buf))); err != nil {
		t.Fatal(err)
	}
	clock.Advance(s.InstanceTTL + time.Second)
	if err := s.EnforceExpiry(); err != ErrLeaderWait {
		t.Fatalf("restore must restart leader wait, got %v", err)
	}
	if ids := instanceIDs(t, s, "web"); len(ids) != 1 {
		t.Fatalf("restore expired the instance: %v", ids)
	}
}

func TestRemoveServiceStillExpiresInstances(t *testing.T) {
	clock := newFrozenClock(time.Unix(1_700_000_000, 0).UTC())
	s := openLeaderStoreAt(t, clock.Now)
	s.InstanceTTL = 10 * time.Second
	if err := s.AddService("web", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddInstance("web", &discoverd.Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	key := instanceKey{service: "web", instanceID: "a"}
	if !s.heartbeats.has(key) {
		t.Fatal("expected a stamp after add")
	}
	clock.Advance(2*s.InstanceTTL + time.Second)
	if err := s.RemoveService("web"); err != nil {
		t.Fatal(err)
	}
	// The remove command leaves the instance record. Expiry still drops it,
	// and that apply drops the stamp.
	if err := s.EnforceExpiry(); err != nil {
		t.Fatal(err)
	}
	if s.heartbeats.has(key) {
		t.Fatal("expiry left the stamp after service remove")
	}
	if ids := instanceIDs(t, s, "web"); len(ids) != 0 {
		t.Fatalf("expiry left instances: %v", ids)
	}
}

func TestRemoveInstanceDropsHeartbeatStamp(t *testing.T) {
	s := openLeaderStore(t)
	if err := s.AddService("web", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddInstance("web", &discoverd.Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	key := instanceKey{service: "web", instanceID: "a"}
	if err := s.RemoveInstance("web", "a"); err != nil {
		t.Fatal(err)
	}
	if s.heartbeats.has(key) {
		t.Fatal("instance remove left the stamp")
	}
}

type frozenClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFrozenClock(t time.Time) *frozenClock {
	return &frozenClock{t: t}
}

func (c *frozenClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *frozenClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func instanceIDs(t *testing.T, s *Store, service string) map[string]bool {
	t.Helper()
	got, err := s.Instances(service)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(got))
	for _, inst := range got {
		ids[inst.ID] = true
	}
	return ids
}

func openLeaderStore(t *testing.T) *Store {
	t.Helper()
	return openLeaderStoreAt(t, nil)
}

func openLeaderStoreAt(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	ln, err := keepalive.ReusableListen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	s := NewStore(t.TempDir())
	s.Listener = ln
	s.Advertise, _ = net.ResolveTCPAddr("tcp", net.JoinHostPort("127.0.0.1", port))
	s.HeartbeatTimeout = 50 * time.Millisecond
	s.ElectionTimeout = 50 * time.Millisecond
	s.LeaderLeaseTimeout = 50 * time.Millisecond
	s.CommitTimeout = 5 * time.Millisecond
	s.EnableSingleNode = true
	s.ExpiryCheckInterval = time.Hour
	s.LogOutput = io.Discard
	if now != nil {
		s.Now = now
	}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	select {
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for leadership")
	case <-s.LeaderCh():
	}
	return s
}
