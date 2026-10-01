package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"

	"github.com/docker/go-units"
	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("ps", runPs, `
usage: flynn-host ps [-a|--all] [-q|--quiet] [-f <format>]

List jobs`)
}

type sortJobs []host.ActiveJob

func (s sortJobs) Len() int           { return len(s) }
func (s sortJobs) Less(i, j int) bool { return s[i].CreatedAt.Sub(s[j].CreatedAt) < 0 }
func (s sortJobs) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func runPs(args *docopt.Args, client *cluster.Client) error {
	jobs, err := jobList(client, args.Bool["-a"] || args.Bool["--all"])
	if err != nil {
		return err
	}
	if args.Bool["-q"] || args.Bool["--quiet"] {
		for _, job := range jobs {
			if format := args.String["<format>"]; format != "" {
				tmpl, err := template.New("format").Funcs(template.FuncMap{
					"metadata": func(key string) string { return job.Job.Metadata[key] },
				}).Parse(format + "\n")
				if err != nil {
					return err
				}
				if err := tmpl.Execute(os.Stdout, job); err != nil {
					return err
				}
				continue
			}
			fmt.Println(job.Job.ID)
		}
		return nil
	}
	printJobs(jobs, os.Stdout)
	return nil
}

func jobList(client *cluster.Client, all bool) (sortJobs, error) {
	hosts, err := client.Hosts()
	if err != nil {
		return nil, fmt.Errorf("could not list hosts: %s", discoverdListHint(err))
	}
	if len(hosts) == 0 {
		return nil, errors.New("no hosts found")
	}

	var jobs []host.ActiveJob
	for _, h := range hosts {
		hostJobs, err := h.ListJobs()
		if err != nil {
			return nil, fmt.Errorf("could not get jobs for host %s: %s", h.ID(), err)
		}
		for _, job := range hostJobs {
			jobs = append(jobs, job)
		}
	}

	sorted := make(sortJobs, 0, len(jobs))
	for _, job := range jobs {
		if !all && job.Status != host.StatusStarting && job.Status != host.StatusRunning {
			continue
		}
		sorted = append(sorted, job)
	}
	sort.Sort(sort.Reverse(sorted))
	return sorted, nil
}

func printJobs(jobs sortJobs, out io.Writer) {
	w := tabwriter.NewWriter(out, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w,
		"NAME",
		"ID",
		"STATE",
		"CREATED",
		"CONTROLLER APP",
		"CONTROLLER TYPE",
		"ERROR",
	)

	for _, job := range jobs {
		var created string
		if !job.CreatedAt.IsZero() {
			created = units.HumanDuration(time.Now().UTC().Sub(job.CreatedAt)) + " ago"
		}
		var jobError string
		if job.Error != nil {
			jobError = *job.Error
		}
		name := host.JobDisplayName(job.Job)
		app, typ, id := "", "", ""
		if job.Job != nil {
			id = job.Job.ID
			if job.Job.Metadata != nil {
				app = job.Job.Metadata["flynn-controller.app_name"]
				typ = job.Job.Metadata["flynn-controller.type"]
			}
		}

		listRec(w,
			name,
			id,
			job.Status,
			created,
			app,
			typ,
			jobError,
		)
	}
}

func discoverdListHint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "192.0.2.200:1111") && strings.Contains(msg, "connection refused") {
		return msg + " (discoverd is down on the vagrant-dev listen IP; script/vagrant.sh build used to stop-all the cluster — reload if host.json exists, otherwise bootstrap again)"
	}
	return msg
}
