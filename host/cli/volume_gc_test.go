package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/host/volume"
)

func TestVolumeGCKeepFromJobs(t *testing.T) {
	jobs := map[string]host.ActiveJob{
		"run": {
			Status: host.StatusRunning,
			Job: &host.Job{
				ID: "job-run",
				Config: host.ContainerConfig{
					Volumes: []host.VolumeBinding{{VolumeID: "data-1"}},
				},
				Mountspecs: []*host.Mountspec{{ID: "layer-1"}},
			},
		},
		"done": {
			Status: host.StatusDone,
			Job: &host.Job{
				ID: "job-done",
				Config: host.ContainerConfig{
					Volumes: []host.VolumeBinding{{VolumeID: "stale-data"}},
				},
			},
		},
	}
	keep := volumeGCKeepFromJobs(jobs)
	for _, id := range []string{"job-run", "data-1", "layer-1"} {
		if _, ok := keep[id]; !ok {
			t.Fatalf("running job must keep %s", id)
		}
	}
	if _, ok := keep["job-done"]; ok {
		t.Fatal("finished jobs must not pin volumes")
	}
	if _, ok := keep["stale-data"]; ok {
		t.Fatal("volumes on finished jobs must be eligible for gc")
	}
}

func TestAddControllerVolumeKeep(t *testing.T) {
	keep := map[string]struct{}{}
	destroyed := ct.VolumeStateDestroyed
	decom := time.Now()
	addControllerVolumeKeep(keep, []*ct.Volume{
		{ID: "live"},
		{ID: "gone", State: destroyed},
		{ID: "decom", DecommissionedAt: &decom},
		nil,
		{ID: ""},
	})
	if _, ok := keep["live"]; !ok {
		t.Fatal("controller-tracked volumes must be kept (sirenia)")
	}
	if _, ok := keep["gone"]; ok {
		t.Fatal("destroyed controller volumes must not be kept")
	}
	if _, ok := keep["decom"]; ok {
		t.Fatal("decommissioned controller volumes must not be kept")
	}
}

func TestShouldGCVolume(t *testing.T) {
	keep := map[string]struct{}{"keep-me": {}}
	if shouldGCVolume(&volume.Info{ID: "keep-me"}, keep) {
		t.Fatal("kept volume must not be gc'd")
	}
	if shouldGCVolume(&volume.Info{ID: "sys", Meta: map[string]string{"flynn.system-image": "true"}}, keep) {
		t.Fatal("system image volumes must not be gc'd")
	}
	if !shouldGCVolume(&volume.Info{ID: "orphan"}, keep) {
		t.Fatal("unused non-system volume must be gc'd")
	}
}

func TestVolumeGCSkipsDestroyWhenControllerListFails(t *testing.T) {
	src, err := os.ReadFile("volume.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	warn := strings.Index(body, `warning: could not list controller volumes for gc`)
	skip := strings.Index(body, `skipping volume gc`)
	destroy := strings.Index(body, "DestroyVolume")
	if warn < 0 || skip < 0 {
		t.Fatal("volume gc must skip destroys when the controller volume list fails")
	}
	if destroy < 0 || skip > destroy {
		t.Fatal("must return before DestroyVolume when controller volumes cannot be listed")
	}
	if !strings.Contains(body, "controllerKeyFromActiveJobs(jobs)") {
		t.Fatal("volume gc must seed AUTH_KEY from controller jobs before GET /volumes")
	}
}
