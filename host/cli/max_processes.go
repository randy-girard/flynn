package cli

import (
	"os"
	"sort"
	"strconv"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/dbruntime"
)

// formatTagMap serializes host tags the way the daemon --tags flag parses them.
func formatTagMap(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for k, v := range tags {
		if k == "" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := tags[k]
		if v == "true" {
			parts = append(parts, k)
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// parseDaemonTagList parses the comma-separated value of daemon --tags.
func parseDaemonTagList(raw string) map[string]string {
	tags := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok || key == "" {
			if part != "" {
				tags[part] = "true"
			}
			continue
		}
		tags[key] = val
	}
	return tags
}

// tagsFromDaemonArgs returns the --tags map stored in host.json args.
// The boolean is false when args do not contain --tags.
func tagsFromDaemonArgs(args []string) (map[string]string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--tags" {
			return parseDaemonTagList(args[i+1]), true
		}
	}
	return map[string]string{}, false
}

func setArgsTagValue(args []string, tagValue string) []string {
	out := make([]string, 0, len(args)+2)
	replaced := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--tags" && i+1 < len(args) {
			if tagValue != "" {
				out = append(out, "--tags", tagValue)
			}
			i++
			replaced = true
			continue
		}
		out = append(out, args[i])
	}
	if !replaced && tagValue != "" {
		out = append(out, "--tags", tagValue)
	}
	return out
}

// WriteDaemonTags replaces the --tags argument in host.json and keeps the
// previous file mode. A missing file is left missing.
func WriteDaemonTags(path string, tags map[string]string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	conf, err := config.Open(path)
	if err != nil {
		return err
	}
	conf.Args = setArgsTagValue(conf.Args, formatTagMap(tags))
	if err := conf.WriteTo(path); err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if len(conf.Env) > 0 {
		mode = 0600
	}
	return os.Chmod(path, mode)
}

// BuiltinAppMemoryLimits is the app runtime sizes migration 54 seeds.
// It is the fallback only while flynn-max-processes is still "auto".
func BuiltinAppMemoryLimits() []int64 {
	profiles := resource.BuiltinProfiles()
	out := make([]int64, 0, len(profiles))
	for _, p := range profiles {
		if p.Memory > 0 {
			out = append(out, p.Memory)
		}
	}
	return out
}

// RuntimeMemoryLimits is every positive app runtime and stored database
// runtime memory. Engines that are not in the catalog are absent.
func RuntimeMemoryLimits(profiles []*ct.RuntimeProfile, cat *dbruntime.Catalog) []int64 {
	var out []int64
	for _, p := range profiles {
		if p != nil && p.Memory > 0 {
			out = append(out, p.Memory)
		}
	}
	if cat != nil {
		for _, r := range cat.Runtimes {
			if r.Memory > 0 {
				out = append(out, r.Memory)
			}
		}
	}
	return out
}

// ResolveAutoHostMaxProcesses returns the tag map to publish when source is
// auto and the number changed. liveOK means the controller answered.
// A numeric cap is kept when the controller is down. Operator caps, legacy
// numeric caps, and hosts with no cap are left unchanged.
func ResolveAutoHostMaxProcesses(tags map[string]string, memTotal int64, overlayAddrs int, live []int64, liveOK bool, builtin []int64) (map[string]string, bool) {
	if tags[ct.FormationMaxProcessesSourceTag] != ct.HostMaxProcessesSourceAuto {
		return nil, false
	}
	raw := strings.TrimSpace(tags[ct.FormationMaxProcessesTag])
	var memories []int64
	switch {
	case liveOK:
		memories = live
	case strings.EqualFold(raw, ct.HostMaxProcessesAuto) || raw == "":
		memories = builtin
	default:
		return nil, false
	}
	n := ct.DefaultHostMaxProcesses(memTotal, memories, overlayAddrs)
	if n < 1 {
		return nil, false
	}
	encoded := strconv.Itoa(n)
	if tags[ct.FormationMaxProcessesTag] == encoded {
		return nil, false
	}
	next := make(map[string]string, len(tags)+2)
	for k, v := range tags {
		if k != "" && v != "" {
			next[k] = v
		}
	}
	next[ct.FormationMaxProcessesTag] = encoded
	next[ct.FormationMaxProcessesSourceTag] = ct.HostMaxProcessesSourceAuto
	return next, true
}
