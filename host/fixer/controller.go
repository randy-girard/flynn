package fixer

import (
	"fmt"
	"time"

	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/controllerkey"
	"github.com/randy-girard/flynn/pkg/plugin"
)

// controllerClientFromInstances tries every discoverd controller instance.
// After a node crash the first overlay IP is often stale while another
// replica still answers.
func (f *ClusterFixer) controllerClientFromInstances(instances []*discoverd.Instance) (controller.Client, error) {
	key := ""
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if k := controller.KeyFromEnvOrMeta(inst.Meta); k != "" {
			key = k
			break
		}
	}
	if key == "" {
		key = controllerkey.FromHosts(f.hosts)
	}
	if key == "" {
		return nil, fmt.Errorf("controller AUTH_KEY is unavailable (discoverd no longer publishes it; set AUTH_KEY or CONTROLLER_KEY, or keep a running controller job)")
	}
	var last error
	for _, inst := range instances {
		if inst == nil || inst.Addr == "" {
			continue
		}
		if k := controller.KeyFromEnvOrMeta(inst.Meta); k != "" {
			key = k
		}
		c, err := controller.NewClient("http://"+inst.Addr, key)
		if err != nil {
			last = err
			continue
		}
		if _, err := c.GetAppRelease("controller"); err != nil {
			if f.l != nil {
				f.l.Error("controller instance unreachable, trying next", "addr", inst.Addr, "err", err)
			}
			last = err
			continue
		}
		return c, nil
	}
	if last == nil {
		last = fmt.Errorf("no reachable controller instances")
	}
	return nil, last
}

func (f *ClusterFixer) FixController(instances []*discoverd.Instance, startScheduler bool) error {
	f.l.Info("found controller instance, checking critical formations")
	client, err := f.controllerClientFromInstances(instances)
	if err != nil {
		f.l.Error("controller API unreachable", "err", err)
		if startScheduler {
			return f.StartScheduler(nil, nil)
		}
		return err
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

	// Restore sirenia plugin formations when those apps are present.
	pluginApps, listErr := client.AppList()
	if listErr != nil {
		f.l.Error("error listing apps for plugin formations", "err", listErr)
	} else {
		_ = plugin.WriteInstalled("", plugin.ListInstalled(pluginApps))
		for _, app := range pluginApps {
			if app == nil || !app.Plugin() || !plugin.IsSireniaManaged(app) {
				continue
			}
			release, err := client.GetAppRelease(app.ID)
			if err != nil {
				if err == controller.ErrNotFound {
					continue
				}
				return fmt.Errorf("error getting %s release: %s", app.Name, err)
			}
			formation, err := client.GetFormation(app.ID, release.ID)
			if err != nil {
				if err == controller.ErrNotFound {
					continue
				}
				return fmt.Errorf("error getting %s formation: %s", app.Name, err)
			}
			for typ := range release.Processes {
				want := 0
				if sireniaDataProcessType(app.Name) == typ {
					if len(f.hosts) > 1 && formation.Processes[typ] < 3 {
						want = 3
					} else if formation.Processes[typ] < 1 {
						want = 1
					}
				} else if formation.Processes[typ] < 1 {
					want = 1
				}
				if want > 0 {
					f.l.Info("found broken formation", "app", app.Name, "process", typ)
					if _, ok := changes[app.Name]; !ok {
						if formation.Processes == nil {
							formation.Processes = make(map[string]int)
						}
						changes[app.Name] = formation
					}
					changes[app.Name].Processes[typ] = want
				}
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
	if cf != nil && client != nil {
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
