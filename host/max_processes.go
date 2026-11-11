package main

import (
	"os"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/cli"
)

const autoMaxProcessesInterval = 30 * time.Second

func hostConfigPath() string {
	if p := os.Getenv("FLYNN_HOST_CONFIG"); p != "" {
		return p
	}
	return configFile
}

func readProcMemTotal() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return ct.ParseMemTotal(f)
}

func mergeTagMap(dst, delta map[string]string) map[string]string {
	out := make(map[string]string, len(dst)+len(delta))
	for k, v := range dst {
		if k != "" && v != "" {
			out[k] = v
		}
	}
	for k, v := range delta {
		if k == "" {
			continue
		}
		if v == "" {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

func tagUpdateTouchesCap(tags map[string]string) bool {
	if tags == nil {
		return false
	}
	_, cap := tags[ct.FormationMaxProcessesTag]
	_, source := tags[ct.FormationMaxProcessesSourceTag]
	_, singleton := tags[ct.FormationMaxPersistentSingletonsTag]
	return cap || source || singleton
}

// resolveStartupMaxProcesses turns an unresolved auto cap into a number from
// the builtin app runtimes before the host registers. A controller that is
// not up yet is not an error. The live catalog replaces this on the refresh
// loop.
func resolveStartupMaxProcesses(log log15.Logger, tags map[string]string) map[string]string {
	mem, err := readProcMemTotal()
	if err != nil {
		if log != nil {
			log.Error("reading host memory for process cap", "err", err)
		}
		return tags
	}
	next, ok := cli.ResolveAutoHostMaxProcesses(tags, mem, ct.DefaultOverlayContainerAddrs(), nil, false, cli.BuiltinAppMemoryLimits())
	if !ok {
		return tags
	}
	if err := cli.WriteDaemonTags(hostConfigPath(), next); err != nil && log != nil {
		log.Error("writing process cap to host config", "err", err)
	}
	return next
}

func (h *Host) maintainAutoMaxProcesses() {
	h.refreshAutoMaxProcesses()
	ticker := time.NewTicker(autoMaxProcessesInterval)
	defer ticker.Stop()
	for range ticker.C {
		h.refreshAutoMaxProcesses()
	}
}

func (h *Host) refreshAutoMaxProcesses() {
	if h == nil || h.status == nil {
		return
	}
	h.statusMtx.RLock()
	tags := mergeTagMap(h.status.Tags, nil)
	var subnet string
	if h.status.Network != nil {
		subnet = h.status.Network.Subnet
	}
	h.statusMtx.RUnlock()
	if tags[ct.FormationMaxProcessesSourceTag] != ct.HostMaxProcessesSourceAuto {
		return
	}
	mem, err := readProcMemTotal()
	if err != nil {
		if h.log != nil {
			h.log.Error("reading host memory for process cap", "err", err)
		}
		return
	}
	overlay := ct.DefaultOverlayContainerAddrs()
	if n, ok := ct.OverlayContainerAddrsFromCIDR(subnet); ok {
		overlay = n
	}
	var live []int64
	liveOK := false
	if client, err := cli.ClusterController(); err == nil {
		profiles, err1 := client.ListRuntimeProfiles()
		cat, err2 := client.ListDBRuntimes()
		if err1 == nil && err2 == nil {
			live = cli.RuntimeMemoryLimits(profiles, cat)
			liveOK = true
		}
	}
	next, ok := cli.ResolveAutoHostMaxProcesses(tags, mem, overlay, live, liveOK, cli.BuiltinAppMemoryLimits())
	if !ok {
		return
	}
	if err := h.UpdateTags(next); err != nil && h.log != nil {
		h.log.Error("updating automatic process cap", "err", err)
	}
}
