package router

import (
	"fmt"
	"strings"
)

// PublicURLs are the hostnames operators can open after deploy. HTTP routes
// on the cluster domain also list https:// because the router default cert
// is a wildcard for *.$CLUSTER_DOMAIN even when the route has no cert of
// its own.
func (r *Route) PublicURLs(clusterDomain string) []string {
	if r == nil {
		return nil
	}
	switch r.Type {
	case "tcp":
		if r.Port == 0 {
			return nil
		}
		host := strings.TrimSpace(r.Domain)
		if host == "" {
			return []string{fmt.Sprintf("tcp://:%d", r.Port)}
		}
		return []string{fmt.Sprintf("tcp://%s:%d", host, r.Port)}
	case "http", "":
	default:
		return nil
	}
	domain := strings.TrimSpace(r.Domain)
	if domain == "" {
		return nil
	}
	path := r.Path
	if path == "/" {
		path = ""
	} else if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := domain + path
	httpURL := "http://" + host
	if r.hasHTTPCert() || clusterDomainCovers(domain, clusterDomain) {
		return []string{"https://" + host, httpURL}
	}
	return []string{httpURL}
}

func (r *Route) hasHTTPCert() bool {
	if strings.TrimSpace(r.LegacyTLSCert) != "" {
		return true
	}
	if r.Certificate == nil {
		return false
	}
	return strings.TrimSpace(r.Certificate.Cert) != "" || len(r.Certificate.Chain) > 0
}

func clusterDomainCovers(domain, clusterDomain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	clusterDomain = strings.ToLower(strings.TrimSpace(clusterDomain))
	if domain == "" || clusterDomain == "" {
		return false
	}
	return domain == clusterDomain || strings.HasSuffix(domain, "."+clusterDomain)
}
