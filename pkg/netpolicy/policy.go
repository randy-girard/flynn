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

// UserDatastoreTCPPorts are the data-plane TCP ports user jobs may reach on
// flynn-net-data IPs (the hosts in DATABASE_URL / REDIS_URL / KAFKA_URL / …).
// Admin HTTP is not listed: postgres :5433, mariadb :3307, mongodb :27018,
// redis :6380, kafka :9095, clickhouse :9090. Kafka :9093/:9094 are the
// KRaft controller and inter-broker listeners, not the client data plane
// (9092). ClickHouse 8123/8443/9000/9440 are query listeners; 9009 is
// interserver replication and stays blocked from user jobs.
var UserDatastoreTCPPorts = []string{
	"3306",  // MariaDB / MySQL
	"5432",  // PostgreSQL
	"6379",  // Redis
	"8123",  // ClickHouse HTTP
	"8443",  // ClickHouse HTTPS
	"9000",  // ClickHouse native
	"9092",  // Kafka client
	"9440",  // ClickHouse native TLS
	"27017", // MongoDB
}

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

// HostTrusted reports whether a job may receive the host API key and keep
// host-escape settings. Data-plane engines are system apps, but they are not
// host-trusted. The image builder and plugin web APIs stay trusted.
func HostTrusted(job *host.Job) bool {
	if job == nil {
		return false
	}
	// Engines and one-off jobs in a datastore app (only its web API is
	// trusted). One-off jobs are system class on the network but run in the
	// engine's image and release, so they do not get the host key either.
	if DatastoreAppJob(job) {
		return false
	}
	return isSystemJob(job)
}

// DatastoreAppJob reports an engine or a one-off job in a datastore app:
// not the app's web API. Such jobs are not host-trusted, keep only the user
// job capabilities and lose host-escape settings, whatever their network
// class.
func DatastoreAppJob(job *host.Job) bool {
	return job != nil && !isBuildJob(job) && isSystemJob(job) && isDatastoreProcess(job)
}

// StripDatastoreEscape removes host-escape knobs from a data-plane job: an
// engine, or a one-off job in a datastore app (system class on the network,
// but it runs the engine's image and release). Volume bindings such as
// /data are left in place. Other jobs are unchanged.
func StripDatastoreEscape(job *host.Job) {
	if !DatastoreAppJob(job) {
		return
	}
	job.Config.HostNetwork = false
	job.Config.HostPIDNamespace = false
	job.Config.WriteableCgroups = false
	job.Profiles = nil
	job.Config.LinuxCapabilities = nil
	job.Config.AllowedDevices = nil
	job.Config.AutoCreatedDevices = nil
	job.Config.Mounts = nil
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
	if isDatastoreProcess(job) && !isOneOffJob(job) {
		return ClassDatastore
	}
	return ClassSystem
}

// isOneOffJob reports a job the controller started outside a formation
// (`flynn run`, the cluster backup's pg_dumpall, plugin maintenance and
// backup tasks). Formation jobs, from the scheduler, bootstrap and the host
// fixer, carry flynn-controller.formation=true; jobs with no controller app
// id at all (old fixtures, hand-built jobs) are treated as formation jobs.
func isOneOffJob(job *host.Job) bool {
	switch job.Metadata["flynn-controller.type"] {
	case "runner", "console":
		return true
	}
	if job.Metadata["flynn-controller.formation"] == "true" {
		return false
	}
	return job.Metadata["flynn-controller.app"] != ""
}

// UserMayResolveDiscoverd reports whether a name is a datastore leader a user
// job is allowed to resolve at all (isolated instances, UUID appliances, or
// installed datastore plugins). Per-app attachment is UserMayResolveAttached.
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
	return job.Metadata["flynn-system-app"] == "true"
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

// BuildServices are the discoverd services slugbuilder and dockerbuilder
// jobs may open on the overlay: the controller (artifacts, releases),
// the blobstore (slugs, build cache) and tarreceive (docker layers). Every
// other overlay address is dropped for build jobs; they run tenant code.
var BuildServices = []string{"controller", "blobstore", "tarreceive"}
