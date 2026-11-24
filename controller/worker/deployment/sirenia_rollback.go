package deployment

import (
	"fmt"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	worker "github.com/randy-girard/flynn/controller/worker/types"
)

// rollbackSirenia rolls back a failed sirenia deploy without stopping the
// peers that hold the database (EDGE-76). The plain rollback restores the old
// formation and deletes the new one. Once the deploy has moved the primary to
// the new release, that delete stops the primary and the sync, and the old
// release comes back as peers whose data is older than the new primary's (or
// empty): writes made since the move are lost, or the cluster has no primary.
//
// What it does depends on where the sirenia primary is:
//
//   - The new release runs no engines: nothing moved, so the plain rollback
//     is safe (handled=false).
//   - The primary is on the new release: the new formation keeps its engines
//     and nothing of it is stopped. Every old peer was already replaced, so
//     when the new release has at least the deploy's count of members the
//     other formations stop asking for engines (the step the deploy did not
//     reach); otherwise their counts are left as they are. The other process
//     types are rolled back (old restored, new zeroed). The app's current
//     release stays the old one, and the error says so; the next deploy of
//     the app moves the engines to one release (locateSireniaEngines).
//   - The primary is on another release (mid-roll: some asyncs or the sync
//     are new peers): the old formation is restored, and the new peers are
//     removed only once at least the deploy's count of members run from other
//     releases, so the cluster never drops below its size. If they do not
//     join in time the new peers are kept and the error says so.
//   - The primary cannot be read: the old formation is restored and the new
//     release's engines are kept.
//
// Keeping peers running never loses a write; stopping the wrong ones can. So
// when in doubt the engines stay and the deployment fails loudly.
func (c *context) rollbackSirenia(l log15.Logger, deployment *ct.Deployment, original *ct.Formation, stop chan struct{}) (handled bool, err error) {
	log := l.New("fn", "rollbackSirenia")

	newRelease, _ := c.client.GetRelease(deployment.NewReleaseID)
	oldRelease, _ := c.client.GetRelease(deployment.OldReleaseID)
	processType, service := "", ""
	for _, r := range []*ct.Release{newRelease, oldRelease} {
		if r == nil {
			continue
		}
		if processType == "" {
			processType = r.Env["SIRENIA_PROCESS"]
		}
		if processType != "" && service == "" {
			service = r.Processes[processType].Service
		}
	}
	if processType == "" || service == "" {
		log.Warn("could not find the sirenia process type and service, using the plain rollback")
		return false, nil
	}

	newForm, err := c.client.GetFormation(deployment.AppID, deployment.NewReleaseID)
	if err == controller.ErrNotFound {
		return false, nil
	} else if err != nil {
		log.Error("error getting the new formation, leaving both formations as they are", "err", err)
		return true, fmt.Errorf("could not read the new release %s's formation to roll back a sirenia deploy, so both formations were left as they are: %s", deployment.NewReleaseID, err)
	}
	newEngines := newForm.Processes[processType]
	if newEngines <= 0 {
		log.Info("the new release runs no sirenia engines, using the plain rollback")
		return false, nil
	}

	want := deployment.Processes[processType]
	if want <= 0 {
		want = original.Processes[processType]
	}
	if want <= 0 {
		want = newEngines
	}

	primary, perr := sireniaPrimaryRelease(service)
	log = log.New("process", processType, "service", service, "new.engines", newEngines, "want", want, "primary.release.id", primary)
	switch {
	case perr == nil && primary == deployment.NewReleaseID:
		return true, c.rollbackSireniaPrimaryMoved(log, deployment, original, newForm, processType, service, want, stop)
	case perr == nil && primary != "":
		return true, c.rollbackSireniaMidRoll(log, deployment, original, newForm, processType, service, want, stop)
	default:
		log.Error("could not tell which release runs the sirenia primary; restoring the old formation and keeping the new release's engines", "err", perr)
		if err := c.restoreOriginalFormation(log, original, original.Processes, stop); err != nil {
			log.Error("error restoring the original formation", "err", err)
		}
		c.zeroNonSireniaTypes(log, newForm, processType)
		why := "no primary is published"
		if perr != nil {
			why = perr.Error()
		}
		return true, fmt.Errorf("could not tell which release runs the sirenia primary (%s), so the rollback kept the new release %s's %d %s engines running; the app's current release is still %s and the next deploy moves the engines to one release",
			why, deployment.NewReleaseID, newEngines, processType, deployment.OldReleaseID)
	}
}

func (c *context) rollbackSireniaPrimaryMoved(log log15.Logger, deployment *ct.Deployment, original, newForm *ct.Formation, processType, service string, want int, stop chan struct{}) error {
	newEngines := newForm.Processes[processType]
	members, merr := sireniaClusterReleases(service)
	onNew := 0
	for _, m := range members {
		if m == deployment.NewReleaseID {
			onNew++
		}
	}
	complete := merr == nil && onNew >= want && newEngines >= want
	log.Error("the sirenia primary runs from the new release; keeping its formation and not stopping any of its engines",
		"members.on.new", onNew, "members", len(members), "members.err", merr, "complete", complete)

	processes := make(map[string]int, len(original.Processes))
	for typ, n := range original.Processes {
		processes[typ] = n
	}
	oldEngines := original.Processes[processType]
	if cur, err := c.client.GetFormation(deployment.AppID, deployment.OldReleaseID); err == nil {
		oldEngines = cur.Processes[processType]
	}
	if complete {
		processes[processType] = 0
	} else {
		processes[processType] = oldEngines
	}
	if err := c.restoreOriginalFormation(log, original, processes, stop); err != nil {
		log.Error("error rolling back the old formation's other process types", "err", err)
	}
	if complete {
		c.zeroOtherSireniaFormations(log, deployment, processType)
	}
	c.zeroNonSireniaTypes(log, newForm, processType)

	var detail string
	if complete {
		detail = fmt.Sprintf("its %d members replaced every old peer, so the other releases' %s counts were set to 0", onNew, processType)
	} else {
		detail = fmt.Sprintf("only %d of %d sirenia members run from it, so the old release's %s count was left at %d", onNew, want, processType, processes[processType])
	}
	return fmt.Errorf("the sirenia primary runs from the new release %s, so the rollback kept that release's formation (%s=%d) and stopped none of its engines; %s; the app's current release is still %s, and the next deploy of the app (flynn-host update) moves the engines to one release",
		deployment.NewReleaseID, processType, newEngines, detail, deployment.OldReleaseID)
}

func (c *context) rollbackSireniaMidRoll(log log15.Logger, deployment *ct.Deployment, original, newForm *ct.Formation, processType, service string, want int, stop chan struct{}) error {
	if err := c.restoreOriginalFormation(log, original, original.Processes, stop); err != nil {
		return err
	}
	log.Info("waiting for the old release's sirenia peers to join before removing the new ones")
	deadline := time.Now().Add(sireniaRollbackJoinTimeout)
	others, total := 0, 0
	for {
		members, err := sireniaClusterReleases(service)
		if err == nil {
			others, total = 0, len(members)
			for _, m := range members {
				if m != "" && m != deployment.NewReleaseID {
					others++
				}
			}
			if others >= want {
				break
			}
		} else {
			log.Warn("error reading the sirenia cluster", "err", err)
		}
		if time.Now().After(deadline) {
			log.Error("the old release's sirenia peers did not join in time; keeping the new release's engines", "members.other", others, "members", total)
			c.zeroNonSireniaTypes(log, newForm, processType)
			return fmt.Errorf("rolled back to %s but kept the new release %s's %d %s engines: only %d of %d sirenia members ran from other releases after %s, and removing the new peers could leave the cluster short (one may be the sync); the next deploy's orphan repair removes them once the cluster is whole",
				deployment.OldReleaseID, deployment.NewReleaseID, newForm.Processes[processType], processType, others, want, sireniaRollbackJoinTimeout)
		}
		select {
		case <-stop:
			c.zeroNonSireniaTypes(log, newForm, processType)
			return worker.ErrStopped
		case <-time.After(sireniaRollbackPoll):
		}
	}
	log.Info("the old release's sirenia peers joined", "members.other", others, "members", total)
	return c.deleteNewFormation(log, deployment)
}

// zeroNonSireniaTypes scales every type of the new formation except the
// sirenia engines to zero (the old formation was restored for them).
func (c *context) zeroNonSireniaTypes(log log15.Logger, form *ct.Formation, processType string) {
	processes := make(map[string]int, len(form.Processes))
	changed := false
	for typ, n := range form.Processes {
		if typ != processType && n != 0 {
			n = 0
			changed = true
		}
		processes[typ] = n
	}
	if !changed {
		return
	}
	log.Info("scaling the new release's other process types to zero", "release.id", form.ReleaseID, "processes", processes)
	if err := c.client.ScaleAppRelease(form.AppID, form.ReleaseID, ct.ScaleOptions{Processes: processes, Tags: form.Tags, NoWait: true}); err != nil {
		log.Error("error scaling the new release's other process types to zero", "release.id", form.ReleaseID, "err", err)
	}
}

// zeroOtherSireniaFormations sets processType to 0 on every formation of the
// app except the new release's (an adopted engines formation, EDGE-73).
func (c *context) zeroOtherSireniaFormations(log log15.Logger, deployment *ct.Deployment, processType string) {
	forms, err := c.client.FormationList(deployment.AppID)
	if err != nil {
		log.Error("error listing formations", "err", err)
		return
	}
	for _, f := range forms {
		if f == nil || f.ReleaseID == deployment.NewReleaseID || f.ReleaseID == deployment.OldReleaseID || f.Processes[processType] <= 0 {
			continue
		}
		processes := make(map[string]int, len(f.Processes))
		for typ, n := range f.Processes {
			processes[typ] = n
		}
		processes[processType] = 0
		log.Warn("scaling down the sirenia engines of a release whose peers were replaced", "release.id", f.ReleaseID, "processes", f.Processes)
		if err := c.client.ScaleAppRelease(deployment.AppID, f.ReleaseID, ct.ScaleOptions{Processes: processes, Tags: f.Tags, NoWait: true}); err != nil {
			log.Error("error scaling down sirenia engines", "release.id", f.ReleaseID, "err", err)
		}
	}
}
