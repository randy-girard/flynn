package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
	"golang.org/x/net/context"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	oauthSessionCookie  = "flynn_oauth_session"
	oauthCodeTTL        = 10 * time.Minute
	oauthRefreshTTL     = 30 * 24 * time.Hour
	oauthSessionTTL     = 12 * time.Hour
	oauthAccessTokenTTL = 55 * time.Minute
	oauthClientID       = "flynn-cli"
)

func oauthPublicPath(method, path string) bool {
	switch path {
	case "/.well-known/oauth-authorization-server":
		return method == http.MethodGet || method == http.MethodHead
	case "/oauth/authorize":
		return method == http.MethodGet || method == http.MethodHead || method == http.MethodPost
	case "/oauth/token":
		return method == http.MethodPost
	case "/oauth/audiences":
		return method == http.MethodGet || method == http.MethodHead
	default:
		return false
	}
}

func (c *controllerAPI) oauthIssuer(r *http.Request) string {
	domain := strings.TrimSpace(os.Getenv("DEFAULT_ROUTE_DOMAIN"))
	host := ""
	if r != nil {
		host = r.Host
	}
	if domain != "" && (host == "" || strings.HasPrefix(host, "controller.") || strings.HasPrefix(host, "auth.")) {
		return "https://auth." + domain
	}
	scheme := "http"
	if r != nil && r.TLS != nil {
		scheme = "https"
	}
	if r != nil && r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	if host == "" {
		return "https://auth.local"
	}
	return scheme + "://" + host
}

func (c *controllerAPI) oauthAudience(r *http.Request) string {
	if domain := strings.TrimSpace(os.Getenv("DEFAULT_ROUTE_DOMAIN")); domain != "" {
		return "https://controller." + domain
	}
	host := ""
	if r != nil {
		host = r.Host
	}
	if strings.HasPrefix(host, "auth.") {
		host = "controller." + strings.TrimPrefix(host, "auth.")
	}
	if host == "" {
		return c.oauthIssuer(r)
	}
	scheme := "http"
	if r != nil && r.TLS != nil {
		scheme = "https"
	}
	if r != nil && r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + host
}

func (c *controllerAPI) OAuthMetadata(_ context.Context, w http.ResponseWriter, req *http.Request) {
	issuer := c.oauthIssuer(req)
	httphelper.JSON(w, 200, map[string]interface{}{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"audiences_endpoint":                    issuer + "/oauth/audiences",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "password"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "none"},
	})
}

func (c *controllerAPI) OAuthAudiences(_ context.Context, w http.ResponseWriter, req *http.Request) {
	httphelper.JSON(w, 200, map[string]interface{}{
		"audiences": []map[string]string{{
			"url":  c.oauthAudience(req),
			"name": "Flynn controller",
			"type": "flynn_controller",
		}},
	})
}

func (c *controllerAPI) OAuthAuthorize(_ context.Context, w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost {
		c.oauthAuthorizeLogin(w, req)
		return
	}
	q := req.URL.Query()
	if err := validateAuthorizeQuery(q); err != nil {
		httphelper.ValidationError(w, "oauth", err.Error())
		return
	}
	userID := c.oauthSessionUser(req)
	if userID == "" {
		c.writeAuthorizeLoginForm(w, q, "")
		return
	}
	u, err := c.tenancy.GetUser(userID)
	if err != nil || u == nil || u.Disabled || u.Suspended {
		c.writeAuthorizeLoginForm(w, q, "")
		return
	}
	c.completeAuthorize(w, req, q, u)
}

func (c *controllerAPI) oauthAuthorizeLogin(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		httphelper.ValidationError(w, "oauth", "invalid form")
		return
	}
	q := req.Form
	if err := validateAuthorizeQuery(q); err != nil {
		httphelper.ValidationError(w, "oauth", err.Error())
		return
	}
	identifier := firstNonEmpty(q.Get("login"), q.Get("email"), q.Get("username"))
	password := q.Get("password")
	u, err := c.authenticatePassword(identifier, password)
	if err != nil {
		c.writeAuthorizeLoginForm(w, q, "invalid credentials")
		return
	}
	tok := random.Hex(32)
	if err := c.tenancy.CreateOAuthSession(u.ID, tok, time.Now().Add(oauthSessionTTL)); err != nil {
		httphelper.Error(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthSessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https",
		Expires:  time.Now().Add(oauthSessionTTL),
	})
	c.completeAuthorize(w, req, q, u)
}

func (c *controllerAPI) completeAuthorize(w http.ResponseWriter, req *http.Request, q url.Values, u *ct.User) {
	code := random.Hex(32)
	oc := &data.OAuthCode{
		Code:          code,
		ClientID:      q.Get("client_id"),
		RedirectURI:   q.Get("redirect_uri"),
		CodeChallenge: q.Get("code_challenge"),
		UserID:        u.ID,
		ExpiresAt:     time.Now().Add(oauthCodeTTL),
		CreatedAt:     time.Now(),
		Nonce:         q.Get("nonce"),
		Scopes:        q.Get("scope"),
	}
	if err := c.tenancy.SaveOAuthCode(oc); err != nil {
		httphelper.Error(w, err)
		return
	}
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	if isOAuthOutOfBandRedirect(redirectURI) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, oobAuthorizeHTML(code, state, redirectURI))
		return
	}
	loc, err := oauthHTTPRedirectLocation(redirectURI, code, state)
	if err != nil {
		httphelper.ValidationError(w, "redirect_uri", err.Error())
		return
	}
	http.Redirect(w, req, loc, http.StatusFound)
}

func (c *controllerAPI) oauthSessionUser(req *http.Request) string {
	cookie, err := req.Cookie(oauthSessionCookie)
	if err != nil || cookie.Value == "" {
		return ""
	}
	sess, err := c.tenancy.GetOAuthSession(cookie.Value, time.Now())
	if err != nil || sess == nil {
		return ""
	}
	return sess.UserID
}

func validateAuthorizeQuery(q url.Values) error {
	if q.Get("response_type") != "code" {
		return fmt.Errorf("unsupported response_type")
	}
	if q.Get("code_challenge") == "" {
		return fmt.Errorf("code_challenge required (PKCE)")
	}
	if q.Get("code_challenge_method") != "S256" {
		return fmt.Errorf("code_challenge_method must be S256")
	}
	if strings.TrimSpace(q.Get("redirect_uri")) == "" {
		return fmt.Errorf("redirect_uri required")
	}
	return nil
}

func (c *controllerAPI) writeAuthorizeLoginForm(w http.ResponseWriter, q url.Values, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	fmt.Fprint(w, authorizeLoginHTML(q, errMsg))
}

func (c *controllerAPI) OAuthToken(_ context.Context, w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		oauthTokenError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	switch req.FormValue("grant_type") {
	case "authorization_code":
		c.oauthTokenAuthCode(w, req)
	case "refresh_token":
		c.oauthTokenRefresh(w, req)
	case "password":
		c.oauthTokenPassword(w, req)
	default:
		oauthTokenError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

func (c *controllerAPI) oauthTokenAuthCode(w http.ResponseWriter, req *http.Request) {
	code := req.FormValue("code")
	oc, err := c.tenancy.GetOAuthCode(code)
	if err != nil || oc == nil {
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "invalid code")
		return
	}
	if time.Now().After(oc.ExpiresAt) {
		_ = c.tenancy.DeleteOAuthCode(code)
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "code expired")
		return
	}
	if oc.RedirectURI != req.FormValue("redirect_uri") {
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "redirect URI mismatch")
		return
	}
	if !verifyPKCE(req.FormValue("code_verifier"), oc.CodeChallenge) {
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "invalid code_verifier")
		return
	}
	_ = c.tenancy.DeleteOAuthCode(code)
	u, err := c.tenancy.GetUser(oc.UserID)
	if err != nil || u == nil {
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "user not found")
		return
	}
	cid := oc.ClientID
	if cid == "" {
		cid = firstNonEmpty(req.FormValue("client_id"), oauthClientID)
	}
	c.writeOAuthTokens(w, req, u, cid, oc.Nonce)
}

func (c *controllerAPI) oauthTokenRefresh(w http.ResponseWriter, req *http.Request) {
	raw := req.FormValue("refresh_token")
	rt, err := c.tenancy.GetRefreshToken(raw)
	if err != nil || rt == nil {
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		return
	}
	if time.Now().After(rt.ExpiresAt) {
		_ = c.tenancy.DeleteRefreshToken(raw)
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "refresh token expired")
		return
	}
	u, err := c.tenancy.GetUser(rt.UserID)
	if err != nil || u == nil {
		oauthTokenError(w, http.StatusBadRequest, "invalid_grant", "user not found")
		return
	}
	if err := c.loginBlocked(u); err != nil {
		oauthTokenError(w, http.StatusForbidden, "invalid_grant", err.Error())
		return
	}
	_ = c.tenancy.DeleteRefreshToken(raw)
	cid := firstNonEmpty(req.FormValue("client_id"), rt.ClientID, oauthClientID)
	c.writeOAuthTokens(w, req, u, cid, "")
}

func (c *controllerAPI) oauthTokenPassword(w http.ResponseWriter, req *http.Request) {
	identifier := firstNonEmpty(req.FormValue("username"), req.FormValue("login"), req.FormValue("email"))
	u, err := c.authenticatePassword(identifier, req.FormValue("password"))
	if err != nil {
		oauthTokenError(w, http.StatusUnauthorized, "invalid_grant", "invalid credentials")
		return
	}
	cid := firstNonEmpty(req.FormValue("client_id"), oauthClientID)
	c.writeOAuthTokens(w, req, u, cid, "")
}

func (c *controllerAPI) writeOAuthTokens(w http.ResponseWriter, req *http.Request, u *ct.User, clientID, nonce string) {
	if err := c.loginBlocked(u); err != nil {
		oauthTokenError(w, http.StatusForbidden, "invalid_grant", err.Error())
		return
	}
	access, expiresIn, err := c.mintUserAccessToken(clientID, u)
	if err != nil {
		log.Printf("oauth: failed to mint token: %v", err)
		desc := "failed to mint token"
		if c.tokenSigner == nil {
			desc = "cluster login signing key is not configured"
		}
		oauthTokenError(w, http.StatusInternalServerError, "server_error", desc)
		return
	}
	refresh := "flynn_rt_" + random.Hex(32)
	if _, err := c.tenancy.CreateRefreshToken(u.ID, refresh, clientID, time.Now().Add(oauthRefreshTTL)); err != nil {
		oauthTokenError(w, http.StatusInternalServerError, "server_error", "failed to save refresh token")
		return
	}
	audience := req.FormValue("audience")
	if audience == "" {
		audience = c.oauthAudience(req)
	}
	resp := map[string]interface{}{
		"access_token":             access,
		"token_type":               "Bearer",
		"expires_in":               expiresIn,
		"refresh_token":            refresh,
		"refresh_token_expires_in": int(oauthRefreshTTL.Seconds()),
		"refresh_token_issue_time": time.Now().UTC().Format(time.RFC3339Nano),
		"audience":                 audience,
	}
	if nonce != "" {
		resp["nonce"] = nonce
	}
	httphelper.JSON(w, 200, resp)
}

func (c *controllerAPI) mintUserAccessToken(clientID string, u *ct.User) (string, int, error) {
	if c.tokenSigner == nil {
		return "", 0, fmt.Errorf("no signing key configured")
	}
	if clientID == "" {
		clientID = oauthClientID
	}
	now := time.Now().UTC()
	exp := now.Add(oauthAccessTokenTTL)
	tok := &api.AccessToken{
		ClientId:   clientID,
		UserId:     u.ID,
		UserEmail:  u.Email,
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(exp),
	}
	if u.ClusterAdmin {
		tok.Scopes = []string{"cluster:admin"}
	}
	signed, err := c.tokenSigner.Sign(tok)
	if err != nil {
		return "", 0, err
	}
	return signed, int(exp.Sub(now).Seconds()), nil
}

func (c *controllerAPI) authenticatePassword(identifier, password string) (*ct.User, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || password == "" {
		return nil, fmt.Errorf("invalid credentials")
	}
	u, err := c.lookupLoginUser(identifier)
	if err != nil || u == nil {
		return nil, fmt.Errorf("invalid credentials")
	}
	if u.Disabled || u.Suspended {
		return nil, fmt.Errorf("invalid credentials")
	}
	if !tenancy.CheckPassword(u.PasswordHash, password) {
		return nil, fmt.Errorf("invalid credentials")
	}
	if err := c.loginBlocked(u); err != nil {
		return nil, err
	}
	return u, nil
}

func (c *controllerAPI) loginBlocked(u *ct.User) error {
	if u.Disabled || u.Suspended {
		return fmt.Errorf("invalid credentials")
	}
	s, err := c.tenancy.GetSettings()
	if err != nil {
		return err
	}
	if s.Mode == tenancy.ModeHosted && !u.EmailVerified {
		return fmt.Errorf("verify your email before logging in")
	}
	return nil
}

func (c *controllerAPI) lookupLoginUser(identifier string) (*ct.User, error) {
	id := strings.ToLower(strings.TrimSpace(identifier))
	if id == "" || !strings.Contains(id, "@") {
		return nil, data.ErrNotFound
	}
	return c.tenancy.GetUserByEmail(id)
}

func oauthTokenError(w http.ResponseWriter, status int, code, desc string) {
	httphelper.JSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func verifyPKCE(codeVerifier, codeChallenge string) bool {
	if codeVerifier == "" || codeChallenge == "" {
		return false
	}
	n := len(codeVerifier)
	if n < 43 || n > 128 || n != utf8.RuneCountInString(codeVerifier) {
		return false
	}
	for i := 0; i < n; i++ {
		switch c := codeVerifier[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}
	sum := sha256.Sum256([]byte(codeVerifier))
	enc := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(enc), []byte(codeChallenge)) == 1
}

func isOAuthOutOfBandRedirect(redirectURI string) bool {
	s := strings.TrimSpace(redirectURI)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if lower == "urn:ietf:wg:oauth:2.0:oob" || strings.HasPrefix(lower, "urn:ietf:wg:oauth:2.0:oob?") {
		return true
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	if u.Scheme == "urn" {
		op := strings.ToLower(strings.TrimPrefix(u.Opaque, "//"))
		return op == "ietf:wg:oauth:2.0:oob" || strings.HasPrefix(op, "ietf:wg:oauth:2.0:oob?")
	}
	return false
}

func oauthHTTPRedirectLocation(redirectURI, code, state string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(redirectURI))
	if err != nil {
		return "", err
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", base.Scheme)
	}
	if base.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	q := base.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	base.RawQuery = q.Encode()
	return base.String(), nil
}
