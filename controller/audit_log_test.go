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
	"github.com/randy-girard/flynn/controller/redact"
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
	redact.Env(env)
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
	if strings.Contains(buf.String(), "still-secret") {
		t.Fatalf("tcp route key logged: %s", buf.String())
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

// TestAuditBodyRedactsSecrets runs request bodies that carry passwords,
// keys, and tokens through the AUDIT_LOG=true body capture. No secret may
// reach the logged body, whatever the route or route type.
func TestAuditBodyRedactsSecrets(t *testing.T) {
	for _, tc := range []struct {
		method, path, body, secret string
	}{
		{"POST", "/users", `{"email":"a@example.com","password":"S1-user-create-pw"}`, "S1-user-create-pw"},
		{"PATCH", "/users/u1", `{"password":"S2-user-patch-pw"}`, "S2-user-patch-pw"},
		{"PUT", "/github/app", `{"app_id":1,"private_key":"S3-gh-private-key","webhook_secret":"S3b-gh-hook","client_secret":"S3c-gh-client"}`, "S3"},
		{"PUT", "/providers/postgres/resources/r1", `{"id":"r1","env":{"PGPASSWORD":"S4-pg-pw","DATABASE_URL":"postgres://u:S4b@h/db"}}`, "S4"},
		{"POST", "/providers/postgres/resources", `{"config":{"password":"S5-resource-config-pw"}}`, "S5"},
		{"POST", "/sinks", `{"kind":"syslog","config":{"url":"syslog+tls://S6-sink-token@logs.example.com:514"}}`, "S6"},
		{"POST", "/apps/x/routes", `{"type":"tcp","service":"x-web","tls_key":"S7-tcp-tls-key"}`, "S7"},
		{"PUT", "/apps/x/routes/http/r1", `{"domain":"x.example.com","tls_key":"S8-untyped-tls-key","certificate":{"key":"S8b-cert-key","cert":"public"}}`, "S8"},
		{"POST", "/releases", `{"env":{"SECRET_KEY":"S9-release-env"},"processes":{"web":{"env":{"REDIS_PASSWORD":"S9b-proc-env"}}}}`, "S9"},
		{"POST", "/apps/x/jobs", `{"env":{"AUTH_TOKEN":"S10-job-env"}}`, "S10"},
		{"PUT", "/acme/config", `{"enabled":true,"account_key":"S11-acme-account-key"}`, "S11"},
		{"PUT", "/managed-certificates/m1", `{"key":"S12-managed-cert-key"}`, "S12"},
		{"PUT", "/domain", `{"domain":"new.example.com","tls_key":"S13-domain-tls-key"}`, "S13"},
		{"POST", "/tenant-backups/r1/finish", `{"credentials":{"access_key":"S14-backup-cred"}}`, "S14"},
		{"PUT", "/apps/x/github", `{"repo":"o/r","installation_token":"S15-gh-install-token"}`, "S15"},
		{"PUT", "/apps/x/plugin-addons/redis", `{"config":{"password":"S16-addon-pw"}}`, "S16"},
		{"POST", "/cluster/regions", `{"region":"eu","domain":"eu.example.com","tls_pin":"pin","controller_key":"S17-region-key"}`, "S17"},
		{"POST", "/tokens", `{"name":"ci","token":"S18-token-body"}`, "S18"},
		{"PUT", "/tenancy", `{"mode":"self_hosted","cluster_key":"S19-tenancy-key"}`, "S19"},
		{"PUT", "/cluster/policy", `{"enforced":true,"webhook_secret":"S20-policy-secret"}`, "S20"},
		{"POST", "/apps/x/meta", `{"meta":{"github_token":"S21-meta-token"}}`, "S21"},
		{"POST", "/apps/x/builds", `{"env":{"NPM_TOKEN":"S22-build-env"}}`, "S22"},
	} {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(200)
		})
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rw := httphelper.NewResponseWriter(httptest.NewRecorder(), nil)
		buf := handleRequestWithAuditBodyBuffer(inner, rw, req)
		if strings.Contains(buf.String(), tc.secret) {
			t.Errorf("%s %s logged a secret: %s", tc.method, tc.path, buf.String())
		}
	}

	// Allowed bodies keep the values that are not secrets.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body) })
	req := httptest.NewRequest("POST", "/releases", strings.NewReader(`{"env":{"PORT":"8080","SECRET_KEY":"x"}}`))
	req.Header.Set("Content-Type", "application/json")
	buf := handleRequestWithAuditBodyBuffer(inner, httphelper.NewResponseWriter(httptest.NewRecorder(), nil), req)
	if !strings.Contains(buf.String(), "8080") || !strings.Contains(buf.String(), redactedPlaceholder) {
		t.Fatalf("release body lost its safe values: %s", buf.String())
	}
	// A body the allowlist does not cover is reduced to its field names.
	req = httptest.NewRequest("PATCH", "/users/u1", strings.NewReader(`{"password":"pw-value","email":"e@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	buf = handleRequestWithAuditBodyBuffer(inner, httphelper.NewResponseWriter(httptest.NewRecorder(), nil), req)
	if got := buf.String(); strings.Contains(got, "e@example.com") || !strings.Contains(got, "email") || !strings.Contains(got, "password") {
		t.Fatalf("unlisted body = %s, want field names only", got)
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
		{"PUT", "/providers/postgres", "provider.update", ""},
		{"PUT", "/providers/postgres/resources/x/apps/web", "resource.app.add", "web"},
		{"POST", "/tokens", "token.create", ""},
		{"DELETE", "/tokens/t1", "token.delete", ""},
		{"PUT", "/cluster/runtime-settings", "cluster.runtime_settings.set", ""},
		{"PUT", "/cluster/app-domain", "cluster.app_domain.set", ""},
		{"PUT", "/cluster/postgres-availability", "cluster.postgres_availability.set", ""},
		{"PUT", "/cluster/mysql-availability", "cluster.mysql_availability.set", ""},
		{"PUT", "/cluster/redis-availability", "cluster.redis_availability.set", ""},
		{"PUT", "/cluster/mongodb-availability", "cluster.mongodb_availability.set", ""},
		{"PUT", "/cluster/kafka-availability", "cluster.kafka_availability.set", ""},
		{"PUT", "/cluster/clickhouse-availability", "cluster.clickhouse_availability.set", ""},
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
