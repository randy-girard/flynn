package updaterdeploy

import (
	"encoding/json"
	"fmt"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	sirenia "github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/inconshreveable/log15"
)

const deposedRejoinWaitTimeout = 60 * time.Second

// Overridable for tests.
var (
	deposedRejoinWait         = deposedRejoinWaitTimeout
	deposedRejoinPollInterval = 2 * time.Second
)

// RepairDeposedSireniaPeers clears discoverd-present peers from the sirenia
// Deposed list so the primary can re-add them as asyncs. Absent deposed peers
// are left alone (clearing them does nothing useful). This is intentionally
// narrow: the primary already re-adds present deposed peers via
// evalClusterState; this helper only helps when that path is stuck after an
// upgrade, and avoids racing SetMeta on every host restart.
func RepairDeposedSireniaPeers(log log15.Logger) {
	if log == nil {
		log = log15.New()
	}
	for _, svc := range SireniaApplianceServices() {
		if err := repairDeposedSireniaPeersForService(svc, log.New("service", svc)); err != nil {
			log.Warn("error repairing deposed sirenia peers", "service", svc, "err", err)
		}
	}
}

func repairDeposedSireniaPeersForService(name string, log log15.Logger) error {
	service := discoverdNewService(name)
	meta, err := service.GetMeta()
	if err != nil || meta == nil || len(meta.Data) == 0 {
		return nil
	}

	var state sirenia.State
	if err := json.Unmarshal(meta.Data, &state); err != nil {
		return fmt.Errorf("decode %s sirenia state: %w", name, err)
	}
	if len(state.Deposed) == 0 {
		return nil
	}

	instances, err := discoverdInstancesOrEmpty(service)
	if err != nil {
		return fmt.Errorf("list %s instances: %w", name, err)
	}
	present := presentPeerIdentities(instances)

	kept := make([]*discoverd.Instance, 0, len(state.Deposed))
	var clearedIDs []string
	for _, d := range state.Deposed {
		id := peerApplianceID(d)
		if id != "" && present[id] {
			clearedIDs = append(clearedIDs, id)
			continue
		}
		kept = append(kept, d)
	}
	if len(clearedIDs) == 0 {
		log.Debug("no present deposed peers to clear", "deposed_count", len(state.Deposed))
		return nil
	}

	log.Info("clearing present deposed peers from sirenia cluster",
		"cleared", clearedIDs, "kept", len(kept))
	state.Deposed = kept
	data, err := json.Marshal(&state)
	if err != nil {
		return fmt.Errorf("encode %s sirenia state: %w", name, err)
	}
	meta.Data = data
	if err := service.SetMeta(meta); err != nil {
		return fmt.Errorf("write %s sirenia state: %w", name, err)
	}

	return waitForClearedDeposedInAsync(service, clearedIDs, log)
}

func waitForClearedDeposedInAsync(service discoverdService, clearedIDs []string, log log15.Logger) error {
	if len(clearedIDs) == 0 {
		return nil
	}
	want := make(map[string]bool, len(clearedIDs))
	for _, id := range clearedIDs {
		want[id] = true
	}
	deadline := time.Now().Add(deposedRejoinWait)
	for time.Now().Before(deadline) {
		meta, err := service.GetMeta()
		if err == nil && meta != nil && len(meta.Data) > 0 {
			var state sirenia.State
			if err := json.Unmarshal(meta.Data, &state); err == nil {
				found := 0
				for _, a := range state.Async {
					if want[peerApplianceID(a)] {
						found++
					}
				}
				if found >= len(clearedIDs) {
					log.Info("cleared deposed peers reappeared as asyncs", "count", found)
					return nil
				}
			}
		}
		time.Sleep(deposedRejoinPollInterval)
	}
	log.Warn("timed out waiting for cleared deposed peers to rejoin as asyncs",
		"cleared", clearedIDs, "timeout", deposedRejoinWait)
	return nil
}

func presentPeerIdentities(instances []*discoverd.Instance) map[string]bool {
	present := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if id := peerApplianceID(inst); id != "" {
			present[id] = true
		}
	}
	return present
}

func peerApplianceID(inst *discoverd.Instance) string {
	if inst == nil || inst.Meta == nil {
		return ""
	}
	for _, key := range []string{"POSTGRES_ID", "MARIADB_ID", "MONGODB_ID"} {
		if v := inst.Meta[key]; v != "" {
			return v
		}
	}
	return ""
}
