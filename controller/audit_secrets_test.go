package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	. "github.com/flynn/go-check"
	log "github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// auditedServer is the controller handler with AUDIT_LOG=true, logging into
// buf instead of stdout.
func (s *S) auditedServer(c *C, buf *bytes.Buffer) *httptest.Server {
	prev, had := os.LookupEnv("AUDIT_LOG")
	os.Setenv("AUDIT_LOG", "true")
	h, _, _ := appHandler(s.hc)
	if had {
		os.Setenv("AUDIT_LOG", prev)
	} else {
		os.Unsetenv("AUDIT_LOG")
	}
	lg := log.New()
	lg.SetHandler(log.StreamHandler(buf, log.LogfmtFormat()))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := httphelper.NewResponseWriter(w, r.Context())
		auditLoggerFn(h, lg, "192.0.2.1", rw, r)
	}))
}

func clusterKeyDo(c *C, srv *httptest.Server, method, path, body string) (int, string) {
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(got)
}

func (s *S) auditRowsAfter(c *C, start int64) map[string][]string {
	rows, err := s.hc.db.Query(`SELECT action, data::text FROM core_audit WHERE id > $1 ORDER BY id`, start)
	c.Assert(err, IsNil)
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var action, data string
		c.Assert(rows.Scan(&action, &data), IsNil)
		out[action] = append(out[action], data)
	}
	c.Assert(rows.Err(), IsNil)
	return out
}

func (s *S) maxAuditID(c *C) int64 {
	var id int64
	c.Assert(s.hc.db.QueryRow(`SELECT COALESCE(max(id), 0) FROM core_audit`).Scan(&id), IsNil)
	return id
}

// TestPatchUserAuditOmitsPassword checks the user.update row records that
// the password changed, not the password.
func (s *S) TestPatchUserAuditOmitsPassword(c *C) {
	var logBuf bytes.Buffer
	srv := s.auditedServer(c, &logBuf)
	defer srv.Close()

	code, body := clusterKeyDo(c, srv, "POST", "/users", `{"email":"core21-patch@example.com","password":"core21-initial-pw"}`)
	c.Assert(code, Equals, 200, Commentf(body))
	var u ct.User
	c.Assert(json.Unmarshal([]byte(body), &u), IsNil)

	start := s.maxAuditID(c)
	code, body = clusterKeyDo(c, srv, "PATCH", "/users/"+u.ID, `{"password":"core21-new-pw","disabled":false}`)
	c.Assert(code, Equals, 200, Commentf(body))

	rows := s.auditRowsAfter(c, start)
	c.Assert(rows["user.update"], HasLen, 1, Commentf("%v", rows))
	var data map[string]interface{}
	c.Assert(json.Unmarshal([]byte(rows["user.update"][0]), &data), IsNil)
	_, hasPassword := data["password"]
	c.Assert(hasPassword, Equals, false, Commentf("%v", data))
	c.Assert(data["password_changed"], Equals, true, Commentf("%v", data))
	c.Assert(data["disabled"], Equals, false)

	// A patch without a password does not claim one changed.
	start = s.maxAuditID(c)
	code, body = clusterKeyDo(c, srv, "PATCH", "/users/"+u.ID, `{"disabled":false}`)
	c.Assert(code, Equals, 200, Commentf(body))
	rows = s.auditRowsAfter(c, start)
	c.Assert(rows["user.update"], HasLen, 1)
	c.Assert(strings.Contains(rows["user.update"][0], "password"), Equals, false, Commentf(rows["user.update"][0]))

	c.Assert(strings.Contains(logBuf.String(), "core21-"), Equals, false, Commentf(logBuf.String()))
}

// TestAuditSecretsNeverStored sends bodies that carry passwords, keys, and
// tokens to every audited route that takes one, with AUDIT_LOG=true. No
// secret may reach core_audit or the request log, whether the request
// succeeds or not.
func (s *S) TestAuditSecretsNeverStored(c *C) {
	var logBuf bytes.Buffer
	srv := s.auditedServer(c, &logBuf)
	defer srv.Close()

	app := s.createTestApp(c, &ct.App{Name: "core21-audit-app"})
	code, body := clusterKeyDo(c, srv, "POST", "/users", `{"email":"core21-table@example.com","password":"Z0-seed-pw-secret"}`)
	c.Assert(code, Equals, 200, Commentf(body))
	var u ct.User
	c.Assert(json.Unmarshal([]byte(body), &u), IsNil)

	start := s.maxAuditID(c)
	type call struct{ method, path, body string }
	calls := []call{
		{"POST", "/users", `{"email":"core21-table2@example.com","password":"Z1-user-create-secret"}`},
		{"PATCH", "/users/" + u.ID, `{"password":"Z2-user-patch-secret"}`},
		{"POST", "/tokens", `{"name":"core21","token":"Z3-token-body-secret"}`},
		{"POST", "/releases", `{"env":{"SECRET_KEY":"Z4-release-env-secret","PORT":"80"},"processes":{"web":{"env":{"REDIS_PASSWORD":"Z4b-proc-env-secret"}}}}`},
		{"POST", "/apps/" + app.ID + "/jobs", `{"release":"","env":{"AUTH_TOKEN":"Z5-job-env-secret"},"args":["true"]}`},
		{"POST", "/apps/" + app.ID + "/routes", `{"type":"tcp","service":"core21-web","tls_key":"Z6-tcp-key-secret"}`},
		{"PUT", "/apps/" + app.ID + "/routes/http/00000000-0000-0000-0000-000000000000", `{"domain":"core21.example.com","certificate":{"key":"Z7-cert-key-secret","cert":"pub"}}`},
		{"POST", "/apps/" + app.ID + "/meta", `{"meta":{"github_token":"Z8-meta-token-secret"}}`},
		{"PUT", "/providers/core21-missing/resources/00000000-0000-0000-0000-000000000000", `{"env":{"PGPASSWORD":"Z9-resource-env-secret"}}`},
		{"POST", "/providers/core21-missing/resources", `{"config":{"password":"Z10-resource-config-secret"}}`},
		{"PUT", "/github/app", `{"private_key":"Z11-gh-private-key-secret","webhook_secret":"Z11b-gh-hook-secret"}`},
		{"POST", "/sinks", `{"kind":"syslog","config":{"url":"syslog+tls://Z12-sink-secret@logs.example.com:514"}}`},
		{"PUT", "/acme/config", `{"enabled":false,"account_key":"Z13-acme-key-secret"}`},
		{"PUT", "/cluster/policy", `{"enforced":false,"webhook_secret":"Z15-policy-secret"}`},
		{"PUT", "/accounts/user:" + u.ID + "/quota", `{"max_apps":5,"api_token":"Z16-quota-secret"}`},
		{"PUT", "/cluster/placement", `{"mode":"mixed","secret":"Z17-placement-secret"}`},
	}
	for _, call := range calls {
		clusterKeyDo(c, srv, call.method, call.path, call.body)
	}

	rows := s.auditRowsAfter(c, start)
	c.Assert(rows["user.update"], HasLen, 1, Commentf("user.update not audited: %v", rows))
	for action, list := range rows {
		for _, data := range list {
			c.Assert(strings.Contains(data, "-secret"), Equals, false, Commentf("%s stored a secret: %s", action, data))
		}
	}
	for _, line := range strings.Split(logBuf.String(), "\n") {
		c.Assert(strings.Contains(line, "-secret"), Equals, false, Commentf("request log leaked: %s", line))
	}
	// The release body is on the allowlist, so its safe values are still logged.
	c.Assert(strings.Contains(logBuf.String(), `PORT`), Equals, true, Commentf(logBuf.String()))
}
