package main

import (
	"testing"
	"time"

	"github.com/randy-girard/flynn/controller/tenancy"
)

func TestParsePATLifetime(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":       0,
		"  ":     0,
		"90d":    90 * 24 * time.Hour,
		"1d":     24 * time.Hour,
		"720h":   720 * time.Hour,
		"30m":    30 * time.Minute,
		"0":      patLifetimeNone,
		"none":   patLifetimeNone,
		" NONE ": patLifetimeNone,
	} {
		got, err := parsePATLifetime(raw)
		if err != nil {
			t.Fatalf("parsePATLifetime(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("parsePATLifetime(%q) = %v, want %v", raw, got, want)
		}
	}
	for _, raw := range []string{"-1d", "-5h", "abc", "1x", "d", "0s5"} {
		if _, err := parsePATLifetime(raw); err == nil {
			t.Fatalf("parsePATLifetime(%q) accepted a bad value", raw)
		}
	}
}

func TestPATLifetimeFromEnvFallsBackOnBadValue(t *testing.T) {
	t.Setenv("PAT_MAX_LIFETIME", "forever")
	t.Setenv("PAT_DEFAULT_LIFETIME", "30d")
	p := patLifetimeFromEnv()
	if p.Max != 0 || p.Default != 30*24*time.Hour {
		t.Fatalf("policy = %+v, want built-in max and a 30d default", p)
	}
}

// The zero policy is the shipped default: 90 days when no expiry is asked
// for and at most 365 days. Hosted mode never allows "none".
func TestPATLifetimeResolve(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }

	type tc struct {
		name    string
		policy  patLifetimePolicy
		mode    string
		req     *time.Time
		want    *time.Time
		wantErr bool
	}
	for _, c := range []tc{
		{name: "default applied", mode: tenancy.ModeSelfHosted, want: at(90 * day)},
		{name: "hosted default applied", mode: tenancy.ModeHosted, want: at(90 * day)},
		{name: "explicit within max kept", mode: tenancy.ModeSelfHosted, req: at(200 * day), want: at(200 * day)},
		{name: "explicit at max kept", mode: tenancy.ModeSelfHosted, req: at(365 * day), want: at(365 * day)},
		{name: "explicit past max refused", mode: tenancy.ModeSelfHosted, req: at(366 * day), wantErr: true},
		{name: "past expiry refused", mode: tenancy.ModeSelfHosted, req: at(-time.Minute), wantErr: true},
		{name: "now refused", mode: tenancy.ModeSelfHosted, req: at(0), wantErr: true},
		{name: "custom max", policy: patLifetimePolicy{Max: 30 * day}, mode: tenancy.ModeSelfHosted, req: at(31 * day), wantErr: true},
		{name: "default larger than max is capped", policy: patLifetimePolicy{Max: 30 * day, Default: 60 * day}, mode: tenancy.ModeSelfHosted, want: at(30 * day)},
		{name: "default none falls to max", policy: patLifetimePolicy{Max: 30 * day, Default: patLifetimeNone}, mode: tenancy.ModeSelfHosted, want: at(30 * day)},
		{name: "self-hosted opt out", policy: patLifetimePolicy{Max: patLifetimeNone, Default: patLifetimeNone}, mode: tenancy.ModeSelfHosted, want: nil},
		{name: "self-hosted no max keeps far expiry", policy: patLifetimePolicy{Max: patLifetimeNone}, mode: tenancy.ModeSelfHosted, req: at(3650 * day), want: at(3650 * day)},
		{name: "self-hosted no max still defaults", policy: patLifetimePolicy{Max: patLifetimeNone}, mode: tenancy.ModeSelfHosted, want: at(90 * day)},
		{name: "hosted ignores none max", policy: patLifetimePolicy{Max: patLifetimeNone, Default: patLifetimeNone}, mode: tenancy.ModeHosted, want: at(365 * day)},
		{name: "hosted refuses past built-in max", policy: patLifetimePolicy{Max: patLifetimeNone}, mode: tenancy.ModeHosted, req: at(366 * day), wantErr: true},
	} {
		got, err := c.policy.resolve(now, c.mode, c.req)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an error, got %v", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		switch {
		case c.want == nil && got != nil:
			t.Fatalf("%s: got %v, want no expiry", c.name, got)
		case c.want != nil && (got == nil || !got.Equal(*c.want)):
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
