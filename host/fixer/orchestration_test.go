package fixer

import (
	"testing"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/volume"
	"github.com/inconshreveable/log15"
)

// fakeControllerClient implements just enough of controller.Client for the
// fixer orchestration helpers. Unimplemented methods panic via the embedded
// nil interface, which surfaces accidental use in tests.
type fakeControllerClient struct {
	controller.Client
	apps           []*ct.App
	jobs           map[string][]*ct.Job    // by app ID
	volumes        map[string][]*ct.Volume // by app ID
	decommissioned []string                // volume IDs
}

func (c *fakeControllerClient) AppList() ([]*ct.App, error) { return c.apps, nil }

func (c *fakeControllerClient) JobList(appID string) ([]*ct.Job, error) {
	return c.jobs[appID], nil
}

func (c *fakeControllerClient) AppVolumeList(appID string) ([]*ct.Volume, error) {
	return c.volumes[appID], nil
}

func (c *fakeControllerClient) DecommissionVolume(appID string, vol *ct.Volume) error {
	c.decommissioned = append(c.decommissioned, vol.ID)
	return nil
}

func strptr(s string) *string { return &s }

func TestAppByName(t *testing.T) {
	c := &fakeControllerClient{apps: []*ct.App{{ID: "a1", Name: "postgres"}, {ID: "a2", Name: "mariadb"}}}
	app, err := appByName(c, "mariadb")
	if err != nil || app.ID != "a2" {
		t.Fatalf("appByName(mariadb) = %#v, %v", app, err)
	}
	if _, err := appByName(c, "missing"); err == nil {
		t.Fatal("expected error for missing app")
	}
}

func TestHasMissingVolumeJobs(t *testing.T) {
	missing := "job node2 required volume vol-1, but that volume does not exist"
	c := &fakeControllerClient{
		apps: []*ct.App{{ID: "a1", Name: "postgres"}},
		jobs: map[string][]*ct.Job{"a1": {
			{Type: "postgres", HostError: strptr(missing)},
		}},
	}
	if !hasMissingVolumeJobs(c, "postgres") {
		t.Fatal("expected missing volume job to be detected")
	}

	c.jobs["a1"] = []*ct.Job{{Type: "postgres", HostError: strptr("connection refused")}}
	if hasMissingVolumeJobs(c, "postgres") {
		t.Fatal("unrelated host error should not count as missing volume")
	}

	// A missing-volume error on a non-db job type is ignored.
	c.jobs["a1"] = []*ct.Job{{Type: "web", HostError: strptr(missing)}}
	if hasMissingVolumeJobs(c, "postgres") {
		t.Fatal("missing volume on non-db job type should be ignored")
	}
}

func TestSireniaClusterUnderProvisioned(t *testing.T) {
	f := &ClusterFixer{l: log15.New()} // no hosts => want defaultDBPeers(0) = 1
	c := &fakeControllerClient{
		apps: []*ct.App{{ID: "a1", Name: "postgres", Strategy: "sirenia", ReleaseID: "r1"}},
	}

	// No running peers => under-provisioned.
	c.jobs = map[string][]*ct.Job{"a1": {{Type: "postgres", ReleaseID: "r1", State: ct.JobStateDown}}}
	if !f.sireniaClusterUnderProvisioned(c, "postgres") {
		t.Fatal("expected under-provisioned with no up peers")
	}

	// One up peer meets want=1 => not under-provisioned.
	c.jobs = map[string][]*ct.Job{"a1": {{Type: "postgres", ReleaseID: "r1", State: ct.JobStateUp}}}
	if f.sireniaClusterUnderProvisioned(c, "postgres") {
		t.Fatal("did not expect under-provisioned with one up peer")
	}

	// Non-sirenia app is never under-provisioned.
	c.apps = []*ct.App{{ID: "a1", Name: "postgres", Strategy: "one-by-one", ReleaseID: "r1"}}
	if f.sireniaClusterUnderProvisioned(c, "postgres") {
		t.Fatal("non-sirenia app should not be under-provisioned")
	}
}

func TestFixStaleControllerVolumesDecommissionsMissingDataVolumes(t *testing.T) {
	// No hosts => every controller volume is "missing" from all hosts.
	f := &ClusterFixer{l: log15.New()}
	c := &fakeControllerClient{
		apps: []*ct.App{{ID: "a1", Name: "postgres"}},
		volumes: map[string][]*ct.Volume{"a1": {
			{ID: "data-1", Type: volume.VolumeTypeData},
			{ID: "img-1", Type: volume.VolumeTypeSquashfs}, // not a data volume => keep
		}},
	}
	if err := f.FixStaleControllerVolumes(c); err != nil {
		t.Fatalf("FixStaleControllerVolumes: %v", err)
	}
	if len(c.decommissioned) != 1 || c.decommissioned[0] != "data-1" {
		t.Fatalf("decommissioned = %v, want [data-1]", c.decommissioned)
	}
}
