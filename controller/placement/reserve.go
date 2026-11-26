package placement

import (
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
)

// TenancyHosted matches controller/tenancy.ModeHosted. This package does not
// import tenancy; that package pulls scrypt into the scheduler binary.
const TenancyHosted = "hosted"

// PlatformApp reports a system or platform app: the controller, router,
// discoverd, blobstore, the platform postgres, plugin apps and the rest of
// the control plane. It is the split-placement core class (Classify) of a
// flynn-system-app. Tenant datastores (redis-<uuid> appliances, isolated
// database instances) are system apps too, but they are a tenant's workload
// and are not platform apps.
func PlatformApp(app *ct.App) bool {
	return app != nil && app.System() && Classify(app, false).Role == RoleCore
}

// Reservations is the rule for which processes reserve their CPU and memory
// on a host at placement time. The scheduler places with it, and the
// controller's capacity view and the deployer's pre-check read with it, so
// all three agree.
type Reservations struct {
	// TenancyMode is cluster_settings.tenancy_mode. Empty before the first
	// read: only a profile's stored flag reserves.
	TenancyMode string
	// ProfileReserve is RuntimeProfile.ReserveResources by lower-case name.
	ProfileReserve map[string]bool
}

// ProfileName is the process's runtime profile, trimmed and lower-case.
func ProfileName(proc ct.ProcessType) string {
	return strings.ToLower(strings.TrimSpace(proc.RuntimeProfile))
}

func hostedBuiltinProfile(name string) bool {
	switch name {
	case resource.ProfileSmall, resource.ProfileMedium, resource.ProfileLarge:
		return true
	default:
		return false
	}
}

// Reserves reports whether a job of this app and process reserves. A process
// with no runtime profile never reserves. In hosted mode the builtin small,
// medium and large profiles reserve even when their flag is off, and a
// platform app never reserves (NEW-45): platform jobs neither count against
// nor are blocked by tenant reservations, so a full host can still get its
// router, controller or update replacement. In self_hosted mode the stored
// flag applies to every app.
func (r Reservations) Reserves(app *ct.App, proc ct.ProcessType) bool {
	name := ProfileName(proc)
	if name == "" {
		return false
	}
	if r.TenancyMode == TenancyHosted {
		if PlatformApp(app) {
			return false
		}
		if hostedBuiltinProfile(name) {
			return true
		}
	}
	return r.ProfileReserve[name]
}

// Request is the CPU (milli) and memory (bytes) a process asks for: the
// Request values, or, for a reserving process whose Request was not copied
// onto the process, its Limit, so a host still cannot overbook.
func Request(proc ct.ProcessType, reserves bool) (mem, cpu int64) {
	if spec, ok := proc.Resources[resource.TypeMemory]; ok && spec.Request != nil {
		mem = *spec.Request
	}
	if spec, ok := proc.Resources[resource.TypeCPU]; ok && spec.Request != nil {
		cpu = *spec.Request
	}
	if mem > 0 || cpu > 0 || !reserves {
		return mem, cpu
	}
	if spec, ok := proc.Resources[resource.TypeMemory]; ok && spec.Limit != nil {
		mem = *spec.Limit
	}
	if spec, ok := proc.Resources[resource.TypeCPU]; ok && spec.Limit != nil {
		cpu = *spec.Limit
	}
	return mem, cpu
}

// Fits reports whether need fits next to used on a host with these totals.
// A zero total skips that dimension; both zero means the host was never
// read and a reserving job does not fit.
func Fits(totalMem uint64, totalCPU int64, usedMem, usedCPU, needMem, needCPU int64) bool {
	if needMem <= 0 && needCPU <= 0 {
		return true
	}
	if totalMem == 0 && totalCPU == 0 {
		return false
	}
	if totalMem > 0 && needMem > 0 && uint64(usedMem+needMem) > totalMem {
		return false
	}
	if totalCPU > 0 && needCPU > 0 && usedCPU+needCPU > totalCPU {
		return false
	}
	return true
}
