package main

import (
	"net/http"
	"strings"
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

// TestKeyedBuildLockAdmitsOneHolder (EDGE-40): a lease named by a key that
// is not an app (a pipeline's preview remote) has the same rules as an
// app's: one live holder, renewable by it, free after release.
func (s *S) TestKeyedBuildLockAdmitsOneHolder(c *C) {
	key := ct.PipelineBuildLockKey("keyed-lock-shop")
	first, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "push-a", Description: "a git push", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(first.Key, Equals, key)
	c.Assert(first.Holder, Equals, "push-a")
	c.Assert(first.AcquiredAt, NotNil)

	_, err = s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "push-b", Description: "a git push", TTLSeconds: 60})
	held, ok := err.(*ct.BuildLockHeldError)
	c.Assert(ok, Equals, true, Commentf("err = %#v", err))
	c.Assert(held.Lock.Holder, Equals, "push-a")
	c.Assert(held.Lock.Key, Equals, key)
	c.Assert(held.Error(), Matches, `a git push to this pipeline is in progress \(started .*\)`)

	// another key, and an app lease, are separate
	other, err := s.c.AcquireKeyedBuildLock(ct.PipelineBuildLockKey("keyed-lock-other"), &ct.BuildLock{Holder: "push-b", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(s.c.ReleaseKeyedBuildLock(other.Key, "push-b"), IsNil)
	app := s.createTestApp(c, &ct.App{Name: "keyed-lock-shop"})
	_, err = s.c.AcquireBuildLock(app.ID, &ct.BuildLock{Holder: "push-b", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(s.c.ReleaseBuildLock(app.ID, "push-b"), IsNil)

	time.Sleep(10 * time.Millisecond)
	renewed, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "push-a", Description: "a git push", TTLSeconds: 120})
	c.Assert(err, IsNil)
	c.Assert(renewed.AcquiredAt.Equal(*first.AcquiredAt), Equals, true)
	c.Assert(renewed.ExpiresAt.After(*first.ExpiresAt), Equals, true)

	c.Assert(s.c.ReleaseKeyedBuildLock(key, "push-b"), IsNil)
	_, err = s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "push-b", TTLSeconds: 60})
	c.Assert(err, FitsTypeOf, &ct.BuildLockHeldError{})
	c.Assert(s.c.ReleaseKeyedBuildLock(key, "push-a"), IsNil)
	second, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "push-b", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(second.Holder, Equals, "push-b")
	c.Assert(s.c.ReleaseKeyedBuildLock(key, "push-b"), IsNil)
}

// TestKeyedBuildLockExpires: a holder that stops renewing frees the key.
func (s *S) TestKeyedBuildLockExpires(c *C) {
	key := ct.PipelineBuildLockKey("keyed-lock-expires")
	_, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "dead", TTLSeconds: 1})
	c.Assert(err, IsNil)
	_, err = s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "next", TTLSeconds: 60})
	c.Assert(err, FitsTypeOf, &ct.BuildLockHeldError{})
	time.Sleep(1100 * time.Millisecond)
	got, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "next", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(got.Holder, Equals, "next")
	c.Assert(s.c.ReleaseKeyedBuildLock(key, "next"), IsNil)
}

// TestKeyedBuildLockValidation: the key is <namespace>:<id>, and the holder
// and TTL rules are the app lease's.
func (s *S) TestKeyedBuildLockValidation(c *C) {
	for _, key := range []string{"shop", "pipeline:", ":shop", "Pipeline:shop", "pipeline:-shop", "pipeline:" + strings.Repeat("a", 201)} {
		_, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "x", TTLSeconds: 60})
		c.Assert(err, NotNil, Commentf("key %q", key))
		c.Assert(err, Not(FitsTypeOf), &ct.BuildLockHeldError{})
		c.Assert(s.c.ReleaseKeyedBuildLock(key, "x"), NotNil, Commentf("key %q", key))
	}
	key := ct.PipelineBuildLockKey("keyed-lock-validation")
	_, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{TTLSeconds: 60})
	c.Assert(err, NotNil)
	_, err = s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "x"})
	c.Assert(err, NotNil)
	_, err = s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "x", TTLSeconds: int(ct.MaxBuildLockTTL/time.Second) + 1})
	c.Assert(err, NotNil)
	got, err := s.c.AcquireKeyedBuildLock("pipeline:"+strings.Repeat("a", 200), &ct.BuildLock{Holder: "x", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(s.c.ReleaseKeyedBuildLock(got.Key, "x"), IsNil)
}

// TestKeyedBuildLockIsClusterOnly: an app admin or a receive token cannot
// hold or drop a pipeline's lease.
func (s *S) TestKeyedBuildLockIsClusterOnly(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "keyed-lock-cluster-only"})
	tenant := s.scopedClient(c, app, "app:admin", "app:write", "app:deploy")
	key := ct.PipelineBuildLockKey("keyed-lock-cluster-only")
	_, err := tenant.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "tenant", TTLSeconds: 60})
	c.Assert(err, NotNil)
	c.Assert(tenant.ReleaseKeyedBuildLock(key, "tenant"), NotNil)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		path := "/build-locks/" + key
		if method == http.MethodDelete {
			path += "/r"
		}
		code, _ := s.scopedStatus(c, method, s.receiveToken(c, app), path, `{"holder":"r","ttl_seconds":60}`)
		c.Assert(code, Equals, http.StatusForbidden, Commentf("%s %s", method, path))
	}
	// the tenant's attempts took nothing
	got, err := s.c.AcquireKeyedBuildLock(key, &ct.BuildLock{Holder: "cluster", TTLSeconds: 60})
	c.Assert(err, IsNil)
	c.Assert(s.c.ReleaseKeyedBuildLock(got.Key, "cluster"), IsNil)
}
