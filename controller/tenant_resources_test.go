package main

import (
	"encoding/json"
	"net/http"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/buildtoken"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
)

// A tenant lists the resources its account owns on GET /resources (flynn
// resource, plugin commands that find the app from the resource name). It
// used to be cluster-admin only, so every tenant got 403. Another tenant's
// resources and platform resources stay hidden.
func (s *S) TestTenantListsOnlyOwnResources(c *C) {
	ua := s.createLoginUser(c, "new02-res-a@flynn.test", "new02-res-a", "pw-new02-res-a", false)
	ub := s.createLoginUser(c, "new02-res-b@flynn.test", "new02-res-b", "pw-new02-res-b", false)
	appA := s.createTestApp(c, &ct.App{Name: "new02-res-a", OwnerAccount: "user:" + ua.ID})
	appB := s.createTestApp(c, &ct.App{Name: "new02-res-b", OwnerAccount: "user:" + ub.ID})
	p := s.createTestProvider(c, &ct.Provider{URL: "https://new02.example.com", Name: "new02-res"})

	add := func(owner, app string) *ct.Resource {
		r := &ct.Resource{ProviderID: p.ID, ExternalID: "/things/" + random.UUID(), Env: map[string]string{"SECRET": random.UUID()}, OwnerAccount: owner}
		if app != "" {
			r.Apps = []string{app}
		}
		c.Assert(s.api.resourceRepo.Add(r), IsNil)
		return r
	}
	resA := add("user:"+ua.ID, appA.ID)
	resB := add("user:"+ub.ID, appB.ID)
	platform := add("", "")

	list := func(tok string) []*ct.Resource {
		code, body := s.scopedStatus(c, http.MethodGet, tok, "/resources", "")
		c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", body))
		var out []*ct.Resource
		c.Assert(json.Unmarshal([]byte(body), &out), IsNil)
		return out
	}
	ids := func(rs []*ct.Resource) map[string]bool {
		m := map[string]bool{}
		for _, r := range rs {
			m[r.ID] = true
		}
		return m
	}

	gotA := ids(list(s.signUser(c, ua.ID, nil)))
	c.Assert(gotA[resA.ID], Equals, true)
	c.Assert(gotA[resB.ID], Equals, false)
	c.Assert(gotA[platform.ID], Equals, false)

	gotB := ids(list(s.signUser(c, ub.ID, nil)))
	c.Assert(gotB[resB.ID], Equals, true)
	c.Assert(gotB[resA.ID], Equals, false)
	c.Assert(gotB[platform.ID], Equals, false)

	// A compile credential for A's app is not the owner and gets no catalog.
	build, err := buildtoken.Mint(s.tokenSigner, appA, time.Minute)
	c.Assert(err, IsNil)
	code, _ := s.scopedStatus(c, http.MethodGet, build, "/resources", "")
	c.Assert(code, Equals, http.StatusForbidden)
}
