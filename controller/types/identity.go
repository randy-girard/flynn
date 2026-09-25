package types

import "time"

// TenancySettings is the cluster tenancy mode. SignupEnabled stays false
// unless an operator sets it; switching to hosted does not enable signup.
type TenancySettings struct {
	Mode          string `json:"mode"`
	SignupEnabled bool   `json:"signup_enabled"`
}

// User is a controller identity. PasswordHash is never serialized.
type User struct {
	ID            string     `json:"id"`
	Handle        string     `json:"handle"`
	Email         string     `json:"email"`
	PasswordHash  string     `json:"-"`
	ClusterAdmin  bool       `json:"cluster_admin"`
	Disabled      bool       `json:"disabled"`
	Suspended     bool       `json:"suspended"`
	EmailVerified bool       `json:"email_verified"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

// WhoAmI is the authenticated caller.
type WhoAmI struct {
	User
	Account string `json:"account"`
}

// UserCreate is POST /users.
type UserCreate struct {
	Email        string `json:"email"`
	Handle       string `json:"handle"`
	Password     string `json:"password"`
	ClusterAdmin bool   `json:"cluster_admin"`
}

// UserPatch is PATCH /users/:id. Nil fields are left unchanged.
type UserPatch struct {
	Disabled      *bool   `json:"disabled"`
	ClusterAdmin  *bool   `json:"cluster_admin"`
	Handle        *string `json:"handle"`
	Email         *string `json:"email"`
	EmailVerified *bool   `json:"email_verified"`
	Suspended     *bool   `json:"suspended"`
	Password      *string `json:"password"`
}

// Handle is a row in the shared handle namespace.
type Handle struct {
	Handle  string `json:"handle"`
	Account string `json:"account"`
	Kind    string `json:"kind"`
}

// Membership is one ledger row the enterprise plugin writes.
type Membership struct {
	UserID  string `json:"user_id"`
	Subject string `json:"subject"`
	Role    string `json:"role"`
}

// AccessTokenRecord is a personal access token. Token is set only on create.
type AccessTokenRecord struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scopes    string     `json:"scopes"`
	Token     string     `json:"token,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// TokenCreate is POST /tokens.
type TokenCreate struct {
	Name      string     `json:"name"`
	Scopes    string     `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// AccountQuota is the stored quota. Null integers mean unlimited.
type AccountQuota struct {
	Account          string `json:"account"`
	MaxApps          *int   `json:"max_apps"`
	MaxProcesses     *int   `json:"max_processes"`
	MaxMemoryMB      *int   `json:"max_memory_mb"`
	MaxResources     *int   `json:"max_resources"`
	MaxCollaborators *int   `json:"max_collaborators"`
	Source           string `json:"source,omitempty"`
}

// Collaborator is an account or app collaborator.
type Collaborator struct {
	UserID string `json:"user_id"`
	Handle string `json:"handle,omitempty"`
	Role   string `json:"role"`
}

// CollaboratorCreate accepts a user id or handle.
type CollaboratorCreate struct {
	UserID string `json:"user_id"`
	Handle string `json:"handle"`
	Role   string `json:"role"`
}

// TransferRequest is POST /apps/:app/transfer.
type TransferRequest struct {
	OwnerAccount string `json:"owner_account"`
}

// DomainVerification is a TXT challenge for a custom hostname.
type DomainVerification struct {
	Hostname     string     `json:"hostname"`
	OwnerAccount string     `json:"owner_account"`
	Token        string     `json:"token"`
	TXTName      string     `json:"txt_name"`
	VerifiedAt   *time.Time `json:"verified_at"`
}

// DomainCheck supplies TXT records so tests do not need live DNS.
type DomainCheck struct {
	Records []string `json:"records"`
}
