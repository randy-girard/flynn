package netpolicy

import (
	"net"
	"sort"

	host "github.com/randy-girard/flynn/host/types"
)

// MetaDatastoreApp is flynn-net-data instance meta naming the datastore app
// (its controller app id) an engine belongs to. flynn-host pairs engines of
// the same app so they may open each other on any port (EDGE-29).
const MetaDatastoreApp = "flynn-datastore-app"

// MetaDatastoreSystem is "true" on flynn-net-data instances of the platform
// Postgres, which engines of other apps may not open at all.
const MetaDatastoreSystem = "flynn-datastore-system"

// datastoreSystem reports a platform datastore engine (the system postgres
// app, which holds the controller, router and blobstore databases).
func datastoreSystem(job *host.Job) bool {
	return job != nil && job.Metadata[host.MetaControllerAppName] == "postgres" && isSystemJob(job)
}

// DatastoreSystemFromMeta reports a registration of a platform engine.
func DatastoreSystemFromMeta(meta map[string]string) bool {
	return meta[MetaDatastoreSystem] == "true"
}

// DatastoreJobSystem reports a local platform datastore engine.
func DatastoreJobSystem(job *host.Job) bool { return datastoreSystem(job) }

// DatastorePeerTCPPorts are the ports any datastore engine may open on any
// other engine, including engines of another app: a plugin follower is its
// own app and replicates from its primary's app. Every listener here needs a
// password, a client certificate or a key: Postgres 5432 and sirenia admin
// 5433, MariaDB 3306 and admin 3307, Redis 6379, its TLS port 16379 and admin
// 6380, MongoDB 27017 and admin 27018, ClickHouse 8123/8443/9000/9440 and
// admin 9090, Kafka 9092 (mTLS) and admin 9095. Unauthenticated
// cluster-internal listeners (ClickHouse interserver 9009 and Keeper
// 9181/9234, Kafka KRaft 9093 and inter-broker 9094, metrics 9180) are open
// only between engines of the same app.
var DatastorePeerTCPPorts = []string{
	"3306", "3307",
	"5432", "5433",
	"6379", "6380", "16379",
	"8123", "8443", "9000", "9440", "9090",
	"9092", "9095",
	"27017", "27018",
}

// datastoreApp is the app id of a datastore job, or its app name when the
// job carries no id.
func datastoreApp(job *host.Job) string {
	if job == nil || job.Metadata == nil {
		return ""
	}
	if id := job.Metadata["flynn-controller.app"]; id != "" {
		return id
	}
	return job.Metadata[host.MetaControllerAppName]
}

// DatastoreAppFromMeta is the app a flynn-net-data registration names. ok
// is false for a registration from a flynn-host that predates the key.
func DatastoreAppFromMeta(meta map[string]string) (app string, ok bool) {
	app, ok = meta[MetaDatastoreApp]
	return app, ok
}

// DatastoreJobApp is the app flynn-host publishes for a local datastore job.
func DatastoreJobApp(job *host.Job) string { return datastoreApp(job) }

// DatastorePeer is one datastore engine IP and the app it belongs to. Known
// is false for a legacy registration that names no app.
type DatastorePeer struct {
	IP     net.IP
	App    string
	Known  bool
	System bool
}

// SystemDatastoreIPs are the unique platform engine IPs among peers.
func SystemDatastoreIPs(peers []DatastorePeer) []net.IP {
	seen := map[string]bool{}
	var out []net.IP
	for _, p := range peers {
		v4 := p.IP.To4()
		if !p.System || v4 == nil || seen[v4.String()] {
			continue
		}
		seen[v4.String()] = true
		out = append(out, v4)
	}
	return out
}

// DatastorePeerPairs is every ordered (source, destination) pair of
// distinct engine IPs that may open each other on any port: same app, or
// either side a legacy registration (paired with every engine so a
// cluster keeps working during a rolling flynn-host update; the host that
// owns it publishes the app on its next registration).
func DatastorePeerPairs(peers []DatastorePeer) [][2]net.IP {
	type entry struct {
		ip    net.IP
		app   string
		known bool
	}
	byIP := map[string]entry{}
	for _, p := range peers {
		v4 := p.IP.To4()
		if v4 == nil {
			continue
		}
		k := v4.String()
		if e, ok := byIP[k]; ok && e.known {
			continue
		}
		byIP[k] = entry{ip: v4, app: p.App, known: p.Known}
	}
	keys := make([]string, 0, len(byIP))
	for k := range byIP {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out [][2]net.IP
	for _, a := range keys {
		for _, b := range keys {
			if a == b {
				continue
			}
			ea, eb := byIP[a], byIP[b]
			if !ea.known || !eb.known || ea.app == eb.app {
				out = append(out, [2]net.IP{ea.ip, eb.ip})
			}
		}
	}
	return out
}
