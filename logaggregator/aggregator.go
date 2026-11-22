package main

import (
	"sync"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/logaggregator/buffer"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
)

// DefaultBufferTTL is how long a buffer with no new message and no follower
// is kept. It is also how long a deleted app's late lines are refused.
const DefaultBufferTTL = 7 * 24 * time.Hour

// sweepInterval is how often idle buffers and old tombstones are dropped.
const sweepInterval = time.Minute

// Aggregator is a log aggregation server that collects syslog messages.
//
// A buffer exists only for an app id that has logged or that someone follows.
// It is dropped when the controller reports the app deleted, or when it has
// had no message for ttl and nobody follows it, so memory does not grow with
// every app that ever ran.
type Aggregator struct {
	bmu     sync.Mutex // protects buffers and deleted
	buffers map[string]*buffer.Buffer
	// deleted holds the ids of deleted apps and when they were dropped, so
	// late lines from their dying jobs (or a replayed snapshot) do not
	// recreate the buffer.
	deleted map[string]time.Time

	ttl time.Duration

	msgc chan *rfc5424.Message

	pmu    sync.Mutex
	pausec chan struct{}
}

// NewAggregator creates a new running Aggregator with DefaultBufferTTL.
func NewAggregator() *Aggregator {
	return newAggregator(DefaultBufferTTL)
}

func newAggregator(ttl time.Duration) *Aggregator {
	if ttl <= 0 {
		ttl = DefaultBufferTTL
	}
	a := &Aggregator{
		buffers: make(map[string]*buffer.Buffer),
		deleted: make(map[string]time.Time),
		ttl:     ttl,
		msgc:    make(chan *rfc5424.Message, 1000),
		pausec:  make(chan struct{}),
	}
	go a.run()
	return a
}

// Feed inserts a message in the aggregator.
func (a *Aggregator) Feed(msg *rfc5424.Message) {
	a.msgc <- msg
}

func (a *Aggregator) Pause() func() {
	a.pmu.Lock()

	a.pausec <- struct{}{}

	return func() {
		<-a.pausec
		a.pmu.Unlock()
	}
}

// Read returns the buffered messages for id. It does not create a buffer for
// an id that has none.
func (a *Aggregator) Read(id string) []*rfc5424.Message {
	a.bmu.Lock()
	buf, ok := a.buffers[id]
	a.bmu.Unlock()
	if !ok {
		return nil
	}
	return buf.Read()
}

// ReadAll returns the buffered messages of every buffer that is not empty and
// not expired. It is what the snapshot for a new instance is built from.
func (a *Aggregator) ReadAll() [][]*rfc5424.Message {
	// TODO(benburkert): restructure Aggregator & ring.Buffer to avoid nested locks
	a.bmu.Lock()
	defer a.bmu.Unlock()

	cutoff := time.Now().Add(-a.ttl)
	buffers := make([][]*rfc5424.Message, 0, len(a.buffers))
	for _, buf := range a.buffers {
		if buf.Len() <= 0 || buf.LastActivity().Before(cutoff) {
			continue
		}
		buffers = append(buffers, buf.Read())
	}

	return buffers
}

// ReadAndSubscribe returns the buffered messages and adds a subscriber channel
// for id. Following an id that has not logged yet creates its buffer.
func (a *Aggregator) ReadAndSubscribe(id string, msgc chan<- *rfc5424.Message, donec <-chan struct{}) []*rfc5424.Message {
	// hold bmu so a sweep cannot drop the buffer before the subscriber is on it
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return a.getBufferLocked(id).ReadAndSubscribe(msgc, donec)
}

// Reset clears all buffered data and closes subscribers.
func (a *Aggregator) Reset() {
	a.bmu.Lock()
	defer a.bmu.Unlock()

	for k, buf := range a.buffers {
		buf.Close()
		delete(a.buffers, k)
	}
}

// Shutdown stops the Aggregator, resets the buffers, and closes buffer
// subscribers.
func (a *Aggregator) Shutdown() {
	a.Reset()
	close(a.msgc)
}

// Subscribe adds a subscriber channel for id. Following an id that has not
// logged yet creates its buffer.
func (a *Aggregator) Subscribe(id string, msgc chan<- *rfc5424.Message, donec <-chan struct{}) {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	a.getBufferLocked(id).Subscribe(msgc, donec)
}

// DropApp removes the buffer of a deleted app, ends its followers, and refuses
// its lines for the buffer TTL.
func (a *Aggregator) DropApp(id string) {
	a.dropApp(id, time.Now())
}

func (a *Aggregator) dropApp(id string, now time.Time) {
	a.bmu.Lock()
	defer a.bmu.Unlock()

	if buf, ok := a.buffers[id]; ok {
		buf.Close()
		delete(a.buffers, id)
	}
	a.deleted[id] = now
}

// BufferCount returns the number of app buffers held.
func (a *Aggregator) BufferCount() int {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return len(a.buffers)
}

// TombstoneCount returns the number of deleted app ids still refused.
func (a *Aggregator) TombstoneCount() int {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return len(a.deleted)
}

// expire drops buffers nobody follows that are empty or have had no message
// for ttl, and forgets deleted app ids older than ttl.
func (a *Aggregator) expire(now time.Time) {
	a.bmu.Lock()
	defer a.bmu.Unlock()

	cutoff := now.Add(-a.ttl)
	for id, buf := range a.buffers {
		if buf.Subscribers() > 0 {
			continue
		}
		if buf.Len() <= 0 || buf.LastActivity().Before(cutoff) {
			buf.Close()
			delete(a.buffers, id)
		}
	}
	for id, at := range a.deleted {
		if at.Before(cutoff) {
			delete(a.deleted, id)
		}
	}
}

// getBufferLocked returns the buffer for id, creating it. a.bmu must be held.
func (a *Aggregator) getBufferLocked(id string) *buffer.Buffer {
	if buf, ok := a.buffers[id]; ok {
		return buf
	}

	buf := buffer.NewBuffer()
	a.buffers[id] = buf
	return buf
}

func (a *Aggregator) run() {
	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()
	for {
		select {
		case msg, ok := <-a.msgc:
			if !ok {
				return
			}
			a.feed(msg)

		case now := <-sweep.C:
			a.expire(now)

		case <-a.pausec:
			a.pausec <- struct{}{}
		}
	}
}

func (a *Aggregator) feed(msg *rfc5424.Message) {
	id := string(msg.AppName)

	a.bmu.Lock()
	defer a.bmu.Unlock()

	if _, gone := a.deleted[id]; gone {
		return
	}
	if err := a.getBufferLocked(id).Add(msg); err != nil {
		log15.Error("error buffering log message", "app", id, "err", err)
	}
}
