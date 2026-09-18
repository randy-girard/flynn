package updaterdeploy

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/plugin"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
	sirenia "github.com/randy-girard/flynn/pkg/sirenia/state"
)

const (
	defaultSireniaQuorumRepairTimeout = 2 * time.Minute
	sireniaRepairRetryPendingGrace    = 5 * time.Minute
	defaultSireniaRepairSeedGrace     = 30 * time.Minute
	sireniaRepairSeedGraceEnv         = "SIRENIA_REPAIR_SEED_GRACE"
)

// Overridable for tests so wait loops finish quickly.
var (
	sireniaQuorumRepairTimeout = defaultSireniaQuorumRepairTimeout
	sireniaQuorumPollInterval  = 2 * time.Second
	sireniaRepairClock         = time.Now
)

// RepairSireniaClusterQuorum restarts sirenia database jobs that are stuck
// (unreachable, retry-pending past grace, role mismatch, or not running past
// the seed grace window) or no longer registered in discoverd, and waits for
// HA clusters to regain healthy async peers. Peers that are still seeding
// within the grace window are left alone so a large basebackup is not killed
// mid-flight during upgrades.
func RepairSireniaClusterQuorum(ctrl controller.Client, restartDownJobs bool, log log15.Logger) error {
	if log == nil {
		log = log15.New()
	}

	apps, err := ctrl.AppList()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}

	for _, app := range apps {
		if app == nil || !plugin.IsSireniaManaged(app) {
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

	restartIDs, reasons, downJobs := jobsToRestartForSireniaQuorum(jobs, activeRelease, processType, instances, sireniaRepairClock())
	var restarted int
	for _, jobID := range restartIDs {
		log.Warn("restarting unregistered or stuck sirenia job",
			"job.id", jobID, "registered", registered[jobID], "reason", reasons[jobID])
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

// sireniaPeerProbe fetches Status for a discoverd instance. Tests may override
// probeSireniaInstance to avoid dialing unreachable fake addresses.
type sireniaPeerProbe func(inst *discoverd.Instance) (*sireniaclient.Status, error)

var probeSireniaInstance sireniaPeerProbe = defaultProbeSireniaInstance

func defaultProbeSireniaInstance(inst *discoverd.Instance) (*sireniaclient.Status, error) {
	if inst == nil || inst.Addr == "" {
		return nil, fmt.Errorf("missing instance address")
	}
	return sireniaclient.NewClient(inst.Addr).Status()
}

// checkSireniaInstanceHealthy is a thin wrapper around the stuck probe with
// unknown job age (age=0). Seeding peers within the seed grace are treated as
// healthy; unreachable / retry-pending / role-mismatch peers are not.
// Tests may override it to avoid dialing.
var checkSireniaInstanceHealthy = sireniaInstanceHealthy

func sireniaInstanceHealthy(inst *discoverd.Instance) bool {
	status, err := probeSireniaInstance(inst)
	return !sireniaInstanceStuck(status, err, 0, sireniaRepairClock())
}

// sireniaInstanceStuck reports whether a peer should be restarted. A peer is
// stuck when Status() fails, RetryPending is older than the retry grace, its
// local role never converged while meta lists it in the cluster, or the
// database has not been Running for longer than the seed grace.
func sireniaInstanceStuck(status *sireniaclient.Status, err error, jobAge time.Duration, now time.Time) bool {
	return sireniaStuckReason(status, err, jobAge, now, sireniaRepairSeedGrace(), sireniaRepairRetryPendingGrace) != ""
}

func sireniaStuckReason(status *sireniaclient.Status, probeErr error, jobAge time.Duration, now time.Time, seedGrace, retryGrace time.Duration) string {
	if probeErr != nil || status == nil {
		return "unreachable"
	}
	if status.Peer != nil && status.Peer.RetryPending != nil {
		if now.Sub(*status.Peer.RetryPending) >= retryGrace {
			return "retry_pending"
		}
	}
	if status.Peer != nil {
		role := status.Peer.Role
		if (role == sirenia.RoleUnassigned || role == sirenia.RoleUnknown) &&
			peerIDListedInClusterState(status.Peer.ID, status.Peer.State) {
			return "role_mismatch"
		}
	}
	running := status.Database != nil && status.Database.Running
	if !running && jobAge >= seedGrace {
		return "not_running_past_grace"
	}
	return ""
}

func peerIDListedInClusterState(peerID string, state *sirenia.State) bool {
	if state == nil || peerID == "" {
		return false
	}
	match := func(inst *discoverd.Instance) bool {
		if inst == nil || inst.Meta == nil {
			return false
		}
		for _, v := range inst.Meta {
			if v == peerID {
				return true
			}
		}
		return false
	}
	if match(state.Primary) || match(state.Sync) {
		return true
	}
	for _, async := range state.Async {
		if match(async) {
			return true
		}
	}
	return false
}

func sireniaRepairSeedGrace() time.Duration {
	if v := os.Getenv(sireniaRepairSeedGraceEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultSireniaRepairSeedGrace
}

func sireniaJobAge(job *ct.Job, now time.Time) time.Duration {
	if job == nil {
		return 0
	}
	if job.CreatedAt != nil {
		return now.Sub(*job.CreatedAt)
	}
	if job.UpdatedAt != nil {
		return now.Sub(*job.UpdatedAt)
	}
	// Unknown age: treat as within seed grace so we do not kill on
	// Running=false alone.
	return 0
}

func waitForSireniaAsyncQuorum(service discoverdService, appName string, expected int, log log15.Logger) error {
	if expected <= 2 {
		return nil
	}
	deadline := sireniaRepairClock().Add(sireniaQuorumRepairTimeout)
	for sireniaRepairClock().Before(deadline) {
		meta, err := service.GetMeta()
		if err == nil && meta != nil && len(meta.Data) > 0 {
			var state sirenia.State
			if err := json.Unmarshal(meta.Data, &state); err == nil {
				if sireniaAsyncQuorumSatisfied(&state, expected) && sireniaMetaPeersHealthy(&state) {
					log.Info("sirenia cluster regained async quorum", "asyncs", len(state.Async))
					return nil
				}
			}
		}
		time.Sleep(sireniaQuorumPollInterval)
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
// restarted because they are missing from discoverd or are stuck, a map of
// job ID → stuck reason for logging, plus a count of down jobs for optional
// formation re-assert.
func jobsToRestartForSireniaQuorum(jobs []*ct.Job, activeRelease, processType string, instances []*discoverd.Instance, now time.Time) (restart []string, reasons map[string]string, down int) {
	reasons = make(map[string]string)
	seedGrace := sireniaRepairSeedGrace()
	retryGrace := sireniaRepairRetryPendingGrace
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
		if inst == nil {
			restart = append(restart, job.ID)
			reasons[job.ID] = "unregistered"
			continue
		}
		status, err := probeSireniaInstance(inst)
		reason := sireniaStuckReason(status, err, sireniaJobAge(job, now), now, seedGrace, retryGrace)
		if reason == "" {
			continue
		}
		restart = append(restart, job.ID)
		reasons[job.ID] = reason
	}
	return restart, reasons, down
}
