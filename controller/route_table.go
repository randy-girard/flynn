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
}

// routeTable registers controller HTTP routes. Every route goes through it,
// so the list of routes can be walked by tests.
type routeTable struct {
	router *httprouter.Router
	routes []registeredRoute
}

func newRouteTable() *routeTable {
	return &routeTable{router: httprouter.New()}
}

func (t *routeTable) handle(method, pattern string, h httprouter.Handle) {
	t.routes = append(t.routes, registeredRoute{Method: method, Pattern: pattern})
	t.router.Handle(method, pattern, h)
}

func (t *routeTable) handler(method, pattern string, h http.Handler) {
	t.handle(method, pattern, func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
		h.ServeHTTP(w, req)
	})
}

func (t *routeTable) GET(pattern string, h httprouter.Handle)  { t.handle(http.MethodGet, pattern, h) }
func (t *routeTable) POST(pattern string, h httprouter.Handle) { t.handle(http.MethodPost, pattern, h) }
func (t *routeTable) PUT(pattern string, h httprouter.Handle)  { t.handle(http.MethodPut, pattern, h) }
func (t *routeTable) PATCH(pattern string, h httprouter.Handle) {
	t.handle(http.MethodPatch, pattern, h)
}
func (t *routeTable) DELETE(pattern string, h httprouter.Handle) {
	t.handle(http.MethodDelete, pattern, h)
}

// preAuthPublic reports whether muxHandler serves this request before it
// authenticates the caller.
func preAuthPublic(method, path string) bool {
	// GitHub signs the webhook body; the handler checks the signature.
	if path == "/github/webhook" && method == http.MethodPost {
		return true
	}
	// GET /ca-cert is TOFU: cluster:add must download the CA before the
	// key is known to be valid. A wrong Basic password used to 401 here
	// even though the same request with no credentials succeeded.
	if path == "/ca-cert" && (method == http.MethodGet || method == http.MethodHead) {
		return true
	}
	// GET /cluster/region is the peer dial. A wrong Basic password must
	// not 401 it, and the handler must not require CONTROLLER_KEY.
	if authz.ClusterRegionPublic(method, path) {
		return true
	}
	return oauthPublicPath(method, path)
}

// routeAllowed is the authz decision muxHandler applies to an authenticated
// request.
func routeAllowed(tok *authorizer.Token, method, path string, resolve func(string) string) bool {
	return authz.HTTPAllowedResolved(tok, method, path, resolve)
}
