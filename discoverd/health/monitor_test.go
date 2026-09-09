package health

import (
	"errors"
	"sync"
	"time"

	. "github.com/flynn/go-check"
)

type MonitorSuite struct{}

var _ = Suite(&MonitorSuite{})

type CheckFunc func() error

func (f CheckFunc) Check() error { return f() }

func (MonitorSuite) TestMonitor(c *C) {
	type step struct {
		up    bool
		event MonitorStatus
	}

	for _, t := range []struct {
		name      string
		steps     []step
		threshold int
	}{
		{
			name:  "service doesn't come up",
			steps: []step{{}, {}, {}},
		},
		{
			name: "service comes up right away",
			steps: []step{
				{event: MonitorStatusUp, up: true},
				{up: true},
				{up: true},
			},
		},
		{
			name: "service comes up after a few checks",
			steps: []step{
				{}, {}, {},
				{event: MonitorStatusUp, up: true},
			},
		},
		{
			name: "up/down/up - default threshold",
			steps: []step{
				{event: MonitorStatusUp, up: true},
				{},
				{event: MonitorStatusDown},
				{up: true},
				{event: MonitorStatusUp, up: true},
			},
		},
		{
			name:      "up/down/up - custom threshold",
			threshold: 3,
			steps: []step{
				{event: MonitorStatusUp, up: true},
				{},
				{},
				{event: MonitorStatusDown},
				{up: true},
				{up: true},
				{event: MonitorStatusUp, up: true},
			},
		},
		{
			name: "flapping - alternate",
			steps: []step{
				{event: MonitorStatusUp, up: true},
				{},
				{up: true},
				{},
				{up: true},
				{},
				{event: MonitorStatusDown},
				{up: true},
				{},
				{up: true},
				{},
			},
		},
		{
			name:      "flapping - consecutive",
			threshold: 3,
			steps: []step{
				{event: MonitorStatusUp, up: true},
				{},
				{},
				{up: true},
				{},
				{},
				{up: true},
				{},
				{},
				{event: MonitorStatusDown},
				{up: true},
				{up: true},
				{},
				{up: true},
				{up: true},
				{},
			},
		},
	} {
		c.Log(t.name)

		var expected []MonitorEvent
		for _, s := range t.steps {
			if s.event == 0 {
				continue
			}
			ev := MonitorEvent{Status: s.event}
			if !s.up {
				ev.Err = errors.New("check failure")
			}
			expected = append(expected, ev)
		}

		var mu sync.Mutex
		i := 0
		check := CheckFunc(func() error {
			mu.Lock()
			defer mu.Unlock()
			if i >= len(t.steps) {
				// Hold the final step's result so we do not emit a spurious
				// Down after the script ends (e.g. Up then "finished" errors).
				if len(t.steps) > 0 && t.steps[len(t.steps)-1].up {
					return nil
				}
				return errors.New("check failure")
			}
			step := t.steps[i]
			i++
			if !step.up {
				return errors.New("check failure")
			}
			return nil
		})

		actualEvents := make(chan MonitorEvent, 16)
		// Millisecond intervals are fast enough for tests but avoid the
		// nanosecond scheduling races that dropped Up events when the stream
		// was closed from inside Check before Monitor.up() could send.
		stream := Monitor{
			Threshold:     t.threshold,
			StartInterval: time.Millisecond,
			Interval:      time.Millisecond,
		}.Run(check, actualEvents)

		for ei, want := range expected {
			select {
			case actual := <-actualEvents:
				c.Assert(actual.Check, FitsTypeOf, CheckFunc(nil))
				actual.Check = nil
				c.Assert(actual.Status, Equals, want.Status)
				if want.Err != nil {
					c.Assert(actual.Err, NotNil)
					c.Assert(actual.Err.Error(), Equals, want.Err.Error())
				} else {
					c.Assert(actual.Err, IsNil)
				}
			case <-time.After(2 * time.Second):
				c.Fatalf("%s: timeout waiting for event %d (status %s)", t.name, ei, want.Status)
			}
		}

		// Wait until the scripted checks have all run (important when no
		// events are expected), then ensure nothing else is emitted.
		deadline := time.Now().Add(2 * time.Second)
		for {
			mu.Lock()
			done := i >= len(t.steps)
			mu.Unlock()
			if done {
				break
			}
			if time.Now().After(deadline) {
				c.Fatalf("%s: timeout waiting for check script to finish", t.name)
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case actual := <-actualEvents:
			stream.Close()
			c.Fatalf("%s: unexpected extra event %#v", t.name, actual)
		case <-time.After(25 * time.Millisecond):
		}
		stream.Close()
		for range actualEvents {
		}
	}
}
