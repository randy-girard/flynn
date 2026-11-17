package appmanifest

import (
	"fmt"
	"io"
)

// ContainerAddon is one flynn.yml setup.addons entry after the plan prefix
// has been mapped to a Flynn add-on type. Runtime is options.runtime. Empty
// means the type's default when the type publishes runtimes, and omitted
// when it does not.
type ContainerAddon struct {
	Type    string
	As      string
	Runtime string
}

// ApplyContainerSetup provisions flynn.yml setup on the first untouched
// deploy and on a pending resume. A later push sees meta flynn-app.json
// applied and does not create another database. The returned env is the
// release env after config is written, including addon URLs.
func (a *Applier) ApplyContainerSetup(appID string, addons []ContainerAddon, config map[string]string) (map[string]string, error) {
	if a.Log == nil {
		a.Log = io.Discard
	}
	st, err := a.LoadState(appID)
	if err != nil {
		return nil, err
	}
	switch Decide(st, true) {
	case ActionNone, ActionSkip:
		return a.headEnv(appID)
	}
	prepared, err := a.containerAddons(addons)
	if err != nil {
		return nil, err
	}
	app := st.App
	if metaValue(app, MetaStatus) != StatusPending {
		if err := a.updateMeta(app, func(meta map[string]string) {
			meta[MetaStatus] = StatusPending
		}); err != nil {
			return nil, err
		}
	}
	fmt.Fprintln(a.Log, "-----> Applying flynn.yml setup")
	if err := a.provisionAddons(app, &Manifest{Addons: prepared}); err != nil {
		return nil, fmt.Errorf("flynn.yml: %w", err)
	}
	if err := a.putEnv(appID, config, false); err != nil {
		return nil, fmt.Errorf("flynn.yml: %w", err)
	}
	if err := a.updateMeta(app, func(meta map[string]string) {
		meta[MetaStatus] = StatusApplied
	}); err != nil {
		return nil, err
	}
	return a.headEnv(appID)
}

func (a *Applier) containerAddons(addons []ContainerAddon) ([]Addon, error) {
	out := make([]Addon, 0, len(addons))
	for _, addon := range addons {
		if a.Catalog == nil || a.Catalog.AddonByType(addon.Type) == nil {
			return nil, fmt.Errorf("flynn.yml: unknown addon type %q", addon.Type)
		}
		spec := a.Catalog.AddonByType(addon.Type)
		opts := map[string]any{}
		if addon.Runtime != "" {
			opts["runtime"] = addon.Runtime
		} else if spec.Runtimes != nil {
			opts["runtime"] = "small"
		}
		item := Addon{Type: addon.Type, As: addon.As, Options: opts}
		if _, _, err := a.prepareAddon(item); err != nil {
			return nil, fmt.Errorf("flynn.yml: %w", err)
		}
		out = append(out, item)
	}
	return out, nil
}

func (a *Applier) headEnv(appID string) (map[string]string, error) {
	head, err := a.b.Head(appID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if head != nil {
		for k, v := range head.Env {
			out[k] = v
		}
	}
	return out, nil
}
