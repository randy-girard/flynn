package data

import (
	"regexp"
	"strings"

	"github.com/jackc/pgx"
)

// appDomainName matches a hostname such as flynnapps.com or 1.localflynn.com.
// A single "localhost" label is allowed for development clusters.
var appDomainName = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidAppDomain reports whether s can be the suffix of an app hostname.
func ValidAppDomain(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || strings.Contains(s, "://") || strings.ContainsAny(s, "/: *") {
		return false
	}
	if s == "localhost" {
		return true
	}
	return appDomainName.MatchString(s)
}

// EffectiveAppDomain is the suffix new apps use. An empty override keeps the
// controller's DEFAULT_ROUTE_DOMAIN, which itself defaults to the cluster domain.
func EffectiveAppDomain(override, fallback string) string {
	if d := strings.ToLower(strings.TrimSpace(override)); d != "" {
		return d
	}
	return strings.ToLower(strings.TrimSpace(fallback))
}

func (r *TenancyRepo) GetAppDomain() (string, error) {
	if r == nil || r.db == nil {
		return "", nil
	}
	var domain string
	err := r.db.QueryRow(`SELECT app_domain FROM cluster_settings WHERE id = 1`).Scan(&domain)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return strings.TrimSpace(domain), err
}

func (r *TenancyRepo) SetAppDomain(domain string) error {
	if r == nil || r.db == nil {
		return ErrNotFound
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	return r.db.Exec(`UPDATE cluster_settings SET app_domain = $1 WHERE id = 1`, domain)
}
