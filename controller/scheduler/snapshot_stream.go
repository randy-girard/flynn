package main

import (
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/stream"
)

// snapshotStream is a controller event stream that first sends the current
// list, then an empty marker item, then live changes (formations, volumes).
type snapshotStream[T any] struct {
	// name is used in log messages ("formation", "volume").
	name string
	// connect opens the stream, resuming after since when it is set.
	connect func(since *time.Time, ch chan T) (stream.Stream, error)
	// isMarker reports the empty item that ends the current list.
	isMarker func(T) bool
	// updatedAt is the resume point an item moves the stream to.
	updatedAt func(T) *time.Time
	// initial handles an item of the current list before the main loop runs.
	initial func(T)
	// deliver hands a live item to the main loop.
	deliver func(T)
}

// followSnapshotStream keeps cfg's stream connected in the background and
// returns once the current list has arrived. Until then each connection has
// s.snapshotTimeout to deliver the marker before it is dropped and
// reconnected (the controller keeps a stream whose query hangs open with
// keepalives, so only a deadline notices it). Past s.snapshotGiveUp the
// scheduler exits: it is already registered in discoverd and must not stay
// there without the list, so another scheduler can lead. A stopped scheduler
// just returns.
func followSnapshotStream[T any](s *Scheduler, log log15.Logger, cfg snapshotStream[T]) {
	var events chan T
	var st stream.Stream
	var since *time.Time
	connect := func() (err error) {
		log.Info("connecting " + cfg.name + " event stream")
		events = make(chan T, eventBufferSize)
		st, err = cfg.connect(since, events)
		if err != nil {
			log.Error("error connecting "+cfg.name+" event stream", "err", err)
		}
		return
	}

	current := make(chan struct{})
	go func() {
		var isCurrent bool
		attempt := 0
		for {
			for {
				if err := connect(); err == nil {
					break
				}
				time.Sleep(formationReconnectWait(attempt))
				attempt++
			}
			var deadline <-chan time.Time
			var timer *time.Timer
			if !isCurrent {
				timer = time.NewTimer(s.snapshotTimeout)
				deadline = timer.C
			}
		recv:
			for {
				select {
				case item, ok := <-events:
					if !ok {
						break recv
					}
					if cfg.isMarker(item) {
						if !isCurrent {
							isCurrent = true
							close(current)
							deadline = nil
						}
						attempt = 0
						continue
					}
					if t := cfg.updatedAt(item); t != nil {
						since = t
					}
					// before the marker the item is part of the current
					// list, which must be applied before the main loop
					if !isCurrent {
						cfg.initial(item)
						continue
					}
					cfg.deliver(item)
				case <-deadline:
					deadline = nil
					log.Warn(cfg.name+" snapshot did not arrive in time, reconnecting", "timeout", s.snapshotTimeout)
					if st == nil {
						break recv
					}
					// closing the stream closes events, which ends this loop
					st.Close()
				}
			}
			if timer != nil {
				timer.Stop()
			}
			var err error
			if st != nil {
				err = st.Err()
			}
			log.Warn(cfg.name+" event stream disconnected", "err", err)
			// A stream that ends before the current marker must not be
			// retried immediately. That storm keeps the controller from
			// finishing the snapshot, so this process never registers.
			if !isCurrent {
				time.Sleep(formationReconnectWait(attempt))
				attempt++
			}
		}
	}()

	start := time.Now()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	giveUp := time.NewTimer(s.snapshotGiveUp)
	defer giveUp.Stop()
	for {
		select {
		case <-current:
			return
		case <-s.stop:
			// a stopped scheduler is leaving anyway; exiting the
			// process here would end whatever else runs in it
			return
		case <-tick.C:
			log.Warn("still waiting for current "+cfg.name+" list", "duration", time.Since(start))
		case <-giveUp.C:
			log.Crit("no current "+cfg.name+" list, exiting so another scheduler can take over", "duration", time.Since(start))
			s.exit()
			// exit may return in tests; keep waiting only if it did
			select {
			case <-current:
				return
			case <-time.After(time.Hour):
			}
		}
	}
}
