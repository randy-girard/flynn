package main

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/access"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
	"golang.org/x/net/context"
)

func (c *controllerAPI) SuspendAccount(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	account := accountParam(ctx)
	if err := c.tenancy.SetAccountSuspended(account, true); err != nil {
		respondWithError(w, err)
		return
	}
	if id, ok := strings.CutPrefix(account, "user:"); ok {
		if u, err := c.tenancy.GetUser(id); err == nil {
			u.Suspended = true
			_ = c.tenancy.UpdateUser(u)
		}
	}
	c.scaleAccountToZero(account)
	_ = c.tenancy.Audit(c.actor(ctx), "account.suspend", account, "", map[string]string{
		"intent": "scale_to_zero",
	})
	w.WriteHeader(200)
}

func (c *controllerAPI) UnsuspendAccount(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	account := accountParam(ctx)
	if err := c.tenancy.SetAccountSuspended(account, false); err != nil {
		respondWithError(w, err)
		return
	}
	if id, ok := strings.CutPrefix(account, "user:"); ok {
		if u, err := c.tenancy.GetUser(id); err == nil {
			u.Suspended = false
			_ = c.tenancy.UpdateUser(u)
		}
	}
	_ = c.tenancy.Audit(c.actor(ctx), "account.unsuspend", account, "", map[string]string{
		"intent": "restore_logins_only",
	})
	w.WriteHeader(200)
}

func (c *controllerAPI) scaleAccountToZero(account string) {
	list, err := c.appRepo.List()
	if err != nil {
		return
	}
	apps, _ := list.([]*ct.App)
	for _, app := range apps {
		if app.OwnerAccount != account || app.System() {
			continue
		}
		forms, err := c.formationRepo.List(app.ID)
		if err != nil {
			continue
		}
		for _, f := range forms {
			relRaw, err := c.releaseRepo.Get(f.ReleaseID)
			if err != nil {
				continue
			}
			rel, _ := relRaw.(*ct.Release)
			if rel == nil {
				continue
			}
			zeros := map[string]int{}
			for name := range rel.Processes {
				zeros[name] = 0
			}
			if len(zeros) == 0 {
				continue
			}
			req := newScaleRequest(&ct.Formation{AppID: app.ID, ReleaseID: f.ReleaseID, Processes: zeros}, rel)
			_, _ = c.formationRepo.AddScaleRequest(req, false)
		}
	}
}

func (c *controllerAPI) GetQuota(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	account := accountParam(ctx)
	settings, err := c.tenancy.GetSettings()
	if err != nil {
		respondWithError(w, err)
		return
	}
	stored, err := c.tenancy.GetQuota(account)
	if err != nil && err != data.ErrNotFound {
		respondWithError(w, err)
		return
	}
	var explicit *tenancy.Limits
	source := "unlimited"
	if stored != nil {
		explicit = quotaLimits(stored)
		source = "explicit"
	} else if settings.Mode == tenancy.ModeHosted {
		source = "hosted_default"
	}
	limits, err := tenancy.EffectiveLimits(settings.Mode, explicit)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, ct.AccountQuota{
		Account:          account,
		MaxApps:          limits.MaxApps,
		MaxProcesses:     limits.MaxProcesses,
		MaxMemoryMB:      limits.MaxMemoryMB,
		MaxResources:     limits.MaxResources,
		MaxCollaborators: limits.MaxCollaborators,
		Source:           source,
	})
}

func (c *controllerAPI) PutQuota(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	account := accountParam(ctx)
	var body ct.AccountQuota
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	body.Account = account
	if _, err := tenancy.EffectiveLimits(tenancy.ModeSelfHosted, quotaLimits(&body)); err != nil {
		httphelper.ValidationError(w, "quota", err.Error())
		return
	}
	if err := c.tenancy.SetQuota(&body); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "quota.set", account, "", body)
	c.GetQuota(ctx, w, req)
}

func quotaLimits(q *ct.AccountQuota) *tenancy.Limits {
	if q == nil {
		return nil
	}
	return &tenancy.Limits{
		MaxApps: q.MaxApps, MaxProcesses: q.MaxProcesses, MaxMemoryMB: q.MaxMemoryMB,
		MaxResources: q.MaxResources, MaxCollaborators: q.MaxCollaborators,
	}
}

func (c *controllerAPI) CreateHandle(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.Handle
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	body.Handle = strings.TrimSpace(strings.ToLower(body.Handle))
	if err := tenancy.ValidateHandle(body.Handle); err != nil {
		httphelper.ValidationError(w, "handle", err.Error())
		return
	}
	switch body.Kind {
	case "user", "org", "enterprise_account":
	default:
		httphelper.ValidationError(w, "kind", "must be user, org, or enterprise_account")
		return
	}
	if body.Account == "" {
		httphelper.ValidationError(w, "account", "is required")
		return
	}
	if err := c.tenancy.ReserveHandle(&body); err != nil {
		if err == data.ErrConflict {
			httphelper.ConflictError(w, "handle is already taken")
			return
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, body)
}

func (c *controllerAPI) DeleteHandle(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	if err := c.tenancy.DeleteHandle(params.ByName("handles_id")); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}

func (c *controllerAPI) GetHandle(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	h, err := c.tenancy.GetHandle(params.ByName("handles_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, h)
}

func (c *controllerAPI) PutMembership(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !c.requireClusterKey(ctx, w) {
		return
	}
	var body ct.Membership
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if body.UserID == "" || body.Subject == "" || body.Role == "" {
		httphelper.ValidationError(w, "membership", "user_id, subject, and role are required")
		return
	}
	if err := c.tenancy.PutMembership(body); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, body)
}

func (c *controllerAPI) DeleteMembership(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !c.requireClusterKey(ctx, w) {
		return
	}
	q := req.URL.Query()
	if err := c.tenancy.DeleteMembership(q.Get("user_id"), q.Get("subject")); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}

func (c *controllerAPI) ListMemberships(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	rows, err := c.tenancy.ListMemberships(req.URL.Query().Get("user_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	if rows == nil {
		rows = []ct.Membership{}
	}
	httphelper.JSON(w, 200, rows)
}

func accountParam(ctx context.Context) string {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	return params.ByName("accounts_id")
}

func (c *controllerAPI) accessFor(ctx context.Context, app *ct.App) access.Result {
	tok := authz.TokenFromContext(ctx)
	owner := ""
	appID := ""
	if app != nil {
		owner = app.OwnerAccount
		appID = app.ID
	}
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		return access.Resolve(access.Input{ClusterAdmin: true, CallerUserID: "cluster", AppID: appID, OwnerAccount: owner})
	}
	if c.tenancy == nil || tok.UserID == "" {
		return access.Result{}
	}
	rows, err := c.tenancy.AccessibleApps(tok.UserID)
	if err != nil {
		return access.Result{}
	}
	for _, row := range rows {
		if app != nil && row.AppID != app.ID {
			continue
		}
		var ledger []access.LedgerRole
		if row.OrgRole != "" && row.OwnerAccount != "" {
			ledger = append(ledger, access.LedgerRole{Subject: row.OwnerAccount, Role: row.OrgRole})
		}
		if row.AppLedgerRole != "" {
			ledger = append(ledger, access.LedgerRole{Subject: "app:" + row.AppID, Role: row.AppLedgerRole})
		}
		return access.Resolve(access.Input{
			CallerUserID:   tok.UserID,
			AppID:          row.AppID,
			OwnerAccount:   row.OwnerAccount,
			AccountRole:    row.AccountRole,
			AppRole:        row.AppRole,
			Ledger:         ledger,
			OwnerSuspended: row.OwnerSuspended,
		})
	}
	if app != nil {
		return access.Resolve(access.Input{CallerUserID: tok.UserID, AppID: app.ID, OwnerAccount: app.OwnerAccount})
	}
	return access.Result{}
}

func (c *controllerAPI) accessForAccount(ctx context.Context, account string) access.Result {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		return access.Resolve(access.Input{ClusterAdmin: true, CallerUserID: "cluster", OwnerAccount: account})
	}
	if c.tenancy == nil || tok.UserID == "" {
		return access.Result{}
	}
	var role string
	if cols, err := c.tenancy.ListAccountCollaborators(account); err == nil {
		for _, col := range cols {
			if col.UserID == tok.UserID {
				role = col.Role
			}
		}
	}
	var ledger []access.LedgerRole
	if rows, err := c.tenancy.ListMemberships(tok.UserID); err == nil {
		for _, row := range rows {
			if row.Subject == account {
				ledger = append(ledger, access.LedgerRole{Subject: row.Subject, Role: row.Role})
			}
		}
	}
	suspended, _ := c.tenancy.AccountSuspended(account)
	return access.Resolve(access.Input{
		CallerUserID:    tok.UserID,
		OwnerAccount:    account,
		AccountRole:     role,
		Ledger:          ledger,
		OwnerSuspended:  suspended,
		CallerSuspended: false,
	})
}

func (c *controllerAPI) ListAccountCollaborators(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	account := accountParam(ctx)
	if !c.canAdminAccount(ctx, w, account) {
		return
	}
	rows, err := c.tenancy.ListAccountCollaborators(account)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if rows == nil {
		rows = []ct.Collaborator{}
	}
	httphelper.JSON(w, 200, rows)
}

func (c *controllerAPI) canAdminAccount(ctx context.Context, w http.ResponseWriter, account string) bool {
	res := c.accessForAccount(ctx, account)
	if access.Has(res.Permissions, access.PermAppAdmin) || res.ImplicitOwner || res.OrgManager {
		return true
	}
	tok := authz.TokenFromContext(ctx)
	if tok != nil && (tok.ClusterKey || tok.HasClusterAdmin()) {
		return true
	}
	httphelper.Forbidden(w, "admin on the account is required")
	return false
}

func (c *controllerAPI) AddAccountCollaborator(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	account := accountParam(ctx)
	if !c.canAdminAccount(ctx, w, account) {
		return
	}
	body, user, ok := c.readCollaborator(w, req)
	if !ok {
		return
	}
	if account == "user:"+user.ID {
		httphelper.ConflictError(w, "the personal account owner cannot be added or removed as a collaborator")
		return
	}
	if err := c.rejectIfSuspended(account); err != nil {
		respondWithError(w, err)
		return
	}
	n, err := c.tenancy.CountCollaborators(account)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.quotaAllows(account, 0, 0, 0, 0, n+1); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.tenancy.UpsertAccountCollaborator(account, user.ID, body.Role); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "collaborator.add", account, "", map[string]string{"user_id": user.ID, "role": body.Role})
	httphelper.JSON(w, 200, ct.Collaborator{UserID: user.ID, Handle: user.Handle, Role: body.Role})
}

func (c *controllerAPI) DeleteAccountCollaborator(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	account := accountParam(ctx)
	if !c.canAdminAccount(ctx, w, account) {
		return
	}
	params, _ := ctxhelper.ParamsFromContext(ctx)
	userID := params.ByName("users_id")
	if account == "user:"+userID {
		httphelper.ConflictError(w, "the personal account owner cannot be removed")
		return
	}
	if err := c.tenancy.DeleteAccountCollaborator(account, userID); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "collaborator.remove", account, "", map[string]string{"user_id": userID})
	w.WriteHeader(200)
}

func (c *controllerAPI) readCollaborator(w http.ResponseWriter, req *http.Request) (ct.CollaboratorCreate, *ct.User, bool) {
	var body ct.CollaboratorCreate
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return body, nil, false
	}
	if !validCollabRole(body.Role) {
		httphelper.ValidationError(w, "role", "must be view, deploy, manage, or admin")
		return body, nil, false
	}
	var user *ct.User
	var err error
	if body.UserID != "" {
		user, err = c.tenancy.GetUser(body.UserID)
	} else if body.Handle != "" {
		user, err = c.tenancy.GetUserByHandle(strings.ToLower(body.Handle))
	} else {
		httphelper.ValidationError(w, "user_id", "user_id or handle is required")
		return body, nil, false
	}
	if err != nil {
		respondWithError(w, err)
		return body, nil, false
	}
	return body, user, true
}

func (c *controllerAPI) ListAppCollaborators(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	app := c.getApp(ctx)
	if !c.canReadApp(ctx, w, app) {
		return
	}
	rows, err := c.tenancy.ListAppCollaborators(app.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if rows == nil {
		rows = []ct.Collaborator{}
	}
	httphelper.JSON(w, 200, rows)
}

func (c *controllerAPI) AddAppCollaborator(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	res := c.accessFor(ctx, app)
	if !access.Has(res.Permissions, access.PermAppAdmin) {
		httphelper.Forbidden(w, "admin on the app is required")
		return
	}
	body, user, ok := c.readCollaborator(w, req)
	if !ok {
		return
	}
	if app.OwnerAccount == "user:"+user.ID {
		httphelper.ConflictError(w, "the personal account owner cannot be removed")
		return
	}
	if err := c.tenancy.UpsertAppCollaborator(app.ID, user.ID, body.Role); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "app_collaborator.add", app.OwnerAccount, app.ID, map[string]string{"user_id": user.ID, "role": body.Role})
	httphelper.JSON(w, 200, ct.Collaborator{UserID: user.ID, Handle: user.Handle, Role: body.Role})
}

func (c *controllerAPI) DeleteAppCollaborator(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	app := c.getApp(ctx)
	res := c.accessFor(ctx, app)
	if !access.Has(res.Permissions, access.PermAppAdmin) {
		httphelper.Forbidden(w, "admin on the app is required")
		return
	}
	params, _ := ctxhelper.ParamsFromContext(ctx)
	userID := params.ByName("users_id")
	if app.OwnerAccount == "user:"+userID {
		httphelper.ConflictError(w, "the personal account owner cannot be removed")
		return
	}
	if err := c.tenancy.DeleteAppCollaborator(app.ID, userID); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "app_collaborator.remove", app.OwnerAccount, app.ID, map[string]string{"user_id": userID})
	w.WriteHeader(200)
}

func (c *controllerAPI) canReadApp(ctx context.Context, w http.ResponseWriter, app *ct.App) bool {
	res := c.accessFor(ctx, app)
	if len(res.Permissions) > 0 {
		return true
	}
	httphelper.Forbidden(w, "you cannot access this app")
	return false
}

func (c *controllerAPI) TransferApp(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	var body ct.TransferRequest
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	body.OwnerAccount = strings.TrimSpace(body.OwnerAccount)
	if body.OwnerAccount == "" {
		httphelper.ValidationError(w, "owner_account", "is required")
		return
	}
	source := c.accessFor(ctx, app)
	target := c.accessFor(ctx, &ct.App{OwnerAccount: body.OwnerAccount})
	tok := authz.TokenFromContext(ctx)
	allowed := tok != nil && (tok.ClusterKey || tok.HasClusterAdmin()) || access.CanTransfer(source, target)
	if !allowed {
		httphelper.Forbidden(w, "transfer requires admin on the source and target; collaborators cannot transfer apps")
		return
	}
	if err := c.rejectIfSuspended(body.OwnerAccount); err != nil {
		respondWithError(w, err)
		return
	}
	n, err := c.tenancy.CountOwnedApps(body.OwnerAccount)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if app.OwnerAccount != body.OwnerAccount {
		if err := c.quotaAllows(body.OwnerAccount, n+1, 0, 0, 0, 0); err != nil {
			respondWithError(w, err)
			return
		}
	}
	shared, err := c.tenancy.SharedResourceBlocksTransfer(app.ID, body.OwnerAccount)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if shared {
		httphelper.ConflictError(w, "detach resources shared with another account before transfer")
		return
	}
	from := app.OwnerAccount
	if err := c.tenancy.SetAppOwner(app.ID, body.OwnerAccount); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.tenancy.MoveAppResources(app.ID, body.OwnerAccount); err != nil {
		respondWithError(w, err)
		return
	}
	cutover := time.Now().UTC().Format(time.RFC3339)
	_ = c.tenancy.Audit(c.actor(ctx), "app.transfer", body.OwnerAccount, app.ID, map[string]string{
		"from": from, "to": body.OwnerAccount, "cutover": cutover,
	})
	app.OwnerAccount = body.OwnerAccount
	httphelper.JSON(w, 200, app)
}

func (c *controllerAPI) VerifyDomain(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body struct {
		Hostname     string `json:"hostname"`
		OwnerAccount string `json:"owner_account"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	body.Hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(body.Hostname)), ".")
	body.OwnerAccount = strings.TrimSpace(body.OwnerAccount)
	if body.Hostname == "" || body.OwnerAccount == "" {
		httphelper.ValidationError(w, "hostname", "hostname and owner_account are required")
		return
	}
	if !c.canAdminAccount(ctx, w, body.OwnerAccount) {
		return
	}
	token := random.Hex(16)
	if err := c.tenancy.InsertDomain(body.Hostname, body.OwnerAccount, token); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, ct.DomainVerification{
		Hostname: body.Hostname, OwnerAccount: body.OwnerAccount, Token: token, TXTName: tenancy.TXTName(body.Hostname),
	})
}

func (c *controllerAPI) CheckDomain(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	hostname := strings.TrimSuffix(strings.ToLower(params.ByName("hostname")), ".")
	d, err := c.tenancy.GetDomain(hostname)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if !c.canAdminAccount(ctx, w, d.OwnerAccount) {
		return
	}
	var body ct.DomainCheck
	_ = httphelper.DecodeJSON(req, &body)
	records := body.Records
	if len(records) == 0 {
		looked, err := net.LookupTXT(tenancy.TXTName(hostname))
		if err != nil {
			httphelper.ValidationError(w, "hostname", "TXT lookup failed and no records were supplied")
			return
		}
		records = looked
	}
	if !tenancy.VerifyTXT(records, d.Token) {
		httphelper.ValidationError(w, "hostname", "TXT record does not match the verification token")
		return
	}
	now := time.Now().UTC()
	if err := c.tenancy.MarkDomainVerified(hostname, now); err != nil {
		respondWithError(w, err)
		return
	}
	d.VerifiedAt = &now
	d.TXTName = tenancy.TXTName(hostname)
	httphelper.JSON(w, 200, d)
}
