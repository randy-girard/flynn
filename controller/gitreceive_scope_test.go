package main

import (
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/buildtoken"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *S) scopedStatus(c *C, method, auth, rawURL, body string) (int, string) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.srv.URL+rawURL, r)
	c.Assert(err, IsNil)
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, string(got)
}

// TestGitreceiveBuildTokenCannotCrossApps checks the receiver credential
// against another app's env, delete, and cluster settings, and that it can
// start a compile for its own app far enough to pass authorization.
func (s *S) TestGitreceiveBuildTokenCannotCrossApps(c *C) {
	const secret = "f4-other-app-secret"
	appA := s.createTestApp(c, &ct.App{Name: "f4-receive-own"})
	appB := s.createTestApp(c, &ct.App{Name: "f4-receive-other"})
	s.createTestRelease(c, appB.ID, &ct.Release{Env: map[string]string{"SECRET": secret}})
	artifact := s.createTestArtifact(c, &ct.Artifact{})

	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "receive-a",
		UserEmail:  "receive:f4-receive-own",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeGitreceiveBuild},
		AppGrants: []*api.AppGrant{{
			AppId:       appA.ID,
			Permissions: []string{"app:write"},
		}},
	})
	c.Assert(err, IsNil)

	code, body := s.scopedStatus(c, http.MethodGet, raw, "/apps/"+appB.ID+"/release", "")
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)

	code, body = s.scopedStatus(c, http.MethodDelete, raw, "/apps/"+appB.ID, "")
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)

	code, _ = s.scopedStatus(c, http.MethodPut, raw, "/cluster/settings", `{}`)
	c.Assert(code, Equals, http.StatusForbidden)

	code, _ = s.scopedStatus(c, http.MethodGet, raw, "/artifacts", "")
	c.Assert(code, Equals, http.StatusForbidden)

	code, body = s.scopedStatus(c, http.MethodGet, raw, "/artifacts/"+artifact.ID, "")
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(strings.Contains(body, artifact.ID), Equals, true)

	code, _ = s.scopedStatus(c, http.MethodPost, raw, "/apps/"+appA.ID+"/builds", `{}`)
	c.Assert(code, Not(Equals), http.StatusForbidden)

	admin, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "admin",
		UserEmail:  "admin@flynn.test",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{"cluster:admin"},
	})
	c.Assert(err, IsNil)
	code, _ = s.scopedStatus(c, http.MethodPost, admin, "/apps/"+appA.ID+"/builds", `{}`)
	c.Assert(code, Equals, http.StatusForbidden)
}

// TestBuildTokenCannotMintPAT: a build or receive credential carries the app
// owner's id so the owner's rate-limit bucket is shared, but it must not
// become the owner. The controller used to expand it into the owner's whole
// account: every app the owner reaches, plus POST /tokens.
func (s *S) TestBuildTokenCannotMintPAT(c *C) {
	owner := s.createLoginUser(c, "core06-owner@flynn.test", "core06-owner", "pw-core06-owner", false)
	appA := s.createTestApp(c, &ct.App{Name: "core06-own-a", OwnerAccount: "user:" + owner.ID})
	appB := s.createTestApp(c, &ct.App{Name: "core06-own-b", OwnerAccount: "user:" + owner.ID})
	artifact := s.createTestArtifact(c, &ct.Artifact{})

	build, err := buildtoken.Mint(s.tokenSigner, appA, time.Minute)
	c.Assert(err, IsNil)
	receive, err := buildtoken.MintReceive(s.tokenSigner, appA, time.Minute)
	c.Assert(err, IsNil)

	// Control: the owner's own session still has the account.
	now := time.Now()
	ownerJWT, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     owner.ID,
		UserEmail:  owner.Email,
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
	})
	c.Assert(err, IsNil)
	code, _ := s.scopedStatus(c, http.MethodGet, ownerJWT, "/apps/"+appB.ID, "")
	c.Assert(code, Equals, http.StatusOK)

	for name, raw := range map[string]string{"build": build, "receive": receive} {
		c.Logf("credential %s", name)
		code, _ = s.scopedStatus(c, http.MethodPost, raw, "/tokens", `{"name":"x"}`)
		c.Assert(code, Equals, http.StatusForbidden)
		code, _ = s.scopedStatus(c, http.MethodGet, raw, "/tokens", "")
		c.Assert(code, Equals, http.StatusForbidden)
		code, _ = s.scopedStatus(c, http.MethodGet, raw, "/whoami", "")
		c.Assert(code, Equals, http.StatusForbidden)
		code, _ = s.scopedStatus(c, http.MethodGet, raw, "/apps/"+appB.ID, "")
		c.Assert(code, Equals, http.StatusForbidden)
		code, _ = s.scopedStatus(c, http.MethodGet, raw, "/apps/"+appB.ID+"/release", "")
		c.Assert(code, Equals, http.StatusForbidden)
		// The app being built stays reachable.
		code, _ = s.scopedStatus(c, http.MethodGet, raw, "/apps/"+appA.ID, "")
		c.Assert(code, Equals, http.StatusOK)
	}

	// What a compile does with its token.
	code, _ = s.scopedStatus(c, http.MethodGet, receive, "/artifacts/"+artifact.ID, "")
	c.Assert(code, Equals, http.StatusOK)
	code, _ = s.scopedStatus(c, http.MethodPost, receive, "/apps/"+appA.ID+"/builds", `{}`)
	c.Assert(code, Not(Equals), http.StatusForbidden)
}
