package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	router "github.com/randy-girard/flynn/router/types"
)

func TestRedactEnvSecrets(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":   "postgres://u:p@h/db",
		"REDIS_PASSWORD": "s3cret",
		"AUTH_TOKEN":     "tok",
		"ACCESS_KEY":     "ak",
		"SAFE":           "ok",
	}
	redactEnv(env)
	if env["SAFE"] != "ok" {
		t.Fatal("non-secret kept")
	}
	for _, k := range []string{"DATABASE_URL", "REDIS_PASSWORD", "AUTH_TOKEN", "ACCESS_KEY"} {
		if env[k] != redactedPlaceholder {
			t.Fatalf("%s=%q", k, env[k])
		}
	}
}

func TestMaybeRedactReleaseJobAndRouteBodies(t *testing.T) {
	release := &ct.Release{
		Env: map[string]string{"SECRET_KEY": "hidden", "PORT": "80"},
		Processes: map[string]ct.ProcessType{
			"web": {Env: map[string]string{"REDIS_PASSWORD": "pw"}},
		},
	}
	buf := mustJSON(t, release)
	req := httptest.NewRequest("POST", "/releases", nil)
	maybeRedactBody(req, buf)
	var out ct.Release
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Env["SECRET_KEY"] != redactedPlaceholder || out.Env["PORT"] != "80" {
		t.Fatalf("release env=%v", out.Env)
	}
	if out.Processes["web"].Env["REDIS_PASSWORD"] != redactedPlaceholder {
		t.Fatal("process env")
	}

	job := &ct.NewJob{Env: map[string]string{"AUTH_TOKEN": "t"}}
	buf = mustJSON(t, job)
	req = httptest.NewRequest("POST", "/apps/x/jobs", nil)
	maybeRedactBody(req, buf)
	var jobOut ct.NewJob
	json.Unmarshal(buf.Bytes(), &jobOut)
	if jobOut.Env["AUTH_TOKEN"] != redactedPlaceholder {
		t.Fatalf("%v", jobOut.Env)
	}

	route := &router.Route{
		Type:         "http",
		LegacyTLSKey: "legacy-pem",
		Certificate:  &router.Certificate{Key: "leaf-pem", Cert: "public"},
	}
	buf = mustJSON(t, route)
	req = httptest.NewRequest("POST", "/apps/x/routes", nil)
	maybeRedactBody(req, buf)
	var routeOut router.Route
	json.Unmarshal(buf.Bytes(), &routeOut)
	if routeOut.LegacyTLSKey != redactedPlaceholder || routeOut.Certificate.Key != redactedPlaceholder {
		t.Fatalf("%+v", routeOut)
	}
	if routeOut.Certificate.Cert != "public" {
		t.Fatal("cert body must stay")
	}

	tcp := &router.Route{Type: "tcp", LegacyTLSKey: "still-secret"}
	buf = mustJSON(t, tcp)
	req = httptest.NewRequest("PUT", "/apps/x/routes/1", nil)
	maybeRedactBody(req, buf)
	if strings.Contains(buf.String(), redactedPlaceholder) {
		t.Fatal("tcp routes must not be rewritten as HTTP TLS redaction")
	}
}

func TestLimitedBodyReaderAndAuditBuffer(t *testing.T) {
	r := &limitedBodyReader{R: strings.NewReader("abcdefghij"), N: 4}
	got, err := io.ReadAll(r)
	if string(got) != "abcd" {
		t.Fatalf("%q", got)
	}
	if err != httphelper.ErrRequestBodyTooBig {
		t.Fatalf("err=%v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(204)
	})
	body := `{"env":{"SECRET_KEY":"hide-me"}}`
	req := httptest.NewRequest("POST", "/releases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rw := httphelper.NewResponseWriter(httptest.NewRecorder(), nil)
	buf := handleRequestWithAuditBodyBuffer(inner, rw, req)
	if strings.Contains(buf.String(), "hide-me") {
		t.Fatalf("secret leaked: %s", buf.String())
	}
	if !strings.Contains(buf.String(), redactedPlaceholder) {
		t.Fatalf("expected redaction: %s", buf.String())
	}
}

func mustJSON(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

func TestAuditHTTPAction(t *testing.T) {
	for _, tc := range []struct {
		method, path, action, ref string
	}{
		{"POST", "/apps", "app.create", ""},
		{"DELETE", "/apps/web", "app.delete", "web"},
		{"POST", "/apps/web/meta", "app.update", "web"},
		{"PUT", "/apps/web/release", "app.release.set", "web"},
		{"POST", "/apps/web/deploy", "deployment.create", "web"},
		{"PUT", "/apps/web/formations/r1", "formation.set", "web"},
		{"PUT", "/apps/web/scale/r1", "scale.request", "web"},
		{"POST", "/apps/web/jobs", "job.run", "web"},
		{"DELETE", "/apps/web/jobs/j1", "job.kill", "web"},
		{"POST", "/apps/web/routes", "route.create", "web"},
		{"DELETE", "/apps/web/routes/http/r1", "route.delete", "web"},
		{"POST", "/apps/web/github/deploy", "github.deploy", "web"},
		{"POST", "/releases", "release.create", ""},
		{"POST", "/providers/postgres/resources", "resource.create", ""},
		{"PUT", "/providers/postgres/resources/x/apps/web", "resource.app.add", "web"},
		{"POST", "/tokens", "token.create", ""},
		{"DELETE", "/tokens/t1", "token.delete", ""},
		{"PUT", "/cluster/runtime-settings", "cluster.runtime_settings.set", ""},
		{"PUT", "/github/app", "github.app.set", ""},
	} {
		action, ref, ok := auditHTTPAction(tc.method, tc.path)
		if !ok || action != tc.action || ref != tc.ref {
			t.Errorf("%s %s = %q %q %v, want %q %q", tc.method, tc.path, action, ref, ok, tc.action, tc.ref)
		}
	}
	for _, tc := range [][2]string{
		{"GET", "/apps/web"},
		{"PUT", "/apps/web/jobs/j1"},
		{"POST", "/apps/web/scheduler-events"},
		{"PUT", "/cluster/policy"},
		{"POST", "/accounts/a/collaborators"},
		{"POST", "/artifacts"},
	} {
		if action, _, ok := auditHTTPAction(tc[0], tc[1]); ok {
			t.Errorf("%s %s recorded as %q", tc[0], tc[1], action)
		}
	}
}

func (s *S) TestClusterAudit(c *C) {
	var start int64
	c.Assert(s.hc.db.QueryRow(`SELECT COALESCE(max(id), 0) FROM core_audit`).Scan(&start), IsNil)

	app := s.createTestApp(c, &ct.App{Name: "cluster-audit-app"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	c.Assert(s.c.SetAppRelease(app.ID, release.ID), IsNil)

	list, err := s.c.ListClusterAudit(start, 100)
	c.Assert(err, IsNil)
	byAction := map[string]*ct.AuditEntry{}
	for _, e := range list {
		c.Assert(e.ID > start, Equals, true)
		byAction[e.Action] = e
	}
	for _, a := range []string{"app.create", "release.create", "app.release.set"} {
		c.Assert(byAction[a], NotNil, Commentf("missing %s in %v", a, list))
		c.Assert(byAction[a].Actor, Equals, "cluster-key")
	}
	set := byAction["app.release.set"]
	c.Assert(set.AppID, Equals, app.ID)
	c.Assert(string(set.Data), Matches, `.*"path": ?"/apps/`+app.ID+`/release".*`)

	prune := func(query string) int {
		req, err := http.NewRequest("DELETE", s.srv.URL+"/cluster/audit?"+query, nil)
		c.Assert(err, IsNil)
		req.SetBasicAuth("", authKey)
		res, err := http.DefaultClient.Do(req)
		c.Assert(err, IsNil)
		res.Body.Close()
		return res.StatusCode
	}
	c.Assert(prune("max_id=1"), Equals, 400)
	created := byAction["app.create"].ID
	before := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	c.Assert(prune(fmt.Sprintf("before=%s&max_id=%d", before, created)), Equals, 200)

	list, err = s.c.ListClusterAudit(start, 100)
	c.Assert(err, IsNil)
	c.Assert(list[0].ID > created, Equals, true)
	c.Assert(list[len(list)-1].Action, Equals, "audit.prune")
}
