package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

func (c *controllerAPI) ensureAuthRouteLoop() {
	delay := 2 * time.Second
	for i := 0; ; i++ {
		if err := c.ensureAuthRoute(); err == nil {
			return
		} else if i == 0 || i%5 == 4 {
			log.Printf("controller: auth route: %v", err)
		}
		time.Sleep(delay)
		if i == 30 {
			delay = 15 * time.Second
		}
	}
}

func (c *controllerAPI) ensureAuthRoute() error {
	if c == nil || c.appRepo == nil || c.routeRepo == nil {
		return fmt.Errorf("not ready")
	}
	domain := strings.TrimSpace(os.Getenv("DEFAULT_ROUTE_DOMAIN"))
	if domain == "" {
		return nil
	}
	authHost := "auth." + domain
	ctrlHost := "controller." + domain
	appI, err := c.appRepo.Get("controller")
	if err != nil {
		return err
	}
	app, _ := appI.(*ct.App)
	if app == nil {
		return fmt.Errorf("controller app not found")
	}
	routes, err := c.routeRepo.List(routeParentRef(app.ID))
	if err != nil {
		return err
	}
	var ctrl, existing *router.Route
	for _, r := range routes {
		if r == nil || r.Type != "http" {
			continue
		}
		if r.Domain == authHost {
			existing = r
		}
		if r.Domain == ctrlHost {
			ctrl = r
		}
	}
	if ctrl == nil {
		return fmt.Errorf("controller.%s route not found yet", domain)
	}
	want := authHTTPRoute(ctrl, authHost)
	if existing == nil {
		if err := c.routeRepo.Add(want); err != nil && err != data.ErrRouteConflict {
			return err
		}
		log.Printf("controller: added HTTP route %s", authHost)
		return nil
	}
	if !authRouteNeedsUpdate(existing, want) {
		return nil
	}
	existing.Path = "/"
	existing.Service = want.Service
	existing.DrainBackends = want.DrainBackends
	existing.Sticky = want.Sticky
	existing.Leader = want.Leader
	existing.DisableKeepAlives = want.DisableKeepAlives
	if want.Certificate != nil {
		existing.Certificate = want.Certificate
	}
	if err := c.routeRepo.Update(existing); err != nil {
		return err
	}
	log.Printf("controller: updated HTTP route %s", authHost)
	return nil
}

func authHTTPRoute(ctrl *router.Route, authHost string) *router.Route {
	route := &router.Route{
		Type:              "http",
		ParentRef:         ctrl.ParentRef,
		Service:           ctrl.Service,
		Domain:            authHost,
		Path:              "/",
		DrainBackends:     ctrl.DrainBackends,
		Sticky:            ctrl.Sticky,
		Leader:            ctrl.Leader,
		DisableKeepAlives: ctrl.DisableKeepAlives,
	}
	if ctrl.Certificate != nil && ctrl.Certificate.Cert != "" && ctrl.Certificate.Key != "" {
		route.Certificate = &router.Certificate{
			Cert: ctrl.Certificate.Cert,
			Key:  ctrl.Certificate.Key,
		}
	}
	return route
}

func authRouteNeedsUpdate(existing, want *router.Route) bool {
	if existing == nil || want == nil {
		return true
	}
	if existing.Path != "/" {
		return true
	}
	if want.Certificate != nil && (existing.Certificate == nil || existing.Certificate.Cert == "") {
		return true
	}
	return false
}
