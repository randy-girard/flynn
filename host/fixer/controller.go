package fixer

import (
	"fmt"
	"time"

	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/controller/utils"
	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cluster"
)

func (f *ClusterFixer) FixController(instances []*discoverd.Instance, startScheduler bool) error {
	f.l.Info("found controller instance, checking critical formations")
	inst := instances[0]
	client, err := controller.NewClient("http://"+inst.Addr, inst.Meta["AUTH_KEY"])
	if err != nil {
		return fmt.Errorf("unexpected error creating controller client: %s", err)
	}

	// check that formations for critical components are expected
	apps := []string{"controller", "router", "discoverd", "flannel", "postgres", "tarreceive"}
	changes := make(map[string]*ct.Formation, len(apps)+2)
	var controllerFormation *ct.Formation
	var formationErr error
	for _, app := range apps {
		release, err := client.GetAppRelease(app)
		if err != nil {
			f.l.Error("error getting app release", "app", app, "err", err)
			if app == "controller" {
				formationErr = fmt.Errorf("error getting %s release: %s", app, err)
			}
			continue
		}
		formation, err := client.GetFormation(app, release.ID)
		if err != nil {
			f.l.Error("error getting formation", "app", app, "err", err)
			if app == "controller" {
				formationErr = fmt.Errorf("error getting %s formation: %s", app, err)
			}
			continue
		}
		if app == "controller" {
			controllerFormation = formation
		}
		for typ := range release.Processes {
			var want int
			if app == "postgres" && typ == "postgres" && len(f.hosts) > 1 && formation.Processes[typ] < 3 {
				want = 3
			} else if formation.Processes[typ] < 1 {
				want = 1
			}
			if want > 0 {
				f.l.Info("found broken formation", "app", app, "process", typ)
				if _, ok := changes[app]; !ok {
					if formation.Processes == nil {
						formation.Processes = make(map[string]int)
					}
					changes[app] = formation
				}
				changes[app].Processes[typ] = want
			}
		}
	}

	// Restore sirenia appliance formations when optional DBs are present.
	for _, app := range []string{"mariadb", "mongodb"} {
		release, err := client.GetAppRelease(app)
		if err != nil {
			if err == controller.ErrNotFound {
				continue
			}
			return fmt.Errorf("error getting %s release: %s", app, err)
		}
		formation, err := client.GetFormation(app, release.ID)
		if err != nil {
			if err == controller.ErrNotFound {
				continue
			}
			return fmt.Errorf("error getting %s formation: %s", app, err)
		}
		for typ := range release.Processes {
			want := 0
			if sireniaDataProcessType(app) == typ {
				if len(f.hosts) > 1 && formation.Processes[typ] < 3 {
					want = 3
				} else if formation.Processes[typ] < 1 {
					want = 1
				}
			} else if formation.Processes[typ] < 1 {
				want = 1
			}
			if want > 0 {
				f.l.Info("found broken formation", "app", app, "process", typ)
				if _, ok := changes[app]; !ok {
					if formation.Processes == nil {
						formation.Processes = make(map[string]int)
					}
					changes[app] = formation
				}
				changes[app].Processes[typ] = want
			}
		}
	}

	for app, formation := range changes {
		f.l.Info("fixing broken formation", "app", app)
		if err := client.PutFormation(formation); err != nil {
			f.l.Error("error putting formation", "app", app, "err", err)
		}
	}

	if startScheduler {
		if controllerFormation == nil {
			controllerFormation = f.controllerFormationFallback()
		}
		if err := f.StartScheduler(client, controllerFormation); err != nil {
			return err
		}
	}
	return formationErr
}

func (f *ClusterFixer) StartScheduler(client controller.Client, cf *ct.Formation) error {
	if f.hasRunningScheduler() {
		f.l.Info("scheduler job is running")
		return f.ensureSingleScheduler()
	}

	f.l.Info("scheduler is not up, attempting to fix")
	if err := f.FixHostBackend(); err != nil {
		f.l.Error("error ensuring host backends are configured", "err", err)
	}

	if cf == nil {
		cf = f.controllerFormationFallback()
	}
	if cf == nil {
		return fmt.Errorf("no controller formation available to start scheduler")
	}

	// start scheduler
	var schedulerJob *host.Job
	if cf != nil {
		ef, err := utils.ExpandFormation(client, cf)
		if err != nil {
			f.l.Error("error expanding controller formation, using job template", "err", err)
		} else {
			schedulerJob = utils.JobConfig(ef, "scheduler", f.hosts[0].ID(), "")
		}
	}
	if schedulerJob == nil {
		releases := f.FindAppReleaseJobs("controller", "scheduler")
		if len(releases) == 0 {
			return fmt.Errorf("no scheduler job template found on cluster hosts")
		}
		for _, schedulerJob = range releases[0] {
			break
		}
		schedulerJob = cloneJob(schedulerJob)
		schedulerJob.ID = cluster.GenerateJobID(f.hosts[0].ID(), "")
		f.FixJobEnv(schedulerJob)
	}
	if err := f.hosts[0].AddJob(schedulerJob); err != nil {
		return fmt.Errorf("error starting scheduler job on %s: %s", f.hosts[0].ID(), err)
	}
	f.l.Info("started scheduler job")
	if _, err := discoverd.GetInstances("controller-scheduler", 2*time.Minute); err != nil {
		return fmt.Errorf("scheduler did not register in discoverd: %s", err)
	}
	return f.ensureSingleScheduler()
}

// controllerFormationFallback builds a minimal controller formation from a
// running scheduler job template when the controller API is unavailable.
func (f *ClusterFixer) controllerFormationFallback() *ct.Formation {
	releases := f.FindAppReleaseJobs("controller", "scheduler")
	if len(releases) == 0 {
		return nil
	}
	var job *host.Job
	for _, job = range releases[0] {
		break
	}
	if job == nil {
		return nil
	}
	return &ct.Formation{
		AppID:     job.Metadata["flynn-controller.app"],
		ReleaseID: job.Metadata["flynn-controller.release"],
		Processes: map[string]int{"scheduler": 1},
	}
}

func (f *ClusterFixer) hasRunningScheduler() bool {
	if leader, err := discoverd.NewService("controller-scheduler").Leader(); err == nil && leader != nil {
		if jobID := leader.Meta["FLYNN_JOB_ID"]; jobID != "" {
			if hostID, err := cluster.ExtractHostID(jobID); err == nil {
				if h := f.Host(hostID); h != nil {
					if aj, err := h.GetJob(jobID); err == nil && schedulerJobActive(*aj) {
						return true
					}
				}
			}
		}
	}
	for _, h := range f.hosts {
		jobs, err := h.ListJobs()
		if err != nil {
			continue
		}
		for _, j := range jobs {
			if j.Job.Metadata["flynn-controller.app_name"] != "controller" || j.Job.Metadata["flynn-controller.type"] != "scheduler" {
				continue
			}
			if schedulerJobActive(j) {
				return true
			}
		}
	}
	return false
}

func schedulerJobActive(j host.ActiveJob) bool {
	if j.Status != host.StatusRunning && j.Status != host.StatusStarting {
		return false
	}
	return j.PID != nil && *j.PID > 0
}

// ensureSingleScheduler leaves one running scheduler job and stops the rest.
// Multiple schedulers fight over placements and can stack sirenia processes on
// the same host after recovery operations.
func (f *ClusterFixer) ensureSingleScheduler() error {
	var keepID string
	for _, h := range f.hosts {
		jobs, err := h.ListJobs()
		if err != nil {
			return fmt.Errorf("error listing jobs from %s: %s", h.ID(), err)
		}
		for _, j := range jobs {
			if j.Job.Metadata["flynn-controller.app_name"] != "controller" || j.Job.Metadata["flynn-controller.type"] != "scheduler" {
				continue
			}
			if j.Status != host.StatusRunning && j.Status != host.StatusStarting {
				continue
			}
			if !schedulerJobActive(j) {
				f.l.Info("stopping stuck scheduler job", "job.id", j.Job.ID)
				if err := h.StopJob(j.Job.ID); err != nil {
					f.l.Error("error stopping stuck scheduler", "id", j.Job.ID, "error", err)
				}
				continue
			}
			if keepID == "" {
				keepID = j.Job.ID
				continue
			}
			f.l.Info("stopping duplicate scheduler", "job.id", j.Job.ID)
			if err := h.StopJob(j.Job.ID); err != nil {
				f.l.Error("error stopping duplicate scheduler", "id", j.Job.ID, "error", err)
			}
		}
	}
	if keepID != "" {
		f.l.Info("scheduler singleton enforced", "job.id", keepID)
	}
	return nil
}

func (f *ClusterFixer) KillSchedulers() error {
	f.l.Info("killing any running schedulers to prevent interference")
	for _, h := range f.hosts {
		jobs, err := h.ListJobs()
		if err != nil {
			return fmt.Errorf("error listing jobs from %s: %s", h.ID(), err)
		}
		for _, j := range jobs {
			if j.Job.Metadata["flynn-controller.app_name"] != "controller" || j.Job.Metadata["flynn-controller.type"] != "scheduler" {
				continue
			}
			if j.Status != host.StatusRunning && j.Status != host.StatusStarting {
				continue
			}
			if err := h.StopJob(j.Job.ID); err != nil {
				f.l.Error("error stopping scheduler job", "id", j.Job.ID, "error", err)
			}
			f.l.Info("stopped scheduler instance", "job.id", j.Job.ID)
		}
	}
	return nil
}
