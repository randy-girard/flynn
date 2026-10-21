package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

func TestParseGitRepoPath(t *testing.T) {
	kind, name := parseGitRepoPath("/myapp.git/info/refs", "/info/refs")
	if kind != "app" || name != "myapp" {
		t.Fatalf("app %s %s", kind, name)
	}
	kind, name = parseGitRepoPath("/_preview/shop.git/info/refs", "/info/refs")
	if kind != "preview" || name != "shop" {
		t.Fatalf("preview %s %s", kind, name)
	}
	kind, name = parseGitRepoPath("/_preview/shop/git-receive-pack", "/git-receive-pack")
	if kind != "preview" || name != "shop" {
		t.Fatalf("preview receive %s %s", kind, name)
	}
	if kind, name = parseGitRepoPath("/_preview/Bad_Name.git/info/refs", "/info/refs"); kind != "" || name != "" {
		t.Fatalf("invalid preview %s %s", kind, name)
	}
}

type fakePreview struct {
	info *previewRemoteInfo
	err  error
}

func (f *fakePreview) PreviewRemote(string) (*previewRemoteInfo, error) {
	if f == nil {
		return nil, fmt.Errorf("preview remotes require the pipelines plugin")
	}
	return f.info, f.err
}

func (f *fakePreview) EnsurePreview(string, string, string) (*previewEnsureResult, error) {
	return &previewEnsureResult{AppName: "shop-feat"}, nil
}

func TestGitHandlerPreviewRemote(t *testing.T) {
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	apps := &fakeApps{apps: map[string]*ct.App{"myapp": {ID: "uuid-1", Name: "myapp"}}}
	h := newGitHandler(apps, auth)
	h.preview = &fakePreview{info: &previewRemoteInfo{Available: false, Reason: "preview apps are not enabled on this pipeline"}}
	h.prepare = func(cacheKey string) (string, error) {
		return "", fmt.Errorf("prepare should not run")
	}

	do := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.SetBasicAuth("", "cluster-secret")
		h.ServeHTTP(rec, req)
		return rec
	}

	if got := do("/_preview/shop.git/info/refs?service=git-receive-pack"); got.Code != 404 || !strings.Contains(got.Body.String(), "not enabled") {
		t.Fatalf("disabled=%d %s", got.Code, got.Body.String())
	}

	var prepared string
	h.preview = &fakePreview{info: &previewRemoteInfo{Available: true, Pipeline: "shop", AppIDs: []string{"uuid-1"}, AppNames: []string{"myapp"}}}
	h.prepare = func(cacheKey string) (string, error) {
		prepared = cacheKey
		dir := t.TempDir()
		cmd := exec.Command("git", "init", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git init: %s: %s", err, out)
		}
		return dir, nil
	}
	if got := do("/_preview/shop.git/info/refs?service=git-receive-pack"); got.Code != 200 {
		t.Fatalf("enabled=%d %s", got.Code, got.Body.String())
	}
	if prepared != "preview-shop" {
		t.Fatalf("prepare cacheKey=%q", prepared)
	}

	h.preview = nil
	if got := do("/_preview/shop.git/info/refs?service=git-receive-pack"); got.Code != 404 {
		t.Fatalf("no plugin=%d", got.Code)
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

func TestRepairDanglingHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(dir, ".git", "HEAD")
	missing := "fde5360677540c4e27a73aad4be3d7baf7727231"
	if err := os.WriteFile(head, []byte(missing+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repairDanglingHEAD(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(head)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "ref: refs/heads/main" {
		t.Fatalf("HEAD = %q", got)
	}

	// A detached HEAD whose object exists is a valid repo and must be kept.
	write := exec.Command("git", "config", "user.email", "t@example.com")
	write.Dir = dir
	if err := write.Run(); err != nil {
		t.Fatal(err)
	}
	write = exec.Command("git", "config", "user.name", "t")
	write.Dir = dir
	if err := write.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "f.txt")
	add.Dir = dir
	if err := add.Run(); err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "commit", "-q", "-m", "init")
	commit.Dir = dir
	if err := commit.Run(); err != nil {
		t.Fatal(err)
	}
	rev := exec.Command("git", "rev-parse", "HEAD")
	rev.Dir = dir
	out, err := rev.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	if err := os.WriteFile(head, []byte(sha+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repairDanglingHEAD(dir); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(head)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != sha {
		t.Fatalf("existing object HEAD changed to %q", got)
	}
}

func TestArchiveTreeLeavesHEADAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if strings.Contains(archiveTreeFunc, "git checkout --force --quiet $1") || strings.Contains(string(previewPrereceiveHook), "unset GIT_QUARANTINE_PATH") {
		t.Fatal("preview hook still checks out into the receive repository")
	}
	dir := t.TempDir()
	script := []struct {
		name string
		args []string
	}{
		{"init", []string{"init", "-q"}},
		{"email", []string{"config", "user.email", "t@example.com"}},
		{"name", []string{"config", "user.name", "t"}},
	}
	for _, step := range script {
		cmd := exec.Command("git", step.args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "file.txt"), []byte("hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "app/file.txt")
	add.Dir = dir
	if err := add.Run(); err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "commit", "-q", "-m", "init")
	commit.Dir = dir
	if err := commit.Run(); err != nil {
		t.Fatal(err)
	}
	revCmd := exec.Command("git", "rev-parse", "HEAD")
	revCmd.Dir = dir
	revOut, err := revCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	rev := strings.TrimSpace(string(revOut))
	before, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(dir, "out.tar")
	hook := exec.Command("bash", "-c", archiveTreeFunc+`
git-archive-all "$1" > "$2"
`, "bash", rev, tarPath)
	hook.Dir = filepath.Join(dir, ".git")
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("HEAD changed from %q to %q", before, after)
	}
	list := exec.Command("tar", "-tf", tarPath)
	listed, err := list.Output()
	if err != nil {
		t.Fatalf("tar: %v", err)
	}
	if !strings.Contains(string(listed), "app/file.txt") {
		t.Fatalf("archive missing file: %s", listed)
	}
}
