package httpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flynn/flynn/pkg/httphelper"
)

func TestPrepareReqTokenTakesPrecedenceOverKey(t *testing.T) {
	c := &Client{Token: "scoped-jwt", Key: "cluster-key"}
	req, err := c.prepareReq("GET", "http://example/x", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer scoped-jwt" {
		t.Fatalf("Authorization=%q", got)
	}
	if u, p, ok := req.BasicAuth(); ok && p == "cluster-key" {
		t.Fatalf("cluster key must not be sent when Token is set (basic=%s:%s)", u, p)
	}

	c = &Client{Key: "cluster-key"}
	req, err = c.prepareReq("POST", "http://example/x", nil, map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	_, p, ok := req.BasicAuth()
	if !ok || p != "cluster-key" {
		t.Fatalf("basic auth %v %q", ok, p)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatal("json content-type")
	}
}

func TestPostWithHostAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	t.Setenv("FLYNN_HOST_AUTH_KEY", "")
	if _, err := PostWithHostAuth(srv.URL, "text/plain", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("empty env sent Authorization=%q", gotAuth)
	}

	t.Setenv("FLYNN_HOST_AUTH_KEY", "host-secret")
	if _, err := PostWithHostAuth(srv.URL, "text/plain", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", "/", nil)
	req.SetBasicAuth("", "host-secret")
	want := req.Header.Get("Authorization")
	if gotAuth != want {
		t.Fatalf("Authorization=%q want %q", gotAuth, want)
	}
}

func TestRawReqMapsForbiddenJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httphelper.Forbidden(w, "app-scoped token")
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client(), Token: "jwt"}
	_, err := c.RawReq("GET", "/", nil, nil, nil)
	je, ok := err.(httphelper.JSONError)
	if !ok {
		t.Fatalf("err=%v", err)
	}
	if je.Code != httphelper.ForbiddenErrorCode {
		t.Fatalf("%+v", je)
	}
	if !strings.Contains(je.Message, "flynn help login") {
		t.Fatalf("help text missing: %s", je.Message)
	}
}

func TestRawReqJSONBodyRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"k":"v"`) {
			t.Errorf("body=%s", body)
		}
		json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	var out map[string]string
	if _, err := c.RawReq("POST", "/", nil, map[string]string{"k": "v"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != "yes" {
		t.Fatalf("%v", out)
	}
}
