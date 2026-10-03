package netpolicy

import (
	"net/url"
	"sort"
	"strings"

	host "github.com/randy-girard/flynn/host/types"
)

// MetaDiscoverdAllow is flynn-net-user instance meta listing leader service
// names this user job may resolve (comma-separated). An empty value means
// none. The key must be present on current flynn-host registrations so DNS
// can tell a new-style allowlist from a legacy client.
const MetaDiscoverdAllow = "flynn-discoverd-allow"

var identityDiscoverdEnv = []string{
	"FLYNN_REDIS",
	"FLYNN_KAFKA",
	"FLYNN_CLICKHOUSE",
	"FLYNN_MYSQL",
	"FLYNN_MONGO",
	"FLYNN_MONGODB",
	"FLYNN_POSTGRES",
}

// OverlayInstanceMeta is the discoverd instance meta flynn-host publishes for
// an overlay IP. User jobs include the attached datastore allowlist.
func OverlayInstanceMeta(job *host.Job) map[string]string {
	class := ClassifyJob(job)
	meta := map[string]string{"class": class.String()}
	if job == nil {
		return meta
	}
	if job.ID != "" {
		meta["job.id"] = job.ID
	}
	if class == ClassUser {
		meta[MetaDiscoverdAllow] = strings.Join(DiscoverdServicesFromEnv(job.Config.Env), ",")
	}
	return meta
}

// DiscoverdServicesFromEnv collects datastore service names Flynn attached to
// this job (REDIS_URL / DATABASE_URL / FLYNN_REDIS / …). Only names that
// UserMayResolveDiscoverd would accept as a leader are kept.
func DiscoverdServicesFromEnv(env map[string]string) []string {
	if env == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return
		}
		if !UserMayResolveDiscoverd(true, name) {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, v := range env {
		for _, name := range discoverdServicesInValue(v) {
			add(name)
		}
	}
	for _, k := range identityDiscoverdEnv {
		add(env[k])
	}
	sort.Strings(out)
	return out
}

// ParseDiscoverdAllow reads MetaDiscoverdAllow. present is false on legacy
// flynn-net-user instances that never set the key.
func ParseDiscoverdAllow(meta map[string]string) (allowed []string, present bool) {
	if meta == nil {
		return nil, false
	}
	v, ok := meta[MetaDiscoverdAllow]
	if !ok {
		return nil, false
	}
	if strings.TrimSpace(v) == "" {
		return nil, true
	}
	for _, p := range strings.Split(v, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		allowed = append(allowed, p)
	}
	return allowed, true
}

// UserMayResolveAttached is UserMayResolveDiscoverd plus a per-job allowlist.
func UserMayResolveAttached(leader bool, service string, allowed []string) bool {
	if !UserMayResolveDiscoverd(leader, service) {
		return false
	}
	service = strings.ToLower(strings.TrimSpace(service))
	for _, a := range allowed {
		if a == service {
			return true
		}
	}
	return false
}

func discoverdServicesInValue(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if u, err := url.Parse(v); err == nil && u.Scheme != "" && u.Host != "" {
		add(discoverdServiceFromHost(u.Hostname()))
	}
	add(discoverdServiceFromHost(v))
	lower := strings.ToLower(v)
	const needle = ".discoverd"
	for i := 0; i < len(lower); {
		j := strings.Index(lower[i:], needle)
		if j < 0 {
			break
		}
		end := i + j
		start := end
		for start > 0 {
			c := lower[start-1]
			if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' {
				start--
				continue
			}
			break
		}
		add(discoverdServiceFromHost(lower[start : end+len(needle)]))
		i = end + len(needle)
	}
	return out
}

func discoverdServiceFromHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	const domain = ".discoverd"
	if !strings.HasSuffix(host, domain) {
		return ""
	}
	name := strings.TrimSuffix(host, domain)
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "leader.") {
		name = strings.TrimPrefix(name, "leader.")
	}
	if name == "" || strings.Contains(name, ".") {
		return ""
	}
	return name
}
