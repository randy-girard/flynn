package main

import (
	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func portServiceRelease(appID, artifactID, service string) *ct.Release {
	return &ct.Release{
		AppID:       appID,
		ArtifactIDs: []string{artifactID},
		Processes: map[string]ct.ProcessType{"web": {
			Args: []string{"web"},
			Ports: []ct.Port{{Port: 8080, Proto: "tcp", Service: &host.Service{
				Name: service, Create: true,
			}}},
		}},
	}
}

// CORE-17 / EDGE-01: a tenant release may not declare a discoverd service
// owned by a system app or another tenant app.
func (s *S) TestCreateReleaseRejectsForeignPortService(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "core17-shop"})
	s.createTestApp(c, &ct.App{Name: "core17-other"})
	s.createTestApp(c, &ct.App{Name: "core17-shop-admin"})
	art := s.createTestArtifact(c, &ct.Artifact{})
	tenant := s.scopedClient(c, app, authz.PermAppEnvWrite)
	for _, svc := range []string{"controller", "blobstore", "acme-challenge", "postgres", "core17-other-web", "core17-shop-admin-web"} {
		for name, client := range map[string]interface {
			CreateRelease(string, *ct.Release) error
		}{"tenant": tenant, "admin": s.c} {
			err := client.CreateRelease(app.ID, portServiceRelease(app.ID, art.ID, svc))
			c.Assert(err, NotNil, Commentf("%s %s", name, svc))
			c.Assert(hh.IsValidationError(err), Equals, true, Commentf("%s %s: %v", name, svc, err))
		}
	}
	// ProcessType.Service follows the same rule.
	rel := &ct.Release{AppID: app.ID, ArtifactIDs: []string{art.ID}, Processes: map[string]ct.ProcessType{
		"web": {Args: []string{"web"}, Service: "controller"},
	}}
	err := tenant.CreateRelease(app.ID, rel)
	c.Assert(hh.IsValidationError(err), Equals, true, Commentf("%v", err))

	for _, svc := range []string{"core17-shop-web", "core17-shop", "core17-shop-api"} {
		c.Assert(tenant.CreateRelease(app.ID, portServiceRelease(app.ID, art.ID, svc)), IsNil, Commentf(svc))
	}
}

func (s *S) TestTenantReleaseCannotDeclareForeignService(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "edge01-app"})
	s.createTestApp(c, &ct.App{Name: "other-app"})
	art := s.createTestArtifact(c, &ct.Artifact{})
	tenant := s.scopedClient(c, app, authz.PermAppEnvWrite)
	for _, svc := range []string{"acme-challenge", "blobstore", "other-app-web"} {
		err := tenant.CreateRelease(app.ID, portServiceRelease(app.ID, art.ID, svc))
		c.Assert(hh.IsValidationError(err), Equals, true, Commentf("%s: %v", svc, err))
	}
	c.Assert(tenant.CreateRelease(app.ID, portServiceRelease(app.ID, art.ID, "edge01-app-web")), IsNil)
}

func (s *S) TestSetAppReleaseRejectsForeignPortService(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "core17-set"})
	art := s.createTestArtifact(c, &ct.Artifact{})
	// a release stored before the rule existed
	rel := portServiceRelease(app.ID, art.ID, "controller")
	c.Assert(s.api.releaseRepo.Add(rel), IsNil)
	err := s.c.SetAppRelease(app.ID, rel.ID)
	c.Assert(hh.IsValidationError(err), Equals, true, Commentf("%v", err))
	cur, err := s.c.GetAppRelease(app.ID)
	if err == nil {
		c.Assert(cur.ID, Not(Equals), rel.ID)
	}

	ok := portServiceRelease(app.ID, art.ID, "core17-set-web")
	c.Assert(s.c.CreateRelease(app.ID, ok), IsNil)
	c.Assert(s.c.SetAppRelease(app.ID, ok.ID), IsNil)
}

func (s *S) TestSystemAppReleaseKeepsServiceNames(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "core17-sys", Meta: map[string]string{"flynn-system-app": "true"}})
	art := s.createTestArtifact(c, &ct.Artifact{})
	c.Assert(s.c.CreateRelease(app.ID, portServiceRelease(app.ID, art.ID, "acme-challenge")), IsNil)
}
