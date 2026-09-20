package deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/worker/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
)

// syncTimeout bounds how long the sirenia deploy waits for a freshly started
// peer to catch up with its upstream (or for a new primary to become
// read-write). Large databases can take far longer than the historical
// 3-minute value to complete their initial base backup, so the default is
// generous and can be overridden via SIRENIA_DEPLOY_SYNC_TIMEOUT (a Go
// duration string, e.g. "30m").
var syncTimeout = func() time.Duration {
	const def = 15 * time.Minute
	if v := os.Getenv("SIRENIA_DEPLOY_SYNC_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}()

func (d *DeployJob) deploySirenia() (err error) {
	log := d.logger.New("fn", "deploySirenia")
	log.Info("starting sirenia deployment")

	if repairErr := updaterdeploy.RepairOrphanSireniaFormations(d.client, log); repairErr != nil {
		log.Warn("error repairing orphan sirenia formations", "err", repairErr)
	} else if f, ferr := d.client.GetFormation(d.AppID, d.NewReleaseID); ferr == nil {
		d.newFormation = f
		if d.newFormation.Processes == nil {
			d.newFormation.Processes = make(map[string]int)
		}
	}

	loggedErr := func(format string, v ...interface{}) error {
		e := fmt.Sprintf(format, v...)
		log.Error(e)
		return errors.New(e)
	}

	processType := d.oldRelease.Env["SIRENIA_PROCESS"]
	// if the process type isn't set try getting it from the new release
	if processType == "" {
		processType = d.newRelease.Env["SIRENIA_PROCESS"]
	}
	// if it's still not set we have a problem.
	if processType == "" {
		return errors.New("unable to determine sirenia process type")
	}
	proc, ok := d.newRelease.Processes[processType]
	if !ok {
		return errors.New("sirenia process type not present in new release")
	}

	singletonRelease := func() bool {
		if d.oldRelease != nil && d.oldRelease.Env["SINGLETON"] == "true" {
			return true
		}
		return d.newRelease.Env["SINGLETON"] == "true"
	}()

	// if sirenia process type is scaled to 0, skip and deploy non-sirenia processes
	if d.Processes[processType] == 0 {
		log.Info("sirenia process type scale = 0, skipping")
		return d.deployOneByOne()
	}
	// A single database peer runs in sirenia "singleton" mode. The HA rolling
	// strategy below requires primary, sync, and async peers; for a single
	// node we have to stop the old peer before starting the new one so the
	// data volume is released and can be adopted by the new release.
	if d.Processes[processType] == 1 {
		log.Info("sirenia process type scale = 1, using singleton deployment")
		return d.deploySireniaSingleton(processType, log)
	}
	// Bootstrap or legacy clusters can have SINGLETON=true while formation
	// counts still reflect HA; never use the HA rolling path in that case.
	if singletonRelease {
		log.Info("sirenia SINGLETON=true in release env, using singleton deployment")
		return d.deploySireniaSingleton(processType, log)
	}

	events := make(chan *discoverd.Event)
	stream, err := discoverd.NewService(proc.Service).Watch(events)
	if err != nil {
		log.Error("error creating service discovery watcher", "service", processType, "err", err)
		return err
	}
	defer stream.Close()

	var serviceMeta *discoverd.ServiceMeta
	timeout := time.After(d.timeout)
loop:
	for {
		select {
		case <-d.stop:
			return worker.ErrStopped
		case event, ok := <-events:
			if !ok {
				return loggedErr("service event stream closed unexpectedly: %s", stream.Err())
			}
			switch event.Kind {
			case discoverd.EventKindCurrent:
				break loop
			case discoverd.EventKindServiceMeta:
				serviceMeta = event.ServiceMeta
			case discoverd.EventKindUp:
				if event.Instance.Meta["FLYNN_RELEASE_ID"] == d.NewReleaseID {
					return loggedErr("sirenia cluster in unexpected state")
				}
			}
		case <-timeout:
			return loggedErr("timed out waiting for current service event")
		}
	}

	if serviceMeta == nil {
		return loggedErr("missing sirenia cluster state")
	}

	var state state.State
	log.Info("decoding sirenia cluster state")
	if err := json.Unmarshal(serviceMeta.Data, &state); err != nil {
		log.Error("error decoding sirenia cluster state", "err", err)
		return err
	}

	// abort if in singleton mode or not deploying from a clean state
	if state.Singleton {
		log.Info("sirenia discoverd state is singleton, using singleton deployment")
		return d.deploySireniaSingleton(processType, log)
	}
	readyState, err := d.waitForSireniaClusterReady(proc.Service, processType, &state, events, stream, log)
	if err != nil {
		return loggedErr("%s", err.Error())
	}
	state = *readyState

	// Peers from aborted rolling deploys can linger briefly in discoverd even
	// after orphan formation repair; try repair again before failing the deploy.
	if repairErr := updaterdeploy.RepairOrphanSireniaFormations(d.client, log); repairErr != nil {
		log.Warn("error repairing orphan sirenia formations before deploy", "err", repairErr)
	}
	instances, err := discoverd.InstancesOrEmpty(discoverd.NewService(proc.Service))
	if err != nil {
		return loggedErr("error listing sirenia service instances: %s", err)
	}
	for _, inst := range instances {
		if inst == nil || inst.Meta == nil {
			continue
		}
		if rid := inst.Meta["FLYNN_RELEASE_ID"]; rid != "" && rid != d.OldReleaseID {
			log.Warn("sirenia peer from non-active release still in discoverd after repair",
				"release.id", rid, "active.release.id", d.OldReleaseID, "addr", inst.Addr)
		}
	}

	svc := discoverd.NewService(proc.Service)
	// instanceRegistered reports whether an instance is still present in
	// discoverd. On a discoverd error it assumes the instance is still present
	// so callers fall back to the event stream / timeout rather than declaring
	// a live peer gone.
	instanceRegistered := func(id string) bool {
		insts, err := discoverd.InstancesOrEmpty(svc)
		if err != nil {
			return true
		}
		for _, i := range insts {
			if i != nil && i.ID == id {
				return true
			}
		}
		return false
	}

	stopInstance := func(inst *discoverd.Instance) error {
		log := log.New("job_id", inst.Meta["FLYNN_JOB_ID"], "addr", inst.Addr)

		sendDown := func() {
			d.deployEvents <- ct.DeploymentEvent{
				ReleaseID: d.OldReleaseID,
				JobState:  ct.JobStateDown,
				JobType:   processType,
			}
		}

		d.deployEvents <- ct.DeploymentEvent{
			ReleaseID: d.OldReleaseID,
			JobState:  ct.JobStateStopping,
			JobType:   processType,
		}

		// A peer left over from a previous aborted deploy may already be gone
		// from discoverd (its job is dead and its heartbeat lease expired).
		// There is nothing to stop, and trying to reach it would block on dial
		// retries for ~30s and then fail the whole deploy.
		if !instanceRegistered(inst.ID) {
			log.Info("peer already gone from discoverd, treating as stopped")
			sendDown()
			return nil
		}

		peer := sireniaclient.NewClient(inst.Addr)
		log.Info("stopping peer")
		if err := peer.Stop(); err != nil {
			switch {
			case sireniaclient.IsRecoverableStopError(err):
				log.Warn("stop request timed out, waiting for peer to leave discoverd", "err", err)
			case sireniaclient.IsPeerUnreachableError(err):
				log.Warn("peer unreachable, treating as already stopping and waiting for it to leave discoverd", "err", err)
			default:
				log.Error("error stopping peer", "err", err)
				return err
			}
		}
		log.Info("waiting for peer to stop")
		timeout := time.After(d.timeout)
		poll := time.NewTicker(2 * time.Second)
		defer poll.Stop()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return loggedErr("service event stream closed unexpectedly: %s", stream.Err())
				}
				if event.Kind == discoverd.EventKindDown && event.Instance != nil && event.Instance.ID == inst.ID {
					sendDown()
					return nil
				}
			case <-poll.C:
				// The instance's Down event may have been consumed while
				// waiting for another instance to come up, so also detect
				// departure by polling discoverd directly.
				if !instanceRegistered(inst.ID) {
					log.Info("peer no longer registered in discoverd, treating as stopped")
					sendDown()
					return nil
				}
			case <-timeout:
				return loggedErr("timed out waiting for peer to stop")
			}
		}
	}

	idKey := sireniaclient.ProcessIDKey(processType)

	// newPrimary is the first new instance started, newSync the second
	var newPrimary, newSync *discoverd.Instance
	startInstance := func() (*discoverd.Instance, error) {
		log.Info("starting new instance")
		d.deployEvents <- ct.DeploymentEvent{
			ReleaseID: d.NewReleaseID,
			JobState:  ct.JobStateStarting,
			JobType:   processType,
		}
		d.newFormation.Processes[processType]++
		// use PutFormation rather than ScaleAppRelease so we can use a
		// custom wait loop below
		if err := d.client.PutFormation(d.newFormation); err != nil {
			log.Error("error scaling new formation up by one", "err", err)
			return nil, err
		}
		log.Info("waiting for new instance to come up")
		var exclude []*discoverd.Instance
		if newPrimary != nil {
			exclude = append(exclude, newPrimary)
		}
		if newSync != nil {
			exclude = append(exclude, newSync)
		}
		var inst *discoverd.Instance
		timeout := time.After(d.timeout)
		poll := time.NewTicker(2 * time.Second)
		defer poll.Stop()
	loop:
		for {
			if found := lookupSireniaPeer(svc, d.NewReleaseID, processType, idKey, exclude...); found != nil {
				inst = found
				break loop
			}
			select {
			case event, ok := <-events:
				if !ok {
					return nil, loggedErr("service event stream closed unexpectedly: %s", stream.Err())
				}
				if event.Kind == discoverd.EventKindUp &&
					sireniaPeerMatchesRelease(event.Instance, d.NewReleaseID, processType) &&
					!sireniaPeerExcluded(event.Instance, idKey, exclude) {
					inst = event.Instance
					break loop
				}
			case <-poll.C:
			case <-timeout:
				return nil, loggedErr("timed out waiting for new instance to come up")
			}
		}
		if newPrimary == nil {
			newPrimary = inst
		} else if newSync == nil {
			newSync = inst
		}
		d.deployEvents <- ct.DeploymentEvent{
			ReleaseID: d.NewReleaseID,
			JobState:  ct.JobStateUp,
			JobType:   processType,
		}
		return inst, nil
	}
	waitForSync := func(upstream, downstream *discoverd.Instance) error {
		log.Info("waiting for replication sync", "upstream", upstream.Addr, "downstream", downstream.Addr)
		sc := sireniaclient.NewClient(upstream.Addr)
		if err := sc.WaitForReplSync(downstream, sireniaclient.ProcessIDKey(processType), syncTimeout); err != nil {
			log.Error("error waiting for replication sync", "err", err)
			return err
		}
		return nil
	}
	waitForReadWrite := func(inst *discoverd.Instance) error {
		log.Info("waiting for read-write", "inst", inst.Addr)
		sc := sireniaclient.NewClient(inst.Addr)
		if err := sc.WaitForReadWrite(syncTimeout); err != nil {
			log.Error("error waiting for read-write", "err", err)
			return err
		}
		return nil
	}
	waitForTopology := func(pred func(*sireniaclient.Status) bool, what string) error {
		log.Info("waiting for sirenia topology", "what", what)
		sc := sireniaclient.NewClient(state.Primary.Addr)
		if err := sc.WaitUntil(pred, syncTimeout); err != nil {
			if st, serr := sc.Status(); serr == nil && st != nil && st.Peer != nil && st.Peer.State != nil {
				log.Error("error waiting for sirenia topology", "what", what, "err", err, "asyncs", len(st.Peer.State.Async))
			} else {
				log.Error("error waiting for sirenia topology", "what", what, "err", err)
			}
			return err
		}
		return nil
	}
	waitForReplica := func(inst, upstream *discoverd.Instance, what string) error {
		log.Info("waiting for replica to follow upstream", "what", what, "inst", inst.Addr, "upstream", upstream.Addr)
		sc := sireniaclient.NewClient(inst.Addr)
		if err := sc.WaitUntil(sireniaclient.ReplicaOf(upstream, idKey), syncTimeout); err != nil {
			log.Error("error waiting for replica to follow upstream", "what", what, "err", err)
			return err
		}
		return nil
	}
	// waitForSyncPeer blocks until the cluster (as reported by upstream) names
	// successor as its synchronous peer. Stopping the primary before its
	// designated successor is the recorded sync leaves the cluster unable to
	// elect a new primary: only the sync can take over, so if a different peer
	// is still the sync when the primary dies, that peer must take over instead
	// of the new peer we started, and the new primary never becomes read-write.
	waitForSyncPeer := func(upstream, successor *discoverd.Instance) error {
		log.Info("waiting for successor to become the synchronous peer", "upstream", upstream.Addr, "successor", successor.Addr)
		sc := sireniaclient.NewClient(upstream.Addr)
		deadline := time.After(syncTimeout)
		poll := time.NewTicker(time.Second)
		defer poll.Stop()
		for {
			status, err := sc.Status()
			if err == nil && status.Peer != nil && status.Peer.State != nil &&
				sireniaclient.SamePeer(idKey, status.Peer.State.Sync, successor) {
				return nil
			}
			select {
			case <-deadline:
				return loggedErr("timed out waiting for successor %s to become the synchronous peer", successor.Addr)
			case <-poll.C:
			}
		}
	}

	// asyncUpstream is the instance we will query for replication status
	// of the new async, which will be the sync if there is only one
	// async, or the tail async otherwise.
	asyncUpstream := state.Sync
	if len(state.Async) > 1 {
		asyncUpstream = state.Async[len(state.Async)-1]
	}
	for i := 0; i < len(state.Async); i++ {
		log.Info("replacing an Async node")
		oldAsync := state.Async[i]
		newInst, err := startInstance()
		if err != nil {
			return err
		}
		// Discoverd registration happens before pg_basebackup. Keep the old
		// async up until the new job is the tail of the chain and has a
		// running database following it; stopping first killed the backup
		// source and the replacement never joined the replica set.
		if err := waitForTopology(sireniaclient.AsyncReplaceTopologyReady(oldAsync, newInst, idKey), "new peer is tail async of the peer it replaces"); err != nil {
			return err
		}
		if err := waitForReplica(newInst, oldAsync, "new async finished base backup"); err != nil {
			return err
		}
		if err := stopInstance(oldAsync); err != nil {
			return err
		}
		if err := waitForSync(asyncUpstream, newInst); err != nil {
			return err
		}
		// the new instance is now the tail async
		asyncUpstream = newInst
	}

	log.Info("replacing the Sync node")
	// Start the replacement *before* stopping the old sync so the replica
	// set keeps three live peers during the new job's base backup. Wait
	// until the cluster names that job as the tail async (still following
	// the current first async, not the primary) and it has a running
	// database following that actual upstream; only then stop the old sync
	// and wait for the promoted async to follow the primary.
	newInst, err := startInstance()
	if err != nil {
		return err
	}
	if err := waitForTopology(sireniaclient.SyncReplaceTopologyReady(state.Sync, newPrimary, newInst, idKey), "new peer is tail async"); err != nil {
		return err
	}
	if err := waitForReplica(newInst, newPrimary, "sync replacement finished base backup"); err != nil {
		return err
	}
	if err := stopInstance(state.Sync); err != nil {
		return err
	}
	if err := waitForSyncPeer(state.Primary, newPrimary); err != nil {
		return err
	}
	if err := waitForSync(state.Primary, newPrimary); err != nil {
		return err
	}

	// wait for the new Sync to catch the new Primary *before* killing the
	// old Primary to avoid backups failing
	if err := waitForSync(newPrimary, newSync); err != nil {
		return err
	}

	log.Info("replacing the Primary node")
	_, err = startInstance()
	if err != nil {
		return err
	}
	// Ensure newPrimary is the recorded synchronous peer before stopping the
	// old primary so the takeover elects it (and not some other peer) as the
	// new primary.
	if err := waitForSyncPeer(state.Primary, newPrimary); err != nil {
		return err
	}
	if err := stopInstance(state.Primary); err != nil {
		return err
	}
	if err := waitForReadWrite(newPrimary); err != nil {
		return err
	}

	log.Info("stopping old jobs")
	d.oldFormation.Processes[processType] = 0
	if err := d.scaleOldRelease(true); err != nil {
		log.Error("error scaling old formation", "err", err)
		return err
	}

	// do a one-by-one deploy for the other process types
	return d.deployOneByOne()
}

// deploySireniaSingleton replaces a single sirenia peer by submitting both
// formation changes (new release -> target, then old release -> 0) while the
// database is still up, then waiting for the new peer to register in
// discoverd.
//
// The serial scaleOneDownOneUp path cannot be used here because the singleton
// sirenia process is typically the controller's own datastore: once the
// scheduler stops the old job, the controller can no longer persist scale
// request or job state transitions, so waiting on the controller event stream
// for confirmation of the scale-down hangs until the deploy times out.
//
// The new formation is written first so the scale-up is committed and the
// scheduler has seen it before SIGTERM reaches postgres. Scaling the old
// release down first races a fast postgres shutdown: the second PutFormation
// (or the controller GetExpanded that turns it into a scheduler event) is
// aborted, the new job is never started, and the cluster is left without a
// datastore. The scheduler will not allocate a second empty volume while the
// old singleton still holds /data; it retries until that job is stopping and
// then adopts the existing dataset. Discoverd is independent of the
// controller database, so waiting for the new peer's Up event still works
// after postgres has restarted.
func (d *DeployJob) deploySireniaSingleton(processType string, log log15.Logger) error {
	proc, ok := d.newRelease.Processes[processType]
	if !ok {
		return fmt.Errorf("sirenia process type %q not present in new release", processType)
	}
	if proc.Service == "" {
		return fmt.Errorf("sirenia process type %q has no discoverd service", processType)
	}

	svc := discoverd.NewService(proc.Service)
	events := make(chan *discoverd.Event)
	stream, err := svc.Watch(events)
	if err != nil {
		log.Error("error creating service discovery watcher", "service", proc.Service, "err", err)
		return err
	}
	defer stream.Close()

	// drain initial events until current so any pre-existing Up events for
	// the old peer are absorbed before we start tracking the swap
	timeout := time.After(d.timeout)
waitCurrent:
	for {
		select {
		case <-d.stop:
			return worker.ErrStopped
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("service event stream closed unexpectedly: %s", stream.Err())
			}
			if event.Kind == discoverd.EventKindCurrent {
				break waitCurrent
			}
		case <-timeout:
			return errors.New("timed out waiting for sirenia discoverd current event")
		}
	}

	// write the new formation first while postgres can still commit, then
	// scale the old peer to zero. the scheduler places the new job against
	// the in-use volume (retrying) and only then stops the old job.
	d.newFormation.Processes[processType] = d.Processes[processType]
	log.Info("scaling new formation up", "release.id", d.NewReleaseID, "job.type", processType, "count", d.Processes[processType])
	if err := d.client.PutFormation(d.newFormation); err != nil {
		log.Error("error scaling new formation up", "release.id", d.NewReleaseID, "err", err)
		return err
	}
	d.deployEvents <- ct.DeploymentEvent{
		ReleaseID: d.NewReleaseID,
		JobState:  ct.JobStateStarting,
		JobType:   processType,
	}

	d.oldFormation.Processes[processType] = 0
	log.Info("scaling old formation down", "release.id", d.OldReleaseID, "job.type", processType)
	if err := d.client.PutFormation(d.oldFormation); err != nil {
		log.Error("error scaling old formation down", "release.id", d.OldReleaseID, "err", err)
		return err
	}
	d.deployEvents <- ct.DeploymentEvent{
		ReleaseID: d.OldReleaseID,
		JobState:  ct.JobStateStopping,
		JobType:   processType,
	}

	// wait for the new peer to register in discoverd, which happens once
	// the new job is started by the scheduler and the sirenia process
	// reaches its running state on the adopted volume. Poll discoverd in
	// case the Up event was already consumed (or missed) after PutFormation.
	timeout = time.After(d.timeout)
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	for {
		if inst := lookupSireniaPeer(svc, d.NewReleaseID, processType, sireniaclient.ProcessIDKey(processType)); inst != nil {
			log.Info("new sirenia peer registered", "addr", inst.Addr)
			d.deployEvents <- ct.DeploymentEvent{
				ReleaseID: d.NewReleaseID,
				JobState:  ct.JobStateUp,
				JobType:   processType,
			}
			log.Info("waiting for new sirenia peer to accept writes", "addr", inst.Addr)
			if err := sireniaclient.NewClient(inst.Addr).WaitForReadWrite(syncTimeout); err != nil {
				return fmt.Errorf("new sirenia peer did not become read-write: %s", err)
			}
			// proceed with non-sirenia process types now that
			// postgres is back up and the controller can again
			// persist scale state
			return d.deployOneByOne()
		}
		select {
		case <-d.stop:
			return worker.ErrStopped
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("service event stream closed unexpectedly: %s", stream.Err())
			}
			if event.Kind == discoverd.EventKindDown &&
				sireniaPeerMatchesRelease(event.Instance, d.OldReleaseID, processType) {
				d.deployEvents <- ct.DeploymentEvent{
					ReleaseID: d.OldReleaseID,
					JobState:  ct.JobStateDown,
					JobType:   processType,
				}
			}
		case <-poll.C:
		case <-timeout:
			return errors.New("timed out waiting for new sirenia peer to come up")
		}
	}
}
