package main

import (
	"sync"

	"github.com/randy-girard/flynn/router/types"
)

type Watcher interface {
	Watch(ch chan *router.Event, sendCurrent bool)
	Unwatch(ch chan *router.Event)
}

func NewWatchManager() *WatchManager {
	return &WatchManager{
		watchers: make(map[chan *router.Event]*watchSub),
		backends: make(map[string]map[string]*router.Backend),
	}
}

type watchSub struct {
	ch   chan *router.Event
	done chan struct{}
}

type WatchManager struct {
	mtx      sync.RWMutex
	watchers map[chan *router.Event]*watchSub
	backends map[string]map[string]*router.Backend
}

func (m *WatchManager) Watch(ch chan *router.Event, sendCurrent bool) {
	var current []*router.Event
	sub := &watchSub{ch: ch, done: make(chan struct{})}
	m.mtx.Lock()
	if sendCurrent {
		for _, backends := range m.backends {
			for _, backend := range backends {
				current = append(current, &router.Event{
					Event:   router.EventTypeBackendUp,
					Backend: backend,
				})
			}
		}
	}
	m.watchers[ch] = sub
	m.mtx.Unlock()
	for _, ev := range current {
		sendWatchEvent(sub, ev)
	}
}

func (m *WatchManager) Unwatch(ch chan *router.Event) {
	m.mtx.Lock()
	sub := m.watchers[ch]
	delete(m.watchers, ch)
	m.mtx.Unlock()
	if sub == nil {
		return
	}
	close(sub.done)
	go func() {
		for {
			select {
			case <-ch:
			case <-sub.done:
				// done is already closed; drain leftover events then exit
				for {
					select {
					case <-ch:
					default:
						return
					}
				}
			}
		}
	}()
}

func (m *WatchManager) Send(event *router.Event) {
	m.mtx.Lock()
	var extra []*router.Event
	switch event.Event {
	case router.EventTypeBackendUp:
		if m.backends[event.Backend.Service] == nil {
			m.backends[event.Backend.Service] = make(map[string]*router.Backend)
		}
		m.backends[event.Backend.Service][event.Backend.JobID] = event.Backend
	case router.EventTypeBackendDown:
		if backends, ok := m.backends[event.Backend.Service]; ok {
			delete(backends, event.Backend.JobID)
			if len(backends) == 0 {
				m.backends[event.Backend.Service] = nil
			}
		}
	case router.EventTypeRouteRemove:
		if backends, ok := m.backends[event.Route.Service]; ok {
			for _, backend := range backends {
				extra = append(extra, &router.Event{
					Event:   router.EventTypeBackendDown,
					Backend: backend,
				})
			}
			delete(m.backends, event.Route.Service)
		}
	}
	subs := make([]*watchSub, 0, len(m.watchers))
	for _, sub := range m.watchers {
		subs = append(subs, sub)
	}
	m.mtx.Unlock()

	for _, sub := range subs {
		sub := sub
		go func() {
			for _, ev := range extra {
				sendWatchEvent(sub, ev)
			}
			sendWatchEvent(sub, event)
		}()
	}
}

func sendWatchEvent(sub *watchSub, event *router.Event) {
	select {
	case <-sub.done:
		return
	default:
	}
	select {
	case sub.ch <- event:
	case <-sub.done:
	}
}
