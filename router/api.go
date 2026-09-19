package main

import (
	"net/http"
	"strings"

	"github.com/julienschmidt/httprouter"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/pprof"
	"github.com/randy-girard/flynn/pkg/sse"
	"github.com/randy-girard/flynn/pkg/status"
	"github.com/randy-girard/flynn/router/proxy"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

type API struct {
	router *Router
}

func apiHandler(rtr *Router) http.Handler {
	api := &API{router: rtr}
	r := httprouter.New()

	r.HandlerFunc("GET", status.Path, status.HealthyHandler.ServeHTTP)

	r.GET("/events", httphelper.WrapHandler(api.StreamEvents))
	r.GET("/metrics", httphelper.WrapHandler(api.GetMetrics))

	r.HandlerFunc("GET", "/debug/*path", pprof.Handler.ServeHTTP)

	return httphelper.ContextInjector("router", httphelper.NewRequestLogger(r))
}

func (api *API) StreamEvents(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	log, _ := ctxhelper.LoggerFromContext(ctx)

	httpListener := api.router.ListenerFor("http")
	tcpListener := api.router.ListenerFor("tcp")

	httpEvents := make(chan *router.Event, 32)
	tcpEvents := make(chan *router.Event, 32)
	sseEvents := make(chan *router.StreamEvent)
	go httpListener.Watch(httpEvents, true)
	go tcpListener.Watch(tcpEvents, true)
	defer httpListener.Unwatch(httpEvents)
	defer tcpListener.Unwatch(tcpEvents)

	reqTypes := strings.Split(req.URL.Query().Get("types"), ",")
	eventTypes := make(map[router.EventType]struct{}, len(reqTypes))
	for _, typ := range reqTypes {
		eventTypes[router.EventType(typ)] = struct{}{}
	}

	sendEvents := func(events chan *router.Event) {
		for {
			select {
			case e, ok := <-events:
				if !ok {
					return
				}
				if _, ok := eventTypes[e.Event]; !ok {
					continue
				}
				sseEvents <- &router.StreamEvent{
					Event:   e.Event,
					Route:   e.Route,
					Backend: e.Backend,
					Error:   e.Error,
				}
			case <-ctx.Done():
				return
			}
		}
	}
	go sendEvents(httpEvents)
	go sendEvents(tcpEvents)
	sse.ServeStream(w, sseEvents, log)
}

func (api *API) GetMetrics(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	httphelper.JSON(w, 200, proxy.Snapshot())
}
