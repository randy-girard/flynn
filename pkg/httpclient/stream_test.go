package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/randy-girard/flynn/pkg/sse"
)

type idEvent struct {
	ID int64 `json:"id"`
}

// eventServer answers each connection with the function for that connection
// number, and records the Last-Event-Id every connection asked for.
type eventServer struct {
	mu    sync.Mutex
	asked []string
	conns []func(w http.ResponseWriter, r *http.Request)
}

func (s *eventServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := len(s.asked)
	s.asked = append(s.asked, r.Header.Get("Last-Event-Id"))
	s.mu.Unlock()
	if n >= len(s.conns) {
		// hold any later connection open with no event
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	s.conns[n](w, r)
}

func (s *eventServer) lastEventIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func sseOpen(w http.ResponseWriter, anchor string) {
	w.Header().Set("Content-Type", "text/event-stream")
	if anchor != "" {
		w.Header().Set(sse.LastEventIDHeader, anchor)
	}
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
}

func sseSend(w http.ResponseWriter, id int64) {
	fmt.Fprintf(w, "data: {\"id\":%d}\n\n", id)
	w.(http.Flusher).Flush()
}

func waitConnections(t *testing.T, s *eventServer, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ids := s.lastEventIDs(); len(ids) >= n {
			return ids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server saw %d connections, want %d", len(s.lastEventIDs()), n)
	return nil
}

// EDGE-94: a stream that drops before its first event used to reconnect with
// Last-Event-Id 0, and the controller replays nothing for 0, so every event
// in the gap was lost. The controller now tells the client where the stream
// starts; a reconnect before any event resumes from there.
func TestResumingStreamResumesFromAnchorWhenNoEventSeen(t *testing.T) {
	s := &eventServer{conns: []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) { sseOpen(w, "41") }, // drops with no event
	}}
	srv := httptest.NewServer(s)
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	ch := make(chan *idEvent)
	stream, err := c.ResumingStream("GET", "/events", ch)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ids := waitConnections(t, s, 2)
	if ids[0] != "0" || ids[1] != "41" {
		t.Fatalf("Last-Event-Id per connection = %q, want [0 41]", ids)
	}
}

// Once an event was seen its id is the cursor, whatever a later connection
// reports as its start.
func TestResumingStreamKeepsSeenIDOverAnchor(t *testing.T) {
	s := &eventServer{conns: []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) { sseOpen(w, "41"); sseSend(w, 50) },
		func(w http.ResponseWriter, r *http.Request) { sseOpen(w, "60") },
	}}
	srv := httptest.NewServer(s)
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	ch := make(chan *idEvent)
	stream, err := c.ResumingStream("GET", "/events", ch)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case e := <-ch:
		if e.ID != 50 {
			t.Fatalf("event id %d", e.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	ids := waitConnections(t, s, 3)
	if ids[1] != "50" || ids[2] != "50" {
		t.Fatalf("Last-Event-Id per connection = %q, want [0 50 50]", ids)
	}
}

// A server that does not send the header (a controller from before EDGE-94)
// leaves the old behaviour: the cursor is the last event seen.
func TestResumingStreamWithoutAnchorUsesLastEvent(t *testing.T) {
	s := &eventServer{conns: []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) { sseOpen(w, ""); sseSend(w, 7) },
	}}
	srv := httptest.NewServer(s)
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	ch := make(chan *idEvent)
	stream, err := c.ResumingStream("GET", "/events", ch)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-ch
	ids := waitConnections(t, s, 2)
	if ids[1] != "7" {
		t.Fatalf("Last-Event-Id per connection = %q, want [0 7]", ids)
	}
}

// A caller that keeps state built from a list (the router) needs to know
// that the stream dropped, so it can list again. With EndOnDisconnect the
// first connect is still retried, but a dropped connection ends the stream
// with ErrStreamDisconnected instead of reconnecting.
func TestResumingStreamEndOnDisconnect(t *testing.T) {
	s := &eventServer{conns: []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) { http.Error(w, "starting", 503) },
		func(w http.ResponseWriter, r *http.Request) { sseOpen(w, "3"); sseSend(w, 4) },
	}}
	srv := httptest.NewServer(s)
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	ch := make(chan *idEvent)
	stream, err := c.ResumingStreamWithOptions("GET", "/events", ch, StreamOptions{EndOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if e := <-ch; e == nil || e.ID != 4 {
		t.Fatalf("event %+v", e)
	}
	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("unexpected event %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end after the connection dropped")
	}
	if !errors.Is(stream.Err(), ErrStreamDisconnected) {
		t.Fatalf("stream error %v, want ErrStreamDisconnected", stream.Err())
	}
	time.Sleep(300 * time.Millisecond)
	if ids := s.lastEventIDs(); len(ids) != 2 {
		t.Fatalf("server saw %d connections, want 2 (one refused, one dropped, no reconnect)", len(ids))
	}
}
