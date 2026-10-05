package data

import (
	"time"

	"github.com/jackc/pgx"
)

// OAuthCode is a short-lived authorization code (PKCE).
type OAuthCode struct {
	Code          string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	UserID        string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	Nonce         string
	Scopes        string
}

// OAuthRefreshToken is a rotating refresh token for the controller issuer.
type OAuthRefreshToken struct {
	Token     string
	UserID    string
	ClientID  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// OAuthSession is a browser cookie used by GET /oauth/authorize.
type OAuthSession struct {
	Token     string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (r *TenancyRepo) SaveOAuthCode(c *OAuthCode) error {
	return r.db.Exec(`
		INSERT INTO oauth_codes (code, client_id, redirect_uri, code_challenge, user_id, expires_at, created_at, nonce, scopes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		c.Code, c.ClientID, c.RedirectURI, c.CodeChallenge, c.UserID, c.ExpiresAt, c.CreatedAt, c.Nonce, c.Scopes)
}

func (r *TenancyRepo) GetOAuthCode(code string) (*OAuthCode, error) {
	c := &OAuthCode{}
	err := r.db.QueryRow(`
		SELECT code, client_id, redirect_uri, code_challenge, user_id, expires_at, created_at, COALESCE(nonce, ''), COALESCE(scopes, '')
		FROM oauth_codes WHERE code=$1`, code).Scan(
		&c.Code, &c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.UserID, &c.ExpiresAt, &c.CreatedAt, &c.Nonce, &c.Scopes)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

func (r *TenancyRepo) DeleteOAuthCode(code string) error {
	return r.db.Exec(`DELETE FROM oauth_codes WHERE code=$1`, code)
}

func (r *TenancyRepo) CreateRefreshToken(userID, token, clientID string, expires time.Time) (*OAuthRefreshToken, error) {
	rt := &OAuthRefreshToken{Token: token, UserID: userID, ClientID: clientID, ExpiresAt: expires}
	err := r.db.QueryRow(`
		INSERT INTO oauth_refresh_tokens (token, user_id, client_id, expires_at)
		VALUES ($1, $2, $3, $4) RETURNING created_at`,
		token, userID, clientID, expires).Scan(&rt.CreatedAt)
	return rt, err
}

func (r *TenancyRepo) GetRefreshToken(token string) (*OAuthRefreshToken, error) {
	rt := &OAuthRefreshToken{}
	err := r.db.QueryRow(`
		SELECT token, user_id, client_id, created_at, expires_at
		FROM oauth_refresh_tokens WHERE token=$1`, token).Scan(
		&rt.Token, &rt.UserID, &rt.ClientID, &rt.CreatedAt, &rt.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return rt, err
}

func (r *TenancyRepo) DeleteRefreshToken(token string) error {
	return r.db.Exec(`DELETE FROM oauth_refresh_tokens WHERE token=$1`, token)
}

func (r *TenancyRepo) CreateOAuthSession(userID, token string, expires time.Time) error {
	return r.db.Exec(`
		INSERT INTO oauth_sessions (token, user_id, expires_at) VALUES ($1, $2, $3)`,
		token, userID, expires)
}

func (r *TenancyRepo) GetOAuthSession(token string, now time.Time) (*OAuthSession, error) {
	s := &OAuthSession{}
	err := r.db.QueryRow(`
		SELECT token, user_id, created_at, expires_at
		FROM oauth_sessions WHERE token=$1 AND expires_at > $2`, token, now).Scan(
		&s.Token, &s.UserID, &s.CreatedAt, &s.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return s, err
}
