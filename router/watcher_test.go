package main

import (
	"sync"
	"testing"
	"time"

	router "github.com/randy-girard/flynn/router/types"
)

func TestWatchManagerDeliversRouteSet(t *testing.T) {
	m := NewWatchManager()
	ch := make(chan *router.Event, 1)
	m.Watch(ch, false)
	m.Send(&router.Event{Event: router.EventTypeRouteSet, ID: "foo.bar"})
	select {
	case e := <-ch:
		if e.Event != router.EventTypeRouteSet || e.ID != "foo.bar" {
			t.Fatalf("got %#v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout exceeded waiting for set")
	}
	m.Unwatch(ch)
}

func TestWatchManagerSendDoesNotDeadlockUnwatch(t *testing.T) {
	m := NewWatchManager()
	blocked := make(chan *router.Event) // unbuffered, never received
	live := make(chan *router.Event, 1)
	m.Watch(blocked, false)
	m.Watch(live, false)

	sent := make(chan struct{})
	go func() {
		m.Send(&router.Event{Event: router.EventTypeRouteSet, ID: "foo.bar"})
		close(sent)
	}()

	select {
	case e := <-live:
		if e.ID != "foo.bar" {
			t.Fatalf("id %q", e.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live watcher did not receive set while another watcher was blocked")
	}

	unwatched := make(chan struct{})
	go func() {
		m.Unwatch(live)
		close(unwatched)
	}()
	select {
	case <-unwatched:
	case <-time.After(2 * time.Second):
		t.Fatal("Unwatch blocked while Send waited on another watcher")
	}

	m.Unwatch(blocked)
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after blocked watcher was unwatched")
	}
}

func TestWatchManagerConcurrentSendUnwatch(t *testing.T) {
	m := NewWatchManager()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := make(chan *router.Event, 4)
			m.Watch(ch, false)
			for j := 0; j < 8; j++ {
				m.Send(&router.Event{Event: router.EventTypeRouteSet, ID: "foo.bar"})
			}
			m.Unwatch(ch)
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Send/Unwatch did not finish")
	}
}
