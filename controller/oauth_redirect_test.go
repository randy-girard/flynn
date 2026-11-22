package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/pkg/random"
)

const testOAuthChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

var attackerRedirects = []string{
	"https://evil.example/cb",
	"http://evil.example/cb",
	"http://127.0.0.1.evil.example/cb",
	"http://localhost.evil.example/cb",
	"http://user@127.0.0.1:8085/",
	"https://127.0.0.1:8085/",
	"javascript:alert(1)",
	"//evil.example/cb",
	"ftp://127.0.0.1/",
	"urn:example:evil",
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func authorizeQuery(clientID, redirectURI string) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {testOAuthChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"st"},
	}
}

// oauthSessionFor gives a live flynn_oauth_session cookie value for a new user.
func (s *S) oauthSessionFor(c *C, email string) string {
	u := s.createLoginUser(c, email, strings.Split(email, "@")[0], "pw-"+random.Hex(6), true)
	tok := random.Hex(32)
	c.Assert(s.api.tenancy.CreateOAuthSession(u.ID, tok, time.Now().Add(time.Hour)), IsNil)
	return tok
}

func (s *S) authorizeGet(c *C, cookie string, q url.Values) *http.Response {
	req, err := http.NewRequest("GET", s.srv.URL+"/oauth/authorize?"+q.Encode(), nil)
	c.Assert(err, IsNil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: oauthSessionCookie, Value: cookie})
	}
	res, err := noRedirectClient().Do(req)
	c.Assert(err, IsNil)
	return res
}

func (s *S) authorizePost(c *C, cookie string, form url.Values, hdr map[string]string) *http.Response {
	req, err := http.NewRequest("POST", s.srv.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: oauthSessionCookie, Value: cookie})
	}
	res, err := noRedirectClient().Do(req)
	c.Assert(err, IsNil)
	return res
}

// A user with a live session cookie who follows a crafted authorize link
// must not have a code sent to a third-party host.
func (s *S) TestOAuthAuthorizeRejectsNonLoopbackRedirect(c *C) {
	cookie := s.oauthSessionFor(c, "victim19@example.com")
	for _, ru := range attackerRedirects {
		res := s.authorizeGet(c, cookie, authorizeQuery("flynn-cli", ru))
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		c.Assert(res.StatusCode, Equals, http.StatusBadRequest, Commentf("GET %s: %s", ru, body))
		c.Assert(res.Header.Get("Location"), Equals, "", Commentf("GET %s", ru))
	}

	// The password POST must not redirect there either.
	s.createLoginUser(c, "victim19b@example.com", "victim19b", "pw-victim-19b", true)
	for _, ru := range attackerRedirects {
		form := authorizeQuery("flynn-cli", ru)
		form.Set("login", "victim19b@example.com")
		form.Set("password", "pw-victim-19b")
		res := s.authorizePost(c, "", form, nil)
		res.Body.Close()
		c.Assert(res.StatusCode, Equals, http.StatusBadRequest, Commentf("POST %s", ru))
		c.Assert(res.Header.Get("Location"), Equals, "", Commentf("POST %s", ru))
	}
}

func (s *S) TestOAuthAuthorizeRejectsUnknownClient(c *C) {
	cookie := s.oauthSessionFor(c, "client19@example.com")
	for _, cid := range []string{"", "evil-app"} {
		res := s.authorizeGet(c, cookie, authorizeQuery(cid, "http://127.0.0.1:8085/"))
		res.Body.Close()
		c.Assert(res.StatusCode, Equals, http.StatusBadRequest, Commentf("client_id %q", cid))
		c.Assert(res.Header.Get("Location"), Equals, "")
	}
}

// An existing session gets a consent page, not a silent redirect; the
// consent POST then sends the code to the CLI's loopback listener.
func (s *S) TestOAuthAuthorizeLoopbackNeedsConsentWithSession(c *C) {
	cookie := s.oauthSessionFor(c, "consent19@example.com")
	for _, ru := range []string{"http://127.0.0.1:8085/", "http://localhost:8085/cb", "http://[::1]:8085/"} {
		q := authorizeQuery("flynn-cli", ru)
		res := s.authorizeGet(c, cookie, q)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		c.Assert(res.StatusCode, Equals, http.StatusOK, Commentf("%s: %s", ru, body))
		c.Assert(res.Header.Get("Location"), Equals, "")
		c.Assert(strings.Contains(string(body), "consent19@example.com"), Equals, true, Commentf("%s", body))
		c.Assert(strings.Contains(string(body), `name="consent"`), Equals, true)

		q.Set("consent", "1")
		res = s.authorizePost(c, cookie, q, map[string]string{"Sec-Fetch-Site": "same-origin"})
		res.Body.Close()
		c.Assert(res.StatusCode, Equals, http.StatusFound, Commentf("%s", ru))
		loc, err := res.Location()
		c.Assert(err, IsNil)
		want, _ := url.Parse(ru)
		c.Assert(loc.Host, Equals, want.Host)
		c.Assert(loc.Query().Get("code"), Not(Equals), "")
		c.Assert(loc.Query().Get("state"), Equals, "st")
	}

	// OOB still shows the code page after consent.
	q := authorizeQuery("flynn-cli", "urn:ietf:wg:oauth:2.0:oob")
	q.Set("consent", "1")
	res := s.authorizePost(c, cookie, q, nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	c.Assert(res.StatusCode, Equals, http.StatusOK)
	c.Assert(strings.Contains(string(body), `id="code"`), Equals, true)
}

func (s *S) TestOAuthAuthorizeConsentRejectsCrossSiteAndNoSession(c *C) {
	cookie := s.oauthSessionFor(c, "csrf19@example.com")
	q := authorizeQuery("flynn-cli", "http://127.0.0.1:8085/")
	q.Set("consent", "1")
	res := s.authorizePost(c, cookie, q, map[string]string{"Sec-Fetch-Site": "cross-site"})
	res.Body.Close()
	c.Assert(res.StatusCode, Equals, http.StatusForbidden)
	c.Assert(res.Header.Get("Location"), Equals, "")

	res = s.authorizePost(c, cookie, q, map[string]string{"Origin": "https://evil.example"})
	res.Body.Close()
	c.Assert(res.StatusCode, Equals, http.StatusForbidden)
	c.Assert(res.Header.Get("Location"), Equals, "")

	// No session: falls back to the login form, no code.
	res = s.authorizePost(c, "", q, nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	c.Assert(res.Header.Get("Location"), Equals, "")
	c.Assert(strings.Contains(string(body), `name="password"`), Equals, true)
}

// CORE-54: a code is bound to the client it was issued for (RFC 6749
// 4.1.3). Exchanging it under another client_id is invalid_grant and burns
// the code; the flynn CLI sends the same client_id at authorize and token
// time, and a request with no client_id still works for old clients.
func (s *S) TestOAuthTokenRejectsClientMismatch(c *C) {
	u := s.createLoginUser(c, "core54@example.com", "core54", "core54-pw-ok", false)
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	newCode := func() string {
		code := random.Hex(32)
		c.Assert(s.api.tenancy.SaveOAuthCode(&data.OAuthCode{
			Code: code, ClientID: "flynn-cli", RedirectURI: "http://127.0.0.1:8085/",
			CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]), UserID: u.ID,
			ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
		}), IsNil)
		return code
	}
	exchange := func(code, clientID string) (int, map[string]interface{}) {
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {"http://127.0.0.1:8085/"},
			"code_verifier": {verifier},
		}
		if clientID != "" {
			form.Set("client_id", clientID)
		}
		res, err := http.PostForm(s.srv.URL+"/oauth/token", form)
		c.Assert(err, IsNil)
		defer res.Body.Close()
		var body map[string]interface{}
		b, _ := io.ReadAll(res.Body)
		_ = json.Unmarshal(b, &body)
		return res.StatusCode, body
	}

	code := newCode()
	status, body := exchange(code, "other")
	c.Assert(status, Equals, http.StatusBadRequest, Commentf("%v", body))
	c.Assert(body["error"], Equals, "invalid_grant")
	c.Assert(body["access_token"], IsNil)
	status, body = exchange(code, "flynn-cli")
	c.Assert(status, Equals, http.StatusBadRequest, Commentf("code reused after a mismatch: %v", body))

	for _, cid := range []string{"flynn-cli", ""} {
		status, body := exchange(newCode(), cid)
		c.Assert(status, Equals, http.StatusOK, Commentf("client_id %q: %v", cid, body))
		c.Assert(body["access_token"], Not(Equals), "")
	}
}

func TestOAuthRedirectURIAllowed(t *testing.T) {
	for _, ru := range []string{"http://127.0.0.1:8085/", "http://localhost:8085/", "http://[::1]:8085/", "urn:ietf:wg:oauth:2.0:oob"} {
		if !oauthRedirectURIAllowed(ru) {
			t.Errorf("%q should be allowed", ru)
		}
	}
	for _, ru := range attackerRedirects {
		if oauthRedirectURIAllowed(ru) {
			t.Errorf("%q should be refused", ru)
		}
	}
	if _, err := oauthHTTPRedirectLocation("https://evil.example/cb", "c", "s"); err == nil {
		t.Error("oauthHTTPRedirectLocation accepted a third-party host")
	}
}
