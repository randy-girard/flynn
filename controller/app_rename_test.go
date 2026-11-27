package main

import (
	"strings"

	. "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
	router "github.com/randy-girard/flynn/router/types"
)

func webProcesses(service string) map[string]ct.ProcessType {
	return map[string]ct.ProcessType{"web": {
		Args:    []string{"web"},
		Service: service,
		Ports: []ct.Port{{Port: 8080, Proto: "tcp", Service: &host.Service{
			Name: service, Create: true,
		}}},
	}}
}

// NEW-59: renaming an app whose release declares "<old>-web" is refused
// with a 400 that says why, instead of leaving the next push or job start to
// fail CORE-17's service-name check.
func (s *S) TestRenameAppWithOwnedServicesIsRefused(c *C) {
	name := "rename-ref-" + random.String(6)
	app := s.createTestApp(c, &ct.App{Name: name})
	release := s.createTestRelease(c, app.ID, &ct.Release{Processes: webProcesses(name + "-web")})
	c.Assert(s.c.SetAppRelease(app.ID, release.ID), IsNil)

	err := s.c.UpdateApp(&ct.App{ID: app.ID, Name: name + "-v2"})
	c.Assert(hh.IsValidationError(err), Equals, true, Commentf("err = %v", err))
	c.Assert(strings.Contains(err.Error(), name+"-web"), Equals, true, Commentf("err = %v", err))

	got, err := s.c.GetApp(app.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Name, Equals, name)
}

// A rename the check allows leaves the app able to release under its new
// name, and its route to "<old>-web" follows to "<new>-web".
func (s *S) TestRenameAppThenReleaseUnderNewName(c *C) {
	name := "rename-ok-" + random.String(6)
	app := s.createTestApp(c, &ct.App{Name: name})
	c.Assert(s.c.CreateRoute(app.ID, (&router.HTTPRoute{
		Domain:  name + ".example.com",
		Service: name + "-web",
	}).ToRoute()), IsNil)
	next := name + "-v2"
	c.Assert(s.c.UpdateApp(&ct.App{ID: app.ID, Name: next}), IsNil)

	release := s.createTestRelease(c, app.ID, &ct.Release{Processes: webProcesses(next + "-web")})
	c.Assert(s.c.SetAppRelease(app.ID, release.ID), IsNil)

	routes, err := s.c.AppRouteList(app.ID)
	c.Assert(err, IsNil)
	services := map[string]bool{}
	for _, r := range routes {
		services[r.Service] = true
	}
	c.Assert(services[name+"-web"], Equals, false, Commentf("routes %+v", routes))
	c.Assert(services[next+"-web"], Equals, true, Commentf("routes %+v", routes))
}
