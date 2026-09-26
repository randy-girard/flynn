// Package instanceport assigns a TCP port to each database instance and
// decides which hosts should allow it. The host port is the external path.
// In-cluster clients keep using discoverd. One instance's port is not a
// route to another instance: a plan lists only that instance's port.
package instanceport

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	// MinPort and MaxPort are the external TCP range for per-instance
	// databases (the same window operators already open for TCP services).
	MinPort = 3000
	MaxPort = 3500

	// EnvPort and EnvID are stored on the resource and copied into the app env.
	EnvPort = "FLYNN_INSTANCE_PORT"
	EnvID   = "FLYNN_INSTANCE_ID"

	// Job metadata on the instance process. Client apps that only received
	// EnvPort must not open the host firewall.
	MetaInstanceID = "flynn-instance.id"
	MetaPort       = "flynn-instance.port"
)

// Job is one running process of a database instance.
type Job struct {
	InstanceID string
	Port       int
	Host       string
}

// Exposure is a single host allow for one instance. It has no forward target.
type Exposure struct {
	InstanceID string
	Port       int
	Host       string
}

// InstancePlan is the expose set for one instance. Port is only that
// instance's port. Hosts are the hosts currently running it.
type InstancePlan struct {
	InstanceID string
	Port       int
	Hosts      []string
}

// ExposePorts is the port list for this plan. It is a single port.
func (p InstancePlan) ExposePorts() []int {
	if p.Port <= 0 {
		return nil
	}
	return []int{p.Port}
}

var (
	ErrCollision = fmt.Errorf("instance port already assigned")
	ErrExhausted = fmt.Errorf("instance port range exhausted")
	ErrRange     = fmt.Errorf("instance port outside %d-%d", MinPort, MaxPort)
)

// IsDatastore reports providers that take a per-instance external port
// once they run one database per instance. mysql is the MariaDB provider name.
func IsDatastore(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "postgres", "redis", "mariadb", "mysql", "mongodb", "kafka", "clickhouse":
		return true
	default:
		return false
	}
}

// AssignEnv stores a unique external port on a resource env.
// A port already present is kept when this instance owns it and rejected when another instance does.
func AssignEnv(instanceID string, env map[string]string, used map[int]string) (map[string]string, error) {
	var port int
	if existing, ok := ParsePort(metaVal(env, EnvPort)); ok {
		if err := Claim(existing, instanceID, used); err != nil {
			return nil, err
		}
		port = existing
	} else {
		var err error
		port, err = Allocate(used)
		if err != nil {
			return nil, err
		}
	}
	return StampEnv(env, instanceID, port), nil
}

// Allocate returns the lowest free port in [MinPort, MaxPort].
// used maps a port to the instance that owns it. A port in used is never returned.
func Allocate(used map[int]string) (int, error) {
	return AllocateRange(used, MinPort, MaxPort)
}

// AllocateRange returns the lowest free port in [min, max].
func AllocateRange(used map[int]string, min, max int) (int, error) {
	if min <= 0 || max > 65535 || min > max {
		return 0, ErrRange
	}
	for port := min; port <= max; port++ {
		if _, taken := used[port]; taken {
			continue
		}
		return port, nil
	}
	return 0, ErrExhausted
}

// Claim rejects a port another instance already owns, or a port outside the range.
// The same instance may keep its port.
func Claim(port int, instanceID string, used map[int]string) error {
	return ClaimRange(port, instanceID, used, MinPort, MaxPort)
}

// ClaimRange is Claim with an explicit inclusive range.
func ClaimRange(port int, instanceID string, used map[int]string, min, max int) error {
	if port < min || port > max {
		return ErrRange
	}
	if owner, taken := used[port]; taken && owner != instanceID {
		return ErrCollision
	}
	return nil
}

// ParsePort reads a decimal TCP port. Zero and non-numeric values are rejected.
func ParsePort(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 65535 {
		return 0, false
	}
	return n, true
}

// StampEnv copies env and records the instance id and external port.
// Discoverd connection URLs keep their service port so in-cluster clients
// stay on discoverd. A URL whose host is not discoverd is rewritten to this
// port, which is the external path.
func StampEnv(env map[string]string, instanceID string, port int) map[string]string {
	out := make(map[string]string, len(env)+2)
	for k, v := range env {
		out[k] = v
	}
	if instanceID != "" {
		out[EnvID] = instanceID
	}
	out[EnvPort] = strconv.Itoa(port)
	for k, v := range out {
		if !strings.HasSuffix(k, "_URL") {
			continue
		}
		if next, ok := externalURL(v, port); ok {
			out[k] = next
		}
	}
	return out
}

func externalURL(raw string, port int) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, false
	}
	host := u.Hostname()
	if host == "" || strings.Contains(strings.ToLower(host), "discoverd") {
		return raw, false
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return u.String(), true
}

// ParseJob reads an instance process. Metadata is required. Env is only a
// fallback when the job is marked flynn-datastore, so an app that merely
// received FLYNN_INSTANCE_PORT does not open the host port.
func ParseJob(host string, meta, env map[string]string) (Job, bool) {
	host = strings.TrimSpace(host)
	if host == "" {
		return Job{}, false
	}
	id := strings.TrimSpace(metaVal(meta, MetaInstanceID))
	portStr := strings.TrimSpace(metaVal(meta, MetaPort))
	if id == "" || portStr == "" {
		if metaVal(meta, "flynn-datastore") != "true" {
			return Job{}, false
		}
		id = strings.TrimSpace(metaVal(env, EnvID))
		portStr = strings.TrimSpace(metaVal(env, EnvPort))
	}
	port, ok := ParsePort(portStr)
	if !ok || id == "" {
		return Job{}, false
	}
	return Job{InstanceID: id, Port: port, Host: host}, true
}

func metaVal(m map[string]string, k string) string {
	if m == nil {
		return ""
	}
	return m[k]
}

// Desired is the expose set: each running job contributes its own port on its
// own host. An exposure never carries another instance's port.
func Desired(jobs []Job) []Exposure {
	seen := map[string]Exposure{}
	for _, j := range jobs {
		j.InstanceID = strings.TrimSpace(j.InstanceID)
		j.Host = strings.TrimSpace(j.Host)
		if j.InstanceID == "" || j.Host == "" || j.Port <= 0 || j.Port > 65535 {
			continue
		}
		e := Exposure{InstanceID: j.InstanceID, Port: j.Port, Host: j.Host}
		seen[exposureKey(e)] = e
	}
	out := make([]Exposure, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sortExposures(out)
	return out
}

// ForInstance returns exposures for one instance. Each port is that instance's.
func ForInstance(instanceID string, exposures []Exposure) []Exposure {
	var out []Exposure
	for _, e := range exposures {
		if e.InstanceID == instanceID {
			out = append(out, e)
		}
	}
	return out
}

// PortsForHost is the TCP ports that should be open on host.
func PortsForHost(host string, jobs []Job) []int {
	host = strings.TrimSpace(host)
	seen := map[int]struct{}{}
	var ports []int
	for _, e := range Desired(jobs) {
		if e.Host != host {
			continue
		}
		if _, ok := seen[e.Port]; ok {
			continue
		}
		seen[e.Port] = struct{}{}
		ports = append(ports, e.Port)
	}
	sort.Ints(ports)
	return ports
}

// Plans groups jobs into one plan per instance. Every job of an instance must
// use the same port. The plan lists that port and the hosts running the instance.
func Plans(jobs []Job) ([]InstancePlan, error) {
	type acc struct {
		port  int
		hosts map[string]struct{}
	}
	byID := map[string]*acc{}
	for _, e := range Desired(jobs) {
		a, ok := byID[e.InstanceID]
		if !ok {
			a = &acc{port: e.Port, hosts: map[string]struct{}{}}
			byID[e.InstanceID] = a
		}
		if a.port != e.Port {
			return nil, fmt.Errorf("instance %s has ports %d and %d", e.InstanceID, a.port, e.Port)
		}
		a.hosts[e.Host] = struct{}{}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]InstancePlan, 0, len(ids))
	for _, id := range ids {
		a := byID[id]
		hosts := make([]string, 0, len(a.hosts))
		for h := range a.hosts {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		out = append(out, InstancePlan{InstanceID: id, Port: a.port, Hosts: hosts})
	}
	return out, nil
}

// Diff returns exposes to open and exposes to close so desired becomes current.
// A job that moved opens on the new host and closes on the host that no longer runs it.
func Diff(current, desired []Exposure) (open, close []Exposure) {
	cur := indexExposures(current)
	want := indexExposures(desired)
	for k, e := range want {
		if _, ok := cur[k]; !ok {
			open = append(open, e)
		}
	}
	for k, e := range cur {
		if _, ok := want[k]; !ok {
			close = append(close, e)
		}
	}
	sortExposures(open)
	sortExposures(close)
	return open, close
}

func indexExposures(in []Exposure) map[string]Exposure {
	out := make(map[string]Exposure, len(in))
	for _, e := range in {
		out[exposureKey(e)] = e
	}
	return out
}

func exposureKey(e Exposure) string {
	return e.Host + "|" + e.InstanceID + "|" + strconv.Itoa(e.Port)
}

func sortExposures(s []Exposure) {
	sort.Slice(s, func(i, j int) bool { return exposureKey(s[i]) < exposureKey(s[j]) })
}
