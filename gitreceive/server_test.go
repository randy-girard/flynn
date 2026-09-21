package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeApps struct {
	apps map[string]*ct.App
}

func (f *fakeApps) GetApp(id string) (*ct.App, error) {
	if f == nil {
		return nil, controller.ErrNotFound
	}
	if app, ok := f.apps[id]; ok {
		return app, nil
	}
	return nil, controller.ErrNotFound
}

func TestGitHandlerAuthAndRouting(t *testing.T) {
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(nil, auth)

	var forwarded string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		forwarded = string(b)
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	h.webhookURL = upstream.URL
	h.httpClient = upstream.Client()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", status.Path, nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/github/webhook", strings.NewReader(`{"zen":"ok"}`)))
	if rec.Code != 204 {
		t.Fatalf("webhook proxy=%d body=%s", rec.Code, rec.Body.String())
	}
	if forwarded != `{"zen":"ok"}` {
		t.Fatalf("forwarded body %q", forwarded)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/myapp.git/info/refs", nil))
	if rec.Code != 401 {
		t.Fatalf("unauthed=%d", rec.Code)
	}

	authed := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.SetBasicAuth("", "cluster-secret")
		h.ServeHTTP(rec, req)
		return rec
	}
	if got := authed("/not-a-git-service"); got.Code != 403 {
		t.Fatalf("unknown service=%d", got.Code)
	}
	if got := authed("/Bad_Name.git/info/refs"); got.Code != 403 {
		t.Fatalf("invalid app name=%d", got.Code)
	}
}

func TestGitPktLineAndSubCommand(t *testing.T) {
	if subCommand("git-receive-pack") != "receive-pack" || subCommand("git-upload-pack") != "upload-pack" {
		t.Fatal("subCommand")
	}
	var buf bytes.Buffer
	if err := pktLine(&buf, "ok"); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "0006ok" {
		t.Fatalf("pktLine=%q", got)
	}
	if err := pktFlush(&buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("0000")) {
		t.Fatalf("%q", buf.Bytes())
	}

	cmd, _ := gitCommand(gitEnv{App: "demo"}, "true")
	found := false
	for _, e := range cmd.Env {
		if e == "RECEIVE_APP=demo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("env=%v", cmd.Env)
	}
}

func TestGitHandlerTokenGrants(t *testing.T) {
	pubKey, privKey, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := authorizer.ParseTokenKey(pubKey)
	if err != nil {
		t.Fatal(err)
	}
	sign, err := tokensigner.ParseSigningKey(privKey)
	if err != nil {
		t.Fatal(err)
	}
	auth := authorizer.New([]string{"cluster-secret"}, nil, pk, time.Hour)
	apps := &fakeApps{apps: map[string]*ct.App{
		"myapp": {ID: "uuid-1", Name: "myapp"},
		"other": {ID: "uuid-2", Name: "other"},
	}}
	h := newGitHandler(apps, auth)

	var prepared string
	h.prepare = func(cacheKey string) (string, error) {
		prepared = cacheKey
		dir := t.TempDir()
		cmd := exec.Command("git", "init", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git init: %s: %s", err, out)
		}
		return dir, nil
	}

	var forwarded string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		forwarded = string(b)
		if got := r.Header.Get("X-Hub-Signature-256"); got != "sha256=abc" {
			t.Errorf("webhook HMAC header not forwarded: %q", got)
		}
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	h.webhookURL = upstream.URL
	h.httpClient = upstream.Client()

	signTok := func(scopes []string, appID string, perms []string) string {
		t.Helper()
		now := time.Now()
		tok := &api.AccessToken{
			IssueTime:  timestamppb.New(now),
			ExpireTime: timestamppb.New(now.Add(10 * time.Minute)),
			Scopes:     scopes,
		}
		if appID != "" {
			tok.AppGrants = []*api.AppGrant{{AppId: appID, Permissions: perms}}
		}
		s, err := sign.Sign(tok)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	do := func(method, path, cred string, body io.Reader) *httptest.ResponseRecorder {
		t.Helper()
		prepared = ""
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, body)
		switch {
		case cred == "":
		case cred == "cluster":
			req.SetBasicAuth("", "cluster-secret")
		default:
			req.Header.Set("Authorization", "Bearer "+cred)
		}
		h.ServeHTTP(rec, req)
		return rec
	}

	pushPath := "/myapp.git/info/refs?service=git-receive-pack"
	fetchPath := "/myapp.git/info/refs?service=git-upload-pack"
	postPush := "/myapp.git/git-receive-pack"

	// Webhook is HMAC-authenticated upstream; gitreceive must not require git creds.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(`{"zen":"ok"}`))
	req.Header.Set("X-Hub-Signature-256", "sha256=abc")
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("webhook proxy=%d body=%s", rec.Code, rec.Body.String())
	}
	if forwarded != `{"zen":"ok"}` {
		t.Fatalf("forwarded body %q", forwarded)
	}

	if got := do("GET", pushPath, "", nil); got.Code != 401 {
		t.Fatalf("unauthed push=%d", got.Code)
	}

	cases := []struct {
		name     string
		method   string
		path     string
		cred     string
		want     int
		wantPrep string
		skipPrep bool
	}{
		{"cluster_key_push", "GET", pushPath, "cluster", 200, "uuid-1", false},
		{"cluster_key_push_other", "GET", "/other.git/info/refs?service=git-receive-pack", "cluster", 200, "uuid-2", false},
		{"admin_jwt_push", "GET", pushPath, signTok([]string{"cluster:admin"}, "", nil), 200, "uuid-1", false},
		{"deploy_push_own", "GET", pushPath, signTok(nil, "uuid-1", []string{"app:deploy"}), 200, "uuid-1", false},
		{"write_push_own", "GET", pushPath, signTok(nil, "uuid-1", []string{"app:write"}), 200, "uuid-1", false},
		{"build_token_push_own", "GET", pushPath, signTok([]string{"build:artifacts"}, "uuid-1", []string{"app:write"}), 200, "uuid-1", false},
		{"build_token_cannot_push_other", "GET", "/other.git/info/refs?service=git-receive-pack", signTok([]string{"build:artifacts"}, "uuid-1", []string{"app:write"}), 403, "", true},
		{"read_cannot_push", "GET", pushPath, signTok(nil, "uuid-1", []string{"app:read"}), 403, "", true},
		{"read_can_fetch_own", "GET", fetchPath, signTok(nil, "uuid-1", []string{"app:read"}), 200, "uuid-1", false},
		{"read_cannot_fetch_other", "GET", "/other.git/info/refs?service=git-upload-pack", signTok(nil, "uuid-1", []string{"app:read"}), 403, "", true},
		{"wrong_app_cannot_push", "GET", pushPath, signTok(nil, "uuid-2", []string{"app:write"}), 403, "", true},
		{"read_post_receive_pack", "POST", postPush, signTok(nil, "uuid-1", []string{"app:read"}), 403, "", true},
		{"unknown_app", "GET", "/missing.git/info/refs?service=git-receive-pack", "cluster", 404, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := do(tc.method, tc.path, tc.cred, strings.NewReader(""))
			if got.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", got.Code, got.Body.String(), tc.want)
			}
			if tc.skipPrep {
				if prepared != "" {
					t.Fatalf("prepare ran for denied/unknown request: %s", prepared)
				}
				return
			}
			if prepared != tc.wantPrep {
				t.Fatalf("prepare cacheKey=%q, want %q", prepared, tc.wantPrep)
			}
		})
	}
}
