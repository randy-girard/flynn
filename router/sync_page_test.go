package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/randy-girard/flynn/pkg/stream"
	router "github.com/randy-girard/flynn/router/types"
)

// treeHandler records Set order and applies the HTTP domain-tree rule:
// a sub-path does not enter the tree until its root exists.
type treeHandler struct {
	current map[string]struct{}
	roots   map[string]bool
	sets    []string
	removes []string
	missed  []string
	inTree  map[string]struct{}
}

func newTreeHandler(current ...string) *treeHandler {
	h := &treeHandler{
		current: map[string]struct{}{},
		roots:   map[string]bool{},
		inTree:  map[string]struct{}{},
	}
	for _, id := range current {
		h.current[id] = struct{}{}
	}
	return h
}

func (h *treeHandler) Current() map[string]struct{} {
	out := make(map[string]struct{}, len(h.current))
	for id := range h.current {
		out[id] = struct{}{}
	}
	return out
}

func (h *treeHandler) Set(route *router.Route) error {
	h.sets = append(h.sets, route.ID)
	path := route.Path
	if path == "" {
		path = "/"
	}
	if path != "/" && !h.roots[route.Domain] {
		h.missed = append(h.missed, route.ID)
		return nil
	}
	if path == "/" {
		h.roots[route.Domain] = true
	}
	h.inTree[route.ID] = struct{}{}
	return nil
}

func (h *treeHandler) Remove(id string) error {
	h.removes = append(h.removes, id)
	delete(h.inTree, id)
	return nil
}

type scriptedStore struct {
	routes          []*router.Route
	err             error
	extra           *router.Event
	ch              chan *router.Event
	stream          *stream.Basic
	closeDuringList bool
}

func (s *scriptedStore) List() ([]*router.Route, error) {
	if s.extra != nil && s.ch != nil {
		select {
		case s.ch <- s.extra:
		case <-time.After(2 * time.Second):
			return s.routes, errors.New("watch blocked during list")
		}
	}
	if s.closeDuringList && s.ch != nil {
		close(s.ch)
		s.ch = nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.routes, nil
}

func (s *scriptedStore) Watch(ch chan *router.Event) (stream.Stream, error) {
	s.ch = ch
	s.stream = stream.New()
	return s.stream, nil
}

func TestInitialSyncSortsRootBeforeSubpath(t *testing.T) {
	sub := &router.Route{ID: "sub", Type: "http", Domain: "example.com", Path: "/api"}
	root := &router.Route{ID: "root", Type: "http", Domain: "example.com", Path: "/"}
	store := &scriptedStore{routes: []*router.Route{sub, root}}
	h := newTreeHandler()
	start := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncer := NewSyncer(store, "http")
	go func() { done <- syncer.Sync(ctx, h, start) }()
	select {
	case <-start:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("sync did not finish the snapshot")
	}
	if len(h.missed) != 0 {
		t.Fatalf("sub-path missed the tree: %v sets=%v", h.missed, h.sets)
	}
	if _, ok := h.inTree["root"]; !ok {
		t.Fatal("root missing from tree")
	}
	if _, ok := h.inTree["sub"]; !ok {
		t.Fatalf("sub-path missing from tree, sets=%v missed=%v", h.sets, h.missed)
	}
	cancel()
	<-done
}

func TestInitialSyncSetsDuplicateIDOnce(t *testing.T) {
	route := &router.Route{ID: "dup", Type: "tcp", Port: 5000}
	store := &scriptedStore{routes: []*router.Route{route, route}}
	h := newTreeHandler()
	start := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- NewSyncer(store, "tcp").Sync(ctx, h, start) }()
	select {
	case <-start:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("sync did not finish the snapshot")
	}
	if len(h.sets) != 1 {
		t.Fatalf("Set calls = %v, want one listener", h.sets)
	}
	cancel()
	<-done
}

func TestInitialSyncKeepsCreateThatArrivesDuringList(t *testing.T) {
	root := &router.Route{ID: "root", Type: "http", Domain: "example.com", Path: "/"}
	created := &router.Route{ID: "late", Type: "http", Domain: "example.com", Path: "/api"}
	store := &scriptedStore{
		routes: []*router.Route{root},
		extra:  &router.Event{Event: router.EventTypeRouteSet, ID: created.ID, Route: created},
	}
	h := newTreeHandler("stale")
	start := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- NewSyncer(store, "http").Sync(ctx, h, start) }()
	select {
	case <-start:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("sync did not finish the snapshot")
	}
	if _, ok := h.inTree["late"]; !ok {
		t.Fatalf("create during list was not applied, sets=%v", h.sets)
	}
	if _, ok := h.inTree["root"]; !ok {
		t.Fatal("root missing")
	}
	removed := false
	for _, id := range h.removes {
		if id == "late" {
			t.Fatal("create during list was removed")
		}
		if id == "stale" {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("stale route was not removed: %v", h.removes)
	}
	cancel()
	<-done
}

func TestInitialSyncDoesNotRemoveWhenListFails(t *testing.T) {
	store := &scriptedStore{err: errors.New("page 2 failed")}
	h := newTreeHandler("keep")
	start := make(chan struct{})
	err := NewSyncer(store, "http").Sync(context.Background(), h, start)
	if err == nil {
		t.Fatal("expected list error")
	}
	if len(h.removes) != 0 || len(h.sets) != 0 {
		t.Fatalf("sets=%v removes=%v", h.sets, h.removes)
	}
	select {
	case <-start:
		t.Fatal("start closed after a failed list")
	default:
	}
}

func TestInitialSyncDoesNotRemoveWhenWatchClosesDuringList(t *testing.T) {
	store := &scriptedStore{
		routes:          []*router.Route{{ID: "a", Type: "http", Domain: "example.com", Path: "/"}},
		closeDuringList: true,
	}
	h := newTreeHandler("keep")
	start := make(chan struct{})
	err := NewSyncer(store, "http").Sync(context.Background(), h, start)
	if err == nil {
		t.Fatal("expected watch error")
	}
	if len(h.removes) != 0 {
		t.Fatalf("removed %v after the watch closed", h.removes)
	}
}
