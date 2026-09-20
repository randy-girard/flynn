package main

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestTagsMatchHostIDsTag(t *testing.T) {
	formation := NewFormation(&ct.ExpandedFormation{
		Tags: map[string]map[string]string{
			"app": {ct.FormationHostIDsTag: "host1,host3", "disk": "ssd"},
		},
	})
	job := &Job{Type: "app", Formation: formation}

	if !job.TagsMatchHost(&Host{ID: "host1", Tags: map[string]string{"disk": "ssd"}}) {
		t.Fatal("host1 with matching disk tag must match")
	}
	if job.TagsMatchHost(&Host{ID: "host2", Tags: map[string]string{"disk": "ssd"}}) {
		t.Fatal("host2 is not in flynn-host-ids")
	}
	if job.TagsMatchHost(&Host{ID: "host1", Tags: map[string]string{"disk": "hdd"}}) {
		t.Fatal("host1 with wrong disk tag must not match")
	}
	if job.TagsMatchHost(nil) {
		t.Fatal("nil host must not match")
	}
}

func TestMatchingOmniHostCountHonorsHostIDs(t *testing.T) {
	s := &Scheduler{
		hosts: map[string]*Host{
			"host1": {ID: "host1"},
			"host2": {ID: "host2"},
			"host3": {ID: "host3", Shutdown: true},
		},
	}
	formation := NewFormation(&ct.ExpandedFormation{
		Release:   &ct.Release{Processes: map[string]ct.ProcessType{"app": {Omni: true}}},
		Processes: Processes{"app": 1},
		Tags: map[string]map[string]string{
			"app": {ct.FormationHostIDsTag: "host1,host3"},
		},
	})
	if got := s.matchingOmniHostCount(formation, "app"); got != 1 {
		t.Fatalf("matchingOmniHostCount = %d, want 1 (host3 is shutdown)", got)
	}
	formation.Processes["app"] = 3
	if !s.rectifyFormationOmni(formation) {
		t.Fatal("expected omni count to drop from all-hosts to tagged hosts")
	}
	if formation.Processes["app"] != 1 {
		t.Fatalf("Processes[app] = %d, want 1", formation.Processes["app"])
	}
}
