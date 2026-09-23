package resource

import (
	"fmt"
	"strings"

	"github.com/docker/go-units"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

// Builtin runtime environment names bootstrapped on the controller.
const (
	ProfileSmall  = "small"
	ProfileMedium = "medium"
	ProfileLarge  = "large"
)

// NamedProfile is a named CPU/memory preset applied to a process type.
type NamedProfile struct {
	Name    string
	Memory  int64 // bytes
	CPU     int64 // milliCPU
	Builtin bool
}

// BuiltinProfiles returns the three cluster defaults (small / medium / large).
// Medium matches resource.Defaults() memory and CPU used for one-off jobs.
// New process types default to small the first time they are added to a release.
func BuiltinProfiles() []NamedProfile {
	return []NamedProfile{
		{Name: ProfileSmall, Memory: 512 * units.MiB, CPU: 500, Builtin: true},
		{Name: ProfileMedium, Memory: 1 * units.GiB, CPU: 1000, Builtin: true},
		{Name: ProfileLarge, Memory: 2 * units.GiB, CPU: 2000, Builtin: true},
	}
}

// HasMemoryOrCPU reports whether r already has an explicit memory or CPU request/limit.
func HasMemoryOrCPU(r Resources) bool {
	if r == nil {
		return false
	}
	if spec, ok := r[TypeMemory]; ok && (spec.Limit != nil || spec.Request != nil) {
		return true
	}
	if spec, ok := r[TypeCPU]; ok && (spec.Limit != nil || spec.Request != nil) {
		return true
	}
	return false
}

// ProcessRuntimeProfile is the named profile to apply when persisting a process type.
// An explicit profile is kept. First-time types (no memory or CPU set) default to small.
func ProcessRuntimeProfile(name string, r Resources) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	if HasMemoryOrCPU(r) {
		return ""
	}
	return ProfileSmall
}

// ProfileByName returns the builtin profile with the given name (case-insensitive).
func ProfileByName(name string) (NamedProfile, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range BuiltinProfiles() {
		if p.Name == want {
			return p, true
		}
	}
	return NamedProfile{}, false
}

// ApplyNamedLimits sets memory and CPU request+limit from a named profile.
func ApplyNamedLimits(r Resources, memoryBytes, milliCPU int64) {
	if r == nil {
		return
	}
	r[TypeMemory] = Spec{Request: typeconv.Int64Ptr(memoryBytes), Limit: typeconv.Int64Ptr(memoryBytes)}
	r[TypeCPU] = Spec{Request: typeconv.Int64Ptr(milliCPU), Limit: typeconv.Int64Ptr(milliCPU)}
}

// ValidateProfileName rejects empty or whitespace-only names.
func ValidateProfileName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("runtime name is required")
	}
	return nil
}
