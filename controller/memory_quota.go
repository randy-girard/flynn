package main

import (
	"math"
	"strings"

	"github.com/docker/go-units"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/host/resource"
)

// memoryFormation is one formation counted toward an account memory quota.
type memoryFormation struct {
	Owner     string
	AppID     string
	ReleaseID string
	Processes map[string]int
	Tags      map[string]map[string]string
	Types     map[string]ct.ProcessType
}

// memoryHost is a host the scheduler might place an omni job on.
// Shutdown follows matchingOmniHostCount: a draining host does not count.
type memoryHost struct {
	ID       string
	Tags     map[string]string
	Shutdown bool
}

// scaleQuotaApplies is the gate in front of enforceScale. Platform apps have
// an empty OwnerAccount and stay unchecked.
func scaleQuotaApplies(c *controllerAPI, app *ct.App) bool {
	return c != nil && c.tenancy != nil && app != nil && app.OwnerAccount != ""
}

// overlayProcessCounts is the process map AddScaleRequest stores: request
// keys replace stored keys, and omitted keys stay.
func positiveProcessCount(counts map[string]int) int {
	n := 0
	for _, count := range counts {
		if count > 0 {
			n += count
		}
	}
	return n
}

// projectedAccountProcesses is Usage.Processes with this formation replaced
// by the overlay that will be stored. One usage total in, no second read.
func projectedAccountProcesses(usage int, oldCounts, newCounts map[string]int) int {
	merged := overlayProcessCounts(oldCounts, newCounts)
	procs := usage - positiveProcessCount(oldCounts) + positiveProcessCount(merged)
	if procs < 0 {
		return 0
	}
	return procs
}

func overlayProcessCounts(stored, req map[string]int) map[string]int {
	out := make(map[string]int, len(stored)+len(req))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range req {
		out[k] = v
	}
	return out
}

func overlayFormationTags(stored, req map[string]map[string]string, apply bool) map[string]map[string]string {
	out := make(map[string]map[string]string, len(stored)+len(req))
	for typ, tags := range stored {
		out[typ] = copyStringMap(tags)
	}
	if !apply {
		return out
	}
	for typ, tags := range req {
		out[typ] = copyStringMap(tags)
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// projectFormations replaces the formation being scaled. A result whose
// process counts sum to 0 is not runnable, matching formation_list_active.
func projectFormations(existing []memoryFormation, updated memoryFormation) []memoryFormation {
	out := make([]memoryFormation, 0, len(existing)+1)
	found := false
	for _, f := range existing {
		if f.AppID == updated.AppID && f.ReleaseID == updated.ReleaseID {
			found = true
			if formationRunnable(updated.Processes) {
				out = append(out, updated)
			}
			continue
		}
		if !formationRunnable(f.Processes) {
			continue
		}
		out = append(out, f)
	}
	if !found && formationRunnable(updated.Processes) {
		out = append(out, updated)
	}
	return out
}

func formationRunnable(counts map[string]int) bool {
	var sum int64
	for _, n := range counts {
		if n > 0 && sum > math.MaxInt64-int64(n) {
			return true
		}
		sum += int64(n)
	}
	return sum > 0
}

// accountMemoryMB is the scheduler memory for account, in MiB. It uses the
// same ceil as usageFromFormation. A nil, non-positive, or missing limit does
// not count as 0: that job has no memory.max and the scale is rejected.
func accountMemoryMB(account string, forms []memoryFormation, hosts []memoryHost, profiles map[string]map[string]string) (int64, error) {
	if account == "" {
		return 0, nil
	}
	var total int64
	for _, f := range forms {
		if f.Owner != account || !formationRunnable(f.Processes) {
			continue
		}
		part, err := formationMemoryMB(f, hosts, profiles)
		if err != nil {
			return 0, err
		}
		total, err = addMemoryMB(total, part)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func formationMemoryMB(f memoryFormation, hosts []memoryHost, profiles map[string]map[string]string) (int64, error) {
	var total int64
	for name, n := range f.Processes {
		if n <= 0 {
			continue
		}
		proc, ok := f.Types[name]
		if !ok {
			return 0, memorySpecError(name)
		}
		spec, ok := proc.Resources[resource.TypeMemory]
		if !ok || spec.Limit == nil || *spec.Limit <= 0 {
			return 0, memorySpecError(name)
		}
		perJob, err := ceilMemoryMB(*spec.Limit)
		if err != nil {
			return 0, err
		}
		jobs := int64(n)
		if proc.Omni {
			matched := matchingMemoryHosts(f, name, proc, hosts, profiles)
			jobs, err = mulMemory(int64(n), int64(matched))
			if err != nil {
				return 0, err
			}
		}
		part, err := mulMemory(jobs, perJob)
		if err != nil {
			return 0, err
		}
		total, err = addMemoryMB(total, part)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

// matchingMemoryHosts is matchingOmniHostCount for one process type:
// non-shutdown hosts whose tags match, including flynn-host-ids and the
// runtime profile tags the scheduler merges in.
func matchingMemoryHosts(f memoryFormation, typ string, proc ct.ProcessType, hosts []memoryHost, profiles map[string]map[string]string) int {
	var formTags map[string]string
	if f.Tags != nil {
		formTags = f.Tags[typ]
	}
	var runtime map[string]string
	if name := strings.ToLower(strings.TrimSpace(proc.RuntimeProfile)); name != "" && profiles != nil {
		runtime = profiles[name]
	}
	tags := ct.MergePlacementTags(formTags, runtime)
	n := 0
	for _, host := range hosts {
		if memoryHostMatches(tags, host) {
			n++
		}
	}
	return n
}

func memoryHostMatches(tags map[string]string, host memoryHost) bool {
	if host.Shutdown {
		return false
	}
	if !ct.HostIDsTagMatches(tags, host.ID) {
		return false
	}
	if !ct.AvoidHostIDsTagMatches(tags, host.ID) {
		return false
	}
	for k, v := range tags {
		if k == ct.FormationHostIDsTag || k == ct.FormationAvoidHostIDsTag || k == ct.FormationMaxProcessesTag || k == ct.FormationMaxProcessesSourceTag || k == ct.FormationMaxPersistentSingletonsTag {
			continue
		}
		if host.Tags[k] != v {
			return false
		}
	}
	return true
}

func ceilMemoryMB(bytes int64) (int64, error) {
	if bytes <= 0 {
		return 0, memorySpecError("memory")
	}
	const mib = int64(1024 * 1024)
	if bytes > math.MaxInt64-(mib-1) {
		return 0, memoryOverflowError()
	}
	return (bytes + mib - 1) / mib, nil
}

func mulMemory(a, b int64) (int64, error) {
	if a <= 0 || b <= 0 {
		return 0, nil
	}
	if a > math.MaxInt64/b {
		return 0, memoryOverflowError()
	}
	return a * b, nil
}

func addMemoryMB(a, b int64) (int64, error) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, memoryOverflowError()
	}
	return a + b, nil
}

func memorySpecError(proc string) error {
	return ct.ValidationError{Field: "processes." + proc, Message: "memory limit must be greater than zero"}
}

func memoryOverflowError() error {
	return ct.ValidationError{Field: "max_memory_mb", Message: "process memory total overflows"}
}

// memoryAllows is the max_memory_mb comparison quotaAllows runs. Equal to
// the limit is allowed. A nil limit is unlimited. Zero used is not a failure.
func memoryAllows(limit *int, used int64) error {
	if used == 0 {
		return nil
	}
	if used < 0 || used > int64(math.MaxInt) {
		return memoryOverflowError()
	}
	if tenancy.Exceeds(limit, int(used)) {
		return ct.ValidationError{Field: "max_memory_mb", Message: "account quota exceeded"}
	}
	return nil
}

func memoryQuotaArg(mem int64) (int, error) {
	if mem < 0 || mem > int64(math.MaxInt) {
		return 0, memoryOverflowError()
	}
	return int(mem), nil
}

// oneOffMemoryMB is the memory a RunJob or startDetachedJob will actually
// cap. SetDefaults fills a nil limit with 1 GiB. Build jobs then replace
// that with 12 GiB when the host can write the build cgroup, and are
// unlimited when it cannot.
func oneOffMemoryMB(appName, procType string, resources resource.Resources, buildLimits bool) (int64, error) {
	if appName == "builder" || procType == "slugbuilder" || procType == "dockerbuilder" {
		if !buildLimits {
			return 0, ct.ValidationError{Field: "resources.memory", Message: "build job has no memory limit on this host"}
		}
		return ceilMemoryMB(12 * units.GiB)
	}
	copied := resources
	if copied != nil {
		copied = resource.Resources{}
		for k, v := range resources {
			copied[k] = v
		}
	}
	resource.SetDefaults(&copied)
	spec := copied[resource.TypeMemory]
	if spec.Limit == nil || *spec.Limit <= 0 {
		return 0, ct.ValidationError{Field: "resources.memory", Message: "memory limit must be greater than zero"}
	}
	return ceilMemoryMB(*spec.Limit)
}

// buildJobMemoryAvailable reports whether a build one-off gets the 12 GiB
// cap. The host probe is not on the host API. The unit installed by
// host/img/packages.sh sets Delegate=yes, which is the condition that probe
// succeeds under, so a nil override counts 12 GiB. An explicit false matches
// a host that would leave memory.max at 0.
func (c *controllerAPI) buildJobMemoryAvailable() bool {
	if c != nil && c.buildJobMemoryLimits != nil {
		return *c.buildJobMemoryLimits
	}
	return true
}

func (c *controllerAPI) projectedScaleMemory(app *ct.App, release *ct.Release, newCounts map[string]int, newTags map[string]map[string]string, applyTags bool) (int64, error) {
	if app == nil || release == nil {
		return 0, ct.ValidationError{Field: "release", Message: "release is required"}
	}
	forms, err := c.accountFormations(app.OwnerAccount)
	if err != nil {
		return 0, err
	}
	var storedProcs map[string]int
	var storedTags map[string]map[string]string
	types := release.Processes
	found := false
	for _, f := range forms {
		if f.AppID == app.ID && f.ReleaseID == release.ID {
			found = true
			storedProcs = f.Processes
			storedTags = f.Tags
			if types == nil {
				types = f.Types
			}
			break
		}
	}
	if !found && c.formationRepo != nil {
		f, err := c.formationRepo.Get(app.ID, release.ID)
		if err != nil && err != data.ErrNotFound {
			return 0, err
		}
		if err == nil && f != nil {
			storedProcs = f.Processes
			storedTags = f.Tags
		}
	}
	updated := memoryFormation{
		Owner:     app.OwnerAccount,
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: overlayProcessCounts(storedProcs, newCounts),
		Tags:      overlayFormationTags(storedTags, newTags, applyTags),
		Types:     types,
	}
	forms = projectFormations(forms, updated)
	hosts, err := c.memoryHosts(forms)
	if err != nil {
		return 0, err
	}
	profiles, err := c.memoryProfiles(forms)
	if err != nil {
		return 0, err
	}
	return accountMemoryMB(app.OwnerAccount, forms, hosts, profiles)
}

func (c *controllerAPI) accountFormations(account string) ([]memoryFormation, error) {
	if c == nil || c.tenancy == nil {
		return nil, nil
	}
	rows, err := c.tenancy.RunnableFormations(account)
	if err != nil {
		return nil, err
	}
	out := make([]memoryFormation, 0, len(rows))
	for _, row := range rows {
		out = append(out, memoryFormation{
			Owner:     row.Owner,
			AppID:     row.AppID,
			ReleaseID: row.ReleaseID,
			Processes: row.Processes,
			Tags:      row.Tags,
			Types:     row.Types,
		})
	}
	return out, nil
}

func (c *controllerAPI) enforceOneOffMemory(app *ct.App, procType string, resources resource.Resources) error {
	if !scaleQuotaApplies(c, app) {
		return nil
	}
	forms, err := c.accountFormations(app.OwnerAccount)
	if err != nil {
		return err
	}
	hosts, err := c.memoryHosts(forms)
	if err != nil {
		return err
	}
	profiles, err := c.memoryProfiles(forms)
	if err != nil {
		return err
	}
	current, err := accountMemoryMB(app.OwnerAccount, forms, hosts, profiles)
	if err != nil {
		return err
	}
	extra, err := oneOffMemoryMB(app.Name, procType, resources, c.buildJobMemoryAvailable())
	if err != nil {
		return err
	}
	total, err := addMemoryMB(current, extra)
	if err != nil {
		return err
	}
	arg, err := memoryQuotaArg(total)
	if err != nil {
		return err
	}
	return c.quotaAllows(app.OwnerAccount, 0, 0, arg, 0, 0)
}

func formsNeedOmniHosts(forms []memoryFormation) bool {
	for _, f := range forms {
		for name, n := range f.Processes {
			if n > 0 && f.Types[name].Omni {
				return true
			}
		}
	}
	return false
}

func formsNeedProfiles(forms []memoryFormation) bool {
	for _, f := range forms {
		for name, n := range f.Processes {
			if n <= 0 || !f.Types[name].Omni {
				continue
			}
			if strings.TrimSpace(f.Types[name].RuntimeProfile) != "" {
				return true
			}
		}
	}
	return false
}

func (c *controllerAPI) memoryHosts(forms []memoryFormation) ([]memoryHost, error) {
	if !formsNeedOmniHosts(forms) {
		return nil, nil
	}
	if c == nil || c.clusterClient == nil {
		return nil, ct.ValidationError{Field: "processes", Message: "cluster hosts are unavailable"}
	}
	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		return nil, err
	}
	out := make([]memoryHost, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		out = append(out, memoryHostFromClient(h))
	}
	return out, nil
}

// memoryHostFromClient cannot see discoverd's shutdown meta. Hosts() still
// returns a host after it is marked shutdown and before the down event, so
// this can count one extra host during that drain. The scheduler's in-memory
// Shutdown flag is not on the cluster client.
func memoryHostFromClient(h utils.HostClient) memoryHost {
	return memoryHost{ID: h.ID(), Tags: h.Tags()}
}

func (c *controllerAPI) memoryProfiles(forms []memoryFormation) (map[string]map[string]string, error) {
	if !formsNeedProfiles(forms) {
		return nil, nil
	}
	if c == nil || c.runtimeProfileRepo == nil {
		return nil, ct.ValidationError{Field: "processes", Message: "runtime profiles are unavailable"}
	}
	list, err := c.runtimeProfileRepo.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(list))
	for _, p := range list {
		if p == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(p.Name))
		if name == "" || len(p.Tags) == 0 {
			continue
		}
		out[name] = copyStringMap(p.Tags)
	}
	return out, nil
}
