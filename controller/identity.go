package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/access"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
	"golang.org/x/net/context"
)

type apiContextKey struct{}

func apiFromContext(ctx context.Context) *controllerAPI {
	api, _ := ctx.Value(apiContextKey{}).(*controllerAPI)
	return api
}

func (c *controllerAPI) actor(ctx context.Context) string {
	return actorFromToken(authz.TokenFromContext(ctx))
}

func actorFromToken(tok *authorizer.Token) string {
	if tok == nil || tok.ClusterKey {
		return "cluster-key"
	}
	if tok.UserID != "" {
		return "user:" + tok.UserID
	}
	if tok.User != "" {
		return tok.User
	}
	return tok.ID
}

func (c *controllerAPI) requireClusterKey(ctx context.Context, w http.ResponseWriter) bool {
	tok := authz.TokenFromContext(ctx)
	if tok != nil && tok.ClusterKey {
		return true
	}
	httphelper.Forbidden(w, "only the cluster key can change cluster_admin")
	return false
}

func validCollabRole(role string) bool {
	switch role {
	case "view", "deploy", "manage", "admin":
		return true
	default:
		return false
	}
}

// errCredentialRevoked is a login token signed before the user's last
// password change. The middleware answers 401.
var errCredentialRevoked = errors.New("credential revoked by a password change")

func (c *controllerAPI) expandToken(tok *authorizer.Token) error {
	if c == nil || c.tenancy == nil || tok == nil || tok.ClusterKey || tok.UserID == "" {
		return nil
	}
	// A compile credential carries the owner only as a rate-limit identity.
	// Expanding it would hand a build the owner's other apps, cluster:admin
	// for an admin owner, and PAT minting. It keeps the one grant it was minted
	// with.
	if authz.IsBuildCredential(tok) {
		return nil
	}
	user, err := c.tenancy.GetUser(tok.UserID)
	if err == data.ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if user.Disabled || user.Suspended {
		tok.Blocked = true
		tok.AppGrants = nil
		tok.Scopes = nil
		return nil
	}
	// A password change revokes login tokens signed before it.
	if user.TokensValidAfter != nil && !tok.IssuedAt.IsZero() &&
		tok.IssuedAt.Add(credentialRevokeSkew).Before(*user.TokensValidAfter) {
		return errCredentialRevoked
	}
	suspended, err := c.tenancy.AccountSuspended("user:" + user.ID)
	if err != nil {
		return err
	}
	if suspended {
		tok.Blocked = true
		tok.AppGrants = nil
		tok.Scopes = nil
		return nil
	}
	// Admin comes from the user row only. A personal access token row or a
	// login token signed before a demotion must not keep cluster:admin.
	tok.Scopes = withoutAdminScopes(tok.Scopes)
	if user.ClusterAdmin {
		tok.Scopes = append(tok.Scopes, "cluster:admin")
		return nil
	}
	rows, err := c.tenancy.AccessibleApps(user.ID)
	if err != nil {
		return err
	}
	tok.AppGrants = appGrantsFromAccess(user.ID, rows, tok.AppGrants)
	return nil
}

// appGrantsFromAccess folds the apps a user can reach into grants, keyed by
// app id only: a name key would let an app named after another app's id
// collect that app's grants.
func appGrantsFromAccess(userID string, rows []data.AppAccess, grants []authorizer.AppGrant) []authorizer.AppGrant {
	for _, row := range rows {
		var ledger []access.LedgerRole
		if row.OrgRole != "" && row.OwnerAccount != "" {
			ledger = append(ledger, access.LedgerRole{Subject: row.OwnerAccount, Role: row.OrgRole})
		}
		if row.AppLedgerRole != "" {
			ledger = append(ledger, access.LedgerRole{Subject: "app:" + row.AppID, Role: row.AppLedgerRole})
		}
		res := access.Resolve(access.Input{
			CallerUserID:   userID,
			AppID:          row.AppID,
			OwnerAccount:   row.OwnerAccount,
			AccountRole:    row.AccountRole,
			AppRole:        row.AppRole,
			Ledger:         ledger,
			OwnerSuspended: row.OwnerSuspended,
		})
		if len(res.Permissions) == 0 {
			continue
		}
		grants = unionGrant(grants, row.AppID, res.Permissions)
	}
	return grants
}

func unionGrant(grants []authorizer.AppGrant, appID string, perms []string) []authorizer.AppGrant {
	if appID == "" {
		return grants
	}
	for i := range grants {
		if grants[i].AppID != appID {
			continue
		}
		seen := map[string]struct{}{}
		for _, p := range grants[i].Permissions {
			seen[p] = struct{}{}
		}
		for _, p := range perms {
			if _, ok := seen[p]; ok {
				continue
			}
			grants[i].Permissions = append(grants[i].Permissions, p)
		}
		return grants
	}
	return append(grants, authorizer.AppGrant{AppID: appID, Permissions: append([]string(nil), perms...)})
}

func (c *controllerAPI) authenticatePAT(r *http.Request) (*authorizer.Token, error) {
	if c == nil || c.tenancy == nil {
		return nil, authorizer.ErrInvalid
	}
	secret := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		secret = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if secret == "" {
		_, password, _ := r.BasicAuth()
		secret = password
	}
	if !strings.HasPrefix(secret, "flynn_pat_") {
		return nil, authorizer.ErrInvalid
	}
	sum := sha256.Sum256([]byte(secret))
	p, err := c.tenancy.LookupPAT(hex.EncodeToString(sum[:]), time.Now())
	if err != nil {
		return nil, authorizer.ErrInvalid
	}
	// A row written before the allowlist may carry cluster:admin or a service
	// scope. Only allowlisted scopes reach the token.
	var scopes []string
	for _, s := range splitPATScopes(p.Scopes) {
		if patScopeAllowed(s) {
			scopes = append(scopes, s)
		}
	}
	return &authorizer.Token{ID: p.UserID, User: p.Email, UserID: p.UserID, Scopes: scopes}, nil
}

// patAllowedScopes are the scopes a personal access token may store. A PAT
// speaks for its user: cluster admin comes from users.cluster_admin and app
// access from the user's grants in expandToken, never from the token row.
// cluster:admin, *, the service scopes (account:quota, acme:issue) and the
// compile scopes are refused.
var patAllowedScopes = map[string]struct{}{"user": {}}

func patScopeAllowed(s string) bool {
	_, ok := patAllowedScopes[s]
	return ok
}

func splitPATScopes(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
}

// normalizePATScopes checks requested PAT scopes against the allowlist and
// returns them space-separated without duplicates.
func normalizePATScopes(s string) (string, error) {
	var out []string
	seen := map[string]struct{}{}
	for _, scope := range splitPATScopes(s) {
		if !patScopeAllowed(scope) {
			return "", fmt.Errorf("may not include %q; a personal access token allows only: user", scope)
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return strings.Join(out, " "), nil
}

// withoutAdminScopes drops cluster:admin and * so expandToken can derive
// admin from the user row.
func withoutAdminScopes(scopes []string) []string {
	out := scopes[:0:0]
	for _, s := range scopes {
		if s == "cluster:admin" || s == "*" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (c *controllerAPI) GetTenancy(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	s, err := c.tenancy.GetSettings()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, s)
}

func (c *controllerAPI) PutTenancy(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	// signup_enabled is not a field here. This handler writes tenancy_mode
	// only, including when the JSON also contains signup_enabled.
	var body ct.TenancyModeUpdate
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := tenancy.ValidateMode(body.Mode); err != nil {
		httphelper.ValidationError(w, "mode", err.Error())
		return
	}
	if body.Force {
		if body.Mode != tenancy.ModeSelfHosted {
			httphelper.ValidationError(w, "force", "force only applies to mode self_hosted")
			return
		}
		if tok := authz.TokenFromContext(ctx); tok == nil || !tok.ClusterKey {
			httphelper.Forbidden(w, "only the cluster key can force self_hosted")
			return
		}
	}
	from, owned, err := c.unsafeSelfHostedReversal(body.Mode)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if owned > 0 {
		if !body.Force {
			respondWithError(w, ct.ValidationError{
				Field:   "mode",
				Message: "refusing self_hosted while a non-system app has an owner_account; this is not a safe reversal and does not delete quota rows or clear reserve_resources (the cluster key can override with force: flynn-host tenancy:mode --force self_hosted)",
			})
			return
		}
		// The override is recorded before the mode changes, so a forced
		// switch is never left without its audit row.
		if err := c.tenancy.Audit(c.actor(ctx), "tenancy.force_self_hosted", "", "", map[string]interface{}{
			"from":       from,
			"mode":       body.Mode,
			"force":      true,
			"owned_apps": owned,
			"warning":    ForceSelfHostedWarning,
		}); err != nil {
			respondWithError(w, err)
			return
		}
	}
	if err := c.tenancy.SetSettings(body.Mode, nil); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "tenancy.update", "", "", map[string]interface{}{"mode": body.Mode, "force": body.Force})
	c.GetTenancy(ctx, w, req)
}

// ForceSelfHostedWarning is what a forced hosted → self_hosted switch does.
// It is stored on the tenancy.force_self_hosted audit row.
const ForceSelfHostedWarning = "owner_account is not cleared; account_quotas rows stay and still apply; an account with no quota row becomes unlimited immediately, including while its apps keep running; reserve_resources is not cleared; running jobs are not rescheduled"

// unsafeSelfHostedReversal reports the current mode and how many non-system
// apps still have an owner when mode is self_hosted and the cluster is
// hosted. That switch makes accounts with no quota row unlimited
// immediately, including while apps are still running. It does not delete
// quota rows or clear reserve_resources, and it is not a safe reversal, so
// PutTenancy refuses it unless the cluster key sends force. A cluster that
// is already self_hosted is left alone (owned is 0).
func (c *controllerAPI) unsafeSelfHostedReversal(mode string) (string, int, error) {
	if c == nil || c.tenancy == nil || mode != tenancy.ModeSelfHosted {
		return "", 0, nil
	}
	current, err := c.tenancy.GetSettings()
	if err != nil {
		return "", 0, err
	}
	if current.Mode != tenancy.ModeHosted {
		return current.Mode, 0, nil
	}
	n, err := c.tenancy.CountNonSystemOwnedApps()
	if err != nil {
		return "", 0, err
	}
	return current.Mode, n, nil
}

func (c *controllerAPI) CreateUser(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.UserCreate
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	if body.Email == "" || !strings.Contains(body.Email, "@") {
		httphelper.ValidationError(w, "email", "must be an email address")
		return
	}
	body.Handle = tenancy.HandleFromEmail(body.Email)
	if err := tenancy.ValidateHandle(body.Handle); err != nil {
		httphelper.ValidationError(w, "email", err.Error())
		return
	}
	if body.ClusterAdmin && !c.requireClusterKey(ctx, w) {
		return
	}
	hash, err := tenancy.HashPassword(body.Password)
	if err != nil {
		httphelper.ValidationError(w, "password", err.Error())
		return
	}
	u := &ct.User{
		ID:            random.UUID(),
		Handle:        body.Handle,
		Email:         body.Email,
		PasswordHash:  hash,
		ClusterAdmin:  body.ClusterAdmin,
		EmailVerified: true,
	}
	if err := c.tenancy.CreateUser(u); err != nil {
		if err == data.ErrConflict {
			httphelper.ConflictError(w, "handle or email is already taken")
			return
		}
		respondWithError(w, err)
		return
	}
	u.PasswordHash = ""
	_ = c.tenancy.Audit(c.actor(ctx), "user.create", "user:"+u.ID, "", map[string]string{"handle": u.Handle})
	httphelper.JSON(w, 200, u)
}

func (c *controllerAPI) ListUsers(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	users, err := c.tenancy.ListUsers()
	if err != nil {
		respondWithError(w, err)
		return
	}
	if users == nil {
		users = []*ct.User{}
	}
	for _, u := range users {
		u.PasswordHash = ""
	}
	httphelper.JSON(w, 200, users)
}

func (c *controllerAPI) GetUser(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	u, err := c.tenancy.GetUser(params.ByName("users_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	u.PasswordHash = ""
	httphelper.JSON(w, 200, u)
}

func (c *controllerAPI) PatchUser(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	u, err := c.tenancy.GetUser(params.ByName("users_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	var body ct.UserPatch
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if body.ClusterAdmin != nil && !c.requireClusterKey(ctx, w) {
		return
	}
	if body.Handle != nil {
		httphelper.ValidationError(w, "handle", "user handles are not used; sign in with email")
		return
	}
	if body.Email != nil {
		u.Email = strings.TrimSpace(strings.ToLower(*body.Email))
	}
	if body.Disabled != nil {
		u.Disabled = *body.Disabled
	}
	if body.EmailVerified != nil {
		u.EmailVerified = *body.EmailVerified
	}
	if body.ClusterAdmin != nil {
		u.ClusterAdmin = *body.ClusterAdmin
	}
	// Setting the password the user already has is not a change: the
	// dashboard re-sends it when it syncs a user, and that must not sign the
	// user out everywhere.
	passwordChanged := false
	if body.Password != nil && !tenancy.CheckPassword(u.PasswordHash, *body.Password) {
		hash, err := tenancy.HashPassword(*body.Password)
		if err != nil {
			httphelper.ValidationError(w, "password", err.Error())
			return
		}
		u.PasswordHash = hash
		passwordChanged = true
	}
	if body.Suspended != nil {
		u.Suspended = *body.Suspended
		if err := c.tenancy.SetAccountSuspended("user:"+u.ID, *body.Suspended); err != nil {
			respondWithError(w, err)
			return
		}
	}
	// A changed password revokes every credential the user holds, the
	// caller's own included: login and refresh tokens, personal access
	// tokens, and /oauth/authorize sessions. The dashboard keeps its own
	// session and mints fresh login tokens, so a dashboard user stays signed
	// in there; the CLI must run flynn login again.
	update := c.tenancy.UpdateUser
	if passwordChanged {
		update = func(u *ct.User) error { return c.tenancy.UpdateUserRevokingCredentials(u, time.Now()) }
	}
	if err := update(u); err != nil {
		if err == data.ErrConflict {
			httphelper.ConflictError(w, "email is already taken")
			return
		}
		respondWithError(w, err)
		return
	}
	// Scale down only once the user row says suspended, as
	// SuspendAccount does; a failure is returned so the caller retries.
	var scaleErr error
	if body.Suspended != nil && *body.Suspended {
		scaleErr = c.scaleAccountToZero("user:" + u.ID)
	}
	u.PasswordHash = ""
	audit := userUpdateAudit(&body)
	audit.CredentialsRevoked = passwordChanged
	_ = c.tenancy.Audit(c.actor(ctx), "user.update", "user:"+u.ID, "", audit)
	if scaleErr != nil {
		respondWithError(w, scaleErr)
		return
	}
	httphelper.JSON(w, 200, u)
}

// userUpdateAuditData is the user.update core_audit row. It records that the
// password changed, never the password.
type userUpdateAuditData struct {
	Disabled        *bool   `json:"disabled"`
	ClusterAdmin    *bool   `json:"cluster_admin"`
	Handle          *string `json:"handle"`
	Email           *string `json:"email"`
	EmailVerified   *bool   `json:"email_verified"`
	Suspended       *bool   `json:"suspended"`
	PasswordChanged bool    `json:"password_changed,omitempty"`
	// CredentialsRevoked is set when the password really changed and the
	// user's tokens and sessions were revoked.
	CredentialsRevoked bool `json:"credentials_revoked,omitempty"`
}

func userUpdateAudit(p *ct.UserPatch) *userUpdateAuditData {
	return &userUpdateAuditData{
		Disabled:        p.Disabled,
		ClusterAdmin:    p.ClusterAdmin,
		Handle:          p.Handle,
		Email:           p.Email,
		EmailVerified:   p.EmailVerified,
		Suspended:       p.Suspended,
		PasswordChanged: p.Password != nil,
	}
}

func (c *controllerAPI) DeleteUser(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	id := params.ByName("users_id")
	if _, err := c.tenancy.GetUser(id); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.tenancy.DeleteUser(id); err != nil {
		var inUse *data.UserInUseError
		switch {
		case err == data.ErrUserOwnsApps:
			httphelper.ConflictError(w, "user still owns personal apps; transfer or delete them first")
		case errors.As(err, &inUse):
			// Not the raw SQL error (CORE-51): name the table, keep the 409.
			msg := "user is still referenced"
			if inUse.Table != "" {
				msg += " by " + inUse.Table
			}
			httphelper.ConflictError(w, msg+"; remove those references first")
		default:
			respondWithError(w, err)
		}
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "user.delete", "user:"+id, "", nil)
	w.WriteHeader(200)
}

func (c *controllerAPI) BootstrapUserToken(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	u, err := c.tenancy.GetUser(params.ByName("users_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	c.issuePAT(ctx, w, u.ID, ct.TokenCreate{Name: "bootstrap", Scopes: "user"})
}

func (c *controllerAPI) WhoAmI(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || !authz.UserBound(tok) {
		httphelper.JSON(w, 200, ct.WhoAmI{User: ct.User{Handle: "cluster", ClusterAdmin: true}, Account: ""})
		return
	}
	u, err := c.tenancy.GetUser(tok.UserID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	u.PasswordHash = ""
	httphelper.JSON(w, 200, ct.WhoAmI{User: *u, Account: "user:" + u.ID})
}

// WhoAmIGrants is GET /whoami/grants: the caller's grants after expandToken,
// keyed by app id. gitreceive forwards a tenant's credential here, because a
// login token carries no grants and a personal access token is only known to
// the controller. Compile and service credentials never reach it (the route
// is a user session route).
func (c *controllerAPI) WhoAmIGrants(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	tok := authz.TokenFromContext(ctx)
	out := ct.CallerGrants{AppGrants: []ct.CallerAppGrant{}}
	if tok == nil || tok.HasClusterAdmin() {
		out.ClusterAdmin = true
		if tok != nil {
			out.UserID = tok.UserID
		}
		httphelper.JSON(w, 200, out)
		return
	}
	out.UserID = tok.UserID
	for _, g := range tok.AppGrants {
		if g.AppID == "" || len(g.Permissions) == 0 {
			continue
		}
		out.AppGrants = append(out.AppGrants, ct.CallerAppGrant{AppID: g.AppID, Permissions: append([]string(nil), g.Permissions...)})
	}
	httphelper.JSON(w, 200, out)
}

func (c *controllerAPI) CreateToken(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	tok := authz.TokenFromContext(ctx)
	if !authz.UserBound(tok) {
		httphelper.Forbidden(w, "personal access tokens require a user")
		return
	}
	var body ct.TokenCreate
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	c.issuePAT(ctx, w, tok.UserID, body)
}

func (c *controllerAPI) issuePAT(ctx context.Context, w http.ResponseWriter, userID string, body ct.TokenCreate) {
	if strings.TrimSpace(body.Name) == "" {
		body.Name = "token"
	}
	scopes, err := normalizePATScopes(body.Scopes)
	if err != nil {
		httphelper.ValidationError(w, "scopes", err.Error())
		return
	}
	body.Scopes = scopes
	settings, err := c.tenancy.GetSettings()
	if err != nil {
		respondWithError(w, err)
		return
	}
	expires, err := c.patLifetime.resolve(time.Now(), settings.Mode, body.ExpiresAt)
	if err != nil {
		httphelper.ValidationError(w, "expires_at", err.Error())
		return
	}
	body.ExpiresAt = expires
	material := "flynn_pat_" + random.Hex(32)
	sum := sha256.Sum256([]byte(material))
	rec, err := c.tenancy.CreatePAT(userID, random.UUID(), body.Name, body.Scopes, hex.EncodeToString(sum[:]), body.ExpiresAt)
	if err != nil {
		respondWithError(w, err)
		return
	}
	rec.Token = material
	_ = c.tenancy.Audit(c.actor(ctx), "token.create", "user:"+userID, "", map[string]string{"id": rec.ID, "name": rec.Name})
	httphelper.JSON(w, 200, rec)
}

func (c *controllerAPI) ListTokens(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	tok := authz.TokenFromContext(ctx)
	if !authz.UserBound(tok) {
		httphelper.JSON(w, 200, []ct.AccessTokenRecord{})
		return
	}
	recs, err := c.tenancy.ListPATs(tok.UserID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if recs == nil {
		recs = []ct.AccessTokenRecord{}
	}
	httphelper.JSON(w, 200, recs)
}

func (c *controllerAPI) DeleteToken(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	tok := authz.TokenFromContext(ctx)
	params, _ := ctxhelper.ParamsFromContext(ctx)
	if !authz.UserBound(tok) {
		httphelper.Forbidden(w, "personal access tokens require a user")
		return
	}
	if err := c.tenancy.RevokePAT(tok.UserID, params.ByName("tokens_id")); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "token.revoke", "user:"+tok.UserID, "", map[string]string{"id": params.ByName("tokens_id")})
	w.WriteHeader(200)
}
