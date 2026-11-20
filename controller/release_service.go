package main

import (
	"fmt"
	"sort"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// releaseServiceNames lists the discoverd names a release declares, in a
// stable order: ProcessType.Service and every port's host.Service.Name.
func releaseServiceNames(release *ct.Release) []string {
	if release == nil {
		return nil
	}
	types := make([]string, 0, len(release.Processes))
	for typ := range release.Processes {
		types = append(types, typ)
	}
	sort.Strings(types)
	seen := map[string]struct{}{}
	var out []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, typ := range types {
		proc := release.Processes[typ]
		add(proc.Service)
		for _, p := range proc.Ports {
			if p.Service != nil {
				add(p.Service.Name)
			}
		}
	}
	return out
}

// releaseServiceError rejects a release for a non-system app that declares
// a discoverd service the app does not own (CORE-17 / EDGE-01). Allowed
// names are "<app>" and "<app>-*", except platform services and names a
// more specific existing app owns ("shop" may not declare "shop-admin-web"
// when an app "shop-admin" exists). System and plugin apps keep their names.
// The rule applies to every caller: flynn-host enforces the same prefix rule
// and cannot tell an admin-minted release from a tenant one.
func (c *controllerAPI) releaseServiceError(app *ct.App, release *ct.Release) error {
	if app == nil || release == nil || app.System() {
		return nil
	}
	for _, name := range releaseServiceNames(release) {
		if err := netpolicy.UserServiceError(app.Name, name); err != nil {
			return ct.ValidationError{Field: "processes", Message: err.Error()}
		}
		owner, err := c.lookupServiceOwnerApp(name)
		if err != nil {
			return err
		}
		if owner != nil && owner.ID != app.ID {
			return ct.ValidationError{Field: "processes", Message: fmt.Sprintf("service %q belongs to another app", name)}
		}
	}
	return nil
}
