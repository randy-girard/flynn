package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"golang.org/x/net/context"
)

func TestRedactJobsAndRelease(t *testing.T) {
	jobs := []*ct.Job{
		{UUID: "1", Type: "web"},
		{UUID: "2", Type: "slugbuilder"},
		{UUID: "3", Type: "dockerbuilder"},
		{UUID: "4", Type: "worker"},
	}
	got := redactJobs(jobs)
	if len(got) != 2 || got[0].Type != "web" || got[1].Type != "worker" {
		t.Fatalf("redactJobs = %+v", got)
	}

	rel := &ct.Release{Processes: map[string]ct.ProcessType{
		"web":         {Args: []string{"web"}},
		"slugbuilder": {Args: []string{"/builder/build.sh"}},
		"worker":      {Args: []string{"worker"}},
	}}
	out := redactRelease(rel)
	if _, ok := out.Processes["slugbuilder"]; ok {
		t.Fatal("slugbuilder must be stripped from the release copy")
	}
	if _, ok := rel.Processes["slugbuilder"]; !ok {
		t.Fatal("redactRelease must not mutate the original")
	}
	if len(out.Processes) != 2 {
		t.Fatalf("processes = %v, want web and worker", out.Processes)
	}
}

func TestPreserveInternalProcessCounts(t *testing.T) {
	dst := map[string]int{"web": 2}
	stripInternalProcessCounts(dst)
	dst = preserveInternalProcessCounts(dst, map[string]int{"web": 9, "slugbuilder": 0})
	if dst["web"] != 2 {
		t.Fatalf("web = %d, want 2", dst["web"])
	}
	if dst["slugbuilder"] != 0 {
		t.Fatalf("slugbuilder = %d, want preserved 0", dst["slugbuilder"])
	}
}

func TestLogLineInternalProcess(t *testing.T) {
	if !logLineInternalProcess([]byte(`{"process_type":"slugbuilder","msg":"build"}`)) {
		t.Fatal("slugbuilder log line must be hidden")
	}
	if logLineInternalProcess([]byte(`{"process_type":"web","msg":"hello"}`)) {
		t.Fatal("web log line must stay visible")
	}
}

func TestFilterInternalProcessLogs(t *testing.T) {
	in := io.NopCloser(strings.NewReader(
		`{"process_type":"web","msg":"hello"}` + "\n" +
			`{"process_type":"slugbuilder","msg":"build"}` + "\n" +
			`{"process_type":"worker","msg":"ok"}` + "\n",
	))
	out, err := io.ReadAll(filterInternalProcessLogs(in))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `"process_type":"web"`) || !strings.Contains(got, `"process_type":"worker"`) {
		t.Fatalf("user logs missing: %s", got)
	}
	if strings.Contains(got, "slugbuilder") {
		t.Fatalf("slugbuilder leaked: %s", got)
	}
}

func TestRedactEventsDropsInternalJobs(t *testing.T) {
	jobWeb, _ := json.Marshal(ct.Job{UUID: "1", Type: "web"})
	jobSB, _ := json.Marshal(ct.Job{UUID: "2", Type: "slugbuilder"})
	scale, _ := json.Marshal(ct.ScaleRequest{
		OldProcesses: map[string]int{"web": 1, "slugbuilder": 0},
		NewProcesses: &map[string]int{"web": 2, "slugbuilder": 0},
	})
	got := redactEvents([]*ct.Event{
		{ObjectType: ct.EventTypeJob, Data: jobWeb},
		{ObjectType: ct.EventTypeJob, Data: jobSB},
		{ObjectType: ct.EventTypeScaleRequest, Data: scale},
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if dropInternalEvent(got[0]) {
		t.Fatal("web job event must stay")
	}
	var sr ct.ScaleRequest
	if err := json.Unmarshal(got[1].Data, &sr); err != nil {
		t.Fatal(err)
	}
	if _, ok := sr.OldProcesses["slugbuilder"]; ok {
		t.Fatal("slugbuilder must be stripped from scale events")
	}
	if sr.NewProcesses == nil || (*sr.NewProcesses)["web"] != 2 {
		t.Fatalf("web scale missing: %+v", sr.NewProcesses)
	}
}

func TestHideInternalRespectsClusterKey(t *testing.T) {
	app := &ct.App{Meta: map[string]string{}}
	jwt := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{Scopes: []string{"cluster:admin"}})
	if !hideInternal(jwt, app) {
		t.Fatal("dashboard JWT must hide internals on user apps")
	}
	key := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{ClusterKey: true})
	if hideInternal(key, app) {
		t.Fatal("cluster key must still see internals")
	}
	sys := &ct.App{Meta: map[string]string{"flynn-system-app": "true"}}
	if hideInternal(jwt, sys) {
		t.Fatal("system apps stay visible to dashboard JWTs")
	}
}

func TestPreserveInternalProcessTypes(t *testing.T) {
	prev := &ct.Release{Processes: map[string]ct.ProcessType{
		"web":         {Args: []string{"web"}},
		"slugbuilder": {Args: []string{"/builder/build.sh"}, RuntimeProfile: "large"},
	}}
	got := preserveInternalProcessTypes(map[string]ct.ProcessType{
		"web":         {Args: []string{"web"}},
		"slugbuilder": {Args: []string{"hack"}, RuntimeProfile: "small"},
		"worker":      {Args: []string{"worker"}},
	}, prev)
	if _, ok := got["web"]; !ok {
		t.Fatal("web must stay")
	}
	if got["slugbuilder"].RuntimeProfile != "large" {
		t.Fatalf("slugbuilder limits must be taken from the previous release, got %+v", got["slugbuilder"])
	}
	if _, ok := got["worker"]; !ok {
		t.Fatal("worker must stay")
	}

	stripped := preserveInternalProcessTypes(map[string]ct.ProcessType{"slugbuilder": {RuntimeProfile: "small"}}, nil)
	if _, ok := stripped["slugbuilder"]; ok {
		t.Fatal("first release must not invent slugbuilder")
	}
}

func TestHideInternalLimits(t *testing.T) {
	app := &ct.App{ID: "app-1", Name: "demo", Meta: map[string]string{}}
	jwt := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{Scopes: []string{"cluster:admin"}})
	if hideInternalLimits(jwt, app) {
		t.Fatal("cluster admins must see builder limits on the release")
	}
	if !hideInternal(jwt, app) {
		t.Fatal("cluster admins still must not see builder jobs")
	}
	write := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{
		AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}},
	})
	if !hideInternalLimits(write, app) {
		t.Fatal("app:write must not see builder limits")
	}
	admin := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{
		AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}},
	})
	if hideInternalLimits(admin, app) {
		t.Fatal("app:admin must see builder limits")
	}
}
