package server

import (
	"fmt"
	"testing"
	"time"
)

func TestHeartbeatClockExpiresOnlyTheOldest(t *testing.T) {
	c := newHeartbeatClock()
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < 100; i++ {
		c.stamp(instanceKey{service: "web", instanceID: fmt.Sprintf("i%d", i)}, base)
	}
	if got := c.expired(base.Add(10*time.Second), 10*time.Second); len(got) != 0 {
		t.Fatalf("fresh stamps expired: %d", len(got))
	}

	old := instanceKey{service: "web", instanceID: "old"}
	c.stamp(old, base.Add(-11*time.Second))
	got := c.expired(base, 10*time.Second)
	if len(got) != 1 || got[0] != old {
		t.Fatalf("expired %v", got)
	}
	again := c.expired(base, 10*time.Second)
	if len(again) != 1 || again[0] != old {
		t.Fatalf("expired dropped the key before apply: %v", again)
	}

	c.stamp(old, base)
	if got := c.expired(base, 10*time.Second); len(got) != 0 {
		t.Fatalf("restamp still expired: %v", got)
	}
}

func TestHeartbeatClockRemoveService(t *testing.T) {
	c := newHeartbeatClock()
	now := time.Unix(1_700_000_000, 0).UTC()
	c.stamp(instanceKey{service: "web", instanceID: "a"}, now)
	c.stamp(instanceKey{service: "web", instanceID: "b"}, now)
	c.stamp(instanceKey{service: "other", instanceID: "a"}, now)
	c.removeService("web")
	if c.has(instanceKey{service: "web", instanceID: "a"}) || c.has(instanceKey{service: "web", instanceID: "b"}) {
		t.Fatal("service remove left web stamps")
	}
	if !c.has(instanceKey{service: "other", instanceID: "a"}) {
		t.Fatal("service remove cleared another service")
	}
}
