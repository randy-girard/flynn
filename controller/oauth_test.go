package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
)

func (s *S) createLoginUser(c *C, email, handle, password string, admin bool) *ct.User {
	hash, err := tenancy.HashPassword(password)
	c.Assert(err, IsNil)
	u := &ct.User{
		ID:            random.UUID(),
		Handle:        handle,
		Email:         email,
		PasswordHash:  hash,
		ClusterAdmin:  admin,
		EmailVerified: true,
	}
	c.Assert(s.api.tenancy.CreateUser(u), IsNil)
	return u
}

func (s *S) TestOAuthMetadataUnauthenticated(c *C) {
	res, err := http.Get(s.srv.URL + "/.well-known/oauth-authorization-server")
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 200)
	var meta map[string]interface{}
	c.Assert(json.NewDecoder(res.Body).Decode(&meta), IsNil)
	c.Assert(meta["token_endpoint"], Equals, s.srv.URL+"/oauth/token")
	grants, _ := meta["grant_types_supported"].([]interface{})
	joined := ""
	for _, g := range grants {
		joined += g.(string) + ","
	}
	c.Assert(strings.Contains(joined, "password"), Equals, true)
	c.Assert(strings.Contains(joined, "authorization_code"), Equals, true)
}

func (s *S) TestOAuthPasswordGrant(c *C) {
	s.createLoginUser(c, "ops@example.com", "ops", "s3cret-pass", true)
	form := url.Values{
		"grant_type": {"password"},
		"username":   {"ops@example.com"},
		"password":   {"s3cret-pass"},
		"client_id":  {"flynn-cli"},
		"audience":   {s.srv.URL},
	}
	res, err := http.PostForm(s.srv.URL+"/oauth/token", form)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	c.Assert(res.StatusCode, Equals, 200, Commentf("%s", body))
	var tok map[string]interface{}
	c.Assert(json.Unmarshal(body, &tok), IsNil)
	c.Assert(tok["token_type"], Equals, "Bearer")
	c.Assert(tok["access_token"], Not(Equals), "")
	c.Assert(tok["refresh_token"], Not(Equals), "")
	c.Assert(tok["audience"], Equals, s.srv.URL)

	req, err := http.NewRequest("GET", s.srv.URL+"/whoami", nil)
	c.Assert(err, IsNil)
	req.Header.Set("Authorization", "Bearer "+tok["access_token"].(string))
	who, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer who.Body.Close()
	c.Assert(who.StatusCode, Equals, 200)
	var me ct.WhoAmI
	c.Assert(json.NewDecoder(who.Body).Decode(&me), IsNil)
	c.Assert(me.Email, Equals, "ops@example.com")
	c.Assert(me.ClusterAdmin, Equals, true)
}

func (s *S) TestOAuthPasswordGrantRejectsBadPassword(c *C) {
	s.createLoginUser(c, "bad@example.com", "badpw", "right-password", false)
	form := url.Values{
		"grant_type": {"password"},
		"username":   {"bad@example.com"},
		"password":   {"wrong"},
		"client_id":  {"flynn-cli"},
	}
	res, err := http.PostForm(s.srv.URL+"/oauth/token", form)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 401)
}

func (s *S) TestOAuthPasswordGrantRequiresEmail(c *C) {
	s.createLoginUser(c, "handleuser@example.com", "handleuser", "pw-handle-ok", false)
	form := url.Values{
		"grant_type": {"password"},
		"username":   {"handleuser"},
		"password":   {"pw-handle-ok"},
		"client_id":  {"flynn-cli"},
	}
	res, err := http.PostForm(s.srv.URL+"/oauth/token", form)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 401)
}

func (s *S) TestOAuthRefreshToken(c *C) {
	s.createLoginUser(c, "refresh@example.com", "refresh", "pw-refresh-1", true)
	form := url.Values{
		"grant_type": {"password"},
		"username":   {"refresh@example.com"},
		"password":   {"pw-refresh-1"},
		"client_id":  {"flynn-cli"},
		"audience":   {s.srv.URL},
	}
	res, err := http.PostForm(s.srv.URL+"/oauth/token", form)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	var first map[string]interface{}
	c.Assert(json.NewDecoder(res.Body).Decode(&first), IsNil)
	oldRefresh := first["refresh_token"].(string)

	refreshForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {oldRefresh},
		"client_id":     {"flynn-cli"},
		"audience":      {s.srv.URL},
	}
	res2, err := http.PostForm(s.srv.URL+"/oauth/token", refreshForm)
	c.Assert(err, IsNil)
	defer res2.Body.Close()
	c.Assert(res2.StatusCode, Equals, 200)
	var second map[string]interface{}
	c.Assert(json.NewDecoder(res2.Body).Decode(&second), IsNil)
	c.Assert(second["refresh_token"], Not(Equals), oldRefresh)
	c.Assert(second["access_token"], Not(Equals), "")

	reuse, err := http.PostForm(s.srv.URL+"/oauth/token", refreshForm)
	c.Assert(err, IsNil)
	defer reuse.Body.Close()
	c.Assert(reuse.StatusCode, Equals, 400)
}

func (s *S) TestOAuthAuthorizeCodeFlow(c *C) {
	s.createLoginUser(c, "code@example.com", "codeuser", "pw-code-ok", true)
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	form := url.Values{
		"response_type":         {"code"},
		"client_id":             {"flynn-cli"},
		"redirect_uri":          {"http://127.0.0.1:8085/"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"st"},
		"nonce":                 {"n1"},
		"login":                 {"code@example.com"},
		"password":              {"pw-code-ok"},
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.PostForm(s.srv.URL+"/oauth/authorize", form)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, http.StatusFound)
	loc, err := res.Location()
	c.Assert(err, IsNil)
	code := loc.Query().Get("code")
	c.Assert(code, Not(Equals), "")
	c.Assert(loc.Query().Get("state"), Equals, "st")

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"http://127.0.0.1:8085/"},
		"code_verifier": {verifier},
		"client_id":     {"flynn-cli"},
		"audience":      {s.srv.URL},
	}
	tokRes, err := http.PostForm(s.srv.URL+"/oauth/token", tokenForm)
	c.Assert(err, IsNil)
	defer tokRes.Body.Close()
	body, _ := io.ReadAll(tokRes.Body)
	c.Assert(tokRes.StatusCode, Equals, 200, Commentf("%s", body))
	var tok map[string]interface{}
	c.Assert(json.Unmarshal(body, &tok), IsNil)
	c.Assert(tok["nonce"], Equals, "n1")
	c.Assert(tok["access_token"], Not(Equals), "")
}

func (s *S) TestOAuthTokenFailsWithoutSigningKey(c *C) {
	s.createLoginUser(c, "nosign@example.com", "nosign", "pw-nosign", true)
	orig := s.api.tokenSigner
	s.api.tokenSigner = nil
	defer func() { s.api.tokenSigner = orig }()
	form := url.Values{
		"grant_type": {"password"},
		"username":   {"nosign@example.com"},
		"password":   {"pw-nosign"},
		"client_id":  {"flynn-cli"},
	}
	res, err := http.PostForm(s.srv.URL+"/oauth/token", form)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	c.Assert(res.StatusCode, Equals, 500, Commentf("%s", body))
	c.Assert(strings.Contains(string(body), "signing key"), Equals, true)
}

func (s *S) TestOAuthAuthorizeShowsLoginForm(c *C) {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"flynn-cli"},
		"redirect_uri":          {"urn:ietf:wg:oauth:2.0:oob"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
	}
	res, err := http.Get(s.srv.URL + "/oauth/authorize?" + q.Encode())
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 200)
	body, _ := io.ReadAll(res.Body)
	c.Assert(strings.Contains(string(body), "Log in"), Equals, true)
	c.Assert(strings.Contains(string(body), "login-card"), Equals, true)
	c.Assert(strings.Contains(string(body), "brand-mark"), Equals, true)
}

func TestOAuthPublicPath(t *testing.T) {
	if !oauthPublicPath(http.MethodGet, "/oauth/authorize") || oauthPublicPath(http.MethodGet, "/apps") {
		t.Fatal("oauth public path")
	}
	if !oauthPublicPath(http.MethodPost, "/oauth/token") {
		t.Fatal("token")
	}
}

func TestOAuthIssuerFallback(t *testing.T) {
	api := &controllerAPI{}
	req := httptest.NewRequest(http.MethodGet, "http://controller.test/.well-known/oauth-authorization-server", nil)
	req.Host = "controller.test"
	if got := api.oauthIssuer(req); got != "http://controller.test" {
		t.Fatalf("got %q", got)
	}
}

func TestOAuthIssuerUsesAuthSubdomain(t *testing.T) {
	t.Setenv("DEFAULT_ROUTE_DOMAIN", "1.localflynn.com")
	api := &controllerAPI{}
	req := httptest.NewRequest(http.MethodGet, "https://auth.1.localflynn.com/.well-known/oauth-authorization-server", nil)
	req.Host = "auth.1.localflynn.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := api.oauthIssuer(req); got != "https://auth.1.localflynn.com" {
		t.Fatalf("got %q", got)
	}
	if got := api.oauthAudience(req); got != "https://controller.1.localflynn.com" {
		t.Fatalf("audience %q", got)
	}
	ctrl := httptest.NewRequest(http.MethodGet, "https://controller.1.localflynn.com/.well-known/oauth-authorization-server", nil)
	ctrl.Host = "controller.1.localflynn.com"
	ctrl.Header.Set("X-Forwarded-Proto", "https")
	if got := api.oauthIssuer(ctrl); got != "https://auth.1.localflynn.com" {
		t.Fatalf("controller host advertised %q", got)
	}
}

func TestOAuthIssuerEmptyHostUsesAuthDomain(t *testing.T) {
	t.Setenv("DEFAULT_ROUTE_DOMAIN", "demo.localflynn.com")
	api := &controllerAPI{}
	if got := api.oauthIssuer(nil); got != "https://auth.demo.localflynn.com" {
		t.Fatalf("got %q", got)
	}
}

// --- CORE-31 -----------------------------------------------------------------

func passwordGrantFrom(c *C, srvURL, ip, email, password string) *http.Response {
	form := url.Values{
		"grant_type": {"password"},
		"username":   {email},
		"password":   {password},
		"client_id":  {"flynn-cli"},
		"audience":   {srvURL},
	}
	req, err := http.NewRequest(http.MethodPost, srvURL+"/oauth/token", strings.NewReader(form.Encode()))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	return res
}

func passwordGrantStatus(c *C, srvURL, ip, email, password string) (int, http.Header, map[string]interface{}) {
	res := passwordGrantFrom(c, srvURL, ip, email, password)
	defer res.Body.Close()
	var out map[string]interface{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, res.Header, out
}

// POST /oauth/token grant_type=password is throttled per account and per
// address, answers 429 with Retry-After, and does not lock the owner out.
func (s *S) TestPasswordGrantIsRateLimited(c *C) {
	clk := &fakeClock{t: time.Now()}
	s.api.loginLimiter = newTestLoginLimiter(clk)
	lim := defaultLoginLimits()
	s.createLoginUser(c, "core31-rl@example.com", "core31-rl", "core31-right-pw", false)

	// Per account and address.
	for i := 0; i < lim.pairBurst; i++ {
		code, _, _ := passwordGrantStatus(c, s.srv.URL, "198.51.100.1", "core31-rl@example.com", "wrong")
		c.Assert(code, Equals, http.StatusUnauthorized, Commentf("attempt %d", i+1))
	}
	code, hdr, body := passwordGrantStatus(c, s.srv.URL, "198.51.100.1", "core31-rl@example.com", "core31-right-pw")
	c.Assert(code, Equals, http.StatusTooManyRequests, Commentf("%v", body))
	c.Assert(hdr.Get("Retry-After"), Not(Equals), "")
	c.Assert(body["error"], Equals, "too_many_attempts")

	// The owner from another address still signs in.
	code, _, body = passwordGrantStatus(c, s.srv.URL, "203.0.113.20", "core31-rl@example.com", "core31-right-pw")
	c.Assert(code, Equals, http.StatusOK, Commentf("%v", body))

	// Backoff, not a lock: after the refill interval one more try is allowed.
	clk.add(lim.pairEvery)
	code, _, body = passwordGrantStatus(c, s.srv.URL, "198.51.100.1", "core31-rl@example.com", "core31-right-pw")
	c.Assert(code, Equals, http.StatusOK, Commentf("%v", body))

	// Per address: spraying many accounts from one address.
	for i := 0; i < lim.ipBurst; i++ {
		code, _, _ := passwordGrantStatus(c, s.srv.URL, "198.51.100.99", fmt.Sprintf("core31-spray-%d@example.com", i), "guess")
		c.Assert(code, Equals, http.StatusUnauthorized, Commentf("spray %d", i))
	}
	code, _, _ = passwordGrantStatus(c, s.srv.URL, "198.51.100.99", "core31-rl@example.com", "core31-right-pw")
	c.Assert(code, Equals, http.StatusTooManyRequests)

	// The form login on /oauth/authorize shares the same limiter.
	for i := 0; i < lim.pairBurst; i++ {
		c.Assert(s.authorizeLoginStatus(c, "198.51.100.2", "core31-rl@example.com", "wrong"), Equals, http.StatusUnauthorized)
	}
	c.Assert(s.authorizeLoginStatus(c, "198.51.100.2", "core31-rl@example.com", "core31-right-pw"), Equals, http.StatusTooManyRequests)
	c.Assert(s.authorizeLoginStatus(c, "203.0.113.21", "core31-rl@example.com", "core31-right-pw"), Equals, http.StatusFound)
}

// The dashboard signs users in with the cluster key from its own address. It
// forwards the browser's address, so the per-address bucket is the user's,
// not the dashboard's.
func (s *S) TestPasswordGrantServiceCallerForwardsAddress(c *C) {
	clk := &fakeClock{t: time.Now()}
	s.api.loginLimiter = newTestLoginLimiter(clk)
	lim := defaultLoginLimits()
	s.createLoginUser(c, "core31-dash@example.com", "core31-dash", "core31-dash-pw", false)
	send := func(xff, email, pw string) int {
		form := url.Values{"grant_type": {"password"}, "username": {email}, "password": {pw}, "client_id": {"flynn-dashboard"}}
		req, err := http.NewRequest(http.MethodPost, s.srv.URL+"/oauth/token", strings.NewReader(form.Encode()))
		c.Assert(err, IsNil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth("", authKey)
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		res, err := http.DefaultClient.Do(req)
		c.Assert(err, IsNil)
		res.Body.Close()
		return res.StatusCode
	}
	for i := 0; i < lim.ipBurst; i++ {
		c.Assert(send("198.51.100.70", fmt.Sprintf("core31-d%d@example.com", i), "x"), Equals, http.StatusUnauthorized)
	}
	c.Assert(send("198.51.100.70", "core31-dash@example.com", "core31-dash-pw"), Equals, http.StatusTooManyRequests)
	// Another browser behind the same dashboard is not affected.
	c.Assert(send("198.51.100.71", "core31-dash@example.com", "core31-dash-pw"), Equals, http.StatusOK)
	// No forwarded address: only the account buckets apply.
	for i := 0; i < lim.ipBurst+2; i++ {
		c.Assert(send("", fmt.Sprintf("core31-n%d@example.com", i), "x"), Equals, http.StatusUnauthorized)
	}
	c.Assert(send("", "core31-dash@example.com", "core31-dash-pw"), Equals, http.StatusOK)
}

func (s *S) authorizeLoginStatus(c *C, ip, email, password string) int {
	form := url.Values{
		"response_type":         {"code"},
		"client_id":             {"flynn-cli"},
		"redirect_uri":          {"http://127.0.0.1:8085/"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
		"login":                 {email},
		"password":              {password},
	}
	req, err := http.NewRequest(http.MethodPost, s.srv.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", ip)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	c.Assert(err, IsNil)
	res.Body.Close()
	return res.StatusCode
}

// Concurrent uses of one refresh token: exactly one may succeed.
func (s *S) TestRefreshTokenSingleUse(c *C) {
	s.createLoginUser(c, "core31-rt@example.com", "core31-rt", "core31-rt-pw", false)
	code, _, first := passwordGrantStatus(c, s.srv.URL, "", "core31-rt@example.com", "core31-rt-pw")
	c.Assert(code, Equals, http.StatusOK)
	rt := first["refresh_token"].(string)

	const n = 16
	var wg sync.WaitGroup
	var ok int32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := http.PostForm(s.srv.URL+"/oauth/token", url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"flynn-cli"},
			})
			if err != nil {
				return
			}
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	c.Assert(atomic.LoadInt32(&ok), Equals, int32(1))
}

// Concurrent uses of one authorization code: exactly one may succeed.
func (s *S) TestAuthorizationCodeSingleUse(c *C) {
	u := s.createLoginUser(c, "core31-code@example.com", "core31-code", "core31-code-pw", false)
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	codeVal := random.Hex(32)
	c.Assert(s.api.tenancy.SaveOAuthCode(&data.OAuthCode{
		Code: codeVal, ClientID: "flynn-cli", RedirectURI: "http://127.0.0.1:8085/",
		CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]), UserID: u.ID,
		ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	}), IsNil)

	const n = 16
	var wg sync.WaitGroup
	var ok int32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := http.PostForm(s.srv.URL+"/oauth/token", url.Values{
				"grant_type": {"authorization_code"}, "code": {codeVal}, "client_id": {"flynn-cli"},
				"redirect_uri": {"http://127.0.0.1:8085/"}, "code_verifier": {verifier},
			})
			if err != nil {
				return
			}
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	c.Assert(atomic.LoadInt32(&ok), Equals, int32(1))
}

func (s *S) patchUserAsCluster(c *C, id, body string) int {
	req, err := http.NewRequest(http.MethodPatch, s.srv.URL+"/users/"+id, strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	res.Body.Close()
	return res.StatusCode
}

// After a password change every credential issued before it stops working:
// login tokens, refresh tokens, personal access tokens and the /oauth/authorize
// browser session. A new login with the new password works.
func (s *S) TestPasswordChangeRevokesTokens(c *C) {
	defer func(orig time.Duration) { credentialRevokeSkew = orig }(credentialRevokeSkew)
	credentialRevokeSkew = 0
	u := s.createLoginUser(c, "core31-pw@example.com", "core31-pw", "core31-old-pw", false)
	other := s.createLoginUser(c, "core31-bystander@example.com", "core31-by", "core31-by-pw", false)

	code, _, tok := passwordGrantStatus(c, s.srv.URL, "", "core31-pw@example.com", "core31-old-pw")
	c.Assert(code, Equals, http.StatusOK)
	access := tok["access_token"].(string)
	refresh := tok["refresh_token"].(string)
	pat := s.storePAT(c, u.ID, "user")
	c.Assert(s.api.tenancy.CreateOAuthSession(u.ID, "core31-session-cookie", time.Now().Add(time.Hour)), IsNil)
	otherPAT := s.storePAT(c, other.ID, "user")

	code, _ = s.scopedStatus(c, http.MethodGet, access, "/whoami", "")
	c.Assert(code, Equals, http.StatusOK)
	code, _ = s.scopedStatus(c, http.MethodGet, pat, "/whoami", "")
	c.Assert(code, Equals, http.StatusOK)

	c.Assert(s.patchUserAsCluster(c, u.ID, `{"password":"core31-new-pw"}`), Equals, http.StatusOK)

	code, _ = s.scopedStatus(c, http.MethodGet, access, "/whoami", "")
	c.Assert(code, Equals, http.StatusUnauthorized, Commentf("login token survived a password change"))
	code, _ = s.scopedStatus(c, http.MethodGet, pat, "/whoami", "")
	c.Assert(code, Equals, http.StatusUnauthorized, Commentf("PAT survived a password change"))
	res, err := http.PostForm(s.srv.URL+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"flynn-cli"}})
	c.Assert(err, IsNil)
	res.Body.Close()
	c.Assert(res.StatusCode, Equals, http.StatusBadRequest, Commentf("refresh token survived a password change"))
	_, err = s.api.tenancy.GetOAuthSession("core31-session-cookie", time.Now())
	c.Assert(err, Equals, data.ErrNotFound, Commentf("oauth session survived a password change"))

	// Other users keep their credentials.
	code, _ = s.scopedStatus(c, http.MethodGet, otherPAT, "/whoami", "")
	c.Assert(code, Equals, http.StatusOK)

	// The old password no longer works; the new one does and its token works.
	code, _, _ = passwordGrantStatus(c, s.srv.URL, "", "core31-pw@example.com", "core31-old-pw")
	c.Assert(code, Equals, http.StatusUnauthorized)
	code, _, tok = passwordGrantStatus(c, s.srv.URL, "", "core31-pw@example.com", "core31-new-pw")
	c.Assert(code, Equals, http.StatusOK)
	code, _ = s.scopedStatus(c, http.MethodGet, tok["access_token"].(string), "/whoami", "")
	c.Assert(code, Equals, http.StatusOK)
}

// The dashboard re-sends the current password when it syncs a user. Setting
// the same password again is not a change and must not sign the user out.
func (s *S) TestPasswordResyncKeepsTokens(c *C) {
	u := s.createLoginUser(c, "core31-sync@example.com", "core31-sync", "core31-sync-pw", false)
	pat := s.storePAT(c, u.ID, "user")
	c.Assert(s.patchUserAsCluster(c, u.ID, `{"password":"core31-sync-pw","email_verified":true}`), Equals, http.StatusOK)
	code, _ := s.scopedStatus(c, http.MethodGet, pat, "/whoami", "")
	c.Assert(code, Equals, http.StatusOK)
}

// Expired authorization codes, refresh tokens and browser sessions are
// deleted; live ones stay.
func (s *S) TestPurgeExpiredOAuthRows(c *C) {
	u := s.createLoginUser(c, "core31-purge@example.com", "core31-purge", "core31-purge-pw", false)
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, oc := range []*data.OAuthCode{
		{Code: "core31-code-old", RedirectURI: "urn:ietf:wg:oauth:2.0:oob", UserID: u.ID, ExpiresAt: past, CreatedAt: past},
		{Code: "core31-code-new", RedirectURI: "urn:ietf:wg:oauth:2.0:oob", UserID: u.ID, ExpiresAt: future, CreatedAt: now},
	} {
		c.Assert(s.api.tenancy.SaveOAuthCode(oc), IsNil)
	}
	_, err := s.api.tenancy.CreateRefreshToken(u.ID, "core31-rt-old", "flynn-cli", past)
	c.Assert(err, IsNil)
	_, err = s.api.tenancy.CreateRefreshToken(u.ID, "core31-rt-new", "flynn-cli", future)
	c.Assert(err, IsNil)
	c.Assert(s.api.tenancy.CreateOAuthSession(u.ID, "core31-sess-old", past), IsNil)
	c.Assert(s.api.tenancy.CreateOAuthSession(u.ID, "core31-sess-new", future), IsNil)

	n, err := s.api.tenancy.PurgeExpiredOAuth(now)
	c.Assert(err, IsNil)
	c.Assert(n >= 3, Equals, true, Commentf("purged %d", n))

	_, err = s.api.tenancy.ConsumeOAuthCode("core31-code-old")
	c.Assert(err, Equals, data.ErrNotFound)
	_, err = s.api.tenancy.ConsumeRefreshToken("core31-rt-old")
	c.Assert(err, Equals, data.ErrNotFound)
	_, err = s.api.tenancy.GetOAuthSession("core31-sess-old", past.Add(-time.Hour))
	c.Assert(err, Equals, data.ErrNotFound)

	_, err = s.api.tenancy.ConsumeOAuthCode("core31-code-new")
	c.Assert(err, IsNil)
	_, err = s.api.tenancy.ConsumeRefreshToken("core31-rt-new")
	c.Assert(err, IsNil)
	_, err = s.api.tenancy.GetOAuthSession("core31-sess-new", now)
	c.Assert(err, IsNil)
}
