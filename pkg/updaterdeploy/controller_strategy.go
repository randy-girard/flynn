package updaterdeploy

import (
	"fmt"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

// EnsureControllerStrategy persists the controller deploy strategy for this
// cluster size. 1-node uses all-at-once so a new scheduler starts before the
// last one is stopped. HA uses one-by-one so omni rolling keeps a scheduler
// alive to update JobList.
func EnsureControllerStrategy(client appStrategyUpdater, app *ct.App, hostCount int, log log15.Logger) error {
	if app == nil || !app.EnsureControllerStrategy(hostCount) {
		return nil
	}
	if err := client.UpdateApp(app); err != nil {
		return fmt.Errorf("error setting controller deploy strategy: %w", err)
	}
	if log != nil {
		log.Info("set controller deploy strategy", "name", app.Name, "strategy", app.Strategy, "hosts", hostCount)
	}
	return nil
}
