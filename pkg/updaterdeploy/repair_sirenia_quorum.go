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
func RepairSireniaClusterQuorum(ctrl controller.Client, restartDownJobs bool, log log15.Logger) error {
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
		if err := repairSireniaClusterQuorumForApp(ctrl, app, restartDownJobs, log); err != nil {
			return err
		}
	}
	return nil
}

func repairSireniaClusterQuorumForApp(ctrl controller.Client, app *ct.App, restartDownJobs bool, log log15.Logger) error {
	log = log.New("app", app.Name)

	service := discoverdNewService(app.Name)
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
	var activeFormation *ct.Formation
	for _, formation := range formations {
		if formation != nil && formation.ReleaseID == activeRelease {
			activeFormation = formation
			expected = formation.Processes[processType]
			break
		}
	}
	if expected <= 2 {
		return nil
	}

	if sireniaAsyncQuorumSatisfied(&state, expected) && sireniaMetaPeersHealthy(&state) {
		return nil
	}
	if sireniaAsyncQuorumSatisfied(&state, expected) {
		log.Warn("sirenia meta reports quorum but peers are unhealthy, repairing")
	}

	instances, err := discoverdInstancesOrEmpty(service)
	if err != nil {
		return fmt.Errorf("list %s discoverd instances: %w", app.Name, err)
	}
	registered := registeredSireniaJobs(instances)

	jobs, err := ctrl.JobList(app.ID)
	if err != nil {
		return fmt.Errorf("list %s jobs: %w", app.Name, err)
	}

	restartIDs, downJobs := jobsToRestartForSireniaQuorum(jobs, activeRelease, processType, instances, checkSireniaInstanceHealthy)
	var restarted int
	for _, jobID := range restartIDs {
		log.Warn("restarting unregistered or unhealthy sirenia job",
			"job.id", jobID, "registered", registered[jobID])
		if err := ctrl.DeleteJob(app.ID, jobID); err != nil {
			return fmt.Errorf("restart %s job %s: %w", app.Name, jobID, err)
		}
		restarted++
	}
	if restarted > 0 {
		log.Info("restarted sirenia jobs to restore quorum", "count", restarted)
	}

	// A down job is no longer running and is invisible to the scheduler's
	// formation reconciliation, so killing it does nothing. When
	// --restart-down-jobs is set, re-assert the formation via a scale
	// request so the scheduler observes the shortfall and starts
	// replacements for the down peers.
	if restartDownJobs && downJobs > 0 && activeFormation != nil {
		if err := restartDownSireniaJobs(ctrl, app, activeFormation, downJobs, log); err != nil {
			return err
		}
	}

	return waitForSireniaAsyncQuorum(service, app.Name, expected, log)
}

// restartDownSireniaJobs re-asserts the active formation's process counts so
// the scheduler reconciles the difference between the expected count and the
// currently-running peers, starting replacements for any down jobs.
func restartDownSireniaJobs(ctrl controller.Client, app *ct.App, formation *ct.Formation, downJobs int, log log15.Logger) error {
	if len(formation.Processes) == 0 {
		return nil
	}
	processes := make(map[string]int, len(formation.Processes))
	for typ, n := range formation.Processes {
		processes[typ] = n
	}
	log.Warn("re-asserting sirenia formation to restart down jobs",
		"down_jobs", downJobs, "processes", processes)
	if err := ctrl.ScaleAppRelease(app.ID, formation.ReleaseID, ct.ScaleOptions{
		Processes: processes,
	}); err != nil {
		return fmt.Errorf("restart down %s jobs: %w", app.Name, err)
	}
	return nil
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

func sireniaMetaPeersHealthy(state *sirenia.State) bool {
	if state == nil || state.Primary == nil {
		return false
	}
	if !checkSireniaInstanceHealthy(state.Primary) {
		return false
	}
	if !state.Singleton && state.Sync != nil && !checkSireniaInstanceHealthy(state.Sync) {
		return false
	}
	for _, async := range state.Async {
		if !checkSireniaInstanceHealthy(async) {
			return false
		}
	}
	return true
}

// checkSireniaInstanceHealthy is the live Status() probe; tests may override
// it to avoid dialing unreachable fake addresses.
var checkSireniaInstanceHealthy = sireniaInstanceHealthy

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

func waitForSireniaAsyncQuorum(service discoverdService, appName string, expected int, log log15.Logger) error {
	if expected <= 2 {
		return nil
	}
	deadline := time.Now().Add(sireniaQuorumRepairTimeout)
	for time.Now().Before(deadline) {
		meta, err := service.GetMeta()
		if err == nil && meta != nil && len(meta.Data) > 0 {
			var state sirenia.State
			if err := json.Unmarshal(meta.Data, &state); err == nil {
				if sireniaAsyncQuorumSatisfied(&state, expected) {
					log.Info("sirenia cluster regained async quorum", "asyncs", len(state.Async))
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s sirenia cluster to regain async peers", appName)
}

// sireniaAsyncQuorumSatisfied reports whether discoverd meta shows a primary,
// sync, and enough async peers to match the formation process count.
// expected is the formation process count (primary+sync+asyncs).
func sireniaAsyncQuorumSatisfied(state *sirenia.State, expected int) bool {
	if state == nil || expected <= 2 {
		return false
	}
	return len(state.Async) > 0 && 2+len(state.Async) == expected
}

// jobsToRestartForSireniaQuorum returns up/starting job IDs that should be
// restarted because they are missing from discoverd or fail the health probe,
// plus a count of down jobs for optional formation re-assert.
func jobsToRestartForSireniaQuorum(jobs []*ct.Job, activeRelease, processType string, instances []*discoverd.Instance, healthy func(*discoverd.Instance) bool) (restart []string, down int) {
	if healthy == nil {
		healthy = func(*discoverd.Instance) bool { return false }
	}
	for _, job := range jobs {
		if job == nil || job.ReleaseID != activeRelease || job.Type != processType {
			continue
		}
		if job.State == ct.JobStateDown {
			down++
			continue
		}
		if job.State != ct.JobStateUp && job.State != ct.JobStateStarting {
			continue
		}
		inst := sireniaInstanceForJob(instances, job.ID)
		if inst != nil && healthy(inst) {
			continue
		}
		restart = append(restart, job.ID)
	}
	return restart, down
}
