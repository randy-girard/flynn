package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time      { return f.t }
func (f *fakeClock) add(d time.Duration) { f.t = f.t.Add(d) }
func newTestLoginLimiter(clk *fakeClock) *loginLimiter {
	l := newLoginLimiter(defaultLoginLimits())
	l.now = clk.now
	return l
}

func failN(t *testing.T, l *loginLimiter, account, ip string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		r, retry, ok := l.reserve(account, ip)
		if !ok {
			t.Fatalf("attempt %d for %s from %s refused early (retry %s)", i+1, account, ip, retry)
		}
		r.failure()
	}
}

// Failed attempts for one account from one address are throttled, the
// throttle refills with time (no hard lock), and the same account from
// another address is not affected.
func TestLoginLimiterPerAccountAndAddress(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	l := newTestLoginLimiter(clk)
	lim := defaultLoginLimits()

	failN(t, l, "owner@example.com", "198.51.100.7", lim.pairBurst)
	_, retry, ok := l.reserve("owner@example.com", "198.51.100.7")
	if ok {
		t.Fatal("attempt past the per-account-per-address burst was allowed")
	}
	if retry <= 0 || retry > lim.pairEvery {
		t.Fatalf("retry-after %s, want (0, %s]", retry, lim.pairEvery)
	}
	if r, _, ok := l.reserve("owner@example.com", "203.0.113.9"); !ok {
		t.Fatal("the owner on another address was throttled by someone else's failures")
	} else {
		r.success()
	}
	clk.add(lim.pairEvery)
	if r, _, ok := l.reserve("owner@example.com", "198.51.100.7"); !ok {
		t.Fatal("throttle did not refill: it must back off, not lock")
	} else {
		r.failure()
	}
}

// Failures for one account across many addresses hit the account bucket.
// An address that signed in to the account recently is exempt from it.
func TestLoginLimiterAccountWideAndTrustedAddress(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	l := newTestLoginLimiter(clk)
	lim := defaultLoginLimits()

	r, _, ok := l.reserve("victim@example.com", "192.0.2.1")
	if !ok {
		t.Fatal("first attempt refused")
	}
	r.success()

	for i := 0; i < lim.accountBurst; i++ {
		failN(t, l, "victim@example.com", fmt.Sprintf("10.9.%d.%d", i/200, i%200+1), 1)
	}
	if _, _, ok := l.reserve("victim@example.com", "10.200.0.1"); ok {
		t.Fatal("account-wide burst did not throttle a new address")
	}
	if r, _, ok := l.reserve("victim@example.com", "192.0.2.1"); !ok {
		t.Fatal("the address the owner signed in from was locked out by a distributed attack")
	} else {
		r.success()
	}
}

// One address spraying many accounts is throttled by the address bucket.
func TestLoginLimiterPerAddress(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	l := newTestLoginLimiter(clk)
	lim := defaultLoginLimits()
	for i := 0; i < lim.ipBurst; i++ {
		failN(t, l, fmt.Sprintf("user%d@example.com", i), "198.51.100.50", 1)
	}
	if _, _, ok := l.reserve("fresh@example.com", "198.51.100.50"); ok {
		t.Fatal("per-address burst did not throttle a spray")
	}
	if r, _, ok := l.reserve("fresh@example.com", "198.51.100.51"); !ok {
		t.Fatal("a different address was throttled")
	} else {
		r.failure()
	}
}

// A success refunds its reservation, so users behind one NAT who sign in
// correctly never use up the shared address budget.
func TestLoginLimiterSuccessDoesNotCount(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	l := newTestLoginLimiter(clk)
	lim := defaultLoginLimits()
	for i := 0; i < lim.ipBurst*3; i++ {
		r, _, ok := l.reserve(fmt.Sprintf("u%d@example.com", i), "198.51.100.60")
		if !ok {
			t.Fatalf("successful sign-in %d was throttled", i)
		}
		r.success()
	}
}

// An empty address (service caller that did not forward one) skips only
// the address bucket. The account buckets still apply.
func TestLoginLimiterNoAddress(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	l := newTestLoginLimiter(clk)
	lim := defaultLoginLimits()
	failN(t, l, "svc@example.com", "", lim.pairBurst)
	if _, _, ok := l.reserve("svc@example.com", ""); ok {
		t.Fatal("account without address was not throttled")
	}
	for i := 0; i < lim.ipBurst+5; i++ {
		failN(t, l, fmt.Sprintf("x%d@example.com", i), "", 1)
	}
}

// The tables stay bounded when an attacker rotates accounts and addresses.
func TestLoginLimiterBoundedMemory(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	lim := defaultLoginLimits()
	lim.maxEntries = 500
	l := newLoginLimiter(lim)
	l.now = clk.now
	for i := 0; i < 5000; i++ {
		r, _, ok := l.reserve(fmt.Sprintf("a%d@example.com", i), fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256))
		if ok {
			r.failure()
		}
	}
	if n := len(l.buckets); n > lim.maxEntries {
		t.Fatalf("%d buckets, want at most %d", n, lim.maxEntries)
	}
	clk.add(24 * time.Hour)
	l.reserve("late@example.com", "10.255.0.1")
	if n := len(l.buckets); n > 10 {
		t.Fatalf("%d buckets after every bucket refilled, want them swept", n)
	}
}

func TestLoginClientIP(t *testing.T) {
	for _, tc := range []struct {
		remote, xff string
		service     bool
		want        string
	}{
		{"10.0.0.5:4000", "", false, "10.0.0.5"},
		{"10.0.0.5:4000", "1.2.3.4, 203.0.113.8", false, "203.0.113.8"},
		{"10.0.0.5:4000", "", true, ""},
		{"10.0.0.5:4000", "203.0.113.9", true, "203.0.113.9"},
		{"10.0.0.5:4000", "garbage", false, "10.0.0.5"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := loginClientIP(r, tc.service); got != tc.want {
			t.Errorf("loginClientIP(%q, %q, service=%v) = %q, want %q", tc.remote, tc.xff, tc.service, got, tc.want)
		}
	}
}
