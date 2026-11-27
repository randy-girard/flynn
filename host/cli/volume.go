package cli

import (
	"errors"
	"fmt"
	"io"
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
	Register("volume", runVolumeList, `
usage: flynn-host volume

Display a list of all volumes of known Flynn hosts. volume:list is the same
command. volume:help is the same as volume --help.

Examples:

    $ flynn-host volume
    $ flynn-host volume:list
`)
	Register("volume:list", runVolumeList, `
usage: flynn-host volume:list

Display a list of all volumes of known Flynn hosts. The same as flynn-host
volume. volume:list:help is the same as volume:list --help.

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
usage: flynn-host volume:gc [--dry-run] [--deleted-apps] [--grace=<duration>]

Garbage collect currently unused volumes: volumes no running job binds and
the controller does not track (or tracks as destroyed or decommissioned).
System images and the discoverd and flannel state volumes stay.

With --deleted-apps, also collect the data volumes of apps that were deleted
at least --grace ago and never decommissioned (apps deleted before app
deletion started decommissioning volumes). A volume a running job still
binds stays, and so does every volume of an app deleted with
flynn-controller.keep-volumes=true. Run it with --dry-run first.

Options:
    --dry-run            List what would be deleted, with sizes, and delete nothing
    --deleted-apps       Also collect volumes of deleted apps
    --grace=<duration>   With --deleted-apps, only apps deleted at least this long ago [default: 24h]

Examples:

    $ flynn-host volume:gc
    $ flynn-host volume:gc --deleted-apps --dry-run
    $ flynn-host volume:gc --deleted-apps --grace=1h
`)
}

// volumeGCOptions are the volume:gc flags. The zero value is the plain gc
// that update and disk:reclaim run.
type volumeGCOptions struct {
	DryRun      bool
	DeletedApps bool
	Grace       time.Duration
}

func runVolumeGarbageCollection(args *docopt.Args, client *cluster.Client) error {
	opts := volumeGCOptions{
		DryRun:      args.Bool["--dry-run"],
		DeletedApps: args.Bool["--deleted-apps"],
		Grace:       24 * time.Hour,
	}
	if g := strings.TrimSpace(args.String["--grace"]); g != "" {
		d, err := time.ParseDuration(g)
		if err != nil || d < 0 {
			return fmt.Errorf("invalid --grace %q: want a duration such as 24h", g)
		}
		opts.Grace = d
	}
	hosts, err := client.Hosts()
	if err != nil {
		return fmt.Errorf("could not list hosts: %s", err)
	}
	if len(hosts) == 0 {
		return errors.New("no hosts found")
	}
	return garbageCollectVolumes(hosts, nil, opts)
}

func volumeGCKeepFromJobs(jobs map[string]host.ActiveJob) map[string]struct{} {
	keep := make(map[string]struct{})
	for _, j := range jobs {
		if j.Job == nil {
			continue
		}
		live := j.Status == host.StatusRunning || j.Status == host.StatusStarting
		if live {
			keep[j.Job.ID] = struct{}{}
			for _, vb := range j.Job.Config.Volumes {
				if vb.VolumeID != "" {
					keep[vb.VolumeID] = struct{}{}
				}
			}
		}
		// Squashfs layers are shared by every job of a release. After a host
		// restart, jobs are often not Running yet; GC must still pin their
		// image layers so a failed zfs destroy cannot unmount them.
		for _, m := range j.Job.Mountspecs {
			if m != nil && m.ID != "" {
				keep[m.ID] = struct{}{}
			}
		}
		// discoverd's raft disk and flannel state are host-local. Image-pull
		// GC runs while those jobs are down, and the controller volume list
		// does not cover them. The newest job can be a replacement that never
		// bound the disk, so every remaining host-infra binding stays. User
		// apps are unchanged: a finished job's volume is still collected.
		if hostInfraPersistentVolume(j.Job.Metadata[host.MetaControllerAppName]) {
			for _, vb := range j.Job.Config.Volumes {
				if vb.VolumeID != "" {
					keep[vb.VolumeID] = struct{}{}
				}
			}
		}
	}
	return keep
}

func hostInfraPersistentVolume(app string) bool {
	return app == "discoverd" || app == "flannel"
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
	return garbageCollectVolumes(hosts, log, volumeGCOptions{})
}

// orphanedVolumeLister is the controller call behind --deleted-apps.
type orphanedVolumeLister interface {
	OrphanedVolumeList(grace time.Duration) ([]*ct.Volume, error)
}

func garbageCollectVolumes(hosts []*cluster.Host, log log15.Logger, opts volumeGCOptions) error {
	keep := make(map[string]struct{})
	for _, h := range hosts {
		jobs, err := h.ListJobs()
		if err != nil {
			fmt.Printf("error listing jobs on host %s: %s\n", h.ID(), err)
			return fmt.Errorf("skipping volume gc: error listing jobs on host %s: %w", h.ID(), err)
		}
		for id := range volumeGCKeepFromJobs(jobs) {
			keep[id] = struct{}{}
		}
		// SEC-028: discoverd no longer publishes AUTH_KEY. Seed it from the
		// controller job we already listed so GET /volumes does not 401.
		if k := controllerKeyFromActiveJobs(jobs); k != "" {
			seedControllerKey(k)
		}
	}
	jobKeep := make(map[string]struct{}, len(keep))
	for id := range keep {
		jobKeep[id] = struct{}{}
	}

	volumes, err := clusterVolumes(hosts)
	if err != nil {
		return err
	}

	// Keep volumes the controller scheduler still tracks. Without this,
	// garbage collection can delete datasets that sirenia rolling deploys
	// still reference, causing updates to hang until timeout.
	// If the controller volume list cannot be loaded (including a 401 after
	// SEC-028 stopped publishing AUTH_KEY in discoverd), skip destroys.
	ctrl, err := controllerClient()
	if err != nil {
		fmt.Printf("warning: could not connect to controller for volume gc: %s\n", err)
		return fmt.Errorf("skipping volume gc: %w", err)
	}
	ctrlVols, err := ctrl.VolumeList()
	if err != nil {
		fmt.Printf("warning: could not list controller volumes for gc: %s\n", err)
		return fmt.Errorf("skipping volume gc: %w", err)
	}
	addControllerVolumeKeep(keep, ctrlVols)

	if opts.DeletedApps {
		lister, ok := ctrl.(orphanedVolumeLister)
		if !ok {
			return errors.New("skipping volume gc: controller client cannot list deleted-app volumes")
		}
		orphans, err := lister.OrphanedVolumeList(opts.Grace)
		if err != nil {
			fmt.Printf("warning: could not list deleted-app volumes for gc: %s\n", err)
			return fmt.Errorf("skipping volume gc: %w", err)
		}
		released := dropDeletedAppVolumes(keep, jobKeep, orphans)
		fmt.Printf("%d volumes of apps deleted more than %s ago (%d still bound by a running job)\n", len(released), opts.Grace, len(orphans)-len(released))
	}

	targets := make([]gcTarget, 0, len(volumes))
	for _, v := range volumes {
		targets = append(targets, gcTarget{HostID: v.Host.ID(), Volume: v.Volume, host: v.Host})
	}
	res := collectVolumes(targets, keep, opts.DryRun, func(t gcTarget) error {
		return t.host.DestroyVolume(t.Volume.ID)
	}, os.Stdout, log)
	verb := "Deleted"
	if opts.DryRun {
		verb = "Would delete"
	}
	fmt.Printf("%s %d volumes (%s quota)\n", verb, res.count, units.BytesSize(float64(res.bytes)))
	if res.failed {
		return errors.New("could not garbage collect all volumes")
	}
	return nil
}

// dropDeletedAppVolumes stops keeping the controller volumes of deleted apps
// (orphans) that no running job binds (jobKeep). It returns the ones it
// released.
func dropDeletedAppVolumes(keep, jobKeep map[string]struct{}, orphans []*ct.Volume) []*ct.Volume {
	var released []*ct.Volume
	for _, vol := range orphans {
		if vol == nil || vol.ID == "" {
			continue
		}
		if _, ok := jobKeep[vol.ID]; ok {
			continue
		}
		delete(keep, vol.ID)
		released = append(released, vol)
	}
	return released
}

type gcTarget struct {
	HostID string
	Volume *volume.Info
	host   *cluster.Host
}

type gcResult struct {
	count  int
	bytes  int64
	failed bool
}

// collectVolumes destroys (or with dryRun lists) every target shouldGCVolume
// allows, printing one line per volume to out and logging each delete.
func collectVolumes(targets []gcTarget, keep map[string]struct{}, dryRun bool, destroy func(gcTarget) error, out io.Writer, log log15.Logger) gcResult {
	var res gcResult
	for _, t := range targets {
		if !shouldGCVolume(t.Volume, keep) {
			continue
		}
		app := t.Volume.Meta["flynn-controller.app"]
		size := units.BytesSize(float64(t.Volume.Size))
		if dryRun {
			fmt.Fprintf(out, "Would delete %s volume %s on %s (app %s, %s quota)\n", t.Volume.Type, t.Volume.ID, t.HostID, app, size)
			res.count++
			res.bytes += t.Volume.Size
			continue
		}
		if err := destroy(t); err != nil {
			res.failed = true
			fmt.Fprintf(out, "could not delete %s volume %s: %s\n", t.Volume.Type, t.Volume.ID, err)
			continue
		}
		res.count++
		res.bytes += t.Volume.Size
		fmt.Fprintln(out, "Deleted", t.Volume.Type, "volume", t.Volume.ID, "on", t.HostID)
		if log != nil {
			log.Info("deleted unused volume", "type", t.Volume.Type, "id", t.Volume.ID, "host", t.HostID, "app", app)
		}
	}
	return res
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
