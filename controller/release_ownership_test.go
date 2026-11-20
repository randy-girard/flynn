package main

import (
	"fmt"
	"strings"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/authz"
	controller "github.com/randy-girard/flynn/controller/client"
	v1controller "github.com/randy-girard/flynn/controller/client/v1"
	ct "github.com/randy-girard/flynn/controller/types"
)

// CORE-18: deploy, formation, scale and release-delete routes take a
// release id, and an app-scoped caller must not be able to point its own
// app at another app's release (and so run it with that release's env).

// ownershipFixture makes two apps, each with a release that carries a
// secret env var, and a client scoped to app "mine" only.
type ownershipFixture struct {
	mine, other       *ct.App
	mineRel, otherRel *ct.Release
	tenant            controller.Client
}

const ownershipSecret = "other-app-secret-value"

func (s *S) newOwnershipFixture(c *C, name string) *ownershipFixture {
	f := &ownershipFixture{}
	f.mine = s.createTestApp(c, &ct.App{Name: name + "-mine"})
	f.other = s.createTestApp(c, &ct.App{Name: name + "-other"})
	f.mineRel = s.createTestRelease(c, f.mine.ID, &ct.Release{
		Env:       map[string]string{"MINE": "1"},
		Processes: map[string]ct.ProcessType{"web": {Args: []string{"web"}}},
	})
	f.otherRel = s.createTestRelease(c, f.other.ID, &ct.Release{
		Env:       map[string]string{"SECRET": ownershipSecret},
		Processes: map[string]ct.ProcessType{"web": {Args: []string{"web"}}},
	})
	f.tenant = s.scopedClient(c, f.mine,
		authz.PermAppDeploy, authz.PermAppWrite, authz.PermAppScaleWrite, authz.PermAppScaleRead,
		authz.PermAppActivityRead, authz.PermAppRead)
	return f
}

func (f *ownershipFixture) http() *v1controller.Client {
	return f.tenant.(*v1controller.Client)
}

// assertNotLeaked checks that a refusal does not tell the caller whether
// the release exists or which app owns it.
func assertNotLeaked(c *C, err error, f *ownershipFixture) {
	c.Assert(err, NotNil)
	msg := err.Error()
	c.Assert(strings.Contains(msg, ownershipSecret), Equals, false, Commentf("%s", msg))
	c.Assert(strings.Contains(msg, f.other.ID), Equals, false, Commentf("%s", msg))
	c.Assert(strings.Contains(msg, f.other.Name), Equals, false, Commentf("%s", msg))
	c.Assert(strings.Contains(msg, "belong"), Equals, false, Commentf("%s", msg))
}

func (s *S) TestDeployRejectsReleaseOfAnotherApp(c *C) {
	f := s.newOwnershipFixture(c, "core18-deploy")

	_, err := f.tenant.CreateDeployment(f.mine.ID, f.otherRel.ID)
	assertNotLeaked(c, err, f)
	// The refusal reads the same as a release id that does not exist.
	_, missingErr := f.tenant.CreateDeployment(f.mine.ID, "00000000-0000-0000-0000-000000000000")
	c.Assert(missingErr, NotNil)
	c.Assert(err.Error(), Equals, strings.Replace(missingErr.Error(), "00000000-0000-0000-0000-000000000000", f.otherRel.ID, 1))

	// Nothing was recorded or switched on the caller's app.
	list, err := s.c.DeploymentList(f.mine.ID)
	c.Assert(err, IsNil)
	c.Assert(list, HasLen, 0)
	_, err = s.c.GetAppRelease(f.mine.ID)
	c.Assert(err, Equals, controller.ErrNotFound)

	// The cluster key cannot either: deploy is strict like PUT /release.
	_, err = s.c.CreateDeployment(f.mine.ID, f.otherRel.ID)
	c.Assert(err, NotNil)
	list, err = s.c.DeploymentList(f.mine.ID)
	c.Assert(err, IsNil)
	c.Assert(list, HasLen, 0)
}

func (s *S) TestDeployOwnReleaseStillWorks(c *C) {
	f := s.newOwnershipFixture(c, "core18-deploy-own")
	d, err := f.tenant.CreateDeployment(f.mine.ID, f.mineRel.ID)
	c.Assert(err, IsNil)
	c.Assert(d.NewReleaseID, Equals, f.mineRel.ID)
	got, err := s.c.GetAppRelease(f.mine.ID)
	c.Assert(err, IsNil)
	c.Assert(got.ID, Equals, f.mineRel.ID)

	// Rollback: deploy a second release, then the first one again.
	second := s.createTestRelease(c, f.mine.ID, &ct.Release{Env: map[string]string{"MINE": "2"}})
	_, err = f.tenant.CreateDeployment(f.mine.ID, second.ID)
	c.Assert(err, IsNil)
	_, err = f.tenant.CreateDeployment(f.mine.ID, f.mineRel.ID)
	c.Assert(err, IsNil)
}

func (s *S) TestPutFormationRejectsReleaseOfAnotherApp(c *C) {
	f := s.newOwnershipFixture(c, "core18-formation")

	err := f.tenant.PutFormation(&ct.Formation{AppID: f.mine.ID, ReleaseID: f.otherRel.ID, Processes: map[string]int{"web": 0}})
	c.Assert(err, Equals, controller.ErrNotFound)
	_, err = s.c.GetFormation(f.mine.ID, f.otherRel.ID)
	c.Assert(err, Equals, controller.ErrNotFound)

	// Same app works.
	c.Assert(f.tenant.PutFormation(&ct.Formation{AppID: f.mine.ID, ReleaseID: f.mineRel.ID, Processes: map[string]int{"web": 0}}), IsNil)

	// The cluster key keeps cross-app formations (flynn release:delete test
	// and operator repair use them).
	c.Assert(s.c.PutFormation(&ct.Formation{AppID: f.mine.ID, ReleaseID: f.otherRel.ID, Processes: map[string]int{"web": 0}}), IsNil)
	c.Assert(s.c.DeleteFormation(f.mine.ID, f.otherRel.ID), IsNil)
}

func (s *S) TestPutScaleRequestRejectsReleaseOfAnotherApp(c *C) {
	f := s.newOwnershipFixture(c, "core18-scale")
	procs := map[string]int{"web": 0}
	body := &ct.ScaleRequest{NewProcesses: &procs, State: ct.ScaleRequestStatePending}
	err := f.http().Put(fmt.Sprintf("/apps/%s/scale/%s", f.mine.ID, f.otherRel.ID), body, &ct.ScaleRequest{})
	c.Assert(err, Equals, controller.ErrNotFound)
	_, err = s.c.GetFormation(f.mine.ID, f.otherRel.ID)
	c.Assert(err, Equals, controller.ErrNotFound)

	body = &ct.ScaleRequest{NewProcesses: &procs, State: ct.ScaleRequestStatePending}
	c.Assert(f.http().Put(fmt.Sprintf("/apps/%s/scale/%s", f.mine.ID, f.mineRel.ID), body, &ct.ScaleRequest{}), IsNil)
}

func (s *S) TestDeleteFormationRejectsReleaseOfAnotherApp(c *C) {
	f := s.newOwnershipFixture(c, "core18-delformation")
	// The other app's own formation for its release, and a cross-app one
	// an operator put on the caller's app.
	c.Assert(s.c.PutFormation(&ct.Formation{AppID: f.other.ID, ReleaseID: f.otherRel.ID, Processes: map[string]int{"web": 0}}), IsNil)
	c.Assert(s.c.PutFormation(&ct.Formation{AppID: f.mine.ID, ReleaseID: f.otherRel.ID, Processes: map[string]int{"web": 0}}), IsNil)

	err := f.tenant.DeleteFormation(f.mine.ID, f.otherRel.ID)
	c.Assert(err, Equals, controller.ErrNotFound)
	_, err = s.c.GetFormation(f.other.ID, f.otherRel.ID)
	c.Assert(err, IsNil)
	_, err = s.c.GetFormation(f.mine.ID, f.otherRel.ID)
	c.Assert(err, IsNil)
	c.Assert(s.c.DeleteFormation(f.mine.ID, f.otherRel.ID), IsNil)

	c.Assert(f.tenant.PutFormation(&ct.Formation{AppID: f.mine.ID, ReleaseID: f.mineRel.ID, Processes: map[string]int{"web": 0}}), IsNil)
	c.Assert(f.tenant.DeleteFormation(f.mine.ID, f.mineRel.ID), IsNil)
}

func (s *S) TestDeleteReleaseRejectsReleaseOfAnotherApp(c *C) {
	f := s.newOwnershipFixture(c, "core18-delrelease")

	err := f.http().Delete(fmt.Sprintf("/apps/%s/releases/%s", f.mine.ID, f.otherRel.ID), nil)
	c.Assert(err, Equals, controller.ErrNotFound)
	got, err := s.c.GetRelease(f.otherRel.ID)
	c.Assert(err, IsNil)
	c.Assert(got.ID, Equals, f.otherRel.ID)
	rels, err := s.c.AppReleaseList(f.other.ID)
	c.Assert(err, IsNil)
	c.Assert(rels, HasLen, 1)

	// Own (not current) release can still be deleted.
	c.Assert(f.http().Delete(fmt.Sprintf("/apps/%s/releases/%s", f.mine.ID, f.mineRel.ID), nil), IsNil)
	_, err = s.c.GetRelease(f.mineRel.ID)
	c.Assert(err, Equals, controller.ErrNotFound)
}
