package updaterdeploy

import (
	"fmt"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

// EnsureRouterStrategy persists one-down-one-up on the cluster router so
// flynn-host update stops the old host-network job before starting the
// replacement (otherwise the new job cannot bind :80/:443).
func EnsureRouterStrategy(client appStrategyUpdater, app *ct.App, log log15.Logger) error {
	if app == nil || !app.EnsureRouterStrategy() {
		return nil
	}
	if err := client.UpdateApp(app); err != nil {
		return fmt.Errorf("error setting router deploy strategy: %w", err)
	}
	if log != nil {
		log.Info("set router deploy strategy", "name", app.Name, "strategy", ct.RouterStrategy)
	}
	return nil
}
