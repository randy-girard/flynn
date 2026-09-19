package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/docker/go-units"
	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("volume:list", runVolumeList, `
usage: flynn-host volume:list

Display a list of all volumes of known Flynn hosts.

Examples:

    $ flynn-host volume:list
`)
	Register("volume:create", runVolumeCreate, `
usage: flynn-host volume:create [--provider=<provider>] <host>

Create a data volume on a host.

Examples:

    $ flynn-host volume:create --provider default host0
`)
	Register("volume:delete", runVolumeDelete, `
usage: flynn-host volume:delete ID...

Delete volumes, destroying any data stored on them.

Examples:

    $ flynn-host volume:delete 102fad07-07a3-4841-bded-d9e8a3eedbd6
`)
	Register("volume:gc", runVolumeGarbageCollection, `
usage: flynn-host volume:gc

Garbage collect currently unused volumes.

Examples:

    $ flynn-host volume:gc
`)
}

func runVolumeGarbageCollection(args *docopt.Args, client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return fmt.Errorf("could not list hosts: %s", err)
	}
	if len(hosts) == 0 {
		return errors.New("no hosts found")
	}
	return garbageCollectUnusedVolumes(hosts, nil)
}

func volumeGCKeepFromJobs(jobs map[string]host.ActiveJob) map[string]struct{} {
	keep := make(map[string]struct{})
	for _, j := range jobs {
		if j.Status != host.StatusRunning && j.Status != host.StatusStarting {
			continue
		}
		if j.Job == nil {
			continue
		}
		keep[j.Job.ID] = struct{}{}
		for _, vb := range j.Job.Config.Volumes {
			if vb.VolumeID != "" {
				keep[vb.VolumeID] = struct{}{}
			}
		}
		for _, m := range j.Job.Mountspecs {
			if m != nil && m.ID != "" {
				keep[m.ID] = struct{}{}
			}
		}
	}
	return keep
}

func addControllerVolumeKeep(keep map[string]struct{}, vols []*ct.Volume) {
	for _, vol := range vols {
		if vol == nil || vol.ID == "" {
			continue
		}
		if vol.State == ct.VolumeStateDestroyed || vol.DecommissionedAt != nil {
			continue
		}
		keep[vol.ID] = struct{}{}
	}
}

func shouldGCVolume(v *volume.Info, keep map[string]struct{}) bool {
	if v == nil || v.ID == "" {
		return false
	}
	if _, ok := keep[v.ID]; ok {
		return false
	}
	if v.Meta["flynn.system-image"] == "true" {
		return false
	}
	return true
}

// garbageCollectUnusedVolumes deletes host volumes that are not attached to a
// running job, not tracked by the controller scheduler, and not system images.
func garbageCollectUnusedVolumes(hosts []*cluster.Host, log log15.Logger) error {
	keep := make(map[string]struct{})
	for _, h := range hosts {
		jobs, err := h.ListJobs()
		if err != nil {
			fmt.Printf("error listing jobs on host %s: %s\n", h.ID(), err)
			continue
		}
		for id := range volumeGCKeepFromJobs(jobs) {
			keep[id] = struct{}{}
		}
	}

	volumes, err := clusterVolumes(hosts)
	if err != nil {
		return err
	}

	// Keep volumes the controller scheduler still tracks. Without this,
	// garbage collection can delete datasets that sirenia rolling deploys
	// still reference, causing updates to hang until timeout.
	if ctrl, err := controllerClient(); err == nil {
		ctrlVols, err := ctrl.VolumeList()
		if err != nil {
			fmt.Printf("warning: could not list controller volumes for gc: %s\n", err)
		} else {
			addControllerVolumeKeep(keep, ctrlVols)
		}
	} else {
		fmt.Printf("warning: could not connect to controller for volume gc: %s\n", err)
	}

	success := true
	for _, v := range volumes {
		if !shouldGCVolume(v.Volume, keep) {
			continue
		}
		if err := v.Host.DestroyVolume(v.Volume.ID); err != nil {
			success = false
			fmt.Printf("could not delete %s volume %s: %s\n", v.Volume.Type, v.Volume.ID, err)
			continue
		}
		fmt.Println("Deleted", v.Volume.Type, "volume", v.Volume.ID)
		if log != nil {
			log.Info("deleted unused volume", "type", v.Volume.Type, "id", v.Volume.ID, "host", v.Host.ID())
		}
	}
	if !success {
		return errors.New("could not garbage collect all volumes")
	}
	return nil
}

func runVolumeDelete(args *docopt.Args, client *cluster.Client) error {
	success := true
	hosts, err := client.Hosts()
	if err != nil {
		return fmt.Errorf("could not list hosts: %s", err)
	}
	if len(hosts) == 0 {
		return errors.New("no hosts found")
	}

	volumes, err := clusterVolumes(hosts)
	if err != nil {
		return err
	}

outer:
	for _, id := range cliutil.List(args, "ID") {
		// find this volume in the list
		for _, v := range volumes {
			if v.Volume.ID == id {
				if err := v.Host.DestroyVolume(id); err != nil {
					success = false
					fmt.Printf("could not delete volume %s: %s\n", id, err)
					continue outer
				}
				// delete the volume
				fmt.Println(id, "deleted")
				continue outer
			}
		}
		success = false
		fmt.Printf("could not delete volume %s: volume not found\n", id)
	}
	if !success {
		return errors.New("could not delete all volumes")
	}
	return nil
}

func runVolumeCreate(args *docopt.Args, client *cluster.Client) error {
	hostId := args.String["<host>"]
	hostClient, err := client.Host(hostId)
	if err != nil {
		fmt.Println("could not connect to host", hostId)
	}
	provider := "default"
	if args.String["--provider"] != "" {
		provider = args.String["--provider"]
	}
	vol := &volume.Info{}
	if err := hostClient.CreateVolume(provider, vol); err != nil {
		fmt.Printf("could not create volume: %s\n", err)
		return err
	}
	fmt.Printf("created volume %s on %s\n", vol.ID, hostId)
	return nil
}

type hostVolume struct {
	Host   *cluster.Host
	Volume *volume.Info
}

type sortVolumes []hostVolume

func (s sortVolumes) Len() int           { return len(s) }
func (s sortVolumes) Less(i, j int) bool { return s[i].Volume.CreatedAt.Sub(s[j].Volume.CreatedAt) < 0 }
func (s sortVolumes) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func clusterVolumes(hosts []*cluster.Host) (sortVolumes, error) {
	var volumes sortVolumes
	for _, h := range hosts {
		hostVolumes, err := h.ListVolumes()
		if err != nil {
			return volumes, fmt.Errorf("could not get volumes for host %s: %s", h.ID(), err)
		}
		for _, v := range hostVolumes {
			volumes = append(volumes, hostVolume{Host: h, Volume: v})
		}
	}
	sort.Sort(volumes)
	return volumes, nil
}

func runVolumeList(args *docopt.Args, client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return fmt.Errorf("could not list hosts: %s", err)
	}
	if len(hosts) == 0 {
		return errors.New("no hosts found")
	}

	volumes, err := clusterVolumes(hosts)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w,
		"ID",
		"TYPE",
		"HOST",
		"CREATED",
		"META",
	)

	for _, volume := range volumes {
		meta := make([]string, 0, len(volume.Volume.Meta))
		for k, v := range volume.Volume.Meta {
			meta = append(meta, fmt.Sprintf("%s=%s", k, v))
		}
		listRec(w,
			volume.Volume.ID,
			volume.Volume.Type,
			volume.Host.ID(),
			units.HumanDuration(time.Now().UTC().Sub(volume.Volume.CreatedAt))+" ago",
			strings.Join(meta, " "),
		)
	}
	return nil
}
