package updaterdeploy

import (
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

// hostInfraApps are persistent host-daemon jobs. Controller all-at-once
// cannot bind their host ports on every node at once. Image pull only
// fills layer-cache; resurrection Dup()s the old Mountspecs. Replace
// these jobs per-host after layers are on disk.
var hostInfraApps = []string{"discoverd", "flannel"}

type hostInfraHost interface {
	ID() string
	ListJobs() (map[string]host.ActiveJob, error)
	StopJob(id string) error
	AddJob(job *host.Job) error
}

const hostInfraReplaceTimeout = 90 * time.Second

// ReplaceHostInfraJobs stops discoverd/flannel on one host at a time and
// starts them with Mountspecs from the pulled image. Other hosts keep DNS
// and overlay. force replaces even when layer IDs already match.
func ReplaceHostInfraJobs(hosts []*cluster.Host, images map[string]*ct.Artifact, force bool, log log15.Logger) error {
	list := make([]hostInfraHost, 0, len(hosts))
	for _, h := range hosts {
		if h != nil {
			list = append(list, h)
		}
	}
	return replaceHostInfraJobs(list, images, force, log)
}

func replaceHostInfraJobs(hosts []hostInfraHost, images map[string]*ct.Artifact, force bool, log log15.Logger) error {
	if log != nil {
		log.Info("replacing persistent host-infra jobs", "hosts", len(hosts), "force", force)
	}
	for _, h := range hosts {
		for _, name := range hostInfraApps {
			img := images[name]
			if img == nil {
				continue
			}
			if err := replaceHostInfraOnHost(h, name, img, force, log); err != nil {
				return err
			}
		}
	}
	return nil
}

func replaceHostInfraOnHost(h hostInfraHost, name string, image *ct.Artifact, force bool, log log15.Logger) error {
	jobs, err := h.ListJobs()
	if err != nil {
		return fmt.Errorf("list jobs on %s: %w", h.ID(), err)
	}
	old := findRunningHostInfraJob(jobs, name)
	if old == nil {
		if log != nil {
			log.Info("no running host-infra job to replace", "host", h.ID(), "app", name)
		}
		return nil
	}
	want := artifactLayerIDs(image)
	have := jobLayerIDs(old)
	if !force && reflect.DeepEqual(have, want) {
		if log != nil {
			log.Info("host-infra job already on pulled image", "host", h.ID(), "app", name)
		}
		return nil
	}
	template := hostInfraReplacementJob(old, h.ID(), image)
	if log != nil {
		log.Info("replacing host-infra job", "host", h.ID(), "app", name, "old", old.ID)
	}
	if err := h.StopJob(old.ID); err != nil {
		return fmt.Errorf("stop %s on %s: %w", name, h.ID(), err)
	}
	deadline := time.Now().Add(hostInfraReplaceTimeout)
	for time.Now().Before(deadline) {
		jobs, err := h.ListJobs()
		if err != nil {
			return fmt.Errorf("list jobs on %s: %w", h.ID(), err)
		}
		if j := findRunningHostInfraJobWithLayers(jobs, name, want); j != nil {
			if log != nil {
				log.Info("host-infra job running on pulled image", "host", h.ID(), "app", name, "job", j.ID)
			}
			return nil
		}
		stale := findHostInfraJobNotLayers(jobs, name, want)
		if stale != nil {
			if log != nil {
				log.Info("stopping scheduler restart on old host-infra image", "host", h.ID(), "app", name, "job", stale.ID)
			}
			_ = h.StopJob(stale.ID)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if startingHostInfraJobWithLayers(jobs, name, want) {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		job := hostInfraReplacementJob(template, h.ID(), image)
		if log != nil {
			log.Info("starting host-infra job", "host", h.ID(), "app", name, "job", job.ID)
		}
		if err := h.AddJob(job); err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s on %s did not become running on pulled image after %s", name, h.ID(), hostInfraReplaceTimeout)
}

func hostInfraReplacementJob(old *host.Job, hostID string, image *ct.Artifact) *host.Job {
	newJob := old.Dup()
	newJob.ID = cluster.GenerateJobID(hostID, "")
	newJob.Mountspecs = nil
	utils.SetupMountspecs(newJob, []*ct.Artifact{image})
	if newJob.Config.Env == nil {
		newJob.Config.Env = map[string]string{}
	}
	newJob.Config.Env["FLYNN_JOB_ID"] = newJob.ID
	if newJob.Metadata == nil {
		newJob.Metadata = map[string]string{}
	}
	newJob.Metadata[host.MetaControllerReason] = host.JobReasonReplace
	return newJob
}

func findRunningHostInfraJob(jobs map[string]host.ActiveJob, appName string) *host.Job {
	for _, j := range jobs {
		if j.Job == nil || j.Status != host.StatusRunning {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] == appName {
			return j.Job
		}
	}
	return nil
}

func findRunningHostInfraJobWithLayers(jobs map[string]host.ActiveJob, appName string, want []string) *host.Job {
	for _, j := range jobs {
		if j.Job == nil || j.Status != host.StatusRunning {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != appName {
			continue
		}
		if reflect.DeepEqual(jobLayerIDs(j.Job), want) {
			return j.Job
		}
	}
	return nil
}

func startingHostInfraJobWithLayers(jobs map[string]host.ActiveJob, appName string, want []string) bool {
	for _, j := range jobs {
		if j.Job == nil || j.Status != host.StatusStarting {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != appName {
			continue
		}
		if reflect.DeepEqual(jobLayerIDs(j.Job), want) {
			return true
		}
	}
	return false
}

func findHostInfraJobNotLayers(jobs map[string]host.ActiveJob, appName string, want []string) *host.Job {
	for _, j := range jobs {
		if j.Job == nil || j.Status != host.StatusRunning && j.Status != host.StatusStarting {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != appName {
			continue
		}
		if !reflect.DeepEqual(jobLayerIDs(j.Job), want) {
			return j.Job
		}
	}
	return nil
}

func jobLayerIDs(job *host.Job) []string {
	if job == nil {
		return nil
	}
	ids := make([]string, 0, len(job.Mountspecs))
	for _, m := range job.Mountspecs {
		if m != nil && m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func artifactLayerIDs(image *ct.Artifact) []string {
	if image == nil || image.Manifest() == nil || len(image.Manifest().Rootfs) != 1 {
		return nil
	}
	var ids []string
	for _, layer := range image.Manifest().Rootfs[0].Layers {
		if layer != nil && layer.ID != "" {
			ids = append(ids, layer.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
