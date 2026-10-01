package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/pkg/resname"
)

// rejectLockedSet blocks env:set and env:unset of keys currently injected by a
// resource attachment. After the resource is removed those keys are gone, so a
// later env:set is allowed.
func rejectLockedSet(locked map[string]string, updates map[string]*string) error {
	var blocked []string
	for k := range updates {
		if _, ok := locked[k]; ok {
			blocked = append(blocked, k)
		}
	}
	if len(blocked) == 0 {
		return nil
	}
	sort.Strings(blocked)
	return fmt.Errorf("cannot change an attached env var while the resource is attached: %s", strings.Join(blocked, ", "))
}

func rejectEnvSetAttachedURLs(client controller.Client, app string, env map[string]*string) error {
	resources, err := client.AppResourceList(app)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil
		}
		return err
	}
	releaseEnv := map[string]string{}
	if release, err := client.GetAppRelease(app); err == nil && release != nil && release.Env != nil {
		releaseEnv = release.Env
	} else if err != nil && !errors.Is(err, controller.ErrNotFound) {
		return err
	}
	var envs []map[string]string
	for _, r := range resources {
		if r != nil && r.Env != nil {
			envs = append(envs, r.Env)
		}
	}
	return rejectLockedSet(resname.LockedKeys(releaseEnv, envs...), env)
}
