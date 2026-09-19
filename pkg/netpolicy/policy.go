// Package netpolicy classifies overlay jobs and decides which discoverd names
// a user job may resolve. Enforcement lives in flynn-host iptables/ipsets;
// discoverd DNS uses the same service catalog.
package netpolicy

import (
	"net"

	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

// Discoverd service names (and matching ipset names) that publish overlay IPs
// for cluster-wide firewall sync.
const (
	ServiceUser  = "flynn-net-user"
	ServiceBuild = "flynn-net-build"
	ServiceData  = "flynn-net-data"
	ServiceSys   = "flynn-net-sys"
)

// DiscoverdHTTPPort is the host-local discoverd HTTP/Raft listener. User jobs
// may reach it only on the overlay gateway, not on the host public IP.
const DiscoverdHTTPPort = "1111"

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
	if plugin.DatastoreService(service) || isUUIDApplianceName(service) {
		return true
	}
	return false
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
	if job.Metadata["flynn-controller.type"] == "web" {
		return false
	}
	if job.Metadata["flynn-datastore"] == "true" {
		return true
	}
	name := job.Metadata["flynn-controller.app_name"]
	if name == "postgres" || isUUIDApplianceName(name) || plugin.DatastoreService(name) {
		return true
	}
	return false
}

func isUUIDApplianceName(name string) bool {
	const uuidLen = 36
	if len(name) < uuidLen+2 {
		return false
	}
	if name[len(name)-uuidLen-1] != '-' {
		return false
	}
	id := name[len(name)-uuidLen:]
	if len(id) != uuidLen {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHex(c) {
				return false
			}
		}
	}
	return true
}

func isHex(c rune) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
