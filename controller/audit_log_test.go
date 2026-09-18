package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
