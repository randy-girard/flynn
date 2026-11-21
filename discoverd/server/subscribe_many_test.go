package server_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/discoverd/server"
)

// manyInstances is more than StreamBufferSize, the size of the channel the
// HTTP handler gives the store (CORE-23).
const manyInstances = 100

func mustAddInstances(t *testing.T, s *Store, service string, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		inst := &discoverd.Instance{ID: fmt.Sprintf("inst%d", i), Addr: fmt.Sprintf("10.0.%d.%d:80", i/250, i%250+1), Proto: "tcp"}
		if err := s.AddInstance(service, inst); err != nil {
			// Error, not Fatal: callers may run this off the test goroutine.
			t.Error(err)
			return
		}
	}
}

// within runs fn and fails the test if it does not return in d.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s (store wedged)", what, d)
	}
}

// A stream on a service with more instances than the stream buffer must
// deliver every instance plus `current`, and must not hold the store lock
// while it does so.
func TestStreamServiceWithMoreInstancesThanBuffer(t *testing.T) {
	if manyInstances <= server.StreamBufferSize {
		t.Fatalf("test needs more than %d instances", server.StreamBufferSize)
	}
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("svc", nil); err != nil {
		t.Fatal(err)
	}
	mustAddInstances(t, s, "svc", 0, manyInstances)

	h := server.NewHandler(false, []string{""})
	h.Store = s.Store
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/services/svc", nil)
	req.Header.Set("Accept", "text/event-stream")
	type result struct {
		ups     int
		current bool
		err     error
	}
	resCh := make(chan result, 1)
	extraUp := make(chan struct{}, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer res.Body.Close()
		var r result
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev discoverd.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				resCh <- result{err: err}
				return
			}
			switch ev.Kind {
			case discoverd.EventKindUp:
				if r.current {
					if ev.Instance.ID == "late" {
						extraUp <- struct{}{}
						return
					}
					continue
				}
				r.ups++
			case discoverd.EventKindCurrent:
				r.current = true
				resCh <- r
			}
		}
		resCh <- result{err: fmt.Errorf("stream ended: %v", sc.Err())}
	}()

	// A write on the same store must not wait for the stream.
	within(t, 2*time.Second, "AddInstance during stream setup", func() {
		if err := s.AddService("other", nil); err != nil {
			t.Error(err)
		}
	})

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.ups != manyInstances {
			t.Fatalf("expected %d up events before current, got %d", manyInstances, r.ups)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not deliver all instances and current within 2s")
	}

	// Live events still reach the stream after the snapshot.
	within(t, 2*time.Second, "AddInstance after stream", func() {
		if err := s.AddInstance("svc", &discoverd.Instance{ID: "late", Addr: "10.9.9.9:80", Proto: "tcp"}); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-extraUp:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not receive the live up event")
	}
}

// A subscriber that never reads must not block Subscribe, writes or other
// subscribers, however many instances the service has; it is dropped once it
// falls too far behind.
func TestStore_Subscribe_NonDrainingManyInstances(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("svc", nil); err != nil {
		t.Fatal(err)
	}
	mustAddInstances(t, s, "svc", 0, manyInstances)

	// Another subscriber that drains.
	other := make(chan *discoverd.Event, server.StreamBufferSize)
	otherSub := s.Subscribe("svc", false, discoverd.EventKindUp, other)
	defer otherSub.Close()
	got := make(chan string, 1000)
	go func() {
		for ev := range other {
			got <- ev.Instance.ID
		}
	}()

	stuck := make(chan *discoverd.Event, server.StreamBufferSize)
	var stuckSub interface{ Err() error }
	within(t, 2*time.Second, "Subscribe with a non-draining channel", func() {
		stuckSub = s.Subscribe("svc", true, discoverd.EventKindAll, stuck)
	})

	// Enough writes to overflow the stuck subscriber's headroom.
	const more = 3 * server.StreamBufferSize
	within(t, 10*time.Second, "AddInstance with a stuck subscriber", func() {
		mustAddInstances(t, s, "svc", manyInstances, more)
	})

	seen := 0
	deadline := time.After(5 * time.Second)
	for seen < more {
		select {
		case <-got:
			seen++
		case <-deadline:
			t.Fatalf("other subscriber received %d of %d events", seen, more)
		}
	}

	waitFor := time.Now().Add(5 * time.Second)
	for stuckSub.Err() != server.ErrSendBlocked {
		if time.Now().After(waitFor) {
			t.Fatalf("stuck subscriber was not dropped, err=%v", stuckSub.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A draining subscriber gets every current instance, then current, then live
// events, in order.
func TestStore_Subscribe_SendCurrentManyInstancesOrdered(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("svc", nil); err != nil {
		t.Fatal(err)
	}
	mustAddInstances(t, s, "svc", 0, manyInstances)

	ch := make(chan *discoverd.Event)
	var sub interface{ Close() error }
	within(t, 2*time.Second, "Subscribe", func() {
		sub = s.Subscribe("svc", true, discoverd.EventKindUp|discoverd.EventKindCurrent, ch)
	})
	defer sub.Close()
	addErr := make(chan error, 1)
	go func() {
		addErr <- s.AddInstance("svc", &discoverd.Instance{ID: fmt.Sprintf("inst%d", manyInstances), Addr: "10.9.9.8:80", Proto: "tcp"})
	}()
	defer func() {
		if err := <-addErr; err != nil {
			t.Error(err)
		}
	}()

	timeout := time.After(5 * time.Second)
	next := func() *discoverd.Event {
		select {
		case ev := <-ch:
			return ev
		case <-timeout:
			t.Fatal("timed out reading subscription")
		}
		return nil
	}
	for i := 0; i < manyInstances; i++ {
		ev := next()
		if ev.Kind != discoverd.EventKindUp || ev.Instance.ID != fmt.Sprintf("inst%d", i) {
			t.Fatalf("event %d: got %s %v", i, ev.Kind, ev.Instance)
		}
	}
	if ev := next(); ev.Kind != discoverd.EventKindCurrent {
		t.Fatalf("expected current, got %s", ev.Kind)
	}
	if ev := next(); ev.Kind != discoverd.EventKindUp || ev.Instance.ID != fmt.Sprintf("inst%d", manyInstances) {
		t.Fatalf("expected live up for inst%d, got %s %v", manyInstances, ev.Kind, ev.Instance)
	}
}
