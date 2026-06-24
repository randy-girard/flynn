package cli

import (
	"reflect"

	ct "github.com/flynn/flynn/controller/types"
	updater "github.com/flynn/flynn/updater/types"
)

// releaseConfigChanged reports whether updateFn would alter the release env or
// process definitions relative to what is currently deployed.
func releaseConfigChanged(before, after *ct.Release) bool {
	if before == nil || after == nil {
		return before != after
	}
	if len(before.Env) != len(after.Env) {
		return true
	}
	for k, v := range before.Env {
		if after.Env[k] != v {
			return true
		}
	}
	for k, v := range after.Env {
		if before.Env[k] != v {
			return true
		}
	}
	if len(before.Processes) != len(after.Processes) {
		return true
	}
	return !reflect.DeepEqual(before.Processes, after.Processes)
}

func cloneReleaseForUpdate(release *ct.Release) *ct.Release {
	if release == nil {
		return nil
	}
	cloned := *release
	if release.Env != nil {
		cloned.Env = make(map[string]string, len(release.Env))
		for k, v := range release.Env {
			cloned.Env[k] = v
		}
	}
	if release.Processes != nil {
		cloned.Processes = make(map[string]ct.ProcessType, len(release.Processes))
		for k, v := range release.Processes {
			cloned.Processes[k] = v
		}
	}
	return &cloned
}

// shouldSkipUnchangedDeploy returns whether a system app deploy can be skipped
// because the target image manifest and release configuration are both already
// deployed. --force still runs binary/image rollout but should not trigger a
// full sirenia redeploy when nothing changed.
func shouldSkipUnchangedDeploy(skipDeploy, force bool, release *ct.Release, updateFn updater.UpdateReleaseFn) (skip bool, forceConfigMigration bool) {
	if !skipDeploy {
		return false, false
	}
	if updateFn != nil {
		updated := cloneReleaseForUpdate(release)
		updateFn(updated)
		if !releaseConfigChanged(release, updated) {
			return true, false
		}
		return false, true
	}
	if !force {
		return true, false
	}
	return false, false
}
