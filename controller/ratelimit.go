package main

import (
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/status"
)

const (
	// Per web process. Scaling controller web multiplies these ceilings.
	// The counter stays in memory: a Postgres write on every request is the
	// load this limit exists to shed. Hosted and self-hosted use the same
	// numbers. tenancy_mode is not consulted.
	defaultUserRateLimit    = 600
	defaultClusterRateLimit = 100000
	defaultRateLimitWindow  = time.Minute
)

// logRateLimit records one rejection. Callers invoke it only on the first
// reject of a window. The arguments are the bucket class, the user id, and
// the bucket key. None of them is a bearer token or the install key.
var logRateLimit = func(class, userID, key string) {
	logger.Warn("controller request rate limited", "bucket", class, "user_id", userID, "limit_key", key)
}

type accountLimiter struct {
	mu           sync.Mutex
	userLimit    int
	clusterLimit int
	window       time.Duration
	now          func() time.Time
	keys         map[string]*limitBucket
	lastSweep    time.Time
}

type limitBucket struct {
	start  time.Time
	count  int
	logged bool
}

type limitDecision struct {
	allowed    bool
	retryAfter time.Duration
	class      string
	userID     string
	log        bool
	key        string
}

func limiterFromEnv() *accountLimiter {
	return newAccountLimiter(
		parseRateLimit(os.Getenv("CONTROLLER_RATE_LIMIT_USER"), defaultUserRateLimit),
		parseRateLimit(os.Getenv("CONTROLLER_RATE_LIMIT_CLUSTER"), defaultClusterRateLimit),
		parseWindow(os.Getenv("CONTROLLER_RATE_LIMIT_WINDOW"), defaultRateLimitWindow),
	)
}

func newAccountLimiter(userLimit, clusterLimit int, window time.Duration) *accountLimiter {
	if userLimit <= 0 {
		userLimit = defaultUserRateLimit
	}
	if clusterLimit <= 0 {
		clusterLimit = defaultClusterRateLimit
	}
	if window <= 0 {
		window = defaultRateLimitWindow
	}
	return &accountLimiter{
		userLimit:    userLimit,
		clusterLimit: clusterLimit,
		window:       window,
		now:          time.Now,
		keys:         make(map[string]*limitBucket),
	}
}

// parseRateLimit returns def for an empty, invalid, or non-positive value.
// There is no value that disables the limit.
func parseRateLimit(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func parseWindow(raw string, def time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// accountLimitKey is the fixed-window bucket. JWTs and PATs share UserID.
// Every install key shares one cluster bucket. A token with no user id is
// bucketed by its app grants, not by Token.ID, because a reminted JWT would
// otherwise get a fresh budget.
func accountLimitKey(tok *authorizer.Token) (string, bool) {
	if tok == nil {
		return "nogrant", false
	}
	if tok.ClusterKey {
		return "cluster", true
	}
	if tok.UserID != "" {
		return "user:" + tok.UserID, false
	}
	if ids := grantAppIDs(tok); len(ids) > 0 {
		return "apps:" + strings.Join(ids, ","), false
	}
	return "nogrant", false
}

func grantAppIDs(tok *authorizer.Token) []string {
	seen := make(map[string]struct{}, len(tok.AppGrants))
	ids := make([]string, 0, len(tok.AppGrants))
	for _, g := range tok.AppGrants {
		id := strings.TrimSpace(g.AppID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// allow counts one request. A rejection does not increment, so a client that
// retries inside the window cannot push the next window back. A nil limiter
// allows the request.
func (l *accountLimiter) allow(tok *authorizer.Token) limitDecision {
	if l == nil {
		return limitDecision{allowed: true}
	}
	key, cluster := accountLimitKey(tok)
	limit := l.userLimit
	class := "user"
	userID := ""
	if cluster {
		limit = l.clusterLimit
		class = "cluster"
	} else if tok != nil {
		userID = tok.UserID
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	b := l.keys[key]
	if b == nil || now.Sub(b.start) >= l.window {
		b = &limitBucket{start: now}
		l.keys[key] = b
	}
	if b.count >= limit {
		retry := l.window - now.Sub(b.start)
		if retry < time.Second {
			retry = time.Second
		}
		first := !b.logged
		b.logged = true
		return limitDecision{
			allowed:    false,
			retryAfter: retry,
			class:      class,
			userID:     userID,
			log:        first,
			key:        key,
		}
	}
	b.count++
	return limitDecision{allowed: true, class: class, userID: userID, key: key}
}

func (l *accountLimiter) sweepLocked(now time.Time) {
	if l.lastSweep.IsZero() {
		l.lastSweep = now
		return
	}
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for key, b := range l.keys {
		if now.Sub(b.start) >= 2*l.window {
			delete(l.keys, key)
		}
	}
}

// clusterKeyStatusExempt is the status app's probe. It uses the cluster key.
// A user or PAT calling the same path still consumes the user bucket.
func clusterKeyStatusExempt(r *http.Request, tok *authorizer.Token) bool {
	if r == nil || r.URL == nil || tok == nil || !tok.ClusterKey {
		return false
	}
	if r.URL.Path != status.Path {
		return false
	}
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	secs := int(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		return 1
	}
	return secs
}

func retryAfterHeader(d time.Duration) string {
	return strconv.Itoa(retryAfterSeconds(d))
}

// writeRateLimited sends 429. retry is false because Client.send treats
// retry:true as "try again every 100ms for 10 seconds".
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", retryAfterHeader(retryAfter))
	httphelper.Error(w, httphelper.JSONError{
		Code:    httphelper.RatelimitedErrorCode,
		Message: "too many requests, try again later",
		Retry:   false,
	})
}
