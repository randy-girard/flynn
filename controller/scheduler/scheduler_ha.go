package main

import (
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/sirenia/ha"
)

// maybePromoteSireniaHA flips singleton postgres/MariaDB/MongoDB appliances to
// HA when the cluster has at least three active hosts. Releases are immutable,
// so SINGLETON=false is a new release at scale 1 (volume handoff), then a later
// pass scales the data process to 3 once that primary is running.
func (s *Scheduler) maybePromoteSireniaHA() {
	if !s.IsLeader() || s.activeHostCount() < ha.MinHosts {
		return
	}
	seen := make(map[string]struct{})
	for _, f := range s.formations {
		if f == nil || f.App == nil || f.Release == nil || !f.Release.IsSirenia() {
			continue
		}
		appID := f.App.ID
		if _, ok := seen[appID]; ok {
			continue
		}
		procs := map[string]int(f.OriginalProcesses)
		if ha.NeedsEnvFlip(f.Release, procs) {
			if s.sireniaNonSingletonFormation(appID) != nil {
				seen[appID] = struct{}{}
				continue
			}
			log := s.logger.New("fn", "maybePromoteSireniaHA", "app", f.App.Name, "app.id", appID)
			log.Info("promoting singleton sirenia appliance to HA (env flip)")
			if err := s.promoteSireniaEnvFlip(f); err != nil {
				log.Error("error flipping SINGLETON", "err", err)
			}
			seen[appID] = struct{}{}
			continue
		}
		if ha.NeedsScale(f.Release, procs) {
			if !s.sireniaDataJobRunning(f) {
				continue
			}
			log := s.logger.New("fn", "maybePromoteSireniaHA", "app", f.App.Name, "app.id", appID)
			log.Info("scaling sirenia appliance to HA replica count")
			if err := s.scaleSireniaHA(f); err != nil {
				log.Error("error scaling sirenia to HA", "err", err)
			}
			seen[appID] = struct{}{}
		}
	}
}

func (s *Scheduler) sireniaNonSingletonFormation(appID string) *Formation {
	for _, f := range s.formations {
		if f == nil || f.App == nil || f.Release == nil || f.App.ID != appID {
			continue
		}
		if f.Release.IsSirenia() && !f.Release.IsSireniaSingleton() {
			data := ha.DataProcess(f.Release)
			if f.OriginalProcesses[data] > 0 {
				return f
			}
		}
	}
	return nil
}

func (s *Scheduler) sireniaDataJobRunning(f *Formation) bool {
	proc := ha.DataProcess(f.Release)
	for _, job := range s.jobs {
		if job.AppID != f.App.ID || job.ReleaseID != f.Release.ID || job.Type != proc {
			continue
		}
		if job.State == JobStateRunning {
			return true
		}
	}
	return false
}

func (s *Scheduler) promoteSireniaEnvFlip(f *Formation) error {
	newRel := ha.CloneWithoutSingleton(f.Release)
	if err := s.CreateRelease(f.App.ID, newRel); err != nil {
		return err
	}
	procs := copyProcMap(f.OriginalProcesses)
	if err := s.PutFormation(&ct.Formation{AppID: f.App.ID, ReleaseID: newRel.ID, Processes: procs}); err != nil {
		return err
	}
	if err := s.SetAppRelease(f.App.ID, newRel.ID); err != nil {
		return err
	}
	zeros := zeroProcMap(f.OriginalProcesses)
	if err := s.PutFormation(&ct.Formation{AppID: f.App.ID, ReleaseID: f.Release.ID, Processes: zeros}); err != nil {
		return err
	}
	newEF := *f.ExpandedFormation
	newEF.Release = newRel
	newEF.Processes = procs
	s.formations.Add(NewFormation(&newEF))
	f.SetProcesses(Processes(zeros))
	return nil
}

func (s *Scheduler) scaleSireniaHA(f *Formation) error {
	procs := ha.HAProcesses(f.Release, f.OriginalProcesses)
	if err := s.PutFormation(&ct.Formation{AppID: f.App.ID, ReleaseID: f.Release.ID, Processes: procs}); err != nil {
		return err
	}
	f.SetProcesses(Processes(procs))
	return nil
}

func copyProcMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func zeroProcMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k := range in {
		out[k] = 0
	}
	return out
}
