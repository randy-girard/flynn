package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	. "github.com/flynn/go-check"
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
