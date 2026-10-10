package main

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestControllerJobSkipsNilVolumes(t *testing.T) {
	j := &Job{
		ID:    "uuid",
		JobID: "host-uuid",
		AppID: "app",
		Volumes: []*Volume{
			nil,
			{Volume: ct.Volume{ID: "vol-1"}},
		},
	}
	got := j.ControllerJob()
	if len(got.VolumeIDs) != 1 || got.VolumeIDs[0] != "vol-1" {
		t.Fatalf("VolumeIDs=%v, want [vol-1]", got.VolumeIDs)
	}
}

func persistSingletonJob(typ string) *Job {
	return &Job{
		Type: typ,
		Formation: &Formation{
			OriginalProcesses: Processes{typ: 1},
			ExpandedFormation: &ct.ExpandedFormation{
				Release: &ct.Release{
					Processes: map[string]ct.ProcessType{
						typ: {Volumes: []ct.VolumeReq{{Path: "/data"}}},
					},
				},
			},
		},
	}
}

func TestIsPersistentSingleton(t *testing.T) {
	if !persistSingletonJob("kafka").isPersistentSingleton() {
		t.Fatal("kafka one-broker volume must pack as a persistent singleton")
	}
	ha := persistSingletonJob("postgres")
	ha.Formation.OriginalProcesses["postgres"] = 3
	if ha.isPersistentSingleton() {
		t.Fatal("sirenia HA peers must keep spreading, not pack onto one host")
	}
	omni := persistSingletonJob("router")
	omni.Formation.Release.Processes["router"] = ct.ProcessType{
		Omni:    true,
		Volumes: []ct.VolumeReq{{Path: "/data"}},
	}
	if omni.isPersistentSingleton() {
		t.Fatal("omni jobs must not pack as persistent singletons")
	}
	ephemeral := persistSingletonJob("web")
	ephemeral.Formation.Release.Processes["web"] = ct.ProcessType{
		Volumes: []ct.VolumeReq{{Path: "/data", DeleteOnStop: true}},
	}
	if ephemeral.isPersistentSingleton() {
		t.Fatal("ephemeral volumes must not pack as persistent singletons")
	}
}

func TestGetHostJobCountsByAppType(t *testing.T) {
	jobs := Jobs{
		"a": {ID: "a", AppID: "pg", Type: "postgres", HostID: "h1", State: JobStateRunning},
		"b": {ID: "b", AppID: "pg", Type: "postgres", HostID: "h1", State: JobStateStopped},
		"c": {ID: "c", AppID: "other", Type: "postgres", HostID: "h2", State: JobStateRunning},
	}
	got := jobs.GetHostJobCountsByAppType("pg", "postgres")
	if got["h1"] != 1 || got["h2"] != 0 {
		t.Fatalf("got %v, want h1=1 (stopped and other app ignored)", got)
	}
}

func TestGetHostSpreadCountsPrefersHostsWithoutNewReleasePeer(t *testing.T) {
	app := &ct.App{ID: "pg"}
	oldF := &Formation{ExpandedFormation: &ct.ExpandedFormation{App: app, Release: &ct.Release{ID: "old"}}}
	newF := &Formation{ExpandedFormation: &ct.ExpandedFormation{App: app, Release: &ct.Release{ID: "new"}}}
	jobs := Jobs{
		"old-primary": {ID: "old-primary", AppID: "pg", Type: "postgres", HostID: "h3", State: JobStateRunning, Formation: oldF},
		"old-sync":    {ID: "old-sync", AppID: "pg", Type: "postgres", HostID: "h2", State: JobStateRunning, Formation: oldF},
		"new-1":       {ID: "new-1", AppID: "pg", Type: "postgres", HostID: "h1", State: JobStateRunning, Formation: newF},
	}
	got := jobs.GetHostSpreadCounts(newF.key(), "postgres")
	if got["h1"] <= got["h2"] || got["h1"] <= got["h3"] {
		t.Fatalf("got %v: h1 already has a new-release peer and must rank last", got)
	}
}
