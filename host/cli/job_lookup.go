package cli

import (
	"fmt"
	"strings"

	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func lookupLogJobs(client *cluster.Client, name string, all bool) (sortJobs, error) {
	jobs, err := resolveJobs(client, name, all)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		if all {
			return nil, fmt.Errorf("no jobs found for %q (not a job ID, process name, or app name)", name)
		}
		return nil, fmt.Errorf("no running jobs for %q (try flynn-host log --all %s)", name, name)
	}
	return jobs, nil
}

func resolveOneJob(client *cluster.Client, name string, all bool) (*host.ActiveJob, error) {
	jobs, err := resolveJobs(client, name, all)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no jobs found for %q (not a job ID, process name, or app name)", name)
	}
	if len(jobs) > 1 {
		return nil, fmt.Errorf("%q matches %d jobs; use a job ID from flynn-host ps", name, len(jobs))
	}
	job := jobs[0]
	return &job, nil
}

func resolveJobs(client *cluster.Client, name string, all bool) (sortJobs, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("job ID, process name, or app name required")
	}
	if hostID, err := cluster.ExtractHostID(name); err == nil {
		if hc, err := client.Host(hostID); err == nil {
			if job, err := hc.GetJob(name); err == nil && job != nil && job.Job != nil {
				return sortJobs{*job}, nil
			}
		}
	}
	jobs, err := jobList(client, all)
	if err != nil {
		return nil, err
	}
	return jobsMatching(jobs, name), nil
}

func jobsMatching(jobs []host.ActiveJob, name string) sortJobs {
	var out sortJobs
	for _, job := range jobs {
		if jobMatchesLookup(job, name) {
			out = append(out, job)
		}
	}
	return out
}

func jobMatchesLookup(job host.ActiveJob, name string) bool {
	return jobMatchesApp(job, name) || jobMatchesProcessName(job, name)
}

func jobMatchesProcessName(job host.ActiveJob, name string) bool {
	if name == "" || job.Job == nil {
		return false
	}
	short := host.JobDisplayName(job.Job)
	if short != "" && short == name {
		return true
	}
	if job.Job.Metadata != nil {
		if job.Job.Metadata[host.MetaControllerName] == name {
			return true
		}
		if job.Job.Metadata["name"] == name {
			return true
		}
		app := job.Job.Metadata["flynn-controller.app_name"]
		if app != "" && short != "" && app+"."+short == name {
			return true
		}
	}
	return false
}
