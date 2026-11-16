package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

const placementCheckAction = "Run flynn-host placement:mode and flynn-host init --role. This check does not write the mode or any tag."

// placementJob is one running job sampled for the placement line.
// HostTags are the host's tags. Meta is the job metadata, which includes
// the app meta the scheduler copied.
type placementJob struct {
	HostTags map[string]string
	AppID    string
	AppName  string
	Type     string
	Meta     map[string]string
}

func listPlacementJobs(client *cluster.Client) ([]placementJob, error) {
	if client == nil {
		return nil, fmt.Errorf("no cluster client")
	}
	hosts, err := client.Hosts()
	if err != nil {
		return nil, err
	}
	var out []placementJob
	for _, h := range hosts {
		if h == nil {
			continue
		}
		jobs, err := h.ListActiveJobs()
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if job.Job == nil {
				continue
			}
			meta := job.Job.Metadata
			out = append(out, placementJob{
				HostTags: h.Tags(),
				AppID:    meta[host.MetaControllerApp],
				AppName:  meta[host.MetaControllerAppName],
				Type:     meta[host.MetaControllerType],
				Meta:     meta,
			})
		}
	}
	return out, nil
}

func addPlacementLine(add func(productionRow), api productionAPI, settings ct.WorkloadPlacement, readErr error, hosts []*cluster.Host, hostsErr error, jobs []placementJob, jobsErr error) {
	if readErr != nil {
		add(productionRow{status: "fail", check: "placement", detail: "error=" + readErr.Error(), action: placementCheckAction})
		return
	}
	mode := strings.TrimSpace(settings.Mode)
	if mode == "" {
		mode = placement.ModeMixed
	}
	if err := placement.ValidateMode(mode); err != nil {
		add(productionRow{status: "fail", check: "placement", detail: "error=unknown placement mode", action: placementCheckAction})
		return
	}
	if jobsErr != nil {
		add(productionRow{status: "fail", check: "placement", detail: "error=" + jobsErr.Error(), action: placementCheckAction})
		return
	}
	names, err := placementMismatches(api, jobs)
	if err != nil {
		add(productionRow{status: "fail", check: "placement", detail: "error=" + err.Error(), action: placementCheckAction})
		return
	}
	if mode == placement.ModeMixed {
		add(productionRow{status: "ok", check: "placement", detail: "mode=mixed", action: placementCheckAction})
		return
	}
	if hostsErr != nil {
		add(productionRow{status: "fail", check: "placement", detail: "error=" + hostsErr.Error(), action: placementCheckAction})
		return
	}
	core, app := placementRoleCounts(hosts)
	status := "ok"
	detail := fmt.Sprintf("mode=split core=%d app=%d", core, app)
	if core == 0 {
		status = "fail"
	} else if app == 0 {
		status = "warn"
	}
	if len(names) > 0 {
		if status != "fail" {
			status = "warn"
		}
		detail += " mismatch=" + strings.Join(names, ",")
	}
	add(productionRow{status: status, check: "placement", detail: detail, action: placementCheckAction})
}

func placementRoleCounts(hosts []*cluster.Host) (core, app int) {
	for _, h := range hosts {
		if h == nil || h.ShuttingDown() {
			continue
		}
		switch h.Tags()[placement.TagRole] {
		case placement.RoleCore:
			core++
		case placement.RoleApp:
			app++
		}
	}
	return core, app
}

// placementMismatches names apps whose non-omni jobs sit on the wrong role.
// The check uses split rules even while the cluster is mixed, then the caller
// drops the names so mixed does not warn. A release read error fails the line.
func placementMismatches(api productionAPI, jobs []placementJob) ([]string, error) {
	cache := map[string]*ct.Release{}
	seen := map[string]struct{}{}
	var names []string
	for _, job := range jobs {
		if job.AppID == "" {
			continue
		}
		rel, ok := cache[job.AppID]
		if !ok {
			var err error
			rel, err = api.GetAppRelease(job.AppID)
			if err != nil {
				return nil, err
			}
			cache[job.AppID] = rel
		}
		omni := false
		if rel != nil {
			if proc, ok := rel.Processes[job.Type]; ok {
				omni = proc.Omni
			}
		}
		app := &ct.App{ID: job.AppID, Name: job.AppName, Meta: map[string]string{}}
		for _, key := range []string{"flynn-system-app", "flynn-plugin", "flynn-datastore"} {
			if v, ok := job.Meta[key]; ok {
				app.Meta[key] = v
			}
		}
		if placement.HostAllows(placement.ModeSplit, placement.Classify(app, omni), job.HostTags) {
			continue
		}
		name := job.AppName
		if name == "" {
			name = job.AppID
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
