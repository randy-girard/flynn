package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/types"
)

func TestJobMatchesProcessName(t *testing.T) {
	job := host.ActiveJob{Job: &host.Job{Metadata: map[string]string{
		"flynn-controller.app_name": "dashboard-plugin",
		"flynn-controller.type":     "web",
		host.MetaControllerName:     "web.5154",
	}}}
	if !jobMatchesProcessName(job, "web.5154") {
		t.Fatal("process name")
	}
	if !jobMatchesProcessName(job, "dashboard-plugin.web.5154") {
		t.Fatal("app.process name")
	}
	if jobMatchesProcessName(job, "web.1") {
		t.Fatal("other process")
	}
	if !jobMatchesLookup(job, "web.5154") {
		t.Fatal("lookup process name")
	}
	if !jobMatchesLookup(job, "dashboard-plugin") {
		t.Fatal("lookup still matches app name")
	}
}

func TestJobMatchesFallbackProcessName(t *testing.T) {
	job := host.ActiveJob{Job: &host.Job{
		ID: "devnode1-97100ccb-1b45-4af1-9c71-78e1d3571042",
		Metadata: map[string]string{
			"flynn-controller.app_name": "controller",
			"flynn-controller.type":     "scheduler",
		},
	}}
	if !jobMatchesProcessName(job, "scheduler.8676") {
		t.Fatal("bootstrap jobs must match the display name")
	}
	if !jobMatchesProcessName(job, "controller.scheduler.8676") {
		t.Fatal("app.display name")
	}
}

func TestJobMatchesApp(t *testing.T) {
	job := host.ActiveJob{Job: &host.Job{Metadata: map[string]string{
		"flynn-controller.app_name": "dashboard",
		"flynn-controller.app":      "ae678393-3bd4-42c4-a528-fe7c3faa5cdb",
		"flynn-controller.type":     "web",
	}}}
	if !jobMatchesApp(job, "dashboard") {
		t.Fatal("app name")
	}
	if !jobMatchesApp(job, "ae678393-3bd4-42c4-a528-fe7c3faa5cdb") {
		t.Fatal("app id")
	}
	if jobMatchesApp(job, "postgres") {
		t.Fatal("other app")
	}
	if jobMatchesApp(host.ActiveJob{}, "dashboard") {
		t.Fatal("nil job")
	}
}

func TestJobsMatchingApp(t *testing.T) {
	jobs := []host.ActiveJob{
		{Job: &host.Job{ID: "h-1", Metadata: map[string]string{"flynn-controller.app_name": "dashboard"}}},
		{Job: &host.Job{ID: "h-2", Metadata: map[string]string{"flynn-controller.app_name": "postgres"}}},
		{Job: &host.Job{ID: "h-3", Metadata: map[string]string{"flynn-controller.app_name": "dashboard"}}},
	}
	got := jobsMatchingApp(jobs, "dashboard")
	if len(got) != 2 || got[0].Job.ID != "h-1" || got[1].Job.ID != "h-3" {
		t.Fatalf("%+v", got)
	}

	named := []host.ActiveJob{
		{Job: &host.Job{ID: "h-4", Metadata: map[string]string{host.MetaControllerName: "web.5154", "flynn-controller.app_name": "dashboard-plugin"}}},
		{Job: &host.Job{ID: "h-5", Metadata: map[string]string{host.MetaControllerName: "web.1", "flynn-controller.app_name": "dashboard-plugin"}}},
	}
	got = jobsMatching(named, "web.5154")
	if len(got) != 1 || got[0].Job.ID != "h-4" {
		t.Fatalf("process name %+v", got)
	}
}

func TestLogLinePrefix(t *testing.T) {
	job := host.ActiveJob{Job: &host.Job{
		ID: "localhost-6e007458-ff0b-4aee-a42d-b318694e0b4c",
		Metadata: map[string]string{
			"flynn-controller.app_name": "dashboard",
			"flynn-controller.type":     "web",
		},
	}}
	got := logLinePrefix(job)
	if got != "dashboard.web.8163 | " {
		t.Fatalf("%q", got)
	}

	named := host.ActiveJob{Job: &host.Job{
		ID: "localhost-6e007458-ff0b-4aee-a42d-b318694e0b4c",
		Metadata: map[string]string{
			"flynn-controller.app_name": "dashboard",
			"flynn-controller.type":     "web",
			host.MetaControllerName:     "web.4821",
		},
	}}
	if got := logLinePrefix(named); got != "dashboard.web.4821 | " {
		t.Fatalf("named prefix %q", got)
	}
}

func TestPrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &prefixWriter{w: &buf, prefix: "app.web.abc | ", atBOL: true}
	if _, err := w.Write([]byte("hello\nworld")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("!\n")); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "app.web.abc | hello\napp.web.abc | world!\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if strings.Count(got, "app.web.abc | ") != 2 {
		t.Fatalf("prefix count: %q", got)
	}
}

func TestRunningLogJobs(t *testing.T) {
	jobs := sortJobs{
		{Status: host.StatusRunning, Job: &host.Job{ID: "a"}},
		{Status: host.StatusDone, Job: &host.Job{ID: "b"}},
		{Status: host.StatusStarting, Job: &host.Job{ID: "c"}},
	}
	got := runningLogJobs(jobs)
	if len(got) != 2 || got[0].Job.ID != "a" || got[1].Job.ID != "c" {
		t.Fatalf("%+v", got)
	}
}
