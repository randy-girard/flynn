package plugin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/randy-girard/flynn/pkg/dbruntime"
)

// CreateInput is the generic addons:create option set. Core copies these
// fields into the provider config. It does not decide what --follow means;
// the plugin's docopt and reject rules decide whether the flag is accepted.
type CreateInput struct {
	As           string
	Follow       string
	Runtime      string
	Replication  string
	CPU          string
	Memory       string
	Disk         string
	AutoFailover bool
}

// ProvisionConfig builds the provider config JSON for a resource-backend add-on.
// Runtime names are checked against the type's declared runtimes. Other fields
// are passed through for the plugin to validate.
func (a *AddonSpec) ProvisionConfig(in CreateInput, cat dbruntime.Catalog) (json.RawMessage, error) {
	if a == nil {
		return nil, fmt.Errorf("addon type is required")
	}
	if a.Backend != BackendResource {
		return nil, fmt.Errorf("%s addons are not provisioned as resources", a.Type)
	}
	body := map[string]any{}
	if as := strings.TrimSpace(in.As); as != "" {
		body["as"] = as
	}
	if follow := strings.TrimSpace(in.Follow); follow != "" {
		body["follow"] = follow
	}
	if mode := strings.TrimSpace(in.Replication); mode != "" {
		body["replication"] = mode
	}
	if in.AutoFailover {
		body["auto_failover"] = true
	}
	if a.Runtimes == nil {
		if in.Runtime != "" || in.CPU != "" || in.Memory != "" || in.Disk != "" {
			return nil, fmt.Errorf("%s addons do not take --runtime, --cpu, --memory, or --disk", a.Type)
		}
		raw, err := json.Marshal(body)
		return raw, err
	}
	name, size, err := a.resolveRuntime(in, cat)
	if err != nil {
		return nil, err
	}
	if name != "" {
		body["runtime"] = name
		engine := a.Runtimes.Engine
		if engine == "" {
			engine = a.Type
		}
		if tags := dbruntime.PlacementTags(cat, engine, name); len(tags) > 0 {
			body["tags"] = tags
		}
		if rt, ok := cat.Find(engine, name); ok && rt.Dedicated {
			body["dedicated"] = true
		}
	}
	if size.CPU > 0 {
		body["cpu"] = size.CPU
	}
	if size.Memory > 0 {
		body["memory"] = size.Memory
	}
	if size.Disk > 0 {
		body["disk"] = size.Disk
	}
	raw, err := json.Marshal(body)
	return raw, err
}

func (a *AddonSpec) resolveRuntime(in CreateInput, cat dbruntime.Catalog) (string, dbruntime.Size, error) {
	rt := a.Runtimes
	name := strings.ToLower(strings.TrimSpace(in.Runtime))
	customSet := in.CPU != "" || in.Memory != "" || in.Disk != ""
	if customSet && !rt.CustomSizing {
		return "", dbruntime.Size{}, fmt.Errorf("%s addons do not take --cpu, --memory, or --disk", a.Type)
	}
	if !customSet && name == "" {
		name = rt.Default
	}
	if rt.Source == "list" {
		if customSet {
			sz, err := parseCustomSize(in)
			if err != nil {
				return "", dbruntime.Size{}, err
			}
			if !cat.AllowCustomSizes {
				return "", dbruntime.Size{}, dbruntime.ErrCustomSizesDisabled
			}
			return "custom", sz, nil
		}
		if !runtimeListed(rt, name) {
			return "", dbruntime.Size{}, fmt.Errorf("unknown runtime %q for %s", name, a.Type)
		}
		return name, dbruntime.Size{}, nil
	}
	var custom dbruntime.Size
	if customSet {
		sz, err := parseCustomSize(in)
		if err != nil {
			return "", dbruntime.Size{}, err
		}
		custom = sz
	}
	allow := cat.AllowCustomSizes && rt.CustomSizing
	sz, resolved, err := dbruntime.ResolveProvision(rt.Engine, name, custom, cat, allow)
	if err != nil {
		return "", dbruntime.Size{}, err
	}
	return resolved, sz, nil
}

func runtimeListed(rt *AddonRuntimes, name string) bool {
	if rt == nil {
		return false
	}
	for _, n := range rt.Names {
		if strings.EqualFold(n.Name, name) {
			return true
		}
	}
	return false
}

func parseCustomSize(in CreateInput) (dbruntime.Size, error) {
	var sz dbruntime.Size
	if in.CPU != "" {
		n, err := dbruntime.ParseCPU(in.CPU)
		if err != nil {
			return sz, err
		}
		sz.CPU = n
	}
	if in.Memory != "" {
		n, err := dbruntime.ParseBytes(in.Memory)
		if err != nil {
			return sz, err
		}
		sz.Memory = n
	}
	if in.Disk != "" {
		n, err := dbruntime.ParseBytes(in.Disk)
		if err != nil {
			return sz, err
		}
		sz.Disk = n
	}
	return sz, nil
}

// RuntimeSummary is the runtimes column for addons:types.
func (a *AddonSpec) RuntimeSummary(cat *dbruntime.Catalog) string {
	if a == nil || a.Runtimes == nil {
		return ""
	}
	rt := a.Runtimes
	if rt.Source == "list" {
		var names []string
		for _, n := range rt.Names {
			label := n.Name
			if strings.EqualFold(n.Name, rt.Default) {
				label += " (default)"
			}
			names = append(names, label)
		}
		return strings.Join(names, ", ")
	}
	if cat == nil {
		if rt.Default != "" {
			return rt.Default + " (default)"
		}
		return ""
	}
	var names []string
	for _, row := range cat.List() {
		eng, err := dbruntime.NormalizeEngine(row.Engine)
		if err != nil {
			continue
		}
		if eng != rt.Engine && row.Engine != rt.Engine {
			continue
		}
		label := row.Name
		if row.Name == rt.Default {
			label += " (default)"
		}
		names = append(names, label)
	}
	if len(names) == 0 && rt.Default != "" {
		return rt.Default + " (default)"
	}
	return strings.Join(names, ", ")
}
