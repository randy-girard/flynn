package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/githubapp"
	"golang.org/x/net/context"
)

type memGitHubStore struct {
	mu    sync.Mutex
	cfg   *ct.GitHubAppConfig
	conns map[string]*ct.GitHubRepoConnection
}

func newMemGitHubStore() *memGitHubStore {
	return &memGitHubStore{cfg: &ct.GitHubAppConfig{}, conns: map[string]*ct.GitHubRepoConnection{}}
}

func (m *memGitHubStore) GetConfig() (*ct.GitHubAppConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *m.cfg
	return &cp, nil
}

func (m *memGitHubStore) UpdateConfig(cfg *ct.GitHubAppConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *cfg
	m.cfg = &cp
	return nil
}

func (m *memGitHubStore) GetConnection(appID string) (*ct.GitHubRepoConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[appID]
	if !ok {
		return nil, ct.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *memGitHubStore) ListByRepo(owner, repo string) ([]*ct.GitHubRepoConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*ct.GitHubRepoConnection
	for _, c := range m.conns {
		if strings.EqualFold(c.Owner, owner) && strings.EqualFold(c.Repo, repo) {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *memGitHubStore) ListAll() ([]*ct.GitHubRepoConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ct.GitHubRepoConnection, 0, len(m.conns))
	for _, c := range m.conns {
		cp := *c
		out = append(out, &cp)
	}
	return out, nil
}

func (m *memGitHubStore) PutConnection(c *ct.GitHubRepoConnection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ID == "" {
		c.ID = "conn-" + c.AppID
	}
	cp := *c
	m.conns[c.AppID] = &cp
	return nil
}

func (m *memGitHubStore) UpdateDeployState(c *ct.GitHubRepoConnection) error {
	return m.PutConnection(c)
}

func (m *memGitHubStore) DeleteConnection(appID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns, appID)
	return nil
}

type fakeGitHubAPI struct {
	insts   []githubapp.Installation
	repos   map[int64][]githubapp.Repo
	sha     string
	checks  githubapp.CheckState
	token   string
	findErr error
}

func (f *fakeGitHubAPI) ListInstallations() ([]githubapp.Installation, error) {
	return f.insts, nil
}
func (f *fakeGitHubAPI) ListRepos(id int64) ([]githubapp.Repo, error) { return f.repos[id], nil }
func (f *fakeGitHubAPI) FindRepo(owner, repo string) (*githubapp.Installation, *githubapp.Repo, error) {
	if f.findErr != nil {
		return nil, nil, f.findErr
	}
	want := strings.ToLower(owner + "/" + repo)
	for _, inst := range f.insts {
		for i := range f.repos[inst.ID] {
			if strings.ToLower(f.repos[inst.ID][i].FullName) == want {
				r := f.repos[inst.ID][i]
				in := inst
				return &in, &r, nil
			}
		}
	}
	return nil, nil, fmtError("not found")
}
func (f *fakeGitHubAPI) CommitSHA(int64, string, string, string) (string, error) { return f.sha, nil }
func (f *fakeGitHubAPI) ChecksForSHA(int64, string, string, string) (githubapp.CheckState, error) {
	if f.checks == "" {
		return githubapp.CheckPassed, nil
	}
	return f.checks, nil
}
func (f *fakeGitHubAPI) InstallationToken(int64) (*githubapp.Token, error) {
	tok := f.token
	if tok == "" {
		tok = "ghs_test"
	}
	return &githubapp.Token{Token: tok}, nil
}

type fmtError string

func (e fmtError) Error() string { return string(e) }

type fakeTaffy struct {
	mu    sync.Mutex
	calls []map[string]string
	jobID string
}

func (f *fakeTaffy) Launch(app *ct.App, cloneURL, branch, sha string, meta map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, map[string]string{"app": app.Name, "clone": cloneURL, "branch": branch, "sha": sha})
	if f.jobID == "" {
		return "job-1", nil
	}
	return f.jobID, nil
}

func jsonReq(method, path string, body []byte) *http.Request {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func testPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestGitHubAppConfigRoundTrip(t *testing.T) {
	store := newMemGitHubStore()
	api := &controllerAPI{githubStore: store, githubDomain: "example.local"}
	pemKey := testPEM(t)

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(ct.GitHubAppConfig{AppID: 99, Slug: "flynn-deploy", PrivateKey: pemKey, WebhookSecret: "whsec"})
	req := jsonReq(http.MethodPut, "/github/app", body)
	api.UpdateGitHubApp(context.Background(), rec, req)
	if rec.Code != 200 {
		t.Fatalf("put %d %s", rec.Code, rec.Body.Bytes())
	}
	var got ct.GitHubAppConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PrivateKey != "" || !got.Configured || got.AppID != 99 || got.WebhookURL == "" {
		t.Fatalf("%+v", got)
	}

	rec = httptest.NewRecorder()
	api.GetGitHubApp(context.Background(), rec, httptest.NewRequest(http.MethodGet, "/github/app", nil))
	if rec.Code != 200 {
		t.Fatalf("get %d", rec.Code)
	}
	var wrap struct {
		Config ct.GitHubAppConfig   `json:"config"`
		Setup  githubapp.SetupGuide `json:"setup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wrap); err != nil {
		t.Fatal(err)
	}
	if !wrap.Config.Configured || wrap.Config.PrivateKey != "" || len(wrap.Setup.Steps) != 7 {
		t.Fatalf("%+v", wrap)
	}
}

func TestGitHubConnectAndManualDeploy(t *testing.T) {
	store := newMemGitHubStore()
	gh := &fakeGitHubAPI{
		insts: []githubapp.Installation{{ID: 7, Account: githubapp.Account{Login: "acme", Type: "Organization"}}},
		repos: map[int64][]githubapp.Repo{7: {{Name: "app", FullName: "acme/app", DefaultBranch: "main"}}},
		sha:   "deadbeef",
	}
	taffy := &fakeTaffy{}
	api := &controllerAPI{githubStore: store, githubAPI: gh, taffy: taffy, githubDomain: "example.local"}
	app := &ct.App{ID: "app-1", Name: "demo"}
	ctx := context.WithValue(context.Background(), "app", app)

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(ct.GitHubRepoConnection{Repo: "acme/app", AutoDeploy: true, WaitForChecks: true})
	req := jsonReq(http.MethodPut, "/apps/app-1/github", body)
	api.PutAppGitHub(ctx, rec, req)
	if rec.Code != 200 {
		t.Fatalf("connect %d %s", rec.Code, rec.Body.Bytes())
	}
	var conn ct.GitHubRepoConnection
	_ = json.Unmarshal(rec.Body.Bytes(), &conn)
	if conn.InstallationID != 7 || conn.Branch != "main" || conn.Owner != "acme" || !conn.WaitForChecks {
		t.Fatalf("%+v", conn)
	}

	rec = httptest.NewRecorder()
	api.DeployAppGitHub(ctx, rec, jsonReq(http.MethodPost, "/apps/app-1/github/deploy", []byte("{}")))
	if rec.Code != 200 {
		t.Fatalf("deploy %d %s", rec.Code, rec.Body.Bytes())
	}
	if len(taffy.calls) != 1 || taffy.calls[0]["sha"] != "deadbeef" {
		t.Fatalf("taffy %+v", taffy.calls)
	}
	if !strings.Contains(taffy.calls[0]["clone"], "x-access-token:") {
		t.Fatalf("clone %s", taffy.calls[0]["clone"])
	}
}

func TestGitHubWebhookPushAndChecks(t *testing.T) {
	store := newMemGitHubStore()
	_ = store.UpdateConfig(&ct.GitHubAppConfig{AppID: 1, WebhookSecret: "whsec", PrivateKey: "x"})
	_ = store.PutConnection(&ct.GitHubRepoConnection{
		AppID: "app-1", InstallationID: 7, Owner: "acme", Repo: "app", Branch: "main", AutoDeploy: true, WaitForChecks: true,
	})
	gh := &fakeGitHubAPI{sha: "abc", checks: githubapp.CheckPending, token: "tok"}
	taffy := &fakeTaffy{}
	api := &controllerAPI{githubStore: store, githubAPI: gh, taffy: taffy}

	sign := func(body []byte) string {
		mac := hmac.New(sha256.New, []byte("whsec"))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	post := func(event string, body []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
		req.Header.Set(githubapp.HeaderEvent, event)
		req.Header.Set(githubapp.HeaderSignature, sign(body))
		api.GitHubWebhook(context.Background(), rec, req)
		return rec
	}

	push := []byte(`{"ref":"refs/heads/main","after":"abc","repository":{"name":"app","full_name":"acme/app","owner":{"login":"acme"}},"installation":{"id":7}}`)
	if rec := post(githubapp.EventPush, push); rec.Code != 200 {
		t.Fatalf("push %d %s", rec.Code, rec.Body.Bytes())
	}
	if len(taffy.calls) != 0 {
		t.Fatalf("should wait for checks: %+v", taffy.calls)
	}
	conn, _ := store.GetConnection("app-1")
	if conn.PendingSHA != "abc" {
		t.Fatalf("pending %s", conn.PendingSHA)
	}

	gh.checks = githubapp.CheckPassed
	suite := []byte(`{"action":"completed","check_suite":{"head_sha":"abc","head_branch":"main","status":"completed","conclusion":"success"},"repository":{"name":"app","owner":{"login":"acme"}},"installation":{"id":7}}`)
	if rec := post(githubapp.EventCheckSuite, suite); rec.Code != 200 {
		t.Fatalf("suite %d %s", rec.Code, rec.Body.Bytes())
	}
	if len(taffy.calls) != 1 || taffy.calls[0]["sha"] != "abc" {
		t.Fatalf("deploy after checks %+v", taffy.calls)
	}

	bad := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(push))
	req.Header.Set(githubapp.HeaderEvent, githubapp.EventPush)
	req.Header.Set(githubapp.HeaderSignature, "sha256=00")
	api.GitHubWebhook(context.Background(), bad, req)
	if bad.Code != 401 {
		t.Fatalf("bad sig %d", bad.Code)
	}
}

func TestGitHubWebhookPingAndAutoDeployWithoutChecks(t *testing.T) {
	store := newMemGitHubStore()
	_ = store.UpdateConfig(&ct.GitHubAppConfig{WebhookSecret: "s"})
	_ = store.PutConnection(&ct.GitHubRepoConnection{
		AppID: "app-1", InstallationID: 7, Owner: "acme", Repo: "app", Branch: "main", AutoDeploy: true,
	})
	taffy := &fakeTaffy{}
	api := &controllerAPI{githubStore: store, githubAPI: &fakeGitHubAPI{token: "t"}, taffy: taffy}
	body := []byte(`{"zen":"ok"}`)
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write(body)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
	req.Header.Set(githubapp.HeaderEvent, githubapp.EventPing)
	req.Header.Set(githubapp.HeaderSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	api.GitHubWebhook(context.Background(), rec, req)
	if rec.Code != 200 {
		t.Fatalf("ping %d", rec.Code)
	}

	push := []byte(`{"ref":"refs/heads/main","after":"fff","repository":{"name":"app","owner":{"login":"acme"}}}`)
	mac = hmac.New(sha256.New, []byte("s"))
	mac.Write(push)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(push))
	req.Header.Set(githubapp.HeaderEvent, githubapp.EventPush)
	req.Header.Set(githubapp.HeaderSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	api.GitHubWebhook(context.Background(), rec, req)
	if rec.Code != 200 || len(taffy.calls) != 1 {
		t.Fatalf("auto deploy %d %+v %s", rec.Code, taffy.calls, rec.Body.Bytes())
	}
}

func TestGitHubUpdatePreservesSecrets(t *testing.T) {
	store := newMemGitHubStore()
	pemKey := testPEM(t)
	api := &controllerAPI{githubStore: store}
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(ct.GitHubAppConfig{AppID: 1, PrivateKey: pemKey, WebhookSecret: "keep"})
	api.UpdateGitHubApp(context.Background(), rec, jsonReq(http.MethodPut, "/github/app", body))
	if rec.Code != 200 {
		t.Fatalf("%s", rec.Body.Bytes())
	}
	rec = httptest.NewRecorder()
	api.UpdateGitHubApp(context.Background(), rec, jsonReq(http.MethodPut, "/github/app", []byte(`{"app_id":1,"slug":"x"}`)))
	cfg, _ := store.GetConfig()
	if cfg.PrivateKey != pemKey || cfg.WebhookSecret != "keep" || cfg.Slug != "x" {
		t.Fatalf("%+v", cfg)
	}
}

func TestParseGitHubAppID(t *testing.T) {
	id, err := parseGitHubAppID("12")
	if err != nil || id != 12 {
		t.Fatalf("%d %v", id, err)
	}
	if _, err := parseGitHubAppID(""); err == nil {
		t.Fatal("empty")
	}
}

func githubListAPI() *controllerAPI {
	store := newMemGitHubStore()
	_ = store.PutConnection(&ct.GitHubRepoConnection{AppID: "app-1", InstallationID: 3, Owner: "acme", Repo: "app"})
	return &controllerAPI{
		githubStore: store,
		githubAPI: &fakeGitHubAPI{
			insts: []githubapp.Installation{
				{ID: 3, Account: githubapp.Account{Login: "acme", Type: "Organization"}},
				{ID: 9, Account: githubapp.Account{Login: "other", Type: "User"}},
			},
			repos: map[int64][]githubapp.Repo{
				3: {{Name: "app", FullName: "acme/app", DefaultBranch: "master"}},
				9: {{Name: "secret", FullName: "other/secret", DefaultBranch: "main", Private: true}},
			},
		},
	}
}

func tokenCtx(tok *authorizer.Token) context.Context {
	return context.WithValue(context.Background(), authz.TokenContextKey, tok)
}

func installationReposCtx(tok *authorizer.Token, id string) context.Context {
	ctx := tokenCtx(tok)
	return ctxhelper.NewContextParams(ctx, httprouter.Params{{Key: "installation_id", Value: id}})
}

func TestGitHubListInstallations(t *testing.T) {
	api := githubListAPI()
	rec := httptest.NewRecorder()
	api.ListGitHubInstallations(context.Background(), rec, httptest.NewRequest(http.MethodGet, "/github/installations", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.Bytes())
	}
	var insts []ct.GitHubInstallation
	_ = json.Unmarshal(rec.Body.Bytes(), &insts)
	if len(insts) != 2 {
		t.Fatalf("unauthenticated/admin catalog = %+v, want both installations", insts)
	}
}

func TestGitHubListInstallationsFiltersToLinkedApps(t *testing.T) {
	api := githubListAPI()
	tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:github:write"}}}}
	rec := httptest.NewRecorder()
	api.ListGitHubInstallations(tokenCtx(tok), rec, httptest.NewRequest(http.MethodGet, "/github/installations", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.Bytes())
	}
	var insts []ct.GitHubInstallation
	_ = json.Unmarshal(rec.Body.Bytes(), &insts)
	if len(insts) != 1 || insts[0].ID != 3 || insts[0].Login != "acme" {
		t.Fatalf("filtered catalog = %+v, want only acme (3)", insts)
	}

	rec = httptest.NewRecorder()
	api.ListGitHubInstallationRepos(installationReposCtx(tok, "3"), rec, httptest.NewRequest(http.MethodGet, "/github/installations/3/repos", nil))
	if rec.Code != 200 {
		t.Fatalf("linked repos %d %s", rec.Code, rec.Body.Bytes())
	}
	var repos []ct.GitHubRepo
	_ = json.Unmarshal(rec.Body.Bytes(), &repos)
	if len(repos) != 1 || repos[0].FullName != "acme/app" {
		t.Fatalf("linked repos = %+v", repos)
	}

	rec = httptest.NewRecorder()
	api.ListGitHubInstallationRepos(installationReposCtx(tok, "9"), rec, httptest.NewRequest(http.MethodGet, "/github/installations/9/repos", nil))
	if rec.Code != 200 {
		t.Fatalf("unlinked repos %d %s", rec.Code, rec.Body.Bytes())
	}
	repos = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &repos)
	if len(repos) != 0 {
		t.Fatalf("unlinked installation must not list repos: %+v", repos)
	}
}

func TestGitHubListInstallationsFirstConnectUnfiltered(t *testing.T) {
	api := githubListAPI()
	tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-new", Permissions: []string{"app:write"}}}}
	rec := httptest.NewRecorder()
	api.ListGitHubInstallations(tokenCtx(tok), rec, httptest.NewRequest(http.MethodGet, "/github/installations", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.Bytes())
	}
	var insts []ct.GitHubInstallation
	_ = json.Unmarshal(rec.Body.Bytes(), &insts)
	if len(insts) != 2 {
		t.Fatalf("first connect must list the GitHub App catalog for the picker: %+v", insts)
	}

	rec = httptest.NewRecorder()
	api.ListGitHubInstallationRepos(installationReposCtx(tok, "9"), rec, httptest.NewRequest(http.MethodGet, "/github/installations/9/repos", nil))
	if rec.Code != 200 {
		t.Fatalf("first-connect repos %d %s", rec.Code, rec.Body.Bytes())
	}
	var repos []ct.GitHubRepo
	_ = json.Unmarshal(rec.Body.Bytes(), &repos)
	if len(repos) != 1 || repos[0].FullName != "other/secret" {
		t.Fatalf("first-connect repos = %+v", repos)
	}
}

func TestGitHubListInstallationsAdminSeesAll(t *testing.T) {
	api := githubListAPI()
	tok := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	rec := httptest.NewRecorder()
	api.ListGitHubInstallations(tokenCtx(tok), rec, httptest.NewRequest(http.MethodGet, "/github/installations", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.Bytes())
	}
	var insts []ct.GitHubInstallation
	_ = json.Unmarshal(rec.Body.Bytes(), &insts)
	if len(insts) != 2 {
		t.Fatalf("admin catalog = %+v, want both installations", insts)
	}
}
