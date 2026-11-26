package deployment

import (
	"errors"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

const mib = 1 << 20

// capacityCluster is fakeFormationCluster plus the app record and
// GET /cluster/capacity, and it counts scale requests.
type capacityCluster struct {
	*fakeFormationCluster
	app      *ct.App
	capacity *ct.ClusterCapacity
	capErr   error
	scales   int
}

func (c *capacityCluster) GetApp(id string) (*ct.App, error) { return c.app, nil }

func (c *capacityCluster) GetClusterCapacity() (*ct.ClusterCapacity, error) {
	return c.capacity, c.capErr
}

func (c *capacityCluster) ScaleAppRelease(appID, releaseID string, opts ct.ScaleOptions) error {
	c.scales++
	return c.fakeFormationCluster.ScaleAppRelease(appID, releaseID, opts)
}

func sizedProcess(profile string, memMiB, cpu int64, omni, hostNetwork bool) ct.ProcessType {
	return ct.ProcessType{
		RuntimeProfile: profile,
		Omni:           omni,
		HostNetwork:    hostNetwork,
		// data/release.go stores a non-reserving runtime as Request 0 and
		// Limit the runtime size; hosted mode reserves the limit.
		Resources: resource.Resources{
			resource.TypeMemory: {Request: typeconv.Int64Ptr(0), Limit: typeconv.Int64Ptr(memMiB * mib)},
			resource.TypeCPU:    {Request: typeconv.Int64Ptr(0), Limit: typeconv.Int64Ptr(cpu)},
		},
	}
}

// hostedCapacity is two 2 GB / 1-CPU hosts. Each runs another tenant's
// 1 GB / 500m job, and each host in oldHosts also runs the app's old typ job
// (512 MB, 500m).
func hostedCapacity(typ string, oldHosts ...string) *ct.ClusterCapacity {
	cap := &ct.ClusterCapacity{TenancyMode: "hosted", ReserveProfiles: map[string]bool{"small": false, "medium": false}}
	old := map[string]bool{}
	for _, h := range oldHosts {
		old[h] = true
	}
	for _, h := range []string{"h1", "h2"} {
		hc := &ct.HostCapacity{
			ID: h, MemoryTotalBytes: 2048 * mib, CPUMilli: 1000,
			ReservedMemoryBytes: 1024 * mib, ReservedCPUMilli: 500,
			Reserved: []*ct.ReservedJob{
				{JobID: "other-" + h, AppID: "other", ReleaseID: "o", Type: "web", MemoryBytes: 1024 * mib, CPUMilli: 500},
			},
		}
		if old[h] {
			hc.ReservedMemoryBytes += 512 * mib
			hc.ReservedCPUMilli += 500
			hc.Reserved = append(hc.Reserved, &ct.ReservedJob{JobID: "old-" + typ + "-" + h, AppID: "shop", ReleaseID: "old", Type: typ, MemoryBytes: 512 * mib, CPUMilli: 500})
		}
		cap.Hosts = append(cap.Hosts, hc)
	}
	return cap
}

func newCapacityCluster(app *ct.App, oldProc, newProc ct.ProcessType, typ string) *capacityCluster {
	f := newFakeFormationCluster("h1", "h2")
	f.releases["old"] = &ct.Release{ID: "old", Processes: map[string]ct.ProcessType{typ: oldProc}}
	f.releases["new"] = &ct.Release{ID: "new", Processes: map[string]ct.ProcessType{typ: newProc}}
	f.formations["old"] = &ct.Formation{AppID: app.ID, ReleaseID: "old", Processes: map[string]int{typ: 1}}
	f.jobsOverride = func(jobs []*ct.Job) []*ct.Job {
		for _, j := range jobs {
			if j.ReleaseID == "old" {
				j.ID = "old-" + j.Type + "-" + j.HostID
			}
		}
		return jobs
	}
	// The fake runs an omni type on every host and other types on h1.
	oldHosts := []string{"h1"}
	if oldProc.Omni {
		oldHosts = []string{"h1", "h2"}
	}
	return &capacityCluster{fakeFormationCluster: f, app: app, capacity: hostedCapacity(typ, oldHosts...)}
}

func capacityDeploy(c *capacityCluster, strategy, typ string) *DeployJob {
	return &DeployJob{
		Deployment: &ct.Deployment{
			ID: "dep", AppID: c.app.ID, OldReleaseID: "old", NewReleaseID: "new",
			Strategy: strategy, Processes: map[string]int{typ: 1},
		},
		client:       c,
		oldRelease:   c.releases["old"],
		newRelease:   c.releases["new"],
		deployEvents: make(chan ct.DeploymentEvent, 64),
		logger:       log15.New(),
		stop:         make(chan struct{}),
	}
}

// TestDeployRefusesStopFirstRollThatCannotPlaceReplacement: NEW-45. A
// host-network omni process rolls stop-old-then-start-new on each host. When
// the new job would not fit on the host even after the old one stops, the
// deploy fails before anything is stopped, with a message that says so.
func TestDeployRefusesStopFirstRollThatCannotPlaceReplacement(t *testing.T) {
	app := &ct.App{ID: "shop", Name: "shop"}
	// small -> medium: 1 GB / 1000m does not fit next to the other
	// tenant's 1 GB / 500m on a 2 GB / 1-CPU host.
	c := newCapacityCluster(app, sizedProcess("small", 512, 500, true, true), sizedProcess("medium", 1024, 1000, true, true), "edge")
	err := capacityDeploy(c, "one-by-one", "edge").Perform()
	if err == nil {
		t.Fatal("the roll must be refused: the replacement cannot be placed after the old job stops")
	}
	for _, want := range []string{"edge", "h1", "no job was stopped"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if !IsSkipRollback(err) {
		t.Fatalf("nothing changed, so there is nothing to roll back: %T", err)
	}
	if c.scales != 0 {
		t.Fatalf("%d scale requests were sent; the pre-check must run before any job is stopped", c.scales)
	}
	if n := c.formations["old"].Processes["edge"]; n != 1 {
		t.Fatalf("old formation edge=%d, want 1", n)
	}
}

func TestCapacityPreCheck(t *testing.T) {
	tenant := &ct.App{ID: "shop", Name: "shop"}
	router := &ct.App{ID: "router", Name: "router", Meta: map[string]string{"flynn-system-app": "true"}}
	small := sizedProcess("small", 512, 500, true, true)
	medium := sizedProcess("medium", 1024, 1000, true, true)

	t.Run("same size fits once the old job stops", func(t *testing.T) {
		c := newCapacityCluster(tenant, small, small, "edge")
		if err := capacityDeploy(c, "one-by-one", "edge").checkCapacityBeforeStop(log15.New()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("platform router is never blocked by tenant reservations", func(t *testing.T) {
		c := newCapacityCluster(router, small, medium, "app")
		for _, h := range c.capacity.Hosts {
			h.ReservedMemoryBytes, h.ReservedCPUMilli = 64*1024*mib, 64000
		}
		if err := capacityDeploy(c, "one-down-one-up", "app").checkCapacityBeforeStop(log15.New()); err != nil {
			t.Fatal(err)
		}
		c.app = tenant
		if err := capacityDeploy(c, "one-down-one-up", "app").checkCapacityBeforeStop(log15.New()); err == nil {
			t.Fatal("control: the same roll of a tenant app must be refused")
		}
	})
	t.Run("start-first strategies are not checked", func(t *testing.T) {
		web := sizedProcess("small", 512, 500, false, false)
		big := sizedProcess("medium", 1024, 1000, false, false)
		c := newCapacityCluster(tenant, web, big, "web")
		if err := capacityDeploy(c, "one-by-one", "web").checkCapacityBeforeStop(log15.New()); err != nil {
			t.Fatalf("one-by-one starts the new web job first and fails without stopping anything: %v", err)
		}
	})
	t.Run("one-down-one-up places the replacement on any host that fits", func(t *testing.T) {
		web := sizedProcess("small", 512, 500, false, false)
		c := newCapacityCluster(tenant, web, web, "web")
		if err := capacityDeploy(c, "one-down-one-up", "web").checkCapacityBeforeStop(log15.New()); err != nil {
			t.Fatal(err)
		}
		big := sizedProcess("medium", 1024, 1000, false, false)
		c = newCapacityCluster(tenant, web, big, "web")
		err := capacityDeploy(c, "one-down-one-up", "web").checkCapacityBeforeStop(log15.New())
		if err == nil || !strings.Contains(err.Error(), "no job was stopped") {
			t.Fatalf("a replacement that fits no host must be refused: %v", err)
		}
	})
	t.Run("self_hosted with the flag off does not reserve", func(t *testing.T) {
		c := newCapacityCluster(tenant, small, medium, "edge")
		c.capacity.TenancyMode = "self_hosted"
		if err := capacityDeploy(c, "one-by-one", "edge").checkCapacityBeforeStop(log15.New()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown capacity does not block the deploy", func(t *testing.T) {
		c := newCapacityCluster(tenant, small, medium, "edge")
		c.capErr = errors.New("404 not found")
		if err := capacityDeploy(c, "one-by-one", "edge").checkCapacityBeforeStop(log15.New()); err != nil {
			t.Fatalf("an older controller without GET /cluster/capacity must not block deploys: %v", err)
		}
		c = newCapacityCluster(tenant, small, medium, "edge")
		for _, h := range c.capacity.Hosts {
			h.MemoryTotalBytes, h.CPUMilli, h.StatsError = 0, 0, "timeout"
		}
		if err := capacityDeploy(c, "one-by-one", "edge").checkCapacityBeforeStop(log15.New()); err != nil {
			t.Fatalf("a host whose totals could not be read is not proof of no room: %v", err)
		}
	})
}
