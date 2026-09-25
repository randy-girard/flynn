// Package access resolves effective permissions for one app from identity
// inputs. It does not touch the database. The controller loads memberships
// on each request and unions the result into the token before HTTP checks.
package access

// Permission names match controller/authz coarse grants plus org scopes the
// enterprise plugin stores in the membership ledger.
const (
	PermAppRead         = "app:read"
	PermAppDeploy       = "app:deploy"
	PermAppWrite        = "app:write"
	PermAppAdmin        = "app:admin"
	PermOrgAppsCreate   = "org:apps:create"
	PermOrgMembersWrite = "org:members:write"
	PermOrgBilling      = "org:billing"
)

// LedgerRole is one membership-ledger row (subject org:<id> or app:<id>).
type LedgerRole struct {
	Subject string
	Role    string
}

// Input is everything the resolver needs for one caller and one app.
type Input struct {
	CallerUserID    string
	AppID           string
	OwnerAccount    string
	AccountRole     string
	AppRole         string
	Ledger          []LedgerRole
	CallerSuspended bool
	OwnerSuspended  bool
	ClusterAdmin    bool
}

// Result is the union of every matching role. Empty Permissions is a deny
// for a non-admin caller.
type Result struct {
	Permissions   []string
	ImplicitOwner bool
	OrgManager    bool
	ReadOnly      bool
}

// Resolve returns the caller's effective permissions on one app.
func Resolve(in Input) Result {
	if in.ClusterAdmin && (in.CallerSuspended || in.OwnerSuspended) {
		return Result{Permissions: []string{PermAppRead}, ReadOnly: true}
	}
	if in.CallerSuspended || in.OwnerSuspended {
		return Result{}
	}
	if in.ClusterAdmin {
		return Result{Permissions: []string{
			PermAppAdmin, PermOrgAppsCreate, PermOrgMembersWrite, PermOrgBilling,
		}}
	}
	if in.CallerUserID == "" {
		return Result{}
	}

	var parts [][]string
	implicit := in.OwnerAccount != "" && in.OwnerAccount == "user:"+in.CallerUserID
	if implicit {
		parts = append(parts, adminPerms())
	}
	if in.AccountRole != "" {
		parts = append(parts, collaboratorPerms(in.AccountRole))
	}
	if in.AppRole != "" {
		parts = append(parts, collaboratorPerms(in.AppRole))
	}
	orgManager := false
	for _, row := range in.Ledger {
		if row.Subject == in.OwnerAccount && isOrgSubject(row.Subject) {
			perms, manager := orgPerms(row.Role)
			parts = append(parts, perms)
			if manager {
				orgManager = true
			}
			continue
		}
		if in.AppID != "" && row.Subject == "app:"+in.AppID {
			if perms, manager := orgPerms(row.Role); len(perms) > 0 && isOrgRole(row.Role) {
				parts = append(parts, perms)
				if manager {
					orgManager = true
				}
				continue
			}
			parts = append(parts, collaboratorPerms(row.Role))
		}
	}
	return Result{
		Permissions:   union(parts...),
		ImplicitOwner: implicit,
		OrgManager:    orgManager,
	}
}

// CanTransfer reports whether the caller may move an app from source to target.
// Account and app collaborators cannot transfer, including admin collaborators.
// The personal owner and org owner/admin roles can, when they hold that
// authority on both sides.
func CanTransfer(source, target Result) bool {
	if source.ReadOnly || target.ReadOnly {
		return false
	}
	sourceOK := source.ImplicitOwner || source.OrgManager
	targetOK := target.ImplicitOwner || target.OrgManager
	return sourceOK && targetOK
}

// Has reports whether perms includes perm.
func Has(perms []string, perm string) bool {
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}

func collaboratorPerms(role string) []string {
	switch role {
	case "view":
		return []string{PermAppRead}
	case "deploy":
		return []string{PermAppDeploy}
	case "manage":
		return []string{PermAppWrite}
	case "admin":
		return adminPerms()
	default:
		return nil
	}
}

func adminPerms() []string {
	return []string{PermAppAdmin}
}

func isOrgSubject(subject string) bool {
	return len(subject) > 4 && subject[:4] == "org:"
}

func isOrgRole(role string) bool {
	switch role {
	case "owner", "admin", "member", "billing":
		return true
	default:
		return false
	}
}

func orgPerms(role string) ([]string, bool) {
	switch role {
	case "owner":
		return []string{PermAppAdmin, PermOrgAppsCreate, PermOrgMembersWrite, PermOrgBilling}, true
	case "admin":
		return []string{PermAppAdmin, PermOrgAppsCreate, PermOrgMembersWrite}, true
	case "member":
		return []string{PermAppRead, PermAppDeploy}, false
	case "billing":
		return []string{PermOrgBilling, PermAppRead}, false
	default:
		return nil, false
	}
}

func union(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, g := range groups {
		for _, p := range g {
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}
