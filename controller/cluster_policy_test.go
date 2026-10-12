package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	. "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func customCPU() resource.Resources {
	return resource.Resources{resource.TypeCPU: {Limit: typeconv.Int64Ptr(500)}}
}

func TestNormalizeClusterPolicy(t *testing.T) {
	p := &ct.ClusterPolicy{
		DefaultRuntime: " Medium ",
		ImageAllowlist: []string{" Heroku-24", "heroku-24", "", "container"},
	}
	normalizeClusterPolicy(p)
	if p.DefaultRuntime != "medium" {
		t.Fatalf("DefaultRuntime = %q", p.DefaultRuntime)
	}
	if want := []string{"heroku-24", "container"}; !reflect.DeepEqual(p.ImageAllowlist, want) {
		t.Fatalf("ImageAllowlist = %v, want %v", p.ImageAllowlist, want)
	}
}

func TestApplyClusterPolicyRuntime(t *testing.T) {
	pol := &ct.ClusterPolicy{DefaultRuntime: "medium", DisableCustomCPU: true}
	release := &ct.Release{Processes: map[string]ct.ProcessType{
		"web":           {},
		"worker":        {RuntimeProfile: "large", Resources: customCPU()},
		"dockerbuilder": {Resources: customCPU()},
	}}
	if err := applyClusterPolicy(pol, release, nil); err != nil {
		t.Fatal(err)
	}
	if got := release.Processes["web"].RuntimeProfile; got != "medium" {
		t.Fatalf("web runtime = %q, want default medium", got)
	}
	if got := release.Processes["worker"].RuntimeProfile; got != "large" {
		t.Fatalf("worker runtime = %q, explicit runtime must be kept", got)
	}

	release.Processes["web"] = ct.ProcessType{Resources: customCPU()}
	err := applyClusterPolicy(pol, release, nil)
	if e, ok := err.(ct.ValidationError); !ok || e.Field != "processes.web.resources" {
		t.Fatalf("custom CPU without runtime: err = %v", err)
	}
	if err := applyClusterPolicy(&ct.ClusterPolicy{}, release, nil); err != nil {
		t.Fatalf("custom CPU allowed when policy is off: %v", err)
	}
}

func TestApplyClusterPolicyImageAllowlist(t *testing.T) {
	slugrunner := &ct.Artifact{Meta: map[string]string{"flynn.component": "slugrunner-24"}}
	docker := &ct.Artifact{URI: "https://example.com/x", Meta: map[string]string{"docker-receive.repository": "acme/web"}}
	for _, tc := range []struct {
		name  string
		allow []string
		meta  map[string]string
		image *ct.Artifact
		ok    bool
	}{
		{"empty allowlist", nil, nil, docker, true},
		{"stack from slugrunner", []string{"heroku-24"}, nil, slugrunner, true},
		{"component base name", []string{"slugrunner"}, nil, slugrunner, true},
		{"container stack meta", []string{"container"}, map[string]string{"slugrunner.stack": "container"}, &ct.Artifact{}, true},
		{"stack not allowed", []string{"container"}, nil, slugrunner, false},
		{"docker repo allowed", []string{"acme/web"}, map[string]string{"docker-receive": "true"}, docker, true},
		{"docker ignores inherited stack", []string{"heroku-24"}, map[string]string{"docker-receive": "true", "slugrunner.stack": "heroku-24"}, docker, false},
	} {
		release := &ct.Release{Meta: tc.meta}
		err := applyClusterPolicy(&ct.ClusterPolicy{ImageAllowlist: tc.allow}, release, tc.image)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestEnforceClusterPolicySkipsSystemApps(t *testing.T) {
	api := &controllerAPI{}
	sys := &ct.App{Name: "router", Meta: map[string]string{"flynn-system-app": "true"}}
	if err := api.enforceClusterPolicy(sys, &ct.Release{}); err != nil {
		t.Fatal(err)
	}
	if err := api.enforceClusterPolicy(nil, &ct.Release{}); err != nil {
		t.Fatal(err)
	}
}

func (s *S) putClusterPolicy(c *C, in *ct.ClusterPolicy) (int, *ct.ClusterPolicy) {
	body, err := json.Marshal(in)
	c.Assert(err, IsNil)
	req, err := http.NewRequest("PUT", s.srv.URL+"/cluster/policy", bytes.NewReader(body))
	c.Assert(err, IsNil)
	req.SetBasicAuth("", authKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	out := &ct.ClusterPolicy{}
	if res.StatusCode == 200 {
		c.Assert(json.NewDecoder(res.Body).Decode(out), IsNil)
	}
	return res.StatusCode, out
}

func (s *S) TestClusterPolicy(c *C) {
	defer s.putClusterPolicy(c, &ct.ClusterPolicy{})

	var got ct.ClusterPolicy
	s.getJSON(c, "/cluster/policy", &got)
	c.Assert(got.DefaultRuntime, Equals, "")
	c.Assert(got.DisableCustomCPU, Equals, false)

	code, _ := s.putClusterPolicy(c, &ct.ClusterPolicy{DefaultRuntime: "no-such-runtime"})
	c.Assert(code, Equals, 400)

	code, out := s.putClusterPolicy(c, &ct.ClusterPolicy{DefaultRuntime: "Medium", DisableCustomCPU: true, ImageAllowlist: []string{" Heroku-24 "}})
	c.Assert(code, Equals, 200)
	c.Assert(out.DefaultRuntime, Equals, "medium")
	c.Assert(out.ImageAllowlist, DeepEquals, []string{"heroku-24"})
	c.Assert(out.UpdatedAt, NotNil)
	c.Assert(out.Enforced, Equals, false)
	s.getJSON(c, "/cluster/policy", &got)
	c.Assert(got.DisableCustomCPU, Equals, true)
	c.Assert(got.Enforced, Equals, false)

	app := s.createTestApp(c, &ct.App{Name: "cluster-policy-app"})
	slug := s.createTestArtifact(c, &ct.Artifact{Meta: map[string]string{"flynn.component": "slugrunner-24"}})

	// Without the enterprise plugin the stored policy is kept but not enforced.
	rel := &ct.Release{ArtifactIDs: []string{slug.ID}, Processes: map[string]ct.ProcessType{"web": {Resources: customCPU()}}}
	c.Assert(s.c.CreateRelease(app.ID, rel), IsNil)

	plugin := s.createTestApp(c, &ct.App{Name: enterprisePluginApp, Meta: map[string]string{"flynn-system-app": "true", "flynn-plugin": "true"}})
	// Uninstall deletes the plugin app (app_deletion sets deleted_at).
	uninstall := func() {
		c.Assert(s.hc.db.Exec(`UPDATE apps SET deleted_at = now() WHERE app_id = $1`, plugin.ID), IsNil)
	}
	defer uninstall()
	s.getJSON(c, "/cluster/policy", &got)
	c.Assert(got.Enforced, Equals, true)

	rel = &ct.Release{ArtifactIDs: []string{slug.ID}, Processes: map[string]ct.ProcessType{"web": {}}}
	c.Assert(s.c.CreateRelease(app.ID, rel), IsNil)
	c.Assert(rel.Processes["web"].RuntimeProfile, Equals, "medium")

	rel = &ct.Release{ArtifactIDs: []string{slug.ID}, Processes: map[string]ct.ProcessType{"web": {Resources: customCPU()}}}
	c.Assert(hh.IsValidationError(s.c.CreateRelease(app.ID, rel)), Equals, true)

	other := s.createTestArtifact(c, &ct.Artifact{})
	rel = &ct.Release{ArtifactIDs: []string{other.ID}, Processes: map[string]ct.ProcessType{"web": {}}}
	c.Assert(hh.IsValidationError(s.c.CreateRelease(app.ID, rel)), Equals, true)

	uninstall()
	s.getJSON(c, "/cluster/policy", &got)
	c.Assert(got.Enforced, Equals, false)
	c.Assert(got.DisableCustomCPU, Equals, true)
	c.Assert(s.c.CreateRelease(app.ID, rel), IsNil)
}
