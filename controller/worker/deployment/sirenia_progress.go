package deployment

import (
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/worker/types"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
)

const sireniaProgressEvery = 10 * time.Second

// waitSireniaProgress runs fn while emitting a replication snapshot every
// 10s so flynn-host update is not silent during pg_basebackup / WAL catch-up.
func (d *DeployJob) waitSireniaProgress(log log15.Logger, processType, what string, addrs []string, fn func() error) error {
	tracker := &sireniaclient.CatchupTracker{}
	d.emitSireniaProgress(log, processType, what, addrs, tracker)
	done := make(chan error, 1)
	go func() { done <- fn() }()
	tick := time.NewTicker(sireniaProgressEvery)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-tick.C:
			d.emitSireniaProgress(log, processType, what, addrs, tracker)
		case <-d.stop:
			return worker.ErrStopped
		}
	}
}

func (d *DeployJob) emitSireniaProgress(log log15.Logger, processType, what string, addrs []string, tracker *sireniaclient.CatchupTracker) {
	msg := sireniaProgressMessage(what, addrs, tracker)
	log.Info("sirenia wait progress", "what", what, "status", msg)
	if d.deployEvents == nil {
		return
	}
	ev := ct.DeploymentEvent{
		ReleaseID: d.NewReleaseID,
		JobType:   processType,
		Progress:  msg,
	}
	select {
	case d.deployEvents <- ev:
	default:
	}
}

func sireniaProgressMessage(what string, addrs []string, tracker *sireniaclient.CatchupTracker) string {
	if len(addrs) == 2 && addrs[0] != "" && addrs[1] != "" {
		up, upErr := sireniaclient.NewClient(addrs[0]).Status()
		down, downErr := sireniaclient.NewClient(addrs[1]).Status()
		return what + ": " + tracker.FormatReplSync(up, down, upErr, downErr)
	}
	parts := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr == "" {
			continue
		}
		st, err := sireniaclient.NewClient(addr).Status()
		parts = append(parts, addr+": "+sireniaclient.FormatReplicationProgress(st, err))
	}
	if len(parts) == 0 {
		return what
	}
	return what + ": " + strings.Join(parts, "; ")
}
