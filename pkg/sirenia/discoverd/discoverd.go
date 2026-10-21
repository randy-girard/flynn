package discoverd

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// peerResyncInterval is how often a sirenia peer re-reads the discoverd
// instance list. A watch can miss an Update after a service recreate and
// then believe a live sync or primary is gone. The resync puts that peer
// back into the local view so the state machine does not promote an
// unassigned replica in its place.
var peerResyncInterval = 5 * time.Second

type Discoverd struct {
	service       discoverd.Service
	events        chan *state.DiscoverdEvent
	runOnce       sync.Once
	log           log15.Logger
	metaGoneNoted bool
}

func NewDiscoverd(s discoverd.Service, log log15.Logger) state.Discoverd {
	return &Discoverd{
		service: s,
		events:  make(chan *state.DiscoverdEvent),
		log:     log,
	}
}

func (d *Discoverd) SetState(state *state.DiscoverdState) error {
	data, err := json.Marshal(state.State)
	if err != nil {
		return err
	}
	meta := &discoverd.ServiceMeta{Index: state.Index, Data: data}
	if state.State.Primary != nil {
		meta.LeaderID = state.State.Primary.ID
	}
	if err := d.service.SetMeta(meta); err != nil {
		return err
	}
	state.Index = meta.Index
	return nil
}

func (d *Discoverd) receiveEvents() {
	log := d.log.New("fn", "receiveEvents")
	log.Info("starting event handler")

	var peers instSlice
	addPeer := func(add *discoverd.Instance) {
		for i, inst := range peers {
			if inst.ID == add.ID {
				peers[i] = add
				return
			}
		}
		peers = append(peers, add)
		sort.Sort(peers)
	}
	delPeer := func(del *discoverd.Instance) {
		for i, inst := range peers {
			if inst.ID == del.ID {
				peers = append(peers[:i], peers[i+1:]...)
				return
			}
		}
	}

	dstate := &state.DiscoverdState{}
	initialized := false
	var sentIndex uint64
	var sentPeers instSlice
	maybeEvent := func(current bool) {
		if !current {
			return
		}
		if !initialized {
			sentPeers = make(instSlice, len(peers))
			copy(sentPeers, peers)
			sentIndex = dstate.Index
			initialized = true
			d.events <- &state.DiscoverdEvent{
				Kind:  state.DiscoverdEventInit,
				Peers: sentPeers,
				State: dstate,
			}
			return
		}

		if dstate.Index > sentIndex {
			sentIndex = dstate.Index
			d.events <- &state.DiscoverdEvent{
				Kind:  state.DiscoverdEventState,
				State: dstate,
			}
		}
		if !sentPeers.Equal(peers) {
			sentPeers = make(instSlice, len(peers))
			copy(sentPeers, peers)
			d.events <- &state.DiscoverdEvent{
				Kind:  state.DiscoverdEventPeers,
				Peers: sentPeers,
			}
		}
	}

	backoff := false
	resync := time.NewTicker(peerResyncInterval)
	defer resync.Stop()
	for {
		// Each watch is a new snapshot. Keeping peers from the previous
		// stream leaves ghosts after a missed Down, or drops a peer whose
		// re-register arrived as Update and was never published.
		current := false
		peers = nil
		events := make(chan *discoverd.Event)
		stream, err := d.service.Watch(events)
		if err != nil {
			log.Error("error watching service", "at", "watch", "err", err)
			if backoff {
				time.Sleep(time.Second)
			}
			backoff = true
			continue
		}
		backoff = false

	watchLoop:
		for {
			select {
			case <-resync.C:
				if !current {
					continue
				}
				listed, err := d.service.Instances()
				if err != nil || len(listed) == 0 {
					if err != nil {
						log.Warn("peer resync failed", "err", err)
					}
					continue
				}
				next, changed := reconcileWatchPeers(peers, listed)
				if changed {
					log.Info("resyncing discoverd peers", "have", len(peers), "listed", len(listed))
					peers = next
					maybeEvent(true)
				}
				d.noteMissingMeta(true)
			case e, ok := <-events:
				if !ok {
					break watchLoop
				}
				switch e.Kind {
				case discoverd.EventKindUp:
					addPeer(e.Instance)
					maybeEvent(current)
				case discoverd.EventKindServiceMeta:
					if e.ServiceMeta.Index <= dstate.Index {
						continue
					}
					dstate = &state.DiscoverdState{
						Index: e.ServiceMeta.Index,
					}
					if len(e.ServiceMeta.Data) > 0 {
						var s *state.State
						if err := json.Unmarshal(e.ServiceMeta.Data, &s); err != nil {
							log.Error("error unmarshalling service meta into state", "at", "unmarshal_state", "err", err)
						} else {
							dstate.State = s
						}
					}
					maybeEvent(current)
				case discoverd.EventKindCurrent:
					current = true
					maybeEvent(current)
					d.noteMissingMeta(true)
				case discoverd.EventKindUpdate:
					// Heartbeats re-register an existing instance as Update.
					// Publishing that is what puts a live sync back into the
					// state machine after a partial watch snapshot.
					addPeer(e.Instance)
					maybeEvent(current)
				case discoverd.EventKindDown:
					delPeer(e.Instance)
					maybeEvent(current)
				}
			}
		}
		log.Error("disconnected from service watch", "at", "watch_disconnect", "err", stream.Err())
		if backoff {
			time.Sleep(time.Second)
		}
		backoff = true
	}
}

// metaAbsent reports that discoverd has no cluster state to restore from.
// A transport error is not absence: the primary must not republish over a
// service it could not read.
func metaAbsent(err error, meta *discoverd.ServiceMeta) bool {
	if discoverd.IsNotFound(err) {
		return true
	}
	if err != nil || meta == nil {
		return false
	}
	return len(meta.Data) == 0
}

func (d *Discoverd) noteMissingMeta(current bool) {
	if !current {
		return
	}
	meta, err := d.service.GetMeta()
	if !metaAbsent(err, meta) {
		d.metaGoneNoted = false
		return
	}
	if !d.metaGoneNoted {
		d.log.Info("discoverd cluster meta is missing; the primary will republish it")
		d.metaGoneNoted = true
	}
	d.events <- &state.DiscoverdEvent{Kind: state.DiscoverdEventMetaGone}
}

// reconcileWatchPeers returns the live discoverd list when it differs from
// the watch's in-memory set. An empty list is ignored so a brief discoverd
// blip cannot wipe membership and start a takeover.
func reconcileWatchPeers(have instSlice, listed []*discoverd.Instance) (instSlice, bool) {
	if len(listed) == 0 {
		return have, false
	}
	next := make(instSlice, len(listed))
	copy(next, listed)
	sort.Sort(next)
	if have.Equal(next) {
		return have, false
	}
	return next, true
}

func (d *Discoverd) Events() <-chan *state.DiscoverdEvent {
	d.runOnce.Do(func() { go d.receiveEvents() })
	return d.events
}

type instSlice []*discoverd.Instance

func (p instSlice) Len() int           { return len(p) }
func (p instSlice) Less(i, j int) bool { return p[i].Index < p[j].Index }
func (p instSlice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

func (x instSlice) Equal(y instSlice) bool {
	if len(x) != len(y) {
		return false
	}
	for i, xx := range x {
		if !sameWatchPeer(xx, y[i]) {
			return false
		}
	}
	return true
}

func sameWatchPeer(a, b *discoverd.Instance) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != b.ID || a.Addr != b.Addr || len(a.Meta) != len(b.Meta) {
		return false
	}
	for k, v := range a.Meta {
		if b.Meta[k] != v {
			return false
		}
	}
	return true
}
