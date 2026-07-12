package updaterdeploy

import (
	"encoding/json"
	"fmt"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	sirenia "github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/inconshreveable/log15"
)

var sireniaApps = []string{"postgres", "mariadb", "mongodb"}

type discoverdService interface {
	GetMeta() (*discoverd.ServiceMeta, error)
}

var discoverdNewService = func(name string) discoverdService {
	return discoverd.NewService(name)
}

// RepairOrphanSireniaFormations scales down database processes on formations
// that are not part of the currently running sirenia cluster. Failed rolling
// deploys increment the new release formation but abort before scaling the old
// release down, leaving extra peers in discoverd. Subsequent deploys then wait
// for replication sync on the wrong upstream/downstream pair and time out.
func RepairOrphanSireniaFormations(ctrl controller.Client, log log15.Logger) error {
	if log == nil {
		log = log15.New()
	}

	apps, err := ctrl.AppList()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	appsByName := make(map[string]*ct.App, len(apps))
	for _, app := range apps {
		if app != nil && app.Name != "" {
			appsByName[app.Name] = app
		}
	}

	var repaired int
	for _, appName := range sireniaApps {
		app, ok := appsByName[appName]
		if !ok {
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

func repairOrphanSireniaFormationsForApp(ctrl controller.Client, app *ct.App, log log15.Logger) (int, error) {
	log = log.New("app", app.Name)

	service := discoverdNewService(app.Name)
	meta, err := service.GetMeta()
	if err != nil {
		return 0, nil
	}
	if meta == nil || len(meta.Data) == 0 {
		return 0, nil
	}

	var state sirenia.State
	if err := json.Unmarshal(meta.Data, &state); err != nil {
		return 0, fmt.Errorf("decode %s sirenia state: %w", app.Name, err)
	}
	if state.Primary == nil || state.Primary.Meta == nil {
		return 0, nil
	}
	activeRelease := state.Primary.Meta["FLYNN_RELEASE_ID"]
	if activeRelease == "" {
		return 0, nil
	}

	release, err := ctrl.GetRelease(activeRelease)
	if err != nil {
		return 0, fmt.Errorf("get active %s release: %w", app.Name, err)
	}
	processType := release.Env["SIRENIA_PROCESS"]
	if processType == "" {
		processType = app.Name
	}

	formations, err := ctrl.FormationList(app.ID)
	if err != nil {
		return 0, fmt.Errorf("list %s formations: %w", app.Name, err)
	}

	var repaired int
	for _, formation := range formations {
		if formation == nil || formation.ReleaseID == activeRelease {
			continue
		}
		count := formation.Processes[processType]
		if count <= 0 {
			continue
		}
		log.Warn("scaling orphan sirenia formation to zero",
			"release.id", formation.ReleaseID,
			"process", processType,
			"count", count,
			"active.release.id", activeRelease,
		)
		formation.Processes[processType] = 0
		if err := ctrl.PutFormation(formation); err != nil {
			return repaired, fmt.Errorf("scale orphan %s formation %s: %w", app.Name, formation.ReleaseID, err)
		}
		repaired++
	}
	return repaired, nil
}
