package main

import (
	"crypto/sha256"
	"encoding/hex"
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
	tok := authz.TokenFromContext(ctx)
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

func (c *controllerAPI) expandToken(tok *authorizer.Token) error {
	if c == nil || c.tenancy == nil || tok == nil || tok.ClusterKey || tok.UserID == "" {
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
	if user.ClusterAdmin {
		tok.Scopes = append(tok.Scopes, "cluster:admin")
		return nil
	}
	rows, err := c.tenancy.AccessibleApps(user.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var ledger []access.LedgerRole
		if row.OrgRole != "" && row.OwnerAccount != "" {
			ledger = append(ledger, access.LedgerRole{Subject: row.OwnerAccount, Role: row.OrgRole})
		}
		if row.AppLedgerRole != "" {
			ledger = append(ledger, access.LedgerRole{Subject: "app:" + row.AppID, Role: row.AppLedgerRole})
		}
		res := access.Resolve(access.Input{
			CallerUserID:   user.ID,
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
		tok.AppGrants = unionGrant(tok.AppGrants, row.AppID, res.Permissions)
		if row.Name != "" {
			tok.AppGrants = unionGrant(tok.AppGrants, row.Name, res.Permissions)
		}
	}
	return nil
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
	var scopes []string
	for _, s := range strings.FieldsFunc(p.Scopes, func(r rune) bool { return r == ' ' || r == ',' }) {
		if s != "" {
			scopes = append(scopes, s)
		}
	}
	return &authorizer.Token{ID: p.UserID, User: p.Email, UserID: p.UserID, Scopes: scopes}, nil
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
	var body struct {
		Mode          string `json:"mode"`
		SignupEnabled *bool  `json:"signup_enabled"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := tenancy.ValidateMode(body.Mode); err != nil {
		httphelper.ValidationError(w, "mode", err.Error())
		return
	}
	if err := c.tenancy.SetSettings(body.Mode, body.SignupEnabled); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "tenancy.update", "", "", body)
	c.GetTenancy(ctx, w, req)
}

func (c *controllerAPI) CreateUser(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.UserCreate
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	body.Handle = strings.TrimSpace(strings.ToLower(body.Handle))
	if body.Email == "" || !strings.Contains(body.Email, "@") {
		httphelper.ValidationError(w, "email", "must be an email address")
		return
	}
	if err := tenancy.ValidateHandle(body.Handle); err != nil {
		httphelper.ValidationError(w, "handle", err.Error())
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
		h := strings.TrimSpace(strings.ToLower(*body.Handle))
		if err := tenancy.ValidateHandle(h); err != nil {
			httphelper.ValidationError(w, "handle", err.Error())
			return
		}
		if err := c.tenancy.DeleteHandle(u.Handle); err != nil {
			respondWithError(w, err)
			return
		}
		if err := c.tenancy.ReserveHandle(&ct.Handle{Handle: h, Account: "user:" + u.ID, Kind: "user"}); err != nil {
			_ = c.tenancy.ReserveHandle(&ct.Handle{Handle: u.Handle, Account: "user:" + u.ID, Kind: "user"})
			if err == data.ErrConflict {
				httphelper.ConflictError(w, "handle is already taken")
				return
			}
			respondWithError(w, err)
			return
		}
		u.Handle = h
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
	if body.Password != nil {
		hash, err := tenancy.HashPassword(*body.Password)
		if err != nil {
			httphelper.ValidationError(w, "password", err.Error())
			return
		}
		u.PasswordHash = hash
	}
	if body.Suspended != nil {
		u.Suspended = *body.Suspended
		_ = c.tenancy.SetAccountSuspended("user:"+u.ID, *body.Suspended)
		if *body.Suspended {
			c.scaleAccountToZero("user:" + u.ID)
		}
	}
	if err := c.tenancy.UpdateUser(u); err != nil {
		if err == data.ErrConflict {
			httphelper.ConflictError(w, "email is already taken")
			return
		}
		respondWithError(w, err)
		return
	}
	u.PasswordHash = ""
	_ = c.tenancy.Audit(c.actor(ctx), "user.update", "user:"+u.ID, "", body)
	httphelper.JSON(w, 200, u)
}

func (c *controllerAPI) DeleteUser(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	id := params.ByName("users_id")
	if _, err := c.tenancy.GetUser(id); err != nil {
		respondWithError(w, err)
		return
	}
	n, err := c.tenancy.CountOwnedApps("user:" + id)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if n > 0 {
		httphelper.ConflictError(w, "user still owns personal apps; transfer or delete them first")
		return
	}
	if err := c.tenancy.DeleteUser(id); err != nil {
		respondWithError(w, err)
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
	if tok == nil || tok.ClusterKey || tok.UserID == "" {
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

func (c *controllerAPI) CreateToken(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.UserID == "" {
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
	if tok == nil || tok.UserID == "" {
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
	if tok == nil || tok.UserID == "" {
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
