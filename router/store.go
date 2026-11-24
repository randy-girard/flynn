package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/attempt"
	"github.com/randy-girard/flynn/pkg/dialer"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/stream"
	router "github.com/randy-girard/flynn/router/types"
)

type Store interface {
	List() ([]*router.Route, error)
	Watch(ch chan *router.Event) (stream.Stream, error)
}

var controllerAttempts = attempt.Strategy{
	Total: 2 * time.Minute,
	Delay: time.Second,
}

// controllerKeepAlive bounds how long a route stream stays on a controller
// whose node died without closing the connection (a reboot or power loss
// sends no FIN): about 5 s idle plus 3 probes 5 s apart. The default dialer
// took 2 m 45 s, and the router kept serving routes deleted in that time.
var controllerKeepAlive = net.KeepAliveConfig{
	Enable:   true,
	Idle:     5 * time.Second,
	Interval: 5 * time.Second,
	Count:    3,
}

// controllerHTTPClient is httphelper.RetryClient with controllerKeepAlive
// on its connections.
func controllerHTTPClient() *http.Client {
	tr := httphelper.RetryClient.Transport.(*http.Transport).Clone()
	d := net.Dialer{Timeout: dialer.Default.Timeout, KeepAliveConfig: controllerKeepAlive}
	tr.Dial = dialer.RetryDial(d.Dial)
	return &http.Client{Transport: tr}
}

func NewControllerStore() (*ControllerStore, error) {
	// Discover a controller instance (and obtain AUTH_KEY from env, falling
	// back to instance meta during mixed-version rolling updates), but create
	// the client using the discoverd DNS name so it automatically follows the
	// controller if its overlay IP changes (e.g. after a daemon restart).
	// Retry for up to 2 minutes to handle startup ordering during updates,
	// where the controller service may not yet be registered in discoverd.
	var client controller.Client
	err := controllerAttempts.Run(func() error {
		instances, err := discoverd.NewService("controller").Instances()
		if err != nil {
			return err
		}
		if len(instances) == 0 {
			return fmt.Errorf("no controller instances available")
		}
		inst := instances[0]
		client, err = controller.NewClientWithHTTP("", controller.KeyFromEnvOrMeta(inst.Meta), controllerHTTPClient())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &ControllerStore{client}, nil
}

type ControllerStore struct {
	client controller.Client
}

func (c *ControllerStore) List() ([]*router.Route, error) {
	return c.client.RouteList()
}

// Watch streams route changes. The stream ends with an error when its
// connection to the controller drops, instead of resuming: the Syncer then
// lists every route again and removes the ones that are gone, so a change
// made while the stream was down is never lost (EDGE-94: a stream resumed
// before its first event asked for the events after id 0, the controller
// replays nothing for 0, and a deleted route stayed on one node).
func (c *ControllerStore) Watch(ch chan *router.Event) (stream.Stream, error) {
	events := make(chan *ct.Event)
	eventStream, err := c.client.StreamEvents(ct.StreamEventsOptions{
		ObjectTypes: []ct.EventType{
			ct.EventTypeRoute,
			ct.EventTypeRouteDeletion,
		},
		EndOnDisconnect: true,
	}, events)
	if err != nil {
		return nil, err
	}
	routeStream := stream.New()
	go func() {
		defer close(ch)
		defer eventStream.Close()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					routeStream.Error = eventStream.Err()
					return
				}
				var route router.Route
				if err := json.Unmarshal(event.Data, &route); err != nil {
					routeStream.Error = err
					return
				}
				routerEvent := &router.Event{
					Event: c.toRouterEventType(event.ObjectType),
					ID:    route.ID,
					Route: &route,
				}
				select {
				case ch <- routerEvent:
				case <-routeStream.StopCh:
					return
				}
			case <-routeStream.StopCh:
				return
			}
		}
	}()
	return routeStream, nil
}

func (c *ControllerStore) toRouterEventType(typ ct.EventType) router.EventType {
	switch typ {
	case ct.EventTypeRoute:
		return router.EventTypeRouteSet
	case ct.EventTypeRouteDeletion:
		return router.EventTypeRouteRemove
	default:
		return router.EventType("")
	}
}
