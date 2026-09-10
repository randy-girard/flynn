package updaterdeploy

import (
	"fmt"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/inconshreveable/log15"
)

type appStrategyUpdater interface {
	UpdateApp(*ct.App) error
}

// EnsureRedisApplianceStrategy persists one-down-one-up on a redis appliance
// so flynn-host update reuses /data instead of allocating an empty volume.
func EnsureRedisApplianceStrategy(client appStrategyUpdater, app *ct.App, log log15.Logger) error {
	if app == nil || !app.EnsureRedisApplianceStrategy() {
		return nil
	}
	if err := client.UpdateApp(app); err != nil {
		return fmt.Errorf("error setting redis appliance strategy: %w", err)
	}
	if log != nil {
		log.Info("set redis appliance deploy strategy", "name", app.Name, "strategy", ct.RedisApplianceStrategy)
	}
	return nil
}
