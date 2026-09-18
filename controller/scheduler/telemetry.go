package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/randy-girard/flynn/pkg/version"
)

var telemetryURL = "https://dl.flynn.cloud.randygirard.com/measure/scheduler"

func init() {
	if u := os.Getenv("TELEMETRY_URL"); u != "" {
		telemetryURL = u
	}
}

func (s *Scheduler) tickSendTelemetry() {
	go func() {
		for range time.Tick(12 * time.Hour) {
			s.triggerSendTelemetry()
		}
	}()
}

func (s *Scheduler) triggerSendTelemetry() {
	select {
	case s.sendTelemetry <- struct{}{}:
	default:
	}
}

func (s *Scheduler) SendTelemetry() {
	if !s.IsLeader() || os.Getenv("TELEMETRY_DISABLED") == "true" {
		return
	}

	params := make(url.Values)
	params.Add("id_version", version.String())
	params.Add("id_bootstrap", os.Getenv("TELEMETRY_BOOTSTRAP_ID"))
	params.Add("id_cluster", os.Getenv("TELEMETRY_CLUSTER_ID"))

	params.Add("ct_hosts", strconv.Itoa(len(s.hosts)))

	var jobs int
	for _, j := range s.jobs {
		if j.State == JobStateRunning {
			jobs++
		}
	}
	params.Add("ct_running_jobs", strconv.Itoa(jobs))

	var formations int
	apps := make(map[string]struct{})
	dbs := make(map[string]map[string]struct{})
	skipFlynn := map[string]bool{
		"FLYNN_APP_ID":       true,
		"FLYNN_APP_NAME":     true,
		"FLYNN_RELEASE_ID":   true,
		"FLYNN_PROCESS_TYPE": true,
		"FLYNN_JOB_ID":       true,
	}
	for _, f := range s.formations {
		if f.App.Meta["flynn-system-app"] == "true" || f.GetProcesses().IsEmpty() {
			continue
		}
		formations++
		apps[f.App.ID] = struct{}{}

		if f.Release.Env["FLYNN_POSTGRES"] != "" {
			if db := f.Release.Env["PGDATABASE"]; db != "" {
				if dbs["postgres"] == nil {
					dbs["postgres"] = map[string]struct{}{}
				}
				dbs["postgres"][db] = struct{}{}
			}
		}
		for k, v := range f.Release.Env {
			if !strings.HasPrefix(k, "FLYNN_") || v == "" || skipFlynn[k] || k == "FLYNN_POSTGRES" {
				continue
			}
			p := strings.ToLower(strings.TrimPrefix(k, "FLYNN_"))
			if p == "" {
				continue
			}
			if dbs[p] == nil {
				dbs[p] = map[string]struct{}{}
			}
			dbs[p][v] = struct{}{}
		}
	}
	params.Add("ct_running_apps", strconv.Itoa(len(apps)))
	params.Add("ct_running_formations", strconv.Itoa(formations))
	for p, set := range dbs {
		params.Add(fmt.Sprintf("ct_%s_dbs", p), strconv.Itoa(len(set)))
	}

	go func() {
		req, _ := http.NewRequest("GET", telemetryURL, nil)
		req.Header.Set("User-Agent", "flynn-scheduler/"+version.String())
		req.URL.RawQuery = params.Encode()

		for i := 0; i < 5; i++ {
			res, err := http.DefaultClient.Do(req)
			if res != nil {
				res.Body.Close()
			}
			if err == nil && res.StatusCode == 200 {
				return
			}
			time.Sleep(10 * time.Second)
		}
	}()
}
