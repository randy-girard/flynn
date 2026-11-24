package updaterdeploy

import (
	"encoding/json"
	"fmt"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/plugin"
	sirenia "github.com/randy-girard/flynn/pkg/sirenia/state"
)

type discoverdService interface {
	GetMeta() (*discoverd.ServiceMeta, error)
	SetMeta(*discoverd.ServiceMeta) error
	Instances() ([]*discoverd.Instance, error)
}

var discoverdNewService = func(name string) discoverdService {
	return discoverd.NewService(name)
}

func discoverdInstancesOrEmpty(s discoverdService) ([]*discoverd.Instance, error) {
	instances, err := s.Instances()
	if err != nil && discoverd.IsNotFound(err) {
		return []*discoverd.Instance{}, nil
	}
	return instances, err
}

// RepairOrphanSireniaFormations scales down database processes on formations
// that are not part of the currently running sirenia cluster. Failed rolling
// deploys increment the new release formation but abort before scaling the old
// release down, leaving extra peers in discoverd. Subsequent deploys then wait
// for replication sync on the wrong upstream/downstream pair and time out.
//
// It only acts when the primary runs from the app's current release, and it
// never reduces that formation (EDGE-73). When the primary runs from another
// release, the current release's formation is what the next deploy copies:
// zeroing it is how the system Postgres ended up with postgres=0 on its
// current release while an older release ran every engine (EDGE-68), after
// which each sirenia deploy finished as a no-op. That mismatch is logged and
// left for the sirenia deploy, which moves the engines to the new release.
func RepairOrphanSireniaFormations(ctrl controller.Client, log log15.Logger) error {
	if log == nil {
		log = log15.New()
	}

	var apps []*ct.App
	if err := callWithRetry(func() error {
		var e error
		apps, e = ctrl.AppList()
		return e
	}); err != nil {
		return fmt.Errorf("list apps: %w", err)
	}

	var repaired int
	for _, app := range apps {
		if app == nil || !plugin.IsSireniaManaged(app) {
			continue
		}
		n, err := repairOrphanSireniaFormationsForApp(ctrl, app, log)
		if err != nil {
			return err
		}
		repaired += n
	}
	if repaired > 0 {
		log.Info("repaired orphan sirenia formations", "count", repaired)
	}
	return nil
}

// SireniaFormationState is what the formations of one sirenia app say about
// where its engines run.
type SireniaFormationState struct {
	App         string
	ProcessType string
	// CurrentRelease is the app's current release ("" when it has none).
	CurrentRelease string
	// CurrentCount is the current release's formation count of ProcessType.
	CurrentCount int
	// PrimaryRelease is FLYNN_RELEASE_ID of the sirenia primary.
	PrimaryRelease string
	// PrimaryCount is that release's formation count of ProcessType.
	PrimaryCount int
	// Others maps every other release whose formation asks for ProcessType
	// engines to its count (EDGE-76: a sirenia rollback that kept a failed
	// release's peers).
	Others map[string]int
}

// Split reports whether engines run from more than one formation.
func (s SireniaFormationState) Split() bool {
	n := len(s.Others)
	if s.CurrentCount > 0 {
		n++
	}
	if s.PrimaryRelease != s.CurrentRelease && s.PrimaryCount > 0 {
		n++
	}
	return n > 1
}

// Mismatch reports whether the primary runs from a release that is not the
// app's current release.
func (s SireniaFormationState) Mismatch() bool {
	return s.PrimaryRelease != "" && s.PrimaryRelease != s.CurrentRelease
}

// Stuck reports the EDGE-68 shape: the current release asks for no engines
// while the primary's release runs them. A deploy that copies the current
// formation would leave the engines where they are.
func (s SireniaFormationState) Stuck() bool {
	return s.Mismatch() && s.CurrentCount <= 0 && s.PrimaryCount > 0
}

// PlanOrphanSireniaRepair returns copies of the formations to scale to zero
// for processType, given the app's current release and the release the
// sirenia primary runs from. It plans nothing unless the primary runs from
// the current release and that formation runs engines, so it never reduces
// the current release, the primary's release, or the only formation that
// runs engines. The input is not modified.
func PlanOrphanSireniaRepair(currentRelease, primaryRelease, processType string, formations []*ct.Formation) []*ct.Formation {
	if currentRelease == "" || primaryRelease == "" || currentRelease != primaryRelease {
		return nil
	}
	current := 0
	for _, f := range formations {
		if f != nil && f.ReleaseID == currentRelease {
			current = f.Processes[processType]
		}
	}
	if current <= 0 {
		return nil
	}
	var zero []*ct.Formation
	for _, f := range formations {
		if f == nil || f.ReleaseID == currentRelease || f.Processes[processType] <= 0 {
			continue
		}
		cp := *f
		cp.Processes = make(map[string]int, len(f.Processes))
		for typ, n := range f.Processes {
			cp.Processes[typ] = n
		}
		cp.Processes[processType] = 0
		zero = append(zero, &cp)
	}
	return zero
}

// SireniaFormationStates reads every sirenia app's formations and primary.
// Apps without a published sirenia state are left out.
func SireniaFormationStates(ctrl controller.Client) ([]SireniaFormationState, error) {
	var apps []*ct.App
	if err := callWithRetry(func() error {
		var e error
		apps, e = ctrl.AppList()
		return e
	}); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	var out []SireniaFormationState
	for _, app := range apps {
		if app == nil || !plugin.IsSireniaManaged(app) {
			continue
		}
		state, _, ok, err := readSireniaFormationState(ctrl, app)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, state)
		}
	}
	return out, nil
}

// readSireniaFormationState returns ok=false when the app has no sirenia
// primary in discoverd.
func readSireniaFormationState(ctrl controller.Client, app *ct.App) (SireniaFormationState, []*ct.Formation, bool, error) {
	state := SireniaFormationState{App: app.Name}

	service := discoverdNewService(app.Name)
	meta, err := service.GetMeta()
	if err != nil || meta == nil || len(meta.Data) == 0 {
		return state, nil, false, nil
	}
	var cluster sirenia.State
	if err := json.Unmarshal(meta.Data, &cluster); err != nil {
		return state, nil, false, fmt.Errorf("decode %s sirenia state: %w", app.Name, err)
	}
	if cluster.Primary == nil || cluster.Primary.Meta == nil {
		return state, nil, false, nil
	}
	state.PrimaryRelease = cluster.Primary.Meta["FLYNN_RELEASE_ID"]
	if state.PrimaryRelease == "" {
		return state, nil, false, nil
	}

	var release *ct.Release
	if err := callWithRetry(func() error {
		var e error
		release, e = ctrl.GetRelease(state.PrimaryRelease)
		return e
	}); err != nil {
		return state, nil, false, fmt.Errorf("get active %s release: %w", app.Name, err)
	}
	state.ProcessType = release.Env["SIRENIA_PROCESS"]
	if state.ProcessType == "" {
		state.ProcessType = app.Name
	}

	// A never-deployed app has no current release; that is not retried.
	if err := callWithRetry(func() error {
		current, e := ctrl.GetAppRelease(app.ID)
		if e == controller.ErrNotFound {
			return nil
		}
		if e == nil && current != nil {
			state.CurrentRelease = current.ID
		}
		return e
	}); err != nil {
		return state, nil, false, fmt.Errorf("get current %s release: %w", app.Name, err)
	}

	var formations []*ct.Formation
	if err := callWithRetry(func() error {
		var e error
		formations, e = ctrl.FormationList(app.ID)
		return e
	}); err != nil {
		return state, nil, false, fmt.Errorf("list %s formations: %w", app.Name, err)
	}
	for _, f := range formations {
		if f == nil {
			continue
		}
		if f.ReleaseID == state.CurrentRelease {
			state.CurrentCount = f.Processes[state.ProcessType]
		}
		if f.ReleaseID == state.PrimaryRelease {
			state.PrimaryCount = f.Processes[state.ProcessType]
		}
		if f.ReleaseID != state.CurrentRelease && f.ReleaseID != state.PrimaryRelease && f.Processes[state.ProcessType] > 0 {
			if state.Others == nil {
				state.Others = make(map[string]int)
			}
			state.Others[f.ReleaseID] = f.Processes[state.ProcessType]
		}
	}
	return state, formations, true, nil
}

func repairOrphanSireniaFormationsForApp(ctrl controller.Client, app *ct.App, log log15.Logger) (int, error) {
	log = log.New("app", app.Name)

	state, formations, ok, err := readSireniaFormationState(ctrl, app)
	if err != nil || !ok {
		return 0, err
	}
	if state.CurrentRelease == "" {
		log.Warn("not repairing sirenia formations: the app has no current release", "active.release.id", state.PrimaryRelease)
		return 0, nil
	}
	if state.Mismatch() {
		log.Warn("sirenia engines run from a release that is not the app's current release; leaving every formation alone, the next deploy of the app moves the engines to the new release",
			"process", state.ProcessType,
			"current.release.id", state.CurrentRelease,
			"current.count", state.CurrentCount,
			"active.release.id", state.PrimaryRelease,
			"active.count", state.PrimaryCount,
		)
		return 0, nil
	}

	zero := PlanOrphanSireniaRepair(state.CurrentRelease, state.PrimaryRelease, state.ProcessType, formations)
	if len(zero) == 0 && state.CurrentCount <= 0 {
		for _, f := range formations {
			if f != nil && f.ReleaseID != state.CurrentRelease && f.Processes[state.ProcessType] > 0 {
				log.Warn("not repairing sirenia formations: the current release's formation runs no engines",
					"process", state.ProcessType, "current.release.id", state.CurrentRelease, "release.id", f.ReleaseID, "count", f.Processes[state.ProcessType])
			}
		}
	}
	counts := make(map[string]int, len(formations))
	for _, f := range formations {
		if f != nil {
			counts[f.ReleaseID] = f.Processes[state.ProcessType]
		}
	}
	var repaired int
	for _, formation := range zero {
		log.Warn("scaling orphan sirenia formation to zero",
			"release.id", formation.ReleaseID,
			"process", state.ProcessType,
			"count", counts[formation.ReleaseID],
			"active.release.id", state.PrimaryRelease,
		)
		if err := callWithRetry(func() error { return ctrl.PutFormation(formation) }); err != nil {
			return repaired, fmt.Errorf("scale orphan %s formation %s: %w", app.Name, formation.ReleaseID, err)
		}
		repaired++
	}
	return repaired, nil
}
