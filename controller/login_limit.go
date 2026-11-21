package main

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Password sign-in (POST /oauth/token grant_type=password and the form on
// POST /oauth/authorize) is throttled by three token buckets. Each attempt
// takes one token from every bucket that applies, before the password is
// checked; a successful sign-in gives its tokens back, so only failures
// count. An empty bucket answers 429 with Retry-After. Buckets refill at a
// steady rate, so the result is a backoff, never a lock that an attacker
// could hold against the owner.
//
//   - account+address: the owner mistyping, or one address guessing one
//     account. Small burst, quick refill.
//   - account: many addresses guessing one account. An address that signed
//     in to the account in the last trustTTL is exempt, so a distributed
//     attack does not lock the owner out from where they usually sign in.
//   - address: one address trying many accounts. Generous enough for many
//     users behind one NAT; CONTROLLER_LOGIN_IP_BURST raises it.
//
// State is in memory per controller process (like the request limiter), and
// bounded by maxEntries.
type loginLimits struct {
	pairBurst    int
	pairEvery    time.Duration
	accountBurst int
	accountEvery time.Duration
	ipBurst      int
	ipEvery      time.Duration
	trustTTL     time.Duration
	maxTrustIPs  int
	maxEntries   int
}

func defaultLoginLimits() loginLimits {
	return loginLimits{
		pairBurst:    5,
		pairEvery:    time.Minute,
		accountBurst: 20,
		accountEvery: 2 * time.Minute,
		ipBurst:      30,
		ipEvery:      20 * time.Second,
		trustTTL:     24 * time.Hour,
		maxTrustIPs:  8,
		maxEntries:   100000,
	}
}

func loginLimitsFromEnv() loginLimits {
	l := defaultLoginLimits()
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CONTROLLER_LOGIN_IP_BURST"))); err == nil && n > 0 {
		l.ipBurst = n
	}
	return l
}

type loginBucket struct {
	tokens float64
	last   time.Time
	burst  int
	every  time.Duration
}

func (b *loginBucket) refill(now time.Time) {
	if el := now.Sub(b.last); el > 0 {
		b.tokens += float64(el) / float64(b.every)
		if b.tokens > float64(b.burst) {
			b.tokens = float64(b.burst)
		}
	}
	b.last = now
}

func (b *loginBucket) wait() time.Duration {
	return time.Duration((1 - b.tokens) * float64(b.every))
}

type loginLimiter struct {
	mu        sync.Mutex
	limits    loginLimits
	now       func() time.Time
	buckets   map[string]*loginBucket
	trusted   map[string]map[string]time.Time // account -> address -> expiry
	lastSweep time.Time
}

func newLoginLimiter(l loginLimits) *loginLimiter {
	return &loginLimiter{
		limits:  l,
		now:     time.Now,
		buckets: make(map[string]*loginBucket),
		trusted: make(map[string]map[string]time.Time),
	}
}

// loginReservation is one attempt's tokens. Call success or failure once.
type loginReservation struct {
	l       *loginLimiter
	account string
	ip      string
	keys    []string
}

func normalizeLoginAccount(identifier string) string {
	return strings.ToLower(strings.TrimSpace(identifier))
}

// reserve takes a token from every bucket that applies, or none if any is
// empty; then it returns how long until the emptiest one has a token. A nil
// limiter allows everything.
func (l *loginLimiter) reserve(identifier, ip string) (loginReservation, time.Duration, bool) {
	if l == nil {
		return loginReservation{}, 0, true
	}
	account := normalizeLoginAccount(identifier)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)

	type want struct {
		key   string
		burst int
		every time.Duration
	}
	var ws []want
	if account != "" {
		ws = append(ws, want{"p\x00" + account + "\x00" + ip, l.limits.pairBurst, l.limits.pairEvery})
		if !l.trustedLocked(account, ip, now) {
			ws = append(ws, want{"a\x00" + account, l.limits.accountBurst, l.limits.accountEvery})
		}
	}
	if ip != "" {
		ws = append(ws, want{"i\x00" + ip, l.limits.ipBurst, l.limits.ipEvery})
	}

	var retry time.Duration
	bs := make([]*loginBucket, len(ws))
	for i, w := range ws {
		b := l.buckets[w.key]
		if b == nil {
			b = &loginBucket{tokens: float64(w.burst), last: now, burst: w.burst, every: w.every}
		} else {
			b.refill(now)
		}
		bs[i] = b
		if b.tokens < 1 {
			if d := b.wait(); d > retry {
				retry = d
			}
		}
	}
	if retry > 0 {
		if retry < time.Second {
			retry = time.Second
		}
		return loginReservation{}, retry, false
	}
	r := loginReservation{l: l, account: account, ip: ip}
	for i, w := range ws {
		bs[i].tokens--
		if l.buckets[w.key] == nil {
			l.makeRoomLocked()
			l.buckets[w.key] = bs[i]
		}
		r.keys = append(r.keys, w.key)
	}
	return r, 0, true
}

// success gives the tokens back and trusts the address for the account.
func (r loginReservation) success() {
	l := r.l
	if l == nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range r.keys {
		if b := l.buckets[k]; b != nil {
			b.refill(now)
			b.tokens++
			if b.tokens > float64(b.burst) {
				b.tokens = float64(b.burst)
			}
		}
	}
	if r.account == "" || r.ip == "" {
		return
	}
	ips := l.trusted[r.account]
	if ips == nil {
		if len(l.trusted) >= l.limits.maxEntries {
			for k := range l.trusted {
				delete(l.trusted, k)
				break
			}
		}
		ips = make(map[string]time.Time)
		l.trusted[r.account] = ips
	}
	if _, ok := ips[r.ip]; !ok && len(ips) >= l.limits.maxTrustIPs {
		oldest, at := "", time.Time{}
		for ip, exp := range ips {
			if oldest == "" || exp.Before(at) {
				oldest, at = ip, exp
			}
		}
		delete(ips, oldest)
	}
	ips[r.ip] = now.Add(l.limits.trustTTL)
}

// failure keeps the tokens taken by reserve.
func (r loginReservation) failure() {}

func (l *loginLimiter) trustedLocked(account, ip string, now time.Time) bool {
	if ip == "" {
		return false
	}
	exp, ok := l.trusted[account][ip]
	return ok && now.Before(exp)
}

// makeRoomLocked keeps the bucket table at maxEntries. Full buckets carry no
// state and go first (sweep); past that an arbitrary bucket is dropped,
// which only ever forgives failures, never blocks anyone.
func (l *loginLimiter) makeRoomLocked() {
	if len(l.buckets) < l.limits.maxEntries {
		return
	}
	l.sweepAllLocked(l.now())
	for k := range l.buckets {
		if len(l.buckets) < l.limits.maxEntries {
			return
		}
		delete(l.buckets, k)
	}
}

func (l *loginLimiter) sweepLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.sweepAllLocked(now)
}

func (l *loginLimiter) sweepAllLocked(now time.Time) {
	l.lastSweep = now
	for k, b := range l.buckets {
		b.refill(now)
		if b.tokens >= float64(b.burst) {
			delete(l.buckets, k)
		}
	}
	for acct, ips := range l.trusted {
		for ip, exp := range ips {
			if !now.Before(exp) {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(l.trusted, acct)
		}
	}
}

// loginClientIP is the address a sign-in is counted against. The router
// appends its peer to X-Forwarded-For, so only the last entry is used (the
// same rule as the request log). A caller that presents the cluster key (the
// dashboard signing a browser user in) may forward the user's address the
// same way; when it does not, the result is "" and only the account buckets
// apply, so one service address never throttles every user behind it.
func loginClientIP(r *http.Request, serviceCaller bool) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		last := xff
		if i := strings.LastIndexByte(xff, ','); i >= 0 {
			last = xff[i+1:]
		}
		if ip := net.ParseIP(strings.TrimSpace(last)); ip != nil {
			return ip.String()
		}
	}
	if serviceCaller {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil {
		return ip.String()
	}
	return strings.TrimSpace(host)
}

// loginServiceCaller reports whether the sign-in request carries the cluster
// key (HTTP Basic password), as the dashboard's password grant does.
func (c *controllerAPI) loginServiceCaller(r *http.Request) bool {
	if c == nil || c.authorizer == nil {
		return false
	}
	_, pw, ok := r.BasicAuth()
	if !ok || pw == "" {
		return false
	}
	tok, err := c.authorizer.AuthorizeKey(pw)
	return err == nil && tok != nil && tok.ClusterKey
}

// reserveLogin applies the sign-in limiter to req for identifier.
func (c *controllerAPI) reserveLogin(req *http.Request, identifier string) (loginReservation, time.Duration, bool) {
	return c.loginLimiter.reserve(identifier, loginClientIP(req, c.loginServiceCaller(req)))
}

// credentialRevokeSkew lets a login token signed up to this long before a
// password change through, for a token minted on another host (the
// dashboard) whose clock is slightly behind.
var credentialRevokeSkew = time.Second
