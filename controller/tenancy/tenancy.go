// Package tenancy holds pure hosted-mode rules: handles, passwords, quotas,
// reserved hostnames, and domain TXT checks. Persistence lives in the
// controller data layer.
package tenancy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const (
	ModeSelfHosted = "self_hosted"
	ModeHosted     = "hosted"

	HostedMaxApps          = 5
	HostedMaxProcesses     = 4
	HostedMaxMemoryMB      = 512
	HostedMaxResources     = 2
	HostedMaxCollaborators = 2
)

// Limits is a quota. A nil field means unlimited.
type Limits struct {
	MaxApps          *int
	MaxProcesses     *int
	MaxMemoryMB      *int
	MaxResources     *int
	MaxCollaborators *int
}

// ValidateMode accepts only the two cluster settings.
func ValidateMode(mode string) error {
	switch mode {
	case ModeSelfHosted, ModeHosted:
		return nil
	default:
		return fmt.Errorf("tenancy mode must be self_hosted or hosted")
	}
}

// ValidateHandle enforces the shared user and org slug namespace.
func ValidateHandle(handle string) error {
	if len(handle) < 2 || len(handle) > 39 {
		return fmt.Errorf("handle must be 2–39 characters")
	}
	for i := 0; i < len(handle); i++ {
		c := handle[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (i > 0 && c == '-')
		if !ok {
			return fmt.Errorf("handle must match [a-z0-9][a-z0-9-]{1,38}")
		}
	}
	return nil
}

// EffectiveLimits applies an explicit row, otherwise hosted free-tier defaults
// or self-hosted unlimited. Zero or negative explicit values are errors.
func EffectiveLimits(mode string, explicit *Limits) (Limits, error) {
	if explicit != nil {
		if err := validateExplicit(*explicit); err != nil {
			return Limits{}, err
		}
		return *explicit, nil
	}
	if mode == ModeHosted {
		return hostedFree(), nil
	}
	return Limits{}, nil
}

func hostedFree() Limits {
	return Limits{
		MaxApps:          intPtr(HostedMaxApps),
		MaxProcesses:     intPtr(HostedMaxProcesses),
		MaxMemoryMB:      intPtr(HostedMaxMemoryMB),
		MaxResources:     intPtr(HostedMaxResources),
		MaxCollaborators: intPtr(HostedMaxCollaborators),
	}
}

func validateExplicit(l Limits) error {
	fields := []struct {
		name string
		v    *int
	}{
		{"max_apps", l.MaxApps},
		{"max_processes", l.MaxProcesses},
		{"max_memory_mb", l.MaxMemoryMB},
		{"max_resources", l.MaxResources},
		{"max_collaborators", l.MaxCollaborators},
	}
	for _, f := range fields {
		if f.v != nil && *f.v <= 0 {
			return fmt.Errorf("%s must be a positive integer or null for unlimited", f.name)
		}
	}
	return nil
}

// Exceeds reports whether used is over limit. A nil limit is unlimited.
func Exceeds(limit *int, used int) bool {
	if limit == nil {
		return false
	}
	return used > *limit
}

func intPtr(n int) *int { return &n }

// HashPassword stores a scrypt hash. The dashboard login plugin checks
// credentials against the controller; this hash is not a dashboard import.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password is required")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := scrypt.Key([]byte(password), salt, 1<<14, 8, 1, 32)
	if err != nil {
		return "", err
	}
	return "scrypt$16384$8$1$" + base64.RawURLEncoding.EncodeToString(salt) + "$" + base64.RawURLEncoding.EncodeToString(dk), nil
}

// CheckPassword reports whether password matches a HashPassword value.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	dk, err := scrypt.Key([]byte(password), salt, 1<<14, 8, 1, 32)
	if err != nil || len(dk) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare(dk, want) == 1
}

var reservedLabels = map[string]struct{}{
	"dashboard":  {},
	"controller": {},
	"status":     {},
	"blobstore":  {},
	"git":        {},
	"www":        {},
}

// HostnameAllowed enforces hosted custom-domain rules. self_hosted allows
// every hostname. Tenants may use <app>.<cluster-domain>. Other names under
// the cluster domain are reserved. Names outside it need a verified domain
// for the owner (exact or parent).
func HostnameAllowed(mode, hostname, appName, clusterDomain string, verified []string) error {
	if mode != ModeHosted {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if host == "" {
		return nil
	}
	apex := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(clusterDomain)), ".")
	appName = strings.ToLower(strings.TrimSpace(appName))
	if apex != "" && (host == apex || strings.HasSuffix(host, "."+apex)) {
		if appName != "" && host == appName+"."+apex {
			return nil
		}
		return fmt.Errorf("hostname %s is reserved", host)
	}
	label := host
	if i := strings.IndexByte(host, '.'); i >= 0 {
		label = host[:i]
	}
	if _, ok := reservedLabels[label]; ok && !strings.Contains(host, ".") {
		return fmt.Errorf("hostname %s is reserved", host)
	}
	for _, v := range verified {
		v = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
		if v == "" {
			continue
		}
		if host == v || strings.HasSuffix(host, "."+v) {
			return nil
		}
	}
	return fmt.Errorf("hostname %s requires a verified domain for this account", host)
}

// VerifyTXT reports whether any TXT record equals token.
func VerifyTXT(records []string, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	for _, r := range records {
		if strings.TrimSpace(r) == token {
			return true
		}
	}
	return false
}

// TXTName is the DNS name operators publish for hostname.
func TXTName(hostname string) string {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	return "_flynn-verify." + hostname
}

// ScaleIncreases is true when any process count grows.
func ScaleIncreases(oldCounts, newCounts map[string]int) bool {
	for name, n := range newCounts {
		if n > oldCounts[name] {
			return true
		}
	}
	return false
}
