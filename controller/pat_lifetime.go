package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/tenancy"
)

// Personal access token lifetime (CORE-50). A token with no expires_at gets
// the default lifetime, and one asking for longer than the maximum is
// refused. PAT_MAX_LIFETIME and PAT_DEFAULT_LIFETIME on the controller app
// override the built-in values; "0" or "none" means no limit, which only a
// self_hosted cluster honours: a hosted cluster with no maximum uses the
// built-in one.
const (
	builtinPATMaxLifetime     = 365 * 24 * time.Hour
	builtinPATDefaultLifetime = 90 * 24 * time.Hour

	// patLifetimeNone is the parsed "0" / "none". A zero Duration means
	// "use the built-in value", so the zero policy is the safe default.
	patLifetimeNone = time.Duration(-1)

	// patExpirySlack lets a client that computes now+max on its own clock
	// still land inside the maximum.
	patExpirySlack = time.Minute
)

type patLifetimePolicy struct {
	Max     time.Duration
	Default time.Duration
}

// parsePATLifetime reads a Go duration or a whole number of days ("90d").
// Empty is 0 (built-in value); "0" and "none" are patLifetimeNone.
func parsePATLifetime(raw string) (time.Duration, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "":
		return 0, nil
	case "0", "none":
		return patLifetimeNone, nil
	}
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid lifetime %q: want a positive number of days, a Go duration, or none", raw)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid lifetime %q: want a positive number of days, a Go duration, or none", raw)
	}
	return d, nil
}

// patLifetimeFromEnv reads PAT_MAX_LIFETIME and PAT_DEFAULT_LIFETIME. A bad
// value is logged and the built-in value is used, so a typo never turns the
// limit off.
func patLifetimeFromEnv() patLifetimePolicy {
	var p patLifetimePolicy
	for _, v := range []struct {
		name string
		dst  *time.Duration
	}{{"PAT_MAX_LIFETIME", &p.Max}, {"PAT_DEFAULT_LIFETIME", &p.Default}} {
		d, err := parsePATLifetime(os.Getenv(v.name))
		if err != nil {
			log.Printf("ignoring %s: %v", v.name, err)
			continue
		}
		*v.dst = d
	}
	return p
}

// limits returns the effective maximum and default. Zero means no limit.
func (p patLifetimePolicy) limits(mode string) (max, def time.Duration) {
	max = p.Max
	if max == 0 || (max < 0 && mode == tenancy.ModeHosted) {
		max = builtinPATMaxLifetime
	}
	if max < 0 {
		max = 0
	}
	def = p.Default
	if def == 0 {
		def = builtinPATDefaultLifetime
	}
	if def < 0 {
		def = 0
	}
	if max > 0 && (def == 0 || def > max) {
		def = max
	}
	return max, def
}

// resolve returns the expiry to store for a token created at now. req is the
// caller's expires_at, or nil.
func (p patLifetimePolicy) resolve(now time.Time, mode string, req *time.Time) (*time.Time, error) {
	max, def := p.limits(mode)
	if req == nil {
		if def == 0 {
			return nil, nil
		}
		t := now.Add(def)
		return &t, nil
	}
	if !req.After(now) {
		return nil, fmt.Errorf("must be in the future")
	}
	if max > 0 && req.After(now.Add(max+patExpirySlack)) {
		return nil, fmt.Errorf("must be at most %s from now (the cluster maximum personal access token lifetime)", formatPATLifetime(max))
	}
	return req, nil
}

func formatPATLifetime(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return d.String()
}
