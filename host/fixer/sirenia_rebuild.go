package fixer

import (
	"encoding/json"
	"fmt"
	"time"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	sirenia "github.com/flynn/flynn/pkg/sirenia/client"
	state "github.com/flynn/flynn/pkg/sirenia/state"
)

func (f *ClusterFixer) getControllerClient() (controller.Client, error) {
	instances, err := discoverd.NewService("controller").Instances()
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no controller instances")
	}
	key := instances[0].Meta["AUTH_KEY"]
	if key == "" {
		return nil, fmt.Errorf("controller AUTH_KEY not found")
	}
	return controller.NewClient("http://"+instances[0].Addr, key)
}

func appByName(c controller.Client, name string) (*ct.App, error) {
	apps, err := c.AppList()
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		if a.Name == name {
			return a, nil
		}
	}
	return nil, fmt.Errorf("app %q not found", name)
}

func (f *ClusterFixer) sireniaNeedsRebuild(svc string, c controller.Client, clusterState *state.State, instances []*discoverd.Instance) bool {
	if hasMissingVolumeJobs(c, svc) {
		return true
	}
	app, err := appByName(c, svc)
	if err != nil {
		return false
	}
	if clusterState == nil || clusterState.Primary == nil {
		return true
	}
	if clusterState.Primary.Meta["FLYNN_RELEASE_ID"] != "" && clusterState.Primary.Meta["FLYNN_RELEASE_ID"] != app.ReleaseID {
		return true
	}
	if jobID := clusterState.Primary.Meta["FLYNN_JOB_ID"]; jobID != "" && !instanceWithJobID(instances, jobID) {
		return true
	}
	leader, _ := discoverd.NewService(svc).Leader()
	if leader == nil && len(instances) > 0 {
		return true
	}
	return false
}

func hasMissingVolumeJobs(c controller.Client, appName string) bool {
	app, err := appByName(c, appName)
	if err != nil {
		return false
	}
	jobs, err := c.JobList(app.ID)
	if err != nil {
		return false
	}
	for _, job := range jobs {
		if job.Type != appName {
			continue
		}
		if job.HostError != nil && isMissingVolumeError(*job.HostError) {
			return true
		}
	}
	return false
}

func instanceWithJobID(instances []*discoverd.Instance, jobID string) bool {
	for _, inst := range instances {
		if inst.Meta["FLYNN_JOB_ID"] == jobID {
			return true
		}
	}
	return false
}

func desiredDBPeers(hostCount int, formation *ct.Formation, processName string) int {
	if formation != nil {
		if n := formation.Processes[processName]; n > 0 {
			return n
		}
	}
	return defaultDBPeers(hostCount)
}

func defaultDBPeers(hostCount int) int {
	if hostCount >= 3 {
		return 3
	}
	if hostCount >= 2 {
		return 2
	}
	return 1
}

// sireniaClusterUnderProvisioned reports whether a sirenia database app has
// fewer running peers than expected for the cluster size.
func (f *ClusterFixer) sireniaClusterUnderProvisioned(c controller.Client, svc string) bool {
	app, err := appByName(c, svc)
	if err != nil || app.Strategy != "sirenia" {
		return false
	}
	want := defaultDBPeers(len(f.hosts))
	if want == 0 {
		return false
	}
	jobs, err := c.JobList(app.ID)
	if err != nil {
		return false
	}
	up := 0
	for _, job := range jobs {
		if job.ReleaseID != app.ReleaseID || job.Type != svc {
			continue
		}
		if job.State == ct.JobStateUp {
			up++
		}
	}
	return up < want
}

// RebuildSireniaCluster recovers a sirenia database after missing controller
// volume records or stale discoverd cluster state. It never deletes data from
// hosts; stale controller volume records are decommissioned and the formation
// is rescaled so new volumes are provisioned when needed.
func (f *ClusterFixer) RebuildSireniaCluster(c controller.Client, svc string) error {
	log := f.l.New("fn", "RebuildSireniaCluster", "service", svc)

	app, err := appByName(c, svc)
	if err != nil {
		return err
	}
	if app.Strategy != "sirenia" {
		return fmt.Errorf("app %s is not a sirenia app", svc)
	}

	if err := f.FixStaleControllerVolumes(c); err != nil {
		log.Error("stale volume reconciliation failed", "err", err)
	}

	formation, err := c.GetFormation(app.ID, app.ReleaseID)
	if err != nil {
		return fmt.Errorf("get formation: %w", err)
	}
	target := desiredDBPeers(len(f.hosts), formation, svc)

	log.Info("resetting sirenia formation", "release", app.ReleaseID, "target_peers", target)
	if err := f.scaleDBFormation(c, app, svc, 0); err != nil {
		return err
	}
	time.Sleep(10 * time.Second)

	service := discoverd.NewService(svc)
	if meta, err := service.GetMeta(); err == nil {
		var clusterState state.State
		if len(meta.Data) > 0 {
			_ = json.Unmarshal(meta.Data, &clusterState)
		}
		instances, _ := service.Instances()
		if f.sireniaDiscoverdStateStale(&clusterState, instances, app.ReleaseID) {
			log.Info("removing stale discoverd service state", "service", svc)
			if err := discoverd.DefaultClient.RemoveService(svc); err != nil && !discoverd.IsNotFound(err) {
				log.Error("remove discoverd service", "err", err)
			}
			time.Sleep(2 * time.Second)
		}
	}

	if err := f.scaleDBFormation(c, app, svc, 1); err != nil {
		return err
	}
	time.Sleep(20 * time.Second)

	bootstrap := target
	if bootstrap > 2 {
		bootstrap = 2
	}
	if bootstrap >= 2 {
		if err := f.scaleDBFormation(c, app, svc, 2); err != nil {
			return err
		}
		if err := f.waitForDBPeers(c, app, svc, 2, 5*time.Minute); err != nil {
			log.Error("waiting for bootstrap peers", "err", err)
		}
	}

	if target > 2 {
		if err := f.scaleDBFormation(c, app, svc, target); err != nil {
			return err
		}
	}

	if err := f.waitForDBPeers(c, app, svc, target, 5*time.Minute); err != nil {
		return err
	}
	if err := f.waitForSireniaLeader(svc, 5*time.Minute); err != nil {
		return err
	}

	leader, err := service.Leader()
	if err != nil || leader == nil || leader.Addr == "" {
		return fmt.Errorf("no %s leader after rebuild", svc)
	}
	log.Info("waiting for database to become read-write", "addr", leader.Addr)
	if err := sirenia.NewClient(leader.Addr).WaitForReadWrite(5 * time.Minute); err != nil {
		return err
	}
	return f.EnsureSireniaWebAPI(c, svc)
}

func (f *ClusterFixer) sireniaDiscoverdStateStale(clusterState *state.State, instances []*discoverd.Instance, releaseID string) bool {
	if clusterState == nil || clusterState.Primary == nil {
		return true
	}
	if clusterState.Primary.Meta["FLYNN_RELEASE_ID"] != "" && clusterState.Primary.Meta["FLYNN_RELEASE_ID"] != releaseID {
		return true
	}
	if jobID := clusterState.Primary.Meta["FLYNN_JOB_ID"]; jobID != "" && !instanceWithJobID(instances, jobID) {
		return true
	}
	return false
}

func (f *ClusterFixer) scaleDBFormation(c controller.Client, app *ct.App, processName string, count int) error {
	formation, err := c.GetFormation(app.ID, app.ReleaseID)
	if err != nil {
		return err
	}
	if formation.Processes == nil {
		formation.Processes = make(map[string]int)
	}
	formation.Processes[processName] = count
	return c.PutFormation(formation)
}

func (f *ClusterFixer) waitForDBPeers(c controller.Client, app *ct.App, processName string, want int, timeout time.Duration) error {
	log := f.l.New("fn", "waitForDBPeers", "app", app.Name, "want", want)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jobs, err := c.JobList(app.ID)
		if err != nil {
			return err
		}
		up := 0
		for _, job := range jobs {
			if job.ReleaseID != app.ReleaseID || job.Type != processName {
				continue
			}
			if job.State == ct.JobStateUp {
				up++
			}
		}
		log.Info("database peer status", "up", up, "want", want)
		if up >= want {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %d %s peers on release %s", want, processName, app.ReleaseID)
}

func (f *ClusterFixer) waitForSireniaLeader(svc string, timeout time.Duration) error {
	log := f.l.New("fn", "waitForSireniaLeader", "service", svc)
	service := discoverd.NewService(svc)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		leader, err := service.Leader()
		if err == nil && leader != nil && leader.Addr != "" {
			log.Info("discoverd leader available", "addr", leader.Addr)
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s discoverd leader", svc)
}
