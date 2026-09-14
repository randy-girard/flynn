// Package netpolicy classifies overlay jobs and decides which discoverd names
// a user job may resolve. Enforcement lives in flynn-host iptables/ipsets;
// discoverd DNS uses the same service catalog.
package netpolicy

import (
	"net"
	"strings"

	host "github.com/flynn/flynn/host/types"
)

// Discoverd service names (and matching ipset names) that publish overlay IPs
// for cluster-wide firewall sync.
const (
	ServiceUser  = "flynn-net-user"
	ServiceBuild = "flynn-net-build"
	ServiceData  = "flynn-net-data"
	ServiceSys   = "flynn-net-sys"
)

// DummyPort is used when registering an overlay IP in a netpolicy service.
// Discoverd instances require host:port; the port is not dialed.
const DummyPort = "1"

// InstanceHost parses the overlay IP from a discoverd instance addr
// ("100.64.1.2:1" or a bare IP).
func InstanceHost(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return net.ParseIP(addr)
	}
	return net.ParseIP(host)
}

type Class int

const (
	ClassUser Class = iota
	ClassBuild
	ClassDatastore
	ClassSystem
)

func (c Class) String() string {
	switch c {
	case ClassBuild:
		return "build"
	case ClassDatastore:
		return "datastore"
	case ClassSystem:
		return "system"
	default:
		return "user"
	}
}

// ServiceForClass is the discoverd service that tracks overlay IPs of that class.
func ServiceForClass(c Class) string {
	switch c {
	case ClassBuild:
		return ServiceBuild
	case ClassDatastore:
		return ServiceData
	case ClassSystem:
		return ServiceSys
	default:
		return ServiceUser
	}
}

// ClassifyJob assigns an overlay firewall class. HostNetwork jobs have no
// overlay IP and should not be registered.
func ClassifyJob(job *host.Job) Class {
	if job == nil {
		return ClassUser
	}
	if isBuildJob(job) {
		return ClassBuild
	}
	if !isSystemJob(job) {
		return ClassUser
	}
	if isDatastoreProcess(job) {
		return ClassDatastore
	}
	return ClassSystem
}

// UserMayResolveDiscoverd reports whether a user-class resolver may receive
// records for a parsed discoverd name. Provisioned datastore URLs use only
// leader.<service>.discoverd — never the service's internal name, APIs, or
// other apps.
func UserMayResolveDiscoverd(leader bool, service string) bool {
	if !leader {
		return false
	}
	return isDatastoreApp(service) || service == "redis"
}

func isBuildJob(job *host.Job) bool {
	if job.Metadata == nil {
		return false
	}
	typ := job.Metadata["flynn-controller.type"]
	return typ == "slugbuilder" || typ == "dockerbuilder"
}

func isSystemJob(job *host.Job) bool {
	if job.Partition == "system" {
		return true
	}
	if job.Metadata == nil {
		return false
	}
	return job.Metadata["flynn-system-app"] == "true" ||
		job.Metadata["flynn-controller.app_name"] == "builder"
}

func isDatastoreProcess(job *host.Job) bool {
	if job.Metadata == nil {
		return false
	}
	if !isDatastoreApp(job.Metadata["flynn-controller.app_name"]) {
		return false
	}
	// API processes are type "web" (postgres-api, etc.) and must stay
	// unreachable from user jobs except via the provisioned leader URL.
	return job.Metadata["flynn-controller.type"] != "web"
}

func isDatastoreApp(name string) bool {
	switch name {
	case "postgres", "mariadb", "mongodb", "kafka", "clickhouse":
		return true
	}
	// Per-app appliances are <kind>-<uuid> (redis, kafka, clickhouse).
	// Do not treat <kind>-api as a data-plane name.
	return applianceUUIDName(name, "redis") ||
		applianceUUIDName(name, "kafka") ||
		applianceUUIDName(name, "clickhouse")
}

func applianceUUIDName(name, kind string) bool {
	prefix := kind + "-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	id := name[len(prefix):]
	return strings.Count(id, "-") == 4 && len(id) >= 32
}
