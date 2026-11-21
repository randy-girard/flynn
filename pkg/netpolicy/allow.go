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

// MetaJobDiscoverdAllow is host job metadata the controller and scheduler
// stamp from the resources attached to the app (resource rows, which only a
// provider or the cluster key writes). It is a comma-separated list of
// datastore service names. The "flynn-controller." prefix is reserved: the
// controller strips it from tenant app and one-off job metadata.
const MetaJobDiscoverdAllow = "flynn-controller.discoverd_allow"

// OverlayInstanceMeta is the discoverd instance meta flynn-host publishes for
// an overlay IP. User jobs include the attached datastore allowlist, taken
// from MetaJobDiscoverdAllow. Release and one-off env are tenant-controlled
// and do not widen it.
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
		meta[MetaDiscoverdAllow] = strings.Join(jobDiscoverdAllow(job), ",")
	}
	return meta
}

// jobDiscoverdAllow is the stamped allowlist, filtered to datastore leader
// names. A job with no stamp at all was started by a controller from before
// the stamp existed (it kept running across a flynn-host update); it keeps
// the env-derived list so its datastore still resolves until its next
// deploy. Every job a current controller or scheduler starts is stamped,
// and a tenant cannot remove the reserved key.
func jobDiscoverdAllow(job *host.Job) []string {
	v, ok := job.Metadata[MetaJobDiscoverdAllow]
	if !ok {
		return DiscoverdServicesFromEnv(job.Config.Env)
	}
	return filterDiscoverdServices(strings.Split(v, ","))
}

// AttachedDiscoverdServices is the allowlist the controller stamps for an
// app, from the env of the resources attached to it. It is not filtered by
// the installed-plugin list (the controller does not read the host's
// installed-plugins.json); flynn-host and discoverd DNS filter it again.
// The result is never nil, so callers can tell "no attachments" from "not
// computed".
func AttachedDiscoverdServices(resourceEnvs []map[string]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, env := range resourceEnvs {
		for _, name := range discoverdNamesFromEnv(env) {
			if strings.ContainsAny(name, ", ") {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// StampDiscoverdAllow sets MetaJobDiscoverdAllow on job. An empty list is
// stamped as "" so flynn-host restricts the job to no datastores.
func StampDiscoverdAllow(job *host.Job, services []string) {
	if job == nil {
		return
	}
	if job.Metadata == nil {
		job.Metadata = map[string]string{}
	}
	job.Metadata[MetaJobDiscoverdAllow] = strings.Join(services, ",")
}

// filterDiscoverdServices lowercases, dedupes and sorts names, keeping only
// those UserMayResolveDiscoverd accepts as a leader.
func filterDiscoverdServices(names []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || !UserMayResolveDiscoverd(true, name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DiscoverdServicesFromEnv collects datastore service names from an env map
// (REDIS_URL / DATABASE_URL / FLYNN_REDIS / …). Only names that
// UserMayResolveDiscoverd would accept as a leader are kept. Call it on
// resource env (trusted), not on release or job env (tenant-controlled).
func DiscoverdServicesFromEnv(env map[string]string) []string {
	return filterDiscoverdServices(discoverdNamesFromEnv(env))
}

// discoverdNamesFromEnv is every discoverd service name an env map points at
// (any *.discoverd host in a value, plus the FLYNN_<DATASTORE> identity keys),
// lowercased, before any datastore filtering.
func discoverdNamesFromEnv(env map[string]string) []string {
	var out []string
	for _, v := range env {
		out = append(out, discoverdServicesInValue(v)...)
	}
	for _, k := range identityDiscoverdEnv {
		if name := strings.ToLower(strings.TrimSpace(env[k])); name != "" {
			out = append(out, name)
		}
	}
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
