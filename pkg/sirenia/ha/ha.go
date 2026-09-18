package ha

import ct "github.com/randy-girard/flynn/controller/types"

const (
	// MinHosts is the smallest cluster that can run a sirenia replica set.
	MinHosts = 3
	// DataCount is the HA formation size for the sirenia data process.
	DataCount = 3
	// WebCount is the HA formation size for an appliance web process.
	WebCount = 2
)

// DataProcess is the sirenia process type on a release (SIRENIA_PROCESS).
func DataProcess(r *ct.Release) string {
	if r == nil || r.Env == nil {
		return ""
	}
	return r.Env["SIRENIA_PROCESS"]
}

// NeedsEnvFlip reports whether a running singleton appliance should be
// redeployed with SINGLETON=false before it can form a replica set.
func NeedsEnvFlip(r *ct.Release, procs map[string]int) bool {
	if r == nil || !r.IsSireniaSingleton() {
		return false
	}
	return procs[DataProcess(r)] > 0
}

// NeedsScale reports whether a non-singleton sirenia appliance is still
// below HA data-process count (typically 1 after an env flip).
func NeedsScale(r *ct.Release, procs map[string]int) bool {
	if r == nil || !r.IsSirenia() || r.IsSireniaSingleton() {
		return false
	}
	n := procs[DataProcess(r)]
	return n > 0 && n < DataCount
}

// HAProcesses returns a copy of procs with the data process at DataCount and
// web at WebCount when web is already running.
func HAProcesses(r *ct.Release, procs map[string]int) map[string]int {
	out := make(map[string]int, len(procs))
	for k, v := range procs {
		out[k] = v
	}
	if data := DataProcess(r); data != "" {
		out[data] = DataCount
	}
	if n, ok := out["web"]; ok && n > 0 && n < WebCount {
		out["web"] = WebCount
	}
	return out
}

// CloneWithoutSingleton copies a release for CreateRelease with SINGLETON=false
// and an empty ID so the controller assigns a new one.
func CloneWithoutSingleton(src *ct.Release) *ct.Release {
	if src == nil {
		return &ct.Release{Env: map[string]string{"SINGLETON": "false"}}
	}
	dst := *src
	dst.ID = ""
	dst.CreatedAt = nil
	dst.ArtifactIDs = append([]string(nil), src.ArtifactIDs...)
	dst.Env = copyStringMap(src.Env)
	if dst.Env == nil {
		dst.Env = map[string]string{}
	}
	dst.Env["SINGLETON"] = "false"
	dst.Meta = copyStringMap(src.Meta)
	return &dst
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
