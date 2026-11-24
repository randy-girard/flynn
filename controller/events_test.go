package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	log15 "github.com/inconshreveable/log15"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/randy-girard/flynn/pkg/sse"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

func (s *S) TestEvents(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "app1"})
	app2 := s.createTestApp(c, &ct.App{Name: "app2"})
	release := s.createTestRelease(c, "", &ct.Release{})

	jobID1 := random.UUID()
	jobID2 := random.UUID()
	jobID3 := random.UUID()
	jobs := []*ct.Job{
		{UUID: jobID1, AppID: app1.ID, ReleaseID: release.ID, Type: "web", State: ct.JobStateStarting},
		{UUID: jobID1, AppID: app1.ID, ReleaseID: release.ID, Type: "web", State: ct.JobStateUp},
		{UUID: jobID2, AppID: app1.ID, ReleaseID: release.ID, Type: "web", State: ct.JobStateStarting},
		{UUID: jobID2, AppID: app1.ID, ReleaseID: release.ID, Type: "web", State: ct.JobStateUp},
		{UUID: jobID3, AppID: app2.ID, ReleaseID: release.ID, Type: "web", State: ct.JobStateStarting},
		{UUID: jobID3, AppID: app2.ID, ReleaseID: release.ID, Type: "web", State: ct.JobStateUp},
	}

	listener := data.NewEventListener(data.NewEventRepo(s.hc.db))
	c.Assert(listener.Listen(), IsNil)

	// sub1 should receive job events for app1, job1
	sub1, err := listener.Subscribe([]string{app1.ID}, []string{string(ct.EventTypeJob)}, []string{jobID1})
	c.Assert(err, IsNil)
	defer sub1.Close()

	// sub2 should receive all job events for app1
	sub2, err := listener.Subscribe([]string{app1.ID}, []string{string(ct.EventTypeJob)}, nil)
	c.Assert(err, IsNil)
	defer sub2.Close()

	// sub3 should receive all events for app2
	sub3, err := listener.Subscribe([]string{app2.ID}, []string{}, nil)
	c.Assert(err, IsNil)
	defer sub3.Close()

	// sub4 should receive all job events
	sub4, err := listener.Subscribe(nil, []string{string(ct.EventTypeJob)}, nil)
	c.Assert(err, IsNil)
	defer sub4.Close()

	for _, job := range jobs {
		s.createTestJob(c, job)
	}

	assertJobEvents := func(sub *data.EventSubscriber, expected []*ct.Job) {
		var index int
		for {
			select {
			case e, ok := <-sub.Events:
				if !ok {
					c.Fatalf("unexpected close of event stream: %s", sub.Err)
				}
				var jobEvent ct.Job
				c.Assert(json.Unmarshal(e.Data, &jobEvent), IsNil)
				job := expected[index]
				c.Assert(jobEvent, DeepEquals, *job)
				index += 1
				if index == len(expected) {
					return
				}
			case <-time.After(10 * time.Second):
				c.Fatal("timed out waiting for app event")
			}
		}
	}
	assertJobEvents(sub1, jobs[0:2])
	assertJobEvents(sub2, jobs[0:4])
	assertJobEvents(sub3, jobs[4:6])
	assertJobEvents(sub4, jobs[0:6])
}

func (s *S) TestStreamAppLifeCycleEvents(c *C) {
	events := make(chan *ct.Event)
	stream, err := s.c.StreamEvents(ct.StreamEventsOptions{}, events)
	c.Assert(err, IsNil)
	defer stream.Close()

	app := s.createTestApp(c, &ct.App{Name: "app3"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	nextRelease := s.createTestRelease(c, app.ID, &ct.Release{})

	c.Assert(s.c.SetAppRelease(app.ID, release.ID), IsNil)
	newStrategy := "one-by-one"
	c.Assert(s.c.UpdateApp(&ct.App{
		ID:       app.ID,
		Strategy: newStrategy,
	}), IsNil)
	newMeta := map[string]string{
		"foo": "bar",
	}
	c.Assert(s.c.UpdateApp(&ct.App{
		ID:   app.ID,
		Meta: newMeta,
	}), IsNil)

	c.Assert(s.c.SetAppRelease(app.ID, nextRelease.ID), IsNil)

	assertAppEvent := func(e *ct.Event) *ct.App {
		var eventApp *ct.App
		c.Assert(json.Unmarshal(e.Data, &eventApp), IsNil)
		c.Assert(e.ObjectType, Equals, ct.EventTypeApp)
		c.Assert(e.ObjectID, Equals, app.ID)
		c.Assert(eventApp, NotNil)
		c.Assert(eventApp.ID, Equals, app.ID)
		return eventApp
	}
	assertReleaseEvent := func(e *ct.Event, id string) {
		var eventRelease *ct.Release
		c.Assert(json.Unmarshal(e.Data, &eventRelease), IsNil)
		c.Assert(e.ObjectType, Equals, ct.EventTypeRelease)
		c.Assert(e.ObjectID, Equals, id)
		c.Assert(eventRelease, NotNil)
		c.Assert(eventRelease.ID, Equals, id)
		c.Assert(eventRelease.AppID, Equals, app.ID)
	}

	eventAssertions := []func(*ct.Event){
		func(e *ct.Event) {
			a := assertAppEvent(e)
			c.Assert(a.ReleaseID, Equals, app.ReleaseID)
			c.Assert(a.Strategy, Equals, app.Strategy)
			c.Assert(a.Meta, DeepEquals, app.Meta)
		},
		func(e *ct.Event) {
			assertReleaseEvent(e, release.ID)
		},
		func(e *ct.Event) {
			assertReleaseEvent(e, nextRelease.ID)
		},
		func(e *ct.Event) {
			var eventRelease *ct.AppRelease
			c.Assert(json.Unmarshal(e.Data, &eventRelease), IsNil)
			c.Assert(e.ObjectType, Equals, ct.EventTypeAppRelease)
			c.Assert(e.ObjectID, Equals, release.ID)
			c.Assert(eventRelease, NotNil)
			c.Assert(eventRelease.Release, NotNil)
			c.Assert(eventRelease.Release.ID, Equals, release.ID)
			c.Assert(eventRelease.PrevRelease, IsNil)
		},
		func(e *ct.Event) {
			a := assertAppEvent(e)
			c.Assert(a.Strategy, Equals, newStrategy)
			c.Assert(a.Meta, DeepEquals, app.Meta)
		},
		func(e *ct.Event) {
			a := assertAppEvent(e)
			c.Assert(a.Strategy, Equals, newStrategy)
			c.Assert(a.Meta, DeepEquals, newMeta)
		},
		func(e *ct.Event) {
			var eventRelease *ct.AppRelease
			c.Assert(json.Unmarshal(e.Data, &eventRelease), IsNil)
			c.Assert(e.ObjectType, Equals, ct.EventTypeAppRelease)
			c.Assert(e.ObjectID, Equals, nextRelease.ID)
			c.Assert(eventRelease, NotNil)
			c.Assert(eventRelease.Release, NotNil)
			c.Assert(eventRelease.Release.ID, Equals, nextRelease.ID)
			c.Assert(eventRelease.PrevRelease, NotNil)
			c.Assert(eventRelease.PrevRelease.ID, Equals, release.ID)
		},
	}

outer:
	for i, fn := range eventAssertions {
	inner:
		for {
			select {
			case e, ok := <-events:
				if !ok {
					c.Fatal("unexpected close of event stream")
				}
				// ignore events for other apps
				if e.AppID != app.ID {
					continue inner
				}
				fn(e)
				continue outer
			case <-time.After(10 * time.Second):
				c.Fatalf("Timed out waiting for event %d", i)
			}
		}
	}
}

func (s *S) TestStreamReleaseEvents(c *C) {
	app := s.createTestApp(c, &ct.App{})

	events := make(chan *ct.Event)
	stream, err := s.c.StreamEvents(ct.StreamEventsOptions{}, events)
	c.Assert(err, IsNil)
	defer stream.Close()

	release := s.createTestRelease(c, app.ID, &ct.Release{})

	var gotRelease, gotArtifact bool
	deadline := time.After(10 * time.Second)
	for !gotRelease || !gotArtifact {
		select {
		case e, ok := <-events:
			if !ok {
				c.Fatal("unexpected close of event stream")
			}
			switch e.ObjectType {
			case ct.EventTypeArtifact:
				var eventArtifact *ct.Artifact
				c.Assert(json.Unmarshal(e.Data, &eventArtifact), IsNil)
				c.Assert(e.ObjectID, Equals, release.ArtifactIDs[0])
				c.Assert(eventArtifact, NotNil)
				c.Assert(eventArtifact.ID, Equals, release.ArtifactIDs[0])
				gotArtifact = true
			case ct.EventTypeRelease:
				var eventRelease *ct.Release
				c.Assert(json.Unmarshal(e.Data, &eventRelease), IsNil)
				c.Assert(e.AppID, Equals, app.ID)
				c.Assert(e.ObjectID, Equals, release.ID)
				c.Assert(eventRelease, DeepEquals, release)
				gotRelease = true
			case ct.EventTypeApp:
				// ignore unrelated app updates on the global stream
			default:
				// ignore other event types that may appear on the global stream
			}
		case <-deadline:
			c.Fatalf("timed out waiting for release events (artifact=%v release=%v)", gotArtifact, gotRelease)
		}
	}
}

func (s *S) TestStreamFormationEvents(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "stream-formation-test"})
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"foo": {}},
	})

	events := make(chan *ct.Event)
	stream, err := s.c.StreamEvents(ct.StreamEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeScaleRequest},
	}, events)
	c.Assert(err, IsNil)
	defer stream.Close()

	formation := s.createTestFormation(c, &ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{"foo": 1},
	})
	defer s.deleteTestFormation(formation)

	select {
	case e, ok := <-events:
		if !ok {
			c.Fatal("unexpected close of event stream")
		}
		var req *ct.ScaleRequest
		c.Assert(json.Unmarshal(e.Data, &req), IsNil)
		c.Assert(e.AppID, Equals, app.ID)
		c.Assert(e.ObjectType, Equals, ct.EventTypeScaleRequest)
		c.Assert(e.ObjectID, Equals, req.ID)
		c.Assert(req.NewProcesses, NotNil)
		c.Assert(*req.NewProcesses, DeepEquals, formation.Processes)
	case <-time.After(10 * time.Second):
		c.Fatal("Timed out waiting for scale request event")
	}

	nextFormation := s.createTestFormation(c, &ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{"foo": 2},
	})
	defer s.deleteTestFormation(nextFormation)

	select {
	case e, ok := <-events:
		if !ok {
			c.Fatal("unexpected close of event stream")
		}
		var req *ct.ScaleRequest
		c.Assert(json.Unmarshal(e.Data, &req), IsNil)
		c.Assert(e.AppID, Equals, app.ID)
		c.Assert(e.ObjectType, Equals, ct.EventTypeScaleRequest)
		c.Assert(e.ObjectID, Equals, req.ID)
		c.Assert(req.NewProcesses, NotNil)
		c.Assert(*req.NewProcesses, DeepEquals, nextFormation.Processes)
		c.Assert(req.OldProcesses, DeepEquals, formation.Processes)
	case <-time.After(10 * time.Second):
		c.Fatal("Timed out waiting for scale request event")
	}

	c.Assert(s.c.DeleteFormation(app.ID, release.ID), IsNil)

	select {
	case e, ok := <-events:
		if !ok {
			c.Fatal("unexpected close of event stream")
		}
		var req *ct.ScaleRequest
		c.Assert(json.Unmarshal(e.Data, &req), IsNil)
		c.Assert(e.AppID, Equals, app.ID)
		c.Assert(e.ObjectType, Equals, ct.EventTypeScaleRequest)
		c.Assert(e.ObjectID, Equals, req.ID)
		c.Assert(req.NewProcesses, NotNil)
		c.Assert(*req.NewProcesses, DeepEquals, map[string]int{"foo": 0})
		c.Assert(req.OldProcesses, DeepEquals, nextFormation.Processes)
	case <-time.After(10 * time.Second):
		c.Fatal("Timed out waiting for scale event")
	}
}

func (s *S) TestStreamProviderEvents(c *C) {
	events := make(chan *ct.Event)
	stream, err := s.c.StreamEvents(ct.StreamEventsOptions{}, events)
	c.Assert(err, IsNil)
	defer stream.Close()

	provider := s.createTestProvider(c, &ct.Provider{
		URL:  "https://test-stream-provider.example.com",
		Name: "test-stream-provider",
	})

	select {
	case e, ok := <-events:
		if !ok {
			c.Fatal("unexpected close of event stream")
		}
		var eventProvider *ct.Provider
		c.Assert(json.Unmarshal(e.Data, &eventProvider), IsNil)
		c.Assert(e.AppID, Equals, "")
		c.Assert(e.ObjectType, Equals, ct.EventTypeProvider)
		c.Assert(e.ObjectID, Equals, provider.ID)
		c.Assert(eventProvider, DeepEquals, provider)
	case <-time.After(10 * time.Second):
		c.Fatal("Timed out waiting for provider event")
	}
}

func (s *S) TestStreamResourceEvents(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "app4"})

	events := make(chan *ct.Event)
	stream, err := s.c.StreamEvents(ct.StreamEventsOptions{
		ObjectTypes: []ct.EventType{
			ct.EventTypeResource,
			ct.EventTypeResourceDeletion,
		},
	}, events)
	c.Assert(err, IsNil)
	defer stream.Close()

	resource, provider, srv := s.provisionTestResourceWithServer(c, "stream-resources", []string{app.ID})
	defer srv.Close()

	select {
	case e, ok := <-events:
		if !ok {
			c.Fatal("unexpected close of event stream")
		}
		var eventResource *ct.Resource
		c.Assert(json.Unmarshal(e.Data, &eventResource), IsNil)
		c.Assert(e.AppID, Equals, app.ID)
		c.Assert(e.ObjectType, Equals, ct.EventTypeResource)
		c.Assert(e.ObjectID, Equals, resource.ID)
		c.Assert(eventResource, DeepEquals, resource)
	case <-time.After(10 * time.Second):
		c.Fatal("Timed out waiting for resource event")
	}

	_, err = s.c.DeleteResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)

	select {
	case e, ok := <-events:
		if !ok {
			c.Fatal("unexpected close of event stream")
		}
		var eventResource *ct.Resource
		c.Assert(json.Unmarshal(e.Data, &eventResource), IsNil)
		c.Assert(e.AppID, Equals, app.ID)
		c.Assert(e.ObjectType, Equals, ct.EventTypeResourceDeletion)
		c.Assert(e.ObjectID, Equals, resource.ID)
		c.Assert(eventResource, DeepEquals, resource)
	case <-time.After(10 * time.Second):
		c.Fatal("Timed out waiting for resource_deletion event")
	}
}

func (s *S) TestListEvents(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "app5"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})

	c.Assert(s.c.SetAppRelease(app.ID, release.ID), IsNil)
	newStrategy := "one-by-one"
	c.Assert(s.c.UpdateApp(&ct.App{
		ID:       app.ID,
		Strategy: newStrategy,
	}), IsNil)
	newMeta := map[string]string{
		"foo": "bar",
	}
	c.Assert(s.c.UpdateApp(&ct.App{
		ID:   app.ID,
		Meta: newMeta,
	}), IsNil)

	events, err := s.c.ListEvents(ct.ListEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeApp, ct.EventTypeRelease, ct.EventTypeAppRelease},
		AppID:       app.ID,
	})
	c.Assert(err, IsNil)

	assertAppEvent := func(e *ct.Event) *ct.App {
		var eventApp *ct.App
		c.Assert(json.Unmarshal(e.Data, &eventApp), IsNil)
		c.Assert(e.AppID, Equals, app.ID)
		c.Assert(e.ObjectType, Equals, ct.EventTypeApp)
		c.Assert(e.ObjectID, Equals, app.ID)
		c.Assert(eventApp, NotNil)
		c.Assert(eventApp.ID, Equals, app.ID)
		return eventApp
	}

	eventAssertions := []func(*ct.Event){
		func(e *ct.Event) {
			a := assertAppEvent(e)
			c.Assert(a.ReleaseID, Equals, app.ReleaseID)
			c.Assert(a.Strategy, Equals, app.Strategy)
			c.Assert(a.Meta, DeepEquals, app.Meta)
		},
		func(e *ct.Event) {
			var eventRelease *ct.Release
			c.Assert(json.Unmarshal(e.Data, &eventRelease), IsNil)
			c.Assert(e.AppID, Equals, app.ID)
			c.Assert(e.ObjectType, Equals, ct.EventTypeRelease)
			c.Assert(e.ObjectID, Equals, release.ID)
			c.Assert(eventRelease, NotNil)
			c.Assert(eventRelease.ID, Equals, release.ID)
			c.Assert(eventRelease.AppID, Equals, app.ID)
		},
		func(e *ct.Event) {
			var eventRelease *ct.AppRelease
			c.Assert(json.Unmarshal(e.Data, &eventRelease), IsNil)
			c.Assert(e.AppID, Equals, app.ID)
			c.Assert(e.ObjectType, Equals, ct.EventTypeAppRelease)
			c.Assert(e.ObjectID, Equals, release.ID)
			c.Assert(eventRelease, NotNil)
			c.Assert(eventRelease.Release, NotNil)
			c.Assert(eventRelease.Release.ID, Equals, release.ID)
		},
		func(e *ct.Event) {
			a := assertAppEvent(e)
			c.Assert(a.Strategy, Equals, newStrategy)
			c.Assert(a.Meta, DeepEquals, app.Meta)
		},
		func(e *ct.Event) {
			a := assertAppEvent(e)
			c.Assert(a.Strategy, Equals, newStrategy)
			c.Assert(a.Meta, DeepEquals, newMeta)
		},
	}

	c.Assert(len(events), Equals, len(eventAssertions))
	eventsLen := len(events)
	for i, fn := range eventAssertions {
		fn(events[eventsLen-i-1])
	}

	eventsSlice, err := s.c.ListEvents(ct.ListEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeApp, ct.EventTypeAppRelease},
		BeforeID:    &events[0].ID,
		SinceID:     &events[eventsLen-1].ID,
	})
	c.Assert(err, IsNil)
	c.Assert(len(eventsSlice), Equals, 2)
	c.Assert(eventsSlice[0].ID, Equals, events[1].ID)
	c.Assert(eventsSlice[1].ID, Equals, events[2].ID)

	eventsSlice, err = s.c.ListEvents(ct.ListEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeApp, ct.EventTypeAppRelease},
		BeforeID:    &events[0].ID,
		SinceID:     &events[eventsLen-1].ID,
		Count:       1,
	})
	c.Assert(err, IsNil)
	c.Assert(len(eventsSlice), Equals, 1)
	c.Assert(eventsSlice[0].ID, Equals, events[1].ID)
}

func (s *S) TestGetEvent(c *C) {
	// ensure there's at least one event
	_ = s.createTestRelease(c, "", &ct.Release{})

	events, err := s.c.ListEvents(ct.ListEventsOptions{})
	c.Assert(err, IsNil)
	c.Assert(len(events), Not(Equals), 0)

	event, err := s.c.GetEvent(events[0].ID)
	c.Assert(err, IsNil)
	c.Assert(event, DeepEquals, events[0])
}

// goneWriter is an http.ResponseWriter for a client that has gone away:
// once release is closed, every body write fails, as a write to a closed
// connection would. Until then writes block, which keeps the SSE writer
// goroutine busy so the handler's next send has nobody to receive it.
type goneWriter struct {
	header  http.Header
	release chan struct{}
	writing chan struct{}
	once    sync.Once
}

func newGoneWriter() *goneWriter {
	return &goneWriter{header: make(http.Header), release: make(chan struct{}), writing: make(chan struct{})}
}

func (w *goneWriter) Header() http.Header { return w.header }
func (w *goneWriter) WriteHeader(int)     {}
func (w *goneWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.writing) })
	<-w.release
	return 0, errors.New("write: broken pipe")
}

func (s *S) runStreamEvents(c *C, w http.ResponseWriter, query string, app *ct.App, listener *data.EventListener) chan error {
	req, err := http.NewRequest("GET", "/events?"+query, nil)
	c.Assert(err, IsNil)
	ctx := ctxhelper.NewContextLogger(context.Background(), log15.New())
	done := make(chan error, 1)
	go func() { done <- streamEvents(ctx, w, req, listener, app, data.NewEventRepo(s.hc.db)) }()
	return done
}

// TestStreamEventsReturnsWhenClientGone covers CORE-44: when the client goes
// away during the replay of past events, the handler must return instead of
// blocking forever on a send nobody will receive.
func (s *S) TestStreamEventsReturnsWhenClientGone(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "events-gone-replay"})
	for i := 0; i < 3; i++ {
		s.createTestRelease(c, app.ID, &ct.Release{})
	}
	listener := data.NewEventListener(data.NewEventRepo(s.hc.db))
	c.Assert(listener.Listen(), IsNil)
	defer listener.CloseWithError(nil)

	w := newGoneWriter()
	close(w.release) // every write fails: the client is already gone
	done := s.runStreamEvents(c, w, "past=true", app, listener)
	select {
	case <-done:
	case <-time.After(time.Second):
		c.Fatal("streamEvents did not return within 1s of the client going away during replay")
	}
}

// TestStreamEventsReturnsWhenClientGoneWithLiveEventPending covers the live
// half of CORE-44: an event is waiting to be sent when the SSE writer stops.
func (s *S) TestStreamEventsReturnsWhenClientGoneWithLiveEventPending(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "events-gone-live"})
	listener := data.NewEventListener(data.NewEventRepo(s.hc.db))
	c.Assert(listener.Listen(), IsNil)
	defer listener.CloseWithError(nil)

	w := newGoneWriter()
	done := s.runStreamEvents(c, w, "", app, listener)

	// wait for the subscription to be in place before creating events
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.createTestRelease(c, app.ID, &ct.Release{})
		select {
		case <-w.writing:
		case <-time.After(100 * time.Millisecond):
			c.Assert(time.Now().Before(deadline), Equals, true, Commentf("no event reached the SSE writer"))
			continue
		}
		break
	}
	// the writer is stuck on the first event; give the handler a second
	// event so it is blocked handing it over, then drop the client
	s.createTestRelease(c, app.ID, &ct.Release{})
	time.Sleep(200 * time.Millisecond)
	close(w.release)

	select {
	case <-done:
	case <-time.After(time.Second):
		c.Fatal("streamEvents did not return within 1s of the client going away with an event pending")
	}
}

// EDGE-94: a client whose stream drops before its first event used to resume
// with Last-Event-Id 0, and nothing is replayed for 0, so every change made
// while it was disconnected was lost (a router kept a deleted route). The
// stream now sends the id it starts after; resuming from that id replays the
// changes made in the gap. A stream that replays from the beginning
// (past=true) has no start id.
func (s *S) TestStreamEventsStartIDLetsAClientResume(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "events-start-id"})
	route := s.createTestRoute(c, app.ID, (&router.HTTPRoute{Domain: "start-id.example.com", Service: "foo"}).ToRoute())

	open := func(lastID, query string) (*http.Response, *sse.Decoder) {
		req, err := http.NewRequest("GET", s.srv.URL+"/events?object_types=route,route_deletion&app_id="+app.ID+query, nil)
		c.Assert(err, IsNil)
		req.SetBasicAuth("", authKey)
		req.Header.Set("Accept", "text/event-stream")
		if lastID != "" {
			req.Header.Set("Last-Event-Id", lastID)
		}
		res, err := http.DefaultClient.Do(req)
		c.Assert(err, IsNil)
		c.Assert(res.StatusCode, Equals, 200)
		return res, sse.NewDecoder(bufio.NewReader(res.Body))
	}
	next := func(dec *sse.Decoder) *ct.Event {
		got := make(chan *ct.Event, 1)
		go func() {
			var e ct.Event
			if err := dec.Decode(&e); err != nil {
				got <- nil
				return
			}
			got <- &e
		}()
		select {
		case e := <-got:
			c.Assert(e, NotNil)
			return e
		case <-time.After(10 * time.Second):
			c.Fatal("timed out waiting for event")
		}
		return nil
	}

	var maxID int64
	c.Assert(s.hc.db.QueryRow("SELECT max(event_id) FROM events").Scan(&maxID), IsNil)
	res, dec := open("", "")
	start := res.Header.Get(sse.LastEventIDHeader)
	c.Assert(start, Equals, strconv.FormatInt(maxID, 10))
	// the start id is taken after the stream subscribed: a change made as
	// soon as the client has the response arrives on the stream
	live := s.createTestRoute(c, app.ID, (&router.HTTPRoute{Domain: "start-id-live.example.com", Service: "foo"}).ToRoute())
	e := next(dec)
	c.Assert(e.ObjectType, Equals, ct.EventTypeRoute)
	c.Assert(e.ObjectID, Equals, live.ID)
	res.Body.Close()

	// a client that saw no event resumes from the start id and gets every
	// change made while it was away, including that live one
	c.Assert(s.c.DeleteRoute(app.ID, route.FormattedID()), IsNil)
	added := s.createTestRoute(c, app.ID, (&router.HTTPRoute{Domain: "start-id-added.example.com", Service: "foo"}).ToRoute())
	res, dec = open(start, "")
	c.Assert(res.Header.Get(sse.LastEventIDHeader), Equals, start)
	c.Assert(next(dec).ObjectID, Equals, live.ID)
	e = next(dec)
	c.Assert(e.ObjectType, Equals, ct.EventTypeRouteDeletion)
	c.Assert(e.ObjectID, Equals, route.ID)
	e = next(dec)
	c.Assert(e.ObjectType, Equals, ct.EventTypeRoute)
	c.Assert(e.ObjectID, Equals, added.ID)
	res.Body.Close()

	res, _ = open("", "&past=true")
	c.Assert(res.Header.Get(sse.LastEventIDHeader), Equals, "")
	res.Body.Close()
}
