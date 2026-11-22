package main

import (
	"net/http"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestBuildLockAdmitsOneHolder (EDGE-09): one holder per app. A second holder
// is refused with the first holder's description until the first releases or
// stops renewing; the holder itself may renew.
func (s *S) TestBuildLockAdmitsOneHolder(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "build-lock-one-holder"})
	other := s.createTestApp(c, &ct.App{Name: "build-lock-other-app"})

	first, err := s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "push-a", Description: "a git push", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(first.Holder, Equals, "push-a")
	c.Assert(first.AppID, Equals, app.ID)
	c.Assert(first.AcquiredAt, NotNil)
	c.Assert(first.ExpiresAt, NotNil)

	_, err = s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "push-b", Description: "a GitHub deploy", TTLSeconds: 60})
	held, ok := err.(*ct.BuildLockHeldError)
	c.Assert(ok, Equals, true, Commentf("err = %#v", err))
	c.Assert(held.Lock.Holder, Equals, "push-a")
	c.Assert(held.Lock.Description, Equals, "a git push")
	c.Assert(held.Lock.AcquiredAt.Equal(*first.AcquiredAt), Equals, true)

	// another app is not affected
	_, err = s.c.AcquireBuildLock(other.ID, &ct.BuildLock{Holder: "push-b", TTLSeconds: 60})
	c.Assert(err, IsNil)

	// renewing keeps the start time and moves the expiry
	time.Sleep(10 * time.Millisecond)
	renewed, err := s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "push-a", Description: "a git push", TTLSeconds: 120})
	c.Assert(err, IsNil)
	c.Assert(renewed.AcquiredAt.Equal(*first.AcquiredAt), Equals, true)
	c.Assert(renewed.ExpiresAt.After(*first.ExpiresAt), Equals, true)

	// a release by someone else leaves it held
	c.Assert(s.c.ReleaseBuildLock(app.ID, "push-b"), IsNil)
	_, err = s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "push-b", TTLSeconds: 60})
	c.Assert(err, FitsTypeOf, &ct.BuildLockHeldError{})

	c.Assert(s.c.ReleaseBuildLock(app.ID, "push-a"), IsNil)
	second, err := s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "push-b", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(second.Holder, Equals, "push-b")
	c.Assert(s.c.ReleaseBuildLock(app.ID, "push-b"), IsNil)
	c.Assert(s.c.ReleaseBuildLock(other.ID, "push-b"), IsNil)
}

// TestBuildLockExpires: a holder that stops renewing (its gitreceive or taffy
// job died) frees the app once its lease runs out.
func (s *S) TestBuildLockExpires(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "build-lock-expires"})
	_, err := s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "dead", TTLSeconds: 1})
	c.Assert(err, IsNil)
	_, err = s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "next", TTLSeconds: 60})
	c.Assert(err, FitsTypeOf, &ct.BuildLockHeldError{})
	time.Sleep(1100 * time.Millisecond)
	got, err := s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "next", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(got.Holder, Equals, "next")
	c.Assert(s.c.ReleaseBuildLock(app.ID, "next"), IsNil)
}

// TestBuildLockValidation: a lease needs a holder and a bounded TTL.
func (s *S) TestBuildLockValidation(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "build-lock-validation"})
	_, err := s.c.AcquireBuildLock(app.ID, &ct.BuildLock{TTLSeconds: 60})
	c.Assert(err, NotNil)
	_, err = s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "x"})
	c.Assert(err, NotNil)
	_, err = s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "x", TTLSeconds: int(ct.MaxBuildLockTTL/time.Second) + 1})
	c.Assert(err, NotNil)
	_, err = s.c.AcquireBuildLock("no-such-app-build-lock", &ct.BuildLock{Holder: "x", TTLSeconds: 60})
	c.Assert(err, NotNil)
}

// TestBuildLockIsClusterOnly: an app owner cannot hold or drop the lease
// (that would block or race every deploy of the app).
func (s *S) TestBuildLockIsClusterOnly(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "build-lock-cluster-only"})
	tenant := s.scopedClient(c, app, "app:admin", "app:write", "app:deploy")
	_, err := tenant.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "tenant", TTLSeconds: 60})
	c.Assert(err, NotNil)
	c.Assert(tenant.ReleaseBuildLock(app.ID, "tenant"), NotNil)

	code, _ := s.scopedStatus(c, http.MethodPut, s.receiveToken(c, app), "/apps/"+app.ID+"/build-lock", `{"holder":"r","ttl_seconds":60}`)
	c.Assert(code, Equals, http.StatusForbidden)
}

func (s *S) receiveToken(c *C, app *ct.App) string {
	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "receive-lock",
		UserEmail:  "receive:" + app.Name,
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeGitreceiveBuild},
		AppGrants:  []*api.AppGrant{{AppId: app.ID, Permissions: []string{"app:write"}}},
	})
	c.Assert(err, IsNil)
	return raw
}
