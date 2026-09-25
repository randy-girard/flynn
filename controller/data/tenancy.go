package data

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// ErrConflict is a unique-constraint failure (handle, email, token).
var ErrConflict = errors.New("tenancy: conflict")

// TenancyRepo stores users, memberships, quotas, and tenancy settings.
type TenancyRepo struct {
	db *postgres.DB
}

func NewTenancyRepo(db *postgres.DB) *TenancyRepo {
	return &TenancyRepo{db: db}
}

// AppAccess is one app the caller might see, plus the roles used to resolve it.
type AppAccess struct {
	AppID          string
	Name           string
	OwnerAccount   string
	AccountRole    string
	AppRole        string
	OrgRole        string
	AppLedgerRole  string
	OwnerSuspended bool
}

func (r *TenancyRepo) GetSettings() (*ct.TenancySettings, error) {
	s := &ct.TenancySettings{}
	err := r.db.QueryRow(`SELECT tenancy_mode, signup_enabled FROM cluster_settings WHERE id = 1`).Scan(&s.Mode, &s.SignupEnabled)
	if err == pgx.ErrNoRows {
		return &ct.TenancySettings{Mode: "self_hosted", SignupEnabled: false}, nil
	}
	return s, err
}

func (r *TenancyRepo) SetSettings(mode string, signup *bool) error {
	if signup == nil {
		return r.db.Exec(`UPDATE cluster_settings SET tenancy_mode = $1 WHERE id = 1`, mode)
	}
	return r.db.Exec(`UPDATE cluster_settings SET tenancy_mode = $1, signup_enabled = $2 WHERE id = 1`, mode, *signup)
}

func (r *TenancyRepo) CreateUser(u *ct.User) error {
	err := r.db.QueryRow(`
		INSERT INTO users (id, handle, email, password_hash, cluster_admin, disabled, suspended, email_verified)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at`,
		u.ID, u.Handle, u.Email, u.PasswordHash, u.ClusterAdmin, u.Disabled, u.Suspended, u.EmailVerified,
	).Scan(&u.CreatedAt, &u.UpdatedAt)
	if postgres.IsUniquenessError(err, "") {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	err = r.db.Exec(`INSERT INTO handles (handle, account, kind) VALUES ($1, $2, 'user')`, u.Handle, "user:"+u.ID)
	if postgres.IsUniquenessError(err, "") {
		return ErrConflict
	}
	return err
}

func scanUser(s postgres.Scanner) (*ct.User, error) {
	u := &ct.User{}
	err := s.Scan(&u.ID, &u.Handle, &u.Email, &u.PasswordHash, &u.ClusterAdmin, &u.Disabled, &u.Suspended, &u.EmailVerified, &u.CreatedAt, &u.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return u, err
}

const userCols = `id, handle, email, password_hash, cluster_admin, disabled, suspended, email_verified, created_at, updated_at`

func (r *TenancyRepo) GetUser(id string) (*ct.User, error) {
	return scanUser(r.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (r *TenancyRepo) GetUserByEmail(email string) (*ct.User, error) {
	return scanUser(r.db.QueryRow(`SELECT `+userCols+` FROM users WHERE email = $1`, email))
}

func (r *TenancyRepo) GetUserByHandle(handle string) (*ct.User, error) {
	return scanUser(r.db.QueryRow(`SELECT `+userCols+` FROM users WHERE handle = $1`, handle))
}

func (r *TenancyRepo) ListUsers() ([]*ct.User, error) {
	rows, err := r.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ct.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *TenancyRepo) UpdateUser(u *ct.User) error {
	err := r.db.QueryRow(`
		UPDATE users SET handle=$2, email=$3, password_hash=$4, cluster_admin=$5, disabled=$6, suspended=$7, email_verified=$8, updated_at=now()
		WHERE id=$1 RETURNING updated_at`,
		u.ID, u.Handle, u.Email, u.PasswordHash, u.ClusterAdmin, u.Disabled, u.Suspended, u.EmailVerified,
	).Scan(&u.UpdatedAt)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if postgres.IsUniquenessError(err, "") {
		return ErrConflict
	}
	return err
}

func (r *TenancyRepo) DeleteUser(id string) error {
	if err := r.db.Exec(`DELETE FROM personal_access_tokens WHERE user_id=$1`, id); err != nil {
		return err
	}
	if err := r.db.Exec(`DELETE FROM account_collaborators WHERE user_id=$1`, id); err != nil {
		return err
	}
	if err := r.db.Exec(`DELETE FROM app_collaborators WHERE user_id=$1`, id); err != nil {
		return err
	}
	if err := r.db.Exec(`DELETE FROM memberships WHERE user_id=$1`, id); err != nil {
		return err
	}
	if err := r.db.Exec(`DELETE FROM handles WHERE account=$1`, "user:"+id); err != nil {
		return err
	}
	if err := r.db.Exec(`DELETE FROM users WHERE id=$1`, id); err != nil {
		return err
	}
	return nil
}

func (r *TenancyRepo) CountOwnedApps(account string) (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE owner_account=$1 AND deleted_at IS NULL`, account).Scan(&n)
	return n, err
}

func (r *TenancyRepo) ReserveHandle(h *ct.Handle) error {
	err := r.db.Exec(`INSERT INTO handles (handle, account, kind) VALUES ($1, $2, $3)`, h.Handle, h.Account, h.Kind)
	if postgres.IsUniquenessError(err, "") {
		return ErrConflict
	}
	return err
}

func (r *TenancyRepo) DeleteHandle(handle string) error {
	return r.db.Exec(`DELETE FROM handles WHERE handle=$1`, handle)
}

func (r *TenancyRepo) GetHandle(handle string) (*ct.Handle, error) {
	h := &ct.Handle{}
	err := r.db.QueryRow(`SELECT handle, account, kind FROM handles WHERE handle=$1`, handle).Scan(&h.Handle, &h.Account, &h.Kind)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return h, err
}

func (r *TenancyRepo) PutMembership(m ct.Membership) error {
	return r.db.Exec(`
		INSERT INTO memberships (user_id, subject, role) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, subject) DO UPDATE SET role = EXCLUDED.role`,
		m.UserID, m.Subject, m.Role)
}

func (r *TenancyRepo) DeleteMembership(userID, subject string) error {
	return r.db.Exec(`DELETE FROM memberships WHERE user_id=$1 AND subject=$2`, userID, subject)
}

func (r *TenancyRepo) ListMemberships(userID string) ([]ct.Membership, error) {
	rows, err := r.db.Query(`SELECT user_id, subject, role FROM memberships WHERE user_id=$1 ORDER BY subject`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ct.Membership
	for rows.Next() {
		var m ct.Membership
		if err := rows.Scan(&m.UserID, &m.Subject, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *TenancyRepo) CreatePAT(userID, id, name, scopes, hash string, expires *time.Time) (*ct.AccessTokenRecord, error) {
	rec := &ct.AccessTokenRecord{ID: id, Name: name, Scopes: scopes, ExpiresAt: expires}
	err := r.db.QueryRow(`
		INSERT INTO personal_access_tokens (id, user_id, name, scopes, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		id, userID, name, scopes, hash, expires).Scan(&rec.CreatedAt)
	return rec, err
}

func (r *TenancyRepo) ListPATs(userID string) ([]ct.AccessTokenRecord, error) {
	rows, err := r.db.Query(`
		SELECT id, name, scopes, expires_at, revoked_at, created_at
		FROM personal_access_tokens WHERE user_id=$1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ct.AccessTokenRecord
	for rows.Next() {
		var rec ct.AccessTokenRecord
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Scopes, &rec.ExpiresAt, &rec.RevokedAt, &rec.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *TenancyRepo) RevokePAT(userID, id string) error {
	err := r.db.Exec(`UPDATE personal_access_tokens SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, id, userID)
	return err
}

// PATPrincipal is a token lookup that passed hash, expiry, and revocation checks.
type PATPrincipal struct {
	UserID string
	Email  string
	Scopes string
}

func (r *TenancyRepo) LookupPAT(hash string, now time.Time) (*PATPrincipal, error) {
	p := &PATPrincipal{}
	var disabled, suspended bool
	err := r.db.QueryRow(`
		SELECT u.id, u.email, t.scopes, u.disabled, u.suspended
		FROM personal_access_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > $2)`,
		hash, now).Scan(&p.UserID, &p.Email, &p.Scopes, &disabled, &suspended)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if disabled || suspended {
		return nil, ErrNotFound
	}
	accSuspended, err := r.AccountSuspended("user:" + p.UserID)
	if err != nil {
		return nil, err
	}
	if accSuspended {
		return nil, ErrNotFound
	}
	return p, nil
}

func (r *TenancyRepo) GetQuota(account string) (*ct.AccountQuota, error) {
	q := &ct.AccountQuota{Account: account}
	err := r.db.QueryRow(`
		SELECT max_apps, max_processes, max_memory_mb, max_resources, max_collaborators
		FROM account_quotas WHERE account=$1`, account).Scan(&q.MaxApps, &q.MaxProcesses, &q.MaxMemoryMB, &q.MaxResources, &q.MaxCollaborators)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return q, err
}

func (r *TenancyRepo) SetQuota(q *ct.AccountQuota) error {
	return r.db.Exec(`
		INSERT INTO account_quotas (account, max_apps, max_processes, max_memory_mb, max_resources, max_collaborators)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (account) DO UPDATE SET
			max_apps=EXCLUDED.max_apps, max_processes=EXCLUDED.max_processes, max_memory_mb=EXCLUDED.max_memory_mb,
			max_resources=EXCLUDED.max_resources, max_collaborators=EXCLUDED.max_collaborators`,
		q.Account, q.MaxApps, q.MaxProcesses, q.MaxMemoryMB, q.MaxResources, q.MaxCollaborators)
}

func (r *TenancyRepo) SetAccountSuspended(account string, suspended bool) error {
	if suspended {
		return r.db.Exec(`
			INSERT INTO account_state (account, suspended, suspended_at) VALUES ($1, true, now())
			ON CONFLICT (account) DO UPDATE SET suspended=true, suspended_at=now()`, account)
	}
	return r.db.Exec(`
		INSERT INTO account_state (account, suspended, suspended_at) VALUES ($1, false, NULL)
		ON CONFLICT (account) DO UPDATE SET suspended=false, suspended_at=NULL`, account)
}

func (r *TenancyRepo) AccountSuspended(account string) (bool, error) {
	var suspended bool
	err := r.db.QueryRow(`SELECT suspended FROM account_state WHERE account=$1`, account).Scan(&suspended)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return suspended, err
}

func (r *TenancyRepo) UpsertAccountCollaborator(account, userID, role string) error {
	return r.db.Exec(`
		INSERT INTO account_collaborators (account, user_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (account, user_id) DO UPDATE SET role=EXCLUDED.role`, account, userID, role)
}

func (r *TenancyRepo) DeleteAccountCollaborator(account, userID string) error {
	return r.db.Exec(`DELETE FROM account_collaborators WHERE account=$1 AND user_id=$2`, account, userID)
}

func (r *TenancyRepo) ListAccountCollaborators(account string) ([]ct.Collaborator, error) {
	return r.listCollaborators(`
		SELECT c.user_id, u.handle, c.role
		FROM account_collaborators c JOIN users u ON u.id=c.user_id
		WHERE c.account=$1 ORDER BY u.handle`, account)
}

func (r *TenancyRepo) UpsertAppCollaborator(appID, userID, role string) error {
	return r.db.Exec(`
		INSERT INTO app_collaborators (app_id, user_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (app_id, user_id) DO UPDATE SET role=EXCLUDED.role`, appID, userID, role)
}

func (r *TenancyRepo) DeleteAppCollaborator(appID, userID string) error {
	return r.db.Exec(`DELETE FROM app_collaborators WHERE app_id=$1 AND user_id=$2`, appID, userID)
}

func (r *TenancyRepo) ListAppCollaborators(appID string) ([]ct.Collaborator, error) {
	return r.listCollaborators(`
		SELECT c.user_id, u.handle, c.role
		FROM app_collaborators c JOIN users u ON u.id=c.user_id
		WHERE c.app_id=$1 ORDER BY u.handle`, appID)
}

func (r *TenancyRepo) listCollaborators(sql, arg string) ([]ct.Collaborator, error) {
	rows, err := r.db.Query(sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ct.Collaborator
	for rows.Next() {
		var c ct.Collaborator
		if err := rows.Scan(&c.UserID, &c.Handle, &c.Role); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *TenancyRepo) CountCollaborators(account string) (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM account_collaborators WHERE account=$1`, account).Scan(&n)
	return n, err
}

func (r *TenancyRepo) CountResources(account string) (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM resources WHERE owner_account=$1 AND deleted_at IS NULL`, account).Scan(&n)
	return n, err
}

func (r *TenancyRepo) InsertDomain(hostname, owner, token string) error {
	err := r.db.Exec(`
		INSERT INTO verified_domains (hostname, owner_account, token, verified_at)
		VALUES ($1, $2, $3, NULL)
		ON CONFLICT (hostname) DO UPDATE SET owner_account=EXCLUDED.owner_account, token=EXCLUDED.token, verified_at=NULL
		WHERE verified_domains.verified_at IS NULL OR verified_domains.owner_account=EXCLUDED.owner_account`,
		hostname, owner, token)
	return err
}

func (r *TenancyRepo) GetDomain(hostname string) (*ct.DomainVerification, error) {
	d := &ct.DomainVerification{}
	err := r.db.QueryRow(`SELECT hostname, owner_account, token, verified_at FROM verified_domains WHERE hostname=$1`, hostname).
		Scan(&d.Hostname, &d.OwnerAccount, &d.Token, &d.VerifiedAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return d, err
}

func (r *TenancyRepo) MarkDomainVerified(hostname string, at time.Time) error {
	err := r.db.QueryRow(`UPDATE verified_domains SET verified_at=$2 WHERE hostname=$1 RETURNING hostname`, hostname, at).Scan(&hostname)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	return err
}

func (r *TenancyRepo) VerifiedHostnames(owner string) ([]string, error) {
	rows, err := r.db.Query(`SELECT hostname FROM verified_domains WHERE owner_account=$1 AND verified_at IS NOT NULL`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *TenancyRepo) Audit(actor, action, account, appID string, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var app interface{}
	if appID != "" {
		app = appID
	}
	var acct interface{}
	if account != "" {
		acct = account
	}
	return r.db.Exec(`INSERT INTO core_audit (actor, action, account, app_id, data) VALUES ($1, $2, $3, $4, $5)`,
		actor, action, acct, app, raw)
}

func (r *TenancyRepo) AccessibleApps(userID string) ([]AppAccess, error) {
	rows, err := r.db.Query(`
		SELECT a.app_id, a.name, COALESCE(a.owner_account, ''),
			COALESCE(ac.role, ''), COALESCE(ap.role, ''),
			COALESCE(m.role, ''), COALESCE(am.role, ''),
			COALESCE(st.suspended, false)
		FROM apps a
		LEFT JOIN account_collaborators ac ON ac.account = a.owner_account AND ac.user_id = $1
		LEFT JOIN app_collaborators ap ON ap.app_id = a.app_id AND ap.user_id = $1
		LEFT JOIN memberships m ON m.user_id = $1 AND m.subject = a.owner_account
		LEFT JOIN memberships am ON am.user_id = $1 AND am.subject = 'app:' || a.app_id::text
		LEFT JOIN account_state st ON st.account = a.owner_account
		WHERE a.deleted_at IS NULL
		AND (
			a.owner_account = $2
			OR ac.user_id IS NOT NULL
			OR ap.user_id IS NOT NULL
			OR m.user_id IS NOT NULL
			OR am.user_id IS NOT NULL
		)`, userID, "user:"+userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppAccess
	for rows.Next() {
		var row AppAccess
		if err := rows.Scan(&row.AppID, &row.Name, &row.OwnerAccount, &row.AccountRole, &row.AppRole, &row.OrgRole, &row.AppLedgerRole, &row.OwnerSuspended); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *TenancyRepo) SetResourceOwner(resourceID, owner string) error {
	return r.db.Exec(`UPDATE resources SET owner_account=$2 WHERE resource_id=$1 AND deleted_at IS NULL`, resourceID, owner)
}

func (r *TenancyRepo) SetAppOwner(appID, owner string) error {
	return r.db.Exec(`UPDATE apps SET owner_account=$2, updated_at=now() WHERE app_id=$1`, appID, owner)
}

func (r *TenancyRepo) SharedResourceBlocksTransfer(appID, newOwner string) (bool, error) {
	var n int
	err := r.db.QueryRow(`
		SELECT COUNT(*) FROM app_resources ar
		JOIN resources res ON res.resource_id = ar.resource_id AND res.deleted_at IS NULL
		WHERE ar.app_id = $1 AND ar.deleted_at IS NULL
		AND EXISTS (
			SELECT 1 FROM app_resources ar2
			JOIN apps a2 ON a2.app_id = ar2.app_id
			WHERE ar2.resource_id = res.resource_id AND ar2.deleted_at IS NULL AND ar2.app_id <> $1
			AND COALESCE(a2.owner_account, '') <> $2
		)`, appID, newOwner).Scan(&n)
	return n > 0, err
}

func (r *TenancyRepo) MoveAppResources(appID, owner string) error {
	return r.db.Exec(`
		UPDATE resources SET owner_account=$2
		WHERE deleted_at IS NULL AND resource_id IN (
			SELECT resource_id FROM app_resources WHERE app_id=$1 AND deleted_at IS NULL
		)`, appID, owner)
}

// AccountUsage is current consumption for quota checks.
type AccountUsage struct {
	Apps          int
	Processes     int
	MemoryMB      int
	Resources     int
	Collaborators int
}

func (r *TenancyRepo) Usage(account string) (AccountUsage, error) {
	var u AccountUsage
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE owner_account=$1 AND deleted_at IS NULL`, account).Scan(&u.Apps); err != nil {
		return u, err
	}
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM resources WHERE owner_account=$1 AND deleted_at IS NULL`, account).Scan(&u.Resources); err != nil {
		return u, err
	}
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM account_collaborators WHERE account=$1`, account).Scan(&u.Collaborators); err != nil {
		return u, err
	}
	rows, err := r.db.Query(`
		SELECT r.processes, f.processes
		FROM apps a
		JOIN releases r ON r.release_id = a.release_id
		JOIN formations f ON f.app_id = a.app_id AND f.release_id = a.release_id AND f.deleted_at IS NULL
		WHERE a.owner_account=$1 AND a.deleted_at IS NULL AND r.deleted_at IS NULL`, account)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var procRaw, formRaw []byte
		if err := rows.Scan(&procRaw, &formRaw); err != nil {
			return u, err
		}
		procs, mem := usageFromFormation(procRaw, formRaw)
		u.Processes += procs
		u.MemoryMB += mem
	}
	return u, rows.Err()
}

func usageFromFormation(procRaw, formRaw []byte) (procs, memoryMB int) {
	var types map[string]ct.ProcessType
	var counts map[string]int
	if err := json.Unmarshal(procRaw, &types); err != nil {
		return 0, 0
	}
	if err := json.Unmarshal(formRaw, &counts); err != nil {
		return 0, 0
	}
	for name, n := range counts {
		if n <= 0 {
			continue
		}
		procs += n
		spec := types[name].Resources[resource.TypeMemory]
		var bytes int64
		if spec.Limit != nil {
			bytes = *spec.Limit
		} else if spec.Request != nil {
			bytes = *spec.Request
		}
		memoryMB += n * int((bytes+1024*1024-1)/(1024*1024))
	}
	return procs, memoryMB
}
