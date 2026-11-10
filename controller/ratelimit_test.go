package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/tokensigner"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRateLimitDefaults(t *testing.T) {
	if defaultUserRateLimit != 600 {
		t.Fatalf("user default %d, want 600/minute", defaultUserRateLimit)
	}
	if defaultClusterRateLimit != 100000 {
		t.Fatalf("cluster default %d, want 100000/minute", defaultClusterRateLimit)
	}
	if defaultRateLimitWindow != time.Minute {
		t.Fatalf("window %s, want 1m", defaultRateLimitWindow)
	}
	for _, raw := range []string{"", "0", "-3", "nope"} {
		if parseRateLimit(raw, 600) != 600 {
			t.Fatalf("parseRateLimit(%q) changed the default", raw)
		}
		if parseWindow(raw, time.Minute) != time.Minute {
			t.Fatalf("parseWindow(%q) changed the default", raw)
		}
	}
	if parseRateLimit("10", 600) != 10 {
		t.Fatal("explicit user limit")
	}
	if parseWindow("2m", time.Minute) != 2*time.Minute {
		t.Fatal("explicit window")
	}
}

func TestAccountLimitKey(t *testing.T) {
	user := &authorizer.Token{ID: "sig-1", UserID: "u1"}
	again := &authorizer.Token{ID: "sig-2", UserID: "u1"}
	pat := &authorizer.Token{ID: "u1", UserID: "u1"}
	uk, uc := accountLimitKey(user)
	ak, _ := accountLimitKey(again)
	pk, _ := accountLimitKey(pat)
	if uk != ak || uk != pk || uk != "user:u1" || uc {
		t.Fatalf("user keys %q %q %q cluster=%v", uk, ak, pk, uc)
	}
	admin := &authorizer.Token{ID: "sig-admin", UserID: "admin", Scopes: []string{"cluster:admin"}}
	adminKey, adminCluster := accountLimitKey(admin)
	if adminCluster || adminKey != "user:admin" {
		t.Fatalf("cluster-admin JWT key %q cluster=%v, want the user bucket", adminKey, adminCluster)
	}
	ck, cc := accountLimitKey(&authorizer.Token{ClusterKey: true})
	ck2, _ := accountLimitKey(&authorizer.Token{ClusterKey: true, ID: "key-id"})
	if !cc || ck != "cluster" || ck2 != ck {
		t.Fatalf("cluster keys %q %q cluster=%v", ck, ck2, cc)
	}
	buildA := &authorizer.Token{ID: "build-sig-1", AppGrants: []authorizer.AppGrant{{AppID: "b"}, {AppID: "a"}}}
	buildB := &authorizer.Token{ID: "build-sig-2", AppGrants: []authorizer.AppGrant{{AppID: "a"}, {AppID: "b"}}}
	other := &authorizer.Token{ID: "build-sig-3", AppGrants: []authorizer.AppGrant{{AppID: "c"}}}
	bk, _ := accountLimitKey(buildA)
	bk2, _ := accountLimitKey(buildB)
	ok, _ := accountLimitKey(other)
	if bk != bk2 || bk != "apps:a,b" || ok == bk {
		t.Fatalf("build keys %q %q other %q", bk, bk2, ok)
	}
	n1, _ := accountLimitKey(&authorizer.Token{ID: "empty-1"})
	n2, _ := accountLimitKey(&authorizer.Token{ID: "empty-2"})
	if n1 != n2 || n1 != "nogrant" {
		t.Fatalf("empty tokens %q %q", n1, n2)
	}
}

func TestAccountLimiterWindowAndRejects(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newAccountLimiter(2, 2, time.Minute)
	l.now = func() time.Time { return now }
	user := &authorizer.Token{UserID: "u1", ID: "sig"}
	if !l.allow(user).allowed || !l.allow(&authorizer.Token{UserID: "u1", ID: "other-sig"}).allowed {
		t.Fatal("two allows for one user")
	}
	denied := l.allow(user)
	if denied.allowed || denied.retryAfter < time.Second {
		t.Fatalf("third allow %#v", denied)
	}
	if got := l.keys["user:u1"].count; got != 2 {
		t.Fatalf("count after reject = %d, want 2", got)
	}
	_ = l.allow(user)
	if got := l.keys["user:u1"].count; got != 2 {
		t.Fatalf("reject incremented count to %d", got)
	}
	if l.allow(&authorizer.Token{ClusterKey: true}).allowed == false {
		t.Fatal("cluster bucket is independent")
	}
	now = now.Add(time.Minute)
	if !l.allow(user).allowed {
		t.Fatal("new window must allow")
	}
	if got := l.keys["user:u1"].count; got != 1 {
		t.Fatalf("new window count = %d", got)
	}
}

func TestAccountLimiterDropsIdleKeys(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newAccountLimiter(10, 10, time.Minute)
	l.now = func() time.Time { return now }
	if !l.allow(&authorizer.Token{UserID: "gone"}).allowed {
		t.Fatal("allow")
	}
	now = now.Add(2 * time.Minute)
	if !l.allow(&authorizer.Token{UserID: "stay"}).allowed {
		t.Fatal("allow stay")
	}
	if _, ok := l.keys["user:gone"]; ok {
		t.Fatal("idle key was kept")
	}
	if _, ok := l.keys["user:stay"]; !ok {
		t.Fatal("active key was dropped")
	}
}

func TestAccountLimiterConcurrent(t *testing.T) {
	l := newAccountLimiter(50, 50, time.Minute)
	var ok atomic.Int32
	var wg sync.WaitGroup
	user := &authorizer.Token{UserID: "u"}
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.allow(user).allowed {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 50 {
		t.Fatalf("allows = %d, want 50", ok.Load())
	}
}

func TestNilAccountLimiterAllows(t *testing.T) {
	var l *accountLimiter
	if !l.allow(&authorizer.Token{UserID: "u"}).allowed {
		t.Fatal("nil limiter must allow so an unwired process does not 429")
	}
}

type rateLimitFixture struct {
	srv     *httptest.Server
	api     *controllerAPI
	limiter *accountLimiter
	signer  *tokensigner.Signer
	pub     *ecdsa.PublicKey
	clock   *rateLimitClock
	key     string
}

type rateLimitClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *rateLimitClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *rateLimitClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newRateLimitFixture(t *testing.T, userLimit, clusterLimit int) *rateLimitFixture {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const clusterKey = "cluster-test-key"
	clock := &rateLimitClock{t: time.Unix(1_700_000_000, 0)}
	limiter := newAccountLimiter(userLimit, clusterLimit, time.Minute)
	limiter.now = clock.Now
	router := httprouter.New()
	noContent := func(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
		w.WriteHeader(http.StatusNoContent)
	}
	router.GET("/apps", noContent)
	router.POST("/artifacts", noContent)
	router.POST("/oauth/token", noContent)
	router.Handler(http.MethodGet, status.Path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	router.Handler(http.MethodHead, status.Path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	router.GET("/events", func(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 64*1024))
	})
	ctrl := &controllerAPI{
		authorizer: authorizer.New([]string{clusterKey}, nil, &priv.PublicKey, time.Hour),
		limiter:    limiter,
	}
	srv := httptest.NewServer(muxHandler(router, nil, ctrl.authorizer, ctrl))
	t.Cleanup(srv.Close)
	return &rateLimitFixture{
		srv: srv, api: ctrl, limiter: limiter, signer: tokensigner.New(priv),
		pub: &priv.PublicKey, clock: clock, key: clusterKey,
	}
}

func (f *rateLimitFixture) sign(t *testing.T, userID string, at time.Time, scopes []string, grants []*api.AppGrant) string {
	t.Helper()
	raw, err := f.signer.Sign(&api.AccessToken{
		UserId:     userID,
		UserEmail:  "u@example.com",
		IssueTime:  timestamppb.New(at),
		ExpireTime: timestamppb.New(at.Add(time.Hour)),
		Scopes:     scopes,
		AppGrants:  grants,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *rateLimitFixture) do(t *testing.T, method, path, bearer, basic string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if basic != "" {
		req.SetBasicAuth("", basic)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func assertStatus(t *testing.T, res *http.Response, want int) {
	t.Helper()
	defer res.Body.Close()
	if res.StatusCode != want {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("%s %s: status %d, want %d body %s", res.Request.Method, res.Request.URL.Path, res.StatusCode, want, body)
	}
}

func TestControllerRateLimitPerAccount(t *testing.T) {
	f := newRateLimitFixture(t, 2, 5)
	start := time.Now()
	a := f.sign(t, "u1", start, nil, nil)
	b := f.sign(t, "u1", start.Add(time.Second), nil, nil)
	authA := authorizer.New(nil, nil, f.pub, time.Hour)
	ta, err := authA.AuthorizeToken(a)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := authA.AuthorizeToken(b)
	if err != nil {
		t.Fatal(err)
	}
	if ta.ID == "" || ta.ID == tb.ID {
		t.Fatal("the two JWTs must be different signatures")
	}
	assertStatus(t, f.do(t, http.MethodGet, "/apps", a, ""), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", b, ""), http.StatusNoContent)
	res := f.do(t, http.MethodGet, "/apps", a, "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", res.StatusCode)
	}
	if res.Header.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After %q, want 60", res.Header.Get("Retry-After"))
	}
	var je httphelper.JSONError
	if err := json.NewDecoder(res.Body).Decode(&je); err != nil {
		t.Fatal(err)
	}
	if je.Code != httphelper.RatelimitedErrorCode || je.Retry || httphelper.IsRetryableError(je) {
		t.Fatalf("body %#v, want ratelimited retry=false", je)
	}
	assertStatus(t, f.do(t, http.MethodGet, "/apps", "", f.key), http.StatusNoContent)
	f.clock.Add(time.Minute)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", b, ""), http.StatusNoContent)
}

func TestControllerRateLimitLogsOncePerWindow(t *testing.T) {
	f := newRateLimitFixture(t, 1, 5)
	var n atomic.Int32
	var class, userID string
	var mu sync.Mutex
	orig := logRateLimit
	logRateLimit = func(bucket, uid, key string) {
		n.Add(1)
		mu.Lock()
		class, userID = bucket, uid
		mu.Unlock()
		if key == "" || len(key) > 80 {
			t.Errorf("limit_key %q", key)
		}
	}
	t.Cleanup(func() { logRateLimit = orig })
	tok := f.sign(t, "u1", time.Now(), nil, nil)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", tok, ""), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", tok, ""), http.StatusTooManyRequests)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", tok, ""), http.StatusTooManyRequests)
	if n.Load() != 1 {
		t.Fatalf("log calls %d, want 1", n.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if class != "user" || userID != "u1" {
		t.Fatalf("logged bucket %q user %q", class, userID)
	}
}

func TestControllerRateLimitExemptAndShared(t *testing.T) {
	f := newRateLimitFixture(t, 1, 1)
	user := f.sign(t, "u1", time.Now(), nil, nil)
	for i := 0; i < 5; i++ {
		assertStatus(t, f.do(t, http.MethodGet, "/ping", "", ""), http.StatusOK)
		assertStatus(t, f.do(t, http.MethodPost, "/oauth/token", "", ""), http.StatusNoContent)
	}
	bad := f.do(t, http.MethodGet, "/apps", "", "wrong-key")
	assertStatus(t, bad, http.StatusUnauthorized)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", user, ""), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", user, ""), http.StatusTooManyRequests)

	// A full user bucket does not apply to /ping.
	assertStatus(t, f.do(t, http.MethodGet, "/ping", user, ""), http.StatusOK)

	// Cluster-key health stays up when the cluster bucket is exhausted.
	assertStatus(t, f.do(t, http.MethodGet, "/apps", "", f.key), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", "", f.key), http.StatusTooManyRequests)
	assertStatus(t, f.do(t, http.MethodGet, status.Path, "", f.key), http.StatusOK)
	assertStatus(t, f.do(t, http.MethodHead, status.Path, "", f.key), http.StatusOK)

	// A user calling status still consumes the user bucket. The path is
	// cluster-admin only, so the first call is 403 and the next is 429.
	f.clock.Add(time.Minute)
	assertStatus(t, f.do(t, http.MethodGet, status.Path, user, ""), http.StatusForbidden)
	assertStatus(t, f.do(t, http.MethodGet, status.Path, user, ""), http.StatusTooManyRequests)
}

func TestControllerRateLimitClusterAdminIsUserBudget(t *testing.T) {
	f := newRateLimitFixture(t, 1, 5)
	admin := f.sign(t, "admin", time.Now(), []string{"cluster:admin"}, nil)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", admin, ""), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", admin, ""), http.StatusTooManyRequests)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", "", f.key), http.StatusNoContent)
}

func TestControllerRateLimitEventsCountOnce(t *testing.T) {
	f := newRateLimitFixture(t, 1, 3)
	assertStatus(t, f.do(t, http.MethodGet, "/events", "", f.key), http.StatusOK)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", "", f.key), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", "", f.key), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", "", f.key), http.StatusTooManyRequests)
}

func TestControllerRateLimitBuildTokensShareApp(t *testing.T) {
	f := newRateLimitFixture(t, 1, 5)
	grant := []*api.AppGrant{{AppId: "app-1", Permissions: []string{"app:write"}}}
	scopes := []string{authz.ScopeBuildArtifacts}
	start := time.Now()
	a := f.sign(t, "", start, scopes, grant)
	b := f.sign(t, "", start.Add(time.Second), scopes, grant)
	other := f.sign(t, "", start, scopes, []*api.AppGrant{{AppId: "app-2", Permissions: []string{"app:write"}}})
	assertStatus(t, f.do(t, http.MethodPost, "/artifacts", a, ""), http.StatusNoContent)
	assertStatus(t, f.do(t, http.MethodPost, "/artifacts", b, ""), http.StatusTooManyRequests)
	assertStatus(t, f.do(t, http.MethodPost, "/artifacts", other, ""), http.StatusNoContent)

	emptyA := f.sign(t, "", start, nil, nil)
	emptyB := f.sign(t, "", start.Add(2*time.Second), nil, nil)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", emptyA, ""), http.StatusForbidden)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", emptyB, ""), http.StatusTooManyRequests)
}

type rateLimitHeaderStream struct {
	md metadata.MD
}

func (s *rateLimitHeaderStream) Method() string { return api.Controller_Status_FullMethodName }
func (s *rateLimitHeaderStream) SetHeader(md metadata.MD) error {
	s.md = md
	return nil
}
func (s *rateLimitHeaderStream) SendHeader(metadata.MD) error { return nil }
func (s *rateLimitHeaderStream) SetTrailer(metadata.MD) error { return nil }

type rateLimitServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s rateLimitServerStream) Context() context.Context { return s.ctx }

func TestControllerGRPCRateLimitSharesHTTPBucket(t *testing.T) {
	f := newRateLimitFixture(t, 1, 5)
	user := f.sign(t, "u1", time.Now(), []string{"cluster:admin"}, nil)
	assertStatus(t, f.do(t, http.MethodGet, "/apps", user, ""), http.StatusNoContent)

	g := &grpcAPI{controllerAPI: f.api}
	ctx := grpcAuthCtx(user)
	called := 0
	_, err := g.unaryInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: api.Controller_Status_FullMethodName}, func(context.Context, interface{}) (interface{}, error) {
		called++
		return nil, nil
	})
	if grpcstatus.Code(err) != codes.ResourceExhausted {
		t.Fatalf("grpc code %v err %v, want ResourceExhausted", grpcstatus.Code(err), err)
	}
	if called != 0 {
		t.Fatal("handler ran after the shared bucket was exhausted")
	}
	hs, _ := grpc.ServerTransportStreamFromContext(ctx).(*rateLimitHeaderStream)
	if hs == nil || len(hs.md.Get("retry-after")) != 1 || hs.md.Get("retry-after")[0] == "" {
		t.Fatalf("retry-after metadata %#v", hs)
	}
}

func TestControllerGRPCStreamCountsOnce(t *testing.T) {
	f := newRateLimitFixture(t, 2, 5)
	user := f.sign(t, "u1", time.Now(), []string{"cluster:admin"}, nil)
	g := &grpcAPI{controllerAPI: f.api}
	open := func() error {
		return g.streamInterceptor(nil, rateLimitServerStream{ctx: grpcAuthCtx(user)}, &grpc.StreamServerInfo{FullMethod: api.Controller_StreamApps_FullMethodName}, func(interface{}, grpc.ServerStream) error {
			return nil
		})
	}
	if err := open(); err != nil {
		t.Fatal(err)
	}
	if got := f.limiter.keys["user:u1"].count; got != 1 {
		t.Fatalf("stream count %d, want 1", got)
	}
	if err := open(); err != nil {
		t.Fatal(err)
	}
	err := open()
	if grpcstatus.Code(err) != codes.ResourceExhausted {
		t.Fatalf("third stream %v", err)
	}
	if got := f.limiter.keys["user:u1"].count; got != 2 {
		t.Fatalf("rejects changed count to %d", got)
	}
}

func grpcAuthCtx(token string) context.Context {
	hs := &rateLimitHeaderStream{}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	return grpc.NewContextWithServerTransportStream(ctx, hs)
}
