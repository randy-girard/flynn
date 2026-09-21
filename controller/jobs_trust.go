package main

import (
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
)

const (
	metaDatastore        = "flynn-datastore"
	metaPlugin           = "flynn-plugin"
	metaControllerPrefix = "flynn-controller."
)

// sanitizeOneOffJob strips host-trust knobs from a one-off job request unless
// the target app is a system app or tok is a cluster-admin credential.
// App-scoped jobs:run callers cannot mint a system-class host job.
func sanitizeOneOffJob(app *ct.App, newJob *ct.NewJob, tok *authorizer.Token) error {
	if newJob == nil {
		return nil
	}
	if oneOffJobTrusted(app, tok) {
		return nil
	}
	stripReservedJobMeta(newJob.Meta)
	if newJob.Partition == ct.PartitionTypeSystem {
		newJob.Partition = ct.PartitionTypeUser
	}
	if len(newJob.Profiles) > 0 {
		return ct.ValidationError{Field: "profiles", Message: "not permitted for this app"}
	}
	return nil
}

// oneOffJobTrusted reports whether the caller may set system partition,
// device profiles, and reserved Flynn metadata. A nil token is untrusted
// (unlike Token.HasClusterAdmin, which treats nil as the cluster key).
func oneOffJobTrusted(app *ct.App, tok *authorizer.Token) bool {
	if app != nil && app.System() {
		return true
	}
	return tok != nil && tok.HasClusterAdmin()
}

func stripReservedJobMeta(meta map[string]string) {
	for k := range meta {
		if reservedJobMetaKey(k) {
			delete(meta, k)
		}
	}
}

func reservedJobMetaKey(k string) bool {
	switch k {
	case metaSystemApp, metaDatastore, metaPlugin:
		return true
	}
	return strings.HasPrefix(k, metaControllerPrefix)
}

func releaseOwnedByApp(release *ct.Release, app *ct.App) error {
	if release == nil || app == nil {
		return nil
	}
	if release.AppID != "" && release.AppID != app.ID {
		return ct.ValidationError{Field: "release", Message: "does not belong to this app"}
	}
	return nil
}
