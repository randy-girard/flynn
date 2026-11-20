package updaterdeploy

import (
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
)

// The first controller call after ReplaceHostInfraJobs swaps the last
// discoverd/flannel job can time out while the overlay settles. Overridable
// so tests do not sleep.
var (
	getAppRetryAttempts = 6
	getAppRetryDelay    = 5 * time.Second
)

type appGetter interface {
	GetApp(name string) (*ct.App, error)
}

// GetAppWithRetry looks up a system app, retrying transient controller
// errors. Not found and 401 return at once: callers handle a missing
// optional app, and a bad key never starts working.
func GetAppWithRetry(client appGetter, name string, log log15.Logger) (*ct.App, error) {
	var err error
	for attempt := 1; attempt <= getAppRetryAttempts; attempt++ {
		var app *ct.App
		app, err = client.GetApp(name)
		if err == nil || err == controller.ErrNotFound || httpclient.IsUnauthorized(err) {
			return app, err
		}
		if attempt == getAppRetryAttempts {
			break
		}
		if log != nil {
			log.Warn("error getting app, retrying", "app", name, "attempt", attempt, "err", err)
		}
		if getAppRetryDelay > 0 {
			time.Sleep(getAppRetryDelay)
		}
	}
	return nil, err
}
