package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/types"
)

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
	if got != "dashboard.web.6e007458 | " {
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
