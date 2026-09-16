package plugin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	host "github.com/flynn/flynn/host/types"
)

func writeAppPlugin(t *testing.T, dir, name string) {
	t.Helper()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": name,
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/" + name}},
			},
		},
	})
}

func TestInstallRejectsGitHubRebuild(t *testing.T) {
	err := (&Installer{}).Install(InstallOptions{
		Source:  "https://github.com/acme/flynn-plugin-redis.git",
		Rebuild: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--rebuild") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallNoBuildWithoutDist(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	err := (&Installer{}).Install(InstallOptions{Source: dir, NoBuild: true})
	if err == nil || !strings.Contains(err.Error(), "--no-build") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallMissingBuildScript(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	err := (&Installer{Stderr: io.Discard}).Install(InstallOptions{Source: dir})
	if err == nil || !strings.Contains(err.Error(), "plugin-build") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallInvokesBuildHook(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	called := false
	err := (&Installer{
		Build: func(root string) error {
			called = true
			if root != dir {
				t.Fatalf("root=%s want %s", root, dir)
			}
			return fmt.Errorf("stop")
		},
	}).Install(InstallOptions{Source: dir})
	if !called {
		t.Fatal("Build was not called")
	}
	if err == nil || !strings.Contains(err.Error(), "plugin-build") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitHTTP(srv.Client(), srv.URL, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitHTTP(http.DefaultClient, "http://127.0.0.1:1", 0); err == nil {
		t.Fatal("zero timeout must fail")
	}
}

func TestPutFileAndBytes(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method=%s", r.Method)
		}
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "layer.squashfs")
	if err := os.WriteFile(path, []byte("layer-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := putFile(srv.Client(), srv.URL+"/layers/a.squashfs", path); err != nil {
		t.Fatal(err)
	}
	if string(got) != "layer-bytes" {
		t.Fatalf("putFile body=%q", got)
	}
	if err := putBytes(srv.Client(), srv.URL+"/manifest.json", []byte("manifest")); err != nil {
		t.Fatal(err)
	}
	if string(got) != "manifest" {
		t.Fatalf("putBytes body=%q", got)
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()
	if err := putBytes(fail.Client(), fail.URL, []byte("x")); err == nil {
		t.Fatal("500 must fail")
	}
}

func TestDisplaySourceAndLogf(t *testing.T) {
	if got := displaySource(&Resolved{GitHub: &GitHubSource{Owner: "acme", Repo: "plug", Ref: "v1"}}, "/tmp"); got != "acme/plug@v1" {
		t.Fatalf("github display=%s", got)
	}
	if got := displaySource(&Resolved{}, "/tmp/plugin"); got != "/tmp/plugin" {
		t.Fatalf("local display=%s", got)
	}
	if got := displaySource(nil, "/tmp/plugin"); got != "/tmp/plugin" {
		t.Fatalf("nil display=%s", got)
	}

	in := &Installer{}
	in.logf("hello %s", "world")
	var buf bytes.Buffer
	in.Stdout = &buf
	in.logf("hello %s", "world")
	if !strings.Contains(buf.String(), "hello world") {
		t.Fatalf("log=%q", buf.String())
	}
}

func TestWaitHTTPRetriesThenOK(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitHTTP(srv.Client(), srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("retries=%d", n)
	}
}

func TestPutFileMissingAndRunHookSecrets(t *testing.T) {
	if err := putFile(http.DefaultClient, "http://127.0.0.1/x", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file must fail")
	}

	root := t.TempDir()
	script := filepath.Join(root, "install.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nset -e\ntest \"$CONTROLLER_KEY\" = secret\ntest -n \"$FLYNN_PLUGIN_NAME\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Stdout: io.Discard, Stderr: io.Discard}
	m := &Manifest{Name: "widget", Kind: KindApp}
	if err := in.runHook(root, m, "install.sh", map[string]string{"CONTROLLER_KEY": "secret", "CLUSTER_DOMAIN": "example.local"}); err != nil {
		t.Fatal(err)
	}
	if err := in.runHook(root, m, "install.sh", map[string]string{"CONTROLLER_KEY": "wrong"}); err == nil {
		t.Fatal("hook must see injected CONTROLLER_KEY")
	}
}

func TestInstallHookAndRunHook(t *testing.T) {
	m := &Manifest{Name: "widget", Kind: KindApp}
	if m.installHook() != "" {
		t.Fatal("empty hooks")
	}
	m.Hooks = &Hooks{Install: "hooks/install.sh"}
	if m.installHook() != "hooks/install.sh" {
		t.Fatal(m.installHook())
	}
	in := &Installer{}
	if err := in.runHook(t.TempDir(), m, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := in.runHook(t.TempDir(), m, "hooks/missing.sh", map[string]string{}); err == nil {
		t.Fatal("missing hook must fail")
	}
}

type providerStub struct {
	list      []*ct.Provider
	listErr   error
	created   *ct.Provider
	createErr error
}

func (s *providerStub) ProviderList() ([]*ct.Provider, error) {
	return s.list, s.listErr
}

func (s *providerStub) CreateProvider(p *ct.Provider) error {
	s.created = p
	return s.createErr
}

func TestEnsureProviderIdempotentAndCreates(t *testing.T) {
	existing := &providerStub{list: []*ct.Provider{{Name: "redis", URL: "http://redis-api.discoverd"}}}
	if err := ensureProvider(existing, "redis", "http://other"); err != nil {
		t.Fatal(err)
	}
	if existing.created != nil {
		t.Fatal("must not recreate an existing provider")
	}

	created := &providerStub{}
	if err := ensureProvider(created, "redis", "http://redis-api.discoverd/clusters"); err != nil {
		t.Fatal(err)
	}
	if created.created == nil || created.created.Name != "redis" || created.created.URL != "http://redis-api.discoverd/clusters" {
		t.Fatalf("%+v", created.created)
	}

	if err := ensureProvider(&providerStub{listErr: fmt.Errorf("down")}, "redis", "http://x"); err == nil {
		t.Fatal("list error")
	}
	if err := ensureProvider(&providerStub{createErr: fmt.Errorf("denied")}, "redis", "http://x"); err == nil {
		t.Fatal("create error")
	}
}

func TestInstallerHTTPAndRunBuildMissing(t *testing.T) {
	in := &Installer{}
	if in.http() != http.DefaultClient {
		t.Fatal("default HTTP client")
	}
	in.persistInventory() // nil Client must be a no-op
	if err := in.runBuild(t.TempDir()); err == nil || !strings.Contains(err.Error(), "plugin-build") {
		t.Fatalf("missing plugin-build script: %v", err)
	}
}

type webhookHostStub struct {
	id      string
	listed  []*host.WebhookConfig
	listErr error
	added   []webhookAdd
	addErr  error
}

type webhookAdd struct {
	id      string
	url     string
	headers map[string]string
}

func (s *webhookHostStub) ID() string { return s.id }

func (s *webhookHostStub) ListWebhooks() ([]*host.WebhookConfig, error) {
	return s.listed, s.listErr
}

func (s *webhookHostStub) AddWebhook(id, url string, headers map[string]string) (*host.WebhookConfig, error) {
	s.added = append(s.added, webhookAdd{id: id, url: url, headers: headers})
	if s.addErr != nil {
		return nil, s.addErr
	}
	return &host.WebhookConfig{ID: id, URL: url, Headers: headers}, nil
}

func TestExpandWebhookSpecSecretEnv(t *testing.T) {
	url, headers, err := expandWebhookSpec(WebhookSpec{
		URL:       "http://${APP}.discoverd/webhooks/flynn",
		SecretEnv: "WEBHOOK_INGEST_SECRET",
		Headers:   map[string]string{"X-Extra": "v-${CLUSTER_DOMAIN}"},
	}, map[string]string{
		"APP":                   "dashboard",
		"WEBHOOK_INGEST_SECRET": "s3cret",
		"CLUSTER_DOMAIN":        "ex.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://dashboard.discoverd/webhooks/flynn" {
		t.Fatalf("url=%q", url)
	}
	if headers["X-Flynn-Webhook-Secret"] != "s3cret" || headers["X-Extra"] != "v-ex.local" {
		t.Fatalf("headers=%v", headers)
	}
	_, _, err = expandWebhookSpec(WebhookSpec{URL: "http://x", SecretEnv: "WEBHOOK_INGEST_SECRET"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "WEBHOOK_INGEST_SECRET") {
		t.Fatalf("empty secret: %v", err)
	}

	url, headers, err = expandWebhookSpec(WebhookSpec{
		URL:       "http://widget.discoverd/hook",
		SecretEnv: "WEBHOOK_INGEST_SECRET",
		Headers:   map[string]string{"X-Flynn-Webhook-Secret": "explicit"},
	}, map[string]string{"WEBHOOK_INGEST_SECRET": "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if headers["X-Flynn-Webhook-Secret"] != "explicit" {
		t.Fatalf("explicit secret header must win, got %v", headers)
	}

	url, headers, err = expandWebhookSpec(WebhookSpec{URL: "http://widget.discoverd/hook"}, nil)
	if err != nil || url != "http://widget.discoverd/hook" || headers != nil {
		t.Fatalf("no headers: url=%q headers=%v err=%v", url, headers, err)
	}

	_, _, err = expandWebhookSpec(WebhookSpec{URL: "ftp://widget.discoverd/hook"}, nil)
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("ftp: %v", err)
	}
	_, _, err = expandWebhookSpec(WebhookSpec{URL: "${MISSING}"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("unexpanded: %v", err)
	}
	_, _, err = expandWebhookSpec(WebhookSpec{URL: "${EMPTY}"}, map[string]string{"EMPTY": ""})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty expand: %v", err)
	}
}

func TestEnsureWebhooksRegistersAndIsIdempotent(t *testing.T) {
	stub := &webhookHostStub{id: "node1"}
	in := &Installer{
		Stdout: io.Discard,
		Hosts:  func() ([]WebhookHost, error) { return []WebhookHost{stub}, nil },
	}
	m := &Manifest{
		Name: "dashboard",
		Webhooks: []WebhookSpec{{
			URL:       "http://dashboard.discoverd/webhooks/flynn",
			SecretEnv: "WEBHOOK_INGEST_SECRET",
		}},
	}
	cluster := map[string]string{"WEBHOOK_INGEST_SECRET": "s3cret"}
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(stub.added) != 1 || stub.added[0].url != "http://dashboard.discoverd/webhooks/flynn" {
		t.Fatalf("added=%+v", stub.added)
	}
	if stub.added[0].headers["X-Flynn-Webhook-Secret"] != "s3cret" {
		t.Fatalf("headers=%v", stub.added[0].headers)
	}
	wantID := pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn")
	if stub.added[0].id != wantID {
		t.Fatalf("id=%s want %s", stub.added[0].id, wantID)
	}

	stub.listed = []*host.WebhookConfig{{ID: "manual", URL: "http://dashboard.discoverd/webhooks/flynn"}}
	stub.added = nil
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(stub.added) != 0 {
		t.Fatalf("must not duplicate an existing URL, added=%+v", stub.added)
	}

	if err := in.ensureWebhooks(&Manifest{Name: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	in.Hosts = func() ([]WebhookHost, error) { return nil, nil }
	if err := in.ensureWebhooks(m, cluster); err == nil {
		t.Fatal("no hosts must fail")
	}

	stub2 := &webhookHostStub{id: "node1"}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{stub2}, fmt.Errorf("discoverd down") }
	if err := in.ensureWebhooks(m, cluster); err == nil || !strings.Contains(err.Error(), "discoverd down") {
		t.Fatalf("hosts error: %v", err)
	}

	failAdd := &webhookHostStub{id: "node1", addErr: fmt.Errorf("denied")}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{failAdd}, nil }
	if err := in.ensureWebhooks(m, cluster); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("add error: %v", err)
	}

	failList := &webhookHostStub{id: "node1", listErr: fmt.Errorf("list failed")}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{failList}, nil }
	if err := in.ensureWebhooks(m, cluster); err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("list error: %v", err)
	}

	n1 := &webhookHostStub{id: "n1"}
	n2 := &webhookHostStub{id: "n2"}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{n1, n2}, nil }
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(n1.added) != 1 || len(n2.added) != 1 {
		t.Fatalf("must register on every host n1=%d n2=%d", len(n1.added), len(n2.added))
	}

	sameID := &webhookHostStub{
		id:     "node1",
		listed: []*host.WebhookConfig{{ID: wantID, URL: "http://dashboard.discoverd/webhooks/flynn"}},
	}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{sameID}, nil }
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(sameID.added) != 1 {
		t.Fatalf("same plugin id must upsert headers, added=%d", len(sameID.added))
	}
}

func TestPluginWebhookIDStable(t *testing.T) {
	a := pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn")
	b := pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn")
	c := pluginWebhookID("other", "http://dashboard.discoverd/webhooks/flynn")
	if a != b || !strings.HasPrefix(a, "plugin-dashboard-") {
		t.Fatalf("id=%s", a)
	}
	if a == c {
		t.Fatal("plugin name must be part of the id")
	}
}
