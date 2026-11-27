package main

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
)

// registeredRoute is one method and pattern the controller serves.
type registeredRoute struct {
	Method  string
	Pattern string
	Req     authz.Requirement
}

// routeTable registers controller HTTP routes. Each registration names the
// route's authz requirement, so a route cannot be served without one; the
// requirement is what muxHandler checks before the handler runs (CORE-15).
type routeTable struct {
	router *httprouter.Router
	authz  *authz.RouteTable
	routes []registeredRoute
}

func newRouteTable() *routeTable {
	return &routeTable{router: httprouter.New(), authz: authz.NewRouteTable()}
}

func (t *routeTable) handle(method, pattern string, req authz.Requirement, h httprouter.Handle) {
	t.authz.Add(method, pattern, req)
	t.routes = append(t.routes, registeredRoute{Method: method, Pattern: pattern, Req: req})
	t.router.Handle(method, pattern, h)
}

func (t *routeTable) handler(method, pattern string, req authz.Requirement, h http.Handler) {
	t.handle(method, pattern, req, func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		h.ServeHTTP(w, r)
	})
}

func (t *routeTable) GET(pattern string, req authz.Requirement, h httprouter.Handle) {
	t.handle(http.MethodGet, pattern, req, h)
}

func (t *routeTable) POST(pattern string, req authz.Requirement, h httprouter.Handle) {
	t.handle(http.MethodPost, pattern, req, h)
}

func (t *routeTable) PUT(pattern string, req authz.Requirement, h httprouter.Handle) {
	t.handle(http.MethodPut, pattern, req, h)
}

func (t *routeTable) PATCH(pattern string, req authz.Requirement, h httprouter.Handle) {
	t.handle(http.MethodPatch, pattern, req, h)
}

func (t *routeTable) DELETE(pattern string, req authz.Requirement, h httprouter.Handle) {
	t.handle(http.MethodDelete, pattern, req, h)
}

// controllerRoutes is the authz table of every controller route. It holds
// no handler state; muxHandler and the tests read it.
var controllerRoutes = func() *authz.RouteTable {
	rt := newRouteTable()
	registerRoutes(rt, &controllerAPI{}, func() error { return nil })
	return rt.authz
}()

// preAuthPublic reports whether muxHandler serves this request before it
// authenticates the caller: a route declared authz.Public.
func preAuthPublic(method, path string) bool {
	return controllerRoutes.Public(method, path)
}

// routeAllowed is the authz decision muxHandler applies to an authenticated
// request: the requirement of the route that serves it, or cluster-only
// when none does.
func routeAllowed(tok *authorizer.Token, method, path string, resolve func(string) string) bool {
	return controllerRoutes.Allowed(tok, method, path, resolve)
}
