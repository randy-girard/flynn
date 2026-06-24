package deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	worker "github.com/flynn/flynn/controller/worker/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/inconshreveable/log15"
)

// sireniaClusterDeployReady reports whether an HA sirenia cluster has the
// expected async peer count and is safe to start a rolling deploy. Clusters
// with deposed peers but no asyncs may become ready once the primary
// auto-rejoins those peers.
func sireniaClusterDeployReady(s *state.State, processCount int) bool {
	if s == nil || s.Singleton {
		return true
	}
	return len(s.Async) > 0 && 2+len(s.Async) == processCount
}

func (d *DeployJob) waitForSireniaClusterReady(service, processType string, initial *state.State, events <-chan *discoverd.Event, stream interface{ Err() error }, log log15.Logger) (*state.State, error) {
	cluster := *initial
	if sireniaClusterDeployReady(&cluster, d.Processes[processType]) {
		return &cluster, nil
	}
	if len(cluster.Deposed) == 0 && len(cluster.Async) == 0 {
		return nil, errors.New("sirenia cluster in unhealthy state (has no asyncs)")
	}
	if len(cluster.Deposed) == 0 && 2+len(cluster.Async) != d.Processes[processType] {
		return nil, errors.New("sirenia cluster in unhealthy state (too few asyncs)")
	}

	waitBudget := 2 * time.Minute
	if d.timeout < waitBudget {
		waitBudget = d.timeout
	}
	deadline := time.Now().Add(waitBudget)
	log.Info("waiting for sirenia cluster to become ready for deploy",
		"asyncs", len(cluster.Async), "deposed", len(cluster.Deposed),
		"expected_processes", d.Processes[processType], "timeout", waitBudget)

	svc := discoverd.NewService(service)
	for time.Now().Before(deadline) {
		if sireniaClusterDeployReady(&cluster, d.Processes[processType]) {
			log.Info("sirenia cluster ready for deploy", "asyncs", len(cluster.Async))
			return &cluster, nil
		}
		select {
		case <-d.stop:
			return nil, worker.ErrStopped
		case event, ok := <-events:
			if !ok {
				return nil, fmt.Errorf("service event stream closed unexpectedly: %s", stream.Err())
			}
			if event.Kind == discoverd.EventKindServiceMeta && event.ServiceMeta != nil && len(event.ServiceMeta.Data) > 0 {
				var updated state.State
				if err := json.Unmarshal(event.ServiceMeta.Data, &updated); err == nil {
					cluster = updated
				}
			}
		case <-time.After(2 * time.Second):
			meta, err := svc.GetMeta()
			if err == nil && meta != nil && len(meta.Data) > 0 {
				var updated state.State
				if err := json.Unmarshal(meta.Data, &updated); err == nil {
					cluster = updated
				}
			}
		}
	}
	if len(cluster.Async) == 0 {
		return nil, errors.New("sirenia cluster in unhealthy state (has no asyncs)")
	}
	return nil, errors.New("sirenia cluster in unhealthy state (too few asyncs)")
}
