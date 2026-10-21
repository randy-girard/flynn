package updaterdeploy

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
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

const hostInfraReleaseKey = "flynn-controller.release"

// Overridable so tests do not wait for a scheduler that does not exist.
var (
	hostInfraSchedulerGrace      = 20 * time.Second
	hostInfraFormationRetryDelay = 2 * time.Second
	hostInfraStopRetryWait       = 200 * time.Millisecond
)

const hostInfraStopAttempts = 3

// HostInfraController is the controller API used to move discoverd/flannel
// onto a release that carries the pulled image.
type HostInfraController interface {
	GetApp(appID string) (*ct.App, error)
	GetAppRelease(appID string) (*ct.Release, error)
	GetArtifact(artifactID string) (*ct.Artifact, error)
	CreateArtifact(artifact *ct.Artifact) error
	AppReleaseList(appID string) ([]*ct.Release, error)
	CreateRelease(appID string, release *ct.Release) error
	GetFormation(appID, releaseID string) (*ct.Formation, error)
	PutFormation(formation *ct.Formation) error
	SetAppRelease(appID, releaseID string) error
}

// ReplaceHostInfraJobs stops discoverd/flannel on one host at a time and
// starts them with Mountspecs from the pulled image. Other hosts keep DNS
// and overlay. force replaces even when layer IDs already match.
//
// With a controller client, each app also moves to a release that uses the
// pulled image, so the scheduler restarts the new image after a reboot. The
// old formation drops one host at a time (flynn-avoid-host-ids) and the new
// formation picks it up (flynn-host-ids), so the two releases never share a
// host port.
func ReplaceHostInfraJobs(client HostInfraController, hosts []*cluster.Host, images map[string]*ct.Artifact, force bool, log log15.Logger) error {
	hosts = cluster.UniqueHosts(hosts)
	list := make([]hostInfraHost, 0, len(hosts))
	for _, h := range hosts {
		if h != nil {
			list = append(list, h)
		}
	}
	rollouts := make(map[string]*hostInfraRollout, len(hostInfraApps))
	if client != nil {
		for _, name := range hostInfraApps {
			img := images[name]
			if img == nil {
				continue
			}
			r, err := prepareHostInfraRollout(client, name, img, log)
			if err != nil {
				return err
			}
			rollouts[name] = r
		}
	}
	return replaceHostInfraJobs(list, images, rollouts, force, log)
}

func replaceHostInfraJobs(hosts []hostInfraHost, images map[string]*ct.Artifact, rollouts map[string]*hostInfraRollout, force bool, log log15.Logger) error {
	hosts = uniqueInfraHosts(hosts)
	if log != nil {
		log.Info("replacing persistent host-infra jobs", "hosts", len(hosts), "force", force)
	}
	for _, h := range hosts {
		for _, name := range hostInfraApps {
			img := images[name]
			if img == nil {
				continue
			}
			if err := replaceHostInfraOnHost(h, name, img, rollouts[name], force, log); err != nil {
				return err
			}
		}
	}
	for _, name := range hostInfraApps {
		if r := rollouts[name]; r.switching() {
			if err := r.finish(log); err != nil {
				return err
			}
		}
	}
	return nil
}

func replaceHostInfraOnHost(h hostInfraHost, name string, image *ct.Artifact, r *hostInfraRollout, force bool, log log15.Logger) error {
	jobs, err := h.ListJobs()
	if err != nil {
		return fmt.Errorf("list jobs on %s: %w", h.ID(), err)
	}
	switching := r.switching()
	release := r.targetReleaseID()
	old := findRunningHostInfraJob(jobs, name)
	if old == nil && !switching {
		if log != nil {
			log.Info("no running host-infra job to replace", "host", h.ID(), "app", name)
		}
		return nil
	}
	want := artifactLayerIDs(image)
	if old != nil && !force && hostInfraJobMatches(old, want, release) {
		if switching {
			if err := r.releaseHost(h.ID(), log); err != nil {
				return err
			}
			if err := r.adoptHost(h.ID(), log); err != nil {
				return err
			}
		}
		if log != nil {
			log.Info("host-infra job already on pulled image", "host", h.ID(), "app", name)
		}
		return nil
	}
	source := old
	if source == nil {
		source = anyHostInfraJob(jobs, name)
	}
	var template *host.Job
	if source != nil {
		template = hostInfraReplacementJob(source, h.ID(), image, release)
	}
	if switching {
		if err := r.releaseHost(h.ID(), log); err != nil {
			return err
		}
	}
	if old != nil {
		if log != nil {
			log.Info("replacing host-infra job", "host", h.ID(), "app", name, "old", old.ID)
		}
		if err := stopHostInfraJob(h, old.ID, name); err != nil {
			return err
		}
	}
	adopted := false
	var adoptedAt time.Time
	deadline := time.Now().Add(hostInfraReplaceTimeout)
	for time.Now().Before(deadline) {
		jobs, err := h.ListJobs()
		if err != nil {
			return fmt.Errorf("list jobs on %s: %w", h.ID(), err)
		}
		if j := findRunningHostInfraJobWithLayers(jobs, name, want, release); j != nil {
			if log != nil {
				log.Info("host-infra job running on pulled image", "host", h.ID(), "app", name, "job", j.ID)
			}
			return nil
		}
		stale := findStaleHostInfraJob(jobs, name, want, release)
		if stale != nil {
			if log != nil {
				log.Info("stopping scheduler restart on old host-infra image", "host", h.ID(), "app", name, "job", stale.ID)
			}
			_ = stopHostInfraJob(h, stale.ID, name)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if startingHostInfraJobWithLayers(jobs, name, want, release) {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if switching {
			if !adopted {
				if err := r.adoptHost(h.ID(), log); err != nil {
					return err
				}
				adopted = true
				adoptedAt = time.Now()
				continue
			}
			if template == nil || time.Since(adoptedAt) < hostInfraSchedulerGrace {
				time.Sleep(250 * time.Millisecond)
				continue
			}
		}
		if msg := templateVolumeMissing(template, jobs, name); msg != "" {
			return fmt.Errorf("%s on %s: data volume is missing (%s)", name, h.ID(), msg)
		}
		job := hostInfraReplacementJob(template, h.ID(), image, release)
		if log != nil {
			log.Info("starting host-infra job", "host", h.ID(), "app", name, "job", job.ID)
		}
		if err := h.AddJob(job); err != nil {
			if volumeMissing(err) {
				return fmt.Errorf("%s on %s: %w", name, h.ID(), err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s on %s did not become running on pulled image after %s", name, h.ID(), hostInfraReplaceTimeout)
}

func anyHostInfraJob(jobs map[string]host.ActiveJob, appName string) *host.Job {
	var found *host.Job
	for _, j := range jobs {
		if j.Job == nil || j.Job.Metadata["flynn-controller.app_name"] != appName {
			continue
		}
		found = j.Job
		if j.Status == host.StatusRunning || j.Status == host.StatusStarting {
			return j.Job
		}
	}
	return found
}

func uniqueInfraHosts(hosts []hostInfraHost) []hostInfraHost {
	seen := make(map[string]bool, len(hosts))
	out := make([]hostInfraHost, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		id := h.ID()
		if id != "" && seen[id] {
			continue
		}
		if id != "" {
			seen[id] = true
		}
		out = append(out, h)
	}
	return out
}

func jobStillActive(jobs map[string]host.ActiveJob, id string) bool {
	j, ok := jobs[id]
	if !ok || j.Job == nil {
		return false
	}
	return j.Status == host.StatusRunning || j.Status == host.StatusStarting
}

func isTransientHostStop(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed") ||
		strings.Contains(msg, "eof")
}

// stopHostInfraJob retries StopJob on transient HTTP errors. A timeout
// after the job is already gone (Delete hung, then the container exited)
// is treated as success so one slow host does not abort the whole update.
func stopHostInfraJob(h hostInfraHost, id, name string) error {
	var last error
	for attempt := 1; attempt <= hostInfraStopAttempts; attempt++ {
		err := h.StopJob(id)
		if err == nil {
			return nil
		}
		last = err
		jobs, listErr := h.ListJobs()
		if listErr == nil && !jobStillActive(jobs, id) {
			return nil
		}
		if !isTransientHostStop(err) {
			return fmt.Errorf("stop %s on %s: %w", name, h.ID(), err)
		}
		if attempt < hostInfraStopAttempts {
			time.Sleep(hostInfraStopRetryWait)
		}
	}
	if jobs, listErr := h.ListJobs(); listErr == nil && !jobStillActive(jobs, id) {
		return nil
	}
	return fmt.Errorf("stop %s on %s: %w", name, h.ID(), last)
}

func volumeMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "volume") && strings.Contains(msg, "does not exist")
}

// templateVolumeMissing reports when the job we are about to start pins a
// volume that a previous start already found missing. Older failures for a
// different volume must not block a healthy replacement.
func templateVolumeMissing(template *host.Job, jobs map[string]host.ActiveJob, appName string) string {
	if template == nil || len(template.Config.Volumes) == 0 {
		return ""
	}
	for _, j := range jobs {
		if j.Job == nil || j.Error == nil || j.Status != host.StatusFailed {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != appName || !volumeMissing(fmt.Errorf("%s", *j.Error)) {
			continue
		}
		for _, v := range template.Config.Volumes {
			if v.VolumeID != "" && strings.Contains(*j.Error, v.VolumeID) {
				return *j.Error
			}
		}
	}
	return ""
}

func hostInfraReplacementJob(old *host.Job, hostID string, image *ct.Artifact, releaseID string) *host.Job {
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
	if releaseID != "" {
		newJob.Metadata[hostInfraReleaseKey] = releaseID
	}
	return newJob
}

// hostInfraJobMatches reports whether job runs the wanted layers and, when
// releaseID is set, belongs to that controller release.
func hostInfraJobMatches(job *host.Job, want []string, releaseID string) bool {
	if !reflect.DeepEqual(jobLayerIDs(job), want) {
		return false
	}
	return releaseID == "" || job.Metadata[hostInfraReleaseKey] == releaseID
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

func findRunningHostInfraJobWithLayers(jobs map[string]host.ActiveJob, appName string, want []string, releaseID string) *host.Job {
	for _, j := range jobs {
		if j.Job == nil || j.Status != host.StatusRunning {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != appName {
			continue
		}
		if hostInfraJobMatches(j.Job, want, releaseID) {
			return j.Job
		}
	}
	return nil
}

func startingHostInfraJobWithLayers(jobs map[string]host.ActiveJob, appName string, want []string, releaseID string) bool {
	for _, j := range jobs {
		if j.Job == nil || j.Status != host.StatusStarting {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != appName {
			continue
		}
		if hostInfraJobMatches(j.Job, want, releaseID) {
			return true
		}
	}
	return false
}

// findStaleHostInfraJob returns a running or starting job on the old layers
// or (when releaseID is set) on another controller release.
func findStaleHostInfraJob(jobs map[string]host.ActiveJob, appName string, want []string, releaseID string) *host.Job {
	for _, j := range jobs {
		if j.Job == nil || j.Status != host.StatusRunning && j.Status != host.StatusStarting {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != appName {
			continue
		}
		if !hostInfraJobMatches(j.Job, want, releaseID) {
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

// hostInfraRollout moves one host-infra app from oldRelease to newRelease.
// When both are the same release the app already uses the pulled image and
// only the jobs are replaced.
type hostInfraRollout struct {
	client     HostInfraController
	appID      string
	oldRelease *ct.Release
	newRelease *ct.Release
	processes  map[string]int
	oldTags    map[string]map[string]string
	avoid      []string
	newHosts   []string
}

func (r *hostInfraRollout) switching() bool {
	return r != nil && r.oldRelease != nil && r.newRelease != nil && r.oldRelease.ID != r.newRelease.ID
}

func (r *hostInfraRollout) targetReleaseID() string {
	if r == nil || r.newRelease == nil {
		return ""
	}
	return r.newRelease.ID
}

// prepareHostInfraRollout returns the release the app's jobs should run.
// A new release (or one left by an interrupted update) is used when the
// current release does not carry the pulled image.
func prepareHostInfraRollout(client HostInfraController, name string, image *ct.Artifact, log log15.Logger) (*hostInfraRollout, error) {
	app, err := client.GetApp(name)
	if err == controller.ErrNotFound {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("get %s app: %w", name, err)
	}
	current, err := client.GetAppRelease(app.ID)
	if err != nil {
		return nil, fmt.Errorf("get %s release: %w", name, err)
	}
	if len(current.ArtifactIDs) == 0 {
		return nil, nil
	}
	r := &hostInfraRollout{client: client, appID: app.ID, oldRelease: current, newRelease: current}
	artifact, err := client.GetArtifact(current.ArtifactIDs[0])
	if err != nil {
		return nil, fmt.Errorf("get %s release artifact: %w", name, err)
	}
	if artifact.ID == image.ID || artifactsShareLayers(artifact, image) {
		return r, nil
	}
	oldFormation, err := client.GetFormation(app.ID, current.ID)
	if err != nil {
		return nil, fmt.Errorf("get %s formation: %w", name, err)
	}
	r.processes = copyProcesses(oldFormation.Processes)
	r.oldTags = oldFormation.Tags
	r.avoid = formationHostIDs(oldFormation.Tags, ct.FormationAvoidHostIDsTag)

	if err := CreateArtifactWithRetry(client, name, image, log); err != nil {
		return nil, err
	}
	releases, err := client.AppReleaseList(app.ID)
	if err != nil {
		return nil, fmt.Errorf("list %s releases: %w", name, err)
	}
	next := ReusableUpdateRelease(current.ID, image.ID, releases)
	if next == nil {
		release := *current
		release.ID = ""
		release.CreatedAt = nil
		release.ArtifactIDs = append([]string{image.ID}, current.ArtifactIDs[1:]...)
		if err := client.CreateRelease(app.ID, &release); err != nil {
			return nil, fmt.Errorf("create %s release: %w", name, err)
		}
		next = &release
	} else if f, err := client.GetFormation(app.ID, next.ID); err == nil && f != nil {
		r.newHosts = formationHostIDs(f.Tags, ct.FormationHostIDsTag)
		if processCount(r.processes) == 0 {
			r.processes = copyProcesses(f.Processes)
		}
	}
	r.newRelease = next
	if processCount(r.processes) == 0 {
		if err := client.SetAppRelease(app.ID, next.ID); err != nil {
			return nil, fmt.Errorf("set %s release: %w", name, err)
		}
		r.oldRelease = next
	}
	if log != nil {
		log.Info("moving host-infra app to pulled image release", "app", name,
			"old_release", current.ID, "new_release", next.ID, "switching", r.switching())
	}
	return r, nil
}

// releaseHost stops the old release from scheduling onto hostID.
func (r *hostInfraRollout) releaseHost(hostID string, log log15.Logger) error {
	r.avoid = appendHostID(r.avoid, hostID)
	return r.putFormation(r.oldRelease.ID, r.processes, r.oldFormationTags(), log)
}

// adoptHost lets the new release schedule onto hostID.
func (r *hostInfraRollout) adoptHost(hostID string, log log15.Logger) error {
	r.newHosts = appendHostID(r.newHosts, hostID)
	tags := make(map[string]map[string]string, len(r.processes))
	for typ := range r.processes {
		tags[typ] = map[string]string{ct.FormationHostIDsTag: ct.EncodeHostIDsTag(r.newHosts)}
	}
	return r.putFormation(r.newRelease.ID, r.processes, tags, log)
}

// finish runs the new release on every host, points the app at it, and
// scales the old release to zero.
func (r *hostInfraRollout) finish(log log15.Logger) error {
	if err := r.putFormation(r.newRelease.ID, r.processes, nil, log); err != nil {
		return err
	}
	if err := r.client.SetAppRelease(r.appID, r.newRelease.ID); err != nil {
		return fmt.Errorf("set host-infra app release: %w", err)
	}
	zero := make(map[string]int, len(r.processes))
	for typ := range r.processes {
		zero[typ] = 0
	}
	if err := r.putFormation(r.oldRelease.ID, zero, r.oldFormationTags(), log); err != nil {
		return err
	}
	if log != nil {
		log.Info("host-infra app moved to pulled image release", "app", r.appID, "release", r.newRelease.ID)
	}
	return nil
}

func (r *hostInfraRollout) oldFormationTags() map[string]map[string]string {
	tags := make(map[string]map[string]string, len(r.processes))
	for typ := range r.processes {
		inner := make(map[string]string, len(r.oldTags[typ])+1)
		for k, v := range r.oldTags[typ] {
			inner[k] = v
		}
		inner[ct.FormationAvoidHostIDsTag] = ct.EncodeHostIDsTag(r.avoid)
		tags[typ] = inner
	}
	return tags
}

func (r *hostInfraRollout) putFormation(releaseID string, processes map[string]int, tags map[string]map[string]string, log log15.Logger) error {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = r.client.PutFormation(&ct.Formation{
			AppID:     r.appID,
			ReleaseID: releaseID,
			Processes: copyProcesses(processes),
			Tags:      tags,
		})
		if err == nil {
			return nil
		}
		if log != nil {
			log.Warn("error updating host-infra formation, retrying", "app", r.appID, "release", releaseID, "attempt", attempt, "err", err)
		}
		time.Sleep(hostInfraFormationRetryDelay)
	}
	return fmt.Errorf("update %s formation for release %s: %w", r.appID, releaseID, err)
}

func artifactsShareLayers(a, b *ct.Artifact) bool {
	want := artifactLayerIDs(b)
	return len(want) > 0 && reflect.DeepEqual(artifactLayerIDs(a), want)
}

func formationHostIDs(tags map[string]map[string]string, key string) []string {
	var ids []string
	for _, inner := range tags {
		ids = append(ids, ct.ParseHostIDsTag(inner[key])...)
	}
	return ct.ParseHostIDsTag(ct.EncodeHostIDsTag(ids))
}

func appendHostID(ids []string, id string) []string {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func copyProcesses(p map[string]int) map[string]int {
	out := make(map[string]int, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

func processCount(p map[string]int) int {
	n := 0
	for _, v := range p {
		n += v
	}
	return n
}
