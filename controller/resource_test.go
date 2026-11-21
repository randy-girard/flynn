package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
)

func (s *S) provisionTestResourceWithServer(c *C, name string, apps []string) (*ct.Resource, *ct.Provider, *httptest.Server) {
	data := []byte(`{"foo":"bar"}`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		c.Assert(req.URL.Path, Equals, "/things")
		in, err := ioutil.ReadAll(req.Body)
		c.Assert(err, IsNil)
		c.Assert(string(in), Equals, string(data))
		w.Write([]byte(fmt.Sprintf(`{"id":"/things/%s","env":{"foo":"baz"}}`, name)))
	})
	srv := httptest.NewServer(handler)

	p := &ct.Provider{URL: fmt.Sprintf("http://%s/things", srv.Listener.Addr()), Name: name}
	c.Assert(s.c.CreateProvider(p), IsNil)
	conf := json.RawMessage(data)
	out, err := s.c.ProvisionResource(&ct.ResourceReq{ProviderID: p.ID, Config: &conf, Apps: apps})
	c.Assert(err, IsNil)
	return out, p, srv
}

func (s *S) provisionTestResource(c *C, name string, apps []string) (*ct.Resource, *ct.Provider) {
	out, p, srv := s.provisionTestResourceWithServer(c, name, apps)
	srv.Close()
	return out, p
}

func (s *S) TestProvisionResource(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "provision-resource1"})
	app2 := s.createTestApp(c, &ct.App{Name: "provision-resource2"})

	resource, provider := s.provisionTestResource(c, "provision-resource", []string{app1.ID, app2.Name})
	c.Assert(resource.Env["foo"], Equals, "baz")
	c.Assert(resource.ProviderID, Equals, provider.ID)
	c.Assert(resource.ExternalID, Equals, "/things/provision-resource")
	c.Assert(resource.ID, Not(Equals), "")
	c.Assert(resource.Apps, DeepEquals, []string{app1.ID, app2.ID})

	gotResource, err := s.c.GetResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource, DeepEquals, resource)

	_, err = s.c.GetResource(provider.ID, resource.ID+"fail")
	c.Assert(err, Equals, controller.ErrNotFound)
}

func (s *S) TestPutResource(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "put-resource"})
	provider := s.createTestProvider(c, &ct.Provider{URL: "https://example.ca", Name: "put-resource"})

	resource := &ct.Resource{
		ID:         random.UUID(),
		ProviderID: provider.ID,
		ExternalID: "/foo/bar",
		Env:        map[string]string{"FOO": "BAR"},
		Apps:       []string{app.ID},
	}
	c.Assert(s.c.PutResource(resource), IsNil)

	c.Assert(resource.ProviderID, Equals, provider.ID)
	c.Assert(resource.CreatedAt, Not(IsNil))

	gotResource, err := s.c.GetResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource, DeepEquals, resource)
}

func (s *S) TestPutResourceUpdatesExisting(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "put-resource-update"})
	resource, provider := s.provisionTestResource(c, "put-resource-update", []string{app.ID})
	resource.Env = map[string]string{"FLYNN_POSTGRES": "postgresql-next-1", "FOO": "BAZ"}
	c.Assert(s.c.PutResource(resource), IsNil)
	got, err := s.c.GetResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Env["FLYNN_POSTGRES"], Equals, "postgresql-next-1")
	c.Assert(got.Env["FOO"], Equals, "BAZ")
}

func (s *S) TestAddResourceApp(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "add-resource-app1"})
	app2 := s.createTestApp(c, &ct.App{Name: "add-resource-app2"})
	resource, provider := s.provisionTestResource(c, "add-resource-app", []string{})

	gotResource, err := s.c.AddResourceApp(provider.ID, resource.ID, app1.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource.Apps, DeepEquals, []string{app1.ID})

	gotResource, err = s.c.AddResourceApp(provider.ID, resource.ID, app2.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource.Apps, DeepEquals, []string{app1.ID, app2.ID})
}

func (s *S) TestDeleteResourceApp(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "delete-resource-app1"})
	app2 := s.createTestApp(c, &ct.App{Name: "delete-resource-app2"})
	resource, provider := s.provisionTestResource(c, "delete-resource-app", []string{app1.ID, app2.ID})

	gotResource, err := s.c.DeleteResourceApp(provider.ID, resource.ID, app1.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource.Apps, DeepEquals, []string{app2.ID})

	gotResource, err = s.c.DeleteResourceApp(provider.ID, resource.ID, app2.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource.Apps, IsNil)
}

func (s *S) TestDeleteResourceAppThenAdd(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "delete-then-add-resource-app1"})
	app2 := s.createTestApp(c, &ct.App{Name: "delete-then-add-resource-app2"})
	resource, provider := s.provisionTestResource(c, "delete-then-add-resource-app", []string{app1.ID, app2.ID})

	gotResource, err := s.c.DeleteResourceApp(provider.ID, resource.ID, app1.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource.Apps, DeepEquals, []string{app2.ID})

	gotResource, err = s.c.DeleteResourceApp(provider.ID, resource.ID, app2.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource.Apps, IsNil)

	gotResource, err = s.c.AddResourceApp(provider.ID, resource.ID, app1.ID)
	c.Assert(err, IsNil)
	c.Assert(gotResource.Apps, DeepEquals, []string{app1.ID})
}

func (s *S) TestResourceLists(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "resource-list1"})
	app2 := s.createTestApp(c, &ct.App{Name: "resource-list2"})
	apps := []string{app1.ID, app2.ID}

	resource, provider := s.provisionTestResource(c, "resource-list", apps)

	check := func(list []*ct.Resource, err error) {
		c.Assert(err, IsNil)

		c.Assert(len(list) > 0, Equals, true)
		c.Assert(list[0].ID, Equals, resource.ID)
		c.Assert(list[0].Apps, DeepEquals, apps)
	}

	check(s.c.ResourceList(provider.ID))
	check(s.c.ResourceList(provider.Name))
	check(s.c.AppResourceList(app1.ID))
	check(s.c.AppResourceList(app2.ID))
	check(s.c.ResourceListAll())
}

func (s *S) TestAppResourceListWithDeletedAppResource(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "resource-app-list1"})
	app2 := s.createTestApp(c, &ct.App{Name: "resource-app-list2"})

	resource, provider := s.provisionTestResource(c, "resource-app-list", []string{app1.ID, app2.ID})

	_, err := s.c.DeleteResourceApp(provider.ID, resource.ID, app1.ID)
	c.Assert(err, IsNil)

	list, err := s.c.AppResourceList(app1.ID)
	c.Assert(err, IsNil)
	c.Assert(len(list), Equals, 0)

	list, err = s.c.AppResourceList(app2.ID)
	c.Assert(err, IsNil)
	c.Assert(len(list), Equals, 1)
	c.Assert(list[0].ID, Equals, resource.ID)
	c.Assert(list[0].Apps, DeepEquals, []string{app2.ID})
}

func (s *S) TestDetachResourceLeavesOtherAppResources(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "detach-keeps-other"})
	first, provider := s.provisionTestResource(c, "detach-keep-a", []string{app.ID})
	second, _ := s.provisionTestResource(c, "detach-keep-b", []string{app.ID})

	got, err := s.c.DeleteResourceApp(provider.ID, first.ID, app.ID)
	c.Assert(err, IsNil)
	c.Assert(got.ID, Equals, first.ID)

	list, err := s.c.AppResourceList(app.ID)
	c.Assert(err, IsNil)
	c.Assert(len(list), Equals, 1)
	c.Assert(list[0].ID, Equals, second.ID)
}

func (s *S) TestProvisionResourceSetsOwnerApp(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "owner-app-resource"})
	resource, _ := s.provisionTestResource(c, "owner-app-resource", []string{app.ID})
	c.Assert(resource.OwnerApp, Equals, app.ID)
}

func (s *S) TestAddResourceAppRequiresSameOwnerAccount(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "share-res-a", OwnerAccount: "user:ada"})
	app2 := s.createTestApp(c, &ct.App{Name: "share-res-b", OwnerAccount: "user:ada"})
	other := s.createTestApp(c, &ct.App{Name: "share-res-other", OwnerAccount: "user:bev"})
	resource, provider := s.provisionTestResource(c, "share-res", []string{app1.ID})
	c.Assert(resource.OwnerAccount, Equals, "user:ada")
	c.Assert(resource.OwnerApp, Equals, app1.ID)

	got, err := s.c.AddResourceApp(provider.ID, resource.ID, app2.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Apps, DeepEquals, []string{app1.ID, app2.ID})

	_, err = s.c.AddResourceApp(provider.ID, resource.ID, other.ID)
	c.Assert(err, NotNil)
}

func (s *S) TestProvisionPlatformPostgresRejectsUserApp(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "user-appliance-pg"})
	p := s.createTestProvider(c, &ct.Provider{URL: "http://postgres-api.discoverd/databases?suite=provision", Name: "platform-postgres-user"})
	_, err := s.c.ProvisionResource(&ct.ResourceReq{ProviderID: p.ID, Apps: []string{app.ID}})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*flynn-plugin-postgres.*")
}

func (s *S) TestAddResourceAppRejectsPlatformPostgresUserApp(c *C) {
	sys := s.createTestApp(c, &ct.App{Name: "sys-appliance-pg", Meta: map[string]string{"flynn-system-app": "true"}})
	plugin := s.createTestApp(c, &ct.App{Name: "plugin-appliance-pg", Meta: map[string]string{"flynn-plugin": "true"}})
	user := s.createTestApp(c, &ct.App{Name: "user-attach-pg"})
	p := s.createTestProvider(c, &ct.Provider{URL: "http://postgres-api.discoverd/databases?suite=attach", Name: "platform-postgres-attach"})
	res := &ct.Resource{
		ID:         random.UUID(),
		ProviderID: p.ID,
		ExternalID: "/appliance/sys",
		Env:        map[string]string{"PGDATABASE": "sys"},
		Apps:       []string{sys.ID},
	}
	c.Assert(s.c.PutResource(res), IsNil)

	_, err := s.c.AddResourceApp(p.ID, res.ID, user.ID)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*flynn-plugin-postgres.*")

	got, err := s.c.AddResourceApp(p.ID, res.ID, plugin.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Apps, HasLen, 2)
}

func (s *S) TestDeleteResourceRejectsNonOwnerApp(c *C) {
	app1 := s.createTestApp(c, &ct.App{Name: "own-del-a", OwnerAccount: "user:ada"})
	app2 := s.createTestApp(c, &ct.App{Name: "own-del-b", OwnerAccount: "user:ada"})
	resource, provider := s.provisionTestResource(c, "own-del", []string{app1.ID})
	_, err := s.c.AddResourceApp(provider.ID, resource.ID, app2.ID)
	c.Assert(err, IsNil)

	req, err := http.NewRequest("DELETE", s.srv.URL+"/providers/"+provider.ID+"/resources/"+resource.ID+"?app_id="+app2.ID, nil)
	c.Assert(err, IsNil)
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 400)

	got, err := s.c.GetResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)
	c.Assert(got.ID, Equals, resource.ID)
}

// resourceAuthz sends a request to the controller as the given bearer token.
func (s *S) resourceAuthz(c *C, method, auth, path, body string) (int, string) {
	if auth != authKey {
		return s.scopedStatus(c, method, auth, path, body)
	}
	req, err := http.NewRequest(method, s.srv.URL+path, strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	got, err := ioutil.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, string(got)
}

func (s *S) TestGetResourceDeniesOtherAccount(c *C) {
	ada, bev := random.UUID(), random.UUID()
	app := s.createTestApp(c, &ct.App{Name: "get-res-deny", OwnerAccount: "user:" + ada})
	resource, provider := s.provisionTestResource(c, "get-res-deny", []string{app.ID})
	c.Assert(resource.OwnerAccount, Equals, "user:"+ada)
	path := "/providers/" + provider.ID + "/resources/" + resource.ID

	// Another tenant gets no credentials.
	code, body := s.resourceAuthz(c, "GET", s.signUser(c, bev, nil), path, "")
	c.Assert(code, Equals, 403)
	c.Assert(strings.Contains(body, "baz"), Equals, false)

	// The owner and the cluster key still can.
	code, body = s.resourceAuthz(c, "GET", s.signUser(c, ada, nil), path, "")
	c.Assert(code, Equals, 200)
	c.Assert(strings.Contains(body, "baz"), Equals, true)
	_, err := s.c.GetResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)
}

func (s *S) TestGetUnownedResourceIsAdminOnly(c *C) {
	bev := random.UUID()
	provider := s.createTestProvider(c, &ct.Provider{URL: "https://example.ca/get-unowned", Name: "get-unowned"})
	res := &ct.Resource{ID: random.UUID(), ProviderID: provider.ID, ExternalID: "/x", Env: map[string]string{"SECRET": "platform"}}
	c.Assert(s.c.PutResource(res), IsNil)
	path := "/providers/" + provider.ID + "/resources/" + res.ID

	code, body := s.resourceAuthz(c, "GET", s.signUser(c, bev, nil), path, "")
	c.Assert(code, Equals, 403)
	c.Assert(strings.Contains(body, "platform"), Equals, false)
	_, err := s.c.GetResource(provider.ID, res.ID)
	c.Assert(err, IsNil)
}

func (s *S) TestResourceHandlersRejectProviderMismatch(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "mismatch-res"})
	resource, provider := s.provisionTestResource(c, "mismatch-res", []string{app.ID})
	other := s.createTestProvider(c, &ct.Provider{URL: "https://example.ca/mismatch-other", Name: "mismatch-other"})
	base := "/providers/" + other.ID + "/resources/" + resource.ID
	for _, tc := range []struct{ method, path string }{
		{"GET", base},
		{"DELETE", base},
		{"PUT", base + "/apps/" + app.ID},
		{"DELETE", base + "/apps/" + app.ID},
	} {
		code, _ := s.resourceAuthz(c, tc.method, authKey, tc.path, "")
		c.Assert(code, Equals, 404, Commentf("%s %s", tc.method, tc.path))
	}
	got, err := s.c.GetResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Apps, DeepEquals, []string{app.ID})
}

func (s *S) TestAttachResourceProviderMismatch(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "attach-mismatch"})
	provider := s.createTestProvider(c, &ct.Provider{URL: "https://example.ca/attach-mismatch-a", Name: "attach-mismatch-a"})
	other := s.createTestProvider(c, &ct.Provider{URL: "https://example.ca/attach-mismatch-b", Name: "attach-mismatch-b"})
	res := &ct.Resource{ID: random.UUID(), ProviderID: provider.ID, ExternalID: "/x", Env: map[string]string{"A": "b"}}
	c.Assert(s.c.PutResource(res), IsNil)
	_, err := s.c.AddResourceApp(other.ID, res.ID, app.ID)
	c.Assert(err, Equals, controller.ErrNotFound)
}

func (s *S) TestPutResourceRequiresClusterKey(c *C) {
	ada, bev := random.UUID(), random.UUID()
	app := s.createTestApp(c, &ct.App{Name: "put-res-deny", OwnerAccount: "user:" + ada})
	resource, provider := s.provisionTestResource(c, "put-res-deny", []string{app.ID})
	path := "/providers/" + provider.ID + "/resources/" + resource.ID
	body := `{"external_id":"/victim","env":{"foo":"evil"}}`

	for _, user := range []string{bev, ada} {
		code, _ := s.resourceAuthz(c, "PUT", s.signUser(c, user, nil), path, body)
		c.Assert(code, Equals, 403, Commentf(user))
	}
	got, err := s.c.GetResource(provider.ID, resource.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Env["foo"], Equals, "baz")
	c.Assert(got.ExternalID, Equals, resource.ExternalID)

	// A new row with an arbitrary owner is also refused.
	code, _ := s.resourceAuthz(c, "PUT", s.signUser(c, bev, nil),
		"/providers/"+provider.ID+"/resources/"+random.UUID(),
		`{"external_id":"/x","env":{"a":"b"},"owner_account":"user:`+ada+`"}`)
	c.Assert(code, Equals, 403)

	// The cluster key (bootstrap, plugins) still can.
	code, _ = s.resourceAuthz(c, "PUT", authKey, path, body)
	c.Assert(code, Equals, 200)
}

func (s *S) TestUnownedResourceDeleteNeedsClusterKey(c *C) {
	bev := random.UUID()
	provider := s.createTestProvider(c, &ct.Provider{URL: "https://example.ca/del-unowned", Name: "del-unowned"})
	res := &ct.Resource{ID: random.UUID(), ProviderID: provider.ID, ExternalID: "/x", Env: map[string]string{"A": "b"}}
	c.Assert(s.c.PutResource(res), IsNil)
	path := "/providers/" + provider.ID + "/resources/" + res.ID

	code, _ := s.resourceAuthz(c, "DELETE", s.signUser(c, bev, nil), path, "")
	c.Assert(code, Equals, 403)
	_, err := s.c.GetResource(provider.ID, res.ID)
	c.Assert(err, IsNil)
}

func (s *S) TestAttachUnownedResourceNeedsClusterKey(c *C) {
	bev := random.UUID()
	app := s.createTestApp(c, &ct.App{Name: "attach-unowned", OwnerAccount: "user:" + bev})
	provider := s.createTestProvider(c, &ct.Provider{URL: "https://example.ca/attach-unowned", Name: "attach-unowned"})
	res := &ct.Resource{ID: random.UUID(), ProviderID: provider.ID, ExternalID: "/x", Env: map[string]string{"A": "b"}}
	c.Assert(s.c.PutResource(res), IsNil)

	code, body := s.resourceAuthz(c, "PUT", s.signUser(c, bev, nil),
		"/providers/"+provider.ID+"/resources/"+res.ID+"/apps/"+app.ID, "")
	c.Assert(code, Equals, 403, Commentf(body))
	got, err := s.c.GetResource(provider.ID, res.ID)
	c.Assert(err, IsNil)
	c.Assert(got.OwnerAccount, Equals, "")
	c.Assert(got.Apps, HasLen, 0)
}

// countingProvider is a provider that counts provision calls.
func (s *S) countingProvider(c *C, name string) (*ct.Provider, *int, func()) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		calls++
		w.Write([]byte(fmt.Sprintf(`{"id":"/things/%s-%d","env":{"foo":"baz"}}`, name, calls)))
	}))
	p := s.createTestProvider(c, &ct.Provider{URL: fmt.Sprintf("http://%s/things", srv.Listener.Addr()), Name: name})
	return p, &calls, srv.Close
}

func (s *S) TestProvisionRejectsSecondAppOfAnotherAccount(c *C) {
	ada, bev := random.UUID(), random.UUID()
	mine := s.createTestApp(c, &ct.App{Name: "prov-two-mine", OwnerAccount: "user:" + ada})
	victim := s.createTestApp(c, &ct.App{Name: "prov-two-victim", OwnerAccount: "user:" + bev})
	p, calls, done := s.countingProvider(c, "prov-two")
	defer done()
	path := "/providers/" + p.ID + "/resources"

	for _, apps := range []string{
		`["` + mine.ID + `","` + victim.ID + `"]`,
		`["` + mine.ID + `","` + victim.Name + `"]`,
	} {
		code, body := s.resourceAuthz(c, "POST", s.signUser(c, ada, nil), path, `{"apps":`+apps+`}`)
		c.Assert(code, Equals, 403, Commentf(body))
	}
	// The cluster key cannot mix owners either.
	code, body := s.resourceAuthz(c, "POST", authKey, path, `{"apps":["`+mine.ID+`","`+victim.ID+`"]}`)
	c.Assert(code, Equals, 403, Commentf(body))

	c.Assert(*calls, Equals, 0)
	for _, app := range []*ct.App{mine, victim} {
		list, err := s.c.AppResourceList(app.ID)
		c.Assert(err, IsNil)
		c.Assert(list, HasLen, 0)
	}
}

func (s *S) TestProvisionRequiresManageOnEveryApp(c *C) {
	ada, bev := random.UUID(), random.UUID()
	// Both apps belong to bev; ada holds a grant on the first one only.
	first := s.createTestApp(c, &ct.App{Name: "prov-manage-a", OwnerAccount: "user:" + bev})
	second := s.createTestApp(c, &ct.App{Name: "prov-manage-b", OwnerAccount: "user:" + bev})
	p, calls, done := s.countingProvider(c, "prov-manage")
	defer done()
	tok := s.signUser(c, ada, []*api.AppGrant{{AppId: first.ID, Permissions: []string{"app:write"}}})
	code, body := s.resourceAuthz(c, "POST", tok, "/providers/"+p.ID+"/resources",
		`{"apps":["`+first.ID+`","`+second.ID+`"]}`)
	c.Assert(code, Equals, 403, Commentf(body))
	c.Assert(*calls, Equals, 0)
}

func (s *S) TestProvisionAllowsSeveralAppsOfOneAccount(c *C) {
	ada := random.UUID()
	a := s.createTestApp(c, &ct.App{Name: "prov-same-a", OwnerAccount: "user:" + ada})
	b := s.createTestApp(c, &ct.App{Name: "prov-same-b", OwnerAccount: "user:" + ada})
	p, calls, done := s.countingProvider(c, "prov-same")
	defer done()
	code, body := s.resourceAuthz(c, "POST", s.signUser(c, ada, nil), "/providers/"+p.ID+"/resources",
		`{"apps":["`+a.ID+`","`+b.Name+`"]}`)
	c.Assert(code, Equals, 200, Commentf(body))
	c.Assert(*calls, Equals, 1)
	var res ct.Resource
	c.Assert(json.Unmarshal([]byte(body), &res), IsNil)
	c.Assert(res.Apps, DeepEquals, []string{a.ID, b.ID})
	c.Assert(res.OwnerAccount, Equals, "user:"+ada)
	c.Assert(res.OwnerApp, Equals, a.ID)
}

func (s *S) TestProvisionPlatformPostgresChecksEveryApp(c *C) {
	sys := s.createTestApp(c, &ct.App{Name: "sys-two-pg", Meta: map[string]string{"flynn-system-app": "true"}})
	user := s.createTestApp(c, &ct.App{Name: "user-two-pg"})
	p := s.createTestProvider(c, &ct.Provider{URL: "http://postgres-api.discoverd/databases?suite=two", Name: "platform-postgres-two"})
	_, err := s.c.ProvisionResource(&ct.ResourceReq{ProviderID: p.ID, Apps: []string{sys.ID, user.ID}})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*flynn-plugin-postgres.*")
	list, err := s.c.AppResourceList(user.ID)
	c.Assert(err, IsNil)
	c.Assert(list, HasLen, 0)
}
