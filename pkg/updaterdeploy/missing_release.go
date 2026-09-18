package updaterdeploy

import (
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

const MissingAppReleaseReason = "app has no release"

// MissingAppReleaseSkip reports whether GetAppRelease failed because a
// non-system app has never been deployed. Cluster updates skip those apps
// (dashboard-created apps with no git push) instead of aborting. Required
// system apps still fail so a missing controller/postgres release is fatal.
func MissingAppReleaseSkip(app *ct.App, err error) bool {
	return err == controller.ErrNotFound && app != nil && !app.System()
}
