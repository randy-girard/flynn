package main

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"sync"

	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

// errRouteWatchClosed is returned when the watch ends while the initial
// snapshot is still being read. Remove must not run on that partial view.
var errRouteWatchClosed = errors.New("router: route watch closed during initial sync")

var ErrNotFound = errors.New("router: route not found")

type SyncHandler interface {
	Set(route *router.Route) error
	Remove(id string) error
	Current() map[string]struct{}
}

func NewSyncer(store Store, routeType string) *Syncer {
	return &Syncer{store, routeType}
}

type Syncer struct {
	store     Store
	routeType string
}

func (s *Syncer) Sync(ctx context.Context, h SyncHandler, startc chan<- struct{}) error {
	events := make(chan *router.Event)
	stream, err := s.store.Watch(events)
	if err != nil {
		return err
	}
	defer stream.Close()

	// List walks every page. The watch send is unbuffered, so drain it for
	// the whole sync or the first event blocks the page walk.
	stop := make(chan struct{})
	defer close(stop)
	queued := newRouteEventQueue()
	go queued.run(events, stream.Err, stop)

	initialRoutes, listErr := s.store.List()
	pending, watchErr := queued.take()
	if listErr != nil {
		return listErr
	}
	if watchErr != nil {
		return watchErr
	}
	initialRoutes = mergeInitialRoutes(initialRoutes, pending, s.routeType)
	s.sortInitialRoutes(initialRoutes)

	seen := map[string]struct{}{}
	toRemove := h.Current()
	for _, route := range initialRoutes {
		if route == nil || route.Type != s.routeType || route.ID == "" {
			continue
		}
		if _, dup := seen[route.ID]; dup {
			continue
		}
		seen[route.ID] = struct{}{}
		delete(toRemove, route.ID)
		if err := h.Set(route); err != nil {
			return err
		}
	}
	// Remove only ids missing from the complete union, and only after List
	// and the watch both succeeded.
	for id := range toRemove {
		if err := h.Remove(id); err != nil {
			return err
		}
	}
	close(startc)

	for {
		if ctx.Err() != nil {
			return nil
		}
		event, err := queued.next(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if event == nil {
			return err
		}
		if event.Route == nil || event.Route.Type != s.routeType {
			continue
		}
		if err := s.handleUpdate(h, event); err != nil {
			return err
		}
	}
}

func (s *Syncer) handleUpdate(h SyncHandler, event *router.Event) error {
	var err error
	switch event.Event {
	case router.EventTypeRouteSet:
		err = h.Set(event.Route)
	case router.EventTypeRouteRemove:
		err = h.Remove(event.Route.ID)
		// ignore non-existent routes
		if err == ErrNotFound {
			err = nil
		}
	default:
		err = fmt.Errorf("unknown event type: %v", event.Event)
	}
	return err
}

// sortInitialRoutes sorts the given initial routes by domain and path so that
// we process root-path routes before sub-path routes (otherwise we'd get a
// consistency violation adding a sub-path when the root-path doesn't yet
// exist)
func (s *Syncer) sortInitialRoutes(routes []*router.Route) {
	sort.Sort(sortInitialRoutes(routes))
}

type sortInitialRoutes []*router.Route

func (r sortInitialRoutes) Len() int { return len(r) }
func (r sortInitialRoutes) Less(i, j int) bool {
	return path.Join(r[i].Domain, r[i].Path) < path.Join(r[j].Domain, r[j].Path)
}
func (r sortInitialRoutes) Swap(i, j int) { r[i], r[j] = r[j], r[i] }

// mergeInitialRoutes folds events observed while List was walking pages into
// the snapshot. A set replaces the route. A remove drops it. Each id is kept
// once so TCP Set does not listen twice.
func mergeInitialRoutes(list []*router.Route, events []*router.Event, routeType string) []*router.Route {
	type slot struct {
		route *router.Route
		dead  bool
	}
	order := make([]string, 0, len(list))
	byID := make(map[string]*slot, len(list))
	add := func(route *router.Route) {
		if route == nil || route.ID == "" || route.Type != routeType {
			return
		}
		s, ok := byID[route.ID]
		if !ok {
			s = &slot{}
			byID[route.ID] = s
			order = append(order, route.ID)
		}
		s.route = route
		s.dead = false
	}
	for _, route := range list {
		add(route)
	}
	for _, event := range events {
		if event == nil || event.Route == nil || event.Route.Type != routeType {
			continue
		}
		switch event.Event {
		case router.EventTypeRouteRemove:
			if s, ok := byID[event.Route.ID]; ok {
				s.dead = true
			}
		case router.EventTypeRouteSet:
			add(event.Route)
		}
	}
	out := make([]*router.Route, 0, len(order))
	for _, id := range order {
		s := byID[id]
		if s == nil || s.dead || s.route == nil {
			continue
		}
		out = append(out, s.route)
	}
	return out
}

type routeSnap struct {
	events []*router.Event
	err    error
	closed bool
}

type routeEventQueue struct {
	mu      sync.Mutex
	events  []*router.Event
	err     error
	closed  bool
	notify  chan struct{}
	snapReq chan chan routeSnap
}

func newRouteEventQueue() *routeEventQueue {
	return &routeEventQueue{
		notify:  make(chan struct{}, 1),
		snapReq: make(chan chan routeSnap),
	}
}

func (q *routeEventQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *routeEventQueue) push(event *router.Event) {
	q.mu.Lock()
	q.events = append(q.events, event)
	q.mu.Unlock()
	q.signal()
}

func (q *routeEventQueue) fail(err error) {
	q.mu.Lock()
	q.closed = true
	if err != nil {
		q.err = err
	}
	q.mu.Unlock()
	q.signal()
}

// run is the only reader of the watch. A snapshot request drains a closed
// channel on this goroutine, so a watch that ended during List is visible
// before Remove runs.
func (q *routeEventQueue) run(events <-chan *router.Event, streamErr func() error, stop <-chan struct{}) {
	for {
		q.mu.Lock()
		closed := q.closed
		q.mu.Unlock()
		if closed {
			select {
			case <-stop:
				return
			case reply := <-q.snapReq:
				q.replySnap(reply)
			}
			continue
		}
		select {
		case <-stop:
			return
		case reply := <-q.snapReq:
			q.drainReady(events, streamErr)
			q.replySnap(reply)
		case event, ok := <-events:
			if !ok {
				q.fail(streamErr())
				continue
			}
			q.push(event)
		}
	}
}

func (q *routeEventQueue) drainReady(events <-chan *router.Event, streamErr func() error) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				q.fail(streamErr())
				return
			}
			q.push(event)
		default:
			return
		}
	}
}

func (q *routeEventQueue) replySnap(reply chan routeSnap) {
	q.mu.Lock()
	snap := routeSnap{events: q.events, err: q.err, closed: q.closed}
	q.events = nil
	q.mu.Unlock()
	reply <- snap
}

// take returns events buffered so far. A watch that closed during List is an
// error even when the stream has no error, so Remove is not run.
func (q *routeEventQueue) take() ([]*router.Event, error) {
	reply := make(chan routeSnap)
	q.snapReq <- reply
	snap := <-reply
	if snap.closed {
		if snap.err != nil {
			return nil, snap.err
		}
		return nil, errRouteWatchClosed
	}
	return snap.events, nil
}

func (q *routeEventQueue) next(ctx context.Context) (*router.Event, error) {
	for {
		q.mu.Lock()
		if len(q.events) > 0 {
			event := q.events[0]
			q.events = q.events[1:]
			q.mu.Unlock()
			return event, nil
		}
		closed := q.closed
		err := q.err
		q.mu.Unlock()
		if closed {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-q.notify:
		}
	}
}
