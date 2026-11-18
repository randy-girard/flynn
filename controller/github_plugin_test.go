package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"golang.org/x/net/context"
)

func TestGitHubPluginProxyWhenHealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/status" {
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		}
		if r.URL.Path != "/github/app" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": ct.GitHubAppConfig{Configured: true, AppID: 7, Slug: "from-plugin"},
			"setup":  map[string]any{"steps": []any{}},
		})
	}))
	defer ts.Close()
	githubPluginMu.Lock()
	githubPluginOK = false
	githubPluginChecked = time.Time{}
	githubPluginMu.Unlock()

	api := &controllerAPI{githubPluginURL: ts.URL, githubHTTP: ts.Client()}
	rec := httptest.NewRecorder()
	api.maybeGitHubPlugin(func(ctx context.Context, w http.ResponseWriter, req *http.Request) {
		t.Fatal("local handler should not run")
	})(context.Background(), rec, httptest.NewRequest(http.MethodGet, "/github/app", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-plugin") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestGitHubExportUsesLocalStore(t *testing.T) {
	store := newMemGitHubStore()
	_ = store.UpdateConfig(&ct.GitHubAppConfig{AppID: 3, PrivateKey: "pem", WebhookSecret: "s"})
	_ = store.PutConnection(&ct.GitHubRepoConnection{AppID: "app-1", Owner: "acme", Repo: "app"})
	api := &controllerAPI{githubStore: store}
	rec := httptest.NewRecorder()
	api.ExportGitHub(context.Background(), rec, httptest.NewRequest(http.MethodGet, "/github/export", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.Bytes())
	}
	if !strings.Contains(rec.Body.String(), `"pem"`) || !strings.Contains(rec.Body.String(), "app-1") {
		t.Fatalf("%s", rec.Body.String())
	}
}

// The proxy forwards with the controller key, so the plugin cannot tell a
// tenant from an operator unless the controller says who is calling.
func TestGitHubPluginProxyMarksNonAdminCallers(t *testing.T) {
	got := make(chan string, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/status" {
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		}
		got <- r.Header.Get("X-Flynn-Origin")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer ts.Close()
	githubPluginMu.Lock()
	githubPluginOK = false
	githubPluginChecked = time.Time{}
	githubPluginMu.Unlock()
	api := &controllerAPI{githubPluginURL: ts.URL, githubHTTP: ts.Client()}
	call := func(tok *authorizer.Token, spoof string) string {
		t.Helper()
		ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
		req := httptest.NewRequest(http.MethodPut, "/apps/app-b/github", strings.NewReader("{}"))
		if spoof != "" {
			req.Header.Set("X-Flynn-Origin", spoof)
		}
		api.maybeGitHubPlugin(func(context.Context, http.ResponseWriter, *http.Request) {
			t.Fatal("local handler should not run")
		})(ctx, httptest.NewRecorder(), req)
		return <-got
	}
	if v := call(&authorizer.Token{UserID: "u1", Scopes: []string{"app:write"}}, ""); v != "user" {
		t.Fatalf("tenant token origin %q", v)
	}
	if v := call(&authorizer.Token{Scopes: []string{"cluster:admin"}}, "user"); v != "" {
		t.Fatalf("cluster admin origin %q (client-supplied value must not pass through)", v)
	}
	if v := call(&authorizer.Token{UserID: "u1"}, ""); v != "user" {
		t.Fatalf("empty-scope token origin %q", v)
	}
}
