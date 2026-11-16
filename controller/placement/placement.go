// Package placement is the cluster workload-placement mode. mixed is today's
// scheduler. split places by the host tag flynn-role. It is not a region:
// a region is another cluster.
package placement

import (
	"fmt"

	ct "github.com/randy-girard/flynn/controller/types"
)

const (
	// ModeMixed ignores flynn-role. It is the default and the behavior
	// before the first successful read.
	ModeMixed = "mixed"
	// ModeSplit places non-omni jobs by flynn-role.
	ModeSplit = "split"

	// TagRole is the host tag init --role and tags:set write.
	TagRole = "flynn-role"
	// RoleCore is an install host: control-plane processes and plugin apps.
	RoleCore = "core"
	// RoleApp is a host for user apps and tenant datastores.
	RoleApp = "app"
)

// Class is where one process may run while split is on.
// Omni processes run on every host. Otherwise Role is core or app.
type Class struct {
	Omni bool
	Role string
}

// Host is a placement target. Shutdown hosts do not satisfy split's core check.
type Host struct {
	Tags     map[string]string
	Shutdown bool
}

// ValidateMode accepts only mixed and split.
func ValidateMode(mode string) error {
	switch mode {
	case ModeMixed, ModeSplit:
		return nil
	default:
		return fmt.Errorf("mode must be mixed or split")
	}
}

// Classify reports the split role for a process. Omni is independent of the
// app. Plugin apps stay on core even when they are also datastores. The
// platform app named postgres stays on core. Other system datastores, and
// every app that is not a system app, go to app hosts. Remaining system
// processes (controller web and worker, blobstore, gitreceive, and the rest
// of the control plane) stay on core.
func Classify(app *ct.App, omni bool) Class {
	if omni {
		return Class{Omni: true}
	}
	if app != nil && app.Plugin() {
		return Class{Role: RoleCore}
	}
	if app != nil && app.System() && app.Name == "postgres" {
		return Class{Role: RoleCore}
	}
	if app != nil && app.System() && app.Datastore() {
		return Class{Role: RoleApp}
	}
	if app == nil || !app.System() {
		return Class{Role: RoleApp}
	}
	return Class{Role: RoleCore}
}

// HostAllows reports whether this host may start the process. Any mode other
// than split ignores flynn-role. A host with no flynn-role runs omni only.
func HostAllows(mode string, class Class, tags map[string]string) bool {
	if mode != ModeSplit {
		return true
	}
	if class.Omni {
		return true
	}
	return tags[TagRole] == class.Role
}

// HasCore reports whether a host that is not shutting down is tagged core.
func HasCore(hosts []Host) bool {
	for _, h := range hosts {
		if h.Shutdown {
			continue
		}
		if h.Tags[TagRole] == RoleCore {
			return true
		}
	}
	return false
}

// HasApp reports whether a host that is not shutting down is tagged app.
// Zero app hosts is allowed.
func HasApp(hosts []Host) bool {
	for _, h := range hosts {
		if h.Shutdown {
			continue
		}
		if h.Tags[TagRole] == RoleApp {
			return true
		}
	}
	return false
}
