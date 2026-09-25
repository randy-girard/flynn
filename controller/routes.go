package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/schema"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

func (c *controllerAPI) CreateRoute(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var route router.Route
	if err := httphelper.DecodeJSON(req, &route); err != nil {
		respondWithError(w, err)
		return
	}
	route.ParentRef = routeParentRef(c.getApp(ctx).ID)

	if err := schema.Validate(&route); err != nil {
		respondWithError(w, err)
		return
	}

	if router.HTTPPathRequiresClusterAdmin(route.Path) {
		tok := authz.TokenFromContext(ctx)
		if tok == nil || !tok.HasClusterAdmin() {
			httphelper.Forbidden(w, "path-based HTTP routes can only be created with flynn-host cluster administrator credentials")
			return
		}
	}

	if !c.enforceRouteServiceOwnership(ctx, w, c.getApp(ctx), route.Service) {
		return
	}
	if err := c.checkHostname(ctx, c.getApp(ctx), route.Domain); err != nil {
		respondWithError(w, err)
		return
	}

	// Check if ACME is enabled when managed certificate is requested
	if route.ManagedCertificateDomain != nil && *route.ManagedCertificateDomain != "" {
		enabled, err := c.acmeConfigRepo.IsEnabled()
		if err != nil {
			respondWithError(w, err)
			return
		}
		if !enabled {
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.ValidationErrorCode,
				Message: "ACME/Let's Encrypt is not enabled. Run 'flynn-host acme:configure' and 'flynn-host acme:enable' first.",
			})
			return
		}
	}

	err := c.routeRepo.Add(&route)
	if err != nil {
		rjson, jerr := json.Marshal(&route)
		if jerr != nil {
			httphelper.Error(w, jerr)
			return
		}
		jsonError := httphelper.JSONError{Detail: rjson}
		switch err {
		case data.ErrRouteConflict:
			jsonError.Code = httphelper.ConflictErrorCode
			jsonError.Message = "Duplicate route"
		case data.ErrRouteReserved:
			jsonError.Code = httphelper.ConflictErrorCode
			jsonError.Message = "Port reserved for HTTP/HTTPS traffic"
		case data.ErrRouteUnreservedHTTP:
			jsonError.Code = httphelper.ValidationErrorCode
			jsonError.Message = "Port not reserved for HTTP traffic"
		case data.ErrRouteUnreservedHTTPS:
			jsonError.Code = httphelper.ValidationErrorCode
			jsonError.Message = "Port not reserved for HTTPS traffic"
		case data.ErrRouteInvalid:
			jsonError.Code = httphelper.ValidationErrorCode
			jsonError.Message = "Invalid route"
		default:
			httphelper.Error(w, err)
			return
		}
		httphelper.Error(w, jsonError)
		return
	}

	httphelper.JSON(w, 200, &route)
}

func (c *controllerAPI) GetRoute(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	route, err := c.getRoute(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	data.MarkIncludedRoutes(c.getApp(ctx), c.appRepo.DefaultDomain(), route)
	httphelper.JSON(w, 200, route)
}

type sortedRoutes []*router.Route

func (p sortedRoutes) Len() int           { return len(p) }
func (p sortedRoutes) Less(i, j int) bool { return p[i].CreatedAt.After(p[j].CreatedAt) }
func (p sortedRoutes) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

func (c *controllerAPI) GetRouteList(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	routes, err := c.routeRepo.List("")
	if err != nil {
		respondWithError(w, err)
		return
	}
	routes = c.filterRoutes(ctx, routes)
	sort.Sort(sortedRoutes(routes))
	httphelper.JSON(w, 200, routes)
}

func (c *controllerAPI) GetAppRouteList(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	routes, err := c.routeRepo.List(routeParentRef(app.ID))
	if err != nil {
		respondWithError(w, err)
		return
	}
	routes, err = c.appRepo.EnsureIncludedRoute(app, routes)
	if err != nil {
		respondWithError(w, err)
		return
	}
	sort.Sort(sortedRoutes(routes))
	httphelper.JSON(w, 200, routes)
}

func (c *controllerAPI) UpdateRoute(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)

	var route router.Route
	if err := httphelper.DecodeJSON(req, &route); err != nil {
		respondWithError(w, err)
		return
	}
	route.Type = params.ByName("routes_type")
	route.ID = params.ByName("routes_id")

	existing, err := c.getRoute(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	app := c.getApp(ctx)
	if data.IsIncludedHTTPRoute(existing, app, c.appRepo.DefaultDomain()) {
		if incoming := strings.TrimSpace(route.Domain); incoming != "" && !strings.EqualFold(incoming, existing.Domain) {
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.ValidationErrorCode,
				Message: fmt.Sprintf("the included HTTP route %s cannot change domain", existing.Domain),
			})
			return
		}
		if route.Path != "" && data.HTTPRoutePath(route.Path) != data.HTTPRoutePath(existing.Path) {
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.ValidationErrorCode,
				Message: fmt.Sprintf("the included HTTP route %s cannot change path", existing.Domain),
			})
			return
		}
		route.Domain = existing.Domain
		route.Path = existing.Path
	}

	if !c.enforceRouteServiceOwnership(ctx, w, c.getApp(ctx), route.Service) {
		return
	}

	// Check if ACME is enabled when managed certificate is requested
	if route.ManagedCertificateDomain != nil && *route.ManagedCertificateDomain != "" {
		enabled, err := c.acmeConfigRepo.IsEnabled()
		if err != nil {
			respondWithError(w, err)
			return
		}
		if !enabled {
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.ValidationErrorCode,
				Message: "ACME/Let's Encrypt is not enabled. Run 'flynn-host acme:configure' and 'flynn-host acme:enable' first.",
			})
			return
		}
	}

	if err := c.routeRepo.Update(&route); err != nil {
		if err == data.ErrRouteNotFound {
			err = ErrNotFound
		}
		respondWithError(w, err)
		return
	}
	data.MarkIncludedRoutes(c.getApp(ctx), c.appRepo.DefaultDomain(), &route)
	httphelper.JSON(w, 200, route)
}

func (c *controllerAPI) DeleteRoute(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	route, err := c.getRoute(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	app := c.getApp(ctx)
	if data.IsIncludedHTTPRoute(route, app, c.appRepo.DefaultDomain()) && req.URL.Query().Get("app_deletion") != "1" {
		httphelper.Forbidden(w, fmt.Sprintf("the included HTTP route %s cannot be removed", route.Domain))
		return
	}

	if err := c.routeRepo.Delete(route); err != nil {
		if err == data.ErrRouteInvalid {
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.ValidationErrorCode,
				Message: "Route has dependent routes",
			})
			return
		}
		if err == data.ErrRouteNotFound {
			err = ErrNotFound
		}
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}
