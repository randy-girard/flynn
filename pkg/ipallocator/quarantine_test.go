package ipallocator

import (
	"net"
	"testing"
	"time"
)

// quarantineNet is a /29: usable addresses .1 to .6.
func quarantineNet() *net.IPNet {
	_, n, _ := net.ParseCIDR("10.9.0.0/29")
	return n
}

func clockAt(a *IPAllocator, t time.Time) *time.Time {
	now := t
	a.now = func() time.Time { return now }
	return &now
}

// A restarted host has a fresh allocator whose round-robin cursor is at the
// bottom of the subnet. An address released moments before the restart must
// not be the first one it hands out.
func TestReleaseIPAfterHoldsAddressAcrossFreshAllocator(t *testing.T) {
	a := New()
	now := clockAt(a, time.Unix(1000, 0))
	n := quarantineNet()
	released := net.ParseIP("10.9.0.1")
	if err := a.ReleaseIPAfter(n, released, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		ip, err := a.RequestIP(n, nil)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if ip.Equal(released) {
			t.Fatalf("request %d returned %s, released less than a minute ago", i, ip)
		}
	}
}

func TestReleaseIPAfterReturnsAddressOnceDeadlinePasses(t *testing.T) {
	a := New()
	now := clockAt(a, time.Unix(1000, 0))
	n := quarantineNet()
	ip, err := a.RequestIP(n, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseIPAfter(n, ip, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RequestIP(n, ip); err != ErrIPAlreadyAllocated {
		t.Fatalf("specific request for a quarantined address: got %v, want ErrIPAlreadyAllocated", err)
	}
	*now = now.Add(time.Minute + time.Second)
	if got, err := a.RequestIP(n, ip); err != nil || !got.Equal(ip) {
		t.Fatalf("after the deadline: got %v, %v; want %s", got, err, ip)
	}
}

// Quarantine must not turn a full pool into a failed job start: when nothing
// else is free, the address whose deadline is nearest is reused.
func TestReleaseIPAfterFallsBackToEarliestDeadlineWhenPoolIsFull(t *testing.T) {
	a := New()
	now := clockAt(a, time.Unix(1000, 0))
	n := quarantineNet()
	var ips []net.IP
	for i := 0; i < 6; i++ {
		ip, err := a.RequestIP(n, nil)
		if err != nil {
			t.Fatal(err)
		}
		ips = append(ips, ip)
	}
	if err := a.ReleaseIPAfter(n, ips[1], now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseIPAfter(n, ips[4], now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := a.RequestIP(n, nil)
	if err != nil {
		t.Fatalf("full pool with quarantined addresses: %v", err)
	}
	if !got.Equal(ips[4]) {
		t.Fatalf("got %s, want %s (nearest deadline)", got, ips[4])
	}
	got, err = a.RequestIP(n, nil)
	if err != nil || !got.Equal(ips[1]) {
		t.Fatalf("got %v, %v; want %s", got, err, ips[1])
	}
	if _, err := a.RequestIP(n, nil); err != ErrNoAvailableIPs {
		t.Fatalf("pool exhausted: got %v, want ErrNoAvailableIPs", err)
	}
}

func TestReleaseIPAfterKeepsLaterDeadline(t *testing.T) {
	a := New()
	now := clockAt(a, time.Unix(1000, 0))
	n := quarantineNet()
	ip := net.ParseIP("10.9.0.3")
	a.ReleaseIPAfter(n, ip, now.Add(5*time.Minute))
	a.ReleaseIPAfter(n, ip, now.Add(time.Minute))
	*now = now.Add(2 * time.Minute)
	if _, err := a.RequestIP(n, ip); err != ErrIPAlreadyAllocated {
		t.Fatalf("earlier deadline overrode a later one: got %v", err)
	}
}

func TestReleaseIPAfterPastDeadlineFreesNow(t *testing.T) {
	a := New()
	now := clockAt(a, time.Unix(1000, 0))
	n := quarantineNet()
	ip, _ := a.RequestIP(n, nil)
	a.ReleaseIPAfter(n, ip, now.Add(-time.Second))
	if got, err := a.RequestIP(n, ip); err != nil || !got.Equal(ip) {
		t.Fatalf("got %v, %v; want %s", got, err, ip)
	}
}

func TestReleaseIPClearsQuarantine(t *testing.T) {
	a := New()
	now := clockAt(a, time.Unix(1000, 0))
	n := quarantineNet()
	ip := net.ParseIP("10.9.0.2")
	a.ReleaseIPAfter(n, ip, now.Add(time.Minute))
	a.ReleaseIP(n, ip)
	if got, err := a.RequestIP(n, ip); err != nil || !got.Equal(ip) {
		t.Fatalf("got %v, %v; want %s", got, err, ip)
	}
}
