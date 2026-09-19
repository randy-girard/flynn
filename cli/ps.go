package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	register("ps", runPs, `
usage: flynn ps [-a] [-c] [-q] [-t <type>]

List flynn jobs.

Options:
  -a, --all           Show all jobs (default is running and pending)
  -c, --command       Show command
  -q, --quiet         Only display IDs
  -t, --type=<type>   Show jobs of type <type>

Example:

       $ flynn ps
       NAME     TYPE  STATE  CREATED             ID
       web.4821 web   up     About a minute ago  host0-52aedfbf-e613-40f2-941a-d832d10fc400
       web.91   web   up     25 seconds ago      host0-205595d8-206a-46a2-be30-2e98f53df272

       $ flynn ps --all --command
       NAME       TYPE  STATE  CREATED        ID                                           COMMAND
       runner.304 down  5 seconds ago         host0-129b821f-3195-4b3b-b04b-669196cfbb03  /runner/init /bin/bash

       $ flynn ps --all --quiet
       web.4821
       web.91

       $ flynn ps --all --type=run
       NAME       TYPE  STATE  CREATED        ID
       runner.304 run   down   5 seconds ago  host0-129b821f-3195-4b3b-b04b-669196cfbb03
`)
}

func runPs(args *docopt.Args, client controller.Client) error {
	jobs, err := client.JobList(mustApp())
	if err != nil {
		return err
	}
	sort.Sort(sortJobs(jobs))
	w := tabWriter()
	defer w.Flush()
	if !args.Bool["--quiet"] {
		headers := []interface{}{"NAME", "TYPE", "STATE", "CREATED", "ID"}
		if args.Bool["--command"] {
			headers = append(headers, "COMMAND")
		}
		listRec(w, headers...)
	}
	for _, j := range jobs {
		if !args.Bool["--all"] && j.State != ct.JobStateUp && j.State != ct.JobStatePending {
			continue
		}
		if j.Type == "" {
			j.Type = "run"
		}
		if args.String["<type>"] != "" && j.Type != args.String["<type>"] {
			continue
		}
		id := j.ID
		if id == "" {
			id = j.UUID
		}
		name := ct.JobDisplayName(j)
		if args.Bool["--quiet"] {
			fmt.Println(name)
			continue
		}
		var created string
		if j.CreatedAt != nil {
			created = units.HumanDuration(time.Now().UTC().Sub(*j.CreatedAt)) + " ago"
		}
		fields := []interface{}{name, j.Type, j.State, created, id}
		if args.Bool["--command"] {
			fields = append(fields, strings.Join(j.Args, " "))
		}
		listRec(w, fields...)
	}
	return nil
}

// sortJobs sorts Jobs in chronological order based on their CreatedAt time
type sortJobs []*ct.Job

func (s sortJobs) Len() int { return len(s) }
func (s sortJobs) Less(i, j int) bool {
	return s[i].CreatedAt == nil || s[j].CreatedAt != nil && (*s[j].CreatedAt).Sub(*s[i].CreatedAt) > 0
}
func (s sortJobs) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
