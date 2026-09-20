package deployment

import (
	"fmt"
	"sort"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	worker "github.com/randy-girard/flynn/controller/worker/types"
)

// omniRollStep is one host in an omni one-down-one-up: stop old jobs except
// OldHostIDs (or all of them when OldZero), then start new jobs on NewHostIDs
// (or every host when ClearNewTags).
type omniRollStep struct {
	OldHostIDs   []string
	NewHostIDs   []string
	OldZero      bool
	ClearNewTags bool
}

func uniqueSortedHostIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func omniRollPlan(oldHosts, alreadyNew []string) []omniRollStep {
	oldHosts = uniqueSortedHostIDs(oldHosts)
	alreadyNew = uniqueSortedHostIDs(alreadyNew)
	if len(oldHosts) == 0 {
		return nil
	}
	steps := make([]omniRollStep, 0, len(oldHosts))
	for i := range oldHosts {
		remaining := append([]string{}, oldHosts[i+1:]...)
		newSet := uniqueSortedHostIDs(append(append([]string{}, alreadyNew...), oldHosts[:i+1]...))
		step := omniRollStep{NewHostIDs: newSet}
		if len(remaining) == 0 {
			step.OldZero = true
			step.ClearNewTags = true
			step.NewHostIDs = nil
		} else {
			step.OldHostIDs = remaining
		}
		steps = append(steps, step)
	}
	return steps
}

func (d *DeployJob) processIsOmni(typ string) bool {
	if d.newRelease != nil {
		if p, ok := d.newRelease.Processes[typ]; ok && p.Omni {
			return true
		}
	}
	if d.oldRelease != nil {
		if p, ok := d.oldRelease.Processes[typ]; ok && p.Omni {
			return true
		}
	}
	return false
}

func (d *DeployJob) oldReleaseStillActive() bool {
	if d.oldFormation == nil {
		return false
	}
	for _, n := range d.oldFormation.Processes {
		if n > 0 {
			return true
		}
	}
	return false
}

func jobHoldsHostPort(job *ct.Job) bool {
	if job == nil {
		return false
	}
	switch job.State {
	case ct.JobStatePending, ct.JobStateStarting, ct.JobStateUp, ct.JobStateStopping:
		return true
	default:
		return false
	}
}

func (d *DeployJob) omniHostIDs(typ, releaseID string) ([]string, error) {
	jobs, err := d.client.JobList(d.AppID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, job := range jobs {
		if job == nil || job.ReleaseID != releaseID || job.Type != typ || job.HostID == "" {
			continue
		}
		if !jobHoldsHostPort(job) {
			continue
		}
		ids = append(ids, job.HostID)
	}
	return uniqueSortedHostIDs(ids), nil
}

func hostIDProcessTags(typ string, ids []string, clear bool) map[string]map[string]string {
	inner := map[string]string{}
	if !clear && len(ids) > 0 {
		inner[ct.FormationHostIDsTag] = ct.EncodeHostIDsTag(ids)
	}
	return map[string]map[string]string{typ: inner}
}

func (d *DeployJob) setFormationHostIDs(form *ct.Formation, typ string, ids []string, clear bool, processes int) {
	if form.Processes == nil {
		form.Processes = make(map[string]int)
	}
	form.Processes[typ] = processes
	if form.Tags == nil {
		form.Tags = make(map[string]map[string]string)
	}
	form.Tags[typ] = hostIDProcessTags(typ, ids, clear)[typ]
}

func (d *DeployJob) scaleOmniOneDownOneUp(typ string, log log15.Logger) error {
	log = log.New("fn", "scaleOmniOneDownOneUp", "job.type", typ)
	log.Info("rolling omni process one host at a time")

	oldHosts, err := d.omniHostIDs(typ, d.OldReleaseID)
	if err != nil {
		return err
	}
	alreadyNew, err := d.omniHostIDs(typ, d.NewReleaseID)
	if err != nil {
		return err
	}

	plan := omniRollPlan(oldHosts, alreadyNew)
	if len(plan) == 0 {
		return d.finishOmniRoll(typ, log)
	}

	for i, step := range plan {
		log.Info("omni roll step", "step", i+1, "old_hosts", step.OldHostIDs, "new_hosts", step.NewHostIDs, "old_zero", step.OldZero)

		oldCount := d.Processes[typ]
		if step.OldZero {
			oldCount = 0
		}
		d.setFormationHostIDs(d.oldFormation, typ, step.OldHostIDs, step.OldZero, oldCount)
		if err := d.scaleOldRelease(true); err != nil {
			log.Error("error scaling old omni formation", "err", err)
			return err
		}
		if err := d.waitOldOmniJobsStopped(typ, step.OldHostIDs, log); err != nil {
			return err
		}

		newCount := d.Processes[typ]
		d.setFormationHostIDs(d.newFormation, typ, step.NewHostIDs, step.ClearNewTags, newCount)
		if err := d.scaleNewRelease(); err != nil {
			log.Error("error scaling new omni formation", "err", err)
			return err
		}
	}

	return d.finishOmniRoll(typ, log)
}

func (d *DeployJob) finishOmniRoll(typ string, log log15.Logger) error {
	if d.newFormation.Processes[typ] != d.Processes[typ] || len(d.newFormation.Tags[typ]) > 0 {
		d.setFormationHostIDs(d.newFormation, typ, nil, true, d.Processes[typ])
		if err := d.scaleNewRelease(); err != nil {
			log.Error("error clearing new omni host tags", "err", err)
			return err
		}
	}
	if d.oldFormation.Processes[typ] != 0 {
		d.setFormationHostIDs(d.oldFormation, typ, nil, true, 0)
		if err := d.scaleOldRelease(true); err != nil {
			log.Error("error scaling old omni formation to zero", "err", err)
			return err
		}
		if err := d.waitOldOmniJobsStopped(typ, nil, log); err != nil {
			return err
		}
	}
	return nil
}

func (d *DeployJob) waitOldOmniJobsStopped(typ string, remainingHosts []string, log log15.Logger) error {
	allowed := make(map[string]struct{}, len(remainingHosts))
	for _, id := range remainingHosts {
		allowed[id] = struct{}{}
	}
	deadline := time.Now().Add(d.timeout)
	for {
		jobs, err := d.client.JobList(d.AppID)
		if err != nil {
			return err
		}
		var leftover []string
		for _, job := range jobs {
			if job == nil || job.ReleaseID != d.OldReleaseID || job.Type != typ {
				continue
			}
			if !jobHoldsHostPort(job) {
				continue
			}
			if _, ok := allowed[job.HostID]; ok {
				continue
			}
			leftover = append(leftover, job.HostID)
		}
		if len(leftover) == 0 {
			return nil
		}
		log.Info("waiting for old omni jobs to release host ports", "hosts", leftover)
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for old %s jobs to stop on %v", typ, leftover)
		}
		select {
		case <-d.stop:
			return worker.ErrStopped
		case <-time.After(200 * time.Millisecond):
		}
	}
}
