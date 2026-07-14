package fixer

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/host/volume"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
)

type ClusterFixer struct {
	hosts []*cluster.Host
	c     *cluster.Client
	l     log15.Logger
}

func NewClusterFixer(hosts []*cluster.Host, c *cluster.Client, l log15.Logger) *ClusterFixer {
	return &ClusterFixer{
		hosts: hosts,
		c:     c,
		l:     l,
	}
}

func (f *ClusterFixer) Run(args *docopt.Args, c *cluster.Client) error {
	f.c = c
	f.l = log15.New()
	var err error

	minHosts, err := strconv.Atoi(args.String["--min-hosts"])
	if err != nil || minHosts < 1 {
		return fmt.Errorf("invalid or missing --min-hosts value")
	}

	f.hosts, err = c.Hosts()
	if err != nil {
		f.l.Error("unable to list hosts from discoverd, falling back to peer IP list", "error", err)
		var ips []string
		if ipList := args.String["--peer-ips"]; ipList != "" {
			ips = strings.Split(ipList, ",")
			if minHosts == 0 {
				minHosts = len(ips)
			}
		}
		if len(ips) == 0 {
			return fmt.Errorf("error connecting to discoverd, use --peer-ips: %s", err)
		}
		if len(ips) < minHosts {
			return fmt.Errorf("number of peer IPs provided (%d) is less than --min-hosts (%d)", len(ips), minHosts)
		}

		f.hosts = make([]*cluster.Host, len(ips))
		for i, ip := range ips {
			url := fmt.Sprintf("http://%s:1113", ip)
			status, err := cluster.NewHost("", url, nil, nil).GetStatus()
			if err != nil {
				return fmt.Errorf("error connecting to %s: %s", ip, err)
			}
			f.hosts[i] = cluster.NewHost(status.ID, url, nil, nil)
		}
	}
	// check expected number of hosts
	if len(f.hosts) < minHosts {
		// TODO(titanous): be smarter about this
		return fmt.Errorf("expected at least %d hosts, but %d found", minHosts, len(f.hosts))
	}
	f.l.Info("found expected hosts", "n", len(f.hosts))

	if err := f.FixDiscoverd(); err != nil {
		return err
	}
	if err := f.FixFlannel(); err != nil {
		return err
	}
	if err := f.FixLocalDisk(); err != nil {
		return err
	}

	f.l.Info("waiting for discoverd to be available")
	timeout := time.After(time.Minute)
	for {
		var err error
		if _, err = discoverd.GetInstances("discoverd", 30*time.Second); err != nil {
			time.Sleep(100 * time.Millisecond)
		} else {
			break
		}
		select {
		case <-timeout:
			return fmt.Errorf("timed out waiting for discoverd, last error: %s", err)
		}
	}

	if err := f.FixHostBackend(); err != nil {
		f.l.Error("error ensuring host backends", "err", err)
	}

	f.l.Info("checking for running controller API")
	controllerService := discoverd.NewService("controller")
	controllerInstances, _ := controllerService.Instances()
	if len(controllerInstances) > 0 {
		f.l.Info("found running controller API instances", "n", len(controllerInstances))
		if err := f.FixController(controllerInstances, false); err != nil {
			f.l.Error("error fixing controller", "err", err)
			// if unable to write correct formations, we need to kill the scheduler so that the rest of this works
			if err := f.KillSchedulers(); err != nil {
				return err
			}
		}
	}

	f.l.Info("checking status of sirenia databases")
	for _, db := range []string{"postgres", "mariadb", "mongodb"} {
		f.l.Info("checking for database state", "db", db)
		if _, err := discoverd.NewService(db).GetMeta(); err != nil {
			if discoverd.IsNotFound(err) {
				f.l.Info("skipping recovery of db, no state in discoverd", "db", db)
				continue
			}
			f.l.Error("error checking database state", "db", db)
			return err
		}
		if err := f.CheckSirenia(db); err != nil {
			if err := f.KillSchedulers(); err != nil {
				return err
			}
			if err := f.FixSirenia(db); err != nil {
				f.l.Error("error fixing sirenia database", "db", db, "err", err)
			}
		}
	}

	f.l.Info("checking for running controller API")
	controllerInstances, _ = controllerService.Instances()
	if len(controllerInstances) == 0 {
		// kill schedulers to prevent interference
		if err := f.KillSchedulers(); err != nil {
			return err
		}
		controllerInstances, err = f.StartAppJob("controller", "web", "controller")
		if err != nil {
			return err
		}
	} else {
		f.l.Info("found running controller API instances", "n", len(controllerInstances))
	}

	if err := f.FixController(controllerInstances, true); err != nil {
		f.l.Error("error fixing controller", "err", err)
	} else if err := f.ensureSingleScheduler(); err != nil {
		f.l.Error("error enforcing scheduler singleton", "err", err)
	}

	f.l.Info("cluster fix complete")

	return nil
}

func (f *ClusterFixer) Host(id string) *cluster.Host {
	for _, h := range f.hosts {
		if h.ID() == id {
			return h
		}
	}
	return nil
}

func (f *ClusterFixer) StartAppJob(app, typ, service string) ([]*discoverd.Instance, error) {
	f.l.Info(fmt.Sprintf("no %s %s process running, getting release details from hosts", app, typ))
	releases := f.FindAppReleaseJobs(app, typ)
	if len(releases) == 0 {
		return nil, fmt.Errorf("didn't find any %s %s release jobs", app, typ)
	}

	// get a job template from the first release
	var job *host.Job
	for _, job = range releases[0] {
		break
	}
	host := f.hosts[0]
	job.ID = cluster.GenerateJobID(host.ID(), "")
	// provision new temporary volumes
	for i, v := range job.Config.Volumes {
		if v.DeleteOnStop {
			f.l.Info(fmt.Sprintf("provisioning volume for %s %s job", app, typ), "job.id", job.ID, "release", job.Metadata["flynn-controller.release"])
			vol := &volume.Info{}
			if err := host.CreateVolume("default", vol); err != nil {
				return nil, fmt.Errorf("error provisioning volume for %s %s job: %s", app, typ, err)
			}
			job.Config.Volumes[i].VolumeID = vol.ID
		}
	}
	f.FixJobEnv(job)
	// run it on the host
	f.l.Info(fmt.Sprintf("starting %s %s job", app, typ), "job.id", job.ID, "release", job.Metadata["flynn-controller.release"])
	if err := host.AddJob(job); err != nil {
		return nil, fmt.Errorf("error starting %s %s job: %s", app, typ, err)
	}
	f.l.Info("waiting for job to start")
	return discoverd.GetInstances(service, time.Minute)
}

func (f *ClusterFixer) GetJob(jobID string) (*host.Job, *cluster.Host, error) {
	hostID, err := cluster.ExtractHostID(jobID)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing host ID from %q", jobID)
	}
	host, err := f.c.Host(hostID)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get host for job lookup: %s", err)
	}
	job, err := host.GetJob(jobID)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get job from host: %s", err)
	}
	return job.Job, host, nil
}

// lookupSireniaJob returns a job template for a sirenia process. It prefers
// the job ID recorded in discoverd state, but falls back to any known job for
// the app on the expected host so volume bindings are preserved.
func (f *ClusterFixer) lookupSireniaJob(jobID, app, typ, hostID string) (*host.Job, *cluster.Host, error) {
	if jobID != "" {
		if job, host, err := f.GetJob(jobID); err == nil {
			return job, host, nil
		}
		if hostID == "" {
			hostID, _ = cluster.ExtractHostID(jobID)
		}
	}
	if hostID != "" {
		if h := f.Host(hostID); h != nil {
			for _, release := range f.FindAppReleaseJobs(app, typ) {
				if job, ok := release[hostID]; ok {
					return job, h, nil
				}
			}
		}
	}
	for _, release := range f.FindAppReleaseJobs(app, typ) {
		for id, job := range release {
			if h := f.Host(id); h != nil {
				return job, h, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("no %s %s job template found", app, typ)
}

// isSireniaJobUp reports whether a sirenia job is registered in discoverd or
// actively running on its host. Addr-based checks go stale when containers are
// restarted on new overlay addresses.
func (f *ClusterFixer) isSireniaJobUp(jobID string, instances []*discoverd.Instance) bool {
	if jobID == "" {
		return false
	}
	for _, i := range instances {
		if i.Meta["FLYNN_JOB_ID"] == jobID {
			return true
		}
	}
	hostID, err := cluster.ExtractHostID(jobID)
	if err != nil {
		return false
	}
	h := f.Host(hostID)
	if h == nil {
		return false
	}
	aj, err := h.GetJob(jobID)
	if err != nil {
		return false
	}
	if aj.Status != host.StatusRunning && aj.Status != host.StatusStarting {
		return false
	}
	// A container can remain "running" on the host with a stale overlay IP
	// after a flannel subnet change while discoverd registration is gone.
	// Treat it as down so recovery restarts it on the current subnet.
	if aj.InternalIP != "" {
		if cfg, err := f.inferHostNetworkConfig(h); err == nil {
			if !jobIPOnSubnet(aj.InternalIP, cfg.Subnet) {
				return false
			}
		}
	}
	return true
}

func jobIPOnSubnet(ipAddr, subnet string) bool {
	ip := net.ParseIP(ipAddr)
	if ip == nil {
		return true
	}
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return true
	}
	return ipnet.Contains(ip)
}

// FindAppReleaseJobs returns a slice with one map of host id to job for each
// known release of the given app and type, most recent first
func (f *ClusterFixer) FindAppReleaseJobs(app, typ string) []map[string]*host.Job {
	var sortReleases ReleasesByCreate
	releases := make(map[string]map[string]*host.ActiveJob) // map of releaseID -> hostID -> job ordered
	// connect to each host, list jobs, find distinct releases
	for _, h := range f.hosts {
		jobs, err := h.ListJobs()
		if err != nil {
			f.l.Error("error listing jobs", "host", h.ID(), "error", err)
			continue
		}
		for _, j := range jobs {
			if j.Job.Metadata["flynn-controller.app_name"] != app || j.Job.Metadata["flynn-controller.type"] != typ {
				continue
			}
			id := j.Job.Metadata["flynn-controller.release"]
			if id == "" {
				continue
			}
			m, ok := releases[id]
			if !ok {
				sortReleases = append(sortReleases, SortableRelease{id, j.StartedAt})
				m = make(map[string]*host.ActiveJob)
				releases[id] = m
			}
			if curr, ok := m[h.ID()]; ok && curr.StartedAt.Before(j.StartedAt) {
				continue
			}
			jobCopy := j
			m[h.ID()] = &jobCopy
		}
	}
	sort.Sort(sortReleases)
	res := make([]map[string]*host.Job, len(sortReleases))
	for i, r := range sortReleases {
		res[i] = make(map[string]*host.Job, len(releases[r.id]))
		for k, v := range releases[r.id] {
			res[i][k] = v.Job
		}
	}
	return res
}

func (f *ClusterFixer) FixJobEnv(job *host.Job) {
	job.Config.Env["FLYNN_JOB_ID"] = job.ID
}

// sireniaDataProcessType returns the controller process type for a sirenia
// database's data node (distinct from its API "web" process).
func sireniaDataProcessType(svc string) string {
	return svc
}

type SortableRelease struct {
	id string
	ts time.Time
}

type ReleasesByCreate []SortableRelease

func (p ReleasesByCreate) Len() int           { return len(p) }
func (p ReleasesByCreate) Less(i, j int) bool { return p[i].ts.After(p[j].ts) }
func (p ReleasesByCreate) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
