package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/docker/go-units"
	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	register("volume", runVolumeList, `
usage: flynn volume

List cluster volumes.
`)
	register("volume:show", runVolumeShow, `
usage: flynn volume:show [--json] <id>

Show information about a volume.

Options:
	--json  print the volume as JSON
`)
	register("volume:decommission", runVolumeDecommission, `
usage: flynn volume:decommission <id>

Decommission a volume. A decommissioned volume will continue to exist but will no longer
be attached to new jobs by the scheduler.
`)
}

func runVolumeList(args *docopt.Args, client controller.Client) error {
	volumes, err := client.AppVolumeList(mustApp())
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	listRec(w, "ID", "HOST", "STATE", "SIZE", "ATTACHED JOB", "CREATED", "DECOMMISSIONED")
	for _, v := range volumes {
		var jobID string
		if v.JobID != nil {
			jobID = cluster.GenerateJobID(v.HostID, *v.JobID)
		}
		var created string
		if v.CreatedAt != nil {
			created = units.HumanDuration(time.Now().UTC().Sub(*v.CreatedAt)) + " ago"
		}
		listRec(w, v.ID, v.HostID, v.State, volumeSize(v.Size), jobID, created, v.DecommissionedAt != nil)
	}

	return nil
}

// volumeSize is a volume's capacity for display. 0 means the controller has
// no size for it: recorded before it stored sizes, or a legacy uncapped
// dataset.
func volumeSize(size int64) string {
	if size <= 0 {
		return "-"
	}
	return units.BytesSize(float64(size))
}

func runVolumeShow(args *docopt.Args, client controller.Client) error {
	vol, err := client.GetVolume(mustApp(), args.String["<id>"])
	if err != nil {
		return err
	}
	if args.Bool["--json"] {
		return json.NewEncoder(os.Stdout).Encode(vol)
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "ID:", vol.ID)
	listRec(w, "State:", vol.State)
	listRec(w, "HostID:", vol.HostID)
	listRec(w, "Size:", volumeSize(vol.Size))
	listRec(w, "AppID:", vol.AppID)
	listRec(w, "ReleaseID:", vol.ReleaseID)
	var jobID string
	if vol.JobID != nil {
		jobID = cluster.GenerateJobID(vol.HostID, *vol.JobID)
	}
	listRec(w, "JobID:", jobID)
	listRec(w, "JobType:", vol.JobType)
	listRec(w, "CreatedAt:", vol.CreatedAt)
	listRec(w, "UpdatedAt:", vol.UpdatedAt)
	listRec(w, "DecommissionedAt:", vol.DecommissionedAt)
	return nil
}

func runVolumeDecommission(args *docopt.Args, client controller.Client) error {
	vol := &ct.Volume{ID: args.String["<id>"]}
	if err := client.DecommissionVolume(mustApp(), vol); err != nil {
		return err
	}
	fmt.Printf("volume %s successfully decommissioned at %s\n", vol.ID, vol.DecommissionedAt)
	return nil
}
