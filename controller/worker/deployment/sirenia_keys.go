package deployment

import (
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
)

// applianceKeyEnv is the per-instance admin HTTP key of an isolated database
// app (the mongodb, mysql, redis, kafka and clickhouse plugin instances).
// Those members accept only that key, never the cluster key.
const applianceKeyEnv = "APPLIANCE_KEY"

func releaseApplianceKey(rel *ct.Release) string {
	if rel == nil {
		return ""
	}
	return strings.TrimSpace(rel.Env[applianceKeyEnv])
}

// sireniaPeerKeys is the new release's APPLIANCE_KEY, then the old release's
// when it differs. Nil means a system appliance that takes the cluster key.
func (d *DeployJob) sireniaPeerKeys() []string {
	var keys []string
	for _, k := range []string{releaseApplianceKey(d.newRelease), releaseApplianceKey(d.oldRelease)} {
		if k != "" && (len(keys) == 0 || keys[0] != k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// sireniaClient is the admin HTTP client for a peer of the app being
// deployed. Peers of an isolated instance get its APPLIANCE_KEY and never the
// cluster key; system appliances get the cluster key.
func (d *DeployJob) sireniaClient(addr string) *sireniaclient.Client {
	if keys := d.sireniaPeerKeys(); len(keys) > 0 {
		return sireniaclient.NewClientWithKeys(addr, keys...)
	}
	return sireniaclient.NewClient(addr)
}
