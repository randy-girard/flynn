package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

// rejectAttachedURLSet matches flynn-plugin-postgres RejectAttachedURLSet.
// attached maps env names currently injected by a resource attachment.
// A nil update is env:unset and is allowed. After detach the key is gone,
// so a later env:set is allowed.
func rejectAttachedURLSet(attached map[string]string, updates map[string]*string) error {
	var blocked []string
	for k, v := range updates {
		if v == nil || !strings.HasSuffix(k, "_URL") {
			continue
		}
		if _, ok := attached[k]; ok {
			blocked = append(blocked, k)
		}
	}
	if len(blocked) == 0 {
		return nil
	}
	sort.Strings(blocked)
	return fmt.Errorf("cannot env:set an attached URL while the resource is attached: %s", strings.Join(blocked, ", "))
}

func attachedURLKeys(resources []*ct.Resource) map[string]string {
	attached := map[string]string{}
	for _, r := range resources {
		if r == nil {
			continue
		}
		for k, v := range r.Env {
			if strings.HasSuffix(k, "_URL") && v != "" {
				attached[k] = v
			}
		}
	}
	return attached
}

func rejectEnvSetAttachedURLs(client controller.Client, app string, env map[string]*string) error {
	resources, err := client.AppResourceList(app)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil
		}
		return err
	}
	return rejectAttachedURLSet(attachedURLKeys(resources), env)
}
