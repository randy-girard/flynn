package updaterdeploy

import (
	"encoding/json"
	"fmt"
	"time"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	sireniaclient "github.com/flynn/flynn/pkg/sirenia/client"
	sirenia "github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/inconshreveable/log15"
)

const sireniaQuorumRepairTimeout = 2 * time.Minute

// RepairSireniaClusterQuorum restarts sirenia database jobs that are running on
// the host but no longer registered in discoverd, and waits for HA clusters to
// regain async peers. This can happen when a rolling deploy stops a peer for
// reconfiguration but the process never comes back online, or after orphan
// formation cleanup removes missing asyncs from cluster state while a zombie
// job remains.
func RepairSireniaClusterQuorum(ctrl controller.Client, log log15.Logger) error {
	if log == nil {
		log = log15.New()
	}

	apps, err := ctrl.AppList()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	appsByName := make(map[string]*ct.App, len(apps))
	for _, app := range apps {
		if app != nil && app.Name != "" {
			appsByName[app.Name] = app
		}
	}

	for _, appName := range sireniaApps {
		app, ok := appsByName[appName]
		if !ok {
			continue
		}
		if err := repairSireniaClusterQuorumForApp(ctrl, app, log); err != nil {
			return err
		}
	}
	return nil
}

func repairSireniaClusterQuorumForApp(ctrl controller.Client, app *ct.App, log log15.Logger) error {
	log = log.New("app", app.Name)

	service := discoverd.NewService(app.Name)
	meta, err := service.GetMeta()
	if err != nil || meta == nil || len(meta.Data) == 0 {
		return nil
	}

	var state sirenia.State
	if err := json.Unmarshal(meta.Data, &state); err != nil {
		return fmt.Errorf("decode %s sirenia state: %w", app.Name, err)
	}
	if state.Singleton || state.Primary == nil || state.Primary.Meta == nil {
		return nil
	}

	activeRelease := state.Primary.Meta["FLYNN_RELEASE_ID"]
	if activeRelease == "" {
		return nil
	}

	release, err := ctrl.GetRelease(activeRelease)
	if err != nil {
		return fmt.Errorf("get active %s release: %w", app.Name, err)
	}
	processType := release.Env["SIRENIA_PROCESS"]
	if processType == "" {
		processType = app.Name
	}

	formations, err := ctrl.FormationList(app.ID)
	if err != nil {
		return fmt.Errorf("list %s formations: %w", app.Name, err)
	}
	expected := 0
	for _, formation := range formations {
		if formation != nil && formation.ReleaseID == activeRelease {
			expected = formation.Processes[processType]
			break
		}
	}
	if expected <= 2 {
		return nil
	}

	if len(state.Async) > 0 && 2+len(state.Async) == expected {
		return nil
	}

	instances, err := service.Instances()
	if err != nil {
		return fmt.Errorf("list %s discoverd instances: %w", app.Name, err)
	}
	registered := registeredSireniaJobs(instances)

	jobs, err := ctrl.JobList(app.ID)
	if err != nil {
		return fmt.Errorf("list %s jobs: %w", app.Name, err)
	}

	var restarted int
	for _, job := range jobs {
		if job == nil || job.ReleaseID != activeRelease || job.Type != processType {
			continue
		}
		if job.State != ct.JobStateUp && job.State != ct.JobStateStarting {
			continue
		}
		inst := sireniaInstanceForJob(instances, job.ID)
		if inst != nil && sireniaInstanceHealthy(inst) {
			continue
		}
		log.Warn("restarting unregistered or unhealthy sirenia job",
			"job.id", job.ID, "state", job.State, "registered", registered[job.ID])
		if err := ctrl.DeleteJob(app.ID, job.ID); err != nil {
			return fmt.Errorf("restart %s job %s: %w", app.Name, job.ID, err)
		}
		restarted++
	}
	if restarted > 0 {
		log.Info("restarted sirenia jobs to restore quorum", "count", restarted)
	}

	return waitForSireniaAsyncQuorum(service, app.Name, expected, log)
}

func registeredSireniaJobs(instances []*discoverd.Instance) map[string]bool {
	registered := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.Meta == nil {
			continue
		}
		if jobID := inst.Meta["FLYNN_JOB_ID"]; jobID != "" {
			registered[jobID] = true
		}
	}
	return registered
}

func sireniaInstanceForJob(instances []*discoverd.Instance, jobID string) *discoverd.Instance {
	for _, inst := range instances {
		if inst != nil && inst.Meta != nil && inst.Meta["FLYNN_JOB_ID"] == jobID {
			return inst
		}
	}
	return nil
}

func sireniaInstanceHealthy(inst *discoverd.Instance) bool {
	if inst == nil || inst.Addr == "" {
		return false
	}
	status, err := sireniaclient.NewClient(inst.Addr).Status()
	if err != nil {
		return false
	}
	return status.Database != nil && status.Database.Running
}

func waitForSireniaAsyncQuorum(service discoverd.Service, appName string, expected int, log log15.Logger) error {
	if expected <= 2 {
		return nil
	}
	deadline := time.Now().Add(sireniaQuorumRepairTimeout)
	for time.Now().Before(deadline) {
		meta, err := service.GetMeta()
		if err == nil && meta != nil && len(meta.Data) > 0 {
			var state sirenia.State
			if err := json.Unmarshal(meta.Data, &state); err == nil {
				if len(state.Async) > 0 && 2+len(state.Async) == expected {
					log.Info("sirenia cluster regained async quorum", "asyncs", len(state.Async))
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s sirenia cluster to regain async peers", appName)
}
