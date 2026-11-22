package main

import (
	"encoding/json"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/stream"
)

// appDeletionCatchUp is how many past app deletions each (re)connect replays,
// so deletions made while this instance was starting or disconnected, or
// carried in from the leader's snapshot, are still dropped.
const appDeletionCatchUp = 1000

// appEventSource is the part of the controller client the watcher uses.
type appEventSource interface {
	StreamEvents(opts ct.StreamEventsOptions, output chan *ct.Event) (stream.Stream, error)
}

// watchAppDeletions drops the buffer of every app the controller reports
// deleted, until stop is closed. The controller may not be up yet (bootstrap,
// a full-cluster restart), so it reconnects after retry for as long as it runs.
func watchAppDeletions(agg *Aggregator, src appEventSource, stop <-chan struct{}, retry time.Duration) {
	for {
		events := make(chan *ct.Event)
		s, err := src.StreamEvents(ct.StreamEventsOptions{
			ObjectTypes: []ct.EventType{ct.EventTypeAppDeletion},
			Past:        true,
			Count:       appDeletionCatchUp,
		}, events)
		if err != nil {
			log15.Info("app deletion stream unavailable, retrying", "err", err)
		} else if !drainAppDeletions(agg, events, stop) {
			s.Close()
			return
		} else if err := s.Err(); err != nil {
			log15.Info("app deletion stream ended, reconnecting", "err", err)
		}
		select {
		case <-stop:
			return
		case <-time.After(retry):
		}
	}
}

// drainAppDeletions applies deletion events until the stream ends (true) or
// stop is closed (false).
func drainAppDeletions(agg *Aggregator, events <-chan *ct.Event, stop <-chan struct{}) bool {
	for {
		select {
		case <-stop:
			return false
		case e, ok := <-events:
			if !ok {
				return true
			}
			if id := deletedAppID(e); id != "" {
				agg.DropApp(id)
			}
		}
	}
}

// deletedAppID returns the app id of a successful deletion event, or "" for a
// failed one (the app and its logs are still there).
func deletedAppID(e *ct.Event) string {
	if e == nil || e.ObjectType != ct.EventTypeAppDeletion {
		return ""
	}
	var d ct.AppDeletionEvent
	if err := json.Unmarshal(e.Data, &d); err != nil || d.Error != "" {
		return ""
	}
	if d.AppDeletion != nil && d.AppDeletion.AppID != "" {
		return d.AppDeletion.AppID
	}
	if e.AppID != "" {
		return e.AppID
	}
	return e.ObjectID
}
